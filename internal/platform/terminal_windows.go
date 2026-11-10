//go:build windows

package platform

import (
	"errors"
	"io/fs"
	"log"
	"os"
)

// ReadTerminalSettings 读出 %LOCALAPPDATA% 下**存在的** settings.json：两包（stable
// 在前、Preview 在后）+ 未打包版（Scoop / Chocolatey / ZIP）。包版每份盖上所属包
// AUMID；未打包版没有 AUMID，执行层会走 wt.exe 直启兜底。不存在（没装对应版本，
// 常态）静默跳过；其它读错误（ACL / 占用）记 warn 跳过，绝不 panic。每次调用现读
// （profile 会被用户在 WT 设置里增删改）。
func ReadTerminalSettings() []TerminalSource {
	localAppData := os.Getenv("LOCALAPPDATA")
	if localAppData == "" {
		log.Printf("voke：LOCALAPPDATA 未设置，WT profiles 不可用")
		return nil
	}
	var out []TerminalSource
	for _, pkg := range WTPackages() {
		out = appendSettings(out, WTSettingsPath(localAppData, pkg), PackageAUMID(pkg))
	}
	// 未打包版放在最后：与包版同名 profile 去重时，包版（有 AUMID、激活更可靠）优先。
	out = appendSettings(out, WTUnpackagedSettingsPath(localAppData), "")
	return out
}

// appendSettings 读一份存在的 settings.json 并追加；不存在静默跳过。
func appendSettings(out []TerminalSource, path, aumid string) []TerminalSource {
	b, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Printf("voke 读取 WT settings.json 失败，跳过：%s：%v", path, err)
		}
		return out
	}
	return append(out, TerminalSource{Contents: string(b), AUMID: aumid})
}

// WTExe 返回 wt.exe 完整路径（App Activation 失败兜底直启用）。先试 MSIX 的 App
// Execution Alias；未打包版（没有 alias）再查 PATH（注册表当前值，Scoop / winget
// shim 在这里）与 App Paths；都找不到才退回裸 "wt.exe" 交给 CreateProcess。
func WTExe() string {
	if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
		if p := WTExePath(localAppData); fileExists(p) {
			return p
		}
	}
	if p := findExecutableInDirs(pathDirs(), "wt.exe"); p != "" {
		return p
	}
	if p := appPathExe("wt.exe"); p != "" {
		return p
	}
	return "wt.exe"
}
