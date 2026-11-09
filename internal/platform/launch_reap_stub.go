//go:build !windows

package platform

import "os/exec"

// releaseLaunched 在 Unix 上异步 Wait：Release 不会回收僵尸，必须有人 wait
// 掉子进程的退出状态。启动器不需要结果，goroutine 自行消化错误。
func releaseLaunched(cmd *exec.Cmd) {
	go func() { _ = cmd.Wait() }()
}
