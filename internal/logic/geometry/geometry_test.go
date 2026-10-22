// geometry 的同包测试：面板定位算术。随 §7.2 步骤4 从 panel_behavior_test.go
// （package main）搬进来——原用例只验 panelTopLeft 的**数学**，不依赖 mygo
// ui.NewTester 的真实帧，故属 logic 层可测范围；依赖渲染的用例
// （TestPanelStillShowsEightRows 等）仍留在 main 侧，那里要真实帧才有证据。
//
// 纪律（PoC 文档 §10.1.1 两个「测试是假的」陷阱）：
//  1. **直测生产函数** PanelTopLeft，绝不在测试里复刻一份算术——复刻过一回，
//     变异「把生产改回旧口径」时全绿。
//  2. 期望值写**绝对像素 / 绝对百分比**，不用被测常量（PanelWidth /
//     PanelHeight / CenterPercent）反推阈值：变异常量时跟着失效。入参里的
//     720/494 也写字面量，避免「常量被改 → 测试跟着一起改」的假绿。
package geometry

import "testing"

// TestPanelCenterAnchorMatchesLaunchers 锁定垂直定位的**锚点**与系数
// （§7.6.3 定案；用户反馈「偏上」后由 48% 下移到 52%）。从 panel_behavior_test.go
// 原样搬来，并按「绝对值」纪律把期望从「用常量反推」升级为「写死像素」。
//
// 断言的是「面板中心落在 work area 的 52%」这个换算关系（锚面板中心，
// 而不是锚输入框中心——后者才是「42 仍偏高」的根因，见 §7.6.2）：
//   - 1080p 面板中心 52%，输入框中心 ~30.9%
//   - 1440p 面板中心 52%
func TestPanelCenterAnchorMatchesLaunchers(t *testing.T) {
	// 输入框中心相对面板顶的偏移：实测输入行高 56 DIP（view.go 的
	// Row.Padding(16,18) + TextInput 高 24），故中心 = 顶 + 28。这是**渲染
	// 实测**事实（§7.6.2 的 chrome 表），不是本包算术的输入，故留在测试里。
	const inputCenterOffset = 28
	for _, tc := range []struct {
		name    string
		workW   int
		workH   int
		wantTop int // 绝对像素：面板顶 y
		wantX   int // 绝对像素：水平居中
	}{
		{"1080p", 1920, 1040, 293, 600},
		{"1440p", 2560, 1400, 481, 920},
	} {
		gotX, top := PanelTopLeft(0, 0, tc.workW, tc.workH, 720, 494)
		if top != tc.wantTop {
			t.Errorf("%s：面板顶 y=%d，期望 %d（逐像素锁定 §7.6.3 定案）",
				tc.name, top, tc.wantTop)
		}
		if gotX != tc.wantX {
			t.Errorf("%s：水平 x=%d，期望 work area 水平居中 %d", tc.name, gotX, tc.wantX)
		}
		// 面板中心 = 顶 + 半个面板高（494/2 = 247，写字面量不引常量）。
		center := top + 247
		if got := float64(center) / float64(tc.workH) * 100; got < 51.5 || got > 52.5 {
			t.Errorf("%s：面板中心落在 %.2f%%，期望 52%%（略偏下）", tc.name, got)
		}
		// 输入框中心（面板顶 + 28）随面板整体下移，允许 26–40% 区间。
		ic := float64(top+inputCenterOffset) / float64(tc.workH) * 100
		if ic < 26 || ic > 40 {
			t.Errorf("%s：输入框中心落在 %.2f%%，期望 26–40%%", tc.name, ic)
		}
		// 不能贴顶、也不能掉出下边界。
		if top < 0 || top+494 > tc.workH {
			t.Errorf("%s：面板顶 y=%d 越界（面板高 494，work area %d）",
				tc.name, top, tc.workH)
		}
	}
	// 关键回归：旧锚点（面板顶占剩余空间 42%）在 1440p 上会把面板中心
	// 压到 44.8%，比 1080p 更偏上——跨分辨率漂移。锁住新锚点不漂。
	old := func(workH int) float64 {
		top := (workH - 494) * 42 / 100
		return float64(top+494/2) / float64(workH) * 100
	}
	if a, b := old(1040), old(1400); a-b < 0.5 {
		t.Errorf("旧口径在两分辨率间几乎不漂（%.2f%% vs %.2f%%），说明本用例"+
			"已失去区分能力，请重新设计断言", a, b)
	}
}

// TestPanelTopLeftAbsolutePixels 把逐像素落点铺全：除 1080p / 1440p 两档外，
// 补 §6 验收里点名的 **125% 缩放**（DIP 口径 1536×864）与 work area 起点非零
// 的副屏两种排布。E-1 行为等价的判据就是这张表——搬包前后同一组入参必须得
// 到同一组像素。
func TestPanelTopLeftAbsolutePixels(t *testing.T) {
	for _, tc := range []struct {
		name           string
		waX, waY       int
		waW, waH       int
		wantX, wantTop int
	}{
		{"1080p", 0, 0, 1920, 1040, 600, 293},
		{"1440p", 0, 0, 2560, 1400, 920, 481},
		// 125% 缩放下 mygo 给的 WorkArea 已是 DIP（system.go:358-366 用该屏
		// 自己的 dpi 做 toDIP），1920×1080 物理 → 1536×864 DIP。
		{"1080p@125%（DIP 1536×864）", 0, 0, 1536, 864, 408, 202},
		{"副屏在主屏右侧（x 偏移 1920）", 1920, 0, 2560, 1400, 2840, 481},
		{"副屏在主屏下方（y 偏移 240）", 0, 240, 1920, 800, 600, 409},
	} {
		gotX, gotTop := PanelTopLeft(tc.waX, tc.waY, tc.waW, tc.waH, 720, 494)
		if gotX != tc.wantX || gotTop != tc.wantTop {
			t.Errorf("%s：PanelTopLeft(%d,%d,%d,%d,720,494) = (%d,%d)，期望 (%d,%d)",
				tc.name, tc.waX, tc.waY, tc.waW, tc.waH,
				gotX, gotTop, tc.wantX, tc.wantTop)
		}
		// 锚点口径与分辨率无关：面板中心恒落 work area 的 52%。
		centerRel := float64(gotTop+494/2-tc.waY) / float64(tc.waH) * 100
		if centerRel < 51.5 || centerRel > 52.5 {
			t.Errorf("%s：面板中心落在 work area 的 %.2f%%，期望 52%%（略偏下）",
				tc.name, centerRel)
		}
		// 上下左右都不越界（面板 720×494 是固定值，主屏 work area 恒大于它）。
		if gotTop < tc.waY || gotTop+494 > tc.waY+tc.waH {
			t.Errorf("%s：面板顶 y=%d 越出 work area [%d,%d)",
				tc.name, gotTop, tc.waY, tc.waY+tc.waH)
		}
		if gotX < tc.waX || gotX+720 > tc.waX+tc.waW {
			t.Errorf("%s：面板左 x=%d 越出 work area [%d,%d)",
				tc.name, gotX, tc.waX, tc.waX+tc.waW)
		}
	}
}

// TestPanelSizeAndCenterPercentLocked 锁住 §7.6.3 / §8.6 的定案事实本身：
// 面板尺寸 720×494（宽度 2026-10-22 经真机反馈由 640 加宽；高度保持 8 行对齐）、
// 锚点系数 52。
//
// 为什么单独一条：main 侧的渲染用例（TestPanelStillShowsEightRows、行视觉
// 那几条）拿这两个尺寸当 Tester 画布，是**间接**依赖；这里直接钉死字面值，
// 改小/改大都立刻红，且提醒改动者——尺寸变了就要同步 §7.6.2 的 chrome 实测表。
func TestPanelSizeAndCenterPercentLocked(t *testing.T) {
	if PanelWidth != 720 {
		t.Errorf("PanelWidth = %d，期望 720", PanelWidth)
	}
	if PanelHeight != 494 {
		t.Errorf("PanelHeight = %d，期望 494（可见 8 行 + 110 DIP chrome）", PanelHeight)
	}
	if CenterPercent != 52 {
		t.Errorf("CenterPercent = %d，期望 52（面板中心落 work area 52%%，§7.6.3）",
			CenterPercent)
	}
}
