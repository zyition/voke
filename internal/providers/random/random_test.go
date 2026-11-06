// random provider 测试：一级纯选择器（空 → 随机整数 + 启用字符集、非空 → 提示）、
// 二级首条「重新生成」+ count 条 Copy、范围 / 长度解析、固定种子可复现、每次查询重掷。
// 字符串子模式由配置里的字符集驱动（cs:<名称>），默认预设与 Descriptor 共用同一份定义。
package random

import (
	"strconv"
	"strings"
	"testing"

	"voke/internal/logic/charset"
	"voke/internal/providers"
)

const (
	nameAlnum    = "字母 + 数字"
	nameSymbols  = "含特殊字符"
	nameHex      = "十六进制"
	charsSymbols = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789!@#$%^&*-_=+"
)

func seeded() *Provider { return newSeeded(2024) }

func withCount(c int) *Provider {
	p := seeded()
	cfg := p.Config()
	cfg.Count = c
	p.SetConfig(cfg)
	return p
}

func rand(p *Provider, q string) []providers.Match {
	return p.Query(providers.QueryCtx{Raw: "@r" + q, Query: q})
}

func randMode(p *Provider, q, mode string) []providers.Match {
	return p.Query(providers.QueryCtx{Raw: "@r" + q, Query: q, SubMode: mode})
}

func titles(ms []providers.Match) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Title)
	}
	return out
}

func alphabetOf(t *testing.T, p *Provider, name string) []rune {
	t.Helper()
	for _, cs := range p.charsets {
		if cs.name == name {
			return cs.alphabet
		}
	}
	t.Fatalf("找不到字符集 %q", name)
	return nil
}

func TestMetadata(t *testing.T) {
	p := seeded()
	if p.ID() != "random" || p.Name() != "随机值" || p.Description() != "随机整数 / 随机字符串" {
		t.Errorf("元数据不符：%s/%s/%s", p.ID(), p.Name(), p.Description())
	}
	c := p.Config()
	if c.IsGlobal {
		t.Error("随机值不得进全局")
	}
	if c.Prefix != "@r" || c.Activation != "Indirect" || c.Count != 5 {
		t.Errorf("配置不符（@r/Indirect/count=5）：%+v", c)
	}
	if c.DefaultLength != 16 {
		t.Errorf("插件级默认长度应为 16：%d", c.DefaultLength)
	}
	if len(c.Charsets) != 3 || c.Charsets[0].Name != nameAlnum || c.Charsets[2].Pattern != "0-9a-f" {
		t.Errorf("默认字符集不符：%+v", c.Charsets)
	}
	if c.Charsets[0].Length != 0 || c.Charsets[2].Length != 32 {
		t.Errorf("字符集默认长度应：前两套跟随全局、十六进制 32：%+v", c.Charsets)
	}
}

func TestLevel1EmptyListsIntAndEnabledCharsets(t *testing.T) {
	hits := rand(seeded(), "")
	if len(hits) != 4 {
		t.Fatalf("一级空输入应 1 整数 + 3 字符集：%v", titles(hits))
	}
	want := []struct{ title, mode string }{
		{"随机整数", "int"},
		{nameAlnum, "cs:" + nameAlnum},
		{nameSymbols, "cs:" + nameSymbols},
		{nameHex, "cs:" + nameHex},
	}
	for i, h := range hits {
		if h.Title != want[i].title {
			t.Errorf("第 %d 方向标题 = %q，想要 %q", i, h.Title, want[i].title)
		}
		if h.Interact.Kind != providers.InteractEnterSubquery || h.Interact.Sub != want[i].mode {
			t.Errorf("第 %d 方向应 EnterSubquery(%s)：%+v", i, want[i].mode, h.Interact)
		}
		if h.Action.Kind != providers.ActionNone {
			t.Errorf("方向行动作应为 None：%+v", h.Action)
		}
	}
}

func TestLevel1AnyNonEmptyIsPureSelector(t *testing.T) {
	p := seeded()
	for _, q := range []string{"1-100", "1..100", "100", "str", "STR", "str:8", "hex:40", "abc", "-", "1-2-3"} {
		hits := rand(p, q)
		if len(hits) != 1 {
			t.Errorf("%q 一级应只给一条提示：%v", q, titles(hits))
			continue
		}
		if !hits[0].IsHint() || hits[0].Title != "请先选择随机类型" {
			t.Errorf("%q 一级提示错误：%+v", q, hits[0])
		}
	}
}

func TestLevel2IntEmptyGeneratesCount(t *testing.T) {
	p := withCount(4)
	hits := randMode(p, "", "int")
	if len(hits) != 5 {
		t.Fatalf("1 重新生成 + 4 结果：%v", titles(hits))
	}
	if hits[0].Action.Kind != providers.ActionRefresh {
		t.Errorf("首条应 Refresh：%+v", hits[0].Action)
	}
	for _, h := range hits[1:] {
		if h.Action.Kind != providers.ActionCopy {
			t.Fatalf("结果行必须 Copy：%+v", h.Action)
		}
		v, err := strconv.ParseInt(h.Action.Text, 10, 64)
		if err != nil {
			t.Fatalf("整数标题应可解析：%q", h.Action.Text)
		}
		if v < 1 || v > 100 {
			t.Errorf("值 %d 越界 [1,100]", v)
		}
	}
}

func TestLevel2RefreshFirstWithCountLabel(t *testing.T) {
	hits := randMode(withCount(5), "", "int")
	if hits[0].Title != "重新生成" || hits[0].Action.Kind != providers.ActionRefresh {
		t.Errorf("首条重新生成错误：%+v", hits[0])
	}
	if !strings.Contains(hits[0].Subtitle, "再生成 5 个") {
		t.Errorf("count>1 副标题带数量：%q", hits[0].Subtitle)
	}
	if s := randMode(withCount(1), "", "int")[0].Subtitle; s != "重新生成" {
		t.Errorf("count=1 副标题应无数量：%q", s)
	}
}

func TestLevel2IntParamUsesRange(t *testing.T) {
	hits := randMode(withCount(3), "5-10", "int")
	if len(hits) != 4 {
		t.Fatalf("1 重新生成 + 3 结果：%v", titles(hits))
	}
	for _, h := range hits[1:] {
		v, err := strconv.ParseInt(h.Title, 10, 64)
		if err != nil {
			t.Fatalf("标题应为整数：%q", h.Title)
		}
		if v < 5 || v > 10 {
			t.Errorf("值 %d 不在 [5,10]", v)
		}
		if !strings.Contains(h.Subtitle, "[5, 10]") {
			t.Errorf("副标题带范围：%q", h.Subtitle)
		}
	}
}

func TestLevel2CharsetParamUsesLength(t *testing.T) {
	p := withCount(2)
	for _, tc := range []struct {
		name  string
		valid func(string) bool
	}{
		{nameAlnum, isAlnum},
		{nameSymbols, func(s string) bool { return allIn(s, charsSymbols) }},
		{nameHex, isHex},
	} {
		hits := randMode(p, "24", "cs:"+tc.name)
		if len(hits) != 3 {
			t.Fatalf("%s 应 3 条：%v", tc.name, titles(hits))
		}
		for _, h := range hits[1:] {
			if len(h.Title) != 24 {
				t.Errorf("%s 长度应 24：%q", tc.name, h.Title)
			}
			if !tc.valid(h.Title) {
				t.Errorf("%s 生成了字符集之外的字符：%q", tc.name, h.Title)
			}
			if !strings.Contains(h.Subtitle, tc.name) {
				t.Errorf("副标题应带字符集名称 %q：%q", tc.name, h.Subtitle)
			}
		}
	}
}

func TestLevel2ParseFailureHint(t *testing.T) {
	p := seeded()
	for _, tc := range []struct{ mode, q string }{{"int", "abc"}, {"cs:" + nameHex, "abc"}, {"cs:" + nameHex, "x:y"}} {
		hits := randMode(p, tc.q, tc.mode)
		if len(hits) != 2 {
			t.Fatalf("%s/%s 应 2 条：%v", tc.mode, tc.q, titles(hits))
		}
		if hits[0].Action.Kind != providers.ActionRefresh {
			t.Errorf("%s/%s 首条应仍在：%+v", tc.mode, tc.q, hits[0])
		}
		if !hits[1].IsHint() || hits[1].Title != "无法解析" || !strings.Contains(hits[1].Subtitle, "示例") {
			t.Errorf("%s/%s 错误提示错误：%+v", tc.mode, tc.q, hits[1])
		}
	}
}

func TestUnknownSubModeShowsUnavailableHint(t *testing.T) {
	p := seeded()
	for _, mode := range []string{"cs:不存在", "str", "sym", "hex"} {
		hits := randMode(p, "", mode)
		if len(hits) != 1 || !hits[0].IsHint() || hits[0].Title != "字符集不可用" {
			t.Errorf("未知子模式 %q 应给不可用提示：%v", mode, titles(hits))
		}
	}
}

func TestPlaceholder(t *testing.T) {
	p := seeded()
	if got := p.Placeholder(""); got != "选择随机类型" {
		t.Errorf("一级占位符 = %q", got)
	}
	if !strings.Contains(p.Placeholder("int"), "范围") ||
		!strings.Contains(p.Placeholder("cs:"+nameAlnum), "16") ||
		!strings.Contains(p.Placeholder("cs:"+nameHex), "32") {
		t.Error("二级占位符应随类型与默认长度变化")
	}
}

func TestDefaultLengthFallback(t *testing.T) {
	p := seeded()
	cfg := p.Config()
	cfg.DefaultLength = 24
	cfg.Charsets = []charset.Spec{
		{Name: "跟随全局", Pattern: "a-z", Enabled: true},
		{Name: "自有长度", Pattern: "A-Z", Enabled: true, Length: 8},
	}
	p.SetConfig(cfg)

	hits := rand(p, "")
	if len(hits) != 3 {
		t.Fatalf("应 1 整数 + 2 字符集：%v", titles(hits))
	}
	if !strings.Contains(hits[1].Subtitle, "24") || !strings.Contains(hits[2].Subtitle, "8") {
		t.Errorf("一级副标题应显示各自生效长度：%q / %q", hits[1].Subtitle, hits[2].Subtitle)
	}

	follow := randMode(p, "", "cs:跟随全局")
	if len(follow[1].Title) != 24 {
		t.Errorf("未配置长度的字符集应跟随插件默认 24：%q", follow[1].Title)
	}
	own := randMode(p, "", "cs:自有长度")
	if len(own[1].Title) != 8 {
		t.Errorf("字符集自有长度应优先：%q", own[1].Title)
	}

	cfg.DefaultLength = 0 // 直接 SetConfig 时缺失 / 越界 → 回落内置 16
	p.SetConfig(cfg)
	if got := randMode(p, "", "cs:跟随全局")[1].Title; len(got) != 16 {
		t.Errorf("全局长度缺失时应回落 16：%q", got)
	}
}

func TestDefaultHexUses32(t *testing.T) {
	hits := randMode(seeded(), "", "cs:"+nameHex)
	if len(hits[1].Title) != 32 {
		t.Errorf("十六进制默认应 32 位，实得 %d：%q", len(hits[1].Title), hits[1].Title)
	}
}

func TestCustomCharsetsDriveValues(t *testing.T) {
	p := seeded()
	cfg := p.Config()
	cfg.Charsets = []charset.Spec{
		{Name: "大写十六进制", Pattern: "A-F0-9", Enabled: true},
		{Name: "小写十六进制", Pattern: "a-f0-9", Enabled: true},
		{Name: "停用项", Pattern: "xyz", Enabled: false},
	}
	p.SetConfig(cfg)

	hits := rand(p, "")
	if len(hits) != 3 {
		t.Fatalf("应 1 整数 + 2 个启用字符集：%v", titles(hits))
	}
	if hits[1].Title != "大写十六进制" || hits[2].Title != "小写十六进制" {
		t.Errorf("字符集顺序 / 停用过滤错误：%v", titles(hits))
	}

	upper := randMode(p, "32", "cs:大写十六进制")
	if !allIn(upper[1].Title, "ABCDEF0123456789") {
		t.Errorf("大写字符集生成了集合外字符：%q", upper[1].Title)
	}
	if strings.ContainsAny(upper[1].Title, "abcdefghijklmnopqrstuvwxyz") {
		t.Errorf("字符集必须区分大小写：%q", upper[1].Title)
	}
	lower := randMode(p, "32", "cs:小写十六进制")
	if !allIn(lower[1].Title, "abcdef0123456789") {
		t.Errorf("小写字符集生成了集合外字符：%q", lower[1].Title)
	}
	// 停用的字符集不能作为子模式使用 → 给不可用提示。
	if got := randMode(p, "", "cs:停用项"); len(got) != 1 || !got[0].IsHint() {
		t.Errorf("停用字符集应给不可用提示：%v", titles(got))
	}
}

func TestSubModeLabel(t *testing.T) {
	p := seeded()
	if got := p.SubModeLabel("cs:" + nameHex); got != nameHex {
		t.Errorf("字符集子模式应显示名称，实得 %q", got)
	}
	if got := p.SubModeLabel("enc"); got != "enc" {
		t.Errorf("非字符集子模式应原样返回，实得 %q", got)
	}
}

func TestDegenerateRange(t *testing.T) {
	p := seeded()
	if p.randomInt(7, 7) != 7 || p.randomInt(5, 5) != 5 {
		t.Error("退化区间应返回唯一值")
	}
}

func TestStringAlnumAndNotConstant(t *testing.T) {
	p := seeded()
	alpha := alphabetOf(t, p, nameAlnum)
	a, b := p.randomStringWith(32, alpha), p.randomStringWith(32, alpha)
	if !isAlnum(a) || len(a) != 32 {
		t.Errorf("随机串应 32 位全字母数字：%q", a)
	}
	if a == b {
		t.Error("连续生成不能相同")
	}
	if p.randomStringWith(8, nil) != "" {
		t.Error("空字符集应返回空串而不是 panic")
	}
}

func TestFreshQueryRegenerates(t *testing.T) {
	p := New() // 生产熵源：两次查询第二条应不同
	if randMode(p, "", "int")[1].Title == randMode(p, "", "int")[1].Title {
		t.Error("每次查询必须重新生成")
	}
}

func TestSeededReproducible(t *testing.T) {
	if randMode(seeded(), "5-10", "int")[1].Title != randMode(newSeeded(2024), "5-10", "int")[1].Title {
		t.Error("固定种子下 int 应可复现")
	}
	if randMode(seeded(), "20", "cs:"+nameHex)[1].Title != randMode(newSeeded(2024), "20", "cs:"+nameHex)[1].Title {
		t.Error("固定种子下字符集应可复现")
	}
}

// ── 纯解析函数直测 ──

func TestParseRangeForms(t *testing.T) {
	cases := []struct {
		q         string
		low, high int64
		ok        bool
	}{
		{"100", 0, 100, true},
		{"1-100", 1, 100, true},
		{"1..100", 1, 100, true},
		{"1..=100", 1, 100, true},
		{"100-1", 1, 100, true},
		{"-50-50", -50, 50, true},
		{"-5", -5, 0, true},
		{"a-b", 0, 0, false},
		{"1-2-3", 0, 0, false},
		{"1-999999999999999", 0, 0, false},
		// 跨度天文级、high-low 溢出成负数：必须判非法（旧 `span<0 则 -span` 会折成 1 误判合法），对齐 Rust i128。
		{"-9223372036854775808..9223372036854775807", 0, 0, false},
	}
	for _, c := range cases {
		low, high, ok := parseRange(c.q)
		if ok != c.ok || (ok && (low != c.low || high != c.high)) {
			t.Errorf("parseRange(%q) = (%d,%d,%v)，想要 (%d,%d,%v)", c.q, low, high, ok, c.low, c.high, c.ok)
		}
	}
}

func TestParseLengthParam(t *testing.T) {
	for q, want := range map[string]int{"24": 24, "x:24": 24, "str:8": 8, " 16 ": 16} {
		if got, ok := parseLengthParam(q); !ok || got != want {
			t.Errorf("parseLengthParam(%q) = %d,%v，想要 %d,true", q, got, ok, want)
		}
	}
	for _, q := range []string{"0", "-3", "abc", ""} {
		if _, ok := parseLengthParam(q); ok {
			t.Errorf("parseLengthParam(%q) 应拒绝", q)
		}
	}
}

// ── 测试小工具 ──

func isAlnum(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'f' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

func allIn(s, table string) bool {
	for i := 0; i < len(s); i++ {
		if !strings.ContainsRune(table, rune(s[i])) {
			return false
		}
	}
	return true
}
