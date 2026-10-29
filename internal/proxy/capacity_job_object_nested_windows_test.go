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
	nestedRootHeadroom   = uint64(96 << 20)
	nestedLeafHeadroom   = uint64(192 << 20)
	nestedLargeCommit    = uintptr(128 << 20)
	nestedRecoveryCommit = uintptr(4 << 20)
)

// TestCapacityWindowsNestedJobChild is a separate, test-only process. It
// verifies both the immediate job's observable limit and actual allocation
// enforcement of a stricter ancestor. The real CI runner/parent is not limited.
func TestCapacityWindowsNestedJobChild(t *testing.T) {
	if os.Getenv("PXGO_NESTED_JOB_CHILD") != "1" {
		t.Skip("native nested Job Object child only")
	}
	fmt.Printf("NESTED_JOB_READY pid=%d\n", os.Getpid())
	input := bufio.NewScanner(os.Stdin)
	if !input.Scan() || input.Text() != "RUN" {
		t.Fatal("child did not receive RUN after nested Job Object assignment")
	}
	var observed windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	var size uint32
	err := windows.QueryInformationJobObject(0, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&observed)), uint32(unsafe.Sizeof(observed)), &size)
	if err != nil || size != uint32(unsafe.Sizeof(observed)) ||
		observed.BasicLimitInformation.LimitFlags&windows.JOB_OBJECT_LIMIT_PROCESS_MEMORY == 0 ||
		observed.ProcessMemoryLimit == 0 {
		t.Fatalf("child immediate job query failed: err=%v size=%d limit=%d flags=%x",
			err, size, observed.ProcessMemoryLimit, observed.BasicLimitInformation.LimitFlags)
	}
	fmt.Printf("NESTED_JOB_IMMEDIATE limit_bytes=%d process_mem_flag=true\n", observed.ProcessMemoryLimit)
	// 128MiB fits the observed LEAF cap+192MiB, but not the unobserved
	// ROOT cap+96MiB. Enforcement must be stricter than a NULL-job query.
	addr, err := windows.VirtualAlloc(0, nestedLargeCommit,
		windows.MEM_COMMIT|windows.MEM_RESERVE, windows.PAGE_READWRITE)
	if addr != 0 {
		_ = windows.VirtualFree(addr, 0, windows.MEM_RELEASE)
		t.Fatal("128MiB allocation unexpectedly succeeded despite tighter ancestor job")
	}
	if err == nil || !capacityJobMemoryError(err) {
		t.Fatalf("128MiB denied for unrelated reason, cannot prove nested cap: %v", err)
	}
	recovered, err := windows.VirtualAlloc(0, nestedRecoveryCommit,
		windows.MEM_COMMIT|windows.MEM_RESERVE, windows.PAGE_READWRITE)
	if err != nil || recovered == 0 {
		t.Fatalf("4MiB allocation failed in same child: %v", err)
	}
	if err := windows.VirtualFree(recovered, 0, windows.MEM_RELEASE); err != nil {
		t.Fatalf("free recovered child commit: %v", err)
	}
	fmt.Printf("NESTED_JOB_ALLOC_RESULT root_denied_bytes=%d recovered_bytes=%d\n",
		nestedLargeCommit, nestedRecoveryCommit)
	if !input.Scan() || input.Text() != "QUIT" {
		t.Fatal("nested job child did not receive clean QUIT")
	}
}

func setNestedJobCommitLimit(job windows.Handle, limit uint64) error {
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{ProcessMemoryLimit: uintptr(limit)}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_PROCESS_MEMORY |
		windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
	return err
}

func verifyNestedJobLimit(job windows.Handle, limit uint64) error {
	var got windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	var size uint32
	err := windows.QueryInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&got)), uint32(unsafe.Sizeof(got)), &size)
	if err != nil || size != uint32(unsafe.Sizeof(got)) ||
		got.BasicLimitInformation.LimitFlags&windows.JOB_OBJECT_LIMIT_PROCESS_MEMORY == 0 ||
		uint64(got.ProcessMemoryLimit) != limit {
		return fmt.Errorf("nested job readback expected=%d observed=%d flags=%x err=%v",
			limit, got.ProcessMemoryLimit, got.BasicLimitInformation.LimitFlags, err)
	}
	return nil
}

// Strictly opt-in, native Windows. The root and immediate leaf jobs contain
// ONLY the identified child test process, never the parent or CI runner.
func TestCapacityWindowsNestedAncestorCommitLimit(t *testing.T) {
	if os.Getenv("PXGO_NESTED_JOB_ANCESTOR_PROBE") != "1" {
		t.Skip("opt-in native nested Job Object ancestor memory limit research")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0],
		"-test.run=^TestCapacityWindowsNestedJobChild$", "-test.v", "-test.timeout=40s")
	cmd.Env = append(os.Environ(), "PXGO_NESTED_JOB_CHILD=1")
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var root, leaf windows.Handle
	waited := false
	defer func() {
		_ = in.Close()
		if !waited {
			cancel()
			if err := cmd.Wait(); err != nil && ctx.Err() == nil {
				t.Errorf("nested child unexpected early failure: %v stderr=%q", err, stderr.String())
			}
		}
		// KILL_ON_JOB_CLOSE is attached only to the limited child, and
		// these handles close only AFTER the child has been waited.
		if leaf != 0 {
			_ = windows.CloseHandle(leaf)
		}
		if root != 0 {
			_ = windows.CloseHandle(root)
		}
	}()
	reader := bufio.NewReader(out)
	ready, err := capacityReadJobPrefix(reader, "NESTED_JOB_READY ")
	if err != nil || ready != fmt.Sprintf("pid=%d", cmd.Process.Pid) {
		t.Fatalf("child PID/ready mismatch ready=%q expected=%d err=%v stderr=%q",
			ready, cmd.Process.Pid, err, stderr.String())
	}
	before, err := readCapacityIsolatedResources(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	rootCap, leafCap := before.PrivateBytes+nestedRootHeadroom, before.PrivateBytes+nestedLeafHeadroom
	if rootCap <= before.PrivateBytes || leafCap <= rootCap {
		t.Fatal("overflow/invalid nested headroom")
	}
	root, err = windows.CreateJobObject(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err = windows.CreateJobObject(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := setNestedJobCommitLimit(root, rootCap); err != nil {
		t.Fatalf("root Job Object memory cap: %v", err)
	}
	if err := setNestedJobCommitLimit(leaf, leafCap); err != nil {
		t.Fatalf("leaf Job Object memory cap: %v", err)
	}
	if err := verifyNestedJobLimit(root, rootCap); err != nil {
		t.Fatal(err)
	}
	if err := verifyNestedJobLimit(leaf, leafCap); err != nil {
		t.Fatal(err)
	}
	handle, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|
		windows.PROCESS_QUERY_INFORMATION, false, uint32(cmd.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	// Explicit ROOT -> LEAF assignment is mandatory for valid hierarchy.
	err = windows.AssignProcessToJobObject(root, handle)
	if err == nil {
		err = windows.AssignProcessToJobObject(leaf, handle)
	}
	_ = windows.CloseHandle(handle)
	if err != nil {
		t.Fatalf("cannot form nested child-only Job Object: %v", err)
	}
	if _, err := io.WriteString(in, "RUN\n"); err != nil {
		t.Fatal(err)
	}
	observed, err := capacityReadJobPrefix(reader, "NESTED_JOB_IMMEDIATE ")
	if err != nil || observed != fmt.Sprintf("limit_bytes=%d process_mem_flag=true", leafCap) {
		t.Fatalf("child NULL-job query did not show immediate leaf cap=%d observed=%q err=%v",
			leafCap, observed, err)
	}
	probe, err := capacityReadJobPrefix(reader, "NESTED_JOB_ALLOC_RESULT ")
	if err != nil || probe != fmt.Sprintf("root_denied_bytes=%d recovered_bytes=%d",
		nestedLargeCommit, nestedRecoveryCommit) {
		t.Fatalf("stricter ancestor commitment denial not observed: probe=%q err=%v", probe, err)
	}
	after, err := readCapacityIsolatedResources(cmd.Process.Pid)
	if err != nil || after.PrivateBytes > rootCap {
		t.Fatalf("child private commit exceeded root ancestor cap: private=%d root=%d err=%v",
			after.PrivateBytes, rootCap, err)
	}
	if _, err := io.WriteString(in, "QUIT\n"); err != nil {
		t.Fatal(err)
	}
	_ = in.Close()
	waitErr := cmd.Wait()
	waited = true
	if waitErr != nil {
		t.Fatalf("nested child did not quit normally: %v stderr=%q", waitErr, stderr.String())
	}
	// A success marker requires both owned child-only Job Object handles
	// to close normally, not merely an eventual deferred best-effort close.
	if err := windows.CloseHandle(leaf); err != nil {
		t.Fatalf("close leaf Job Object after child exit: %v", err)
	}
	leaf = 0
	if err := windows.CloseHandle(root); err != nil {
		t.Fatalf("close root Job Object after child exit: %v", err)
	}
	root = 0
	t.Logf("NESTED_JOB_RESULT enforced_root_stricter=true pid=%d baseline_private=%d ancestor_limit=%d immediate_query_limit=%d denied_commit=134217728 recovered_commit=4194304 after_private=%d",
		cmd.Process.Pid, before.PrivateBytes, rootCap, leafCap, after.PrivateBytes)
}
