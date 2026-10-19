// main.go 是 voke-go PoC 的装配入口：单实例、全局热键、托盘、退出逻辑。
//
// 隔离纪律（与生产 voke 并存运行不得互相干扰，见 docs/notes/voke-go-poc.md §3）：
//   - 二进制名 voke-go.exe（生产 voke 是 voke.exe）；
//   - 默认热键 Ctrl+Alt+Space（生产 voke 是 alt+space）；
//   - 单实例按 app id 区分（mygo 的锁按 App.Name 粒度，与可执行路径无关），
//     且生产 voke 用的是命名互斥体 Local\voke.instance，机制不同、天然隔离；
//   - 数据/日志目录 %APPDATA%\voke-go\（严禁碰 %APPDATA%\voke\；口径落在
//     internal/platform 的 InitLog / DataDir）；
//   - 不进开机自启动（不调用任何 OpenAtLogin 相关 API）。
package app

import (
	"log"

	"github.com/egoist/mygo"
	"github.com/zyition/voke/poc/voke-go/internal/platform"
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
