//go:build windows

package platform

import (
	"os"
	"sync"
	"syscall"
	"unsafe"
)

// windows_enum_windows.go：Win32 顶层窗口枚举（voke-sys/src/windows.rs 的 `#[cfg(windows)]` 半边
// 的 Go 对照）。纯过滤规则在 windowfilter.go（Linux 可测），这里只「把系统状态变成数据」：
// EnumWindows 同步回调 → 每窗取可见性/pid/标题/类名/cloaked/扩展样式 → 喂 ShouldIncludeWindow →
// 过了再取进程路径（最贵，最后做）。字段与 windows provider 的 Entry 对齐：枚举产出
// ProcessPath（进程 exe），provider 把它放进 Match.Icon 作「图标来源路径」，渲染层经
// platform.AppIcon 懒取位图 + 缓存（O4 模型，见 internal/ui/provider_results.go）。
//
// **只应串行调用**：EnumWindows 是同线程**同步回调**（不需 COM 套间、不排消息），窗口多时阻塞
// 几十~几百毫秒。syscall.NewCallback 的回调就在调用线程上逐个执行，故用包内 sink + 互斥串行化。

// WindowInfo 是一条可切换顶层窗口的系统侧数据。
type WindowInfo struct {
	Hwnd        int64
	Title       string
	ProcessName string
	ProcessPath string
}

const (
	gwlExStyle       = 0xFFFFFFEC // GWLP_EXSTYLE = -20（32 位补码；callee 按 int 读低 32 位）
	dwmwaCloaked     = 14         // DWMWA_CLOAKED
	processQueryLim  = 0x00001000 // PROCESS_QUERY_LIMITED_INFORMATION
	processNameWin32 = 0          // QueryFullProcessImageNameW 的 dwFlags
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	dwmapi   = syscall.NewLazyDLL("dwmapi.dll")

	procEnumWindows             = user32.NewProc("EnumWindows")
	procIsWindowVisible         = user32.NewProc("IsWindowVisible")
	procGetWindowThreadProcId   = user32.NewProc("GetWindowThreadProcessId")
	procGetWindowTextW          = user32.NewProc("GetWindowTextW")
	procGetClassNameW           = user32.NewProc("GetClassNameW")
	procGetWindowLongPtrW       = user32.NewProc("GetWindowLongPtrW")
	procDwmGetWindowAttribute   = dwmapi.NewProc("DwmGetWindowAttribute")
	procOpenProcess             = kernel32.NewProc("OpenProcess")
	procQueryFullProcImageNameW = kernel32.NewProc("QueryFullProcessImageNameW")
	procCloseHandle             = kernel32.NewProc("CloseHandle")
)

var (
	enumMu   sync.Mutex
	enumSink []WindowInfo
)

// EnumerateWindows 枚举当前可切换的顶层窗口（返回顺序是系统 Z 序，**不**在此排序——排序归 core）。
func EnumerateWindows() []WindowInfo {
	enumMu.Lock()
	defer enumMu.Unlock()
	enumSink = nil
	procEnumWindows.Call(syscall.NewCallback(enumWindowsProc), 0)
	out := enumSink
	enumSink = nil
	return out
}

// enumWindowsProc 是 EnumWindows 回调：收则 append，恒返回 1（TRUE）继续枚举。跨 FFI 不 panic。
func enumWindowsProc(hwnd, _ uintptr) uintptr {
	if w, ok := windowInfo(hwnd); ok {
		enumSink = append(enumSink, w)
	}
	return 1
}

// windowInfo 取单窗各字段、过过滤链、（过了才）取进程信息。任一取不到按「跳过 / 留空」处理。
func windowInfo(hwnd uintptr) (WindowInfo, bool) {
	visible, _, _ := procIsWindowVisible.Call(hwnd)

	var pid uint32
	procGetWindowThreadProcId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	ownPid := uint32(os.Getpid())

	title := windowText(hwnd)
	class := windowClass(hwnd)
	cloaked := isCloaked(hwnd)

	exStyle, _, _ := procGetWindowLongPtrW.Call(hwnd, gwlExStyle)
	tool := HasExtStyle(int64(exStyle), WsExToolWindow)
	app := HasExtStyle(int64(exStyle), WsExAppWindow)

	if !ShouldIncludeWindow(visible != 0, cloaked, title, class, pid, ownPid, tool, app) {
		return WindowInfo{}, false
	}

	path := processPathForPid(pid)
	return WindowInfo{Hwnd: int64(hwnd), Title: title, ProcessName: ProcessNameFromPath(path), ProcessPath: path}, true
}

func windowText(hwnd uintptr) string {
	var buf [512]uint16
	r, _, _ := procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if r == 0 {
		return ""
	}
	n := int(r)
	if n > len(buf) {
		n = len(buf)
	}
	return syscall.UTF16ToString(buf[:n])
}

func windowClass(hwnd uintptr) string {
	var buf [256]uint16
	r, _, _ := procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if r == 0 {
		return ""
	}
	n := int(r)
	if n > len(buf) {
		n = len(buf)
	}
	return syscall.UTF16ToString(buf[:n])
}

// isCloaked：DWMWA_CLOAKED；调用失败（非 S_OK）按未 cloaked（宁不误杀）。
func isCloaked(hwnd uintptr) bool {
	var cloaked uint32
	r, _, _ := procDwmGetWindowAttribute.Call(hwnd, dwmwaCloaked, uintptr(unsafe.Pointer(&cloaked)), 4)
	return r == 0 && cloaked != 0
}

// processPathForPid：OpenProcess(QUERY_LIMITED) + QueryFullProcessImageNameW；受保护进程取不到
// 返回空串（常态非错误）。缓冲从 512 起、按返回的所需长度最多扩两轮。
func processPathForPid(pid uint32) string {
	if pid == 0 {
		return ""
	}
	h, _, _ := procOpenProcess.Call(processQueryLim, 0, uintptr(pid))
	if h == 0 {
		return ""
	}
	defer procCloseHandle.Call(h)

	bufLen := uint32(512)
	for range 3 {
		buf := make([]uint16, bufLen)
		size := bufLen
		r, _, _ := procQueryFullProcImageNameW.Call(h, processNameWin32, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
		if r != 0 {
			if int(size) > len(buf) {
				size = bufLen
			}
			return syscall.UTF16ToString(buf[:size])
		}
		needed := size + 1 // 失败时 size 被写成所需长度（含 NUL）
		if needed <= bufLen {
			break
		}
		bufLen = needed
	}
	return ""
}
