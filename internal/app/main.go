// main.go 是 voke 的装配入口：单实例、全局热键、托盘、退出逻辑。
//
// 运行事实（详见 docs/architecture.md）：
//   - 二进制为 voke.exe；默认热键 Alt+Space；
//   - 单实例用 mygo 的 per-user socket 锁（按 app 名，不是命名互斥体）；
//   - 数据/日志目录 %APPDATA%\voke\（InitLog / DataDir 在 internal/platform）；
//   - 开机自启由设置页开关写入/删除 HKCU\...\Run\voke。
package app

import (
	"log"
	"os"

	"github.com/egoist/mygo"
	"voke/internal/platform"
)

const (
	// appID 是 mygo 单实例锁的 key（与显示名一致）。注意 mygo 用 per-user socket、
	// 生产 voke 用命名互斥体，二者机制不同；本版将替换原版，故名称统一为 voke。
	appID = "voke"
	// hotkeyShow 是默认热键（与生产 voke 一致：Alt+Space）；语法为 Electron 风格加速器
	// （大小写不敏感，"Alt+Space" 与 "alt+space" 等价）。
	hotkeyShow = "Alt+Space"
)

func Main() {
	platform.InitLog()
	// 提权粘贴辅助进程：必须在单实例锁 / mygo App 初始化之前分流，否则会去抢
	// 首实例的锁、或在后台建托盘。它只发一次粘贴按键就退出。
	if handled, code := HandleElevatedPaste(os.Args[1:]); handled {
		os.Exit(code)
	}
	// 启动早期纠正自身工作目录（对照生产 main.rs 在 log init 后、相对路径解析前调
	// voke_sys::fix_own_cwd）：开机自启继承到 System32 时改到 %USERPROFILE%，避免继承
	// cwd 启动的子进程拿错起始目录；用户显式指定的 cwd 不动。
	platform.FixOwnCwd()

	// 单实例：第二实例在 RequestSingleInstanceLock 里把命令行交给首实例后
	// 返回 false，此时直接退出即可（首实例经 OnSecondInstance 唤出面板）。
	// §7.2.2 步骤8a：这两句原语（先取锁、拿到锁才 SetName）已收进
	// platform.RequestSingleInstance，顺序与返回值处置保持原样；下面的
	// OnSecondInstance / OnWindowAllClosed / OnWillQuit / WhenReady 是**注册**，
	// 回调体引用 appState 与面板句柄，属装配层，留在本文件（步骤8）。
	if !platform.RequestSingleInstance(appID) {
		return
	}

	st := &appState{}

	// 第二实例启动 → 唤出首实例的面板。
	mygo.App.OnSecondInstance(func([]string, string) { st.showPanel() })
	// 面板隐藏后没有任何窗口时保持运行（托盘常驻，不退出）。
	mygo.App.OnWindowAllClosed(func() {})
	// 退出序列的**第一步**（先于逐个 w.close() 触发）：置 quitting，让面板/设置窗
	// 的 OnClose 不再「关=隐藏」而是放行真关闭——否则 w.close() 被 veto 返回 false，
	// prepareQuit 会提前中止、永不 os.Exit，进程退不掉（托盘退出的根因修复）。
	mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) { st.quitting = true })
	// 退出前销毁托盘图标。
	mygo.App.OnWillQuit(func(*mygo.QuitEvent) {
		st.stopIconTrimTimer()
		st.stopWindowRefreshTimer()
		if st.clipWatchStop != nil {
			st.clipWatchStop()
		}
		if st.tray != nil {
			st.tray.Destroy()
		}
	})
	mygo.App.WhenReady(func() {
		st.ready()
	})

	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
