package proxy

import (
	"encoding/base64"
	"testing"
)

func TestAPISS0019ClassifyUpstreamAuthMechanism(t *testing.T) {
	kerberosOIDValue := []byte{0x2a, 0x86, 0x48, 0x86, 0xf7, 0x12, 0x01, 0x02, 0x02}
	ntlmToken := []byte{'N', 'T', 'L', 'M', 'S', 'S', 'P', 0, 1, 0, 0, 0}

	kerberosSelected := derTLV(0xa1, derTLV(0x30,
		derTLV(0xa1, derTLV(0x06, kerberosOIDValue)),
	))
	ntlmSelected := derTLV(0xa1, derTLV(0x30,
		derTLV(0xa1, derTLV(0x06, ntlmOIDValue)),
	))

	mechList := append(derTLV(0x06, kerberosOIDValue), derTLV(0x06, ntlmOIDValue)...)
	advertisedOnly := derTLV(0x60, append(
		derTLV(0x06, spnegoOIDValue),
		derTLV(0xa0, derTLV(0x30, derTLV(0xa0, derTLV(0x30, mechList))))...,
	))

	tests := []struct {
		name   string
		header string
		want   string
	}{
		{name: "ntlm scheme", header: "NTLM " + base64.StdEncoding.EncodeToString(ntlmToken), want: "NTLM"},
		{name: "negotiate raw ntlm fallback", header: "Negotiate " + base64.StdEncoding.EncodeToString(ntlmToken), want: "NTLM"},
		{name: "negotiate spnego wrapped ntlm init", header: "Negotiate " + base64.StdEncoding.EncodeToString(spnegoNegTokenInit(ntlmToken)), want: "NTLM"},
		{name: "negotiate spnego wrapped ntlm response", header: "Negotiate " + base64.StdEncoding.EncodeToString(spnegoNegTokenResp(ntlmToken)), want: "NTLM"},
		{name: "negotiate selected kerberos", header: "Negotiate " + base64.StdEncoding.EncodeToString(kerberosSelected), want: authMechanismKerberos},
		{name: "negotiate selected ntlm", header: "Negotiate " + base64.StdEncoding.EncodeToString(ntlmSelected), want: "NTLM"},
		{name: "negotiate advertises kerberos and ntlm only", header: "Negotiate " + base64.StdEncoding.EncodeToString(advertisedOnly), want: "Negotiate"},
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
