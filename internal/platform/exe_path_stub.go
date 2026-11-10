//go:build !windows

package platform

// pathDirs 非 Windows 占位：编辑器 / wt.exe 发现只面向 Windows 安装布局，
// Linux 单测不应被开发机 PATH 影响（否则 TestEditorCommandsNotFound 会随机器变红）。
func pathDirs() []string { return nil }

// appPathExe 非 Windows：App Paths 是 Windows 注册表概念，恒无。
func appPathExe(string) string { return "" }
