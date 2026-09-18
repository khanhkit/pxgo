package systemproxy

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

var ErrResolverClosed = errors.New("system proxy resolver is closed")

const defaultResolverTimeout = 15 * time.Second

type resolverBackend interface {
	resolve(ctx context.Context, rawurl string, cfg Config) (string, error)
	close() error
}

type Resolver struct {
	mu      sync.RWMutex
	backend resolverBackend
	timeout time.Duration
	closed  bool
}

func newResolverWithBackend(backend resolverBackend) *Resolver {
	return newResolverWithBackendTimeout(backend, defaultResolverTimeout)
}

func newResolverWithBackendTimeout(backend resolverBackend, timeout time.Duration) *Resolver {
	return &Resolver{backend: backend, timeout: timeout}
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
	timeout := r.timeout
	if timeout <= 0 {
		timeout = defaultResolverTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return r.backend.resolve(ctx, rawurl, cfg)
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

type ManualProxyMap struct {
	Default  string
	ByScheme map[string]string
}

func (m ManualProxyMap) Empty() bool {
	return m.Default == "" && len(m.ByScheme) == 0
}

func (m ManualProxyMap) ForScheme(scheme string) string {
	if proxy := m.ByScheme[strings.ToLower(strings.TrimSpace(scheme))]; proxy != "" {
		return proxy
	}
	return m.Default
}

func ParseManualProxyMap(proxyServer string) ManualProxyMap {
	out := ManualProxyMap{ByScheme: make(map[string]string)}
	trimmed := strings.TrimSpace(proxyServer)
	if trimmed == "" {
		return out
	}
	if !strings.Contains(trimmed, "=") {
		out.Default = ParseManualProxyString(trimmed)
		return out
	}
	for _, item := range strings.Split(trimmed, ";") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		scheme, proxy, ok := strings.Cut(item, "=")
		if !ok {
			if value := ParseManualProxyString(item); value != "" {
				out.Default = value
			}
			continue
		}
		scheme = strings.ToLower(strings.TrimSpace(scheme))
		proxy = strings.TrimSpace(proxy)
		if scheme == "" || proxy == "" {
			continue
		}
		if scheme == "socks" && !strings.Contains(proxy, "://") {
			proxy = "socks5://" + proxy
		}
		out.ByScheme[scheme] = ParseManualProxyString(proxy)
	}
	return out
}

type Config struct {
	ManualProxy ManualProxyMap
	PACURL      string
	Bypass      string
	Supported   bool
	Found       bool
	IsPAC       bool
	AutoDetect  bool
}

func configFromDiscoveredSources(autoDetect bool, pacURL, proxy, bypass string) Config {
	cfg := Config{
		AutoDetect:  autoDetect,
		PACURL:      pacURL,
		Bypass:      bypass,
		Supported:   true,
		IsPAC:       pacURL != "",
		ManualProxy: ParseManualProxyMap(proxy),
	}
	cfg.Found = cfg.AutoDetect || cfg.IsPAC || !cfg.ManualProxy.Empty()
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
