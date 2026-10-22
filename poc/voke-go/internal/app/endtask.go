// endtask.go：窗口 provider 的「结束任务」动作——快捷键选取、执行与无响应兜底。
//
// 语义对齐 Windows 任务栏「结束任务」：先 WM_CLOSE 让应用自己退出（可弹保存
// 提示）；后台等待宽限期后，仅当窗口仍存在且被系统判定无响应时才 TerminateProcess。
// 响应正常的应用不会被强杀。
package app

import (
	"log"
	"time"

	"github.com/zyition/voke/poc/voke-go/internal/platform"
	"github.com/zyition/voke/poc/voke-go/internal/providers"
	"github.com/zyition/voke/poc/voke-go/internal/providers/windows"
)

// endTaskHungGrace 覆盖 IsHungAppWindow 的 5 秒无响应判定阈值，留一点余量。
const endTaskHungGrace = 6 * time.Second

// endTaskSelected 是 Ctrl+Shift+Delete 的入口：只有当前选中行是窗口结果时才动作；
// 其它模式 / 其它插件行静默忽略，不抢占按键语义。
func (st *appState) endTaskSelected() {
	m, ok := st.selectedWindowMatch()
	if !ok {
		return
	}
	for _, a := range m.Actions {
		if a.ID == windows.EndTaskActionID {
			m.Action = a.Action
			st.runProviderIntent(m, m.ProviderID)
			return
		}
	}
}

// selectedWindowMatch 取当前选中行的窗口 Match：插件态直接取 provRows；全局合并态
// 只在选中行属于 windows 组时取 globalProvRows。apps 行与其它 provider 行都返回 false。
func (st *appState) selectedWindowMatch() (providers.Match, bool) {
	if st.providerActive() && st.engineState.PluginID() == providerWindowsID {
		if st.provChosen < 0 || st.provChosen >= len(st.provRows) {
			return providers.Match{}, false
		}
		return st.provRows[st.provChosen].Match, true
	}
	if st.globalActive() {
		if st.globalChosen < 0 || st.globalChosen >= len(st.globalRows) {
			return providers.Match{}, false
		}
		r := st.globalRows[st.globalChosen]
		if r.apps || r.groupID != providerWindowsID || r.provIdx < 0 || r.provIdx >= len(st.globalProvRows) {
			return providers.Match{}, false
		}
		return st.globalProvRows[r.provIdx].Match, true
	}
	return providers.Match{}, false
}

// endTask 发起结束任务。返回 true 表示 WM_CLOSE 已投递（调用方隐藏面板并复位）；
// false 表示请求失败，面板保留并显示提示。
//
// 强杀兜底在后台 goroutine：它要等满宽限期，绝不能占用主线程。goroutine 只写日志，
// 不触碰 appState。
func (st *appState) endTask(hwnd uintptr, title string) bool {
	if err := platform.RequestEndTask(hwnd); err != nil {
		log.Printf("voke：结束任务失败（窗口 %q，hwnd=%#x）：%v", title, hwnd, err)
		st.setNotice("无法结束任务（窗口可能受保护或权限不足）")
		return false
	}
	go func() {
		forced, err := platform.ForceEndTaskIfHung(hwnd, endTaskHungGrace)
		switch {
		case err != nil:
			log.Printf("voke：结束任务兜底失败（窗口 %q，hwnd=%#x）：%v", title, hwnd, err)
		case forced:
			log.Printf("voke：窗口 %q 无响应，已强制结束", title)
		}
	}()
	return true
}
