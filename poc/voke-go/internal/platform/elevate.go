// elevate.go：提权能力的跨平台约定（ShellExecuteEx 实现在 elevate_windows.go）。
package platform

import "errors"

// ErrElevationCanceled 表示用户在 UAC 提示里取消 / 拒绝提权。
var ErrElevationCanceled = errors.New("用户取消了管理员权限请求")
