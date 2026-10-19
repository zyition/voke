// apps.go：应用子系统的**装配侧**（对应 Rust 侧 main.rs 的扫描 + shell.rs 的启动）。
// 纯逻辑部分已按 §7.2.1 下沉：
//   - 条目模型 + singleflight 加载闸门 → internal/logic/appsmodel
//   - 图标有界缓存（map + FIFO 淘汰）  → internal/logic/iconcache
//
// §7.2 步骤6b 又把「会碰到 mygo 的那一截」抽进 internal/platform/shell.go，
// 本文件从此**不再有直接的平台调用**，只作调用方：
//   - 枚举 Shell.InstalledApps（可后台）  → platform.InstalledApps
//   - 启动 Shell.LaunchApp（主线程）      → platform.LaunchApp（调用点在 view.go 的 execLaunch）
//   - 取图标 Shell.FileIcon + 像素尺寸（主线程）→ platform.AppIcon（调用点在 view.go 的 appState.icon）
//
// 留在本文件的是**与 App 装配缝在一起的那几针**，不是平台调用：
//   - loadAppsAsync 的编排（枚举 → 去重 → 耗时打点 → 内存快照 → 回主线程）；
//   - 界面接缝 appsWin / appsApply 与 bindApps——它们持 *mygo.Window、由 panel.go
//     的 ready 回调在窗口创建之后注入，与 mygo 的 App / Window 生命周期强耦合，
//     **留步骤8**（§7.2：app 层装配），本步不动、也不改注入时机；
//   - appsmodel.Loader 的 init 注入。
//
// 线程模型（mygo 契约，逐函数标注见 internal/platform/shell.go 头部；
// 参照 loop.go:94 / content.go:172）：
//   - platform.InstalledApps 可在后台 goroutine 调（内部 waitReady + 自持 COM 线程）；
//     platform.AppIcon / platform.LaunchApp 背后的 FileIcon / LaunchApp 走
//     onMainValue，只在主线程调（视图渲染 / Enter 启动）；
//   - 后台线程回主线程只经 Window.Update（投递后不等待）。
package app

import (
	"log"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/zyition/voke/poc/voke-go/internal/logic/appsmodel"
	"github.com/zyition/voke/poc/voke-go/internal/platform"
)

// appsScanDebounce 是「唤起重扫」的防抖窗口，对照生产 main.rs 的 APPS_SCAN_DEBOUNCE
// （60s）：装完应用最迟下次唤起（距上次扫描 ≥ 此值）即见，又不必每次唤起都重扫磁盘。
const appsScanDebounce = 60 * time.Second

// init 把 platform 侧的真实枚举体注入 logic 层的加载闸门（appsmodel.Loader）。
// 为什么放 init 而不是 main() 的装配段：appsmodel.EnsureLoaded() 内部是
// `go Loader()`，未注入就是 go nil() → 整个进程炸；init 保证「包加载即注入」，
// 一定早于任何按键路径。单测里 panel_behavior_test.go 的 resetLoadGate 会临时
// 把它换成空实现（不读磁盘），用例结束复原。
func init() { appsmodel.Loader = loadAppsAsync }

// appsBindMu 保护界面接缝（装配期主线程写一次，枚举线程读）。
var (
	appsBindMu sync.RWMutex
	appsWin    *mygo.Window               // 主面板窗口句柄
	appsApply  func([]appsmodel.AppEntry) // 枚举完成后的主线程刷新回调
)

// bindApps 注入 apps 子系统的界面接缝。由主线程在装配期调用
// （panel.go 的 ready 内、窗口创建之后——顺序反了会让首次枚举结果
// 刷新丢失）：
//
//	bindApps(st.panel, func(apps []appsmodel.AppEntry) { st.apps = apps })
//
// 后台枚举线程只经这两个句柄回主线程刷新，不直接碰窗口与 appState。
// 未注入（纯逻辑测试 / 装配早期）时枚举照跑、数据照打点，只不刷新 UI。
func bindApps(win *mygo.Window, apply func(apps []appsmodel.AppEntry)) {
	appsBindMu.Lock()
	defer appsBindMu.Unlock()
	appsWin, appsApply = win, apply
}

// loadAppsAsync 是后台枚举体：枚举 → 去重 → 耗时打点 → 内存快照 →
// 回主线程刷新。由 appsmodel.EnsureLoaded() 经注入的 Loader 调用，
// 故**只在有查询需求时**才跑（闸门语义见 appsmodel.appsmodel.go）。
//
// 线程纪律（mygo 契约，PoC 文档 §11 已核实）：
//   - platform.InstalledApps 是唯一后台安全的 Shell API（自建 COM STA，
//     modules.go:103-120），所以整段跑在 goroutine 里；
//   - 结果回主线程只经 Window.Update（content.go:162-177，异步投递）。
func loadAppsAsync() {
	// 无论成功/失败，本轮枚举结束时清 inFlight，放行下一次唤醒重扫（MarkLoaded 只解
	// 并发闸，不改一次性首载闸门 loaded）。defer 保证两个 return 分支都覆盖。
	defer appsmodel.MarkLoaded()
	start := time.Now()
	found, err := platform.InstalledApps()
	elapsed := time.Since(start)
	if err != nil {
		log.Printf("应用枚举失败（耗时 %s）：%v", elapsed, err)
		// 失败也要通知界面，否则空态文案会一直停在「正在枚举…」。
		// 闸门已置位，不会重试（见 appsmodel.loaded）。
		appsBindMu.RLock()
		win, apply := appsWin, appsApply
		appsBindMu.RUnlock()
		if win != nil && apply != nil {
			win.Update(func() { apply(nil) })
		}
		return
	}
	entries := make([]appsmodel.AppEntry, 0, len(found))
	for _, a := range found {
		entries = append(entries, appsmodel.AppEntry{Name: a.Name, Path: a.Path, ID: a.ID})
	}
	// 双来源合并（对照 Rust）：开始菜单 .lnk（英文文件名/描述）在前，AppsFolder
	// （本地化显示名）在后；rule-2 跳过与 .lnk 同名的非 MSIX 条目，避免重复。
	shortcuts := platform.ShortcutApps()
	entries = appsmodel.MergeShortcutApps(entries, shortcuts)
	entries = appsmodel.DedupeApps(entries)
	// Target 只服务跨来源去重（.lnk 目标 vs AppsFolder ID 词干），进入 UI 前清掉：
	// 真机 40 条快捷方式共约 2KB 绝对路径，量小但不该随应用列表常驻。
	for i := range entries {
		entries[i].Target = ""
	}
	// 耗时打点：喂 V3 与生产口径（几十到几百毫秒，apps.rs:589-590）对照。
	log.Printf("应用枚举完成：%d 条（AppsFolder %d + 快捷方式 %d，耗时 %s）", len(entries), len(found), len(shortcuts), elapsed)
	// 「引擎索引完成」是 memory-footprint.md §1 快照标签体系的最后一点
	// （第 8 行）；索引 = 应用列表就绪，与 UI 是否已接线无关。
	platform.SnapshotMem("引擎索引完成")

	appsBindMu.RLock()
	win, apply := appsWin, appsApply
	appsBindMu.RUnlock()
	if win == nil || apply == nil {
		return
	}
	win.Update(func() { apply(entries) })
}
