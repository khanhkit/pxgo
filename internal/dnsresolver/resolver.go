package dnsresolver

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

const (
	defaultDNSPort       = "53"
	defaultLookupTimeout = 20 * time.Second
	maxDNSMessageBytes   = 65535
	resolverSchemeUDP    = "udp"
	resolverSchemeTCP    = "tcp"
	resolverSchemeHTTPS  = "https"
	resolverModeSystem   = "system"
	resolverModeDNS      = "dns"
)

// Policy owns outbound hostname resolution. The zero/default configuration
// keeps the operating-system resolver. Any configured endpoint set is
// fail-closed: ordinary target lookups never fall back to system DNS.
type LookupFunc func(context.Context, string) ([]net.IP, error)

type Policy struct {
	system    bool
	endpoints []endpoint
	rules     []policyRule
	timeout   time.Duration
	http      *http.Client

	mu        sync.RWMutex
	lastError string
}

type Rule struct {
	Resolver string
	Only     []string
	Bypass   []string
}

type policyRule struct {
	resolver *Policy
	only     []string
	bypass   []string
}

type Status struct {
	Mode      string   `json:"mode"`
	Endpoints []string `json:"endpoints,omitempty"`
	Bootstrap string   `json:"bootstrap,omitempty"`
	LastError string   `json:"last_error,omitempty"`
}

type endpoint struct {
	scheme string
	addr   string
	url    *url.URL
	label  string
}

type protocolError struct{ msg string }

func (e *protocolError) Error() string { return e.msg }

type responseError struct {
	msg      string
	terminal bool
}

func (e *responseError) Error() string { return e.msg }

func (e *responseError) Terminal() bool { return e.terminal }

// New validates and constructs one resolver policy. Grammar:
//
//	system
//	1.1.1.1
//	1.1.1.1:53
//	udp://1.1.1.1:53
//	tcp://1.1.1.1:53
//	https://resolver.example/dns-query
//
// Multiple non-system endpoints may be comma-separated for deterministic
// ordered failover.
func New(raw string, timeout time.Duration) (*Policy, error) {
	return newPolicy(raw, timeout, nil)
}

func NewRules(rules []Rule, timeout time.Duration) (*Policy, error) {
	if len(rules) == 0 {
		return New("", timeout)
	}
	if len(rules) == 1 && len(rules[0].Only) == 0 && len(rules[0].Bypass) == 0 {
		return New(rules[0].Resolver, timeout)
	}
	p := &Policy{timeout: normalizedTimeout(timeout)}
	for _, rawRule := range rules {
		resolver, err := New(rawRule.Resolver, timeout)
		if err != nil {
			return nil, err
		}
		only, err := normalizeRulePatterns(rawRule.Only)
		if err != nil {
			return nil, fmt.Errorf("dns_only: %w", err)
		}
		bypass, err := normalizeRulePatterns(rawRule.Bypass)
		if err != nil {
			return nil, fmt.Errorf("dns_bypass: %w", err)
		}
		p.rules = append(p.rules, policyRule{resolver: resolver, only: only, bypass: bypass})
	}
	return p, nil
}

func normalizeRulePatterns(values []string) ([]string, error) {
	var out []string
	for _, value := range values {
		for _, raw := range strings.Split(value, ",") {
			pattern := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(raw), "."))
			if pattern == "" {
				continue
			}
			if strings.ContainsAny(pattern, "/:") || (strings.Contains(pattern, "*") && pattern != "*" && !strings.HasPrefix(pattern, "*.")) {
				return nil, fmt.Errorf("invalid domain pattern %q", raw)
			}
			out = append(out, pattern)
		}
	}
	return out, nil
}

func matchDomain(host, pattern string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	pattern = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(pattern), "."))
	if pattern == "*" {
		return host != ""
	}
	if base, ok := strings.CutPrefix(pattern, "*."); ok {
		return host != base && strings.HasSuffix(host, "."+base)
	}
	return host == pattern || strings.HasSuffix(host, "."+pattern)
}

func matchesAny(host string, patterns []string) bool {
	for _, pattern := range patterns {
		if matchDomain(host, pattern) {
			return true
		}
	}
	return false
}

func newPolicy(raw string, timeout time.Duration, client *http.Client) (*Policy, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.EqualFold(raw, resolverModeSystem) {
		return &Policy{system: true, timeout: normalizedTimeout(timeout)}, nil
	}
	parts := strings.Split(raw, ",")
	endpoints := make([]endpoint, 0, len(parts))
	for _, part := range parts {
		ep, err := parseEndpoint(strings.TrimSpace(part))
		if err != nil {
			return nil, err
		}
		endpoints = append(endpoints, ep)
	}
	if len(endpoints) == 0 {
		return nil, errors.New("dns resolver list is empty")
	}
	if client == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		// DoH bootstrap deliberately bypasses application proxy settings so it
		// cannot recurse through the local PxGo listener. A hostname DoH endpoint
		// is bootstrapped by the OS resolver; target names are not.
		transport.Proxy = nil
		client = &http.Client{Transport: transport}
	}
	return &Policy{endpoints: endpoints, timeout: normalizedTimeout(timeout), http: client}, nil
}

func normalizedTimeout(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return defaultLookupTimeout
	}
	return timeout
}

func parseEndpoint(raw string) (endpoint, error) {
	if raw == "" {
		return endpoint{}, errors.New("empty dns resolver endpoint")
	}
	if strings.EqualFold(raw, resolverModeSystem) {
		return endpoint{}, errors.New("system cannot be mixed with configured dns endpoints")
	}
	if !strings.Contains(raw, "://") {
		addr, err := normalizeClassicAddress(raw)
		if err != nil {
			return endpoint{}, err
		}
		return endpoint{scheme: resolverSchemeUDP, addr: addr, label: resolverSchemeUDP + "://" + addr}, nil
	}

	u, err := url.Parse(raw)
	if err != nil {
		return endpoint{}, fmt.Errorf("invalid dns resolver endpoint: %w", err)
	}
	if u.User != nil || u.Fragment != "" || u.RawQuery != "" {
		return endpoint{}, errors.New("dns resolver must not contain credentials, query, or fragment")
	}
	switch strings.ToLower(u.Scheme) {
	case resolverSchemeUDP, resolverSchemeTCP:
		if u.Path != "" && u.Path != "/" {
			return endpoint{}, fmt.Errorf("classic dns resolver %q must not contain a path", raw)
		}
		addr, err := normalizeClassicAddress(u.Host)
		if err != nil {
			return endpoint{}, err
		}
		scheme := strings.ToLower(u.Scheme)
		return endpoint{scheme: scheme, addr: addr, label: scheme + "://" + addr}, nil
	case resolverSchemeHTTPS:
		if u.Hostname() == "" {
			return endpoint{}, fmt.Errorf("DoH resolver %q has no host", raw)
		}
		if err := validatePort(u.Port()); err != nil {
			return endpoint{}, fmt.Errorf("DoH resolver %q: %w", raw, err)
		}
		if u.Path == "" || u.Path == "/" {
			return endpoint{}, fmt.Errorf("DoH resolver %q must include an endpoint path", raw)
		}
		u.Scheme = resolverSchemeHTTPS
		return endpoint{scheme: resolverSchemeHTTPS, url: u, label: resolverSchemeHTTPS + "://" + u.Host}, nil
	default:
		return endpoint{}, fmt.Errorf("unsupported dns resolver scheme %q", u.Scheme)
	}
}

func normalizeClassicAddress(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("classic dns resolver has no address")
	}
	if ip := net.ParseIP(strings.Trim(raw, "[]")); ip != nil {
		return net.JoinHostPort(ip.String(), defaultDNSPort), nil
	}
	host, port, err := net.SplitHostPort(raw)
	if err != nil {
		return "", fmt.Errorf("classic dns resolver %q must be an IP or IP:port", raw)
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil {
		return "", fmt.Errorf("classic dns resolver %q must use an IP literal to avoid bootstrap DNS", raw)
	}
	if err := validatePort(port); err != nil {
		return "", fmt.Errorf("classic dns resolver %q: %w", raw, err)
	}
	return net.JoinHostPort(ip.String(), port), nil
}

func validatePort(port string) error {
	if port == "" {
		return nil
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("invalid port %q", port)
	}
	return nil
}

func (p *Policy) IsSystem() bool { return p == nil || p.system }

func (p *Policy) Status() Status {
	if p == nil || p.system {
		return Status{Mode: resolverModeSystem}
	}
	status := Status{Mode: p.mode()}
	p.appendStatusEndpoints(&status)
	p.mu.RLock()
	status.LastError = p.lastError
	p.mu.RUnlock()
	return status
}

func (p *Policy) appendStatusEndpoints(status *Status) {
	for _, ep := range p.endpoints {
		status.Endpoints = append(status.Endpoints, ep.label)
		if ep.scheme == resolverSchemeHTTPS && net.ParseIP(ep.url.Hostname()) == nil {
			status.Bootstrap = resolverModeSystem
		}
	}
	for _, rule := range p.rules {
		if rule.resolver != nil {
			rule.resolver.appendStatusEndpoints(status)
		}
	}
}

func (p *Policy) mode() string {
	hasClassic, hasDoH := false, false
	var visit func(*Policy)
	visit = func(policy *Policy) {
		if policy == nil {
			return
		}
		for _, ep := range policy.endpoints {
			if ep.scheme == resolverSchemeHTTPS {
				hasDoH = true
			} else {
				hasClassic = true
			}
		}
		for _, rule := range policy.rules {
			visit(rule.resolver)
		}
	}
	visit(p)
	switch {
	case hasClassic && hasDoH:
		return "mixed"
	case hasDoH:
		return "doh"
	case hasClassic:
		return resolverModeDNS
	default:
		return resolverModeSystem
	}
}

// DialContext connects to address using system dialing in compatibility mode
// and explicit cached resolution in configured DNS/DoH modes.
func (p *Policy) DialContext(ctx context.Context, network, address string, lookup LookupFunc) (net.Conn, error) {
	timeout := defaultLookupTimeout
	if p != nil {
		timeout = p.timeout
	}
	dialer := net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	if p == nil || p.system {
		return dialer.DialContext(ctx, network, address)
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	if net.ParseIP(strings.Trim(host, "[]")) != nil {
		return dialer.DialContext(ctx, network, address)
	}
	if lookup == nil {
		lookup = p.LookupIP
	}
	ips, err := lookup(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("dns resolver returned no addresses for %s", host)
	}
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var failures []error
	for _, ip := range ips {
		if strings.HasSuffix(network, "4") && ip.To4() == nil {
			continue
		}
		if strings.HasSuffix(network, "6") && ip.To4() != nil {
			continue
		}
		candidate := net.JoinHostPort(ip.String(), port)
		conn, err := dialer.DialContext(dialCtx, network, candidate)
		if err == nil {
			return conn, nil
		}
		failures = append(failures, err)
		if dialCtx.Err() != nil {
			return nil, dialCtx.Err()
		}
	}
	if len(failures) == 0 {
		return nil, fmt.Errorf("dns resolver returned no usable addresses for %s", host)
	}
	return nil, errors.Join(failures...)
}

// LookupIP resolves host according to the configured policy. Literal IPs never
// perform a DNS request.
func (p *Policy) LookupIP(ctx context.Context, host string) ([]net.IP, error) {
	ips, _, err := p.LookupIPTTL(ctx, host)
	return ips, err
}

// LookupIPTTL resolves host and returns the minimum TTL of matching A/AAAA
// records for explicit DNS/DoH policies. System DNS does not expose TTL here.
func (p *Policy) LookupIPTTL(ctx context.Context, host string) ([]net.IP, time.Duration, error) {
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		return []net.IP{append(net.IP(nil), ip...)}, 0, nil
	}
	if p == nil || p.system {
		ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
		return ips, 0, err
	}
	if len(p.rules) > 0 {
		for _, rule := range p.rules {
			if matchesAny(host, rule.bypass) {
				ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
				p.recordError(err)
				return ips, 0, err
			}
			if len(rule.only) > 0 && !matchesAny(host, rule.only) {
				continue
			}
			ips, ttl, err := rule.resolver.LookupIPTTL(ctx, host)
			p.recordError(err)
			return ips, ttl, err
		}
		ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
		p.recordError(err)
		return ips, 0, err
	}
	name, err := dnsName(host)
	if err != nil {
		p.recordError(err)
		return nil, 0, err
	}
	var failures []error
	for _, ep := range p.endpoints {
		epCtx, cancel := context.WithTimeout(ctx, p.timeout)
		ips, ttl, terminal, err := p.lookupEndpoint(epCtx, ep, name)
		cancel()
		if err == nil {
			p.recordError(nil)
			return ips, ttl, nil
		}
		if terminal {
			p.recordError(err)
			return nil, 0, err
		}
		failures = append(failures, fmt.Errorf("%s: %w", ep.label, err))
		if ctx.Err() != nil {
			p.recordError(ctx.Err())
			return nil, 0, ctx.Err()
		}
	}
	err = errors.Join(failures...)
	p.recordError(err)
	return nil, 0, err
}

func dnsName(host string) (dnsmessage.Name, error) {
	host = strings.TrimSpace(strings.TrimSuffix(host, "."))
	if host == "" {
		return dnsmessage.Name{}, errors.New("empty dns hostname")
	}
	name, err := dnsmessage.NewName(host + ".")
	if err != nil {
		return dnsmessage.Name{}, fmt.Errorf("invalid dns hostname %q: %w", host, err)
	}
	return name, nil
}

func (p *Policy) lookupEndpoint(ctx context.Context, ep endpoint, name dnsmessage.Name) ([]net.IP, time.Duration, bool, error) {
	var ips []net.IP
	var ttl time.Duration
	ttlSet := false
	var familyErrors []error
	terminalNegative := true
	for _, qtype := range []dnsmessage.Type{dnsmessage.TypeA, dnsmessage.TypeAAAA} {
		query, id, err := buildQuery(name, qtype)
		if err != nil {
			return nil, 0, false, err
		}
		response, err := p.exchange(ctx, ep, query)
		if err != nil {
			familyErrors = append(familyErrors, err)
			terminalNegative = false
			continue
		}
		familyIPs, familyTTL, terminal, err := parseResponseTTL(response, id, name, qtype)
		if err != nil {
			familyErrors = append(familyErrors, err)
			if !terminal {
				terminalNegative = false
			}
			continue
		}
		ips = append(ips, familyIPs...)
		if !ttlSet || familyTTL < ttl {
			ttl = familyTTL
			ttlSet = true
		}
	}
	if len(ips) > 0 {
		return dedupeIPs(ips), ttl, false, nil
	}
	if len(familyErrors) == 0 {
		return nil, 0, true, &responseError{msg: "dns response contains no A or AAAA records", terminal: true}
	}
	return nil, 0, terminalNegative, errors.Join(familyErrors...)
}

func buildQuery(name dnsmessage.Name, qtype dnsmessage.Type) ([]byte, uint16, error) {
	var idBytes [2]byte
	if _, err := io.ReadFull(rand.Reader, idBytes[:]); err != nil {
		return nil, 0, fmt.Errorf("generate dns query id: %w", err)
	}
	id := binary.BigEndian.Uint16(idBytes[:])
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: id, RecursionDesired: true})
	if err := b.StartQuestions(); err != nil {
		return nil, 0, err
	}
	if err := b.Question(dnsmessage.Question{Name: name, Type: qtype, Class: dnsmessage.ClassINET}); err != nil {
		return nil, 0, err
	}
	msg, err := b.Finish()
	return msg, id, err
}

func (p *Policy) exchange(parent context.Context, ep endpoint, query []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, p.timeout)
	defer cancel()
	switch ep.scheme {
	case resolverSchemeUDP:
		response, truncated, err := exchangeUDP(ctx, ep.addr, query)
		if err != nil {
			return nil, err
		}
		if truncated {
			return exchangeTCP(ctx, ep.addr, query)
		}
		return response, nil
	case resolverSchemeTCP:
		return exchangeTCP(ctx, ep.addr, query)
	case resolverSchemeHTTPS:
		return p.exchangeDoH(ctx, ep.url, query)
	default:
		return nil, fmt.Errorf("unsupported resolver transport %q", ep.scheme)
	}
}

func exchangeUDP(ctx context.Context, addr string, query []byte) ([]byte, bool, error) {
	d := net.Dialer{}
	conn, err := d.DialContext(ctx, "udp", addr)
	if err != nil {
		return nil, false, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if _, err := conn.Write(query); err != nil {
		return nil, false, contextError(ctx, err)
	}
	buf := make([]byte, maxDNSMessageBytes)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, false, contextError(ctx, err)
	}
	response := append([]byte(nil), buf[:n]...)
	var parser dnsmessage.Parser
	header, err := parser.Start(response)
	if err != nil {
		return nil, false, &protocolError{msg: "malformed UDP DNS response header"}
	}
	return response, header.Truncated, nil
}

func exchangeTCP(ctx context.Context, addr string, query []byte) ([]byte, error) {
	d := net.Dialer{}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if len(query) > maxDNSMessageBytes {
		return nil, errors.New("dns query too large")
	}
	frame := make([]byte, 2+len(query))
	// #nosec G115 -- query length is bounded to maxDNSMessageBytes (uint16 max) above.
	binary.BigEndian.PutUint16(frame[:2], uint16(len(query)))
	copy(frame[2:], query)
	if _, err := conn.Write(frame); err != nil {
		return nil, contextError(ctx, err)
	}
	var length [2]byte
	if _, err := io.ReadFull(conn, length[:]); err != nil {
		return nil, contextError(ctx, err)
	}
	n := int(binary.BigEndian.Uint16(length[:]))
	if n == 0 || n > maxDNSMessageBytes {
		return nil, &protocolError{msg: fmt.Sprintf("invalid TCP DNS response length %d", n)}
	}
	response := make([]byte, n)
	if _, err := io.ReadFull(conn, response); err != nil {
		return nil, contextError(ctx, err)
	}
	return response, nil
}

func (p *Policy) exchangeDoH(ctx context.Context, endpointURL *url.URL, query []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL.String(), bytes.NewReader(query))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/dns-message")
	req.Header.Set("Accept", "application/dns-message")
	resp, err := p.http.Do(req)
	if err != nil {
		return nil, contextError(ctx, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("DoH endpoint returned %s", resp.Status)
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/dns-message") {
		return nil, &protocolError{msg: fmt.Sprintf("DoH endpoint returned invalid content type %q", resp.Header.Get("Content-Type"))}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDNSMessageBytes+1))
	if err != nil {
		return nil, contextError(ctx, err)
	}
	if len(data) == 0 || len(data) > maxDNSMessageBytes {
		return nil, &protocolError{msg: "DoH response size is invalid"}
	}
	return data, nil
}

func parseResponse(msg []byte, wantID uint16, wantName dnsmessage.Name, wantType dnsmessage.Type) ([]net.IP, bool, error) {
	ips, _, terminal, err := parseResponseTTL(msg, wantID, wantName, wantType)
	return ips, terminal, err
}

func parseResponseTTL(msg []byte, wantID uint16, wantName dnsmessage.Name, wantType dnsmessage.Type) ([]net.IP, time.Duration, bool, error) {
	var parser dnsmessage.Parser
	header, err := parser.Start(msg)
	if err != nil {
		return nil, 0, false, &protocolError{msg: "malformed DNS response header"}
	}
	if !header.Response || header.ID != wantID {
		return nil, 0, false, &protocolError{msg: "DNS response does not match query id"}
	}
	questions, err := parser.AllQuestions()
	if err != nil || len(questions) != 1 {
		return nil, 0, false, &protocolError{msg: "DNS response has invalid question section"}
	}
	q := questions[0]
	if !sameName(q.Name, wantName) || q.Type != wantType || q.Class != dnsmessage.ClassINET {
		return nil, 0, false, &protocolError{msg: "DNS response question does not match request"}
	}
	switch header.RCode {
	case dnsmessage.RCodeSuccess:
	case dnsmessage.RCodeNameError:
		return nil, 0, true, &responseError{msg: "dns name does not exist", terminal: true}
	default:
		return nil, 0, false, &responseError{msg: "dns response code " + header.RCode.String(), terminal: false}
	}
	answers, err := parser.AllAnswers()
	if err != nil {
		return nil, 0, false, &protocolError{msg: "malformed DNS answer section"}
	}

	allowed := map[string]bool{canonicalName(wantName): true}
	for changed := true; changed; {
		changed = false
		for _, answer := range answers {
			if !allowed[canonicalName(answer.Header.Name)] {
				continue
			}
			cname, ok := answer.Body.(*dnsmessage.CNAMEResource)
			if !ok {
				continue
			}
			key := canonicalName(cname.CNAME)
			if !allowed[key] {
				allowed[key] = true
				changed = true
			}
		}
	}
	var ips []net.IP
	var ttl time.Duration
	ttlSet := false
	for _, answer := range answers {
		if answer.Header.Class != dnsmessage.ClassINET || !allowed[canonicalName(answer.Header.Name)] {
			continue
		}
		switch body := answer.Body.(type) {
		case *dnsmessage.CNAMEResource:
			recordTTL := time.Duration(answer.Header.TTL) * time.Second
			if !ttlSet || recordTTL < ttl {
				ttl = recordTTL
				ttlSet = true
			}
		case *dnsmessage.AResource:
			if wantType == dnsmessage.TypeA {
				ips = append(ips, net.IPv4(body.A[0], body.A[1], body.A[2], body.A[3]))
				recordTTL := time.Duration(answer.Header.TTL) * time.Second
				if !ttlSet || recordTTL < ttl {
					ttl = recordTTL
					ttlSet = true
				}
			}
		case *dnsmessage.AAAAResource:
			if wantType == dnsmessage.TypeAAAA {
				ip := make(net.IP, net.IPv6len)
				copy(ip, body.AAAA[:])
				ips = append(ips, ip)
				recordTTL := time.Duration(answer.Header.TTL) * time.Second
				if !ttlSet || recordTTL < ttl {
					ttl = recordTTL
					ttlSet = true
				}
			}
		}
	}
	if len(ips) == 0 {
		return nil, 0, true, &responseError{msg: "dns response contains no matching address records", terminal: true}
	}
	return ips, ttl, true, nil
}

func sameName(a, b dnsmessage.Name) bool {
	return canonicalName(a) == canonicalName(b)
}

func canonicalName(name dnsmessage.Name) string {
	return strings.ToLower(name.String())
}

func dedupeIPs(ips []net.IP) []net.IP {
	seen := map[string]bool{}
	out := make([]net.IP, 0, len(ips))
	for _, ip := range ips {
		key := ip.String()
		if key == "<nil>" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, append(net.IP(nil), ip...))
	}
	return out
}

func contextError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return err
}

func (p *Policy) recordError(err error) {
	value := ""
	if err != nil {
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			value = "timeout"
		case errors.Is(err, context.Canceled):
			value = "canceled"
		default:
			var proto *protocolError
			var resp *responseError
			switch {
			case errors.As(err, &proto):
				value = "protocol"
			case errors.As(err, &resp):
				value = "dns-response"
			default:
				value = "transport"
			}
		}
	}
	p.mu.Lock()
	p.lastError = value
	p.mu.Unlock()
}
