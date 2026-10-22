// Package panelstate 是面板的「纯决策状态机」（§7.2 步骤5）。
//
// 它从 package main 的 appState 里拆出**只有状态、只有判断、没有副作用**的
// 那一层：查询 / 全量应用 / 过滤结果 / 选中行 / 懒加载标志。所有会碰到 UI 或
// 平台的事（滚进视野 ScrollIntoView、启动 launchApp、隐藏面板 hidePanel、
// 触发后台枚举 appsmodel.EnsureLoaded）**一律外化成返回值**——本包只说「该做
// 什么」（返回 []Action），绝不自己做。执行交回 main 侧的 appState，它持有
// mygo 句柄并据此调用（见 panel.go / view.go 的 execute/apply）。
//
// 分层铁律（§7.2.1）：本包**零 mygo、零 x/sys/windows**，只依赖标准库与同为
// logic 层的 internal/logic/search（FilterApps）、internal/logic/appsmodel
// （AppEntry 类型）。故 `go test ./internal/logic/...` 在 Linux 原生可跑。
//
// 行为等价（E-1）要求时机与顺序逐字不变，尤其：
//   - 懒加载只在「查询非空（TrimSpace 后）且未 loaded 且未 loading」时触发；
//   - ClickRow / Open 只产出启动意图，不自己清空或隐藏；启动**成功**后的
//     「先隐藏、再复位」由 app 层执行器负责（对照 Rust dispatcher +
//     reset_after_action），失败保留面板；
//   - choose/filter 复位选中的连锁（filter → choose(0)）保持不变。
//
// 时间基准可注入：OnQueryChanged / EmptyHint 都收一个 now 参数，本包内部
// **绝不**调 time.Now()，以便单测对「10s 加载超时兜底」等边界做确定断言。
package panelstate

import (
	"strings"
	"time"

	"voke/internal/logic/appsmodel"
	"voke/internal/logic/frequency"
	"voke/internal/logic/search"
)

// ActionKind 标识一个「意图」的类型——main 侧据此执行对应的 UI/平台副作用。
type ActionKind int

const (
	// ActionEnsureLoad：触发一次懒加载枚举（main 里调 appsmodel.EnsureLoaded，
	// 内部 singleflight 闸门保证连续按键只真枚举一次）。对应原
	// onQueryChanged 里的 appsmodel.EnsureLoaded()。
	ActionEnsureLoad ActionKind = iota
	// ActionScrollTo：把 Index 行滚进视野（main 里 st.list.ScrollIntoView(Index)）。
	// 对应原 choose 末尾的 st.list.ScrollIntoView(st.chosen)。
	ActionScrollTo
	// ActionLaunch：启动 Results[Index]（main 里 launchApp）。对应原 clickRow
	// 里的 launchApp(app)。启动成功后的隐藏/复位由 main 侧在副作用成功后提交。
	ActionLaunch
)

// Action 是一条待执行的意图。Index 携带 ScrollTo / Launch 需要的行下标。
type Action struct {
	Kind  ActionKind
	Index int
}

// Model 是面板的纯状态。字段与 appState 原来的纯状态一一对应，
// 只在主线程读写（与迁移前 appState 的线程约定一致）。
type Model struct {
	Query   string               // 搜索框内容
	Apps    []appsmodel.AppEntry // 应用全量列表（枚举回调注入）
	Results []appsmodel.AppEntry // Filter 过滤后的列表数据源
	Chosen  int                  // 列表选中行
	Loading bool                 // 应用枚举进行中（空态文案用）
	Loaded  bool                 // 枚举已触发过（成功或失败都算），区分「从没加载」与「加载过无命中」
	LoadAt  time.Time            // 枚举起点（Loading 超时兜底，见 EmptyHint）
	// Freq 是 apps 使用频率表（排序加成来源）；由 app 层在装配时注入 [appState.freq]
	// 同一张表（map 值语义，Record 就地自增，故注入一次即长期有效）。nil → boost 恒
	// 1.0（FilterApps 退化成只按原始分，与注入本字段前逐字一致）。
	Freq frequency.Store
}

// Filter 用 search.FilterApps 把 Apps 过滤进 Results，并复位选中到第 0 行。
// 对应原 appState.filter()。返回的意图来自 Choose(0)（可能含 ScrollTo(0)）。
// 传入 m.Freq 让 apps-only 全局查询也吃到「越用越靠前」的 rank（见 FilterApps）。
func (m *Model) Filter() []Action {
	m.Results = search.FilterApps(m.Query, m.Apps, m.Freq)
	return m.Choose(0)
}

// Choose 把选中钳位在 [0, len-1] 并返回「滚到该行」的意图。对应原
// appState.choose()，但**不自己调 ScrollIntoView**——滚动是 main 侧副作用。
// 结果为空时只复位选中（返回 nil），与原实现「空列表不滚动」一致。
func (m *Model) Choose(i int) []Action {
	if len(m.Results) == 0 {
		m.Chosen = 0
		return nil
	}
	m.Chosen = max(0, min(i, len(m.Results)-1))
	return []Action{{Kind: ActionScrollTo, Index: m.Chosen}}
}

// OnQueryChanged 是搜索框内容变化后的统一入口（懒加载触发点）。对应原
// appState.onQueryChanged()：
//
//  1. 查询非空且未加载且未在加载中 → 置 Loading/LoadAt，返回 ActionEnsureLoad
//     （由 main 去调 appsmodel.EnsureLoaded）；
//  2. 无论是否触发加载，都立刻 Filter()（此时 Apps 可能为空，先按现有数据过滤，
//     枚举回来的那一帧再走一次 Filter 即可出结果）。
//
// now 由调用方注入（main 传 time.Now()），本包不碰系统时钟。
func (m *Model) OnQueryChanged(now time.Time) []Action {
	var acts []Action
	if strings.TrimSpace(m.Query) != "" && !m.Loaded && !m.Loading {
		m.Loading, m.LoadAt = true, now
		acts = append(acts, Action{Kind: ActionEnsureLoad})
	}
	return append(acts, m.Filter()...)
}

// ClickRow 处理「单击某行 i」：越界直接返回 nil（什么都不做，与原实现一致）；
// 否则设 Chosen=i 并返回启动意图 [ActionLaunch(i)]。**不自己启动、不清空、不隐藏**
// ——启动成功后的隐藏与复位由 main 侧在副作用成功后按 Rust 动作分类提交。
func (m *Model) ClickRow(i int) []Action {
	if i < 0 || i >= len(m.Results) {
		return nil
	}
	m.Chosen = i
	return []Action{{Kind: ActionLaunch, Index: i}}
}

// Open 启动选中项。对应原 appState.open()：选中越界（>= 结果数）时返回 nil，
// 否则等价于 ClickRow(Chosen)。
func (m *Model) Open() []Action {
	if m.Chosen >= len(m.Results) {
		return nil
	}
	return m.ClickRow(m.Chosen)
}

// EmptyHint 是结果为空时的占位文案（四态，逐字搬自原 appState.emptyHint）：
// 加载中（带 10s 超时兜底）/ 从没加载 / 加载失败（枚举 0 条）/ 加载完无命中。
// now 由调用方注入，便于对「超时边界」做确定断言。
func (m *Model) EmptyHint(now time.Time) string {
	switch {
	case m.Loading && now.Sub(m.LoadAt) < 10*time.Second:
		return "正在枚举应用…"
	case !m.Loaded:
		return "输入以搜索应用"
	case len(m.Apps) == 0:
		return "暂无应用（枚举失败见日志）"
	default:
		return "没有匹配的应用"
	}
}
