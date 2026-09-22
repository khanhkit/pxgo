package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/khanhkit/pxgo/internal/config"
	"github.com/khanhkit/pxgo/internal/diagnostic"
)

func configForHTTPServer(t *testing.T, server *httptest.Server) config.Config {
	t.Helper()
	host, portText, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	var port int
	if _, err := fmt.Sscanf(portText, "%d", &port); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Listen = host
	cfg.Port = port
	return cfg
}

func TestTOBSDOC011DebugSinkFailureIsBestEffort(t *testing.T) {
	diagnostic.ResetForTest()
	old := setupDebugFunc
	setupDebugFunc = func(config.Config) error {
		return errors.New("open https://user:pass@example.test/log?token=secret: denied")
	}
	t.Cleanup(func() { setupDebugFunc = old })

	setupDebugBestEffort(config.Default())
	events := diagnostic.Events()
	if len(events) != 1 || events[0].Kind != "diagnostic.log-error" {
		t.Fatalf("events=%+v", events)
	}
	if strings.Contains(events[0].Reason, "pass") || strings.Contains(events[0].Reason, "secret") {
		t.Fatalf("diagnostic event leaked secret: %+v", events[0])
	}
}

func TestTOBSDOC013DoctorReportLiveWorker(t *testing.T) {
	want := diagnostic.Snapshot{Ready: true, Port: 3128}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != diagnostic.DoctorControlPath {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(want)
	}))
	defer server.Close()

	report, err := doctorReport(configForHTTPServer(t, server), "")
	if err != nil {
		t.Fatal(err)
	}
	if !report.Live || report.Snapshot.Port != want.Port {
		t.Fatalf("report=%+v", report)
	}
}

func TestTOBSDOC013DoctorReportRejectsMalformedLiveResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{not-json"))
	}))
	defer server.Close()
	_, err := doctorReport(configForHTTPServer(t, server), "")
	if err == nil {
		t.Fatal("malformed live doctor response accepted")
	}
}

func TestTOBSDOC013DoctorFallsBackToPersistedSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fatal.json")
	diagnostic.BestEffortWriteFatalSnapshot(path, diagnostic.Snapshot{Port: 4444})

	cfg := config.Default()
	cfg.Listen = "127.0.0.1"
	cfg.Port = 1 // deliberately unreachable
	report, err := doctorReport(cfg, path)
	if err != nil {
		t.Fatal(err)
	}
	if report.Live || report.Snapshot.Port != 4444 || report.Error == "" {
		t.Fatalf("fallback report=%+v", report)
	}
}
