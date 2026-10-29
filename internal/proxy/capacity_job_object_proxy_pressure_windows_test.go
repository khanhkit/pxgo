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
	"runtime"
	"strconv"
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
	capacityJobProxyHeld     = 2560
	capacityJobProxyShort    = 600
	capacityJobProxyBudget   = uint64(384 << 20)
	capacityJobProxyHold     = uintptr(64 << 20)
	capacityJobProxyHeapHold = 32 << 20
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
	var heldCommit uintptr
	var heldGoHeap [][]byte
	var heapBaseline uint64
	defer func() {
		if heldCommit != 0 {
			_ = windows.VirtualFree(heldCommit, 0, windows.MEM_RELEASE)
		}
	}()
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
		case "JOBWIDEINFO":
			var mine windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
			var n uint32
			err := windows.QueryInformationJobObject(0, windows.JobObjectExtendedLimitInformation,
				uintptr(unsafe.Pointer(&mine)), uint32(unsafe.Sizeof(mine)), &n)
			flags := mine.BasicLimitInformation.LimitFlags
			if err != nil || n != uint32(unsafe.Sizeof(mine)) ||
				flags&windows.JOB_OBJECT_LIMIT_JOB_MEMORY == 0 ||
				flags&windows.JOB_OBJECT_LIMIT_PROCESS_MEMORY == 0 ||
				mine.JobMemoryLimit == 0 || mine.ProcessMemoryLimit <= mine.JobMemoryLimit {
				t.Fatalf("real PxGo child cannot see stricter job-wide cap: %v size=%d flags=%x", err, n, flags)
			}
			fmt.Printf("JOB_PROXY_OWN_JOBWIDE job_cap=%d process_cap=%d flags_verified=true\n",
				mine.JobMemoryLimit, mine.ProcessMemoryLimit)
		case "JOBWIDE_PROBE":
			if !in.Scan() {
				t.Fatal("missing dynamically calibrated job-wide test allocation bytes")
			}
			requested, err := strconv.ParseUint(strings.TrimSpace(in.Text()), 10, 64)
			if err != nil || requested <= uint64(capacityJobProbeSmallCommit) || requested > uint64(256<<20) {
				t.Fatalf("invalid job-wide probe bytes=%q err=%v", in.Text(), err)
			}
			addr, err := windows.VirtualAlloc(0, uintptr(requested),
				windows.MEM_COMMIT|windows.MEM_RESERVE, windows.PAGE_READWRITE)
			if addr != 0 {
				_ = windows.VirtualFree(addr, 0, windows.MEM_RELEASE)
				t.Fatalf("allocation unexpectedly succeeded above stricter job-wide cap: %d", requested)
			}
			if err == nil || !capacityJobMemoryError(err) {
				t.Fatalf("job-wide commit denied for unrelated reason: bytes=%d err=%v", requested, err)
			}
			// A small commit still succeeds inside the same living child process.
			again, err := windows.VirtualAlloc(0, capacityJobProbeSmallCommit,
				windows.MEM_COMMIT|windows.MEM_RESERVE, windows.PAGE_READWRITE)
			if err != nil || again == 0 {
				t.Fatalf("job-wide probe recovery failed: %v", err)
			}
			if err := windows.VirtualFree(again, 0, windows.MEM_RELEASE); err != nil {
				t.Fatal(err)
			}
			fmt.Printf("JOB_PROXY_JOBWIDE_PROBE denied_bytes=%d recovered_bytes=%d\n",
				requested, capacityJobProbeSmallCommit)
		case "JOBWIDE_CONTROL":
			// Independent causal control: the same live PxGo child attempts
			// the SAME MEM_COMMIT after the parent raises ONLY JobMemoryLimit
			// to the unchanged process cap. Fail if any other resource denies.
			if !in.Scan() {
				t.Fatal("missing job-wide positive-control allocation bytes")
			}
			requested, err := strconv.ParseUint(strings.TrimSpace(in.Text()), 10, 64)
			if err != nil || requested <= uint64(capacityJobProbeSmallCommit) || requested > uint64(256<<20) {
				t.Fatalf("invalid native control bytes=%q err=%v", in.Text(), err)
			}
			var mine windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
			var n uint32
			err = windows.QueryInformationJobObject(0, windows.JobObjectExtendedLimitInformation,
				uintptr(unsafe.Pointer(&mine)), uint32(unsafe.Sizeof(mine)), &n)
			if err != nil || n != uint32(unsafe.Sizeof(mine)) ||
				mine.BasicLimitInformation.LimitFlags&windows.JOB_OBJECT_LIMIT_JOB_MEMORY == 0 ||
				mine.JobMemoryLimit != mine.ProcessMemoryLimit {
				t.Fatalf("positive-control native Job Object wasn't raised: %v job=%d process=%d", err, mine.JobMemoryLimit, mine.ProcessMemoryLimit)
			}
			addr, err := windows.VirtualAlloc(0, uintptr(requested),
				windows.MEM_COMMIT|windows.MEM_RESERVE, windows.PAGE_READWRITE)
			if err != nil || addr == 0 {
				t.Fatalf("same allocation denied AFTER relaxing ONLY job-wide cap: %v bytes=%d", err, requested)
			}
			if err := windows.VirtualFree(addr, 0, windows.MEM_RELEASE); err != nil {
				t.Fatalf("native job-wide positive-control free failed: %v", err)
			}
			fmt.Printf("JOB_PROXY_JOBWIDE_CONTROL allowed_bytes=%d cap_equal=true\n", requested)
		case "SENSOR":
			// Existing opt-in AUTO Windows estimate uses host-wide global
			// memory, NOT this process's stricter Job Object quota.
			sample := readCapacitySample()
			if !sample.available || sample.resource != capacityWindowsSensor ||
				sample.freeSlots <= 0 {
				t.Fatalf("cannot obtain native Windows AUTO sensor: %+v", sample)
			}
			fmt.Printf("JOB_PROXY_GLOBAL_SENSOR resource=windows-memory-handles available=true reported_free_slots=%d\n",
				sample.freeSlots)
		case "HEAP_HOLD":
			if heldGoHeap != nil {
				t.Fatal("Go heap ballast already held")
			}
			runtime.GC()
			var before runtime.MemStats
			runtime.ReadMemStats(&before)
			heapBaseline = before.Alloc
			heldGoHeap = make([][]byte, 0, 8)
			for range 8 {
				chunk := make([]byte, 4<<20)
				for page := 0; page < len(chunk); page += 4096 {
					chunk[page] = 0xA5
				}
				heldGoHeap = append(heldGoHeap, chunk)
			}
			runtime.GC()
			var live runtime.MemStats
			runtime.ReadMemStats(&live)
			runtime.KeepAlive(heldGoHeap)
			if live.Alloc < heapBaseline+uint64(28<<20) {
				t.Fatalf("32MiB Go heap retention not proved: before=%d after=%d", heapBaseline, live.Alloc)
			}
			fmt.Printf("JOB_PROXY_GO_HEAP_HOLD allocated_bytes=%d alloc_before=%d alloc_after=%d\n", capacityJobProxyHeapHold, heapBaseline, live.Alloc)
		case "HEAP_STATUS":
			if len(heldGoHeap) != 8 {
				t.Fatal("Go heap ballast unexpectedly missing")
			}
			runtime.GC()
			var during runtime.MemStats
			runtime.ReadMemStats(&during)
			runtime.KeepAlive(heldGoHeap)
			if during.Alloc < heapBaseline+uint64(28<<20) {
				t.Fatalf("Go heap ballast not sustained: baseline=%d now=%d", heapBaseline, during.Alloc)
			}
			fmt.Printf("JOB_PROXY_GO_HEAP_STATUS allocated_bytes=%d alloc_current=%d\n", capacityJobProxyHeapHold, during.Alloc)
		case "HEAP_FREE":
			if len(heldGoHeap) != 8 {
				t.Fatal("Go heap ballast already freed")
			}
			heldGoHeap = nil
			runtime.GC()
			var after runtime.MemStats
			runtime.ReadMemStats(&after)
			fmt.Printf("JOB_PROXY_GO_HEAP_FREE released_bytes=%d alloc_after_gc=%d\n", capacityJobProxyHeapHold, after.Alloc)
		case "HOLD_COMMIT":
			if heldCommit != 0 {
				t.Fatal("already holding a native committed-memory ballast")
			}
			addr, err := windows.VirtualAlloc(0, capacityJobProxyHold,
				windows.MEM_COMMIT|windows.MEM_RESERVE, windows.PAGE_READWRITE)
			if err != nil || addr == 0 {
				t.Fatalf("64MiB native live-child ballast failed: %v", err)
			}
			heldCommit = addr
			fmt.Printf("JOB_PROXY_HOLD committed_bytes=%d\n", capacityJobProxyHold)
		case "FREE_COMMIT":
			if heldCommit == 0 {
				t.Fatal("cannot free non-existent Job Object ballast")
			}
			if err := windows.VirtualFree(heldCommit, 0, windows.MEM_RELEASE); err != nil {
				t.Fatalf("release native live-child ballast failed: %v", err)
			}
			heldCommit = 0
			fmt.Printf("JOB_PROXY_UNHOLD released_bytes=%d\n", capacityJobProxyHold)
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
	jobWide := os.Getenv("PXGO_CAPACITY_JOB_PROXY_JOBWIDE") == "1"
	hardLimit := baseline.PrivateBytes + capacityJobProxyBudget
	var jobWideLimit uint64
	if jobWide {
		// One 64MiB gap makes job-wide memory stricter than process memory.
		hardLimit = baseline.PrivateBytes + uint64(448<<20)
		jobWideLimit = baseline.PrivateBytes + capacityJobProxyBudget
	}
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
	if jobWide {
		limits.JobMemoryLimit = uintptr(jobWideLimit)
		limits.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_JOB_MEMORY
	}
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
		readback.BasicLimitInformation.LimitFlags&windows.JOB_OBJECT_LIMIT_PROCESS_MEMORY == 0 ||
		(jobWide && (uint64(readback.JobMemoryLimit) != jobWideLimit ||
			readback.BasicLimitInformation.LimitFlags&windows.JOB_OBJECT_LIMIT_JOB_MEMORY == 0)) {
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
	if jobWide {
		if _, err := io.WriteString(stdin, "JOBWIDEINFO\n"); err != nil {
			t.Fatal(err)
		}
		self, err := capacityReadJobPrefix(p.output, "JOB_PROXY_OWN_JOBWIDE ")
		if err != nil || self != fmt.Sprintf("job_cap=%d process_cap=%d flags_verified=true", jobWideLimit, hardLimit) {
			t.Fatalf("real child did not observe stricter job-wide cap: self=%q err=%v", self, err)
		}
		t.Logf("JOB_PROXY_OWN_JOBWIDE verified=true pid=%d job_cap=%d process_cap=%d",
			cmd.Process.Pid, jobWideLimit, hardLimit)
	}
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
	var jobWideProbeBytes uint64
	if jobWide {
		// Calibrate while 2560 sockets are ACTUALLY held. The allocation
		// exceeds job-wide headroom by 32MiB yet is at least 32MiB BELOW
		// the process-only cap; therefore a denial isolates job-wide quota.
		if plateauResources.PrivateBytes+uint64(64<<20) >= jobWideLimit {
			t.Fatalf("insufficient 64MiB safe headroom in real child before job-wide probe: %d / %d",
				plateauResources.PrivateBytes, jobWideLimit)
		}
		jobWideProbeBytes = jobWideLimit - plateauResources.PrivateBytes + uint64(32<<20)
		if plateauResources.PrivateBytes+jobWideProbeBytes+uint64(32<<20) > hardLimit ||
			jobWideProbeBytes > uint64(256<<20) {
			t.Fatalf("job-wide calibrated denial not isolated from process cap: live=%d job=%d process=%d bytes=%d",
				plateauResources.PrivateBytes, jobWideLimit, hardLimit, jobWideProbeBytes)
		}
		if _, err := fmt.Fprintf(stdin, "JOBWIDE_PROBE\n%d\n", jobWideProbeBytes); err != nil {
			t.Fatal(err)
		}
		proof, err := capacityReadJobPrefix(p.output, "JOB_PROXY_JOBWIDE_PROBE ")
		if err != nil || proof != fmt.Sprintf("denied_bytes=%d recovered_bytes=%d", jobWideProbeBytes, capacityJobProbeSmallCommit) {
			t.Fatalf("real child job-wide allocation denial and recovery unproven: %q err=%v", proof, err)
		}
		// Emit only AFTER validating the exact child IPC marker; the CI log
		// cannot directly see private stdin/stdout evidence consumed by parent.
		t.Logf("JOB_PROXY_JOBWIDE_PROBE denied_bytes=%d recovered_bytes=%d",
			jobWideProbeBytes, capacityJobProbeSmallCommit)
		// Causal control: change ONLY the owned child Job Object total limit
		// to match its UNCHANGED process cap. Same child, same tunnels,
		// same probe size must now succeed; restore strict job cap after Free.
		limits.JobMemoryLimit = uintptr(hardLimit)
		_, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)))
		if err != nil {
			t.Fatalf("could not relax ONLY JobMemoryLimit for positive control: %v", err)
		}
		if _, err := fmt.Fprintf(stdin, "JOBWIDE_CONTROL\n%d\n", jobWideProbeBytes); err != nil {
			t.Fatal(err)
		}
		allowed, err := capacityReadJobPrefix(p.output, "JOB_PROXY_JOBWIDE_CONTROL ")
		if err != nil || allowed != fmt.Sprintf("allowed_bytes=%d cap_equal=true", jobWideProbeBytes) {
			t.Fatalf("same native allocation did not succeed with job-wide cap relaxed: marker=%q err=%v", allowed, err)
		}
		t.Logf("JOB_PROXY_JOBWIDE_CONTROL allowed_bytes=%d cap_equal=true", jobWideProbeBytes)
		limits.JobMemoryLimit = uintptr(jobWideLimit)
		_, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)))
		if err != nil {
			t.Fatalf("failed restoring strict child-only JobMemoryLimit: %v", err)
		}
		var restored windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
		var size uint32
		err = windows.QueryInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&restored)), uint32(unsafe.Sizeof(restored)), &size)
		if err != nil || size != uint32(unsafe.Sizeof(restored)) ||
			uint64(restored.JobMemoryLimit) != jobWideLimit ||
			uint64(restored.ProcessMemoryLimit) != hardLimit ||
			restored.BasicLimitInformation.LimitFlags&windows.JOB_OBJECT_LIMIT_JOB_MEMORY == 0 {
			t.Fatalf("strict job-wide cap not restored: err=%v job=%d process=%d", err, restored.JobMemoryLimit, restored.ProcessMemoryLimit)
		}
	}
	// The optional native ballast is a LIVE committed virtual allocation
	// IN THE REAL PxGo CHILD while all 2560 authenticated tunnels are held.
	// Keep the CPU/Go generator and upstream listeners outside the Job.
	holdCommit := os.Getenv("PXGO_CAPACITY_JOB_PROXY_HOLD_COMMIT") == "1"
	var heldCommittedPrivate uint64
	if holdCommit {
		if plateauResources.PrivateBytes+uint64(capacityJobProxyHold)+uint64(64<<20) >= hardLimit {
			t.Fatalf("unsafe child-only memory ballast headroom: plateau=%d hard=%d",
				plateauResources.PrivateBytes, hardLimit)
		}
		if _, err := io.WriteString(stdin, "HOLD_COMMIT\n"); err != nil {
			t.Fatal(err)
		}
		marker, err := capacityReadJobPrefix(p.output, "JOB_PROXY_HOLD ")
		if err != nil || marker != fmt.Sprintf("committed_bytes=%d", capacityJobProxyHold) {
			t.Fatalf("live child 64MiB commitment not proven: marker=%q err=%v", marker, err)
		}
		measured, err := readCapacityIsolatedResources(cmd.Process.Pid)
		if err != nil {
			t.Fatal(err)
		}
		heldCommittedPrivate = measured.PrivateBytes
		if heldCommittedPrivate < plateauResources.PrivateBytes+uint64(48<<20) || heldCommittedPrivate >= hardLimit {
			t.Fatalf("native child ballast did not account 48MiB minimum: plateau=%d with_ballast=%d hard=%d",
				plateauResources.PrivateBytes, heldCommittedPrivate, hardLimit)
		}
		if os.Getenv("PXGO_CAPACITY_JOB_PROXY_SENSOR_GAP") == "1" {
			// Compare the EXISTING process-handle + host-global-memory
			// AUTO estimate to the extra slot count implied by the same
			// experimental 1MiB-per-socket heuristic inside a tighter child
			// Job Object. The current sensor does not read this Job cap.
			if _, err := io.WriteString(stdin, "SENSOR\n"); err != nil {
				t.Fatal(err)
			}
			global, err := capacityReadJobPrefix(p.output, "JOB_PROXY_GLOBAL_SENSOR ")
			var slots int
			parsed, scanErr := fmt.Sscanf(global,
				"resource=windows-memory-handles available=true reported_free_slots=%d", &slots)
			if err != nil || scanErr != nil || parsed != 1 || slots < 1 {
				t.Fatalf("unverified host-global Windows sensor estimate: raw=%q err=%v parse=%v", global, err, scanErr)
			}
			jobEquivalentSlots := int((hardLimit - heldCommittedPrivate) / uint64(1<<20))
			if jobEquivalentSlots >= slots || jobEquivalentSlots < 1 {
				t.Fatalf("sensor mismatch expected on isolated 16GiB hosted Windows runner: host_slots=%d job_cap_slots=%d", slots, jobEquivalentSlots)
			}
			t.Logf("JOB_PROXY_SENSOR_GAP_RESULT child_job_commit_cap=%d child_private_bytes=%d job_equivalent_slots=%d global_sensor_reported_slots=%d global_exceeds_job=true",
				hardLimit, heldCommittedPrivate, jobEquivalentSlots, slots)
		}
	}
	// Research-only Go heap ballast: intentionally distinct from the
	// R39 native VirtualAlloc ballast; never combine both on one child.
	holdGoHeap := os.Getenv("PXGO_CAPACITY_JOB_PROXY_GO_HEAP_HOLD") == "1"
	var heapAllocBefore, heapAllocHeld uint64
	var heapPrivateHeld uint64
	if holdGoHeap {
		if holdCommit || plateauResources.PrivateBytes+uint64(80<<20) >= hardLimit {
			t.Fatalf("unsafe Go heap test headroom or competing native ballast: native=%t plateau=%d hard=%d",
				holdCommit, plateauResources.PrivateBytes, hardLimit)
		}
		if _, err := io.WriteString(stdin, "HEAP_HOLD\n"); err != nil {
			t.Fatal(err)
		}
		marker, err := capacityReadJobPrefix(p.output, "JOB_PROXY_GO_HEAP_HOLD ")
		var allocated uint64
		if err != nil {
			t.Fatal(err)
		}
		n, parseErr := fmt.Sscanf(marker, "allocated_bytes=%d alloc_before=%d alloc_after=%d",
			&allocated, &heapAllocBefore, &heapAllocHeld)
		if parseErr != nil || n != 3 ||
			marker != fmt.Sprintf("allocated_bytes=%d alloc_before=%d alloc_after=%d", allocated, heapAllocBefore, heapAllocHeld) ||
			allocated != uint64(capacityJobProxyHeapHold) ||
			heapAllocHeld < heapAllocBefore+uint64(28<<20) {
			t.Fatalf("child Go heap hold marker invalid %q parse=%v", marker, parseErr)
		}
		measured, err := readCapacityIsolatedResources(cmd.Process.Pid)
		if err != nil {
			t.Fatal(err)
		}
		heapPrivateHeld = measured.PrivateBytes
		if heapPrivateHeld < plateauResources.PrivateBytes+uint64(16<<20) || heapPrivateHeld >= hardLimit {
			t.Fatalf("Go heap private commit increase not independently observed: baseline=%d held=%d hard=%d",
				plateauResources.PrivateBytes, heapPrivateHeld, hardLimit)
		}
	}
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
	if holdGoHeap {
		// Assert the Go allocator retained the allocation THROUGH the
		// full 600/600 HTTP workload, not merely between control commands.
		if _, err := io.WriteString(stdin, "HEAP_STATUS\n"); err != nil {
			t.Fatal(err)
		}
		marker, err := capacityReadJobPrefix(p.output, "JOB_PROXY_GO_HEAP_STATUS ")
		if err != nil {
			t.Fatal(err)
		}
		var allocated, allocDuring uint64
		n, parseErr := fmt.Sscanf(marker, "allocated_bytes=%d alloc_current=%d", &allocated, &allocDuring)
		if parseErr != nil || n != 2 ||
			marker != fmt.Sprintf("allocated_bytes=%d alloc_current=%d", allocated, allocDuring) ||
			allocated != uint64(capacityJobProxyHeapHold) ||
			allocDuring < heapAllocBefore+uint64(28<<20) {
			t.Fatalf("Go heap no longer live during real short HTTP: %q parse=%v", marker, parseErr)
		}
		activePrivate, err := readCapacityIsolatedResources(cmd.Process.Pid)
		if err != nil || activePrivate.PrivateBytes < plateauResources.PrivateBytes+uint64(16<<20) ||
			activePrivate.PrivateBytes >= hardLimit {
			t.Fatalf("Go heap native commit during short HTTP invalid: private=%d baseline=%d hard=%d err=%v",
				activePrivate.PrivateBytes, plateauResources.PrivateBytes, hardLimit, err)
		}
		if _, err := io.WriteString(stdin, "HEAP_FREE\n"); err != nil {
			t.Fatal(err)
		}
		freeMarker, err := capacityReadJobPrefix(p.output, "JOB_PROXY_GO_HEAP_FREE ")
		if err != nil {
			t.Fatal(err)
		}
		var freed, allocAfterGC uint64
		n, parseErr = fmt.Sscanf(freeMarker, "released_bytes=%d alloc_after_gc=%d", &freed, &allocAfterGC)
		if parseErr != nil || n != 2 ||
			freeMarker != fmt.Sprintf("released_bytes=%d alloc_after_gc=%d", freed, allocAfterGC) ||
			freed != uint64(capacityJobProxyHeapHold) ||
			allocAfterGC+uint64(24<<20) > allocDuring {
			t.Fatalf("Go heap GC release not observed: %q during=%d parse=%v", freeMarker, allocDuring, parseErr)
		}
		t.Logf("JOB_PROXY_GO_HEAP_RESULT held_bytes=%d held_connect=2560 completed_short=600 heap_alloc_before=%d heap_alloc_after_hold=%d heap_alloc_during=%d heap_alloc_after_gc=%d process_private_held=%d hard_commit=%d released_gc=true",
			capacityJobProxyHeapHold, heapAllocBefore, heapAllocHeld, allocDuring, allocAfterGC, heapPrivateHeld, hardLimit)
	}
	if holdCommit {
		// Measure AFTER all 600 short HTTP requests with the child ballast
		// still allocated; free only after proving sustained live commit.
		beforeFree, err := readCapacityIsolatedResources(cmd.Process.Pid)
		if err != nil || beforeFree.PrivateBytes < plateauResources.PrivateBytes+uint64(48<<20) {
			t.Fatalf("64MiB child commit not sustained under short traffic: private=%d plateau=%d err=%v",
				beforeFree.PrivateBytes, plateauResources.PrivateBytes, err)
		}
		if _, err := io.WriteString(stdin, "FREE_COMMIT\n"); err != nil {
			t.Fatal(err)
		}
		marker, err := capacityReadJobPrefix(p.output, "JOB_PROXY_UNHOLD ")
		if err != nil || marker != fmt.Sprintf("released_bytes=%d", capacityJobProxyHold) {
			t.Fatalf("child 64MiB commitment not released: marker=%q err=%v", marker, err)
		}
		afterFree, err := readCapacityIsolatedResources(cmd.Process.Pid)
		if err != nil || afterFree.PrivateBytes+uint64(48<<20) > beforeFree.PrivateBytes {
			t.Fatalf("physical Win32 commit release not measured: before=%d after=%d err=%v",
				beforeFree.PrivateBytes, afterFree.PrivateBytes, err)
		}
		t.Logf("JOB_PROXY_SUSTAINED_COMMIT_RESULT held_bytes=%d completed_short=%d enforced=true private_with_ballast=%d private_after_release=%d hard_commit=%d",
			capacityJobProxyHold, capacityJobProxyShort, beforeFree.PrivateBytes, afterFree.PrivateBytes, hardLimit)
	}
	if jobWide {
		t.Logf("JOB_PROXY_JOBWIDE_RESULT enforceable=true pid=%d held_connect=%d completed_short=%d job_total_limit=%d process_limit=%d job_only_denied=%d recovered_bytes=%d same_allocation_control_succeeded=true",
			cmd.Process.Pid, capacityJobProxyHeld, capacityJobProxyShort,
			jobWideLimit, hardLimit, jobWideProbeBytes, capacityJobProbeSmallCommit)
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
