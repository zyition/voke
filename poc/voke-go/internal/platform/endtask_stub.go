//go:build !windows

package platform

import "errors"

// EndTask 非 Windows 占位：窗口任务管理是 Win32 能力。
func EndTask(uintptr) error {
	return errors.New("结束任务仅支持 Windows")
}
