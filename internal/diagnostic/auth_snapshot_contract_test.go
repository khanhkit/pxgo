package diagnostic

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAPISS0019AuthSnapshotSerializesUpstreamMechanism(t *testing.T) {
	data, err := json.Marshal(AuthSnapshot{UpstreamMechanism: "Kerberos"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"upstream_mechanism":"Kerberos"`) {
		t.Fatalf("auth snapshot JSON missing mechanism: %s", data)
	}
}
