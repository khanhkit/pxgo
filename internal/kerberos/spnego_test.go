package kerberos

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWaitForInitialCCacheWaitsOnlyForInFlightRefresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "krb5cc_test")
	m := &Manager{}
	m.mu.Lock()
	m.refreshing = true
	m.mu.Unlock()

	done := make(chan error, 1)
	go func() {
		done <- waitForInitialCCache(m, path, time.Second)
	}()

	time.Sleep(40 * time.Millisecond)
	if err := os.WriteFile(path, []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("waitForInitialCCache: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("waitForInitialCCache did not observe newly created cache")
	}
}

func TestWaitForInitialCCacheFailsFastWithoutRefresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing")
	start := time.Now()
	err := waitForInitialCCache(&Manager{}, path, time.Second)
	if err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("err=%v, want not-ready error", err)
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("non-refreshing cache wait took %s", elapsed)
	}
}
