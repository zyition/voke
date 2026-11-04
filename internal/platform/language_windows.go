//go:build windows

// language_windows.go：读取 Windows 界面语言，用于「跟随系统」的语言偏好。
//
// GetUserDefaultUILanguage 返回 LANGID：低 10 位是主语言 ID，中文为 0x04
// （LANG_CHINESE，含简繁各子语言）。其余语言一律按英文界面处理——本项目只提供
// 中英两套文案，非中文用户看到英文比看到中文更有用。
package platform

import (
	"syscall"

	"voke/internal/i18n"
)

var procGetUserDefaultUILanguage = syscall.NewLazyDLL("kernel32.dll").
	NewProc("GetUserDefaultUILanguage")

// SystemLanguage 返回系统界面语言对应的界面语言（zh / en）。
// 主线程 / 后台线程均可调用（纯 Win32 查询，无 COM）。
func SystemLanguage() i18n.Language {
	r, _, _ := procGetUserDefaultUILanguage.Call()
	primary := uint16(r) & 0x3ff
	if primary == 0x04 { // LANG_CHINESE
		return i18n.Chinese
	}
	return i18n.English
}
