//go:build !windows

package platform

import "log"

// SetAutostart 非 Windows 占位：开机自启走 Windows 注册表 Run 键，其它平台无对应实现
// （Linux 桌面应写 autostart desktop-entry，PoC 不做）。记日志返回 nil，不影响启动。
func SetAutostart(enabled bool) error {
	log.Printf("voke：开机自启在当前平台不支持（autostart=%v 忽略）", enabled)
	return nil
}
