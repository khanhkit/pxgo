package guardian

import "testing"

func TestTCGUARDSTATE015ExitClassification(t *testing.T) {
	tests := []struct {
		name     string
		ready    bool
		stopping bool
		recycle  bool
		want     ExitDisposition
	}{
		{"pre-ready failure", false, false, false, ExitStartupFailure},
		{"post-ready crash", true, false, false, ExitRestart},
		{"normal stop", true, true, false, ExitNormalStop},
		{"internal recycle", true, false, true, ExitRestart},
		{"pre-ready recycle is startup failure", false, false, true, ExitStartupFailure},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyExit(tc.ready, tc.stopping, tc.recycle); got != tc.want {
				t.Fatalf("ClassifyExit=%v, want %v", got, tc.want)
			}
		})
	}
}
