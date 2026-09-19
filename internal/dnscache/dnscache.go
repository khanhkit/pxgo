// Package dnscache provides a small TTL cache in front of net.LookupIP so hot
// paths (noproxy matching, PAC dnsResolve) do not hit the resolver on every
// request.
package dnscache

import (
	"net"
	"sync"
	"time"
)

const (
	positiveTTL = 60 * time.Second
	negativeTTL = 5 * time.Second
	maxEntries  = 4096
)

// lookupIP is swappable in tests.
var lookupIP = net.LookupIP

type entry struct {
	ips     []net.IP
	expires time.Time
}

type lookupCall struct {
	done       chan struct{}
	ips        []net.IP
	generation uint64
}

var (
	mu         sync.RWMutex
	cache      = map[string]entry{}
	inflight   = map[string]*lookupCall{}
	generation uint64
)

// Lookup resolves host, serving repeated lookups from a TTL cache. Concurrent
// misses for the same host share one resolver generation. Returned IP values
// are defensive copies so callers cannot mutate cache-owned bytes. Resolution
// failures are cached briefly so a broken resolver is not hammered.
func Lookup(host string) []net.IP {
	now := time.Now()
	mu.RLock()
	e, ok := cache[host]
	if ok && now.Before(e.expires) {
		ips := cloneIPs(e.ips)
		mu.RUnlock()
		return ips
	}
	mu.RUnlock()

	// Re-check under the write lock before creating an inflight generation.
	// This closes the miss TOCTOU window and makes one generation authoritative.
	mu.Lock()
	now = time.Now()
	if e, ok := cache[host]; ok && now.Before(e.expires) {
		ips := cloneIPs(e.ips)
		mu.Unlock()
		return ips
	}
	if call, ok := inflight[host]; ok {
		done := call.done
		mu.Unlock()
		<-done
		return cloneIPs(call.ips)
	}
	call := &lookupCall{done: make(chan struct{}), generation: generation}
	inflight[host] = call
	mu.Unlock()

	ips, err := lookupIP(host)
	ttl := positiveTTL
	if err != nil {
		ips = nil
		ttl = negativeTTL
	}
	completedAt := time.Now()
	stored := cloneIPs(ips)

	mu.Lock()
	if call.generation == generation {
		if len(cache) >= maxEntries {
			evictLocked(completedAt)
		}
		cache[host] = entry{
			ips:     stored,
			expires: completedAt.Add(ttl),
		}
	}
	call.ips = stored
	if inflight[host] == call {
		delete(inflight, host)
	}
	close(call.done)
	mu.Unlock()

	return cloneIPs(stored)
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

// evictLocked removes expired entries first. If the cache is still full, it
// evicts only the live entry with the earliest expiration time. A hostname
// lexical tie-break keeps the choice deterministic when expirations are equal.
func evictLocked(now time.Time) {
	for host, e := range cache {
		if !now.Before(e.expires) {
			delete(cache, host)
		}
	}
	for len(cache) >= maxEntries {
		var victim string
		var oldest time.Time
		for host, e := range cache {
			if victim == "" || e.expires.Before(oldest) || (e.expires.Equal(oldest) && host < victim) {
				victim = host
				oldest = e.expires
			}
		}
		if victim == "" {
			return
		}
		delete(cache, victim)
	}
}

// ClearNetworkState invalidates cache entries and detaches in-flight resolver
// generations after a network epoch. Calls already waiting on an older
// generation may still receive that result, but stale generations cannot
// repopulate the new cache or delete a newer in-flight lookup.
func ClearNetworkState() {
	mu.Lock()
	generation++
	cache = map[string]entry{}
	inflight = map[string]*lookupCall{}
	mu.Unlock()
}

// ResetForTest clears all transient resolver state.
func ResetForTest() {
	ClearNetworkState()
}
