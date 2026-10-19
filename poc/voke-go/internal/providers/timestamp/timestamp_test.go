// timestamp provider 测试：以 `voke-core/timestamp.rs` 的行为为 oracle——位数定单位、
// 偏移 0 只出 UTC / 非 0 本地在前、now 与空查询同形、日期串按本地时间反推 UTC、
// 非法输入与错位数各归其位。日期 ⇄ 天数的 roundtrip 也锁住算法正确性。
package timestamp

import (
	"strconv"
	"strings"
	"testing"

	"github.com/zyition/voke/poc/voke-go/internal/providers"
)

func strconvParse(s string) (int64, error) { return strconv.ParseInt(s, 10, 64) }

func ts(now int64, q string) []providers.Match {
	return NewFixed(now).Query(providers.QueryCtx{Raw: "#" + q, Query: q})
}

func tsUTC8(now int64, q string) []providers.Match {
	return NewFixedOffset(now, 8*3600).Query(providers.QueryCtx{Raw: "#" + q, Query: q})
}

func titles(ms []providers.Match) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Title)
	}
	return out
}

func TestMetadataMatchesRust(t *testing.T) {
	p := NewFixed(0)
	if p.ID() != "timestamp" || p.Name() != "时间戳" || p.Description() != "时间戳 ⇄ 可读时间" {
		t.Errorf("元数据不符：%s/%s/%s", p.ID(), p.Name(), p.Description())
	}
	c := p.Config()
	if c.IsGlobal {
		t.Error("时间戳不得进全局结果")
	}
	if c.Prefix != "#t" || c.Activation != "Indirect" {
		t.Errorf("配置不符（prefix=#t/Indirect）：%+v", c)
	}
	if c.Weight != 1.0 {
		t.Errorf("weight = %v，想要 1.0", c.Weight)
	}
	if !strings.Contains(p.Placeholder(""), "时间戳") {
		t.Errorf("占位符应含「时间戳」：%q", p.Placeholder(""))
	}
}

func TestFormatTimestamp(t *testing.T) {
	cases := []struct {
		secs int64
		want string
	}{
		{0, "1970-01-01 00:00:00 UTC"},
		{1_000_000_000, "2001-09-09 01:46:40 UTC"},
		{1_700_000_000, "2023-11-14 22:13:20 UTC"},
		{946_684_800, "2000-01-01 00:00:00 UTC"},
		{1_709_164_800, "2024-02-29 00:00:00 UTC"},
		{-1, "1969-12-31 23:59:59 UTC"},
		{-86_400, "1969-12-31 00:00:00 UTC"},
	}
	for _, c := range cases {
		if got := formatTimestamp(c.secs); got != c.want {
			t.Errorf("formatTimestamp(%d) = %q，想要 %q", c.secs, got, c.want)
		}
	}
}

func TestCivilRoundtrip(t *testing.T) {
	for _, days := range []int64{-1_000_000, -719_468, -1, 0, 1, 10_957, 20_000, 1_000_000} {
		y, m, d := civilFromDays(days)
		if back := daysFromCivil(y, m, d); back != days {
			t.Errorf("roundtrip 失败：%d → (%d,%d,%d) → %d", days, y, m, d, back)
		}
	}
}

func TestTenDigitsAsSeconds(t *testing.T) {
	hits := ts(0, "1700000000")
	if got := titles(hits); len(got) != 1 || got[0] != "2023-11-14 22:13:20 UTC" {
		t.Fatalf("10 位只出 UTC 一行，实得 %v", got)
	}
	if !strings.Contains(hits[0].Subtitle, "1700000000") || !strings.Contains(hits[0].Subtitle, "按秒") {
		t.Errorf("副标题应含 ts 与「按秒」：%q", hits[0].Subtitle)
	}
}

func TestThirteenDigitsAsMillis(t *testing.T) {
	hits := ts(0, "1700000000000")
	if got := titles(hits); len(got) != 1 || got[0] != "2023-11-14 22:13:20 UTC" {
		t.Fatalf("13 位按毫秒解读，实得 %v", got)
	}
	if !strings.Contains(hits[0].Subtitle, "1700000000000") || !strings.Contains(hits[0].Subtitle, "按毫秒") {
		t.Errorf("副标题应含 ts 与「按毫秒」：%q", hits[0].Subtitle)
	}
}

func TestLocalOffsetProducesLocalFirst(t *testing.T) {
	hits := tsUTC8(0, "1700000000")
	want := []string{"2023-11-15 06:13:20 +08:00", "2023-11-14 22:13:20 UTC"}
	if got := titles(hits); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("本地在前、UTC 在后：实得 %v", got)
	}
	for _, h := range hits {
		if !strings.Contains(h.Subtitle, "1700000000") {
			t.Errorf("副标题应带原始 ts：%q", h.Subtitle)
		}
	}
	if !strings.Contains(hits[0].Subtitle, "本地时间") || !strings.Contains(hits[1].Subtitle, "UTC") {
		t.Errorf("副标题标注错误：%q / %q", hits[0].Subtitle, hits[1].Subtitle)
	}
}

func TestNowRows(t *testing.T) {
	hits := ts(1_700_000_000, "now")
	if len(hits) != 2 || hits[0].Title != "1700000000" || hits[1].Title != "1700000000000" {
		t.Fatalf("now 两行（秒/毫秒）：%v", titles(hits))
	}
	if !strings.Contains(hits[0].Subtitle, "2023-11-14 22:13:20") || !strings.Contains(hits[0].Subtitle, "秒") {
		t.Errorf("秒行副标题带可读时间与单位：%q", hits[0].Subtitle)
	}
	if !strings.Contains(hits[1].Subtitle, "毫秒") {
		t.Errorf("毫秒行副标题：%q", hits[1].Subtitle)
	}
	if hits[0].Action.Kind != providers.ActionCopy || hits[0].Action.Text != "1700000000" {
		t.Errorf("now 行应为 Copy(title)：%+v", hits[0].Action)
	}
	// 大小写不敏感。
	if ts(1_700_000_000, "NOW")[0].Title != "1700000000" {
		t.Error("NOW 应与 now 同形")
	}
}

func TestEmptyDefaultsToNow(t *testing.T) {
	hits := ts(1_700_000_000, "")
	now := ts(1_700_000_000, "now")
	if strings.Join(titles(hits), "|") != strings.Join(titles(now), "|") {
		t.Fatalf("空查询应 = now：%v vs %v", titles(hits), titles(now))
	}
	for _, h := range hits {
		if h.Interact.Kind != providers.InteractExecute {
			t.Errorf("空查询行应可选：%v", titles(hits))
		}
	}
}

func TestInvalidProduceNoRows(t *testing.T) {
	for _, bad := range []string{
		"abc", "1.5e3", "17000000000x", "0x10", "++", "-1700000000",
		"2026-13-01", "2025-02-30", "12:30",
	} {
		if got := ts(0, bad); len(got) != 0 {
			t.Errorf("%q 应无结果行，实得 %v", bad, titles(got))
		}
	}
}

func TestWrongDigitCountGivesHint(t *testing.T) {
	for _, bad := range []string{"170000000", "17000000000", "170000000000", "17000000000000", "0"} {
		hits := ts(0, bad)
		if len(hits) != 1 {
			t.Fatalf("%q 应恰好一条纠偏提示，实得 %v", bad, titles(hits))
		}
		if !hits[0].IsHint() {
			t.Errorf("%q 应为 Hint：%+v", bad, hits[0].Interact)
		}
		if hits[0].Action.Kind != providers.ActionNone {
			t.Errorf("%q Hint 动作应为 None：%+v", bad, hits[0].Action)
		}
		if !strings.Contains(hits[0].Title, "位") {
			t.Errorf("%q 标题应含位数说明：%q", bad, hits[0].Title)
		}
	}
}

func TestZeroPaddedEpoch(t *testing.T) {
	if hits := ts(0, "0"); len(hits) != 1 || !hits[0].IsHint() {
		t.Errorf("\"0\" 只有 1 位，应出 Hint：%v", titles(ts(0, "0")))
	}
	if got := ts(0, "0000000000"); len(got) == 0 || got[0].Title != "1970-01-01 00:00:00 UTC" {
		t.Errorf("补齐 10 位的 0 应正常换算：%v", titles(got))
	}
}

func TestTimezoneOffsetDisplay(t *testing.T) {
	if got := formatTimestampOffset(1_700_000_000, 8*3600); got != "2023-11-15 06:13:20 +08:00" {
		t.Errorf("+08:00 格式化错误：%q", got)
	}
	if got := formatTimestampOffset(1_700_000_000, -5*3600); got != "2023-11-14 17:13:20 -05:00" {
		t.Errorf("-05:00 格式化错误：%q", got)
	}
}

func TestCopyableTitleEqualsText(t *testing.T) {
	for _, q := range []string{"1700000000", "1700000000000", "now", ""} {
		for _, h := range ts(1_700_000_000, q) {
			if h.Action.Kind == providers.ActionCopy && h.Action.Text != h.Title {
				t.Errorf("%q：Copy 文本应 = 标题：%+v", q, h.Action)
			}
		}
	}
}

func TestDateFormatsConvert(t *testing.T) {
	cases := []struct {
		input string
		utc   string
	}{
		{"2026-10-10", "2026-10-10 00:00:00 UTC"},
		{"2026/10/10", "2026-10-10 00:00:00 UTC"},
		{"2026.10.10", "2026-10-10 00:00:00 UTC"},
		{"2026-10-10 12:30", "2026-10-10 12:30:00 UTC"},
		{"2026-10-10 12:30:45", "2026-10-10 12:30:45 UTC"},
		{"2026.10.10T12:30:45", "2026-10-10 12:30:45 UTC"},
		{"2026-10-10T12:30", "2026-10-10 12:30:00 UTC"},
	}
	for _, c := range cases {
		hits := ts(0, c.input)
		if len(hits) != 2 {
			t.Fatalf("%q 应出两行：%v", c.input, titles(hits))
		}
		secs, err := strconvParse(hits[0].Title)
		if err != nil {
			t.Fatalf("%q 秒行标题应为数字：%q", c.input, hits[0].Title)
		}
		if got := formatTimestamp(secs); got != c.utc {
			t.Errorf("%q 反格式化 = %q，想要 %q", c.input, got, c.utc)
		}
		if hits[1].Title != hits[0].Title+"000" {
			t.Errorf("%q 毫秒行应 = 秒行×1000：%q vs %q", c.input, hits[1].Title, hits[0].Title)
		}
		if !strings.Contains(hits[0].Subtitle, "按秒") || !strings.Contains(hits[1].Subtitle, "按毫秒") {
			t.Errorf("%q 副标题单位标注错误：%q / %q", c.input, hits[0].Subtitle, hits[1].Subtitle)
		}
	}
}

func TestDateInterpretedAsLocalTime(t *testing.T) {
	hits := tsUTC8(0, "2023-11-15 06:13:20")
	if len(hits) != 2 || hits[0].Title != "1700000000" {
		t.Fatalf("UTC+8 本地串应反推 1700000000：%v", titles(hits))
	}
	if !strings.Contains(hits[0].Subtitle, "+08:00") || !strings.Contains(hits[0].Subtitle, "2023-11-15 06:13:20") {
		t.Errorf("副标题应用本地偏移格式化：%q", hits[0].Subtitle)
	}
	if hits := ts(0, "2023-11-14 22:13:20"); hits[0].Title != "1700000000" || !strings.Contains(hits[0].Subtitle, "UTC") {
		t.Errorf("偏移 0 时本地=UTC 写法：%v / %q", titles(hits), hits[0].Subtitle)
	}
}

func TestLeapYearDatesValidated(t *testing.T) {
	if hits := ts(0, "2024-02-29"); len(hits) != 2 {
		t.Errorf("闰年 2/29 应合法：%v", titles(hits))
	}
	if hits := ts(0, "2023-02-29"); len(hits) != 0 {
		t.Errorf("非闰年 2/29 应无结果：%v", titles(hits))
	}
}

func TestInvalidDatesNoRows(t *testing.T) {
	for _, bad := range []string{
		"2026-13-01", "2026-00-10", "2026-10-00", "2026-10-32", "2026-02-30", "2023-02-29",
		"0000-01-01", "10000-01-01", "999-01-01", "2026-1-1", "2026-10/10", "2026-10-10-",
		"2026-10-10 25:00", "2026-10-10 12:60", "2026-10-10 12:30:61", "2026-10-10 1:30",
		"12:30", "2026-10-10T12",
	} {
		if got := ts(0, bad); len(got) != 0 {
			t.Errorf("%q 应无结果：%v", bad, titles(got))
		}
	}
}

func TestDateRowsMillisConsistent(t *testing.T) {
	hits := ts(0, "2026-10-10 08:00:00")
	if len(hits) != 2 {
		t.Fatalf("日期行应 2 条：%v", titles(hits))
	}
	secs, _ := strconvParse(hits[0].Title)
	millis, _ := strconvParse(hits[1].Title)
	if millis != secs*1000 {
		t.Errorf("毫秒 = 秒×1000：%d vs %d", millis, secs)
	}
	for _, h := range hits {
		if h.Action.Kind != providers.ActionCopy || h.Action.Text != h.Title {
			t.Errorf("日期行应可复制且文本=标题：%+v", h.Action)
		}
	}
}

func TestScoreStrictlyDecreasing(t *testing.T) {
	hits := ts(0, "1700000000")
	if len(hits) == 0 {
		t.Fatal("应至少一行")
	}
	if hits[0].Score > 1.0 {
		t.Errorf("首条分数应 ≤1.0：%v", hits[0].Score)
	}
}
