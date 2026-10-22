//go:build windows

package platform

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unsafe"
)

// endtask_windows.go：窗口「结束任务」的 Win32 实现。
//
// 行为对齐 Windows 任务栏的「结束任务」：先 PostMessage(WM_CLOSE) 让应用自己退出
// （可弹保存确认）；调用方在后台等待一段宽限时间，窗口仍存在且 IsHungAppWindow
// 判定无响应时，才 TerminateProcess 强杀。响应正常的应用永远不会被强杀。
//
// 线程：RequestEndTask 主线程 / 后台均可（PostMessage 不阻塞）；ForceEndTaskIfHung
// 会 sleep 到宽限期结束，**只许后台**（app 层用 goroutine 调）。
const (
	wmClose          = 0x0010
	processTerminate = 0x0001
)

var (
	procPostMessageW     = user32.NewProc("PostMessageW")
	procIsHungAppWindow  = user32.NewProc("IsHungAppWindow")
	procTerminateProcess = kernel32.NewProc("TerminateProcess")
)

// windowProcess 解析窗口所属进程并做安全闸门（自身 / 核心系统进程 / 取不到映像）。
func windowProcess(hwnd uintptr) (uint32, string, error) {
	if ok, _, _ := procIsWindow.Call(hwnd); ok == 0 {
		return 0, "", errors.New("窗口已关闭")
	}
	var pid uint32
	procGetWindowThreadProcId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	exe := strings.ToLower(ProcessNameFromPath(processPathForPid(pid)))
	if reason := endTaskDeniedReason(pid, exe); reason != "" {
		return 0, "", errors.New(reason)
	}
	return pid, exe, nil
}

// RequestEndTask 向窗口发送 WM_CLOSE（优雅退出请求）。成功只代表消息已投递，
// 不代表应用已经退出；返回错误时面板应保留并提示用户。
func RequestEndTask(hwnd uintptr) error {
	if hwnd == 0 {
		return errors.New("无效的窗口")
	}
	if _, _, err := windowProcess(hwnd); err != nil {
		return err
	}
	ok, _, callErr := procPostMessageW.Call(hwnd, wmClose, 0, 0)
	if ok == 0 {
		return fmt.Errorf("发送关闭请求失败：%v", callErr)
	}
	return nil
}

// ForceEndTaskIfHung 在 grace 宽限期内等待窗口自行退出；若窗口仍存在且被
// IsHungAppWindow 判为无响应，则 TerminateProcess。返回 true 表示执行了强杀。
//
// 只杀窗口所属的那一个进程：同一进程的其它窗口会一起消失，子进程不会自动清理。
// 判断前重新解析 PID，避免句柄在等待期间被复用后误杀新进程。
func ForceEndTaskIfHung(hwnd uintptr, grace time.Duration) (bool, error) {
	deadline := time.Now().Add(grace)
	for {
		if ok, _, _ := procIsWindow.Call(hwnd); ok == 0 {
			return false, nil
		}
		if !time.Now().Before(deadline) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	hung, _, _ := procIsHungAppWindow.Call(hwnd)
	if hung == 0 {
		return false, nil // 仍存活但响应正常：尊重它自己的关闭流程
	}
	pid, exe, err := windowProcess(hwnd)
	if err != nil {
		return false, err
	}
	h, _, openErr := procOpenProcess.Call(processTerminate|processQueryLim, 0, uintptr(pid))
	if h == 0 {
		return false, fmt.Errorf("打开进程 %s(%d) 失败（可能需要管理员权限）：%v", exe, pid, openErr)
	}
	defer procCloseHandle.Call(h)
	if ok, _, termErr := procTerminateProcess.Call(h, 1); ok == 0 {
		return false, fmt.Errorf("结束进程 %s(%d) 失败：%v", exe, pid, termErr)
	}
	return true, nil
}
