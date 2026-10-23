//go:build !windows

package platform

// StartClipboardWatch 非 Windows 占位：没有 WM_CLIPBOARDUPDATE 这套机制，
// 调用方回退到轮询序列号（非 Windows 上 ClipboardSequence 恒 0，实际不做事）。
func StartClipboardWatch() (<-chan struct{}, func(), bool) {
	return nil, nil, false
}
