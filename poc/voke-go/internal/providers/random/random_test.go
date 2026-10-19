// random provider 测试：以 `voke-core/random.rs` 行为为 oracle——一级纯选择器（空→
// 四方向、非空→提示）、二级首条「重新生成」+ count 条 Copy、范围 / 长度解析、
// 固定种子可复现、每次查询重掷。范围越界与非法参数各归其位。
package random

import (
	"strconv"
	"strings"
	"testing"

	"github.com/zyition/voke/poc/voke-go/internal/providers"
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
}

func TestLevel1EmptyListsFourTypes(t *testing.T) {
	hits := rand(seeded(), "")
	if len(hits) != 4 {
		t.Fatalf("一级空输入应 4 个方向：%v", titles(hits))
	}
	want := []struct{ title, mode string }{
		{"随机整数", "int"}, {"随机字符串", "str"}, {"含特殊字符", "sym"}, {"十六进制", "hex"},
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
		var v int64
		if _, err := parseInto(h.Action.Text, &v); err != nil {
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
		var v int64
		if _, err := parseInto(h.Title, &v); err != nil {
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

func TestLevel2StringParamUsesLength(t *testing.T) {
	p := withCount(2)
	for _, tc := range []struct{ mode, q string }{{"str", "24"}, {"sym", "20"}, {"hex", "40"}} {
		hits := randMode(p, tc.q, tc.mode)
		if len(hits) != 3 {
			t.Fatalf("%s/%s 应 3 条：%v", tc.mode, tc.q, titles(hits))
		}
		for _, h := range hits[1:] {
			if len(h.Title) != mustInt(tc.q) {
				t.Errorf("%s 长度应 %s：%q", tc.mode, tc.q, h.Title)
			}
			switch tc.mode {
			case "str":
				if !isAlnum(h.Title) {
					t.Errorf("str 应全字母数字：%q", h.Title)
				}
			case "sym":
				if !allIn(h.Title, charsSym) {
					t.Errorf("sym 应全在符号表：%q", h.Title)
				}
			case "hex":
				if !isHex(h.Title) {
					t.Errorf("hex 应全十六进制：%q", h.Title)
				}
			}
		}
	}
}

func TestLevel2ParseFailureHint(t *testing.T) {
	p := seeded()
	for _, tc := range []struct{ mode, q string }{{"int", "abc"}, {"str", "abc"}, {"hex", "x:y"}} {
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

func TestPlaceholder(t *testing.T) {
	p := seeded()
	if got := p.Placeholder(""); got != "选择随机类型" {
		t.Errorf("一级占位符 = %q", got)
	}
	if !strings.Contains(p.Placeholder("int"), "范围") ||
		!strings.Contains(p.Placeholder("str"), "长度") ||
		!strings.Contains(p.Placeholder("hex"), "64") {
		t.Error("二级占位符应随类型变化")
	}
}

func TestSymbolAndHexAlphabets(t *testing.T) {
	p := seeded()
	sym := p.randomStringWith(256, alphaSym)
	if !strings.ContainsAny(sym, "!@#$%^&*-_=+") {
		t.Errorf("256 位符号串应至少含一个特殊字符：%q", sym)
	}
	hx := p.randomStringWith(256, alphaHex)
	if !isHex(hx) {
		t.Errorf("十六进制表不含 g-z：%q", hx)
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
	a, b := p.randomString(32), p.randomString(32)
	if !isAlnum(a) || len(a) != 32 {
		t.Errorf("随机串应 32 位全字母数字：%q", a)
	}
	if a == b {
		t.Error("连续生成不能相同")
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
	if randMode(seeded(), "20", "str")[1].Title != randMode(newSeeded(2024), "20", "str")[1].Title {
		t.Error("固定种子下 str 应可复现")
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

func TestParseNamedAlphabets(t *testing.T) {
	for q, want := range map[string]alphabetKind{
		"str": alphaAlnum, "string": alphaAlnum, "alnum": alphaAlnum,
		"sym": alphaSym, "special": alphaSym, "symbols": alphaSym,
		"hex": alphaHex, "STR": alphaAlnum,
	} {
		r, ok := parse(q)
		if !ok || !r.isString || r.alpha != want || r.length != defaultStringLen {
			t.Errorf("parse(%q) = %+v ok=%v，想要 String(len=16,%v)", q, r, ok, want)
		}
	}
}

func TestParseLengthColon(t *testing.T) {
	r, ok := parse("str:8")
	if !ok || !r.isString || r.length != 8 || r.alpha != alphaAlnum {
		t.Errorf("parse(str:8) = %+v ok=%v", r, ok)
	}
	if _, ok := parse("str:0"); ok {
		t.Error("str:0 长度非法应拒绝")
	}
}

// ── 测试小工具 ──

func parseInto(s string, out *int64) (int64, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	*out = n
	return n, err
}

func mustInt(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

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
