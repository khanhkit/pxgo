//go:build linux

package proxy

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
)

// Separate disposable OS PROCESS in the SAME finite Linux cgroup as the proxy.
// Does not depend on Python being installed in the read-only Alpine image.
func TestR146UnrelatedAllocatorChild(t *testing.T) {
	if os.Getenv("PXGO_R146_ALLOCATOR_CHILD") != "1" {
		t.Skip("child process helper")
	}
	const alloc = 24 << 20
	b := make([]byte, alloc)
	for i := 0; i < len(b); i += 4096 {
		b[i] = byte(i/4096 + 1)
	}
	fmt.Println("R146_CHILD_READY")
	_, _ = io.Copy(io.Discard, os.Stdin)
	if b[0] == 255 {
		t.Log("unlikely side effect to keep allocation live")
	}
}

// Only authorized in isolated Docker --memory 384m --memory-swap 384m,
// --network none, --read-only, --pids-limit 128. This is a real Linux kernel
// sensor + physical listener + governor + UNRELATED external process test.
// It does not prove 1MiB is a HARD upper bound for any TCP tunnel.
func TestR146NativeFiniteCgroupExternalAllocatorPreservesSixTrackedTCP(t *testing.T) {
	if os.Getenv("PXGO_R146_REAL_CGROUP") != "1" {
		t.Skip("explicit Docker cgroup research gate")
	}
	limit := readR128Metric(t, "memory.max")
	if limit < 256<<20 || limit > 512<<20 {
		t.Fatalf("expected isolated 256..512MiB cgroup, got %d", limit)
	}
	beforeOOM := readR128OOMKills(t)
	pre := readCapacitySample()
	if !pre.available || !pre.linuxMemoryConstrained || pre.linuxMemoryFreeSlots < 96 || pre.linuxMemoryPressure {
		t.Fatalf("need safe >=96 slots under 384MiB cgroup: %+v", pre)
	}
	g := newCapacityGovernor(modeAuto, config.Default())
	defer g.close()
	g.sample(pre, time.Now())
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := newDynamicAdmissionListener(raw, g, 250*time.Millisecond)
	defer ln.Close()
	type pair struct{ server, client net.Conn }
	pairs := make([]pair, 0, 6)
	defer func() {
		for _, p := range pairs {
			_ = p.server.Close()
			_ = p.client.Close()
		}
	}()
	for i := 0; i < 6; i++ {
		client, e := net.DialTimeout("tcp", ln.Addr().String(), time.Second)
		if e != nil {
			t.Fatal(e)
		}
		server, e := ln.Accept()
		if e != nil {
			_ = client.Close()
			t.Fatal(e)
		}
		pairs = append(pairs, pair{server, client})
	}
	g.mu.Lock()
	liveBefore, entries, activeBefore := g.memoryActiveDebt, len(g.leaseReservations), g.active
	g.mu.Unlock()
	if liveBefore != 6 || entries != 6 || activeBefore != 6 {
		t.Fatalf("setup debt=%d map=%d active=%d", liveBefore, entries, activeBefore)
	}
	// Use own test binary as a dedicated ephemeral external workload in same
	// cgroup; monitor it under read-only, no-network Docker without privileges.
	bin, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	child := exec.Command(bin, "-test.run=^TestR146UnrelatedAllocatorChild$", "-test.v")
	child.Env = append(os.Environ(), "PXGO_R146_ALLOCATOR_CHILD=1")
	reader, e := child.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	writer, e := child.StdinPipe()
	if e != nil {
		t.Fatal(e)
	}
	if e = child.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() {
		_ = writer.Close()
		if child.ProcessState == nil {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
	}()
	scan := bufio.NewScanner(reader)
	ready := false
	for i := 0; i < 12 && scan.Scan(); i++ {
		if strings.Contains(scan.Text(), "R146_CHILD_READY") {
			ready = true
			break
		}
	}
	if !ready {
		t.Fatalf("external 24MiB child not ready: %v", scan.Err())
	}
	during := readCapacitySample()
	decline := pre.linuxMemoryFreeSlots - during.linuxMemoryFreeSlots
	if !during.available || !during.linuxMemoryConstrained || decline < 12 {
		t.Fatalf("external cgroup child not observed: before=%d during=%d decline=%d", pre.linuxMemoryFreeSlots, during.linuxMemoryFreeSlots, decline)
	}
	g.sample(during, time.Now())
	g.mu.Lock()
	live, closed, total, active := g.memoryActiveDebt, g.memoryClosedDebt, g.memoryPendingSlots, g.active
	g.mu.Unlock()
	if live != 6 || closed != 0 || total != 6 || active != 6 {
		t.Fatalf("unrelated process wrongly retired active memory debt live=%d closed=%d total=%d active=%d", live, closed, total, active)
	}
	// Real old sockets still carry payload despite the external charge.
	for i, p := range pairs {
		_ = p.server.SetReadDeadline(time.Now().Add(time.Second))
		if _, e = p.client.Write([]byte("live")); e != nil {
			t.Fatalf("client %d: %v", i, e)
		}
		got := make([]byte, 4)
		if _, e = io.ReadFull(p.server, got); e != nil || string(got) != "live" {
			t.Fatalf("held TCP %d broken: %q %v", i, got, e)
		}
	}
	_ = writer.Close()
	if e = child.Wait(); e != nil {
		t.Fatal(e)
	}
	afterOOM := readR128OOMKills(t)
	if afterOOM != beforeOOM {
		t.Fatalf("OOM kill occurred: delta=%d", afterOOM-beforeOOM)
	}
	t.Logf("R146_REAL_EXTERNAL_CGROUP_RESULT memory_max=%d external_alloc=24MiB before_free=%d during_free=%d decline=%d six_real_tcp_alive=true active_debt_preserved=true oom_kill_delta=0", limit, pre.linuxMemoryFreeSlots, during.linuxMemoryFreeSlots, decline)
}
