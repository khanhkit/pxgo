package pac

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dop251/goja"

	"github.com/pavelsimo/pxgo/internal/dnscache"
)

const (
	directProxy = "DIRECT"
	localhostIP = "127.0.0.1"
	maxPACBytes = 4 << 20
)

var errPACExecutionTimeout = errors.New("PAC JavaScript execution timeout")

// Overridable in tests.
var (
	pacHTTPTimeout   = 10 * time.Second
	pacExecTimeout   = 2 * time.Second
	pacRetryInterval = 30 * time.Second
)

var pacHTTPTransport = func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	return t
}()

type Pac struct {
	location string
	encoding string

	// mu guards (re)loading only; evaluation runs lock-free against the
	// current runtime so concurrent requests do not serialize on the VM.
	mu              sync.Mutex
	lastLoadAttempt time.Time
	lastLoadErr     error
	runtime         atomic.Pointer[pacRuntime]
}

// pacRuntime is one compiled PAC script plus a pool of VMs that have run it.
// A reload swaps in a whole new pacRuntime, so stale pooled VMs are dropped
// together with the old one.
type pacRuntime struct {
	program *goja.Program
	pool    sync.Pool // of *pacVM
	owner   *Pac
}

type pacVM struct {
	vm *goja.Runtime
	fn goja.Callable
}

func New(location, encoding string) *Pac {
	if encoding == "" {
		encoding = "utf-8"
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

func (p *Pac) load() (*pacRuntime, error) {
	data, err := p.readPACData()
	if err != nil {
		return nil, fmt.Errorf("read PAC: %w", err)
	}
	if p.encoding != "utf-8" && p.encoding != "latin-1" {
		return nil, fmt.Errorf("unsupported PAC encoding %q", p.encoding)
	}
	text := string(data)
	if p.encoding == "latin-1" {
		runes := make([]rune, len(data))
		for i, b := range data {
			runes[i] = rune(b)
		}
		text = string(runes)
	}
	program, err := goja.Compile("pac.js", pacUtils+"\n"+text, false)
	if err != nil {
		return nil, fmt.Errorf("compile PAC: %w", err)
	}
	rt := &pacRuntime{program: program, owner: p}
	vm, err := rt.newVM()
	if err != nil {
		return nil, fmt.Errorf("initialize PAC: %w", err)
	}
	rt.pool.Put(vm)
	return rt, nil
}

func (rt *pacRuntime) newVM() (*pacVM, error) {
	vm := goja.New()
	_ = vm.Set("dnsResolve", rt.owner.DNSResolve)
	_ = vm.Set("myIpAddress", rt.owner.MyIPAddress)
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
	v, _ := rt.pool.Get().(*pacVM)
	if v == nil {
		v, err = rt.newVM()
		if err != nil {
			return "", fmt.Errorf("create PAC VM: %w", err)
		}
	}
	reusable := false
	defer func() {
		if reusable {
			rt.pool.Put(v)
		}
	}()
	out, err := runPACBounded(v.vm, func() (goja.Value, error) {
		return v.fn(goja.Undefined(), v.vm.ToValue(rawurl), v.vm.ToValue(host))
	})
	if err != nil {
		return "", fmt.Errorf("execute PAC: %w", err)
	}
	reusable = true
	return normalizePACResult(out.String()), nil
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

var pacResultReplacer = strings.NewReplacer(
	"PROXY ", "",
	"HTTP ", "",
	"HTTPS ", "https://",
	"SOCKS4 ", "socks4://",
	"SOCKS5 ", "socks5://",
	"SOCKS ", "socks5://",
	";", ",",
)

func normalizePACResult(proxies string) string {
	return pacResultReplacer.Replace(proxies)
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
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return localhostIP
	}
	for _, addr := range addrs {
		ipnet, ok := addr.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() {
			continue
		}
		if ip := ipnet.IP.To4(); ip != nil {
			return ip.String()
		}
	}
	return localhostIP
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
  var date1 = new Date();
  var date2 = new Date();
  if (argc == 1) return hour == arguments[0];
  if (argc == 2) return arguments[0] <= hour && hour <= arguments[1];
  switch (argc) {
    case 6:
      date1.setSeconds(arguments[2]);
      date2.setSeconds(arguments[5]);
    case 4:
      var middle = argc >> 1;
      date1.setHours(arguments[0]);
      date1.setMinutes(arguments[1]);
      date2.setHours(arguments[middle]);
      date2.setMinutes(arguments[middle + 1]);
      if (middle == 2) date2.setSeconds(59);
      break;
    default:
      throw new Error("timeRange: bad number of arguments");
  }
  if (isGMT) {
    date.setFullYear(date.getUTCFullYear());
    date.setMonth(date.getUTCMonth());
    date.setDate(date.getUTCDate());
    date.setHours(date.getUTCHours());
    date.setMinutes(date.getUTCMinutes());
    date.setSeconds(date.getUTCSeconds());
  }
  return date1 <= date2 ? date1 <= date && date <= date2 : date2 >= date || date >= date1;
}
`
