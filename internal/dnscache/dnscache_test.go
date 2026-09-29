package dnscache

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func withFakeResolver(t *testing.T, fn func(host string) ([]net.IP, error)) *int32 {
	t.Helper()
	var calls int32
	old := lookupIP
	lookupIP = func(host string) ([]net.IP, error) {
		atomic.AddInt32(&calls, 1)
		return fn(host)
	}
	t.Cleanup(func() {
		lookupIP = old
		ResetForTest()
	})
	ResetForTest()
	return &calls
}

func TestLookupCachesPositiveResults(t *testing.T) {
	want := []net.IP{net.ParseIP("10.1.2.3")}
	calls := withFakeResolver(t, func(string) ([]net.IP, error) { return want, nil })
	for i := 0; i < 10; i++ {
		got := Lookup("cached.example.test")
		if len(got) != 1 || !got[0].Equal(want[0]) {
			t.Fatalf("lookup %d got %v", i, got)
		}
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("resolver called %d times, want 1", got)
	}
}

func TestLookupCachesNegativeResultsBriefly(t *testing.T) {
	calls := withFakeResolver(t, func(string) ([]net.IP, error) { return nil, errors.New("no such host") })
	for i := 0; i < 10; i++ {
		if got := Lookup("missing.example.test"); got != nil {
			t.Fatalf("lookup %d got %v", i, got)
		}
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("resolver called %d times, want 1", got)
	}
}

func TestLookupExpires(t *testing.T) {
	calls := withFakeResolver(t, func(string) ([]net.IP, error) { return []net.IP{net.ParseIP("10.0.0.1")}, nil })
	Lookup("expiring.example.test")
	defaultCache.mu.Lock()
	e := defaultCache.cache["expiring.example.test"]
	e.expires = time.Now().Add(-time.Second)
	defaultCache.cache["expiring.example.test"] = e
	defaultCache.mu.Unlock()
	Lookup("expiring.example.test")
	if got := atomic.LoadInt32(calls); got != 2 {
		t.Fatalf("resolver called %d times, want 2", got)
	}
}

func TestLookupBoundsCacheSize(t *testing.T) {
	withFakeResolver(t, func(string) ([]net.IP, error) { return []net.IP{net.ParseIP("10.0.0.1")}, nil })
	for i := 0; i < maxEntries+100; i++ {
		Lookup(fmt.Sprintf("host-%d.example.test", i))
	}
	defaultCache.mu.RLock()
	size := len(defaultCache.cache)
	defaultCache.mu.RUnlock()
	if size > maxEntries {
		t.Fatalf("cache grew to %d entries, cap is %d", size, maxEntries)
	}
}

func TestAPISS0031CallerCancellationDoesNotPoisonNegativeCache(t *testing.T) {
	var calls atomic.Int32
	cache := New(func(ctx context.Context, _ string) ([]net.IP, error) {
		if calls.Add(1) == 1 {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return []net.IP{net.ParseIP("192.0.2.55")}, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := cache.LookupContext(ctx, "cancel.example.test"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first lookup error=%v want deadline exceeded", err)
	}
	ips, err := cache.LookupContext(context.Background(), "cancel.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if len(ips) != 1 || !ips[0].Equal(net.ParseIP("192.0.2.55")) {
		t.Fatalf("second lookup ips=%v", ips)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("resolver calls=%d want 2; canceled result was cached", got)
	}
}
