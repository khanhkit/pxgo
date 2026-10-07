package main

import (
	"github.com/khanhkit/pxgo/internal/config"
	"github.com/khanhkit/pxgo/internal/systemproxy"
)

var discoverSystemProxyForConfig = systemproxy.Discover

func systemProxyImportFromDiscovered(src systemproxy.Config) config.SystemProxyImport {
	dynamic := src.AutoDetect
	if !dynamic && !src.IsPAC && src.PACURL == "" && len(src.ManualProxy.ByScheme) > 0 {
		dynamic = true
	}
	out := config.SystemProxyImport{
		Found:   src.Found,
		PAC:     src.PACURL,
		NoProxy: src.Bypass,
		Dynamic: dynamic,
	}
	if src.ManualProxy.Default != "" {
		out.Server = src.ManualProxy.Default
	}
	return out
}

func ensureUserConfig(cfg config.Config) (string, bool, error) {
	var discovered config.SystemProxyImport
	if cfg.ConfigPath == "" {
		discovered = systemProxyImportFromDiscovered(discoverSystemProxyForConfig())
	}
	return config.EnsureCanonicalINI(cfg.ConfigPath, discovered)
}

func applySystemProxyConfig(cfg config.Config) (string, error) {
	discovered := systemProxyImportFromDiscovered(discoverSystemProxyForConfig())
	return config.ApplySystemProxyINI(cfg.ConfigPath, discovered)
}

func shouldBootstrapUserConfig(cfg config.Config) bool {
	return !cfg.Help && !cfg.Version && !cfg.CheckUpdate && !cfg.Update && !cfg.Save && !cfg.ApplySystemProxy &&
		!cfg.Uninstall && !cfg.PasswordAction && !cfg.ClientPasswordAction && !cfg.Doctor && !cfg.Quit && cfg.Test == ""
}
