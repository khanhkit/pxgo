package supervisor

import (
	"reflect"
	"strings"
	"testing"
)

func TestTCSUPOUT001OutcomeTaxonomyHealthConsequence(t *testing.T) {
	tests := []struct {
		kind OutcomeKind
		want bool
	}{
		{OutcomeSuccess, false},
		{OutcomeClientCancelled, false},
		{OutcomeDestinationFailure, false},
		{OutcomeProxyDNSFailure, true},
		{OutcomeProxyDialFailure, true},
		{OutcomeProxyTLSFailure, true},
		{OutcomeProxyProtocolFailure, true},
		{OutcomeAuthExhausted, false},
		{OutcomeRouteFailure, false},
		{OutcomeInternalFailure, false},
	}
	for _, tc := range tests {
		if got := tc.kind.PenalizesCandidate(); got != tc.want {
			t.Fatalf("%v PenalizesCandidate() = %v, want %v", tc.kind, got, tc.want)
		}
	}
}

func TestTCSUPOUT001OutcomeCarriesNoSecretBearingRequestFields(t *testing.T) {
	typ := reflect.TypeOf(Outcome{})
	for i := 0; i < typ.NumField(); i++ {
		name := strings.ToLower(typ.Field(i).Name)
		for _, forbidden := range []string{"url", "credential", "password", "token", "authorization", "header"} {
			if strings.Contains(name, forbidden) {
				t.Fatalf("Outcome field %q is secret/request-bearing; keep event metadata minimal", typ.Field(i).Name)
			}
		}
	}
}
