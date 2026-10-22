// endtask.go：窗口 provider 的「结束任务」动作——快捷键选取与执行。
//
// 语义是直接结束窗口所属进程（TerminateProcess），不是只关窗口：带托盘 / 后台
// 常驻的应用也会被彻底结束。因此不会给应用保存机会，未保存数据会丢失。
package app

import (
	"log"

	"github.com/zyition/voke/poc/voke-go/internal/platform"
	"github.com/zyition/voke/poc/voke-go/internal/providers"
	"github.com/zyition/voke/poc/voke-go/internal/providers/windows"
)

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

// endTask 直接结束窗口所属进程。返回 true 表示已提交终止（调用方隐藏面板并复位）；
// false 表示失败，面板保留并显示提示。
func (st *appState) endTask(hwnd uintptr, title string) bool {
	if err := platform.EndTask(hwnd); err != nil {
		log.Printf("voke：结束任务失败（窗口 %q，hwnd=%#x）：%v", title, hwnd, err)
		st.setNotice("无法结束任务（窗口可能受保护或权限不足）")
		return false
	}
	log.Printf("voke：已结束任务 %q（hwnd=%#x）", title, hwnd)
	return true
}
