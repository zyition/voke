// terminal.go：Windows Terminal 设置文件**定位**（terminal provider 数据源的 sys 半边，
// 对照 voke-sys/src/terminal.rs）。路径拼接是**纯函数**（任何平台编译、Linux 原生可单测）；
// 真正读文件的 ReadTerminalSettings 挂 windows（依赖 %LOCALAPPDATA% 语义与真实文件系统布局），
// 非 Windows 是占位（返回 nil → provider 恒空，与接线前逐字一致）。
//
// 解析 / 合并去重不在本层——那是 internal/providers/terminal 的纯逻辑（ProfilesFromSettings）；
// 这里只「把文件内容读出来 + 给每份盖上所属包 AUMID」（App Activation 要激活对的包）。
package platform

import "path/filepath"

// WT 包目录名（settings.json 在 `%LOCALAPPDATA%\Packages\<这里>\LocalState\`）。
const (
	wtStablePackage  = "Microsoft.WindowsTerminal_8wekyb3d8bbwe"
	wtPreviewPackage = "Microsoft.WindowsTerminalPreview_8wekyb3d8bbwe"
)

// WTPackages 返回两个包目录名，**stable 在前**（合并去重时 stable 优先，顺序即
// terminal.ProfilesFromSettings 的优先级语义）。
func WTPackages() []string { return []string{wtStablePackage, wtPreviewPackage} }

// PackageAUMID = `<包家族名>!App`（App Activation 的 appUserModelId，实测
// `Microsoft.WindowsTerminal_8wekyb3d8bbwe!App`）。纯函数，任何平台可测。
func PackageAUMID(pkg string) string { return pkg + "!App" }

// WTSettingsPath：某包 settings.json 的路径（纯拼路径、不查存在性）。用分段 Join 而非带
// `\` 的字面串——Linux 上 `\` 不是分隔符，分段拼才两平台一致、可单测。
func WTSettingsPath(localAppData, pkg string) string {
	return filepath.Join(localAppData, "Packages", pkg, "LocalState", "settings.json")
}

// WTExePath：wt.exe 的 App Execution Alias 完整路径（`%LOCALAPPDATA%\Microsoft\WindowsApps\wt.exe`）。
// 用完整路径而非裸 `wt.exe`：启动行为与用户改过的 PATH 无关。
func WTExePath(localAppData string) string {
	return filepath.Join(localAppData, "Microsoft", "WindowsApps", "wt.exe")
}

// TerminalSource 是一份 settings.json 原文 + 所属 WT 包的 AUMID（上层转成
// terminal.SettingsSource 喂 ProfilesFromSettings）。
type TerminalSource struct {
	Contents string
	AUMID    string
}
