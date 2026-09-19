//go:build !windows && !linux

package diagnostic

import (
	"runtime"
	"syscall"
)

func platformProcessMetrics() (float64, uint64, error) {
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		return 0, 0, err
	}
	cpu := float64(usage.Utime.Sec+usage.Stime.Sec) + float64(usage.Utime.Usec+usage.Stime.Usec)/1e6
	var rss uint64
	if usage.Maxrss > 0 {
		// Getrusage reports a non-negative peak resident-set size. Guard the
		// signed syscall field before conversion so the diagnostic path cannot
		// wrap a malformed value.
		rss = uint64(usage.Maxrss) // #nosec G115 -- guarded non-negative kernel value
	}
	if runtime.GOOS != "darwin" {
		rss *= 1024
	}
	return cpu, rss, nil
}
