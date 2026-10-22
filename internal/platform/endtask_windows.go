//go:build windows

package platform

import (
	"errors"
	"fmt"
	"strings"
	"unsafe"
)

// endtask_windows.go：窗口「结束任务」的 Win32 实现。
//
// 语义是**直接结束窗口所属进程**（TerminateProcess），不是发送关闭消息：
// 只关窗口的话，带托盘 / 后台常驻的应用会继续活着。代价是不给应用保存机会，
// 未保存数据会丢失——这是「结束任务」本身的语义。
//
// 线程：主线程 / 后台均可；只做 Process 句柄操作，不等待窗口消息，调用很快。
const processTerminate = 0x0001

var procTerminateProcess = kernel32.NewProc("TerminateProcess")

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

// EndTask 直接结束窗口所属进程。返回 nil 表示 TerminateProcess 成功提交；
// 失败（窗口已关、受保护、权限不足）时面板应保留并提示用户。
//
// 只杀窗口所属的那一个进程：同一进程的其它窗口会一起消失，子进程不会自动清理。
func EndTask(hwnd uintptr) error {
	if hwnd == 0 {
		return errors.New("无效的窗口")
	}
	pid, exe, err := windowProcess(hwnd)
	if err != nil {
		return err
	}
	h, _, openErr := procOpenProcess.Call(processTerminate|processQueryLim, 0, uintptr(pid))
	if h == 0 {
		return fmt.Errorf("打开进程 %s(%d) 失败（可能需要管理员权限）：%v", exe, pid, openErr)
	}
	defer procCloseHandle.Call(h)
	if ok, _, termErr := procTerminateProcess.Call(h, 1); ok == 0 {
		return fmt.Errorf("结束进程 %s(%d) 失败：%v", exe, pid, termErr)
	}
	return nil
}
