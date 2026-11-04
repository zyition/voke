// cursor.go：屏幕指针位置的只读采样（mygo Screen 模块的转发件）。
//
// 为什么要单独暴露它：mygo 没有鼠标移动回调，而「hover 即选中」需要区分
// 「指针真的动过」与「列表内容在静止的指针下变了」（过滤/滚动）。调用方
// 逐帧采样一次位置、只做相邻两帧的相等比较，不参与任何定位或坐标换算，
// 故这里直接返回 mygo 的 DIP 屏幕坐标（跨屏 DPI 不同也不影响相等判定）。
package platform

import "github.com/egoist/mygo"

// CursorScreenPoint 返回指针的 DIP 屏幕坐标。
//
// **只许主线程**——mygo Screen 模块走 onMainValue（modules.go:265-272），
// 后台线程调用会等待主循环、无主循环时永久阻塞。
func CursorScreenPoint() (x, y int) {
	p := mygo.Screen.CursorScreenPoint()
	return p.X, p.Y
}
