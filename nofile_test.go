package main

import (
	"errors"
	"reflect"
	"testing"
)

func TestRaiseNofileLimitCapsAtTarget(t *testing.T) {
	var calls []uint64
	got, err := raiseNofileLimitWith(nofileOps{
		get: func() (uint64, uint64, error) { return 256, 100000, nil },
		set: func(soft, _ uint64) error { calls = append(calls, soft); return nil },
	})
	if err != nil || got != nofileTarget || !reflect.DeepEqual(calls, []uint64{nofileTarget}) {
		t.Fatalf("got=%d err=%v calls=%v", got, err, calls)
	}
}

func TestRaiseNofileLimitRespectsHardLimit(t *testing.T) {
	got, err := raiseNofileLimitWith(nofileOps{
		get: func() (uint64, uint64, error) { return 256, 4096, nil },
		set: func(soft, hard uint64) error {
			if soft != 4096 || hard != 4096 {
				t.Fatalf("set=%d/%d", soft, hard)
			}
			return nil
		},
	})
	if err != nil || got != 4096 {
		t.Fatalf("got=%d err=%v", got, err)
	}
}

func TestRaiseNofileLimitFallsBack(t *testing.T) {
	var calls []uint64
	got, err := raiseNofileLimitWith(nofileOps{
		get: func() (uint64, uint64, error) { return 256, 100000, nil },
		set: func(soft, _ uint64) error {
			calls = append(calls, soft)
			if soft > 4096 {
				return errors.New("rejected")
			}
			return nil
		},
	})
	want := []uint64{65536, 8192, 4096}
	if err != nil || got != 4096 || !reflect.DeepEqual(calls, want) {
		t.Fatalf("got=%d err=%v calls=%v want=%v", got, err, calls, want)
	}
}

func TestRaiseNofileLimitNoopWhenSufficient(t *testing.T) {
	sets := 0
	got, err := raiseNofileLimitWith(nofileOps{
		get: func() (uint64, uint64, error) { return 65536, 100000, nil },
		set: func(uint64, uint64) error { sets++; return nil },
	})
	if err != nil || got != 65536 || sets != 0 {
		t.Fatalf("got=%d err=%v sets=%d", got, err, sets)
	}
}

func TestRaiseNofileLimitInitialGetFailure(t *testing.T) {
	want := errors.New("getrlimit failed")
	got, err := raiseNofileLimitWith(nofileOps{
		get: func() (uint64, uint64, error) { return 0, 0, want },
		set: func(uint64, uint64) error {
			t.Fatal("set must not be called after initial get failure")
			return nil
		},
	})
	if got != 0 || !errors.Is(err, want) {
		t.Fatalf("got=%d err=%v want error=%v", got, err, want)
	}
}

func TestRaiseNofileLimitAllFallbacksFailPreservesSoftLimit(t *testing.T) {
	gets := 0
	var calls []uint64
	got, err := raiseNofileLimitWith(nofileOps{
		get: func() (uint64, uint64, error) {
			gets++
			return 512, 100000, nil
		},
		set: func(soft, _ uint64) error {
			calls = append(calls, soft)
			return errors.New("rejected")
		},
	})
	wantCalls := []uint64{65536, 8192, 4096, 2048, 1024}
	if err != nil || got != 512 || gets != 2 || !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("got=%d err=%v gets=%d calls=%v want=%v", got, err, gets, calls, wantCalls)
	}
}

func TestRaiseNofileLimitFinalGetFailureIsSurfaced(t *testing.T) {
	finalErr := errors.New("final getrlimit failed")
	gets := 0
	got, err := raiseNofileLimitWith(nofileOps{
		get: func() (uint64, uint64, error) {
			gets++
			if gets == 1 {
				return 512, 100000, nil
			}
			return 0, 0, finalErr
		},
		set: func(uint64, uint64) error { return errors.New("rejected") },
	})
	if got != 512 || !errors.Is(err, finalErr) {
		t.Fatalf("got=%d err=%v want soft=512 err=%v", got, err, finalErr)
	}
}
