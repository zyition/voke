// platform 层单实例原语（singleinstance.go）的 headless 边界测试。
//
// 8a 是**纯搬迁**步骤，本文件只钉转发层自身可能被搬坏的点，不复述 mygo 的行为。
// hotkey.go / tray.go 不配测试，理由见下面「测不到的三条」第三条与 shell_test.go
// 开头的同款说明（无主循环即 onMainValue 死等 / needsApp panic）。
package platform

import (
	"testing"

	"github.com/egoist/mygo"
)

// TestRequestSingleInstanceGrantsAndNames 钉住转发层的两条契约：**bool 不被改写**
// （拿到锁 → true）与**name 原样递给 SetName**。这两条防的正是「搬的时候把 return
// 写反 / 把 SetName 那一句丢了 / 把调用方的 name 换成硬编码常量」。
//
// 能跑通靠的是 mygo 的 MYGO_GENERATE 分支（single_instance.go:47-49）：该环境变量
// 非空时 RequestSingleInstanceLock **直接返回 true**，不建 socket、不起监听
// goroutine、不注册 OnQuit——这是本层唯一在「没有主循环」下能走通的路径，
// 断言的是生产函数本身（main 调的就是它），不是复刻一份逻辑。
//
// 测不到的三条**不硬凑**：
//   - false 分支要**两个进程**：第二实例得 net.Dial 上首实例的 socket 才返回
//     false（single_instance.go:59-72）；同进程再调一次只会命中「已持锁」快路径
//     （:52-53）恒 true，在单测里造不出 false。
//   - 「先锁、拿到锁才改名」的**顺序**在此不可观测（该分支下锁是 no-op，
//     SetName 也没有可读回的时机信号），只能靠源码逐字对照与 §6.7 的 V7 真机
//     （第二实例退出、首实例面板唤出）覆盖。
//   - RegisterHotkey / NewTray / SetTrayMenu 在「主线程且 App 未初始化」时是
//     panic（needsApp app.go:128-131、NewTray modules.go:666-670）或死等
//     （onMainValue loop.go:94-110），单测里跑它们等于把整个 GUI 拉起来——
//     PoC 不造这种假测，真机行为交 V7。
func TestRequestSingleInstanceGrantsAndNames(t *testing.T) {
	t.Setenv("MYGO_GENERATE", "1")
	// 本包只有这条测试写 App 的名字，收尾置空即回到测前状态：SetName("") 之后
	// Name() 回落到「可执行文件名去 .exe」（app.go:275-290），与测前一致。
	t.Cleanup(func() { mygo.App.SetName("") })

	const name = "voke-单实例测试"
	if !RequestSingleInstance(name) {
		t.Fatal("持锁分支应返回 true——转发层改写了 mygo 的 bool")
	}
	if got := mygo.App.Name(); got != name {
		t.Fatalf("name 应原样递给 SetName：想要 %q，实得 %q", name, got)
	}
}
