//go:build windows

package proxy

import (
	"encoding/base64"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/alexbrainman/sspi/ntlm"
	"golang.org/x/sys/windows"
)

func requireNativeSSPIFixture(t *testing.T) {
	t.Helper()
	if os.Getenv("PXGO_SSPI_NATIVE") == "1" || os.Getenv("GITHUB_ACTIONS") == "true" {
		return
	}
	t.Skip("set PXGO_SSPI_NATIVE=1 locally or run on authorized GitHub Actions Windows to execute native SSPI integration")
}

func decodeProxyAuthToken(t *testing.T, header, wantScheme string) []byte {
	t.Helper()
	scheme, encoded, found := strings.Cut(strings.TrimSpace(header), " ")
	if !found || !strings.EqualFold(scheme, wantScheme) || strings.TrimSpace(encoded) == "" {
		t.Fatalf("proxy auth header=%q, want %s <token>", header, wantScheme)
	}
	token, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		t.Fatalf("decode %s token: %v", wantScheme, err)
	}
	return token
}

var (
	kernel32                  = windows.NewLazySystemDLL("kernel32.dll")
	procGetProcessHandleCount = kernel32.NewProc("GetProcessHandleCount")
)

func currentProcessHandleCount() (uint32, error) {
	process, err := windows.GetCurrentProcess()
	if err != nil {
		return 0, err
	}
	var count uint32
	r1, _, callErr := procGetProcessHandleCount.Call(
		uintptr(process),
		uintptr(unsafe.Pointer(&count)),
	)
	if r1 == 0 {
		return 0, callErr
	}
	return count, nil
}

func openAndCloseNativeNTLMSession() error {
	session, err := newSSPISession("NTLM", "proxy.fixture.invalid")
	if err != nil {
		return err
	}
	if _, err := session.Negotiate(); err != nil {
		_ = session.Close()
		return err
	}
	return session.Close()
}

// TC-SSPI-WIN-008
func TestTCSSPIWININT008NativeNTLMRoundTrip(t *testing.T) {
	requireNativeSSPIFixture(t)

	session, err := newSSPISession("NTLM", "proxy.fixture.invalid")
	if err != nil {
		t.Fatalf("create native NTLM session: %v", err)
	}
	defer session.Close()

	initialHeader, err := session.Negotiate()
	if err != nil {
		t.Fatalf("initial NTLM token: %v", err)
	}
	initialToken := decodeProxyAuthToken(t, initialHeader, authNTLM)

	serverCred, err := ntlm.AcquireServerCredentials()
	if err != nil {
		t.Fatalf("acquire NTLM server credentials: %v", err)
	}
	defer serverCred.Release()

	serverCtx, challengeToken, err := ntlm.NewServerContext(serverCred, initialToken)
	if err != nil {
		t.Fatalf("create NTLM server context: %v", err)
	}
	defer serverCtx.Release()

	finalHeader, err := session.Authenticate(authNTLM + " " + base64.StdEncoding.EncodeToString(challengeToken))
	if err != nil {
		t.Fatalf("complete native NTLM client exchange: %v", err)
	}
	finalToken := decodeProxyAuthToken(t, finalHeader, authNTLM)
	if err := serverCtx.Update(finalToken); err != nil {
		t.Fatalf("complete native NTLM server exchange: %v", err)
	}
}

// TC-SSPI-WIN-SOAK-009
func TestTCSSPIWINSOAK009NativeHandleCountStable(t *testing.T) {
	requireNativeSSPIFixture(t)

	// Warm native DLL/package caches before taking the baseline.
	for i := 0; i < 16; i++ {
		if err := openAndCloseNativeNTLMSession(); err != nil {
			t.Fatalf("warmup iteration %d: %v", i, err)
		}
	}
	runtime.GC()
	time.Sleep(50 * time.Millisecond)

	before, err := currentProcessHandleCount()
	if err != nil {
		t.Fatalf("read process handle count before soak: %v", err)
	}

	const iterations = 1000
	for i := 0; i < iterations; i++ {
		if err := openAndCloseNativeNTLMSession(); err != nil {
			t.Fatalf("soak iteration %d: %v", i, err)
		}
	}
	runtime.GC()
	time.Sleep(100 * time.Millisecond)

	after, err := currentProcessHandleCount()
	if err != nil {
		t.Fatalf("read process handle count after soak: %v", err)
	}
	const tolerance = uint32(16)
	if after > before+tolerance {
		t.Fatalf("native SSPI handle count grew from %d to %d after %d closed sessions (tolerance %d)", before, after, iterations, tolerance)
	}
	t.Logf("native SSPI handle count stable: before=%d after=%d iterations=%d tolerance=%d", before, after, iterations, tolerance)
}
