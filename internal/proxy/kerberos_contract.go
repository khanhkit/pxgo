package proxy

import (
	"runtime"

	"github.com/pavelsimo/pxgo/internal/config"
	"github.com/pavelsimo/pxgo/internal/kerberos"
)

func validateKerberosFeature(cfg config.Config) error {
	return validateKerberosFeatureForOS(cfg, runtime.GOOS)
}

func validateKerberosFeatureForOS(cfg config.Config, goos string) error {
	return kerberos.ValidateProxyAuthFeature(cfg.Kerberos, goos)
}
