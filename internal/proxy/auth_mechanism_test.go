package proxy

import (
	"encoding/base64"
	"testing"
)

func TestAPISS0019ClassifyUpstreamAuthMechanism(t *testing.T) {
	kerberosOID := []byte{0x06, 0x09, 0x2a, 0x86, 0x48, 0x86, 0xf7, 0x12, 0x01, 0x02, 0x02}
	ntlmToken := append([]byte("prefix"), []byte{'N', 'T', 'L', 'M', 'S', 'S', 'P', 0}...)
	kerberosToken := append([]byte{0x60, 0x0b}, kerberosOID...)

	tests := []struct {
		name   string
		header string
		want   string
	}{
		{name: "ntlm scheme", header: "NTLM " + base64.StdEncoding.EncodeToString(ntlmToken), want: "NTLM"},
		{name: "negotiate ntlm fallback", header: "Negotiate " + base64.StdEncoding.EncodeToString(ntlmToken), want: "NTLM"},
		{name: "negotiate kerberos", header: "Negotiate " + base64.StdEncoding.EncodeToString(kerberosToken), want: "Kerberos"},
		{name: "bare negotiate", header: "Negotiate", want: "Negotiate"},
		{name: "malformed negotiate token", header: "Negotiate !!!", want: "Negotiate"},
		{name: "digest", header: "Digest realm=\"corp\"", want: "Digest"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyUpstreamAuthMechanism(tt.header); got != tt.want {
				t.Fatalf("classifyUpstreamAuthMechanism(%q)=%q want %q", tt.header, got, tt.want)
			}
		})
	}
}
