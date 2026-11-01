//go:build linux

package proxy

import "testing"

func TestR76PSIRejectsDuplicateOrMissingAvg10(t *testing.T) {
	invalid := []string{
		"some avg10=0.00 avg60=NaN avg300=0.00 total=0",
		"some avg10=0.00 avg60=0.00 avg300=0.00 total=-1",
		"some avg10=0.00 avg60=0.00 avg300=0.00 total=0 extra=bad",
		"some avg10=90.00 avg10=0.00 avg60=0.00 avg300=0.00 total=0",
		"some avg10=0.00 avg60=0.00 total=0\nsome avg10=99.0 avg60=0.00 total=0",
		"full avg60=0.00 avg300=0.00 total=0",
		"some avg10=NaN avg60=0.00 avg300=0.00 total=0",
		"some avg10=101 avg60=0.00 avg300=0.00 total=0",
		"unknown avg10=0.00 avg60=0.00 avg300=0.00 total=0",
	}
	for _, raw := range invalid {
		if _, _, err := parseCapacityPSIAvg10([]byte(raw)); err == nil {
			t.Errorf("malformed PSI was silently accepted: %q", raw)
		}
	}
}
