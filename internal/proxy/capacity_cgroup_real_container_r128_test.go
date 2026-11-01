//go:build linux

package proxy

import (
	"errors"
	"io"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
)

func readR128Metric(t *testing.T, key string) uint64 {
	t.Helper()
	raw, err := os.ReadFile("/sys/fs/cgroup/" + key)
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil {
		t.Fatalf("parse cgroup %s=%q: %v", key, raw, err)
	}
	return n
}

func readR128OOMKills(t *testing.T) uint64 {
	t.Helper()
	raw, err := os.ReadFile("/sys/fs/cgroup/memory.events")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		parts := strings.Fields(line)
		if len(parts) == 2 && parts[0] == "oom_kill" {
			n, err := strconv.ParseUint(parts[1], 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			return n
		}
	}
	t.Fatal("memory.events must expose oom_kill")
	return 0
}

// This test runs ONLY in a disposable Linux Docker with finite actual
// cgroup-v2 memory.max, no network, and pids/CPU/memory limits. A synthetic
// byte-budget mock or GOMEMLIMIT is not accepted as real finite evidence.
func TestR128NativeFiniteCgroupPressurePreservesOldTCP(t *testing.T) {
	if os.Getenv("PXGO_R128_REAL_CGROUP") != "1" {
		t.Skip("explicit opt-in on a dedicated finite Docker cgroup required")
	}
	limit := readR128Metric(t, "memory.max")
	if limit < 256<<20 || limit > 512<<20 {
		t.Fatalf("disposable finite cgroup expected 256..512MiB, got memory.max=%d", limit)
	}
	beforeOOM := readR128OOMKills(t)
	pre := readCapacitySample()
	if !pre.available || !pre.linuxMemoryConstrained || pre.linuxMemoryFreeSlots < 2 || pre.linuxMemoryPressure {
		t.Fatalf("real cgroup fresh initial sensor invalid: %+v limit=%d", pre, limit)
	}
	g := newCapacityGovernor(modeAuto, config.Default())
	defer g.close()
	g.sample(pre, time.Now())
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := newDynamicAdmissionListener(raw, g, 100*time.Millisecond)
	defer ln.Close()
	oldClient, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer oldClient.Close()
	oldConn, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer oldConn.Close()

	// Physical consumption must be visible to the kernel and safely stop
	// roughly 59MiB BEFORE OOM. The controller reserves >=64MiB for safety.
	const margin = uint64(59 << 20)
	const chunkSize = 4 << 20
	blocks := make([][]byte, 0, 96)
	for current := readR128Metric(t, "memory.current"); current < limit-margin; current = readR128Metric(t, "memory.current") {
		if len(blocks) >= 96 {
			t.Fatalf("kernel memory.current failed to reflect 384MiB touched allocation")
		}
		block := make([]byte, chunkSize)
		for p := 0; p < len(block); p += 4096 {
			block[p] = byte(len(blocks) + 1)
		}
		blocks = append(blocks, block)
	}
	post := readCapacitySample()
	current := readR128Metric(t, "memory.current")
	if !post.available || !post.linuxMemoryConstrained || post.linuxMemoryFreeSlots != 0 || !post.linuxMemoryPressure {
		t.Fatalf("REAL_CGROUP_FAIL_CLOSED unavailable: post=%+v memory.current=%d memory.max=%d", post, current, limit)
	}
	g.sample(post, time.Now().Add(time.Second))
	if snap := g.snapshot(); snap.ActiveDownstream != 1 || snap.EffectiveLimit != 0 {
		t.Fatalf("real kernel pressure did not close NEW admissions while preserving old: %+v", snap)
	}
	deniedClient, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer deniedClient.Close()
	if denied, e := ln.Accept(); e == nil {
		_ = denied.Close()
		t.Fatal("new physical TCP permit granted while real memory headroom is zero")
	} else {
		var timeout net.Error
		if !errors.As(e, &timeout) || !timeout.Timeout() {
			t.Fatalf("expected bounded new admission timeout, got %v", e)
		}
	}
	_ = oldConn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err = oldClient.Write([]byte("live")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 4)
	if _, err = io.ReadFull(oldConn, got); err != nil || string(got) != "live" {
		t.Fatalf("real pressure broke old admitted tunnel: %q %v", got, err)
	}
	_ = oldConn.Close()
	if snap := g.snapshot(); snap.ActiveDownstream != 0 || snap.EffectiveLimit != 0 {
		t.Fatalf("old socket close minted new budget under kernel memory pressure: %+v", snap)
	}
	afterOOM := readR128OOMKills(t)
	if afterOOM != beforeOOM {
		t.Fatalf("kernel OOM kill occurred: before=%d after=%d", beforeOOM, afterOOM)
	}
	t.Logf("R128_REAL_CGROUP_RESULT memory_max=%d memory_current=%d blocks=%d pre_free_slots=%d post_free_slots=%d oom_kill_delta=0 old_tcp_alive=true new_tcp_denied=true", limit, current, len(blocks), pre.linuxMemoryFreeSlots, post.linuxMemoryFreeSlots)
	runtime.KeepAlive(blocks)
}
