// match 包的对外匹配 API，对应 voke-core/src/matcher.rs。
//
// 与 Rust 参考实现的差异只有一处：MatchScore 多带一个 Quality 档位
// （见 quality.go）。排序分怎么由「档位 + 分值 + 权重 + 使用习惯」合成，
// 不在这里实现——那是 internal/logic/rank 的唯一职责。
// nucleo 的评分公式本身仍是 1:1 移植，语料断言的分值不受影响。
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

// MatchQuality 是候选相对查询串的匹配形态分级，越高越贴题。
//
// 为什么在 nucleo 分之外单列一档：nucleo 只给连续/边界加分，档位之间的分差很小
// （"gma" 对 "gmail" 0.44，对只是散点命中的长名字也有 0.35–0.40），单靠它排序会
// 让「不相干的条目」和「精确命中」挤在一起。主流启动器（Wox / Flow Launcher /
// Raycast）都按「全等 > 前缀 > 缩写 > 包含 > 模糊」分档。
//
// 档位在排序里是**加分**而不是主键（换算见 internal/logic/rank 的 TierBonus）。
type MatchQuality uint8

const (
	// QualityFuzzy：仅子序列命中（"gma" 命中 "Gamma"）。
	QualityFuzzy MatchQuality = iota
	// QualitySubstring：查询是某个搜索键的连续子串（"code" 命中 "vscode"）。
	QualitySubstring
	// QualityAcronym：查询由候选名的若干词首按序拼成（"vsc" / "vscode" /
	// "vs code" 命中 "Visual Studio Code"；判定见 quality.go 的 acronymMatch）。
	// 词首在名字里并不连续，是间接信号，排在有名字前缀命中的条目之后。
	QualityAcronym
	// QualityPrefix：某个搜索键以查询开头（"gma" 命中 "gmail"）。
	QualityPrefix
	// QualityExact：查询与某个搜索键全等（"gmail" 命中 "gmail"；
	// 拼音首字母键同理——"wx" 与「微信」的首字母键 "wx" 全等）。
	QualityExact
)

// MatchScore 对应 matcher.rs 的 MatchScore（外加 quality 档位）。
type MatchScore struct {
	// Score 是归一化到 0.0–1.0 的 nucleo 匹配分，0 表示未命中。
	Score float32
	// Quality 是命中键里最好的匹配形态（全等 / 前缀 / 包含 / 模糊）。
	Quality MatchQuality
}

// scoreSaturation 对应 matcher.rs 的 SCORE_SATURATION。
//
// Rust 侧取值依据：完整匹配约 166（"weixin" vs "weixin"）、首字母串命中约 62
// （"wx" vs "wx"）、弱匹配约 48；取 200 让满匹配接近 1.0 同时保留区分度。
// 本包 1:1 移植了 nucleo 的评分公式，原始分量级不变（用 voke-core 实测语料
// 逐条断言相等，见 matcher_test.go），因此标定值维持 200.0。
// 重新标定的逻辑与结论记录在 docs/notes/matching.md。
const scoreSaturation float32 = 200.0

// Query 是编译好的查询串（pattern 的一次性解析结果），对多条候选复用。
//
// 为什么需要：一次按键要对上千条候选打分。若每条候选都重新编译查询
// （切词 + 转 rune + 建 atom），编译开销会比真正的打分还大——实测 1160 条候选，
// 每候选重编译 399µs / 5.6k 次分配，复用则 235µs / 120 次。
//
// Query 内部复用一块 haystack 缓冲，**不是并发安全的**（与 Matcher 一致）。
type Query struct {
	p   *pattern
	buf []rune
}

// Compile 解析查询串；空串 / 纯空白不产生 atom → ok=false。
func Compile(query string) (*Query, bool) {
	if query == "" {
		return nil, false
	}
	p, ok := buildPattern(query)
	if !ok {
		return nil, false
	}
	return &Query{p: p}, true
}

// ScoreEntry 用已编译查询给一条候选打分：遍历它的搜索键，取「档位最高、
// 其次分值最高」的那条。空键跳过（score_one 对空 haystack 不打分）。
func (q *Query) ScoreEntry(m *Matcher, keys *SearchKeys) (MatchScore, bool) {
	var (
		best  MatchScore
		found bool
	)
	for _, k := range keys.refs {
		if k.text == "" {
			continue
		}
		q.buf = appendRunes(q.buf[:0], k.text)
		raw, ok := q.p.score(m, q.buf)
		if !ok {
			continue
		}
		cand := MatchScore{
			Score:   clamp01(float32(raw) / scoreSaturation),
			Quality: q.p.quality(k.text, k.lower),
		}
		// 先比档位、再比分值；两者都相等时保留先到的键（原名排在拼音键之前）。
		if !found {
			best, found = cand, true
			continue
		}
		best = Best(best, cand)
	}
	if !found {
		return MatchScore{}, false
	}
	return best, true
}

// ScoreEntry 对应 matcher.rs 的 score_entry：一次性调用的便捷包装
// （每次都会编译查询——批量打分请用 Compile + Query.ScoreEntry）。
//
// query 为空 / 纯空白（不产生 atom）时返回 ok=false；
// ASCII query 对全部 ASCII 键取「档位最高、其次分值最高」的一条，汉字 query 只对原名匹配。
func ScoreEntry(m *Matcher, query string, keys *SearchKeys) (MatchScore, bool) {
	q, ok := Compile(query)
	if !ok {
		return MatchScore{}, false
	}
	return q.ScoreEntry(m, keys)
}

// appendRunes 把 s 逐字符追加到 buf（不新增分配，容量不够时按 append 扩容）。
func appendRunes(buf []rune, s string) []rune {
	for _, r := range s {
		buf = append(buf, r)
	}
	return buf
}

// Best 返回两条匹配分里「档位更高、其次分更高」的那条；
// 档位相同且分也相同 → 返回 a（对应 Rust fold 的「并列不替换」）。
func Best(a, b MatchScore) MatchScore {
	if b.Quality > a.Quality || (b.Quality == a.Quality && b.Score > a.Score) {
		return b
	}
	return a
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
