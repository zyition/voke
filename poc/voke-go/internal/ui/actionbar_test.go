package ui

import (
	"testing"

	goui "github.com/egoist/mygo/ui"

	"github.com/zyition/voke/poc/voke-go/internal/providers"
	"github.com/zyition/voke/poc/voke-go/internal/ui/theme"
)

// TestActionBarClicksDoNotTriggerRow 锁定两件事：
//   - action icon 可点且回调到 onAction（Label 也供辅助技术/测试定位）；
//   - 点 icon 不冒泡触发外层的默认行动作。
func TestActionBarClicksDoNotTriggerRow(t *testing.T) {
	actions := []providers.RowAction{
		{ID: "paste", Label: "粘贴", Tooltip: "粘贴  Enter"},
		{ID: "pin", Label: "置顶", Tooltip: "置顶  Ctrl+Shift+P"},
	}
	rowClicks, action := 0, ""
	tt := goui.NewTester(func(c *goui.Context) {
		goui.Column(c).Fill().Children(func() {
			btn := goui.ButtonBase(c).Height(theme.RowSlotH).Grow(1)
			if btn.Clicked() {
				rowClicks++
			}
			btn.Children(func() {
				goui.Row(c).Grow(1).Children(func() {
					goui.Spacer(c)
					ActionBar(c, actions, len(actions), true, func(id string) { action = id })
				})
			})
		})
	}, 320, 120)
	tt.Frame()
	if err := tt.Click("置顶"); err != nil {
		t.Fatalf("点击 action icon 失败：%v", err)
	}
	tt.Frame()
	if action != "pin" {
		t.Errorf("action=%q，期望 pin", action)
	}
	if rowClicks != 0 {
		t.Errorf("点 action icon 触发了外层行点击 %d 次", rowClicks)
	}
}
