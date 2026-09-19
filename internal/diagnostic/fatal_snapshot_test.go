package diagnostic

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTOBSSNAP017BestEffortSnapshotRotatesAndLoads(t *testing.T) {
	ResetForTest()
	path := filepath.Join(t.TempDir(), "diag", "fatal.json")
	for i := 1; i <= maxFatalSnapshots+2; i++ {
		snapshot := Snapshot{Port: i}
		BestEffortWriteFatalSnapshot(path, snapshot)
	}
	loaded, err := LoadSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Port != maxFatalSnapshots+2 {
		t.Fatalf("latest snapshot port=%d", loaded.Port)
	}
	matches, err := filepath.Glob(path + "*")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) > maxFatalSnapshots+1 {
		t.Fatalf("snapshot rotation produced %d files, max %d", len(matches), maxFatalSnapshots+1)
	}
}

func TestTOBSSNAP018PersistenceFailureIsBestEffort(t *testing.T) {
	ResetForTest()
	root := t.TempDir()
	notDir := filepath.Join(root, "not-a-dir")
	if err := os.WriteFile(notDir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	BestEffortWriteFatalSnapshot(filepath.Join(notDir, "fatal.json"), Snapshot{Port: 1234})
	events := Events()
	if len(events) == 0 {
		t.Fatal("persistence failure was not observable in safe in-memory diagnostics")
	}
	if events[len(events)-1].Kind != "diagnostic.snapshot-error" {
		t.Fatalf("last event=%+v", events[len(events)-1])
	}
}
