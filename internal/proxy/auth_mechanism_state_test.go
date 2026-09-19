package proxy

import (
	"encoding/base64"
	"sync"
	"testing"
)

func TestAPISS0019AuthMechanismTrackerObservesConcreteMechanism(t *testing.T) {
	var tracker authMechanismTracker
	if got := tracker.Snapshot(); got != "" {
		t.Fatalf("initial mechanism=%q want empty", got)
	}

	tracker.ObserveHeader("Digest realm=\"corp\"")
	if got := tracker.Snapshot(); got != "Digest" {
		t.Fatalf("digest mechanism=%q", got)
	}

	ntlm := base64.StdEncoding.EncodeToString([]byte{'N', 'T', 'L', 'M', 'S', 'S', 'P', 0})
	tracker.ObserveHeader("Negotiate " + ntlm)
	if got := tracker.Snapshot(); got != "NTLM" {
		t.Fatalf("ntlm mechanism=%q", got)
	}

	kerberosOID := []byte{0x06, 0x09, 0x2a, 0x86, 0x48, 0x86, 0xf7, 0x12, 0x01, 0x02, 0x02}
	tracker.ObserveHeader("Negotiate " + base64.StdEncoding.EncodeToString(kerberosOID))
	if got := tracker.Snapshot(); got != authMechanismKerberos {
		t.Fatalf("kerberos mechanism=%q", got)
	}
}

func TestAPISS0019AuthMechanismTrackerConcurrentAccess(t *testing.T) {
	var tracker authMechanismTracker
	headers := []string{
		"Basic dXNlcjpwYXNz",
		"Digest realm=\"corp\"",
		"NTLM TlRMTVNTUA==",
		"Negotiate",
	}

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for n := 0; n < 100; n++ {
				tracker.ObserveHeader(headers[(i+n)%len(headers)])
				_ = tracker.Snapshot()
			}
		}(i)
	}
	wg.Wait()
	if tracker.Snapshot() == "" {
		t.Fatal("concurrent observations left empty mechanism")
	}
}
