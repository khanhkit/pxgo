//go:build windows

package systemproxy

import (
	"context"
	"fmt"
	"net"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const internetSettingsPath = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`

const (
	winhttpAutoproxyAutoDetect = 0x00000001
	winhttpAutoproxyConfigURL  = 0x00000002

	winhttpAutoDetectTypeDHCP = 0x00000001
	winhttpAutoDetectTypeDNSA = 0x00000002

	winhttpAccessTypeAutomaticProxy = 4
	winhttpFlagAsync                = 0x10000000
	winhttpTimeoutMS                = 15_000

	winhttpCallbackStatusRequestError           = 0x00200000
	winhttpCallbackStatusGetProxyForURLComplete = 0x01000000
	winhttpCallbackFlagsProxyResolver           = winhttpCallbackStatusRequestError | winhttpCallbackStatusGetProxyForURLComplete

	errorIOPending = 997

	internetSchemeHTTP  = 1
	internetSchemeHTTPS = 2
	internetSchemeSOCKS = 4
)

type winHTTPCurrentUserIEProxyConfig struct {
	AutoDetect    int32
	AutoConfigURL *uint16
	Proxy         *uint16
	ProxyBypass   *uint16
}

type winHTTPAutoProxyOptions struct {
	Flags                 uint32
	AutoDetectFlags       uint32
	AutoConfigURL         *uint16
	Reserved              uintptr
	Reserved2             uint32
	AutoLogonIfChallenged int32
}

type winHTTPProxyResultEntry struct {
	Proxy       int32
	Bypass      int32
	ProxyScheme int32
	ProxyName   *uint16
	ProxyPort   uint16
}

type winHTTPProxyResult struct {
	EntriesCount uint32
	Entries      *winHTTPProxyResultEntry
}

type winHTTPAsyncResult struct {
	Result uintptr
	Error  uint32
}

type winHTTPAsyncEvent struct {
	err error
}

var (
	winHTTPCallbackRegistry sync.Map
	winHTTPCallbackID       atomic.Uint64
	winHTTPStatusCallback   = windows.NewCallback(winHTTPProxyStatusCallback)

	winhttpDLL = windows.NewLazySystemDLL("winhttp.dll")
	kernelDLL  = windows.NewLazySystemDLL("kernel32.dll")

	procWinHttpGetIEProxyConfigForCurrentUser = winhttpDLL.NewProc("WinHttpGetIEProxyConfigForCurrentUser")
	procWinHttpOpen                           = winhttpDLL.NewProc("WinHttpOpen")
	procWinHttpSetTimeouts                    = winhttpDLL.NewProc("WinHttpSetTimeouts")
	procWinHttpSetStatusCallback              = winhttpDLL.NewProc("WinHttpSetStatusCallback")
	procWinHttpCreateProxyResolver            = winhttpDLL.NewProc("WinHttpCreateProxyResolver")
	procWinHttpGetProxyForURLEx               = winhttpDLL.NewProc("WinHttpGetProxyForUrlEx")
	procWinHttpGetProxyResult                 = winhttpDLL.NewProc("WinHttpGetProxyResult")
	procWinHttpFreeProxyResult                = winhttpDLL.NewProc("WinHttpFreeProxyResult")
	procWinHttpCloseHandle                    = winhttpDLL.NewProc("WinHttpCloseHandle")
	procGlobalFree                            = kernelDLL.NewProc("GlobalFree")
)

type winHTTPBackend struct {
	session uintptr
}

func NewResolver() (*Resolver, error) {
	agent, err := windows.UTF16PtrFromString("PxGo")
	if err != nil {
		return nil, err
	}
	session, _, callErr := procWinHttpOpen.Call(
		uintptr(unsafe.Pointer(agent)),
		winhttpAccessTypeAutomaticProxy,
		0,
		0,
		winhttpFlagAsync,
	)
	if session == 0 {
		return nil, fmt.Errorf("WinHttpOpen: %w", callErr)
	}

	ok, _, timeoutErr := procWinHttpSetTimeouts.Call(
		session,
		winhttpTimeoutMS,
		winhttpTimeoutMS,
		winhttpTimeoutMS,
		winhttpTimeoutMS,
	)
	if ok == 0 {
		procWinHttpCloseHandle.Call(session)
		return nil, fmt.Errorf("WinHttpSetTimeouts: %w", timeoutErr)
	}

	callback, _, callbackErr := procWinHttpSetStatusCallback.Call(
		session,
		winHTTPStatusCallback,
		winhttpCallbackFlagsProxyResolver,
		0,
	)
	if callback == ^uintptr(0) {
		procWinHttpCloseHandle.Call(session)
		return nil, fmt.Errorf("WinHttpSetStatusCallback: %w", callbackErr)
	}
	return newResolverWithBackend(&winHTTPBackend{session: session}), nil
}

func Discover() Config {
	if cfg, ok := discoverWinHTTPIEProxyConfig(); ok {
		cfg.Supported = true
		return cfg
	}
	cfg := discoverRegistryProxyConfig()
	cfg.Supported = true
	return cfg
}

func discoverWinHTTPIEProxyConfig() (Config, bool) {
	var ieConfig winHTTPCurrentUserIEProxyConfig
	ok, _, _ := procWinHttpGetIEProxyConfigForCurrentUser.Call(uintptr(unsafe.Pointer(&ieConfig)))
	if ok == 0 {
		return Config{}, false
	}
	defer globalFreeUTF16(ieConfig.AutoConfigURL)
	defer globalFreeUTF16(ieConfig.Proxy)
	defer globalFreeUTF16(ieConfig.ProxyBypass)

	return configFromDiscoveredSources(
		ieConfig.AutoDetect != 0,
		windows.UTF16PtrToString(ieConfig.AutoConfigURL),
		windows.UTF16PtrToString(ieConfig.Proxy),
		windows.UTF16PtrToString(ieConfig.ProxyBypass),
	), true
}

func discoverRegistryProxyConfig() Config {
	key, err := registry.OpenKey(registry.CURRENT_USER, internetSettingsPath, registry.QUERY_VALUE)
	if err != nil {
		return Config{}
	}
	defer key.Close()
	pacURL, _, _ := key.GetStringValue("AutoConfigURL")
	bypass, _, _ := key.GetStringValue("ProxyOverride")

	var proxyServer string
	if enabled, _, err := key.GetIntegerValue("ProxyEnable"); err == nil && enabled != 0 {
		proxyServer, _, _ = key.GetStringValue("ProxyServer")
	}

	return configFromDiscoveredSources(false, pacURL, proxyServer, bypass)
}

func ResolveProxyForURL(rawurl string, cfg Config) (string, error) {
	if !cfg.AutoDetect && !cfg.IsPAC {
		return "", nil
	}
	resolver, err := NewResolver()
	if err != nil {
		return "", err
	}

	result, resolveErr := resolver.ResolveProxyForURL(rawurl, cfg)
	closeErr := resolver.Close()
	if resolveErr != nil {
		return "", resolveErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	return result, nil
}

func winHTTPProxyStatusCallback(_ uintptr, contextValue uintptr, status uint32, statusInfo uintptr, statusInfoLen uint32) uintptr {
	value, ok := winHTTPCallbackRegistry.Load(contextValue)
	if !ok {
		return 0
	}
	completion := value.(chan winHTTPAsyncEvent)

	var event winHTTPAsyncEvent
	switch status {
	case winhttpCallbackStatusGetProxyForURLComplete:
		// Successful completion; the result is retrieved from the resolver handle.
	case winhttpCallbackStatusRequestError:
		if statusInfo == 0 || statusInfoLen < uint32(unsafe.Sizeof(winHTTPAsyncResult{})) {
			event.err = fmt.Errorf("WinHTTP async proxy resolution failed without error details")
		} else {
			asyncResult := (*winHTTPAsyncResult)(unsafe.Pointer(statusInfo))
			event.err = windows.Errno(asyncResult.Error)
		}
	default:
		return 0
	}

	select {
	case completion <- event:
	default:
	}
	return 0
}

func nextWinHTTPCallbackID() uintptr {
	for {
		id := uintptr(winHTTPCallbackID.Add(1))
		if id != 0 {
			return id
		}
	}
}

func (b *winHTTPBackend) resolve(ctx context.Context, rawurl string, cfg Config) (string, error) {
	var resolverHandle uintptr
	status, _, _ := procWinHttpCreateProxyResolver.Call(
		b.session,
		uintptr(unsafe.Pointer(&resolverHandle)),
	)
	if status != 0 {
		return "", fmt.Errorf("WinHttpCreateProxyResolver: %w", windows.Errno(status))
	}
	closed := false
	closeResolver := func() {
		if !closed && resolverHandle != 0 {
			procWinHttpCloseHandle.Call(resolverHandle)
			closed = true
		}
	}
	defer closeResolver()

	urlp, err := windows.UTF16PtrFromString(rawurl)
	if err != nil {
		return "", err
	}
	options := &winHTTPAutoProxyOptions{AutoLogonIfChallenged: 1}
	var pacURL *uint16
	if cfg.AutoDetect {
		options.Flags |= winhttpAutoproxyAutoDetect
		options.AutoDetectFlags = winhttpAutoDetectTypeDHCP | winhttpAutoDetectTypeDNSA
	}
	if cfg.IsPAC {
		pacURL, err = windows.UTF16PtrFromString(cfg.PACURL)
		if err != nil {
			return "", err
		}
		options.Flags |= winhttpAutoproxyConfigURL
		options.AutoConfigURL = pacURL
	}
	defer runtime.KeepAlive(urlp)
	defer runtime.KeepAlive(options)
	defer runtime.KeepAlive(pacURL)

	contextID := nextWinHTTPCallbackID()
	completion := make(chan winHTTPAsyncEvent, 1)
	winHTTPCallbackRegistry.Store(contextID, completion)
	defer winHTTPCallbackRegistry.Delete(contextID)

	status, _, _ = procWinHttpGetProxyForURLEx.Call(
		resolverHandle,
		uintptr(unsafe.Pointer(urlp)),
		uintptr(unsafe.Pointer(options)),
		contextID,
	)
	if status != errorIOPending {
		return "", fmt.Errorf("WinHttpGetProxyForUrlEx(%q): %w", rawurl, windows.Errno(status))
	}

	select {
	case event := <-completion:
		if event.err != nil {
			return "", fmt.Errorf("WinHttpGetProxyForUrlEx(%q): %w", rawurl, event.err)
		}
	case <-ctx.Done():
		closeResolver()
		return "", ctx.Err()
	}

	var result winHTTPProxyResult
	status, _, _ = procWinHttpGetProxyResult.Call(
		resolverHandle,
		uintptr(unsafe.Pointer(&result)),
	)
	if status != 0 {
		return "", fmt.Errorf("WinHttpGetProxyResult(%q): %w", rawurl, windows.Errno(status))
	}
	defer procWinHttpFreeProxyResult.Call(uintptr(unsafe.Pointer(&result)))
	return formatWinHTTPProxyResult(&result)
}

func formatWinHTTPProxyResult(result *winHTTPProxyResult) (string, error) {
	if result == nil || result.EntriesCount == 0 || result.Entries == nil {
		return "", fmt.Errorf("WinHttpGetProxyResult returned no entries")
	}
	entries := unsafe.Slice(result.Entries, int(result.EntriesCount))
	proxies := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Proxy == 0 {
			proxies = append(proxies, "DIRECT")
			continue
		}
		host := windows.UTF16PtrToString(entry.ProxyName)
		if host == "" {
			return "", fmt.Errorf("WinHttpGetProxyResult returned proxy without a host")
		}
		endpoint := host
		if entry.ProxyPort != 0 {
			endpoint = net.JoinHostPort(host, strconv.Itoa(int(entry.ProxyPort)))
		}
		switch entry.ProxyScheme {
		case internetSchemeHTTP:
		case internetSchemeHTTPS:
			endpoint = "https://" + endpoint
		case internetSchemeSOCKS:
			endpoint = "socks5://" + endpoint
		default:
			return "", fmt.Errorf("WinHttpGetProxyResult returned unsupported proxy scheme %d", entry.ProxyScheme)
		}
		proxies = append(proxies, endpoint)
	}
	if len(proxies) == 0 {
		return "", fmt.Errorf("WinHttpGetProxyResult returned no usable entries")
	}
	return strings.Join(proxies, ","), nil
}

func (b *winHTTPBackend) close() error {
	if b.session == 0 {
		return nil
	}
	session := b.session
	b.session = 0
	ok, _, callErr := procWinHttpCloseHandle.Call(session)
	if ok == 0 {
		return fmt.Errorf("WinHttpCloseHandle: %w", callErr)
	}
	return nil
}

func globalFreeUTF16(ptr *uint16) {
	if ptr != nil {
		procGlobalFree.Call(uintptr(unsafe.Pointer(ptr)))
	}
}
