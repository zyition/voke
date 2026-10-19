//go:build windows

package platform

import "syscall"

const swRestore = 9

var (
	procShowWindow          = syscall.NewLazyDLL("user32.dll").NewProc("ShowWindow")
	procSetForegroundWindow = syscall.NewLazyDLL("user32.dll").NewProc("SetForegroundWindow")
)

// ActivateWindow 把 hwnd 指向的窗口切到前台（最小化的先 SW_RESTORE 再 SetForegroundWindow）。
// 对照 voke-sys 的 window activate。hwnd==0 直接 false。
func ActivateWindow(hwnd uintptr) bool {
	if hwnd == 0 {
		return false
	}
	procShowWindow.Call(hwnd, swRestore)
	r, _, _ := procSetForegroundWindow.Call(hwnd)
	return r != 0
}
