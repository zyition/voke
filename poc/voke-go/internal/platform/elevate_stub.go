//go:build !windows

package platform

import "errors"

// IsProcessElevated 非 Windows 占位：没有 UIPI / 管理员窗口概念。
func IsProcessElevated(uint32) bool { return false }

// CurrentProcessElevated 非 Windows 占位。
func CurrentProcessElevated() bool { return false }

// TargetNeedsElevation 非 Windows 占位：恒 false，走普通粘贴路径。
func TargetNeedsElevation(ClipTarget) bool { return false }

// RunElevated 非 Windows 占位：提权是 Windows 概念。
func RunElevated(string, []string, bool, func(uint32)) error {
	return errors.New("提权仅支持 Windows")
}
