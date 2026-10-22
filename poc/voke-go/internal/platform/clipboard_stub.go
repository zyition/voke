//go:build !windows

package platform

// ClipboardSequence 非 Windows 占位：无 Win32 序列号，恒 0（监控不会触发采集）。
func ClipboardSequence() uint32 { return 0 }

// ForegroundTarget 非 Windows 占位：返回零值，粘贴动作回退为「已复制」。
func ForegroundTarget() ClipTarget { return ClipTarget{} }

// PasteToTarget 非 Windows 占位：恒 false（不可用）。
func PasteToTarget(ClipTarget, bool) bool { return false }
