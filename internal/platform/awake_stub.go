//go:build !windows

package platform

// assertExecutionState 非 Windows 占位：保持唤醒是 Windows 电源模型（SetThreadExecutionState），
// Linux/macOS 下无对应，什么都不做（与 clearExecutionState 一致）。
//
// 不在此打日志：SetAwake 在后台 goroutine 里调它，那行日志可能延迟到**下一个测试**
// 才落到全局 log；若届时 InitLog 已把 log 指向某个测试的日志文件，就会污染该用例
// 「文件恰好 N 行」的断言。而且重申循环每 30s 都会刷一行，纯属噪音。
func assertExecutionState(keepScreen bool) {}

// clearExecutionState 非 Windows 占位。
func clearExecutionState() {}
