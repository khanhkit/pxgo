package proxy

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pavelsimo/pxgo/internal/config"
	"github.com/pavelsimo/pxgo/internal/debug"
	"github.com/pavelsimo/pxgo/internal/kerberos"
	"github.com/pavelsimo/pxgo/internal/supervisor"
	"github.com/pavelsimo/pxgo/internal/wproxy"
)

const (
	digestRealm      = "PxClient"
	authAny          = "ANY"
	authAnySafe      = "ANYSAFE"
	authBasic        = "BASIC"
	authDigest       = "DIGEST"
	authNegotiate    = "NEGOTIATE"
	authNone         = "NONE"
	authNTLM         = "NTLM"
	authSchemeBasic  = "Basic"
	authSchemeDigest = "Digest"
	authSchemeNeg    = "Negotiate"
	digestQopAuth    = "auth"
	httpScheme       = "http"
	httpsScheme      = "https"
	maxMemoryBody    = 1 << 20
	goosWindows      = "windows"
	quitControlPath  = "/PxgoQuit"
)

type Server struct {
	cfg        config.Config
	w          *wproxy.Wproxy
	wmu        sync.RWMutex
	lastReload time.Time
	srv        *http.Server
	listeners  []net.Listener
	port       int
	stateMu    sync.RWMutex
	clients    sync.Map // remoteAddr string -> *clientState
	krb        *kerberos.Manager
	sup        *supervisor.Supervisor
	closed     chan struct{}
	once       sync.Once
	active     int64
	transports sync.Map // proxy key -> *http.Transport, reused across requests

	tunnelMu           sync.Mutex
	tunnels            map[*managedTunnel]struct{}
	tunnelPending      int
	tunnelZero         chan struct{}
	tunnelShuttingDown bool

	// Derived from immutable config once in New so request handlers do not
	// re-parse it on every call.
	clientAuthList []string
	allowSet       wproxy.IPSet
	hostIPs        atomic.Pointer[hostIPEntry]
}

// hostIPEntry caches the local interface addresses used by --hostonly checks;
// enumerating interfaces is a syscall storm we do not want per request.
type hostIPEntry struct {
	ips     []net.IP
	expires time.Time
}

func New(cfg config.Config) (*Server, error) {
	if err := validateUpstreamAuth(cfg.Auth); err != nil {
		return nil, err
	}
	if err := validateClientAuth(cfg.ClientAuth); err != nil {
		return nil, err
	}
	if err := validateAllow(cfg.Allow); err != nil {
		return nil, err
	}
	if err := validateDownstreamCredentials(cfg); err != nil {
		return nil, err
	}
	if err := validateGatewaySecurity(cfg); err != nil {
		return nil, err
	}
	if err := validateServerBudgets(cfg); err != nil {
		return nil, err
	}
	wp, err := buildWproxy(cfg)
	if err != nil {
		return nil, err
	}
	krb, err := buildKerberosManager(cfg)
	if err != nil {
		return nil, err
	}
	if krb != nil {
		krb.Check(true)
	}
	s := &Server{
		cfg:        cfg,
		w:          wp,
		lastReload: time.Now(),
		port:       cfg.Port,
		krb:        krb,
		closed:     make(chan struct{}),
		tunnels:    make(map[*managedTunnel]struct{}),
		tunnelZero: closedSignal(),
	}
	s.sup = newRuntimeSupervisor(s)
	s.clientAuthList = clientAuthMethods(cfg.ClientAuth)
	if cfg.Allow != "" {
		// Already validated by validateAllow above.
		s.allowSet, _, _ = wproxy.ParseNoProxy(cfg.Allow, true)
	}
	return s, nil
}

func validateAllow(allow string) error {
	if strings.TrimSpace(allow) == "" {
		return nil
	}
	if _, _, err := wproxy.ParseNoProxy(allow, true); err != nil {
		return fmt.Errorf("unsupported allow value: %w", err)
	}
	return nil
}

func validateDownstreamCredentials(cfg config.Config) error {
	if len(clientAuthMethods(cfg.ClientAuth)) == 0 {
		return nil
	}
	if strings.TrimSpace(cfg.ClientUsername) == "" {
		return errors.New("client authentication requires --client-username")
	}
	if cfg.ClientPassword == "" {
		return errors.New("client authentication requires a non-empty client password")
	}
	return nil
}

func validateGatewaySecurity(cfg config.Config) error {
	methods := clientAuthMethods(cfg.ClientAuth)
	if isRemotePlaintextExposure(cfg) && containsClientAuthMethod(methods, authBasic) {
		return errors.New("plaintext remote exposure cannot advertise BASIC client authentication; use ANYSAFE/DIGEST/NTLM/NEGOTIATE or a loopback listener")
	}
	if !cfg.Gateway {
		return nil
	}
	if cfg.Hostonly || hasRestrictiveAllow(cfg.Allow) || len(methods) != 0 {
		return nil
	}
	return errors.New("gateway requires an explicit restrictive --allow policy or downstream client authentication")
}

func containsClientAuthMethod(methods []string, want string) bool {
	for _, method := range methods {
		if method == want {
			return true
		}
	}
	return false
}

func hasRestrictiveAllow(allow string) bool {
	allow = strings.TrimSpace(allow)
	if allow == "" {
		return false
	}
	for _, raw := range strings.Split(allow, ",") {
		token := strings.ToLower(strings.TrimSpace(raw))
		switch token {
		case "", "*", "*.*.*.*", "0.0.0.0/0", "::/0":
			return false
		}
	}
	return true
}

func isRemotePlaintextExposure(cfg config.Config) bool {
	if cfg.Gateway {
		return true
	}
	if cfg.Hostonly {
		return false
	}
	for _, raw := range strings.Split(cfg.Listen, ",") {
		host := strings.Trim(strings.TrimSpace(raw), "[]")
		if host == "" {
			continue
		}
		if strings.EqualFold(host, "localhost") {
			continue
		}
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return true
		}
	}
	return false
}

func validateServerBudgets(cfg config.Config) error {
	if cfg.Workers <= 0 || cfg.Threads <= 0 {
		return errors.New("workers and threads must both be greater than zero")
	}
	maxInt := int(^uint(0) >> 1)
	if cfg.Workers > maxInt/cfg.Threads {
		return errors.New("workers*threads connection budget overflows int")
	}
	if cfg.Idle <= 0 {
		return errors.New("idle timeout must be greater than zero")
	}
	if cfg.SockTimeout <= 0 || math.IsNaN(cfg.SockTimeout) || math.IsInf(cfg.SockTimeout, 0) {
		return errors.New("socktimeout must be a finite value greater than zero")
	}
	if cfg.SockTimeout > float64((1<<63-1)/(2*int64(time.Second))) {
		return errors.New("socktimeout is too large")
	}
	if int64(cfg.Idle) > (1<<63-1)/int64(time.Second) {
		return errors.New("idle timeout is too large")
	}
	return nil
}

func connectionBudget(cfg config.Config) int {
	return cfg.Workers * cfg.Threads
}

func configuredSockTimeout(cfg config.Config) time.Duration {
	return time.Duration(cfg.SockTimeout * float64(time.Second))
}

func configuredWriteTimeout(cfg config.Config) time.Duration {
	return 2 * configuredSockTimeout(cfg)
}

func buildWproxy(cfg config.Config) (*wproxy.Wproxy, error) {
	mode := wproxy.ModeNone
	var servers []wproxy.Server
	var err error
	if cfg.PAC != "" {
		mode = wproxy.ModeConfigPAC
		servers = []wproxy.Server{{Host: cfg.PAC, Port: 0, Scheme: "pac"}}
	} else if cfg.Server != "" {
		mode = wproxy.ModeConfig
		servers, err = wproxy.ParseProxy(cfg.Server)
		if err != nil {
			return nil, err
		}
	}
	wp, err := wproxy.New(mode, servers, cfg.NoProxy, cfg.PACEncoding)
	if err != nil {
		return nil, err
	}
	return wp, nil
}

func buildKerberosManager(cfg config.Config) (*kerberos.Manager, error) {
	if !cfg.Kerberos {
		return nil, nil
	}
	if cfg.Username == "" {
		return nil, errors.New("--kerberos requires --username")
	}
	mgr := kerberos.New(cfg.Username, func() *string {
		if password, ok := config.GetPassword(config.Realm, cfg.Username); ok {
			return &password
		}
		if cfg.Password == "" {
			return nil
		}
		return &cfg.Password
	}, kerberos.DetectHeimdal())
	if runtime.GOOS == goosWindows {
		failWithBackoff := func() bool {
			mgr.Backoff = kerberos.CheckInterval
			return false
		}
		mgr.KinitWithPasswordFunc = failWithBackoff
		mgr.KinitRenewFunc = failWithBackoff
		mgr.KlistValidFunc = func() bool { return false }
	}
	return mgr, nil
}

func (s *Server) ListenAddr() string {
	hosts := s.listenHosts()
	return net.JoinHostPort(hosts[0], fmt.Sprintf("%d", s.Port()))
}

func (s *Server) ListenAddrs() []string {
	hosts := s.listenHosts()
	addrs := make([]string, 0, len(hosts))
	port := s.Port()
	for _, host := range hosts {
		addrs = append(addrs, net.JoinHostPort(host, fmt.Sprintf("%d", port)))
	}
	return addrs
}

func (s *Server) listenHosts() []string {
	if s.cfg.Gateway || s.cfg.Hostonly {
		return []string{"0.0.0.0"}
	}
	seen := map[string]bool{}
	var hosts []string
	for _, raw := range strings.Split(s.cfg.Listen, ",") {
		host := strings.TrimSpace(raw)
		if host == "" {
			continue
		}
		if !seen[host] {
			hosts = append(hosts, host)
			seen[host] = true
		}
	}
	if len(hosts) == 0 {
		hosts = append(hosts, "127.0.0.1")
	}
	return hosts
}

func (s *Server) Start() error {
	port := s.cfg.Port
	var listeners []net.Listener
	admissionSlots := make(chan struct{}, connectionBudget(s.cfg))
	for _, host := range s.listenHosts() {
		addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
		rawListener, err := net.Listen("tcp", addr)
		if err != nil {
			for _, opened := range listeners {
				_ = opened.Close()
			}
			return err
		}
		ln := newAdmissionListener(rawListener, admissionSlots)
		listeners = append(listeners, ln)
		if port == 0 {
			port = rawListener.Addr().(*net.TCPAddr).Port
		}
	}
	sockTimeout := configuredSockTimeout(s.cfg)
	srv := &http.Server{
		Handler:           s,
		ReadHeaderTimeout: sockTimeout,
		ReadTimeout:       sockTimeout,
		WriteTimeout:      configuredWriteTimeout(s.cfg),
		IdleTimeout:       time.Duration(s.cfg.Idle) * time.Second,
		ConnState: func(conn net.Conn, state http.ConnState) {
			if state == http.StateClosed || state == http.StateHijacked {
				s.clearClientState(conn.RemoteAddr().String())
			}
		},
	}
	s.stateMu.Lock()
	s.listeners = listeners
	s.port = port
	s.srv = srv
	s.stateMu.Unlock()
	go s.maintenanceLoop()
	errc := make(chan error, len(listeners))
	for _, ln := range listeners {
		debug.Dprint("listening on " + ln.Addr().String())
		go func(ln net.Listener) {
			err := srv.Serve(ln)
			if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
				err = nil
			}
			errc <- err
		}(ln)
	}
	for range listeners {
		if err := <-errc; err != nil {
			_ = s.Shutdown(context.Background())
			return err
		}
	}
	return nil
}

func (s *Server) Shutdown(ctx context.Context) error {
	var err error
	s.once.Do(func() {
		tunnelsDone := s.beginTunnelShutdown()
		s.stateMu.RLock()
		srv := s.srv
		s.stateMu.RUnlock()
		if srv != nil {
			err = srv.Shutdown(ctx)
		}
		if tunnelErr := waitTunnelDrain(ctx, tunnelsDone); tunnelErr != nil && err == nil {
			err = tunnelErr
		}
		if s.sup != nil {
			s.sup.Close()
		}
		s.clearTransports()
		s.wmu.Lock()
		wp := s.w
		s.w = nil
		if wp != nil {
			if closeErr := wp.Close(); closeErr != nil && err == nil {
				err = fmt.Errorf("close proxy resolver: %w", closeErr)
			}
		}
		s.wmu.Unlock()
		if s.krb != nil {
			s.krb.Cleanup()
		}
		close(s.closed)
	})
	return err
}

func (s *Server) Port() int {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return s.port
}

func (s *Server) Ready() bool {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return s.srv != nil && len(s.listeners) != 0
}

// RuntimeStatus exposes a read-only Supervisor snapshot for diagnostics and the
// external Process Guardian. It does not expose recovery mutation APIs.
func (s *Server) RuntimeStatus() supervisor.Status {
	if s.sup == nil {
		return supervisor.Status{}
	}
	return s.sup.Status()
}

// RuntimeFatalSignals exposes only escalation requests. Guardian owns any
// process-level restart decision; Runtime Supervisor never exits the process.
func (s *Server) RuntimeFatalSignals() <-chan supervisor.FatalSignal {
	if s.sup == nil {
		return nil
	}
	return s.sup.FatalSignals()
}

func (s *Server) ActiveTunnels() int64 {
	return atomic.LoadInt64(&s.active)
}

func (s *Server) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	defer func() {
		if recovered := recover(); recovered != nil {
			s.recordRuntimeOutcome(supervisor.OutcomeInternalFailure, wproxy.Server{})
			debug.LogPanic(config.GetLogfile(config.LogCWD), recovered)
			http.Error(rw, "internal server error", http.StatusInternalServerError)
		}
	}()
	debug.Dprint(req.Method + " " + req.RequestURI)
	if isQuitControlRequest(req) {
		if !isLoopbackRemote(req.RemoteAddr) || !s.isClientAllowed(req.RemoteAddr) {
			http.Error(rw, "forbidden", http.StatusForbidden)
			return
		}
		rw.WriteHeader(http.StatusOK)
		go func() {
			time.Sleep(50 * time.Millisecond)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = s.Shutdown(ctx)
		}()
		return
	}
	if !s.isClientAllowed(req.RemoteAddr) {
		debug.Dprint("client not allowed: " + req.RemoteAddr)
		http.Error(rw, "forbidden", http.StatusForbidden)
		return
	}
	if s.clientAuthEnabled() && !s.authenticateClient(req) {
		debug.Dprint("client auth required: " + req.RemoteAddr)
		s.clearClientAuthed(req.RemoteAddr)
		for _, challenge := range s.clientAuthChallenges(req) {
			rw.Header().Add("Proxy-Authenticate", challenge)
		}
		http.Error(rw, "proxy authentication required", http.StatusProxyAuthRequired)
		return
	}
	if req.Method == http.MethodConnect {
		s.handleConnect(rw, req)
		return
	}
	s.handleHTTP(rw, req)
}

func isQuitControlRequest(req *http.Request) bool {
	return req != nil && req.Method == http.MethodGet && req.URL != nil && !req.URL.IsAbs() && req.RequestURI == quitControlPath
}

func isLoopbackRemote(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func (s *Server) isClientAllowed(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if s.cfg.Allow != "" && s.cfg.Allow != "*.*.*.*" && s.cfg.Allow != "0.0.0.0/0" {
		if s.allowSet.Contains(ip) {
			return true
		}
		if !s.cfg.Hostonly || s.cfg.Gateway {
			return false
		}
	}
	if s.cfg.Hostonly {
		for _, hostIP := range s.cachedHostIPs() {
			if hostIP.Equal(ip) {
				return true
			}
		}
		return false
	}
	return true
}

// cachedHostIPs refreshes lazily rather than via a background goroutine so
// Servers created without Shutdown (common in tests) do not leak a ticker.
func (s *Server) cachedHostIPs() []net.IP {
	if e := s.hostIPs.Load(); e != nil && time.Now().Before(e.expires) {
		return e.ips
	}
	ips := config.GetHostIPs()
	s.hostIPs.Store(&hostIPEntry{ips: ips, expires: time.Now().Add(30 * time.Second)})
	return ips
}

// maintenanceLoop runs time-based housekeeping (proxy reload, Kerberos ticket
// refresh) off the request path. It stops when Shutdown closes s.closed.
func (s *Server) maintenanceLoop() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case now := <-t.C:
			if err := s.reloadProxyIfDue(); err != nil {
				debug.Dprintf("proxy reload failed, keeping previous: %v", err)
			}
			s.reloadKerberos(false)
			if s.sup != nil {
				s.sup.Tick(now)
			}
		case <-s.closed:
			return
		}
	}
}

func (s *Server) reloadProxyIfDue() error {
	return s.reloadProxy(context.Background(), false)
}

func (s *Server) refreshProxy(ctx context.Context) error {
	return s.reloadProxy(ctx, true)
}

func (s *Server) reloadProxy(ctx context.Context, force bool) error {
	if s.cfg.ProxyReload <= 0 {
		return nil
	}
	s.wmu.RLock()
	reloadable := s.proxyReloadableLocked()
	due := force || time.Since(s.lastReload) >= time.Duration(s.cfg.ProxyReload)*time.Second
	s.wmu.RUnlock()
	if !reloadable || !due {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// buildWproxy may do bounded network I/O (PAC/system discovery); keep it
	// outside the lock so request-path routing is never stalled by recovery.
	wp, err := buildWproxy(s.cfg)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		_ = wp.Close()
		return err
	}
	s.wmu.Lock()
	old := s.w
	changed := old == nil || wp.Mode != old.Mode || !equalServers(wp.Servers, old.Servers)
	s.w = wp
	s.lastReload = time.Now()
	var closeErr error
	if old != nil {
		closeErr = old.Close()
	}
	s.wmu.Unlock()
	if changed {
		// Drop keep-alive pools only when routing endpoints actually changed.
		s.clearTransports()
	}
	if closeErr != nil {
		return fmt.Errorf("close previous proxy resolver: %w", closeErr)
	}
	return nil
}

func equalServers(a, b []wproxy.Server) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (s *Server) proxyReloadableLocked() bool {
	if s.w == nil {
		return false
	}
	if s.w.Mode == wproxy.ModeConfig || s.w.Mode == wproxy.ModeEnv {
		return false
	}
	if s.w.Mode == wproxy.ModeConfigPAC && !isHTTPURL(s.cfg.PAC) {
		return false
	}
	return true
}

func isHTTPURL(rawurl string) bool {
	lower := strings.ToLower(rawurl)
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")
}

func (s *Server) reloadKerberos(force bool) {
	if s.krb != nil {
		s.krb.Check(force)
	}
}

func (s *Server) currentWproxy() *wproxy.Wproxy {
	s.wmu.RLock()
	defer s.wmu.RUnlock()
	return s.w
}

func (s *Server) findProxyForURL(rawurl string) ([]wproxy.Server, error) {
	s.wmu.RLock()
	defer s.wmu.RUnlock()
	if s.w == nil {
		return nil, errors.New("proxy resolver is not initialized")
	}
	proxies, _, _, err := s.w.FindProxyForURL(rawurl)
	return proxies, err
}

func (s *Server) handleHTTP(rw http.ResponseWriter, req *http.Request) {
	req = withInformationalResponseForwarding(req, rw)
	targetURL := req.URL.String()
	if !req.URL.IsAbs() {
		scheme := httpScheme
		targetURL = scheme + "://" + req.Host + req.URL.RequestURI()
	}
	debug.Dprint("HTTP target: " + targetURL)
	proxies, err := s.findProxyForURL(targetURL)
	if err != nil {
		s.recoverRuntimeOutcome(supervisor.OutcomeRouteFailure, wproxy.Server{})
		debug.Dprint("HTTP proxy lookup error: " + err.Error())
		http.Error(rw, err.Error(), http.StatusBadGateway)
		return
	}
	debug.Dprintf("HTTP proxies: %v", proxies)
	u, err := url.Parse(targetURL)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	// Buffer the body only when it may be re-sent (proxy fallback or a 407
	// auth retry); otherwise stream it straight through.
	var body *replayableBody
	if s.needsReplayableBody(req, proxies) {
		body, err = newReplayableBodyForRequest(req.Context(), req.Body, req.ContentLength)
		if err != nil {
			switch {
			case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
				debug.Dprint("HTTP replay capture canceled: " + err.Error())
				return
			case errors.Is(err, errReplayBodyTooLarge):
				http.Error(rw, err.Error(), http.StatusRequestEntityTooLarge)
			case errors.Is(err, errReplaySpoolQuota):
				http.Error(rw, err.Error(), http.StatusInsufficientStorage)
			default:
				http.Error(rw, err.Error(), http.StatusBadRequest)
			}
			return
		}
		defer func() {
			if err := body.Close(); err != nil {
				debug.Dprint("HTTP replay body cleanup failed: " + err.Error())
			}
		}()
	}
	incomingProxyAuth := req.Header.Get("Proxy-Authorization")
	resp, err := s.roundTripHTTPWithProxyFallback(req, u, body, targetURL, incomingProxyAuth, proxies)
	if err != nil {
		debug.Dprint("HTTP error: " + err.Error())
		http.Error(rw, err.Error(), http.StatusBadGateway)
		return
	}
	debug.Dprintf("HTTP response: %d %s", resp.StatusCode, targetURL)
	if resp.StatusCode == http.StatusSwitchingProtocols {
		s.handleHTTPUpgrade(rw, req, resp)
		return
	}
	defer resp.Body.Close()
	s.writeHTTPResponse(rw, resp)
}

// needsReplayableBody reports whether the request body must be buffered so it
// can be re-sent. That is the case when more than one attempt may consume it:
// either proxy fallback across candidates (http.Transport closes the body even
// on a failed attempt, so a streamed body cannot be replayed), or an upstream
// 407 auth retry that can actually produce credentials.
func (s *Server) needsReplayableBody(req *http.Request, proxies []wproxy.Server) bool {
	if req.Body == nil || req.Body == http.NoBody {
		return false
	}
	candidates := proxyCandidates(proxies)
	if len(candidates) == 0 {
		return false
	}
	if len(candidates) > 1 {
		return true
	}
	if candidates[0] == wproxy.Direct {
		return false
	}
	if req.Header.Get("Proxy-Authorization") != "" {
		return true // passthrough auth is retried on 407
	}
	if len(upstreamAuthModes(s.cfg.Auth)) == 0 {
		return false // auth=NONE: never retried
	}
	if s.cfg.Username != "" && s.cfg.Password != "" {
		return true
	}
	return runtime.GOOS == goosWindows // SSPI may authenticate without configured credentials
}

func (s *Server) roundTripHTTPWithProxyFallback(req *http.Request, u *url.URL, body *replayableBody, targetURL, incomingProxyAuth string, proxies []wproxy.Server) (*http.Response, error) {
	candidates := s.orderedProxyCandidates(proxyCandidates(proxies))
	var lastErr error
	for _, candidate := range candidates {
		if candidate == wproxy.Direct {
			debug.Dprint("HTTP: trying direct connection to " + targetURL)
		} else {
			debug.Dprintf("HTTP: trying proxy %s:%d for %s", candidate.Host, candidate.Port, targetURL)
		}
		transport := s.httpTransportForProxy(candidate)
		usesUpstreamProxy := candidate != wproxy.Direct
		outReq, err := s.newOutboundRequest(req, u, body, "")
		if err != nil {
			if req.Context().Err() != nil {
				s.recordRuntimeOutcome(supervisor.OutcomeClientCancelled, candidate)
				return nil, req.Context().Err()
			}
			lastErr = err
			continue
		}
		if usesUpstreamProxy {
			if auth := upstreamProxyAuthHeader(s.cfg, req.Method, targetURL, "", incomingProxyAuth); auth != "" {
				outReq.Header.Set("Proxy-Authorization", auth)
			}
		}
		resp, err := transport.RoundTrip(outReq)
		if err != nil {
			kind := classifyProxyTransportOutcome(req.Context(), candidate, err)
			s.recoverRuntimeOutcome(kind, candidate)
			if req.Context().Err() != nil {
				return nil, req.Context().Err()
			}
			debug.Dprint("HTTP: proxy attempt failed: " + err.Error())
			lastErr = err
			continue
		}

		// Any syntactically valid HTTP response proves the transport path itself
		// is usable, including origin 4xx/5xx and upstream 407 responses.
		s.recordRuntimeOutcome(supervisor.OutcomeSuccess, candidate)

		if usesUpstreamProxy && resp.StatusCode == http.StatusProxyAuthRequired {
			resp, err = s.retryHTTPProxyAuth(transport, req, u, body, targetURL, incomingProxyAuth, resp)
			if err != nil {
				s.recoverRuntimeOutcome(classifyProxyTransportOutcome(req.Context(), candidate, err), candidate)
				return nil, err
			}
			if resp != nil && resp.StatusCode == http.StatusProxyAuthRequired {
				s.recordRuntimeOutcome(supervisor.OutcomeAuthExhausted, candidate)
			} else {
				s.recordRuntimeOutcome(supervisor.OutcomeSuccess, candidate)
			}
		}
		return resp, nil
	}
	if lastErr == nil {
		lastErr = errors.New("no proxy candidates")
	}
	debug.Dprint("HTTP: all candidates failed: " + lastErr.Error())
	return nil, lastErr
}

func proxyCandidates(proxies []wproxy.Server) []wproxy.Server {
	return proxies
}

func (s *Server) newOutboundRequest(req *http.Request, u *url.URL, body *replayableBody, proxyAuth string) (*http.Request, error) {
	outReq := req.Clone(req.Context())
	outReq.URL = u
	outReq.Host = u.Host
	outReq.RequestURI = ""
	if body != nil {
		rc, err := body.Open()
		if err != nil {
			return nil, err
		}
		outReq.Body = rc
		outReq.ContentLength = body.Size()
	} // else: stream req.Body as-is (single attempt, no replay needed)
	outReq.Header = cloneHeader(req.Header)
	upgrade := requestedUpgrade(req)
	stripIntermediaryHeaders(outReq.Header, true)
	if upgrade != "" {
		outReq.Header.Set(headerConnection, "Upgrade")
		outReq.Header.Set("Upgrade", upgrade)
	}
	appendVia(outReq.Header)
	if s.cfg.UserAgent != "" {
		outReq.Header.Set("User-Agent", s.cfg.UserAgent)
	}
	if proxyAuth != "" {
		outReq.Header.Set("Proxy-Authorization", proxyAuth)
	}
	return outReq, nil
}

func cloneHeader(h http.Header) http.Header {
	out := make(http.Header, len(h))
	copyHeader(out, h)
	return out
}

func copyHeader(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}
