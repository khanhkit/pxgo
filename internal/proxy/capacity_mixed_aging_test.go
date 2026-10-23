package proxy

import (
	"bufio"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
)

// This optional native real-clock check prevents a fake-clock-only promotion
// test from claiming that idle-but-open HTTP/1.1 sockets are promoted by the
// ACTUAL maintenance ticker after 30s without a second HTTP request.
func TestCapacityMixedRealTimeAgedShortToLong(t *testing.T) {
	if os.Getenv("PXGO_CAPACITY_AGING_SOAK") != "1" {
		t.Skip("explicit 30s aging soak")
	}
	if os.Getenv("PXGO_EXPERIMENTAL_CAPACITY_MODE") != "auto" || os.Getenv("PXGO_EXPERIMENTAL_CONNECTION_CLASSES") != "1" {
		t.Fatal("real-time soak requires opt-in AUTO + classifier")
	}
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("mixed-ok")) }))
	defer origin.Close()
	cfg := config.Default()
	cfg.ProxyReload = 0
	cfg.Idle = 55
	cfg.SockTimeout = 12
	px := startTestProxy(t, cfg)
	conn, err := net.DialTimeout("tcp", px.ListenAddr(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err = capacityMixedHTTP(conn, bufio.NewReader(conn), origin.URL, true); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetDeadline(time.Time{})
	initial := px.capacity.classes.snapshot()
	if initial.Short != 1 || initial.Long != 0 || initial.Total != 1 {
		t.Fatalf("unexpected initial short prediction: %+v", initial)
	}
	opened := time.Now()
	deadline := opened.Add(classDurationThreshold + 5*time.Second)
	for time.Now().Before(deadline) {
		s := px.capacity.classes.snapshot()
		if s.Long == 1 && s.Promoted == 1 && s.Short == 0 && s.Total == 1 {
			t.Logf("MIXED_AGE_RESULT promoted_after=%s active=%d current_debt=%d", time.Since(opened), s.Total, s.LongDebt)
			_ = conn.Close()
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("live maintenance failed to promote an aged HTTP keepalive: %+v", px.capacity.classes.snapshot())
}
