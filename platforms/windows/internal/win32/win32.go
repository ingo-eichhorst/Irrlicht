//go:build windows

// Package win32 provides minimal Win32 syscall wrappers used by the
// Windows tray app: notification-icon lifecycle, message-only window
// hosting, popup-menu rendering, and ShellExecute. Built on top of
// golang.org/x/sys/windows so we never fork third-party tray libs.
package win32

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ----- shell32 -----

var (
	shell32              = windows.NewLazySystemDLL("shell32.dll")
	procShellNotifyIconW = shell32.NewProc("Shell_NotifyIconW")
	procShellExecuteW    = shell32.NewProc("ShellExecuteW")
)

// NIM_* — NOTIFYICONDATA messages.
const (
	NIM_ADD    = 0x00000000
	NIM_MODIFY = 0x00000001
	NIM_DELETE = 0x00000002
)

// NIF_* — NOTIFYICONDATA flags indicating which fields are valid.
const (
	NIF_MESSAGE = 0x00000001
	NIF_ICON    = 0x00000002
	NIF_TIP     = 0x00000004
)

// NotifyIconData mirrors NOTIFYICONDATAW. Field order and types match the
// Win32 header exactly so Go's natural padding aligns with the C layout.
// Caller must set CbSize = unsafe.Sizeof(NotifyIconData{}) before passing.
type NotifyIconData struct {
	CbSize           uint32
	HWnd             windows.HWND
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            windows.Handle
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UTimeoutOrVersion uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GUIDItem         windows.GUID
	HBalloonIcon     windows.Handle
}

// ShellNotifyIcon wraps Shell_NotifyIconW. The message determines the
// operation (NIM_ADD / NIM_MODIFY / NIM_DELETE).
func ShellNotifyIcon(message uint32, data *NotifyIconData) error {
	r1, _, err := procShellNotifyIconW.Call(uintptr(message), uintptr(unsafe.Pointer(data)))
	if r1 == 0 {
		return fmt.Errorf("Shell_NotifyIconW: %w", err)
	}
	return nil
}

// ShellExecute opens a URL or file via the user's registered handler.
// Used to launch the dashboard in the default browser.
func ShellExecute(verb, file string) error {
	v, _ := windows.UTF16PtrFromString(verb)
	f, err := windows.UTF16PtrFromString(file)
	if err != nil {
		return err
	}
	r1, _, _ := procShellExecuteW.Call(0, uintptr(unsafe.Pointer(v)), uintptr(unsafe.Pointer(f)), 0, 0, 1 /* SW_SHOWNORMAL */)
	if r1 <= 32 {
		return fmt.Errorf("ShellExecuteW failed: code %d", r1)
	}
	return nil
}

// ----- user32 / kernel32 -----

var (
	user32                  = windows.NewLazySystemDLL("user32.dll")
	procRegisterClassExW    = user32.NewProc("RegisterClassExW")
	procCreateWindowExW     = user32.NewProc("CreateWindowExW")
	procDefWindowProcW      = user32.NewProc("DefWindowProcW")
	procGetMessageW         = user32.NewProc("GetMessageW")
	procTranslateMessage    = user32.NewProc("TranslateMessage")
	procDispatchMessageW    = user32.NewProc("DispatchMessageW")
	procPostQuitMessage     = user32.NewProc("PostQuitMessage")
	procDestroyWindow       = user32.NewProc("DestroyWindow")
	procCreatePopupMenu     = user32.NewProc("CreatePopupMenu")
	procAppendMenuW         = user32.NewProc("AppendMenuW")
	procDestroyMenu         = user32.NewProc("DestroyMenu")
	procTrackPopupMenu      = user32.NewProc("TrackPopupMenu")
	procGetCursorPos        = user32.NewProc("GetCursorPos")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	procPostMessageW        = user32.NewProc("PostMessageW")
	procCreateIconIndirect  = user32.NewProc("CreateIconIndirect")
	procDestroyIcon         = user32.NewProc("DestroyIcon")

	gdi32              = windows.NewLazySystemDLL("gdi32.dll")
	procCreateBitmap   = gdi32.NewProc("CreateBitmap")
	procDeleteObject   = gdi32.NewProc("DeleteObject")

	kernel32             = windows.NewLazySystemDLL("kernel32.dll")
	procGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")
)

// Window message and style constants used by the tray.
const (
	WM_USER         = 0x0400
	WM_COMMAND      = 0x0111
	WM_DESTROY      = 0x0002
	WM_RBUTTONUP    = 0x0205
	WM_LBUTTONUP    = 0x0202
	HWND_MESSAGE    = ^uintptr(2) // (HWND)-3
	TPM_RIGHTBUTTON = 0x0002
	MF_STRING       = 0x00000000
	MF_SEPARATOR    = 0x00000800
)

// WndClassEx mirrors WNDCLASSEXW.
type WndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   windows.Handle
	Icon       windows.Handle
	Cursor     windows.Handle
	Background windows.Handle
	MenuName   *uint16
	ClassName  *uint16
	IconSm     windows.Handle
}

// Point mirrors POINT.
type Point struct{ X, Y int32 }

// Msg mirrors MSG.
type Msg struct {
	HWnd    windows.HWND
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      Point
	Private uint32
}

// IconInfo mirrors ICONINFO.
type IconInfo struct {
	IsIcon   uint32
	XHotspot uint32
	YHotspot uint32
	HbmMask  windows.Handle
	HbmColor windows.Handle
}

// GetModuleHandle returns the process's HINSTANCE.
func GetModuleHandle() windows.Handle {
	h, _, _ := procGetModuleHandleW.Call(0)
	return windows.Handle(h)
}

// RegisterClassEx registers a window class. Returns a non-zero atom on success.
func RegisterClassEx(wc *WndClassEx) (uint16, error) {
	atom, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(wc)))
	if atom == 0 {
		return 0, fmt.Errorf("RegisterClassExW: %w", err)
	}
	return uint16(atom), nil
}

// CreateMessageWindow creates a hidden message-only window for receiving
// tray notifications. HWND_MESSAGE parents the window so it never appears
// in the taskbar or Z-order.
func CreateMessageWindow(className string) (windows.HWND, error) {
	cls, _ := windows.UTF16PtrFromString(className)
	hwnd, _, err := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(cls)),
		0, 0, 0, 0, 0, 0,
		HWND_MESSAGE,
		0,
		uintptr(GetModuleHandle()),
		0,
	)
	if hwnd == 0 {
		return 0, fmt.Errorf("CreateWindowExW: %w", err)
	}
	return windows.HWND(hwnd), nil
}

// DefWindowProc forwards unhandled messages to the system default handler.
func DefWindowProc(hwnd windows.HWND, msg uint32, wParam, lParam uintptr) uintptr {
	r, _, _ := procDefWindowProcW.Call(uintptr(hwnd), uintptr(msg), wParam, lParam)
	return r
}

// RunMessageLoop pumps Win32 messages until WM_QUIT. Must be called on the
// thread that created the window.
func RunMessageLoop() {
	var msg Msg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(r) <= 0 {
			return
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}
}

// PostQuit posts WM_QUIT to break out of RunMessageLoop.
func PostQuit() { procPostQuitMessage.Call(0) }

// PostMessage posts a message to the given window.
func PostMessage(hwnd windows.HWND, msg uint32, wParam, lParam uintptr) {
	procPostMessageW.Call(uintptr(hwnd), uintptr(msg), wParam, lParam)
}

// DestroyWindow destroys a window created by CreateMessageWindow.
func DestroyWindow(hwnd windows.HWND) { procDestroyWindow.Call(uintptr(hwnd)) }

// ----- popup menu -----

// PopupMenu wraps an HMENU with a builder API.
type PopupMenu struct{ Handle uintptr }

// CreatePopupMenu allocates an empty popup menu. Caller must call Destroy.
func CreatePopupMenu() *PopupMenu {
	h, _, _ := procCreatePopupMenu.Call()
	return &PopupMenu{Handle: h}
}

// AppendItem adds a labeled command. id is the WM_COMMAND identifier sent
// when the user picks the item.
func (m *PopupMenu) AppendItem(id uint16, label string) {
	s, _ := windows.UTF16PtrFromString(label)
	procAppendMenuW.Call(m.Handle, MF_STRING, uintptr(id), uintptr(unsafe.Pointer(s)))
}

// AppendSeparator adds a separator line to the menu.
func (m *PopupMenu) AppendSeparator() {
	procAppendMenuW.Call(m.Handle, MF_SEPARATOR, 0, 0)
}

// Track shows the popup at the current cursor position. The trailing null
// PostMessage is the documented Win32 idiom that ensures the menu loop
// terminates properly when the user clicks outside.
func (m *PopupMenu) Track(hwnd windows.HWND) {
	var pt Point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	procSetForegroundWindow.Call(uintptr(hwnd))
	procTrackPopupMenu.Call(m.Handle, TPM_RIGHTBUTTON, uintptr(pt.X), uintptr(pt.Y), 0, uintptr(hwnd), 0)
	procPostMessageW.Call(uintptr(hwnd), 0, 0, 0)
}

// Destroy releases the menu handle.
func (m *PopupMenu) Destroy() { procDestroyMenu.Call(m.Handle) }

// ----- icon construction -----

// CreateColorIcon builds a 16x16 HICON drawn as a filled colored disc on
// a transparent background. The mask bitmap controls visibility, the color
// bitmap supplies the actual pixel color so we get true RGB tray icons
// without checking in any .ico files.
//
// CreateIconIndirect takes ownership of the bitmaps' lifetimes only after
// success — on failure the caller must DeleteObject them, which we do.
func CreateColorIcon(r, g, b byte) (windows.Handle, error) {
	const w, h = 16, 16

	// AND-mask: 1bpp, 1 = transparent, 0 = use color bitmap.
	andMask := make([]byte, (w/8)*h)
	// Color: 32bpp BGRA (Windows DIB convention is little-endian RGB → BGRA in memory).
	color := make([]byte, w*h*4)
	cx, cy := float64(w-1)/2, float64(h-1)/2
	rad := 7.0
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx, dy := float64(x)-cx, float64(y)-cy
			inside := dx*dx+dy*dy <= rad*rad
			if inside {
				i := (y*w + x) * 4
				color[i+0] = b
				color[i+1] = g
				color[i+2] = r
				color[i+3] = 0xFF
			} else {
				bit := byte(0x80 >> uint(x%8))
				andMask[y*(w/8)+x/8] |= bit
			}
		}
	}

	hbmMask, _, err := procCreateBitmap.Call(uintptr(w), uintptr(h), 1, 1, uintptr(unsafe.Pointer(&andMask[0])))
	if hbmMask == 0 {
		return 0, fmt.Errorf("CreateBitmap mask: %w", err)
	}
	hbmColor, _, err := procCreateBitmap.Call(uintptr(w), uintptr(h), 1, 32, uintptr(unsafe.Pointer(&color[0])))
	if hbmColor == 0 {
		procDeleteObject.Call(hbmMask)
		return 0, fmt.Errorf("CreateBitmap color: %w", err)
	}

	info := IconInfo{
		IsIcon:   1,
		HbmMask:  windows.Handle(hbmMask),
		HbmColor: windows.Handle(hbmColor),
	}
	hicon, _, err := procCreateIconIndirect.Call(uintptr(unsafe.Pointer(&info)))
	// Bitmaps are referenced by the icon; we can release our handles now.
	procDeleteObject.Call(hbmMask)
	procDeleteObject.Call(hbmColor)
	if hicon == 0 {
		return 0, fmt.Errorf("CreateIconIndirect: %w", err)
	}
	return windows.Handle(hicon), nil
}

// DestroyIcon releases an HICON allocated by CreateColorIcon.
func DestroyIcon(h windows.Handle) { procDestroyIcon.Call(uintptr(h)) }

// ----- single-instance mutex -----

// AcquireSingleInstanceMutex returns true if this is the first instance
// holding the named mutex. The OS releases the handle on process exit.
func AcquireSingleInstanceMutex(name string) (bool, error) {
	n, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return false, err
	}
	h, err := windows.CreateMutex(nil, true, n)
	if err != nil {
		if errno, ok := err.(syscall.Errno); ok && errno == windows.ERROR_ALREADY_EXISTS {
			windows.CloseHandle(h)
			return false, nil
		}
		return false, err
	}
	_ = h // intentionally retained until process exit
	return true, nil
}
