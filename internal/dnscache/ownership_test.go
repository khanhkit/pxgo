package dnscache

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAPISS0017ConcurrentMissIsCoalesced(t *testing.T) {
	var calls int32
	old := lookupIP
	started := make(chan struct{})
	release := make(chan struct{})
	lookupIP = func(string) ([]net.IP, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			close(started)
		}
		<-release
		return []net.IP{net.IPv4(10, 1, 2, 3).To4()}, nil
	}
	t.Cleanup(func() {
		lookupIP = old
		ResetForTest()
	})
	ResetForTest()

	const goroutines = 32
	start := make(chan struct{})
	results := make(chan []net.IP, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			<-start
			results <- Lookup("coalesced.example.test")
		}()
	}
	close(start)
	<-started
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	close(results)

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("resolver calls=%d want=1", got)
	}
	for got := range results {
		if len(got) != 1 || !got[0].Equal(net.ParseIP("10.1.2.3")) {
			t.Fatalf("lookup result=%v", got)
		}
	}
}

func TestAPISS0017CachedIPsAreImmutableAcrossCallers(t *testing.T) {
	original := []net.IP{net.IPv4(10, 2, 3, 4).To4()}
	calls := withFakeResolver(t, func(string) ([]net.IP, error) {
		return original, nil
	})

	first := Lookup("immutable.example.test")
	if len(first) != 1 {
		t.Fatalf("first=%v", first)
	}

	// Mutating resolver-owned input after insertion must not mutate the cache.
	original[0][0] = 99
	second := Lookup("immutable.example.test")
	if !second[0].Equal(net.ParseIP("10.2.3.4")) {
		t.Fatalf("resolver-owned slice aliased cache: %v", second)
	}

	// Mutating one caller's returned bytes must not mutate subsequent callers.
	second[0][1] = 88
	third := Lookup("immutable.example.test")
	if !third[0].Equal(net.ParseIP("10.2.3.4")) {
		t.Fatalf("returned slice aliased cache: %v", third)
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("resolver calls=%d want=1", got)
	}
}

func TestAPISS0017CapacityEvictsOneOldestEntry(t *testing.T) {
	old := lookupIP
	lookupIP = func(string) ([]net.IP, error) {
		return []net.IP{net.IPv4(192, 0, 2, 1).To4()}, nil
	}
	t.Cleanup(func() {
		lookupIP = old
		ResetForTest()
	})
	ResetForTest()

	base := time.Now().Add(time.Hour)
	mu.Lock()
	for i := 0; i < maxEntries; i++ {
		host := fmt.Sprintf("existing-%04d.example.test", i)
		cache[host] = entry{
			ips:     []net.IP{net.IPv4(10, 0, byte(i>>8), byte(i)).To4()},
			expires: base.Add(time.Duration(i) * time.Second),
		}
	}
	mu.Unlock()

	Lookup("new.example.test")

	mu.RLock()
	defer mu.RUnlock()
	if len(cache) != maxEntries {
		t.Fatalf("cache size=%d want=%d", len(cache), maxEntries)
	}
	if _, ok := cache["existing-0000.example.test"]; ok {
		t.Fatal("oldest entry was not evicted")
	}
	if _, ok := cache["existing-0001.example.test"]; !ok {
		t.Fatal("bounded eviction removed more than one live victim")
	}
	if _, ok := cache["new.example.test"]; !ok {
		t.Fatal("new entry not admitted")
	}
}

func TestAPISS0017ExpiryStartsAfterResolverCompletes(t *testing.T) {
	old := lookupIP
	started := make(chan struct{})
	release := make(chan struct{})
	lookupIP = func(string) ([]net.IP, error) {
		close(started)
		<-release
		return []net.IP{net.IPv4(203, 0, 113, 10).To4()}, nil
	}
	t.Cleanup(func() {
		lookupIP = old
		ResetForTest()
	})
	ResetForTest()

	done := make(chan struct{})
	go func() {
		defer close(done)
		Lookup("ttl.example.test")
	}()
	<-started
	time.Sleep(40 * time.Millisecond)
	resolvedAt := time.Now()
	close(release)
	<-done

	mu.RLock()
	e := cache["ttl.example.test"]
	mu.RUnlock()
	if remaining := e.expires.Sub(resolvedAt); remaining < positiveTTL-10*time.Millisecond {
		t.Fatalf("TTL started before resolver completion: remaining=%v want approximately %v", remaining, positiveTTL)
	}
}

func TestAPISS0017EvictionTreatsExactExpiryAsExpired(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	now := time.Now()
	mu.Lock()
	cache["expired.example.test"] = entry{
		ips:     []net.IP{net.IPv4(198, 51, 100, 1).To4()},
		expires: now,
	}
	evictLocked(now)
	_, ok := cache["expired.example.test"]
	mu.Unlock()
	if ok {
		t.Fatal("entry with expires == now was not evicted")
	}
}

func TestAPISS0017ConcurrentNegativeMissIsCoalesced(t *testing.T) {
	var calls int32
	old := lookupIP
	release := make(chan struct{})
	started := make(chan struct{})
	lookupIP = func(string) ([]net.IP, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			close(started)
		}
		<-release
		return nil, errors.New("not found")
	}
	t.Cleanup(func() {
		lookupIP = old
		ResetForTest()
	})
	ResetForTest()

	const goroutines = 16
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			<-start
			if got := Lookup("missing-coalesced.example.test"); got != nil {
				t.Errorf("negative lookup=%v want nil", got)
			}
		}()
	}
	close(start)
	<-started
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("negative resolver calls=%d want=1", got)
	}
}
