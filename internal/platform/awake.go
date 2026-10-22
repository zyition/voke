// awake.go：保持系统唤醒的调度状态机（跨平台部分）。平台相关的置位 / 清位
// （assertExecutionState / clearExecutionState）在 awake_windows.go（kernel32
// SetThreadExecutionState）与 awake_stub.go（非 Windows 记日志）里。
//
// 对照 voke-sys/src/awake.rs：SetThreadExecutionState 的置位会「粘住」，需要周期性重申
// 并在到期时清除——因此这里跑一个后台 goroutine 每 30s 重申一次，minutes!=nil 时在到期
// 点自动清位返回；CancelAwake 立即清位停线程。重复 SetAwake 重置（换时长 / 换是否连屏）。
//
// 激活状态（active / keepScreen / expiresAt）在 Go 侧独立跟踪，不依赖 OS：故非 Windows
// （置位是 stub）也能被 AwakeState 查到，供 awake provider 的 fetcher 与托盘菜单反映真实态。
package platform

import (
	"log"
	"sync"
	"time"
)

var (
	awakeMu         sync.Mutex
	awakeStop       chan struct{} // 非 nil = 有活动重申 goroutine
	awakeActive     bool
	awakeKeepScreen bool
	awakeExpiresAt  *int64 // unix 秒；nil = 永久
)

// AwakeState 返回当前保持唤醒状态（active / 是否连屏 / 到期时刻）。
func AwakeState() (active, keepScreen bool, expiresAt *int64) {
	awakeMu.Lock()
	defer awakeMu.Unlock()
	return awakeActive, awakeKeepScreen, awakeExpiresAt
}

// SetAwake 开始 / 重置保持唤醒。minutes=nil 永久；否则 n 分钟后自动恢复。keepScreen=true
// 连显示器一起保持。
func SetAwake(minutes *int, keepScreen bool) {
	awakeMu.Lock()
	defer awakeMu.Unlock()
	if awakeStop != nil {
		close(awakeStop) // 停掉上一个重申循环（会各自清位）
	}
	stop := make(chan struct{})
	awakeStop = stop
	awakeActive, awakeKeepScreen = true, keepScreen
	awakeExpiresAt = nil
	if minutes != nil {
		e := time.Now().Unix() + int64(*minutes)*60
		awakeExpiresAt = &e
	}

	go func() {
		assertExecutionState(keepScreen) // 立即置位
		var deadline <-chan time.Time
		if minutes != nil {
			t := time.NewTimer(time.Duration(*minutes) * time.Minute)
			defer t.Stop()
			deadline = t.C
		}
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				// 被 CancelAwake / 新 SetAwake 取消：**不在这里清位**——取消方（Cancel）
				// 或新循环（Set）负责置位；老循环再清会与新一轮置位竞态、把唤醒误关。
				return
			case <-deadline:
				awakeMu.Lock()
				owner := awakeStop == stop
				if owner {
					awakeStop, awakeActive, awakeExpiresAt = nil, false, nil
				}
				awakeMu.Unlock()
				if !owner {
					return // 已被新 SetAwake 替换：状态归新循环，别清
				}
				clearExecutionState()
				log.Printf("voke：保持唤醒到期，已恢复正常休眠")
				return
			case <-ticker.C:
				// 仅在仍是当前会话时重申；被替换的老循环不得再置位。
				awakeMu.Lock()
				owner := awakeStop == stop
				awakeMu.Unlock()
				if owner {
					assertExecutionState(keepScreen)
				}
			}
		}
	}()
}

// CancelAwake 立即取消保持唤醒。
func CancelAwake() {
	awakeMu.Lock()
	defer awakeMu.Unlock()
	if awakeStop != nil {
		close(awakeStop)
		awakeStop = nil
	}
	awakeActive, awakeExpiresAt = false, nil
	clearExecutionState() // 兜底清位（重申循环里也会清，这里保证无活动线程时也清）
}
