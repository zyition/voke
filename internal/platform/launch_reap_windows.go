//go:build windows

package platform

import "os/exec"

// releaseLaunched 交还已启动进程的句柄：Windows 上 Release 关闭 Process 持有的
// 进程 / 线程句柄。启动器不关心退出码，释放后不再引用该进程。
func releaseLaunched(cmd *exec.Cmd) {
	_ = cmd.Process.Release()
}
