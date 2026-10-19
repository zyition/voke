// match 包的对外匹配 API，1:1 对应 voke-core/src/matcher.rs。
package match

// Matcher 是模糊匹配器。对应 nucleo_matcher::Matcher：
// 调用方持有并跨查询复用（内部只有配置，无跨调用状态；
// nucleo 的 135KB scratch slab 在 Go 侧按次分配，语义等价）。
//
// Matcher 不是并发安全的（与 Rust 侧一致：Atom::score 会覆写内部配置）。
type Matcher struct {
	cfg matcherConfig
}

// NewMatcher 对应 Matcher::new(Config::DEFAULT)。
func NewMatcher() *Matcher {
	return &Matcher{cfg: defaultMatcherConfig()}
}

// MatchScore 对应 matcher.rs 的 MatchScore。
type MatchScore struct {
	// Score 是归一化到 0.0–1.0 的匹配分，0 表示未命中。
	Score float32
}

// scoreSaturation 对应 matcher.rs 的 SCORE_SATURATION。
//
// Rust 侧取值依据：完整匹配约 166（"weixin" vs "weixin"）、首字母串命中约 62
// （"wx" vs "wx"）、弱匹配约 48；取 200 让满匹配接近 1.0 同时保留区分度。
// 本包 1:1 移植了 nucleo 的评分公式，原始分量级不变（用 voke-core 实测语料
// 逐条断言相等，见 matcher_test.go），因此标定值维持 200.0。
// 重新标定的逻辑与结论记录在 docs/notes/voke-go-poc.md §10-8。
const scoreSaturation float32 = 200.0

// ScoreEntry 对应 matcher.rs 的 score_entry。
//
// query 为空 / 纯空白（不产生 atom）时返回 ok=false；
// ASCII query 对全部 ASCII 键取最高分，汉字 query 只对原名匹配。
func ScoreEntry(m *Matcher, query string, keys *SearchKeys) (MatchScore, bool) {
	if query == "" {
		return MatchScore{}, false
	}
	p, ok := buildPattern(query)
	if !ok {
		return MatchScore{}, false
	}
	var (
		best  float32
		found bool
	)
	for _, k := range keys.ASCIIKeys() {
		if k == "" {
			continue // score_one：空 haystack 不打分
		}
		raw, ok := p.score(m, []rune(k))
		if !ok {
			continue
		}
		s := clamp01(float32(raw) / scoreSaturation)
		// Rust fold：并列时保留先到的（prev >= s 不替换）。
		if !found || s > best {
			best = s
			found = true
		}
	}
	if !found {
		return MatchScore{}, false
	}
	return MatchScore{Score: best}, true
}

// Entry 对应 score_entries 的 (String, SearchKeys) 条目。
type Entry struct {
	ID   string
	Keys *SearchKeys
}

// ScoredEntry 对应 score_entries 返回的 (String, MatchScore)。
type ScoredEntry struct {
	ID    string
	Score MatchScore
}

// ScoreEntries 对应 matcher.rs 的 score_entries：一次性建 matcher 批量打分。
func ScoreEntries(entries []Entry, query string) []ScoredEntry {
	m := NewMatcher()
	var hits []ScoredEntry
	for _, e := range entries {
		if s, ok := ScoreEntry(m, query, e.Keys); ok {
			hits = append(hits, ScoredEntry{ID: e.ID, Score: s})
		}
	}
	return hits
}

// buildPattern 对应 matcher.rs 的 build_pattern：
// ASCII query 走 Fuzzy atom，汉字 query 走 Substring atom；
// 空查询或纯空白不产生 atom，返回 ok=false。
func buildPattern(query string) (*pattern, bool) {
	kind := atomKindSubstring
	if !HasNonASCII(query) {
		kind = atomKindFuzzy
	}
	p := newPattern(query, kind)
	if len(p.atoms) == 0 {
		return nil, false
	}
	return p, true
}

// clamp01 对应 f32::clamp(0.0, 1.0)。
func clamp01(v float32) float32 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
