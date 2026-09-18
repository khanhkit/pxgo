package wproxy

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/pavelsimo/pxgo/internal/dnscache"
	"github.com/pavelsimo/pxgo/internal/pac"
	"github.com/pavelsimo/pxgo/internal/systemproxy"
)

const (
	ModeNone = iota
	ModeAuto
	ModePAC
	ModeManual
	ModeEnv
	ModeConfig
	ModeConfigPAC
)

const (
	directHost  = "DIRECT"
	directKey   = "direct://DIRECT:80"
	httpScheme  = "http"
	httpsScheme = "https"
)

var Direct = Server{Host: directHost, Port: 80, Scheme: "direct"}

type Server struct {
	Host   string
	Port   int
	Scheme string
}

type IPSet struct {
	nets   []*net.IPNet
	ranges [][2]net.IP
}

func (s *IPSet) AddCIDR(cidr string) error {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		ip := net.ParseIP(cidr)
		if ip == nil {
			return errors.New("bad ip")
		}
		bits := 128
		if ip4 := ip.To4(); ip4 != nil {
			bits = 32
			ip = ip4
		}
		ipnet = &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)}
	}
	s.nets = append(s.nets, ipnet)
	return nil
}

func (s *IPSet) AddRange(start, end string) error {
	a := net.ParseIP(start).To4()
	b := net.ParseIP(end).To4()
	if a == nil || b == nil {
		return errors.New("bad range")
	}
	s.ranges = append(s.ranges, [2]net.IP{a, b})
	return nil
}

func (s IPSet) Contains(ip net.IP) bool {
	if ip == nil {
		return false
	}
	for _, n := range s.nets {
		if n.Contains(ip) {
			return true
		}
	}
	// Ranges are IPv4-only (see AddRange).
	if ip4 := ip.To4(); ip4 != nil {
		for _, r := range s.ranges {
			if compareIP(ip4, r[0]) >= 0 && compareIP(ip4, r[1]) <= 0 {
				return true
			}
		}
	}
	return false
}

func (s IPSet) Size() int {
	return len(s.nets) + len(s.ranges)
}

func compareIP(a, b net.IP) int {
	for i := 0; i < 4; i++ {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	return 0
}

type noProxyMatcher struct {
	host           string
	port           int
	subdomainsOnly bool
	local          bool
}

func ParseProxy(proxystrs string) ([]Server, error) {
	var servers []Server
	seen := map[string]bool{}
	if strings.TrimSpace(proxystrs) == "" {
		return servers, nil
	}
	for _, item := range strings.Split(proxystrs, ",") {
		proxystr := strings.TrimSpace(item)
		if proxystr == "" {
			continue
		}
		if strings.EqualFold(proxystr, directHost) {
			if !seen[directKey] {
				servers = append(servers, Direct)
				seen[directKey] = true
			}
			continue
		}
		server, err := parseProxyEndpoint(proxystr)
		if err != nil {
			return nil, err
		}
		key := server.Scheme + "://" + net.JoinHostPort(strings.ToLower(server.Host), strconv.Itoa(server.Port))
		if !seen[key] {
			servers = append(servers, server)
			seen[key] = true
		}
	}
	return servers, nil
}

func parseProxyEndpoint(raw string) (Server, error) {
	proxystr := strings.TrimSpace(raw)
	if proxystr == "" {
		return Server{}, errors.New("empty proxy server")
	}

	scheme := httpScheme
	host := ""
	var port int
	if strings.Contains(proxystr, "://") {
		u, err := url.Parse(proxystr)
		if err != nil {
			return Server{}, fmt.Errorf("bad proxy server: %w", err)
		}
		scheme = strings.ToLower(u.Scheme)
		if !validProxyScheme(scheme) {
			return Server{}, fmt.Errorf("unsupported proxy scheme: %s", scheme)
		}
		if u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
			return Server{}, fmt.Errorf("proxy server must be an authority, got %q", proxystr)
		}
		host = u.Hostname()
		port = defaultProxyPort(scheme)
		if u.Port() != "" {
			port, err = parsePort(u.Port())
			if err != nil {
				return Server{}, fmt.Errorf("bad proxy server port: %w", err)
			}
		}
	} else {
		var err error
		host, port, err = parseAuthority(proxystr, 80)
		if err != nil {
			return Server{}, fmt.Errorf("bad proxy server: %w", err)
		}
	}
	if err := validateHost(host); err != nil {
		return Server{}, fmt.Errorf("bad proxy server host: %w", err)
	}
	return Server{Host: host, Port: port, Scheme: scheme}, nil
}

func validProxyScheme(scheme string) bool {
	switch scheme {
	case httpScheme, httpsScheme, "socks", "socks4", "socks4a", "socks5":
		return true
	default:
		return false
	}
}

func defaultProxyPort(scheme string) int {
	switch scheme {
	case httpsScheme:
		return 443
	case "socks", "socks4", "socks4a", "socks5":
		return 1080
	default:
		return 80
	}
}

func parsePort(raw string) (int, error) {
	port, err := strconv.Atoi(raw)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("invalid port %q", raw)
	}
	return port, nil
}

func parseAuthority(raw string, defaultPort int) (string, int, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", 0, errors.New("empty authority")
	}
	if ip := net.ParseIP(value); ip != nil {
		return ip.String(), defaultPort, nil
	}
	if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		ip := net.ParseIP(strings.TrimSuffix(strings.TrimPrefix(value, "["), "]"))
		if ip == nil {
			return "", 0, fmt.Errorf("invalid bracketed IP %q", value)
		}
		return ip.String(), defaultPort, nil
	}
	if strings.Contains(value, ":") {
		host, portText, err := net.SplitHostPort(value)
		if err != nil {
			return "", 0, err
		}
		port, err := parsePort(portText)
		if err != nil {
			return "", 0, err
		}
		if err := validateHost(host); err != nil {
			return "", 0, err
		}
		return host, port, nil
	}
	if err := validateHost(value); err != nil {
		return "", 0, err
	}
	return value, defaultPort, nil
}

func validateHost(host string) error {
	if strings.TrimSpace(host) == "" {
		return errors.New("empty host")
	}
	if strings.ContainsAny(host, " \t\r\n/?#[]") {
		return fmt.Errorf("invalid host %q", host)
	}
	return nil
}

func ParseNoProxy(noproxystr string, iponly bool) (IPSet, map[string]bool, error) {
	set, hosts, _, err := parseNoProxy(noproxystr, iponly)
	return set, hosts, err
}

func parseNoProxy(noproxystr string, iponly bool) (IPSet, map[string]bool, []noProxyMatcher, error) {
	set := IPSet{}
	hosts := map[string]bool{}
	var matchers []noProxyMatcher
	if strings.TrimSpace(noproxystr) == "" {
		return set, hosts, matchers, nil
	}
	repl := strings.NewReplacer(";", ",", " ", ",")
	for _, raw := range strings.Split(repl.Replace(strings.ToLower(noproxystr)), ",") {
		bypass := strings.TrimSpace(raw)
		if bypass == "" {
			continue
		}
		if bypass == "<local>" {
			hosts["localhost"] = true
			if err := set.AddCIDR("127.0.0.0/8"); err != nil {
				return set, hosts, matchers, err
			}
			if err := set.AddCIDR("::1/128"); err != nil {
				return set, hosts, matchers, err
			}
			if !iponly {
				matchers = append(matchers, noProxyMatcher{local: true})
			}
			continue
		}
		if bypass == "*" && !iponly {
			hosts[bypass] = true
			matchers = append(matchers, noProxyMatcher{host: "*"})
			continue
		}
		if ip := parseIPToken(bypass); ip != nil {
			if err := set.AddCIDR(ip.String()); err != nil {
				return set, hosts, matchers, err
			}
			continue
		}
		if strings.Contains(bypass, "/") {
			if err := set.AddCIDR(bypass); err != nil {
				return set, hosts, matchers, fmt.Errorf("bad no-proxy network %q: %w", bypass, err)
			}
			continue
		}
		if looksLikeIPRange(bypass) {
			a, b, _ := strings.Cut(bypass, "-")
			if err := set.AddRange(a, b); err != nil {
				return set, hosts, matchers, fmt.Errorf("bad no-proxy range %q: %w", bypass, err)
			}
			continue
		}
		if strings.Contains(bypass, "*") {
			if isIPv4GlobCandidate(bypass) {
				if err := addGlob(&set, bypass); err != nil {
					return set, hosts, matchers, fmt.Errorf("bad no-proxy glob %q: %w", bypass, err)
				}
				continue
			}
			if !iponly && strings.HasPrefix(bypass, "*.") {
				host := normalizeHostRule(strings.TrimPrefix(bypass, "*."))
				if err := validateHost(host); err != nil {
					return set, hosts, matchers, fmt.Errorf("bad no-proxy host wildcard %q: %w", bypass, err)
				}
				hosts["*."+host] = true
				matchers = append(matchers, noProxyMatcher{host: host, subdomainsOnly: true})
				continue
			}
			return set, hosts, matchers, fmt.Errorf("unsupported no-proxy wildcard %q", bypass)
		}
		if iponly {
			return set, hosts, matchers, fmt.Errorf("bad ip: %s", bypass)
		}
		if strings.Contains(bypass, ":") {
			host, port, err := parseNoProxyHostPort(bypass)
			if err != nil {
				return set, hosts, matchers, err
			}
			key := net.JoinHostPort(host, strconv.Itoa(port))
			hosts[key] = true
			matchers = append(matchers, noProxyMatcher{host: normalizeHostRule(host), port: port})
			continue
		}
		host := normalizeHostRule(bypass)
		if err := validateHost(host); err != nil {
			return set, hosts, matchers, fmt.Errorf("bad no-proxy host %q: %w", bypass, err)
		}
		hosts[bypass] = true
		matchers = append(matchers, noProxyMatcher{host: host})
	}
	return set, hosts, matchers, nil
}

func parseIPToken(token string) net.IP {
	if strings.HasPrefix(token, "[") && strings.HasSuffix(token, "]") {
		token = strings.TrimSuffix(strings.TrimPrefix(token, "["), "]")
	}
	return net.ParseIP(token)
}

func looksLikeIPRange(token string) bool {
	if strings.Count(token, "-") != 1 {
		return false
	}
	a, b, _ := strings.Cut(token, "-")
	return looksLikeIP(a) && looksLikeIP(b)
}

func looksLikeIP(token string) bool {
	return net.ParseIP(token) != nil || strings.Count(token, ".") == 3 || strings.Contains(token, ":")
}

func isIPv4GlobCandidate(token string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 4 {
		return false
	}
	for _, part := range parts {
		if part == "*" {
			continue
		}
		if _, err := strconv.Atoi(part); err != nil {
			return false
		}
	}
	return true
}

func parseNoProxyHostPort(token string) (string, int, error) {
	host, portText, err := net.SplitHostPort(token)
	if err != nil {
		return "", 0, fmt.Errorf("bad no-proxy host:port %q: %w", token, err)
	}
	if err := validateHost(host); err != nil {
		return "", 0, fmt.Errorf("bad no-proxy host:port %q: %w", token, err)
	}
	port, err := parsePort(portText)
	if err != nil {
		return "", 0, fmt.Errorf("bad no-proxy host:port %q: %w", token, err)
	}
	return host, port, nil
}

func normalizeHostRule(host string) string {
	return strings.TrimSuffix(strings.TrimPrefix(strings.ToLower(host), "."), ".")
}

func addGlob(set *IPSet, glob string) error {
	parts := strings.Split(glob, ".")
	if len(parts) != 4 {
		return errors.New("bad glob")
	}
	start := make([]string, 4)
	end := make([]string, 4)
	for i, p := range parts {
		if p == "*" {
			start[i] = "0"
			end[i] = "255"
		} else if _, err := strconv.Atoi(p); err == nil {
			start[i] = p
			end[i] = p
		} else {
			return errors.New("bad glob")
		}
	}
	return set.AddRange(strings.Join(start, "."), strings.Join(end, "."))
}

type Wproxy struct {
	Mode            int
	Servers         []Server
	NoProxy         IPSet
	NoProxyHosts    map[string]bool
	NoProxyHostsStr string
	PAC             *pac.Pac
	noProxyMatchers []noProxyMatcher
}

func New(mode int, servers []Server, noproxy, pacEncoding string) (*Wproxy, error) {
	np, hosts, matchers, err := parseNoProxy(noproxy, false)
	if err != nil {
		return nil, err
	}
	w := &Wproxy{Mode: mode, Servers: servers, NoProxy: np, NoProxyHosts: hosts, noProxyMatchers: matchers}
	if mode == ModeConfigPAC && len(servers) > 0 {
		w.PAC = pac.New(servers[0].Host, pacEncoding)
		if err := w.PAC.Load(); err != nil {
			return nil, fmt.Errorf("load configured PAC: %w", err)
		}
	}
	if mode == ModeNone {
		if env := firstEnv("http_proxy", "HTTP_PROXY"); env != "" {
			parsed, err := ParseProxy(env)
			if err != nil {
				return nil, err
			}
			w.Mode = ModeEnv
			w.Servers = parsed
			if no := firstEnv("no_proxy", "NO_PROXY"); no != "" {
				if err := mergeNoProxy(w, no); err != nil {
					return nil, fmt.Errorf("parse no_proxy: %w", err)
				}
			}
		}
		if w.Mode == ModeNone {
			sysproxy := systemproxy.Discover()
			switch {
			case sysproxy.Found && sysproxy.AutoDetect:
				w.Mode = ModeAuto
				if err := mergeNoProxy(w, sysproxy.Bypass); err != nil {
					return nil, fmt.Errorf("parse system proxy bypass: %w", err)
				}
			case sysproxy.Found && sysproxy.IsPAC:
				w.Mode = ModePAC
				w.Servers = []Server{{Host: sysproxy.PACURL, Scheme: "pac"}}
				if err := mergeNoProxy(w, sysproxy.Bypass); err != nil {
					return nil, fmt.Errorf("parse system proxy bypass: %w", err)
				}
			case sysproxy.Found:
				parsed, err := ParseProxy(sysproxy.ManualProxy)
				if err != nil {
					return nil, err
				}
				w.Mode = ModeManual
				w.Servers = parsed
				if err := mergeNoProxy(w, sysproxy.Bypass); err != nil {
					return nil, fmt.Errorf("parse system proxy bypass: %w", err)
				}
			}
		}
	}
	var hostList []string
	for h := range w.NoProxyHosts {
		hostList = append(hostList, h)
	}
	w.NoProxyHostsStr = strings.Join(hostList, ",")
	return w, nil
}

func mergeNoProxy(w *Wproxy, noproxy string) error {
	if noproxy == "" {
		return nil
	}
	np2, hosts2, matchers2, err := parseNoProxy(noproxy, false)
	if err != nil {
		return err
	}
	w.NoProxy.nets = append(w.NoProxy.nets, np2.nets...)
	w.NoProxy.ranges = append(w.NoProxy.ranges, np2.ranges...)
	w.noProxyMatchers = append(w.noProxyMatchers, matchers2...)
	for h := range hosts2 {
		w.NoProxyHosts[h] = true
	}
	return nil
}

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if val := os.Getenv(key); val != "" {
			return val
		}
	}
	return ""
}

func (w *Wproxy) GetNetloc(rawurl string) (Server, string, error) {
	if !strings.Contains(rawurl, "://") {
		host, port, err := parseAuthority(rawurl, 80)
		if err != nil {
			return Server{}, "", fmt.Errorf("bad target authority: %w", err)
		}
		return Server{Host: host, Port: port}, "/", nil
	}
	u, err := url.Parse(rawurl)
	if err != nil {
		return Server{}, "", err
	}
	host := u.Hostname()
	if err := validateHost(host); err != nil {
		return Server{}, "", fmt.Errorf("bad target host: %w", err)
	}
	port := 0
	if u.Port() != "" {
		port, err = parsePort(u.Port())
		if err != nil {
			return Server{}, "", fmt.Errorf("bad target port: %w", err)
		}
	} else {
		switch strings.ToLower(u.Scheme) {
		case httpsScheme:
			port = 443
		case "ftp":
			port = 21
		default:
			port = 80
		}
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	if u.RawQuery != "" {
		path += "?" + u.RawQuery
	}
	return Server{Host: host, Port: port, Scheme: strings.ToLower(u.Scheme)}, path, nil
}

// FindProxyForURL resolves the proxy candidates for rawurl. The returned
// slice may be shared with the Wproxy and must be treated as read-only.
func (w *Wproxy) FindProxyForURL(rawurl string) ([]Server, Server, string, error) {
	netloc, path, err := w.GetNetloc(rawurl)
	if err != nil {
		return nil, Server{}, "", err
	}
	if w.Mode == ModeNone {
		return []Server{Direct}, netloc, path, nil
	}
	if w.isNoProxy(netloc) {
		return []Server{Direct}, netloc, path, nil
	}
	if w.Mode == ModeConfigPAC && w.PAC != nil {
		out, err := w.PAC.FindProxyForURLWithError(rawurl, netloc.Host)
		if err != nil {
			return nil, netloc, path, fmt.Errorf("evaluate configured PAC: %w", err)
		}
		return parseProxyOrDirect(out), netloc, path, nil
	}
	if w.Mode == ModeAuto || w.Mode == ModePAC {
		cfg := systemproxy.Config{AutoDetect: w.Mode == ModeAuto, IsPAC: w.Mode == ModePAC}
		if w.Mode == ModePAC && len(w.Servers) > 0 {
			cfg.PACURL = w.Servers[0].Host
		}
		out, _ := systemproxy.ResolveProxyForURL(rawurl, cfg)
		if strings.TrimSpace(out) == "" {
			return []Server{Direct}, netloc, path, nil
		}
		return parseProxyOrDirect(out), netloc, path, nil
	}
	return w.Servers, netloc, path, nil
}

func parseProxyOrDirect(proxy string) []Server {
	servers, err := ParseProxy(proxy)
	if err != nil {
		return []Server{Direct}
	}
	return servers
}

func (w *Wproxy) isNoProxy(netloc Server) bool {
	host := normalizeHostRule(netloc.Host)
	if w.hostMatchesNoProxy(netloc) {
		return true
	}
	if w.NoProxy.Size() == 0 {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return w.NoProxy.Contains(ip)
	}
	for _, ip := range dnscache.Lookup(host) {
		if w.NoProxy.Contains(ip) {
			return true
		}
	}
	return false
}

func (w *Wproxy) hostMatchesNoProxy(netloc Server) bool {
	host := normalizeHostRule(netloc.Host)
	for _, matcher := range w.noProxyMatchers {
		if matcher.local {
			if net.ParseIP(host) == nil && !strings.Contains(host, ".") {
				return true
			}
			continue
		}
		if matcher.port != 0 && matcher.port != netloc.Port {
			continue
		}
		if matcher.host == "*" {
			return true
		}
		if matcher.subdomainsOnly {
			if strings.HasSuffix(host, "."+matcher.host) {
				return true
			}
			continue
		}
		if host == matcher.host || strings.HasSuffix(host, "."+matcher.host) {
			return true
		}
	}
	return false
}
