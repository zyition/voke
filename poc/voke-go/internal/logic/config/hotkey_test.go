// hotkey 的测试：以 `voke-core/config.rs` 的 HotkeySpec 测试为 oracle——基础解析、全
// 修饰键 + 别名、非法输入拒绝、parse↔String 往返、默认热键 alt+space。
package config

import "testing"

func TestParseBasic(t *testing.T) {
	h, ok := ParseHotkey("ctrl+alt+space")
	if !ok || !h.Ctrl || !h.Alt || h.Shift || h.Win || h.Key != "space" {
		t.Errorf("ctrl+alt+space 解析错误：%+v ok=%v", h, ok)
	}
}

func TestParseAllModifiersAndAlias(t *testing.T) {
	h, ok := ParseHotkey("Win+Shift+Ctrl+a")
	if !ok || !h.Ctrl || !h.Shift || !h.Win || h.Alt || h.Key != "a" {
		t.Errorf("全修饰键解析错误：%+v ok=%v", h, ok)
	}
	h2, ok := ParseHotkey("super+control+b")
	if !ok || !h2.Win || !h2.Ctrl || h2.Key != "b" {
		t.Errorf("别名 super/control：%+v ok=%v", h2, ok)
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	for _, bad := range []string{"", "   ", "ctrl+alt", "ctrl+a+b"} {
		if _, ok := ParseHotkey(bad); ok {
			t.Errorf("%q 应解析失败", bad)
		}
	}
}

func TestParseStringRoundtrip(t *testing.T) {
	h, _ := ParseHotkey("ctrl+alt+space")
	if h.String() != "ctrl+alt+space" {
		t.Errorf("String = %q", h.String())
	}
	h2, ok := ParseHotkey(h.String())
	if !ok || h2 != h {
		t.Errorf("往返不等：%+v vs %+v", h2, h)
	}
}

func TestDefaultHotkeyIsAltSpace(t *testing.T) {
	h := DefaultHotkey()
	if !h.Alt || h.Ctrl || h.Shift || h.Win || h.Key != "space" {
		t.Errorf("默认热键应 alt+space：%+v", h)
	}
	if h.String() != "alt+space" {
		t.Errorf("默认热键 String = %q", h.String())
	}
	if parsed, _ := ParseHotkey("alt+space"); parsed != h {
		t.Errorf("Default 与解析 alt+space 应一致：%+v vs %+v", parsed, h)
	}
}
