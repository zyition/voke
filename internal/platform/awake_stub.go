//go:build !windows

package platform

import "log"

// assertExecutionState 非 Windows 占位：保持唤醒是 Windows 电源模型（SetThreadExecutionState），
// Linux/macOS 下无对应，记日志即可（与 mem_stub.go 的「当前平台不提供」口径一致）。
func assertExecutionState(keepScreen bool) {
	log.Printf("voke：保持唤醒在当前平台不提供（assert 为占位）")
}

// clearExecutionState 非 Windows 占位。
func clearExecutionState() {}
