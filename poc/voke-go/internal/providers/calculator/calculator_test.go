// calculator provider 测试：以 `voke-core/calculator.rs` 为 oracle——四则与优先级、
// 括号、取模、幂（右结合 + 一元负号）、小数与科学计数、非法输入一律空结果、超长 /
// 超深不 panic、格式化去噪、Match 契约（标题=Copy 文本、副标题回显表达式）。
package calculator

import (
	"strings"
	"testing"

	"github.com/zyition/voke/poc/voke-go/internal/providers"
)

func eval(expr string) (float64, bool) { return Evaluate(expr) }

func calc(expr string) []providers.Match {
	return New().Query(providers.QueryCtx{Raw: "=" + expr, Query: expr})
}

func mustEval(t *testing.T, expr string) float64 {
	t.Helper()
	v, ok := eval(expr)
	if !ok {
		t.Fatalf("eval(%q) 期望合法，实际非法", expr)
	}
	return v
}

func TestMetadata(t *testing.T) {
	p := New()
	if p.ID() != "calculator" || p.Name() != "计算器" || p.Description() != "算术表达式求值，如 1+1" {
		t.Errorf("元数据不符：%s/%s/%s", p.ID(), p.Name(), p.Description())
	}
	c := p.Config()
	if c.IsGlobal {
		t.Error("计算器不得进全局")
	}
	if c.Prefix != "=" || c.Activation != "Direct" || !c.Enabled {
		t.Errorf("配置不符（=/Direct/enabled）：%+v", c)
	}
}

func TestArith(t *testing.T) {
	cases := []struct {
		expr string
		want float64
	}{
		{"1+1", 2}, {"10-3-2", 5}, {" 1 + 1 ", 2},
		{"2*3", 6}, {"10/4", 2.5}, {"100/5/2", 10},
		{"2+3*4", 14}, {"2*3+4", 10}, {"1+2*3-4/2", 5},
		{"(2+3)*4", 20}, {"2*(3+(4-1))", 12}, {"((((1))))", 1},
		{"7%3", 1}, {"10%5", 0}, {"2+7%3", 3},
	}
	for _, c := range cases {
		if got := mustEval(t, c.expr); got != c.want {
			t.Errorf("eval(%q) = %v，想要 %v", c.expr, got, c.want)
		}
	}
}

func TestPower(t *testing.T) {
	cases := []struct {
		expr string
		want float64
	}{
		{"2^10", 1024}, {"2^3^2", 512},
		{"-2^2", -4}, {"(-2)^2", 4},
		{"2^-1", 0.5}, {"2^-2", 0.25},
	}
	for _, c := range cases {
		if got := mustEval(t, c.expr); got != c.want {
			t.Errorf("eval(%q) = %v，想要 %v", c.expr, got, c.want)
		}
	}
}

func TestUnary(t *testing.T) {
	cases := []struct {
		expr string
		want float64
	}{
		{"-5+3", -2}, {"3*-2", -6}, {"-(1+2)", -3}, {"--3", 3}, {"+7", 7}, {"1++2", 3},
	}
	for _, c := range cases {
		if got := mustEval(t, c.expr); got != c.want {
			t.Errorf("eval(%q) = %v，想要 %v", c.expr, got, c.want)
		}
	}
}

func TestDecimalsAndScientific(t *testing.T) {
	cases := []struct {
		expr string
		want float64
	}{
		{"1.5*2", 3}, {"0.5", 0.5}, {".5+.25", 0.75}, {"5.", 5},
		{"1e3", 1000}, {"1E3", 1000}, {"1e3+1", 1001}, {"3.5e-2", 0.035}, {"1e+2*2", 200},
	}
	for _, c := range cases {
		if got := mustEval(t, c.expr); got != c.want {
			t.Errorf("eval(%q) = %v，想要 %v", c.expr, got, c.want)
		}
	}
}

func TestInvalidProduceNoMatch(t *testing.T) {
	for _, bad := range []string{
		"", "   ", "1+", "+", "(", ")", "()", "(1+2",
		"abc", "1a", "1 2", "1.2.3", "1e", "1e+", ".",
		"1/0", "0/0", "1%0", "1e400", "１+１", "2**3", "^2",
	} {
		if got := calc(bad); len(got) != 0 {
			t.Errorf("%q 不该产生结果，实得 %+v", bad, got)
		}
	}
}

func TestOversizedInputRejected(t *testing.T) {
	if _, ok := eval(strings.Repeat("1", maxInputLen+1)); ok {
		t.Error("超长输入应判非法")
	}
	okExpr := "1" + strings.Repeat("+0", (maxInputLen-1)/2)
	if len([]rune(okExpr)) <= maxInputLen {
		if got, ok := eval(okExpr); !ok || got != 1.0 {
			t.Errorf("上限内应可求值为 1，实得 %v ok=%v len=%d", got, ok, len([]rune(okExpr)))
		}
	}
}

func TestDeepNestingRejectedNoPanic(t *testing.T) {
	depth := maxDepth * 4
	expr := strings.Repeat("(", depth) + "1" + strings.Repeat(")", depth)
	if _, ok := eval(expr); ok {
		t.Error("超深嵌套应判非法而非栈溢出")
	}
}

func TestFormatNumber(t *testing.T) {
	cases := []struct {
		v    float64
		want string
	}{
		{4.0, "4"}, {-2.0, "-2"}, {0.0, "0"}, {-0.0, "0"}, {1024.0, "1024"},
		{1e20, "100000000000000000000"},
		{1e-12, "0"},
	}
	for _, c := range cases {
		if got := formatNumber(c.v); got != c.want {
			t.Errorf("formatNumber(%v) = %q，想要 %q", c.v, got, c.want)
		}
	}
	// 去噪：0.1+0.2 的 IEEE754 结果直显是 0.30000000000000004。
	if got := formatNumber(mustEval(t, "0.1+0.2")); got != "0.3" {
		t.Errorf("formatNumber(0.1+0.2) = %q，想要 \"0.3\"", got)
	}
	if got := formatNumber(mustEval(t, "10/4")); got != "2.5" {
		t.Errorf("formatNumber(10/4) = %q", got)
	}
	if got := formatNumber(mustEval(t, "1/3")); got != "0.3333333333" {
		t.Errorf("formatNumber(1/3) = %q", got)
	}
	if got := formatNumber(mustEval(t, "2^40")); got != "1099511627776" {
		t.Errorf("formatNumber(2^40) = %q", got)
	}
}

func TestMatchContract(t *testing.T) {
	hits := calc("1+1")
	if len(hits) != 1 {
		t.Fatalf("应 1 条：%+v", hits)
	}
	m := hits[0]
	if m.ProviderID != "calculator" || m.Title != "2" || m.Subtitle != "1+1" {
		t.Errorf("Match 字段错误：%+v", m)
	}
	if m.Interact.Kind != providers.InteractExecute {
		t.Errorf("应 Execute：%+v", m.Interact)
	}
	if m.Action.Kind != providers.ActionCopy || m.Action.Text != "2" {
		t.Errorf("动作应 Copy(\"2\")：%+v", m.Action)
	}
	if m.Score != 1.0 {
		t.Errorf("分数应 1.0：%v", m.Score)
	}
}

func TestTitleEqualsCopyText(t *testing.T) {
	for _, expr := range []string{"1+1", "0.1+0.2", "2^10", "1/3"} {
		hits := calc(expr)
		if len(hits) != 1 || hits[0].Action.Text != hits[0].Title {
			t.Errorf("%q：Copy 文本应 = 标题：%+v", expr, hits)
		}
	}
}
