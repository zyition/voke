//go:build windows

// opendir_windows.go：「关于」页的配置目录入口。用 ShellExecuteW 的 open 谓词
// 打开目录，交给系统默认文件管理器（不硬编码 explorer.exe）。
package platform

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procShellExecuteW = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteW")

// OpenDirectory 用默认文件管理器打开 dir。主线程 / 后台线程均可调用（纯 Syscall，
// 不碰 COM）；失败返回 error，由调用方落日志。
func OpenDirectory(dir string) error {
	if dir == "" {
		return fmt.Errorf("目录为空")
	}
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	file, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return err
	}
	r, _, callErr := procShellExecuteW.Call(
		0,
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(file)),
		0,
		0,
		uintptr(windows.SW_SHOWNORMAL),
	)
	// ShellExecuteW 的返回值 <= 32 表示失败（win32 文档的约定）。
	if r <= 32 {
		return fmt.Errorf("ShellExecuteW(open %s) 失败：%v", dir, callErr)
	}
	return nil
}
