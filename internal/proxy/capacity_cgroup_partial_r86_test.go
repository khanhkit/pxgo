//go:build linux

package proxy

import (
	"os"
	"path/filepath"
	"testing"
)

func TestR86MissingMemoryMaxMustNotIgnorePartialFiniteTelemetry(t *testing.T) {
	root := t.TempDir()
	got, err := readCapacityCgroupMemoryNode(root)
	if err != nil || got.constrained {
		t.Fatalf("genuine absent controller must preserve FD-only: got=%+v err=%v", got, err)
	}
	put := func(name, contents string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	put("memory.current", "1073741824")
	if _, err = readCapacityCgroupMemoryNode(root); err == nil {
		t.Fatal("MISSING_MAX_WITH_ACTIVE_CURRENT_FELL_BACK_TO_UNLIMITED")
	}
	if err := os.Remove(filepath.Join(root, "memory.current")); err != nil {
		t.Fatal(err)
	}
	put("memory.high", "536870912")
	if _, err = readCapacityCgroupMemoryNode(root); err == nil {
		t.Fatal("MISSING_MAX_WITH_FINITE_THROTTLE_FELL_BACK_TO_UNLIMITED")
	}
	put("memory.high", "max")
	if _, err = readCapacityCgroupMemoryNode(root); err == nil {
		t.Fatal("MISSING_MAX_WITH_VISIBLE_HIGH_FELL_BACK_TO_UNLIMITED")
	}
	if err := os.Remove(filepath.Join(root, "memory.high")); err != nil {
		t.Fatal(err)
	}
	put("memory.pressure", "some avg10=0.00 avg60=0.00 avg300=0.00 total=0\n")
	got, err = readCapacityCgroupMemoryNode(root)
	if err != nil || got.constrained {
		t.Fatalf("PSI may exist on root without memory.max: got=%+v err=%v", got, err)
	}
}
