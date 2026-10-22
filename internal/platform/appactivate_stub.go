//go:build !windows

package platform

import "log"

// ActivateApp 非 Windows 占位：Store 应用 / App Activation 是 Windows 概念（无 COM
// IApplicationActivationManager）。对照 voke-sys 非 windows 分支：记日志、返回 (0,false)。
// execPlatformAction 据 false 走「兜底直启」，Linux 上兜底路径也不存在 → 整体 no-op。
func ActivateApp(aumid, arguments string) (uint32, bool) {
	log.Printf("voke：当前平台不激活 Store 应用（aumid=%s 参数=%q）", aumid, arguments)
	return 0, false
}
