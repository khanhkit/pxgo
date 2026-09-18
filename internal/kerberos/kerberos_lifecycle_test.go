package kerberos

import (
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
