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
