//go:build !windows

package platform

import "log"

// ActivateWindow 非 Windows 占位：窗口枚举 / 切换是 Windows 概念（hwnd 在 Linux 无意义）。
func ActivateWindow(hwnd uintptr) bool {
	log.Printf("voke：切换窗口在当前平台不提供（hwnd=%d）", hwnd)
	return false
}

// CloseWindow 非 Windows 占位：窗口关闭是 Win32 能力。
func CloseWindow(hwnd uintptr) bool {
	log.Printf("voke：关闭窗口在当前平台不提供（hwnd=%d）", hwnd)
	return false
}
