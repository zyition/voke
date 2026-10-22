package app

import (
	"strings"
	"testing"

	"voke/internal/logic/config"
	"voke/internal/logic/engine"
)

// desiredPluginHotkeys 只收已启用且配置了 hotkey 的插件；停用的不注册，
// 规范化后重复的按键按注册顺序保留第一条、后者给出错误。
func TestDesiredPluginHotkeys(t *testing.T) {
	cfg := config.Configs{Providers: []config.ProviderConfig{
		{ID: "apps", Name: "应用", Enabled: true, Hotkey: "Alt+`"},
		{ID: "clipboard", Name: "剪贴板", Enabled: true, Hotkey: "alt+`"},
		{ID: "base64", Name: "Base64", Enabled: false, Hotkey: "Ctrl+Shift+B"},
		{ID: engine.PickerPluginID, Name: "插件列表", Enabled: true, Hotkey: "Ctrl+P"},
		{ID: "uuid", Name: "UUID", Enabled: true, Hotkey: ""},
	}}
	desired, errs := desiredPluginHotkeys(cfg)
	if len(desired) != 1 || desired["apps"] != "Alt+`" {
		t.Errorf("应只保留第一条有效热键，实得 %+v", desired)
	}
	if !strings.Contains(errs["clipboard"], "重复") || !strings.Contains(errs["clipboard"], "应用") {
		t.Errorf("重复键应指出冲突方，实得 %q", errs["clipboard"])
	}
	if errs[engine.PickerPluginID] == "" {
		t.Error("picker 配置热键应明确报不支持")
	}
	if _, ok := desired["base64"]; ok {
		t.Error("停用插件不应注册热键")
	}
}

// 设置页校验只比较启用的插件；停用插件的残留热键不阻塞保存，重新启用时会再校验。
func TestHotkeySettingsError(t *testing.T) {
	main := "Alt+Space"
	ok := []providerEdit{
		{id: "apps", name: "应用", enabled: true, hotkey: "Alt+`"},
		{id: "base64", name: "Base64", enabled: false, hotkey: "Alt+`"},
	}
	if msg := hotkeySettingsError(ok, main); msg != "" {
		t.Fatalf("停用插件的热键不参与冲突，实得 %q", msg)
	}

	dup := []providerEdit{
		{id: "apps", name: "应用", enabled: true, hotkey: "alt+`"},
		{id: "clipboard", name: "剪贴板", enabled: true, hotkey: "Alt+`"},
	}
	if msg := hotkeySettingsError(dup, "Ctrl+Space"); !strings.Contains(msg, "剪贴板") ||
		!strings.Contains(msg, "应用") {
		t.Errorf("插件间冲突应点名双方，实得 %q", msg)
	}

	withMain := []providerEdit{{id: "clipboard", name: "剪贴板", enabled: true, hotkey: "alt+space"}}
	if msg := hotkeySettingsError(withMain, "Alt+Space"); !strings.Contains(msg, "显示/隐藏热键") {
		t.Errorf("与主热键冲突应明确说明，实得 %q", msg)
	}

	bare := []providerEdit{{id: "clipboard", name: "剪贴板", enabled: true, hotkey: "a"}}
	if msg := hotkeySettingsError(bare, ""); !strings.Contains(msg, "修饰键") {
		t.Errorf("裸主键应被拒，实得 %q", msg)
	}
}
