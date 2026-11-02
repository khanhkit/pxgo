//go:build linux

package proxy

import (
	"github.com/khanhkit/pxgo/internal/config"
	"net"
	"os"
	"testing"
	"time"
)

// Research-only liveness counterexample using REAL TCP in a dedicated Docker
// container with memory.max=384MiB. Measures how virtual closed debts grow
// even when memory.current reports plentiful physical free bytes. Never
// relaxes admission safety to force a desired result. DOES NOT mock a kernel.
func TestR146NativeFiniteCgroupShortRealTCPLivenessChurn(t *testing.T) {
	if os.Getenv("PXGO_R146_CHURN_CGROUP") != "1" {
		t.Skip("finite memcg manual-research only")
	}
	limit := readR128Metric(t, "memory.max")
	if limit < 256<<20 || limit > 512<<20 {
		t.Fatalf("requires bounded 256..512MiB container, actual=%d", limit)
	}
	beforeOOM := readR128OOMKills(t)
	g := newCapacityGovernor(modeAuto, config.Default())
	defer g.close()
	g.sample(readCapacitySample(), time.Now())
	raw, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	ln := newDynamicAdmissionListener(raw, g, 8*time.Millisecond)
	defer ln.Close()
	type pair struct{ server, client net.Conn }
	held := make([]pair, 0, 4)
	defer func() {
		for _, p := range held {
			_ = p.server.Close()
			_ = p.client.Close()
		}
	}()
	for i := 0; i < 4; i++ {
		c, e := net.DialTimeout("tcp", ln.Addr().String(), time.Second)
		if e != nil {
			t.Fatal(e)
		}
		s, e := ln.Accept()
		if e != nil {
			_ = c.Close()
			t.Fatal(e)
		}
		held = append(held, pair{s, c})
	}
	accepted, denied := 0, 0
	firstDenialAt := -1
	const iterations = 600
	for i := 0; i < iterations; i++ {
		g.sample(readCapacitySample(), time.Now())
		client, e := net.DialTimeout("tcp", ln.Addr().String(), 150*time.Millisecond)
		if e != nil {
			t.Fatal(e)
		}
		server, e := ln.Accept()
		if e == nil {
			accepted++
			_ = server.Close()
		} else {
			denied++
			if firstDenialAt < 0 {
				firstDenialAt = i
			}
		}
		_ = client.Close()
		g.mu.Lock()
		live, closed, total, remaining, active := g.memoryActiveDebt, g.memoryClosedDebt, g.memoryPendingSlots, g.memorySampleRemaining, g.active
		g.mu.Unlock()
		if live != 4 || total != live+closed || active != 4 || remaining < 0 {
			t.Fatalf("churn invariant failed i=%d live=%d closed=%d total=%d rem=%d active=%d", i, live, closed, total, remaining, active)
		}
	}
	current := readR128Metric(t, "memory.current")
	s := readCapacitySample()
	afterOOM := readR128OOMKills(t)
	if afterOOM != beforeOOM {
		t.Fatalf("OOM in bounded liveness study, delta=%d", afterOOM-beforeOOM)
	}
	g.mu.Lock()
	closed, remaining := g.memoryClosedDebt, g.memorySampleRemaining
	g.mu.Unlock()
	t.Logf("R146_REAL_TCP_CHURN_RESULT memory_max=%d memory_current=%d physical_free_slots=%d iterations=%d accepted=%d denied=%d first_denial_index=%d closed_virtual_debt=%d virtual_remaining=%d held_real_tcp=4 oom_kill_delta=0", limit, current, s.linuxMemoryFreeSlots, iterations, accepted, denied, firstDenialAt, closed, remaining)
	// This is a performance-observation study. A denial despite 100+ verified
	// physical free slots is a severe starvation symptom and MUST NOT be
	// marketed as a healthy throughput result even if the experiment test passes.
	if denied > 0 && s.linuxMemoryFreeSlots > 100 {
		t.Log("R146_LIVENESS_WARNING virtual closed debt denied real TCP while >100 physical slots remain")
	}
}
