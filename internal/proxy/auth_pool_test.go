package proxy

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"sync"
	"testing"

	"github.com/khanhkit/pxgo/internal/config"
	"github.com/khanhkit/pxgo/internal/wproxy"
)

func TestAPISS0003SequentialNTLMRequestsReuseAuthenticatedConnection(t *testing.T) {
	type connState struct {
		authenticated bool
	}
	var (
		mu           sync.Mutex
		states       = map[string]*connState{}
		challenges   int
		type1Count   int
		type3Count   int
		requestCount int
	)

	parent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requestCount++
		state := states[r.RemoteAddr]
		if state == nil {
			state = &connState{}
			states[r.RemoteAddr] = state
		}
		authenticated := state.authenticated
		mu.Unlock()

		if authenticated {
			fmt.Fprint(w, "reused")
			return
		}

		auth := r.Header.Get("Proxy-Authorization")
		if auth == "" {
			mu.Lock()
			challenges++
			mu.Unlock()
			w.Header().Set("Proxy-Authenticate", "NTLM")
			http.Error(w, "auth required", http.StatusProxyAuthRequired)
			return
		}
		switch ntlmMessageType(t, auth) {
		case 1:
			mu.Lock()
			challenges++
			type1Count++
			mu.Unlock()
			w.Header().Set("Proxy-Authenticate", "NTLM "+minimalNTLMChallenge(t))
			http.Error(w, "challenge", http.StatusProxyAuthRequired)
		case 3:
			mu.Lock()
			type3Count++
			state.authenticated = true
			mu.Unlock()
			fmt.Fprint(w, "authenticated")
		default:
			http.Error(w, "unexpected auth token", http.StatusBadRequest)
		}
	}))
	defer parent.Close()

	parentURL, err := url.Parse(parent.URL)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Server = parentURL.Host
	cfg.Auth = "NTLM"
	cfg.Username = "DOMAIN\\test"
	cfg.Password = "12345"
	child := startTestProxy(t, cfg)
	client := proxyClient(t, child.Port())

	const applicationRequests = 100
	for i := 0; i < applicationRequests; i++ {
		resp, err := client.Get("http://ntlm-reuse.example.test/resource")
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d status=%s body=%q", i+1, resp.Status, data)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if challenges != 2 {
		t.Fatalf("407 challenge count=%d want=2; second application request re-authenticated", challenges)
	}
	if type1Count != 1 || type3Count != 1 {
		t.Fatalf("NTLM handshakes type1=%d type3=%d want 1/1", type1Count, type3Count)
	}
	wantRequests := applicationRequests + 2 // two 407 handshake requests plus one upstream request per application request
	if requestCount != wantRequests {
		t.Fatalf("upstream request count=%d want=%d", requestCount, wantRequests)
	}
	if len(states) != 2 {
		t.Fatalf("upstream TCP connections=%d want=2 (initial challenge connection + reusable authenticated connection)", len(states))
	}
}

func TestAPISS0003ConcurrentNTLMRequestsShareAuthenticatedPool(t *testing.T) {
	type connState struct {
		authenticated bool
	}
	var (
		mu         sync.Mutex
		states     = map[string]*connState{}
		type1Count int
		type3Count int
	)

	parent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		state := states[r.RemoteAddr]
		if state == nil {
			state = &connState{}
			states[r.RemoteAddr] = state
		}
		authenticated := state.authenticated
		mu.Unlock()

		if authenticated {
			fmt.Fprint(w, "reused")
			return
		}
		auth := r.Header.Get("Proxy-Authorization")
		if auth == "" {
			w.Header().Set("Proxy-Authenticate", "NTLM")
			http.Error(w, "auth required", http.StatusProxyAuthRequired)
			return
		}
		switch ntlmMessageType(t, auth) {
		case 1:
			mu.Lock()
			type1Count++
			mu.Unlock()
			w.Header().Set("Proxy-Authenticate", "NTLM "+minimalNTLMChallenge(t))
			http.Error(w, "challenge", http.StatusProxyAuthRequired)
		case 3:
			mu.Lock()
			type3Count++
			state.authenticated = true
			mu.Unlock()
			fmt.Fprint(w, "authenticated")
		default:
			http.Error(w, "unexpected auth token", http.StatusBadRequest)
		}
	}))
	defer parent.Close()

	parentURL, err := url.Parse(parent.URL)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Server = parentURL.Host
	cfg.Auth = "NTLM"
	cfg.Username = "DOMAIN\\test"
	cfg.Password = "12345"
	child := startTestProxy(t, cfg)
	client := proxyClient(t, child.Port())

	const requests = 24
	errs := make(chan error, requests)
	var wg sync.WaitGroup
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := client.Get("http://ntlm-concurrent.example.test/resource")
			if err != nil {
				errs <- err
				return
			}
			_, readErr := io.Copy(io.Discard, resp.Body)
			closeErr := resp.Body.Close()
			if readErr != nil {
				errs <- readErr
				return
			}
			if closeErr != nil {
				errs <- closeErr
				return
			}
			if resp.StatusCode != http.StatusOK {
				errs <- fmt.Errorf("status=%s", resp.Status)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if type1Count != 1 || type3Count != 1 {
		t.Fatalf("concurrent NTLM handshakes type1=%d type3=%d want 1/1", type1Count, type3Count)
	}
	authenticatedConnections := 0
	for _, state := range states {
		if state.authenticated {
			authenticatedConnections++
		}
	}
	if authenticatedConnections != 1 {
		t.Fatalf("authenticated upstream connections=%d want 1", authenticatedConnections)
	}
}

func TestAPISS0003PooledNTLMReauthenticatesAfterConnectionClose(t *testing.T) {
	type connState struct {
		authenticated bool
	}
	var (
		mu         sync.Mutex
		states     = map[string]*connState{}
		type1Count int
		type3Count int
		closeFirst = true
	)

	parent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		state := states[r.RemoteAddr]
		if state == nil {
			state = &connState{}
			states[r.RemoteAddr] = state
		}
		authenticated := state.authenticated
		mu.Unlock()

		if authenticated {
			fmt.Fprint(w, "reused")
			return
		}
		auth := r.Header.Get("Proxy-Authorization")
		if auth == "" {
			w.Header().Set("Proxy-Authenticate", "NTLM")
			http.Error(w, "auth required", http.StatusProxyAuthRequired)
			return
		}
		switch ntlmMessageType(t, auth) {
		case 1:
			mu.Lock()
			type1Count++
			mu.Unlock()
			w.Header().Set("Proxy-Authenticate", "NTLM "+minimalNTLMChallenge(t))
			http.Error(w, "challenge", http.StatusProxyAuthRequired)
		case 3:
			mu.Lock()
			type3Count++
			state.authenticated = true
			shouldClose := closeFirst
			closeFirst = false
			mu.Unlock()
			if shouldClose {
				w.Header().Set("Connection", "close")
			}
			fmt.Fprint(w, "authenticated")
		default:
			http.Error(w, "unexpected auth token", http.StatusBadRequest)
		}
	}))
	defer parent.Close()

	parentURL, err := url.Parse(parent.URL)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Server = parentURL.Host
	cfg.Auth = "NTLM"
	cfg.Username = "DOMAIN\\test"
	cfg.Password = "12345"
	child := startTestProxy(t, cfg)
	client := proxyClient(t, child.Port())

	for i := 0; i < 3; i++ {
		resp, err := client.Get("http://ntlm-reconnect.example.test/resource")
		if err != nil {
			t.Fatal(err)
		}
		_, readErr := io.Copy(io.Discard, resp.Body)
		closeErr := resp.Body.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d status=%s", i+1, resp.Status)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if type1Count != 2 || type3Count != 2 {
		t.Fatalf("reauth handshakes type1=%d type3=%d want 2/2", type1Count, type3Count)
	}
}

func TestAPISS0003AuthIdentityFingerprintSeparatesCredentials(t *testing.T) {
	a := config.Default()
	a.Username = "DOMAIN\\alice"
	a.Password = "one"
	b := a
	b.Password = "two"
	c := a
	c.Username = "DOMAIN\\bob"

	ka := upstreamConnectionAuthIdentity(a, "")
	kb := upstreamConnectionAuthIdentity(b, "")
	kc := upstreamConnectionAuthIdentity(c, "")
	if ka == "" || kb == "" || kc == "" {
		t.Fatal("explicit credential identity fingerprint is empty")
	}
	if ka == kb || ka == kc || kb == kc {
		t.Fatalf("credential identities collided: a=%q b=%q c=%q", ka, kb, kc)
	}
	partial := config.Default()
	partial.Username = "DOMAIN\\partial"
	if got := upstreamConnectionAuthIdentity(partial, ""); got != "" {
		t.Fatalf("partial credential identity=%q want empty", got)
	}
	anonymous := upstreamConnectionAuthIdentity(config.Default(), "")
	if got := upstreamConnectionAuthIdentity(config.Default(), "NTLM passthrough-token"); got != anonymous {
		t.Fatalf("passthrough token changed authoritative identity: got=%q base=%q", got, anonymous)
	}
	if runtime.GOOS == goosWindows {
		if anonymous != "sspi:current-user" {
			t.Fatalf("Windows anonymous identity=%q want sspi:current-user", anonymous)
		}
	} else if anonymous != "" {
		t.Fatalf("non-Windows anonymous identity=%q want empty", anonymous)
	}
}

func TestAPISS0003TransportCacheStrictCapUnderConcurrency(t *testing.T) {
	cache := newBoundedTransportCache(8)
	var wg sync.WaitGroup
	for i := 0; i < 128; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("route-%03d", i)
			cache.getOrCreate(key, func() *transportCacheEntry {
				return &transportCacheEntry{transport: &http.Transport{}}
			})
		}(i)
	}
	wg.Wait()
	if got := cache.len(); got != 8 {
		t.Fatalf("cache size=%d want strict cap 8", got)
	}
	cache.clear()
}

func TestAPISS0003TransportRouteKeyCanonicalizesCase(t *testing.T) {
	a := wproxy.Server{Scheme: "HTTP", Host: "Proxy.Example.COM", Port: 3128}
	b := wproxy.Server{Scheme: "http", Host: "proxy.example.com", Port: 3128}
	if ka, kb := proxyTransportRouteKey(a), proxyTransportRouteKey(b); ka != kb {
		t.Fatalf("route keys differ: %q != %q", ka, kb)
	}
}

func TestAPISS0003TransportCacheClosesEvictedEntry(t *testing.T) {
	cache := newBoundedTransportCache(1)
	closed := 0
	cache.getOrCreate("first", func() *transportCacheEntry {
		return &transportCacheEntry{
			transport: &http.Transport{},
			onClose: func() {
				closed++
			},
		}
	})
	cache.getOrCreate("second", func() *transportCacheEntry {
		return &transportCacheEntry{transport: &http.Transport{}}
	})
	if got := cache.len(); got != 1 {
		t.Fatalf("cache size=%d want 1", got)
	}
	if closed != 1 {
		t.Fatalf("evicted entry closes=%d want 1", closed)
	}
	cache.clear()
}

func TestAPISS0003TransportCacheClosesLosingCandidate(t *testing.T) {
	cache := newBoundedTransportCache(4)
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		closed int
	)

	factory := func() *transportCacheEntry {
		entered <- struct{}{}
		<-release
		return &transportCacheEntry{
			transport: &http.Transport{},
			onClose: func() {
				mu.Lock()
				closed++
				mu.Unlock()
			},
		}
	}

	wg.Add(2)
	go func() {
		defer wg.Done()
		cache.getOrCreate("same", factory)
	}()
	go func() {
		defer wg.Done()
		cache.getOrCreate("same", factory)
	}()

	<-entered
	<-entered
	close(release)
	wg.Wait()

	if got := cache.len(); got != 1 {
		t.Fatalf("cache size=%d want 1", got)
	}
	mu.Lock()
	gotClosed := closed
	mu.Unlock()
	if gotClosed != 1 {
		t.Fatalf("losing candidate closes=%d want 1", gotClosed)
	}
	cache.clear()
}
