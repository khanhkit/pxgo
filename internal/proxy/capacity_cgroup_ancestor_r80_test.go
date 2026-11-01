//go:build linux

package proxy

import (
	"os"
	"path/filepath"
	"testing"
)

func TestR80CgroupAncestorBudgetMustClipLeaf(t *testing.T) {
	root := t.TempDir()
	write := func(rel, name, body string) {
		t.Helper()
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Root memory is shared across sibling cgroups; leaf may look healthy
	// despite exhausted ancestor. Exactly one physical admission gate remains.
	write("", "memory.max", "1073741824")
	write("", "memory.current", "1056964608")
	write("", "memory.high", "max")
	write("batch", "memory.max", "2147483648")
	write("batch", "memory.current", "536870912")
	write("batch", "memory.high", "max")
	write("batch/proxy", "memory.max", "2147483648")
	write("batch/proxy", "memory.current", "268435456")
	write("batch/proxy", "memory.high", "max")

	got, err := readCapacityCgroupHierarchy(root, "batch/proxy")
	if err != nil {
		t.Fatal(err)
	}
	if !got.constrained || got.freeSlots != 0 || !got.pressured {
		t.Fatalf("EXHAUSTED_ANCESTOR_BYPASSED %+v", got)
	}
	write("", "memory.max", "4294967296")
	write("", "memory.current", "1073741824")
	got, err = readCapacityCgroupHierarchy(root, "batch/proxy")
	if err != nil {
		t.Fatal(err)
	}
	if !got.constrained || got.freeSlots != 1280 {
		t.Fatalf("LEAF_SHOULD_BE_TIGHTEST %+v", got)
	}
}

func TestR80CgroupAncestorPSIPropagatesPressure(t *testing.T) {
	root := t.TempDir()
	add := func(rel, name, value string) {
		t.Helper()
		dir := filepath.Join(root, rel)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []string{"", "sub", "sub/proxy"} {
		add(d, "memory.max", "max")
		add(d, "memory.current", "1048576")
		add(d, "memory.high", "max")
		add(d, "memory.pressure", "some avg10=0.00 avg60=0.00 avg300=0.00 total=0\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n")
	}
	add("", "memory.pressure", "some avg10=30.00 avg60=0.00 avg300=0.00 total=0\nfull avg10=1.50 avg60=0.00 avg300=0.00 total=0\n")
	got, err := readCapacityCgroupHierarchy(root, "sub/proxy")
	if err != nil || got.constrained || !got.pressured {
		t.Fatalf("UNLIMITED_ROOT_PSI_IGNORED %+v %v", got, err)
	}
}

func TestR80CgroupHierarchyRejectsTraversalAndUnreadableFiniteAncestor(t *testing.T) {
	root := t.TempDir()
	bad := []string{"../outside", "/absolute", "a/../../../etc"}
	for _, relative := range bad {
		if _, err := readCapacityCgroupHierarchy(root, relative); err == nil {
			t.Errorf("PATH_ESCAPE_ACCEPTED: %q", relative)
		}
	}
	leaf := filepath.Join(root, "child")
	if err := os.MkdirAll(leaf, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "memory.max"), []byte("1073741824"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readCapacityCgroupHierarchy(root, "child"); err == nil {
		t.Fatal("UNREADABLE_FINITE_ANCESTOR_MUST_FAIL_CLOSED")
	}
}

func TestR80SelfCgroupPathAndMountRootParse(t *testing.T) {
	cases := []struct {
		cgroup, mountRoot, want string
		bad                     bool
	}{
		{"0::/system.slice/pxgo.service\n", "/", "system.slice/pxgo.service", false},
		{"0::/container/workers/app\n", "/container", "workers/app", false},
		{"0::/container\n", "/container", ".", false},
		{"0::/other\n", "/container", "", true},
		{"0::/foo\n0::/bar\n", "/", "", true},
		{"1:memory:/foo\n", "/", "", true},
	}
	for _, tc := range cases {
		got, err := parseCapacitySelfCgroupPath([]byte(tc.cgroup), tc.mountRoot)
		if (err != nil) != tc.bad || (!tc.bad && got != tc.want) {
			t.Errorf("path=%q mount=%q got=%q err=%v", tc.cgroup, tc.mountRoot, got, err)
		}
	}
}
