// panelstate 的**纯决策**测试（§7.2 步骤5 从 package main 的
// panel_behavior_test.go 搬来逻辑断言，并按 §10.1.1 直测生产方法）。
//
// 三条纪律（对齐 §10.1.1）：
//  1. 一律调生产的 Model.* 方法，绝不在测试里复刻一份过滤/钳位/文案逻辑；
//  2. 期望值写**绝对值**（文案逐字、Intent 序列逐条），不拿被测常量当阈值做同义反复；
//  3. 时间基准注入（now 传死值），本包内部不调 time.Now()，边界可确定断言。
//
// 「意图是否被正确执行到 UI/平台」由 main 侧用 ui.NewTester 的用例负责
// （row_visual_test.go / panel_behavior_test.go）；这里只验决策与返回的意图。
package panelstate

import (
	"testing"
	"time"

	"github.com/zyition/voke/poc/voke-go/internal/logic/appsmodel"
	"github.com/zyition/voke/poc/voke-go/internal/logic/frequency"
	"github.com/zyition/voke/poc/voke-go/internal/logic/testutil"
)

// hasAction 判断意图序列里是否含某 Kind（顺序无关的辅助，仅用于个别断言）。
func hasAction(acts []Action, kind ActionKind) bool {
	for _, a := range acts {
		if a.Kind == kind {
			return true
		}
	}
	return false
}

// TestEmptyHintStates 覆盖唤起懒加载后 EmptyHint 的四个分支（逐字搬自原
// main 侧 TestEmptyHintStates，期望文案绝对值不变）：
// 唤起（未加载）显示引导、加载中显示枚举、加载超时兜底、加载完无命中、枚举失败。
func TestEmptyHintStates(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cases := []struct {
		name string
		m    Model
		want string
	}{
		{"唤起未加载", Model{}, "输入以搜索应用"},
		{"加载中", Model{Loading: true, LoadAt: now}, "正在枚举应用…"},
		{"加载超时兜底", Model{Loading: true, LoadAt: now.Add(-11 * time.Second)}, "输入以搜索应用"},
		{"加载完成无命中", Model{Loaded: true, Apps: []appsmodel.AppEntry{{Name: "A"}}}, "没有匹配的应用"},
		{"枚举失败", Model{Loaded: true}, "暂无应用（枚举失败见日志）"},
	}
	for _, tc := range cases {
		if got := tc.m.EmptyHint(now); got != tc.want {
			t.Errorf("%s：EmptyHint()=%q，期望 %q", tc.name, got, tc.want)
		}
	}
}

// TestEmptyHintTimeoutBoundary 锁 10s 兜底的**两侧**边界（对拍原实现
// `time.Since(loadAt) < 10*time.Second` 的严格小于语义）：9.999s 仍算加载中、
// 恰好 10s 起算超时回落到「从没加载」的引导文案。
func TestEmptyHintTimeoutBoundary(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	justIn := Model{Loading: true, LoadAt: now.Add(-9999 * time.Millisecond)}
	if got := justIn.EmptyHint(now); got != "正在枚举应用…" {
		t.Errorf("9.999s：EmptyHint=%q，期望仍在加载中", got)
	}
	atTimeout := Model{Loading: true, LoadAt: now.Add(-10 * time.Second)}
	if got := atTimeout.EmptyHint(now); got != "输入以搜索应用" {
		t.Errorf("恰好 10s（严格小于应判超时）：EmptyHint=%q，期望回落引导文案", got)
	}
}

// TestFilterResetsChosenAndEmitsScroll 验 Filter 的两条不变量：
//   - 结果非空 → 复位选中到 0 并产出 ScrollTo(0) 意图；
//   - 结果为空 → 复位选中到 0 但**不**产出任何意图（对齐原 choose 的空表早退）。
func TestFilterResetsChosenAndEmitsScroll(t *testing.T) {
	m := Model{Apps: testutil.MkApps("AA", "BB", "CC"), Chosen: 2}
	acts := m.Filter() // Query 为空 → 全量返回
	if m.Chosen != 0 {
		t.Errorf("Filter 后选中应复位到 0，实际 %d", m.Chosen)
	}
	if len(m.Results) != 3 {
		t.Fatalf("空查询应返回全量 3 条，实际 %d 条", len(m.Results))
	}
	want := []Action{{Kind: ActionScrollTo, Index: 0}}
	if len(acts) != 1 || acts[0] != want[0] {
		t.Errorf("Filter 意图=%v，期望 [ScrollTo 0]", acts)
	}

	// 空应用列表：Filter 不产出滚动意图，选中仍复位 0。
	empty := Model{Chosen: 5}
	if acts := empty.Filter(); len(acts) != 0 {
		t.Errorf("空结果 Filter 应无意图，实际 %v", acts)
	}
	if empty.Chosen != 0 {
		t.Errorf("空结果 Filter 后选中应复位 0，实际 %d", empty.Chosen)
	}
}

// TestFilterUsesModelFreq 钉住「Model.Freq 确实被 Filter 传给 search.FilterApps」这条
// 接线：注入一张让「CC」高频的频率表，空查询下 Results 首位应变 CC（不注入则保枚举序）。
// 若哪天 Filter 不再传 m.Freq（退回 FilterApps(query,apps) 两参），本用例立刻红——这是
// apps-only 全局「越用越靠前」唯一的接线点。
func TestFilterUsesModelFreq(t *testing.T) {
	m := Model{
		Apps: testutil.MkApps("AA", "BB", "CC"),
		Freq: frequency.Store{frequency.Key(appsmodel.ProviderID, "CC"): 30},
	}
	m.Filter()
	if len(m.Results) != 3 || m.Results[0].Name != "CC" {
		t.Fatalf("注入 Freq 后空查询应把高频 CC 顶到首位，实得 %d 条 / 首位 %q", len(m.Results), m.Results[0].Name)
	}
}

// TestChooseClampsAndEmitsScrollIntent 验 Choose 的钳位与意图（这是变异测试
// 的靶子：把钳位改成不夹取，本用例立刻红）。
func TestChooseClampsAndEmitsScrollIntent(t *testing.T) {
	m := Model{Results: testutil.MkApps("AA", "BB", "CC")}
	if acts := m.Choose(10); len(acts) != 1 || acts[0].Kind != ActionScrollTo || m.Chosen != 2 {
		t.Errorf("超上界 choose(10)：Chosen=%d acts=%v，期望钳到 2 且 ScrollTo 2", m.Chosen, acts)
	}
	if acts := m.Choose(-3); len(acts) != 1 || acts[0].Kind != ActionScrollTo || m.Chosen != 0 {
		t.Errorf("超下界 choose(-3)：Chosen=%d acts=%v，期望钳到 0 且 ScrollTo 0", m.Chosen, acts)
	}
	if acts := m.Choose(2); m.Chosen != 2 || acts[0].Index != 2 {
		t.Errorf("界内 choose(2)：Chosen=%d index=%d，期望各 2", m.Chosen, acts[0].Index)
	}
	// 空结果：不滚动、复位 0。
	empty := Model{}
	if acts := empty.Choose(4); len(acts) != 0 || empty.Chosen != 0 {
		t.Errorf("空结果 choose(4)：Chosen=%d acts=%v，期望 0 且无意图", empty.Chosen, acts)
	}
}

// TestOnQueryChangedReturnsEnsureLoadOnce 验懒加载触发点**决策**：
// 非空查询且未 loaded/loading → 首个产出 ActionEnsureLoad 并置 Loading/LoadAt；
// 之后因 Loading 已置位不再重复产出；清空查询不产出。
// （意图到 appsmodel.EnsureLoaded 的落地接线由 main 的
// TestOnQueryChangedWiresEnsureLoad 覆盖，二者测不同层、不重复。）
func TestOnQueryChangedReturnsEnsureLoadOnce(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	m := Model{Query: "w"}
	first := m.OnQueryChanged(now)
	if !hasAction(first, ActionEnsureLoad) {
		t.Errorf("首个非空查询应产出 EnsureLoad，实际 %v", first)
	}
	if !m.Loading {
		t.Error("产出 EnsureLoad 时应同时置 Loading=true")
	}
	if !m.LoadAt.Equal(now) {
		t.Errorf("LoadAt 应记为注入的 now=%v，实际 %v", now, m.LoadAt)
	}
	// 连续按键不再重复产出 EnsureLoad（Loading 闸门）。
	for _, q := range []string{"wx", "wxl"} {
		m.Query = q
		if hasAction(m.OnQueryChanged(now.Add(time.Second)), ActionEnsureLoad) {
			t.Errorf("查询 %q 时 Loading 已置位，不应再产出 EnsureLoad", q)
		}
	}
	// 纯空白查询不触发。
	m2 := Model{Query: "   "}
	if hasAction(m2.OnQueryChanged(now), ActionEnsureLoad) {
		t.Error("全空白查询不应触发 EnsureLoad")
	}
}

// TestClickRowLaunchesOnly 是 E-1 时序的关键靶：
//   - ClickRow 合法 → 只产出 [Launch i]（**绝不**自带 Hide），并把 Chosen 设成 i；
//   - 越界（i<0 或 i>=len）→ 无任何意图、不碰 Chosen。
//
// 启动成功后的「先隐藏、再复位」归 app 层执行器（对照 Rust dispatcher +
// reset_after_action）；Model 只表达启动意图，失败时面板状态一点不动。
//
// 变异测试「让 ClickRow 也返回 Hide（即启动失败仍隐藏）」会被本例第一断言抓红。
func TestClickRowLaunchesOnly(t *testing.T) {
	m := Model{Apps: testutil.MkApps("AA", "BB", "CC")}
	m.Query = ""
	m.Filter() // 填 Results（全量 3 条）

	acts := m.ClickRow(1)
	if m.Chosen != 1 {
		t.Errorf("ClickRow(1) 应把选中设成 1，实际 %d", m.Chosen)
	}
	if len(acts) != 1 || acts[0] != (Action{Kind: ActionLaunch, Index: 1}) {
		t.Fatalf("ClickRow 应只产出 [Launch 1]，不得自带 Hide，实际 %v", acts)
	}
	// 越界：不产出、不改选中。
	m.Chosen = 0
	if got := m.ClickRow(-1); got != nil {
		t.Errorf("ClickRow(-1) 应返回 nil，实际 %v", got)
	}
	if got := m.ClickRow(3); got != nil {
		t.Errorf("ClickRow(3)（越上界）应返回 nil，实际 %v", got)
	}
	if m.Chosen != 0 {
		t.Errorf("越界 ClickRow 不应改动 Chosen，实际变 %d", m.Chosen)
	}
}

// TestOpenBoundsAndDelegates 验 Open：选中越界（>= 结果数）返回 nil；
// 否则等价于 ClickRow(Chosen)，只产出 [Launch Chosen]、不自带 Hide。
func TestOpenBoundsAndDelegates(t *testing.T) {
	m := Model{Apps: testutil.MkApps("AA", "BB")}
	m.Filter()
	m.Chosen = 1
	acts := m.Open()
	if len(acts) != 1 || acts[0] != (Action{Kind: ActionLaunch, Index: 1}) {
		t.Errorf("Open（选中=1）应产出 [Launch 1]，实际 %v", acts)
	}
	// 空结果：Chosen=0 >= 0 → 越界，返回 nil（对齐原 open 早退）。
	empty := Model{}
	if got := empty.Open(); got != nil {
		t.Errorf("空结果 Open 应返回 nil，实际 %v", got)
	}
}
