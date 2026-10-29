//go:build linux

package proxy

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type capacityIsolatedResourceSample struct {
	PrivateBytes, RSSBytes uint64
	Handles                uint32
	CPUNanos               uint64
}

func readCapacityIsolatedResources(pid int) (capacityIsolatedResourceSample, error) {
	var s capacityIsolatedResourceSample
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return s, err
	}
	seenRSS, seenAnon := false, false
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if fields[0] != "VmRSS:" && fields[0] != "RssAnon:" {
			continue
		}
		if len(fields) != 3 || fields[2] != "kB" {
			return s, fmt.Errorf("malformed process status metric %q", line)
		}
		n, parseErr := strconv.ParseUint(fields[1], 10, 64)
		if parseErr != nil {
			return s, fmt.Errorf("malformed process memory field %q: %w", line, parseErr)
		}
		if fields[0] == "VmRSS:" {
			s.RSSBytes = n * 1024
			seenRSS = true
		}
		if fields[0] == "RssAnon:" {
			s.PrivateBytes = n * 1024
			seenAnon = true
		}
	}
	if !seenRSS || !seenAnon || s.RSSBytes == 0 || s.PrivateBytes == 0 {
		return s, fmt.Errorf("missing required VmRSS/RssAnon for pid=%d", pid)
	}
	fd, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
	if err != nil {
		return s, err
	}
	s.Handles = uint32(len(fd))
	if s.RSSBytes == 0 {
		return s, fmt.Errorf("cannot read Linux proxy process RSS pid=%d", pid)
	}
	return s, nil
}
