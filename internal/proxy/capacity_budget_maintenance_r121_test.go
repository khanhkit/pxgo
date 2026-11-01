package proxy

import (
	"testing"
	"time"

	"github.com/khanhkit/pxgo/internal/config"
)

// TestR121ClassBudgetFollowsEffectiveLimitAfterMaintenance ensures the
// independent housekeeping tick cannot reopen a strict long-class quota
// wider than the actual manual/OS constrained physical admission window.
func TestR121ClassBudgetFollowsEffectiveLimitAfterMaintenance(t *testing.T) {
	t.Setenv("PXGO_EXPERIMENTAL_CAPACITY_MODE", "auto")
	t.Setenv("PXGO_EXPERIMENTAL_CONNECTION_CLASSES", "1")
	t.Setenv("PXGO_EXPERIMENTAL_STRICT_LONG_INTAKE", "1")
	cfg := config.Default()
	cfg.MaxConnections = 16
	cfg.ProxyReload = 0
	s := startTestProxy(t, cfg)

	before := s.capacity.snapshot()
	if before.EffectiveLimit != 16 || before.ClassPools == nil || before.ClassPools.LongSoftBudget != 12 {
		t.Fatalf("precondition: expected manual hard cap16 and long class12, got %+v", before)
	}
	if before.SoftWindow <= before.EffectiveLimit {
		t.Fatalf("precondition: need distinct soft/effective limits, got %+v", before)
	}

	// Actual server Start launched the native 1-second maintenance ticker.
	// An old implementation overwrites effective-aware budgets with SoftWindow.
	time.Sleep(1500 * time.Millisecond)
	after := s.capacity.snapshot()
	if after.EffectiveLimit != 16 || after.ClassPools == nil ||
		after.ClassPools.ShortSoftBudget != 4 || after.ClassPools.LongSoftBudget != 12 {
		t.Fatalf("maintenance must preserve physical class budget: before_limit=%d before_soft=%d before_classes=%+v after_limit=%d after_soft=%d after_classes=%+v", before.EffectiveLimit, before.SoftWindow, *before.ClassPools, after.EffectiveLimit, after.SoftWindow, after.ClassPools)
	}
}
