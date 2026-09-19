package diagnostic

import (
	"net/url"
	"regexp"
	"strings"
)

const redactedValue = "REDACTED"

var (
	urlPattern      = regexp.MustCompile(`(?i)\bhttps?://[^\s"'<>]+`)
	authPattern     = regexp.MustCompile(`(?i)\b(proxy-authorization|authorization)\s*[:=]\s*(?:[A-Za-z]+\s+)?[^\s,;]+`)
	secretKVPattern = regexp.MustCompile(`(?i)\b(password|passwd|pwd|token|api[_-]?key|secret|client_secret)\s*=\s*[^&\s,;]+`)
)

// RedactText removes common secret-bearing values from operational text. It is
// intentionally conservative: losing query detail is preferable to retaining
// credentials or tokens in logs or diagnostic history.
func RedactText(input string) string {
	if input == "" {
		return ""
	}
	out := urlPattern.ReplaceAllStringFunc(input, redactURL)
	out = authPattern.ReplaceAllString(out, "$1: "+redactedValue)
	out = secretKVPattern.ReplaceAllString(out, "$1="+redactedValue)
	return out
}

func redactURL(raw string) string {
	trailing := ""
	for len(raw) > 0 {
		last := raw[len(raw)-1]
		if last != '.' && last != ',' && last != ')' && last != ']' {
			break
		}
		trailing = string(last) + trailing
		raw = raw[:len(raw)-1]
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return RedactQueryFallback(raw) + trailing
	}
	u.User = nil
	if u.RawQuery != "" {
		u.RawQuery = redactedValue
	}
	if u.Fragment != "" {
		u.Fragment = redactedValue
	}
	return u.String() + trailing
}

// RedactQueryFallback handles URL-like text that net/url cannot parse.
func RedactQueryFallback(raw string) string {
	if idx := strings.IndexByte(raw, '?'); idx >= 0 {
		return raw[:idx+1] + redactedValue
	}
	return raw
}
