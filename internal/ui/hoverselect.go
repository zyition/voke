// hoverselect.go：列表「hover 即选中」的差分状态。
//
// mygo 没有鼠标移动回调，只能在渲染每行时用 Hovered() 判断指针在哪。但
// Hovered() 变化有两个来源：指针真的动了，或者列表内容/滚动在静止的指针下
// 变了。若两者都算选中，面板一打开、或输入过滤刚把列表换掉，选中就会跳到
// 鼠标所在行——与默认选中第一条的路径不一致。
//
// 所以这里吃两个信号：本帧 hover 行 + 本帧指针是否真的移动过（app 侧逐帧
// 采样屏幕指针后传入）。只有「hover 行变化且指针确实动过」才改写选中：
// 指针停留期间的方向键导航、输入触发的列表重排都不会被抢回；指针一移动，
// 选中仍即时跟到指针所在行。
package ui

import goui "github.com/egoist/mygo/ui"

// HoverSelect 由列表调用方持有，每个列表一份。
type HoverSelect struct {
	// lastPlusOne 是上一次**由指针移动跟随**的 hover 行 +1；0 = 还没跟随过
	// （零值）。只在移动帧里更新，所以键盘操作期间的 hover 变化不会把它弄脏。
	lastPlusOne int
}

// Select 用本帧 hover 行与指针移动信号推进状态，返回需要改写的选中行。
// changed=false 表示本帧不动选中（指针没动 / 不在任何行上 / 与上次跟随的行相同）。
func (h *HoverSelect) Select(hovered int, pointerMoved bool) (row int, changed bool) {
	if !pointerMoved {
		return 0, false
	}
	if hovered < 0 {
		// 指针离开列表：清掉记忆，移出再移回同一行才算一次新的跟随。
		h.lastPlusOne = 0
		return 0, false
	}
	cur := hovered + 1
	if cur == h.lastPlusOne {
		return 0, false
	}
	h.lastPlusOne = cur
	return hovered, true
}

// applyHoverSelect 把本帧 hover 行落到选中。本帧的行已按旧选中画完，
// 再请求一帧让高亮跟上（生产侧 host.Invalidate 会调度下一帧）。
func applyHoverSelect(c *goui.Context, hs *HoverSelect, hovered int, chosen *int, pointerMoved bool) {
	if hs == nil || chosen == nil {
		return
	}
	row, changed := hs.Select(hovered, pointerMoved)
	if !changed || row == *chosen {
		return
	}
	*chosen = row
	c.Invalidate()
}
