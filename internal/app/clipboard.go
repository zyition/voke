// clipboard.go：剪贴板历史的运行时装配——后台轮询采集、程序化写入抑制、
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
	"voke/internal/logic/appcfg"
	"voke/internal/logic/cliphist"
	"voke/internal/platform"
	clipboardprovider "voke/internal/providers/clipboard"
)

const (
	// clipboardPollInterval 是序列号轮询周期。只读一个 DWORD，500ms 足够及时，
	// 又不会为后台轮询占太多 CPU。
	clipboardPollInterval = 500 * time.Millisecond
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

// startClipboardWatcher 启动后台轮询：只读 Win32 序列号，**变化后**才回主线程
// 读内容、写历史。
//
// 省电约束（笔记本场景）：
//   - 序列号比较放在后台 goroutine 里，稳态下**不产生任何主线程唤醒**（早先在每次
//     tick 都投一次 RunOnMain，等于每 500ms 白叫一次消息循环）；
//   - 剪贴板插件被停用时 clipWatchOn=false，连序列号系统调用都不做；
//   - 空转时只有一个 ticker 与一次 GetClipboardSequenceNumber（读一个 DWORD）。
//
// 必须等 App 就绪后调用（ready 里）；goroutine 生命周期与进程一致。
func (st *appState) startClipboardWatcher() {
	st.clipSeq.Store(platform.ClipboardSequence())
	// 首选事件驱动：系统级 WM_CLIPBOARDUPDATE（见 platform.StartClipboardWatch），
	// 空闲时零唤醒、零系统调用。
	if ch, stop, ok := platform.StartClipboardWatch(); ok {
		st.clipWatchStop = stop
		go func() {
			for range ch {
				if !st.clipWatchOn.Load() {
					continue
				}
				mygo.RunOnMain(st.onClipboardSignal)
			}
		}()
		return
	}
	// 回退：500ms 轮询序列号（非 Windows / 监听窗口创建失败）。只读一个 DWORD，
	// 序列号比较放在后台，稳态下不唤醒主线程。
	go func() {
		ticker := time.NewTicker(clipboardPollInterval)
		defer ticker.Stop()
		var last uint32
		for range ticker.C {
			if !st.clipWatchOn.Load() {
				last = 0 // 停用期间不做任何系统调用
				continue
			}
			seq := platform.ClipboardSequence()
			if seq == 0 || seq == last {
				continue // 序列号不可用 / 没变化
			}
			last = seq
			if st.clipSeq.Load() == seq {
				continue // 程序化写入已同步过
			}
			mygo.RunOnMain(func() {
				st.clipSeq.Store(seq)
				st.captureClipboard()
			})
		}
	}()
}

// onClipboardSignal 在主线程处理「系统剪贴板可能变了」：事件会合并、也会被我们自己
// 的写入触发，所以重读序列号去重，真变了才读内容。
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

// syncClipboardWatch 按配置决定后台轮询是否工作（剪贴板插件停用 → 完全空转）。
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
	if _, err := st.hist.Add(cliphist.Payload{Text: p.Text, HTML: string(p.HTML), RTF: p.RTF, PNG: p.PNG}, time.Now()); err != nil {
		// 空内容 / 超上限是预期跳过，不刷日志；其它错误同样不应打断面板。
		return
	}
	if st.isClipboardPlugin() && st.panel != nil && st.panel.IsVisible() {
		st.refreshClipboardRows()
	}
}

// isClipboardPlugin 报告当前是否落在剪贴板插件态。
func (st *appState) isClipboardPlugin() bool {
	return st.providerActive() && st.engineState.PluginID() == providerClipboardID
}

// refreshClipboardRows 重跑剪贴板查询，并尽量保持当前选中行。
func (st *appState) refreshClipboardRows() {
	keep := st.provChosen
	st.refreshProviderResults()
	st.chooseProvider(keep)
}

// setNotice 在主线程设置一次性提示并请求重绘。
func (st *appState) setNotice(msg string) {
	st.notice = msg
	st.noticeAt = time.Now()
	if st.panel != nil && st.panel.IsVisible() {
		st.panel.Update(func() {})
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
		st.setNotice("粘贴失败：无法写入剪贴板")
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
		st.setNotice("内容已复制，请手动粘贴")
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
			st.showPasteFallback("粘贴失败：内容已复制，请手动粘贴")
		})
	})
}

// pasteElevated 通过管理员辅助进程把 Ctrl+V / Shift+Insert 送进目标窗口。
// 内容此时已经写好：UAC 被拒或辅助进程失败时，用户仍可手动粘贴。
func (st *appState) pasteElevated(target platform.ClipTarget) {
	exe, err := os.Executable()
	if err != nil {
		log.Printf("voke：提权粘贴无法定位 voke.exe：%v", err)
		st.showPasteFallback("粘贴失败：内容已复制，请手动粘贴")
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
			st.showPasteFallback("粘贴失败：内容已复制，请手动粘贴")
		})
	})
	if err == nil {
		log.Printf("voke：已请求管理员权限粘贴（hwnd=%#x terminal=%v）",
			target.Hwnd, target.Terminal)
		return
	}
	msg := "管理员粘贴启动失败：内容已复制，请手动粘贴"
	if errors.Is(err, platform.ErrElevationCanceled) {
		msg = "未获得管理员权限，内容已复制，请手动粘贴"
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
		st.hist.Unpin(ref)
	} else if !st.hist.Pin(ref) {
		st.setNotice("置顶最多 10 条，请先取消一条")
		return
	}
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
		st.setNotice("该条目没有文本，无法纯文本粘贴")
		return true
	}
	st.pasteClipboardItem(it.ID, true)
	return true
}
