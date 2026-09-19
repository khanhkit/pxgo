package proxy

import (
	"bytes"
	"encoding/base64"
	"strings"
)

var kerberosMechanismOIDDER = []byte{0x06, 0x09, 0x2a, 0x86, 0x48, 0x86, 0xf7, 0x12, 0x01, 0x02, 0x02}

func classifyUpstreamAuthMechanism(header string) string {
	scheme, token, hasToken := strings.Cut(strings.TrimSpace(header), " ")
	switch strings.ToUpper(strings.TrimSpace(scheme)) {
	case authNTLM:
		return authNTLM
	case authNegotiate:
		if !hasToken || strings.TrimSpace(token) == "" {
			return authSchemeNeg
		}
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(token))
		if err != nil {
			return authSchemeNeg
		}
		if bytes.Contains(raw, []byte{'N', 'T', 'L', 'M', 'S', 'S', 'P', 0}) {
			return authNTLM
		}
		if bytes.Contains(raw, kerberosMechanismOIDDER) {
			return "Kerberos"
		}
		return authSchemeNeg
	case authDigest:
		return authSchemeDigest
	case authBasic:
		return authSchemeBasic
	default:
		return strings.TrimSpace(scheme)
	}
}
