//go:build linux

package proxy

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// parseCapacitySelfCgroupPath maps the kernel's full cgroup-v2 membership to
// a path below the mounted cgroup2 root. A cgroup namespace may hide ancestors
// above the mount; those are NOT claimed observable.
func parseCapacitySelfCgroupPath(raw []byte, mountRoot string) (string, error) {
	var membership string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			return "", fmt.Errorf("malformed /proc/self/cgroup membership")
		}
		if parts[0] == "0" && parts[1] == "" {
			if membership != "" {
				return "", fmt.Errorf("duplicate unified cgroup membership")
			}
			membership = parts[2]
		}
	}
	if membership == "" || !filepath.IsAbs(membership) || !filepath.IsAbs(mountRoot) {
		return "", fmt.Errorf("unified cgroup membership or mount root unavailable")
	}
	// filepath.Clean would turn "/../sibling" into "/sibling", hiding an
	// explicit namespace escape. Inspect raw components BEFORE normalizing.
	for _, segment := range strings.Split(strings.TrimPrefix(membership, "/"), "/") {
		if segment == ".." || segment == "." {
			return "", fmt.Errorf("unsafe cgroup namespace-relative path")
		}
	}
	member := filepath.Clean(membership)
	base := filepath.Clean(mountRoot)
	relative, err := filepath.Rel(base, member)
	if err != nil || !capacityCgroupRelativeSafe(relative) {
		return "", fmt.Errorf("cgroup lies outside the visible mount")
	}
	return relative, nil
}

func capacityCgroupRelativeSafe(relative string) bool {
	if filepath.IsAbs(relative) {
		return false
	}
	clean := filepath.Clean(relative)
	return clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

// The only permissible hierarchy is the existing mounted cgroup-v2 tree,
// from its visible root through the current membership. Each ancestor's
// accounting includes descendants, so each independently limits additional
// allocations; the strictest budget is used, never their sum.
func readCapacityCgroupHierarchy(root, relative string) (capacityCgroupMemory, error) {
	if !capacityCgroupRelativeSafe(relative) {
		return capacityCgroupMemory{}, fmt.Errorf("cgroup hierarchy path escape")
	}
	rel := filepath.Clean(relative)
	segments := []string{}
	if rel != "." {
		segments = strings.Split(rel, string(filepath.Separator))
	}
	if len(segments) > 64 {
		return capacityCgroupMemory{}, fmt.Errorf("cgroup hierarchy too deep")
	}
	result := capacityCgroupMemory{}
	for i := 0; i <= len(segments); i++ {
		path := root
		if i > 0 {
			path = filepath.Join(root, filepath.Join(segments[:i]...))
		}
		one, err := readCapacityCgroupMemoryNode(path)
		if err != nil {
			return capacityCgroupMemory{}, fmt.Errorf("cgroup memory level %d: %w", i, err)
		}
		result.pressured = result.pressured || one.pressured
		if one.constrained {
			if !result.constrained || one.freeSlots < result.freeSlots {
				result.freeSlots = one.freeSlots
			}
			result.constrained = true
		}
	}
	return result, nil
}

// Scanner avoids allocating the entire (sometimes enormous) mountinfo file
// on every resource-sampling tick. It still checks for duplicate mounts.
var errCapacityNoCgroup2Mount = errors.New("cgroup2 mount root not found")

func parseCapacityCgroupMountRootReader(reader io.Reader, mountPoint string) (string, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var found string
	cgroup2Mark := []byte(" - cgroup2 ")
	for scanner.Scan() {
		// Skip unrelated mounts without allocating strings or field slices.
		if !bytes.Contains(scanner.Bytes(), cgroup2Mark) {
			continue
		}
		sides := strings.SplitN(scanner.Text(), " - ", 2)
		if len(sides) != 2 {
			continue
		}
		left, right := strings.Fields(sides[0]), strings.Fields(sides[1])
		if len(left) < 5 || len(right) < 1 || right[0] != "cgroup2" || left[4] != mountPoint {
			continue
		}
		if found != "" {
			return "", fmt.Errorf("duplicate cgroup2 mount point")
		}
		found = left[3]
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read cgroup2 mountinfo: %w", err)
	}
	if found == "" {
		return "", errCapacityNoCgroup2Mount
	}
	if !filepath.IsAbs(found) {
		return "", fmt.Errorf("invalid cgroup2 mount root")
	}
	return found, nil
}

// Current process membership and cgroup mount root must agree. Reading only
// /sys/fs/cgroup/memory.max is wrong on a host where process membership is
// under /system.slice or a nested cgroup. An inaccessible visible ancestor
// is fail-closed; inaccessible ancestors hidden by namespaces remain an
// explicitly UNSOLVED platform limitation requiring independent audit.
func readCapacityCgroupMembership(root string) (capacityCgroupMemory, error) {
	cgRaw, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return capacityCgroupMemory{}, err
	}
	mountFile, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return capacityCgroupMemory{}, err
	}
	defer mountFile.Close()
	return readCapacityCgroupMembershipFrom(root, cgRaw, mountFile)
}

// Compatibility with systems that have no unified controller: a genuinely
// absent cgroup2 mount AND absent memory.max means no usable v2 memory bound.
// Preserve FD-only AUTO behavior there. Any contradictory visible cgroup2
// memory file or malformed membership remains fail-closed.
func readCapacityCgroupMembershipFrom(root string, cgRaw []byte, mounts io.Reader) (capacityCgroupMemory, error) {
	mountRoot, err := parseCapacityCgroupMountRootReader(mounts, root)
	if err != nil {
		if errors.Is(err, errCapacityNoCgroup2Mount) {
			_, statErr := os.Stat(filepath.Join(root, "memory.max"))
			if errors.Is(statErr, os.ErrNotExist) {
				return capacityCgroupMemory{}, nil
			}
		}
		return capacityCgroupMemory{}, err
	}
	relative, err := parseCapacitySelfCgroupPath(cgRaw, mountRoot)
	if err != nil {
		return capacityCgroupMemory{}, err
	}
	return readCapacityCgroupHierarchy(root, relative)
}

// Missing cgroup controller on a host with no cgroup2 memory files is not a
// finite-memory sensor error. A visible ancestor with known finite limits and
// missing required measurements *is* an error. Keep these states separate.
func readCapacityCgroupMemory(root string) (capacityCgroupMemory, error) {
	return readCapacityCgroupMembership(root)
}
