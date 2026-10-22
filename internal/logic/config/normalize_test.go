// §7.3.4 归一化校验 C1–C7 的单测：每条都有**成功**与**失败**两侧，
// 失败侧断言**确切消息文本**（V-7 的验收标准是「启动即报中文错误、列出冲突项」，
// 只断言 err != nil 等于没测）。期望值一律写绝对值。

package config

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"voke/internal/logic/engine"
)

func TestNormalizeDescriptorDefaultsWinWhenUserWritesNothing(t *testing.T) {
	c := mustNormalize(t, fakeRegistry(), nil)

	wantWarnings(t, c) // 没有任何用户输入 → 不该冒出一条警告

	// 整表逐字段比对（顺序也是断言的一部分：BuildTable 的优先级靠注册顺序）。
	want := []ProviderConfig{
		{ID: "apps", Name: "应用", Description: "搜索并启动开始菜单应用",
			Aliases: []string{"app", "yy"}, Enabled: true, IsGlobal: true,
			Prefix: "", Activation: engine.Direct, Weight: 1.2, Count: 5},
		{ID: "windows", Name: "窗口", Description: "切换到已打开的窗口",
			Aliases: []string{"win", "ck"}, Enabled: true, IsGlobal: true,
			Prefix: "", Activation: engine.Direct, Weight: 1.0, Count: 5},
		{ID: "calculator", Name: "计算器", Description: "算术表达式求值，如 1+1",
			Aliases: []string{"calc", "jsq"}, Enabled: true, IsGlobal: false,
			Prefix: "=", Activation: engine.Direct, Weight: 1.0, Count: 5},
		{ID: "timestamp", Name: "时间戳", Description: "时间戳 ⇄ 可读时间",
			Aliases: []string{"t", "ts", "time"}, Enabled: true, IsGlobal: false,
			Prefix: "$", Activation: engine.Indirect, Weight: 1.0, Count: 5},
		{ID: "base64", Name: "Base64", Description: "base64 编解码（加密 / 解密）",
			Aliases: []string{"b", "b64"}, Enabled: true, IsGlobal: false,
			Prefix: "@b", Activation: engine.Direct, Weight: 1.0, Count: 5},
		{ID: "random", Name: "随机数", Description: "随机整数 / 随机字符串",
			Aliases: []string{"r", "rand"}, Enabled: true, IsGlobal: false,
			Prefix: "@r", Activation: engine.Indirect, Weight: 1.0, Count: 6},
		{ID: engine.PickerPluginID, Name: "插件列表", Description: "按名称或别名筛选插件",
			Enabled: true, IsGlobal: false,
			Prefix: "#", Activation: engine.Direct, Weight: 1.0, Count: 5},
	}
	if !reflect.DeepEqual(c.Providers, want) {
		t.Errorf("默认归一结果整体不符：\n实得 %+v\n期望 %+v", c.Providers, want)
	}
}

func TestNormalizeUserOverridesWin(t *testing.T) {
	c := mustNormalize(t, fakeRegistry(), []UserOverride{
		{ID: "apps", Enabled: bp(false), Weight: fp(2.5), Count: ip(7), IsGlobal: bp(false), Prefix: sp("{")},
		{ID: "base64", Prefix: sp("}"), Activation: sp("indirect")},
		{ID: engine.PickerPluginID, Prefix: sp("??")},
	})

	apps := mustLookup(t, c, "apps")
	if apps.Enabled || apps.Weight != 2.5 || apps.Count != 7 || apps.IsGlobal || apps.Prefix != "{" {
		t.Errorf("apps 覆盖未生效：%+v", apps)
	}
	b64 := mustLookup(t, c, "base64")
	if b64.Prefix != "}" || b64.Activation != engine.Indirect {
		t.Errorf("base64 覆盖未生效：%+v", b64)
	}
	picker := mustLookup(t, c, engine.PickerPluginID)
	if picker.Prefix != "??" {
		t.Errorf("picker 前缀 = %q，期望 %q（前缀可配置，D5 的修点）", picker.Prefix, "??")
	}
	// 没写的插件仍取 Descriptor 默认，且整体不该冒出警告。
	if calc := mustLookup(t, c, "calculator"); calc.Prefix != "=" || calc.Weight != 1.0 {
		t.Errorf("calculator 不该被动到：%+v", calc)
	}
	wantWarnings(t, c)
}

func TestNormalizeActivationLenientAccepted(t *testing.T) {
	for _, tt := range []struct {
		raw  string
		want engine.Activation
	}{
		{"direct", engine.Direct},
		{"indirect", engine.Indirect},
		{"DIRECT", engine.Direct},       // 大小写不敏感（同生产宽松反序列化）
		{" Indirect ", engine.Indirect}, // 首尾空白不敏感
	} {
		c := mustNormalize(t, fakeRegistry(), []UserOverride{
			{ID: "timestamp", Activation: sp(tt.raw)},
		})
		if got := mustLookup(t, c, "timestamp").Activation; got != tt.want {
			t.Errorf("activation %q → %v，期望 %v", tt.raw, got, tt.want)
		}
		wantWarnings(t, c)
	}
}

// ── C1：provider id 必须在注册表 ──────────────────────────────────────────

func TestNormalizeListsEveryUnknownID(t *testing.T) {
	// C1 失败侧：两个未知 id 必须**全部**列出（生产 D4 就是一个都不提）。
	_, err := Normalize(fakeRegistry(), []UserOverride{
		{ID: "mystery", Enabled: bp(false)},
		{ID: "ghost", Prefix: sp("%")},
	})
	want := "未知的 provider id：ghost、mystery（注册表里的 id：" + fakeIDs + "）"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v，期望 %q", err, want)
	}
	if !errors.Is(err, ErrUnknownProviderID) {
		t.Errorf("errors.Is(err, ErrUnknownProviderID) = false，接线层没法按类型分流：%v", err)
	}

	// C1 成功侧：id 全在注册表 → 无错。
	if _, err := Normalize(fakeRegistry(), []UserOverride{{ID: "ghost"}}); err == nil {
		t.Fatal("未知 id 必须报错，不能静默收下")
	}
	c := mustNormalize(t, fakeRegistry(), []UserOverride{{ID: engine.PickerPluginID, Prefix: sp("%")}})
	wantWarnings(t, c)
}

func TestNormalizeRegistrySelfCheck(t *testing.T) {
	// 注册表里 id 重复 = 装配期 bug，必须报错（否则 C1 的「已知集合」本身是坏的）。
	_, err := Normalize([]Descriptor{{ID: "dup"}, {ID: "dup"}}, nil)
	if err == nil || err.Error() != "注册表 id 重复：dup（注册表 id 必须唯一）" {
		t.Errorf("err = %v，期望「注册表 id 重复：dup（注册表 id 必须唯一）」", err)
	}
	if !errors.Is(err, ErrDuplicateDescriptor) {
		t.Errorf("err 不是 ErrDuplicateDescriptor：%v", err)
	}
	// 空 id 同样拒（空 id 会让前缀表与 Lookup 都不可用）。
	if _, err := Normalize([]Descriptor{{ID: ""}}, nil); err == nil ||
		err.Error() != "注册表里有插件未声明 id（Descriptor.ID 为空串）" {
		t.Errorf("err = %v", err)
	}
}

// ── C2：触发串不能互为前缀 ──────────────────────────────────────────────

func TestNormalizePrefixCollisionNamesBothPlugins(t *testing.T) {
	// 普通撞车：base64 抢 calculator 的 "="（两条都是 Direct，触发串全等）。
	_, err := Normalize(fakeRegistry(), []UserOverride{
		{ID: "base64", Prefix: sp("=")},
	})
	want := "前缀冲突：calculator（计算器，直接）的前缀 \"=\" 与 base64（Base64，直接）的前缀 \"=\" 冲突：" +
		"触发串 \"=\" 与 \"=\" 互为前缀（C2：各前缀展开后的触发串不能互为前缀）"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v，期望 %q", err, want)
	}
	if !errors.Is(err, ErrPrefixConflict) {
		t.Errorf("err 不是 ErrPrefixConflict：%v", err)
	}

	// 触发串含空格：random 覆盖成 "#" 后仍是 Indirect（默认激活方式），触发串 "# "；
	// picker 的 "#" 是 Direct，触发串 "#" 是 "# " 的前缀 → 冲突。
	_, err = Normalize(fakeRegistry(), []UserOverride{
		{ID: "random", Prefix: sp("#")},
	})
	want = "前缀冲突：random（随机数，间接）的前缀 \"#\" 与 __picker（插件列表，直接）的前缀 \"#\" 冲突：" +
		"触发串 \"# \" 与 \"#\" 互为前缀（C2：各前缀展开后的触发串不能互为前缀）"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v，期望 %q", err, want)
	}

	// 两组撞车要一次全说清（不要用户改一个报一个）。
	_, err = Normalize(fakeRegistry(), []UserOverride{
		{ID: "base64", Prefix: sp("=")},
		{ID: "timestamp", Prefix: sp("%")},
		{ID: "random", Prefix: sp("%")},
	})
	want = "前缀冲突：calculator（计算器，直接）的前缀 \"=\" 与 base64（Base64，直接）的前缀 \"=\" 冲突：" +
		"触发串 \"=\" 与 \"=\" 互为前缀；timestamp（时间戳，间接）的前缀 \"%\" 与 random（随机数，间接）的前缀 \"%\" 冲突：" +
		"触发串 \"% \" 与 \"% \" 互为前缀（C2：各前缀展开后的触发串不能互为前缀）"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v，期望 %q", err, want)
	}
}

// TestNormalizePrefixRelationIsNotExactMatch 锁住 C2 的核心：判的是**展开后的触发串**
// 的互为前缀关系，不是字面量全等。# 与 #b、@ 与 @b 这种长短关系同样要被拦下，
// 而两个 Indirect 前缀只要展开后不构成前缀关系（@b / @bc）就必须放行。
func TestNormalizePrefixRelationIsNotExactMatch(t *testing.T) {
	// 用户实际遇到的形状：picker "#" Direct 遮蔽 base64 "#b" Indirect。
	_, err := Normalize(fakeRegistry(), []UserOverride{
		{ID: engine.PickerPluginID, Prefix: sp("#")},
		{ID: "base64", Prefix: sp("#b"), Activation: sp("indirect")},
	})
	if !errors.Is(err, ErrPrefixConflict) {
		t.Fatalf("picker # 与 base64 #b 应判冲突，err = %v", err)
	}
	if !strings.Contains(err.Error(), "触发串 \"#b \" 与 \"#\" 互为前缀") {
		t.Errorf("错误消息应说明触发串关系（含间接的空格），实得：%v", err)
	}

	// 两个 Indirect：触发串 "@b " 与 "@bc " 互不为前缀 → 合法共存。
	c := mustNormalize(t, fakeRegistry(), []UserOverride{
		{ID: "base64", Prefix: sp("@b"), Activation: sp("indirect")},
		{ID: "random", Prefix: sp("@bc"), Activation: sp("indirect")},
	})
	if got := mustLookup(t, c, "base64").Prefix; got != "@b" {
		t.Errorf("base64 前缀 = %q，期望 @b", got)
	}
	if got := mustLookup(t, c, "random").Prefix; got != "@bc" {
		t.Errorf("random 前缀 = %q，期望 @bc", got)
	}

	// Direct 短串 vs Indirect 长串："@" 是 "@bc " 的前缀 → 冲突（敲 @ 就进 base64）。
	_, err = Normalize(fakeRegistry(), []UserOverride{
		{ID: "base64", Prefix: sp("@"), Activation: sp("direct")},
		{ID: "random", Prefix: sp("@bc"), Activation: sp("indirect")},
	})
	if !errors.Is(err, ErrPrefixConflict) {
		t.Errorf("Direct @ 与 Indirect @bc 应判冲突（@ 是 @bc␠ 的前缀），err = %v", err)
	}
}

func TestNormalizePrefixlessAndDisabledStillValid(t *testing.T) {
	// 成功侧 1：多个「无前缀」插件共存合法（空前缀不参与 C2）。
	c := mustNormalize(t, fakeRegistry(), nil)
	wantWarnings(t, c)

	// 成功侧 2：把 picker 前缀改掉后，别的插件就能用 "#"——同字符不重叠就不撞。
	c = mustNormalize(t, fakeRegistry(), []UserOverride{
		{ID: engine.PickerPluginID, Prefix: sp("??")},
		{ID: "base64", Prefix: sp("#")},
	})
	if mustLookup(t, c, "base64").Prefix != "#" {
		t.Errorf("base64 该拿到 \"#\"：%+v", mustLookup(t, c, "base64"))
	}

	// 决策点：禁用插件仍占着前缀（避免拨开开关那一刻静默改掉别人的路由）。
	if _, err := Normalize(fakeRegistry(), []UserOverride{
		{ID: "calculator", Enabled: bp(false)},
		{ID: "base64", Prefix: sp("=")},
	}); err == nil {
		t.Error("calculator 已禁用，但 base64 抢它的前缀仍该报错（当前定案）")
	}
}

// ── C3：前缀非空且不含空白 → 该项置空 + 中文警告 ────────────────────────────

func TestNormalizeWhitespacePrefixWarnsAndClears(t *testing.T) {
	c := mustNormalize(t, fakeRegistry(), []UserOverride{
		{ID: "base64", Prefix: sp("b 6")},
		{ID: "calculator", Prefix: sp("\t")},
	})
	if got := mustLookup(t, c, "base64").Prefix; got != "" {
		t.Errorf("base64 前缀 = %q，含空白必须置空", got)
	}
	if got := mustLookup(t, c, "calculator").Prefix; got != "" {
		t.Errorf("calculator 前缀 = %q，制表符也算空白", got)
	}
	wantWarnings(t, c,
		"插件 calculator 的前缀 \"\\t\" 含空白，已置空（C3：前缀不能含空白）",
		"插件 base64 的前缀 \"b 6\" 含空白，已置空（C3：前缀不能含空白）",
	)

	// 成功侧：不含空白的多字符符号前缀照收；显式写空串 = 合法「无前缀」，不警告。
	c = mustNormalize(t, fakeRegistry(), []UserOverride{
		{ID: "timestamp", Prefix: sp("<>")},
		{ID: "apps", Prefix: sp("")},
	})
	if got := mustLookup(t, c, "timestamp").Prefix; got != "<>" {
		t.Errorf("timestamp 前缀 = %q，期望 %q", got, "<>")
	}
	wantWarnings(t, c)
}

// ── C4：前缀不得与保留键冲突 ────────────────────────────────────────────

func TestNormalizeReservedPrefixRejectsLettersAndDigits(t *testing.T) {
	tests := []struct {
		name   string
		id     string
		prefix string
		kind   string
	}{
		{"单字母（V-6 会误触发 Camera / Chrome）", "calculator", "c", "字母键"},
		{"双字母（§7.3.6b 明确弃用）", "calculator", "ca", "字母键"},
		{"大写字母", "base64", "B", "字母键"},
		{"中文也算字母", "base64", "计", "字母键"},
		{"数字（时间戳 / 算式打头）", "random", "7", "数字键"},
	}
	for _, tt := range tests {
		_, err := Normalize(fakeRegistry(), []UserOverride{{ID: tt.id, Prefix: sp(tt.prefix)}})
		want := "保留键冲突：" + tt.id + " 的前缀 \"" + tt.prefix + "\" 是" + tt.kind +
			"（§7.3.6b：前缀按符号键分配，不占用字母与数字）"
		if err == nil || err.Error() != want {
			t.Errorf("%s：err = %v，期望 %q", tt.name, err, want)
		}
		if !errors.Is(err, ErrReservedPrefix) {
			t.Errorf("%s：err 不是 ErrReservedPrefix：%v", tt.name, err)
		}
	}

	// 多处撞车一次列全。
	_, err := Normalize(fakeRegistry(), []UserOverride{
		{ID: "apps", Prefix: sp("a")},
		{ID: "base64", Prefix: sp("3")},
	})
	want := "保留键冲突：apps 的前缀 \"a\" 是字母键；base64 的前缀 \"3\" 是数字键" +
		"（§7.3.6b：前缀按符号键分配，不占用字母与数字）"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v，期望 %q", err, want)
	}
}

func TestNormalizeSymbolPrefixesAllAccepted(t *testing.T) {
	// C4 成功侧：符号键一律放行，且 "#" 没有任何特殊性（§7.3.4 尾注：那段基于旧
	// 语义的注释可退休）。用单插件注册表，免得撞车（C2）混进这轮的判定里。
	solo := []Descriptor{{
		ID: "solo", Name: "单插件", DefaultEnabled: true, DefaultIsGlobal: false,
		DefaultActivation: engine.Direct, DefaultWeight: 1.0, DefaultCount: 5,
	}}
	for _, p := range []string{"=", "{", "}", "$", "%", "#", "@b", "@u", "@r", "??", ">", "<", ")", "`", "+", "~"} {
		c := mustNormalize(t, solo, []UserOverride{{ID: "solo", Prefix: sp(p)}})
		if got := mustLookup(t, c, "solo").Prefix; got != p {
			t.Errorf("前缀 %q 被改成 %q", p, got)
		}
		wantWarnings(t, c)
	}
}

// ── C5：weight 有限且 clamp 0..3（静默夹紧） ─────────────────────────────

func TestNormalizeWeightClampedSilently(t *testing.T) {
	tests := []struct {
		in   float64
		want float64
	}{
		{9.0, 3.0},    // 上限夹紧
		{3.0, 3.0},    // 边界内
		{2.5, 2.5},    // 不动
		{0.0, 0.0},    // 下限边界
		{-2.0, 0.0},   // 下限夹紧
		{3.0001, 3.0}, // 刚过界也要夹住
	}
	for _, tt := range tests {
		c := mustNormalize(t, fakeRegistry(), []UserOverride{{ID: "calculator", Weight: fp(tt.in)}})
		if got := mustLookup(t, c, "calculator").Weight; got != tt.want {
			t.Errorf("weight %v → %v，期望 %v", tt.in, got, tt.want)
		}
		wantWarnings(t, c) // 夹紧是**静默**的（C5 定案）
	}
}

func TestNormalizeNonFiniteWeightFallsBackToDescriptor(t *testing.T) {
	// NaN / ±Inf 不是「夹紧」能解决的：回落 Descriptor 默认，并吭一声。
	for _, tt := range []struct {
		in   float64
		show string
		id   string
		want float64
		msg  string
	}{
		{in: math.NaN(), show: "NaN", id: "timestamp", want: 1.0,
			msg: "插件 timestamp 的 weight NaN 不是有限数，改用 1（C5）"},
		{in: math.Inf(1), show: "+Inf", id: "apps", want: 1.2,
			msg: "插件 apps 的 weight +Inf 不是有限数，改用 1.2（C5）"},
		{in: math.Inf(-1), show: "-Inf", id: "calculator", want: 1.0,
			msg: "插件 calculator 的 weight -Inf 不是有限数，改用 1（C5）"},
	} {
		c := mustNormalize(t, fakeRegistry(), []UserOverride{{ID: tt.id, Weight: fp(tt.in)}})
		if got := mustLookup(t, c, tt.id).Weight; got != tt.want {
			t.Errorf("weight %s → %v，期望 Descriptor 默认 %v", tt.show, got, tt.want)
		}
		wantWarnings(t, c, tt.msg)
	}

	// Descriptor 默认值本身也不可用时，落到乘法单位元 1.0（本包唯一允许的兜底数）。
	broken := []Descriptor{{ID: "broken", DefaultEnabled: true, DefaultWeight: math.NaN()}}
	c := mustNormalize(t, broken, []UserOverride{{ID: "broken", Weight: fp(math.NaN())}})
	if got := mustLookup(t, c, "broken").Weight; got != 1.0 {
		t.Errorf("weight = %v，期望 1（neutralWeight）", got)
	}
	wantWarnings(t, c, "插件 broken 的 weight NaN 不是有限数，改用 1（C5）")
}

// ── C6：count clamp 1..50（静默夹紧） ──────────────────────────────────

func TestNormalizeCountClampedSilently(t *testing.T) {
	tests := []struct {
		in   int
		want int
	}{
		{0, 1},     // 下限（防止 random 一次吐 0 条把结果区打空）
		{-5, 1},    // 负数同下限
		{1, 1},     // 边界内
		{7, 7},     // 不动
		{50, 50},   // 上限边界
		{9999, 50}, // 上限夹紧
	}
	for _, tt := range tests {
		c := mustNormalize(t, fakeRegistry(), []UserOverride{{ID: "random", Count: ip(tt.in)}})
		if got := mustLookup(t, c, "random").Count; got != tt.want {
			t.Errorf("count %d → %d，期望 %d", tt.in, got, tt.want)
		}
		wantWarnings(t, c) // 静默（C6 定案）
	}

	// Descriptor 没声明条数（0）也夹到下限：归一后的 Count 恒在 1..50，
	// 不用条数概念的插件忽略它。
	c := mustNormalize(t, []Descriptor{{ID: "nocount", DefaultEnabled: true}}, nil)
	if got := mustLookup(t, c, "nocount").Count; got != 1 {
		t.Errorf("count = %d，期望 1", got)
	}
	wantWarnings(t, c)
}

// ── C7：activation 未知取值 → 记日志按「没写」处理 ─────────────────────────

func TestNormalizeUnknownActivationTreatedAsUnset(t *testing.T) {
	// 失败侧：笔误废不掉整份配置（同生产 deserialize_activation_lenient），
	// 但必须留下一条中文警告，且取值回落 Descriptor 的 Indirect。
	c := mustNormalize(t, fakeRegistry(), []UserOverride{
		{ID: "timestamp", Activation: sp("sidirect")},
	})
	if got := mustLookup(t, c, "timestamp").Activation; got != engine.Indirect {
		t.Errorf("activation 未知 → %v，期望回落 Descriptor 默认 Indirect", got)
	}
	wantWarnings(t, c, "插件 timestamp 的 activation \"sidirect\" 不是 direct / indirect，按「没写」处理（C7）")

	// 空串也算未知取值（用户写了 activation = "" 是笔误，不是「没写」）。
	c = mustNormalize(t, fakeRegistry(), []UserOverride{{ID: "base64", Activation: sp("")}})
	if got := mustLookup(t, c, "base64").Activation; got != engine.Direct {
		t.Errorf("activation 空串 → %v，期望回落 Descriptor 默认 Direct", got)
	}
	wantWarnings(t, c, "插件 base64 的 activation \"\" 不是 direct / indirect，按「没写」处理（C7）")

	// 成功侧：合法值不产生警告（见 TestNormalizeActivationLenientAccepted）。
}

// ── 警告顺序（接线层照此落日志） ────────────────────────────────────────

func TestNormalizeWarningsAreOrdered(t *testing.T) {
	// 注册顺序优先于 override 的书写顺序：calculator 在 timestamp 之前。
	c := mustNormalize(t, fakeRegistry(), []UserOverride{
		{ID: "timestamp", Prefix: sp(" ")},
		{ID: "calculator", Activation: sp("nope")},
		{ID: "apps", Weight: fp(4)}, // 夹紧是静默的，不该出现在警告里
	})
	wantWarnings(t, c,
		"插件 calculator 的 activation \"nope\" 不是 direct / indirect，按「没写」处理（C7）",
		"插件 timestamp 的前缀 \" \" 含空白，已置空（C3：前缀不能含空白）",
	)
}

func TestNormalizeDuplicateOverrideWarnsAndLastWins(t *testing.T) {
	// 重复覆盖不静默：留下一条警告，取值以**最后一条整项**为准（不是逐字段合并，
	// 所以第一条里的 weight 也一起作废，回落 Descriptor 默认 1.0）。
	c := mustNormalize(t, fakeRegistry(), []UserOverride{
		{ID: "calculator", Prefix: sp("%"), Weight: fp(2)},
		{ID: "calculator", Prefix: sp("&")},
	})
	p := mustLookup(t, c, "calculator")
	if p.Prefix != "&" || p.Weight != 1.0 {
		t.Errorf("最后一条整项为准没生效：%+v", p)
	}
	wantWarnings(t, c, "插件 calculator 在用户配置里出现 2 次，以最后一条为准")
}
