//go:build !windows

package platform

import "syscall"

// noConsoleAttr：非 Windows 无控制台窗口闪框问题，恒返回 nil。
func noConsoleAttr(bool) *syscall.SysProcAttr { return nil }
