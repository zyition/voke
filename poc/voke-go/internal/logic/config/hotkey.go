// hotkey.go：热键串 ⇔ 语义拆解的纯逻辑——1:1 对照生产 `voke-core/src/config.rs`
// 的 `HotkeySpec`（parse + Display + Default）。
//
// 分层：纯数据 + 纯解析，零平台依赖。Rust 侧它被 `AppConfig::normalize` 校验、由
// sys 层翻译成 RegisterHotKey 的修饰键 + 虚拟键码；PoC 的 mygo 路径直接用
// Electron 风格加速器串（mygo 自解析），故本单元是 config 内核的忠实移植、供配置
// 读写层与未来迁移复用，不绑定任何具体调用点。
package config

import "strings"

// defaultHotkey 是默认热键 `Alt+Space`（用户拍板，接管 PowerToys Run 的位置）。
const defaultHotkey = "alt+space"

// Hotkey 是一条热键的语义拆解：`Ctrl+Alt+Space` ⇒ {Ctrl, Alt, Key:"space"}。
type Hotkey struct {
	Ctrl  bool
	Alt   bool
	Shift bool
	Win   bool
	// Key 是主键名（小写，如 "space" / "a" / "f1"）。
	Key string
}

// ParseHotkey 解析 `"ctrl+alt+space"` 之类的热键串：`+` 分隔，最后一个非修饰键是
// key，其余是修饰键（ctrl/alt/shift/win，大小写不敏感，别名 control/super/meta/windows
// 兼容）。没有主键、出现两个主键、串为空 → ok=false。
func ParseHotkey(s string) (Hotkey, bool) {
	var h Hotkey
	var key string
	haveKey := false
	for _, raw := range strings.Split(s, "+") {
		tok := strings.TrimSpace(raw)
		if tok == "" {
			continue
		}
		switch strings.ToLower(tok) {
		case "ctrl", "control":
			h.Ctrl = true
		case "alt":
			h.Alt = true
		case "shift":
			h.Shift = true
		case "win", "super", "meta", "windows":
			h.Win = true
		default:
			if haveKey {
				return Hotkey{}, false // 第二个主键，非法
			}
			key, haveKey = strings.ToLower(tok), true
		}
	}
	if !haveKey || key == "" {
		return Hotkey{}, false
	}
	h.Key = key
	return h, true
}

// String 还原成 `"ctrl+alt+space"` 形态（修饰键固定顺序 ctrl/alt/shift/win + 主键），
// 与 ParseHotkey 往返稳定。
func (h Hotkey) String() string {
	parts := make([]string, 0, 5)
	if h.Ctrl {
		parts = append(parts, "ctrl")
	}
	if h.Alt {
		parts = append(parts, "alt")
	}
	if h.Shift {
		parts = append(parts, "shift")
	}
	if h.Win {
		parts = append(parts, "win")
	}
	parts = append(parts, h.Key)
	return strings.Join(parts, "+")
}

// DefaultHotkey 返回默认热键 Alt+Space（解析失败也回退到它，对照 Rust Default）。
func DefaultHotkey() Hotkey {
	if h, ok := ParseHotkey(defaultHotkey); ok {
		return h
	}
	return Hotkey{Alt: true, Key: "space"}
}

// CanonicalHotkey 返回用于**冲突比较**的规范串：大小写、修饰键别名、顺序都归一。
// 解析失败时退化为去首尾空白的原串（仍可做保守的相同性比较）。注册仍用原串——
// 本函数只服务「两个热键是不是同一个」这类判断，不改变用户配置。
func CanonicalHotkey(s string) string {
	raw := strings.TrimSpace(s)
	if h, ok := ParseHotkey(raw); ok {
		return h.String()
	}
	return raw
}

// HasModifier 报告热键是否带至少一个修饰键（Ctrl / Alt / Shift / Win）。
// 全局热键不允许裸主键：那会劫持所有应用里的正常输入。
func HasModifier(s string) bool {
	h, ok := ParseHotkey(s)
	if !ok {
		return false
	}
	return h.Ctrl || h.Alt || h.Shift || h.Win
}
