package proxy

import (
	"encoding/base64"
	"strings"
)

const authMechanismKerberos = "Kerberos"

var kerberosOIDValue = []byte{0x2a, 0x86, 0x48, 0x86, 0xf7, 0x12, 0x01, 0x02, 0x02}

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
		if isNTLMSSP(raw) {
			return authNTLM
		}
		if selected := selectedSPNEGOMechanism(raw); selected != "" {
			return selected
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

func selectedSPNEGOMechanism(token []byte) string {
	if tag, content, rest, ok := readDERTLV(token); ok && tag == 0x60 && len(rest) == 0 {
		oidTag, oidContent, remainder, ok := readDERTLV(content)
		if !ok || oidTag != 0x06 || !sameBytes(oidContent, spnegoOIDValue) {
			return ""
		}
		token = remainder
	}

	tag, content, rest, ok := readDERTLV(token)
	if !ok || tag != 0xa1 || len(rest) != 0 {
		// Only NegTokenResp carries the selected supportedMech. NegTokenInit
		// advertises candidates and must never be treated as proof that one
		// mechanism actually won negotiation.
		return ""
	}
	seqTag, seqContent, seqRest, ok := readDERTLV(content)
	if !ok || seqTag != 0x30 || len(seqRest) != 0 {
		return ""
	}
	for len(seqContent) > 0 {
		fieldTag, fieldContent, fieldRest, ok := readDERTLV(seqContent)
		if !ok {
			return ""
		}
		seqContent = fieldRest
		if fieldTag != 0xa1 {
			continue
		}
		oidTag, oidContent, oidRest, ok := readDERTLV(fieldContent)
		if !ok || oidTag != 0x06 || len(oidRest) != 0 {
			return ""
		}
		switch {
		case sameBytes(oidContent, kerberosOIDValue):
			return authMechanismKerberos
		case sameBytes(oidContent, ntlmOIDValue):
			return authNTLM
		default:
			return authSchemeNeg
		}
	}
	return ""
}
