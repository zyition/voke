// hoverselect.go：列表「hover 即选中」的差分状态。
//
// mygo 没有鼠标移动回调，只能在渲染每行时用 Hovered() 判断指针在哪。若直接
// 把 hover 行写进选中，指针停在某行时会把方向键的选择每帧抢回来；这里只在
// 「hover 行发生变化」时改写选中：指针进入某行 = 选中它，停留期间的键盘导航
// 不受影响，指针离开列表只清记忆、保留最后选中（同 Windows 开始菜单等）。
package ui

import goui "github.com/egoist/mygo/ui"

// HoverSelect 由列表调用方持有，每个列表一份。
type HoverSelect struct {
	// lastPlusOne 是上一次看到的 hover 行 +1；0 = 不在任何行上（零值即「无」）。
	lastPlusOne int
}

// Select 用本帧 hover 行（-1 = 无）推进状态，返回需要改写的选中行。
// changed=false 表示本帧不动选中（hover 未变化 / 指针不在行上）。
func (h *HoverSelect) Select(hovered int) (row int, changed bool) {
	cur := hovered + 1
	if cur == h.lastPlusOne {
		return 0, false
	}
	h.lastPlusOne = cur
	if hovered < 0 {
		return 0, false
	}
	return hovered, true
}

// applyHoverSelect 把本帧 hover 行落到选中。本帧的行已按旧选中画完，
// 再请求一帧让高亮跟上（生产侧 host.Invalidate 会调度下一帧）。
func applyHoverSelect(c *goui.Context, hs *HoverSelect, hovered int, chosen *int) {
	if hs == nil || chosen == nil {
		return
	}
	row, changed := hs.Select(hovered)
	if !changed || row == *chosen {
		return
	}
	*chosen = row
	c.Invalidate()
}
