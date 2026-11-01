//go:build darwin

package proxy

import (
	"os"

	"golang.org/x/sys/unix"
)

// Darwin uses a real RLIMIT_NOFILE and the fdescfs /dev/fd view. Unlike a
// guessed fixed fallback, an unreadable native budget never opens AUTO intake.
// Sampled descriptors include our own sampler FD, leaving conservative headroom.
func readCapacitySample() capacitySample {
	var limit unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &limit); err != nil {
		return capacitySample{reason: "darwin-rlimit-unavailable"}
	}
	entries, err := os.ReadDir("/dev/fd")
	if err != nil || limit.Cur > uint64(^uint(0)>>1) {
		return capacitySample{reason: "darwin-fd-usage-unavailable"}
	}
	total := int(limit.Cur)
	used := len(entries)
	if total <= 0 || used > total {
		return capacitySample{reason: "darwin-fd-count-contradictory"}
	}
	return capacitySample{available: true, freeFD: total - used, totalFD: total}
}
