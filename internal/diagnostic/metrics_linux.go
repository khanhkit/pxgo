//go:build linux

package diagnostic

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

func platformProcessMetrics() (float64, uint64, error) {
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		return 0, 0, err
	}
	cpu := float64(usage.Utime.Sec+usage.Stime.Sec) + float64(usage.Utime.Usec+usage.Stime.Usec)/1e6

	data, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return cpu, 0, err
	}
	fields := strings.Fields(string(data))
	if len(fields) < 2 {
		return cpu, 0, fmt.Errorf("unexpected /proc/self/statm format")
	}
	residentPages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return cpu, 0, fmt.Errorf("parse resident pages: %w", err)
	}
	pageSize := os.Getpagesize()
	if pageSize <= 0 {
		return cpu, 0, fmt.Errorf("invalid OS page size %d", pageSize)
	}
	return cpu, residentPages * uint64(pageSize), nil // #nosec G115 -- guarded positive OS page size
}
