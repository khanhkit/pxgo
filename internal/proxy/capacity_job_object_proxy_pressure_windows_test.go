//go:build windows

package proxy

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/khanhkit/pxgo/internal/config"
	"github.com/khanhkit/pxgo/internal/diagnostic"
	"golang.org/x/sys/windows"
)

const (
	capacityJobProxyHeld   = 2560
	capacityJobProxyShort  = 600
	capacityJobProxyBudget = uint64(384 << 20)
)

func TestCapacityWindowsJobProxyChild(t *testing.T) {
	if os.Getenv("PXGO_CAPACITY_JOB_PROXY_CHILD") != "1" {
		t.Skip("child-only real PxGo Job Object test")
	}
	cfg := config.Default()
	cfg.Listen = "127.0.0.1"
	cfg.Idle = 50
	cfg.ProxyReload = 0
	cfg.SockTimeout = 20
	px := startTestProxy(t, cfg)
	fmt.Printf("JOB_PROXY_READY addr=%s pid=%d\n", px.ListenAddr(), os.Getpid())
	in := bufio.NewScanner(os.Stdin)
	for in.Scan() {
		switch in.Text() {
		case "SNAP":
			raw, err := json.Marshal(px.capacityDiagnosticSnapshot())
			if err != nil {
				t.Fatal(err)
			}
			fmt.Printf("JOB_PROXY_SNAPSHOT %s\n", raw)
		case "JOBINFO":
			// Query the actual Job Object from WITHIN the child process.
			// A nil Job handle addresses the immediately associated job;
			// this does not claim to expose stricter ancestral nested jobs.
			var mine windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
			var size uint32
			err := windows.QueryInformationJobObject(0, windows.JobObjectExtendedLimitInformation,
				uintptr(unsafe.Pointer(&mine)), uint32(unsafe.Sizeof(mine)), &size)
			if err != nil || size != uint32(unsafe.Sizeof(mine)) ||
				mine.BasicLimitInformation.LimitFlags&windows.JOB_OBJECT_LIMIT_PROCESS_MEMORY == 0 ||
				mine.ProcessMemoryLimit == 0 {
				t.Fatalf("child could not prove its own enforced process Job Object cap: err=%v size=%d flags=%x limit=%d",
					err, size, mine.BasicLimitInformation.LimitFlags, mine.ProcessMemoryLimit)
			}
			fmt.Printf("JOB_PROXY_OWN_JOBINFO process_commit_limit=%d process_mem_flag=true\n",
				mine.ProcessMemoryLimit)
		case "PROBE":
			// Fail closed: allocation denial only counts with a recognized
			// Win32 commit-quota error, never arbitrary VirtualAlloc errors.
			addr, err := windows.VirtualAlloc(0, capacityJobProbeLargeCommit,
				windows.MEM_COMMIT|windows.MEM_RESERVE, windows.PAGE_READWRITE)
			if addr != 0 {
				_ = windows.VirtualFree(addr, 0, windows.MEM_RELEASE)
				t.Fatal("256MiB child VirtualAlloc unexpectedly succeeded")
			}
			if err == nil || !capacityJobMemoryError(err) {
				t.Fatalf("allocation denial did not prove memory budget: %v", err)
			}
			again, err := windows.VirtualAlloc(0, capacityJobProbeSmallCommit,
				windows.MEM_COMMIT|windows.MEM_RESERVE, windows.PAGE_READWRITE)
			if err != nil || again == 0 {
				t.Fatalf("4MiB live child recovery commit failed: %v", err)
			}
			if err := windows.VirtualFree(again, 0, windows.MEM_RELEASE); err != nil {
				t.Fatal(err)
			}
			fmt.Printf("JOB_PROXY_PROBE denied_bytes=%d recovered_bytes=%d\n",
				capacityJobProbeLargeCommit, capacityJobProbeSmallCommit)
		case "QUIT":
			return
		default:
			t.Fatalf("invalid child command %q", in.Text())
		}
	}
	if err := in.Err(); err != nil {
		t.Fatal(err)
	}
}

func capacityJobProxySnapshot(p *capacityIsolatedProcess) (diagnostic.CapacitySnapshot, error) {
	var snap diagnostic.CapacitySnapshot
	if _, err := io.WriteString(p.stdin, "SNAP\n"); err != nil {
		return snap, err
	}
	line, err := capacityReadJobPrefix(p.output, "JOB_PROXY_SNAPSHOT ")
	if err != nil {
		return snap, err
	}
	return snap, json.Unmarshal([]byte(line), &snap)
}

//nolint:gocyclo // A single native integration gate keeps all hard resource and teardown assertions visible.
func TestCapacityWindowsJobProxy2560Pressure(t *testing.T) {
	if os.Getenv("PXGO_CAPACITY_JOB_PROXY_2560") != "1" {
		t.Skip("opt-in real proxy under child-only Windows commit quota")
	}
	if os.Getenv("PXGO_EXPERIMENTAL_CAPACITY_MODE") != "auto" {
		t.Fatal("requires explicit experimental AUTO mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 110*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCapacityWindowsJobProxyChild$", "-test.v", "-test.timeout=100s")
	cmd.Env = append(os.Environ(), "PXGO_CAPACITY_JOB_PROXY_CHILD=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &capacityIsolatedProcess{cmd: cmd, stdin: stdin, output: bufio.NewReader(stdout), cancel: cancel}
	held := make([]net.Conn, capacityJobProxyHeld)
	var job windows.Handle
	var echo net.Listener
	var workers sync.WaitGroup
	// In *all* exits (including t.Fatal), drain held clients, stop origin,
	// close isolated child input and wait for its bounded shutdown.
	defer func() {
		for _, c := range held {
			if c != nil {
				_ = c.Close()
			}
		}
		if echo != nil {
			_ = echo.Close()
		}
		workers.Wait()
		_, _ = io.WriteString(stdin, "QUIT\n")
		_ = stdin.Close()
		if err := cmd.Wait(); err != nil {
			t.Errorf("real PxGo child was not gracefully shut down: %v", err)
			return
		}
		// Close the owned child-only Job Object AFTER clean child exit.
		if job != 0 {
			if err := windows.CloseHandle(job); err != nil {
				t.Errorf("close owned Job Object: %v", err)
				return
			}
		}
		fmt.Println("JOB_PROXY_CLEANUP_PROOF held_closed=2560 echo_workers_joined=true child_exited=true")
	}()
	ready, err := capacityReadJobPrefix(p.output, "JOB_PROXY_READY ")
	if err != nil {
		t.Fatalf("child handshake: %v", err)
	}
	if !strings.Contains(ready, fmt.Sprintf("pid=%d", cmd.Process.Pid)) {
		t.Fatalf("child reported incorrect process: %s", ready)
	}
	fields := strings.Fields(ready)
	var proxy string
	for _, f := range fields {
		if strings.HasPrefix(f, "addr=") {
			proxy = strings.TrimPrefix(f, "addr=")
		}
	}
	if proxy == "" {
		t.Fatalf("real proxy did not report listener: %s", ready)
	}
	trial := os.Getenv("PXGO_CAPACITY_JOB_PROXY_TRIAL")
	if trial != "" && trial != "0" && trial != "1" {
		t.Fatalf("invalid Job Object trial label %q", trial)
	}
	if trial == "" {
		trial = "default"
	}
	t.Logf("JOB_PROXY_MATRIX_META policy=%s classes=%s trial=%s", os.Getenv("PXGO_RESEARCH_CAPACITY_POLICY"), os.Getenv("PXGO_EXPERIMENTAL_CONNECTION_CLASSES"), trial)
	baseline, err := readCapacityIsolatedResources(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	hardLimit := baseline.PrivateBytes + capacityJobProxyBudget
	if hardLimit <= baseline.PrivateBytes {
		t.Fatal("process limit overflow")
	}
	// New Job Object contains only the PxGo child; never parent load runner.
	job, err = windows.CreateJobObject(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{ProcessMemoryLimit: uintptr(hardLimit)}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_PROCESS_MEMORY | windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)))
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_INFORMATION,
		false, uint32(cmd.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	err = windows.AssignProcessToJobObject(job, h)
	_ = windows.CloseHandle(h)
	if err != nil {
		t.Fatalf("cannot enforce child-only quota: %v", err)
	}
	var readback windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	var returned uint32
	err = windows.QueryInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&readback)), uint32(unsafe.Sizeof(readback)), &returned)
	if err != nil || returned != uint32(unsafe.Sizeof(readback)) ||
		uint64(readback.ProcessMemoryLimit) != hardLimit ||
		readback.BasicLimitInformation.LimitFlags&windows.JOB_OBJECT_LIMIT_PROCESS_MEMORY == 0 {
		t.Fatalf("Job Object hard commit readback mismatch: err=%v size=%d cap=%d flags=%x",
			err, returned, readback.ProcessMemoryLimit, readback.BasicLimitInformation.LimitFlags)
	}
	// Cross-check the kernel hard cap from INSIDE the limited PxGo PID,
	// rather than relying only on the parent Job Object handle readback.
	if _, err := io.WriteString(stdin, "JOBINFO\n"); err != nil {
		t.Fatal(err)
	}
	childLimit, err := capacityReadJobPrefix(p.output, "JOB_PROXY_OWN_JOBINFO ")
	if err != nil || childLimit != fmt.Sprintf("process_commit_limit=%d process_mem_flag=true", hardLimit) {
		t.Fatalf("child-observed process Job Object cap mismatches parent: marker=%q expected=%d err=%v", childLimit, hardLimit, err)
	}
	t.Logf("JOB_PROXY_OWN_JOBINFO verified=true child_pid=%d process_commit_limit=%d", cmd.Process.Pid, hardLimit)
	monitor := startCapacityIsolatedSampler(cmd.Process.Pid, baseline)
	defer monitor.stopSampling()

	var e error
	echo, e = net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			workers.Add(1)
			go func(c net.Conn) {
				defer workers.Done()
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(c)
		}
	}()
	type opening struct {
		idx  int
		conn net.Conn
		err  error
	}
	results := make(chan opening, capacityJobProxyHeld)
	tokens := make(chan struct{}, 16)
	for i := range held {
		go func(i int) {
			tokens <- struct{}{}
			c, err := capacityMixedConnect(proxy, echo.Addr().String(), i)
			<-tokens
			results <- opening{i, c, err}
		}(i)
	}
	// Drain ALL dial goroutine results before failing; an early t.Fatal
	// could orphan uncollected open tunnels and hang echo workers on teardown.
	var firstConnectErr error
	for range held {
		r := <-results
		if r.err != nil {
			if r.conn != nil {
				_ = r.conn.Close()
			}
			if firstConnectErr == nil {
				firstConnectErr = fmt.Errorf("CONNECT[%d] under quota: %w", r.idx, r.err)
			}
			continue
		}
		held[r.idx] = r.conn
	}
	if firstConnectErr != nil {
		t.Fatal(firstConnectErr)
	}
	plateau, err := capacityJobProxySnapshot(p)
	if err != nil || plateau.ActiveDownstream != capacityJobProxyHeld {
		t.Fatalf("real proxy plateau physical permits=%d expected=%d err=%v",
			plateau.ActiveDownstream, capacityJobProxyHeld, err)
	}
	if os.Getenv("PXGO_CAPACITY_JOB_PROXY_INJECT_FAILURE") == "post_plateau" {
		t.Fatal("INJECTED_FAILURE_AFTER_2560_HELD_CONNECT")
	}
	classes := os.Getenv("PXGO_EXPERIMENTAL_CONNECTION_CLASSES") == "1"
	if classes {
		if plateau.ClassPools == nil || plateau.ClassPools.Long != capacityJobProxyHeld ||
			plateau.ClassPools.Total != capacityJobProxyHeld || plateau.ClassPools.Short != 0 ||
			plateau.ClassPools.Provisional != 0 {
			t.Fatalf("real proxy class tracking inconsistent at plateau: %+v", plateau.ClassPools)
		}
	} else if plateau.ClassPools != nil {
		t.Fatalf("classes unexpectedly enabled: %+v", plateau.ClassPools)
	}
	plateauResources, err := readCapacityIsolatedResources(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if plateauResources.PrivateBytes >= hardLimit ||
		plateauResources.PrivateBytes+uint64(capacityJobProbeLargeCommit) <= hardLimit ||
		plateauResources.PrivateBytes+uint64(capacityJobProbeSmallCommit) >= hardLimit {
		t.Fatalf("insufficient bounded pressure margin: live=%d hard=%d",
			plateauResources.PrivateBytes, hardLimit)
	}
	monitor.setPhase(isolatedMixed)
	if _, err := io.WriteString(stdin, "PROBE\n"); err != nil {
		t.Fatal(err)
	}
	probe, err := capacityReadJobPrefix(p.output, "JOB_PROXY_PROBE ")
	wantProbe := fmt.Sprintf("denied_bytes=%d recovered_bytes=%d",
		capacityJobProbeLargeCommit, capacityJobProbeSmallCommit)
	if err != nil || probe != wantProbe {
		t.Fatalf("live PxGo quota denial/recovery not proven: marker=%q err=%v", probe, err)
	}

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "mixed-ok")
	}))
	defer origin.Close()
	shortTokens := make(chan struct{}, 16)
	shortErrors := make(chan error, capacityJobProxyShort)
	var shortWG sync.WaitGroup
	for i := 0; i < capacityJobProxyShort; i++ {
		shortWG.Add(1)
		go func(index int) {
			defer shortWG.Done()
			shortTokens <- struct{}{}
			defer func() { <-shortTokens }()
			c, err := net.DialTimeout("tcp", proxy, 5*time.Second)
			if err != nil {
				shortErrors <- fmt.Errorf("short[%d] dial: %w", index, err)
				return
			}
			err = capacityMixedHTTP(c, bufio.NewReader(c), origin.URL, false)
			_ = c.Close()
			if err != nil {
				shortErrors <- fmt.Errorf("short[%d] HTTP: %w", index, err)
			}
		}(i)
	}
	shortWG.Wait()
	close(shortErrors)
	count := 0
	var firstErr error
	for e := range shortErrors {
		count++
		if firstErr == nil {
			firstErr = e
		}
	}
	if count != 0 {
		t.Fatalf("real PxGo under quota: %d/%d short requests failed, first=%v",
			count, capacityJobProxyShort, firstErr)
	}
	for i, c := range held {
		if err := verifyCapacityHeldTunnel(c, i); err != nil {
			t.Fatalf("held tunnel[%d] corrupted after live quota denial: %v", i, err)
		}
	}
	monitor.setPhase(isolatedDrain)
	for i, c := range held {
		_ = c.Close()
		held[i] = nil
	}
	deadline := time.Now().Add(5 * time.Second)
	var drained diagnostic.CapacitySnapshot
	for time.Now().Before(deadline) {
		drained, err = capacityJobProxySnapshot(p)
		if err != nil {
			t.Fatal(err)
		}
		if drained.ActiveDownstream == 0 && (drained.ClassPools == nil || drained.ClassPools.Total == 0) {
			break
		}
		time.Sleep(40 * time.Millisecond)
	}
	if drained.ActiveDownstream != 0 || drained.ClassPools != nil && drained.ClassPools.Total != 0 {
		t.Fatalf("real PxGo failed to drain physical/class resources: %+v", drained)
	}
	peak, _, samples, sampleErr := monitor.stopSampling()
	if sampleErr != nil || peak.PrivateBytes > hardLimit || plateauResources.PrivateBytes <= baseline.PrivateBytes ||
		samples[0] == 0 || samples[1] == 0 || samples[2] == 0 {
		t.Fatalf("resource hard limit/telemetry failed err=%v baseline=%+v plateau=%+v peak=%+v hard=%d",
			sampleErr, baseline, plateauResources, peak, hardLimit)
	}
	t.Logf("JOB_PROXY_2560_RESULT enforceable=true pid=%d held=%d completed_short=%d denied_bytes=%d recovered_bytes=%d baseline_private=%d plateau_private=%d peak_private=%d hard_commit=%d peak_handles=%d sample_ramp=%d sample_mixed=%d sample_drain=%d physical_after=0",
		cmd.Process.Pid, capacityJobProxyHeld, capacityJobProxyShort,
		capacityJobProbeLargeCommit, capacityJobProbeSmallCommit,
		baseline.PrivateBytes, plateauResources.PrivateBytes, peak.PrivateBytes, hardLimit, peak.Handles,
		samples[0], samples[1], samples[2])
}

// This negative gate spawns the full 2560-held-CONNECT native test in a
// subprocess, intentionally fails AFTER establishing the physical plateau,
// and requires the parent to finish its real proxy + echo-worker teardown.
// It may not pass merely because a timeout kills the nested test.
func TestCapacityWindowsJobProxyForcedFailureCleanup(t *testing.T) {
	if os.Getenv("PXGO_CAPACITY_JOB_PROXY_CLEANUP_TEST") != "1" {
		t.Skip("opt-in negative real proxy child Job Object cleanup")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0],
		"-test.run=^TestCapacityWindowsJobProxy2560Pressure$", "-test.count=1", "-test.v", "-test.timeout=35s")
	cmd.Env = append(os.Environ(),
		"PXGO_CAPACITY_JOB_PROXY_2560=1",
		"PXGO_CAPACITY_JOB_PROXY_INJECT_FAILURE=post_plateau",
		"PXGO_EXPERIMENTAL_CAPACITY_MODE=auto",
		"PXGO_EXPERIMENTAL_CONNECTION_CLASSES=1",
		"PXGO_RESEARCH_CAPACITY_POLICY=r11")
	start := time.Now()
	output, err := cmd.CombinedOutput()
	raw := string(output)
	if ctx.Err() != nil {
		t.Fatalf("negative 2560-connect cleanup timed out: %v", ctx.Err())
	}
	if err == nil || !strings.Contains(raw, "INJECTED_FAILURE_AFTER_2560_HELD_CONNECT") {
		t.Fatalf("negative fixture did not fail at exact 2560 plateau: err=%v log=%q", err, raw)
	}
	if !strings.Contains(raw, "JOB_PROXY_CLEANUP_PROOF held_closed=2560 echo_workers_joined=true child_exited=true") {
		t.Fatalf("negative fixture failed to prove normal PxGo child and echo cleanup: %q", raw)
	}
	if strings.Contains(raw, "real PxGo child was not gracefully shut down") ||
		strings.Contains(raw, "panic:") || strings.Contains(raw, "fatal error:") {
		t.Fatalf("negative cleanup unexpectedly crashed or leaked child: %q", raw)
	}
	t.Logf("JOB_PROXY_CLEANUP_NEGATIVE expected_injected_failure=true clean_child_exit=true echo_joined=true elapsed=%s", time.Since(start))
}
