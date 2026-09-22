package proxy

import (
	"errors"
	"strings"
	"testing"

	"github.com/khanhkit/pxgo/internal/config"
	"github.com/khanhkit/pxgo/internal/kerberos"
)

func TestAPISS0019KerberosProxyFeatureDisabledIsValid(t *testing.T) {
	cfg := config.Default()
	if err := validateKerberosFeatureForOS(cfg, "linux"); err != nil {
		t.Fatalf("disabled kerberos feature rejected: %v", err)
	}
}

func TestAPISS0019KerberosProxyFeatureRejectsUnixTicketOnlyMode(t *testing.T) {
	cfg := config.Default()
	cfg.Kerberos = true

	for _, goos := range []string{"linux", "darwin"} {
		t.Run(goos, func(t *testing.T) {
			err := validateKerberosFeatureForOS(cfg, goos)
			if !errors.Is(err, kerberos.ErrProxyAuthUnsupported) {
				t.Fatalf("err=%v, want ErrProxyAuthUnsupported", err)
			}
			if !strings.Contains(strings.ToLower(err.Error()), "gssapi") {
				t.Fatalf("error must explain missing GSSAPI consumer: %q", err)
			}
		})
	}
}

func TestAPISS0019KerberosProxyFeatureRejectsWindowsFlag(t *testing.T) {
	cfg := config.Default()
	cfg.Kerberos = true

	err := validateKerberosFeatureForOS(cfg, "windows")
	if !errors.Is(err, kerberos.ErrProxyAuthUnsupported) {
		t.Fatalf("err=%v, want ErrProxyAuthUnsupported", err)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "sspi") {
		t.Fatalf("error must explain Windows SSPI path: %q", err)
	}
}
