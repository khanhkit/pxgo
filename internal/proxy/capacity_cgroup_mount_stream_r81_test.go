//go:build linux

package proxy

import (
	"strings"
	"testing"
)

func TestR81StreamingMountinfoStopsAtCorrectMount(t *testing.T) {
	good := "11 1 0:1 /ignored /tmp none - tmpfs tmpfs rw\n22 1 0:33 /cg-root /sys/fs/cgroup ro - cgroup2 cgroup rw\n"
	got, err := parseCapacityCgroupMountRootReader(strings.NewReader(good), "/sys/fs/cgroup")
	if err != nil || got != "/cg-root" {
		t.Fatalf("root=%q err=%v", got, err)
	}
	if _, err = parseCapacityCgroupMountRootReader(strings.NewReader(good+good), "/sys/fs/cgroup"); err == nil {
		t.Fatal("ambiguous duplicate mount root silently accepted")
	}
	if _, err = parseCapacityCgroupMountRootReader(strings.NewReader("11 1 0:1 / /tmp none - tmpfs tmpfs rw"), "/sys/fs/cgroup"); err == nil {
		t.Fatal("missing cgroup2 mount root incorrectly accepted")
	}
}
