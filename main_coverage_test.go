package main

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
	"github.com/khanhkit/pxgo/internal/diagnostic"
)

func TestSelfTestURLModesAndAllMode(t *testing.T) {
	tests := []struct {
		input string
		want  []string
		all   bool
	}{
		{input: selfTestAll, want: []string{selfTestHTTPURL, selfTestHTTPSURL}, all: true},
		{input: "1", want: []string{selfTestHTTPURL, selfTestHTTPSURL}, all: true},
		{input: "all:example.com", want: []string{"http://example.com", "https://example.com"}, all: true},
		{input: "all:https://example.com", want: []string{"https://example.com"}, all: true},
		{input: "https://example.com", want: nil, all: false},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := selfTestURLs(tc.input)
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("selfTestURLs(%q)=%v want %v", tc.input, got, tc.want)
			}
			if gotAll := selfTestAllMode(tc.input); gotAll != tc.all {
				t.Fatalf("selfTestAllMode(%q)=%v want %v", tc.input, gotAll, tc.all)
			}
		})
	}
}

func TestWaitForRunningProxyAndClosed(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()

	if err := waitForRunningProxy(addr); err != nil {
		_ = ln.Close()
		t.Fatalf("running listener not detected: %v", err)
	}
	if waitForClosed(addr, 30*time.Millisecond) {
		_ = ln.Close()
		t.Fatal("open listener reported closed")
	}
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	if !waitForClosed(addr, time.Second) {
		t.Fatal("closed listener remained reachable")
	}
	if err := waitForRunningProxy(addr); err == nil {
		t.Fatal("closed listener unexpectedly reported running")
	}
}

func TestPrintHelpAndDiagnosticSnapshotPath(t *testing.T) {
	oldStdout := os.Stdout
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writeEnd
	printHelp()
	if err := writeEnd.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdout = oldStdout
	t.Cleanup(func() { os.Stdout = oldStdout })

	data, err := io.ReadAll(readEnd)
	if err != nil {
		t.Fatal(err)
	}
	_ = readEnd.Close()
	if text := string(data); !strings.Contains(text, "Kerberos/SPNEGO") || !strings.Contains(text, "--doctor") {
		t.Fatalf("unexpected help output: %s", text)
	}

	path := diagnosticSnapshotPath()
	if path != "" && filepath.Base(path) != "diagnostic-snapshot.json" {
		t.Fatalf("diagnostic snapshot path=%q", path)
	}
}

func configForHTTPTestServer(t *testing.T, serverURL string) config.Config {
	t.Helper()
	u, err := url.Parse(serverURL)
	if err != nil {
		t.Fatal(err)
	}
	host, portText, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Listen = host
	cfg.Port = port
	return cfg
}

func TestDoctorReportLiveAndSnapshotFallback(t *testing.T) {
	t.Run("live", func(t *testing.T) {
		want := diagnostic.Snapshot{Ready: true, Listen: "127.0.0.1", Port: 4242}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != diagnostic.DoctorControlPath {
				t.Errorf("path=%q want %q", r.URL.Path, diagnostic.DoctorControlPath)
			}
			_ = json.NewEncoder(w).Encode(want)
		}))
		defer server.Close()

		report, err := doctorReport(configForHTTPTestServer(t, server.URL), "")
		if err != nil {
			t.Fatal(err)
		}
		if !report.Live || !report.Snapshot.Ready || report.Snapshot.Port != want.Port {
			t.Fatalf("unexpected live doctor report: %#v", report)
		}
	})

	t.Run("snapshot fallback", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		if err := ln.Close(); err != nil {
			t.Fatal(err)
		}
		host, portText, err := net.SplitHostPort(addr)
		if err != nil {
			t.Fatal(err)
		}
		port, err := strconv.Atoi(portText)
		if err != nil {
			t.Fatal(err)
		}
		cfg := config.Default()
		cfg.Listen = host
		cfg.Port = port
		path := filepath.Join(t.TempDir(), "diagnostic-snapshot.json")
		want := diagnostic.Snapshot{Ready: true, Port: 5151}
		diagnostic.BestEffortWriteFatalSnapshot(path, want)

		report, err := doctorReport(cfg, path)
		if err != nil {
			t.Fatal(err)
		}
		if report.Live || report.Error == "" || report.Snapshot.Port != want.Port {
			t.Fatalf("unexpected fallback doctor report: %#v", report)
		}
	})
}

func TestDoctorReportRejectsMalformedLivePayloads(t *testing.T) {
	for name, payload := range map[string]string{
		"malformed": `{`,
		"multiple":  `{}` + `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, payload)
			}))
			defer server.Close()
			_, err := doctorReport(configForHTTPTestServer(t, server.URL), "")
			if err == nil || !strings.Contains(err.Error(), "doctor failed") {
				t.Fatalf("err=%v want doctor failure", err)
			}
		})
	}
}

func TestDoctorWritesJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(diagnostic.Snapshot{Ready: true, Port: 6262})
	}))
	defer server.Close()

	oldStdout := os.Stdout
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writeEnd
	t.Cleanup(func() { os.Stdout = oldStdout })
	if err := doctor(configForHTTPTestServer(t, server.URL)); err != nil {
		t.Fatal(err)
	}
	if err := writeEnd.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdout = oldStdout
	data, err := io.ReadAll(readEnd)
	if err != nil {
		t.Fatal(err)
	}
	_ = readEnd.Close()
	var report diagnostic.DoctorReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("doctor output is not JSON: %v\n%s", err, data)
	}
	if !report.Live || report.Snapshot.Port != 6262 {
		t.Fatalf("unexpected doctor output: %#v", report)
	}
}

func TestQuitHTTPStatusContracts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		want   string
	}{
		{name: "forbidden", status: http.StatusForbidden, want: "cannot quit"},
		{name: "bad status", status: http.StatusBadGateway, want: "502 Bad Gateway"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/PxgoQuit" {
					t.Errorf("path=%q want /PxgoQuit", r.URL.Path)
				}
				w.WriteHeader(tc.status)
			}))
			defer server.Close()
			err := quit(configForHTTPTestServer(t, server.URL))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("quit err=%v want substring %q", err, tc.want)
			}
		})
	}
}

func TestQuitSucceedsWhenListenerCloses(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		go server.Close()
	}))
	cfg := configForHTTPTestServer(t, server.URL)
	if err := quit(cfg); err != nil {
		server.Close()
		t.Fatal(err)
	}
}
