// Package random 生成随机整数 / 随机字符串。
//
// 分层：纯逻辑（参数解析 + 生成 + 出 Match），零 mygo、零 IO，Linux 原生可测。
// 执行 Copy / Refresh 在 app 层 dispatcher。熵源注入：生产 crypto/rand，测试用固定
// 种子（math/rand）保证可复现。
//
// 字符串子模式不再写死字母表：每套字符集由配置里的「名称 + 字符类正则 + 启用开关」
// 描述（解析见 internal/logic/charset），一级列表按启用的字符集动态生成，二级子模式
// 标识为 `cs:<名称>`。默认预设在 DefaultCharsets 里，与 Descriptor 共用同一份定义。
package random

import (
	crand "crypto/rand"
	"encoding/binary"
	"fmt"
	mrand "math/rand"
	"strconv"
	"strings"

	"voke/internal/i18n"
	"voke/internal/logic/charset"
	"voke/internal/providers"
)

const (
	ID = "random"

	// SubModeInt 是唯一的固定子模式；字符串子模式统一为 charsetSubModePrefix + 名称。
	SubModeInt           = "int"
	charsetSubModePrefix = "cs:"

	prefix           = "@r" // 二级前缀（`#` 归 picker），Indirect：`@r ` 触发，对齐 @b/@u/@t。
	defaultStringLen = 16
	maxStringLen     = charset.MaxLength
	defaultIntMax    = 100
	maxRangeSpan     = 1_000_000_000
)

// DefaultCharsets 是内置字符集预设。名称随界面语言本地化，用户可在设置页改名称 /
// 正则 / 默认长度 / 启停，或增删自定义条目。Length=0 表示跟随插件级默认长度；
// 十六进制保留历史的 32 位默认。正则区分大小写：十六进制只含小写 a-f。
func DefaultCharsets() []charset.Spec {
	return []charset.Spec{
		{Name: i18n.L("字母 + 数字", "Letters + digits"), Pattern: "a-zA-Z0-9", Enabled: true},
		{Name: i18n.L("含特殊字符", "With symbols"), Pattern: `a-zA-Z0-9!@#$%^&*\-_=+`, Enabled: true},
		{Name: i18n.L("十六进制", "Hexadecimal"), Pattern: "0-9a-f", Enabled: true, Length: 32},
	}
}

// charsetSubMode 把字符集名称编码成 engine 子模式标识。
func charsetSubMode(name string) string { return charsetSubModePrefix + name }

// charsetNameFromSubMode 解出子模式里的字符集名称；不是字符集子模式时 ok=false。
func charsetNameFromSubMode(sub string) (string, bool) {
	if !strings.HasPrefix(sub, charsetSubModePrefix) {
		return "", false
	}
	return strings.TrimPrefix(sub, charsetSubModePrefix), true
}

// resolvedCharset 是编译后的字符集：正则匹配出的单字符列表 + 展示信息。
type resolvedCharset struct {
	name     string
	pattern  string
	enabled  bool
	length   int
	alphabet []rune
}

// request 是一次生成的规格：isString=false 走整数 [low, high]，否则定长字符串。
type request struct {
	isString    bool
	low         int64
	high        int64
	length      int
	alphabet    []rune
	charsetName string
}

// Provider 实现 providers.Provider。
type Provider struct {
	config   providers.ProviderConfig
	fill     func([]byte)
	charsets []resolvedCharset
}

var _ providers.Provider = (*Provider)(nil)

func defaultConfig() providers.ProviderConfig {
	return providers.ProviderConfig{
		Enabled: true, IsGlobal: false, Weight: 1.0,
		Prefix: prefix, Activation: "Indirect", Count: 5,
		DefaultLength: defaultStringLen,
		Charsets:      DefaultCharsets(),
	}
}

// New 返回默认配置的 random provider（生产熵源 crypto/rand）。
func New() *Provider {
	p := &Provider{
		config: defaultConfig(),
		fill:   func(b []byte) { _, _ = crand.Read(b) },
	}
	p.rebuildCharsets()
	return p
}

// newSeeded 用固定种子构造（测试可复现，对照 Rust `SeededEntropy`）。
func newSeeded(seed int64) *Provider {
	r := mrand.New(mrand.NewSource(seed))
	p := &Provider{config: defaultConfig(), fill: func(b []byte) { _, _ = r.Read(b) }}
	p.rebuildCharsets()
	return p
}

func (p *Provider) ID() string { return ID }
func (p *Provider) Name() string {
	return i18n.L("随机值", "Random")
}
func (p *Provider) Aliases() []string { return []string{"r", "sj", "rand"} }
func (p *Provider) Description() string {
	return i18n.L("随机整数 / 随机字符串", "Random integers / strings")
}
func (p *Provider) Config() providers.ProviderConfig { return p.config }
func (p *Provider) SetConfig(c providers.ProviderConfig) {
	p.config = c
	p.rebuildCharsets()
}

// SubModeLabel 把 `cs:<名称>` 还原成字符集名称，供面包屑展示。
func (p *Provider) SubModeLabel(subMode string) string {
	if name, ok := charsetNameFromSubMode(subMode); ok {
		return name
	}
	return subMode
}

// rebuildCharsets 把配置里的字符集预编译成字符列表。无效条目直接跳过；配置归一
// （config.Normalize）与设置页保存前都会先校验，正常路径不会走到这里报错。
func (p *Provider) rebuildCharsets() {
	p.charsets = p.charsets[:0]
	for _, spec := range p.config.Charsets {
		name := strings.TrimSpace(spec.Name)
		if name == "" {
			continue
		}
		alphabet, err := charset.Compile(spec.Pattern)
		if err != nil {
			continue
		}
		p.charsets = append(p.charsets, resolvedCharset{
			name: name, pattern: strings.TrimSpace(spec.Pattern),
			enabled: spec.Enabled, length: spec.Length, alphabet: alphabet,
		})
	}
}

// charsetBySubMode 找当前子模式对应的、已启用的字符集。
func (p *Provider) charsetBySubMode(sub string) (resolvedCharset, bool) {
	name, ok := charsetNameFromSubMode(sub)
	if !ok {
		return resolvedCharset{}, false
	}
	for _, cs := range p.charsets {
		if cs.name == name && cs.enabled {
			return cs, true
		}
	}
	return resolvedCharset{}, false
}

// globalLength 是插件级默认长度；配置缺失 / 越界时回落内置 16。
func (p *Provider) globalLength() int {
	n := p.config.DefaultLength
	if n < charset.MinLength || n > charset.MaxLength {
		return defaultStringLen
	}
	return n
}

// effectiveLength 是某套字符集的实际默认长度：字符集自己的 Length 优先，
// 0（或越界）跟随插件级 default_length。
func (p *Provider) effectiveLength(cs resolvedCharset) int {
	if cs.length >= charset.MinLength && cs.length <= charset.MaxLength {
		return cs.length
	}
	return p.globalLength()
}

// Placeholder 对照 Rust：一级纯选择提示，二级按类型给范围 / 长度提示。
func (p *Provider) Placeholder(subMode string) string {
	if subMode == SubModeInt {
		return i18n.L("输入范围如 5-10（默认 1–100），或选「重新生成」", "Enter a range such as 5-10 (default 1–100), or choose Regenerate")
	}
	if cs, ok := p.charsetBySubMode(subMode); ok {
		return fmt.Sprintf(i18n.L("输入长度如 24（默认 %d），或选「重新生成」",
			"Enter a length such as 24 (default %d), or choose Regenerate"), p.effectiveLength(cs))
	}
	if subMode != "" {
		return i18n.L("字符集不可用，按退格返回", "Character set unavailable; press Backspace to go back")
	}
	return i18n.L("选择随机类型", "Choose a random type")
}

// RegeneratesOnQuery：random 每次查询重掷一批 → true。
func (p *Provider) RegeneratesOnQuery() bool { return true }

func (p *Provider) Query(ctx providers.QueryCtx) []providers.Match {
	q := strings.TrimSpace(ctx.Query)
	if ctx.SubMode == SubModeInt {
		return p.queryInt(q)
	}
	if cs, ok := p.charsetBySubMode(ctx.SubMode); ok {
		return p.queryCharset(cs, q)
	}
	if ctx.SubMode != "" {
		// 配置被改掉后可能停在已删除 / 停用的字符集上：给一条说明，别假装还在里面。
		return []providers.Match{hintRow(i18n.L("字符集不可用", "Character set unavailable"),
			i18n.L("该字符集已停用或删除，按退格返回", "This character set is disabled or removed; press Backspace to go back"), 1.0)}
	}
	return p.queryLevel1(q)
}

// queryLevel1：空输入列「随机整数」+ 所有启用的字符集；非空只给「先选类型」提示
// （一级纯选择器）。
func (p *Provider) queryLevel1(query string) []providers.Match {
	if query != "" {
		return []providers.Match{hintRow(i18n.L("请先选择随机类型", "Choose a random type first"),
			i18n.L("进入上方类型后再设置参数（范围 / 长度）", "Open a type above to set its options (range / length)"), 1.0)}
	}
	out := []providers.Match{
		providers.MatchSubquery(ID, i18n.L("随机整数", "Random integer"),
			i18n.L("默认 1–100，进入后可选范围", "Default 1–100; set a range inside"), SubModeInt, 1.0),
	}
	index := 0
	for _, cs := range p.charsets {
		if !cs.enabled {
			continue
		}
		out = append(out, providers.MatchSubquery(ID, cs.name,
			fmt.Sprintf(i18n.L("默认 %d 位；字符集 %s", "%d chars by default; charset %s"), p.effectiveLength(cs), cs.pattern),
			charsetSubMode(cs.name), 0.95-float64(index)*0.03))
		index++
	}
	return out
}

// queryInt 二级：首条固定「重新生成」，随后按默认 / 指定范围生成 count 条。
func (p *Provider) queryInt(query string) []providers.Match {
	out := p.refreshRows()
	req := request{low: 1, high: 100}
	if query != "" {
		r, ok := parse(query)
		if !ok {
			return append(out, hintRow(i18n.L("无法解析", "Cannot parse"), i18n.L("示例：5-10 或 24", "Example: 5-10 or 24"), 0.98))
		}
		req = r
	}
	return p.appendMany(out, req, p.count())
}

// queryCharset 二级：首条固定「重新生成」，随后按默认 / 指定长度生成 count 条。
func (p *Provider) queryCharset(cs resolvedCharset, query string) []providers.Match {
	out := p.refreshRows()
	req := request{isString: true, length: p.effectiveLength(cs), alphabet: cs.alphabet, charsetName: cs.name}
	if query != "" {
		n, ok := parseLengthParam(query)
		if !ok {
			return append(out, hintRow(i18n.L("无法解析", "Cannot parse"), i18n.L("示例：5-10 或 24", "Example: 5-10 or 24"), 0.98))
		}
		req.length = n
	}
	return p.appendMany(out, req, p.count())
}

func (p *Provider) count() int {
	if p.config.Count < 1 {
		return 1
	}
	return p.config.Count
}

// refreshRows 是二级列表首条：动作 Refresh，副标题按 count 提示再生成数量。
func (p *Provider) refreshRows() []providers.Match {
	subtitle := i18n.L("重新生成", "Regenerate")
	if n := p.count(); n > 1 {
		subtitle = fmt.Sprintf(i18n.L("再生成 %d 个", "Generate %d more"), n)
	}
	return []providers.Match{
		providers.MatchExecute(ID, i18n.L("重新生成", "Regenerate"), subtitle,
			providers.Action{Kind: providers.ActionRefresh}, 1.0),
	}
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

func (p *Provider) generate(req request) (string, string) {
	if req.isString {
		return p.randomStringWith(req.length, req.alphabet),
			fmt.Sprintf(i18n.L("随机字符串（%d 位 · %s）", "Random string (%d chars · %s)"), req.length, req.charsetName)
	}
	return strconv.FormatInt(p.randomInt(req.low, req.high), 10),
		fmt.Sprintf(i18n.L("随机整数 [%d, %d]", "Random integer [%d, %d]"), req.low, req.high)
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

// randomStringWith 从给定字符表生成定长随机字符串（长度封顶 maxStringLen）。
// 长度按 rune 计，字符集含非 ASCII 字符时输出字节数会更多。
func (p *Provider) randomStringWith(n int, alphabet []rune) string {
	if len(alphabet) == 0 || n <= 0 {
		return ""
	}
	if n > maxStringLen {
		n = maxStringLen
	}
	out := make([]rune, n)
	for i := range out {
		out[i] = alphabet[p.randomU64()%uint64(len(alphabet))]
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

// parse 把查询解析成整数范围 request；不认识返回 ok=false。空查询 = [0, defaultIntMax]。
func parse(query string) (request, bool) {
	q := strings.TrimSpace(query)
	if q == "" {
		return request{low: 0, high: defaultIntMax}, true
	}
	low, high, ok := parseRange(q)
	if !ok {
		return request{}, false
	}
	return request{low: low, high: high}, true
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

// parseLengthParam 二级字符集的长度参数：容忍 `str:` 这类前缀，再交给 parseLen。
func parseLengthParam(query string) (int, bool) {
	q := strings.TrimSpace(query)
	digits := q
	if _, after, found := strings.Cut(q, ":"); found {
		digits = strings.TrimSpace(after)
	}
	return parseLen(digits)
}
