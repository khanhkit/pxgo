package proxy

import "testing"

func TestR61ActualSSEContentTypeMustBeWellFormed(t *testing.T) {
	cases := []struct {
		v    string
		want bool
	}{
		{"text/event-stream", true},
		{"text/event-stream; charset=utf-8", true},
		{"text/event-stream; broken", false},
		{"text/event-stream; charset=\"unterminated", false},
		{"text/event-stream, application/json", false},
	}
	for _, c := range cases {
		if got := isSSEMediaType(c.v); got != c.want {
			t.Errorf("ct=%q got=%v want=%v", c.v, got, c.want)
		}
	}
}
