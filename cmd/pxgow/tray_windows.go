//go:build windows

package main

import (
	"os/exec"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	wmDestroy       = 0x0002
	wmClose         = 0x0010
	wmCommand       = 0x0111
	wmApp           = 0x8000
	wmTray          = wmApp + 1
	wmRButtonUp     = 0x0205
	wmLButtonDblClk = 0x0203
	mfString        = 0x0000
	tpmRightButton  = 0x0002
	nimAdd          = 0x00000000
	nimDelete       = 0x00000002
	nifMessage      = 0x00000001
	nifIcon         = 0x00000002
	nifTip          = 0x00000004
	idiApplication  = 32512
	quitMenuID      = 1001
)

type (
	point struct{ x, y int32 }
	msg   struct {
		hwnd    uintptr
		message uint32
		wParam  uintptr
		lParam  uintptr
		time    uint32
		pt      point
	}
)

type wndClass struct {
	style      uint32
	wndProc    uintptr
	clsExtra   int32
	wndExtra   int32
	instance   windows.Handle
	icon       windows.Handle
	cursor     windows.Handle
	background windows.Handle
	menuName   *uint16
	className  *uint16
}
type notifyIconData struct {
	cbSize            uint32
	hWnd              uintptr
	uID               uint32
	uFlags            uint32
	uCallbackMessage  uint32
	hIcon             windows.Handle
	szTip             [128]uint16
	dwState           uint32
	dwStateMask       uint32
	szInfo            [256]uint16
	uTimeoutOrVersion uint32
	szInfoTitle       [64]uint16
	dwInfoFlags       uint32
	guidItem          [16]byte
	hBalloonIcon      windows.Handle
}

var (
	user32                  = windows.NewLazySystemDLL("user32.dll")
	shell32                 = windows.NewLazySystemDLL("shell32.dll")
	kernel32                = windows.NewLazySystemDLL("kernel32.dll")
	procRegisterClass       = user32.NewProc("RegisterClassW")
	procCreateWindowEx      = user32.NewProc("CreateWindowExW")
	procDefWindowProc       = user32.NewProc("DefWindowProcW")
	procGetMessage          = user32.NewProc("GetMessageW")
	procTranslateMessage    = user32.NewProc("TranslateMessage")
	procDispatchMessage     = user32.NewProc("DispatchMessageW")
	procPostQuitMessage     = user32.NewProc("PostQuitMessage")
	procDestroyWindow       = user32.NewProc("DestroyWindow")
	procPostMessage         = user32.NewProc("PostMessageW")
	procLoadIcon            = user32.NewProc("LoadIconW")
	procCreatePopupMenu     = user32.NewProc("CreatePopupMenu")
	procAppendMenu          = user32.NewProc("AppendMenuW")
	procTrackPopupMenu      = user32.NewProc("TrackPopupMenu")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	procGetCursorPos        = user32.NewProc("GetCursorPos")
	procDestroyMenu         = user32.NewProc("DestroyMenu")
	procShellNotifyIcon     = shell32.NewProc("Shell_NotifyIconW")
	procGetModuleHandle     = kernel32.NewProc("GetModuleHandleW")
	trayQuitCommand         func()
)

func runTray(child *exec.Cmd, quit func()) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	trayQuitCommand = quit
	defer func() { trayQuitCommand = nil }()

	className, _ := windows.UTF16PtrFromString("PxGoTrayWindow")
	instance, _, _ := procGetModuleHandle.Call(0)
	wc := wndClass{wndProc: windows.NewCallback(trayWindowProc), instance: windows.Handle(instance), className: className}
	if atom, _, callErr := procRegisterClass.Call(uintptr(unsafe.Pointer(&wc))); atom == 0 {
		return callErr
	}
	hwnd, _, callErr := procCreateWindowEx.Call(0, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(className)), 0, 0, 0, 0, 0, 0, 0, instance, 0)
	if hwnd == 0 {
		return callErr
	}
	defer procDestroyWindow.Call(hwnd)

	icon, _, _ := procLoadIcon.Call(instance, 1)
	if icon == 0 {
		icon, _, _ = procLoadIcon.Call(0, idiApplication)
	}
	nid := notifyIconData{cbSize: uint32(unsafe.Sizeof(notifyIconData{})), hWnd: hwnd, uID: 1, uFlags: nifMessage | nifIcon | nifTip, uCallbackMessage: wmTray, hIcon: windows.Handle(icon)}
	copy(nid.szTip[:], windows.StringToUTF16("PxGo - running in background"))
	if ok, _, callErr := procShellNotifyIcon.Call(nimAdd, uintptr(unsafe.Pointer(&nid))); ok == 0 {
		return callErr
	}
	defer procShellNotifyIcon.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))

	go func() {
		_ = child.Wait()
		procPostMessage.Call(hwnd, wmClose, 0, 0)
	}()

	var m msg
	for {
		r, _, err := procGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) == -1 {
			return err
		}
		if r == 0 {
			return nil
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func trayWindowProc(hwnd uintptr, message uint32, wParam, lParam uintptr) uintptr {
	switch message {
	case wmCommand:
		if uint16(wParam&0xffff) == quitMenuID && trayQuitCommand != nil {
			go trayQuitCommand()
		}
		return 0
	case wmTray:
		if uint32(lParam) == wmRButtonUp || uint32(lParam) == wmLButtonDblClk {
			showTrayMenu(hwnd)
		}
		return 0
	case wmClose:
		procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProc.Call(hwnd, uintptr(message), wParam, lParam)
	return r
}

func showTrayMenu(hwnd uintptr) {
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)
	label, _ := windows.UTF16PtrFromString("Quit PxGo")
	procAppendMenu.Call(menu, mfString, quitMenuID, uintptr(unsafe.Pointer(label)))
	var p point
	if ok, _, _ := procGetCursorPos.Call(uintptr(unsafe.Pointer(&p))); ok == 0 {
		return
	}
	procSetForegroundWindow.Call(hwnd)
	procTrackPopupMenu.Call(menu, tpmRightButton, uintptr(p.x), uintptr(p.y), 0, hwnd, 0)
}

func quitBackground(pxgo string, args []string) error {
	quitArgs := controlArgs(args, "--quit")
	cmd := exec.Command(pxgo, quitArgs...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	if err := configureWindowlessChild(cmd); err != nil {
		return err
	}
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
}

func controlArgs(args []string, action string) []string {
	out := []string{action}
	for _, arg := range args {
		if len(arg) >= 7 && arg[:7] == "--port=" || len(arg) >= 9 && arg[:9] == "--config=" {
			out = append(out, arg)
		}
	}
	return out
}
