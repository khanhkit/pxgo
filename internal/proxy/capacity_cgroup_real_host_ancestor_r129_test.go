//go:build linux

package proxy

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Runs only inside a disposable --cgroupns=host Docker container. The host
// namespace exposes the ACTUAL visible cgroup hierarchy, unlike private
// cgroup namespaces; this still cannot prove hidden upstream limits.
func TestR129NativeVisibleCgroupAncestors(t *testing.T) {
	if os.Getenv("PXGO_R129_HOST_CGROUP") != "1" {
		t.Skip("explicit host-visible cgroup namespace experiment only")
	}
	const root = "/sys/fs/cgroup"
	mount, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		t.Fatal(err)
	}
	mountRoot, err := parseCapacityCgroupMountRootReader(strings.NewReader(string(mount)), root)
	if err != nil {
		t.Fatal(err)
	}
	membership, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		t.Fatal(err)
	}
	relative, err := parseCapacitySelfCgroupPath(membership, mountRoot)
	if err != nil || relative == "." {
		t.Fatalf("no host-visible descendant membership: relative=%q mountRoot=%q err=%v", relative, mountRoot, err)
	}
	segments := strings.Split(relative, string(filepath.Separator))
	if len(segments) < 1 {
		t.Fatal("no visible cgroup ancestor hierarchy")
	}
	// os.Root enforces traversal within the kernel cgroup mount, including
	// symlink resolution. Never hand a /proc-derived path directly to ReadFile.
	cgroupRoot, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer cgroupRoot.Close()
	fd, err := cgroupRoot.Open(filepath.Join(relative, "memory.max"))
	if err != nil {
		t.Fatal(err)
	}
	limitRaw, err := io.ReadAll(fd)
	_ = fd.Close()
	if err != nil {
		t.Fatal(err)
	}
	leafMax, unlimited, err := parseCapacityCgroupUint(limitRaw)
	if err != nil || unlimited || leafMax < 256<<20 || leafMax > 512<<20 {
		t.Fatalf("unexpected host-visible leaf limit=%d unlimited=%v err=%v", leafMax, unlimited, err)
	}
	// Exercise the actual production live sensor (which reads all visible
	// ancestor levels), not a test-only reconstructed sensor path.
	sample := readCapacitySample()
	if !sample.available || !sample.linuxMemoryConstrained {
		t.Fatalf("live FD+memory sensor lost finite host-visible constraints: %+v", sample)
	}
	t.Logf("R129_HOST_ANCESTOR_RESULT mount_root=%q visible_cgroup_levels=%d finite_leaf_max=%d sensor_free_slots=%d membership_constrained=true", mountRoot, len(segments)+1, leafMax, sample.linuxMemoryFreeSlots)
}
