// providers_test.go：装配层**真实注册表**这条接缝的 headless 集成测试。
//
// 要锁的东西（§7.3.4 三条铁律在 main 侧的落地效果）：
//
//	providers.go 的 builtinDescriptors() → config.Normalize（C1–C7）→
//	Configs.BuildTable() → engine.Table
//
// internal/logic/config 自己的用例（normalize_test.go / table_test.go）按分包纪律
// 只能喂 **fake 注册表**，engine_wiring_test.go 的 TestBuildPrefixTableShape 只抽了
// 形状的几个字段；「真实的 apps + picker 两条 Descriptor 归一后到底是什么、前缀表
// 到底是什么」这一截一直没有逐字段的绝对值兜底。本文件补的就是这一截：
//
//  1. 期望值**一律绝对**（"#" / "__picker" / 权重 1.2 / 别名 app,yy 全写死），
//     绝不从 Descriptor 反推——Descriptor 被改坏时这里必须红（与
//     engine_wiring_test.go 的头注同一条纪律）；
//  2. 只跑装配层纯函数：不建窗口、不碰 mygo 的 onMain/onMainValue
//     （那条路无主循环会永久阻塞，见 engine_wiring_test.go / apps_test.go 头注）；
//  3. 报错支（Normalize 失败）在 headless 侧**不可达**：buildPrefixTable 不接参数，
//     注册表是函数内的装配期常量，只有改坏 builtinDescriptors 本身才能走进去。
//     本文件不造假调用，而是把「报错时返回什么形状、零值灌进装配层后退化成什么」
//     钉死（TestNormalizeFailureDegradesToPureGlobal：用真实注册表的**副本**走
//     同样的两步），报错支的真实触发用变异实验验（apps 前缀改成与 picker 撞车 →
//     上面两条绝对值断言立刻红）。
package app

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode"

	"github.com/zyition/voke/poc/voke-go/internal/logic/config"
	"github.com/zyition/voke/poc/voke-go/internal/logic/engine"
)

// 内置注册表的绝对期望（wantPickerPrefix / wantAppsID / wantAppsName / wantAppsDesc
// 复用 engine_wiring_test.go 里已写死的那几个字面量，同包同义，不重复定义）。
const (
	wantPickerID    = "__picker" // 必须与 engine.PickerPluginID 逐字相同（命中后 engine 据此进 PluginPicker）
	wantPickerName  = "列出所有插件"
	wantPickerDesc  = "按名称 / 别名过滤插件"
	wantAppsAliases = "app,yy"
)

// TestApplyProviderConfigs 验归一配置真的下发到实例：给 random 写 count=12/weight=2、
// base64 关 enabled，Normalize 后 applyProviderConfigs → reg 里对应 provider 的 Config()
// 反映这些值（而非各自 New() 的内置默认）。apps/picker 不在 reg → 安全跳过。
func TestApplyProviderConfigs(t *testing.T) {
	cnt := 12
	w := 2.0
	en := false
	ovs := []config.UserOverride{
		{ID: "random", Count: &cnt, Weight: &w},
		{ID: "base64", Enabled: &en},
	}
	cfg, err := config.Normalize(builtinDescriptors(), ovs)
	if err != nil {
		t.Fatalf("Normalize 失败：%v", err)
	}
	reg := builtinProviders()
	applyProviderConfigs(cfg, reg)

	r, ok := reg.Get("random")
	if !ok {
		t.Fatal("reg 里应有 random")
	}
	if rc := r.Config(); rc.Count != 12 || rc.Weight != 2.0 {
		t.Errorf("random 应被下发 count=12/weight=2：%+v", rc)
	}
	b, _ := reg.Get("base64")
	if bc := b.Config(); bc.Enabled {
		t.Errorf("base64 应被下发 enabled=false：%+v", bc)
	}
}

// TestBuildPrefixTableHappyPath 直测生产入口 buildPrefixTable()：
// 归一后的 ProviderConfig **全量字段** + engine.Table **全量条目** + 由这张真实表
// 驱动的 engine 判定（前缀命中 / 令牌）全按绝对值断言。
func TestBuildPrefixTableHappyPath(t *testing.T) {
	cfg, table, err := buildPrefixTable()
	if err != nil {
		t.Fatalf("buildPrefixTable 意外报错（内置注册表是装配期常量，报错就等于 Descriptor 被改坏）：%v", err)
	}
	// 无用户覆盖（PoC 不接 TOML，Normalize 第二参数是 nil）⇒ 一条警告都不该有：
	// 「绝不静默」的另一半是「不该有无中生有的警告」。
	if len(cfg.Warnings) != 0 {
		t.Errorf("内置注册表归一不该产生警告，实得 %q", cfg.Warnings)
	}

	// ── 归一层：两条 ProviderConfig，字段逐个钉死（注册顺序 = 优先级顺序）──
	// 条数先钉死：少了条目再往下索引就是 panic，会把整个测试二进制一起带走
	// （本轮实测复现：删掉 picker 那条 → index out of range → 别人的红也看不见了）。
	wantProviders := []config.ProviderConfig{
		{
			ID: "apps", Name: "应用", Description: "搜索并启动开始菜单应用",
			Aliases: []string{"app", "yy"},
			Enabled: true, IsGlobal: true,
			Prefix: "", Activation: engine.Direct, Weight: 1.2, Count: 5,
		},
		{
			ID: wantPickerID, Name: wantPickerName, Description: wantPickerDesc,
			Aliases: nil,
			Enabled: true, IsGlobal: false,
			Prefix: "#", Activation: engine.Direct, Weight: 1.0, Count: 5,
		},
		{
			ID: "base64", Name: "Base64", Description: "base64 编解码（加密 / 解密）",
			Aliases: []string{"b", "b64"},
			Enabled: true, IsGlobal: false,
			Prefix: "@b", Activation: engine.Indirect, Weight: 1.0, Count: 5,
		},
		{
			ID: "uuid", Name: "UUID", Description: "生成 UUID v4（可指定个数）",
			Aliases: []string{"u"},
			Enabled: true, IsGlobal: false,
			Prefix: "@u", Activation: engine.Indirect, Weight: 1.0, Count: 5,
		},
		{
			ID: "timestamp", Name: "时间戳", Description: "时间戳 ⇄ 可读时间",
			Aliases: []string{"t", "sj", "ts", "time"},
			Enabled: true, IsGlobal: false,
			Prefix: "@t", Activation: engine.Indirect, Weight: 1.0, Count: 5,
		},
		{
			ID: "random", Name: "随机值", Description: "随机整数 / 随机字符串",
			Aliases: []string{"r", "sj", "rand"},
			Enabled: true, IsGlobal: false,
			Prefix: "@r", Activation: engine.Indirect, Weight: 1.0, Count: 5,
		},
		{
			ID: "clipboard", Name: "剪贴板", Description: "剪贴板历史（文本 / 富文本 / 图片）",
			Aliases: []string{"clip", "nbt", "jqb"},
			Enabled: true, IsGlobal: false,
			Prefix: "@c", Activation: engine.Indirect, Weight: 1.0, Count: 5,
		},
		{
			ID: "calculator", Name: "计算器", Description: "算术表达式求值，如 1+1",
			Aliases: []string{"calc", "jsq", "c"},
			Enabled: true, IsGlobal: false,
			Prefix: "=", Activation: engine.Direct, Weight: 1.0, Count: 5,
		},
		{
			ID: "awake", Name: "保持唤醒", Description: "阻止系统休眠（可选连屏幕一起）",
			Aliases: []string{"awake", "a"},
			Enabled: true, IsGlobal: true,
			Prefix: "", Activation: engine.Direct, Weight: 1.0, Count: 5,
		},
		{
			ID: "windows", Name: "窗口", Description: "切换到已打开的窗口",
			Aliases: []string{"win", "ck"},
			Enabled: true, IsGlobal: true,
			Prefix: "", Activation: engine.Direct, Weight: 1.0, Count: 5,
		},
		{
			ID: "terminal", Name: "终端配置", Description: "用 Windows Terminal 打开某个配置（profile）",
			Aliases: []string{"wt", "terminal"},
			Enabled: true, IsGlobal: true,
			Prefix: "", Activation: engine.Direct, Weight: 1.0, Count: 5,
		},
		{
			ID: "workspaces", Name: "工作区", Description: "Zed / VSCode 最近打开的工作区，用记录它的编辑器打开",
			Aliases: []string{"ws", "gzq"},
			Enabled: true, IsGlobal: true,
			Prefix: "", Activation: engine.Direct, Weight: 1.0, Count: 5,
		},
	}
	if len(cfg.Providers) != len(wantProviders) {
		t.Fatalf("归一后的插件条数 = %d，想要 %d（实得：%+v）", len(cfg.Providers), len(wantProviders), cfg.Providers)
	}
	if !reflect.DeepEqual(cfg.Providers, wantProviders) {
		t.Errorf("归一后的 ProviderConfig 与 Descriptor 默认值不一致\n得到：%+v\n想要：%+v", cfg.Providers, wantProviders)
	}
	if got := strings.Join(cfg.IDs(), ","); got != "apps,"+wantPickerID+",base64,uuid,timestamp,random,clipboard,calculator,awake,windows,terminal,workspaces" {
		t.Errorf("注册 id 序列（= 表优先级 = picker 列表顺序）= %q，想要 \"apps,%s,base64,uuid,timestamp,random,clipboard,calculator,awake,windows,terminal,workspaces\"", got, wantPickerID)
	}
	if got := strings.Join(cfg.Providers[0].Aliases, ","); got != wantAppsAliases {
		t.Errorf("apps 别名 = %q，想要 %q（别名走 bonus 通道，改掉会让 yy/app 命中失效）", got, wantAppsAliases)
	}
	if len(cfg.Enabled()) != 12 {
		t.Errorf("启用插件数 = %d，想要 12（apps/picker/base64/uuid/timestamp/random/clipboard/calculator/awake/windows/terminal/workspaces）", len(cfg.Enabled()))
	}

	// 三处「哪一处坏了要命」单独再钉一遍（DeepEqual 只说「不一样」）：
	// ① apps 无前缀 + is_global = Global 零回归的结构依据（§7.3.4）；
	apps := cfg.Providers[0]
	if apps.Prefix != "" || !apps.IsGlobal {
		t.Errorf("apps 归一结果 = {Prefix:%q IsGlobal:%v}，想要 {Prefix:\"\" IsGlobal:true}"+
			"：apps 取空前缀 ⇒ 裸查询词就是 Global 链路", apps.Prefix, apps.IsGlobal)
	}
	// ② picker 那条的前缀字符（铁律①：值只能来自 Descriptor，代码里没有第二处 '#'）；
	// ③ picker 的 id 必须仍是 engine 的哨兵 id，否则 engine.Scan 命中后不会进 PluginPicker。
	picker := cfg.Providers[1]
	if picker.Prefix != wantPickerPrefix || picker.Activation != engine.Direct {
		t.Errorf("picker 条目 = {Prefix:%q Activation:%v}，想要 {Prefix:\"%s\" Activation:Direct}",
			picker.Prefix, picker.Activation, wantPickerPrefix)
	}
	if picker.ID != engine.PickerPluginID {
		t.Errorf("picker 的 Descriptor.ID = %q，与 engine.PickerPluginID = %q 脱钩：命中它不会进 PluginPicker",
			picker.ID, engine.PickerPluginID)
	}

	// ── 表层：BuildTable 是唯一来源（铁律③），空前缀条目不进表 ──
	wantTable := engine.Table{
		{Prefix: "#", PluginID: wantPickerID, Activation: engine.Direct},
		{Prefix: "@b", PluginID: "base64", Activation: engine.Indirect},
		{Prefix: "@u", PluginID: "uuid", Activation: engine.Indirect},
		{Prefix: "@t", PluginID: "timestamp", Activation: engine.Indirect},
		{Prefix: "@r", PluginID: "random", Activation: engine.Indirect},
		{Prefix: "@c", PluginID: "clipboard", Activation: engine.Indirect},
		{Prefix: "=", PluginID: "calculator", Activation: engine.Direct},
	}
	if !reflect.DeepEqual(table, wantTable) {
		t.Errorf("engine.Table 全量内容不一致\n得到：%+v\n想要：%+v", table, wantTable)
	}
	if len(table) != 7 {
		t.Errorf("前缀表条目数 = %d，想要 7（picker/base64/uuid/timestamp/random/clipboard/calculator；apps 无前缀不进表）", len(table))
	}
	var prefixes []string
	for _, e := range table {
		prefixes = append(prefixes, e.Prefix)
		if e.PluginID == wantAppsID {
			t.Errorf("前缀表里出现了 apps 条目 {Prefix:%q}：apps 必须无前缀，否则裸查询词会被劫持（Global 回归）", e.Prefix)
		}
	}
	if got := strings.Join(prefixes, ","); got != "#,@b,@u,@t,@r,@c,=" {
		t.Errorf("前缀集合（表序）= %q，想要 \"#,@b,@u,@t,@r,@c,=\"", got)
	}

	// ── 这张真实表喂给 engine 的判定结果（装配产物的可用性闭环）──
	// 全等才切（决策⑥）：'#' 命中；带尾随空格 / 后面还有字符的都留在 Global，
	// 且 Query 原文保留（含首尾空格）。命中那条的 Query 必须是空——
	// 前缀不进输入框（§7.3.3 规则 5，Query 永不含前缀）。
	for _, tc := range []struct {
		input     string
		wantMode  engine.Mode
		wantID    string
		wantQuery string
	}{
		{"#", engine.PluginPicker, "", ""},
		{"# ", engine.Global, "", "# "},
		{"#chr", engine.Global, "", "#chr"},
		{"chr", engine.Global, "", "chr"},
		{"=", engine.Plugin, "calculator", ""},
		{"=1+1", engine.Global, "", "=1+1"},
		{"", engine.Global, "", ""},
	} {
		got := engine.Scan(table, engine.State{}, tc.input)
		if got.Mode() != tc.wantMode || got.PluginID() != tc.wantID || got.Query() != tc.wantQuery {
			t.Errorf("真实表 Scan(%q) = {Mode:%v PluginID:%q Query:%q}，想要 {Mode:%v PluginID:%q Query:%q}",
				tc.input, got.Mode(), got.PluginID(), got.Query(), tc.wantMode, tc.wantID, tc.wantQuery)
		}
	}
	// 模式令牌的唯一来源是这张表（app/ui 不得自己再算一遍）。
	if got := prefixFor(engine.NewState(engine.PluginPicker, "", "", ""), table); got != "#" {
		t.Errorf("PluginPicker 的令牌 = %q，想要 \"#\"（令牌由 Descriptor 的前缀经表带出）", got)
	}
	if got := prefixFor(engine.State{}, table); got != "" {
		t.Errorf("Global 的令牌 = %q，想要 \"\"（裸查询词不该显示任何前缀）", got)
	}
	// apps 无前缀 ⇒ 就算被 picker 选定进了 Plugin 态，也拿不到任何令牌
	//（§10.2 待决策 10① 的既成事实：这里钉住「实现确实如此」，文案另议）。
	if got := prefixFor(engine.SelectPlugin(engine.State{}, wantAppsID), table); got != "" {
		t.Errorf("apps 的 Plugin 态令牌 = %q，想要 \"\"（apps 无前缀）", got)
	}
}

// TestBuiltinDescriptorsInvariants 在**归一之前**先钉注册表自身的不变式：
// config 的 C1–C7 只在 Normalize 里跑一次，装配层这些形状要求（id 非空/唯一、
// 前缀不撞车、不占保留键、Global 通道唯一）必须在 Descriptor 层面就成立。
func TestBuiltinDescriptorsInvariants(t *testing.T) {
	ds := builtinDescriptors()

	// 至少含 apps + picker；新增带前缀的 Descriptor 必须在 builtinProviders 里有实现
	//（见函数末尾不变式），不得凭空注册一个没实现的插件。
	if len(ds) < 2 {
		t.Errorf("内置注册表条目数 = %d，至少应含 apps + picker 两条", len(ds))
	}
	// 注册顺序 = 前缀表优先级 = picker 列表顺序（config.BuildTable 不排序）；前两条恒为 apps、picker。
	if len(ds) >= 2 && (ds[0].ID != wantAppsID || ds[1].ID != wantPickerID) {
		t.Errorf("注册顺序前两条 = %q，想要以 [\"%s\", \"%s\"] 开头", strings.Join(idsOf(ds), ","), wantAppsID, wantPickerID)
	}

	seenID := map[string]bool{}
	seenPrefix := map[string]bool{}
	var prefixed []string // 带前缀条目的前缀，按注册顺序
	var globalCount int
	for i, d := range ds {
		if d.ID == "" {
			t.Errorf("第 %d 条 Descriptor 的 ID 是空串（Normalize 的注册表自检会直接报错）", i)
			continue
		}
		if seenID[d.ID] {
			t.Errorf("Descriptor id %q 重复（ErrDuplicateDescriptor：装配期 bug）", d.ID)
		}
		seenID[d.ID] = true

		if d.Name == "" || d.Description == "" {
			t.Errorf("Descriptor %q 的 Name/Description 有空串：%q / %q（picker 行要渲染两行文本）",
				d.ID, d.Name, d.Description)
		}
		if !d.DefaultEnabled {
			t.Errorf("Descriptor %q 的 DefaultEnabled = false：禁用插件不进前缀表（%q 就路由不到了）", d.ID, d.DefaultPrefix)
		}
		if d.DefaultActivation != engine.Direct && d.DefaultActivation != engine.Indirect {
			t.Errorf("Descriptor %q 的 DefaultActivation = %v，不是 Direct/Indirect 两值之一",
				d.ID, d.DefaultActivation)
		}
		for _, a := range d.Aliases {
			if a == "" || strings.ToLower(a) != a || strings.ContainsFunc(a, func(r rune) bool { return r > unicode.MaxASCII }) {
				t.Errorf("Descriptor %q 的别名 %q 违反约定（非空 / 全小写 / ASCII）", d.ID, a)
			}
		}

		if d.DefaultIsGlobal {
			globalCount++
		}
		if d.DefaultPrefix == "" {
			continue // 空前缀合法，且不参与 C2 / C4 判定
		}
		prefixed = append(prefixed, d.DefaultPrefix)
		if seenPrefix[d.DefaultPrefix] {
			t.Errorf("前缀 %q 被多条 Descriptor 声明（C2 会报错，buildPrefixTable 直接退化成空表）", d.DefaultPrefix)
		}
		seenPrefix[d.DefaultPrefix] = true
		// C4 的形状版：前缀首字符不得是字母或数字（字母抢 Camera/Chrome，
		// 数字打头是全局搜索的常态输入，§7.3.6 b「按符号键分配」）。
		first := []rune(d.DefaultPrefix)[0]
		if unicode.IsLetter(first) || (first >= '0' && first <= '9') {
			t.Errorf("Descriptor %q 的前缀 %q 首字符是保留键（字母/数字），Normalize 的 C4 会拒（装配失败）",
				d.ID, d.DefaultPrefix)
		}
	}

	if got := strings.Join(prefixed, ","); got != "#,@b,@u,@t,@r,@c,=" {
		t.Errorf("带前缀的 Descriptor 的前缀序列 = %q，想要 \"#,@b,@u,@t,@r=\"（picker #、base64 @b、uuid @u、timestamp @t、random @r、calculator =）", got)
	}

	// 不变式（升级自旧「只 apps+picker」）：除 picker 外，每条「带前缀」的 Descriptor
	// 必须在 builtinProviders 有实现——否则命中其前缀只会落到「未接入」占位，等于
	// 凭空注册一个没实现的插件。加 provider 却不进 registry、或进 registry 却不注册
	// Descriptor，都会在这里变红。
	reg := builtinProviders()
	for _, d := range ds {
		if d.DefaultPrefix == "" || d.ID == engine.PickerPluginID {
			continue
		}
		if _, ok := reg.Get(d.ID); !ok {
			t.Errorf("Descriptor %q 带前缀 %q 却没在 builtinProviders 注册实现：命中它会落到「未接入」占位",
				d.ID, d.DefaultPrefix)
		}
	}

	// apps 的 §7.3 / §10.2 语义：取**空前缀** ⇒ 裸查询词的 Global 链路就是它。
	apps := descriptorByID(t, ds, wantAppsID)
	if apps.DefaultPrefix != "" || !apps.DefaultIsGlobal || !apps.DefaultEnabled {
		t.Errorf("apps Descriptor = {DefaultPrefix:%q DefaultIsGlobal:%v DefaultEnabled:%v}，"+
			"想要 {\"\" true true}：apps 一旦有了前缀，那个字符就不再是合法搜索输入（Global 回归）",
			apps.DefaultPrefix, apps.DefaultIsGlobal, apps.DefaultEnabled)
	}
	// Global 通道口径：现有**五条** is_global —— apps（空前缀，裸查询词主链路）、
	// awake / windows / terminal / workspaces（均空前缀、靠名字/打字触发）。
	// 复核结论：接线层的 Global 渲染当前仍**只走 apps**（providerActive 仅认 Plugin/
	// SubMode），其余四条的 Global 直接触发要等 step8b 多 provider 聚合链路接上才生效，
	// 现在它们经 `#` picker 可达。故 TestPickerEnterThenAppsSearchEqualsGlobal 的
	// 「Global 结果集 ≡ apps 单插件」零回归断言在此阶段仍成立。钉死为恰好 5，再加全局
	// 插件必须重新复核这条口径。
	if globalCount != 5 {
		t.Errorf("DefaultIsGlobal = true 的 Descriptor 有 %d 条，想要恰好 5（apps + awake + windows + terminal + workspaces）："+
			"多一条全局插件都要重新复核「Global 渲染是否仍只走 apps、聚合是否已接」这条口径",
			globalCount)
	}
	// picker 是「表里的普通一条」（决策②）：id 就是 engine 的哨兵、前缀由 Descriptor
	// 带进来、自己不参与全局搜索。
	picker := descriptorByID(t, ds, engine.PickerPluginID)
	if picker.ID != wantPickerID || picker.DefaultPrefix != wantPickerPrefix ||
		picker.DefaultActivation != engine.Direct || picker.DefaultIsGlobal {
		t.Errorf("picker Descriptor = {ID:%q DefaultPrefix:%q DefaultActivation:%v DefaultIsGlobal:%v}，"+
			"想要 {%q \"#\" Direct false}",
			picker.ID, picker.DefaultPrefix, picker.DefaultActivation, picker.DefaultIsGlobal, wantPickerID)
	}
}

// TestBuiltinDescriptorsReturnFreshSlice 钉「注册表是装配期常量、每次返回新切片」：
// 调用方（含测试自己造的负例）改坏返回值也不得污染后续装配。
func TestBuiltinDescriptorsReturnFreshSlice(t *testing.T) {
	ds := builtinDescriptors()
	// 前置形状（真实形状由 Invariants / HappyPath 两条钉）：这里要索引 ds[0]，
	// 条数不对就干净地停下，别以 panic 的形式把整个测试二进制带走。
	if len(ds) != 12 || len(ds[0].Aliases) != 2 {
		t.Fatalf("注册表形状与期望不符（%d 条：%s），本用例的写入点无法定位", len(ds), strings.Join(idsOf(ds), ","))
	}
	ds[0].ID = "被改坏的 id"
	ds[0].Name = "被改坏的名字"
	ds[0].DefaultPrefix = "%"
	ds[0].Aliases[0] = "被改坏的别名"

	again := builtinDescriptors()
	if len(again) != 12 || len(again[0].Aliases) != 2 {
		t.Fatalf("第二次取到的注册表形状不对（%d 条：%s）", len(again), strings.Join(idsOf(again), ","))
	}
	if again[0].ID != wantAppsID || again[0].Name != wantAppsName ||
		again[0].DefaultPrefix != "" || again[0].Aliases[0] != "app" {
		t.Errorf("第二次取注册表被上一步的写入污染：%+v", again)
	}
	cfg, table, err := buildPrefixTable()
	if err != nil {
		t.Fatalf("污染后 buildPrefixTable 报错（注册表被写脏了）：%v", err)
	}
	if len(table) != 7 || len(cfg.Providers) != 12 {
		t.Fatalf("污染后装配产物条数不对：table=%+v providers=%+v", table, cfg.Providers)
	}
	if table[0].Prefix != "#" || cfg.Providers[0].Name != wantAppsName {
		t.Errorf("污染后装配产物变了：table=%+v providers=%+v", table, cfg.Providers)
	}
}

// TestNormalizeFailureDegradesToPureGlobal 钉「报错支」的契约形状。
//
// **可达性如实说明**：buildPrefixTable 不接参数、Normalize 的第二参数写死 nil，
// 所以 error != nil 那一支从 headless 侧调不到（不造假调用、也不为它改产品码）。
// 这里做的是另一件事：拿**从真实注册表复制来的坏 Descriptor** 走 buildPrefixTable
// 函数体里同样的两步（config.Normalize → Configs.BuildTable），确认
//   - 报错形态（哪个 C 命中、消息里点名了谁）；
//   - 报错时返回的 Configs 是零值，而零值灌进装配层后确实等价「纯 Global」——
//     这正是 panel.go ready() 里 log 完继续往下跑时所处的状态
//     （appState 侧的端到端版本由 engine_wiring_test.go 的
//     TestEmptyTableDegradesToPureGlobal 守着，本用例不重复建 appState）。
func TestNormalizeFailureDegradesToPureGlobal(t *testing.T) {
	// C2：给 apps 也配上**与 picker 相同**的前缀。撞车目标取注册表里 picker 那条
	// 的真实前缀（它是「注入值」不是「期望值」；那个字符本身是 "#" 这件事
	// 已由 TestBuildPrefixTableHappyPath 用绝对值钉住，所以这里不丢覆盖，
	// 只让本用例专注守 C2 这一件事）。
	clash := copyDescriptors(t)
	clashPrefix := descriptorByID(t, clash, engine.PickerPluginID).DefaultPrefix
	if clashPrefix == "" {
		t.Fatal("picker 这条 Descriptor 现在没有前缀（真实前缀被改成空串了？）：" +
			"本用例注入不了撞车目标，去看 TestBuildPrefixTableHappyPath 的那条红")
	}
	if !setDescriptorPrefix(clash, wantAppsID, clashPrefix) {
		t.Fatalf("注册表里没有 id 为 %q 的 Descriptor，负例形态失效", wantAppsID)
	}
	cfg, err := config.Normalize(clash, nil)
	if err == nil {
		t.Fatalf("apps 与 picker 共用前缀 %q 却没报错：C2 失效", clashPrefix)
	}
	if !errors.Is(err, config.ErrPrefixConflict) {
		t.Errorf("撞车前缀报的错不是 ErrPrefixConflict：%v", err)
	}
	for _, want := range []string{wantAppsID, wantPickerID, "触发串"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("C2 消息没有点名撞车的两条/触发串：%v（想要含 %q）", err, want)
		}
	}
	assertZeroConfigs(t, cfg, "C2 前缀撞车")

	// C4：前缀首字符是字母（`a` 会抢 Camera / Chrome）。
	reserved := copyDescriptors(t)
	if !setDescriptorPrefix(reserved, wantAppsID, "a") {
		t.Fatalf("注册表里没有 id 为 %q 的 Descriptor，负例形态失效", wantAppsID)
	}
	cfg2, err2 := config.Normalize(reserved, nil)
	if err2 == nil || !errors.Is(err2, config.ErrReservedPrefix) {
		t.Errorf("apps 用字母前缀 \"a\" 没撞上 C4：err=%v", err2)
	}
	if want := `apps 的前缀 "a" 是字母键`; err2 != nil && !strings.Contains(err2.Error(), want) {
		t.Errorf("C4 消息没有说清撞了什么：%v（想要含 %q）", err2, want)
	}
	assertZeroConfigs(t, cfg2, "C4 保留键")

	// C1（未知 id）在装配层**结构上不可达**：PoC 不接 TOML，overrides 恒为 nil。
	// 这里不硬造用例，只记录口径：将来接上配置读盘，nil 换成解码出来的
	// []config.UserOverride，C1 才会第一次真正生效（见 providers.go 头注）。

	// 零值 Configs ⇒ 空前缀表 ⇒ engine 永远只返回 Global。
	degraded := config.Configs{}
	table := degraded.BuildTable()
	if len(table) != 0 {
		t.Errorf("零值 Configs 的表非空：%+v", table)
	}
	for _, input := range []string{"", "#", "# ", "#chr", "chr"} {
		got := engine.Scan(table, engine.State{}, input)
		if got.Mode() != engine.Global || got.Query() != input {
			t.Errorf("降级表 Scan(%q) = {Mode:%v Query:%q}，想要 {Global 原文}（前缀路由必须整体失效，回到接线前的纯 Global）",
				input, got.Mode(), got.Query())
		}
	}
	if got := prefixFor(engine.NewState(engine.PluginPicker, "", "", ""), table); got != "" {
		t.Errorf("降级态的令牌 = %q，想要 \"\"（表里查不到条目就不该显示任何前缀）", got)
	}
	// 降级态的 picker 列表是空的（apps 不在归一结果里 ⇒ CandidateFor 全 miss），
	// 且一路不 panic：ready() 拿到零值也照样能把面板开出来。
	if rows := rankPlugins(degraded, "app"); len(rows) != 0 {
		t.Errorf("降级态 picker 列表 = %+v，想要空（一条候选都不该有）", rows)
	}
	if _, ok := degraded.Lookup(wantAppsID); ok {
		t.Error("零值 Configs 的 Lookup 命中了 apps")
	}
	if got := strings.Join(degraded.IDs(), ","); got != "" {
		t.Errorf("零值 Configs 的 IDs = %q，想要空串", got)
	}
}

// assertZeroConfigs 断言 Normalize 报错时返回的是**零值** Configs：
// 调用方不许「带着坏配置继续跑」，警告也必须是空的（panel.go 落日志的循环才会是 no-op）。
func assertZeroConfigs(t *testing.T, cfg config.Configs, label string) {
	t.Helper()
	if len(cfg.Providers) != 0 {
		t.Errorf("%s 报错后仍返回了 %d 条 ProviderConfig：%+v", label, len(cfg.Providers), cfg.Providers)
	}
	if len(cfg.Warnings) != 0 {
		t.Errorf("%s 报错后仍带着警告 %q（报错与警告不能混着返回）", label, cfg.Warnings)
	}
}

// copyDescriptors 复制一份真实注册表（负例只改副本，绝不动 builtinDescriptors 的产物）。
func copyDescriptors(t *testing.T) []config.Descriptor {
	t.Helper()
	src := builtinDescriptors()
	out := make([]config.Descriptor, len(src))
	copy(out, src)
	return out
}

// setDescriptorPrefix 把 id 为 pluginID 的那条的 DefaultPrefix 改掉，返回是否命中。
func setDescriptorPrefix(ds []config.Descriptor, pluginID, prefix string) bool {
	for i := range ds {
		if ds[i].ID == pluginID {
			ds[i].DefaultPrefix = prefix
			return true
		}
	}
	return false
}

// descriptorByID 按 id 取一条 Descriptor（取不到直接判失败，后面不再做 ok 分支）。
func descriptorByID(t *testing.T, ds []config.Descriptor, id string) config.Descriptor {
	t.Helper()
	for _, d := range ds {
		if d.ID == id {
			return d
		}
	}
	t.Fatalf("注册表里没有 id 为 %q 的 Descriptor（实得 %q）", id, strings.Join(idsOf(ds), ","))
	return config.Descriptor{}
}

// idsOf 取 id 序列，只给失败消息用。
func idsOf(ds []config.Descriptor) []string {
	out := make([]string, 0, len(ds))
	for _, d := range ds {
		out = append(out, d.ID)
	}
	return out
}
