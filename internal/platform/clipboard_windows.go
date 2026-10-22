//go:build windows

package platform

import (
	"strings"
	"unsafe"
)

// clipboard_windows.go：剪贴板监控与「粘贴到原窗口」的 Win32 半边。
//
// 只做三件事：
//   - ClipboardSequence：GetClipboardSequenceNumber，供后台轮询判断剪贴板是否变化；
//   - ForegroundTarget：唤起面板前记住前台窗口（句柄 / 类名 / 进程名 / 是否终端）；
//   - PasteToTarget：恢复目标窗口前台并发送 Ctrl+V 或 Shift+Insert。
//
// 内容读写（文本/HTML/RTF/PNG）走 mygo 的跨平台 Clipboard，在 clipboard.go。

const (
	vkShift        = 0x10
	vkControl      = 0x11
	vkInsert       = 0x2D
	vkV            = 0x56
	keyeventfKeyup = 0x0002
)

// keybdInput 对应 Win32 KEYBDINPUT；input 按 x64 INPUT 的 40 字节布局对齐
// （type 4 + 对齐 4 + 联合体 32，联合体以 MOUSEINPUT 为最大成员）。
type keybdInput struct {
	wVk         uint16
	wScan       uint16
	dwFlags     uint32
	time        uint32
	dwExtraInfo uintptr
}

type input struct {
	typ uint32
	_   uint32
	ki  keybdInput
	_   [8]byte // 补齐到 MOUSEINPUT 的联合体尺寸
}

// 编译期钉住 x64 INPUT 的 40 字节布局（SendInput 按 cbSize 解释数组，错一字节
// 就会静默丢掉按键）。两条断言一上一下，任何方向偏差都直接编译失败。
var (
	_ = [unsafe.Sizeof(input{}) - 40]byte{}
	_ = [40 - unsafe.Sizeof(input{})]byte{}
)

var (
	procGetClipboardSequenceNumber = user32.NewProc("GetClipboardSequenceNumber")
	procGetForegroundWindow        = user32.NewProc("GetForegroundWindow")
	procIsWindow                   = user32.NewProc("IsWindow")
	procIsIconic                   = user32.NewProc("IsIconic")
	procSendInput                  = user32.NewProc("SendInput")
	procAttachThreadInput          = user32.NewProc("AttachThreadInput")
	procGetCurrentThreadId         = kernel32.NewProc("GetCurrentThreadId")
)

// ClipboardSequence 返回系统剪贴板序列号：内容/所有权每次变化都会递增。
// 只读取一个 DWORD，任意线程可调；后台轮询用它做「是否值得回主线程读内容」的闸门。
func ClipboardSequence() uint32 {
	n, _, _ := procGetClipboardSequenceNumber.Call()
	return uint32(n)
}

// WindowProcessID 返回窗口所属进程 id；窗口无效或取不到时返回 0。
// 提权粘贴辅助进程用它复核目标确实是管理员进程。
func WindowProcessID(hwnd uintptr) uint32 {
	if hwnd == 0 {
		return 0
	}
	var pid uint32
	procGetWindowThreadProcId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	return pid
}

// ForegroundTarget 取当前前台窗口的信息。主线程调用（唤起面板前）。
// 取不到进程名不算失败——类名足以识别控制台类终端。
func ForegroundTarget() ClipTarget {
	hwnd, _, _ := procGetForegroundWindow.Call()
	if hwnd == 0 {
		return ClipTarget{}
	}
	class := windowClass(hwnd)
	var pid uint32
	procGetWindowThreadProcId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	exe := strings.ToLower(ProcessNameFromPath(processPathForPid(pid)))
	return ClipTarget{
		Hwnd:     hwnd,
		PID:      pid,
		Class:    class,
		Exe:      exe,
		Terminal: IsTerminalWindow(class, exe),
	}
}

// PasteToTarget 把目标窗口切回前台并发送一次粘贴按键。plain 只影响剪贴板里
// 写了什么（调用方已写好），按键本身与格式无关：终端恒用 Shift+Insert，其余
// 窗口用 Ctrl+V。
//
// 返回 false 表示没有把握粘贴已送达：窗口已销毁、前台切换被系统拒绝、或
// SendInput 被 UIPI 拦下（目标以管理员权限运行）。调用方据此保留面板并提示
// 「内容已复制，请手动粘贴」——剪贴板里已经是正确内容，不会丢。
//
// 线程：只许主线程。SetForegroundWindow / AttachThreadInput 都是线程亲和调用；
// SendInput 本身线程无关，但整套动作必须在面板隐藏后由主线程按序执行。
func PasteToTarget(t ClipTarget, plain bool) bool {
	_ = plain // 按键选择只看目标是不是终端；格式差异已在写剪贴板时体现
	if !t.Valid() {
		return false
	}
	if ok, _, _ := procIsWindow.Call(t.Hwnd); ok == 0 {
		return false
	}
	if iconic, _, _ := procIsIconic.Call(t.Hwnd); iconic != 0 {
		procShowWindow.Call(t.Hwnd, swRestore)
	}
	if !forceForeground(t.Hwnd) {
		return false
	}

	var mod, key uint16
	if t.Terminal {
		mod, key = vkShift, vkInsert
	} else {
		mod, key = vkControl, vkV
	}
	events := []input{
		{typ: 1, ki: keybdInput{wVk: mod}},
		{typ: 1, ki: keybdInput{wVk: key}},
		{typ: 1, ki: keybdInput{wVk: key, dwFlags: keyeventfKeyup}},
		{typ: 1, ki: keybdInput{wVk: mod, dwFlags: keyeventfKeyup}},
	}
	n, _, _ := procSendInput.Call(uintptr(len(events)), uintptr(unsafe.Pointer(&events[0])), unsafe.Sizeof(input{}))
	return int(n) == len(events)
}

// forceForeground 尽力把 hwnd 提到前台。最可靠的路径是「本进程此刻仍是前台
// 窗口的拥有者」；面板刚隐藏时通常成立。若已切走，用 AttachThreadInput 把
// 当前线程与前台/目标线程临时挂在一起，绕过 SetForegroundWindow 的前台锁限制。
func forceForeground(hwnd uintptr) bool {
	fg, _, _ := procGetForegroundWindow.Call()
	if fg == hwnd {
		return true
	}
	cur, _, _ := procGetCurrentThreadId.Call()

	var attached []uintptr
	attach := func(thread uintptr) {
		if thread == 0 || thread == cur {
			return
		}
		if ok, _, _ := procAttachThreadInput.Call(cur, thread, 1); ok != 0 {
			attached = append(attached, thread)
		}
	}
	if fg != 0 {
		if thread, _, _ := procGetWindowThreadProcId.Call(fg, 0); thread != 0 {
			attach(thread)
		}
	}
	if thread, _, _ := procGetWindowThreadProcId.Call(hwnd, 0); thread != 0 {
		attach(thread)
	}
	defer func() {
		for _, thread := range attached {
			procAttachThreadInput.Call(cur, thread, 0)
		}
	}()

	procSetForegroundWindow.Call(hwnd)
	fg, _, _ = procGetForegroundWindow.Call()
	return fg == hwnd
}
