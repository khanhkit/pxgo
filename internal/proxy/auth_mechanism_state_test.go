package proxy

import (
	"encoding/base64"
	"sync"
	"testing"
)

func TestAPISS0019AuthMechanismObservationPrefersConcreteEvidence(t *testing.T) {
	var observation authMechanismObservation
	if got := observation.Result(); got != "" {
		t.Fatalf("initial mechanism=%q want empty", got)
	}

	observation.ObserveHeader("Negotiate")
	if got := observation.Result(); got != authSchemeNeg {
		t.Fatalf("generic mechanism=%q", got)
	}

	kerberosSelected := derTLV(0xa1, derTLV(0x30,
		derTLV(0xa1, derTLV(0x06, kerberosOIDValue)),
	))
	observation.ObserveHeader("Negotiate " + base64.StdEncoding.EncodeToString(kerberosSelected))
	if got := observation.Result(); got != authMechanismKerberos {
		t.Fatalf("kerberos mechanism=%q", got)
	}

	// A later generic continuation token must not erase definitive selected
	// mechanism evidence from the same authentication handshake.
	observation.ObserveHeader("Negotiate")
	if got := observation.Result(); got != authMechanismKerberos {
		t.Fatalf("generic continuation downgraded mechanism to %q", got)
	}
}

func TestAPISS0019AuthMechanismObservationRecognizesWrappedNTLM(t *testing.T) {
	ntlm := []byte{'N', 'T', 'L', 'M', 'S', 'S', 'P', 0, 1, 0, 0, 0}
	var observation authMechanismObservation
	observation.ObserveHeader("Negotiate " + base64.StdEncoding.EncodeToString(spnegoNegTokenInit(ntlm)))
	if got := observation.Result(); got != authNTLM {
		t.Fatalf("wrapped NTLM mechanism=%q", got)
	}
}

func TestAPISS0019AuthMechanismTrackerRecordsLastSuccessfulHandshake(t *testing.T) {
	var tracker authMechanismTracker
	if got := tracker.Snapshot(); got != "" {
		t.Fatalf("initial mechanism=%q want empty", got)
	}

	tracker.Record(authMechanismKerberos)
	if got := tracker.Snapshot(); got != authMechanismKerberos {
		t.Fatalf("kerberos mechanism=%q", got)
	}

	// A later successful authentication may legitimately negotiate another
	// mechanism, so the global last-successful state is replaceable.
	tracker.Record(authNTLM)
	if got := tracker.Snapshot(); got != authNTLM {
		t.Fatalf("replacement mechanism=%q", got)
	}

	tracker.Record("")
	if got := tracker.Snapshot(); got != authNTLM {
		t.Fatalf("empty observation erased mechanism: %q", got)
	}
}

func TestAPISS0019AuthMechanismTrackerConcurrentAccess(t *testing.T) {
	var tracker authMechanismTracker
	mechanisms := []string{authSchemeBasic, authSchemeDigest, authNTLM, authSchemeNeg}

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for n := 0; n < 100; n++ {
				tracker.Record(mechanisms[(i+n)%len(mechanisms)])
				_ = tracker.Snapshot()
			}
		}(i)
	}
	wg.Wait()
	if tracker.Snapshot() == "" {
		t.Fatal("concurrent observations left empty mechanism")
	}
}
