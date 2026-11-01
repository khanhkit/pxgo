//go:build windows

package proxy

import (
	"net"
	"strconv"
	"strings"
	"testing"
)

func TestR133WindowsBacklogRequiresSeparateNonLoopbackOptIn(t *testing.T) {
	t.Setenv("PXGO_EXPERIMENTAL_WINDOWS_TCP_BACKLOG", "4096")
	t.Setenv("PXGO_EXPERIMENTAL_WINDOWS_TCP_BACKLOG_ALLOW_NON_LOOPBACK", "")
	// This is an ephemeral raw socket, no accept loop or remote serving.
	for _, address := range []string{"0.0.0.0:0", "192.0.2.1:0", "255.255.255.255:0", "[::ffff:0.0.0.0]:0", "[::ffff:192.0.2.1]:0"} {
		ln, err := listenProxyTCP(address)
		if err == nil {
			_ = ln.Close()
			t.Fatalf("P1_NON_LOOPBACK_EXPOSED_WITHOUT_SECOND_OPTIN: %s", address)
		}
		if !strings.Contains(err.Error(), "ALLOW_NON_LOOPBACK=1") {
			t.Fatalf("non-loopback denial missing actionable guidance for %s: %v", address, err)
		}
	}
	ln, err := listenProxyTCP("127.0.0.1:0")
	if err != nil {
		t.Fatalf("ordinary loopback experimental backlog rejected: %v", err)
	}
	_ = ln.Close()
	t.Setenv("PXGO_EXPERIMENTAL_WINDOWS_TCP_BACKLOG_ALLOW_NON_LOOPBACK", "true")
	ln, err = listenProxyTCP("127.0.0.1:0")
	if err == nil {
		_ = ln.Close()
		t.Fatal("ambiguous non-loopback approval accepted despite malformed env")
	}
	// The second flag alone must never activate experimental backlog.
	t.Setenv("PXGO_EXPERIMENTAL_WINDOWS_TCP_BACKLOG", "")
	ln, err = listenProxyTCP("localhost:0")
	if err != nil {
		t.Fatalf("regular net.Listen unexpectedly changed by standalone override: %v", err)
	}
	_ = ln.Close()
	t.Log("R133_WINDOWS_GUARD_NEGATIVE denied_nonloopback_without_second_optin=true loopback_allowed=true invalid_approval_rejected=true standalone_approval_does_not_activate=true")
}

func TestR133WindowsBacklogExplicitNonLoopbackOptIn(t *testing.T) {
	t.Setenv("PXGO_EXPERIMENTAL_WINDOWS_TCP_BACKLOG", "4096")
	t.Setenv("PXGO_EXPERIMENTAL_WINDOWS_TCP_BACKLOG_ALLOW_NON_LOOPBACK", "1")
	ln, err := listenProxyTCP("0.0.0.0:0")
	if err != nil {
		t.Fatalf("explicit opt-in requested but socket could not open: %v", err)
	}
	defer ln.Close()
	if !ln.Addr().(*net.TCPAddr).IP.IsUnspecified() {
		t.Fatalf("explicit opt-in wrong bind: %s", ln.Addr())
	}
	client, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)))
	if err != nil {
		t.Fatalf("explicit opt-in loopback connectivity: %v", err)
	}
	_ = client.Close()
	t.Log("R133_WINDOWS_GUARD_EXPLICIT_ALLOW approved_nonloopback=true tcp_loopback_works=true")
}
