// appsmodel 的同包测试：加载闸门。
//
// 这些用例必须和闸门字段（loadMu / loaded / loadTrigger）同包才碰得到，
// 故随字段一起从 apps_test.go / panel_behavior_test.go 搬进来；跨包的视图层
// 用例（panel_behavior_test.go TestOnQueryChangedTriggersLoadOnce）改用
// 导出的 ResetLoadGate / LoadTriggerCount 继续断言同一条不变量。
package appsmodel

import (
	"testing"
	"time"
)

// TestEnsureLoadedSingleflight 锁定闸门的核心不变量：连续多次触发只发起
// 一次枚举（等价于 singleflight 去重），以及 ResetLoadGate 能把闸门擦回
// 「未触发」让下一次重新发起。
func TestEnsureLoadedSingleflight(t *testing.T) {
	ResetLoadGate()
	t.Cleanup(ResetLoadGate)

	done := make(chan struct{}, 8)
	old := Loader
	Loader = func() { done <- struct{}{} }
	t.Cleanup(func() { Loader = old })

	// 一个 3 字母查询会连续触发 3 次（view.go 每次按键都调）。
	for i := 0; i < 3; i++ {
		EnsureLoaded()
	}
	<-done // 等那个唯一发起的 goroutine 落地，别和 Cleanup 赛跑

	if got := LoadTriggerCount(); got != 1 {
		t.Errorf("连续触发应只发起 1 次枚举，实际 %d 次", got)
	}
	// 已加载后再触发不新增（后续按键只走 filterApps 全量过滤）。
	EnsureLoaded()
	if got := LoadTriggerCount(); got != 1 {
		t.Errorf("已加载后不应再触发枚举，累计 %d 次", got)
	}

	// 复位 → 重新可触发一次（视图层用例的每例开头就依赖这条）。
	ResetLoadGate()
	if got := LoadTriggerCount(); got != 0 {
		t.Fatalf("ResetLoadGate 应把触发计数清零，实际 %d", got)
	}
	EnsureLoaded()
	<-done
	if got := LoadTriggerCount(); got != 1 {
		t.Errorf("复位后应能重新触发一次，实际 %d 次", got)
	}
}

// TestLoadedStaysSetAfterLoaderRuns 验证 Loader 跑过一次之后闸门仍是
// 「已加载」：真实枚举体失败只记日志、同样不重试（PoC 的一次性加载语义，
// 差异记在 PoC 文档 §8.5）。改成「跑完/失败回滚 loaded」会让每次按键都
// 重读一遍磁盘，本用例会在 calls 上红。
func TestLoadedStaysSetAfterLoaderRuns(t *testing.T) {
	ResetLoadGate()
	t.Cleanup(ResetLoadGate)

	calls := make(chan int, 8)
	count := 0
	old := Loader
	Loader = func() {
		count++
		calls <- count
	}
	t.Cleanup(func() { Loader = old })

	EnsureLoaded()
	<-calls // 等枚举体落地，避免与 t.Cleanup 复原 Loader 赛跑

	EnsureLoaded()
	EnsureLoaded()
	select {
	case got := <-calls:
		t.Fatalf("闸门已置位，不该再发起枚举（第 %d 次）", got)
	default:
	}

	loadMu.Lock()
	isLoaded := loaded
	loadMu.Unlock()
	if !isLoaded {
		t.Error("EnsureLoaded 应把闸门置为已加载（成功或失败都算）")
	}
	if count != 1 {
		t.Errorf("Loader 应只被执行 1 次，实际 %d 次", count)
	}
}

// TestMaybeRefreshOnWake 锁「唤起重扫 + 防抖」三道人挡：① 首载前不强扫（保懒加载）；
// ② 防抖窗口内不重扫；③ 有扫描在跑（inFlight）不并发重扫；超窗且空闲才重扫一次。
func TestMaybeRefreshOnWake(t *testing.T) {
	ResetLoadGate()
	t.Cleanup(ResetLoadGate)
	ch := make(chan struct{}, 8)
	old := Loader
	Loader = func() { ch <- struct{}{} }
	t.Cleanup(func() { Loader = old })

	// ① 从未扫过：唤醒不触发（lastLoadAt 零值）。
	if MaybeRefreshOnWake(time.Now(), time.Minute) {
		t.Error("首载前唤醒不应强扫")
	}
	if n := LoadTriggerCount(); n != 0 {
		t.Fatalf("首载前不应有触发，累计 %d", n)
	}

	// 首载（懒加载）完成。
	EnsureLoaded()
	<-ch
	MarkLoaded() // 清 inFlight，允许后续唤醒重扫
	if n := LoadTriggerCount(); n != 1 {
		t.Fatalf("首载应触发 1 次，累计 %d", n)
	}

	// ② 防抖内不重扫。
	if MaybeRefreshOnWake(time.Now(), time.Minute) {
		t.Error("防抖窗口内不应重扫")
	}
	// ③ 超窗 → 重扫一次。
	if !MaybeRefreshOnWake(time.Now().Add(61*time.Second), time.Minute) {
		t.Error("超过防抖应发起重扫")
	}
	<-ch
	if n := LoadTriggerCount(); n != 2 {
		t.Fatalf("重扫后累计应 2，实际 %d", n)
	}
	// 上一轮未 MarkLoaded（inFlight 仍 true）→ 不并发第三轮。
	if MaybeRefreshOnWake(time.Now().Add(120*time.Second), time.Minute) {
		t.Error("inFlight 时不应再并发重扫")
	}
	if n := LoadTriggerCount(); n != 2 {
		t.Errorf("并发被挡，累计应仍 2，实际 %d", n)
	}
}
