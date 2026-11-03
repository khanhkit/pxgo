//go:build linux

package proxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
)

func r147MemcgSnapshot(t *testing.T) (uint64, uint64, uint64) {
	t.Helper()
	raw, e := os.ReadFile("/sys/fs/cgroup/memory.stat")
	if e != nil {
		t.Fatal(e)
	}
	var sock uint64
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[0] == "sock" {
			sock, _ = strconv.ParseUint(f[1], 10, 64)
		}
	}
	curr := readR128Metric(t, "memory.current")
	kills := readR128OOMKills(t)
	return sock, curr, kills
}

func r147TunnelCount(t *testing.T) int {
	t.Helper()
	raw := os.Getenv("PXGO_R147_TUNNELS")
	if raw == "" {
		return 4
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > 16 {
		t.Fatalf("research tunnel count must be 1..16, got %q", raw)
	}
	return n
}

// Benchmark-only socket buffer enforcement. Does NOT bound all kernel memory
// or Go heap. Track exact TCPConn pointers to avoid repeated socket syscalls.
func r148CapOwnedTCPBuffers(s *Server, n int, seen map[*net.TCPConn]struct{}) (int, error) {
	if n <= 0 {
		return 0, nil
	}
	s.tunnelMu.Lock()
	tunnels := make([]*managedTunnel, 0, len(s.tunnels))
	for tunnel := range s.tunnels {
		tunnels = append(tunnels, tunnel)
	}
	s.tunnelMu.Unlock()
	for _, t := range tunnels {
		upstream, ok := t.upstream.(net.Conn)
		if !ok {
			continue
		}
		for _, raw := range []net.Conn{t.client, upstream} {
			if c, ok := raw.(*admissionConn); ok {
				raw = c.Conn
			}
			c, ok := raw.(*net.TCPConn)
			if !ok {
				continue
			}
			if _, ok := seen[c]; ok {
				continue
			}
			if err := c.SetReadBuffer(n); err != nil {
				return len(seen), err
			}
			if err := c.SetWriteBuffer(n); err != nil {
				return len(seen), err
			}
			seen[c] = struct{}{}
		}
	}
	return len(seen), nil
}

func TestR147ProxyOnlyCgroupMemoryObservedWithIsolatedExternalPeers(t *testing.T) {
	if os.Getenv("PXGO_R147_ROLE") != "proxy" {
		t.Skip("requires isolated 384MiB PxGo-only memcg")
	}
	limit := readR128Metric(t, "memory.max")
	if limit != 384<<20 {
		t.Fatalf("requires exact 384MiB proxy-only memcg; got %d", limit)
	}
	t.Setenv("PXGO_EXPERIMENTAL_CAPACITY_MODE", "legacy") // avoid P1 AUTO while measuring
	cfg := config.Default()
	cfg.Gateway = true
	cfg.Allow = "172.16.0.0/12"
	cfg.Listen = net.IPv4zero.String() // avoid literal repetition; binds only inside isolated Docker network
	cfg.Port = 19876
	cfg.Server = "DIRECT"
	cfg.ProxyReload = 0
	cfg.Idle = 10
	cfg.SockTimeout = 5
	s, e := New(cfg)
	if e != nil {
		t.Fatal(e)
	}
	errc := make(chan error, 1)
	go func() { errc <- s.Start() }()
	deadline := time.Now().Add(8 * time.Second)
	for s.Port() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if s.Port() != 19876 {
		t.Fatalf("PxGo listener unexpected port %d", s.Port())
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if e := s.Shutdown(ctx); e != nil {
			t.Errorf("PxGo shutdown: %v", e)
		}
		select {
		case e := <-errc:
			if e != nil {
				t.Logf("server return: %v", e)
			}
		case <-time.After(4 * time.Second):
			t.Error("PxGo server did not drain")
		}
	}()
	tunnels := r147TunnelCount(t)
	beforeSock, beforeCurr, beforeKills := r147MemcgSnapshot(t)
	peakSock, peakCurr := beforeSock, beforeCurr
	var maxOwnedSockets int
	var maxOwnedQueued uint64
	var maxOwnedForward uint64
	var maxOwnedBufferLimits uint64
	var sockMetricErrors int
	var samples int
	capBytes := 0
	if raw := os.Getenv("PXGO_R148_SOCKET_CAP_BYTES"); raw != "" {
		var err error
		capBytes, err = strconv.Atoi(raw)
		if err != nil || capBytes < 4096 || capBytes > 1<<20 {
			t.Fatalf("invalid test-only socket buffer cap %q", raw)
		}
	}
	seen := make(map[*net.TCPConn]struct{})
	applied := 0
	started := time.Now()
	for time.Since(started) < 18*time.Second {
		sock, curr, _ := r147MemcgSnapshot(t)
		if sock > peakSock {
			peakSock = sock
		}
		if curr > peakCurr {
			peakCurr = curr
		}
		samples++
		if capBytes != 0 {
			n, err := r148CapOwnedTCPBuffers(s, capBytes, seen)
			if err != nil {
				t.Fatalf("test-only TCP buffer setting: %v", err)
			}
			applied = n
		}
		count, queued, forward, limits, metricErr := r147OwnedTunnelSocketMetrics(s)
		if metricErr == nil {
			if count > maxOwnedSockets {
				maxOwnedSockets = count
			}
			if queued > maxOwnedQueued {
				maxOwnedQueued = queued
			}
			if forward > maxOwnedForward {
				maxOwnedForward = forward
			}
			if limits > maxOwnedBufferLimits {
				maxOwnedBufferLimits = limits
			}
		} else {
			sockMetricErrors++
		}
		time.Sleep(20 * time.Millisecond)
	}
	endSock, endCurr, endKills := r147MemcgSnapshot(t)
	if endKills != beforeKills {
		t.Fatalf("proxy-only memcg OOM kills delta=%d", endKills-beforeKills)
	}
	t.Logf("R147_PROXY_ONLY_MEMCG_RESULT tunnels=%d memory_max=%d before_sock=%d peak_sock=%d after_sock=%d isolated_sock_peak_delta=%d before_current=%d peak_current=%d after_current=%d isolated_current_peak_delta=%d samples=%d test_socket_cap_bytes=%d cap_applied_sockets=%d owned_peak_sockets=%d owned_queued_peak_bytes=%d owned_forward_peak_bytes=%d owned_buffer_limit_peak_bytes=%d so_meminfo_errors=%d oom_kill_delta=0 peer_cgroup_separate=true",
		tunnels, limit, beforeSock, peakSock, endSock, int64(peakSock)-int64(beforeSock), beforeCurr, peakCurr, endCurr, int64(peakCurr)-int64(beforeCurr), samples, capBytes, applied, maxOwnedSockets, maxOwnedQueued, maxOwnedForward, maxOwnedBufferLimits, sockMetricErrors)
}

// Entire peer workload runs in its OWN separately limited Docker cgroup.
// The sidecar is client and upstream, and does not belong to PxGo's 384MiB
// container. Traffic stays on a private Docker --internal network.
func TestR147PeerClientAndUpstreamInDifferentMemcg(t *testing.T) {
	if os.Getenv("PXGO_R147_ROLE") != "peer" {
		t.Skip("requires isolated peer Docker cgroup")
	}
	upstream, e := net.Listen("tcp", "0.0.0.0:19877")
	if e != nil {
		t.Fatal(e)
	}
	defer upstream.Close()
	tunnels := r147TunnelCount(t)
	var wg sync.WaitGroup
	var upstreamReceived atomic.Int64
	drain := os.Getenv("PXGO_R148_PEER_DRAIN") == "1"
	slowMS := 0
	if raw := os.Getenv("PXGO_R148_SLOW_READ_MS"); raw != "" {
		n, e := strconv.Atoi(raw)
		if e != nil || n < 1 || n > 100 {
			t.Fatalf("invalid slow reader delay %q", raw)
		}
		slowMS = n
	}
	hold := make(chan struct{})
	go func() {
		for i := 0; i < tunnels; i++ {
			c, err := upstream.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func(c net.Conn) {
				defer wg.Done()
				defer c.Close()
				if drain {
					if slowMS > 0 {
						buf := make([]byte, 16<<10)
						for {
							n, e := c.Read(buf)
							upstreamReceived.Add(int64(n))
							if e != nil {
								break
							}
							time.Sleep(time.Duration(slowMS) * time.Millisecond)
						}
					} else {
						n, _ := io.Copy(io.Discard, c)
						upstreamReceived.Add(n)
					}
				} else {
					<-hold
				}
			}(c)
		}
	}()
	// Wait for PxGo container to bind its listener.
	var probe net.Conn
	for start := time.Now(); time.Since(start) < 30*time.Second; time.Sleep(100 * time.Millisecond) {
		probe, e = net.DialTimeout("tcp", "pxgo-r147-proxy:19876", 300*time.Millisecond)
		if e == nil {
			probe.Close()
			break
		}
	}
	if e != nil {
		t.Fatalf("proxy listener unreachable from peer sidecar: %v", e)
	}
	clients := make([]net.Conn, 0, tunnels)
	sent := make([]int, 0, tunnels)
	for i := 0; i < tunnels; i++ {
		c, err := net.DialTimeout("tcp", "pxgo-r147-proxy:19876", 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		clients = append(clients, c)
		_ = c.SetDeadline(time.Now().Add(20 * time.Second))
		_, err = fmt.Fprintf(c, "CONNECT peer-r147:19877 HTTP/1.1\r\nHost: peer-r147:19877\r\n\r\n")
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: http.MethodConnect})
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 200 {
			t.Fatalf("real PxGo CONNECT i=%d status=%d", i, resp.StatusCode)
		}
		_ = resp.Body.Close()
	}
	if os.Getenv("PXGO_R148_PEER_WAIT_CAP") == "1" {
		time.Sleep(250 * time.Millisecond)
	}
	payload := 2 << 20
	if drain {
		payload = 4 << 20 // 4MiB per tunnel, 32MiB delivered expected
	}
	sent = make([]int, len(clients))
	sendStarted := time.Now()
	var writers sync.WaitGroup
	for i, c := range clients {
		writers.Add(1)
		go func(i int, c net.Conn) {
			defer writers.Done()
			_ = c.SetWriteDeadline(time.Now().Add(12 * time.Second))
			if drain {
				data := make([]byte, 64<<10)
				for sent[i] < payload {
					want := min(len(data), payload-sent[i])
					n, err := c.Write(data[:want])
					sent[i] += n
					if err != nil {
						t.Logf("end-to-end writer %d partial sent=%d err=%v", i, sent[i], err)
						break
					}
				}
			} else {
				n, err := c.Write(make([]byte, payload))
				sent[i] = n
				if err != nil && !strings.Contains(err.Error(), "timeout") {
					t.Logf("client %d partial buffered send err=%v", i, err)
				}
			}
		}(i, c)
	}
	writers.Wait()
	sendElapsed := time.Since(sendStarted)
	if !drain {
		// Keep live upstream sockets and client buffers in their PEER cgroup
		// while PxGo's own cgroup records only the proxy's two tunnel ends.
		time.Sleep(4 * time.Second)
	}
	for _, c := range clients {
		_ = c.Close()
	}
	close(hold)
	wg.Wait()
	if drain {
		want := int64(tunnels * payload)
		received := upstreamReceived.Load()
		if received != want {
			t.Fatalf("upstream confirmed %d of %d required delivered bytes", received, want)
		}
		t.Logf("R148_DRAIN_END_TO_END_RESULT tunnels=%d delivered_bytes=%d send_elapsed_ms=%d all_payload_received=true slow_read_ms=%d", tunnels, received, sendElapsed.Milliseconds(), slowMS)
		if slowMS > 0 {
			t.Logf("R148_SLOW_CONSUMER_RESULT tunnels=%d bytes=%d send_elapsed_ms=%d slow_read_ms=%d complete_delivery=true", tunnels, received, sendElapsed.Milliseconds(), slowMS)
		}
	}
	t.Logf("R147_SIDE_CAR_CONNECT_RESULT tunneled=%d sent_bytes=%v total_sent_bytes=%d peer_memcg_separate=true", tunnels, sent, func() int {
		n := 0
		for _, x := range sent {
			n += x
		}
		return n
	}())
}
