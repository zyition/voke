// theme.go：外观主题（浅色 / 深色 / 跟随系统）的 mygo 薄封装（§7.2 装配层，与
// hotkey/tray 同「只包一层」纪律）。构建标签不加（mygo Theme 面每平台都有后端）。
package platform

import "github.com/egoist/mygo"

// SetThemeSource 强制浅色 / 深色外观，或跟随系统。code 取 "system" / "light" / "dark"
// （与 appcfg.Theme 的码值逐字相同，故调用方直接 string(theme) 传进来，本层不再映射）。
//
// 转发 `mygo.Theme.SetSource`（modules.go:473，收 mygo.ThemeSource）：
//   - 可在 App.Run **之前**调（用存好的偏好让 app 直接以该外观启动）；运行中调则
//     即时 apply + 刷新背景（modules.go:477-489），故设置窗「保存」里当场生效；
//   - `s == ""` 被 mygo 归一成 ThemeSystem（:475），本层不重复兜底。
//
// 线程模型：SetSource 内部走 onMain（:481），非主线程会投递主循环；ready()（WhenReady）
// 与 saveSettings（托盘 Click→主线程）都在主线程，天然满足。
func SetThemeSource(code string) {
	mygo.Theme.SetSource(mygo.ThemeSource(code))
}
