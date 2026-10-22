// Package config 是 §7.3.4 的配置层：三层 schema（Descriptor 写默认值 / 用户覆盖 /
// 归一后的唯一真相 ProviderConfig）+ 归一化校验 C1–C7 + 前缀表产出（BuildTable），
// 外加 §7.3.6 a 的 alias bonus 排序钩子（alias.go）。
//
// 分层：logic 层**纯逻辑**——零 mygo、零 x/sys、零第三方。真实 TOML 的读盘 / 解码属
// 接线层（platform）的活，本包只吃已经解码成 Go 值的用户覆盖（见 UserOverride 的
// 「字段是指针 = 用户没写该键」建模），因此**不新增任何依赖**。
// 允许 import 同为 logic 的 internal/logic/engine，取 Table / PrefixEntry /
// Activation / PickerPluginID 这些既有形状——本包不另立第二套模式类型。
//
// §7.3.4 三条铁律在本包的落点：
//
//	① 默认值只出现在 Descriptor：本包不写任何插件的默认前缀 / 权重 / 激活方式 /
//	   条数。「插件列表」那条也一样——它是注册表里 ID == engine.PickerPluginID
//	   的普通 Descriptor，前缀 "#" 由插件作者侧的 Descriptor 带进来（见 table_test.go
//	   的 fake 注册表），config 里没有任何 "#"/"="/"@b" 字面量。
//	② 判定只有一处：前缀**怎么触发**全在 logic/engine 的全等扫描，本包只产 Table，
//	   不写第二个 strings.HasPrefix。alias bonus 只加分、不产生触发（见 alias.go）。
//	③ 引擎只吃归一后的配置：engine.Table 的唯一来源是 Configs.BuildTable。
//
// 「绝不静默」（§7.3.3 规则 7 修的是生产 D4 的静默失效）在本包的口径：
//   - 报错类（C1 / C2 / C4）→ 返回 error，消息里把**全部**未知 id / **全部**撞车插件
//     都列出来，接线层据此退出或降级；
//   - 警告类（C3 / C7，以及重复覆盖、非有限 weight）→ 落到 Configs.Warnings 的中文
//     句子。本包不自己接 logger：纯逻辑层拿不到日志设施，把话返回给接线层落盘
//     （platform → logic/logcap）比在本包里塞一个全局 logger 干净。
package config

import (
	"sort"
	"strings"

	"voke/internal/logic/engine"
)

// Descriptor 是插件作者写的**默认值**（§7.2.3 / §7.3.4 第一层）。
//
// 只有默认值，没有任何行为：真默认表住在 internal/providers/<name>/（尚未落地），
// 由注册层传进来。ID / Name / Description / Aliases 是**元信息**，用户配置覆盖不了
// （§7.3.4 的用户层只有 enabled / prefix / activation / is_global / weight / count）。
type Descriptor struct {
	// ID 是稳定标识（"apps" / "calculator" / …）。改名等于换 id，需要迁移旧配置。
	ID string
	// Name 是面板里显示的名字。
	Name string
	// Description 是副行。
	Description string
	// Aliases 是匹配用的额外键（多别名设计保留，比 CmdPal 的一对一强）。
	// 约定同生产 Provider::aliases：**全小写 ASCII**，是完整列表、不隐含前缀语义。
	Aliases []string

	// ── 以下全是默认值，用户配置可覆盖；本包不在别处硬编码这些数 ──

	// DefaultEnabled 是不写配置时该插件是否启用。
	DefaultEnabled bool
	// DefaultIsGlobal 是不写配置时是否参与全局搜索。
	DefaultIsGlobal bool
	// DefaultPrefix 是默认前缀（"" = 无前缀）。
	DefaultPrefix string
	// DefaultActivation 是默认激活方式（engine.Direct / engine.Indirect）。
	// 注：生产用「`#` 系前缀 → Indirect」这条隐式规则回填，本包**不**照抄——
	// 按铁律①，默认值就该写在 Descriptor 里，由作者直接声明。
	DefaultActivation engine.Activation
	// DefaultWeight 是默认权重（归一时 clamp 到 0..3）。
	DefaultWeight float64
	// DefaultCount 是默认条数（random 这类插件用；归一时 clamp 到 1..50）。
	DefaultCount int
}

// UserOverride 是用户配置层的一条（§7.3.4 第二层的 Go 建模）。
//
// **每个可覆盖字段都是指针：nil = 用户没写这个键**（TOML 里 `[providers.X]` 只写了
// 一半是常态，缺的必须回落 Descriptor 默认，不能被零值劫持）。
// Activation 存**原始串**而不是枚举：未知取值是 C7 的输入，得让它有机会被看见。
type UserOverride struct {
	// ID 是被覆盖的插件 id；不在注册表 → C1 报错。
	ID string
	// Enabled 对应 enabled。
	Enabled *bool
	// IsGlobal 对应 is_global。
	IsGlobal *bool
	// Prefix 对应 prefix（"" 是合法值「无前缀」；含空白走 C3）。
	Prefix *string
	// Activation 对应 activation（"direct" / "indirect"，大小写与首尾空白宽松）。
	Activation *string
	// Weight 对应 weight。
	Weight *float64
	// Count 对应 count。
	Count *int
	// Hotkey 对应 hotkey：插件全局快捷键（Electron 风格加速器串，如 "Alt+`"）。
	// 刻意没有 Descriptor 默认值：所有插件默认不绑定，由用户在设置页手动录制。
	Hotkey *string
}

// ProviderConfig 是归一后的**唯一一份真相**（§7.3.4 第三层）。
// 字段全是最终生效值，没有 Option 语义，engine / ui / platform 都只读它。
type ProviderConfig struct {
	// ID 是插件 id（稳定标识）。
	ID string
	// Name 是显示名。
	Name string
	// Description 是副行。
	Description string
	// Aliases 是匹配用的额外键（来自 Descriptor，不可覆盖）。
	Aliases []string
	// Enabled 是启用态（false → 不进前缀表、不参与排序）。
	Enabled bool
	// IsGlobal 是是否参与全局搜索。
	IsGlobal bool
	// Prefix 是生效前缀（"" = 无前缀；C3 判非法后也置空）。
	Prefix string
	// Activation 是生效的激活方式（C7 处理后的 engine.Direct / engine.Indirect）。
	Activation engine.Activation
	// Weight 是生效权重（C5 后恒在 0..3）。
	Weight float64
	// Count 是生效条数（C6 后恒在 1..50；不用条数概念的插件忽略它）。
	Count int
	// Hotkey 是生效的插件全局快捷键（"" = 未绑定）。只有用户显式配置才有值；
	// 是否注册还要看 Enabled（见 app/hotkeys.go）。
	Hotkey string
}

// Configs 是 Normalize 的产物：注册顺序的 ProviderConfig + 要落日志的中文警告。
//
// 顺序即优先级——engine.Scan 按表序做**首个全等**命中，所以注册顺序不能丢，
// 这里也**不**按 id 排序（生产 config.rs 用 BTreeMap 按 id 排，是旧实现的副作用）。
type Configs struct {
	// Providers 按注册顺序排列。
	Providers []ProviderConfig
	// Warnings 是归一过程中产生的中文警告（C3 / C7 / 重复覆盖 / 非有限 weight），
	// 顺序与 Providers 的遍历顺序一致，便于断言与落日志。
	Warnings []string
}

// Lookup 按 id 查归一后的配置。
func (c Configs) Lookup(id string) (ProviderConfig, bool) {
	for _, p := range c.Providers {
		if p.ID == id {
			return p, true
		}
	}
	return ProviderConfig{}, false
}

// Enabled 返回启用态的插件（仍按注册顺序）。
func (c Configs) Enabled() []ProviderConfig {
	out := make([]ProviderConfig, 0, len(c.Providers))
	for _, p := range c.Providers {
		if p.Enabled {
			out = append(out, p)
		}
	}
	return out
}

// IDs 返回全部插件 id（注册顺序），供报错消息与测试断言。
func (c Configs) IDs() []string {
	out := make([]string, 0, len(c.Providers))
	for _, p := range c.Providers {
		out = append(out, p.ID)
	}
	return out
}

// joinIDs 把 id 列表拼成中文消息里的顿号串。
func joinIDs(ids []string) string { return strings.Join(ids, "、") }

// sortedCopy 返回升序副本（未知 id 列表要确定性输出，与注册顺序无关）。
func sortedCopy(ids []string) []string {
	out := append([]string(nil), ids...)
	sort.Strings(out)
	return out
}
