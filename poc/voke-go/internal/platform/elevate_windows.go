//go:build windows

package platform

import (
	"errors"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// elevate_windows.go：提权两件事——
//   - IsProcessElevated：判断窗口所属进程是否以管理员权限运行，供剪贴板粘贴
//     在 UIPI 静默拦截前分流；
//   - RunElevated：ShellExecuteEx(verb="runas") 拉起提权进程。UAC 同意后返回，
//     被拒绝时返回 ErrElevationCanceled。
//
// 线程：IsProcessElevated / RunElevated 可在主线程调用（UAC 期间会阻塞到用户选择，
// 调用方通常已准备隐藏面板）。onExit 回调在**后台 goroutine**触发，UI 调用方需自行
// 经 mygo.RunOnMain 回主线程。

const (
	// tokenQuery / tokenElevation 是 OpenProcessToken + GetTokenInformation 的常量。
	tokenQuery     = 0x0008
	tokenElevation = 20
	// seeMaskNoCloseProcess / seeMaskFlagNoUI 对应同名 SHELLEXECUTEINFO 标志：
	// 要进程句柄以便等退出码；错误用返回值/日志表达，不让 Shell 再弹一个框。
	seeMaskNoCloseProcess = 0x00000040
	seeMaskFlagNoUI       = 0x00000400
)

// tokenElevationInfo 对应 TOKEN_ELEVATION。
type tokenElevationInfo struct {
	TokenIsElevated uint32
}

// shellExecuteInfo 对应 x64 SHELLEXECUTEINFOW 的布局（指针字段自然对齐）。
// cbSize 错一字节 ShellExecuteEx 就会拒绝；两条断言钉住 112 字节。
type shellExecuteInfo struct {
	cbSize       uint32
	fMask        uint32
	hwnd         uintptr
	lpVerb       *uint16
	lpFile       *uint16
	lpParameters *uint16
	lpDirectory  *uint16
	nShow        int32
	hInstApp     uintptr
	lpIDList     uintptr
	lpClass      *uint16
	hkeyClass    uintptr
	dwHotKey     uint32
	hIcon        uintptr
	hProcess     uintptr
}

var (
	_ = [unsafe.Sizeof(shellExecuteInfo{}) - 112]byte{}
	_ = [112 - unsafe.Sizeof(shellExecuteInfo{})]byte{}
)

var (
	shell32                 = windows.NewLazySystemDLL("shell32.dll")
	procShellExecuteExW     = shell32.NewProc("ShellExecuteExW")
	procOpenProcessToken    = advapi32.NewProc("OpenProcessToken") // advapi32 见 autostart_windows.go
	procGetTokenInformation = advapi32.NewProc("GetTokenInformation")
)

// IsProcessElevated 报告 pid 是否以管理员权限运行。取不到（受保护进程 / 已退出）
// 时返回 false——宁可走普通粘贴路径，也不误触发 UAC。
func IsProcessElevated(pid uint32) bool {
	if pid == 0 {
		return false
	}
	h, _, _ := procOpenProcess.Call(processQueryLim, 0, uintptr(pid))
	if h == 0 {
		return false
	}
	defer procCloseHandle.Call(h)
	var token uintptr
	if ok, _, _ := procOpenProcessToken.Call(h, tokenQuery, uintptr(unsafe.Pointer(&token))); ok == 0 {
		return false
	}
	defer procCloseHandle.Call(token)
	var info tokenElevationInfo
	var ret uint32
	if ok, _, _ := procGetTokenInformation.Call(token, tokenElevation,
		uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info), uintptr(unsafe.Pointer(&ret))); ok == 0 {
		return false
	}
	return info.TokenIsElevated != 0
}

// CurrentProcessElevated 报告 voke 自身是否已提权。已提权时可直接 SendInput 到
// 管理员窗口，无需再走辅助进程。
func CurrentProcessElevated() bool { return IsProcessElevated(uint32(os.Getpid())) }

// TargetNeedsElevation 判断粘贴目标是否是“比 voke 更高权限”的窗口：目标已提权且
// voke 未提权时，SendInput 会被 UIPI 静默拦截（返回值还可能是成功），必须改用
// 提权辅助进程。
func TargetNeedsElevation(t ClipTarget) bool {
	return t.Valid() && t.PID != 0 && !CurrentProcessElevated() && IsProcessElevated(t.PID)
}

// RunElevated 以管理员身份启动 exe。hidden 控制新进程窗口是否隐藏（提权粘贴辅助
// 进程是 GUI 程序、无窗口，用 true）。成功返回 nil；UAC 被拒返回 ErrElevationCanceled。
//
// onExit 非 nil 时在后台 goroutine 等到进程退出并回调退出码（0=成功）。终端启动
// 可传 nil；提权粘贴用它把辅助进程的失败回显到面板。
func RunElevated(exe string, args []string, hidden bool, onExit func(code uint32)) error {
	verb, err := windows.UTF16PtrFromString("runas")
	if err != nil {
		return err
	}
	file, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return err
	}
	var params *uint16
	if len(args) > 0 {
		params, err = windows.UTF16PtrFromString(windows.ComposeCommandLine(args))
		if err != nil {
			return err
		}
	}
	show := int32(windows.SW_SHOWNORMAL)
	if hidden {
		show = int32(windows.SW_HIDE)
	}
	info := shellExecuteInfo{
		fMask:        seeMaskNoCloseProcess | seeMaskFlagNoUI,
		lpVerb:       verb,
		lpFile:       file,
		lpParameters: params,
		nShow:        show,
	}
	info.cbSize = uint32(unsafe.Sizeof(info))
	ok, _, callErr := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&info)))
	if ok == 0 {
		if errors.Is(callErr, windows.ERROR_CANCELLED) {
			return ErrElevationCanceled
		}
		return fmt.Errorf("ShellExecuteEx(%s) 失败：%v", exe, callErr)
	}
	if info.hProcess == 0 {
		return nil // 没有句柄可等（例如 verb 打开了文档）；启动本身已成功
	}
	handle := windows.Handle(info.hProcess)
	go func() {
		windows.WaitForSingleObject(handle, windows.INFINITE)
		var code uint32
		_ = windows.GetExitCodeProcess(handle, &code)
		windows.CloseHandle(handle)
		if onExit != nil {
			onExit(code)
		}
	}()
	return nil
}
