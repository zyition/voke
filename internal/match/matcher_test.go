// matcher.rs 测试的 1:1 移植（Rust 8 个用例）。
package match

import "testing"

func keys(name string) *SearchKeys {
	return NewSearchKeys(name)
}

func TestMatchesByPinyinInitials(t *testing.T) {
	k := keys("微信")
	m := NewMatcher()
	if _, ok := ScoreEntry(m, "wx", k); !ok {
		t.Error(`"wx" 应命中 微信`)
	}
}

func TestMatchesByFullPinyin(t *testing.T) {
	k := keys("微信")
	m := NewMatcher()
	if _, ok := ScoreEntry(m, "weixin", k); !ok {
		t.Error(`"weixin" 应命中 微信`)
	}
}

func TestMatchesByHanzi(t *testing.T) {
	k := keys("微信")
	m := NewMatcher()
	if _, ok := ScoreEntry(m, "微信", k); !ok {
		t.Error(`"微信" 应命中 微信`)
	}
}

// 「音乐」两个读音（yinyue / yinle）都应可命中。
func TestHeteronymBothReadingsMatch(t *testing.T) {
	k := keys("音乐")
	m := NewMatcher()
	if _, ok := ScoreEntry(m, "yinyue", k); !ok {
		t.Error(`"yinyue" 应命中 音乐`)
	}
	if _, ok := ScoreEntry(m, "yinle", k); !ok {
		t.Error(`"yinle" 应命中 音乐`)
	}
}

func TestEmptyQueryMatchesNothing(t *testing.T) {
	k := keys("微信")
	m := NewMatcher()
	if _, ok := ScoreEntry(m, "", k); ok {
		t.Error("空查询应返回未命中")
	}
}

func TestUnrelatedQueryMisses(t *testing.T) {
	k := keys("微信")
	m := NewMatcher()
	if _, ok := ScoreEntry(m, "zzzqqqxxx", k); ok {
		t.Error(`"zzzqqqxxx" 应未命中`)
	}
}

// 前缀式输入应比散布输入分高（nucleo 连续加分）。
func TestPrefixScoresHigherThanScattered(t *testing.T) {
	k := keys("网易云音乐")
	m := NewMatcher()
	prefix, ok := ScoreEntry(m, "wyyy", k)
	if !ok {
		t.Fatal(`"wyyy" 应命中`)
	}
	scattered, ok := ScoreEntry(m, "wyy", k)
	if !ok {
		t.Fatal(`"wyy" 应命中`)
	}
	if !(prefix.Score > scattered.Score) {
		t.Errorf("%v 应大于 %v", prefix.Score, scattered.Score)
	}
}

// 匹配形态分档：全等 > 前缀 > 词首序列（Acronym）> 包含 > 子序列。
// 档位在排序里是加分（见 internal/logic/rank 的 TierBonus），分值只在档内起作用。
func TestQualityTiers(t *testing.T) {
	m := NewMatcher()
	cases := []struct {
		query, name string
		want        MatchQuality
	}{
		{"gmail", "Gmail", QualityExact},                  // 原名全等
		{"gma", "Gmail", QualityPrefix},                   // 原名前缀
		{"mail", "Gmail", QualitySubstring},               // 原名包含（单词名字没有词首序列）
		{"gma", "Gamma", QualityFuzzy},                    // 只有子序列
		{"wx", "微信", QualityExact},                        // 首字母键全等
		{"weix", "微信", QualityPrefix},                     // 全拼键前缀
		{"eixi", "微信", QualitySubstring},                  // 全拼键包含
		{"vsc", "Visual Studio Code", QualityAcronym},     // 三个词首
		{"vscode", "Visual Studio Code", QualityAcronym},  // 词首 + 续写
		{"vs code", "Visual Studio Code", QualityAcronym}, // 整串去空格后按词首拼出
		{"code", "Visual Studio Code", QualityAcronym},    // 词首命中（第 3 个词）
		{"gma", "Google Mail", QualityAcronym},            // g + ma
		{"vsc", "VSCode Helper", QualityPrefix},           // 名字前缀压过缩写
	}
	for _, c := range cases {
		s, ok := ScoreEntry(m, c.query, NewSearchKeys(c.name))
		if !ok {
			t.Errorf("%q + %q 未命中", c.query, c.name)
			continue
		}
		if s.Quality != c.want {
			t.Errorf("%q + %q 档位 = %d，期望 %d", c.query, c.name, s.Quality, c.want)
		}
	}
}

// 多词查询不再一律掉档：整串判定负责「本来就是一个前缀」的输入。
func TestMultiWordQueryKeepsQuality(t *testing.T) {
	m := NewMatcher()
	s, ok := ScoreEntry(m, "visual studio", NewSearchKeys("Visual Studio 2022"))
	if !ok {
		t.Fatal("visual studio 应命中 Visual Studio 2022")
	}
	if s.Quality != QualityPrefix {
		t.Errorf("档位 = %d，期望 %d（整串是前缀，不该被逐词判定压成子串）", s.Quality, QualityPrefix)
	}
}

// 前缀命中必须压过只是词首拼出的命中——这是「gma 时 gmail 排第一」的直接依据。
func TestPrefixBeatsAcronym(t *testing.T) {
	m := NewMatcher()
	prefix, ok := ScoreEntry(m, "gma", NewSearchKeys("Gmail"))
	if !ok {
		t.Fatal("gma 应命中 Gmail")
	}
	acronym, ok := ScoreEntry(m, "gma", NewSearchKeys("Google Mail"))
	if !ok {
		t.Fatal("gma 应命中 Google Mail")
	}
	if !(prefix.Quality > acronym.Quality) {
		t.Errorf("Gmail 档位 %d 应高于 Google Mail 的 %d", prefix.Quality, acronym.Quality)
	}
}

func TestBatchScoresEntries(t *testing.T) {
	entries := []Entry{
		{ID: "wechat", Keys: keys("微信")},
		{ID: "qq", Keys: keys("QQ音乐")},
	}
	hits := ScoreEntries(entries, "wx")
	if len(hits) != 1 {
		t.Fatalf("hits = %d, 期望 1", len(hits))
	}
	if hits[0].ID != "wechat" {
		t.Errorf("hits[0].ID = %q, 期望 wechat", hits[0].ID)
	}
}
