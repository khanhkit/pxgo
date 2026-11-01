//go:build linux

package proxy

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Experimental cgroup v2 admission signal. It estimates additional physical
// downstream sockets, NOT independently allocatable pool tokens. The memory
// envelope is only a conservative guard; it cannot replace OOM/PSI testing.
type capacityCgroupMemory struct {
	constrained bool
	freeSlots   int
	pressured   bool
}

const (
	capacityCgroupBytesPerSocket = uint64(1 << 20) // research 1MiB/socket
	capacityCgroupMinReserve     = uint64(64 << 20)
	capacityCgroupMaxReserve     = uint64(512 << 20)
)

func parseCapacityCgroupUint(raw []byte) (uint64, bool, error) {
	val := strings.TrimSpace(string(raw))
	if val == "max" {
		return 0, true, nil
	}
	if val == "" {
		return 0, false, fmt.Errorf("empty cgroup metric")
	}
	n, err := strconv.ParseUint(val, 10, 64)
	if err != nil {
		return 0, false, fmt.Errorf("invalid cgroup metric: %w", err)
	}
	return n, false, nil
}

func parseCapacityPSIAvg10(raw []byte) (float64, float64, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return 0, 0, nil
	}
	var some, full float64
	var seenSome, seenFull bool
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		parts := strings.Fields(line)
		if len(parts) == 0 {
			continue
		}
		switch parts[0] {
		case "some":
			if seenSome {
				return 0, 0, fmt.Errorf("duplicate PSI some row")
			}
			seenSome = true
		case "full":
			if seenFull {
				return 0, 0, fmt.Errorf("duplicate PSI full row")
			}
			seenFull = true
		default:
			return 0, 0, fmt.Errorf("unknown PSI class")
		}
		if len(parts) != 5 {
			return 0, 0, fmt.Errorf("invalid PSI field count")
		}
		hasAvg := false
		for _, field := range parts[1:] {
			if !strings.HasPrefix(field, "avg10=") {
				continue
			}
			if hasAvg {
				return 0, 0, fmt.Errorf("duplicate PSI avg10 field")
			}
			f, err := strconv.ParseFloat(strings.TrimPrefix(field, "avg10="), 64)
			if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 || f > 100 {
				return 0, 0, fmt.Errorf("invalid PSI avg10")
			}
			if parts[0] == "some" {
				some = f
			} else {
				full = f
			}
			hasAvg = true
		}
		if !hasAvg {
			return 0, 0, fmt.Errorf("PSI avg10 missing")
		}
		if !strings.HasPrefix(parts[2], "avg60=") || !strings.HasPrefix(parts[3], "avg300=") || !strings.HasPrefix(parts[4], "total=") {
			return 0, 0, fmt.Errorf("invalid PSI metric ordering")
		}
		for _, avgText := range []string{strings.TrimPrefix(parts[2], "avg60="), strings.TrimPrefix(parts[3], "avg300=")} {
			avg, parseErr := strconv.ParseFloat(avgText, 64)
			if parseErr != nil || math.IsNaN(avg) || math.IsInf(avg, 0) || avg < 0 || avg > 100 {
				return 0, 0, fmt.Errorf("invalid PSI historical average")
			}
		}
		if _, err := strconv.ParseUint(strings.TrimPrefix(parts[4], "total="), 10, 64); err != nil {
			return 0, 0, fmt.Errorf("invalid PSI total counter")
		}
	}
	return some, full, nil
}

func parseCapacityCgroupMemory(limitRaw, currentRaw, highRaw, psiRaw []byte) (capacityCgroupMemory, error) {
	maxBytes, unlimited, err := parseCapacityCgroupUint(limitRaw)
	if err != nil {
		return capacityCgroupMemory{}, err
	}
	if !unlimited && maxBytes == 0 {
		return capacityCgroupMemory{}, fmt.Errorf("invalid zero memory.max")
	}
	used, currentUnlimited, err := parseCapacityCgroupUint(currentRaw)
	if err != nil || currentUnlimited {
		return capacityCgroupMemory{}, fmt.Errorf("invalid memory.current")
	}
	high, highUnlimited, err := parseCapacityCgroupUint(highRaw)
	if err != nil {
		return capacityCgroupMemory{}, err
	}
	if !highUnlimited && high == 0 {
		return capacityCgroupMemory{}, fmt.Errorf("invalid zero memory.high")
	}
	if unlimited && highUnlimited {
		some, full, err := parseCapacityPSIAvg10(psiRaw)
		if err != nil {
			return capacityCgroupMemory{}, err
		}
		return capacityCgroupMemory{pressured: some >= 10 || full >= 1}, nil
	}
	// memory.high is a throttling threshold, not an OOM boundary; use it
	// conservatively as a ceiling on *new* work in this experimental mode.
	budget := maxBytes
	if unlimited || (!highUnlimited && high < maxBytes) {
		budget = high
	}
	some, full, err := parseCapacityPSIAvg10(psiRaw)
	if err != nil {
		return capacityCgroupMemory{}, err
	}

	reserve := max(capacityCgroupMinReserve, min(capacityCgroupMaxReserve, budget/8))
	var extraBytes uint64
	if used < budget && budget-used > reserve {
		extraBytes = budget - used - reserve
	}
	free := extraBytes / capacityCgroupBytesPerSocket
	// The absolute limit never becomes additive capacity across the classes.
	slots := int(min(free, uint64(math.MaxInt)))
	pressured := extraBytes == 0 || (!highUnlimited && used >= high) ||
		some >= 10 || full >= 1
	return capacityCgroupMemory{constrained: true, freeSlots: slots, pressured: pressured}, nil
}

// The cgroup2 mount is namespaced inside containers. Missing memory.max means
// no cgroup2 memory controller; a readable finite max with invalid subordinate
// sensors is an UNKNOWN budget and must deny *new* admissions, not kill leases.
func readCapacityCgroupMemoryNode(root string) (capacityCgroupMemory, error) {
	maxRaw, err := os.ReadFile(filepath.Join(root, "memory.max"))
	if errors.Is(err, os.ErrNotExist) {
		// A truly disabled memory controller can lack the max/current/high
		// interfaces. If any subordinate memory metric remains visible,
		// treating a missing max as unlimited would silently authorize
		// FD-only admission from a partially unreadable controller.
		for _, sibling := range []string{"memory.current", "memory.high"} {
			_, statErr := os.Lstat(filepath.Join(root, sibling))
			if statErr == nil || !errors.Is(statErr, os.ErrNotExist) {
				return capacityCgroupMemory{}, fmt.Errorf("incomplete cgroup memory controller: memory.max missing alongside %s", sibling)
			}
		}
		return capacityCgroupMemory{}, nil
	}
	if err != nil {
		return capacityCgroupMemory{}, err
	}
	current, err := os.ReadFile(filepath.Join(root, "memory.current"))
	if err != nil {
		return capacityCgroupMemory{}, err
	}
	high, err := os.ReadFile(filepath.Join(root, "memory.high"))
	if errors.Is(err, os.ErrNotExist) {
		high = []byte("max")
	} else if err != nil {
		return capacityCgroupMemory{}, err
	}
	psi, err := os.ReadFile(filepath.Join(root, "memory.pressure"))
	if errors.Is(err, os.ErrNotExist) {
		psi = nil
	} else if err != nil {
		return capacityCgroupMemory{}, err
	}
	return parseCapacityCgroupMemory(maxRaw, current, high, psi)
}
