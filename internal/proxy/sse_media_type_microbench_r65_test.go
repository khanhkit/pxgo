package proxy

import (
	"strings"
	"testing"
)

func r65LegacySSEMediaType(value string) bool {
	return strings.EqualFold(strings.TrimSpace(strings.SplitN(value, ";", 2)[0]), "text/event-stream")
}

func BenchmarkR65SSEMediaTypeCost(b *testing.B) {
	cases := []struct {
		name   string
		value  string
		strict bool
	}{
		{"legacy_json", "application/json", false},
		{"strict_json", "application/json", true},
		{"legacy_event", "text/event-stream; charset=utf-8", false},
		{"strict_event", "text/event-stream; charset=utf-8", true},
		{"strict_broken_event", "text/event-stream; charset=\"unterminated", true},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if tc.strict {
					_ = isSSEMediaType(tc.value)
				} else {
					_ = r65LegacySSEMediaType(tc.value)
				}
			}
		})
	}
}
