//go:build windows

package systemproxy

import (
	"fmt"
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

	winhttpAccessTypeNoProxy        = 1
	winhttpAccessTypeNamedProxy     = 3
	winhttpAccessTypeAutomaticProxy = 4

	winhttpTimeoutMS = 15_000
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

type winHTTPProxyInfo struct {
	AccessType  uint32
	Proxy       *uint16
	ProxyBypass *uint16
}

var (
	winhttpDLL = windows.NewLazySystemDLL("winhttp.dll")
	kernelDLL  = windows.NewLazySystemDLL("kernel32.dll")

	procWinHttpGetIEProxyConfigForCurrentUser = winhttpDLL.NewProc("WinHttpGetIEProxyConfigForCurrentUser")
	procWinHttpOpen                           = winhttpDLL.NewProc("WinHttpOpen")
	procWinHttpSetTimeouts                    = winhttpDLL.NewProc("WinHttpSetTimeouts")
	procWinHttpGetProxyForURL                 = winhttpDLL.NewProc("WinHttpGetProxyForUrl")
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
		0,
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
	return newResolverWithBackend(&winHTTPBackend{session: session}), nil
}

func Discover() Config {
	if cfg, ok := discoverWinHTTPIEProxyConfig(); ok {
		return cfg
	}
	return discoverRegistryProxyConfig()
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

func (b *winHTTPBackend) resolve(rawurl string, cfg Config) (string, error) {
	urlp, err := windows.UTF16PtrFromString(rawurl)
	if err != nil {
		return "", err
	}

	options := winHTTPAutoProxyOptions{AutoLogonIfChallenged: 1}
	if cfg.AutoDetect {
		options.Flags |= winhttpAutoproxyAutoDetect
		options.AutoDetectFlags = winhttpAutoDetectTypeDHCP | winhttpAutoDetectTypeDNSA
	}
	if cfg.IsPAC {
		pacURL, err := windows.UTF16PtrFromString(cfg.PACURL)
		if err != nil {
			return "", err
		}
		options.Flags |= winhttpAutoproxyConfigURL
		options.AutoConfigURL = pacURL
	}

	var proxyInfo winHTTPProxyInfo
	ok, _, callErr := procWinHttpGetProxyForURL.Call(
		b.session,
		uintptr(unsafe.Pointer(urlp)),
		uintptr(unsafe.Pointer(&options)),
		uintptr(unsafe.Pointer(&proxyInfo)),
	)
	if ok == 0 {
		return "", fmt.Errorf("WinHttpGetProxyForUrl(%q): %w", rawurl, callErr)
	}
	defer globalFreeUTF16(proxyInfo.Proxy)
	defer globalFreeUTF16(proxyInfo.ProxyBypass)

	switch proxyInfo.AccessType {
	case winhttpAccessTypeNamedProxy:
		proxy := windows.UTF16PtrToString(proxyInfo.Proxy)
		if proxy == "" {
			return "", fmt.Errorf("WinHttpGetProxyForUrl returned named proxy without a proxy name")
		}
		return ParseManualProxyString(proxy), nil
	case winhttpAccessTypeNoProxy:
		return "DIRECT", nil
	default:
		return "", fmt.Errorf("WinHttpGetProxyForUrl returned unsupported access type %d", proxyInfo.AccessType)
	}
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
