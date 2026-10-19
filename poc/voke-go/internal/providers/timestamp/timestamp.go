// Package timestamp 做 unix 时间戳 ⇄ 可读时间的换算——1:1 对照生产
// `voke-core/src/timestamp.rs`。
//
// 分层：纯逻辑（解析 + 格式化 + 出 Match），零 mygo、零 IO，Linux 原生可测。
// 执行 Copy 动作在 app 层 dispatcher。时钟与时区偏移由 provider 注入：生产侧
// 之后接真实本地偏移（对齐 `voke_sys::local_offset_seconds`），PoC 默认系统时钟
// + 偏移 0；测试用 NewFixed / NewFixedOffset 固定。日期 ⇄ 时间戳的逆运算复用
// civil_from_days / days_from_civil（Howard Hinnant 公有领域算法）。
package timestamp

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/zyition/voke/poc/voke-go/internal/providers"
)

const (
	ID     = "timestamp"
	prefix = "@t" // 二级前缀（`#` 归 picker），Indirect 激活：`@t ` 触发，对齐 @b/@u。
)

// Provider 实现 providers.Provider。
type Provider struct {
	config providers.ProviderConfig
	now    func() int64 // 当前 unix 秒
	offset func() int32 // 本地相对 UTC 偏移（秒）
}

var _ providers.Provider = (*Provider)(nil)

// New 返回默认配置的 timestamp provider（系统时钟 + 偏移 0）。
func New() *Provider {
	return &Provider{
		config: providers.ProviderConfig{
			Enabled: true, IsGlobal: false, Weight: 1.0,
			Prefix: prefix, Activation: "Indirect", Count: 5,
		},
		now:    func() int64 { return time.Now().Unix() },
		offset: func() int32 { return 0 },
	}
}

// NewFixed 用固定时钟构造（偏移 0），测试便捷入口。
func NewFixed(nowSecs int64) *Provider {
	p := New()
	p.now = func() int64 { return nowSecs }
	return p
}

// NewFixedOffset 用固定时钟 + 固定偏移构造（测试 UTC+8 等本地行）。
func NewFixedOffset(nowSecs int64, offsetSecs int32) *Provider {
	p := NewFixed(nowSecs)
	p.offset = func() int32 { return offsetSecs }
	return p
}

func (p *Provider) ID() string                           { return ID }
func (p *Provider) Name() string                         { return "时间戳" }
func (p *Provider) Aliases() []string                    { return []string{"t", "sj", "ts", "time"} }
func (p *Provider) Description() string                  { return "时间戳 ⇄ 可读时间" }
func (p *Provider) Config() providers.ProviderConfig     { return p.config }
func (p *Provider) SetConfig(c providers.ProviderConfig) { p.config = c }

// Placeholder 对照 Rust：timestamp 无子模式，输时间戳 / now / 日期串，或留空看 now。
func (p *Provider) Placeholder(string) string {
	return "输入时间戳（如 1700000000）、now 或日期（2026-10-10 12:30）"
}

// Query 对照 Rust：按当前时间把查询解析成候选，分数依次递减保证稳定顺序。
// RegeneratesOnQuery：now / 空查询的当前时间戳行随时间变 → true（面板重显不得换值）。
func (p *Provider) RegeneratesOnQuery() bool { return true }

func (p *Provider) Query(ctx providers.QueryCtx) []providers.Match {
	cs := conversions(p.now(), p.offset(), ctx.Query)
	out := make([]providers.Match, 0, len(cs))
	for i, c := range cs {
		score := 1.0 - float64(i)*0.02
		if c.copyable {
			out = append(out, providers.MatchExecute(ID, c.title, c.subtitle,
				providers.Action{Kind: providers.ActionCopy, Text: c.title}, score))
			continue
		}
		m := providers.MatchHint(ID, c.title, c.subtitle)
		m.Score = score
		out = append(out, m)
	}
	return out
}

// conversion 是一次换算结果（对照 Rust `Conversion`）：copyable=false 即纯提示行。
type conversion struct {
	title    string
	subtitle string
	copyable bool
}

func copyRow(title, subtitle string) conversion { return conversion{title, subtitle, true} }
func hintRow(title, subtitle string) conversion { return conversion{title, subtitle, false} }

// conversions 把 query 解析成一组候选（可能为空 = 不认识的输入）。
//
// 输出规格（用户实测反馈后重定）：空查询 / now 出当前 ts 两行；10 位（秒）/
// 13 位（毫秒）纯数字出换算行（偏移非 0 时本地在前、UTC 在后，副标题带原始 ts
// 与单位）；可解析日期串按本地时间出秒 / 毫秒两行；其它位数给一条纠偏 Hint；
// 乱码 / 非法日期返回空。
func conversions(nowSecs int64, offsetSecs int32, query string) []conversion {
	q := strings.TrimSpace(query)

	if q == "" {
		return nowRows(nowSecs, offsetSecs)
	}
	if strings.EqualFold(q, "now") {
		return nowRows(nowSecs, offsetSecs)
	}

	// 非纯数字（日期串含 - / . : T 空格，或乱码 / 只有时间）：先试日期解析，
	// 失败退回空（用法说明已挪进占位符）。
	if !isAllASCIIDigits(q) {
		if dt, ok := parseDateTime(q); ok {
			return dateRows(dt, offsetSecs)
		}
		return nil
	}

	// 纯数字：**位数定单位**——10 位按秒、13 位按毫秒（先除 1000）；其它位数只给纠偏 Hint。
	var secs int64
	switch len(q) {
	case 10:
		v, err := strconv.ParseInt(q, 10, 64)
		if err != nil {
			return nil
		}
		secs = v
	case 13:
		v, err := strconv.ParseInt(q, 10, 64)
		if err != nil {
			return nil
		}
		secs = v / 1000
	default:
		return []conversion{hintRow(
			fmt.Sprintf("%s 是 %d 位", q, len(q)),
			"换算只认 10 位（秒）/ 13 位（毫秒）",
		)}
	}

	unit := "秒"
	if len(q) == 13 {
		unit = "毫秒"
	}
	tag := fmt.Sprintf("%s（按%s）", q, unit)
	var out []conversion
	// 偏移为 0 时「本地」与「UTC」文本相同，只给一份，不造重复行。
	if offsetSecs != 0 {
		out = append(out, copyRow(formatTimestampOffset(secs, offsetSecs), fmt.Sprintf("%s → 本地时间", tag)))
	}
	out = append(out, copyRow(formatTimestamp(secs), fmt.Sprintf("%s → UTC", tag)))
	return out
}

// nowRows：当前 ts（秒）/（毫秒）两行，副标题带对应可读时间。
func nowRows(nowSecs int64, offsetSecs int32) []conversion {
	local := formatTimestampOffset(nowSecs, offsetSecs)
	return []conversion{
		copyRow(strconv.FormatInt(nowSecs, 10), fmt.Sprintf("当前时间戳（秒）· %s", local)),
		copyRow(strconv.FormatInt(nowSecs*1000, 10), fmt.Sprintf("当前时间戳（毫秒）· %s", local)),
	}
}

// dateRows：日期串按本地时间解释，出秒 / 毫秒两行（UTC = 本地秒 − 偏移）。
func dateRows(dt civilDateTime, offsetSecs int32) []conversion {
	localSecs := daysFromCivil(dt.year, dt.month, dt.day)*86400 +
		int64(dt.hour)*3600 + int64(dt.minute)*60 + int64(dt.second)
	utcSecs := localSecs - int64(offsetSecs)
	local := formatTimestampOffset(utcSecs, offsetSecs)
	return []conversion{
		copyRow(strconv.FormatInt(utcSecs, 10), fmt.Sprintf("%s · 按秒", local)),
		copyRow(strconv.FormatInt(utcSecs*1000, 10), fmt.Sprintf("%s · 按毫秒", local)),
	}
}

// formatTimestamp 把 unix 秒格式化成 `YYYY-MM-DD HH:MM:SS UTC`。
func formatTimestamp(secs int64) string { return formatTimestampOffset(secs, 0) }

// formatTimestampOffset 把 unix 秒按给定时区偏移（秒）格式化。
func formatTimestampOffset(secs int64, offsetSecs int32) string {
	shifted := secs + int64(offsetSecs)
	days := floorDiv(shifted, 86400)
	secsOfDay := euclidMod(shifted, 86400)
	y, m, d := civilFromDays(days)
	hh := secsOfDay / 3600
	mm := (secsOfDay % 3600) / 60
	ss := secsOfDay % 60
	suffix := "UTC"
	if offsetSecs != 0 {
		sign := "+"
		abs := int32(offsetSecs)
		if offsetSecs < 0 {
			sign = "-"
			abs = -offsetSecs
		}
		suffix = fmt.Sprintf("%s%02d:%02d", sign, abs/3600, (abs%3600)/60)
	}
	return fmt.Sprintf("%04d-%02d-%02d %02d:%02d:%02d %s", y, m, d, hh, mm, ss, suffix)
}

// civilFromDays 把「1970-01-01 起的天数」换算成公历年月日（Howard Hinnant civil_from_days）。
func civilFromDays(z int64) (year int64, month, day int64) {
	z += 719468
	era := z
	if z < 0 {
		era = z - 146096
	}
	era = era / 146097
	doe := z - era*146097                                  // [0, 146096]
	yoe := (doe - doe/1460 + doe/36524 - doe/146096) / 365 // [0, 399]
	y := yoe + era*400
	doy := doe - (365*yoe + yoe/4 - yoe/100) // [0, 365]
	mp := (5*doy + 2) / 153                  // [0, 11]
	d := doy - (153*mp+2)/5 + 1              // [1, 31]
	m := mp + 3
	if mp >= 10 {
		m = mp - 9
	}
	if m <= 2 {
		y++
	}
	return y, m, d
}

// daysFromCivil 反向：公历年月日 → 1970-01-01 起的天数。
func daysFromCivil(y, m, d int64) int64 {
	if m <= 2 {
		y--
	}
	era := y
	if y < 0 {
		era = y - 399
	}
	era = era / 400
	yoe := y - era*400 // [0, 399]
	mp := m - 3
	if m <= 2 {
		mp = m + 9
	}
	doy := (153*mp+2)/5 + d - 1 // [0, 365]
	doe := yoe*365 + yoe/4 - yoe/100 + doy
	return era*146097 + doe - 719468
}

// civilDateTime 是解析出的「本地墙钟时间」（尚未叠加时区偏移）。
type civilDateTime struct {
	year, month, day, hour, minute, second int64
}

// parseDateTime 解析日期串（时间可选）为本地墙钟时间；不认识 / 越界一律 ok=false。
func parseDateTime(input string) (civilDateTime, bool) {
	s := strings.TrimSpace(input)
	var dateS, timeS string
	if idx := strings.IndexAny(s, "T "); idx >= 0 {
		dateS = strings.TrimSpace(s[:idx])
		timeS = strings.TrimSpace(s[idx+1:])
	} else {
		dateS = s
	}
	y, mo, d, ok := parseDate(dateS)
	if !ok {
		return civilDateTime{}, false
	}
	var hh, mm, ss int64
	if timeS != "" {
		hh, mm, ss, ok = parseTime(timeS)
		if !ok {
			return civilDateTime{}, false
		}
	}
	return civilDateTime{year: y, month: mo, day: d, hour: hh, minute: mm, second: ss}, true
}

// parseDate 解析 `YYYY<sep>MM<sep>DD`（sep ∈ - / . ，三处一致且各段位数精确）。
func parseDate(s string) (year, month, day int64, ok bool) {
	sep := byte(0)
	for i := 0; i < len(s); i++ {
		if c := s[i]; c == '-' || c == '/' || c == '.' {
			sep = c
			break
		}
	}
	if sep == 0 {
		return 0, 0, 0, false
	}
	parts := strings.Split(s, string(sep))
	if len(parts) != 3 {
		return 0, 0, 0, false
	}
	y, m, d := parts[0], parts[1], parts[2]
	if len(y) != 4 || len(m) != 2 || len(d) != 2 {
		return 0, 0, 0, false
	}
	if !isAllASCIIDigits(y) || !isAllASCIIDigits(m) || !isAllASCIIDigits(d) {
		return 0, 0, 0, false
	}
	year, err := strconv.ParseInt(y, 10, 64)
	if err != nil {
		return 0, 0, 0, false
	}
	month, err = strconv.ParseInt(m, 10, 64)
	if err != nil {
		return 0, 0, 0, false
	}
	day, err = strconv.ParseInt(d, 10, 64)
	if err != nil {
		return 0, 0, 0, false
	}
	if year < 1 || year > 9999 || month < 1 || month > 12 {
		return 0, 0, 0, false
	}
	if day < 1 || day > daysInMonth(year, month) {
		return 0, 0, 0, false
	}
	return year, month, day, true
}

// parseTime 解析 `HH:MM`（秒可省）或 `HH:MM:SS`，各段恰好 2 位且在合法范围内。
func parseTime(s string) (hour, minute, second int64, ok bool) {
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, 0, 0, false
	}
	h, m := parts[0], parts[1]
	sec := "00"
	if len(parts) == 3 {
		sec = parts[2]
	}
	if len(h) != 2 || len(m) != 2 || len(sec) != 2 {
		return 0, 0, 0, false
	}
	if !isAllASCIIDigits(h) || !isAllASCIIDigits(m) || !isAllASCIIDigits(sec) {
		return 0, 0, 0, false
	}
	hour, err := strconv.ParseInt(h, 10, 64)
	if err != nil {
		return 0, 0, 0, false
	}
	minute, err = strconv.ParseInt(m, 10, 64)
	if err != nil {
		return 0, 0, 0, false
	}
	second, err = strconv.ParseInt(sec, 10, 64)
	if err != nil {
		return 0, 0, 0, false
	}
	if hour > 23 || minute > 59 || second > 59 {
		return 0, 0, 0, false
	}
	return hour, minute, second, true
}

// isLeapYear 公历闰年（4 年一闰、100 年不闰、400 年再闰）。
func isLeapYear(year int64) bool {
	return (year%4 == 0 && year%100 != 0) || year%400 == 0
}

// daysInMonth 给定年月的天数；月份非法返回 0。
func daysInMonth(year, month int64) int64 {
	switch month {
	case 1, 3, 5, 7, 8, 10, 12:
		return 31
	case 4, 6, 9, 11:
		return 30
	case 2:
		if isLeapYear(year) {
			return 29
		}
		return 28
	default:
		return 0
	}
}

// isAllASCIIDigits 报告 s 非空且全为 ASCII 数字（空串按 Rust `all()` 语义返回 true，
// 调用方已先排除空查询）。
func isAllASCIIDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// floorDiv 欧几里得除法下取整（除数为正）。
func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && a < 0 {
		q--
	}
	return q
}

// euclidMod 非负取模（除数为正）。
func euclidMod(a, b int64) int64 {
	r := a % b
	if r < 0 {
		r += b
	}
	return r
}
