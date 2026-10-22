package app

import (
	"testing"

	"voke/internal/logic/appsmodel"
	"voke/internal/logic/config"
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

// TestCollectGlobalBatches：apps 批在最前；其后只 enabled&&is_global 且在 pluginReg 的插件——
// base64（is_global 假）不进、picker（不在 reg）不进；Linux 下 windows/terminal/workspaces
// 空结果被跳，awake（空查询出两方向）成组。
func TestCollectGlobalBatches(t *testing.T) {
	st := newGlobalState(t)
	apps := []providers.Match{
		foldAppMatch(appsmodel.AppEntry{Name: "Chrome", Path: "p1"}),
		foldAppMatch(appsmodel.AppEntry{Name: "Edge", Path: "p2"}),
	}
	batches := st.collectGlobalBatches(apps, providers.QueryCtx{Raw: "", Query: ""})
	if len(batches) < 2 {
		t.Fatalf("至少 apps + awake 两批，实得 %d：%+v", len(batches), batchIDs(batches))
	}
	if batches[0].ProviderID != "apps" || len(batches[0].Matches) != 2 {
		t.Errorf("首批应是 apps（2 条）：%+v", batches[0])
	}
	ids := batchIDs(batches)
	if containsID(ids, "base64") || containsID(ids, "picker") {
		t.Errorf("base64/picker 不该进全局批次：%v", ids)
	}
	if !containsID(ids, "awake") {
		t.Errorf("awake（is_global、空查询出方向）应在批次里：%v", ids)
	}
}

// TestGlobalAggregateGrouping：聚合保注册顺序分组，apps 组第一（2 条），组内已排序。
func TestGlobalAggregateGrouping(t *testing.T) {
	st := newGlobalState(t)
	apps := []providers.Match{
		foldAppMatch(appsmodel.AppEntry{Name: "Chrome", Path: "p1"}),
		foldAppMatch(appsmodel.AppEntry{Name: "Edge", Path: "p2"}),
	}
	agg := st.globalAggregate(apps, providers.QueryCtx{})
	if len(agg.Groups) < 2 {
		t.Fatalf("至少 apps + awake 两组：%v", groupIDs(agg.Groups))
	}
	if agg.Groups[0].ProviderID != "apps" || len(agg.Groups[0].Matches) != 2 {
		t.Errorf("首组应 apps（2 条）：%+v", agg.Groups[0])
	}
}

// TestBuildGlobalRows：apps 行在最前一组（带 AppEntry 下标），agg 里的 apps 组被跳过（避免走
// provider 执行器丢 LaunchApp 语义），非 apps 组摊平成 provider 行（带 provRows 下标 + 组首标记）。
func TestBuildGlobalRows(t *testing.T) {
	st := newGlobalState(t)
	appsEntries := []appsmodel.AppEntry{{Name: "Chrome", Path: "p1"}, {Name: "Edge", Path: "p2"}}
	appsMatches := []providers.Match{foldAppMatch(appsEntries[0]), foldAppMatch(appsEntries[1])}
	agg := st.globalAggregate(appsMatches, providers.QueryCtx{})

	rows, provRows := buildGlobalRows(appsEntries, agg)
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
	// 首个 provider 行：非 apps、组首、provIdx 0，对应 awake 组首条。
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

func batchIDs(bs []providers.Batch) []string {
	out := make([]string, len(bs))
	for i, b := range bs {
		out[i] = b.ProviderID
	}
	return out
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
	rows, provRows := buildGlobalRows(apps, agg)
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
