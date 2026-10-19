// windowfilter.go：窗口枚举的**纯过滤逻辑**（对照 voke-sys/src/windows.rs 的非 Win32 那半）。
//
// 生产刻意把「该不该收这个窗口」做成纯函数（入参全是 bool/string/数值，由 Win32 侧取好喂进来），
// 好在任何平台编译 + 单测——真实枚举（EnumWindows 回调）没法在 WSL 跑，但过滤规则能。本层同构：
// 放 platform 且**不挂 build 标签**，Linux 原生 `go test` 直接覆盖（见 windowfilter_test.go）。
// 真正的 enumWindows（Windows-only syscall）复用这些谓词。
//
// 过滤链（顺序按「便宜的先做」，逐条对照 windows.rs 模块文档）：
//  1. !IsWindowVisible → 剔除（用户看不见，切过去无意义）；
//  2. pid == 本进程 → 剔除（否则 voke 搜到自己，gpui 窗口类名是 `Zed::Window`）；
//  3. 标题空白 → 剔除（工具窗 / 消息窗不可辨识）；
//  4. 类名在 ExcludedClasses → 剔除（任务栏 / 桌面 / IME / Win11 覆盖层）；
//  5. cloaked（DWMWA_CLOAKED）→ 剔除（UWP 幽灵窗口，一堆同名重复）；
//  6. WS_EX_TOOLWINDOW 且不带 WS_EX_APPWINDOW → 剔除（托盘 / 工具进程不进 Alt-Tab；
//     同时置 APPWINDOW 者有意上任务栏，留）。
package platform

import "strings"

// ExcludedClasses 是系统覆盖层类名（精确匹配 + `.后缀` 变体，大小写不敏感）。
// 后两个（IslandWindow / WinUIDesktopWin32WindowClass）是 Windows 实机探针观察到的
// Win11 覆盖层，有非空中文标题、进程是 explorer.exe，不滤掉会污染结果列表。
var ExcludedClasses = []string{
	"Shell_TrayWnd",
	"Shell_SecondaryTrayWnd",
	"WorkerW",
	"Progman",
	"Windows.UI.Core.CoreWindow",
	"IslandWindow",
	"WinUIDesktopWin32WindowClass",
}

// IsExcludedClass 判断类名是否属于要剔除的系统覆盖层（大小写不敏感、两边都归一）。
// 前缀匹配以覆盖 `WorkerW.xxx` 这类带后缀变体，同时不误伤 `WorkerWindow`
// （后缀必须是空或 `.` 起头）。
//
// 两边都 toLower 是踩过坑的：常量是 `Shell_TrayWnd` 混合大小写，只小写化输入再比
// 前缀会永远匹配不上（实测 Progman / Shell_TrayWnd 全部漏过）。
func IsExcludedClass(class string) bool {
	lower := strings.ToLower(class)
	for _, ex := range ExcludedClasses {
		ex = strings.ToLower(ex)
		if rest, ok := cutPrefix(lower, ex); ok && (rest == "" || strings.HasPrefix(rest, ".")) {
			return true
		}
	}
	return false
}

// cutPrefix 是小依赖的前缀切分（不引 strings.CutPrefix 以贴合逐条语义、便于版本无关）。
func cutPrefix(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):], true
	}
	return s, false
}

// HasExtStyle 判断窗口扩展样式里是否置了某一位（宽度无关：exStyle 取 LONG_PTR，按位与）。
func HasExtStyle(exStyle int64, styleBit uint32) bool {
	return exStyle&int64(styleBit) != 0
}

// 窗口扩展样式位（WS_EX_*）：跨平台数值常量，过滤/枚举/测试都用，故放非门控文件。
const (
	WsExToolWindow = 0x00000080 // WS_EX_TOOLWINDOW：不进任务栏 / Alt-Tab
	WsExAppWindow  = 0x00020000 // WS_EX_APPWINDOW：强制上任务栏（压过 TOOLWINDOW）
)

// ShouldIncludeWindow 是过滤链综合判定（纯逻辑，入参全部由 Win32 侧取好传入）。
// toolWindow / appWindow 分别是 WS_EX_TOOLWINDOW / WS_EX_APPWINDOW 是否置位。
func ShouldIncludeWindow(visible, cloaked bool, title, class string, pid, ownPid uint32, toolWindow, appWindow bool) bool {
	if !visible {
		return false
	}
	if pid == ownPid {
		return false
	}
	if strings.TrimSpace(title) == "" {
		return false
	}
	if IsExcludedClass(class) {
		return false
	}
	if cloaked {
		return false
	}
	if toolWindow && !appWindow {
		return false
	}
	return true
}

// ProcessNameFromPath 从完整进程路径取文件名（`C:\a\Code.exe` → `Code.exe`）；同时认
// `\` 与 `/`（Linux 单测）；空路径返回空串（受保护进程取不到路径，副标题留空）。
func ProcessNameFromPath(path string) string {
	if path == "" {
		return ""
	}
	if i := strings.LastIndexAny(path, `/\`); i >= 0 {
		return path[i+1:]
	}
	return path
}
