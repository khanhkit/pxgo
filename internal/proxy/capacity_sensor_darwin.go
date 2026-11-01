//go:build darwin

package proxy

import (
	"errors"
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
	if limit.Cur > uint64(^uint(0)>>1) {
		return capacitySample{reason: "darwin-rlimit-overflow"}
	}
	total := int(limit.Cur)
	used, err := countDarwinFDs(total)
	if err != nil {
		return capacitySample{reason: "darwin-fd-usage-unavailable"}
	}
	if total <= 0 || used > total {
		return capacitySample{reason: "darwin-fd-count-contradictory"}
	}
	return capacitySample{available: true, freeFD: total - used, totalFD: total}
}

// fdescfs may be unavailable or unmounted on native Darwin runners. An
// independent F_GETFD scan measures only the CURRENT process: EBADF is the
// only ignored error, and high unknown limits fail closed rather than guess.
func countDarwinFDs(limit int) (int, error) {
	entries, err := os.ReadDir("/dev/fd")
	if err == nil && len(entries) >= 3 {
		return len(entries), nil
	}
	// Do not use expensive per-FD fallback on unbounded process limits.
	if limit <= 0 || limit > 16384 {
		return 0, errors.New("unavailable Darwin fdescfs and unsupported FD fallback limit")
	}
	used := 0
	for fd := 0; fd < limit; fd++ {
		_, e := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
		switch {
		case e == nil:
			used++
		case errors.Is(e, unix.EBADF):
		default:
			return 0, e
		}
	}
	return used, nil
}
