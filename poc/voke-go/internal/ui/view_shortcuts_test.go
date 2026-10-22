package ui

import (
	"testing"

	goui "github.com/egoist/mygo/ui"
)

// Ctrl+Enter 由 app 层按插件分派（剪贴板纯文本 / 终端管理员打开），必须在
// **真实帧**里验证：mygo 的 editor.wants 对带 Ctrl 的 Enter 返回 false（不进入
// 输入框），所以它只能由窗口级 Shortcut 消费。本用例锁住「触发 OnCtrlEnter、
// 不触发 OnOpen」。
func TestCtrlEnterTriggersHandler(t *testing.T) {
	var query string
	var input goui.Handle
	var plain, open int

	tt := goui.NewTester(func(c *goui.Context) {
		View(c, ViewInput{
			Hotkey: "Alt+Space", Query: &query, Input: &input, Placeholder: "搜索",
			OnEsc: func() {}, OnMove: func(int) {}, OnOpen: func() { open++ },
			OnCtrlEnter: func() bool { plain++; return true },
			OnPinToggle: func() {}, OnDeleteSelected: func() {},
			OnQueryChanged: func() {}, OnBackspaceAtEmpty: func() bool { return false },
			OnResetInput: func() {}, OnQuit: func() {}, OnOpenSettings: func() {},
			RenderResults: func(*goui.Context) {},
		})
	}, 680, 494)

	tt.Frame() // 注册快捷键
	tt.Key(goui.Ctrl, goui.KeyEnter)
	tt.Frame()
	if plain != 1 || open != 0 {
		t.Fatalf("Ctrl+Enter 应只触发纯文本粘贴：plain=%d open=%d", plain, open)
	}

	tt.Key(0, goui.KeyEnter)
	tt.Frame()
	if open != 1 {
		t.Fatalf("普通 Enter 应仍走打开：open=%d", open)
	}
}

// Ctrl+Shift+Delete 会被输入框编辑器视为「删后一个词」（KeyDelete 的 wants 恒真），
// 必须在 OnTextKey 里先截住；本用例验证它确实到达 OnEndTask。
func TestEndTaskShortcutReachesWindow(t *testing.T) {
	var query string
	var input goui.Handle
	var end int

	tt := goui.NewTester(func(c *goui.Context) {
		View(c, ViewInput{
			Hotkey: "Alt+Space", Query: &query, Input: &input, Placeholder: "搜索",
			OnEsc: func() {}, OnMove: func(int) {}, OnOpen: func() {},
			OnCtrlEnter: func() bool { return true },
			OnPinToggle: func() {}, OnDeleteSelected: func() {}, OnEndTask: func() { end++ },
			OnQueryChanged: func() {}, OnBackspaceAtEmpty: func() bool { return false },
			OnResetInput: func() {}, OnQuit: func() {}, OnOpenSettings: func() {},
			RenderResults: func(*goui.Context) {},
		})
	}, 680, 494)

	tt.Frame()
	tt.Key(goui.Ctrl|goui.Shift, goui.KeyDelete)
	tt.Frame()
	if end != 1 {
		t.Fatalf("Ctrl+Shift+Delete 应到达 OnEndTask，得到 %d", end)
	}
}

// Ctrl+Shift+P / Ctrl+Shift+D 同样走窗口级快捷键（编辑器不接收带 Ctrl 的字母键）。
func TestClipboardShortcutKeysReachWindow(t *testing.T) {
	var query string
	var input goui.Handle
	var pin, del int

	tt := goui.NewTester(func(c *goui.Context) {
		View(c, ViewInput{
			Hotkey: "Alt+Space", Query: &query, Input: &input, Placeholder: "搜索",
			OnEsc: func() {}, OnMove: func(int) {}, OnOpen: func() {},
			OnCtrlEnter: func() bool { return true },
			OnPinToggle: func() { pin++ }, OnDeleteSelected: func() { del++ },
			OnQueryChanged: func() {}, OnBackspaceAtEmpty: func() bool { return false },
			OnResetInput: func() {}, OnQuit: func() {}, OnOpenSettings: func() {},
			RenderResults: func(*goui.Context) {},
		})
	}, 680, 494)

	tt.Frame()
	tt.Key(goui.Ctrl|goui.Shift, goui.KeyP)
	tt.Frame()
	tt.Key(goui.Ctrl|goui.Shift, goui.KeyD)
	tt.Frame()
	if pin != 1 || del != 1 {
		t.Fatalf("Ctrl+Shift+P/D 未到达应用层：pin=%d delete=%d", pin, del)
	}
}
