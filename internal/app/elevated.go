// elevated.go：提权粘贴辅助进程的入口（同一个 voke.exe 以管理员身份再跑一次）。
//
// 为什么需要它：UIPI 会**静默**拦截低权限进程发往管理员窗口的 SendInput——
// SendInput 甚至可能返回成功，所以主进程既粘贴不进去、也拿不到失败信号。Ditto 等
// 剪贴板工具的做法是拉起一个临时提权进程代为发送按键；这里复用同一思路：
// 主进程 ShellExecuteEx(runas) 启动 `voke.exe --elevated-paste <hwnd> <gui|terminal>`，
// 辅助进程等 UAC 结束后把前台切回目标、发送 Ctrl+V / Shift+Insert，然后退出。
//
// 安全边界：辅助进程只做“粘贴”这一件事（不发送回车、不接受任意命令），并复核
// 目标窗口确实属于管理员进程；但它仍是一个 UAC 门控的按键注入原语——调用前应确保
// 是用户主动选择粘贴。
package app

import (
	"log"
	"strconv"
	"time"

	"voke/internal/platform"
)

// elevatedPasteFlag 是辅助进程的专用参数；必须在单实例锁之前处理。
const elevatedPasteFlag = "--elevated-paste"

// elevatedPasteDelay 给 UAC / 前台切换留一点收尾时间，避免按键发到正在消失的
// 安全桌面或 Voke 面板。
const elevatedPasteDelay = 120 * time.Millisecond

// HandleElevatedPaste 解析 `--elevated-paste <hwnd> <gui|terminal>`：不是该模式
// 返回 handled=false；是则执行粘贴并返回进程退出码（0 成功、1 失败、2 参数错误）。
func HandleElevatedPaste(args []string) (handled bool, code int) {
	if len(args) == 0 || args[0] != elevatedPasteFlag {
		return false, 0
	}
	if len(args) != 3 {
		log.Printf("voke：提权粘贴参数不完整：%v", args)
		return true, 2
	}
	hwnd, err := strconv.ParseUint(args[1], 10, 64)
	if err != nil || hwnd == 0 {
		log.Printf("voke：提权粘贴窗口句柄非法：%q", args[1])
		return true, 2
	}
	if args[2] != "gui" && args[2] != "terminal" {
		log.Printf("voke：提权粘贴模式非法：%q", args[2])
		return true, 2
	}
	target := platform.ClipTarget{
		Hwnd:     uintptr(hwnd),
		PID:      platform.WindowProcessID(uintptr(hwnd)),
		Terminal: args[2] == "terminal",
	}
	if target.PID == 0 || !platform.IsProcessElevated(target.PID) {
		log.Printf("voke：提权粘贴目标不是管理员进程，拒绝执行（hwnd=%#x pid=%d）",
			target.Hwnd, target.PID)
		return true, 1
	}
	time.Sleep(elevatedPasteDelay)
	if !platform.PasteToTarget(target, false) {
		log.Printf("voke：提权粘贴失败（hwnd=%#x terminal=%v）", target.Hwnd, target.Terminal)
		return true, 1
	}
	log.Printf("voke：提权粘贴完成（hwnd=%#x terminal=%v）", target.Hwnd, target.Terminal)
	return true, 0
}
