//go:build windows

package proxy

import (
	"net"
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
)

// Research-only NEGATIVE property: a large kernel backlog can complete
// unauthenticated TCP handshakes *before* the shared AUTO physical permit is
// acquired. This does not prove HTTP proxy auth or application admission.
// It documents a DoS/queue resource tradeoff for independent security review.
func TestR131WindowsKernelPreauthQueueExceedsGovernor(t *testing.T) {
	if testing.Short() {
		t.Skip("research-only native WinSock backlog pressure test")
	}
	t.Setenv("PXGO_EXPERIMENTAL_WINDOWS_TCP_BACKLOG", "4096")
	t.Setenv("PXGO_EXPERIMENTAL_CAPACITY_MODE", "auto")
	cfg := config.Default()
	cfg.MaxConnections = 16
	cfg.ProxyReload = 0
	cfg.SockTimeout = 8
	px := startTestProxy(t, cfg)

	const admitted = 16
	const queuedClients = 128
	clients := make([]net.Conn, 0, admitted+queuedClients)
	defer func() {
		for _, c := range clients {
			_ = c.Close()
		}
	}()

	open := func() net.Conn {
		t.Helper()
		c, err := net.DialTimeout("tcp", px.ListenAddr(), 400*time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	for i := 0; i < admitted; i++ {
		clients = append(clients, open()) // deliberately no HTTP headers
	}
	until := time.Now().Add(2 * time.Second)
	for time.Now().Before(until) {
		if px.capacity.snapshot().ActiveDownstream == admitted {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := px.capacity.snapshot(); got.ActiveDownstream != admitted {
		t.Fatalf("could not establish physical admission ceiling %d: %+v", admitted, got)
	}
	// These handshakes are completed by Windows even though all application
	// permits are occupied by unclassified clients and none is authenticated.
	for i := 0; i < queuedClients; i++ {
		clients = append(clients, open())
	}
	got := px.capacity.snapshot()
	if got.ActiveDownstream != admitted || len(clients) != admitted+queuedClients {
		t.Fatalf("application capacity accounting unexpectedly includes queued TCP: %+v clients=%d", got, len(clients))
	}
	t.Logf("R131_WINDOWS_PREAUTH_QUEUE_RESULT tcp_connected=%d active_governor=%d excess_pre_auth_tcp=%d upstream_admission_waits=%d default_flag_off=false backlog=4096", len(clients), got.ActiveDownstream, queuedClients, got.AdmissionWaits)
}
