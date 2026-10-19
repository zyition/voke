// Package random 生成随机整数 / 随机字符串——1:1 对照生产 `voke-core/src/random.rs`。
//
// 分层：纯逻辑（参数解析 + 生成 + 出 Match），零 mygo、零 IO，Linux 原生可测。
// 执行 Copy / Refresh 在 app 层 dispatcher。熵源注入：生产 crypto/rand，测试用固定
// 种子（math/rand）保证可复现。两级子模式（int/str/sym/hex）复用 base64 的
// provider 路由范式，一级是纯选择器、二级首条固定「重新生成」。
package random

import (
	crand "crypto/rand"
	"encoding/binary"
	"fmt"
	mrand "math/rand"
	"strconv"
	"strings"

	"github.com/zyition/voke/poc/voke-go/internal/providers"
)

const (
	ID = "random"

	SubModeInt = "int"
	SubModeStr = "str"
	SubModeSym = "sym"
	SubModeHex = "hex"

	prefix           = "#r" // 二级前缀（`#` 归 picker），Indirect：`#r ` 触发，对齐 #b/#u/#t。
	defaultStringLen = 16
	maxStringLen     = 256
	defaultIntMax    = 100
	maxRangeSpan     = 1_000_000_000
)

// 三张字符表（决策 5：先只给这三张，够覆盖常见用途）。
const (
	charsAlnum = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	charsSym   = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789!@#$%^&*-_=+"
	charsHex   = "0123456789abcdef"
)

type alphabetKind int

const (
	alphaAlnum alphabetKind = iota
	alphaSym
	alphaHex
)

func (a alphabetKind) chars() string {
	switch a {
	case alphaSym:
		return charsSym
	case alphaHex:
		return charsHex
	default:
		return charsAlnum
	}
}

func (a alphabetKind) label() string {
	switch a {
	case alphaSym:
		return "字母 + 数字 + 特殊字符"
	case alphaHex:
		return "十六进制"
	default:
		return "字母 + 数字"
	}
}

// request 是一次生成的规格：isString=false 走整数 [low, high]，否则定长字符串。
type request struct {
	isString bool
	low      int64
	high     int64
	length   int
	alpha    alphabetKind
}

// Provider 实现 providers.Provider。
type Provider struct {
	config providers.ProviderConfig
	fill   func([]byte)
}

var _ providers.Provider = (*Provider)(nil)

func defaultConfig() providers.ProviderConfig {
	return providers.ProviderConfig{
		Enabled: true, IsGlobal: false, Weight: 1.0,
		Prefix: prefix, Activation: "Indirect", Count: 5,
	}
}

// New 返回默认配置的 random provider（生产熵源 crypto/rand）。
func New() *Provider {
	return &Provider{
		config: defaultConfig(),
		fill:   func(b []byte) { _, _ = crand.Read(b) },
	}
}

// newSeeded 用固定种子构造（测试可复现，对照 Rust `SeededEntropy`）。
func newSeeded(seed int64) *Provider {
	r := mrand.New(mrand.NewSource(seed))
	return &Provider{config: defaultConfig(), fill: func(b []byte) { _, _ = r.Read(b) }}
}

func (p *Provider) ID() string                           { return ID }
func (p *Provider) Name() string                         { return "随机值" }
func (p *Provider) Aliases() []string                    { return []string{"r", "sj", "rand"} }
func (p *Provider) Description() string                  { return "随机整数 / 随机字符串" }
func (p *Provider) Config() providers.ProviderConfig     { return p.config }
func (p *Provider) SetConfig(c providers.ProviderConfig) { p.config = c }

// Placeholder 对照 Rust：一级纯选择提示，二级按类型给范围 / 长度提示。
func (p *Provider) Placeholder(subMode string) string {
	switch subMode {
	case SubModeInt:
		return "输入范围如 5-10（默认 1–100），或选「重新生成」"
	case SubModeStr:
		return "输入长度如 24（默认 16），或选「重新生成」"
	case SubModeSym:
		return "输入长度如 24（默认 16），或选「重新生成」"
	case SubModeHex:
		return "输入长度如 64（默认 32），或选「重新生成」"
	default:
		return "选择随机类型"
	}
}

// Query 对照 Rust：sub_mode 决定级别；空 sub_mode 走一级选择器。
// RegeneratesOnQuery：random 每次查询重掷一批 → true。
func (p *Provider) RegeneratesOnQuery() bool { return true }

func (p *Provider) Query(ctx providers.QueryCtx) []providers.Match {
	q := strings.TrimSpace(ctx.Query)
	switch ctx.SubMode {
	case SubModeInt, SubModeStr, SubModeSym, SubModeHex:
		return p.queryLevel2(ctx.SubMode, q)
	default:
		return p.queryLevel1(q)
	}
}

// queryLevel1：空输入列四个进入二级的方向；非空只给「先选类型」提示（一级纯选择器）。
func (p *Provider) queryLevel1(query string) []providers.Match {
	if query == "" {
		return []providers.Match{
			providers.MatchSubquery(ID, "随机整数", "默认 1–100，进入后可选范围", SubModeInt, 1.0),
			providers.MatchSubquery(ID, "随机字符串", "默认 16 位字母+数字，进入后可选长度", SubModeStr, 0.95),
			providers.MatchSubquery(ID, "含特殊字符", "默认 16 位，字母+数字+特殊字符", SubModeSym, 0.9),
			providers.MatchSubquery(ID, "十六进制", "默认 32 位，0-9a-f", SubModeHex, 0.85),
		}
	}
	return []providers.Match{hintRow("请先选择随机类型", "进入上方类型后再设置参数（范围 / 长度）", 1.0)}
}

// queryLevel2：首条固定「重新生成」（Refresh），随后按预置 / 参数生成 count 条。
func (p *Provider) queryLevel2(subMode, query string) []providers.Match {
	count := p.config.Count
	if count < 1 {
		count = 1
	}
	refreshSubtitle := "重新生成"
	if count > 1 {
		refreshSubtitle = fmt.Sprintf("再生成 %d 个", count)
	}
	out := []providers.Match{
		providers.MatchExecute(ID, "重新生成", refreshSubtitle,
			providers.Action{Kind: providers.ActionRefresh}, 1.0),
	}

	defaultReq, ok := defaultRequest(subMode)
	if !ok {
		return out // 未知 sub_mode 兜底：只有重新生成项。
	}
	if query == "" {
		return p.appendMany(out, defaultReq, count)
	}

	var req request
	if subMode == SubModeInt {
		r, ok := parse(query)
		if !ok || r.isString {
			return append(out, hintRow("无法解析", "示例：5-10 或 24", 0.98))
		}
		req = r
	} else {
		n, ok := parseLengthParam(query)
		if !ok {
			return append(out, hintRow("无法解析", "示例：5-10 或 24", 0.98))
		}
		req = request{isString: true, length: n, alpha: defaultReq.alpha}
	}
	return p.appendMany(out, req, count)
}

// appendMany 按 request 生成 count 条（标题即值，动作 Copy），追加到 out。
func (p *Provider) appendMany(out []providers.Match, req request, count int) []providers.Match {
	for i := 0; i < count; i++ {
		value, subtitle := p.generate(req)
		out = append(out, providers.MatchExecute(ID, value, subtitle,
			providers.Action{Kind: providers.ActionCopy, Text: value},
			1.0-(float64(i)+1.0)*0.02))
	}
	return out
}

func defaultRequest(subMode string) (request, bool) {
	switch subMode {
	case SubModeInt:
		return request{low: 1, high: 100}, true
	case SubModeStr:
		return request{isString: true, length: defaultStringLen, alpha: alphaAlnum}, true
	case SubModeSym:
		return request{isString: true, length: defaultStringLen, alpha: alphaSym}, true
	case SubModeHex:
		return request{isString: true, length: 32, alpha: alphaHex}, true
	default:
		return request{}, false
	}
}

func (p *Provider) generate(req request) (string, string) {
	if req.isString {
		return p.randomStringWith(req.length, req.alpha),
			fmt.Sprintf("随机字符串（%d 位 · %s）", req.length, req.alpha.label())
	}
	return strconv.FormatInt(p.randomInt(req.low, req.high), 10),
		fmt.Sprintf("随机整数 [%d, %d]", req.low, req.high)
}

// randomInt 在 [low, high] 内取一个随机整数（含两端；倒序端点自动交换）。
func (p *Provider) randomInt(low, high int64) int64 {
	if low > high {
		low, high = high, low
	}
	span := high - low + 1
	if span > maxRangeSpan {
		span = maxRangeSpan
	}
	if span <= 1 {
		return low
	}
	return low + int64(p.randomU64()%uint64(span))
}

func (p *Provider) randomString(n int) string { return p.randomStringWith(n, alphaAlnum) }

// randomStringWith 生成一个定长随机字符串（指定字符表，长度封顶 maxStringLen）。
func (p *Provider) randomStringWith(n int, a alphabetKind) string {
	table := []byte(a.chars())
	if n > maxStringLen {
		n = maxStringLen
	}
	out := make([]byte, n)
	for i := 0; i < n; i++ {
		out[i] = table[p.randomU64()%uint64(len(table))]
	}
	return string(out)
}

func (p *Provider) randomU64() uint64 {
	var b [8]byte
	p.fill(b[:])
	return binary.LittleEndian.Uint64(b[:])
}

func hintRow(title, subtitle string, score float64) providers.Match {
	m := providers.MatchHint(ID, title, subtitle)
	m.Score = score
	return m
}

// parse 把查询解析成 request；不认识返回 ok=false。空查询 = [0, defaultIntMax]。
func parse(query string) (request, bool) {
	q := strings.TrimSpace(query)
	if q == "" {
		return request{low: 0, high: defaultIntMax}, true
	}
	lower := strings.ToLower(q)
	groups := []struct {
		names []string
		alpha alphabetKind
	}{
		{[]string{"str", "string", "alnum"}, alphaAlnum},
		{[]string{"sym", "special", "symbols"}, alphaSym},
		{[]string{"hex"}, alphaHex},
	}
	for _, g := range groups {
		for _, name := range g.names {
			if lower == name {
				return request{isString: true, length: defaultStringLen, alpha: g.alpha}, true
			}
			if rest, ok := stripNameColon(lower, name); ok {
				n, ok2 := parseLen(rest)
				if !ok2 {
					return request{}, false
				}
				return request{isString: true, length: n, alpha: g.alpha}, true
			}
		}
	}
	low, high, ok := parseRange(q)
	if !ok {
		return request{}, false
	}
	return request{low: low, high: high}, true
}

// stripNameColon：lower 形如 `<name>:rest` 时返回 rest（已 trim）。
func stripNameColon(lower, name string) (string, bool) {
	if !strings.HasPrefix(lower, name) {
		return "", false
	}
	rest := lower[len(name):]
	if !strings.HasPrefix(rest, ":") {
		return "", false
	}
	return strings.TrimSpace(rest[1:]), true
}

// parseRange 解析 `100`（0–100）、`1-100`、`1..100`、`1..=100`、`-50-50`。
func parseRange(query string) (int64, int64, bool) {
	q := strings.TrimSpace(query)
	if idx := strings.Index(q, ".."); idx >= 0 {
		low, err := strconv.ParseInt(strings.TrimSpace(q[:idx]), 10, 64)
		if err != nil {
			return 0, 0, false
		}
		high, err := strconv.ParseInt(strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(q[idx+2:]), "=")), 10, 64)
		if err != nil {
			return 0, 0, false
		}
		return validate(low, high)
	}
	if n, err := strconv.ParseInt(q, 10, 64); err == nil {
		if n < 0 {
			return validate(n, 0)
		}
		return validate(0, n)
	}
	searchFrom := 1
	if strings.HasPrefix(q, "-") {
		searchFrom = 2
	}
	if searchFrom >= len(q) {
		return 0, 0, false
	}
	rel := strings.Index(q[searchFrom:], "-")
	if rel < 0 {
		return 0, 0, false
	}
	splitAt := searchFrom + rel
	low, err := strconv.ParseInt(strings.TrimSpace(q[:splitAt]), 10, 64)
	if err != nil {
		return 0, 0, false
	}
	high, err := strconv.ParseInt(strings.TrimSpace(q[splitAt+1:]), 10, 64)
	if err != nil {
		return 0, 0, false
	}
	return validate(low, high)
}

// validate 交换倒序端点，拒绝超大跨度。
func validate(low, high int64) (int64, int64, bool) {
	if low > high {
		low, high = high, low
	}
	// 交换后 high>=low，high-low 本应非负；若溢出成负数说明真实跨度远超上限，
	// 必须判非法——与 Rust `(high-low as i128).unsigned_abs() > MAX_RANGE_SPAN` 等价
	// （旧写法 `span<0 则 -span` 会把天文级跨度折成小正数而误判合法）。
	span := high - low
	if span < 0 || span > maxRangeSpan {
		return 0, 0, false
	}
	return low, high, true
}

// parseLen 解析字符串长度（0 / 负数 / 不可解析拒绝，超长封顶 maxStringLen）。
func parseLen(text string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil || n <= 0 {
		return 0, false
	}
	if n > maxStringLen {
		n = maxStringLen
	}
	return n, true
}

// parseLengthParam 二级 str/sym/hex 的长度参数：容忍 `str:` 前缀，再交给 parseLen。
func parseLengthParam(query string) (int, bool) {
	q := strings.TrimSpace(query)
	digits := q
	if _, after, found := strings.Cut(q, ":"); found {
		digits = strings.TrimSpace(after)
	}
	return parseLen(digits)
}
