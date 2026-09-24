//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

const nofileHelperEnv = "PXGO_TEST_NOFILE_HELPER"

func TestPXV012RaiseNofileLimitUnixSyscall(t *testing.T) {
	if os.Getenv(nofileHelperEnv) == "1" {
		runNofileLimitSyscallHelper(t)
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestPXV012RaiseNofileLimitUnixSyscall$", "-test.v")
	cmd.Env = append(os.Environ(), nofileHelperEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("nofile syscall helper failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "NOFILE_RAISED") {
		t.Fatalf("nofile syscall helper did not prove a raised limit:\n%s", out)
	}
}

func runNofileLimitSyscallHelper(t *testing.T) {
	var original unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &original); err != nil {
		t.Fatal(err)
	}
	if original.Max <= 256 {
		t.Skipf("hard RLIMIT_NOFILE=%d is too constrained for raise proof", original.Max)
	}

	low := uint64(256)
	if err := unix.Setrlimit(unix.RLIMIT_NOFILE, &unix.Rlimit{Cur: low, Max: original.Max}); err != nil {
		t.Fatalf("lower RLIMIT_NOFILE: %v", err)
	}
	defer func() {
		_ = unix.Setrlimit(unix.RLIMIT_NOFILE, &original)
	}()

	raiseNofileLimitBestEffort()

	var raised unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &raised); err != nil {
		t.Fatal(err)
	}
	if raised.Cur <= low {
		t.Fatalf("soft RLIMIT_NOFILE=%d want >%d (hard=%d)", raised.Cur, low, raised.Max)
	}
	if raised.Cur > raised.Max {
		t.Fatalf("soft RLIMIT_NOFILE=%d exceeds hard=%d", raised.Cur, raised.Max)
	}
	fmt.Printf("NOFILE_RAISED soft=%d hard=%d\n", raised.Cur, raised.Max)
}
