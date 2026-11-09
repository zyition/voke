// launch.go：通用进程启动（os/exec），供 provider 的 Launch / LaunchTerminalProfile
// 动作使用。对照 voke-sys 的 exec 侧：`noConsole` 在 Windows 上加 CREATE_NO_WINDOW
// （编辑器 CLI 如 Zed / code.cmd 是控制台子系统，直启会闪黑框）；跨平台可编译，Linux
// 上 noConsole 无意义（attr 为 nil）。
//
// 与 shell.LaunchApp（apps 专用，走 mygo Shell、必须主线程）不同：本函数是纯 os/exec，
// 可在后台线程调用，用于 provider 结果行「用某个 exe + 参数打开」的通用动作。
package platform

import (
	"log"
	"os/exec"
)

// Launch 以 exe + args 启动一个进程（fire-and-forget：成功返回即不等其退出）。
// exe 为空视为无操作（配置/数据缺失时的兜底，不 panic）。
func Launch(exe string, args []string, noConsole bool) error {
	if exe == "" {
		return nil
	}
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = noConsoleAttr(noConsole)
	if err := cmd.Start(); err != nil {
		log.Printf("voke 启动 %q 失败：%v", exe, err)
		return err
	}
	// fire-and-forget，但必须显式交还进程资源，否则 Windows 每次启动泄漏一个
	// 进程句柄（等 GC），Unix 侧会留下没人回收的僵尸进程。
	releaseLaunched(cmd)
	return nil
}
