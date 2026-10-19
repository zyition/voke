// settings.go 的 headless 可测面：主题标签↔码值往返 + seedSettings（从 st.cfg 顶层 +
// st.providers 刷绑定态）。窗口的 mygo 渲染 / 保存落盘只在真机验（见 settings.go 头），
// 但这两段纯逻辑不碰 mygo，Linux 原生即可自证。
package app

import (
	"path/filepath"
	"testing"

	"github.com/zyition/voke/poc/voke-go/internal/logic/appcfg"
	"github.com/zyition/voke/poc/voke-go/internal/logic/config"
	"github.com/zyition/voke/poc/voke-go/internal/logic/engine"
)

// TestAboutVersionInjected 验证发布流水线 -ldflags -X 注入的版本优先于
// debug.ReadBuildInfo（后者对 `go build` 恒为 "(devel)"）。
func TestAboutVersionInjected(t *testing.T) {
	old := buildVersion
	t.Cleanup(func() { buildVersion = old })
	buildVersion = "0.2.1"
	if got := aboutVersion(); got != "0.2.1" {
		t.Errorf("应显示注入版本 0.2.1，实得 %q", got)
	}
}

// TestAboutHelpers 覆盖「关于」区两段只读值（对照生产 general_page 的「关于」group）：
// 版本非空（未注入时退回 ReadBuildInfo，本地构建显示「开发版」）；配置文件路径按 env 分支——
// 设绝对路径 → 原样显示；设相对路径（IsAbs=false）→ 落到「未落盘…」文案，与生产
// config_path_label 的「取不到路径就不显示半截」语义一致。
func TestAboutHelpers(t *testing.T) {
	if v := aboutVersion(); v == "" {
		t.Error("aboutVersion 不应为空串")
	}
	t.Setenv(configPathEnv, filepath.Join(string(filepath.Separator), "tmp", "voke", "config.toml"))
	if got := aboutConfigPath(); !filepath.IsAbs(got) {
		t.Errorf("设了绝对 env 应原样显示绝对路径，实得 %q", got)
	}
	t.Setenv(configPathEnv, "rel/config.toml")
	if got := aboutConfigPath(); filepath.IsAbs(got) || got == "rel/config.toml" {
		t.Errorf("非绝对路径应走「未落盘」文案，实得 %q", got)
	}
}

func TestThemeLabelCodeRoundTrip(t *testing.T) {
	for _, code := range themeCodes {
		label := themeLabel(appcfg.Theme(code))
		if got := themeCode(label); string(got) != code {
			t.Errorf("往返不符：code=%q → label=%q → code=%q", code, label, got)
		}
	}
	// 未知 / 空 → 跟随系统。
	if themeLabel("bogus") != themeLabels[0] {
		t.Errorf("未知码应回落 %q，拿到 %q", themeLabels[0], themeLabel("bogus"))
	}
	if got := themeCode("乱标签"); got != appcfg.ThemeSystem {
		t.Errorf("未知标签应回落 system，拿到 %q", got)
	}
}

func TestSeedSettings(t *testing.T) {
	st := &appState{
		cfg: appcfg.Loaded{Hotkey: "ctrl+space", Autostart: true, Theme: appcfg.ThemeDark},
		providers: config.Configs{Providers: []config.ProviderConfig{
			{ID: "apps", Name: "应用", Enabled: true, IsGlobal: true, Weight: 1.2},
			{ID: "base64", Name: "Base64", Enabled: false, Prefix: "#b", Activation: engine.Indirect, Weight: 1.0},
		}},
	}
	s := &settingsWindow{}
	st.seedSettings(s)

	if s.hotkey != "ctrl+space" {
		t.Errorf("hotkey 应取 st.cfg.Hotkey，拿到 %q", s.hotkey)
	}
	if !s.autostart {
		t.Errorf("autostart 应为 true")
	}
	if s.theme != themeLabel(appcfg.ThemeDark) {
		t.Errorf("theme 应映成深色标签 %q，拿到 %q", themeLabel(appcfg.ThemeDark), s.theme)
	}
	if len(s.edits) != 2 {
		t.Fatalf("应两行插件，拿到 %d", len(s.edits))
	}
	e0 := s.edits[0]
	if e0.id != "apps" || e0.name != "应用" || !e0.enabled || !e0.isGlobal || e0.weight != 1.2 {
		t.Errorf("apps 行错误：%+v", e0)
	}
	e1 := s.edits[1]
	if e1.id != "base64" || e1.enabled || e1.prefix != "#b" || e1.activation != "间接" {
		t.Errorf("base64 行应未启用/前缀#b/间接：%+v", e1)
	}
	// 中文显示值 ↔ 落码映射（save 靠 activationCode 回落 direct/indirect）。
	if activationCode("间接") != "indirect" || activationCode("直接") != "direct" {
		t.Errorf("activationCode 映射错：间接→%q 直接→%q", activationCode("间接"), activationCode("直接"))
	}
	if activationLabel(engine.Indirect) != "间接" || activationLabel(engine.Direct) != "直接" {
		t.Errorf("activationLabel 映射错")
	}
}

// TestSeedSettingsDefaultHotkey：st.cfg.Hotkey 空（无配置文件）→ 回落 hotkeyShow。
func TestSeedSettingsDefaultHotkey(t *testing.T) {
	st := &appState{}
	s := &settingsWindow{}
	st.seedSettings(s)
	if s.hotkey != hotkeyShow {
		t.Errorf("空配置应回落默认热键 %q，拿到 %q", hotkeyShow, s.hotkey)
	}
	if s.theme != themeLabels[0] {
		t.Errorf("空主题应回落跟随系统，拿到 %q", s.theme)
	}
}
