package proxy

import (
	"context"
	"errors"
	"net"
	"os"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
)

func TestCapacityModeAndLegacyValidation(t *testing.T) {
	cfg := config.Default()
	if err := validateServerBudgets(cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Workers = 0
	if err := validateServerBudgetsForMode(cfg, modeLegacy); err == nil {
		t.Fatal("legacy silently accepted invalid workers")
	}
	if err := validateServerBudgetsForMode(cfg, modeAuto); err != nil {
		t.Fatalf("ignored legacy budget rejected in AUTO: %v", err)
	}
	cfg.MaxConnections = -1
	if err := validateServerBudgetsForMode(cfg, modeAuto); err == nil {
		t.Fatal("negative manual ceiling accepted")
	}
	for _, tc := range []struct {
		raw  string
		mode capacityMode
		bad  bool
	}{{"", modeLegacy, false}, {"legacy", modeLegacy, false}, {"shadow", modeShadow, false}, {"auto", modeAuto, false}, {"oops", "", true}} {
		t.Run(tc.raw, func(t *testing.T) {
			t.Setenv("PXGO_EXPERIMENTAL_CAPACITY_MODE", tc.raw)
			got, err := experimentalCapacityMode()
			if (err != nil) != tc.bad || got != tc.mode {
				t.Fatalf("mode %q=%q err=%v", tc.raw, got, err)
			}
		})
	}
}

func TestCapacityGovernorManualCeilingAndRelease(t *testing.T) {
	cfg := config.Default()
	cfg.MaxConnections = 2
	g := newCapacityGovernor(modeAuto, cfg)
	done := make(chan struct{})
	for i := 0; i < 2; i++ {
		if err := g.acquire(done, time.Second); err != nil {
			t.Fatal(err)
		}
	}
	if got := g.snapshot(); got.ActiveDownstream != 2 || got.EffectiveLimit != 2 {
		t.Fatalf("snapshot=%+v", got)
	}
	err := g.acquire(done, 15*time.Millisecond)
	var timeout net.Error
	if !errors.As(err, &timeout) || !timeout.Timeout() {
		t.Fatalf("want temporary timeout, got %v", err)
	}
	g.release()
	if err := g.acquire(done, time.Second); err != nil {
		t.Fatal(err)
	}
	g.release()
	g.release()
	if g.snapshot().ActiveDownstream != 0 {
		t.Fatal("capacity leaked")
	}
	g.close()
	if err := g.acquire(done, time.Second); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("closed governor accepted: %v", err)
	}
}

func TestCapacityAIMDRequiresIndependentPressureAndFreshDemand(t *testing.T) {
	g := newCapacityGovernor(modeAuto, config.Default())
	now := time.Unix(10000, 0)
	healthy := capacitySample{available: true, freeFD: 8192, totalFD: 10000}
	for i := 0; i < 10; i++ {
		g.sample(healthy, now.Add(time.Duration(i)*time.Second))
	}
	if got := g.snapshot(); got.SoftWindow != capacityColdStartWindow {
		t.Fatalf("idle demand grew: %+v", got)
	}
	g.mu.Lock()
	g.active = g.soft
	g.mu.Unlock()
	for i := 10; i < 16; i++ {
		g.sample(healthy, now.Add(time.Duration(i)*time.Second))
	}
	before := g.snapshot()
	if before.SoftWindow <= capacityColdStartWindow {
		t.Fatalf("sustained healthy saturation failed growth: %+v", before)
	}
	for i := 16; i < 22; i++ {
		g.sample(capacitySample{reason: "missing"}, now.Add(time.Duration(i)*time.Second))
	}
	if g.snapshot().SoftWindow != before.SoftWindow {
		t.Fatal("unsupported sensor changed window")
	}
	pressured := capacitySample{available: true, freeFD: 50, totalFD: 10000}
	g.sample(pressured, now.Add(23*time.Second))
	g.sample(pressured, now.Add(24*time.Second))
	after := g.snapshot()
	if after.SoftWindow >= before.SoftWindow || after.State != "pressure" {
		t.Fatalf("independent pressure not shrinking: before=%+v after=%+v", before, after)
	}
	if after.ActiveDownstream != before.ActiveDownstream {
		t.Fatal("shrink evicted existing connections")
	}
}

func TestCapacityEmergencyAndAutoShutdown(t *testing.T) {
	g := newCapacityGovernor(modeAuto, config.Default())
	done := make(chan struct{})
	g.recordAcceptError(os.ErrClosed)
	if g.snapshot().EffectiveLimit == 0 {
		t.Fatal("non-FD error caused emergency")
	}
	g.recordAcceptError(errors.Join(errors.New("listener"), errors.New("not EMFILE")))
	if g.snapshot().State != "normal" {
		t.Fatal("unexpected pressure")
	}
	g.mu.Lock()
	g.active = g.effective
	g.mu.Unlock()
	result := make(chan error, 1)
	go func() { result <- g.acquire(done, 2*time.Second) }()
	deadline := time.After(time.Second)
	for {
		g.mu.Lock()
		w := g.waiting
		g.mu.Unlock()
		if w > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("waiter not registered")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	g.close()
	select {
	case err := <-result:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not wake waiting admission")
	}
}

func TestCapacitySharedListenerBoundedWaitAndNoDoubleRelease(t *testing.T) {
	cfg := config.Default()
	cfg.MaxConnections = 1
	g := newCapacityGovernor(modeAuto, cfg)
	one, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	two, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	la := newDynamicAdmissionListener(one, g, 35*time.Millisecond)
	lb := newDynamicAdmissionListener(two, g, 35*time.Millisecond)
	defer la.Close()
	defer lb.Close()
	defer g.close()
	client, err := net.Dial("tcp", la.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	first, err := la.Accept()
	if err != nil {
		t.Fatal(err)
	}
	pending, err := net.Dial("tcp", lb.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer pending.Close()
	_, err = lb.Accept()
	var timeout net.Error
	if !errors.As(err, &timeout) {
		t.Fatalf("bounded wait should be temporary: %v", err)
	}
	if g.snapshot().ActiveDownstream != 1 {
		t.Fatal("timed-out accept leaked capacity")
	}
	_ = first.Close()
	_ = first.Close()
	if g.snapshot().ActiveDownstream != 0 {
		t.Fatal("double-close leaked permit")
	}
	client2, err := net.Dial("tcp", lb.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client2.Close()
	accepted, err := lb.Accept()
	if err != nil {
		t.Fatal(err)
	}
	_ = accepted.Close()
}

func TestCapacityConcurrentAdmitRelease(t *testing.T) {
	cfg := config.Default()
	cfg.MaxConnections = 8
	g := newCapacityGovernor(modeAuto, cfg)
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Go(func() {
			if err := g.acquire(make(chan struct{}), time.Second); err == nil {
				time.Sleep(time.Millisecond)
				g.release()
			}
		})
	}
	wg.Wait()
	got := g.snapshot()
	if got.ActiveDownstream != 0 || got.EffectiveLimit != 8 {
		t.Fatalf("unbalanced concurrent capacity: %+v", got)
	}
}

func TestCapacityShadowKeepsLegacyAdmissionAuthoritative(t *testing.T) {
	cfg := config.Default()
	g := newCapacityGovernor(modeShadow, cfg)
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := newAdmissionListener(raw, make(chan struct{}, 1)).(*admissionListener)
	ln.governor = g
	defer ln.Close()
	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	done := make(chan struct{})
	go func() {
		accepted, err := ln.Accept()
		if err == nil {
			if g.snapshot().ActiveDownstream != 1 {
				t.Error("shadow not observing active socket")
			}
			_ = accepted.Close()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shadow improperly blocked on its own governor")
	}
	if g.snapshot().ActiveDownstream != 0 {
		t.Fatal("shadow count leaked")
	}
}

func TestCapacityShadowDoesNotAcquireTwice(t *testing.T) {
	cfg := config.Default()
	g := newCapacityGovernor(modeShadow, cfg)
	if strings.ToLower(string(g.mode)) != "shadow" {
		t.Fatal("not shadow")
	}
	g.observeAdmission()
	g.observeAdmission()
	if g.snapshot().ActiveDownstream != 2 {
		t.Fatal("shadow not observing admitted sockets")
	}
	g.release()
	g.release()
	if g.snapshot().ActiveDownstream != 0 {
		t.Fatal("shadow counter leaked")
	}
	// Shadow still uses only legacy channel, never the dynamic acquire path.
}

func TestCapacityAutoCanAdmitBeyondLegacy32WithFreshLinuxHeadroom(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux /proc nofile sensor required; Windows AUTO graduation remains gated")
	}
	if !readCapacitySample().available {
		t.Skip("enforceable /proc FD sensor unavailable")
	}
	t.Setenv("PXGO_EXPERIMENTAL_CAPACITY_MODE", "auto")
	cfg := config.Default()
	cfg.Port = 0
	cfg.Listen = "127.0.0.1"
	cfg.ProxyReload = 0
	cfg.SockTimeout = 12
	px, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { finished <- px.Start() }()
	defer func() {
		_ = px.Shutdown(context.Background())
		select {
		case <-finished:
		case <-time.After(2 * time.Second):
			t.Error("proxy did not terminate")
		}
	}()
	limit := time.After(3 * time.Second)
	for px.Port() == 0 {
		select {
		case <-limit:
			t.Fatal("proxy listener did not start")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	addr := px.ListenAddr()
	var clients []net.Conn
	defer func() {
		for _, c := range clients {
			_ = c.Close()
		}
	}()
	for i := 0; i < 42; i++ {
		c, err := net.DialTimeout("tcp", addr, time.Second)
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		clients = append(clients, c)
	}
	timeout := time.After(9 * time.Second)
	for {
		snap := px.capacityDiagnosticSnapshot()
		if snap.ActiveDownstream > 32 {
			break
		}
		select {
		case <-timeout:
			t.Fatalf("AUTO did not exceed legacy 32: %+v", snap)
		default:
			time.Sleep(50 * time.Millisecond)
		}
	}
	for _, c := range clients {
		_ = c.Close()
	}
	clients = nil
	timeout = time.After(4 * time.Second)
	for px.capacityDiagnosticSnapshot().ActiveDownstream != 0 {
		select {
		case <-timeout:
			t.Fatalf("downstream lease retained after clients closed: %+v", px.capacityDiagnosticSnapshot())
		default:
			time.Sleep(50 * time.Millisecond)
		}
	}
}

func TestCapacityEMFILEUnavailableSensorUsesOneBoundedRecoveryProbe(t *testing.T) {
	cfg := config.Default()
	g := newCapacityGovernor(modeAuto, cfg)
	g.recordAcceptError(syscall.EMFILE)
	if snap := g.snapshot(); snap.State != capacityStatePressure || snap.EffectiveLimit != 0 {
		t.Fatalf("emergency must close new admission: %+v", snap)
	}
	g.mu.Lock()
	early := g.cooldown.Add(-time.Millisecond)
	late := g.cooldown.Add(time.Millisecond)
	g.mu.Unlock()
	g.sample(capacitySample{reason: "fd sensor unavailable"}, early)
	if g.snapshot().EffectiveLimit != 0 {
		t.Fatal("admitted before emergency cooldown")
	}
	g.sample(capacitySample{reason: "fd sensor unavailable"}, late)
	if snap := g.snapshot(); snap.EffectiveLimit != 1 {
		t.Fatalf("single recovery probe missing: %+v", snap)
	}
	done := make(chan struct{})
	if err := g.acquire(done, time.Second); err != nil {
		t.Fatal(err)
	}
	if snap := g.snapshot(); snap.ActiveDownstream != 1 || snap.EffectiveLimit != 0 {
		t.Fatalf("probe not consumed atomically: %+v", snap)
	}
	if err := g.acquire(done, time.Millisecond); err == nil {
		t.Fatal("unbounded second probe")
	}
	g.release()
	g.mu.Lock()
	late = g.cooldown.Add(time.Millisecond)
	g.mu.Unlock()
	g.sample(capacitySample{reason: "fd sensor unavailable"}, late)
	if g.snapshot().EffectiveLimit != 1 {
		t.Fatal("cooldown cannot autonomously probe recovery")
	}
	if err := g.acquire(done, time.Second); err != nil {
		t.Fatal(err)
	}
	g.recordAcceptError(syscall.ENFILE)
	if g.snapshot().EffectiveLimit != 0 {
		t.Fatal("repeat exhaustion did not fence new admission")
	}
	g.release()
}

func TestCapacityRecoveryProbeNeverExceedsSoftHardOrManual(t *testing.T) {
	cfg := config.Default()
	g := newCapacityGovernor(modeAuto, cfg)
	g.recordAcceptError(syscall.EMFILE)
	g.mu.Lock()
	g.active = g.hard
	late := g.cooldown.Add(time.Millisecond)
	g.mu.Unlock()
	g.sample(capacitySample{reason: "sensor unavailable"}, late)
	if snap := g.snapshot(); snap.EffectiveLimit != 0 {
		t.Fatalf("probe exceeded full hard envelope: %+v", snap)
	}
	g.release() // One existing useful connection naturally closes.
	if snap := g.snapshot(); snap.EffectiveLimit != g.active+1 || snap.EffectiveLimit > snap.HardEnvelope {
		t.Fatalf("single probe did not respect freed headroom: %+v", snap)
	}
	if err := g.acquire(make(chan struct{}), time.Second); err != nil {
		t.Fatal(err)
	}
	if snap := g.snapshot(); snap.ActiveDownstream != snap.HardEnvelope || snap.EffectiveLimit != 0 {
		t.Fatalf("probe violated hard cap: %+v", snap)
	}
	g.release()
	cfg.MaxConnections = 1
	manual := newCapacityGovernor(modeAuto, cfg)
	manual.recordAcceptError(syscall.EMFILE)
	manual.mu.Lock()
	manual.active = 1
	late = manual.cooldown.Add(time.Millisecond)
	manual.mu.Unlock()
	manual.sample(capacitySample{reason: "sensor unavailable"}, late)
	if snap := manual.snapshot(); snap.EffectiveLimit != 0 {
		t.Fatalf("probe bypassed manual hard ceiling: %+v", snap)
	}
	manual.release()
	if snap := manual.snapshot(); snap.EffectiveLimit != 1 {
		t.Fatalf("probe should reopen below manual limit: %+v", snap)
	}
}

func TestCapacityInvalidSampleRevokesOutstandingRecoveryProbe(t *testing.T) {
	g := newCapacityGovernor(modeAuto, config.Default())
	g.recordAcceptError(syscall.EMFILE)
	g.mu.Lock()
	late := g.cooldown.Add(time.Millisecond)
	g.mu.Unlock()
	g.sample(capacitySample{reason: "sensor unavailable"}, late)
	if g.snapshot().EffectiveLimit != 1 {
		t.Fatal("expected one pending probe")
	}
	// An available-but-contradictory sensor sample must withdraw that probe
	// even when the sensor transport itself cannot establish a valid envelope.
	g.sample(capacitySample{available: true, totalFD: 100, freeFD: 101}, late.Add(time.Second))
	if snap := g.snapshot(); snap.SensorAvailability != "invalid" || snap.EffectiveLimit != 0 {
		t.Fatalf("stale probe remains open after contradictory sample: %+v", snap)
	}
	if err := g.acquire(make(chan struct{}), 5*time.Millisecond); err == nil {
		t.Fatal("invalid sensor permitted admission")
	}
}

func TestCapacityFastGrowthRequiresHeadroomAndSustainedDemand(t *testing.T) {
	g := newCapacityGovernor(modeAuto, config.Default())
	if got := g.snapshot(); got.SoftWindow != 256 || got.HardEnvelope != 256 {
		t.Fatalf("start target not 256: %+v", got)
	}
	now := time.Unix(10000, 0)
	ample := capacitySample{available: true, totalFD: 32768, freeFD: 30000}
	g.sample(ample, now)
	if snap := g.snapshot(); snap.SoftWindow != 256 || snap.HardEnvelope <= 256 {
		t.Fatalf("unsafe immediate growth or stale hard envelope: %+v", snap)
	}
	for i := 1; i <= 15; i++ {
		// Deterministically model sustained queued demand without needing
		// thousands of sockets in a unit test; real traffic has a separate gate.
		g.mu.Lock()
		g.waiting = 1
		g.mu.Unlock()
		g.sample(ample, now.Add(time.Duration(i)*time.Second))
	}
	if snap := g.snapshot(); snap.SoftWindow < 2560 {
		t.Fatalf("AUTO failed to grow toward 2560 with healthy headroom and demand: %+v", snap)
	}
	capped := newCapacityGovernor(modeAuto, config.Config{MaxConnections: 96})
	capped.sample(ample, now)
	if snap := capped.snapshot(); snap.EffectiveLimit != 96 {
		t.Fatalf("manual ceiling not effective with 256 start: %+v", snap)
	}
	small := newCapacityGovernor(modeAuto, config.Default())
	small.sample(capacitySample{available: true, totalFD: 128, freeFD: 121}, now)
	if snap := small.snapshot(); snap.EffectiveLimit >= 256 || snap.EffectiveLimit <= 0 {
		t.Fatalf("small RLIMIT not respected at startup: %+v", snap)
	}
}
