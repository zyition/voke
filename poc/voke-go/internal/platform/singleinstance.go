// singleinstance.go：§7.2.2 点名的**单实例原语薄封装**（重构步骤 8a）。
//
// 本文件的存在理由与边界（同 hotkey.go / tray.go）：§7.2.1 第 1 条把 mygo 的
// 平台调用收在 platform 层，而 main 侧只留「接线」。8a 是**严格纯搬迁**——
// 被包的调用参数、顺序、返回值处置一字不变，只是从 `package main` 挪进下面
// 这个转发函数；装配（App.Run / OnSecondInstance / OnWindowAllClosed /
// OnWillQuit / WhenReady 这些注册，回调体引用 appState 与面板句柄）
// 一概留 main，那是步骤 8 的活。
//
// 构建标签：**不加**（照 shell.go 的理由）——这三条原语在 mygo 里每个平台都有
// 后端，Linux 原生 `go build ./...` / `go vet ./...` 编得过、被 vet 查得到，
// 运行时行为仍只面向 Windows。
package platform

import "github.com/egoist/mygo"

// RequestSingleInstance 抢单实例锁，抢到之后把应用名定为 name——§7.2.2 给
// singleinstance.go 记的那两条（「RequestSingleInstanceLock + SetName」）一次包全。
//
// 转发的是原来那两句（main.go 搬来之前的 `if !mygo.App.RequestSingleInstanceLock()
// { return }` + `mygo.App.SetName(appID)`）：
//   - `mygo.App.RequestSingleInstanceLock() bool`（single_instance.go:46）
//   - `mygo.App.SetName(name string)`（app.go:293）
//
// **顺序不可调**（这是本层唯一带行为含义的地方）：锁 key 取的是调用**当时**的
// `a.Name()`（single_instance.go:55 `singleInstanceKey(a.Name())`），而 Name() 在
// SetName 之前回落到「可执行文件名去 .exe」（app.go:275-290）、之后才是 appID。
// PoC 的 exe 名（voke-go.exe）去 .exe 恰好等于 appID `"voke"`，所以两种顺序
// 今天算出**同一个 key**——但这是巧合（PoC 文档 §2.4 把这句 SetName 称作「双保险」
// 正是此意），不能拿巧合当许可：把 SetName 挪到锁前面，就把「按 exe 名锁」改成了
// 「按 appID 锁」，凡把二进制改名 / 复制成别的名字跑，行为立刻不同。搬的这一步
// 什么都不动，故保持「先锁、拿到锁才改名」。
//
// 拿到锁才 SetName 的另一层意思：拿不到锁（第二实例）时 mygo 已把本实例的
// 命令行交给首实例（single_instance.go:59-72），调用方随即 return，改不改名都无关；
// 返回值处置与原来一致——**false 交回调用方决定是否 return**，本层不做任何
// 「顺手记日志 / 顺手 Quit」的加戏。
//
// 线程模型（§7.1.3）：**主线程、且必须在 App.Run 之前**——mygo 官方姿势就是在
// main 里 Run 前调（single_instance.go:38-43 的示例注释即此）。它不进事件循环：
// 既不碰 needsApp（app.go:128-131）也不碰 onMain/onMainValue（loop.go:94-110），
// 底层是 per-user 的 unix socket 文件（single_instance.go:55 + :159-168，落在
// XDG_RUNTIME_DIR / TempDir / /tmp），锁的释放在 mygo 内部挂在 OnQuit 上
// （:87），调用方无需收尾。
func RequestSingleInstance(name string) bool {
	if !mygo.App.RequestSingleInstanceLock() {
		return false
	}
	mygo.App.SetName(name)
	return true
}
