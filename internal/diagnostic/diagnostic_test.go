package diagnostic

import (
	"strings"
	"sync"
	"testing"
)

func TestTCOBSLOG005RedactTextRemovesSecrets(t *testing.T) {
	inputs := []struct {
		in        string
		forbidden []string
	}{
		{
			in:        "GET https://alice:pw@example.com/path?token=abc&next=1",
			forbidden: []string{"alice", "pw", "token=abc", "next=1"},
		},
		{
			in:        "Authorization: Bearer super-secret",
			forbidden: []string{"super-secret"},
		},
		{
			in:        "Proxy-Authorization=Basic dXNlcjpwYXNz",
			forbidden: []string{"dXNlcjpwYXNz"},
		},
		{
			in:        "password=hunter2 api_key=abcdef secret=s3cr3t",
			forbidden: []string{"hunter2", "abcdef", "s3cr3t"},
		},
	}
	for _, tc := range inputs {
		got := RedactText(tc.in)
		for _, secret := range tc.forbidden {
			if strings.Contains(got, secret) {
				t.Fatalf("RedactText(%q) leaked %q in %q", tc.in, secret, got)
			}
		}
	}
}

func TestTOBSEVT008RingCapacityAndSequence(t *testing.T) {
	ring := NewRing(3)
	for i := 0; i < 5; i++ {
		ring.Record("test", "event")
	}
	got := ring.Snapshot()
	if len(got) != 3 {
		t.Fatalf("snapshot length = %d, want 3", len(got))
	}
	if got[0].Sequence != 3 || got[2].Sequence != 5 {
		t.Fatalf("unexpected retained sequences: %+v", got)
	}
}

func TestTOBSEVT009ConcurrentRingSnapshotIsSafeAndCopied(t *testing.T) {
	ring := NewRing(64)
	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				ring.Record("concurrent", "value")
				_ = ring.Snapshot()
			}
		}()
	}
	wg.Wait()

	first := ring.Snapshot()
	if len(first) == 0 {
		t.Fatal("empty snapshot")
	}
	first[0].Reason = "mutated"
	second := ring.Snapshot()
	if second[0].Reason == "mutated" {
		t.Fatal("caller mutated ring internals through snapshot")
	}
}

func TestTOBSEVT010RingRedactsBeforeStorage(t *testing.T) {
	ring := NewRing(4)
	ring.Record("route", "https://user:pass@example.com/?token=secret")
	got := ring.Snapshot()
	if len(got) != 1 {
		t.Fatalf("snapshot length = %d", len(got))
	}
	if strings.Contains(got[0].Reason, "pass") || strings.Contains(got[0].Reason, "secret") {
		t.Fatalf("event leaked secret: %+v", got[0])
	}
}
