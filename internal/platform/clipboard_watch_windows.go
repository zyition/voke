//go:build windows

package platform

// 事件驱动的剪贴板监听：独立线程 + message-only 窗口 + AddClipboardFormatListener。
//
// 为什么不用轮询：轮询是「按时间采样状态」——既费电（每秒固定唤醒、读一次 DWORD），
// 又会漏掉一个周期内被覆盖的中间副本。WM_CLIPBOARDUPDATE 只在真的变化时到达，
// 空闲时零成本。
//
// 线程模型：监听独占一个 LockOSThread 的 goroutine，窗口、窗口过程、消息循环都
// 在这个线程上；事件经容量 1 的 channel 通知调用方，回调里永不阻塞（满了就丢，
// 反正只是「可能变了」的提示，调用方会重读序列号去重）。

import (
	"runtime"
	"syscall"
	"unsafe"
)

const (
	// wmClipboardUpdate 是 AddClipboardFormatListener 通知的消息。
	wmClipboardUpdate = 0x031D
	// hwndMessage 是 HWND_MESSAGE：消息专用窗口，不参与布局、不可见。
	hwndMessage = ^uintptr(2)
)

var (
	procRegisterClassExW              = user32.NewProc("RegisterClassExW")
	procCreateWindowExW               = user32.NewProc("CreateWindowExW")
	procDefWindowProcW                = user32.NewProc("DefWindowProcW")
	procGetMessageW                   = user32.NewProc("GetMessageW")
	procTranslateMessage              = user32.NewProc("TranslateMessage")
	procDispatchMessageW              = user32.NewProc("DispatchMessageW")
	procPostQuitMessage               = user32.NewProc("PostQuitMessage")
	procDestroyWindow                 = user32.NewProc("DestroyWindow")
	procUnregisterClassW              = user32.NewProc("UnregisterClassW")
	procAddClipboardFormatListener    = user32.NewProc("AddClipboardFormatListener")
	procRemoveClipboardFormatListener = user32.NewProc("RemoveClipboardFormatListener")
	procGetModuleHandleW              = kernel32.NewProc("GetModuleHandleW")
)

const clipWatchClass = "voke-clipboard-watch"

// clipWatchSink 是窗口过程投递事件的出口；只在监听线程上设置/读取
// （先赋值、再进消息循环），因此不需要额外同步。
var clipWatchSink chan struct{}

// wndClassExW 对应 WNDCLASSEXW（amd64 布局）。
type wndClassExW struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     uintptr
	hIcon         uintptr
	hCursor       uintptr
	hbrBackground uintptr
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       uintptr
}

// msgW 对应 MSG（amd64 布局）。
type msgW struct {
	hwnd     uintptr
	message  uint32
	_        uint32
	wParam   uintptr
	lParam   uintptr
	time     uint32
	ptX      int32
	ptY      int32
	lPrivate uint32
}

// clipWatchProc 是监听窗口的窗口过程。跨 FFI 调用，保持最小：只做非阻塞投递。
var clipWatchProc = syscall.NewCallback(func(hwnd, m, wp, lp uintptr) uintptr {
	switch m {
	case wmClipboardUpdate:
		if ch := clipWatchSink; ch != nil {
			select {
			case ch <- struct{}{}:
			default: // 已经有待处理信号：合并
			}
		}
		return 0
	case wmClose:
		procPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, m, wp, lp)
	return r
})

// StartClipboardWatch 启动剪贴板事件监听。
//
// 返回事件通道（每次系统剪贴板变化发一个信号，可能合并）、停止函数、是否启动成功。
// 失败（注册窗口类 / 建窗口 / 注册监听器任一失败）时返回 ok=false，调用方回退轮询。
// 停止函数可重复调用；进程退出时也可不调用（窗口随进程销毁）。
func StartClipboardWatch() (<-chan struct{}, func(), bool) {
	w := &clipWatch{ch: make(chan struct{}, 1), done: make(chan struct{})}
	ready := make(chan bool, 1)
	go w.run(ready)
	if !<-ready {
		return nil, nil, false
	}
	stopped := false
	return w.ch, func() {
		if stopped {
			return
		}
		stopped = true
		w.stop()
	}, true
}

type clipWatch struct {
	ch   chan struct{}
	hwnd uintptr
	done chan struct{}
}

// run 在锁定线程上建窗口并跑消息循环，直到收到 WM_CLOSE。
func (w *clipWatch) run(ready chan<- bool) {
	defer func() { close(w.ch); close(w.done) }() // 收尾顺序：先断事件流，再宣告结束
	runtime.LockOSThread()                        // 窗口与消息循环必须同线程
	defer runtime.UnlockOSThread()

	className, _ := syscall.UTF16PtrFromString(clipWatchClass)
	hInst, _, _ := procGetModuleHandleW.Call(0)
	wc := wndClassExW{
		cbSize:        uint32(unsafe.Sizeof(wndClassExW{})),
		lpfnWndProc:   clipWatchProc,
		hInstance:     hInst,
		lpszClassName: className,
	}
	if r, _, _ := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		ready <- false
		return
	}
	hwnd, _, _ := procCreateWindowExW.Call(0,
		uintptr(unsafe.Pointer(className)), 0, 0,
		0, 0, 0, 0, hwndMessage, 0, hInst, 0)
	if hwnd == 0 {
		procUnregisterClassW.Call(uintptr(unsafe.Pointer(className)), hInst)
		ready <- false
		return
	}
	if r, _, _ := procAddClipboardFormatListener.Call(hwnd); r == 0 {
		procDestroyWindow.Call(hwnd)
		procUnregisterClassW.Call(uintptr(unsafe.Pointer(className)), hInst)
		ready <- false
		return
	}

	w.hwnd = hwnd
	clipWatchSink = w.ch
	ready <- true

	var msg msgW
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(r) <= 0 { // 0 = WM_QUIT，-1 = 错误
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}

	clipWatchSink = nil
	procRemoveClipboardFormatListener.Call(hwnd)
	procDestroyWindow.Call(hwnd)
	procUnregisterClassW.Call(uintptr(unsafe.Pointer(className)), hInst)
}

// stop 让消息循环退出并等线程收尾。
func (w *clipWatch) stop() {
	if w.hwnd != 0 {
		procPostMessageW.Call(w.hwnd, wmClose, 0, 0)
	}
	<-w.done
}
