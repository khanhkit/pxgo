package proxy

import (
	"net/http"
	"testing"
	"time"
)

func TestR64BareUpgradeWebSocketHeaderCannotRejectShortJSON(t *testing.T) {
	m := newConnectionClassManager()
	m.sampleBudgets(16)
	m.strictLongIntake = true
	now := time.Now()
	for i := 0; i < 12; i++ {
		l := m.open(now)
		if !l.classifyRequest(&http.Request{Method: http.MethodConnect}, now) {
			t.Fatalf("could not occupy 12-long guard at %d", i)
		}
		defer l.finish()
	}
	incoming := m.open(now)
	defer incoming.finish()
	req, _ := http.NewRequest(http.MethodGet, "http://example.invalid/data", nil)
	req.Header.Set("Upgrade", "websocket")
	if !incoming.classifyRequest(req, now) {
		t.Fatal("R64_FALSE_UPGRADE_REJECTION: missing Connection: Upgrade should not classify as WebSocket tunnel")
	}
	req.Header.Set("Connection", "Upgrade")
	if !incoming.classifyRequest(req, now) {
		t.Fatal("valid WebSocket request hint must reach the origin before class denial")
	}
	if incoming.classifyUpgradeResponse(&http.Response{StatusCode: http.StatusSwitchingProtocols}) {
		t.Fatal("actual accepted upstream 101 must be denied when 12-long budget full")
	}
}
