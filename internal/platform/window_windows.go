//go:build windows

package platform

import "syscall"

const (
	swRestore    = 9
	wmClose      = 0x0010
	wmSysCommand = 0x0112
	scRestore    = 0xF120
)

var (
	procShowWindow          = syscall.NewLazyDLL("user32.dll").NewProc("ShowWindow")
	procSetForegroundWindow = syscall.NewLazyDLL("user32.dll").NewProc("SetForegroundWindow")
	procPostMessageW        = syscall.NewLazyDLL("user32.dll").NewProc("PostMessageW")
	procSendMessageW        = syscall.NewLazyDLL("user32.dll").NewProc("SendMessageW")
	procFlashWindow         = syscall.NewLazyDLL("user32.dll").NewProc("FlashWindow")
)

// ActivateWindow 把 hwnd 指向的窗口提到最前并聚焦。序列对照 PowerToys Run /
// CmdPal 的 WindowWalker（Microsoft.Plugin.WindowWalker/Components/Window.cs）：
//   - 最小化 → ShowWindow(SW_RESTORE)；提权进程上 ShowWindow 可能失败，再退到
//     SendMessage(WM_SYSCOMMAND, SC_RESTORE)；
//   - 其余状态（正常 / 最大化）→ **绝不调用 ShowWindow**，只 SetForegroundWindow。
//     SW_SHOW 在部分窗口上会改写 placement，表现为最大化窗口偶发被还原——这正是
//     之前间歇复现的 bug；PowerToys 同样避开这条路径。
//
// 前台锁导致 SetForegroundWindow 失败时用 forceForeground 兜底，仍失败则
// FlashWindow 闪一下任务栏提示。窗口有效即返回 true：启动器据此收起面板，
// 不能因为一次前台锁误判把面板留在最前面、看起来像「没切过去」。
// hwnd==0 或窗口已销毁返回 false。
func ActivateWindow(hwnd uintptr) bool {
	if hwnd == 0 {
		return false
	}
	if ok, _, _ := procIsWindow.Call(hwnd); ok == 0 {
		return false
	}
	if iconic, _, _ := procIsIconic.Call(hwnd); iconic != 0 {
		if r, _, _ := procShowWindow.Call(hwnd, swRestore); r == 0 {
			procSendMessageW.Call(hwnd, wmSysCommand, scRestore, 0)
		}
	}
	if r, _, _ := procSetForegroundWindow.Call(hwnd); r != 0 {
		return true
	}
	if !forceForeground(hwnd) {
		procFlashWindow.Call(hwnd, 1)
	}
	return true
}

// CloseWindow 向 hwnd 指向的窗口投递 WM_CLOSE，等价点击标题栏关闭按钮：
// 应用可弹保存确认，也可能拒绝关闭。线程：可在任意线程调用（PostMessage
// 不等待目标处理）；当前只由主线程的窗口行 action 调用。
func CloseWindow(hwnd uintptr) bool {
	if hwnd == 0 {
		return false
	}
	r, _, _ := procPostMessageW.Call(hwnd, wmClose, 0, 0)
	return r != 0
}
