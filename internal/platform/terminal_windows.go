//go:build windows

package platform

import (
	"errors"
	"io/fs"
	"log"
	"os"
)

// ReadTerminalSettings 读出 %LOCALAPPDATA% 下**存在的**两包 settings.json（stable 在前），
// 每份盖上所属包 AUMID。对照 voke-sys read_settings_sources：不存在（没装对应版本，常态）
// 静默跳过；其它读错误（ACL / 占用）记 warn 跳过，绝不 panic。每次调用现读（profile 会被
// 用户在 WT 设置里增删改）。
func ReadTerminalSettings() []TerminalSource {
	localAppData := os.Getenv("LOCALAPPDATA")
	if localAppData == "" {
		log.Printf("voke：LOCALAPPDATA 未设置，WT profiles 不可用")
		return nil
	}
	var out []TerminalSource
	for _, pkg := range WTPackages() {
		path := WTSettingsPath(localAppData, pkg)
		b, err := os.ReadFile(path)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				log.Printf("voke 读取 WT settings.json 失败，跳过：%s：%v", path, err)
			}
			continue
		}
		out = append(out, TerminalSource{Contents: string(b), AUMID: PackageAUMID(pkg)})
	}
	return out
}

// WTExe 返回 wt.exe 完整路径（App Activation 失败兜底直启用）。LOCALAPPDATA 缺失退回裸
// "wt.exe"（交给 CreateProcess 走 PATH 查找，alias 目录默认在 PATH，多数仍能启动）。
func WTExe() string {
	if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
		return WTExePath(localAppData)
	}
	return "wt.exe"
}
