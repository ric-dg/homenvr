//go:build windows

// homenvrd-tray is a user-session tray helper for the HomeNVR control panel.
// A SYSTEM service (session 0) cannot show tray icons, so this tiny idle
// binary runs in the logged-in user's session and talks to the panel's HTTP
// API. It keeps a hidden message window and does zero polling: it only wakes
// on tray clicks and makes a request when a menu action is chosen.
//
// It is Windows-only (see tray_other.go for the stub used on other platforms).
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"syscall"
	"time"
	"unsafe"
)

var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	user32   = syscall.NewLazyDLL("user32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")

	procGetModuleHandleW    = kernel32.NewProc("GetModuleHandleW")
	procRegisterClassW      = user32.NewProc("RegisterClassW")
	procCreateWindowExW     = user32.NewProc("CreateWindowExW")
	procDefWindowProcW      = user32.NewProc("DefWindowProcW")
	procGetMessageW         = user32.NewProc("GetMessageW")
	procTranslateMessage    = user32.NewProc("TranslateMessage")
	procDispatchMessageW    = user32.NewProc("DispatchMessageW")
	procLoadIconW           = user32.NewProc("LoadIconW")
	procCreatePopupMenu     = user32.NewProc("CreatePopupMenu")
	procAppendMenuW         = user32.NewProc("AppendMenuW")
	procTrackPopupMenu      = user32.NewProc("TrackPopupMenu")
	procDestroyMenu         = user32.NewProc("DestroyMenu")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	procGetCursorPos        = user32.NewProc("GetCursorPos")
	procPostMessageW        = user32.NewProc("PostMessageW")
	procPostQuitMessage     = user32.NewProc("PostQuitMessage")
	procMessageBoxW         = user32.NewProc("MessageBoxW")
	procShellNotifyIconW    = shell32.NewProc("Shell_NotifyIconW")
	procShellExecuteW       = shell32.NewProc("ShellExecuteW")
)

const (
	hwndMessage = ^syscall.Handle(2) // ((HWND)-3)

	wmNull        = 0x0000
	wmDestroy     = 0x0002
	wmLButtonUp   = 0x0202
	wmLButtonDbl  = 0x0203
	wmRButtonUp   = 0x0205
	wmContextMenu = 0x007B
	wmApp         = 0x8000

	wmIconCallback = wmApp + 1

	nimAdd    = 0
	nimModify = 1
	nimDelete = 2

	nifMessage = 0x1
	nifIcon    = 0x2
	nifTip     = 0x4
	nifInfo    = 0x10

	niifInfo = 0x1

	mfString    = 0x0
	mfSeparator = 0x800
	tpmReturn   = 0x100
	tpmNonotify = 0x80
	tpmRight    = 0x2

	swShownormal = 1

	idiApplication = 32512
)

type point struct {
	x, y int32
}

type msg struct {
	hwnd    syscall.Handle
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      point
}

type wndClass struct {
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     syscall.Handle
	hIcon         syscall.Handle
	hCursor       syscall.Handle
	hbrBackground syscall.Handle
	lpszMenuName  *uint16
	lpszClassName *uint16
}

type notifyIconData struct {
	cbSize           uint32
	hWnd             syscall.Handle
	uID              uint32
	uFlags           uint32
	uCallbackMessage uint32
	hIcon            syscall.Handle
	szTip            [128]uint16
	dwState          uint32
	dwStateMask      uint32
	szInfo           [256]uint16
	uVersion         uint32
	szInfoTitle      [64]uint16
	dwInfoFlags      uint32
	guidItem         syscall.GUID
	hBalloonIcon     syscall.Handle
}

const (
	menuOpenPanel    = 100
	menuOpenLogs     = 101
	menuRunRetention = 102
	menuRestart      = 103
	menuExit         = 104
)

var panelURL string

func fatal(msg string) {
	t, _ := syscall.UTF16PtrFromString("HomeNVR tray")
	m, _ := syscall.UTF16PtrFromString(msg)
	procMessageBoxW.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), 0x10)
	fmt.Fprintln(os.Stderr, "homenvrd-tray:", msg)
	os.Exit(1)
}

func toU16(s string) *uint16 {
	p, err := syscall.UTF16PtrFromString(s)
	if err != nil {
		fatal("invalid string: " + s)
	}
	return p
}

func u16s(s string) []uint16 {
	u, err := syscall.UTF16FromString(s)
	if err != nil {
		fatal("invalid string: " + s)
	}
	return u
}

func httpPost(path string) {
	c := &http.Client{Timeout: 5 * time.Second}
	resp, err := c.Post(panelURL+path, "application/json", nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "homenvrd-tray:", path, err)
		return
	}
	resp.Body.Close()
}

func openURL(u string) {
	procShellExecuteW.Call(0, uintptr(unsafe.Pointer(toU16("open"))), uintptr(unsafe.Pointer(toU16(u))), 0, 0, swShownormal)
}

func showMenu(hwnd syscall.Handle) {
	procSetForegroundWindow.Call(uintptr(hwnd))
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	items := []struct {
		id   uint32
		text string
	}{
		{menuOpenPanel, "Open Panel"},
		{menuOpenLogs, "Open Logs"},
		{menuRunRetention, "Run Retention"},
		{menuRestart, "Restart Service"},
	}
	for _, it := range items {
		procAppendMenuW.Call(menu, mfString, uintptr(it.id), uintptr(unsafe.Pointer(toU16(it.text))))
	}
	procAppendMenuW.Call(menu, mfSeparator, 0, 0)
	procAppendMenuW.Call(menu, mfString, uintptr(menuExit), uintptr(unsafe.Pointer(toU16("Exit"))))

	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	cmd, _, _ := procTrackPopupMenu.Call(menu, tpmRight|tpmReturn|tpmNonotify, uintptr(pt.x), uintptr(pt.y), 0, uintptr(hwnd), 0)
	procDestroyMenu.Call(menu)
	// Menu needs this to dismiss properly.
	procPostMessageW.Call(uintptr(hwnd), wmNull, 0, 0)

	switch cmd {
	case menuOpenPanel:
		openURL(panelURL + "/")
	case menuOpenLogs:
		openURL(panelURL + "/#/logs")
	case menuRunRetention:
		httpPost("/api/retention/run")
	case menuRestart:
		httpPost("/api/restart")
	case menuExit:
		procPostQuitMessage.Call(0)
	}
}

func setBalloon(hwnd syscall.Handle) {
	nid := notifyIconData{cbSize: uint32(unsafe.Sizeof(notifyIconData{}))}
	nid.hWnd = hwnd
	nid.uID = 1
	nid.uFlags = nifInfo
	nid.dwInfoFlags = niifInfo
	copy(nid.szInfoTitle[:], u16s("HomeNVR"))
	copy(nid.szInfo[:], u16s("Running. Panel: "+panelURL))
	procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&nid)))
}

func main() {
	var panel string
	flag.StringVar(&panel, "panel", "http://127.0.0.1:8080", "control panel base URL")
	flag.Parse()
	panelURL = panel

	hInst, _, _ := procGetModuleHandleW.Call(0)
	if hInst == 0 {
		fatal("GetModuleHandleW failed")
	}
	className := toU16("HomeNVRTrayClass")
	class := wndClass{
		lpfnWndProc:   procDefWindowProcW.Addr(),
		hInstance:     syscall.Handle(hInst),
		hIcon:         0,
		lpszClassName: className,
	}
	if atom, _, _ := procRegisterClassW.Call(uintptr(unsafe.Pointer(&class))); atom == 0 {
		fatal("RegisterClassW failed")
	}
	hwnd, _, _ := procCreateWindowExW.Call(
		0, uintptr(unsafe.Pointer(className)), 0, 0,
		0, 0, 0, 0, uintptr(hwndMessage), 0, uintptr(hInst), 0)
	if hwnd == 0 {
		fatal("CreateWindowExW failed")
	}
	icon, _, _ := procLoadIconW.Call(0, idiApplication)

	nid := notifyIconData{cbSize: uint32(unsafe.Sizeof(notifyIconData{}))}
	nid.hWnd = syscall.Handle(hwnd)
	nid.uID = 1
	nid.uFlags = nifMessage | nifIcon | nifTip
	nid.uCallbackMessage = wmIconCallback
	nid.hIcon = syscall.Handle(icon)
	copy(nid.szTip[:], u16s("HomeNVR"))
	if r, _, _ := procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&nid))); r == 0 {
		fatal("Shell_NotifyIcon failed")
	}
	setBalloon(syscall.Handle(hwnd))

	var m msg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if r == 0 { // WM_QUIT
			break
		}
		if r == ^uintptr(0) { // GetMessage error
			break
		}
		if m.message == wmIconCallback {
			switch m.lParam {
			case wmRButtonUp, wmContextMenu:
				showMenu(syscall.Handle(hwnd))
			case wmLButtonDbl:
				openURL(panelURL + "/")
			}
			continue
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}

	procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
}
