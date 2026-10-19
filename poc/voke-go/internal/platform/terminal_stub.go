//go:build !windows

package platform

// ReadTerminalSettings 非 Windows 占位：WT settings 布局是 Windows 概念（无 %LOCALAPPDATA%\
// Packages）。返回 nil → terminal provider 恒空（与接线前 NewEmpty 逐字一致，零回归）。
func ReadTerminalSettings() []TerminalSource { return nil }

// WTExe 非 Windows：无 App Execution Alias，退回裸名（真机上这条不会被执行到）。
func WTExe() string { return "wt.exe" }
