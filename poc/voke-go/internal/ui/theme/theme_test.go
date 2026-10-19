// theme 常量值**钉死**用例：本轮（§7.2 步骤 7 的安全子件）只是把字号与行
// 视觉常量从 view.go 搬进本包，**值必须逐字不变**——像素回归网
// （`row_visual_test.go` / `panel_behavior_test.go`）正是拿这些值当基准的。
//
// 这条用例不是像素测试的替代品：像素测试锁的是「渲染出来的留白/内缩够不够」
// （阈值一律是绝对 DIP，见 PoC 文档 §10.1.1 陷阱 2「用被测常量当阈值 =
// 同义反复」）；这里锁的是「搬动没改值」以及 §7.4 定案的**字号关系**
// （输入框 = 行标题 + 2、副行必须比提示行大一级）。改坏任何一个值，本用例
// 与对应像素用例应同时变红——两条都在，缺一都不算守住。
package theme

import "testing"

func TestThemeConstantsLocked(t *testing.T) {
	// 与生产 voke / 任务约定对齐的行视觉值（§7.5.1、§10.1 第 4–5 条）。
	for _, tc := range []struct {
		name string
		got  int
		want int
	}{
		{"RowHeight（=生产 search.rs:144 ROW_HEIGHT）", RowHeight, 48},
		{"RowSlotH（行距，维持 48 与生产对齐）", RowSlotH, 48},
		{"RowGap（上下各留 2 DIP）", RowGap, 4},
		{"RowInsetX（药丸左右缩进）", RowInsetX, 6},
		{"IconSize（=platform.iconDIPSize）", IconSize, 24},
		// 字号体系（§7.4 定案值：16 / 14 / 12 / 11）。
		{"TitleSize（=mygo 主题默认字号）", TitleSize, 13},
		{"InputSize", InputSize, 15},
		{"DetailSize", DetailSize, 11},
		{"HintSize", HintSize, 10},
		{"FooterSize（底部提示，比 Hint 大一档）", FooterSize, 12},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %d，期望 %d（搬动只许改位置、不许改值）", tc.name, tc.got, tc.want)
		}
	}

	// §7.4 执行口径里**必须成体系**看的两条关系，单独钉一遍：
	// 裸数字抄成 16 也照样通过上面的断言，但表达式一旦被人改成独立硬编码，
	// 下次调 TitleSize 就会悄悄失焦。
	if InputSize != TitleSize+2 {
		t.Errorf("InputSize=%d 不等于 TitleSize+2=%d：§7.4 要求输入框字号写成"+
			"行标题 +2 的表达式，不是裸数字", InputSize, TitleSize+2)
	}
	if DetailSize <= HintSize {
		t.Errorf("DetailSize=%d 没有大于 HintSize=%d：§7.4 执行口径 4——副行与提示行"+
			"同号则层级消失", DetailSize, HintSize)
	}
}
