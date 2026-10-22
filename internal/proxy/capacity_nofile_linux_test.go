//go:build linux

package proxy

import (
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
	"golang.org/x/sys/unix"
)

func TestCapacityLowNofileSubprocess(t *testing.T) {
	if os.Getenv("PXGO_LOW_NOFILE_CHILD") != "1" {
		for _, budget := range []string{"64", "128", "192"} {
			cmd := exec.Command(os.Args[0], "-test.run=^TestCapacityLowNofileSubprocess$", "-test.v")
			cmd.Env = append(os.Environ(), "PXGO_LOW_NOFILE_CHILD=1", "PXGO_LOW_NOFILE_LIMIT="+budget)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("native RLIMIT_NOFILE=%s: %v\n%s", budget, err, out)
			}
			t.Logf("RLIMIT_NOFILE=%s: %s", budget, out)
		}
		return
	}
	var limit unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &limit); err != nil {
		t.Fatal(err)
	}
	target, err := strconv.ParseUint(os.Getenv("PXGO_LOW_NOFILE_LIMIT"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	if limit.Cur < target {
		t.Skip("OS file-descriptor limit stricter than requested fixture")
	}
	limit.Cur = target
	if err := unix.Setrlimit(unix.RLIMIT_NOFILE, &limit); err != nil {
		t.Fatal(err)
	}
	s := readCapacitySample()
	if !s.available {
		t.Fatalf("low-nofile telemetry missing: %+v", s)
	}
	g := newCapacityGovernor(modeAuto, config.Default())
	for i := 0; i < 3; i++ {
		g.sample(s, time.Unix(10+int64(i), 0))
	}
	snap := g.snapshot()
	if snap.EffectiveLimit == 0 {
		t.Fatalf("healthy process with %d free out of %d FD cannot accept any downstream sockets: %+v", s.freeFD, s.totalFD, snap)
	}
	if snap.EffectiveLimit > int(target/8) {
		t.Fatalf("128-FD process incorrectly given generous downstream envelope: %+v", snap)
	}
}
