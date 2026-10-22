package platform

import "testing"

func TestIsTerminalWindow(t *testing.T) {
	cases := []struct {
		name  string
		class string
		exe   string
		want  bool
	}{
		{"conhost 类名", "ConsoleWindowClass", "whatever.exe", true},
		{"Windows Terminal 类名", "CASCADIA_HOSTING_WINDOW_CLASS", "", true},
		{"ConEmu 类名", "VirtualConsoleClass", "", true},
		{"mintty 类名", "mintty", "", true},
		{"Windows Terminal 进程兜底", "CASCADIA", "WindowsTerminal.exe", true},
		{"pwsh 进程兜底", "", "pwsh.exe", true},
		{"普通 GUI 窗口", "Chrome_WidgetWin_1", "code.exe", false},
		{"空目标", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsTerminalWindow(c.class, c.exe); got != c.want {
				t.Fatalf("IsTerminalWindow(%q, %q) = %v，期望 %v", c.class, c.exe, got, c.want)
			}
		})
	}
}
