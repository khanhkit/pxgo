package pac

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/dop251/goja"

	"github.com/pavelsimo/pxgo/internal/dnscache"
)

const (
	directProxy       = "DIRECT"
	localhostIP       = "127.0.0.1"
	utf8Encoding      = "utf-8"
	maxPACBytes       = 4 << 20
	maxPACResultBytes = 16 << 10
	maxPACCandidates  = 32
)

var errPACExecutionTimeout = errors.New("PAC JavaScript execution timeout")

// Overridable in tests.
var (
	pacHTTPTimeout   = 10 * time.Second
	pacExecTimeout   = 2 * time.Second
	pacRetryInterval = 30 * time.Second
	interfaceAddrs   = net.InterfaceAddrs
)

var pacHTTPTransport = func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	return t
}()

type Pac struct {
	location string
	encoding string

	// mu guards generation (re)loading. Each active pacRuntime separately
	// serializes evaluation on its one shared JavaScript global state.
	mu              sync.Mutex
	lastLoadAttempt time.Time
	lastLoadErr     error
	runtime         atomic.Pointer[pacRuntime]
}

// pacRuntime owns one compiled PAC script and one persistent JS global state.
// Evaluations are serialized at this state boundary so mutable PAC globals have
// deterministic browser-like generation semantics rather than per-pool-VM state.
type pacRuntime struct {
	program *goja.Program
	owner   *Pac
	myIP    string

	vmMu sync.Mutex
	vm   *pacVM
}

type pacVM struct {
	vm *goja.Runtime
	fn goja.Callable
}

func New(location, encoding string) *Pac {
	if encoding == "" {
		encoding = utf8Encoding
	}
	return &Pac{location: location, encoding: encoding}
}

func (p *Pac) Loaded() bool {
	return p.runtime.Load() != nil
}

func (p *Pac) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.runtime.Store(nil)
	p.lastLoadAttempt = time.Time{} // allow an immediate reload
	p.lastLoadErr = nil
}

// Load fetches, compiles, and validates one PAC generation before activation.
func (p *Pac) Load() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.runtime.Load() != nil {
		return nil
	}
	rt, err := p.load()
	p.lastLoadAttempt = time.Now()
	p.lastLoadErr = err
	if err != nil {
		return err
	}
	p.runtime.Store(rt)
	return nil
}

// ensureLoaded returns the current runtime, loading the PAC source if needed.
// Failed loads are retried at most every pacRetryInterval so a broken PAC
// source is not re-fetched on every request.
func (p *Pac) ensureLoaded() (*pacRuntime, error) {
	if rt := p.runtime.Load(); rt != nil {
		return rt, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if rt := p.runtime.Load(); rt != nil {
		return rt, nil
	}
	if time.Since(p.lastLoadAttempt) < pacRetryInterval && p.lastLoadErr != nil {
		return nil, p.lastLoadErr
	}
	rt, err := p.load()
	p.lastLoadAttempt = time.Now()
	p.lastLoadErr = err
	if err != nil {
		return nil, err
	}
	p.runtime.Store(rt)
	return rt, nil
}

var windows1252High = [...]rune{
	0x20ac, 0x0081, 0x201a, 0x0192, 0x201e, 0x2026, 0x2020, 0x2021,
	0x02c6, 0x2030, 0x0160, 0x2039, 0x0152, 0x008d, 0x017d, 0x008f,
	0x0090, 0x2018, 0x2019, 0x201c, 0x201d, 0x2022, 0x2013, 0x2014,
	0x02dc, 0x2122, 0x0161, 0x203a, 0x0153, 0x009d, 0x017e, 0x0178,
}

var windows1251High = [...]rune{
	0x0402, 0x0403, 0x201a, 0x0453, 0x201e, 0x2026, 0x2020, 0x2021,
	0x20ac, 0x2030, 0x0409, 0x2039, 0x040a, 0x040c, 0x040b, 0x040f,
	0x0452, 0x2018, 0x2019, 0x201c, 0x201d, 0x2022, 0x2013, 0x2014,
	0x0098, 0x2122, 0x0459, 0x203a, 0x045a, 0x045c, 0x045b, 0x045f,
	0x00a0, 0x040e, 0x045e, 0x0408, 0x00a4, 0x0490, 0x00a6, 0x00a7,
	0x0401, 0x00a9, 0x0404, 0x00ab, 0x00ac, 0x00ad, 0x00ae, 0x0407,
	0x00b0, 0x00b1, 0x0406, 0x0456, 0x0491, 0x00b5, 0x00b6, 0x00b7,
	0x0451, 0x2116, 0x0454, 0x00bb, 0x0458, 0x0405, 0x0455, 0x0457,
}

func decodePAC(data []byte, name string) (string, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.ReplaceAll(name, "_", "-")
	if name == "" {
		name = utf8Encoding
	}

	switch name {
	case utf8Encoding, "utf8", "utf-8-sig":
		data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
		if !utf8.Valid(data) {
			return "", errors.New("PAC source is not valid UTF-8")
		}
		return string(data), nil
	case "latin-1", "latin1", "iso-8859-1", "iso8859-1":
		return decodeSingleByte(data, "latin1"), nil
	case "cp1252", "windows-1252", "windows1252":
		return decodeSingleByte(data, "cp1252"), nil
	case "cp1251", "windows-1251", "windows1251":
		return decodeSingleByte(data, "cp1251"), nil
	case "utf-16", "utf16":
		return decodeUTF16(data, nil, true)
	case "utf-16le", "utf16le":
		return decodeUTF16(data, binary.LittleEndian, false)
	case "utf-16be", "utf16be":
		return decodeUTF16(data, binary.BigEndian, false)
	case "auto":
		switch {
		case bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}):
			return string(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})), nil
		case bytes.HasPrefix(data, []byte{0xff, 0xfe}), bytes.HasPrefix(data, []byte{0xfe, 0xff}):
			return decodeUTF16(data, nil, true)
		case utf8.Valid(data):
			return string(data), nil
		default:
			return decodeSingleByte(data, "cp1252"), nil
		}
	default:
		return "", fmt.Errorf("unsupported PAC encoding %q", name)
	}
}

func decodeSingleByte(data []byte, name string) string {
	out := make([]rune, 0, len(data))
	for _, b := range data {
		switch name {
		case "latin1":
			out = append(out, rune(b))
		case "cp1252":
			if b >= 0x80 && b <= 0x9f {
				out = append(out, windows1252High[b-0x80])
			} else {
				out = append(out, rune(b))
			}
		case "cp1251":
			switch {
			case b < 0x80:
				out = append(out, rune(b))
			case b < 0xc0:
				out = append(out, windows1251High[b-0x80])
			default:
				out = append(out, rune(0x0410)+rune(b-0xc0))
			}
		}
	}
	return string(out)
}

func decodeUTF16(data []byte, order binary.ByteOrder, requireBOM bool) (string, error) {
	switch {
	case bytes.HasPrefix(data, []byte{0xff, 0xfe}):
		order = binary.LittleEndian
		data = data[2:]
	case bytes.HasPrefix(data, []byte{0xfe, 0xff}):
		order = binary.BigEndian
		data = data[2:]
	case requireBOM:
		return "", errors.New("PAC UTF-16 source is missing BOM")
	}
	if order == nil {
		return "", errors.New("PAC UTF-16 byte order is unspecified")
	}
	if len(data)%2 != 0 {
		return "", errors.New("PAC UTF-16 source has odd byte length")
	}

	units := make([]uint16, len(data)/2)
	for i := range units {
		units[i] = order.Uint16(data[i*2:])
	}
	runes := make([]rune, 0, len(units))
	for i := 0; i < len(units); i++ {
		u := units[i]
		switch {
		case 0xd800 <= u && u <= 0xdbff:
			if i+1 >= len(units) || units[i+1] < 0xdc00 || units[i+1] > 0xdfff {
				return "", errors.New("PAC UTF-16 source has invalid surrogate pair")
			}
			runes = append(runes, utf16.DecodeRune(rune(u), rune(units[i+1])))
			i++
		case 0xdc00 <= u && u <= 0xdfff:
			return "", errors.New("PAC UTF-16 source has unexpected low surrogate")
		default:
			runes = append(runes, rune(u))
		}
	}
	return string(runes), nil
}

func (p *Pac) load() (*pacRuntime, error) {
	data, err := p.readPACData()
	if err != nil {
		return nil, fmt.Errorf("read PAC: %w", err)
	}
	text, err := decodePAC(data, p.encoding)
	if err != nil {
		return nil, err
	}
	program, err := goja.Compile("pac.js", pacUtils+"\n"+text, false)
	if err != nil {
		return nil, fmt.Errorf("compile PAC: %w", err)
	}
	rt := &pacRuntime{
		program: program,
		owner:   p,
		myIP:    selectMyIPAddress(),
	}
	vm, err := rt.newVM()
	if err != nil {
		return nil, fmt.Errorf("initialize PAC: %w", err)
	}
	rt.vm = vm
	return rt, nil
}

func (rt *pacRuntime) newVM() (*pacVM, error) {
	vm := goja.New()
	_ = vm.Set("dnsResolve", rt.owner.DNSResolve)
	_ = vm.Set("myIpAddress", func() string { return rt.myIP })
	_ = vm.Set("alert", func(string) {})
	if _, err := runPACBounded(vm, func() (goja.Value, error) {
		return vm.RunProgram(rt.program)
	}); err != nil {
		return nil, err
	}
	fn, ok := goja.AssertFunction(vm.Get("FindProxyForURL"))
	if !ok {
		return nil, errors.New("FindProxyForURL is not callable")
	}
	return &pacVM{vm: vm, fn: fn}, nil
}

func runPACBounded(vm *goja.Runtime, run func() (goja.Value, error)) (goja.Value, error) {
	if pacExecTimeout <= 0 {
		return run()
	}
	interrupted := make(chan struct{})
	timer := time.AfterFunc(pacExecTimeout, func() {
		vm.Interrupt(errPACExecutionTimeout)
		close(interrupted)
	})
	defer func() {
		if !timer.Stop() {
			<-interrupted
		}
		vm.ClearInterrupt()
	}()
	return run()
}

func (p *Pac) readPACData() ([]byte, error) {
	loc := strings.ToLower(p.location)
	if strings.HasPrefix(loc, "http://") || strings.HasPrefix(loc, "https://") {
		client := http.Client{
			Transport: pacHTTPTransport,
			Timeout:   pacHTTPTimeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
		resp, err := client.Get(p.location)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			return nil, fmt.Errorf("PAC URL returned %s", resp.Status)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, maxPACBytes+1))
		if err != nil {
			return nil, err
		}
		if len(data) > maxPACBytes {
			return nil, fmt.Errorf("PAC body exceeds %d bytes", maxPACBytes)
		}
		return data, nil
	}
	return os.ReadFile(p.location)
}

// FindProxyForURLWithError evaluates the active PAC generation and surfaces
// load/execution failures so callers can fail explicitly rather than route DIRECT.
func (p *Pac) FindProxyForURLWithError(rawurl, host string) (string, error) {
	rt, err := p.ensureLoaded()
	if err != nil {
		return "", err
	}

	rt.vmMu.Lock()
	defer rt.vmMu.Unlock()

	out, err := runPACBounded(rt.vm.vm, func() (goja.Value, error) {
		return rt.vm.fn(goja.Undefined(), rt.vm.vm.ToValue(rawurl), rt.vm.vm.ToValue(host))
	})
	if err != nil {
		return "", fmt.Errorf("execute PAC: %w", err)
	}
	normalized, err := NormalizeResult(out.String())
	if err != nil {
		return "", fmt.Errorf("normalize PAC result: %w", err)
	}
	return normalized, nil
}

// FindProxyForURL preserves the package's legacy fail-open API. Production
// routing uses FindProxyForURLWithError so PAC failures are not implicit DIRECT.
func (p *Pac) FindProxyForURL(rawurl, host string) string {
	out, err := p.FindProxyForURLWithError(rawurl, host)
	if err != nil {
		return directProxy
	}
	return out
}

func ValidateCanonicalResult(proxies string) error {
	if len(proxies) > maxPACResultBytes {
		return fmt.Errorf("PAC result exceeds %d bytes", maxPACResultBytes)
	}
	count := 0
	for _, raw := range strings.Split(proxies, ",") {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		count++
		if count > maxPACCandidates {
			return fmt.Errorf("PAC result exceeds %d candidates", maxPACCandidates)
		}
	}
	if count == 0 {
		return errors.New("PAC result contains no routing candidates")
	}
	return nil
}

func NormalizeResult(proxies string) (string, error) {
	if len(proxies) > maxPACResultBytes {
		return "", fmt.Errorf("PAC result exceeds %d bytes", maxPACResultBytes)
	}

	tokens := strings.Split(proxies, ";")
	normalized := make([]string, 0, len(tokens))
	for _, raw := range tokens {
		token := strings.TrimSpace(raw)
		if token == "" {
			continue
		}
		if len(normalized) >= maxPACCandidates {
			return "", fmt.Errorf("PAC result exceeds %d candidates", maxPACCandidates)
		}

		fields := strings.Fields(token)
		if len(fields) == 1 && strings.EqualFold(fields[0], directProxy) {
			normalized = append(normalized, directProxy)
			continue
		}
		if len(fields) != 2 {
			return "", fmt.Errorf("malformed PAC directive %q", token)
		}

		kind := strings.ToUpper(fields[0])
		target := fields[1]
		if strings.ContainsAny(target, ",;") {
			return "", fmt.Errorf("malformed PAC target %q", target)
		}

		switch kind {
		case "PROXY", "HTTP":
			normalized = append(normalized, target)
		case "HTTPS":
			normalized = append(normalized, "https://"+target)
		case "SOCKS4":
			normalized = append(normalized, "socks4://"+target)
		case "SOCKS4A":
			normalized = append(normalized, "socks4a://"+target)
		case "SOCKS5":
			normalized = append(normalized, "socks5://"+target)
		case "SOCKS":
			normalized = append(normalized, "socks5://"+target)
		default:
			return "", fmt.Errorf("unsupported PAC directive %q", fields[0])
		}
	}
	if len(normalized) == 0 {
		return "", errors.New("PAC result contains no routing candidates")
	}
	return strings.Join(normalized, ","), nil
}

func (p *Pac) DNSResolve(host string) string {
	ips := dnscache.Lookup(host)
	if len(ips) == 0 {
		return ""
	}
	for _, ip := range ips {
		if ip4 := ip.To4(); ip4 != nil {
			return ip4.String()
		}
	}
	return ips[0].String()
}

func (p *Pac) MyIPAddress() string {
	return selectMyIPAddress()
}

func selectMyIPAddress() string {
	addrs, err := interfaceAddrs()
	if err != nil {
		return localhostIP
	}
	return selectMyIPAddressFrom(addrs)
}

func selectMyIPAddressFrom(addrs []net.Addr) string {
	candidates := make([]net.IP, 0, len(addrs))
	seen := map[string]bool{}
	for _, addr := range addrs {
		var ip net.IP
		switch value := addr.(type) {
		case *net.IPNet:
			ip = value.IP
		case *net.IPAddr:
			ip = value.IP
		default:
			continue
		}
		ip4 := ip.To4()
		if ip4 == nil || ip4.IsLoopback() || ip4.IsUnspecified() ||
			ip4.IsMulticast() || ip4.IsLinkLocalUnicast() || ip4.IsLinkLocalMulticast() {
			continue
		}
		key := string(ip4)
		if seen[key] {
			continue
		}
		seen[key] = true
		candidates = append(candidates, append(net.IP(nil), ip4...))
	}
	if len(candidates) == 0 {
		return localhostIP
	}

	rank := func(ip net.IP) int {
		if ip.IsPrivate() {
			return 0
		}
		return 1
	}
	sort.Slice(candidates, func(i, j int) bool {
		ri, rj := rank(candidates[i]), rank(candidates[j])
		if ri != rj {
			return ri < rj
		}
		return bytes.Compare(candidates[i], candidates[j]) < 0
	})
	return candidates[0].String()
}

func (p *Pac) String() string {
	return fmt.Sprintf("Pac(%s)", p.location)
}

const pacUtils = `
function dnsDomainIs(host, domain) { return host.length >= domain.length && host.substring(host.length - domain.length) == domain; }
function dnsDomainLevels(host) { return host.split(".").length - 1; }
function isValidIpAddress(ipchars) {
  var matches = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(ipchars);
  if (matches == null) return false;
  return !(matches[1] > 255 || matches[2] > 255 || matches[3] > 255 || matches[4] > 255);
}
function convert_addr(ipchars) {
  var bytes = ipchars.split(".");
  return ((bytes[0] & 0xff) << 24) | ((bytes[1] & 0xff) << 16) | ((bytes[2] & 0xff) << 8) | (bytes[3] & 0xff);
}
function isInNet(ipaddr, pattern, maskstr) {
  if (!isValidIpAddress(pattern) || !isValidIpAddress(maskstr)) return false;
  if (!isValidIpAddress(ipaddr)) {
    ipaddr = dnsResolve(ipaddr);
    if (ipaddr == null || ipaddr == "") return false;
  }
  var host = convert_addr(ipaddr);
  var pat = convert_addr(pattern);
  var mask = convert_addr(maskstr);
  return (host & mask) == (pat & mask);
}
function shExpMatch(str, shexp) {
  var re = shexp.replace(/[.+^${}()|[\]\\]/g, '\\$&').replace(/\*/g, '.*').replace(/\?/g, '.');
  return new RegExp('^' + re + '$').test(str);
}
function isPlainHostName(host) { return host.search("(\\.)|:") == -1; }
function isResolvable(host) {
  var ip = dnsResolve(host);
  return ip != null && ip != "";
}
function localHostOrDomainIs(host, hostdom) { return host == hostdom || hostdom.indexOf(host + '.') == 0; }
var wdays = { SUN: 0, MON: 1, TUE: 2, WED: 3, THU: 4, FRI: 5, SAT: 6 };
var months = { JAN: 0, FEB: 1, MAR: 2, APR: 3, MAY: 4, JUN: 5, JUL: 6, AUG: 7, SEP: 8, OCT: 9, NOV: 10, DEC: 11 };
function weekdayRange() {
  function getDay(weekday) { return weekday in wdays ? wdays[weekday] : -1; }
  var date = new Date();
  var argc = arguments.length;
  if (argc < 1) return false;
  var wday;
  if (arguments[argc - 1] == "GMT") { argc--; wday = date.getUTCDay(); } else { wday = date.getDay(); }
  var wd1 = getDay(arguments[0]);
  var wd2 = argc == 2 ? getDay(arguments[1]) : wd1;
  if (wd1 == -1 || wd2 == -1) return false;
  if (wd1 <= wd2) return wd1 <= wday && wday <= wd2;
  return wd2 >= wday || wday >= wd1;
}
function dateRange() {
  function getMonth(name) { return name in months ? months[name] : -1; }
  var date = new Date();
  var argc = arguments.length;
  if (argc < 1) return false;
  var isGMT = arguments[argc - 1] == "GMT";
  if (isGMT) argc--;
  if (argc == 1) {
    var tmp = parseInt(arguments[0]);
    if (isNaN(tmp)) return (isGMT ? date.getUTCMonth() : date.getMonth()) == getMonth(arguments[0]);
    if (tmp < 32) return (isGMT ? date.getUTCDate() : date.getDate()) == tmp;
    return (isGMT ? date.getUTCFullYear() : date.getFullYear()) == tmp;
  }
  var year = date.getFullYear();
  var date1 = new Date(year, 0, 1, 0, 0, 0);
  var date2 = new Date(year, 11, 31, 23, 59, 59);
  var adjustMonth = false;
  for (var i = 0; i < argc >> 1; i++) {
    var left = parseInt(arguments[i]);
    if (isNaN(left)) date1.setMonth(getMonth(arguments[i]));
    else if (left < 32) { adjustMonth = argc <= 2; date1.setDate(left); }
    else date1.setFullYear(left);
  }
  for (var j = argc >> 1; j < argc; j++) {
    var right = parseInt(arguments[j]);
    if (isNaN(right)) date2.setMonth(getMonth(arguments[j]));
    else if (right < 32) date2.setDate(right);
    else date2.setFullYear(right);
  }
  if (adjustMonth) { date1.setMonth(date.getMonth()); date2.setMonth(date.getMonth()); }
  if (isGMT) {
    var tmpDate = date;
    tmpDate.setFullYear(date.getUTCFullYear());
    tmpDate.setMonth(date.getUTCMonth());
    tmpDate.setDate(date.getUTCDate());
    tmpDate.setHours(date.getUTCHours());
    tmpDate.setMinutes(date.getUTCMinutes());
    tmpDate.setSeconds(date.getUTCSeconds());
    date = tmpDate;
  }
  return date1 <= date2 ? date1 <= date && date <= date2 : date2 >= date || date >= date1;
}
function timeRange() {
  var argc = arguments.length;
  var date = new Date();
  var isGMT = false;
  if (argc < 1) return false;
  if (arguments[argc - 1] == "GMT") { isGMT = true; argc--; }

  var hour = isGMT ? date.getUTCHours() : date.getHours();
  var minute = isGMT ? date.getUTCMinutes() : date.getMinutes();
  var second = isGMT ? date.getUTCSeconds() : date.getSeconds();
  var millisecond = isGMT ? date.getUTCMilliseconds() : date.getMilliseconds();

  if (argc == 1) return hour == arguments[0];

  function inWrappedRange(value, start, end) {
    return start <= end ? start <= value && value <= end : value >= start || value <= end;
  }

  if (argc == 2) return inWrappedRange(hour, arguments[0], arguments[1]);

  var now = (((hour * 60) + minute) * 60 + second) * 1000 + millisecond;
  var start, end;
  if (argc == 4) {
    start = ((arguments[0] * 60) + arguments[1]) * 60 * 1000;
    end = ((((arguments[2] * 60) + arguments[3]) * 60) + 59) * 1000 + 999;
  } else if (argc == 6) {
    start = ((((arguments[0] * 60) + arguments[1]) * 60) + arguments[2]) * 1000;
    end = ((((arguments[3] * 60) + arguments[4]) * 60) + arguments[5]) * 1000 + 999;
  } else {
    throw new Error("timeRange: bad number of arguments");
  }
  return inWrappedRange(now, start, end);
}
`
