// clipboard.go：剪贴板历史的运行时装配——系统事件驱动采集、程序化写入抑制、
// 粘贴回原窗口、置顶 / 删除动作与失败提示。
//
// 逻辑边界：历史存储（cliphist）与 provider（providers/clipboard）都是纯 Go；
// 平台读写 / 窗口 / 按键在 internal/platform；本文件只负责把它们接起来，并在
// 主线程维护 appState 里的剪贴板状态。
package app

import (
	"errors"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/egoist/mygo"
	"voke/internal/i18n"
	"voke/internal/logic/appcfg"
	"voke/internal/logic/cliphist"
	"voke/internal/platform"
	clipboardprovider "voke/internal/providers/clipboard"
)

const (
	// clipboardPasteDelay 是隐藏面板到发送按键之间的等待：让前台切换完成，
	// 避免把 Ctrl+V 发到正在消失的面板。
	clipboardPasteDelay = 120 * time.Millisecond
	// clipboardSuppressWindow 覆盖 OLE 延迟渲染可能带来的额外序列号变化。
	clipboardSuppressWindow = 3 * time.Second
	// noticeTTL 是提示文案的保留时长（之后唤起面板时清掉）。
	noticeTTL = 6 * time.Second
)

// clipboardMaxEntries 取配置里的历史上限；未写 / 越界（手改配置）均回落默认，
// 不让一个笔误把历史压到 1 条。
func clipboardMaxEntries(loaded appcfg.Loaded) int {
	if loaded.HasClipboardMaxEntries {
		n := loaded.ClipboardMaxEntries
		if n >= cliphist.MinMaxEntries && n <= cliphist.MaxMaxEntries {
			return n
		}
	}
	return cliphist.DefaultMaxEntries
}

// startClipboardWatcher 启动系统剪贴板变化监听（mygo `Clipboard.OnChanged`：
// Windows 走原生 WM_CLIPBOARDUPDATE，回调在主线程）。**变化后**才读内容、写历史。
//
// 省电约束：剪贴板插件停用时由 syncClipboardWatch 注销监听，空闲零回调；
// 监听挂在 mygo 应用自己的消息窗口上，不需要自建线程 / 窗口 / 轮询。
// 必须等 App 就绪后调用（ready 里）。
func (st *appState) startClipboardWatcher() {
	st.clipSeq.Store(platform.ClipboardSequence())
	st.syncClipboardWatch()
}

// onClipboardSignal 在主线程处理「系统剪贴板可能变了」：mygo 会合并连续变化，
// 也会把程序化写入报告回来，所以重读序列号去重，真变了才读内容。
func (st *appState) onClipboardSignal() {
	if !st.clipWatchOn.Load() {
		return
	}
	seq := platform.ClipboardSequence()
	if seq == 0 || seq == st.clipSeq.Load() {
		return
	}
	st.clipSeq.Store(seq)
	st.captureClipboard()
}

// syncClipboardWatch 按配置决定是否监听剪贴板（插件停用 → 注销监听，零回调）。
// 只在主线程调用（ready / 配置重载）。
func (st *appState) syncClipboardWatch() {
	on := true
	for _, p := range st.providers.Providers {
		if p.ID == clipboardprovider.ID {
			on = p.Enabled
			break
		}
	}
	st.clipWatchOn.Store(on)
	if on {
		if st.clipWatchOff == nil {
			st.clipWatchOff = mygo.Clipboard.OnChanged(st.onClipboardSignal)
		}
		return
	}
	if st.clipWatchOff != nil {
		st.clipWatchOff()
		st.clipWatchOff = nil
	}
}

// captureClipboard 在主线程读当前剪贴板并写入历史。
func (st *appState) captureClipboard() {
	if st.hist == nil {
		return
	}
	p, ok := platform.ReadClipboardPayload()
	if !ok {
		return
	}
	if !st.clipSuppressUntil.IsZero() {
		if time.Now().Before(st.clipSuppressUntil) && st.clipSuppress.Equal(p) {
			return // 自己刚写入（含延迟渲染再次触发的序列号变化）
		}
		st.clipSuppressUntil = time.Time{}
	}
	it, err := st.hist.Add(cliphist.Payload{Text: p.Text, HTML: string(p.HTML), RTF: p.RTF, PNG: p.PNG}, time.Now())
	if err != nil {
		// 空内容 / 超上限是预期跳过，不刷日志；其它错误同样不应打断面板。
		return
	}
	st.persistClipUpsert(it)
	if st.isClipboardPlugin() && st.panel != nil && st.panel.IsVisible() {
		st.refreshClipboardRows()
	}
}

// isClipboardPlugin 报告当前是否落在剪贴板插件态。
func (st *appState) isClipboardPlugin() bool {
	return st.providerActive() && st.engineState.PluginID() == providerClipboardID
}

// refreshClipboardRows 重跑剪贴板查询，并尽量保持当前选中行。
// keep 随查询任务进 job，落地时由 applyQueryResult 钳位后恢复（异步化后不能再
// 在投递后立刻 chooseProvider——那时结果还没回来）。
func (st *appState) refreshClipboardRows() {
	st.submitProviderQuery(st.provChosen)
}

// setNotice 在主线程设置一次性提示并请求重绘。
func (st *appState) setNotice(msg string) {
	st.notice = msg
	st.noticeAt = time.Now()
	if st.panel != nil && st.panel.IsVisible() {
		st.panel.Invalidate() // 合并到下一帧重绘，不投递空回调
	}
}

// pasteClipboardItem 把某条历史写回系统剪贴板并粘贴到唤起前的窗口。
// plain=true 时只写纯文本（Ctrl+Enter）。返回值仅供调用方记录，不参与隐藏逻辑。
func (st *appState) pasteClipboardItem(ref string, plain bool) {
	if st.hist == nil {
		return
	}
	it, ok := st.hist.Find(ref)
	if !ok {
		return
	}
	payload := platform.ClipPayload{Text: it.Text, HTML: []byte(it.HTML), RTF: it.RTF, PNG: it.PNG}
	var err error
	if plain {
		err = platform.WriteClipboardPlain(payload)
	} else {
		err = platform.WriteClipboardPayload(payload)
	}
	if err != nil {
		log.Printf("voke：剪贴板历史写入失败（plain=%v）：%v", plain, err)
		st.setNotice(i18n.L("粘贴失败：无法写入剪贴板", "Paste failed: could not write to the clipboard"))
		return
	}
	// 程序化写入也会让序列号递增；先同步到已处理位，并记下内容指纹抑制
	// 延迟渲染造成的二次序列号变化。
	st.clipSeq.Store(platform.ClipboardSequence())
	st.clipSuppress = payload
	st.clipSuppressUntil = time.Now().Add(clipboardSuppressWindow)

	target := st.clipTarget
	if !target.Valid() {
		// 没有可恢复的目标窗口：面板保持可见，内容已在剪贴板里，用户可手动粘贴。
		st.setNotice(i18n.L("内容已复制，请手动粘贴", "Copied. Paste it manually."))
		return
	}
	// 管理员窗口：UIPI 会静默吞掉 SendInput（连失败都拿不到），必须改走临时
	// 提权辅助进程（UAC），否则就会出现“没粘贴、也没提示”。
	if platform.TargetNeedsElevation(target) {
		st.pasteElevated(target)
		return
	}
	st.hidePanel()
	time.AfterFunc(clipboardPasteDelay, func() {
		mygo.RunOnMain(func() {
			if platform.PasteToTarget(target, plain) {
				return
			}
			st.showPasteFallback(i18n.L("粘贴失败：内容已复制，请手动粘贴", "Paste failed. Content copied—paste it manually."))
		})
	})
}

// pasteElevated 通过管理员辅助进程把 Ctrl+V / Shift+Insert 送进目标窗口。
// 内容此时已经写好：UAC 被拒或辅助进程失败时，用户仍可手动粘贴。
func (st *appState) pasteElevated(target platform.ClipTarget) {
	exe, err := os.Executable()
	if err != nil {
		log.Printf("voke：提权粘贴无法定位 voke.exe：%v", err)
		st.showPasteFallback(i18n.L("粘贴失败：内容已复制，请手动粘贴", "Paste failed. Content copied—paste it manually."))
		return
	}
	mode := "gui"
	if target.Terminal {
		mode = "terminal"
	}
	args := []string{elevatedPasteFlag, strconv.FormatUint(uint64(target.Hwnd), 10), mode}

	// 先隐藏面板，把前台让回目标窗口；UAC 结束后辅助进程才能把目标切到前台。
	st.hidePanel()
	err = platform.RunElevated(exe, args, true, func(code uint32) {
		if code == 0 {
			return
		}
		mygo.RunOnMain(func() {
			if st.quitting || st.panel == nil || st.panel.IsDestroyed() {
				return
			}
			st.showPasteFallback(i18n.L("粘贴失败：内容已复制，请手动粘贴", "Paste failed. Content copied—paste it manually."))
		})
	})
	if err == nil {
		log.Printf("voke：已请求管理员权限粘贴（hwnd=%#x terminal=%v）",
			target.Hwnd, target.Terminal)
		return
	}
	msg := i18n.L("管理员粘贴启动失败：内容已复制，请手动粘贴", "Elevated paste failed to start. Content copied—paste it manually.")
	if errors.Is(err, platform.ErrElevationCanceled) {
		msg = i18n.L("未获得管理员权限，内容已复制，请手动粘贴", "Administrator permission not granted. Content copied—paste it manually.")
	}
	log.Printf("voke：提权粘贴启动失败（hwnd=%#x）：%v", target.Hwnd, err)
	st.showPasteFallback(msg)
}

// showPasteFallback 重新显示面板并提示用户手动粘贴。保留原目标窗口，
// 避免这次显示把「粘贴目标」覆盖成面板自身。走 showPanelKeep：失败回显必须
// 留在剪贴板页（插件热键进来的会话在隐藏时没有写入主快照）。
//
// 提权路径（UAC 取消 / 辅助进程失败）会先隐藏面板再 ShellExecuteEx：UAC 夺焦
// 让 OnBlur 排在消息队列里，回到主循环后可能把刚显示的面板再次隐藏。故稍后
// 再确认一次可见性——用户已主动收起则不打扰。
func (st *appState) showPasteFallback(msg string) {
	target := st.clipTarget
	st.showPanelKeep()
	st.clipTarget = target
	st.setNotice(msg)
	time.AfterFunc(60*time.Millisecond, func() {
		mygo.RunOnMain(func() {
			if st.quitting || st.panel == nil || st.panel.IsDestroyed() || st.panel.IsVisible() {
				return
			}
			st.clipTarget = target
			st.showPanelKeep()
			st.setNotice(msg)
		})
	})
}

// toggleClipboardPin 置顶 / 取消置顶当前条目。置顶已满时给提示、不改历史。
func (st *appState) toggleClipboardPin(ref string) {
	if st.hist == nil {
		return
	}
	it, ok := st.hist.Find(ref)
	if !ok {
		return
	}
	if it.Pinned {
		if !st.hist.Unpin(ref) {
			return
		}
	} else if !st.hist.Pin(ref) {
		st.setNotice(i18n.L("置顶最多 10 条，请先取消一条", "At most 10 pinned items—unpin one first"))
		return
	}
	st.persistClipItem(ref)
	st.refreshClipboardRows()
}

// deleteClipboardItem 删除一条历史并刷新列表。
func (st *appState) deleteClipboardItem(ref string) {
	if st.hist == nil {
		return
	}
	if !st.hist.Delete(ref) {
		return
	}
	st.persistClipReconcile()
	st.refreshClipboardRows()
}

// toggleSelectedPin 是 Ctrl+Shift+T 的入口：只对剪贴板插件的当前选中行生效，
// 其它模式静默忽略。
func (st *appState) toggleSelectedPin() {
	if !st.isClipboardPlugin() || st.provChosen < 0 || st.provChosen >= len(st.provRows) {
		return
	}
	if ref := st.provRows[st.provChosen].Action.Ref; ref != "" {
		st.toggleClipboardPin(ref)
	}
}

// deleteSelected 是 Ctrl+Delete 的入口：按当前行的 RowAction.Shortcut 分派——
// 剪贴板行删除条目，窗口行关闭窗口（WM_CLOSE），其它行静默忽略。
func (st *appState) deleteSelected() {
	st.runSelectedRowShortcut("Ctrl+Delete")
}

// pasteSelectedPlain 是 Ctrl+Enter 的入口：仅剪贴板插件态把当前行按纯文本粘贴。
// 返回 false 表示未消费，交回普通 Enter 路径（保持其它插件的 Ctrl+Enter 行为）。
func (st *appState) pasteSelectedPlain() bool {
	if !st.isClipboardPlugin() || st.provChosen < 0 || st.provChosen >= len(st.provRows) {
		return false
	}
	it, ok := st.hist.Find(st.provRows[st.provChosen].Action.Ref)
	if !ok {
		return false
	}
	if it.Text == "" {
		st.setNotice(i18n.L("该条目没有文本，无法纯文本粘贴", "This item has no text to paste as plain text"))
		return true
	}
	st.pasteClipboardItem(it.ID, true)
	return true
}
