//go:build linux

package proxy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestR82NoUnifiedControllerFallsBackToFDOnly(t *testing.T) {
	root := t.TempDir()
	v1mounts := "12 11 0:41 / /sys/fs/cgroup rw - tmpfs tmpfs rw\n13 12 0:42 / /sys/fs/cgroup/memory rw - cgroup cgroup rw,memory\n"
	legacyMembership := []byte("2:memory:/system.slice/test.service\n3:cpu:/system.slice/test.service\n")
	got, err := readCapacityCgroupMembershipFrom(root, legacyMembership, strings.NewReader(v1mounts))
	if err != nil || got.constrained || got.pressured || got.freeSlots != 0 {
		t.Fatalf("CGROUP1_WITH_NO_V2_MOUNT_MUST_RETAIN_FD_SENSOR: %+v err=%v", got, err)
	}
	// Contradictory case: a memory.max exists but no matching mount. This must
	// never silently downgrade to a false "unlimited" memory sensor.
	if err = os.WriteFile(filepath.Join(root, "memory.max"), []byte("1073741824"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = readCapacityCgroupMembershipFrom(root, legacyMembership, strings.NewReader(v1mounts)); err == nil {
		t.Fatal("PRESENT_MEMORY_MAX_WITH_UNMAPPED_CONTROLLER_MUST_FAIL_CLOSED")
	}
}

func TestR82MalformedV2MembershipRemainsFailClosed(t *testing.T) {
	root := t.TempDir()
	for _, kv := range [][2]string{
		{"memory.max", "1073741824"},
		{"memory.current", "268435456"},
		{"memory.high", "max"},
	} {
		if err := os.WriteFile(filepath.Join(root, kv[0]), []byte(kv[1]), 0600); err != nil {
			t.Fatal(err)
		}
	}
	mounts := "22 1 0:99 / /sys/fs/cgroup ro - cgroup2 cgroup rw\n"
	for _, membership := range []string{"0::/a\n0::/b\n", "not-cgroup-data", "0::/../../escape"} {
		if _, err := readCapacityCgroupMembershipFrom(root, []byte(membership), strings.NewReader(mounts)); err == nil {
			t.Errorf("malformed/escaping cgroup membership unexpectedly trusted: %q", membership)
		}
	}
}
