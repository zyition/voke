// closewindow.go：窗口 provider 的「关闭窗口」动作——向窗口投递 WM_CLOSE，
// 等价点击标题栏关闭按钮。与 end-task 的区别：给应用保存 / 确认的机会，
// 不强制结束进程；应用也可能拒绝关闭。
package app

import (
	"log"

	"voke/internal/platform"
)

// closeWindow 请求关闭窗口。返回 true 表示 WM_CLOSE 已投递（调用方隐藏并复位
// 面板）；false 表示窗口句柄无效或权限不足，面板保留并提示。
func (st *appState) closeWindow(hwnd uintptr, title string) bool {
	if !platform.CloseWindow(hwnd) {
		log.Printf("voke：关闭窗口失败（窗口 %q，hwnd=%#x）", title, hwnd)
		st.setNotice("无法关闭窗口（窗口可能已退出或权限不足）")
		return false
	}
	log.Printf("voke：已请求关闭窗口 %q（hwnd=%#x）", title, hwnd)
	return true
}
