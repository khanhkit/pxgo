//go:build linux

package proxy

import "testing"

func TestR83RawCgroupMembershipTraversalNeverNormalizesToSafePath(t *testing.T) {
	for _, raw := range []string{"0::/../../escape\n", "0::/foo/../bar\n", "0::/../outside\n"} {
		got, err := parseCapacitySelfCgroupPath([]byte(raw), "/")
		if err == nil {
			t.Errorf("UNTRUSTED_RAW_TRAVERSAL_ACCEPTED raw=%q resolved=%q", raw, got)
		}
	}
}
