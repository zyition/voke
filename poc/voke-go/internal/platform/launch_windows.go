//go:build windows

package platform

import "syscall"

// createNoWindow 是 winbase.h 的 CREATE_NO_WINDOW（Go stdlib syscall 未导出该常量）。
const createNoWindow = 0x08000000

// noConsoleAttr：noConsole=true 时给出 CREATE_NO_WINDOW 属性（隐藏控制台子系统 CLI 的
// 黑框，如 Zed 的 bin\Zed.exe、经 cmd.exe /c 跑的 code.cmd）；false → nil（默认窗口）。
func noConsoleAttr(noConsole bool) *syscall.SysProcAttr {
	if !noConsole {
		return nil
	}
	return &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}
