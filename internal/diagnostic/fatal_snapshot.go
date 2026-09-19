package diagnostic

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

const (
	maxFatalSnapshotBytes = 1 << 20
	maxFatalSnapshots     = 2
	diagnosticDirMode     = 0o700
	diagnosticFileMode    = 0o600
)

var snapshotMu sync.Mutex

// BestEffortWriteFatalSnapshot persists one bounded safe diagnostic snapshot.
// It intentionally returns no error: persistence failure must never block proxy
// traffic or process recovery. Failures are reflected only in the in-memory
// diagnostic ring when that ring remains available.
func BestEffortWriteFatalSnapshot(path string, snapshot Snapshot) {
	if path == "" {
		return
	}
	if err := writeFatalSnapshot(path, snapshot); err != nil {
		Record("diagnostic.snapshot-error", err.Error())
	}
}

func writeFatalSnapshot(path string, snapshot Snapshot) error {
	snapshotMu.Lock()
	defer snapshotMu.Unlock()

	data, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	if len(data) > maxFatalSnapshotBytes {
		return fmt.Errorf("diagnostic snapshot exceeds %d bytes", maxFatalSnapshotBytes)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, diagnosticDirMode); err != nil {
		return err
	}

	for i := maxFatalSnapshots; i >= 1; i-- {
		dst := path + "." + strconv.Itoa(i)
		if i == maxFatalSnapshots {
			if err := os.Remove(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		src := path
		if i > 1 {
			src = path + "." + strconv.Itoa(i-1)
		}
		if err := os.Rename(src, dst); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}

	tmp, err := os.CreateTemp(dir, ".pxgo-diagnostic-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}
	if err := tmp.Chmod(diagnosticFileMode); err != nil {
		cleanup()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return os.Chmod(path, diagnosticFileMode)
}

func LoadSnapshot(path string) (Snapshot, error) {
	var snapshot Snapshot
	f, err := os.Open(path)
	if err != nil {
		return snapshot, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxFatalSnapshotBytes+1))
	if err != nil {
		return snapshot, err
	}
	if len(data) > maxFatalSnapshotBytes {
		return snapshot, fmt.Errorf("diagnostic snapshot exceeds %d bytes", maxFatalSnapshotBytes)
	}
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return snapshot, err
	}
	return snapshot, nil
}
