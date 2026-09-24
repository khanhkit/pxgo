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

func runWithMutedIO(t *testing.T, args ...string) int {
	t.Helper()
	oldArgs, oldStdout, oldStderr := os.Args, os.Stdout, os.Stderr
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		os.Args = oldArgs
		os.Stdout = oldStdout
		os.Stderr = oldStderr
		_ = devNull.Close()
	}()
	t.Setenv("PXGOINT_GUARDIAN_ADDR", "")
	t.Setenv("PXGOINT_GUARDIAN_TOKEN", "")
	os.Args = append([]string{oldArgs[0]}, args...)
	os.Stdout, os.Stderr = devNull, devNull
	return run()
}

func TestRunOneShotDispatchContracts(t *testing.T) {
	if code := runWithMutedIO(t, "--definitely-unknown-option"); code != 2 {
		t.Fatalf("parse-error exit=%d want 2", code)
	}
	if code := runWithMutedIO(t, "--help"); code != 0 {
		t.Fatalf("help exit=%d want 0", code)
	}
	if code := runWithMutedIO(t, "--version"); code != 0 {
		t.Fatalf("version exit=%d want 0", code)
	}

	configPath := filepath.Join(t.TempDir(), "saved.ini")
	if code := runWithMutedIO(t, "--save", "--config="+configPath, "--port=43211"); code != 0 {
		t.Fatalf("save exit=%d want 0", code)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "port = 43211") {
		t.Fatalf("saved config missing requested port:\n%s", data)
	}
}

func TestRunInstallDispatchUsesPreparedStartupCommand(t *testing.T) {
	oldInstall := installStartupFunc
	defer func() { installStartupFunc = oldInstall }()

	calls := 0
	installStartupFunc = func(cmd string, force bool) error {
		calls++
		if !force {
			t.Fatal("install did not propagate --force")
		}
		if !strings.Contains(cmd, "--config") {
			t.Fatalf("startup command missing config argument: %q", cmd)
		}
		return nil
	}
	configPath := filepath.Join(t.TempDir(), "install.ini")
	if code := runWithMutedIO(t, "--install", "--force", "--config="+configPath); code != 0 {
		t.Fatalf("install exit=%d want 0", code)
	}
	if calls != 1 {
		t.Fatalf("install calls=%d want 1", calls)
	}
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("install did not persist startup config: %v", err)
	}
}

func TestRunDoctorQuitAndRestartDispatch(t *testing.T) {
	t.Run("doctor", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != diagnostic.DoctorControlPath {
				t.Errorf("doctor path=%q", r.URL.Path)
			}
			_ = json.NewEncoder(w).Encode(diagnostic.Snapshot{Ready: true})
		}))
		defer server.Close()
		cfg := configForHTTPTestServer(t, server.URL)
		if code := runWithMutedIO(t, "--doctor", "--listen="+cfg.Listen, "--port="+strconv.Itoa(cfg.Port)); code != 0 {
			t.Fatalf("doctor exit=%d want 0", code)
		}
	})

	for _, tc := range []struct {
		name    string
		restart bool
	}{
		{name: "quit"},
		{name: "restart", restart: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldParent := runGuardianParentFunc
			defer func() { runGuardianParentFunc = oldParent }()
			parentCalls := 0
			runGuardianParentFunc = func(config.Config) int {
				parentCalls++
				return 0
			}

			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/PxgoQuit" {
					t.Errorf("quit path=%q", r.URL.Path)
				}
				w.WriteHeader(http.StatusOK)
				go server.Close()
			}))
			cfg := configForHTTPTestServer(t, server.URL)
			args := []string{"--quit", "--listen=" + cfg.Listen, "--port=" + strconv.Itoa(cfg.Port)}
			if tc.restart {
				args[0] = "--restart"
			}
			if code := runWithMutedIO(t, args...); code != 0 {
				server.Close()
				t.Fatalf("%s exit=%d want 0", tc.name, code)
			}
			if tc.restart && parentCalls != 1 {
				t.Fatalf("restart parent calls=%d want 1", parentCalls)
			}
			if !tc.restart && parentCalls != 0 {
				t.Fatalf("quit parent calls=%d want 0", parentCalls)
			}
		})
	}
}

func TestDoSelfTestRequestAuthRetryReplaysBody(t *testing.T) {
	nilResp, err := doSelfTestRequest(http.DefaultClient, nil, config.Default(), false)
	if nilResp != nil {
		_ = nilResp.Body.Close()
	}
	if err == nil {
		t.Fatal("nil self-test request unexpectedly succeeded")
	}

	var attempts int
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		if !strings.HasPrefix(r.Header.Get("Proxy-Authorization"), "Basic ") {
			w.Header().Set("Proxy-Authenticate", `Basic realm="self-test"`)
			w.WriteHeader(http.StatusProxyAuthRequired)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := config.Default()
	cfg.Auth = "BASIC"
	cfg.Username = "self-test-user"
	cfg.Password = "self-test-secret"
	req, err := http.NewRequest(http.MethodPost, server.URL+"/resource", strings.NewReader("replay-body"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := doSelfTestRequest(server.Client(), req, cfg, true)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%s want 200", resp.Status)
	}
	if attempts != 2 {
		t.Fatalf("attempts=%d want 2", attempts)
	}
	if len(bodies) != 2 || bodies[0] != "replay-body" || bodies[1] != "replay-body" {
		t.Fatalf("request body was not replayed: %#v", bodies)
	}
}

func TestDoSelfTestRequestStopsAfterBoundedAuthRetries(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		w.Header().Set("Proxy-Authenticate", `Basic realm="self-test"`)
		w.WriteHeader(http.StatusProxyAuthRequired)
	}))
	defer server.Close()

	cfg := config.Default()
	cfg.Auth = "BASIC"
	cfg.Username = "bounded-user"
	cfg.Password = "bounded-secret"
	req, err := http.NewRequest(http.MethodGet, server.URL+"/bounded", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := doSelfTestRequest(server.Client(), req, cfg, true)
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil {
		t.Fatal("bounded retry returned nil response")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusProxyAuthRequired {
		t.Fatalf("status=%s want 407", resp.Status)
	}
	if attempts != 3 {
		t.Fatalf("attempts=%d want 3", attempts)
	}
}
