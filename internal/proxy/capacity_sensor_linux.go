//go:build linux

package proxy

import (
	"os"

	"golang.org/x/sys/unix"
)

func readCapacitySample() capacitySample {
	var r unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &r); err != nil {
		return capacitySample{reason: "rlimit-unavailable"}
	}
	files, err := os.ReadDir("/proc/self/fd")
	if err != nil || r.Cur > uint64(^uint(0)>>1) {
		return capacitySample{reason: "fd-usage-unavailable"}
	}
	used := len(files)
	limit := int(r.Cur)
	if used > limit {
		return capacitySample{reason: "fd-count-contradictory"}
	}
	return capacitySample{available: true, freeFD: limit - used, totalFD: limit}
}
