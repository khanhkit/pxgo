package dnscache

import (
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func TestTCSUPNET011ClearNetworkStateInvalidatesCachedGeneration(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	oldLookup := lookupIP
	t.Cleanup(func() { lookupIP = oldLookup })

	var calls atomic.Int32
	lookupIP = func(string) ([]net.IP, error) {
		if calls.Add(1) == 1 {
			return []net.IP{net.ParseIP("192.0.2.10")}, nil
		}
		return []net.IP{net.ParseIP("192.0.2.20")}, nil
	}

	first := Lookup("epoch.example.test")
	if got := first[0].String(); got != "192.0.2.10" {
		t.Fatalf("first lookup = %s", got)
	}
	ClearNetworkState()
	second := Lookup("epoch.example.test")
	if got := second[0].String(); got != "192.0.2.20" {
		t.Fatalf("post-epoch lookup = %s, want fresh resolver result", got)
	}
	if calls.Load() != 2 {
		t.Fatalf("resolver calls = %d, want 2", calls.Load())
	}
}

func TestTCSUPNET011OldInflightGenerationCannotRepopulateAfterClear(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	oldLookup := lookupIP
	t.Cleanup(func() { lookupIP = oldLookup })

	started := make(chan struct{}, 2)
	releaseFirst := make(chan struct{})
	var calls atomic.Int32
	lookupIP = func(string) ([]net.IP, error) {
		call := calls.Add(1)
		started <- struct{}{}
		if call == 1 {
			<-releaseFirst
			return []net.IP{net.ParseIP("192.0.2.30")}, nil
		}
		return []net.IP{net.ParseIP("192.0.2.40")}, nil
	}

	firstDone := make(chan []net.IP, 1)
	go func() { firstDone <- Lookup("inflight.example.test") }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first lookup did not start")
	}

	ClearNetworkState()
	secondDone := make(chan []net.IP, 1)
	go func() { secondDone <- Lookup("inflight.example.test") }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("post-clear lookup did not start a fresh generation")
	}

	select {
	case second := <-secondDone:
		if got := second[0].String(); got != "192.0.2.40" {
			t.Fatalf("second lookup = %s", got)
		}
	case <-time.After(time.Second):
		t.Fatal("second lookup blocked behind stale generation")
	}

	close(releaseFirst)
	select {
	case first := <-firstDone:
		if got := first[0].String(); got != "192.0.2.30" {
			t.Fatalf("first lookup = %s", got)
		}
	case <-time.After(time.Second):
		t.Fatal("first lookup did not finish")
	}

	third := Lookup("inflight.example.test")
	if got := third[0].String(); got != "192.0.2.40" {
		t.Fatalf("stale generation repopulated cache: third lookup = %s", got)
	}
	if calls.Load() != 2 {
		t.Fatalf("resolver calls = %d, want 2", calls.Load())
	}
}
