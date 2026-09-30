package update

import (
	"path/filepath"
	"testing"
	"time"
)

func TestUpdateStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "update-state.json")
	when := time.Date(2026, 9, 30, 3, 4, 5, 0, time.UTC)
	want := StateFromStatus(Status{
		Current: "1.0.0", Latest: "1.1.0", Available: true,
		Provider: ProviderDirect, Channel: Stable,
	}, ResultAvailable, when)
	if err := WriteState(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("state=%+v want %+v", got, want)
	}
}

func TestStatePath(t *testing.T) {
	if got := StatePath("/tmp/pxgo"); got != filepath.Join("/tmp/pxgo", "update-state.json") {
		t.Fatalf("path=%q", got)
	}
	if got := StatePath(""); got != "" {
		t.Fatalf("empty path=%q", got)
	}
}
