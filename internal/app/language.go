// language.go：把配置里的语言偏好解析成实际界面语言，并写入 internal/i18n。
//
// 解析规则：
//   - "zh" / "en" 直接用；
//   - "system" / 空串 / 未知值 → 跟随系统（platform.SystemLanguage）；
//     非中文系统一律落到英文（本项目只有中英两套文案）。
package app

import (
	"voke/internal/i18n"
	"voke/internal/logic/appcfg"
	"voke/internal/platform"
)

// resolveLanguage 解析语言偏好（纯函数，便于测试）。
func resolveLanguage(pref appcfg.Language) i18n.Language {
	switch pref {
	case appcfg.LanguageChinese:
		return i18n.Chinese
	case appcfg.LanguageEnglish:
		return i18n.English
	default:
		return platform.SystemLanguage()
	}
}

// applyLanguage 应用语言偏好，返回语言是否发生变化。调用方在变化后需要重跑配置装配
// （Descriptor 里的名称 / 描述是构建时取当前语言）并刷新托盘等已生成的文案。
func (st *appState) applyLanguage(pref appcfg.Language) bool {
	return i18n.SetLanguage(resolveLanguage(pref))
}
