//go:build linux

package proxy

import (
	"net"
	"os"
	"testing"
	"time"
)

type r148MemSnap struct{ sock, current, oom uint64 }

func r148Snap(t *testing.T) r148MemSnap {
	t.Helper()
	s, c, k := r147MemcgSnapshot(t)
	return r148MemSnap{s, c, k}
}

// Do NOT mistake a successful observation for an OS guarantee:
// memory.current/memory.stat sock are aggregate snapshots, not a per-socket
// charge completion fence. This bounded fixture records normal TCP close,
// half-close and RST close against an actual isolated 384MiB cgroup.
func TestR148NativeKernelPostCloseSocketChargeObservations(t *testing.T) {
	if os.Getenv("PXGO_R148_REAL_CGROUP") != "1" {
		t.Skip("finite 384MiB Docker-only research")
	}
	if readR128Metric(t, "memory.max") != 384<<20 {
		t.Fatal("need exact 384MiB limited Docker")
	}
	const pairs = 8
	beforeOOM := readR128OOMKills(t)
	for _, mode := range []string{"graceful", "rst", "halfclose"} {
		baseline := r148Snap(t)
		listener, e := net.ListenTCP("tcp", &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
		if e != nil {
			t.Fatal(e)
		}
		clients := make([]*net.TCPConn, 0, pairs)
		servers := make([]*net.TCPConn, 0, pairs)
		var total int
		for i := 0; i < pairs; i++ {
			c, e := net.DialTCP("tcp", nil, listener.Addr().(*net.TCPAddr))
			if e != nil {
				t.Fatal(e)
			}
			_ = c.SetWriteBuffer(2 << 20)
			_ = c.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
			s, e := listener.AcceptTCP()
			if e != nil {
				t.Fatal(e)
			}
			_ = s.SetReadBuffer(2 << 20)
			if mode == "rst" {
				_ = c.SetLinger(0)
				_ = s.SetLinger(0)
			}
			n, _ := c.Write(make([]byte, 768<<10))
			total += n
			clients = append(clients, c)
			servers = append(servers, s)
		}
		active := r148Snap(t)
		if active.sock <= baseline.sock {
			t.Fatalf("kernel socket memory not charged in %s", mode)
		}
		if mode == "halfclose" {
			for _, c := range clients {
				_ = c.CloseWrite()
			}
		} else {
			// Release first endpoint only (different from full tunnel completion).
			for _, s := range servers {
				_ = s.Close()
			}
		}
		afterFirst := r148Snap(t)
		for _, c := range clients {
			_ = c.Close()
		}
		for _, s := range servers {
			_ = s.Close()
		}
		_ = listener.Close()
		afterBoth := r148Snap(t)
		time.Sleep(20 * time.Millisecond)
		after20 := r148Snap(t)
		time.Sleep(150 * time.Millisecond)
		after170 := r148Snap(t)
		if after170.oom != beforeOOM {
			t.Fatalf("OOM kill detected in %s", mode)
		}
		t.Logf("R148_REAL_CLOSE_OBSERVATION mode=%s pairs=%d buffered_bytes=%d baseline_sock=%d peak_sock=%d first_endpoint_sock=%d both_closed_sock=%d after20ms_sock=%d after170ms_sock=%d baseline_current=%d peak_current=%d after170ms_current=%d oom_kill_delta=0",
			mode, pairs, total, baseline.sock, active.sock, afterFirst.sock, afterBoth.sock, after20.sock, after170.sock, baseline.current, active.current, after170.current)
	}
}
