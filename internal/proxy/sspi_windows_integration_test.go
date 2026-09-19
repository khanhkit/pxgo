//go:build windows

package proxy

import (
	"bytes"
	"encoding/asn1"
	"encoding/base64"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/alexbrainman/sspi/negotiate"
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

func requireRealADSSPIFixture(t *testing.T) string {
	t.Helper()
	if os.Getenv("PXGO_SSPI_AD") != "1" {
		t.Skip("set PXGO_SSPI_AD=1 only on the protected domain-joined pxgo-ad runner")
	}
	host := strings.TrimSpace(os.Getenv("PXGO_SSPI_AD_PROXY_HOST"))
	if host == "" {
		t.Fatal("PXGO_SSPI_AD_PROXY_HOST is required for real AD SSPI verification")
	}

	upnOut, err := exec.Command("whoami", "/upn").CombinedOutput()
	if err != nil {
		t.Fatalf("read domain UPN: %v: %s", err, strings.TrimSpace(string(upnOut)))
	}
	upn := strings.TrimSpace(string(upnOut))
	if upn == "" || !strings.Contains(upn, "@") {
		t.Fatalf("runner identity %q is not a domain UPN", upn)
	}

	spn := proxySPN(host)
	klistOut, err := exec.Command("klist", "get", spn).CombinedOutput()
	if err != nil {
		t.Fatalf("obtain Kerberos service ticket for %s: %v: %s", spn, err, strings.TrimSpace(string(klistOut)))
	}
	if !strings.Contains(strings.ToLower(string(klistOut)), strings.ToLower(spn)) {
		t.Fatalf("klist output does not prove service ticket for %s:\n%s", spn, klistOut)
	}
	t.Logf("real AD runner identity=%s service=%s", upn, spn)
	return host
}

func managedSSPISessionComplete(s authSession) bool {
	managed, ok := s.(*sspiAuthSession)
	if !ok {
		return false
	}
	managed.mu.Lock()
	defer managed.mu.Unlock()
	return managed.complete
}

func containsNTLMSSP(token []byte) bool {
	return bytes.Contains(token, []byte{'N', 'T', 'L', 'M', 'S', 'S', 'P', 0})
}

// TC-SSPI-WIN-AD-010
//
// This test is intentionally environment-gated. It must run only on a
// domain-joined Windows runner whose current service identity owns the
// HTTP/<PXGO_SSPI_AD_PROXY_HOST> SPN. klist proves that the KDC issues a real
// Kerberos service ticket for the target. The PxGo Negotiate session is then
// completed against a native inbound Negotiate context running as that same
// service identity. A direct NTLMSSP token is rejected explicitly.
func TestTCSSPIWINAD010RealADNegotiateUsesKerberos(t *testing.T) {
	host := requireRealADSSPIFixture(t)
	spn := proxySPN(host)

	session, err := newSSPISession(authSchemeNeg, host)
	if err != nil {
		t.Fatalf("create PxGo Negotiate session for %s: %v", spn, err)
	}
	defer func() {
		if err := session.Close(); err != nil {
			t.Errorf("close PxGo Negotiate session: %v", err)
		}
	}()

	initialHeader, err := session.Negotiate()
	if err != nil {
		t.Fatalf("create initial Negotiate token: %v", err)
	}
	initialToken := decodeProxyAuthToken(t, initialHeader, authSchemeNeg)
	if containsNTLMSSP(initialToken) {
		t.Fatal("Negotiate initial token fell back to NTLMSSP instead of Kerberos")
	}
	kerberosOID, err := asn1.Marshal(asn1.ObjectIdentifier{1, 2, 840, 113554, 1, 2, 2})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(initialToken, kerberosOID) {
		t.Fatalf("Negotiate token for %s does not advertise the Kerberos mechanism OID", spn)
	}

	serverCred, err := negotiate.AcquireServerCredentials("")
	if err != nil {
		t.Fatalf("acquire inbound Negotiate credentials for current SPN-owning identity: %v", err)
	}
	defer serverCred.Release()

	serverCtx, serverDone, toClient, err := negotiate.NewServerContext(serverCred, initialToken)
	if err != nil {
		t.Fatalf("accept initial Negotiate token for %s: %v", spn, err)
	}
	defer serverCtx.Release()

	const maxLegs = 8
	for leg := 0; leg < maxLegs && (!serverDone || !managedSSPISessionComplete(session)); leg++ {
		if len(toClient) == 0 {
			t.Fatalf("Negotiate exchange stopped before both sides completed at leg %d", leg)
		}

		clientHeader, err := session.Authenticate(authSchemeNeg + " " + base64.StdEncoding.EncodeToString(toClient))
		if err != nil {
			t.Fatalf("PxGo Negotiate continuation leg %d: %v", leg, err)
		}
		toServer := decodeProxyAuthToken(t, clientHeader, authSchemeNeg)
		if containsNTLMSSP(toServer) {
			t.Fatalf("Negotiate continuation leg %d fell back to NTLMSSP", leg)
		}

		serverDone, toClient, err = serverCtx.Update(toServer)
		if err != nil {
			t.Fatalf("server Negotiate continuation leg %d: %v", leg, err)
		}
	}
	if !serverDone {
		t.Fatal("real AD Negotiate server context did not complete")
	}
	if !managedSSPISessionComplete(session) {
		t.Fatal("PxGo Negotiate client session did not complete")
	}

	username, err := serverCtx.GetUsername()
	if err != nil {
		t.Fatalf("read authenticated domain username: %v", err)
	}
	if strings.TrimSpace(username) == "" {
		t.Fatal("real AD Negotiate server context authenticated an empty username")
	}

	tickets, err := exec.Command("klist").CombinedOutput()
	if err != nil {
		t.Fatalf("inspect Kerberos ticket cache after Negotiate: %v: %s", err, strings.TrimSpace(string(tickets)))
	}
	if !strings.Contains(strings.ToLower(string(tickets)), strings.ToLower(spn)) {
		t.Fatalf("Kerberos cache does not contain %s after Negotiate:\n%s", spn, tickets)
	}
	t.Logf("real AD Negotiate/Kerberos exchange complete: service=%s user=%s", spn, username)
}
