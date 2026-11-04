// pointer.go：面板的「指针这一帧动没动」采样。
//
// mygo 没有鼠标移动回调，UI 层只能拿 Hovered() 问「指针现在是不是在某行上」。
// 但 Hovered() 变化有两个来源——指针真的动了，或者列表内容/滚动在静止的指针
// 下变了（输入过滤、方向键把列表滚到新位置）。后者若也算数，就会表现为用户
// 反馈的「面板一打开 / 一输入就跳到鼠标所在条目」，与前缀激活、默认选中第一
// 条的路径不一致。
//
// 所以 app 侧在每帧渲染前采样一次屏幕指针位置，和上一帧比较，把「真的移动过」
// 作为独立信号传给列表（ui.HoverSelect）。面板刚唤起时先 reset()，让打开面板
// 本身不算一次移动；指针停留期间的方向键导航与输入都不会触发跟随。
package app

// pointerSample 记住上一帧的屏幕指针位置，回答「这一次采样之间指针是否移动」。
type pointerSample struct {
	x, y  int
	valid bool
}

// movedTo 用本次采样推进状态，返回相对上一次采样是否移动。
// 首次采样（valid=false）只建立基准，不算移动。
func (p *pointerSample) movedTo(x, y int) bool {
	moved := p.valid && (p.x != x || p.y != y)
	p.x, p.y, p.valid = x, y, true
	return moved
}

// reset 丢掉基准：下一次采样重新建立、不算移动。面板从隐藏变可见时调用，
// 否则「唤起前指针就在某行上」会被误判成一次移动。
func (p *pointerSample) reset() {
	p.valid = false
}

// samplePointer 每帧渲染前调用一次，刷新 st.pointerMoved。
// cursorPoint 为 nil（headless 测试 / 装配完成前）时恒为 false。
func (st *appState) samplePointer() {
	st.pointerMoved = false
	if st.cursorPoint == nil {
		return
	}
	x, y := st.cursorPoint()
	st.pointerMoved = st.pointer.movedTo(x, y)
}
