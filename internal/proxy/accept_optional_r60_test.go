package proxy

import (
	"net/http"
	"testing"
	"time"
)

func TestR59OptionalSSEAcceptDoesNotPrematurelyRejectJSON(t *testing.T) {
	m := newConnectionClassManager()
	m.sampleBudgets(16)
	m.strictLongIntake = true
	now := time.Now()
	for i := 0; i < 12; i++ {
		l := m.open(now)
		if !l.classifyRequest(&http.Request{Method: http.MethodConnect}, now) {
			t.Fatal("failed to occupy long budget")
		}
		defer l.finish()
	}
	l := m.open(now)
	defer l.finish()
	req, _ := http.NewRequest(http.MethodGet, "http://example.test/json", nil)
	req.Header.Set("Accept", "application/json, text/event-stream;q=0.1")
	if !l.classifyRequest(req, now) {
		t.Fatal("optional Accept SSE hint denied before upstream JSON response")
	}
}
