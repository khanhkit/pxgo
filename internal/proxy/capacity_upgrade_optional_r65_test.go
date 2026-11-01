package proxy

import (
	"net/http"
	"testing"
	"time"
)

// A valid WebSocket request header is an intention, not proof that the origin
// will accept switching protocols; a 200 JSON response remains short.
func TestR65ValidWebSocketRequestHintCannotRejectJSONBeforeOrigin(t *testing.T) {
	m := newConnectionClassManager()
	m.sampleBudgets(16)
	m.strictLongIntake = true
	now := time.Now()
	for i := 0; i < 12; i++ {
		l := m.open(now)
		if !l.classifyRequest(&http.Request{Method: http.MethodConnect}, now) {
			t.Fatal("failed long plateau")
		}
		defer l.finish()
	}
	incoming := m.open(now)
	defer incoming.finish()
	req, _ := http.NewRequest(http.MethodGet, "http://example.invalid/json", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	if !incoming.classifyRequest(req, now) {
		t.Fatal("R65_FALSE_WEBSOCKET_INTENT_DENIAL: valid Upgrade intent is not a real upstream 101 switch")
	}
}
