// 随渲染层从 package main 搬进 internal/ui 的**交互帧 / 空列表帧** Tester 用例
// （§7.2 步骤7b）。断言、点击序列、阈值与搬前逐字相同，未削弱、未抄第二份实现。
//
// 这三条都是「要真实帧」的 mygo NewTester 用例：
//   - TestListEmptyRendersHintNoRows：n=0 时 ui.List 安全性（不建行、不 panic、空态可见）；
//   - TestRowSingleClickExecutesOnce / TestRowClickKeepsKeyboardNavigation：
//     ButtonBase 作行内容祖先的命中链 + 每帧回抢焦点后键盘导航仍归应用层。
//
// 它们刻意用与 Row() 同款的结构就地搭一行（测的是 mygo 的命中链 / 焦点机制本身，
// 不是行像素——行像素由同包 row_visual_test.go 直测生产 Row()）。
// 搬动只把原 `&appState{model:...}` / `st.list` / `st.choose` 换成本包的
// `panelstate.Model` / `goui.ListState` / `Model.Choose`，帧里的执行与断言不变。
package ui

import (
	"strings"
	"testing"

	goui "github.com/egoist/mygo/ui"

	"github.com/zyition/voke/poc/voke-go/internal/logic/geometry"
	"github.com/zyition/voke/poc/voke-go/internal/logic/panelstate"
	"github.com/zyition/voke/poc/voke-go/internal/logic/testutil"
	"github.com/zyition/voke/poc/voke-go/internal/ui/theme"
)

// TestListEmptyRendersHintNoRows 验证 n=0 时 ui.List 安全：不构建任何行、
// 不 panic、空态文案可见，且加载完成后列表能正常出现（懒加载的落点）。
func TestListEmptyRendersHintNoRows(t *testing.T) {
	rows := 0
	names := []string{"AA", "BB", "CC"}
	n := 0
	tt := goui.NewTester(func(c *goui.Context) {
		th := c.Theme()
		goui.Column(c).Fill().Children(func() {
			goui.TextInputBase(c, new(string))
			goui.List(c, &goui.ListState{}, n, func(i int) {
				rows++
				goui.Text(c, names[i])
			}).Grow(1).Padding(6).Children(func() {
				if n == 0 {
					goui.Text(c, "输入以搜索应用").TextColor(th.TextMuted).Padding(16)
				}
			})
		})
	}, geometry.PanelWidth, geometry.PanelHeight)
	tt.Frame()
	tt.Frame()
	if rows != 0 {
		t.Errorf("n=0 时不应构建任何行，实际构建 %d 行", rows)
	}
	if !tt.HasText("输入以搜索应用") {
		t.Error("唤起时空态引导文案不可见")
	}
	n = len(names)
	tt.Frame()
	tt.Frame()
	for _, name := range names {
		if !tt.HasText(name) {
			t.Errorf("加载完成后 %q 不可见", name)
		}
	}
}

// TestRowSingleClickExecutesOnce 锁定「单击即执行」的核心行为，且验证
// **换 ButtonBase 之后命中链没坏**。
//
// 用的是 internal/ui/view.go 里 Row() 的同款结构（ButtonBase 包住整行内容，药丸是它
// 的子元素）：连点不同行应得到与点击次数一一对应的执行序列。
func TestRowSingleClickExecutesOnce(t *testing.T) {
	names := []string{"AA", "BB", "CC", "DD"}
	var launched []string
	m := panelstate.Model{Results: testutil.MkApps(names...)}
	var ls goui.ListState
	tt := goui.NewTester(func(c *goui.Context) {
		th := c.Theme()
		goui.Column(c).Fill().Children(func() {
			goui.TextInputBase(c, new(string))
			ls.Selected = &m.Chosen
			goui.List(c, &ls, len(m.Results), func(i int) {
				pill := goui.Transparent
				if i == m.Chosen {
					pill = th.Selection.Over(th.Background)
				}
				// 与 Row() 同款：ButtonBase 是整行内容的祖先。
				btn := goui.ButtonBase(c).Height(theme.RowSlotH).Grow(1).AlignItems(goui.Center).
					Background(th.Background)
				if btn.Clicked() {
					m.Chosen = i
					launched = append(launched, names[i])
				}
				btn.Children(func() {
					goui.Row(c).Grow(1).Height(float32(theme.RowSlotH - theme.RowGap)).MarginX(theme.RowInsetX).
						Radius(th.Radius).Background(pill).Children(func() {
						goui.Text(c, names[i]).SingleLine().FontSize(theme.TitleSize)
					})
				})
			}).Grow(1).Padding(6)
		})
	}, geometry.PanelWidth, geometry.PanelHeight)
	tt.Frame()
	for _, name := range []string{"BB", "DD", "AA", "BB"} {
		if err := tt.Click(name); err != nil {
			t.Fatalf("点击 %q 失败：%v", name, err)
		}
		tt.Frame()
	}
	want := "BB,DD,AA,BB"
	if got := strings.Join(launched, ","); got != want {
		t.Errorf("单击执行序列=%v，期望 %v（一次点击对应一次执行）", launched, want)
	}
}

// TestRowClickKeepsKeyboardNavigation 锁定配套约束：样式重做后，
// 键盘 ↑↓ 必须仍然归应用层——因为可获焦点的按钮会抢焦点
// （base.go:26 coreButtonBase 的 .Focusable()），靠每帧回抢焦点兜住。
func TestRowClickKeepsKeyboardNavigation(t *testing.T) {
	names := []string{"AA", "BB", "CC", "DD"}
	m := panelstate.Model{Results: testutil.MkApps(names...)}
	var ls goui.ListState
	var input goui.Handle
	down, up := 0, 0
	tt := goui.NewTester(func(c *goui.Context) {
		th := c.Theme()
		goui.Column(c).Fill().Children(func() {
			goui.TextInputBase(c, new(string)).Bind(&input)
			ls.Selected = &m.Chosen
			goui.List(c, &ls, len(m.Results), func(i int) {
				pill := goui.Transparent
				if i == m.Chosen {
					pill = th.Selection.Over(th.Background)
				}
				btn := goui.ButtonBase(c).Height(theme.RowSlotH).Grow(1).Background(th.Background)
				if btn.Clicked() {
					m.Chosen = i
				}
				btn.Children(func() {
					goui.Row(c).Grow(1).Height(float32(theme.RowSlotH - theme.RowGap)).MarginX(theme.RowInsetX).
						Radius(th.Radius).Background(pill).Children(func() {
						goui.Text(c, names[i]).SingleLine()
					})
				})
			}).Grow(1).Padding(6)
		})
		input.Focus()
		if c.Shortcut(0, goui.KeyDown) {
			down++
			m.Choose(m.Chosen + 1)
		}
		if c.Shortcut(0, goui.KeyUp) {
			up++
			m.Choose(m.Chosen - 1)
		}
	}, geometry.PanelWidth, geometry.PanelHeight)
	tt.Frame()
	if err := tt.Click("CC"); err != nil {
		t.Fatalf("点击 CC 失败：%v", err)
	}
	tt.Frame()
	tt.Key(0, goui.KeyDown)
	tt.Frame()
	tt.Key(0, goui.KeyUp)
	tt.Frame()
	if down != 1 || up != 1 {
		t.Errorf("点击后方向键应仍归应用层：↓=%d ↑=%d，期望各 1", down, up)
	}
}
