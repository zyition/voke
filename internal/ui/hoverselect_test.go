package ui

import (
	"testing"

	goui "github.com/egoist/mygo/ui"

	"voke/internal/logic/geometry"
	"voke/internal/providers"
)

// TestHoverSelectDiff 锁定差分语义：指针真的移动进某行才选中，停留不抢键盘，
// 同一行内的后续移动帧不重复改写。
func TestHoverSelectDiff(t *testing.T) {
	var h HoverSelect
	if row, ok := h.Select(2, true); !ok || row != 2 {
		t.Fatalf("指针移入第 2 行应返回选中 2，得到 row=%d changed=%v", row, ok)
	}
	if _, ok := h.Select(2, true); ok {
		t.Fatal("指针仍在同一行时不应重复改写选中")
	}
	if _, ok := h.Select(-1, true); ok {
		t.Fatal("指针离开列表不应改写选中")
	}
	if row, ok := h.Select(2, true); !ok || row != 2 {
		t.Fatalf("移出再进入第 2 行应重新选中，得到 row=%d changed=%v", row, ok)
	}
	if row, ok := h.Select(0, true); !ok || row != 0 {
		t.Fatalf("进入第 0 行也应选中，得到 row=%d changed=%v", row, ok)
	}
}

// TestHoverSelectIgnoresHoverWithoutPointerMove 守住用户要求：面板打开时指针
// 恰好停在某行、或键盘输入让静止指针下的行号变了，都不跟鼠标；只有指针真的
// 移动才跟随。
func TestHoverSelectIgnoresHoverWithoutPointerMove(t *testing.T) {
	var h HoverSelect
	// 打开面板：指针已在第 2 行，但这一帧指针没动。
	if _, ok := h.Select(2, false); ok {
		t.Fatal("面板刚打开、指针未移动时不应跟随 hover")
	}
	// 输入过滤把静止指针下的行换成第 0 行：仍不跟随。
	if _, ok := h.Select(0, false); ok {
		t.Fatal("键盘操作导致列表重排时不应跟随 hover")
	}
	// 指针真的移动：跟随到当前行。
	if row, ok := h.Select(1, true); !ok || row != 1 {
		t.Fatalf("指针移动后应跟随到第 1 行，得到 row=%d changed=%v", row, ok)
	}
	// 指针移动但落在同一行（悬停行号未变）：不重复改写。
	if _, ok := h.Select(1, true); ok {
		t.Fatal("指针在同一行内移动不应重复改写选中")
	}
}

// TestProviderRowsHoverSelects 用真实 Tester 验证接线：指针移到第三行后，
// 选中（快捷键/回车的作用目标）随之变成第三行；指针没动时（打开面板 /
// 键盘导航）hover 行不会抢走选中。
func TestProviderRowsHoverSelects(t *testing.T) {
	rows := []providers.RankedMatch{
		{Match: providers.Match{ProviderID: "clipboard", Title: "AA"}},
		{Match: providers.Match{ProviderID: "clipboard", Title: "BB"}},
		{Match: providers.Match{ProviderID: "clipboard", Title: "CC"}},
	}
	chosen := 0
	var ls goui.ListState
	var hs HoverSelect
	moved := false
	tt := goui.NewTester(func(c *goui.Context) {
		goui.Column(c).Fill().Children(func() {
			ProviderResults(c, rows, &chosen, &ls, &hs, moved, nil,
				func() {}, func(int) {}, func(int, string) {}, "")
		})
	}, geometry.PanelWidth, geometry.PanelHeight)
	tt.Frame()
	rect, ok := tt.Find("CC")
	if !ok {
		t.Fatal("第三行文字不可见，无法模拟 hover")
	}
	// 指针落在 CC 上但这一帧没有移动（等价于面板打开时指针就在该行）：
	// 选中保持默认的第 0 行。
	tt.Move(rect.X+rect.W/2, rect.Y+rect.H/2)
	tt.Frame()
	tt.Frame()
	if chosen != 0 {
		t.Fatalf("指针未移动时选中跳到 hover 行：得到 %d，期望 0", chosen)
	}

	// 指针真的移动 → 跟随到第三行。
	moved = true
	tt.Frame()
	moved = false
	tt.Frame()
	tt.Frame()
	if chosen != 2 {
		t.Fatalf("hover 第三行后选中行 = %d，期望 2", chosen)
	}

	// 指针停在 CC 上时，外部把选中改到 BB（方向键导航的等价结果）：
	// 后续帧不应把选中抢回 CC。
	chosen = 1
	tt.Frame()
	tt.Frame()
	if chosen != 1 {
		t.Fatalf("指针停留期间键盘选中被 hover 覆盖：得到 %d，期望 1", chosen)
	}
}
