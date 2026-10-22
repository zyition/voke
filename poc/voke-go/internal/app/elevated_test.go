package app

import (
	"strings"
	"testing"

	"github.com/zyition/voke/poc/voke-go/internal/logic/engine"
	"github.com/zyition/voke/poc/voke-go/internal/providers"
	"github.com/zyition/voke/poc/voke-go/internal/providers/terminal"
)

func TestHandleElevatedPasteArgs(t *testing.T) {
	if handled, _ := HandleElevatedPaste(nil); handled {
		t.Error("普通启动不应被当成提权粘贴")
	}
	if handled, _ := HandleElevatedPaste([]string{"--something-else"}); handled {
		t.Error("其它参数不应被当成提权粘贴")
	}
	for _, args := range [][]string{
		{elevatedPasteFlag},
		{elevatedPasteFlag, "not-a-number", "gui"},
		{elevatedPasteFlag, "0", "gui"},
		{elevatedPasteFlag, "123", "bogus"},
	} {
		handled, code := HandleElevatedPaste(args)
		if !handled || code != 2 {
			t.Errorf("%v 应返回参数错误（handled=%v code=%d）", args, handled, code)
		}
	}
	// 非 Windows 上 WindowProcessID 恒 0：合法参数也会在“目标不是管理员进程”处拒绝。
	if handled, code := HandleElevatedPaste([]string{elevatedPasteFlag, "123", "gui"}); !handled || code != 1 {
		t.Errorf("取不到目标进程时应拒绝执行，handled=%v code=%d", handled, code)
	}
}

// Ctrl+Enter 在终端插件里应命中行内“以管理员身份打开”，而不是剪贴板纯文本路径。
// Linux 上 RunElevated 是占位错误：动作被识别并执行到失败分支，设置提示、面板保留。
func TestCtrlEnterRunsTerminalAdminAction(t *testing.T) {
	st := &appState{
		pluginReg:   builtinProviders(),
		engineState: engine.SelectPlugin(engine.State{}, terminal.ID),
		provRows: []providers.RankedMatch{{Match: providers.Match{
			ProviderID: terminal.ID,
			Title:      "Windows PowerShell",
			Actions: []providers.RowAction{{
				ID:       "open-admin",
				Shortcut: "Ctrl+Enter",
				Action: providers.Action{
					Kind:         providers.ActionLaunchTerminalProfile,
					Elevated:     true,
					Profile:      "PowerShell",
					FallbackPath: `C:\wt\wt.exe`,
				},
			}},
		}}},
		provChosen: 0,
	}
	if !st.ctrlEnterSelected() {
		t.Fatal("Ctrl+Enter 应命中管理员动作")
	}
	if !strings.Contains(st.notice, "管理员") {
		t.Errorf("执行失败应留下管理员提示，实得 %q", st.notice)
	}
}
