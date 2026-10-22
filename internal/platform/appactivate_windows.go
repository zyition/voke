//go:build windows

package platform

import (
	"syscall"
	"unsafe"
)

// COM 常量（值取自 windows-0.62.2 与微软文档，对照 voke-sys/src/appactivate.rs）：
const (
	clsctxInprocServer = 0x1 // CLSCTX_INPROC_SERVER
	sOK                = 0   // S_OK
)

// appActivationVtbl 是 IApplicationActivationManager 的 vtable 槽（继承 IUnknown：
// 0=QueryInterface 1=AddRef 2=Release，第一个自有方法 ActivateApplication 在槽 3）。字段以
// uintptr 存函数地址；整张表经**类型化指针**读取（见 ActivateApp），不做 uintptr→unsafe.Pointer
// 转换——那种写法会被 go vet 的 unsafeptr 检查拦下。
type appActivationVtbl struct {
	queryInterface    uintptr
	addRef            uintptr
	release           uintptr
	activateApp       uintptr
	activateForWindow uintptr
	activateForProg   uintptr
	installDpi        uintptr
}

var (
	ole32            = syscall.NewLazyDLL("ole32.dll")
	procCoInitEx     = ole32.NewProc("CoInitializeEx")
	procCoCreateInst = ole32.NewProc("CoCreateInstance")
)

// CLSID_ApplicationActivationManager = 45ba127d-10a8-46ea-8ab7-56ea9078943c
// （windows 的 Win32::UI::Shell::ApplicationActivationManager，与 CmdPal 引用一致）。
var clsidAppActivationMgr = syscall.GUID{
	Data1: 0x45ba127d, Data2: 0x10a8, Data3: 0x46ea,
	Data4: [8]byte{0x8a, 0xb7, 0x56, 0xea, 0x90, 0x78, 0x94, 0x3c},
}

// IID_IApplicationActivationManager = 2e941141-7f97-4756-ba1d-9decde894a3d。
var iidAppActivationMgr = syscall.GUID{
	Data1: 0x2e941141, Data2: 0x7f97, Data3: 0x4756,
	Data4: [8]byte{0xba, 0x1d, 0x9d, 0xec, 0xde, 0x89, 0x4a, 0x3d},
}

// ActivateApp 经 COM `IApplicationActivationManager::ActivateApplication` 激活 Store 应用
// （WT profile），可带命令行参数（`--profile "<guid>"`）。对照 appactivate.rs：
// `CoInitializeEx(MTA)` → `CoCreateInstance(CLSID, INPROC_SERVER)` → `ActivateApplication(
// aumid, args, AO_NONE, &pid)` → `Release`。成功返回新进程 pid 与 true。
//
// 语义（对齐生产 / CmdPal）：WT 已在运行时激活请求转交给它（按 windowingBehavior 开新窗 / 新
// 标签），不起第二进程。COM 初始化落在**当前（mygo 主）线程**、不 CoUninitialize（长生命周期
// 线程保持套间）；若该线程已以他模型初始化，CoInitializeEx 返回 RPC_E_CHANGED_MODE，忽略后
// CoCreateInstance 仍可用。调用点 execPlatformAction 在主线程（托盘 / 面板回调）。
func ActivateApp(aumid, arguments string) (uint32, bool) {
	procCoInitEx.Call(0, 0) // COINIT_MULTITHREADED

	auPtr, err := syscall.UTF16PtrFromString(aumid)
	if err != nil {
		return 0, false
	}
	argPtr, err := syscall.UTF16PtrFromString(arguments)
	if err != nil {
		return 0, false
	}

	var obj unsafe.Pointer
	hr, _, _ := procCoCreateInst.Call(
		uintptr(unsafe.Pointer(&clsidAppActivationMgr)),
		0,
		uintptr(clsctxInprocServer),
		uintptr(unsafe.Pointer(&iidAppActivationMgr)),
		uintptr(unsafe.Pointer(&obj)), // out LPVOID*
	)
	if hr != sOK || obj == nil {
		return 0, false
	}
	// obj（this）指向对象，其首字即 vtable 指针：`**appActivationVtbl` 两次解引用取到方法表，
	// 全程类型化指针、无 uintptr→Pointer 转换。
	vtbl := *(**appActivationVtbl)(obj)
	defer syscall.Syscall(vtbl.release, 1, uintptr(obj), 0, 0) // Release(this)

	var pid uint32
	hr, _, _ = syscall.Syscall6(vtbl.activateApp, 5,
		uintptr(obj),
		uintptr(unsafe.Pointer(auPtr)),  // pszAppUserModelID
		uintptr(unsafe.Pointer(argPtr)), // pszArguments
		0,                               // eAD = AO_NONE
		uintptr(unsafe.Pointer(&pid)),   // out pdwProcessId
		0,
	)
	if hr != sOK {
		return 0, false
	}
	return pid, true
}
