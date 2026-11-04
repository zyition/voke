//go:build !windows

// language_stub.go：非 Windows 占位。产品面向 Windows，跟随系统在 Linux/macOS 上
// 统一回落中文，保证本机跑测试与开发构建时文案稳定（不受 LANG 环境影响）。
package platform

import "voke/internal/i18n"

// SystemLanguage 非 Windows 占位：返回中文。
func SystemLanguage() i18n.Language { return i18n.Chinese }
