package config

import (
	"testing"

	"github.com/zyition/voke/poc/voke-go/internal/logic/engine"
)

func hotkeyTestDescriptors() []Descriptor {
	return []Descriptor{{
		ID: "clipboard", Name: "剪贴板", DefaultEnabled: true,
		DefaultActivation: engine.Indirect, DefaultPrefix: "@c",
		DefaultWeight: 1, DefaultCount: 5,
	}}
}

func TestNormalizePluginHotkey(t *testing.T) {
	hk := "Alt+`"
	cfg, err := Normalize(hotkeyTestDescriptors(), []UserOverride{{ID: "clipboard", Hotkey: &hk}})
	if err != nil {
		t.Fatalf("合法插件热键不该报错：%v", err)
	}
	if got := cfg.Providers[0].Hotkey; got != "Alt+`" {
		t.Errorf("热键应原样下发，实得 %q", got)
	}
}

func TestNormalizePluginHotkeyDefaultsEmpty(t *testing.T) {
	cfg, err := Normalize(hotkeyTestDescriptors(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Providers[0].Hotkey != "" {
		t.Errorf("没有默认热键，实得 %q", cfg.Providers[0].Hotkey)
	}
}

// C8：裸主键 / 不可解析 / 只有修饰键都置空并给出警告，不让全局热键劫持正常输入。
func TestNormalizePluginHotkeyInvalidCleared(t *testing.T) {
	for _, raw := range []string{"a", "Alt+", "Alt+Control", "   "} {
		hk := raw
		cfg, err := Normalize(hotkeyTestDescriptors(), []UserOverride{{ID: "clipboard", Hotkey: &hk}})
		if err != nil {
			t.Fatalf("%q 不应让归一失败：%v", raw, err)
		}
		if cfg.Providers[0].Hotkey != "" {
			t.Errorf("%q 应被 C8 置空，实得 %q", raw, cfg.Providers[0].Hotkey)
		}
		if raw != "   " && len(cfg.Warnings) == 0 {
			t.Errorf("%q 应留下一条警告", raw)
		}
	}
}

func TestCanonicalHotkey(t *testing.T) {
	same := []string{"Alt+`", "alt+`", "ALT+`", " alt + ` "}
	want := CanonicalHotkey(same[0])
	for _, s := range same[1:] {
		if got := CanonicalHotkey(s); got != want {
			t.Errorf("%q 规范化应等于 %q，实得 %q", s, want, got)
		}
	}
	if CanonicalHotkey("Super+X") != CanonicalHotkey("win+x") {
		t.Error("win / super 应视为同一修饰键")
	}
	if HasModifier("a") || !HasModifier("Ctrl+a") || !HasModifier("Alt+`") {
		t.Error("HasModifier 判定错误")
	}
}
