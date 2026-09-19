package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pavelsimo/pxgo/internal/config"
	"github.com/pavelsimo/pxgo/internal/diagnostic"
)

func TestTOBSDOC012DoctorControlRequestShape(t *testing.T) {
	origin := httptest.NewRequest(http.MethodGet, doctorControlPath, nil)
	origin.RequestURI = doctorControlPath
	if !isDoctorControlRequest(origin) {
		t.Fatal("loopback-style origin-form doctor request not recognized")
	}
	absolute := httptest.NewRequest(http.MethodGet, "http://example.com"+doctorControlPath, nil)
	absolute.RequestURI = "http://example.com" + doctorControlPath
	if isDoctorControlRequest(absolute) {
		t.Fatal("absolute-form proxy URL was consumed as local doctor control")
	}
	post := httptest.NewRequest(http.MethodPost, doctorControlPath, nil)
	post.RequestURI = doctorControlPath
	if isDoctorControlRequest(post) {
		t.Fatal("non-GET doctor request recognized")
	}
}

func TestTOBSDOC012DoctorIsLoopbackOnlyAndReadOnly(t *testing.T) {
	diagnostic.ResetForTest()
	cfg := config.Default()
	cfg.Server = "DIRECT"
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	remote := httptest.NewRequest(http.MethodGet, doctorControlPath, nil)
	remote.RequestURI = doctorControlPath
	remote.RemoteAddr = "198.51.100.8:12345"
	remoteRW := httptest.NewRecorder()
	s.ServeHTTP(remoteRW, remote)
	if remoteRW.Code != http.StatusForbidden {
		t.Fatalf("remote doctor status = %d, want 403", remoteRW.Code)
	}

	local := httptest.NewRequest(http.MethodGet, doctorControlPath, nil)
	local.RequestURI = doctorControlPath
	local.RemoteAddr = "127.0.0.1:12345"
	localRW := httptest.NewRecorder()
	s.ServeHTTP(localRW, local)
	if localRW.Code != http.StatusOK {
		t.Fatalf("local doctor status = %d body=%q", localRW.Code, localRW.Body.String())
	}
	var snapshot diagnostic.Snapshot
	if err := json.Unmarshal(localRW.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("doctor JSON: %v", err)
	}
	if snapshot.Runtime.ProgressSequence != s.RuntimeStatus().ProgressSequence {
		t.Fatalf("runtime snapshot mismatch: %+v", snapshot.Runtime)
	}
	select {
	case <-s.closed:
		t.Fatal("doctor request mutated server lifecycle")
	default:
	}
}

func TestTOBSDOC014SnapshotConsumesOwnerState(t *testing.T) {
	diagnostic.ResetForTest()
	cfg := config.Default()
	cfg.Server = "DIRECT"
	cfg.Kerberos = false
	cfg.Sources["server"] = "env:PXGO_SERVER"
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	diagnostic.Record("runtime", "route refresh")
	s.sup.Tick(time.Now())

	snapshot := s.DiagnosticSnapshot()
	if snapshot.Route.Source == "" {
		t.Fatal("route source missing")
	}
	if snapshot.Route.Mode < 0 {
		t.Fatalf("invalid route mode %d", snapshot.Route.Mode)
	}
	if snapshot.Auth.UpstreamMode != cfg.Auth || snapshot.Auth.KerberosEnabled {
		t.Fatalf("auth snapshot = %+v", snapshot.Auth)
	}
	if snapshot.Runtime.ProgressSequence == 0 {
		t.Fatalf("runtime progress missing: %+v", snapshot.Runtime)
	}
	if snapshot.Process.Goroutines <= 0 {
		t.Fatalf("goroutines = %d", snapshot.Process.Goroutines)
	}
	if snapshot.Process.CPUSeconds < 0 || snapshot.Process.RSSBytes == 0 {
		t.Fatalf("process metrics = %+v", snapshot.Process)
	}
	if snapshot.ConfigSources["server"] != "env" {
		t.Fatalf("safe config source = %q, want env", snapshot.ConfigSources["server"])
	}
	if len(snapshot.Events) != 1 || !strings.Contains(snapshot.Events[0].Reason, "route refresh") {
		t.Fatalf("events = %+v", snapshot.Events)
	}
}
