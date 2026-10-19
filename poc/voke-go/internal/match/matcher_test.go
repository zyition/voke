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
