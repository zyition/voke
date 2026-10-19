// hotkey.go：§7.2.2 点名的**全局热键薄封装**（重构步骤 8a，对应 O2「只包一层」）。
//
// 边界同 singleinstance.go / tray.go：只把 main 里那句直接调 mygo 的原语换成
// 本层的转发，参数 / 顺序 / 返回值处置逐字不变；**回调 fn 本体留 main**。
// 构建标签不加（mygo 的 GlobalShortcut 面每个平台都有后端）。
package platform

import (
	"strings"

	"github.com/egoist/mygo"
	goui "github.com/egoist/mygo/ui"
)

// RegisterHotkey 注册系统级热键，按下时调 fn；失败（被别的程序占用、串不合法等）
// 原样带回 error，不 panic、不重试、不落日志——**怎么处理是调用方的事**
// （panel.go 原来就是「记一条中文日志、不阻断启动」，搬完照旧）。
//
// 转发 `mygo.GlobalShortcut.Register`（modules.go:540
// `func (g *GlobalShortcutModule) Register(acc string, fn func()) error`）：
//   - accel 是 Electron 风格加速器串（如 "Ctrl+Alt+Space"，大小写不敏感），
//     本层**不做任何归一 / 校验**——mygo 内部自己 Parse（modules.go:542-545）
//     并用解析后的串做去重 key，包一层再归一就成了行为变更；
//   - fn 是无参回调，按下时**在主线程被直接调用**：WM_HOTKEY →
//     `pressed()`（modules.go:627-634）**无 channel、无 Request/Reply**，
//     这正是 O2 能整删 Rust 侧 hotkey.rs（388 行 + 一个专用线程）的依据。
//
// 线程模型（§7.1.3）：**只许主线程，且必须 App 就绪之后**。两条硬约束来自 mygo：
//   - `needsApp("GlobalShortcut.Register")`（modules.go:541 → app.go:128-131）
//     在「主线程且 App 未初始化」时**直接 panic**「called before App.Run」；
//   - 真正向桌面 bind 那一步走 onMainValue（modules.go:559 → loop.go:110 →
//     :94-108），投递给主循环后**阻塞等返回值、无超时**——没有主循环就永久卡死。
//
// 调用点 panel.go 的 ready() 跑在 App.WhenReady 里，两条天然满足（搬之前如此，
// 搬之后不变）。
func RegisterHotkey(accel string, fn func()) error {
	return mygo.GlobalShortcut.Register(accel, fn)
}

// UnregisterHotkey 撤销 accel 上的全局热键绑定（改键第一步：先撤旧键）。
//
// 转发 `mygo.GlobalShortcut.Unregister`（modules.go:593）——与 Register 同去重 key
// （解析后的串），传原始串即可。**无返回值的幂等操作**：键没注册过也是空操作，
// 不 panic、不报错。线程模型同 Register（只许主线程 + App 就绪）。
//
// 调用方：settings.go 的改键路径（撤旧 → 注新，失败回滚）。改键之所以需要它，正是
// 本文件旧注释预留的那句「真要做改键时再按 modules.go:593 补，那时也得有对应调用方
// 与测试一起进来」——现在调用方（saveSettings）与真机验证一起到了。
func UnregisterHotkey(accel string) {
	mygo.GlobalShortcut.Unregister(accel)
}

// accelKeyNames 把 mygo 的按键常量映射为 accelerator.Parse 接受的名字，并统一为
// **规范显示形态**（字母/功能键大写、命名键 Title-case，如 Ctrl+Alt+Space），避免
// 录出来的串大小写混杂；Parse 本身大小写不敏感，故显示与解析两不误。
var accelKeyNames = map[goui.Key]string{
	goui.KeyA: "A", goui.KeyB: "B", goui.KeyC: "C", goui.KeyD: "D", goui.KeyE: "E",
	goui.KeyF: "F", goui.KeyG: "G", goui.KeyH: "H", goui.KeyI: "I", goui.KeyJ: "J",
	goui.KeyK: "K", goui.KeyL: "L", goui.KeyM: "M", goui.KeyN: "N", goui.KeyO: "O",
	goui.KeyP: "P", goui.KeyQ: "Q", goui.KeyR: "R", goui.KeyS: "S", goui.KeyT: "T",
	goui.KeyU: "U", goui.KeyV: "V", goui.KeyW: "W", goui.KeyX: "X", goui.KeyY: "Y", goui.KeyZ: "Z",
	goui.Key0: "0", goui.Key1: "1", goui.Key2: "2", goui.Key3: "3", goui.Key4: "4",
	goui.Key5: "5", goui.Key6: "6", goui.Key7: "7", goui.Key8: "8", goui.Key9: "9",
	goui.KeyF1: "F1", goui.KeyF2: "F2", goui.KeyF3: "F3", goui.KeyF4: "F4",
	goui.KeyF5: "F5", goui.KeyF6: "F6", goui.KeyF7: "F7", goui.KeyF8: "F8",
	goui.KeyF9: "F9", goui.KeyF10: "F10", goui.KeyF11: "F11", goui.KeyF12: "F12",
	goui.KeyEnter: "Enter", goui.KeyEscape: "Escape", goui.KeyBackspace: "Backspace",
	goui.KeyTab: "Tab", goui.KeySpace: "Space", goui.KeyDelete: "Delete", goui.KeyInsert: "Insert",
	goui.KeyHome: "Home", goui.KeyEnd: "End", goui.KeyPageUp: "PageUp", goui.KeyPageDown: "PageDown",
	goui.KeyLeft: "Left", goui.KeyRight: "Right", goui.KeyUp: "Up", goui.KeyDown: "Down",
	goui.KeyMinus: "-", goui.KeyEqual: "=", goui.KeyComma: ",", goui.KeyPeriod: ".",
	goui.KeySlash: "/", goui.KeySemicolon: ";", goui.KeyQuote: "'",
	goui.KeyBracketLeft: "[", goui.KeyBracketRight: "]", goui.KeyBackslash: "\\", goui.KeyBackquote: "`",
}

// AccelFromKey 把一次按键（Key + 修饰键）转成 GlobalShortcut 可解析的 Electron 风格
// 加速器串（如 "Ctrl+Alt+Space"）。不支持的键、或**没有任何修饰键**（会劫持普通输入）
// 返回 ok=false，由调用方提示用户重按。
func AccelFromKey(key goui.Key, mods goui.Modifiers) (string, bool) {
	name, ok := accelKeyNames[key]
	if !ok {
		return "", false
	}
	parts := make([]string, 0, 5)
	if mods&goui.Ctrl != 0 {
		parts = append(parts, "Ctrl")
	}
	if mods&goui.Alt != 0 {
		parts = append(parts, "Alt")
	}
	if mods&goui.Shift != 0 {
		parts = append(parts, "Shift")
	}
	if mods&goui.Super != 0 {
		parts = append(parts, "Super")
	}
	if len(parts) == 0 {
		return "", false
	}
	parts = append(parts, name)
	return strings.Join(parts, "+"), true
}
