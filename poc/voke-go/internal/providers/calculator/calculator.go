// Package calculator 内联算术求值——1:1 对照生产 `voke-core/src/calculator.rs`。
//
// 分层：纯逻辑（词法 + 递归下降 + 出 Match），零 mygo、零 IO，Linux 原生可测。
// 只在 `=` 前缀（Direct）下命中、不进全局（数字串会污染应用/窗口搜索）。手写
// 递归下降不引第三方求值库，守住「core 零新依赖」。搜索路径绝不 panic：长度 /
// 嵌套深度封顶，结果非有限值（inf/NaN）一律判非法返回空。
package calculator

import (
	"math"
	"strconv"
	"strings"

	"github.com/zyition/voke/poc/voke-go/internal/providers"
)

const (
	ID = "calculator"

	prefix      = "=" // Direct：输入 `=` 即切入本插件，之后输入即表达式。
	maxInputLen = 512
	maxDepth    = 64
)

// Provider 实现 providers.Provider（无状态）。
type Provider struct {
	config providers.ProviderConfig
}

var _ providers.Provider = (*Provider)(nil)

// New 返回默认配置的 calculator provider（is_global=false、prefix==、Direct）。
func New() *Provider {
	return &Provider{config: providers.ProviderConfig{
		Enabled: true, IsGlobal: false, Weight: 1.0,
		Prefix: prefix, Activation: "Direct", Count: 5,
	}}
}

func (p *Provider) ID() string                           { return ID }
func (p *Provider) Name() string                         { return "计算器" }
func (p *Provider) Aliases() []string                    { return []string{"calc", "jsq", "c"} }
func (p *Provider) Description() string                  { return "算术表达式求值，如 1+1" }
func (p *Provider) Config() providers.ProviderConfig     { return p.config }
func (p *Provider) SetConfig(c providers.ProviderConfig) { p.config = c }

// Placeholder 对照 Rust：无子模式，激活后直接输表达式。
func (p *Provider) Placeholder(string) string { return "输入计算式，如 1+1" }

// Query 对照 Rust：非法 / 非有限结果返回空列表（用户逐字符输入过程不该闪结果）。
// RegeneratesOnQuery：表达式求值确定性 → false。
func (p *Provider) RegeneratesOnQuery() bool { return false }

func (p *Provider) Query(ctx providers.QueryCtx) []providers.Match {
	v, ok := Evaluate(ctx.Query)
	if !ok {
		return nil
	}
	text := formatNumber(v)
	return []providers.Match{providers.MatchExecute(ID, text, strings.TrimSpace(ctx.Query),
		providers.Action{Kind: providers.ActionCopy, Text: text}, 1.0)}
}

// ── 求值入口 ──

// Evaluate 求一个算术表达式的值；非法输入、非有限结果返回 ok=false。
func Evaluate(input string) (float64, bool) {
	runes := []rune(input)
	if len(runes) > maxInputLen {
		return 0, false
	}
	toks, ok := tokenize(runes)
	if !ok || len(toks) == 0 {
		return 0, false
	}
	p := &parser{toks: toks}
	v, ok := p.expr()
	if !ok {
		return 0, false
	}
	if p.pos != len(p.toks) {
		return 0, false // 尾随内容（`1 2`、`(1)3`）判非法
	}
	if math.IsInf(v, 0) || math.IsNaN(v) {
		return 0, false
	}
	return v, true
}

// formatNumber 把结果格式化成「人类会复制」的文本（整数去小数点、消 IEEE754 噪声、
// 极大值保持最短十进制往返，对照 Rust `format_number`）。
func formatNumber(v float64) string {
	if v == 0.0 {
		return "0" // 同时兜住 -0.0
	}
	if v == math.Trunc(v) && math.Abs(v) < 1e15 {
		return strconv.FormatInt(int64(v), 10)
	}
	if math.Abs(v) < 1e15 {
		rounded := math.Round(v*1e10) / 1e10
		if rounded == 0.0 {
			return "0"
		}
		return strconv.FormatFloat(rounded, 'f', -1, 64)
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// ── 词法 ──

type tkind int

const (
	tkNum tkind = iota
	tkPlus
	tkMinus
	tkStar
	tkSlash
	tkPercent
	tkCaret
	tkLParen
	tkRParen
)

type token struct {
	kind tkind
	num  float64
}

func tokenize(runes []rune) ([]token, bool) {
	toks := make([]token, 0, len(runes))
	for i := 0; i < len(runes); {
		switch c := runes[i]; c {
		case ' ', '\t', '\n', '\r':
			i++
		case '+':
			toks = append(toks, token{kind: tkPlus})
			i++
		case '-':
			toks = append(toks, token{kind: tkMinus})
			i++
		case '*':
			toks = append(toks, token{kind: tkStar})
			i++
		case '/':
			toks = append(toks, token{kind: tkSlash})
			i++
		case '%':
			toks = append(toks, token{kind: tkPercent})
			i++
		case '^':
			toks = append(toks, token{kind: tkCaret})
			i++
		case '(':
			toks = append(toks, token{kind: tkLParen})
			i++
		case ')':
			toks = append(toks, token{kind: tkRParen})
			i++
		default:
			if isDigit(c) || c == '.' {
				t, next, ok := readNumber(runes, i)
				if !ok {
					return nil, false
				}
				toks = append(toks, t)
				i = next
			} else {
				return nil, false // UnexpectedChar（含全角符号）
			}
		}
	}
	return toks, true
}

// readNumber 读一个无符号数字字面量（含小数与科学计数后缀），返回 (token, 新下标)。
func readNumber(runes []rune, start int) (token, int, bool) {
	i := start
	seenDot, seenDigit := false, false
	for i < len(runes) {
		c := runes[i]
		if isDigit(c) {
			seenDigit = true
			i++
		} else if c == '.' && !seenDot {
			seenDot = true
			i++
		} else {
			break
		}
	}
	if !seenDigit {
		return token{}, 0, false // 孤立小数点
	}
	if i < len(runes) && (runes[i] == 'e' || runes[i] == 'E') {
		j := i + 1
		if j < len(runes) && (runes[j] == '+' || runes[j] == '-') {
			j++
		}
		digitsStart := j
		for j < len(runes) && isDigit(runes[j]) {
			j++
		}
		if j == digitsStart {
			return token{}, 0, false // `1e` / `1e+`：指数没数字
		}
		i = j
	}
	v, err := strconv.ParseFloat(string(runes[start:i]), 64)
	if err != nil {
		// 溢出（1e400→±Inf）保留值交给 evaluate 的 is_finite 拦（与 Rust 一致）；其余畸形拒绝。
		if rangeErr(err) {
			return token{kind: tkNum, num: v}, i, true
		}
		return token{}, 0, false
	}
	return token{kind: tkNum, num: v}, i, true
}

func rangeErr(err error) bool {
	ne, ok := err.(*strconv.NumError)
	return ok && ne.Err == strconv.ErrRange
}

// ── 语法（递归下降）──

type parser struct {
	toks  []token
	pos   int
	depth int
}

func (p *parser) peek() (token, bool) {
	if p.pos < len(p.toks) {
		return p.toks[p.pos], true
	}
	return token{}, false
}

func (p *parser) advance() (token, bool) {
	t, ok := p.peek()
	if ok {
		p.pos++
	}
	return t, ok
}

func (p *parser) enter() bool {
	if p.depth >= maxDepth {
		return false
	}
	p.depth++
	return true
}

func (p *parser) expr() (float64, bool) {
	left, ok := p.term()
	if !ok {
		return 0, false
	}
	for {
		t, has := p.peek()
		if !has {
			break
		}
		if t.kind == tkPlus {
			p.advance()
			r, ok := p.term()
			if !ok {
				return 0, false
			}
			left += r
		} else if t.kind == tkMinus {
			p.advance()
			r, ok := p.term()
			if !ok {
				return 0, false
			}
			left -= r
		} else {
			break
		}
	}
	return left, true
}

func (p *parser) term() (float64, bool) {
	left, ok := p.unary()
	if !ok {
		return 0, false
	}
	for {
		t, has := p.peek()
		if !has {
			break
		}
		switch t.kind {
		case tkStar:
			p.advance()
			r, ok := p.unary()
			if !ok {
				return 0, false
			}
			left *= r
		case tkSlash:
			p.advance()
			r, ok := p.unary()
			if !ok {
				return 0, false
			}
			left /= r // 除零不特判：结果 inf，由 evaluate 拦
		case tkPercent:
			p.advance()
			r, ok := p.unary()
			if !ok {
				return 0, false
			}
			left = math.Mod(left, r)
		default:
			return left, true
		}
	}
	return left, true
}

func (p *parser) unary() (float64, bool) {
	if !p.enter() {
		return 0, false
	}
	var result float64
	var ok bool
	switch t, has := p.peek(); {
	case has && t.kind == tkMinus:
		p.advance()
		v, o := p.unary()
		result, ok = -v, o
	case has && t.kind == tkPlus:
		p.advance()
		result, ok = p.unary()
	default:
		result, ok = p.power()
	}
	p.depth--
	return result, ok
}

// power：`^` 右结合，且指数可带一元负号（`2^-1`）。
func (p *parser) power() (float64, bool) {
	base, ok := p.primary()
	if !ok {
		return 0, false
	}
	if t, has := p.peek(); has && t.kind == tkCaret {
		p.advance()
		if !p.enter() {
			return 0, false
		}
		exp, o := p.unary()
		p.depth--
		if !o {
			return 0, false
		}
		return math.Pow(base, exp), true
	}
	return base, true
}

func (p *parser) primary() (float64, bool) {
	t, has := p.advance()
	if !has {
		return 0, false // UnexpectedEnd
	}
	switch t.kind {
	case tkNum:
		return t.num, true
	case tkLParen:
		if !p.enter() {
			return 0, false
		}
		v, o := p.expr()
		p.depth--
		if !o {
			return 0, false
		}
		if c, has2 := p.advance(); has2 && c.kind == tkRParen {
			return v, true
		}
		return 0, false // Unbalanced（缺 `)`）
	default:
		return 0, false
	}
}

func isDigit(c rune) bool { return c >= '0' && c <= '9' }
