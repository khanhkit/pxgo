//go:build linux

package proxy

import (
	"os"
	"path/filepath"
	"testing"
)

func TestR80ParentHighLimitAndPressureOverrideHealthyLeaf(t *testing.T) {
	root := t.TempDir()
	put := func(rel, name, value string) {
		t.Helper()
		dir := filepath.Join(root, rel)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	put("", "memory.max", "max")
	put("", "memory.high", "536870912")    // 512MiB throttle, smaller than 1GiB child max
	put("", "memory.current", "503316480") // 480MiB used => < reserve, zero new permits
	put("worker", "memory.max", "2147483648")
	put("worker", "memory.high", "max")
	put("worker", "memory.current", "268435456")
	got, err := readCapacityCgroupHierarchy(root, "worker")
	if err != nil || !got.constrained || got.freeSlots != 0 || !got.pressured {
		t.Fatalf("PARENT_HIGH_MUST_BLOCK_NEW_INTAKE: got=%+v err=%v", got, err)
	}
	put("", "memory.high", "max")
	put("", "memory.pressure", "some avg10=19.00 avg60=0.00 avg300=0.00 total=0\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n")
	got, err = readCapacityCgroupHierarchy(root, "worker")
	if err != nil || !got.pressured || got.freeSlots == 0 {
		t.Fatalf("PARENT_PSI_PRESSURE_LOST: %+v err=%v", got, err)
	}
}

func TestR80FailClosedOnCorruptVisibleAncestor(t *testing.T) {
	root := t.TempDir()
	put := func(rel, name, value string) {
		t.Helper()
		dir := filepath.Join(root, rel)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	put("", "memory.max", "1073741824")
	put("", "memory.current", "503316480")
	put("", "memory.high", "max")
	put("worker", "memory.max", "2147483648")
	put("worker", "memory.current", "268435456")
	put("worker", "memory.high", "max")
	put("", "memory.pressure", "some avg10=0.00 avg10=0.00 avg60=0.00 avg300=0.00 total=0\n")
	if _, err := readCapacityCgroupHierarchy(root, "worker"); err == nil {
		t.Fatal("CORRUPT_VISIBLE_ANCESTOR_PSI_WAS_IGNORED")
	}
}
