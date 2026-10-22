//go:build windows

package platform

import "syscall"

// SetThreadExecutionState 标志（winbase.h）。
const (
	esContinuous      = 0x80000000
	esSystemRequired  = 0x00000001
	esDisplayRequired = 0x00000002
)

var procSetThreadExecutionState = syscall.NewLazyDLL("kernel32.dll").NewProc("SetThreadExecutionState")

// assertExecutionState 置「系统不休眠」（keepScreen 时连显示器一起）。ES_CONTINUOUS +
// 所需标志；周期性重申由 awake.go 的 goroutine 负责。
func assertExecutionState(keepScreen bool) {
	flags := uintptr(esContinuous | esSystemRequired)
	if keepScreen {
		flags |= uintptr(esDisplayRequired)
	}
	procSetThreadExecutionState.Call(flags)
}

// clearExecutionState 仅带 ES_CONTINUOUS = 清除所有保持条件，恢复正常休眠策略。
func clearExecutionState() {
	procSetThreadExecutionState.Call(uintptr(esContinuous))
}
