// 列表行视觉的**像素级**回归测试（2026-10-16 真机反馈第二轮）。
//
// ## 为什么必须扫真实渲染、且必须调生产代码
//
// 本轮的核心结论「边框来自我们调的 ui.Button 的 styleButton，而不是
// ui.List 默认行为」就是靠渲染对比拿到的——ui.List 从头到尾没画过描边，
// 光看 API 签名根本看不出来。三条纪律：
//
//  1. **扫像素**，不扫代码字符串。断言「源码里没有 Border(」是典型的空转
//     断言：换成 ButtonBase 之后再手工加回 .Border(1, t.Border) 它照样绿。
//  2. **调生产代码 Row()**（internal/ui），不在测试里抄一份行渲染。第一版测试照抄了
//     Row() 的结构，结果变异测试（把生产 Row() 改回 ui.Button）**照样绿**
//     ——测试测的是测试自己那份副本。改成直接调 Row() 后同一变异立刻变红。
//  3. **限定扫描区间**：面板 chrome 自身有两条 `ui.Divider`（输入行下方
//     y=56、列表下方），颜色正是 t.Border；全列扫描会把它们误判成「行的
//     边框」。只扫行槽区（列表区内部）才是在测行。
//
// Tester 默认浅色主题（SetDark 才切深色，headless.go:387），本文件按浅色
// 主题比对；深色走同一套逻辑只是颜色不同，不重复断言。
//
// §7.2 步骤7b：本用例随渲染层（Row / Icon / AppsResults）一起从 package main
// 搬进 internal/ui——它直测的就是本包的未导出/导出渲染件，留在 main 反而隔了一层。
// 断言与像素阈值（minGap / minInsetY / minInsetX / listAreaTop=57 等绝对值）原样搬、未削弱。
package ui

import (
	"image"
	"testing"

	goui "github.com/egoist/mygo/ui"

	"github.com/zyition/voke/poc/voke-go/internal/logic/geometry"
	"github.com/zyition/voke/poc/voke-go/internal/logic/panelstate"
	"github.com/zyition/voke/poc/voke-go/internal/logic/testutil"
	"github.com/zyition/voke/poc/voke-go/internal/ui/theme"
)

// 面板 chrome 的实测纵向布局（探针反读，见 §11.11）：输入行 y 0..55
// （高 56）+ 分隔线 y=56 → 列表区从 y=57 起；列表自带 Padding(6)
// → 第 i 行的行槽顶 = 57 + 6 + i*theme.RowSlotH。
//
// 引 theme 常量的只有**「构造被测布局所需的那个尺寸」**（行槽顶 / 中心 /
// 扫描区下界）；所有**断言下限**仍是硬数字（minGap / minInsetY / minInsetX），
// 绝不用 theme.RowGap 之类反推——那是 §10.1.1 陷阱 2 点名的同义反复
// （把 RowGap 变异成 0 时阈值会跟着一起失效，测试照样绿，已实测漏过 2 条）。
const (
	listAreaTop = 57
	listPadY    = 6
	listPadX    = 6                      // 列表左右 padding，即行槽左右缘
	row0SlotTop = listAreaTop + listPadY // = 63
	row0CenterY = row0SlotTop + theme.RowSlotH/2
	rowPillX    = 200 // 药丸内部扫描列（避开文字与圆角）
	rowCount    = 4   // 测试用 4 行

	// listRowsTop/Bottom 框住**行槽区**，刻意排除面板自身的两条 Divider。
	listRowsTop    = row0SlotTop
	listRowsBottom = row0SlotTop + rowCount*theme.RowSlotH
)

// rowKind 是扫描像素的归类结果。
type rowKind int

const (
	kindOther  rowKind = iota
	kindBg             // 面板底色
	kindBorder         // 主题描边色（用户看到的「框」）
	kindHover          // 主题 hover 色
)

func (k rowKind) String() string {
	switch k {
	case kindBg:
		return "面板底色"
	case kindBorder:
		return "描边色"
	case kindHover:
		return "hover色"
	default:
		return "其他"
	}
}

// rowSpan 是扫描线上的一段连续同色像素。
type rowSpan struct {
	kind     rowKind
	from, to int
}

// pxAt 取像素的三个 8 位通道。
func pxAt(img *image.RGBA, x, y int) (r, g, b uint8) {
	c := img.RGBAAt(x, y)
	return c.R, c.G, c.B
}

// scanColumnY 扫一列的 [y0, y1) 区间，把连续同色段合并返回。
func scanColumnY(img *image.RGBA, x, y0, y1 int, th *goui.Theme) []rowSpan {
	b := img.Bounds()
	if y1 > b.Dy() {
		y1 = b.Dy()
	}
	var out []rowSpan
	var cur rowSpan
	started := false
	for y := y0; y < y1; y++ {
		r, g, bb := pxAt(img, x, y)
		kind := kindOther
		switch {
		case r == th.Border.R && g == th.Border.G && bb == th.Border.B:
			kind = kindBorder
		case r == th.Background.R && g == th.Background.G && bb == th.Background.B:
			kind = kindBg
		case r == th.SurfaceHover.R && g == th.SurfaceHover.G && bb == th.SurfaceHover.B:
			kind = kindHover
		}
		if started && cur.kind == kind {
			cur.to = y
			continue
		}
		if started {
			out = append(out, cur)
		}
		cur, started = rowSpan{kind: kind, from: y, to: y}, true
	}
	if started {
		out = append(out, cur)
	}
	return out
}

// scanRowX 扫一行，返回所有「非底色」段的左右边界（用于测水平内缩）。
func scanRowX(img *image.RGBA, y int, th *goui.Theme) []rowSpan {
	b := img.Bounds()
	var out []rowSpan
	var cur rowSpan
	started := false
	for x := 0; x < b.Dx(); x++ {
		r, g, bb := pxAt(img, x, y)
		hit := !(r == th.Background.R && g == th.Background.G && bb == th.Background.B)
		switch {
		case hit && !started:
			cur, started = rowSpan{from: x, to: x}, true
		case hit:
			cur.to = x
		case started:
			out = append(out, cur)
			started = false
		}
	}
	if started {
		out = append(out, cur)
	}
	return out
}

// marks 过滤掉底色段，只留「画了东西」的段。
func marks(spans []rowSpan) []rowSpan {
	var out []rowSpan
	for _, s := range spans {
		if s.kind != kindBg {
			out = append(out, s)
		}
	}
	return out
}

// newRealRowTester 造一个**调用生产 Row()** 的 Tester：面板 chrome 与
// internal/ui/view.go 的 View 同款（输入行 + 两条 Divider + 虚拟化列表）。
//
// chosen 指定选中行；预置图标缓存为 nil，绕开 platform.AppIcon → Shell.FileIcon
// → Screen.Displays()（后两者走 onMainValue，在无主循环的 Tester 里会永久阻塞）。
func newRealRowTester(t *testing.T, chosen int) (*goui.Tester, *goui.Theme) {
	t.Helper()
	m := panelstate.Model{Results: testutil.MkApps("AA", "BB", "CC", "DD"), Chosen: chosen}
	icons := map[string]*goui.Bitmap{}
	for _, a := range m.Results {
		icons[a.Path] = nil
	}
	var ls goui.ListState
	// renderedTheme 而非 theme：包名 theme（字号/行视觉常量的单一事实来源）
	// 在本文件被 import，局部变量再叫 theme 会把包名遮蔽掉。
	var renderedTheme *goui.Theme
	tt := goui.NewTester(func(c *goui.Context) {
		th := c.Theme()
		renderedTheme = th
		goui.Column(c).Fill().Children(func() {
			goui.Row(c).Padding(16, 18).Gap(12).AlignItems(goui.Center).Children(func() {
				goui.TextInputBase(c, new(string)).Grow(1).FontSize(theme.InputSize)
			})
			goui.Divider(c)
			ls.Selected = &m.Chosen
			goui.List(c, &ls, len(m.Results), func(i int) {
				Row(c, &m, &icons, i, func(int) {}) // ← 生产代码
			}).Grow(1).Padding(6)
			goui.Divider(c)
		})
	}, geometry.PanelWidth, geometry.PanelHeight)
	tt.Frame()
	tt.Frame()
	if renderedTheme == nil {
		t.Fatal("渲染回调没跑起来，拿不到主题")
	}
	return tt, renderedTheme
}

// hoverRowY 给出行 i 的中心 y，供 tt.Move 触发真实 hover。
func hoverRowY(i int) float32 {
	return float32(row0SlotTop + i*theme.RowSlotH + theme.RowSlotH/2)
}

// TestRowHasNoBorderPixels 是本轮最关键的一条：列表行**不得**画出任何描边
// 像素。扫描三列：药丸左外侧（行槽内、药丸外）、药丸中央、药丸右外侧。
// 旧实现（ui.Button）会在行槽左右缘各画出一条 t.Border 竖线，这里必然抓到。
//
// 同时覆盖静止态与真实 hover 态（用 tt.Move 移指针，靠 mygo 的 setHover
// 驱动重绘，input.go:154-179），因为 hover 高亮是本轮新加的路径。
func TestRowHasNoBorderPixels(t *testing.T) {
	for _, tc := range []struct {
		name   string
		chosen int
		hover  int
	}{
		{"静止", 0, -1},
		{"hover 第2行", 0, 2},
		{"hover 第1行(紧邻选中行)", 0, 1},
	} {
		tt, th := newRealRowTester(t, tc.chosen)
		if tc.hover >= 0 {
			tt.Move(rowPillX, hoverRowY(tc.hover))
			tt.Frame()
			tt.Frame()
		}
		img := tt.Image()
		for _, x := range []int{listPadX + 2, rowPillX, geometry.PanelWidth - listPadX - 2} {
			for _, s := range scanColumnY(img, x, listRowsTop, listRowsBottom, th) {
				if s.kind == kindBorder {
					t.Errorf("%s：x=%d 出现描边像素 y=%d..%d，行不该有框"+
						"（多半是 Row() 又用回了 ui.Button，其 styleButton 无条件"+
						" Border(1, t.Border)，widgets.go:168-170）",
						tc.name, x, s.from, s.to)
				}
			}
		}
	}
}

// TestRowHoverPillAppears 确认 hover 高亮**真的会亮**——否则「去框」就变成
// 「什么反馈都没有」，比原来更糟。用真实指针移动触发，不伪造状态。
func TestRowHoverPillAppears(t *testing.T) {
	tt, th := newRealRowTester(t, 0)
	// 先确认未 hover 时第 2 行没有 hover 色。
	tt.Move(rowPillX, hoverRowY(2))
	tt.Frame()
	tt.Frame()
	img := tt.Image()
	found := false
	for _, s := range scanColumnY(img, rowPillX, listRowsTop, listRowsBottom, th) {
		if s.kind == kindHover {
			found = true
			break
		}
	}
	if !found {
		t.Error("指针移到第 2 行却没有出现 hover 高亮：去框后若没有任何反馈，" +
			"用户将无从判断指针落在哪一行")
	}
	// 把指针移回输入框，hover 色应消失。
	tt.Move(rowPillX, 10)
	tt.Frame()
	tt.Frame()
	img = tt.Image()
	for _, s := range scanColumnY(img, rowPillX, listRowsTop, listRowsBottom, th) {
		if s.kind == kindHover {
			t.Errorf("指针已移回输入框，第 2 行仍有 hover 高亮 y=%d..%d", s.from, s.to)
		}
	}
}

// TestRowPillGapBetweenRows 锁定「行间必须留垂直间隙、绝不允许框首尾相连」
// 这条用户硬要求：让 hover 行紧邻选中行，断言两个高亮块之间夹着底色像素，
// 且空隙 ≥ rowGap。
func TestRowPillGapBetweenRows(t *testing.T) {
	tt, th := newRealRowTester(t, 0) // 0 选中
	tt.Move(rowPillX, hoverRowY(1))  // 1 hover —— 正好在选中行下方
	tt.Frame()
	tt.Frame()
	img := tt.Image()
	all := scanColumnY(img, rowPillX, listRowsTop, listRowsBottom, th)
	mm := marks(all)
	if len(mm) < 2 {
		t.Fatalf("期望看到 2 个高亮块（0 选中 + 1 hover），实际 %d 个：%v", len(mm), all)
	}
	first, second := mm[0], mm[1]
	gap := second.from - first.to - 1
	// 阈值用**绝对值** 2 DIP，不写 `gap < rowGap`：后者是同义反复——
	// 变异测试把 rowGap 改成 0 时断言会跟着失效，测试照样绿（已实测）。
	// 这里要锁的是「行间必须有可见留白」这条用户硬要求本身。
	const minGap = 2
	if gap < minGap {
		t.Errorf("相邻两行高亮块之间只有 %d DIP 空隙（y=%d..%d 与 y=%d..%d），"+
			"少于下限 %d DIP：用户明确要求行间留白，绝不允许框连成一片"+
			"（当前 RowGap=%d）", gap, first.from, first.to, second.from, second.to,
			minGap, theme.RowGap)
	}
}

// TestRowPillHeight 锁定药丸高度 = 行槽 − rowGap（上下各留 rowGap/2）。
// 这是「行间不相接」的量化版本。
func TestRowPillHeight(t *testing.T) {
	tt, th := newRealRowTester(t, 0)
	img := tt.Image()
	mm := marks(scanColumnY(img, rowPillX, listRowsTop, listRowsBottom, th))
	if len(mm) == 0 {
		t.Fatal("没找到选中行的高亮像素")
	}
	got := mm[0].to - mm[0].from + 1
	// 同样是绝对阈值：药丸必须**明显矮于**行槽，否则上下留白被吃掉、
	// 相邻两行的高亮块就会贴在一起。写成 `got != rowSlotH-rowGap` 是同义反复
	//（变异 rowGap 时断言自动跟随），已实测会被绕过。
	const minInsetY = 2
	if slack := theme.RowSlotH - got; slack < minInsetY {
		t.Errorf("药丸高 %d DIP，距行槽 %d 只剩 %d DIP 上下留白（要求 ≥ %d）："+
			"留白被吃掉相邻行就会连成一片（RowSlotH=%d，RowGap=%d）",
			got, theme.RowSlotH, slack, minInsetY, theme.RowSlotH, theme.RowGap)
	}
}

// TestRowPillInsetHorizontally 锁定药丸左右各缩进 rowInsetX：
// 高亮块不该贴着行槽边缘（用户要「不拉满」的观感）。
func TestRowPillInsetHorizontally(t *testing.T) {
	tt, th := newRealRowTester(t, 0)
	img := tt.Image()
	// 取药丸垂直正中偏上 4 DIP，避开标题/副行文字。
	segs := scanRowX(img, row0CenterY-4, th)
	if len(segs) == 0 {
		t.Fatal("第 0 行没找到高亮像素，无法测水平内缩")
	}
	seg := segs[0]
	slotLeft, slotRight := listPadX, geometry.PanelWidth-listPadX
	// 绝对阈值，不写 `< rowInsetX`（同义反复：变异 rowInsetX 时断言跟着失效，
	// 已实测 rowInsetX=0 能溜过去）。这里锁的是「高亮块不贴边」这条观感要求。
	const minInsetX = 4
	if left := seg.from - slotLeft; left < minInsetX {
		t.Errorf("药丸左侧只缩进 %d DIP（x=%d，行槽左缘 %d），下限 %d DIP："+
			"高亮块贴边就失去了「不拉满」的观感（当前 RowInsetX=%d）",
			left, seg.from, slotLeft, minInsetX, theme.RowInsetX)
	}
	if right := slotRight - seg.to; right < minInsetX {
		t.Errorf("药丸右侧只缩进 %d DIP（x=%d，行槽右缘 %d），下限 %d DIP："+
			"高亮块贴边就失去了「不拉满」的观感（当前 RowInsetX=%d）",
			right, seg.to, slotRight, minInsetX, theme.RowInsetX)
	}
}

// TestSelectedRowNoAccentBleed 守住 §11.5 E7 的实现坑：选中行不得露出 mygo
// 行节点自带的 Accent 实底——外层 ButtonBase 的 t.Background 遮罩一旦失效，
// 药丸四周就会显出一圈蓝边（「框」的另一种更隐蔽的来源）。
func TestSelectedRowNoAccentBleed(t *testing.T) {
	tt, th := newRealRowTester(t, 0)
	img := tt.Image()
	b := img.Bounds()
	// 在选中行药丸上下各 1 DIP 处横扫：那里若出现 Accent，说明遮罩没盖住。
	for _, y := range []int{row0SlotTop + 1, row0SlotTop + theme.RowSlotH - 2} {
		for x := 0; x < b.Dx(); x += 3 {
			r, g, bb := pxAt(img, x, y)
			if r == th.Accent.R && g == th.Accent.G && bb == th.Accent.B {
				t.Fatalf("y=%d x=%d 露出 Accent 实底：外层 ButtonBase 的"+
					" t.Background 遮罩失效（mygo 会在选中行上 Accent，"+
					"list.go:582 → table.go:561-570）", y, x)
			}
		}
	}
	// 同时确认药丸中心确实是 Selection.Over(Background)。
	want := th.Selection.Over(th.Background)
	r, g, bb := pxAt(img, rowPillX, row0CenterY)
	if r != want.R || g != want.G || bb != want.B {
		t.Errorf("选中行药丸中心像素 = (%d,%d,%d)，期望 Selection.Over(Background) = (%d,%d,%d)",
			r, g, bb, want.R, want.G, want.B)
	}
}

// TestPanelStillShowsEightRows 守住本轮的硬约束：面板尺寸 680×494 与
// 「可见 8 行」都不变（§11.11 与任务约定）。本轮给药丸加了上下 + 左右
// 内缩，若有人误把行距或面板高度改大，8 行就会看不完——这条能在单测里
// 抓到，不必等真机。
func TestPanelStillShowsEightRows(t *testing.T) {
	m := panelstate.Model{Results: testutil.MkApps(
		"AA", "BB", "CC", "DD", "EE", "FF", "GG", "HH", "II")}
	icons := map[string]*goui.Bitmap{}
	for _, a := range m.Results {
		icons[a.Path] = nil
	}
	var ls goui.ListState
	var listBottom float32
	tt := goui.NewTester(func(c *goui.Context) {
		goui.Column(c).Fill().Children(func() {
			goui.Row(c).Padding(16, 18).Gap(12).AlignItems(goui.Center).Children(func() {
				goui.TextInputBase(c, new(string)).Grow(1).FontSize(theme.InputSize)
			})
			goui.Divider(c)
			ls.Selected = &m.Chosen
			l := goui.List(c, &ls, len(m.Results), func(i int) {
				Row(c, &m, &icons, i, func(int) {})
			}).Grow(1).Padding(6)
			l.Draw(func(p *goui.Painter, r goui.Rect) {
				listBottom = r.Y + r.H
			})
			goui.Divider(c)
			goui.Row(c).Padding(8, 16).Gap(16).Children(func() {
				goui.Text(c, "hint").FontSize(theme.HintSize)
			})
		})
	}, geometry.PanelWidth, geometry.PanelHeight)
	tt.Frame()
	tt.Frame()

	// 「可见 8 行」= 第 8 行要**完整**落在列表可视区内，即第 8 行行槽的底
	// （= 第 9 行行槽的顶）不越过列表区底部。
	eighthBottom := float32(row0SlotTop + 8*theme.RowSlotH)
	if eighthBottom > listBottom {
		t.Errorf("第 8 行行槽底 y=%.0f 超出列表区底部 y=%.0f：可见行数少于 8"+
			"（RowSlotH=%d，药丸上下内缩 RowGap=%d）",
			eighthBottom, listBottom, theme.RowSlotH, theme.RowGap)
	}
	// 第 8 行的行槽顶也必须确实在可视区内（防止面板被撑得过高导致空列表）。
	eighthTop := float32(row0SlotTop + 7*theme.RowSlotH)
	if eighthTop >= listBottom {
		t.Errorf("第 8 行行槽顶 y=%.0f 已超出列表区底部 y=%.0f", eighthTop, listBottom)
	}
	if listBottom > float32(geometry.PanelHeight) {
		t.Errorf("列表区底部 y=%.0f 超出面板高 %d：面板被内容撑高了",
			listBottom, geometry.PanelHeight)
	}
}
