//go:build !windows

package platform

// WindowInfo 与 Windows 侧同形（供跨平台调用点统一构造 windows provider 的 Entry）。
type WindowInfo struct {
	Hwnd        int64
	Title       string
	ProcessName string
	ProcessPath string
}

// EnumerateWindows 非 Windows 占位：顶层窗口枚举是 Win32 概念（无 EnumWindows）。返回 nil →
// windows provider 恒空，与接线前 NewEmpty 逐字一致（零回归）。
func EnumerateWindows() []WindowInfo { return nil }
