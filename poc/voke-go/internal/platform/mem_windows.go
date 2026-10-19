//go:build windows

// mem_windows.go：进程内存快照打点（Windows 实现；非 Windows 占位见 mem_stub.go）。
//
// 口径逐项对齐 voke-sys/src/mem.rs：
//   - 数据源 K32GetProcessMemoryInfo 的 PROCESS_MEMORY_COUNTERS_EX；
//   - WorkingSetSize ↔ WorkingSet、PrivateUsage ↔ PrivateCommit（字节）；
//   - 日志格式 `内存快照 [<tag>]：WorkingSet=x.xMB PrivateCommit=y.yMB
//     GoHeap=a.aMB HeapSys=b.bMB HeapIdle=c.cMB HeapReleased=d.dMB Objects=n
//     GC=g`，单位 MB、保留一位小数，方便事后 grep "内存快照" 汇总。
//     HeapIdle/HeapReleased 为 2026-10-16 追加（HeapSys 永不回落，看不出归还）。
//
// 后六项来自 runtime.ReadMemStats，是**诊断脚手架**（2026-10-16 内存增长
// 定因加）：V1 门槛以 PrivateCommit 为准，但这六项能把增长劈成两半——
// PrivateCommit 涨而 GoHeap/HeapSys 不涨 = 涨在 mygo/驱动的原生侧
// （D3D11 纹理、DirectWrite 字形图集、布局缓存的原生部分），是可修的
// PoC 代码问题；GoHeap/HeapSys 同步涨 = 涨在 Go 堆里（mygo 的 ui 侧
// 缓存），先怀疑框架地板。HeapIdle/HeapReleased 进一步区分「攥在手里
// 没还」与「已归还 OS」。Objects 是活对象数，配合 GC 次数看泄漏形状。
//
// 与 mem.rs 的差异（ Go 侧无 windows crate 的现成绑定，且本 PoC 不引
// golang.org/x/sys——那要动 go.mod）：DLL 过程解析用 stdlib syscall 的
// LazyDLL 手工完成，包级变量只解析一次；结构体按 C 定义手写。
package platform

import (
	"fmt"
	"log"
	"runtime"
	"syscall"
	"unsafe"
)

// processMemoryCounters 对应 Windows PROCESS_MEMORY_COUNTERS_EX（psapi.h）。
// 字段布局：DWORD cb + DWORD PageFaultCount + 10 个 SIZE_T（64 位下 8 字节）。
// 必须用 _EX 版：基础版 PROCESS_MEMORY_COUNTERS 只有前 10 个字段，
// 拿不到 PrivateUsage（mem.rs 同款论证）。cb 必须等于结构体实际大小。
type processMemoryCounters struct {
	cb                         uint32
	pageFaultCount             uint32
	peakWorkingSetSize         uintptr
	workingSetSize             uintptr
	quotaPeakPagedPoolUsage    uintptr
	quotaPagedPoolUsage        uintptr
	quotaPeakNonPagedPoolUsage uintptr
	quotaNonPagedPoolUsage     uintptr
	pagefileUsage              uintptr
	peakPagefileUsage          uintptr
	privateUsage               uintptr
}

// procK32GetProcessMemoryInfo 是 kernel32 的过程句柄。
// DLL 过程解析只做一次：包级变量在进程初始化期完成 LazyDLL 绑定，
// SnapshotMem 运行期不再解析（在函数体内 NewProc 会每次调用重复查符号）。
var procK32GetProcessMemoryInfo = syscall.NewLazyDLL("kernel32.dll").
	NewProc("K32GetProcessMemoryInfo")

// SnapshotMem 打一条内存快照日志。主线程/后台线程均可调用：纯 SyscallN
// 加 ReadMemStats，无 COM、无堆分配依赖（ReadMemStats 会 stop the world，
// 但 30s 一条的量级无所谓）。标签体系与归因方法见 docs/notes/memory.md
// （启动 / 面板首次唤起 / 引擎索引完成）。
func SnapshotMem(tag string) {
	ws, priv, err := processMemory()
	if err != nil {
		log.Printf("内存快照 [%s]：读取失败：%v", tag, err)
		return
	}
	// Go 侧六项：HeapAlloc 是活堆、HeapSys 是向系统要的堆总量、HeapIdle 是
	// 已拿到但闲置的 span、HeapReleased 是**已还给 OS 的物理内存**、
	// HeapObjects 是活对象数、NumGC 是累计 GC 次数。与 PrivateCommit 对照
	// 即见增长归属。
	//
	// 为什么要 HeapIdle/HeapReleased（2026-10-16 真机日志补测）：HeapSys 按
	// Go 语义**永不回落**（它是向 OS 要过的堆总量峰值），只看它无法回答
	// 「内存有没有还回去」。三者的关系是：
	//
	//	HeapSys   = 向 OS 要过的堆总量（只增不减，看不出归还）
	//	HeapIdle  = 其中已拿到但当前闲置的（HeapIdle - HeapReleased 才是
	//	            真正**捏在进程手里没还给 OS** 的闲置量）
	//	HeapReleased = 已 scavenge 归还给 OS 的物理内存（会随 GC 上升）
	//
	// 于是「HeapReleased 长期为 0 而 HeapIdle 一路涨」= 内存拿到了就再也不
	// 还，是真泄漏信号；「HeapIdle 涨但 HeapReleased 同步涨」= 只是 Go 把
	// 内存攥在手里没主动还，可由 GC 频率/scavenger 解释。单位与其它项一致，
	// 保留一位小数。
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	log.Printf("内存快照 [%s]：WorkingSet=%.1fMB PrivateCommit=%.1fMB "+
		"GoHeap=%.1fMB HeapSys=%.1fMB HeapIdle=%.1fMB HeapReleased=%.1fMB "+
		"Objects=%d GC=%d",
		tag, mbOf(ws), mbOf(priv),
		mbOf(ms.HeapAlloc), mbOf(ms.HeapSys),
		mbOf(ms.HeapIdle), mbOf(ms.HeapReleased),
		ms.HeapObjects, ms.NumGC)
}

// processMemory 读本进程内存，返回 (WorkingSetSize, PrivateUsage)，单位字节。
// 失败返回 error（对应 mem.rs 的 None 分支语义）。
func processMemory() (workingSet, privateCommit uint64, err error) {
	// GetCurrentProcess 返回本进程伪句柄，调用后始终有效、无需关闭。
	h, err := syscall.GetCurrentProcess()
	if err != nil {
		return 0, 0, fmt.Errorf("GetCurrentProcess：%w", err)
	}
	var c processMemoryCounters
	c.cb = uint32(unsafe.Sizeof(c))
	// K32GetProcessMemoryInfo(HANDLE, PPROCESS_MEMORY_COUNTERS, DWORD cb) -> BOOL
	r0, _, e1 := procK32GetProcessMemoryInfo.Call(
		uintptr(h),
		uintptr(unsafe.Pointer(&c)),
		uintptr(c.cb),
	)
	if r0 == 0 {
		if en, ok := e1.(syscall.Errno); ok && en != 0 {
			return 0, 0, fmt.Errorf("K32GetProcessMemoryInfo：%w", en)
		}
		return 0, 0, fmt.Errorf("K32GetProcessMemoryInfo 返回 FALSE")
	}
	return uint64(c.workingSetSize), uint64(c.privateUsage), nil
}

// mbOf 把字节数换算成 MB（1024*1024，与 mem.rs 的 f64 换算一致）。
func mbOf(b uint64) float64 { return float64(b) / (1024 * 1024) }
