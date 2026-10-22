package ui

import (
	"testing"

	goui "github.com/egoist/mygo/ui"

	"voke/internal/logic/geometry"
	"voke/internal/providers"
)

// TestHoverSelectDiff 锁定差分语义：进入某行才选中，停留不抢键盘，离开只清记忆。
func TestHoverSelectDiff(t *testing.T) {
	var h HoverSelect
	if row, ok := h.Select(2); !ok || row != 2 {
		t.Fatalf("首次进入第 2 行应返回选中 2，得到 row=%d changed=%v", row, ok)
	}
	if _, ok := h.Select(2); ok {
		t.Fatal("hover 行未变时不应改写选中（否则指针停留会每帧抢回方向键）")
	}
	if _, ok := h.Select(-1); ok {
		t.Fatal("指针离开列表不应改写选中")
	}
	if row, ok := h.Select(2); !ok || row != 2 {
		t.Fatalf("移出再进入第 2 行应重新选中，得到 row=%d changed=%v", row, ok)
	}
	if _, ok := h.Select(-1); ok {
		t.Fatal("指针再次离开不应改写选中")
	}
	if row, ok := h.Select(0); !ok || row != 0 {
		t.Fatalf("零值状态下进入第 0 行也应选中，得到 row=%d changed=%v", row, ok)
	}
}

// TestProviderRowsHoverSelects 用真实 Tester 验证接线：指针移到第三行后，
// 选中（快捷键/回车的作用目标）随之变成第三行；指针停留时应用层改选中
// （等价于方向键导航）不会被 hover 抢回去。
func TestProviderRowsHoverSelects(t *testing.T) {
	rows := []providers.RankedMatch{
		{Match: providers.Match{ProviderID: "clipboard", Title: "AA"}},
		{Match: providers.Match{ProviderID: "clipboard", Title: "BB"}},
		{Match: providers.Match{ProviderID: "clipboard", Title: "CC"}},
	}
	chosen := 0
	var ls goui.ListState
	var hs HoverSelect
	tt := goui.NewTester(func(c *goui.Context) {
		goui.Column(c).Fill().Children(func() {
			ProviderResults(c, rows, &chosen, &ls, &hs, nil,
				func() {}, func(int) {}, func(int, string) {}, "")
		})
	}, geometry.PanelWidth, geometry.PanelHeight)
	tt.Frame()
	rect, ok := tt.Find("CC")
	if !ok {
		t.Fatal("第三行文字不可见，无法模拟 hover")
	}
	tt.Move(rect.X+rect.W/2, rect.Y+rect.H/2)
	tt.Frame()
	tt.Frame()
	if chosen != 2 {
		t.Fatalf("hover 第三行后选中行 = %d，期望 2", chosen)
	}

	// 指针停在 CC 上时，外部把选中改到 BB（方向键导航的等价结果）：
	// hover 行没变化，后续帧不应把选中抢回 CC。
	chosen = 1
	tt.Frame()
	tt.Frame()
	if chosen != 1 {
		t.Fatalf("指针停留期间键盘选中被 hover 覆盖：得到 %d，期望 1", chosen)
	}
}
