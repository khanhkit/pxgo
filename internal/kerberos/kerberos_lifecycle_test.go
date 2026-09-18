package kerberos

import (
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestCheckStartsRefreshWithoutBlockingCaller(t *testing.T) {
	mgr := makeManager()
	started := make(chan struct{})
	release := make(chan struct{})
	mgr.KinitWithPasswordFunc = func() bool {
		close(started)
		<-release
		return true
	}

	returned := make(chan struct{})
	go func() {
		mgr.Check(true)
		close(returned)
	}()

	select {
	case <-started:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("refresh did not start")
	}
	select {
	case <-returned:
	case <-time.After(50 * time.Millisecond):
		close(release)
		t.Fatal("Check blocked caller on slow refresh")
	}
	close(release)
}

func TestCheckRefreshIsSingleflight(t *testing.T) {
	mgr := makeManager()
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	mgr.KinitWithPasswordFunc = func() bool {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return true
	}

	firstDone := make(chan struct{})
	go func() {
		mgr.Check(true)
		close(firstDone)
	}()
	select {
	case <-started:
	case <-time.After(250 * time.Millisecond):
		close(release)
		t.Fatal("refresh did not start")
	}

	secondDone := make(chan struct{})
	go func() {
		mgr.Check(true)
		close(secondDone)
	}()
	select {
	case <-secondDone:
	case <-time.After(50 * time.Millisecond):
		close(release)
		<-firstDone
		<-secondDone
		t.Fatal("second Check blocked behind in-flight refresh")
	}
	if got := calls.Load(); got != 1 {
		close(release)
		<-firstDone
		t.Fatalf("refresh calls=%d, want singleflight=1", got)
	}
	close(release)
	<-firstDone
}

func TestNearExpiryIgnoresBackoffAndAttemptsRenewal(t *testing.T) {
	mgr := makeManager()
	mgr.TicketExpiry = time.Now().Add(5 * time.Minute)
	mgr.Backoff = CheckInterval
	mgr.KlistValidFunc = func() bool { return false }
	renewed := make(chan struct{})
	mgr.KinitRenewFunc = func() bool {
		close(renewed)
		return true
	}
	mgr.KinitWithPasswordFunc = func() bool {
		t.Fatal("password kinit should not run when renewal succeeds")
		return false
	}

	mgr.Check(false)
	select {
	case <-renewed:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("near-expiry ticket was delayed by backoff")
	}
}

func TestConcurrentExpiryStateUpdatesAreSynchronized(t *testing.T) {
	mgr := makeManager()
	const workers = 16
	done := make(chan struct{}, workers)
	for i := 0; i < workers; i++ {
		go func() {
			mgr.ParseAndSetExpiry(mitKlistOutput)
			done <- struct{}{}
		}()
	}
	for i := 0; i < workers; i++ {
		<-done
	}
	expiry, next, _ := managerState(mgr)
	if expiry.IsZero() || next.IsZero() {
		t.Fatalf("expiry=%v next=%v", expiry, next)
	}
}

func waitForRefresh(t *testing.T, mgr *Manager, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		mgr.mu.Lock()
		refreshing := mgr.refreshing
		mgr.mu.Unlock()
		if !refreshing {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for Kerberos refresh")
		}
		time.Sleep(time.Millisecond)
	}
}

func managerState(mgr *Manager) (time.Time, time.Time, time.Duration) {
	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	return mgr.TicketExpiry, mgr.NextCheck, mgr.Backoff
}

func TestCleanupRemovesCCacheAfterInFlightRefresh(t *testing.T) {
	mgr := makeManager()
	path := t.TempDir() + "/krb5cc_test"
	mgr.CCacheName = "FILE:" + path
	started := make(chan struct{})
	release := make(chan struct{})
	mgr.KinitWithPasswordFunc = func() bool {
		close(started)
		<-release
		if err := os.WriteFile(path, []byte("ticket"), 0o600); err != nil {
			t.Errorf("write fake ccache: %v", err)
		}
		return true
	}

	mgr.Check(true)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("refresh did not start")
	}
	mgr.Cleanup()
	close(release)
	waitForRefresh(t, mgr, time.Second)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("ccache exists after cleanup + refresh completion: %v", err)
	}
}

func TestCheckAfterCleanupDoesNotStartRefresh(t *testing.T) {
	mgr := makeManager()
	started := make(chan struct{}, 1)
	mgr.KinitWithPasswordFunc = func() bool {
		started <- struct{}{}
		return true
	}
	mgr.Cleanup()
	mgr.Check(true)
	select {
	case <-started:
		t.Fatal("refresh started after manager cleanup")
	case <-time.After(50 * time.Millisecond):
	}
}
