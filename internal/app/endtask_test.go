package app

import (
	"testing"

	"voke/internal/logic/engine"
	"voke/internal/providers"
	"voke/internal/providers/windows"
)

func windowTaskMatch(hwnd uintptr) providers.Match {
	return providers.Match{
		ProviderID: windows.ID,
		Title:      "Window",
		Action:     providers.Action{Kind: providers.ActionActivateWindow, Hwnd: hwnd},
		Actions: []providers.RowAction{
			{
				ID:       windows.CloseWindowActionID,
				Shortcut: "Ctrl+Delete",
				Action:   providers.Action{Kind: providers.ActionCloseWindow, Hwnd: hwnd},
			},
			{
				ID:       windows.EndTaskActionID,
				Shortcut: "Ctrl+Shift+Delete",
				Action:   providers.Action{Kind: providers.ActionEndTask, Hwnd: hwnd},
			},
		},
	}
}

// Linux 上 platform.EndTask 是占位错误；用它验证「选中窗口行 → 发结束意图 →
// 失败时保留面板并提示」这条无副作用的链路。
func TestEndTaskSelectedPluginMode(t *testing.T) {
	st := &appState{pluginReg: builtinProviders()}
	st.engineState = engine.SelectPlugin(engine.State{}, windows.ID)
	st.provRows = []providers.RankedMatch{{Match: windowTaskMatch(123)}}
	st.provChosen = 0

	st.endTaskSelected()
	if st.notice == "" {
		t.Fatal("结束任务失败应设置提示")
	}
}

func TestEndTaskSelectedGlobalMode(t *testing.T) {
	st := &appState{pluginReg: builtinProviders()}
	st.globalRows = []globalRow{{provIdx: 0, groupID: windows.ID}}
	st.globalProvRows = []providers.RankedMatch{{Match: windowTaskMatch(456)}}
	st.globalChosen = 0

	st.endTaskSelected()
	if st.notice == "" {
		t.Fatal("全局窗口行结束失败应设置提示")
	}
}

func TestEndTaskSelectedIgnoresNonWindowRows(t *testing.T) {
	st := &appState{pluginReg: builtinProviders()}
	// 插件态但选中的是别的插件：不动作。
	st.engineState = engine.SelectPlugin(engine.State{}, "base64")
	st.provRows = []providers.RankedMatch{{Match: windowTaskMatch(1)}}
	st.provChosen = 0
	st.endTaskSelected()
	if st.notice != "" {
		t.Fatalf("非窗口插件不应触发结束任务：%q", st.notice)
	}

	// 全局态但选中 apps 行：不动作。
	st = &appState{pluginReg: builtinProviders()}
	st.globalRows = []globalRow{{apps: true, appIdx: 0, groupID: appsProviderID}}
	st.globalProvRows = []providers.RankedMatch{{Match: windowTaskMatch(2)}}
	st.globalChosen = 0
	st.endTaskSelected()
	if st.notice != "" {
		t.Fatalf("apps 行不应触发结束任务：%q", st.notice)
	}
}

// Ctrl+Delete 走通用 RowAction.Shortcut 分派：窗口行应命中「关闭窗口」，
// 失败时保留面板并提示（hwnd=0 在 Linux / Windows 都必败，链路确定）。
func TestDeleteSelectedClosesWindow(t *testing.T) {
	st := &appState{pluginReg: builtinProviders()}
	st.engineState = engine.SelectPlugin(engine.State{}, windows.ID)
	st.provRows = []providers.RankedMatch{{Match: windowTaskMatch(0)}}
	st.provChosen = 0

	st.deleteSelected()
	if st.notice == "" {
		t.Fatal("Ctrl+Delete 应命中窗口关闭动作并给出失败提示")
	}
}

func TestProviderActionCloseWindow(t *testing.T) {
	st := &appState{pluginReg: builtinProviders()}
	st.engineState = engine.SelectPlugin(engine.State{}, windows.ID)
	st.provRows = []providers.RankedMatch{{Match: windowTaskMatch(0)}}
	st.provChosen = 0

	// hwnd=0 在 Linux 占位与 Windows 真实现下都必然失败，正好验证失败提示链路。
	st.providerAction(0, windows.CloseWindowActionID)
	if st.notice == "" {
		t.Fatal("关闭窗口失败应设置提示")
	}
}

func TestGlobalActionEndTask(t *testing.T) {
	st := &appState{pluginReg: builtinProviders()}
	st.globalRows = []globalRow{{provIdx: 0, groupID: windows.ID}}
	st.globalProvRows = []providers.RankedMatch{{Match: windowTaskMatch(789)}}
	st.globalAction(0, windows.EndTaskActionID)
	if st.notice == "" {
		t.Fatal("全局行点击结束任务应进入执行器")
	}

	st = &appState{pluginReg: builtinProviders()}
	st.globalRows = []globalRow{{apps: true, appIdx: 0, groupID: appsProviderID}}
	st.globalAction(0, windows.EndTaskActionID)
	if st.notice != "" {
		t.Fatalf("apps 行不应响应行内操作：%q", st.notice)
	}
}

func TestEndTaskSelectedNoRows(t *testing.T) {
	st := &appState{pluginReg: builtinProviders()}
	st.engineState = engine.SelectPlugin(engine.State{}, windows.ID)
	st.endTaskSelected() // 不得 panic
}
