//go:build windows

package systemproxy

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
	"unsafe"
)

const nativeWinHTTPEnv = "PXGO_WINHTTP_NATIVE"

func requireNativeWinHTTP(t *testing.T) {
	t.Helper()
	if os.Getenv(nativeWinHTTPEnv) != "1" {
		t.Skipf("set %s=1 on an authorized Windows runner to execute native WinHTTP fixtures", nativeWinHTTPEnv)
	}
}

func processHandleCount(t *testing.T) uint32 {
	t.Helper()
	getCurrentProcess := kernelDLL.NewProc("GetCurrentProcess")
	getProcessHandleCount := kernelDLL.NewProc("GetProcessHandleCount")

	process, _, _ := getCurrentProcess.Call()
	var count uint32
	ok, _, callErr := getProcessHandleCount.Call(process, uintptr(unsafe.Pointer(&count)))
	if ok == 0 {
		t.Fatalf("GetProcessHandleCount failed: %v", callErr)
	}
	return count
}

func newPACServer(t *testing.T, proxy string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ns-proxy-autoconfig")
		_, _ = fmt.Fprintf(w, "function FindProxyForURL(url, host) { return 'PROXY %s'; }", proxy)
	}))
}

func TestTCWINPACINT009NativeExplicitPAC(t *testing.T) {
	requireNativeWinHTTP(t)

	const wantProxy = "127.0.0.1:18080"
	pacServer := newPACServer(t, wantProxy)
	defer pacServer.Close()

	resolver, err := NewResolver()
	if err != nil {
		t.Fatalf("NewResolver() error = %v", err)
	}
	defer resolver.Close()

	got, err := resolver.ResolveProxyForURL(
		"https://example.com/",
		Config{IsPAC: true, PACURL: pacServer.URL},
	)
	if err != nil {
		t.Fatalf("ResolveProxyForURL() error = %v", err)
	}
	if !strings.Contains(strings.ToLower(got), strings.ToLower(wantProxy)) {
		t.Fatalf("ResolveProxyForURL() = %q, want proxy containing %q", got, wantProxy)
	}
}

func TestTCWINPACNEG010NativePACFailureIsNotDirect(t *testing.T) {
	requireNativeWinHTTP(t)

	resolver, err := NewResolver()
	if err != nil {
		t.Fatalf("NewResolver() error = %v", err)
	}
	defer resolver.Close()

	got, err := resolver.ResolveProxyForURL(
		"https://example.com/",
		Config{IsPAC: true, PACURL: "http://127.0.0.1:1/unreachable.pac"},
	)
	if err == nil {
		t.Fatalf("ResolveProxyForURL() error = nil, result = %q; want native PAC failure", got)
	}
	if strings.EqualFold(strings.TrimSpace(got), "DIRECT") {
		t.Fatalf("native PAC failure was rewritten to DIRECT")
	}
}

func TestTCWINPACSOAK011NativeHandleCountReturnsNearBaseline(t *testing.T) {
	requireNativeWinHTTP(t)

	const wantProxy = "127.0.0.1:18081"
	pacServer := newPACServer(t, wantProxy)
	defer pacServer.Close()

	runtime.GC()
	time.Sleep(100 * time.Millisecond)
	baseline := processHandleCount(t)

	resolver, err := NewResolver()
	if err != nil {
		t.Fatalf("NewResolver() error = %v", err)
	}

	for i := 0; i < 200; i++ {
		got, err := resolver.ResolveProxyForURL(
			fmt.Sprintf("https://example.com/%d", i),
			Config{IsPAC: true, PACURL: pacServer.URL},
		)
		if err != nil {
			_ = resolver.Close()
			t.Fatalf("iteration %d ResolveProxyForURL() error = %v", i, err)
		}
		if !strings.Contains(strings.ToLower(got), strings.ToLower(wantProxy)) {
			_ = resolver.Close()
			t.Fatalf("iteration %d result = %q, want proxy containing %q", i, got, wantProxy)
		}
	}
	if err := resolver.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	runtime.GC()
	time.Sleep(250 * time.Millisecond)
	final := processHandleCount(t)
	if final > baseline+16 {
		t.Fatalf("process handle count grew from %d to %d after resolver close; allowed transient delta is 16", baseline, final)
	}
	t.Logf("native WinHTTP handle-count soak: baseline=%d final=%d", baseline, final)
}

func TestTCWINPACINT012NativeWPADAutodetect(t *testing.T) {
	requireNativeWinHTTP(t)
	wantProxy := strings.TrimSpace(os.Getenv("PXGO_WPAD_EXPECT_PROXY"))
	if wantProxy == "" {
		t.Skip("set PXGO_WPAD_EXPECT_PROXY only on an isolated Windows runner with controlled WPAD DNS/DHCP")
	}

	resolver, err := NewResolver()
	if err != nil {
		t.Fatalf("NewResolver() error = %v", err)
	}
	defer resolver.Close()

	got, err := resolver.ResolveProxyForURL(
		"https://example.com/",
		Config{AutoDetect: true},
	)
	if err != nil {
		t.Fatalf("WPAD ResolveProxyForURL() error = %v", err)
	}
	if !strings.Contains(strings.ToLower(got), strings.ToLower(wantProxy)) {
		t.Fatalf("WPAD result = %q, want proxy containing %q", got, wantProxy)
	}
}
