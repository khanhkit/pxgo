//go:build linux

package proxy

import (
	"os"
	"strings"
	"testing"
)

func TestR80MountinfoRootAndMembershipAreConsistent(t *testing.T) {
	line := "13 12 0:99 / /sys/fs/cgroup ro,nosuid,nodev,noexec - cgroup2 cgroup rw,nsdelegate\n"
	root, err := parseCapacityCgroupMountRootReader(strings.NewReader(line), "/sys/fs/cgroup")
	if err != nil || root != "/" {
		t.Fatalf("mount root=%q err=%v", root, err)
	}
	line = "13 12 0:99 /machines/demo /sys/fs/cgroup ro,nosuid,nodev,noexec - cgroup2 cgroup rw,nsdelegate\n"
	root, err = parseCapacityCgroupMountRootReader(strings.NewReader(line), "/sys/fs/cgroup")
	if err != nil || root != "/machines/demo" {
		t.Fatalf("nested mount root=%q err=%v", root, err)
	}
	if _, err = parseCapacityCgroupMountRootReader(strings.NewReader(line+line), "/sys/fs/cgroup"); err == nil {
		t.Fatal("duplicate cgroup mount should not authorize ambiguous membership")
	}
	if _, err = parseCapacityCgroupMountRootReader(strings.NewReader("1 2 0:3 / /tmp x - tmpfs tmpfs rw"), "/sys/fs/cgroup"); err == nil {
		t.Fatal("missing cgroup2 mount should fail")
	}
}

func TestR80HostCgroupMembershipSensorIsReadable(t *testing.T) {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		t.Fatal(err)
	}
	mi, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		t.Fatal(err)
	}
	root, err := parseCapacityCgroupMountRootReader(strings.NewReader(string(mi)), "/sys/fs/cgroup")
	if err != nil {
		t.Fatal(err)
	}
	rel, err := parseCapacitySelfCgroupPath(data, root)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(rel, "..") {
		t.Fatalf("escaped root: %q", rel)
	}
	val, err := readCapacityCgroupMemory("/sys/fs/cgroup")
	if err != nil {
		t.Fatal(err)
	}
	if val.constrained && val.freeSlots < 0 {
		t.Fatalf("invalid negative capacity %+v", val)
	}
	if !val.constrained {
		t.Log("visible cgroup controller has no finite memory.max or high; no memory budget claimed")
	} else {
		t.Logf("visible relative cgroup=%q conservative slots=%d pressured=%t", rel, val.freeSlots, val.pressured)
	}
}
