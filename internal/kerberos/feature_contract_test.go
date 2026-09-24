package kerberos

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateProxyAuthFeatureAllowsDisabledMode(t *testing.T) {
	if err := ValidateProxyAuthFeature(false, "linux"); err != nil {
		t.Fatalf("disabled kerberos mode returned error: %v", err)
	}
}

func TestValidateProxyAuthFeatureAllowsUnixGSSAPI(t *testing.T) {
	for _, goos := range []string{"linux", "darwin"} {
		t.Run(goos, func(t *testing.T) {
			if err := ValidateProxyAuthFeature(true, goos); err != nil {
				t.Fatalf("supported unix kerberos mode returned error: %v", err)
			}
		})
	}
}

func TestValidateProxyAuthFeatureRejectsUnsupportedUnix(t *testing.T) {
	err := ValidateProxyAuthFeature(true, "freebsd")
	if !errors.Is(err, ErrProxyAuthUnsupported) {
		t.Fatalf("err=%v, want ErrProxyAuthUnsupported", err)
	}
}

func TestValidateProxyAuthFeatureRejectsMisleadingWindowsFlag(t *testing.T) {
	err := ValidateProxyAuthFeature(true, "windows")
	if !errors.Is(err, ErrProxyAuthUnsupported) {
		t.Fatalf("err=%v, want ErrProxyAuthUnsupported", err)
	}
	msg := strings.ToLower(err.Error())
	for _, want := range []string{"sspi", "omit --kerberos", "current-user"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q missing %q", err, want)
		}
	}
}
