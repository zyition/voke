// Package providers 定义插件层的统一契约——1:1 对照生产 Rust 的
// `voke-core/src/provider.rs`（Interact / Match / QueryCtx / ProviderConfig /
// Provider trait）与 `action.rs`（Action 枚举）。§7.2.2 目录里的 `providers/` 层。
//
// 分层（§7.2.1）：纯数据 + 纯查询接口，零 mygo、零 IO，Linux 原生可测。副作用
// （执行 Action：启动/复制/设唤醒…）在 app 层 dispatcher 消费，不在本包。
//
// 与现有 apps 路径的关系：本包现承载**全部 10 个 provider**（base64/calculator/
// uuid/random/timestamp/awake/windows/terminal/workspaces + apps 折叠），经
// `builtinProviders()` 注册进 pluginReg；`view.pluginPending()` 的「未接入」分支只在
// 配置里出现无实现的 id 时兜底触发（正常安装不出现）。apps 的**展示**已折进本 Match
// 模型（global.go `foldAppMatch`/`buildGlobalRows`，供全局聚合排序），但其**启动**仍走
// panelstate 的 `execLaunch`（AppsFolder/UWP 启动语义与通用 provider `Launch{Path}` 不同，
// 强行并轨会打断 UWP 启动），故执行侧保留独立路径。
//
// Go 形态取舍（对照 Rust）：① `Provider::query` 在 Rust 为 object-safe 用
// BoxFuture；纯 provider 同步且廉价，故这里接口取同步 `Query(QueryCtx) []Match`，
// 数据型 provider 的后台加载沿用现有 appsmodel singleflight + Window.Update 接缝，
// 不进本接口。② Rust 的 `Option<T>` 用「零值 = 无」表达（Icon/SubMode 空串、
// Minutes 用 *int），减少指针噪声。③ Action 用「Kind + 扁平字段」的和类型，
// 便于值相等比较与构造，非 Rust 的各态带参枚举。
package providers

// Interact 声明「这一行是什么、Enter/点击该干什么」，UI 只按类型分发，无特判。
// 对照 Rust `Interact`。
type Interact uint8

const (
	// InteractExecute：执行 Action（复制/打开/切窗/设唤醒）。Enter 与点击都执行。
	InteractExecute Interact = iota
	// InteractEnterSubquery：进入下一级子模式；Sub 是子模式标识（"enc"/"dec"）。
	InteractEnterSubquery
	// InteractHint：说明书式提示，不渲染成可选行（不进键盘导航、不给点击处理）。
	InteractHint
)

// InteractOf 承载 EnterSubquery 的子模式标识（其余 Interact 恒为空串）。
type InteractSpec struct {
	Kind Interact
	Sub  string
}

// Action 是选中一条 Match 后要执行的副作用意图（和类型，对照 Rust `Action` 枚举）。
// 纯数据；真正执行在 app 层 dispatcher。
type Action struct {
	Kind ActionKind

	// Launch / Reveal：目标路径。
	Path string
	// Launch：命令行参数。
	Args []string
	// Launch：子进程工作目录（空 = 不设置）。
	WorkingDir string
	// Launch：以管理员权限启动。
	Elevated bool
	// Launch：来自开始菜单 .lnk 的快捷方式本体路径（非空则走 shell 路线，忽略
	// Path/Args/WorkingDir）。
	LnkPath string
	// Launch：隐藏控制台窗口（CREATE_NO_WINDOW）。
	NoConsole bool

	// LaunchTerminalProfile：WT 包 AUMID / profile 标识 / 激活失败兜底直启路径。
	Aumid        string
	Profile      string
	FallbackPath string

	// ActivateWindow：窗口句柄（Windows 上是 HWND 数值）。
	Hwnd uintptr

	// OpenUrl：URL；Copy：待写入剪贴板的文本。二者复用 Text。
	Text string

	// SetAwake：分钟数（nil = 不限时）+ 是否连屏幕一起保持。
	Minutes    *int
	KeepScreen bool

	// ActivatePlugin（# 列表选中）：目标插件 id。
	PluginID string

	// Ref 是插件内部的条目引用（当前用于剪贴板历史 item id）。
	// 纯数据，跨层只作不透明标识，不参与展示。
	Ref string
}

// ActionKind 是 Action 的判别式。
type ActionKind uint8

const (
	ActionNone ActionKind = iota
	ActionLaunch
	ActionLaunchTerminalProfile
	ActionActivateWindow
	ActionReveal
	ActionOpenUrl
	ActionCopy
	ActionSetAwake
	ActionCancelAwake
	ActionRefresh
	ActionActivatePlugin
	// 剪贴板历史动作：默认动作与行内操作共用同一套纯数据意图。
	ActionPaste
	ActionPastePlain
	ActionPin
	ActionUnpin
	ActionDelete
)

// RowAction 是结果行上可点的附加操作（纯数据）。provider 按条目类型
// 决定返回哪些操作；UI 只渲染 Label/Shortcut/Icon，不判断业务语义。
type RowAction struct {
	// ID 是稳定标识（同时作为默认图标键）：paste / paste-plain / pin / unpin / delete。
	ID string
	// Label 是 tooltip 里的中文动作名。
	Label string
	// Shortcut 是展示用快捷键串（实际按键由 app 层消费）。
	Shortcut string
	// Tooltip 是预拼好的悬浮提示（Label + 快捷键）；空串时 UI 回落 Label。
	Tooltip string
	// Icon 是 UI 图标键；空串时回落到 ID。
	Icon   string
	Action Action
}

// Match 是一条候选结果（对照 Rust `Match`）。
type Match struct {
	ProviderID string
	Title      string
	Subtitle   string
	// Icon 是本地 PNG 路径，空串 = 无图标（对照 Rust `Option<String>`）。
	Icon string
	// PreviewPNG / PreviewKey 是内存中的缩略图（剪贴板图片用）。UI 先用
	// PreviewKey 走图标缓存解码；两者都不写盘。
	PreviewPNG []byte
	PreviewKey string
	// Actions 是结果行上的附加操作；UI 只在选中/悬浮时显示。
	Actions []RowAction
	Action  Action
	// Score 是 provider 给的原始分 0.0–1.0；聚合时乘 provider 权重与频率加成。
	Score    float64
	Interact InteractSpec
}

// MatchExecute 构造一条可执行结果（最常用形态）。
func MatchExecute(providerID, title, subtitle string, action Action, score float64) Match {
	return Match{ProviderID: providerID, Title: title, Subtitle: subtitle, Action: action,
		Score: score, Interact: InteractSpec{Kind: InteractExecute}}
}

// MatchHint 构造一条提示行（不参与排序，聚合时单独抽出、保持 provider 声明顺序）。
func MatchHint(providerID, title, subtitle string) Match {
	return Match{ProviderID: providerID, Title: title, Subtitle: subtitle,
		Interact: InteractSpec{Kind: InteractHint}}
}

// MatchSubquery 构造一条「进入子模式」的条目。
func MatchSubquery(providerID, title, subtitle, subMode string, score float64) Match {
	return Match{ProviderID: providerID, Title: title, Subtitle: subtitle, Score: score,
		Interact: InteractSpec{Kind: InteractEnterSubquery, Sub: subMode}}
}

// IsHint 报告该 Match 是否为不可选提示行（对照 Rust `Interact::is_hint`）。
func (m Match) IsHint() bool { return m.Interact.Kind == InteractHint }

// IsSelectable 报告该 Match 是否可被键盘导航 / 鼠标点击选中。
func (m Match) IsSelectable() bool { return !m.IsHint() }

// QueryCtx 是传给 provider 的查询输入（对照 Rust `QueryCtx`）。
type QueryCtx struct {
	// Raw 是输入框原文（可能仍带前缀，如 "=1+1"）。
	Raw string
	// Query 是已剥掉前缀的查询词。
	Query string
	// SubMode 是当前插件子模式（空串 = 插件第一级或不在插件里；对照 Option<String>）。
	SubMode string
}

// ProviderConfig 是 provider 的运行期配置（对照 Rust `ProviderConfig`，§3 TOML 对应）。
type ProviderConfig struct {
	Enabled  bool
	IsGlobal bool
	Weight   float64
	// Prefix 是专属触发前缀（"=" / "#" / "@b"）；空串 = 无前缀。
	Prefix     string
	Activation string // "Direct" / "Indirect"；对照 Rust Activation 枚举（此处简化为字符串）。
	Count      int    // 单次查询生成条数（目前仅 random 消费），默认 5。
}

// DefaultProviderConfig 返回 Rust `ProviderConfig::default` 对应的默认值。
func DefaultProviderConfig() ProviderConfig {
	return ProviderConfig{Enabled: true, IsGlobal: true, Weight: 1.0, Activation: "Direct", Count: 5}
}

// Provider 是插件契约（对照 Rust `Provider` trait）。编译期静态注册，不做动态加载。
type Provider interface {
	// ID 是稳定机器键（配置节名、频率表键前缀），如 "base64"。
	ID() string
	// Name 是给用户看的插件名（可随时改文案，不影响持久化），如 "Base64"。
	Name() string
	// Aliases 是插件列表过滤的额外关键词（小写 ASCII），默认空。
	Aliases() []string
	// Description 是一句话描述。
	Description() string
	// Config 返回当前配置。
	Config() ProviderConfig
	// SetConfig 用新配置覆盖当前 provider 的配置。
	SetConfig(ProviderConfig)
	// Placeholder 返回当前（子）模式的输入占位符；subMode 空串 = 插件第一级。
	Placeholder(subMode string) string
	// RegeneratesOnQuery 报告同一输入**重新查询**时结果是否会变（对照 Rust
	// `regenerates_on_query`，默认 false）。true 的 provider（随机 / 时间戳 / uuid）
	// 每次 query 现生成——面板重新显示 / 热更新等「输入没变却重跑」的路径必须跳过
	// 重查，否则用户正要复制的值会被换成新的。false 的确定性 provider 重跑结果一致，
	// 无需跳过。
	RegeneratesOnQuery() bool
	// Query 按输入产出候选结果（同步；数据型 provider 的后台加载不进本接口）。
	Query(ctx QueryCtx) []Match
}
