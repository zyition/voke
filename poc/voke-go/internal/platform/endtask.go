// endtask.go：窗口「结束任务」的跨平台判定（纯数据 / 纯函数，Linux 可测）。
// Win32 的 TerminateProcess 实现在 endtask_windows.go。
package platform

import (
	"os"
	"strings"
)

// protectedProcessNames 是不允许从启动器结束的核心系统进程。强杀这些进程轻则丢
// 用户会话、重则蓝屏，且多数本来就会被系统权限拒绝；这里先挡住并给出明确提示。
// 按进程文件名（小写）比较，不按路径——不同 Windows 目录布局下更稳定。
var protectedProcessNames = map[string]bool{
	"system":                    true,
	"registry":                  true,
	"memory compression":        true,
	"smss.exe":                  true,
	"csrss.exe":                 true,
	"wininit.exe":               true,
	"winlogon.exe":              true,
	"services.exe":              true,
	"lsass.exe":                 true,
	"lsaiso.exe":                true,
	"svchost.exe":               true,
	"dwm.exe":                   true,
	"fontdrvhost.exe":           true,
	"securityhealthservice.exe": true,
}

// endTaskDeniedReason 返回拒绝结束的原因（空串 = 允许）。名字为空表示取不到进程
// 映像（受保护进程 / 权限不足），同样拒绝——宁可不杀，也不要误杀。
func endTaskDeniedReason(pid uint32, exe string) string {
	if pid == 0 {
		return "无效的进程"
	}
	if pid == uint32(os.Getpid()) {
		return "不能结束 voke 自身"
	}
	name := strings.ToLower(strings.TrimSpace(exe))
	if name == "" {
		return "无法识别目标进程"
	}
	if protectedProcessNames[name] {
		return "受保护的系统进程"
	}
	return ""
}
