package main

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/khanhkit/pxgo/internal/guardian"
	pxupdate "github.com/khanhkit/pxgo/internal/update"
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

type fakeUpdateService struct {
	checkStatus  pxupdate.Status
	updateStatus pxupdate.Status
	checkErr     error
	updateErr    error
	checkCalls   int
	updateCalls  int
	prepareHook  func()
	applyHook    func()
}

func (f *fakeUpdateService) Check(_ context.Context, _ string) (pxupdate.Status, error) {
	f.checkCalls++
	return f.checkStatus, f.checkErr
}

func (f *fakeUpdateService) Prepare(_ context.Context, _ string) (pxupdate.PreparedUpdate, error) {
	f.checkCalls++
	if f.prepareHook != nil {
		f.prepareHook()
	}
	return pxupdate.PreparedUpdate{Status: f.checkStatus}, f.checkErr
}

func (f *fakeUpdateService) ApplyPreparedWithRestart(_ context.Context, prepared *pxupdate.PreparedUpdate, _ []string) (pxupdate.Status, error) {
	f.updateCalls++
	if f.applyHook != nil {
		f.applyHook()
	}
	if prepared != nil && f.updateStatus.Current == "" {
		return prepared.Status, f.updateErr
	}
	return f.updateStatus, f.updateErr
}

func (f *fakeUpdateService) Update(_ context.Context, _ string) (pxupdate.Status, error) {
	f.updateCalls++
	return f.updateStatus, f.updateErr
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

func TestRunUpdateDispatchUsesProviderAwareService(t *testing.T) {
	oldFactory := newUpdateServiceFunc
	oldVersion := version
	defer func() {
		newUpdateServiceFunc = oldFactory
		version = oldVersion
	}()
	version = "1.0.0"
	fake := &fakeUpdateService{
		checkStatus:  pxupdate.Status{Current: "1.0.0", Latest: "1.1.0", Available: true, Provider: pxupdate.ProviderScoop, Channel: pxupdate.Stable},
		updateStatus: pxupdate.Status{Current: "1.0.0", Latest: "1.1.0", Available: true, Provider: pxupdate.ProviderScoop, Channel: pxupdate.Stable, Applied: true},
	}
	newUpdateServiceFunc = func(cfg config.Config) (updateService, error) {
		if cfg.InstallProvider != "scoop" || cfg.UpdateChannel != "stable" {
			t.Fatalf("unexpected update config provider=%q channel=%q", cfg.InstallProvider, cfg.UpdateChannel)
		}
		return fake, nil
	}

	if code := runWithMutedIO(t, "--check-update", "--install-provider=scoop"); code != 0 {
		t.Fatalf("check-update exit=%d", code)
	}
	if fake.checkCalls != 1 || fake.updateCalls != 0 {
		t.Fatalf("check calls=%d update calls=%d", fake.checkCalls, fake.updateCalls)
	}
	if code := runWithMutedIO(t, "--update", "--install-provider=scoop"); code != 0 {
		t.Fatalf("update exit=%d", code)
	}
	if fake.checkCalls != 1 || fake.updateCalls != 1 {
		t.Fatalf("check calls=%d update calls=%d", fake.checkCalls, fake.updateCalls)
	}
}

func TestGuardianAutoInstallStopsWorkerBeforeApplyAndRestartsUpdatedProcess(t *testing.T) {
	oldCore := guardianRunParentCoreFunc
	oldDelay := autoUpdateInitialDelayFunc
	oldRestart := restartUpdatedProcessFunc
	oldWrite := writeUpdateStateFunc
	defer func() {
		guardianRunParentCoreFunc = oldCore
		autoUpdateInitialDelayFunc = oldDelay
		restartUpdatedProcessFunc = oldRestart
		writeUpdateStateFunc = oldWrite
	}()

	workerStarted := make(chan struct{}, 1)
	workerStopped := make(chan struct{}, 1)
	guardianRunParentCoreFunc = func(ctx context.Context, _ guardian.CommandSpec, _ guardian.ParentOptions) error {
		workerStarted <- struct{}{}
		<-ctx.Done()
		workerStopped <- struct{}{}
		return nil
	}
	autoUpdateInitialDelayFunc = func(time.Duration) time.Duration { return 0 }
	writeUpdateStateFunc = func(string, pxupdate.State) error { return nil }
	restartErr := errors.New("restart handoff")
	restartUpdatedProcessFunc = func(_ string, _ []string) error {
		return restartErr
	}
	fake := &fakeUpdateService{
		checkStatus:  pxupdate.Status{Current: "1.0.0", Latest: "1.1.0", Available: true, Provider: pxupdate.ProviderDirect, Channel: pxupdate.Stable},
		updateStatus: pxupdate.Status{Current: "1.0.0", Latest: "1.1.0", Available: true, Provider: pxupdate.ProviderDirect, Channel: pxupdate.Stable, Applied: true},
	}
	fake.applyHook = func() {
		select {
		case <-workerStopped:
		default:
			t.Fatal("apply started before Guardian worker stopped")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() { <-workerStarted }()
	err := runGuardianAutoUpdateLoop(ctx, guardian.CommandSpec{}, config.Config{UpdateInterval: time.Hour}, pxupdate.AutoInstall, fake)
	if !errors.Is(err, restartErr) {
		t.Fatalf("loop error=%v want restart handoff", err)
	}
	if fake.checkCalls != 1 || fake.updateCalls != 1 {
		t.Fatalf("prepare calls=%d apply calls=%d", fake.checkCalls, fake.updateCalls)
	}
}

func TestGuardianAutoApplyFailureRestartsHealthyWorker(t *testing.T) {
	oldCore := guardianRunParentCoreFunc
	oldDelay := autoUpdateInitialDelayFunc
	oldRestart := restartUpdatedProcessFunc
	oldWrite := writeUpdateStateFunc
	defer func() {
		guardianRunParentCoreFunc = oldCore
		autoUpdateInitialDelayFunc = oldDelay
		restartUpdatedProcessFunc = oldRestart
		writeUpdateStateFunc = oldWrite
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	starts := 0
	firstStopped := make(chan struct{}, 1)
	guardianRunParentCoreFunc = func(workerCtx context.Context, _ guardian.CommandSpec, _ guardian.ParentOptions) error {
		starts++
		thisStart := starts
		if thisStart == 2 {
			cancel()
		}
		<-workerCtx.Done()
		if thisStart == 1 {
			firstStopped <- struct{}{}
		}
		return nil
	}
	autoUpdateInitialDelayFunc = func(time.Duration) time.Duration { return 0 }
	writeUpdateStateFunc = func(string, pxupdate.State) error { return nil }
	restartUpdatedProcessFunc = func(string, []string) error {
		t.Fatal("restart should not run after failed apply")
		return nil
	}
	fake := &fakeUpdateService{
		checkStatus: pxupdate.Status{Current: "1.0.0", Latest: "1.1.0", Available: true, Provider: pxupdate.ProviderDirect, Channel: pxupdate.Stable},
		updateErr:   errors.New("apply failed"),
	}
	fake.applyHook = func() {
		select {
		case <-firstStopped:
		default:
			t.Fatal("failed apply started before first worker stopped")
		}
	}
	if err := runGuardianAutoUpdateLoop(ctx, guardian.CommandSpec{}, config.Config{UpdateInterval: time.Millisecond}, pxupdate.AutoInstall, fake); err != nil {
		t.Fatalf("loop error=%v", err)
	}
	if starts < 2 {
		t.Fatalf("guardian starts=%d want restart after apply failure", starts)
	}
}

func TestGuardianNotifyCheckDoesNotStopWorker(t *testing.T) {
	oldCore := guardianRunParentCoreFunc
	oldDelay := autoUpdateInitialDelayFunc
	oldWrite := writeUpdateStateFunc
	defer func() {
		guardianRunParentCoreFunc = oldCore
		autoUpdateInitialDelayFunc = oldDelay
		writeUpdateStateFunc = oldWrite
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	workerStopped := make(chan struct{})
	guardianRunParentCoreFunc = func(workerCtx context.Context, _ guardian.CommandSpec, _ guardian.ParentOptions) error {
		<-workerCtx.Done()
		close(workerStopped)
		return nil
	}
	autoUpdateInitialDelayFunc = func(time.Duration) time.Duration { return 0 }
	var state pxupdate.State
	writeUpdateStateFunc = func(_ string, got pxupdate.State) error {
		state = got
		select {
		case <-workerStopped:
			t.Fatal("notify check stopped worker")
		default:
		}
		cancel()
		return nil
	}
	fake := &fakeUpdateService{checkStatus: pxupdate.Status{Current: "1.0.0", Latest: "1.1.0", Available: true, Provider: pxupdate.ProviderBrew, Channel: pxupdate.Stable}}
	if err := runGuardianAutoUpdateLoop(ctx, guardian.CommandSpec{}, config.Config{UpdateInterval: time.Hour}, pxupdate.AutoNotify, fake); err != nil {
		t.Fatalf("loop error=%v", err)
	}
	if state.LastResult != pxupdate.ResultAvailable || state.Provider != pxupdate.ProviderBrew {
		t.Fatalf("notify state=%+v", state)
	}
	if fake.updateCalls != 0 {
		t.Fatalf("notify mode applied update %d times", fake.updateCalls)
	}
}

func TestRunUpdateApplyHelperBypassesConfigParsing(t *testing.T) {
	oldHelper := runUpdateApplyHelperFunc
	defer func() { runUpdateApplyHelperFunc = oldHelper }()
	calls := 0
	runUpdateApplyHelperFunc = func(args []string) (bool, int) {
		calls++
		if len(args) != 1 || args[0] != "--private-helper" {
			t.Fatalf("helper args=%v", args)
		}
		return true, 23
	}
	if code := runWithMutedIO(t, "--private-helper"); code != 23 {
		t.Fatalf("helper exit=%d", code)
	}
	if calls != 1 {
		t.Fatalf("helper calls=%d", calls)
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
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusProxyAuthRequired {
		t.Fatalf("status=%s want 407", resp.Status)
	}
	if attempts != 3 {
		t.Fatalf("attempts=%d want 3", attempts)
	}
}
