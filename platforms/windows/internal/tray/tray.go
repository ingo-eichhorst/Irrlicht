//go:build windows

// Package tray controls the Windows notification-area icon: registers the
// hidden message-only window, draws state-driven icons, owns the right-
// click popup menu, and routes click events to the dashboard launcher.
package tray

import (
	"fmt"
	"log"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"irrlicht/platforms/windows/internal/daemon"
	"irrlicht/platforms/windows/internal/state"
	"irrlicht/platforms/windows/internal/win32"
)

// Brand colors. Match platforms/web/index.html lines 20–28 and the macOS
// MenuBarImageBuilder palette so all three frontends agree on what
// "working" / "waiting" / "ready" look like at a glance.
type rgb struct{ r, g, b byte }

var iconColors = map[state.Tray]rgb{
	state.TrayWorking: {0x8B, 0x5C, 0xF6}, // purple
	state.TrayWaiting: {0xFF, 0x95, 0x00}, // amber
	state.TrayReady:   {0x34, 0xC7, 0x59}, // green
	state.TrayOffline: {0x70, 0x70, 0x70}, // grey when daemon is down
}

// Menu command IDs returned via WM_COMMAND.
const (
	cmdOpenDashboard = 1
	cmdOpenBrowser   = 2
	cmdQuit          = 3
)

// Tray callback message — Shell_NotifyIcon delivers all click events to
// the host window via this single private message.
const wmTrayCallback = win32.WM_USER + 1

// Tray holds the icon lifecycle on a single OS thread.
type Tray struct {
	hwnd      windows.HWND
	iconID    uint32
	className string

	mu          sync.Mutex
	currTray    state.Tray
	currIcon    windows.Handle
	iconCache   map[state.Tray]windows.Handle
	pendingSnap state.Snapshot

	onOpenDashboard func()
	onQuit          func()
}

// New creates a Tray bound to the given action callbacks. Run must be
// called on a goroutine that calls runtime.LockOSThread before starting.
func New(onOpenDashboard, onQuit func()) *Tray {
	return &Tray{
		iconID:          1,
		className:       "IrrlichtTrayWindow",
		iconCache:       make(map[state.Tray]windows.Handle),
		onOpenDashboard: onOpenDashboard,
		onQuit:          onQuit,
	}
}

// Run registers the window class, creates the message-only host window,
// adds the tray icon, and pumps Win32 messages until quit.
func (t *Tray) Run(initial state.Tray, updates <-chan state.Snapshot) error {
	wndproc := syscall.NewCallback(t.wndProc)
	clsName, _ := windows.UTF16PtrFromString(t.className)
	wc := win32.WndClassEx{
		Size:      uint32(unsafe.Sizeof(win32.WndClassEx{})),
		WndProc:   wndproc,
		Instance:  win32.GetModuleHandle(),
		ClassName: clsName,
	}
	if _, err := win32.RegisterClassEx(&wc); err != nil {
		return fmt.Errorf("RegisterClassEx: %w", err)
	}

	hwnd, err := win32.CreateMessageWindow(t.className)
	if err != nil {
		return err
	}
	t.hwnd = hwnd
	defer win32.DestroyWindow(hwnd)

	if err := t.installIcon(initial); err != nil {
		return err
	}
	defer t.removeIcon()

	// Funnel state-snapshot updates into the UI thread via PostMessage —
	// we can't touch the icon from the goroutine that owns the WebSocket.
	go func() {
		for snap := range updates {
			t.mu.Lock()
			t.pendingSnap = snap
			t.mu.Unlock()
			win32.PostMessage(hwnd, wmTrayUpdate, 0, 0)
		}
	}()

	win32.RunMessageLoop()
	return nil
}

// Tray-update private message — wakes the UI thread to apply the latest
// pendingSnap. Carries no payload (PostMessage's two integer params can't
// fit a Snapshot); the wndProc reads it under the mutex.
const wmTrayUpdate = win32.WM_USER + 2

// installIcon adds the tray icon with the initial state.
func (t *Tray) installIcon(s state.Tray) error {
	icon, err := t.iconFor(s)
	if err != nil {
		return err
	}
	t.currIcon = icon
	t.currTray = s

	data := t.iconData(s)
	data.UFlags = win32.NIF_MESSAGE | win32.NIF_ICON | win32.NIF_TIP
	data.HIcon = icon
	if err := win32.ShellNotifyIcon(win32.NIM_ADD, &data); err != nil {
		return fmt.Errorf("NIM_ADD: %w", err)
	}
	return nil
}

// removeIcon takes the tray icon out of the notification area.
func (t *Tray) removeIcon() {
	data := t.iconData(t.currTray)
	_ = win32.ShellNotifyIcon(win32.NIM_DELETE, &data)
}

// updateIcon swaps the icon image and tooltip to reflect a new state.
func (t *Tray) updateIcon(s state.Snapshot) {
	icon, err := t.iconFor(s.Tray)
	if err != nil {
		log.Printf("tray: build icon for %s: %v", s.Tray, err)
		return
	}
	t.currIcon = icon
	t.currTray = s.Tray

	data := t.iconDataForSnap(s)
	data.UFlags = win32.NIF_MESSAGE | win32.NIF_ICON | win32.NIF_TIP
	data.HIcon = icon
	_ = win32.ShellNotifyIcon(win32.NIM_MODIFY, &data)
}

// iconData builds a NotifyIconData with our fixed UID and message routing.
func (t *Tray) iconData(s state.Tray) win32.NotifyIconData {
	d := win32.NotifyIconData{
		HWnd:             t.hwnd,
		UID:              t.iconID,
		UCallbackMessage: wmTrayCallback,
	}
	d.CbSize = uint32(unsafe.Sizeof(d))
	tip := tooltipFor(s, 0, 0, 0)
	copyUTF16(d.SzTip[:], tip)
	return d
}

// iconDataForSnap builds NotifyIconData with a tooltip carrying live counts.
func (t *Tray) iconDataForSnap(s state.Snapshot) win32.NotifyIconData {
	d := win32.NotifyIconData{
		HWnd:             t.hwnd,
		UID:              t.iconID,
		UCallbackMessage: wmTrayCallback,
	}
	d.CbSize = uint32(unsafe.Sizeof(d))
	tip := tooltipFor(s.Tray, s.Counts[state.TrayWorking], s.Counts[state.TrayWaiting], s.Counts[state.TrayReady])
	copyUTF16(d.SzTip[:], tip)
	return d
}

// iconFor returns a cached HICON for the given tray state. Icons are
// drawn once and cached for the process lifetime.
func (t *Tray) iconFor(s state.Tray) (windows.Handle, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if h, ok := t.iconCache[s]; ok {
		return h, nil
	}
	c, ok := iconColors[s]
	if !ok {
		c = iconColors[state.TrayReady]
	}
	h, err := win32.CreateColorIcon(c.r, c.g, c.b)
	if err != nil {
		return 0, err
	}
	t.iconCache[s] = h
	return h, nil
}

// tooltipFor builds a multi-line tooltip describing live counts. SzTip
// caps at 127 chars + null terminator; we stay well under that.
func tooltipFor(s state.Tray, working, waiting, ready int) string {
	switch s {
	case state.TrayOffline:
		return "Irrlicht — daemon offline"
	default:
		return fmt.Sprintf("Irrlicht — %d working, %d waiting, %d ready", working, waiting, ready)
	}
}

// copyUTF16 copies s into dst as a null-terminated UTF-16 sequence.
func copyUTF16(dst []uint16, s string) {
	enc := windows.StringToUTF16(s)
	if len(enc) > len(dst) {
		enc = enc[:len(dst)-1]
		enc = append(enc, 0)
	}
	copy(dst, enc)
}

// wndProc is the message dispatch callback. Handles tray clicks, menu
// commands, and our custom state-update message.
func (t *Tray) wndProc(hwnd windows.HWND, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case wmTrayCallback:
		// LParam is the mouse event from the icon.
		switch uint32(lParam) {
		case win32.WM_LBUTTONUP:
			if t.onOpenDashboard != nil {
				t.onOpenDashboard()
			}
		case win32.WM_RBUTTONUP:
			t.showMenu(hwnd)
		}
		return 0

	case wmTrayUpdate:
		t.mu.Lock()
		snap := t.pendingSnap
		t.mu.Unlock()
		t.updateIcon(snap)
		return 0

	case win32.WM_COMMAND:
		switch uint16(wParam) {
		case cmdOpenDashboard:
			if t.onOpenDashboard != nil {
				t.onOpenDashboard()
			}
		case cmdOpenBrowser:
			if err := win32.ShellExecute("open", daemon.DaemonURL("/")); err != nil {
				log.Printf("tray: open browser: %v", err)
			}
		case cmdQuit:
			if t.onQuit != nil {
				t.onQuit()
			}
			win32.PostQuit()
		}
		return 0

	case win32.WM_DESTROY:
		win32.PostQuit()
		return 0
	}
	return win32.DefWindowProc(hwnd, msg, wParam, lParam)
}

// showMenu builds and tracks the right-click context menu.
func (t *Tray) showMenu(hwnd windows.HWND) {
	m := win32.CreatePopupMenu()
	defer m.Destroy()
	m.AppendItem(cmdOpenDashboard, "Open Dashboard")
	m.AppendItem(cmdOpenBrowser, "Open in Browser")
	m.AppendSeparator()
	m.AppendItem(cmdQuit, "Quit")
	m.Track(hwnd)
}
