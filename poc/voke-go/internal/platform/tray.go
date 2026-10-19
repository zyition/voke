// tray.go：§7.2.2 点名的**托盘 + 菜单构造原语薄封装**（重构步骤 8a）。
//
// 本层只收「向 mygo 要一个对象 / 把对象交回事件循环」这两步构造，
// **菜单项的字面量与它的回调留在 main**（理由见 SetTrayMenu 的注释）——
// §7.2.2 给 tray.go 的定位是「托盘 + 菜单构造」。动态刷新不在本层：
// mygo 的 SetMenu 在**调用时**取菜单快照，菜单弹出不会现读；因此 app 层在
// awake 状态变化 / 面板显隐时显式重建菜单（见 panel.go refreshTrayMenu）。
// 构建标签不加（mygo 的 Tray 面每个平台都有后端：macOS 菜单栏 / Windows
// 通知区各有 internal/{darwin,windows}/ 实现）。
package platform

import "github.com/egoist/mygo"

// NewTray 往通知区 / 菜单栏放一个图标，返回 mygo 的托盘句柄。
//
// 转发 `mygo.NewTray`（modules.go:665 `func NewTray(opts TrayOptions)
// (*Tray, error)`）：TrayOptions（modules.go:637-648）**只填 Icon / ToolTip**
// 两个字段，与 panel.go setupTray 原来那句逐字相同——IconIsTemplate / Title /
// Menu 三个字段原来就没写，零值照旧（Menu 留 nil 是有意的：菜单在下一步
// SetTrayMenu 里挂，与搬之前同一顺序）。
//
// `*mygo.Tray` 原封不动交回调用方持有（appState.tray 字段存的就是它），本层
// **不保存任何托盘状态**，也就不引入第二份真相；退出时的 `Tray.Destroy()`
// （modules.go:748）在 main 的 App.OnWillQuit 回调里，属装配层，不搬。
//
// 线程模型（§7.1.3）：**只许主线程，且必须在 App.WhenReady 里**。mygo 自己判：
// 非主线程先 `App.waitReady()` 等就绪，主线程但尚未就绪**直接 panic**
// 「NewTray called before the application is ready」（modules.go:666-670）。
// 建 native 托盘 + 解码 PNG 那一段跑在 onMain（modules.go:673-692 →
// loop.go:94，主线程即直调、不排队）。
//
// error 与 (nil, error) 原样带回（图标解码失败 / 事件循环已关停
// modules.go:693-697），**不在本层降级也不静默**：调用方（panel.go
// setupTray）沿用搬之前那条中文日志 + `return`（热键与面板照常，只是没托盘）。
func NewTray(icon []byte, toolTip string) (*mygo.Tray, error) {
	return mygo.NewTray(mygo.TrayOptions{Icon: icon, ToolTip: toolTip})
}

// SetTrayMenu 用 items 现建一份菜单并挂到托盘上（点击图标即弹这份菜单）。
//
// 它对应 panel.go setupTray 里的**一整条语句**
// `tray.SetMenu(mygo.NewMenu([]*mygo.MenuItem{...}))`：两个构造原语本来就在
// 同一行里，求值顺序是「先算 items 字面量 → NewMenu → SetMenu」。本函数照原样
// 排（`tray.SetMenu(mygo.NewMenu(items))`），中间不插任何逻辑，所以搬过去之后
// 谁先谁后、传了什么，都与搬之前逐字一致。
//
// 转发：
//   - `mygo.NewMenu`（menu.go:148 `func NewMenu(items []*MenuItem) *Menu`，
//     在 menuMu 下逐项 prepareItem 发 uid）
//   - `(*mygo.Tray).SetMenu`（modules.go:725，取 m.snapshot()（menu.go:399）
//     后经 Tray.do → onMain 递给 native）
//
// **菜单项字面量故意留在 main**（本层不收，属装配/回调纠缠，按任务纪律只搬
// 构造、纠缠上报）：那一份 `[]*mygo.MenuItem` 里有两处引用 main 侧的东西——
//   - 「显示面板」的 `Click` 回调闭包捕获 appState 的 st.showPanel
//     （签名 `func(item *mygo.MenuItem, win *mygo.Window)`，menu.go:109）；
//   - `Accelerator: hotkeyShow` 用的是 main 包的热键常量（main.go:25）。
//
// 另外两项（mygo.Separator() menu.go:127、{Role: mygo.RoleQuit} menu.go:58
// 走 mygo 内建的 App.Quit）本身不纠缠，但它们与上面两项**同在一个字面量里**，
// 拆开单包一个 Separator 就是给本层添两个没人复用的转发。
// 于是 main 仍需 import mygo 来写这份 items——这是「只搬构造」的必然残留，
// 不是漏搬。等步骤 8 的装配层（internal/app）落地时，这段连同 setupTray
// 一起搬走。
//
// 线程模型：**只许主线程**（NewMenu 与 SetMenu 都不做线程跳转，落到 native 靠
// onMain；从别的 goroutine 调会经 onMain 阻塞等主循环、无超时）。调用点是
// panel.go 的 ready()（App.WhenReady 里），与搬之前同一位置。
func SetTrayMenu(tray *mygo.Tray, items []*mygo.MenuItem) {
	tray.SetMenu(mygo.NewMenu(items))
}
