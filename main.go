package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pavelsimo/pxgo/internal/config"
	"github.com/pavelsimo/pxgo/internal/debug"
	"github.com/pavelsimo/pxgo/internal/diagnostic"
	"github.com/pavelsimo/pxgo/internal/guardian"
	"github.com/pavelsimo/pxgo/internal/proxy"
	"github.com/pavelsimo/pxgo/internal/winstartup"
	"golang.org/x/term"
)

var (
	version               = "dev"
	installStartupFunc    = installStartup
	setupDebugFunc        = setupDebug
	runGuardianParentFunc = runGuardianParent
	runGuardianWorkerFunc = runGuardianWorker
)

const (
	authNone               = "NONE"
	localhostIP            = "127.0.0.1"
	controlShutdownTimeout = 5 * time.Second
	startExitWaitTimeout   = time.Second
)

type shutdowner interface {
	Shutdown(context.Context) error
}

func shutdownWithTimeout(s shutdowner, timeout time.Duration) error {
	if s == nil {
		return errors.New("nil shutdown target")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return s.Shutdown(ctx)
}

func main() {
	os.Exit(run())
}

func run() (exitCode int) {
	defer func() {
		if recovered := recover(); recovered != nil {
			debug.LogPanic(config.GetLogfile(config.LogCWD), recovered)
			exitCode = 1
		}
	}()
	cfg, err := config.ParseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	workerControl, internalWorker, controlErr := guardian.WorkerControlFromEnv()
	if controlErr != nil {
		fmt.Fprintln(os.Stderr, controlErr)
		return 5
	}
	if internalWorker {
		return runGuardianWorkerFunc(cfg, workerControl)
	}
	if cfg.Help {
		printHelp()
		return 0
	}
	if cfg.Version {
		fmt.Println(version)
		return 0
	}
	if cfg.Save {
		path := config.ConfigPathForSave(cfg.ConfigPath)
		if err := config.SaveINI(path, cfg); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		fmt.Fprintf(os.Stdout, "Configuration saved to %s\n", path)
		if data, err := os.ReadFile(path); err == nil {
			fmt.Fprint(os.Stdout, string(data))
		}
		return 0
	}
	if cfg.Install {
		configPath := config.ConfigPathForSave(cfg.ConfigPath)
		executable, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 6
		}
		cmd, err := winstartup.PrepareRunCommand(
			executable,
			configPath,
			func(path string) bool {
				_, err := os.Stat(path)
				return err == nil
			},
			func(path string) error {
				return config.SaveINI(path, cfg)
			},
		)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 6
		}
		if err := installStartupFunc(cmd, cfg.Force); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 6
		}
		return 0
	}
	if cfg.Uninstall {
		if err := uninstallStartup(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 6
		}
		return 0
	}
	if cfg.PasswordAction {
		if cfg.Password == "" {
			fmt.Fprintf(os.Stderr, "Password for %s: ", cfg.Username)
			raw, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(os.Stderr)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 2
			}
			cfg.Password = string(raw)
		}
		if err := config.StorePassword(config.Realm, cfg.Username, cfg.Password); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		fmt.Fprintf(os.Stdout, "Password saved for %s\n", cfg.Username)
		return 0
	}
	if cfg.ClientPasswordAction {
		if cfg.ClientPassword == "" {
			fmt.Fprintf(os.Stderr, "Password for %s: ", cfg.ClientUsername)
			raw, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(os.Stderr)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 2
			}
			cfg.ClientPassword = string(raw)
		}
		if err := config.StorePassword(config.ClientRealm, cfg.ClientUsername, cfg.ClientPassword); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		fmt.Fprintf(os.Stdout, "Password saved for %s\n", cfg.ClientUsername)
		return 0
	}
	if cfg.Doctor {
		if err := doctor(cfg); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 3
		}
		return 0
	}
	if cfg.Quit {
		if err := quit(cfg); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 3
		}
		return 0
	}
	if cfg.Restart {
		if err := quit(cfg); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 3
		}
		time.Sleep(100 * time.Millisecond)
	}
	if cfg.Test != "" {
		setupDebugBestEffort(cfg)
		if err := runSelfTest(cfg); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 4
		}
		return 0
	}
	return runGuardianParentFunc(cfg)
}

func isOneShotConfig(cfg config.Config) bool {
	return cfg.Help ||
		cfg.Version ||
		cfg.Save ||
		cfg.Install ||
		cfg.Uninstall ||
		cfg.PasswordAction ||
		cfg.ClientPasswordAction ||
		cfg.Doctor ||
		cfg.Quit ||
		cfg.Test != ""
}

func runGuardianParent(config.Config) int {
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 5
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	spec := guardian.CommandSpec{
		Path:   executable,
		Args:   append([]string(nil), os.Args[1:]...),
		Env:    os.Environ(),
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}
	if err := guardian.RunParent(ctx, spec, guardian.ParentOptions{}); err != nil {
		var startup *guardian.StartupExitError
		if errors.As(err, &startup) {
			if startup.Err != nil {
				fmt.Fprintln(os.Stderr, diagnostic.RedactText(startup.Err.Error()))
			} else {
				fmt.Fprintln(os.Stderr, startup)
			}
			if startup.Code > 0 && startup.Code < 256 {
				return startup.Code
			}
			return 5
		}
		fmt.Fprintln(os.Stderr, diagnostic.RedactText(err.Error()))
		return 5
	}
	return 0
}

func runGuardianWorker(cfg config.Config, control guardian.WorkerControl) int {
	setupDebugBestEffort(cfg)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	connectCtx, cancelConnect := context.WithTimeout(ctx, controlShutdownTimeout)
	session, err := guardian.Connect(connectCtx, control.Addr, control.Token)
	cancelConnect()
	if err != nil {
		fmt.Fprintln(os.Stderr, "guardian control connection failed")
		return int(guardian.WorkerExitStartupFailure)
	}
	defer session.Close()

	s, err := proxy.New(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	fatalSignals := s.RuntimeFatalSignals()
	hooks := guardian.WorkerHooks{
		Start: s.Start,
		Ready: s.Ready,
		Progress: func() uint64 {
			return s.RuntimeStatus().ProgressSequence
		},
		FatalRequested: func() bool {
			if s.RuntimeStatus().FatalRequested {
				return true
			}
			if fatalSignals == nil {
				return false
			}
			select {
			case <-fatalSignals:
				return true
			default:
				return false
			}
		},
		Shutdown: s.Shutdown,
		Snapshot: func() {
			s.BestEffortFatalSnapshot(diagnosticSnapshotPath())
		},
	}
	result := guardian.RunWorker(ctx, session, hooks, guardian.WorkerOptions{})
	if result.Err != nil {
		fmt.Fprintln(os.Stderr, diagnostic.RedactText(result.Err.Error()))
	}
	if d := debug.Instance(); d != nil {
		_ = d.Close()
	}
	return int(result.Exit)
}

func setupDebugBestEffort(cfg config.Config) {
	if err := setupDebugFunc(cfg); err != nil {
		msg := diagnostic.RedactText(err.Error())
		diagnostic.Record("diagnostic.log-error", msg)
		fmt.Fprintln(os.Stderr, "debug logging disabled:", msg)
	}
}

func setupDebug(cfg config.Config) error {
	switch cfg.Log {
	case config.LogNone:
		return nil
	case config.LogStdout:
		_, err := debug.New("", false)
		return err
	default:
		path := config.GetLogfile(cfg.Log)
		if path == "" {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		_, err := debug.New(path, false)
		return err
	}
}

func printHelp() {
	fmt.Println(`pxgo - HTTP/HTTPS proxy

Usage:
  pxgo [options]

Options:
  --proxy, --server=HOST[:PORT]   Upstream proxy server
  --pac=PATH_OR_URL               PAC file path or URL
  --pac-encoding=ENCODING         PAC file encoding
  --port=PORT                     Listen port
  --listen=IP                     Listen address
  --gateway                       Listen on all interfaces; requires restrictive allow or strong client auth
  --hostonly                      Allow local host interfaces only
  --allow=IPGLOB                  Client allow list
  --noproxy=LIST                  Direct-connect bypass list
  --useragent=VALUE               Override forwarded User-Agent
  --auth=TYPE                     Upstream auth: ANY, ANYSAFE, NEGOTIATE, NTLM, DIGEST, BASIC, NONE
  --username=USER                 Upstream auth username
  --kerberos                      Enable Kerberos ticket management
  --client-auth=TYPE              Client auth: NONE, ANY, ANYSAFE, NEGOTIATE, NTLM, DIGEST, BASIC (Basic-capable modes are loopback-only)
  --client-username=USER          Downstream auth username
  --client-nosspi=0|1             Disable SSPI for downstream auth compatibility
  --config=PATH                   Read or save pxgo.ini at PATH
  --save                          Save configuration to pxgo.ini
  --password                      Store upstream password
  --client-password               Store downstream password
  --log= | PXGO_LOG= | settings:log=
  Enable debug logging. default: 0
    1 = Log to script dir [--debug]
    2 = Log to working dir
    3 = Log to working dir with unique filename [--uniqlog]
    4 = Log to stdout [--verbose]. Implies --foreground
  --doctor                        Print local read-only proxy diagnostics
  --quit                          Stop a running proxy
  --restart                       Quit then start the proxy
  --install                       Install pxgo in Windows startup registry
  --uninstall                     Remove pxgo from Windows startup registry
  --force                         Overwrite existing Windows startup entry
  --test[=URL|all[:BASE]]         Run self-test through the proxy
  --test-auth                     Self-test using configured upstream auth via auth=NONE
  --version                       Print version
  -h, --help                      Show help`)
}

func doctor(cfg config.Config) error {
	report, err := doctorReport(cfg, diagnosticSnapshotPath())
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}

func diagnosticSnapshotPath() string {
	dir := config.GetConfigDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "diagnostic-snapshot.json")
}

func doctorReport(cfg config.Config, snapshotPath string) (diagnostic.DoctorReport, error) {
	addr := net.JoinHostPort(listenForClient(cfg.Listen), fmt.Sprint(cfg.Port))
	client := http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			Proxy: nil,
		},
	}
	resp, err := client.Get("http://" + addr + diagnostic.DoctorControlPath)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			var snapshot diagnostic.Snapshot
			decoder := json.NewDecoder(io.LimitReader(resp.Body, 1<<20))
			if decodeErr := decoder.Decode(&snapshot); decodeErr != nil {
				return diagnostic.DoctorReport{}, fmt.Errorf("doctor failed: malformed live snapshot: %w", decodeErr)
			}
			var extra any
			if decodeErr := decoder.Decode(&extra); decodeErr != io.EOF {
				if decodeErr == nil {
					return diagnostic.DoctorReport{}, errors.New("doctor failed: multiple JSON values in live snapshot")
				}
				return diagnostic.DoctorReport{}, fmt.Errorf("doctor failed: malformed live snapshot: %w", decodeErr)
			}
			return diagnostic.DoctorReport{Live: true, Snapshot: snapshot}, nil
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		err = fmt.Errorf("doctor live status: %s", resp.Status)
	}

	if snapshotPath != "" {
		if snapshot, loadErr := diagnostic.LoadSnapshot(snapshotPath); loadErr == nil {
			return diagnostic.DoctorReport{
				Live:     false,
				Error:    diagnostic.RedactText(err.Error()),
				Snapshot: snapshot,
			}, nil
		}
	}
	return diagnostic.DoctorReport{}, fmt.Errorf("doctor failed: %w", err)
}

func quit(cfg config.Config) error {
	addr := net.JoinHostPort(listenForClient(cfg.Listen), fmt.Sprint(cfg.Port))
	if err := waitForRunningProxy(addr); err != nil {
		return err
	}
	client := http.Client{Timeout: 2 * time.Second}
	url := "http://" + addr + "/PxgoQuit"
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		resp, err := client.Get(url)
		if err != nil {
			lastErr = err
			time.Sleep(50 * time.Millisecond)
			continue
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusForbidden {
			return fmt.Errorf("quit failed: cannot quit pxgo on remote or disallowed host")
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("quit failed: %s", resp.Status)
		}
		if waitForClosed(addr, 2*time.Second) {
			return nil
		}
		lastErr = fmt.Errorf("quit failed: proxy still running")
	}
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("quit failed")
}

func waitForRunningProxy(addr string) error {
	for attempt := 0; attempt < 5; attempt++ {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		if isConnectionRefused(err) {
			return fmt.Errorf("pxgo is not running")
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("pxgo is not responding at %s", addr)
}

func waitForClosed(addr string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err != nil {
			return true
		}
		_ = conn.Close()
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

func isConnectionRefused(err error) bool {
	return err != nil && errors.Is(err, syscall.ECONNREFUSED)
}

func runSelfTest(cfg config.Config) (retErr error) {
	testAuthCfg := cfg
	if cfg.TestAuth {
		cfg.Auth = authNone
	}
	s, err := proxy.New(cfg)
	if err != nil {
		return err
	}
	errc := make(chan error, 1)
	go func() { errc <- s.Start() }()

	if err := waitSelfTestReady(s, errc, 5*time.Second); err != nil {
		if shutdownErr := shutdownWithTimeout(s, controlShutdownTimeout); shutdownErr != nil {
			return errors.Join(err, fmt.Errorf("self-test shutdown after start failure: %w", shutdownErr))
		}
		return err
	}

	defer func() {
		shutdownErr := shutdownWithTimeout(s, controlShutdownTimeout)
		if shutdownErr != nil {
			retErr = errors.Join(retErr, fmt.Errorf("self-test shutdown: %w", shutdownErr))
			return
		}
		select {
		case startErr := <-errc:
			if startErr != nil {
				retErr = errors.Join(retErr, fmt.Errorf("self-test proxy: %w", startErr))
			}
		case <-time.After(startExitWaitTimeout):
			retErr = errors.Join(retErr, errors.New("self-test proxy did not stop after shutdown"))
		}
	}()

	urls := selfTestURLs(cfg.Test)
	allMode := selfTestAllMode(cfg.Test)
	if !allMode {
		urls = []string{cfg.Test}
	}
	proxyURL, err := url.Parse(fmt.Sprintf("http://%s:%d", listenForClient(cfg.Listen), cfg.Port))
	if err != nil {
		return fmt.Errorf("self-test proxy URL: %w", err)
	}
	tr := &http.Transport{
		Proxy:           http.ProxyURL(proxyURL),
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // #nosec G402 -- self-test intentionally accepts arbitrary test endpoints.
	}
	client := &http.Client{Transport: tr, Timeout: 30 * time.Second}
	methods := []string{http.MethodGet}
	if allMode {
		methods = []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch}
	}
	var wg sync.WaitGroup
	errs := make(chan error, len(urls)*len(methods))
	for _, u := range urls {
		for _, method := range methods {
			wg.Add(1)
			go func(u, method string) {
				defer wg.Done()
				body := strings.NewReader("")
				if method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch {
					body = strings.NewReader("px-go-test")
				}
				targetURL := u
				if allMode {
					targetURL = strings.TrimRight(u, "/") + "/" + strings.ToLower(method)
				}
				req, err := http.NewRequest(method, targetURL, body)
				if err != nil {
					errs <- fmt.Errorf("%s %s: %w", method, targetURL, err)
					return
				}
				resp, err := doSelfTestRequest(client, req, testAuthCfg, cfg.TestAuth)
				if err != nil {
					errs <- err
					return
				}
				defer resp.Body.Close()
				_, _ = io.Copy(io.Discard, resp.Body)
				if resp.StatusCode >= 400 {
					errs <- fmt.Errorf("%s %s: %s", method, u, resp.Status)
				}
			}(u, method)
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func doSelfTestRequest(client *http.Client, req *http.Request, authCfg config.Config, testAuth bool) (*http.Response, error) {
	if req == nil {
		return nil, errors.New("nil self-test request")
	}
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
		_ = req.Body.Close()
	}
	proxyAuth := ""
	if testAuth {
		proxyAuth = proxy.UpstreamProxyAuthHeader(authCfg, req.Method, req.URL.String(), nil)
	}
	var lastResp *http.Response
	for attempts := 0; attempts < 3; attempts++ {
		next := req.Clone(req.Context())
		if len(body) == 0 {
			next.Body = http.NoBody
		} else {
			next.Body = io.NopCloser(bytes.NewReader(body))
		}
		next.ContentLength = int64(len(body))
		next.Header = req.Header.Clone()
		if proxyAuth != "" {
			next.Header.Set("Proxy-Authorization", proxyAuth)
		}
		resp, err := client.Do(next)
		if err != nil || !testAuth || resp.StatusCode != http.StatusProxyAuthRequired {
			return resp, err
		}
		lastResp = resp
		challenges := resp.Header.Values("Proxy-Authenticate")
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		proxyAuth = proxy.UpstreamProxyAuthHeader(authCfg, req.Method, req.URL.String(), challenges)
		if proxyAuth == "" {
			return resp, nil
		}
	}
	return lastResp, nil
}

func selfTestURLs(test string) []string {
	if test == "all" || test == "1" {
		return []string{"http://httpbin.org", "https://httpbin.org"}
	}
	if strings.HasPrefix(test, "all:") {
		base := strings.TrimPrefix(test, "all:")
		if strings.Contains(base, "://") {
			return []string{base}
		}
		return []string{"http://" + base, "https://" + base}
	}
	return nil
}

func selfTestAllMode(test string) bool {
	return test == "all" || test == "1" || strings.HasPrefix(test, "all:")
}

func waitSelfTestReady(s *proxy.Server, errc <-chan error, timeout time.Duration) error {
	if s == nil {
		return errors.New("nil self-test proxy")
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()

	for {
		if s.Ready() {
			return nil
		}
		select {
		case err := <-errc:
			if err != nil {
				return fmt.Errorf("self-test proxy start: %w", err)
			}
			return errors.New("self-test proxy stopped before becoming ready")
		case <-ticker.C:
		case <-timer.C:
			return errors.New("self-test proxy did not become ready before timeout")
		}
	}
}

func listenForClient(listen string) string {
	for _, raw := range strings.Split(listen, ",") {
		host := strings.TrimSpace(raw)
		if host == "" {
			continue
		}
		if host == "0.0.0.0" {
			return localhostIP
		}
		return host
	}
	return localhostIP
}
