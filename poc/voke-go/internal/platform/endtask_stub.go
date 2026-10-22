//go:build !windows

package platform

import (
	"errors"
	"time"
)

// RequestEndTask 非 Windows 占位：窗口任务管理是 Win32 能力。
func RequestEndTask(uintptr) error {
	return errors.New("结束任务仅支持 Windows")
}

// ForceEndTaskIfHung 非 Windows 占位：恒 false。
func ForceEndTaskIfHung(uintptr, time.Duration) (bool, error) {
	return false, errors.New("结束任务仅支持 Windows")
}
