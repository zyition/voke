// iconpump.go：图标补齐泵——把 Shell.FileIcon 从渲染帧里挪到「停手后的空闲帧」。
//
// ## 为什么要有这个泵（2026-11-10 实测）
//
// 用 mygo 自带的 MYGO_FRAME_STATS=4 加了 VOKE_PROF 探针跑真机：
//   - 慢帧全部落在 build 阶段，而 build 里 99% 是 renderResults；
//   - renderResults 的耗时 ≈ 取图耗时：144 次 Shell.FileIcon 合计 2187.7ms，
//     平均 15.2ms、**最大 72ms**（不是 mygo 文档说的「a few milliseconds」）；
//   - 删字符会放宽结果集，一帧里同时冒 5~9 个从没显示过的行 → 一帧 100~150ms，
//     正好是用户反馈的「删字符偶现卡顿」。
//
// 结论：图标不能进渲染帧。mygo 没有异步图源（ImageSource 只有 *Bitmap / *SVG），
// FileIcon 又只能主线程（onMainValue），所以只能在**调度**上做文章——这就是
// docs/launchers.md「draw those that show, and keep them」的加强版：
//
//   - 渲染帧只查缓存（内存 → **磁盘**，磁盘命中即解码，约 0.3ms），
//     全 miss 才返回 nil → 行内用既有占位（Avatar / 插件角标）；
//   - 需求登记进队列，**停手后**（iconPumpDelay）才补，一次补一张；
//   - 每补一张就 Window.Update 重建一帧（正好把新图标画上去），
//     于是 150ms 的顿帧被摊成 9 帧、每帧一张（约 15ms），输入不再等图标。
//
// ## 三层缓存（从快到慢）
//
//  1. internal/ui 的解码位图 LRU（256 条）——渲染帧只碰这一层；
//  2. internal/platform 的 PNG 字节 FIFO（256 条）——Shell.FileIcon 的结果；
//  3. internal/platform 的磁盘 PNG 缓存（iconstore.go）——跨启动，亚毫秒。
//
// ## 预热
//
// 面板隐藏后把「已知的全部来源」（应用列表 + 窗口快照的进程路径）排进同一个队列，
// 以同样的一拍一张补完；隐藏时只写字节/磁盘两层，不重建视图（见 loadIcon 的
// keep 参数）——隐藏 10s 后那次图标裁剪会清解码缓存，写了也是白写。
//
// ## 线程
//
// 队列 / 去重集合 / 定时器 / 世代号只在主线程读写。取图本身也必须主线程。
// 定时器回调在 timer goroutine 上，统一经 Window.Update（可见时重建一帧）或
// mygo.RunOnMain（隐藏时只补缓存）回到主线程。
package app

import (
	"time"

	"github.com/egoist/mygo"

	"voke/internal/platform"
	appui "voke/internal/ui"
)

const (
	// iconPumpDelay 是「最后一次登记需求」到「开始补图」的静默期：连续打字期间
	// 一次都不补图，停手后才补——输入回显永远不等 FileIcon。
	iconPumpDelay = 90 * time.Millisecond
	// iconPumpInterval 是补完一张到补下一张的间隔：一拍一张，让每次取图单独占一帧。
	iconPumpInterval = 2 * time.Millisecond
	// iconPumpMax 是队列上限（去重后）。真机 155 应用 + 若干窗口进程路径，
	// 256 偏远，取 1024 留余量；超出直接丢新需求（行内保持占位，下次可见再登记）。
	iconPumpMax = 1024
)

// requestIcon 登记一条「需要图标」的来源（由 ui.IconCache.OnMiss 在渲染帧里调用）。
// 已缓存 / 已在队列里的直接忽略。登记后静默 iconPumpDelay 再补。
func (st *appState) requestIcon(path string) {
	if st.icons == nil || path == "" {
		return
	}
	if _, ok := st.icons.Get(path); ok {
		return
	}
	if st.iconQueued == nil {
		st.iconQueued = make(map[string]struct{}, 64)
	}
	if _, dup := st.iconQueued[path]; dup {
		return
	}
	if len(st.iconQueue) >= iconPumpMax {
		return
	}
	st.iconQueue = append(st.iconQueue, path)
	st.iconQueued[path] = struct{}{}
	st.scheduleIconPump(iconPumpDelay)
}

// scheduleIconPump 挂一次「补一张图标」。重复调用让上一次作废（世代号）——
// 连续登记需求时延迟会一直往后推，等价于「等到用户停手」。
func (st *appState) scheduleIconPump(d time.Duration) {
	if st.icons == nil || len(st.iconQueue) == 0 {
		return
	}
	st.iconPumpGen++
	gen := st.iconPumpGen
	if st.iconPumpTimer != nil {
		st.iconPumpTimer.Stop()
	}
	st.iconPumpTimer = time.AfterFunc(d, func() {
		panel := st.panel
		if panel == nil || panel.IsDestroyed() {
			return
		}
		if panel.IsVisible() {
			// 可见：重建一帧把新图标画上去。回调里再校验世代号，
			// 期间用户又敲了字就以新的一拍为准。
			panel.Update(func() {
				if st.iconPumpGen != gen {
					return
				}
				st.pumpOneIcon()
			})
			return
		}
		// 隐藏：只补缓存，不重建视图（预热）。
		mygo.RunOnMain(func() {
			if st.iconPumpGen != gen {
				return
			}
			st.pumpOneIcon()
		})
	})
}

// pumpOneIcon 从队列头取一条来源取图并写回缓存，然后续挂下一拍。
// 只在主线程执行（Window.Update / RunOnMain 回调内）。
func (st *appState) pumpOneIcon() {
	if len(st.iconQueue) == 0 {
		return
	}
	path := st.iconQueue[0]
	st.iconQueue = st.iconQueue[1:]
	if st.iconQueued != nil {
		delete(st.iconQueued, path)
	}
	st.loadIcon(path, st.panel != nil && st.panel.IsVisible())
	if len(st.iconQueue) > 0 {
		st.scheduleIconPump(iconPumpInterval)
	}
}

// loadIcon 取一条来源的图标并写回缓存：先磁盘缓存，未命中才问 Shell（并落盘）。
// keep=false 时只写 PNG 字节 / 磁盘两层，不写解码位图缓存——隐藏期间的预热
// 写了也会被「隐藏 10s 裁到 64 条」清掉，解码本来就是亚毫秒级，不值得留。
func (st *appState) loadIcon(path string, keep bool) {
	if st.icons == nil {
		return
	}
	if _, ok := st.icons.Get(path); ok {
		return
	}
	if png, ok := platform.LoadIconPNG(path); ok {
		if keep {
			st.icons.Put(path, appui.DecodeIcon(png, path))
		}
		return
	}
	// 冷路径：Shell.FileIcon，10~70ms。落盘后同一台机器再也不付第二次。
	data := platform.AppIcon(path)
	platform.SaveIconPNG(path, data)
	if keep {
		st.icons.Put(path, appui.DecodeIcon(data, path))
	}
}

// prewarmIcons 在面板隐藏后把「已知的全部图标来源」排进队列预热：
// 应用列表（AppEntry.Path）+ 窗口快照里的进程路径（窗口行的图标来源）。
// 只在隐藏时调用——面板可见时预热会跟用户的输入抢主线程。
func (st *appState) prewarmIcons() {
	if st.icons == nil {
		return
	}
	for _, a := range st.model.Apps {
		st.requestIcon(a.Path)
	}
	for _, w := range winSnap.get() {
		st.requestIcon(w.ProcessPath)
	}
}
