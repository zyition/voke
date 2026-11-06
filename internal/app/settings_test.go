// settings.go 的 headless 可测面：主题标签↔码值往返 + seedSettings（从 st.cfg 顶层 +
// st.providers 刷绑定态）。窗口的 mygo 渲染 / 保存落盘只在真机验（见 settings.go 头），
// 但这两段纯逻辑不碰 mygo，Linux 原生即可自证。
package app

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"voke/internal/i18n"
	"voke/internal/logic/appcfg"
	"voke/internal/logic/config"
	"voke/internal/logic/engine"
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
	if themeLabel("bogus") != themeLabels()[0] {
		t.Errorf("未知码应回落 %q，拿到 %q", themeLabels()[0], themeLabel("bogus"))
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
			{ID: "base64", Name: "Base64", Enabled: false, Prefix: "@b", Activation: engine.Indirect, Weight: 1.0, Hotkey: "Ctrl+Shift+B"},
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
	if e1.id != "base64" || e1.enabled || e1.prefix != "@b" || e1.activation != "间接" ||
		e1.hotkey != "Ctrl+Shift+B" {
		t.Errorf("base64 行应未启用/前缀@b/间接/热键 Ctrl+Shift+B：%+v", e1)
	}
	// 中文显示值 ↔ 落码映射（save 靠 activationCode 回落 direct/indirect）。
	if activationCode("间接") != "indirect" || activationCode("直接") != "direct" {
		t.Errorf("activationCode 映射错：间接→%q 直接→%q", activationCode("间接"), activationCode("直接"))
	}
	if activationLabel(engine.Indirect) != "间接" || activationLabel(engine.Direct) != "直接" {
		t.Errorf("activationLabel 映射错")
	}
}

// TestPrefixSettingsError 锁设置页保存前的校验：合法的默认预设通过；触发串互为前缀、
// 含空白、撞保留键都在落盘前被拦下，消息指名道姓。
func TestPrefixSettingsError(t *testing.T) {
	picker := providerEdit{id: engine.PickerPluginID, name: "插件列表", prefix: "#", activation: "直接"}
	base64 := providerEdit{id: "base64", name: "Base64", prefix: "@b", activation: "间接"}
	valid := []providerEdit{picker, base64}
	if msg := prefixSettingsError(valid, providerOverrides(valid)); msg != "" {
		t.Fatalf("合法前缀不该报错，实得 %q", msg)
	}

	conflict := []providerEdit{picker, {id: "base64", name: "Base64", prefix: "#b", activation: "间接"}}
	msg := prefixSettingsError(conflict, providerOverrides(conflict))
	if !strings.Contains(msg, "前缀冲突") || !strings.Contains(msg, "base64") || !strings.Contains(msg, "__picker") {
		t.Errorf("冲突消息应点名两个插件，实得 %q", msg)
	}

	space := []providerEdit{{id: "base64", name: "Base64", prefix: "b 6", activation: "间接"}}
	if msg := prefixSettingsError(space, providerOverrides(space)); !strings.Contains(msg, "不能包含空白") {
		t.Errorf("含空白前缀应被拦下，实得 %q", msg)
	}

	reserved := []providerEdit{{id: "base64", name: "Base64", prefix: "b", activation: "间接"}}
	if msg := prefixSettingsError(reserved, providerOverrides(reserved)); !strings.Contains(msg, "保留键冲突") {
		t.Errorf("字母前缀应被 C4 拦下，实得 %q", msg)
	}
}

// TestCharsetSettingsError 锁随机值字符集的保存前校验：空列表、空名称、重名、
// 坏正则都在落盘前拦下；大小写不同的名称是合法的两套。
func TestCharsetSettingsError(t *testing.T) {
	valid := []providerEdit{{id: "random", defaultLength: 16, charsets: []charsetEdit{
		{name: "hex", pattern: "a-f", enabled: true},
		{name: "Hex", pattern: "A-F", enabled: true},
	}}}
	if msg := charsetSettingsError(valid); msg != "" {
		t.Fatalf("合法字符集不应报错：%q", msg)
	}

	cases := []struct {
		name string
		edit providerEdit
		want string
	}{
		{"空列表", providerEdit{id: "random", defaultLength: 16}, "至少需要保留一个"},
		{"默认长度越界", providerEdit{id: "random", defaultLength: 0, charsets: []charsetEdit{{name: "hex", pattern: "a-f"}}}, "默认长度"},
		{"空名称", providerEdit{id: "random", defaultLength: 16, charsets: []charsetEdit{{pattern: "a-z"}}}, "名称不能为空"},
		{"重名", providerEdit{id: "random", defaultLength: 16, charsets: []charsetEdit{
			{name: "hex", pattern: "a-f"}, {name: "hex", pattern: "A-F"},
		}}, "重复"},
		{"坏正则", providerEdit{id: "random", defaultLength: 16, charsets: []charsetEdit{{name: "bad", pattern: "z-a"}}}, "正则无效"},
		{"字符集长度越界", providerEdit{id: "random", defaultLength: 16, charsets: []charsetEdit{{name: "bad", pattern: "a-f", length: 999}}}, "长度必须"},
	}
	for _, tc := range cases {
		if msg := charsetSettingsError([]providerEdit{tc.edit}); !strings.Contains(msg, tc.want) {
			t.Errorf("%s：错误应包含 %q，实得 %q", tc.name, tc.want, msg)
		}
	}
}

// TestProviderOverridesIncludesRandomCharsets：设置页保存时字符集会整份写回（名称 /
// 正则裁剪首尾空白），非随机插件不受影响。
func TestProviderOverridesIncludesRandomCharsets(t *testing.T) {
	edits := []providerEdit{
		{id: "random", enabled: true, activation: "直接", defaultLength: 24, charsets: []charsetEdit{
			{name: " hex ", pattern: " a-f ", length: 8, enabled: false},
			{name: "跟随", pattern: "A-F", length: 0, enabled: true},
		}},
		{id: "base64", enabled: true, activation: "间接"},
	}
	byID := map[string]config.UserOverride{}
	for _, ov := range providerOverrides(edits) {
		byID[ov.ID] = ov
	}
	got := byID["random"].Charsets
	if len(got) != 2 || got[0].Name != "hex" || got[0].Pattern != "a-f" ||
		got[0].Length != 8 || got[0].Enabled {
		t.Errorf("随机值字符集应裁剪后写回：%+v", got)
	}
	if got[1].Length != 0 {
		t.Errorf("长度 0 应原样写回（跟随全局）：%+v", got[1])
	}
	if l := byID["random"].DefaultLength; l == nil || *l != 24 {
		t.Errorf("随机值默认长度应写回：%v", l)
	}
	if byID["base64"].Charsets != nil {
		t.Errorf("非随机插件不应写 charsets：%+v", byID["base64"].Charsets)
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
	if s.theme != themeLabels()[0] {
		t.Errorf("空主题应回落跟随系统，拿到 %q", s.theme)
	}
}

// TestLanguageLabelCodeRoundTrip 锁语言下拉标签↔配置码值往返（当前语言下）。
func TestLanguageLabelCodeRoundTrip(t *testing.T) {
	for _, code := range languageCodes {
		label := languageLabel(code)
		if got := languageCode(label); got != code {
			t.Errorf("往返不符：code=%q → label=%q → code=%q", code, label, got)
		}
	}
	if languageLabel("bogus") != languageLabels()[0] {
		t.Errorf("未知码应回落跟随系统")
	}
}

// TestResolveLanguage 锁语言偏好解析：显式 zh/en 直接用；system 交给系统语言。
func TestResolveLanguage(t *testing.T) {
	if got := resolveLanguage(appcfg.LanguageChinese); got != i18n.Chinese {
		t.Errorf("zh 应解析成中文，实得 %q", got)
	}
	if got := resolveLanguage(appcfg.LanguageEnglish); got != i18n.English {
		t.Errorf("en 应解析成英文，实得 %q", got)
	}
	if got := resolveLanguage(appcfg.LanguageSystem); got != i18n.Chinese {
		// 非 Windows 的 SystemLanguage 占位恒为中文。
		t.Errorf("非 Windows 下 system 应回落中文，实得 %q", got)
	}
}

// TestProviderOverridesKeepsClearedDefaultHotkey：剪贴板有默认 Alt+`，用户清除后必须
// 显式写空串覆盖默认值，不能省略（否则 Normalize 会回落到默认热键、清不掉）。
func TestProviderOverridesKeepsClearedDefaultHotkey(t *testing.T) {
	edits := []providerEdit{
		{id: "clipboard", name: "剪贴板", enabled: true, activation: "直接"},
		{id: "base64", name: "Base64", enabled: true, activation: "间接"},
	}
	ovs := providerOverrides(edits)
	byID := map[string]config.UserOverride{}
	for _, ov := range ovs {
		byID[ov.ID] = ov
	}
	clip := byID["clipboard"]
	if clip.Hotkey == nil || *clip.Hotkey != "" {
		t.Errorf("剪贴板清除热键后应写空串覆盖默认值，实得 %+v", clip.Hotkey)
	}
	if base := byID["base64"]; base.Hotkey != nil {
		t.Errorf("无默认热键的插件清空后应省略该键，实得 %+v", base.Hotkey)
	}
}

// TestSettingsViewRendersSidebarAndPages 用 mygo 的 headless Tester 跑真实设置视图：
// 首屏出「通用」页与语言选项，点左侧「插件」后切到插件卡片，验证新布局不 panic、
// 导航确实生效。只渲染不点击会落盘的控件，故不需要窗口/平台副作用。
func TestSettingsViewRendersSidebarAndPages(t *testing.T) {
	cfg, _, err := buildPrefixTable()
	if err != nil {
		t.Fatalf("buildPrefixTable 失败：%v", err)
	}
	st := &appState{
		cfg:              appcfg.Loaded{Hotkey: hotkeyShow, Theme: appcfg.ThemeSystem},
		providers:        cfg,
		pluginHotkeyErrs: map[string]string{},
	}
	s := &settingsWindow{section: settingsSectionGeneral, pluginsOpen: true}
	st.settings = s
	st.seedSettings(s)

	tt := ui.NewTester(st.settingsView, 880, 600)
	tt.Frame()
	for _, want := range []string{"通用", "界面语言", "跟随系统", "开机自启", "Base64"} {
		if !tt.HasText(want) {
			t.Errorf("通用页应包含文案 %q", want)
		}
	}
	if err := tt.Click("快捷键"); err != nil {
		t.Fatalf("点击左侧「快捷键」失败：%v", err)
	}
	tt.Frame()
	if !tt.HasText("全局热键") {
		t.Errorf("切到快捷键页后应显示全局热键")
	}
}

// TestSettingsSidebarPluginItemJumps：左侧插件条目点击后进入该插件的设置页，
// 内容从顶部开始（不再有「全部插件」总览页）。
func TestSettingsSidebarPluginItemJumps(t *testing.T) {
	cfg, _, err := buildPrefixTable()
	if err != nil {
		t.Fatalf("buildPrefixTable 失败：%v", err)
	}
	st := &appState{
		cfg:              appcfg.Loaded{Hotkey: hotkeyShow, Theme: appcfg.ThemeSystem},
		providers:        cfg,
		pluginHotkeyErrs: map[string]string{},
	}
	s := &settingsWindow{section: settingsSectionGeneral, pluginsOpen: true}
	st.settings = s
	st.seedSettings(s)

	tt := ui.NewTester(st.settingsView, 880, 600)
	tt.Frame()
	// 通用页上只有侧栏出现 Base64，点击不会落到插件卡片的同名标题。
	if err := tt.Click("Base64"); err != nil {
		t.Fatalf("点击左侧 Base64 失败：%v", err)
	}
	tt.Frame()
	if s.section != "plugin:base64" {
		t.Errorf("选中项 = %q，想要 plugin:base64", s.section)
	}
	if !tt.HasText("base64 编解码（加密 / 解密）") || !tt.HasText("参与全局搜索") {
		t.Errorf("应显示 Base64 插件卡片且从顶部开始")
	}
}

// TestSettingsRandomCharsetEditorRenders：随机值插件卡片应有字符集编辑器（名称 / 正则 /
// 添加按钮），且默认三套预设从配置 seed 进来。
func TestSettingsRandomCharsetEditorRenders(t *testing.T) {
	cfg, _, err := buildPrefixTable()
	if err != nil {
		t.Fatalf("buildPrefixTable 失败：%v", err)
	}
	st := &appState{
		cfg:              appcfg.Loaded{Hotkey: hotkeyShow, Theme: appcfg.ThemeSystem},
		providers:        cfg,
		pluginHotkeyErrs: map[string]string{},
	}
	s := &settingsWindow{section: settingsSectionGeneral, pluginsOpen: true}
	st.settings = s
	st.seedSettings(s)

	tt := ui.NewTester(st.settingsView, 880, 600)
	tt.Frame()
	if err := tt.Click("随机值"); err != nil {
		t.Fatalf("点击左侧随机值失败：%v", err)
	}
	tt.Frame()
	tt.Frame()
	for _, want := range []string{"字符集", "添加字符集"} {
		if !tt.HasText(want) {
			t.Errorf("随机值卡片应显示 %q", want)
		}
	}
	idx := providerEditIndex(s.edits, "random")
	if idx < 0 || len(s.edits[idx].charsets) != 3 {
		t.Fatalf("随机值应 seed 三套默认字符集：%+v", s.edits)
	}
	if s.edits[idx].defaultLength != 16 {
		t.Errorf("默认长度应 seed 为 16：%v", s.edits[idx].defaultLength)
	}
	if s.edits[idx].charsets[2].name != "十六进制" || s.edits[idx].charsets[2].length != 32 {
		t.Errorf("十六进制预设应为 32 位：%+v", s.edits[idx].charsets)
	}
}

// TestSettingsAboutShowsProjectURL：关于页展示项目地址，且是可点击链接。
func TestSettingsAboutShowsProjectURL(t *testing.T) {
	cfg, _, err := buildPrefixTable()
	if err != nil {
		t.Fatalf("buildPrefixTable 失败：%v", err)
	}
	st := &appState{
		cfg:              appcfg.Loaded{Hotkey: hotkeyShow, Theme: appcfg.ThemeSystem},
		providers:        cfg,
		pluginHotkeyErrs: map[string]string{},
	}
	s := &settingsWindow{section: settingsSectionGeneral, pluginsOpen: true}
	st.settings = s
	st.seedSettings(s)

	tt := ui.NewTester(st.settingsView, 880, 600)
	tt.Frame()
	// 插件列表默认展开时「关于」在侧栏可视区外；先折叠插件分组再点。
	if err := tt.Click("插件"); err != nil {
		t.Fatalf("折叠插件分组失败：%v", err)
	}
	tt.Frame()
	if tt.HasText("Base64") {
		t.Errorf("折叠后不应再显示插件子项")
	}
	if err := tt.Click("关于"); err != nil {
		t.Fatalf("点击左侧关于失败：%v", err)
	}
	tt.Frame()
	if !tt.HasText(projectURL) {
		t.Errorf("关于页应显示项目地址 %q", projectURL)
	}
	if err := tt.Click(projectURL); err != nil {
		t.Errorf("项目地址应可点击：%v", err)
	}
}
