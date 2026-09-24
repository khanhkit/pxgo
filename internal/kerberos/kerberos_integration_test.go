//go:build kerberos_integration

package kerberos

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestKerberosIntegrationKDC(t *testing.T) {
	requireIntegrationEnv(t, "PXGO_KERBEROS_PRINCIPAL", "PXGO_KERBEROS_PASSWORD", "KRB5_CONFIG")
	principal := os.Getenv("PXGO_KERBEROS_PRINCIPAL")
	password := os.Getenv("PXGO_KERBEROS_PASSWORD")
	isHeimdal := strings.EqualFold(os.Getenv("PXGO_KERBEROS_FLAVOR"), "heimdal")
	mgr := New(principal, func() *string { return &password }, isHeimdal)
	t.Cleanup(mgr.Cleanup)

	if ok := mgr.KinitWithPassword(); !ok {
		t.Fatalf("kinit failed; backoff=%s", mgr.Backoff)
	}
	if mgr.TicketExpiry.IsZero() || time.Now().After(mgr.TicketExpiry) {
		t.Fatalf("ticket expiry not set to a future time: %s", mgr.TicketExpiry)
	}
	if !mgr.KlistValid() {
		t.Fatal("klist validity check failed after kinit")
	}
	if !mgr.KinitRenew() {
		t.Fatal("kinit renewal failed")
	}
	ccache := strings.TrimPrefix(mgr.CCacheName, "FILE:")
	if _, err := os.Stat(ccache); err != nil {
		t.Fatalf("ccache missing before cleanup: %v", err)
	}
	mgr.Cleanup()
	if _, err := os.Stat(ccache); !os.IsNotExist(err) {
		t.Fatalf("ccache still exists after cleanup: %v", err)
	}
}

func TestKerberosIntegrationSPNEGOToken(t *testing.T) {
	requireIntegrationEnv(t, "PXGO_KERBEROS_PRINCIPAL", "PXGO_KERBEROS_PASSWORD", "KRB5_CONFIG", "PXGO_KERBEROS_PROXY_HOST")
	proxyHost := strings.TrimSpace(os.Getenv("PXGO_KERBEROS_PROXY_HOST"))
	principal := os.Getenv("PXGO_KERBEROS_PRINCIPAL")
	password := os.Getenv("PXGO_KERBEROS_PASSWORD")
	isHeimdal := strings.EqualFold(os.Getenv("PXGO_KERBEROS_FLAVOR"), "heimdal")
	mgr := New(principal, func() *string { return &password }, isHeimdal)
	t.Cleanup(mgr.Cleanup)

	if ok := mgr.KinitWithPassword(); !ok {
		t.Fatalf("kinit failed before SPNEGO token acquisition; backoff=%s", mgr.Backoff)
	}
	token, err := mgr.SPNEGOToken(proxyHost)
	if err != nil {
		t.Fatalf("SPNEGOToken(%q): %v", proxyHost, err)
	}
	if len(token) == 0 {
		t.Fatal("SPNEGOToken returned an empty token")
	}
}

func requireIntegrationEnv(t *testing.T, names ...string) {
	t.Helper()
	for _, name := range names {
		if os.Getenv(name) == "" {
			t.Fatalf("%s must be set when kerberos_integration tests execute", name)
		}
	}
}

func TestKerberosIntegrationWrongPassword(t *testing.T) {
	requireIntegrationEnv(t, "PXGO_KERBEROS_PRINCIPAL", "KRB5_CONFIG")
	principal := os.Getenv("PXGO_KERBEROS_PRINCIPAL")
	wrong := "definitely-wrong-password"
	isHeimdal := strings.EqualFold(os.Getenv("PXGO_KERBEROS_FLAVOR"), "heimdal")
	mgr := New(principal, func() *string { return &wrong }, isHeimdal)
	t.Cleanup(mgr.Cleanup)
	if mgr.KinitWithPassword() {
		t.Fatal("kinit unexpectedly succeeded with wrong password")
	}
	if mgr.Backoff == 0 {
		t.Fatal("wrong password should set backoff")
	}
}
