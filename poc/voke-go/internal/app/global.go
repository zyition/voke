// global.go：全局（无前缀）搜索的**跨 provider 聚合数据层**——对照生产
// `QueryEngine::prepare_in_mode`（未激活插件 = 所有 is_global 且 enabled 的 provider）
// `+ aggregate`（按注册顺序分组、组内按 final_score 降序）。
//
// 为什么单列本文件：PoC 此前全局搜索只出 apps（`renderResults` 的 default 分支），而生产
// 全局会把 awake / windows / terminal / workspaces 这些 is_global 插件的结果也聚进来。这是
// 一处**真实功能差异**，不是纯重构。本层先把「算出该显示哪些分组」这条纯路径做出来并 headless
// 测；渲染整合（合并成单条可导航列表 + 组间分隔 + 逐行 open/click 路由）是紧接的下一步切片，
// 因其是 mygo 交互、无法在此真机验证，故先立稳数据地基。
//
// 分层：apps 不在 pluginReg（apps/picker 走各自老路径），故聚合时 apps 由调用方折成 Match 传入，
// 其余全局 provider 从 pluginReg 现取现跑。apps→Match 是**展示折入**（Launch 动作、图标来源=路径），
// 不改 apps 自身搜索/选中/启动那条已被真机验证的链路。
package app

import (
	"github.com/zyition/voke/poc/voke-go/internal/logic/appsmodel"
	"github.com/zyition/voke/poc/voke-go/internal/logic/engine"
	"github.com/zyition/voke/poc/voke-go/internal/providers"
	appui "github.com/zyition/voke/poc/voke-go/internal/ui"
)

// appsProviderID 是 apps 的插件 id（apps 不在 pluginReg，聚合里单独折入）。
const appsProviderID = "apps"

// foldAppMatch 把一条应用折成 providers.Match（展示用）：标题=名、副标题=路径、
// Icon=启动路径（与 apps 行同款 AppIcon 来源）、动作=Launch{Path}、原始分 1.0
// （apps 的顺序由 panelstate 过滤链已定，聚合里 apps 组内靠稳定排序保持插入序）。
func foldAppMatch(a appsmodel.AppEntry) providers.Match {
	return providers.Match{
		ProviderID: appsProviderID,
		Title:      a.Name,
		Subtitle:   a.Path,
		Icon:       a.Path,
		Action:     providers.Action{Kind: providers.ActionLaunch, Path: a.Path},
		Score:      1.0,
		Interact:   providers.InteractSpec{Kind: providers.InteractExecute},
	}
}

// collectGlobalBatches 按注册顺序收集本次全局查询的各 provider 批次：apps 批（传入的已过滤
// 应用折成 Match）在最前，其后是 pluginReg 里 **enabled && is_global** 的插件逐个跑 QueryCtx。
// apps/picker 不在 pluginReg（Get 落 false）天然跳过；非全局（base64 等带前缀）不进全局。
func (st *appState) collectGlobalBatches(apps []providers.Match, ctx providers.QueryCtx) []providers.Batch {
	batches := make([]providers.Batch, 0, len(st.providers.Providers))
	if len(apps) > 0 {
		batches = append(batches, providers.Batch{ProviderID: appsProviderID, Matches: apps})
	}
	for _, pc := range st.providers.Providers {
		if !pc.Enabled || !pc.IsGlobal {
			continue
		}
		p, ok := st.pluginReg.Get(pc.ID)
		if !ok {
			continue
		}
		if ms := p.Query(ctx); len(ms) > 0 {
			batches = append(batches, providers.Batch{ProviderID: pc.ID, Matches: ms})
		}
	}
	return batches
}

// globalAggregate = collectGlobalBatches → Registry.Aggregate（组内 weight×score×freq 排序、
// 组间保注册顺序、提示行单列）。appsMatches 由调用方从 panelstate 过滤结果折入。
func (st *appState) globalAggregate(appsMatches []providers.Match, ctx providers.QueryCtx) providers.Aggregated {
	return st.pluginReg.Aggregate(st.collectGlobalBatches(appsMatches, ctx), st.freq)
}

// ── 渲染就绪的扁平行模型（保住 apps 启动语义）──────────────────────────────
//
// 为什么不能让 apps 走通用 provider Launch：apps 启动是 `platform.LaunchApp` →
// `mygo.Shell.LaunchApp(InstalledApp{Path, ID})`（按 AUMID / `shell:AppsFolder\…` 语义），
// 与 provider 行的 `platform.Launch(exe,args)`（os/exec）不是一回事——把 apps 折成通用
// Launch{Path} 会打断 UWP / AppsFolder 启动（正是文档「apps→Match 留迁移后定」的缘由）。
// 故全局合并列表里，apps 行只带 AppEntry 下标（渲染 + 启动仍走既有 apps 执行器），
// 非 apps 组行带其在 provRows 里的下标（走 provider 执行器）。本层纯、可测；渲染/键盘
// 整合是薄映射（下一步），把风险关在可测的行装配里。
type globalRow struct {
	apps     bool // true=apps 行（appIdx 有效）；false=provider 行（provIdx 有效）
	appIdx   int  // apps 行：在 st.model.Results 的下标（交给既有 apps 执行器）
	provIdx  int  // provider 行：在 provRows 的下标（交给 provider 执行器）
	newGroup bool // 该行是否是其分组的首行（渲染组间分隔/标题）
	groupID  string
}

// buildGlobalRows 把「已过滤的 apps」放在最前一组，随后拼接 agg 里的非 apps 组，摊平成
// 单条可导航列表；返回行序列 + provider 行用的扁平 provRows（apps 的 appsMatch 组被忽略，
// apps 由 apps 参数单独呈现）。apps 为空时也不产生 apps 组（与 apps-only 旧行为一致）。
func buildGlobalRows(apps []appsmodel.AppEntry, agg providers.Aggregated) (rows []globalRow, provRows []providers.RankedMatch) {
	if len(apps) > 0 {
		for i := range apps {
			rows = append(rows, globalRow{apps: true, appIdx: i, newGroup: i == 0, groupID: appsProviderID})
		}
	}
	for _, g := range agg.Groups {
		if g.ProviderID == appsProviderID {
			continue // apps 组由上面的 apps 参数呈现，不走 provider 执行器
		}
		for j, m := range g.Matches {
			provRows = append(provRows, m)
			rows = append(rows, globalRow{
				provIdx: len(provRows) - 1, newGroup: j == 0, groupID: g.ProviderID,
			})
		}
	}
	return rows, provRows
}

// refreshGlobalAgg 在 Global 模式、查询非空时重算合并行（apps 结果 + 非 apps 全局插件组）。
// 只在**查询变化 / apps 加载完成**时调用，不进渲染帧——避免每帧跑 provider（windows 枚举会
// 阻塞主线程）。apps 批不在这里喂（apps 行由 st.model.Results 现取，保 LaunchApp 语义）。
// 非全局 / 空查询 → 清空合并态（renderResults 落回既有 AppsResults 路径，零回归）。
func (st *appState) refreshGlobalAgg() {
	st.globalRows, st.globalProvRows, st.globalChosen = nil, nil, 0
	if st.engineState.Mode() != engine.Global || st.engineState.Query() == "" {
		return
	}
	// 注册表 / 插件配置未就绪（装配前、或只测 apps 链路的用例如未设 pluginReg）→ 不聚合，
	// 保持 apps-only 路径。真实运行在 ready() 里两者都已就绪。
	if st.pluginReg == nil || len(st.providers.Providers) == 0 {
		return
	}
	q := st.engineState.Query()
	batches := st.collectGlobalBatches(nil, providers.QueryCtx{Raw: q, Query: q})
	agg := st.pluginReg.Aggregate(batches, st.freq)
	st.globalRows, st.globalProvRows = buildGlobalRows(st.model.Results, agg)
}

// globalActive 报告本次全局查询是否命中了 apps 以外的全局插件组——只有此时才走合并渲染；
// 否则（含 Linux/测试：数据型插件恒空）走原 apps-only 路径，逐字不变。
func (st *appState) globalActive() bool {
	return st.engineState.Mode() == engine.Global && len(st.globalProvRows) > 0
}

// providerName 取插件显示名（组标题用）；查不到退回 id 原样。
func (st *appState) providerName(id string) string {
	if pc, ok := st.providers.Lookup(id); ok && pc.Name != "" {
		return pc.Name
	}
	if id == appsProviderID {
		return "应用"
	}
	return id
}

// globalView 把合并行映射成 appui 的渲染输入（apps 行取 AppEntry，provider 行取 Match）。
func (st *appState) globalView() []appui.GlobalRow {
	out := make([]appui.GlobalRow, 0, len(st.globalRows))
	for _, r := range st.globalRows {
		if r.apps {
			a := st.model.Results[r.appIdx]
			out = append(out, appui.GlobalRow{
				Title: a.Name, Subtitle: a.Path, Icon: a.Path, ProviderID: appsProviderID,
				GroupLabel: st.providerName(appsProviderID), NewGroup: r.newGroup,
			})
			continue
		}
		m := st.globalProvRows[r.provIdx].Match
		out = append(out, appui.GlobalRow{
			Title: m.Title, Subtitle: m.Subtitle, Icon: m.Icon, ProviderID: r.groupID,
			GroupLabel: st.providerName(r.groupID), NewGroup: r.newGroup,
		})
	}
	return out
}

// chooseGlobal 钳位合并列表选中 + 滚入视野；空列表只复位。
func (st *appState) chooseGlobal(i int) {
	if len(st.globalRows) == 0 {
		st.globalChosen = 0
		return
	}
	st.globalChosen = max(0, min(i, len(st.globalRows)-1))
	st.globalList.ScrollIntoView(st.globalChosen)
}

// openGlobal 执行合并列表第 i 行：apps 行回到**既有 apps 执行器**（设 model.Chosen 走
// model.Open→execLaunch，保留 LaunchApp / 频率语义）；provider 行走 runProviderIntent
// （providerID 取该行自己的 ProviderID）。
func (st *appState) openGlobal(i int) {
	if i < 0 || i >= len(st.globalRows) {
		return
	}
	r := st.globalRows[i]
	if r.apps {
		st.model.Chosen = r.appIdx
		st.execLaunch(st.model.Open())
		return
	}
	m := st.globalProvRows[r.provIdx].Match
	st.runProviderIntent(m, m.ProviderID)
}

// clickGlobal 单击合并行：选中它再执行（与 provider/apps 行「单击即执行」同口径）。
func (st *appState) clickGlobal(i int) {
	st.globalChosen = i
	st.openGlobal(i)
}
