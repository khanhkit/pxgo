package proxy

import (
	"github.com/pavelsimo/pxgo/internal/config"
	"github.com/pavelsimo/pxgo/internal/kerberos"
)

func validateKerberosFeatureForOS(cfg config.Config, goos string) error {
	return kerberos.ValidateProxyAuthFeature(cfg.Kerberos, goos)
}
