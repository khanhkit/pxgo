//go:build windows

package proxy

import (
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	creduiMaxUsernameLength = 513
	creduiMaxPasswordLength = 256

	creduiFlagsDoNotPersist        = 0x00002
	creduiFlagsExcludeCertificates = 0x00008
	creduiFlagsShowSaveCheckBox    = 0x00040
	creduiFlagsAlwaysShowUI        = 0x00080
	creduiFlagsGenericCredentials  = 0x40000
)

type credUIInfo struct {
	cbSize      uint32
	hwndParent  uintptr
	messageText *uint16
	captionText *uint16
	banner      uintptr
}

var credUIPromptForCredentials = windows.NewLazySystemDLL("credui.dll").NewProc("CredUIPromptForCredentialsW")

func promptWindowsUpstreamCredentials(proxyHost string) (promptedUpstreamCredential, error) {
	if err := credUIPromptForCredentials.Find(); err != nil {
		return promptedUpstreamCredential{}, fmt.Errorf("load Windows credential dialog: %w", err)
	}
	target, err := windows.UTF16PtrFromString("PxGo:" + proxyHost)
	if err != nil {
		return promptedUpstreamCredential{}, err
	}
	message, err := windows.UTF16PtrFromString("Windows single sign-on was rejected by the upstream proxy. Enter your domain or proxy credentials to retry.")
	if err != nil {
		return promptedUpstreamCredential{}, err
	}
	caption, err := windows.UTF16PtrFromString("PxGo - Proxy authentication")
	if err != nil {
		return promptedUpstreamCredential{}, err
	}

	info := credUIInfo{
		cbSize:      uint32(unsafe.Sizeof(credUIInfo{})),
		messageText: message,
		captionText: caption,
	}
	username := make([]uint16, creduiMaxUsernameLength+1)
	password := make([]uint16, creduiMaxPasswordLength+1)
	defer func() {
		for i := range username {
			username[i] = 0
		}
		for i := range password {
			password[i] = 0
		}
		runtime.KeepAlive(username)
		runtime.KeepAlive(password)
	}()
	var remember int32
	flags := uintptr(creduiFlagsGenericCredentials |
		creduiFlagsAlwaysShowUI |
		creduiFlagsDoNotPersist |
		creduiFlagsShowSaveCheckBox |
		creduiFlagsExcludeCertificates)

	result, _, _ := credUIPromptForCredentials.Call(
		uintptr(unsafe.Pointer(&info)),
		uintptr(unsafe.Pointer(target)),
		0,
		0,
		uintptr(unsafe.Pointer(&username[0])),
		uintptr(len(username)),
		uintptr(unsafe.Pointer(&password[0])),
		uintptr(len(password)),
		uintptr(unsafe.Pointer(&remember)),
		flags,
	)
	runtime.KeepAlive(info)
	runtime.KeepAlive(target)
	runtime.KeepAlive(message)
	runtime.KeepAlive(caption)
	runtime.KeepAlive(username)
	if result == 1223 {
		return promptedUpstreamCredential{}, errUpstreamCredentialPromptCancelled
	}
	if result != 0 {
		return promptedUpstreamCredential{}, fmt.Errorf("Windows credential dialog failed (error %d)", result)
	}
	credentials := promptedUpstreamCredential{
		Username: windows.UTF16ToString(username),
		Password: windows.UTF16ToString(password),
		Remember: remember != 0,
	}
	if credentials.Username == "" || credentials.Password == "" {
		return promptedUpstreamCredential{}, fmt.Errorf("Windows credential dialog returned empty credentials")
	}
	return credentials, nil
}
