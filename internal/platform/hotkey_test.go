package platform

import (
	"testing"

	goui "github.com/egoist/mygo/ui"
)

// TestAccelFromKey 钉死「按键+修饰键 → 加速器串」的转换（录制器用）：修饰键按
// Ctrl/Alt/Shift/Super 固定顺序拼接、键名小写；无修饰键或不支持的键一律拒绝。
func TestAccelFromKey(t *testing.T) {
	cases := []struct {
		name   string
		key    goui.Key
		mods   goui.Modifiers
		want   string
		wantOK bool
	}{
		{"Ctrl+Alt+Space", goui.KeySpace, goui.Ctrl | goui.Alt, "Ctrl+Alt+Space", true},
		{"Ctrl+A", goui.KeyA, goui.Ctrl, "Ctrl+A", true},
		{"Alt+Shift+F5 顺序固定", goui.KeyF5, goui.Shift | goui.Alt, "Alt+Shift+F5", true},
		{"带 Super", goui.KeyQ, goui.Super | goui.Ctrl, "Ctrl+Super+Q", true},
		{"等号键", goui.KeyEqual, goui.Ctrl, "Ctrl+=", true},
		{"无修饰键拒绝", goui.KeyA, 0, "", false},
		{"不支持的键拒绝", goui.KeyUnknown, goui.Ctrl, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := AccelFromKey(c.key, c.mods)
			if ok != c.wantOK || got != c.want {
				t.Errorf("AccelFromKey(%v,%v) = %q,%v，想要 %q,%v", c.key, c.mods, got, ok, c.want, c.wantOK)
			}
		})
	}
}
