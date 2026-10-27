//go:build windows

package proxy

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	capacityJobProbeLargeCommit = uintptr(256 << 20)
	capacityJobProbeSmallCommit = uintptr(4 << 20)
	capacityJobHeadroom         = uint64(128 << 20)
)

func capacityJobMemoryError(err error) bool {
	// Windows: ERROR_COMMITMENT_LIMIT, ERROR_NOT_ENOUGH_MEMORY,
	// ERROR_OUTOFMEMORY, ERROR_NOT_ENOUGH_QUOTA. No unrelated Win32 error
	// can be accepted as evidence of an enforced memory budget.
	return errors.Is(err, syscall.Errno(1455)) ||
		errors.Is(err, syscall.Errno(8)) ||
		errors.Is(err, syscall.Errno(14)) ||
		errors.Is(err, syscall.Errno(1816))
}

// A separate helper process is placed in a Job Object by the parent test.
// Parent runner, Go load generator, and upstream are NEVER put in the job.
func TestCapacityWindowsJobMemoryChild(t *testing.T) {
	if os.Getenv("PXGO_CAPACITY_JOB_CHILD") != "1" {
		t.Skip("Job Object child only")
	}
	fmt.Printf("JOB_READY pid=%d\n", os.Getpid())
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() || scanner.Text() != "RUN" {
		t.Fatal("Job Object child did not receive gated RUN after assignment")
	}
	addr, err := windows.VirtualAlloc(0, capacityJobProbeLargeCommit, windows.MEM_COMMIT|windows.MEM_RESERVE, windows.PAGE_READWRITE)
	if addr != 0 {
		_ = windows.VirtualFree(addr, 0, windows.MEM_RELEASE)
		t.Fatal("large VirtualAlloc SUCCEEDED despite enforced process memory limit")
	}
	if err == nil || !capacityJobMemoryError(err) {
		t.Fatalf("large VirtualAlloc unexpected error, not proven quota enforcement: %v", err)
	}
	// A smaller allocation MUST succeed within the SAME still-live child.
	recovered, err := windows.VirtualAlloc(0, capacityJobProbeSmallCommit, windows.MEM_COMMIT|windows.MEM_RESERVE, windows.PAGE_READWRITE)
	if err != nil || recovered == 0 {
		t.Fatalf("small allocation did not recover after denied commit: %v", err)
	}
	// MEM_COMMIT must count against the hard process commit charge.
	// Do not confuse committed virtual memory with resident working set.
	if err = windows.VirtualFree(recovered, 0, windows.MEM_RELEASE); err != nil {
		t.Fatal(err)
	}
	fmt.Printf("JOB_RESULT enforced=true denied_bytes=%d recovered_bytes=%d\n", capacityJobProbeLargeCommit, capacityJobProbeSmallCommit)
	if !scanner.Scan() || scanner.Text() != "QUIT" {
		t.Fatal("Job Object child did not receive clean QUIT")
	}
}

func capacityReadJobPrefix(r *bufio.Reader, prefix string) (string, error) {
	for range 100 {
		line, err := r.ReadString('\n')
		if err != nil {
			return "", err
		}
		if i := strings.Index(line, prefix); i >= 0 {
			return strings.TrimSpace(line[i+len(prefix):]), nil
		}
	}
	return "", errors.New("child produced too many lines before expected Job Object marker")
}

func TestCapacityWindowsJobObjectCommitPressure(t *testing.T) {
	if os.Getenv("PXGO_CAPACITY_JOB_PRESSURE") != "1" {
		t.Skip("opt-in child-isolated Windows Job Object enforcement test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCapacityWindowsJobMemoryChild$", "-test.v", "-test.timeout=30s")
	cmd.Env = append(os.Environ(), "PXGO_CAPACITY_JOB_CHILD=1")
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
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Every failure path closes child stdin, lets its finite context stop it,
	// waits for process completion, and preserves original failure evidence.
	waited := false
	defer func() {
		_ = stdin.Close()
		if !waited {
			if e := cmd.Wait(); e != nil && ctx.Err() == nil {
				t.Errorf("Job child cleanup: %v stderr=%q", e, stderr.String())
			}
		}
	}()
	reader := bufio.NewReader(stdout)
	ready, err := capacityReadJobPrefix(reader, "JOB_READY ")
	if err != nil {
		t.Fatalf("child readiness: %v stderr=%q", err, stderr.String())
	}
	if ready != fmt.Sprintf("pid=%d", cmd.Process.Pid) {
		t.Fatalf("child pid mismatch, ready=%q actual=%d", ready, cmd.Process.Pid)
	}
	baseline, err := readCapacityIsolatedResources(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	hardLimit := baseline.PrivateBytes + capacityJobHeadroom
	if hardLimit < baseline.PrivateBytes {
		t.Fatal("overflow in baseline memory limit arithmetic")
	}
	// Narrow resource cap, not a 16GiB runner-wide quota or arbitrary
	// system memory exhaustion. Only the identified CHILD PID is assigned.
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		t.Fatalf("CreateJobObject: %v", err)
	}
	defer windows.CloseHandle(job)
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		ProcessMemoryLimit: uintptr(hardLimit),
	}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_PROCESS_MEMORY | windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)))
	if err != nil {
		t.Fatalf("SetInformationJobObject: %v", err)
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_INFORMATION, false, uint32(cmd.Process.Pid))
	if err != nil {
		t.Fatalf("OpenProcess child: %v", err)
	}
	err = windows.AssignProcessToJobObject(job, h)
	_ = windows.CloseHandle(h)
	if err != nil {
		t.Fatalf("AssignProcessToJobObject (possible hosted parent job restriction): %v", err)
	}
	var readback windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	var returned uint32
	err = windows.QueryInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&readback)), uint32(unsafe.Sizeof(readback)), &returned)
	if err != nil {
		t.Fatalf("QueryInformationJobObject: %v", err)
	}
	if readback.BasicLimitInformation.LimitFlags&windows.JOB_OBJECT_LIMIT_PROCESS_MEMORY == 0 ||
		uint64(readback.ProcessMemoryLimit) != hardLimit || returned != uint32(unsafe.Sizeof(readback)) {
		t.Fatalf("unexpected enforced Job Object limit flags=%x limit=%d want=%d returnSize=%d",
			readback.BasicLimitInformation.LimitFlags, readback.ProcessMemoryLimit, hardLimit, returned)
	}
	if _, err := io.WriteString(stdin, "RUN\n"); err != nil {
		t.Fatal(err)
	}
	result, err := capacityReadJobPrefix(reader, "JOB_RESULT ")
	if err != nil {
		t.Fatalf("child pressure response: %v stderr=%q", err, stderr.String())
	}
	expected := fmt.Sprintf("enforced=true denied_bytes=%d recovered_bytes=%d", capacityJobProbeLargeCommit, capacityJobProbeSmallCommit)
	if result != expected {
		t.Fatalf("Job Object pressure evidence mismatch got=%q want=%q", result, expected)
	}
	after, err := readCapacityIsolatedResources(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if after.PrivateBytes > hardLimit {
		t.Fatalf("committed memory exceeded Job Object limit: after=%d limit=%d", after.PrivateBytes, hardLimit)
	}
	if _, err := io.WriteString(stdin, "QUIT\n"); err != nil {
		t.Fatal(err)
	}
	_ = stdin.Close()
	if err := cmd.Wait(); err != nil {
		waited = true
		t.Fatalf("Job Object child did not exit cleanly: %v stderr=%q", err, stderr.String())
	}
	waited = true
	t.Logf("JOB_OBJECT_RESULT enforceable=true child_pid=%d baseline_private=%d enforced_process_commit_limit=%d denied_commit_bytes=%d recovery_commit_bytes=%d final_private=%d",
		cmd.Process.Pid, baseline.PrivateBytes, hardLimit, capacityJobProbeLargeCommit, capacityJobProbeSmallCommit, after.PrivateBytes)
}
