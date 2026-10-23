// 与 Rust 侧（voke-core + pinyin 0.11 + nucleo-matcher 0.3.1）的 1:1 语料比对。
//
// testdata/rust_corpus.tsv 由 Rust 侧真值程序生成（生成方式见
// docs/notes/matching.md）：
//   - K 行：name \t 去重后的 full（逗号连接） \t 去重后的 initials
//   - S 行：name \t query \t 原始分（u32，Pattern::score 求和）或 MISS
//
// 已知分歧（退化点分歧，§10-9）不含在逐分断言里：含分歧字的名字由
// TestDegradationDivergence 单独断言，其余条目必须逐分相等。
package match

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"testing"
)

// corpusPath 是 Rust 真值语料。
const corpusPath = "testdata/rust_corpus.tsv"

// loadCorpus 读取 Rust 真值语料。
func loadCorpus(t *testing.T) (keys map[string][2]string, scores []corpusScoreRow) {
	t.Helper()
	f, err := os.Open(corpusPath)
	if err != nil {
		t.Fatalf("打开语料失败: %v", err)
	}
	defer f.Close()
	keys = make(map[string][2]string)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		parts := strings.Split(sc.Text(), "\t")
		switch parts[0] {
		case "K":
			if len(parts) != 4 {
				t.Fatalf("K 行格式错误: %q", sc.Text())
			}
			keys[parts[1]] = [2]string{parts[2], parts[3]}
		case "S":
			if len(parts) != 4 {
				t.Fatalf("S 行格式错误: %q", sc.Text())
			}
			var raw uint64
			if parts[3] != "MISS" {
				v, err := strconv.ParseUint(parts[3], 10, 32)
				if err != nil {
					t.Fatalf("S 行分数解析失败: %q", sc.Text())
				}
				raw = v
			}
			scores = append(scores, corpusScoreRow{name: parts[1], query: parts[2], raw: raw, miss: parts[3] == "MISS"})
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("读取语料失败: %v", err)
	}
	return keys, scores
}

type corpusScoreRow struct {
	name  string
	query string
	raw   uint64
	miss  bool
}

// TestKeysMatchRustCorpus 逐个名字比对 SearchKeys 的 full / initials
// （Rust 侧已去重，Go 侧同样去重后比较）。
// 含退化分歧字（pinyin_test.go 的 divergenceChars）的名字跳过，
// 由 TestDegradationDivergence 单独断言。
func TestKeysMatchRustCorpus(t *testing.T) {
	rustKeys, _ := loadCorpus(t)
	if len(rustKeys) == 0 {
		t.Fatal("语料为空")
	}
	for name, want := range rustKeys {
		if nameHasDivergenceChar(name) {
			continue
		}
		k := NewSearchKeys(name)
		gotFull := strings.Join(dedupStrings(k.Full), ",")
		gotInitials := strings.Join(dedupStrings(k.Initials), ",")
		if gotFull != want[0] {
			t.Errorf("%q full:\n  go   = %q\n  rust = %q", name, gotFull, want[0])
		}
		if gotInitials != want[1] {
			t.Errorf("%q initials:\n  go   = %q\n  rust = %q", name, gotInitials, want[1])
		}
	}
}

// TestScoresMatchRustCorpus 逐个 (name, query) 比对命中与否与原始分。
// 含退化分歧字的名字跳过（见 TestDegradationDivergence 与 §10-9）。
func TestScoresMatchRustCorpus(t *testing.T) {
	_, scores := loadCorpus(t)
	if len(scores) == 0 {
		t.Fatal("语料为空")
	}
	m := NewMatcher()
	var checked, skipped int
	for _, row := range scores {
		if nameHasDivergenceChar(row.name) {
			skipped++
			continue
		}
		k := NewSearchKeys(row.name)
		got, ok := rawScoreEntry(m, row.query, k)
		checked++
		switch {
		case row.miss && ok:
			t.Errorf("%q + %q: Rust MISS, Go raw=%d", row.name, row.query, got)
		case !row.miss && !ok:
			t.Errorf("%q + %q: Rust raw=%d, Go MISS", row.name, row.query, row.raw)
		case !row.miss && ok && uint64(got) != row.raw:
			t.Errorf("%q + %q: Rust raw=%d, Go raw=%d", row.name, row.query, row.raw, got)
		}
	}
	t.Logf("语料 %d 条，逐分断言 %d 条，退化分歧字跳过 %d 条", len(scores), checked, skipped)
}

// rawScoreEntry 复刻 score_entry 的取分逻辑但返回未归一化的原始分，
// 便于与 Rust 的 Pattern::score 逐位比对（键集与 Rust 相同：原名 / 全拼 / 首字母）。
func rawScoreEntry(m *Matcher, query string, keys *SearchKeys) (uint32, bool) {
	if query == "" {
		return 0, false
	}
	p, ok := buildPattern(query)
	if !ok {
		return 0, false
	}
	var (
		best  uint32
		found bool
	)
	for _, k := range keys.ASCIIKeys() {
		if k == "" {
			continue
		}
		raw, ok := p.score(m, []rune(k))
		if !ok {
			continue
		}
		if !found || raw > best {
			best = raw
			found = true
		}
	}
	return best, found
}

func dedupStrings(s []string) []string {
	out := make([]string, 0, len(s))
	for _, x := range s {
		if !containsString(out, x) {
			out = append(out, x)
		}
	}
	return out
}
