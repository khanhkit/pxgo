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
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		n, _ := strconv.ParseUint(fields[1], 10, 64)
		switch fields[0] {
		case "VmRSS:":
			s.RSSBytes = n * 1024
		case "RssAnon:":
			s.PrivateBytes = n * 1024
		}
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
