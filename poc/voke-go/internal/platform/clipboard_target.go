// clipboard_target.go：剪贴板「粘贴回原窗口」的目标描述与终端判定。纯数据 + 纯函数，
// 任何平台可编译、Linux 可单测；真正的窗口捕获与按键模拟在 clipboard_windows.go。
package platform

import "strings"

// ClipTarget 是唤起面板时的前台窗口快照；零值 = 没记住（粘贴动作应回退为「已复制」）。
type ClipTarget struct {
	Hwnd uintptr
	// Class 是窗口类名（ConsoleWindowClass / CASCADIA_HOSTING_WINDOW_CLASS / …）。
	Class string
	// Exe 是进程文件名（小写，如 windowsterminal.exe / code.exe）。
	Exe string
	// Terminal 为 true 时粘贴用 Shift+Insert（终端兼容性优于 Ctrl+V）。
	Terminal bool
}

// Valid 表示捕获到了有效窗口。
func (t ClipTarget) Valid() bool { return t.Hwnd != 0 }

// terminalClasses / terminalExes 是「粘贴要按 Shift+Insert」的判定表。
// 类名优先（控制台宿主进程名不一定是用户启动的程序）；进程名兜底覆盖
// 使用自绘窗口类的终端。VS Code 等内嵌终端不在此列——它们接受 Ctrl+V。
var terminalClasses = map[string]bool{
	"ConsoleWindowClass":            true, // conhost（cmd / powershell / wsl 等）
	"CASCADIA_HOSTING_WINDOW_CLASS": true, // Windows Terminal
	"VirtualConsoleClass":           true, // ConEmu / Cmder
	"mintty":                        true, // Git Bash / Cygwin / MSYS2
	"PuTTY":                         true,
	"KiTTY":                         true,
	"Alacritty":                     true,
	"org.wezfurlong.wezterm":        true,
	"tabby":                         true,
}

var terminalExes = map[string]bool{
	"windowsterminal.exe": true,
	"openconsole.exe":     true,
	"conhost.exe":         true,
	"cmd.exe":             true,
	"powershell.exe":      true,
	"pwsh.exe":            true,
	"wt.exe":              true,
	"mintty.exe":          true,
	"putty.exe":           true,
	"alacritty.exe":       true,
	"wezterm-gui.exe":     true,
	"conemu.exe":          true,
	"conemu64.exe":        true,
	"kitty.exe":           true,
	"tabby.exe":           true,
}

// IsTerminalWindow 判断目标窗口是否应按终端处理（Shift+Insert）。class 按原样比较，
// exe 统一转小写后查表；两者都为空时返回 false。
func IsTerminalWindow(class, exe string) bool {
	return terminalClasses[class] || terminalExes[strings.ToLower(exe)]
}
