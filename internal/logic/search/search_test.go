// search.FilterApps 接缝测试：真实引擎（internal/match）接管后的集成语义。
// 内部引擎的正确性由 internal/match 单测 + Rust 真值语料逐分断言保证
// （PoC 文档 §8.7）；这里只验「接缝接线 + 生产行为口径」：空查询全量、
// 三路命中、多音字双读、ASCII 原名、降序稳定排序、50 条截断
// （逐条对照 voke-core/src/apps.rs 的 AppsProvider 行为）。
//
// 这些用例随 filterApps 从 view_test.go（package main）原样搬来（§7.2 步骤3）：
// 直接测生产函数 FilterApps，不另抄第二份实现（§10.1.1）；条目构造走共享的
// testutil.MkApps（main 侧 row_visual_test / panel_behavior_test 亦用同一份）。
package search

import (
	"fmt"
	"testing"

	"voke/internal/logic/appsmodel"
	"voke/internal/logic/frequency"
	"voke/internal/logic/testutil"
)

// entryNames 把条目列表映射成名字切片，供断言失败时打印（纯测试辅助）。
func entryNames(entries []appsmodel.AppEntry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Name
	}
	return out
}

// TestFilterAppsEmptyQueryReturnsAll：空查询 / 纯空白返回全量且顺序不变
// （生产 apps.rs：空查询 score 恒 1.0、不限流）。
func TestFilterAppsEmptyQueryReturnsAll(t *testing.T) {
	in := testutil.MkApps("微信", "Visual Studio Code", "音乐")
	for _, q := range []string{"", "   ", "\t"} {
		got := FilterApps(q, in, nil)
		if len(got) != len(in) {
			t.Fatalf("FilterApps(%q) 应返回全量 %d 条，实际 %d：%v", q, len(in), len(got), entryNames(got))
		}
		for i := range in {
			if got[i].Name != in[i].Name {
				t.Fatalf("FilterApps(%q) 顺序被改动：%v", q, entryNames(got))
			}
		}
	}
}

// TestFilterAppsMatchPaths：V2 匹配语义四件套在接缝层的体现——
// 首字母串 / 全拼 / 汉字原名三路命中，多音字两个读音都可命中，
// ASCII 原名可被子序列查询命中，不相关查询不命中。
func TestFilterAppsMatchPaths(t *testing.T) {
	in := testutil.MkApps("微信", "网易云音乐", "Visual Studio Code", "计算器")
	cases := []struct {
		query string
		want  string // 期望唯一命中的名字
	}{
		{"wx", "微信"},                  // 首字母串
		{"weixin", "微信"},              // 全拼
		{"微信", "微信"},                  // 汉字原名
		{"yinyue", "网易云音乐"},           // 多音字读音一（乐=yue）
		{"yinle", "网易云音乐"},            // 多音字读音二（乐=le）
		{"vsc", "Visual Studio Code"}, // ASCII 原名（子序列）
		{"jsq", "计算器"},                // 首字母串
	}
	for _, c := range cases {
		got := FilterApps(c.query, in, nil)
		if len(got) != 1 || got[0].Name != c.want {
			t.Errorf("FilterApps(%q) = %v, 期望唯一命中 %q", c.query, entryNames(got), c.want)
		}
	}
	if got := FilterApps("zzzqqqxxx", in, nil); len(got) != 0 {
		t.Errorf("不相关查询应无命中，实际 %v", entryNames(got))
	}
}

// TestFilterAppsSortedByScoreDescending：按分降序——键与 query 全等的
// 条目排在仅前缀命中之前（nucleo 精确匹配路径分高）。
func TestFilterAppsSortedByScoreDescending(t *testing.T) {
	in := testutil.MkApps("weixin", "weixinfuwu")
	got := FilterApps("weixin", in, nil)
	if len(got) != 2 {
		t.Fatalf("应命中 2 条，实际 %d：%v", len(got), entryNames(got))
	}
	if got[0].Name != "weixin" || got[1].Name != "weixinfuwu" {
		t.Fatalf("排序错误：%v", entryNames(got))
	}
}

// TestFilterAppsTruncatesAndStable：超过上限的命中截断到 50 条；全同等分时
// 稳定排序保持枚举序（对照生产 apps.rs:249-254：先排序后截断、Rust sort_by
// 为稳定排序）。期望值取绝对值 50（= 生产 AppsProvider::MAX_RESULTS），
// 与 matchMaxResults 常量解耦，确保「上限就是 50」这条被真正钉住。
func TestFilterAppsTruncatesAndStable(t *testing.T) {
	const wantMax = 50
	in := make([]appsmodel.AppEntry, 0, wantMax+10)
	for i := 0; i < wantMax+10; i++ {
		in = append(in, appsmodel.AppEntry{Name: fmt.Sprintf("App%02d", i), Path: fmt.Sprintf("p%d", i), ID: fmt.Sprintf("%d", i)})
	}
	got := FilterApps("app", in, nil)
	if len(got) != wantMax {
		t.Fatalf("应截断到 %d 条，实际 %d", wantMax, len(got))
	}
	for i, e := range got {
		if want := fmt.Sprintf("App%02d", i); e.Name != want {
			t.Fatalf("同分应按枚举序，第 %d 条 = %q，期望 %q", i, e.Name, want)
		}
	}
}

// TestFilterAppsEmptyOrdersByFrequency：空查询也吃频率 rank（对照生产空查询全量
// score=1.0 → engine.rank 的 frequency_boost）。给「音乐」记大量使用，它应浮到榜首，
// 其余按枚举序稳定保留。
func TestFilterAppsEmptyOrdersByFrequency(t *testing.T) {
	in := testutil.MkApps("微信", "Visual Studio Code", "音乐")
	freq := frequency.Store{
		frequency.Key(appsmodel.ProviderID, "音乐"): 20,
	}
	got := entryNames(FilterApps("", in, freq))
	want := []string{"音乐", "微信", "Visual Studio Code"} // 高频顶到最前；其余同 boost(0)=1.0，稳定保枚举序
	if !eqNames(got, want) {
		t.Fatalf("空查询按频率排序：得 %v，想要 %v", got, want)
	}
	// 对照：nil 频率表 → 不改动顺序（与引入 Freq 之前逐字一致，守零回归）。
	if g2 := entryNames(FilterApps("", in, nil)); !eqNames(g2, entryNames(in)) {
		t.Fatalf("nil 频率表空查询应保持枚举序，得 %v", g2)
	}
}

// TestFilterAppsUsageCrossesOneTier：使用加成是**有界**的——少量使用翻不动相邻一档，
// 长期高频（封顶 0.12 > 档差 0.10）才翻得动，且只翻得动一档。
// 用 weixin（全等档）与 weixinfuwu（前缀档，分相同）。
func TestFilterAppsUsageCrossesOneTier(t *testing.T) {
	in := testutil.MkApps("weixin", "weixinfuwu")
	// 无频率：档位高的 weixin 在前。
	if got := entryNames(FilterApps("weixin", in, nil)); got[0] != "weixin" {
		t.Fatalf("nil 频率应 weixin 在前，得 %v", got)
	}
	// 只记几次：翻不动（0.10 的档差 > 几次积累的加成）。
	few := frequency.Store{frequency.Key(appsmodel.ProviderID, "weixinfuwu"): 3}
	if got := entryNames(FilterApps("weixin", in, few)); got[0] != "weixin" {
		t.Fatalf("少量使用不该翻过档位差，得 %v", got)
	}
	// 长期高频：加成封顶 0.12，跨过 0.10 的档差。
	many := frequency.Store{frequency.Key(appsmodel.ProviderID, "weixinfuwu"): 1000}
	if got := entryNames(FilterApps("weixin", in, many)); got[0] != "weixinfuwu" {
		t.Fatalf("长期高频应把 weixinfuwu 顶到首位，得 %v", got)
	}
	// 但跨不了两档：再加一个模糊档的高频条目也上不去。
	in2 := testutil.MkApps("weixin", "wx")
	junk := frequency.Store{frequency.Key(appsmodel.ProviderID, "wx"): 1000000}
	if got := entryNames(FilterApps("weixin", in2, junk)); len(got) == 2 && got[0] != "weixin" {
		t.Fatalf("使用加成不得跨两档，得 %v", got)
	}
}

// TestFilterAppsPrefixBeatsFuzzy：前缀命中（Gmail）必须排在只有散点命中的条目
// 之前——这是用户视角的「搜 gma 时一堆乱七八糟的应用挤在 gmail 前面」。
func TestFilterAppsPrefixBeatsFuzzy(t *testing.T) {
	in := testutil.MkApps("GlobalMarket App", "Google Mail", "Gamma", "Gmail")
	got := entryNames(FilterApps("gma", in, nil))
	if len(got) != 4 {
		t.Fatalf("应命中 4 条，实际 %d：%v", len(got), got)
	}
	if got[0] != "Gmail" {
		t.Fatalf("Gmail 应排首位，得 %v", got)
	}
}

// TestFilterAppsAcronymRanking：拉丁文名字的词首缩写命中（vsc → Visual Studio
// Code）走 Acronym 档——压过「仅包含」命中，但排在「名字以查询开头」之后。
func TestFilterAppsAcronymRanking(t *testing.T) {
	in := testutil.MkApps("AVSC Tool", "Visual Studio Code", "VSCode Helper")
	got := entryNames(FilterApps("vsc", in, nil))
	want := []string{"VSCode Helper", "Visual Studio Code", "AVSC Tool"}
	if !eqNames(got, want) {
		t.Fatalf("得 %v，想要 %v", got, want)
	}
}

// TestFilterAppsNoUsageKeepsRankDescending：没有任何使用记录时，结果必须按匹配分
// 降序。这条守的是排序比较方向——方向写反时整个列表会被倒过来（曾经的真实 bug）。
func TestFilterAppsNoUsageKeepsRankDescending(t *testing.T) {
	in := testutil.MkApps("Google Mail", "Gamma") // 同为模糊档，前者原始分更高
	got := entryNames(FilterApps("gma", in, nil))
	want := []string{"Google Mail", "Gamma"}
	if !eqNames(got, want) {
		t.Fatalf("得 %v，想要 %v", got, want)
	}
}

// eqNames 比较两个名字切片是否逐位相等（纯测试辅助）。
func eqNames(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
