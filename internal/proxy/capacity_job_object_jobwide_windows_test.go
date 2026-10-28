//go:build windows

package proxy

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	capacityJobWideHeadroom = uint64(96 << 20)
	capacityProcessHeadroom = uint64(256 << 20)
	capacityJobWideDenied   = uintptr(128 << 20)
	capacityJobWideRecovery = uintptr(4 << 20)
)

// Separate process to demonstrate that per-job and per-process committed
// memory limits are distinct. Never restrict parent or the hosted CI runner.
func TestCapacityWindowsJobWideChild(t *testing.T) {
	if os.Getenv("PXGO_JOB_WIDE_CHILD") != "1" {
		t.Skip("child-only Job Object total memory test")
	}
	fmt.Printf("JOB_WIDE_READY pid=%d\n", os.Getpid())
	input := bufio.NewScanner(os.Stdin)
	if !input.Scan() || input.Text() != "RUN" {
		t.Fatal("Job-wide child did not receive RUN after assignment")
	}
	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	var n uint32
	err := windows.QueryInformationJobObject(0, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)), &n)
	flags := limits.BasicLimitInformation.LimitFlags
	if err != nil || n != uint32(unsafe.Sizeof(limits)) ||
		flags&windows.JOB_OBJECT_LIMIT_JOB_MEMORY == 0 ||
		flags&windows.JOB_OBJECT_LIMIT_PROCESS_MEMORY == 0 ||
		limits.JobMemoryLimit == 0 || limits.ProcessMemoryLimit <= limits.JobMemoryLimit {
		t.Fatalf("child Job Object total memory limit not queryable: err=%v flags=%x process=%d job=%d size=%d",
			err, flags, limits.ProcessMemoryLimit, limits.JobMemoryLimit, n)
	}
	fmt.Printf("JOB_WIDE_SELF job_cap=%d process_cap=%d flags_verified=true\n",
		limits.JobMemoryLimit, limits.ProcessMemoryLimit)
	// 128MiB fits process baseline+256MiB, but violates stricter JOB-wide
	// cap baseline+96MiB. Only an actual Win32 quota error proves enforcement.
	addr, err := windows.VirtualAlloc(0, capacityJobWideDenied,
		windows.MEM_RESERVE|windows.MEM_COMMIT, windows.PAGE_READWRITE)
	if addr != 0 {
		_ = windows.VirtualFree(addr, 0, windows.MEM_RELEASE)
		t.Fatal("128MiB commit unexpectedly succeeded above enforced job-wide cap")
	}
	if err == nil || !capacityJobMemoryError(err) {
		t.Fatalf("unrecognized memory denial rather than native job-wide quota: %v", err)
	}
	small, err := windows.VirtualAlloc(0, capacityJobWideRecovery,
		windows.MEM_RESERVE|windows.MEM_COMMIT, windows.PAGE_READWRITE)
	if err != nil || small == 0 {
		t.Fatalf("4MiB post-denial native commit failed: %v", err)
	}
	if err = windows.VirtualFree(small, 0, windows.MEM_RELEASE); err != nil {
		t.Fatal(err)
	}
	fmt.Printf("JOB_WIDE_PROBE denied_bytes=%d recovered_bytes=%d\n",
		capacityJobWideDenied, capacityJobWideRecovery)
	if !input.Scan() || input.Text() != "QUIT" {
		t.Fatal("Job-wide child never received clean QUIT")
	}
}

func TestCapacityWindowsJobWideMemoryCap(t *testing.T) {
	if os.Getenv("PXGO_JOB_WIDE_MEMORY_PROBE") != "1" {
		t.Skip("explicit native child-only Windows job-total committed-memory research")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0],
		"-test.run=^TestCapacityWindowsJobWideChild$", "-test.v", "-test.timeout=40s")
	cmd.Env = append(os.Environ(), "PXGO_JOB_WIDE_CHILD=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var job windows.Handle
	waited := false
	defer func() {
		_ = stdin.Close()
		if !waited {
			cancel()
			if err := cmd.Wait(); err != nil && ctx.Err() == nil {
				t.Errorf("job-wide helper cleanup: %v stderr=%q", err, stderr.String())
			}
		}
		if job != 0 {
			_ = windows.CloseHandle(job)
		}
	}()
	out := bufio.NewReader(stdout)
	ready, err := capacityReadJobPrefix(out, "JOB_WIDE_READY ")
	if err != nil || ready != fmt.Sprintf("pid=%d", cmd.Process.Pid) {
		t.Fatalf("job-wide helper not ready: %q pid=%d err=%v stderr=%q", ready, cmd.Process.Pid, err, stderr.String())
	}
	baseline, err := readCapacityIsolatedResources(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	jobCap := baseline.PrivateBytes + capacityJobWideHeadroom
	processCap := baseline.PrivateBytes + capacityProcessHeadroom
	if jobCap <= baseline.PrivateBytes || processCap <= jobCap {
		t.Fatal("overflow/invalid child-only committed-memory budget")
	}
	job, err = windows.CreateJobObject(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		ProcessMemoryLimit: uintptr(processCap),
		JobMemoryLimit:     uintptr(jobCap),
	}
	want.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_PROCESS_MEMORY |
		windows.JOB_OBJECT_LIMIT_JOB_MEMORY | windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&want)), uint32(unsafe.Sizeof(want)))
	if err != nil {
		t.Fatalf("set child-only job+process commit limits: %v", err)
	}
	var readback windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	var size uint32
	err = windows.QueryInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&readback)), uint32(unsafe.Sizeof(readback)), &size)
	if err != nil || size != uint32(unsafe.Sizeof(readback)) ||
		readback.BasicLimitInformation.LimitFlags&windows.JOB_OBJECT_LIMIT_JOB_MEMORY == 0 ||
		readback.BasicLimitInformation.LimitFlags&windows.JOB_OBJECT_LIMIT_PROCESS_MEMORY == 0 ||
		uint64(readback.JobMemoryLimit) != jobCap ||
		uint64(readback.ProcessMemoryLimit) != processCap {
		t.Fatalf("job-wide native limit readback mismatch: job=%d process=%d flags=%x size=%d err=%v",
			readback.JobMemoryLimit, readback.ProcessMemoryLimit,
			readback.BasicLimitInformation.LimitFlags, size, err)
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|
		windows.PROCESS_QUERY_INFORMATION, false, uint32(cmd.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	err = windows.AssignProcessToJobObject(job, h)
	_ = windows.CloseHandle(h)
	if err != nil {
		t.Fatalf("failed to bind only helper child to Job Object: %v", err)
	}
	if _, err := io.WriteString(stdin, "RUN\n"); err != nil {
		t.Fatal(err)
	}
	self, err := capacityReadJobPrefix(out, "JOB_WIDE_SELF ")
	if err != nil || self != fmt.Sprintf("job_cap=%d process_cap=%d flags_verified=true", jobCap, processCap) {
		t.Fatalf("child did not observe independent native limits: self=%q err=%v", self, err)
	}
	probe, err := capacityReadJobPrefix(out, "JOB_WIDE_PROBE ")
	if err != nil || probe != fmt.Sprintf("denied_bytes=%d recovered_bytes=%d", capacityJobWideDenied, capacityJobWideRecovery) {
		t.Fatalf("job-wide native hard quota denied/4MiB recovered not proven: %q err=%v", probe, err)
	}
	after, err := readCapacityIsolatedResources(cmd.Process.Pid)
	if err != nil || after.PrivateBytes > jobCap {
		t.Fatalf("helper PrivateBytes exceeds job-wide native cap: after=%d cap=%d err=%v", after.PrivateBytes, jobCap, err)
	}
	if _, err := io.WriteString(stdin, "QUIT\n"); err != nil {
		t.Fatal(err)
	}
	_ = stdin.Close()
	waitErr := cmd.Wait()
	waited = true
	if waitErr != nil {
		t.Fatalf("helper did not exit cleanly: %v stderr=%q", waitErr, stderr.String())
	}
	if err := windows.CloseHandle(job); err != nil {
		t.Fatalf("job-wide handle close failed: %v", err)
	}
	job = 0
	t.Logf("JOB_WIDE_RESULT enforceable=true pid=%d baseline_private=%d job_total_limit=%d process_limit=%d denied_commit=134217728 recovered_commit=4194304 after_private=%d",
		cmd.Process.Pid, baseline.PrivateBytes, jobCap, processCap, after.PrivateBytes)
}
