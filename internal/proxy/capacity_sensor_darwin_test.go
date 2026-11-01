//go:build darwin

package proxy

import (
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

func TestCapacityDarwinNativeFDProbe(t *testing.T) {
	var r unix.Rlimit
	e := unix.Getrlimit(unix.RLIMIT_NOFILE, &r)
	if e != nil {
		t.Fatalf("Darwin RLIMIT_NOFILE unsupported: %v", e)
	}
	entries, dirErr := os.ReadDir("/dev/fd")
	s := readCapacitySample()
	t.Logf("native Darwin FD sample %+v, rlimit=%d, devfd_entries=%d devfd_err=%v", s, r.Cur, len(entries), dirErr)
	if !s.available || s.freeFD < 0 || s.totalFD <= 0 || s.freeFD > s.totalFD {
		t.Fatalf("Darwin native FD sample not usable: %+v rlimit=%d devfd_err=%v", s, r.Cur, dirErr)
	}
}
