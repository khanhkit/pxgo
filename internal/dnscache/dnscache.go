// Package dnscache provides a bounded TTL cache for DNS lookups used by hot
// paths such as noproxy matching and PAC dnsResolve.
package dnscache

import (
	"context"
	"net"
	"sync"
	"time"
)

const (
	positiveTTL = 60 * time.Second
	negativeTTL = 5 * time.Second
	maxEntries  = 4096
)

type LookupFunc func(context.Context, string) ([]net.IP, error)

// lookupIP remains swappable by the legacy package-level tests. New production
// code should own a Cache instance with an explicit resolver dependency.
var lookupIP = net.LookupIP

type entry struct {
	ips     []net.IP
	err     error
	expires time.Time
}

type lookupCall struct {
	done       chan struct{}
	ips        []net.IP
	err        error
	generation uint64
}

type Cache struct {
	mu         sync.RWMutex
	cache      map[string]entry
	inflight   map[string]*lookupCall
	generation uint64
	lookup     LookupFunc
}

func New(lookup LookupFunc) *Cache {
	if lookup == nil {
		lookup = func(_ context.Context, host string) ([]net.IP, error) { return lookupIP(host) }
	}
	return &Cache{
		cache:    map[string]entry{},
		inflight: map[string]*lookupCall{},
		lookup:   lookup,
	}
}

var defaultCache = New(nil)

func Default() *Cache { return defaultCache }

// Lookup resolves host with the process-default cache. It preserves the legacy
// helper contract used by packages that have not injected a cache instance.
func Lookup(host string) []net.IP {
	ips, _ := defaultCache.LookupContext(context.Background(), host)
	return ips
}

// LookupContext resolves host, coalescing concurrent misses. Waiting callers
// remain context-cancellable, returned IP values are defensive copies, and a
// stale network generation cannot repopulate the active cache.
func (c *Cache) LookupContext(ctx context.Context, host string) ([]net.IP, error) {
	if c == nil {
		return nil, nil
	}
	now := time.Now()
	c.mu.RLock()
	e, ok := c.cache[host]
	if ok && now.Before(e.expires) {
		ips, err := cloneIPs(e.ips), e.err
		c.mu.RUnlock()
		return ips, err
	}
	c.mu.RUnlock()

	c.mu.Lock()
	now = time.Now()
	if e, ok := c.cache[host]; ok && now.Before(e.expires) {
		ips, err := cloneIPs(e.ips), e.err
		c.mu.Unlock()
		return ips, err
	}
	if call, ok := c.inflight[host]; ok {
		done := call.done
		c.mu.Unlock()
		select {
		case <-done:
			return cloneIPs(call.ips), call.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	call := &lookupCall{done: make(chan struct{}), generation: c.generation}
	c.inflight[host] = call
	c.mu.Unlock()

	ips, err := c.lookup(ctx, host)
	ttl := positiveTTL
	if err != nil {
		ips = nil
		ttl = negativeTTL
	}
	completedAt := time.Now()
	stored := cloneIPs(ips)
	// A caller cancellation/deadline is not a resolver verdict. Do not turn it
	// into a shared negative cache entry that would poison unrelated requests.
	cacheable := ctx.Err() == nil

	c.mu.Lock()
	if cacheable && call.generation == c.generation {
		if len(c.cache) >= maxEntries {
			c.evictLocked(completedAt)
		}
		c.cache[host] = entry{
			ips:     stored,
			err:     err,
			expires: completedAt.Add(ttl),
		}
	}
	call.ips = stored
	call.err = err
	if c.inflight[host] == call {
		delete(c.inflight, host)
	}
	close(call.done)
	c.mu.Unlock()

	return cloneIPs(stored), err
}

func cloneIPs(ips []net.IP) []net.IP {
	if ips == nil {
		return nil
	}
	cloned := make([]net.IP, len(ips))
	for i, ip := range ips {
		if ip != nil {
			cloned[i] = append(net.IP(nil), ip...)
		}
	}
	return cloned
}

func (c *Cache) evictLocked(now time.Time) {
	for host, e := range c.cache {
		if !now.Before(e.expires) {
			delete(c.cache, host)
		}
	}
	for len(c.cache) >= maxEntries {
		var victim string
		var oldest time.Time
		for host, e := range c.cache {
			if victim == "" || e.expires.Before(oldest) || (e.expires.Equal(oldest) && host < victim) {
				victim = host
				oldest = e.expires
			}
		}
		if victim == "" {
			return
		}
		delete(c.cache, victim)
	}
}

// ClearNetworkState invalidates cache entries and detaches in-flight resolver
// generations after a network epoch.
func (c *Cache) ClearNetworkState() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.generation++
	c.cache = map[string]entry{}
	c.inflight = map[string]*lookupCall{}
	c.mu.Unlock()
}

func ClearNetworkState() { defaultCache.ClearNetworkState() }

// ResetForTest clears all transient state in the process-default cache.
func ResetForTest() { defaultCache.ClearNetworkState() }
