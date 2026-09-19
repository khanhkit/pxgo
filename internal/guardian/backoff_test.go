package guardian

import (
	"testing"
	"time"
)

func TestTCGUARDBACKOFF007FixedCappedSchedule(t *testing.T) {
	var b Backoff
	want := []time.Duration{
		time.Second,
		5 * time.Second,
		30 * time.Second,
		2 * time.Minute,
		5 * time.Minute,
		5 * time.Minute,
	}
	for i, expected := range want {
		if got := b.Next(); got != expected {
			t.Fatalf("delay[%d]=%v, want %v", i, got, expected)
		}
		if got := b.NextDelay(); got <= 0 {
			t.Fatalf("next delay after step %d = %v", i, got)
		}
	}
}

func TestTCGUARDBACKOFF009StableReadyRunResets(t *testing.T) {
	var b Backoff
	_ = b.Next()
	_ = b.Next()
	_ = b.Next()
	if b.Level() < 3 {
		t.Fatalf("level=%d, want >=3", b.Level())
	}
	if b.ObserveReadyDuration(stableRunReset - time.Second) {
		t.Fatal("short ready run reset backoff")
	}
	if !b.ObserveReadyDuration(stableRunReset) {
		t.Fatal("stable ready run did not reset backoff")
	}
	if b.Level() != 0 {
		t.Fatalf("level after reset=%d", b.Level())
	}
	if got := b.Next(); got != time.Second {
		t.Fatalf("first delay after reset=%v", got)
	}
}
