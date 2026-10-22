package platform

import (
	"strings"
	"testing"
)

// TestExcludedClasses 覆盖 windows.rs：黑名单全命中、大小写两侧都归一、不误伤普通类名、
// 带 `.` 后缀的变体命中而不同前缀不误伤。
func TestExcludedClasses(t *testing.T) {
	for _, class := range append(ExcludedClasses, "windows.ui.core.corewindow") {
		if !IsExcludedClass(class) {
			t.Errorf("%q 应被剔除", class)
		}
	}
	// 大写形态也应命中（两边归一）。
	for _, class := range []string{"SHELL_TRAYWND", "PROGMAN", "WINDOWS.UI.CORE.COREWINDOW"} {
		if !IsExcludedClass(class) {
			t.Errorf("大写 %q 应被剔除", class)
		}
	}
	for _, class := range []string{
		"Zed::Window", "Chrome_WidgetWin_1", "ApplicationFrameWindow",
		"CASCADIA_HOSTING_WINDOW_CLASS", "Notepad", "MozillaWindowClass",
		"HwndWrapper[Code.exe;;guid]",
	} {
		if IsExcludedClass(class) {
			t.Errorf("%q 不该被剔除", class)
		}
	}
	// 后缀变体：`WorkerW.xxx` 剔除、`WorkerWindow`（不同词）不误伤。
	if !IsExcludedClass("WorkerW.Something") {
		t.Error("WorkerW 后缀变体应剔除")
	}
	if IsExcludedClass("WorkerWindow") {
		t.Error("WorkerWindow 不该被误伤")
	}
}

func TestHasExtStyle(t *testing.T) {
	if !HasExtStyle(WsExToolWindow, WsExToolWindow) {
		t.Error("置了 TOOLWINDOW 位应判真")
	}
	if !HasExtStyle(int64(WsExToolWindow)|0x08000000, WsExToolWindow) {
		t.Error("混其它低位仍应命中 TOOLWINDOW")
	}
	if HasExtStyle(0, WsExToolWindow) {
		t.Error("0 不该命中")
	}
	if HasExtStyle(0x00000004, WsExToolWindow) {
		t.Error("别的位不该误命中 TOOLWINDOW")
	}
	// 高位（64 位扩展样式）不影响低位判定。
	if !HasExtStyle(int64(WsExToolWindow)|(1<<40), WsExToolWindow) {
		t.Error("64 位高位不应影响低位判定")
	}
}

// include 用 ownPid=1 固定，其余透传（对齐 windows.rs 的测试夹具）。
func include(visible, cloaked bool, title, class string, pid uint32, tool, app bool) bool {
	return ShouldIncludeWindow(visible, cloaked, title, class, pid, 1, tool, app)
}

func TestShouldIncludeWindowCoversAllRules(t *testing.T) {
	if !include(true, false, "main.rs - voke", "Zed::Window", 42, false, false) {
		t.Error("正常窗口应收")
	}
	if include(false, false, "hidden", "Zed::Window", 42, false, false) {
		t.Error("不可见应剔")
	}
	if include(true, true, "Settings", "ApplicationFrameWindow", 42, false, false) {
		t.Error("cloaked（UWP 幽灵）应剔")
	}
	if include(true, false, "", "Chrome_WidgetWin_1", 42, false, false) {
		t.Error("空标题应剔")
	}
	if include(true, false, "   ", "Chrome_WidgetWin_1", 42, false, false) {
		t.Error("空白标题应剔")
	}
	if include(true, false, "voke", "Zed::Window", 1, false, false) {
		t.Error("本进程窗口应剔（否则搜到自己）")
	}
	if include(true, false, "Program Manager", "Progman", 42, false, false) {
		t.Error("系统覆盖层应剔")
	}
	if include(true, false, "Windows 输入体验", "Windows.UI.Core.CoreWindow", 42, false, false) {
		t.Error("IME 候选窗口（此类+非空标题）应剔")
	}
	// 工具窗口规则。
	if include(true, false, "Zephyr 通知栏托盘", "WindowsForms10.Window.8", 42, true, false) {
		t.Error("WS_EX_TOOLWINDOW 不该进结果")
	}
	if !include(true, false, "Real App", "Chrome_WidgetWin_1", 42, true, true) {
		t.Error("WS_EX_APPWINDOW 应压过 TOOLWINDOW")
	}
	if !include(true, false, "Notepad", "Notepad", 42, false, false) {
		t.Error("普通窗口不受工具位影响")
	}
}

func TestProcessNameFromPath(t *testing.T) {
	cases := map[string]string{
		`C:\a\Code.exe`:        "Code.exe",
		"C:/a/wezterm-gui.exe": "wezterm-gui.exe",
		"Code.exe":             "Code.exe",
		"":                     "",
	}
	for in, want := range cases {
		if got := ProcessNameFromPath(in); got != want {
			t.Errorf("ProcessNameFromPath(%q) = %q，期望 %q", in, got, want)
		}
	}
	// 末尾分隔符的退化情形不应 panic（取空段）。
	if got := ProcessNameFromPath(`C:\a\`); strings.TrimSpace(got) != "" {
		t.Errorf("尾分隔符应得空段，拿到 %q", got)
	}
}
