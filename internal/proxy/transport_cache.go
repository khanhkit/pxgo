package proxy

import (
	"net/http"
	"sync"
)

type transportCacheEntry struct {
	key       string
	transport *http.Transport

	// authMu serializes connection-oriented authentication exchanges for a
	// single pooled transport. MaxConnsPerHost=1 then keeps the authenticated
	// socket authoritative for that identity/scheme entry.
	authMu        sync.Mutex
	authenticated bool
	scheme        string
	identity      string
	routeKey      string

	closeOnce sync.Once
	onClose   func()
}

func (e *transportCacheEntry) close() {
	if e == nil {
		return
	}
	e.closeOnce.Do(func() {
		if e.transport != nil {
			e.transport.CloseIdleConnections()
		}
		if e.onClose != nil {
			e.onClose()
		}
	})
}

type boundedTransportCache struct {
	mu      sync.Mutex
	max     int
	entries map[string]*transportCacheEntry
	order   []string
}

func newBoundedTransportCache(max int) *boundedTransportCache {
	if max < 1 {
		max = 1
	}
	return &boundedTransportCache{
		max:     max,
		entries: make(map[string]*transportCacheEntry),
	}
}

func (c *boundedTransportCache) get(key string) (*transportCacheEntry, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	return entry, ok
}

// getOrCreate deliberately constructs outside the cache lock so transport
// creation never stalls unrelated lookups. The second lookup owns the race:
// a losing candidate is closed immediately instead of leaking native sockets.
func (c *boundedTransportCache) getOrCreate(key string, factory func() *transportCacheEntry) *transportCacheEntry {
	if entry, ok := c.get(key); ok {
		return entry
	}

	candidate := factory()
	if candidate == nil {
		return nil
	}

	var evicted *transportCacheEntry
	c.mu.Lock()
	if entry, ok := c.entries[key]; ok {
		c.mu.Unlock()
		candidate.close()
		return entry
	}

	if len(c.entries) >= c.max {
		for len(c.order) > 0 {
			victimKey := c.order[0]
			c.order = c.order[1:]
			victim, ok := c.entries[victimKey]
			if !ok {
				continue
			}
			delete(c.entries, victimKey)
			evicted = victim
			break
		}
	}
	candidate.key = key
	c.entries[key] = candidate
	c.order = append(c.order, key)
	c.mu.Unlock()

	if evicted != nil {
		evicted.close()
	}
	return candidate
}

func (c *boundedTransportCache) remove(key string, expected *transportCacheEntry) bool {
	if c == nil {
		return false
	}
	var removed *transportCacheEntry
	c.mu.Lock()
	if entry, ok := c.entries[key]; ok && (expected == nil || entry == expected) {
		delete(c.entries, key)
		removed = entry
	}
	c.mu.Unlock()
	if removed != nil {
		removed.close()
		return true
	}
	return false
}

func (c *boundedTransportCache) len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

func (c *boundedTransportCache) clear() {
	if c == nil {
		return
	}
	c.mu.Lock()
	entries := make([]*transportCacheEntry, 0, len(c.entries))
	for _, entry := range c.entries {
		entries = append(entries, entry)
	}
	c.entries = make(map[string]*transportCacheEntry)
	c.order = nil
	c.mu.Unlock()

	for _, entry := range entries {
		entry.close()
	}
}
