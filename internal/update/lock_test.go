package update

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestUpdateLockSerializesApplyOwnership(t *testing.T) {
	path := filepath.Join(t.TempDir(), updateLockFileName)
	first, err := acquireUpdateLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	if second, err := acquireUpdateLock(path); !errors.Is(err, ErrUpdateBusy) {
		if second != nil {
			_ = second.Close()
		}
		t.Fatalf("second lock err=%v want ErrUpdateBusy", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	first = nil

	third, err := acquireUpdateLock(path)
	if err != nil {
		t.Fatalf("reacquire after release: %v", err)
	}
	if err := third.Close(); err != nil {
		t.Fatal(err)
	}
}
