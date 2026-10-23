// rank 内核测试：钉死「档位是加分、使用加成有界、只能跨一档」的排序口径。
package rank

import (
	"math"
	"testing"

	"voke/internal/logic/frequency"
	"voke/internal/match"
)

func TestTierBonusIsEvenlySpaced(t *testing.T) {
	want := map[match.MatchQuality]float64{
		match.QualityFuzzy:     0,
		match.QualitySubstring: 0.10,
		match.QualityAcronym:   0.20,
		match.QualityPrefix:    0.30,
		match.QualityExact:     0.40,
	}
	for q, w := range want {
		if got := TierBonus(q); math.Abs(got-w) > 1e-9 {
			t.Errorf("TierBonus(%d) = %v，想要 %v", q, got, w)
		}
	}
}

// TestUsageCrossesOneTierOnly：同样的 nucleo 分下，
//   - 相邻档：满额使用（0.12）能反超（0.10 档差）；
//   - 跨两档：任何次数都反超不了（0.20 档差）。
func TestUsageCrossesOneTierOnly(t *testing.T) {
	const score = 0.5
	prefix := Signal{Quality: match.QualityPrefix, Score: score, Weight: 1}
	exact := Signal{Quality: match.QualityExact, Score: score, Weight: 1}
	acronym := Signal{Quality: match.QualityAcronym, Score: score, Weight: 1}

	// 相邻档：Prefix + 满额使用 vs Exact + 0。
	usedPrefix := prefix
	usedPrefix.Usage = frequency.UsageCap
	if !(usedPrefix.Final() > exact.Final()) {
		t.Errorf("满额使用应能顶掉相邻一档：%v !> %v", usedPrefix.Final(), exact.Final())
	}

	// 跨两档：Acronym + 满额使用 vs Exact + 0 —— 反超不了。
	usedAcronym := acronym
	usedAcronym.Usage = frequency.UsageCap
	if usedAcronym.Final() >= exact.Final() {
		t.Errorf("使用加成不得跨两档：%v !< %v", usedAcronym.Final(), exact.Final())
	}
}

// TestUsageIsBounded 守住「使用习惯不能把散点命中顶到精确命中之前」——
// 也就是当初用户报的「搜 gma 时一堆乱条目排在 Gmail 前面」。
func TestUsageIsBounded(t *testing.T) {
	gmail := Signal{Quality: match.QualityPrefix, Score: 0.44, Weight: 1}
	junk := Signal{Quality: match.QualityFuzzy, Score: 0.44, Weight: 1, Usage: frequency.UsageCap}
	if junk.Final() >= gmail.Final() {
		t.Errorf("乱条目 %v 不应压过 Gmail %v", junk.Final(), gmail.Final())
	}
}

func TestWeightScalesEverything(t *testing.T) {
	a := Signal{Quality: match.QualityFuzzy, Score: 0.5, Weight: 2}
	b := Signal{Quality: match.QualityFuzzy, Score: 0.5, Weight: 1}
	if !Less(a, b) {
		t.Errorf("权重高者应在前：%v !> %v", a.Final(), b.Final())
	}
}

func TestSortStableByFinal(t *testing.T) {
	items := []Signal{
		{Quality: match.QualityFuzzy, Score: 0.4, Weight: 1},
		{Quality: match.QualityPrefix, Score: 0.4, Weight: 1},
		{Quality: match.QualityExact, Score: 0.4, Weight: 1},
		{Quality: match.QualityFuzzy, Score: 0.2, Weight: 1},
	}
	SortBy(items, func(s Signal) float64 { return s.Final() })
	want := []match.MatchQuality{match.QualityExact, match.QualityPrefix, match.QualityFuzzy, match.QualityFuzzy}
	for i, w := range want {
		if items[i].Quality != w {
			t.Fatalf("第 %d 位 = %d，期望 %d（整体 %+v）", i, items[i].Quality, w, items)
		}
	}
	// 同分保持原序：最后两位都是 Fuzzy，Score 0.4 在前。
	if items[2].Score != 0.4 || items[3].Score != 0.2 {
		t.Errorf("同档应按分数降序：%v / %v", items[2].Score, items[3].Score)
	}
}

func TestUsageOfNilAndStore(t *testing.T) {
	if got := UsageOf(nil, "apps", "微信"); got != 0 {
		t.Errorf("nil 表应返回 0，实得 %v", got)
	}
	freq := frequency.Store{frequency.Key("apps", "微信"): 5}
	if got, want := UsageOf(freq, "apps", "微信"), frequency.UsageBonus(5); got != want {
		t.Errorf("UsageOf = %v，想要 %v", got, want)
	}
}
