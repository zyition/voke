// 本文件定义 §7.3.3「前缀全等扫描 + 显式模式状态机」的类型。
// 命名遵任务指令：模式常量 Global / Plugin / PluginPicker / SubMode，激活常量 Direct / Indirect。

package engine

import "reflect"

// Mode 是搜索框的显式模式，**不再从查询串派生**（这是 D0/D1/D2 的根因）。
//
// 零值 Global 即「全局搜索」，因此 State{} 是一个合法可用状态，接线侧不需要构造初始化。
type Mode uint8

const (
	// Global：裸查询词走全局搜索，无模式令牌。
	Global Mode = iota
	// Plugin：已激活某个插件的第一级（前缀命中或 picker 选定）。
	Plugin
	// PluginPicker：正在展示「插件列表」的候选表。
	PluginPicker
	// SubMode：已在某个插件的子模式里（如 base64 的 enc / dec）。
	SubMode
)

// String 仅用于日志与测试失败输出，不参与判定。
func (m Mode) String() string {
	switch m {
	case Global:
		return "Global"
	case Plugin:
		return "Plugin"
	case PluginPicker:
		return "PluginPicker"
	case SubMode:
		return "SubMode"
	default:
		return "Invalid"
	}
}

// Activated 报告模式是否「已在某个插件作用域内」：此时输入只当查询词，
// 不再做任何前缀判定（§7.3.3 规则 4，结构上消灭 D1）。
func (m Mode) Activated() bool {
	switch m {
	case Plugin, PluginPicker, SubMode:
		return true
	default:
		return false
	}
}

// Activation 是前缀条目的激活方式（§7.3.4 的 activation 字段，归一化后只剩两值）。
type Activation uint8

const (
	// Direct：输入框内容全等于前缀即激活（例 `=`、`{`）。
	Direct Activation = iota
	// Indirect：输入框内容全等于「前缀 + 一个空格」才激活（例 `ba `）。
	// 裸前缀**不**激活，与 Direct 严格隔开。
	Indirect
)

// String 仅用于日志与测试失败输出。
func (a Activation) String() string {
	if a == Indirect {
		return "Indirect"
	}
	return "Direct"
}

// PickerPluginID 是「插件列表」在前缀表里的专属条目 id（决策②）。
// 它与其它条目**走同一条扫描路径**，只是命中后的 Mode 不同；
// 代码里不存在第二处 strip_prefix（铁律②）。
const PickerPluginID = "__picker"

// PrefixEntry 是前缀表的一条条目，由配置层归一化产出（本包不做 C1–C7 归一化）。
type PrefixEntry struct {
	// Prefix 是触发前缀，例 = 或 { 或 #（插件列表条目）。空串表示「该插件无前缀」，扫描时跳过。
	Prefix string
	// PluginID 是命中后进入的插件 id；"__picker"（PickerPluginID）表示插件列表条目。
	PluginID string
	// Activation 决定全等比较的目标串（Direct：== Prefix；Indirect：== Prefix + " "）。
	Activation Activation
}

// Table 是前缀表——唯一真相（§7.3.3 顶部图），顺序即优先级，首个全等命中生效。
type Table []PrefixEntry

// lookup 按 PluginID 找条目（供模式令牌渲染用，不做前缀判定）。
func (t Table) lookup(pluginID string) (PrefixEntry, bool) {
	for _, e := range t {
		if e.PluginID == pluginID {
			return e, true
		}
	}
	return PrefixEntry{}, false
}

// Scope 是作用域栈里的一层。
//
// Query 只对**栈顶**有意义：进入子作用域（push）时，离开层的 Query 随之丢弃，
// 因此栈里除栈顶外的帧 Query 恒为空。退格出栈回到的上一层总是空输入——
// 过滤词是临时输入，不跨作用域恢复。
type Scope struct {
	Kind     Mode
	PluginID string // Plugin / SubMode：所属插件；Global / PluginPicker 为空
	SubMode  string // 仅 SubMode
	Query    string // 该层自己的过滤词
}

// State 是搜索框的**作用域栈**（严格 LIFO）。零值等价于「只有 Global、空查询」，
// 故接线侧仍无需构造初始化。
//
// 出栈回到哪一层由**栈本身**决定，因此天然区分进入路径：
//   - 前缀直达插件：栈 [Global, Plugin] → 退格出栈到 Global；
//   - 经插件列表选中：栈 [Global, PluginPicker, Plugin] → 退格出栈回 PluginPicker；
//   - 子命令：栈 [..., Plugin, SubMode] → 退格回该 Plugin。
//
// 不变量：只有栈顶帧可能带 Query；出栈回到的上一层输入恒为空（父层过滤词在
// 入栈时已丢弃）。
type State struct {
	scopes []Scope
}

// Top 返回栈顶作用域；空栈视为 Global（零值语义）。
func (s State) Top() Scope {
	if len(s.scopes) == 0 {
		return Scope{Kind: Global}
	}
	return s.scopes[len(s.scopes)-1]
}

// Mode 返回栈顶作用域的类型。
func (s State) Mode() Mode { return s.Top().Kind }

// PluginID 返回栈顶所属插件 id（Global / PluginPicker 为空）。
func (s State) PluginID() string { return s.Top().PluginID }

// SubMode 返回栈顶子命令（仅 SubMode 非空）。
func (s State) SubMode() string { return s.Top().SubMode }

// Query 返回栈顶作用域的过滤词。
func (s State) Query() string { return s.Top().Query }

// Scopes 返回栈的只读拷贝（栈底→栈顶），供面包屑渲染；调用方不得借它改写状态。
func (s State) Scopes() []Scope { return append([]Scope(nil), s.scopes...) }

// Equal 语义相等：栈内容逐层一致（含每层过滤词）。供其它包的测试做期望比对
// （State 含切片不可用 ==）。
func (s State) Equal(o State) bool { return reflect.DeepEqual(s.scopes, o.scopes) }

// withQuery 把输入写进**栈顶作用域**的 Query；空栈时先落一个 Global 帧。
func (s State) withQuery(q string) State {
	if len(s.scopes) == 0 {
		return State{scopes: []Scope{{Kind: Global, Query: q}}}
	}
	out := append([]Scope(nil), s.scopes...)
	out[len(out)-1].Query = q
	return State{scopes: out}
}

// push 压入一层新作用域（新层 Query 恒空——前缀/选择都不进输入框）。
// 压栈同时**清空离开层（当前栈顶）的 Query**：进入子作用域即放弃该层的临时
// 过滤词，由此保证「只有栈顶可能带 Query」。
// 空栈时先补一个 Global 底帧，保证出栈有层可回。
func (s State) push(sc Scope) State {
	out := append([]Scope(nil), s.scopes...)
	if len(out) == 0 {
		out = append(out, Scope{Kind: Global})
	}
	out[len(out)-1].Query = ""
	out = append(out, sc)
	return State{scopes: out}
}

// NewState 按「标准进入路径」构造一个初始栈状态（Global / PluginPicker / Plugin /
// SubMode），供装配与测试直接给初值。生产热路径仍只用 Scan / SelectPlugin /
// EnterSubMode——那三个才带真实的进入路径语义。
func NewState(kind Mode, pluginID, subMode, query string) State {
	var s State
	switch kind {
	case Global:
		return s.withQuery(query)
	case PluginPicker:
		return s.withQuery("").push(Scope{Kind: PluginPicker, Query: query})
	case Plugin:
		return s.withQuery("").push(Scope{Kind: Plugin, PluginID: pluginID, Query: query})
	case SubMode:
		p := s.withQuery("").push(Scope{Kind: Plugin, PluginID: pluginID})
		return p.push(Scope{Kind: SubMode, PluginID: pluginID, SubMode: subMode, Query: query})
	default:
		return s
	}
}
