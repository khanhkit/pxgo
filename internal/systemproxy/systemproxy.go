package systemproxy

import (
	"errors"
	"strings"
	"sync"
)

var ErrResolverClosed = errors.New("system proxy resolver is closed")

type resolverBackend interface {
	resolve(rawurl string, cfg Config) (string, error)
	close() error
}

type Resolver struct {
	mu      sync.RWMutex
	backend resolverBackend
	closed  bool
}

func newResolverWithBackend(backend resolverBackend) *Resolver {
	return &Resolver{backend: backend}
}

func (r *Resolver) ResolveProxyForURL(rawurl string, cfg Config) (string, error) {
	if !cfg.AutoDetect && !cfg.IsPAC {
		return "", nil
	}
	if r == nil {
		return "", ErrResolverClosed
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed || r.backend == nil {
		return "", ErrResolverClosed
	}
	return r.backend.resolve(rawurl, cfg)
}

func (r *Resolver) Close() error {
	if r == nil {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	if r.backend == nil {
		return nil
	}
	return r.backend.close()
}

type Config struct {
	ManualProxy string
	PACURL      string
	Bypass      string
	Found       bool
	IsPAC       bool
	AutoDetect  bool
}

func configFromDiscoveredSources(autoDetect bool, pacURL, proxy, bypass string) Config {
	cfg := Config{
		AutoDetect: autoDetect,
		PACURL:     pacURL,
		Bypass:     bypass,
		IsPAC:      pacURL != "",
	}
	if proxy != "" {
		cfg.ManualProxy = ParseManualProxyString(proxy)
	}
	cfg.Found = cfg.AutoDetect || cfg.IsPAC || cfg.ManualProxy != ""
	return cfg
}

func ParseManualProxyString(proxyServer string) string {
	var proxies []string
	for _, item := range strings.FieldsFunc(strings.ToLower(proxyServer), func(r rune) bool {
		return r == ';' || r == ' ' || r == ','
	}) {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if scheme, proxy, ok := strings.Cut(item, "="); ok {
			scheme = strings.TrimSpace(scheme)
			proxy = strings.TrimSpace(proxy)
			switch scheme {
			case "ftp":
				continue
			case "socks":
				if !strings.Contains(proxy, "://") {
					proxy = "socks5://" + proxy
				}
			}
			proxies = append(proxies, proxy)
			continue
		}
		proxies = append(proxies, item)
	}
	seen := map[string]bool{}
	out := proxies[:0]
	for _, proxy := range proxies {
		if !seen[proxy] {
			out = append(out, proxy)
			seen[proxy] = true
		}
	}
	return strings.Join(out, ",")
}
