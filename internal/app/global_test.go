package app

import (
	"testing"

	"voke/internal/logic/appsmodel"
	"voke/internal/logic/config"
	"voke/internal/logic/engine"
	"voke/internal/providers"
)

func newGlobalState(t *testing.T) *appState {
	t.Helper()
	cfgs, err := config.Normalize(builtinDescriptors(), nil)
	if err != nil {
		t.Fatalf("Normalize 默认配置失败：%v", err)
	}
	return &appState{pluginReg: builtinProviders(), providers: cfgs}
}

func TestFoldAppMatch(t *testing.T) {
	m := foldAppMatch(appsmodel.AppEntry{Name: "Chrome", Path: `shell:AppsFolder\Chrome`})
	if m.ProviderID != "apps" || m.Title != "Chrome" || m.Subtitle != `shell:AppsFolder\Chrome` ||
		m.Icon != `shell:AppsFolder\Chrome` || m.Action.Kind != providers.ActionLaunch ||
		m.Action.Path != `shell:AppsFolder\Chrome` || m.Score != 1.0 {
		t.Errorf("apps 折入不符：%+v", m)
	}
}

// globalAggFor 走生产纯路径组装一次全局聚合：主线程算 globals → 查询线程
// processQuery 现查现聚合。替代旧 collectGlobalBatches / globalAggregate 的测法。
func globalAggFor(st *appState, query string) providers.Aggregated {
	table := st.providers.BuildTable()
	state := engine.Scan(table, engine.State{}, query)
	job := queryJob{state: state, globals: st.globalProviderIDs()}
	return processQuery(st.pluginReg, job).agg
}

// TestGlobalProviderIDs：只 enabled && is_global 且已落地注册的插件进 globals；
// base64（is_global 假）/ picker（不在 reg）不进；已退出全局的 awake/terminal/
// vscode/zed 也不进。对照旧 collectGlobalBatches 的筛选口径。
func TestGlobalProviderIDs(t *testing.T) {
	st := newGlobalState(t)
	ids := st.globalProviderIDs()
	if containsID(ids, "base64") || containsID(ids, "picker") {
		t.Errorf("base64/picker 不该进 globals：%v", ids)
	}
	if containsID(ids, "awake") || containsID(ids, "terminal") || containsID(ids, "vscode") || containsID(ids, "zed") {
		t.Errorf("awake/terminal/vscode/zed 已退出全局，不该进 globals：%v", ids)
	}
	if !containsID(ids, "calculator") || !containsID(ids, "windows") {
		t.Errorf("calculator/windows（is_global）应在 globals：%v", ids)
	}
}

// TestGlobalAggregateGrouping：1+1 命中 calculator，聚合产出对应分组且组内已排序。
func TestGlobalAggregateGrouping(t *testing.T) {
	st := newGlobalState(t)
	agg := globalAggFor(st, "1+1")
	if !containsID(groupIDs(agg.Groups), "calculator") {
		t.Fatalf("1+1 应有 calculator 组：%v", groupIDs(agg.Groups))
	}
	for _, g := range agg.Groups {
		if g.ProviderID != "calculator" {
			continue
		}
		for i := 1; i < len(g.Matches); i++ {
			if g.Matches[i-1].FinalScore < g.Matches[i].FinalScore {
				t.Errorf("组内应降序：%+v", g.Matches)
			}
		}
	}
}

// TestBuildGlobalRows：apps 行在最前一组（带 AppEntry 下标），agg 里的 apps 组被跳过（避免走
// provider 执行器丢 LaunchApp 语义），非 apps 组摊平成 provider 行（带 provRows 下标 + 组首标记）。
func TestBuildGlobalRows(t *testing.T) {
	st := newGlobalState(t)
	appsEntries := []appsmodel.AppEntry{{Name: "Chrome", Path: "p1"}, {Name: "Edge", Path: "p2"}}
	agg := globalAggFor(st, "1+1") // 至少含 calculator 组

	rows, provRows := buildGlobalRows(appsEntries, st.appsWeight(), agg)
	if len(rows) < 3 {
		t.Fatalf("至少 2 apps + 1 provider 行：%d", len(rows))
	}
	// 前两行是 apps，指回 AppEntry 下标，首行带组标记。
	if !rows[0].apps || rows[0].appIdx != 0 || !rows[0].newGroup || rows[0].groupID != "apps" {
		t.Errorf("行0 应 apps/0/newGroup：%+v", rows[0])
	}
	if !rows[1].apps || rows[1].appIdx != 1 || rows[1].newGroup {
		t.Errorf("行1 应 apps/1/非组首：%+v", rows[1])
	}
	// 首个 provider 行：非 apps、组首、provIdx 0。
	first := rows[2]
	if first.apps || !first.newGroup || first.provIdx != 0 || first.groupID == "apps" {
		t.Errorf("行2 应是首个 provider 行（非 apps、newGroup、provIdx0）：%+v", first)
	}
	// provRows 只含非 apps 组行；apps 的两条不进 provRows。
	for _, m := range provRows {
		if m.ProviderID == "apps" {
			t.Errorf("apps 不该进 provRows（走 apps 执行器）：%+v", m)
		}
	}
}

// TestBuildGlobalRowsOrderByWeight：apps 组与其它组同权比较——权重更高的插件组排在
// apps 前面（默认 apps 1.2 仍第一，同权重 apps 优先），权重低的组排在 apps 之后。
func TestBuildGlobalRowsOrderByWeight(t *testing.T) {
	st := newGlobalState(t)
	apps := []appsmodel.AppEntry{{Name: "Chrome", Path: "p1"}}

	mk := func(id string, weight float64) providers.Group {
		return providers.Group{ProviderID: id, Weight: weight, Matches: []providers.RankedMatch{
			{Match: providers.Match{ProviderID: id, Title: id + "-row"}},
		}}
	}
	// Aggregate 已按权重降序给出组：vscode(3.0) > calculator(1.0)。
	agg := providers.Aggregated{Groups: []providers.Group{mk("vscode", 3.0), mk("calculator", 1.0)}}

	rows, _ := buildGlobalRows(apps, st.appsWeight(), agg)
	if len(rows) != 3 {
		t.Fatalf("应 1 apps + 2 provider 行：%+v", rows)
	}
	if rows[0].apps || rows[0].groupID != "vscode" || !rows[0].newGroup {
		t.Errorf("行0 应是 vscode 组首（权重 3.0 > apps 1.2）：%+v", rows[0])
	}
	if !rows[1].apps || rows[1].appIdx != 0 || !rows[1].newGroup {
		t.Errorf("行1 应是 apps 组首：%+v", rows[1])
	}
	if rows[2].apps || rows[2].groupID != "calculator" || !rows[2].newGroup {
		t.Errorf("行2 应是 calculator 组首（权重 1.0 < apps 1.2）：%+v", rows[2])
	}
}

func groupIDs(gs []providers.Group) []string {
	out := make([]string, len(gs))
	for i, g := range gs {
		out[i] = g.ProviderID
	}
	return out
}

func containsID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// TestGlobalView：globalView 把合并行映射成渲染输入——apps 行取 AppEntry（组名「应用」），
// provider 行取 Match（组名取插件显示名）。用注入的 agg（含一个非 apps 组）跑 Linux 上不会
// 自然产生的合并场景，专测映射本身。
func TestGlobalView(t *testing.T) {
	st := newGlobalState(t)
	apps := []appsmodel.AppEntry{{Name: "Chrome", Path: "p1"}, {Name: "Edge", Path: "p2"}}
	st.model.Results = apps
	agg := providers.Aggregated{Groups: []providers.Group{
		{ProviderID: appsProviderID, Matches: []providers.RankedMatch{{Match: foldAppMatch(apps[0])}}},
		{ProviderID: "awake", Matches: []providers.RankedMatch{{Match: providers.Match{
			ProviderID: "awake", Title: "仅系统唤醒", Subtitle: "进入后选时长档位",
		}}}},
	}}
	rows, provRows := buildGlobalRows(apps, st.appsWeight(), agg)
	st.globalRows, st.globalProvRows = rows, provRows

	view := st.globalView()
	if len(view) != 3 { // 2 apps + 1 awake（apps 组由 apps 呈现、agg 里的 apps 组被跳过）
		t.Fatalf("应 3 行：%+v", view)
	}
	if view[0].Title != "Chrome" || view[0].GroupLabel != "应用" || !view[0].NewGroup || view[0].Icon != "p1" {
		t.Errorf("首行 apps 映射错：%+v", view[0])
	}
	if view[2].Title != "仅系统唤醒" || view[2].GroupLabel != "保持唤醒" || !view[2].NewGroup {
		t.Errorf("awake 行映射错（组名应取显示名）：%+v", view[2])
	}
}
