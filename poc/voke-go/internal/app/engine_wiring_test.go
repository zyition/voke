// engine_wiring_test.go：§7.3.3「前缀路由」接进面板查询链路后的端到端可见行为
// （main 侧接线：engine + config → appState/view/panel）。
//
// 三条纪律（沿用 §10.1.1 那两条「测试是假的」陷阱教训）：
//
//  1. **直调生产入口**：st.onQueryChanged / onEsc / moveSelection / open /
//     resetSession / buildPrefixTable / rankPlugins / engine.PrefixToken 就是被测
//     对象本身，测试里不复刻任何判定（第一版行视觉测试抄了一份 row()，结果把生产
//     row() 改回 ui.Button 它照样绿）。
//  2. **期望一律绝对值**：模式常量、前缀字符、插件名、结果名序列全写死，
//     不从 Descriptor / 常量反推——Descriptor 被改坏时这里必须红。
//  3. 要真实帧的（规则⑤「前缀不进输入框」、模式令牌、picker 行可见性）用
//     ui.NewTester 跑**生产 st.view**；纯状态断言不建帧，跑得快。
//
// ## 为什么没有「Esc 第二下关面板」「Enter 启动应用」两条用例
//
// hidePanel → Window.Hide、launchApp → Shell.LaunchApp 都走 mygo 的
// onMain / onMainValue，**无主循环时永久阻塞**（panel_behavior_test.go 与
// apps_test.go 头注记的同类坑；本轮实测复现：测试卡在 loop.go:106 的 chan receive，
// 直到 -timeout 才炸）。这是既有用例的既有边界，不是本轮新缺口。
//
// 反过来它也是一道守卫：任何**错误地**走进这两条路的改动（比如 Global 带查询词时
// 误吃 engine.EscBack 的 close=true 去关面板），会以测试超时的形式暴露，
// 而不是静默通过。
package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/zyition/voke/poc/voke-go/internal/logic/appsmodel"
	"github.com/zyition/voke/poc/voke-go/internal/logic/engine"
	"github.com/zyition/voke/poc/voke-go/internal/logic/frequency"
	"github.com/zyition/voke/poc/voke-go/internal/logic/geometry"
	"github.com/zyition/voke/poc/voke-go/internal/logic/testutil"
	"github.com/zyition/voke/poc/voke-go/internal/platform"
	"github.com/zyition/voke/poc/voke-go/internal/providers"
	appui "github.com/zyition/voke/poc/voke-go/internal/ui"
)

// 装配产物的绝对期望。改 providers.go 的 Descriptor 就得一并改这里——这是有意的：
// 默认值只允许出现在 Descriptor（铁律①），改动必须过一次这里的语义检查。
const (
	wantPickerPrefix = "#"
	wantAppsID       = "apps"
	wantAppsName     = "应用"
	wantAppsDesc     = "搜索并启动开始菜单应用"
)

// wantAll 是「三条测试应用全量」的结果名序列，由测试自己传入的那三个名字拼出
// （空查询不过限，同 search.FilterApps 的既有语义）。
var wantAll = strings.Join([]string{"Chrome", "Firefox", "VLC"}, ",")

// newWiredState 造一个**装配齐全**的 appState：前缀表与归一配置走生产装配函数
// buildPrefixTable，apps 数据用 testutil 造（不读磁盘），懒加载闸门换空实现。
//
// 不经 ready()：那条路要建窗口 / 走 onMain，无主循环会永久阻塞。这里要的是
// ready() 里那段装配的**同一份产物**（buildPrefixTable），不是它的副本。
func newWiredState(t *testing.T, appNames ...string) *appState {
	t.Helper()
	resetLoadGate(t)
	cfg, table, err := buildPrefixTable()
	if err != nil {
		t.Fatalf("buildPrefixTable 意外失败：%v", err)
	}
	if len(cfg.Warnings) != 0 {
		t.Fatalf("内置注册表不该产生配置警告，实得 %q", cfg.Warnings)
	}
	st := &appState{table: table, providers: cfg}
	st.model.Apps = testutil.MkApps(appNames...)
	// Loaded=true：枚举「已完成」，过滤才有结果可断言。懒加载闸门本身仍由
	// panel_behavior_test.go 的 TestOnQueryChangedWiresEnsureLoad 守着，
	// 本轮另加一条「picker 过滤词不得触发枚举」的用例。
	st.model.Loaded = true
	return st
}

// typeInto 把输入框置为 s 后走生产入口（= view.go 里 input.Changed() 的回调）。
// 上一帧的框内容留在 engineState.Query 里，正是 onQueryChanged 判定「退格删空」
// 所需的信息，测试不自己另传一套。
func typeInto(st *appState, s string) {
	st.model.Query = s
	st.onQueryChanged()
}

func resultNames(st *appState) string {
	out := make([]string, 0, len(st.model.Results))
	for _, a := range st.model.Results {
		out = append(out, a.Name)
	}
	return strings.Join(out, ",")
}

func rowIDs(rows []pluginRow) string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return strings.Join(out, ",")
}

// wantState 一起断言 engine.State 与输入框文本（接线最重要的不变式：
// 框内容恒等于 State.Query，前缀从不进框）。
func wantState(t *testing.T, st *appState, label string, mode engine.Mode, pluginID, query, box string) {
	t.Helper()
	got := st.engineState
	if got.Mode() != mode || got.PluginID() != pluginID || got.Query() != query {
		t.Errorf("%s：engineState = %+v，想要 {Mode:%s PluginID:%q Query:%q}", label, got, mode, pluginID, query)
	}
	if st.model.Query != box {
		t.Errorf("%s：输入框 = %q，想要 %q（规则⑤：前缀不进输入框）", label, st.model.Query, box)
	}
}

// wantNoToken 断言当前作用域不该向用户展示任何前缀令牌。
func wantNoToken(t *testing.T, st *appState, label string) {
	t.Helper()
	if got := tokenOf(st); got != "" {
		t.Errorf("%s：令牌 = %q，想要空串", label, got)
	}
}

// wantToken 断言当前作用域的前缀令牌字符。面包屑/令牌的唯一来源是栈顶作用域 +
// 前缀表；这里在测试侧按栈顶插件 id 查表还原，接线层不再自算。
func wantToken(t *testing.T, st *appState, label, want string) {
	t.Helper()
	if got := tokenOf(st); got != want {
		t.Errorf("%s：令牌 = %q，想要 %q", label, got, want)
	}
}

// tokenOf 返回栈顶作用域对应的前缀（Global/未登记 → ""）。
func tokenOf(st *appState) string { return prefixFor(st.engineState, st.table) }

// prefixFor 按栈顶作用域（picker 用其专属 id）在前缀表里取前缀；未登记 → ""。
func prefixFor(s engine.State, table engine.Table) string {
	id := s.PluginID()
	if s.Mode() == engine.PluginPicker {
		id = engine.PickerPluginID
	}
	for _, e := range table {
		if e.PluginID == id {
			return e.Prefix
		}
	}
	return ""
}

// wantUntouchedPicker 断言 Global 链路上 picker 的 rows / chosen 一个字节都没动。
func wantUntouchedPicker(t *testing.T, st *appState, label string) {
	t.Helper()
	if st.pickerRows != nil || st.pickerChosen != 0 {
		t.Errorf("%s：Global 不该碰 picker 状态，实得 rows=%+v chosen=%d", label, st.pickerRows, st.pickerChosen)
	}
}

// TestProviderRegenerateGuardSkipsSameInput 锁 Rust `regenerates_on_query` 的调用侧护栏：
// random 的整数候选每次查询重掷，输入不变（面板重显等「没改却重跑」路径）时必须跳过
// 重查，否则用户正要复制的值会变。进 SubMode(random/int) 出一批整数后，直接再调一次
// refreshProviderResults：命中同 key + RegeneratesOnQuery=true → provRows 逐字不变。
func TestProviderRegenerateGuardSkipsSameInput(t *testing.T) {
	st := newWiredState(t, "Chrome", "Firefox", "VLC")
	st.pluginReg = builtinProviders()

	typeInto(st, "#r ") // 进 Plugin(random)
	st.provChosen = 0   // 选「随机整数」
	st.open()           // → SubMode(random/int)，空查询出一批整数
	if st.engineState.SubMode() != "int" || len(st.provRows) < 2 {
		t.Fatalf("应进 SubMode(int) 并出 [重新生成 + 整数]，实得 %+v", st.provRows)
	}
	before := titlesOf(st.provRows)

	st.refreshProviderResults() // 同输入再跑一次
	if after := titlesOf(st.provRows); after != before {
		t.Errorf("会重掷 provider 同输入应跳过重查、结果不变\n前：%s\n后：%s", before, after)
	}

	// 输入一变（哪怕删空再打同长度）key 变 → 必重跑：这里靠换 query 验证护栏不是「永不重跑」。
	typeInto(st, "1-6")
	if got := titlesOf(st.provRows); got == before {
		t.Errorf("换了查询词应重跑（护栏只在同输入跳过）：%s", got)
	}
}

// titlesOf 把 provRows 标题拼成串，便于比较整批是否逐字一致。
func titlesOf(rows []providers.RankedMatch) string {
	parts := make([]string, len(rows))
	for i, r := range rows {
		parts[i] = r.Title
	}
	return strings.Join(parts, "|")
}

// TestBuildPrefixTableShape 守住装配产物的形状：engine.Table 的唯一来源是
// config.BuildTable，注册表默认值全部来自 Descriptor。
func TestBuildPrefixTableShape(t *testing.T) {
	cfg, table, err := buildPrefixTable()
	if err != nil {
		t.Fatalf("buildPrefixTable 失败：%v", err)
	}
	// apps 无前缀 → BuildTable 不为它生成条目；表里有 picker(#)/base64(#b)/uuid(#u)/timestamp(#t)/random(#r)/calculator(=) 六条，表序即优先级。
	if len(table) != 6 ||
		table[0] != (engine.PrefixEntry{Prefix: "#", PluginID: engine.PickerPluginID, Activation: engine.Direct}) ||
		table[1] != (engine.PrefixEntry{Prefix: "#b", PluginID: "base64", Activation: engine.Indirect}) ||
		table[2] != (engine.PrefixEntry{Prefix: "#u", PluginID: "uuid", Activation: engine.Indirect}) ||
		table[3] != (engine.PrefixEntry{Prefix: "#t", PluginID: "timestamp", Activation: engine.Indirect}) ||
		table[4] != (engine.PrefixEntry{Prefix: "#r", PluginID: "random", Activation: engine.Indirect}) ||
		table[5] != (engine.PrefixEntry{Prefix: "=", PluginID: "calculator", Activation: engine.Direct}) {
		t.Errorf("前缀表 = %+v，想要六条 [{# __picker Direct}, {#b base64 Indirect}, {#u uuid Indirect}, {#t timestamp Indirect}, {#r random Indirect}, {= calculator Direct}]", table)
	}
	if got := strings.Join(cfg.IDs(), ","); got != "apps,__picker,base64,uuid,timestamp,random,calculator,awake,windows,terminal,workspaces" {
		t.Errorf("注册 id 序列（注册顺序）= %q，想要 \"apps,__picker,base64,uuid,timestamp,random,calculator,awake,windows,terminal,workspaces\"", got)
	}
	apps, ok := cfg.Lookup(wantAppsID)
	if !ok {
		t.Fatal("归一结果里没有 apps")
	}
	if !apps.Enabled || !apps.IsGlobal || apps.Prefix != "" || apps.Weight != 1.2 || apps.Count != 5 {
		t.Errorf("apps 归一结果 = %+v，想要 {Enabled:true IsGlobal:true Prefix:\"\" Weight:1.2 Count:5}", apps)
	}
	picker, ok := cfg.Lookup(engine.PickerPluginID)
	if !ok {
		t.Fatal("归一结果里没有 picker 条目")
	}
	if picker.Prefix != "#" || picker.Activation != engine.Direct || picker.IsGlobal {
		t.Errorf("picker 条目 = %+v，想要 {Prefix:\"#\" Activation:Direct IsGlobal:false}", picker)
	}
}

// TestGlobalPathUnchangedWithoutPrefix 是「Global 零回归」的主用例：不打前缀时
// query→filter→结果 每一项期望都按**接线前**的语义写死（接线前
// appState.onQueryChanged 只有一行 `st.execute(st.model.OnQueryChanged(time.Now()))`，
// 没有 mode、没有令牌、没有 picker）。
//
// 覆盖：模式恒 Global / State.Query 与框内容恒等于原文（含首尾空格）/ 结果集逐条
// 绝对值 / 模式令牌恒空 / picker 三件套不动 / ↑↓ 仍只动 apps 的选中行。
func TestGlobalPathUnchangedWithoutPrefix(t *testing.T) {
	st := newWiredState(t, "Chrome", "Firefox", "VLC")

	tests := []struct {
		input   string
		wantRes string
	}{
		{"", wantAll},
		{"chr", "Chrome"},
		{"ch", "Chrome"},
		{"fi", "Firefox"},
		{"vlc", "VLC"},
		{"  chr  ", "Chrome"}, // Scan 保留原文（含首尾空格）；FilterApps 自己 trim
		{"   ", wantAll},      // 纯空白不命中任何前缀，trim 后为空 → 全量
		{"nope", ""},
		{"#ba", ""}, // 全等才切（决策⑥）：#ba 不切，仍是 Global 的裸查询词
	}
	for _, tc := range tests {
		typeInto(st, tc.input)
		wantState(t, st, "输入 "+tc.input, engine.Global, "", tc.input, tc.input)
		wantNoToken(t, st, "输入 "+tc.input)
		if got := resultNames(st); got != tc.wantRes {
			t.Errorf("输入 %q：结果 = %q，想要 %q", tc.input, got, tc.wantRes)
		}
		wantUntouchedPicker(t, st, "输入 "+tc.input)
	}

	// ↑↓ 在 Global 下仍只动 apps 的选中行（pickerChosen 恒 0）。
	typeInto(st, "")
	st.moveSelection(1)
	if st.model.Chosen != 1 || st.pickerChosen != 0 {
		t.Errorf("↓ 之后 model.Chosen=%d pickerChosen=%d，想要 1 和 0", st.model.Chosen, st.pickerChosen)
	}
	st.moveSelection(-1)
	if st.model.Chosen != 0 || st.pickerChosen != 0 {
		t.Errorf("↑ 之后 model.Chosen=%d pickerChosen=%d，想要 0 和 0", st.model.Chosen, st.pickerChosen)
	}
}

// TestEmptyTableDegradesToPureGlobal 对应 ready() 的降级分支：装配失败时
// st.table 为空（Normalize 出错返回零值），engine.Scan 在空表下**任何输入都只可能
// 是 Global**（engine_test.go 的空表用例守着这条），于是面板行为退回接线前的
// 纯 Global——注册表写坏不该让面板打不开，更不该改变不打前缀的用法。
func TestEmptyTableDegradesToPureGlobal(t *testing.T) {
	resetLoadGate(t)
	st := &appState{} // 表 / 归一配置都是零值 = 装配失败的降级态
	st.model.Apps = testutil.MkApps("Chrome", "Firefox", "VLC")
	st.model.Loaded = true

	for _, input := range []string{"#", "# ", "#chr", "#ba"} {
		typeInto(st, input)
		wantState(t, st, "空表输入 "+input, engine.Global, "", input, input)
		wantNoToken(t, st, "空表输入 "+input)
		wantUntouchedPicker(t, st, "空表输入 "+input)
	}
}

// TestTypingPickerPrefixEntersPluginPicker 是前缀路由的正路：打 "#" 全等命中
// 专属条目 → PluginPicker、框被清空、令牌是 "#"、列表出「已启用插件」。
func TestTypingPickerPrefixEntersPluginPicker(t *testing.T) {
	st := newWiredState(t, "Chrome", "Firefox", "VLC")
	typeInto(st, "chr") // 先进过一次正常搜索，后面才好对比「picker 不碰 apps」
	typeInto(st, wantPickerPrefix)

	wantState(t, st, "打 #", engine.PluginPicker, "", "", "")
	wantToken(t, st, "打 #", wantPickerPrefix)
	if got := rowIDs(st.pickerRows); got != wantAppsID+",base64,uuid,timestamp,random,calculator,awake,windows,terminal,workspaces" {
		t.Errorf("picker 列表 = %q，想要 %q（已启用插件 apps+base64+uuid+timestamp，picker 哨兵自己不算候选）", got, wantAppsID+",base64,uuid,timestamp,random,calculator,awake,windows,terminal,workspaces")
	}
	if len(st.pickerRows) != 10 {
		t.Fatalf("picker 列表行数 = %d，想要 10（下面的行内容断言按首行=apps 写死）", len(st.pickerRows))
	}
	if r := st.pickerRows[0]; r.Name != wantAppsName || r.Description != wantAppsDesc {
		t.Errorf("picker 首行 = %+v，想要 {%s %s}", r, wantAppsName, wantAppsDesc)
	}
	if st.pickerChosen != 0 {
		t.Errorf("picker 选中 = %d，想要 0（过滤词一变就复位，同 apps 侧 filter→Choose(0)）", st.pickerChosen)
	}
	if st.pluginPending() {
		t.Error("picker 态不该被判成「未接入插件」")
	}
	// picker 不重排 apps：结果仍是进 picker 之前那次 Global 过滤的 Chrome。
	if got := resultNames(st); got != "Chrome" {
		t.Errorf("picker 态不该动 apps 结果，实得 %q", got)
	}
}

// TestRankPluginsUsesBothScoringChannels 直测 picker 列表的数据源：
// 名称走 fuzzy 通道（含拼音 / 首字母键），别名走 bonus 通道（只加分、不触发）。
func TestRankPluginsUsesBothScoringChannels(t *testing.T) {
	cfg, _, err := buildPrefixTable()
	if err != nil {
		t.Fatalf("buildPrefixTable 失败：%v", err)
	}
	tests := []struct {
		query string
		want  string
		why   string
	}{
		{"", wantAppsID + ",base64,uuid,timestamp,random,calculator,awake,windows,terminal,workspaces", "空过滤词不过滤任何行（十个已启用插件都列出）"},
		{"yy", wantAppsID, "别名 yy 全等 → TierExact"},
		{"app", wantAppsID + ",awake", "apps 别名 app 全等在前；awake 经别名 a 前缀 bonus 托底在后"},
		{"yingyong", wantAppsID, "名称全拼走 fuzzy 通道命中"},
		{"yying", wantAppsID, "别名前缀部分匹配 → bonus 托底进 Fuzzy tier"},
		{"zzz", "", "fuzzy 判不掉且别名不沾边 → 被过滤"},
		{"chr", "calculator", "apps 名称/别名不沾边被过滤；calculator 经别名 c 前缀 bonus 命中"},
	}
	for _, tc := range tests {
		if got := rowIDs(rankPlugins(cfg, tc.query)); got != tc.want {
			t.Errorf("%s：query %q 得到 %q，想要 %q", tc.why, tc.query, got, tc.want)
		}
	}
	// picker 哨兵条目自己永远不出现在列表里（选中它没有意义）。
	for _, q := range []string{"", "#", "列出所有插件", "picker"} {
		if got := rowIDs(rankPlugins(cfg, q)); strings.Contains(got, engine.PickerPluginID) {
			t.Errorf("query %q 的列表里出现了 picker 自己：%q", q, got)
		}
	}
}

// TestPickerEnterThenAppsSearchEqualsGlobal 是 picker 的正路端到端：
// # → 列表 → Enter 选定 apps（engine.SelectPlugin）→ 再打字走 apps 过滤，
// 且**同一查询词的结果集与 Global 逐字相同**（apps 是唯一 provider）。
func TestPickerEnterThenAppsSearchEqualsGlobal(t *testing.T) {
	st := newWiredState(t, "Chrome", "Firefox", "VLC")
	global := newWiredState(t, "Chrome", "Firefox", "VLC")

	// 对照组：Global 直接打 chr。
	typeInto(global, "chr")

	typeInto(st, wantPickerPrefix)
	st.open() // Enter 走生产入口，picker 分支应落到 confirmPicker
	wantState(t, st, "Enter 选定 apps", engine.Plugin, wantAppsID, "", "")
	if st.pluginPending() {
		t.Error("PluginID=apps 不该被判成「未接入插件」")
	}
	if got := resultNames(st); got != wantAll {
		t.Errorf("进插件第一级后（空查询词）结果 = %q，想要 %q", got, wantAll)
	}
	// apps 无前缀 → 令牌空串（这条是 providers.go 里 DefaultPrefix:"" 的直接后果，
	// 已记入回报的决策点：picker 选进来的无前缀插件没有可见令牌）。
	wantNoToken(t, st, "Enter 选定 apps")

	// 再打字：不再判前缀（规则④），查询词交给 apps。
	typeInto(st, "chr")
	wantState(t, st, "插件态打 chr", engine.Plugin, wantAppsID, "chr", "chr")
	if got, want := resultNames(st), resultNames(global); got != want {
		t.Errorf("Plugin(apps) 结果 = %q，与 Global 的 %q 不一致（应逐字相同）", got, want)
	}
	if got := resultNames(st); got != "Chrome" {
		t.Errorf("结果 = %q，想要 Chrome", got)
	}

	// 回退链单一由「空 + Backspace」按键驱动（onEmptyBackspace，经 ui OnTextKey）：删到
	// 空只停在当前层，再按一次才退一级——无「删到空那一刻自动弹出」的状态猜测 hack。
	// Esc 只负责隐藏。完整多级链由 engine 包 TestBackspaceBack 钉死。
	typeInto(st, "") // 删空 chr：停在 Plugin(apps)、空查询
	wantState(t, st, "删空停在插件", engine.Plugin, wantAppsID, "", "")
	st.onEmptyBackspace() // 空 + Backspace → PluginPicker
	wantState(t, st, "空退 picker", engine.PluginPicker, "", "", "")
	wantToken(t, st, "空退 picker", wantPickerPrefix)
	if got := rowIDs(st.pickerRows); got != wantAppsID+",base64,uuid,timestamp,random,calculator,awake,windows,terminal,workspaces" {
		t.Errorf("回 picker 后列表 = %q，想要 %q", got, wantAppsID+",base64,uuid,timestamp,random,calculator,awake,windows,terminal,workspaces")
	}
	typeInto(st, "z")     // picker 打过滤词
	typeInto(st, "")      // 删空：停在 PluginPicker 空
	st.onEmptyBackspace() // 空 + Backspace → Global
	wantState(t, st, "空退 Global", engine.Global, "", "", "")
	wantNoToken(t, st, "空退 Global")
}

// TestPickerFilterDoesNotTriggerAppsLoad 锁「picker 与 apps 互不打扰」：
// picker 里的过滤词既不重排 apps 结果，也不触发 apps 的懒加载枚举
// （§7.7 决策④：只有真要搜应用时才枚举）。
func TestPickerFilterDoesNotTriggerAppsLoad(t *testing.T) {
	st := newWiredState(t, "Chrome", "Firefox", "VLC")
	st.model.Loaded = false // 回到「还没枚举过」
	typeInto(st, wantPickerPrefix)
	typeInto(st, "yy")
	if st.model.Loading {
		t.Error("picker 过滤词不该置 model.Loading")
	}
	if got := appsmodel.LoadTriggerCount(); got != 0 {
		t.Errorf("picker 过滤词触发了 %d 次枚举，想要 0 次", got)
	}
	if got := len(st.model.Results); got != 0 {
		t.Errorf("picker 态不该过滤 apps，实得 Results 长度 %d", got)
	}
	if got := rowIDs(st.pickerRows); got != wantAppsID {
		t.Errorf("picker 列表 = %q，想要 %q（yy 靠别名命中）", got, wantAppsID)
	}

	// 对照组：同一个词在 Global 下会触发一次枚举（newWiredState 顺带复位了闸门）。
	global := newWiredState(t, "Chrome", "Firefox", "VLC")
	global.model.Loaded = false
	typeInto(global, "chr")
	if got := appsmodel.LoadTriggerCount(); got != 1 {
		t.Errorf("Global 下的 chr 应触发 1 次枚举，实得 %d", got)
	}
	if !global.model.Loading {
		t.Error("对照组：Global 非空查询应置 Loading")
	}
}

// TestNoPrefixRejudgedOnceActivated 是规则④（V-2 / V-9 的逻辑面）：进了插件作用域
// 之后，前缀字符只当查询词，不再触发任何模式切换。
func TestNoPrefixRejudgedOnceActivated(t *testing.T) {
	st := newWiredState(t, "Chrome", "Firefox", "VLC")

	// picker 里再打 "#ba"：仍是 picker，只当过滤词。
	typeInto(st, wantPickerPrefix)
	typeInto(st, "#ba")
	wantState(t, st, "picker 里打 #ba", engine.PluginPicker, "", "#ba", "#ba")
	wantToken(t, st, "picker 里打 #ba", wantPickerPrefix)

	// 把过滤词收回可用的 "yy" 后 Enter 选定 apps，插件态里打 "#" 也不切。
	typeInto(st, "yy")
	st.open()
	wantState(t, st, "Enter 选定", engine.Plugin, wantAppsID, "", "")
	typeInto(st, wantPickerPrefix)
	wantState(t, st, "插件态打 #", engine.Plugin, wantAppsID, "#", "#")
	wantNoToken(t, st, "插件态打 #（apps 无前缀）")

	// 子模式里打 picker 前缀也不切（用 engine.EnterSubMode 构造 fake 状态）。
	st.engineState = engine.EnterSubMode(st.engineState, "base64", "enc")
	typeInto(st, wantPickerPrefix)
	got := st.engineState
	if got.Mode() != engine.SubMode || got.PluginID() != "base64" || got.SubMode() != "enc" || got.Query() != wantPickerPrefix {
		t.Errorf("子模式里打前缀 = %+v，想要 {SubMode base64 enc Query:\"#\"}", got)
	}
}

// TestBackspaceEmptiesThenStepsDown 锁 Backspace 逐级回退（CmdPal 对齐：Esc 只隐藏、
// 回退链整个交给 Backspace）：
//   - picker 里把过滤词删空 → 退回 Global；
//   - 插件第一级把查询词删空 → **退到 PluginPicker**（不再原地保持，§10.2 第 9 条已定）；
//   - Global 里删字符 = 普通查询词变短，一个额外判定都不加。
func TestBackspaceEmptiesThenStepsDown(t *testing.T) {
	st := newWiredState(t, "Chrome", "Firefox", "VLC")

	// Global 删字符：不逐级（prev.Mode 不是 Activated）。
	typeInto(st, "chr")
	typeInto(st, "ch")
	wantState(t, st, "Global 删字符", engine.Global, "", "ch", "ch")

	// picker：过滤词删空**停在 picker**，再按一次空 Backspace 才退 Global。
	typeInto(st, wantPickerPrefix)
	typeInto(st, "yy")
	wantState(t, st, "picker 打 yy", engine.PluginPicker, "", "yy", "yy")
	typeInto(st, "")
	wantState(t, st, "picker 删空停住", engine.PluginPicker, "", "", "")
	st.onEmptyBackspace()
	wantState(t, st, "空退 Global", engine.Global, "", "", "")

	// Plugin(apps)：删空停在插件，再空 Backspace 退 PluginPicker。
	typeInto(st, wantPickerPrefix)
	st.open()
	typeInto(st, "chr")
	typeInto(st, "")
	wantState(t, st, "插件删空停住", engine.Plugin, wantAppsID, "", "")
	st.onEmptyBackspace()
	wantState(t, st, "空退 picker", engine.PluginPicker, "", "", "")
	wantToken(t, st, "空退 picker", wantPickerPrefix)
	if got := rowIDs(st.pickerRows); got != wantAppsID+",base64,uuid,timestamp,random,calculator,awake,windows,terminal,workspaces" {
		t.Errorf("插件态空退后 picker 列表 = %q，想要 %q", got, wantAppsID+",base64,uuid,timestamp,random,calculator,awake,windows,terminal,workspaces")
	}

	// 子模式（fake）：删空停在子模式，再空 Backspace 退插件第一级（清 SubMode、留 PluginID）。
	// 用真正的进入路径构造父插件帧（SelectPlugin→EnterSubMode），出栈才能回到插件层。
	st.engineState = engine.EnterSubMode(engine.SelectPlugin(engine.State{}, "base64"), "base64", "dec")
	typeInto(st, "aB9")
	typeInto(st, "")
	wantState(t, st, "子模式删空停住", engine.SubMode, "base64", "", "")
	st.onEmptyBackspace()
	wantState(t, st, "空退插件第一级", engine.Plugin, "base64", "", "")
}

// TestEscChainFromSubModeWithFakePlugin 用一个**未注册实现**的 id（terminal：既不在
// builtinDescriptors、也不在 builtinProviders）走「未接入插件」路径，锁两条护栏：不出
// apps 结果、Enter 不启动。（base64/uuid/timestamp/random/calculator 已落地，不能再当未接入 fake。）
func TestEscChainFromSubModeWithFakePlugin(t *testing.T) {
	st := newWiredState(t, "Chrome", "Firefox", "VLC")
	typeInto(st, "chr") // apps 有过一次正常结果，后面才看得出「未接入」态没被改
	before := resultNames(st)

	// SubMode(terminal/enc) —— 未注册的 id；经真实进入路径压入插件帧，出栈才回插件层。
	st.engineState = engine.EnterSubMode(engine.SelectPlugin(engine.State{}, "terminal"), "terminal", "enc")
	if !st.pluginPending() {
		t.Fatal("PluginID=terminal 应被判成未接入插件")
	}
	if st.providerActive() {
		t.Fatal("terminal 未注册实现，不该 providerActive")
	}
	typeInto(st, "aB9")
	wantState(t, st, "未接入插件打字", engine.SubMode, "terminal", "aB9", "aB9")
	if got := resultNames(st); got != before {
		t.Errorf("未接入插件态不该动 apps 结果：%q → %q", before, got)
	}
	st.open() // Enter 在未接入插件上不得启动 apps 列表里的任何一条
	if got := resultNames(st); got != before {
		t.Errorf("未接入插件态的 Enter 动了 apps 结果：%q → %q", before, got)
	}
	if want := "插件 terminal 尚未接入（本轮只落地 应用 与 插件列表）"; st.pendingHint() != want {
		t.Errorf("占位文案 = %q，想要 %q", st.pendingHint(), want)
	}

	// 回退单一由「空 + Backspace」按键驱动：删空停在子模式，再空 Backspace 才退插件第一级。
	// terminal 未注册 → 不在前缀表 → 令牌恒空串。完整多级链由 engine 包钉死。
	typeInto(st, "") // 删空 aB9：停在 SubMode(terminal/enc) 空查询
	wantState(t, st, "删空停在子模式", engine.SubMode, "terminal", "", "")
	st.onEmptyBackspace() // 空 + Backspace：SubMode → Plugin(terminal)
	wantState(t, st, "空退 Plugin(terminal)", engine.Plugin, "terminal", "", "")
	wantNoToken(t, st, "空退（terminal 不在前缀表 → 令牌空串）")
}

// TestBase64EndToEndCompute 端到端跑通 provider 接线（compute 层，不碰剪贴板/Window）：
// 打 "#b " 进 Plugin(base64) → providerActive、出第一级两行 → Enter「加密」进 SubMode
// (base64/enc) → 打 hello 出编码结果行。验证 applyEngineState→refreshProviderResults→
// RunOne→provRows 与 open→openProvider→IntentEnterSubMode 这条真实路由链。Copy 的执行
// （写剪贴板 + 隐藏面板）走 onMain，无主循环不可 headless 跑，留真机验。
func TestBase64EndToEndCompute(t *testing.T) {
	st := newWiredState(t, "Chrome", "Firefox", "VLC")
	st.pluginReg = builtinProviders()

	typeInto(st, "#b ") // Indirect：命中需前缀 + 一个空格
	if st.engineState.Mode() != engine.Plugin || st.engineState.PluginID() != "base64" {
		t.Fatalf("打 \"#b \" 应进 Plugin(base64)，实得 %+v", st.engineState)
	}
	if !st.providerActive() {
		t.Fatal("base64 已在 builtinProviders 注册 → providerActive 应为真")
	}
	if len(st.provRows) != 2 || st.provRows[0].Title != "加密" {
		t.Fatalf("provider 第一级应 2 行、首行「加密」，实得 %+v", st.provRows)
	}

	st.provChosen = 0 // 选中「加密」
	st.open()         // openProvider → IntentEnterSubMode → 进 enc 子模式
	if st.engineState.Mode() != engine.SubMode || st.engineState.SubMode() != "enc" {
		t.Fatalf("Enter「加密」应进 SubMode(base64/enc)，实得 %+v", st.engineState)
	}

	typeInto(st, "hello") // enc 子模式 → 编码结果行
	if len(st.provRows) != 1 || st.provRows[0].Title != "aGVsbG8=" {
		t.Fatalf("enc 下 hello 应出 1 行 aGVsbG8=，实得 %+v", st.provRows)
	}
}

// TestUUIDEndToEndCompute 端到端跑通 uuid 接线（compute 层）：打 "#u " 进 Plugin(uuid)
// → providerActive、默认出 1 条 UUID；打 "2" → requested_count 生效出 2 条。验证 uuid
// 无子模式也走通同一 providerActive/refreshProviderResults 路径。Copy 执行留真机。
func TestUUIDEndToEndCompute(t *testing.T) {
	st := newWiredState(t, "Chrome", "Firefox", "VLC")
	st.pluginReg = builtinProviders()

	typeInto(st, "#u ") // Indirect：前缀 + 空格
	if st.engineState.Mode() != engine.Plugin || st.engineState.PluginID() != "uuid" {
		t.Fatalf("打 \"#u \" 应进 Plugin(uuid)，实得 %+v", st.engineState)
	}
	if !st.providerActive() {
		t.Fatal("uuid 已注册 → providerActive 应为真")
	}
	if len(st.provRows) != 1 || len(st.provRows[0].Title) != 36 {
		t.Fatalf("默认应出 1 条 36 字符 UUID，实得 %+v", st.provRows)
	}

	typeInto(st, "2") // 个数解析
	if len(st.provRows) != 2 {
		t.Fatalf("打 2 应出 2 条，实得 %d", len(st.provRows))
	}
	if st.provRows[0].Subtitle != "UUID v4（1 / 2）" {
		t.Errorf("多条应带序号副标题，实得 %q", st.provRows[0].Subtitle)
	}
}

// TestTimestampEndToEndCompute 端到端跑通 timestamp 接线（compute 层）：打 "#t " 进
// Plugin(timestamp) → providerActive、空查询默认出当前 ts 两行；打 10 位串（偏移 0
// 下 New 走系统时钟但换算与 now 无关）→ 出确定的一行 UTC。锁住 #t 前缀路由与
// refreshProviderResults 对无子模式 value-provider 的复用；Copy 执行留真机。
func TestTimestampEndToEndCompute(t *testing.T) {
	st := newWiredState(t, "Chrome", "Firefox", "VLC")
	st.pluginReg = builtinProviders()

	typeInto(st, "#t ") // Indirect：前缀 + 空格
	if st.engineState.Mode() != engine.Plugin || st.engineState.PluginID() != "timestamp" {
		t.Fatalf("打 \"#t \" 应进 Plugin(timestamp)，实得 %+v", st.engineState)
	}
	if !st.providerActive() {
		t.Fatal("timestamp 已注册 → providerActive 应为真")
	}
	if len(st.provRows) != 2 {
		t.Fatalf("空查询默认应出秒/毫秒两行，实得 %+v", st.provRows)
	}

	typeInto(st, "1700000000") // 10 位（秒），偏移 0 → 只出 UTC 一行
	if len(st.provRows) != 1 || st.provRows[0].Title != "2023-11-14 22:13:20 UTC" {
		t.Fatalf("10 位应出 1 行 UTC，实得 %+v", st.provRows)
	}
	if st.provRows[0].Action.Text != "2023-11-14 22:13:20 UTC" {
		t.Errorf("换算行 Copy 文本应 = 标题：%+v", st.provRows[0].Action)
	}
}

// TestRandomEndToEndCompute 端到端跑通 random 接线（compute 层）：打 "#r " 进
// Plugin(random) → 一级 4 个类型方向 → Enter「随机整数」进 SubMode(random/int) →
// 二级首条「重新生成」+ 默认 count 条整数。验证 #r 前缀 + 两级子模式复用同一
// providerActive/refreshProviderResults/openProvider 链路。Refresh/Copy 执行留真机。
func TestRandomEndToEndCompute(t *testing.T) {
	st := newWiredState(t, "Chrome", "Firefox", "VLC")
	st.pluginReg = builtinProviders()

	typeInto(st, "#r ") // Indirect：前缀 + 空格
	if st.engineState.Mode() != engine.Plugin || st.engineState.PluginID() != "random" {
		t.Fatalf("打 \"#r \" 应进 Plugin(random)，实得 %+v", st.engineState)
	}
	if !st.providerActive() {
		t.Fatal("random 已注册 → providerActive 应为真")
	}
	if len(st.provRows) != 4 || st.provRows[0].Title != "随机整数" {
		t.Fatalf("一级应 4 个类型方向、首行「随机整数」，实得 %+v", st.provRows)
	}

	st.provChosen = 0 // 选中「随机整数」
	st.open()         // openProvider → IntentEnterSubMode → 进 int 子模式
	if st.engineState.Mode() != engine.SubMode || st.engineState.SubMode() != "int" {
		t.Fatalf("Enter「随机整数」应进 SubMode(random/int)，实得 %+v", st.engineState)
	}

	// 二级空输入：首条「重新生成」+ 默认 count(5) 条整数 = 6 行。
	if len(st.provRows) != 6 || st.provRows[0].Title != "重新生成" {
		t.Fatalf("二级 int 空输入应 1 重新生成 + 5 结果，实得 %+v", st.provRows)
	}
	if st.provRows[0].Action.Kind != providers.ActionRefresh {
		t.Errorf("首条应为 Refresh：%+v", st.provRows[0].Action)
	}
}

// TestCalculatorEndToEndCompute 端到端跑通 calculator 接线（compute 层）：打 `=`
// （Direct，前缀即触发）进 Plugin(calculator)，随后输入表达式「1+1」→ 出结果行 "2"。
// 验证 `=` 前缀 + Direct 激活复用同一 providerActive/refreshProviderResults 路径，
// 且非法中间态（如 `=1+`）返回空、不闪结果。Copy 执行留真机。
func TestCalculatorEndToEndCompute(t *testing.T) {
	st := newWiredState(t, "Chrome", "Firefox", "VLC")
	st.pluginReg = builtinProviders()

	typeInto(st, "=") // Direct：前缀即切
	if st.engineState.Mode() != engine.Plugin || st.engineState.PluginID() != "calculator" {
		t.Fatalf("打 \"=\" 应进 Plugin(calculator)，实得 %+v", st.engineState)
	}
	if !st.providerActive() {
		t.Fatal("calculator 已注册 → providerActive 应为真")
	}
	if len(st.provRows) != 0 {
		t.Fatalf("空表达式不该出结果，实得 %+v", st.provRows)
	}

	typeInto(st, "1+") // 非法中间态 → 仍空（逐字符输入不闪结果）
	if len(st.provRows) != 0 {
		t.Fatalf("\"1+\" 非法应无结果，实得 %+v", st.provRows)
	}

	typeInto(st, "1+1") // 合法 → 一行 "2"，副标题回显表达式
	if len(st.provRows) != 1 || st.provRows[0].Title != "2" {
		t.Fatalf("\"1+1\" 应出 1 行 2，实得 %+v", st.provRows)
	}
	if st.provRows[0].Action.Text != "2" {
		t.Errorf("结果行 Copy 文本应 = 标题：%+v", st.provRows[0].Action)
	}
}

// TestAwakeEndToEndCompute 端到端跑通 awake 接线（compute 层）：awake 无前缀，经
// `#` picker 选最后一条（awake）进 Plugin(awake) → providerActive、一级列两方向；
// 打「45」走快捷路径出一条 SetAwake。验证 picker→无前缀 provider 的进入路径与
// refreshProviderResults 复用。SetAwake/CancelAwake 执行落 step8b dispatcher（真机）。
func TestAwakeEndToEndCompute(t *testing.T) {
	st := newWiredState(t, "Chrome", "Firefox", "VLC")
	st.pluginReg = builtinProviders()

	// 进 picker，选中 awake 那一行（无前缀，只能经 picker 进入）。
	typeInto(st, wantPickerPrefix)
	st.pickerChosen = -1
	for i, r := range st.pickerRows {
		if r.ID == "awake" {
			st.pickerChosen = i
			break
		}
	}
	if st.pickerChosen < 0 {
		t.Fatal("picker 列表里没有 awake")
	}
	st.open() // confirmPicker → Plugin(awake)
	if st.engineState.Mode() != engine.Plugin || st.engineState.PluginID() != "awake" {
		t.Fatalf("picker 选定 awake 应进 Plugin(awake)，实得 %+v", st.engineState)
	}
	if !st.providerActive() {
		t.Fatal("awake 已注册 → providerActive 应为真")
	}
	// 一级（New 恒未激活）：两个进入二级的方向。
	if len(st.provRows) != 2 || st.provRows[0].Title != "仅系统唤醒" {
		t.Fatalf("awake 一级应两方向，实得 %+v", st.provRows)
	}

	typeInto(st, "45") // 快捷数字 → 一条 SetAwake（仅系统唤醒）
	if len(st.provRows) != 1 || !strings.Contains(st.provRows[0].Title, "45 分钟") {
		t.Fatalf("快捷 45 应出一条「45 分钟」，实得 %+v", st.provRows)
	}
	if st.provRows[0].Action.Kind != providers.ActionSetAwake {
		t.Errorf("快捷项应为 SetAwake：%+v", st.provRows[0].Action)
	}
}

// TestLoadConfigsWithOverride 端到端验配置接线：env 指向一份把 base64 关掉的 TOML →
// loadConfigs 应用覆盖（base64 disabled、不进前缀表）。证明 appcfg→Normalize 这条路真接上。
func TestLoadConfigsWithOverride(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte("[providers.base64]\nenabled = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(configPathEnv, p)

	_, cfg, table, err := loadConfigs()
	if err != nil {
		t.Fatalf("loadConfigs 失败：%v", err)
	}
	if b, ok := cfg.Lookup("base64"); !ok || b.Enabled {
		t.Errorf("base64 应按配置关闭：%+v ok=%v", b, ok)
	}
	for _, e := range table {
		if e.PluginID == "base64" {
			t.Errorf("disabled base64 不该进前缀表：%+v", table)
		}
	}
	// 其余插件不受影响（uuid 仍默认启用、#u 仍在表）。
	if u, ok := cfg.Lookup("uuid"); !ok || !u.Enabled {
		t.Errorf("uuid 不该被波及：%+v", u)
	}
}

// TestLoadConfigsNoFileIsPureDefault 锁零回归：配置文件不存在（指向 temp 下的 absent.json）
// → 与 buildPrefixTable 逐字一致（首启用无 config 时行为不变）。
func TestLoadConfigsNoFileIsPureDefault(t *testing.T) {
	t.Setenv(configPathEnv, filepath.Join(t.TempDir(), "absent.json"))
	_, cfg, table, err := loadConfigs()
	if err != nil {
		t.Fatalf("loadConfigs 失败：%v", err)
	}
	wantCfg, wantTable, wantErr := buildPrefixTable()
	if wantErr != nil {
		t.Fatalf("buildPrefixTable 失败：%v", wantErr)
	}
	if len(table) != len(wantTable) || len(cfg.Providers) != len(wantCfg.Providers) {
		t.Errorf("无配置文件应与纯默认逐字等：table %d vs %d, providers %d vs %d",
			len(table), len(wantTable), len(cfg.Providers), len(wantCfg.Providers))
	}
}

// TestLoadConfigsBadTOMLFallsBack 锁「坏配置挡不住面板」：TOML 坏 → appcfg 回落默认 +
// 记日志，loadConfigs 返回可用默认产物（base64 仍启用、#b 仍在表），err 为 nil。
func TestLoadConfigsBadTOMLFallsBack(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte("this is = = not toml"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(configPathEnv, p)

	_, cfg, table, err := loadConfigs()
	if err != nil {
		t.Errorf("坏 JSON 应回落默认、不返回致命 error：%v", err)
	}
	if b, ok := cfg.Lookup("base64"); !ok || !b.Enabled {
		t.Errorf("坏配置下 base64 应仍默认启用：%+v", b)
	}
	found := false
	for _, e := range table {
		if e.PluginID == "base64" {
			found = true
		}
	}
	if !found {
		t.Errorf("坏配置下 #b 应仍在表：%+v", table)
	}
}

// TestFrequencyPersistenceRoundtrip 验频率表持久化接线：env 指向路径后 saveFrequency 落盘
// （含 MkdirAll）、loadFrequency 读回等价；文件不存在 → loadFrequency 给空表（首启用不炸）。
func TestFrequencyPersistenceRoundtrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "freq.json")
	t.Setenv(freqPathEnv, p)

	store := frequency.New()
	store.Record(frequency.Key("base64", "加密"))
	store.Record(frequency.Key("base64", "加密"))
	saveFrequency(store)

	back := loadFrequency()
	if back[frequency.Key("base64", "加密")] != 2 {
		t.Errorf("频率表落盘读回错：%+v", back)
	}

	// 不存在的路径 → 空表、不 panic。
	t.Setenv(freqPathEnv, filepath.Join(t.TempDir(), "sub", "absent.json"))
	if len(loadFrequency()) != 0 {
		t.Error("文件不存在应给空表")
	}
}

// TestExecPlatformActionRouting 验副作用动作的分派：各 ActionKind 走对的平台调用并由其
// 返回值决定「是否隐藏面板」。Linux stub 下：SetAwake/CancelAwake→true、Launch 空 exe→true
// （no-op 无错）、ActivateWindow→false（非 Windows 恒不支持）、LaunchTerminalProfile→false
// （App Activation 占位返回 false，且无兜底 wt.exe 路径可回退）、未知→false。
// （Windows 真机上 ActivateWindow 依真实 hwnd、LaunchTerminalProfile 依 COM 激活 / wt.exe
// 回退返回，均留真机验。）
func TestExecPlatformActionRouting(t *testing.T) {
	st := newWiredState(t, "Chrome")

	if !st.execPlatformAction(providers.Action{Kind: providers.ActionSetAwake}) {
		t.Error("SetAwake 应返回 true（生效→记频率+隐藏）")
	}
	st.execPlatformAction(providers.Action{Kind: providers.ActionCancelAwake}) // 收尾停 goroutine

	if !st.execPlatformAction(providers.Action{Kind: providers.ActionLaunch}) {
		t.Error("Launch 空 exe 视为 no-op 成功，应 true")
	}
	if st.execPlatformAction(providers.Action{Kind: providers.ActionActivateWindow, Hwnd: 1234}) {
		t.Error("非 Windows ActivateWindow 应 false（不隐藏）")
	}
	if st.execPlatformAction(providers.Action{Kind: providers.ActionLaunchTerminalProfile, Aumid: "x", Profile: "y"}) {
		t.Error("非 Windows 激活占位 false、无兜底路径，终端 profile 应 false（保留面板）")
	}
	// 带一个不存在的兜底 wt.exe：激活仍 false，回退直启也失败 → 整体 false（且不应 panic）。
	if st.execPlatformAction(providers.Action{Kind: providers.ActionLaunchTerminalProfile, Aumid: "x", Profile: "y", FallbackPath: `/nonexistent/wt.exe`}) {
		t.Error("激活失败且兜底路径不可执行，应 false")
	}
	if st.execPlatformAction(providers.Action{Kind: providers.ActionNone}) {
		t.Error("未知动作应 false")
	}
}

// TestAwakeProviderReflectsPlatformState 验注册表里的 awake provider 现读 platform 真实状态：
// 未激活一级两方向；SetAwake 后首条升级为「保持唤醒中…」取消项（+两方向=3）；Cancel 回落。
func TestAwakeProviderReflectsPlatformState(t *testing.T) {
	platform.CancelAwake() // 确保干净起点
	defer platform.CancelAwake()
	p, ok := builtinProviders().Get("awake")
	if !ok {
		t.Fatal("注册表应有 awake")
	}
	if hits := p.Query(providers.QueryCtx{}); len(hits) != 2 || hits[0].Title != "仅系统唤醒" {
		t.Fatalf("未激活应两方向：%+v", hits)
	}
	min := 45
	platform.SetAwake(&min, false)
	hits := p.Query(providers.QueryCtx{})
	if len(hits) != 3 || !strings.Contains(hits[0].Title, "保持唤醒中") {
		t.Fatalf("激活后首条应是「保持唤醒中」取消项（+两方向）：%+v", hits)
	}
	if hits[0].Action.Kind != providers.ActionCancelAwake {
		t.Errorf("取消项动作应是 CancelAwake：%+v", hits[0].Action)
	}
}

// TestResetSessionClearsModes 直测会话边界函数（调用点是 showPanel 与
// hidePanel，两处都要建窗 / 走 onMain，无主循环不可跑）。
func TestResetSessionClearsModes(t *testing.T) {
	st := newWiredState(t, "Chrome", "Firefox", "VLC")
	typeInto(st, "chr")
	typeInto(st, wantPickerPrefix)
	if st.engineState.Mode() != engine.PluginPicker {
		t.Fatalf("前置条件：模式 = %s", st.engineState.Mode())
	}
	st.resetSession()
	wantState(t, st, "resetSession", engine.Global, "", "", "")
	if st.pickerRows != nil || st.pickerChosen != 0 {
		t.Errorf("resetSession 后 picker 状态 = %+v / %d，想要 nil / 0", st.pickerRows, st.pickerChosen)
	}
	// resetSession 只清模式，不动 apps 的结果（清空输入框由 showPanel 那行负责）。
	if got := resultNames(st); got != "Chrome" {
		t.Errorf("resetSession 不该动 apps 结果，实得 %q", got)
	}
}

// newViewTester 用**生产 st.view** 建一帧（不在测试里复刻 chrome）。
//
// 预置图标缓存为 nil：apps 分支的 row() 会取图标，而 appIcon → Shell.FileIcon →
// Screen.Displays() 走 onMainValue，无主循环会永久阻塞（同 row_visual_test.go 的
// newRealRowTester 的处理）。picker 行不取图标，没有这个坑。
func newViewTester(t *testing.T, st *appState) *ui.Tester {
	t.Helper()
	st.icons = appui.NewIconCache()
	for _, a := range st.model.Apps {
		st.icons.Put(a.Path, nil)
	}
	tt := ui.NewTester(st.view, geometry.PanelWidth, geometry.PanelHeight)
	tt.Frame()
	tt.Frame()
	return tt
}

// TestGlobalFrameHasNoTokenAndKeepsAppsRows 在真实帧里守 Global：
// 不出现模式令牌、不出现插件行文案、apps 行照常可见。
func TestGlobalFrameHasNoTokenAndKeepsAppsRows(t *testing.T) {
	st := newWiredState(t, "Chrome", "Firefox", "VLC")
	tt := newViewTester(t, st)
	typeInto(st, "chr")
	tt.Frame()
	tt.Frame()

	if st.engineState.Mode() != engine.Global {
		t.Fatalf("前置条件：模式 = %s", st.engineState.Mode())
	}
	if tt.HasText(wantPickerPrefix) {
		t.Error("Global 帧里画出了模式令牌（规则⑤要求只在命中前缀后出令牌）")
	}
	// 插件条目的副行文案只可能来自 picker 列表。刻意不用「应用」二字做判据：
	// 输入框占位 "搜索应用" 与空态 "输入以搜索应用" 都含它，子串匹配等于没判。
	if tt.HasText(wantAppsDesc) {
		t.Errorf("Global 帧里画出了插件行副行 %q（picker 列表串进来了）", wantAppsDesc)
	}
	if tt.HasText("搜索插件") {
		t.Error("Global 帧的输入框占位文案被换成了 picker 口径")
	}
	if !tt.HasText("搜索应用") {
		t.Error("Global 帧的输入框占位文案变了")
	}
	if !tt.HasText("Chrome") {
		t.Error("Chrome 行不可见")
	}
	if !tt.HasText("↑↓ 选择  单击/↵ 启动  esc 清空/隐藏") {
		t.Error("底部提示行文案变了")
	}
}

// TestTypingPrefixClearsInputBoxInRealFrame 是最真的一条端到端：用 tt.Type
// 往生产输入框打 "#"，验证
//   - 模式切到 PluginPicker（前缀路由生效的那一刻）；
//   - **前缀不进输入框**（框被清成空串，且下一帧没被 mygo 编辑器改回 "#"）；
//   - 模式令牌 "#" 渲染出来了；
//   - picker 行可见；
//   - 紧接着打的字符只当过滤词（框里是 "b" 而不是 "#b"，规则④ + ⑤）。
func TestTypingPrefixClearsInputBoxInRealFrame(t *testing.T) {
	st := newWiredState(t, "Chrome", "Firefox", "VLC")
	tt := newViewTester(t, st)

	tt.Type(wantPickerPrefix)
	tt.Frame()
	tt.Frame()
	wantState(t, st, "真机式打 #", engine.PluginPicker, "", "", "")
	if !tt.HasText("插件列表") {
		t.Error("面包屑「插件列表」没渲染出来（规则⑤：前缀不进框，以作用域路径出现）")
	}
	if !tt.HasText(wantAppsDesc) {
		t.Errorf("picker 列表没出 %q 这一行（副行 %q 不可见）", wantAppsName, wantAppsDesc)
	}
	if !tt.HasText("搜索插件") {
		t.Error("picker 态的输入框占位文案没切换")
	}
	if tt.HasText("Chrome") {
		t.Error("picker 帧里不该再渲染 apps 行")
	}

	tt.Type("y")
	tt.Frame()
	tt.Frame()
	wantState(t, st, "接着打 y", engine.PluginPicker, "", "y", "y")
	if !tt.HasText(wantAppsDesc) {
		t.Error("过滤词 y 应仍能靠拼音键命中「应用」，行却不见了")
	}

	tt.Type("z")
	tt.Frame()
	tt.Frame()
	wantState(t, st, "接着打 yz", engine.PluginPicker, "", "yz", "yz")
	if !tt.HasText("没有匹配的插件") {
		t.Error("picker 空态文案没出现（yz 谁都命中不了）")
	}
}

// TestModeCopyBranchesAreExact 把 view.go 里「按模式现取现算的文案」四个函数的每条
// 分支钉成**直接断言**（精确字符串相等）。此前只有部分分支被真机式帧测扫到过：
//   - hintLine 的 PluginPicker 分支（「…单击/↵ 进入  esc 返回全局」）**从没被任何
//     断言覆盖**（帧测只查了 Global 那条的「清空/隐藏」）；
//   - pickerHint 的空查询分支（「没有已启用的插件」）**从没被覆盖**（:624 只查了
//     有词不中的「没有匹配的插件」）。
//
// 这两条文案或分支条件被改坏时，整套用例一个都不红。这里用最小程序的 appState
// 直接调方法补齐：四个函数只读 st.engineState（纯数据、零 mygo、不碰窗口/主循环），
// 无需起帧测。placeholder / hintLine 两条已覆盖的分支也一并钉住，防将来改串漏网。
func TestModeCopyBranchesAreExact(t *testing.T) {
	for _, tc := range []struct {
		name string
		st   *appState
		got  func(*appState) string
		want string
	}{
		{"提示行/Global", &appState{engineState: engine.State{}},
			(*appState).hintLine, "↑↓ 选择  单击/↵ 启动  esc 清空/隐藏"},
		{"提示行/PluginPicker", &appState{engineState: engine.NewState(engine.PluginPicker, "", "", "")},
			(*appState).hintLine, "↑↓ 选择  单击/↵ 进入  esc 返回全局"},
		{"占位/Global", &appState{engineState: engine.State{}},
			(*appState).placeholder, "搜索应用"},
		{"占位/PluginPicker", &appState{engineState: engine.NewState(engine.PluginPicker, "", "", "")},
			(*appState).placeholder, "搜索插件"},
		{"picker空态/无词", &appState{engineState: engine.NewState(engine.PluginPicker, "", "", "")},
			(*appState).pickerHint, "没有已启用的插件"},
		{"picker空态/有词不中", &appState{engineState: engine.NewState(engine.PluginPicker, "", "", "yz")},
			(*appState).pickerHint, "没有匹配的插件"},
	} {
		if got := tc.got(tc.st); got != tc.want {
			t.Errorf("%s：文案 = %q，想要 %q", tc.name, got, tc.want)
		}
	}
}

// TestPickerRowSingleClickEntersPlugin 守 picker 上的「单击即执行」（决策⑤在
// 插件列表上的对应物）：真实点击落在 ButtonBase 的命中链上（行内容是它的子元素），
// 一次点击 = 进该插件第一级，而不是启动某条应用。
func TestPickerRowSingleClickEntersPlugin(t *testing.T) {
	st := newWiredState(t, "Chrome", "Firefox", "VLC")
	tt := newViewTester(t, st)
	tt.Type(wantPickerPrefix)
	tt.Frame()
	tt.Frame()
	if err := tt.Click(wantAppsName); err != nil {
		t.Fatalf("点击 picker 行 %q 失败：%v", wantAppsName, err)
	}
	tt.Frame()
	tt.Frame()

	wantState(t, st, "单击 picker 行", engine.Plugin, wantAppsID, "", "")
	if !tt.HasText("Chrome") {
		t.Error("进 apps 第一级后应回到 apps 结果区（可见行没渲染出来）")
	}
	if tt.HasText(wantAppsDesc) {
		t.Error("picker 行没给 apps 让位")
	}
	// 进插件后框里是空串，此时打前缀字符也只当查询词（规则④）。
	tt.Type(wantPickerPrefix)
	tt.Frame()
	tt.Frame()
	wantState(t, st, "插件态真机打 #", engine.Plugin, wantAppsID, wantPickerPrefix, wantPickerPrefix)
}

// TestPickerArrowKeysMoveOnlyPickerSelection 锁 ↑↓ 的模式分派：picker 态里方向键
// 只动 picker 的选中行，apps 的 model.Chosen 一个字节都不动（反过来 Global 态
// 只动 model.Chosen，见 TestGlobalPathUnchangedWithoutPrefix）。
//
// pickerRows 手工铺三行 fake 插件（现注册表只有一条 apps，多行只能造）：
// 造的是**数据**，方向键的路由仍走生产 moveSelection。
func TestPickerArrowKeysMoveOnlyPickerSelection(t *testing.T) {
	st := newWiredState(t, "Chrome", "Firefox", "VLC")
	typeInto(st, "chr") // apps 先有一次正常过滤，选中行落在第 0 行
	st.engineState = engine.NewState(engine.PluginPicker, "", "", "")
	st.pickerRows = []pluginRow{{ID: "apps"}, {ID: "base64"}, {ID: "random"}}
	st.pickerChosen = 0
	st.moveSelection(1)
	if st.pickerChosen != 1 || st.model.Chosen != 0 {
		t.Errorf("↓ 之后 pickerChosen=%d model.Chosen=%d，想要 1 和 0", st.pickerChosen, st.model.Chosen)
	}
	st.moveSelection(1)
	st.moveSelection(1)
	if st.pickerChosen != 2 {
		t.Errorf("连按 ↓ 越过末尾后 pickerChosen=%d，想要钳在 2", st.pickerChosen)
	}
	for range 3 {
		st.moveSelection(-1)
	}
	if st.pickerChosen != 0 {
		t.Errorf("连按 ↑ 越过开头后 pickerChosen=%d，想要钳在 0", st.pickerChosen)
	}
	if st.model.Chosen != 0 {
		t.Errorf("picker 态的方向键不该动 apps 选中行，实得 model.Chosen=%d", st.model.Chosen)
	}

	// 空列表：只复位选中、不滚动（与 panelstate.Model.Choose 同一口径）。
	st.pickerRows = nil
	st.pickerChosen = 3
	st.moveSelection(1)
	if st.pickerChosen != 0 {
		t.Errorf("空 picker 列表 ↓ 之后 pickerChosen=%d，想要 0", st.pickerChosen)
	}
}

// TestNoSecondPrefixJudgementInMain 守铁律②在接线侧的边界：main 包的非测试源码
// 里不得出现第二个前缀判定写法，也不得硬编码 picker 的前缀字符——
// 判定只在 logic/engine，默认值只在 providers.go 的 Descriptor。
func TestNoSecondPrefixJudgementInMain(t *testing.T) {
	forbidden := []string{"strings.HasPrefix", "strings.TrimPrefix", "strings.Cut", `"#"`}
	// providers.go 是 Descriptor 注册表：默认前缀 "#" **必须**出现在那里（铁律①），
	// 所以它不在扫描范围内；engine 包自己那份守卫在 engine_test.go。
	skip := map[string]bool{"providers.go": true}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("读取包目录失败：%v", err)
	}
	checked := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || skip[name] {
			continue
		}
		raw, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatalf("读取 %s 失败：%v", name, err)
		}
		checked++
		for lineNo, line := range strings.Split(string(raw), "\n") {
			code := stripLineComment(line)
			if code == "" {
				continue
			}
			for _, f := range forbidden {
				if strings.Contains(code, f) {
					t.Errorf("%s:%d 出现被禁止的写法 %q（判定只在 engine，前缀默认值只在 Descriptor）",
						name, lineNo+1, f)
				}
			}
		}
	}
	if checked < 4 {
		t.Errorf("只检查了 %d 个源文件，扫描可能失效", checked)
	}
}

// stripLineComment 去掉一行的行尾 // 注释（本包源码没有字符串内含 // 的情况）。
func stripLineComment(line string) string {
	if i := strings.Index(line, "//"); i >= 0 {
		line = line[:i]
	}
	return strings.TrimSpace(line)
}

// TestAppsFreqKeyPrefixConsistent 钉住 apps 频率键前缀三处一致：search.boostOf 用
// appsmodel.ProviderID 读、execLaunch 用 appsmodel.ProviderID 记、Global 聚合里 apps 组用
// appsProviderID / rankPlugins 用 providerAppsID。任一处漂成别的串，启动记的频率就加不到
// 排序上（apps-only 和聚合两条路都会「记了不生效」），本用例先红。
func TestAppsFreqKeyPrefixConsistent(t *testing.T) {
	if appsmodel.ProviderID != appsProviderID {
		t.Fatalf("apps 频率键前缀漂移：appsmodel.ProviderID=%q ≠ appsProviderID=%q",
			appsmodel.ProviderID, appsProviderID)
	}
	if appsmodel.ProviderID != providerAppsID {
		t.Fatalf("apps 频率键前缀漂移：appsmodel.ProviderID=%q ≠ providerAppsID=%q",
			appsmodel.ProviderID, providerAppsID)
	}
}

// TestEnterPluginRoutesToAwakeFirstLevel 锁「程序化直达插件」这条路由——托盘 awake
// 状态条目点击（openAwake）依赖它，对照生产 main.rs `open_awake → activate_plugin
// ("awake")`。enterPlugin 应把模式原子切到 awake 第一级、providerActive 为真、并即时
// 刷出 queryLevel1 空输入的产物（未激活态 → 仅系统唤醒 / 连屏幕一起保持 两条方向候选）。
// id 写死 "awake"（纪律②：不从 awake.ID 反推，Descriptor 改坏时这里必须红）。
func TestEnterPluginRoutesToAwakeFirstLevel(t *testing.T) {
	st := newWiredState(t)
	st.pluginReg = builtinProviders()
	st.enterPlugin("awake")
	wantState(t, st, "直达 awake", engine.Plugin, "awake", "", "")
	if !st.providerActive() {
		t.Fatal("enterPlugin(awake) 后 providerActive 应为真（awake 已注册进 pluginReg）")
	}
	if len(st.provRows) != 2 {
		t.Fatalf("awake 一级应有 2 条亮屏方向候选，实得 %d 条", len(st.provRows))
	}
}

// TestRefreshRowRerollsSameInput 锁「重新生成」行的执行语义（对照生产 random.rs:18-19
// 「让 app 用同一输入再掷一批」+ Action::Refresh 的 should_reset_input=false）：在 random
// 的十六进制二级点首条「重新生成」，值行必须**真的重掷**（32 位 hex，值域 16^32，两次
// 全同的概率可忽略），且面板保留、模式/输入不变。旧实现在这里把 Refresh 当未接入平台动作
// no-op——若回归成 no-op，值行会逐字不变，本用例即在 before==after 上红。
func TestRefreshRowRerollsSameInput(t *testing.T) {
	st := newWiredState(t)
	st.pluginReg = builtinProviders()
	st.enterPlugin("random") // 一级：4 个类型方向（分数降序，[3]=十六进制）
	if len(st.provRows) != 4 {
		t.Fatalf("random 一级应有 4 个类型方向，实得 %d", len(st.provRows))
	}
	st.runProviderIntent(st.provRows[3].Match, "random") // IntentEnterSubMode → 进 hex 二级
	if st.engineState.Mode() != engine.SubMode {
		t.Fatalf("进 hex 二级后应 SubMode，实得 %s", st.engineState.Mode())
	}
	// 首条固定「重新生成」（Refresh），其后是 count 条 hex 值。
	if len(st.provRows) < 2 || st.provRows[0].Action.Kind != providers.ActionRefresh {
		t.Fatalf("hex 二级首条应为「重新生成」(Refresh)，实得 %d 条 / 首条 Kind=%v", len(st.provRows), st.provRows[0].Action.Kind)
	}
	valueTitles := func() string {
		b := make([]string, 0, len(st.provRows))
		for _, r := range st.provRows[1:] {
			b = append(b, r.Title)
		}
		return strings.Join(b, "|")
	}
	before := valueTitles()

	st.runProviderIntent(st.provRows[0].Match, "random") // 点「重新生成」→ 同输入重掷

	if after := valueTitles(); after == "" || after == before {
		t.Errorf("点「重新生成」后值行应重掷（32 位 hex 几乎不可能与 %q 相同），实得 %q", before, after)
	}
	// 语义约束：Refresh 不隐藏面板、不改模式与输入（对照 should_reset_input=false）。
	if st.engineState.Mode() != engine.SubMode || st.engineState.PluginID() != "random" {
		t.Errorf("重掷不应改模式，实得 %+v", st.engineState)
	}
	if !st.providerActive() {
		t.Error("重掷后面板应保留（providerActive 仍为真）")
	}
}
