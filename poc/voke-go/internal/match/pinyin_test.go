// 拼音数据层测试：go-pinyin 输出与 Rust pinyin 0.11 的对齐，
// 以及两包数据已知差异的可执行记录（差异分析见 docs/notes/matching.md）。
package match

import "testing"

// TestReadingsAlignWithRustPinyin 抽查字级读音与 Rust pinyin 0.11 一致
// （go-pinyin 的 v/组合符/预组合声调已在封装层归一化；Rust 多音字表的
// 重复读音在 Go 侧去重，如「中」「女」）。
func TestReadingsAlignWithRustPinyin(t *testing.T) {
	cases := []struct {
		r    rune
		want []string
	}{
		{'微', []string{"wei"}},
		{'信', []string{"xin", "shen"}},
		{'音', []string{"yin"}},
		{'乐', []string{"le", "yue"}},
		{'重', []string{"zhong", "chong", "tong"}},
		{'中', []string{"zhong"}},
		{'女', []string{"nü", "ru"}},
		{'绿', []string{"lü", "lu"}},
		{'律', []string{"lü"}},
		{'略', []string{"lüe"}},
		// ê 系列：go-pinyin 的 phoneticSymbol 缺 ê 声调，封装层回落。
		{'欸', []string{"ai", "ê", "xie", "ei"}},
		{'誒', []string{"ei", "xi", "yi", "ê"}},
		// 无拼音数据的字符返回 nil。
		{'Z', nil},
		{'1', nil},
		{'！', nil},
	}
	for _, c := range cases {
		got := readings(c.r)
		if len(got) != len(c.want) {
			t.Errorf("readings(%q) = %v, 期望 %v", c.r, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("readings(%q) = %v, 期望 %v", c.r, got, c.want)
				break
			}
		}
	}
}

// TestFirstReadingAlignsWithRustPinyin 对应 Rust 的 ToPinyin（首读音）。
func TestFirstReadingAlignsWithRustPinyin(t *testing.T) {
	cases := []struct {
		r    rune
		want string
	}{
		{'微', "wei"},
		{'信', "xin"},
		{'乐', "le"},
		{'重', "zhong"},
		{'绿', "lü"},
		{'律', "lü"},
		{'略', "lüe"},
		{'女', "nü"},
		{'欸', "ai"},
	}
	for _, c := range cases {
		got, ok := firstReading(c.r)
		if !ok {
			t.Errorf("firstReading(%q) 未命中，期望 %q", c.r, c.want)
			continue
		}
		if got != c.want {
			t.Errorf("firstReading(%q) = %q, 期望 %q", c.r, got, c.want)
		}
	}
	if _, ok := firstReading('Z'); ok {
		t.Error("firstReading('Z') 应未命中（无拼音数据）")
	}
}

func TestNormalizeReading(t *testing.T) {
	cases := []struct{ in, want string }{
		{"lv", "lü"},     // go-pinyin 把 ü 写作 v
		{"lve", "lüe"},   //
		{"nv", "nü"},     //
		{"lu", "lu"},     // 无 v 不动
		{"m\u0300", "m"}, // 组合用 grave 剔除（go-pinyin 漏剥）
		{"\u1EBF", "ê"},  // 预组合 ế → ê
		{"\u1EC1", "ê"},  // 预组合 ề → ê
		{"\u1EC3", "ê"},  // 预组合 ệ → ê
		{"\u1EC5", "ê"},  // 预组合 ễ → ê
		{"\u1EC7", "ê"},  // 预组合 ể → ê
		{"wei", "wei"},   //
	}
	for _, c := range cases {
		if got := normalizeReading(c.in); got != c.want {
			t.Errorf("normalizeReading(%q) = %q, 期望 %q", c.in, got, c.want)
		}
	}
}

// divergenceChars 是 Rust pinyin 0.11 多音字表读音数 >8 的 7 个字。
// Rust 按含重复读音的总数触发 VARIANT_LIMIT=8 的单字退化（只留第一个读音），
// go-pinyin 去重后唯一读音数 ≤8、不退化，导致含这些字的名字键集合不同。
// 完整分析见 §10-9。
var divergenceChars = map[rune]bool{
	'呵': true, '敦': true, '欸': true, '湛': true, '繆': true, '誒': true, '那': true,
}

func nameHasDivergenceChar(name string) bool {
	for _, r := range name {
		if divergenceChars[r] {
			return true
		}
	}
	return false
}

// TestDegradationDivergence 记录退化点分歧的具体形态（§10-9 差异清单的可执行版）：
// 每个用例给出 Rust 侧实际键集合（去重后）与 Go 侧键集合。
// 注意分歧是双向的：Rust 退化丢掉的读音 Go 有（Go 多命中），
// Rust 保留而 Go 因在别的字上退化丢失的读音也有（Go 少命中）。
func TestDegradationDivergence(t *testing.T) {
	cases := []struct {
		name     string
		rustFull []string // Rust SearchKeys::from_name 实测（去重后）
		goFull   []string // Go NewSearchKeys 预期
	}{
		{
			name:     "那种",
			rustFull: []string{"nazhong", "nachong"},
			goFull:   []string{"nazhong", "nuozhong", "neizhong", "nezhong", "naizhong"},
		},
		{
			name:     "呵呵",
			rustFull: []string{"hehe"},
			goFull:   []string{"hehe", "hahe", "ahe", "kehe", "huohe"},
		},
		{
			name:     "敦促",
			rustFull: []string{"duncu", "dunchuo"},
			goFull:   []string{"duncu", "duicu", "tuancu", "diaocu", "daocu", "zhuncu", "tuncu"},
		},
		{
			name:     "湛蓝",
			rustFull: []string{"zhanlan", "zhanla"},
			goFull:   []string{"zhanlan", "chenlan", "danlan", "tanlan", "jinlan", "yinlan", "shenlan"},
		},
		{
			name:     "誒",
			rustFull: []string{"ei"},
			goFull:   []string{"ei", "xi", "yi", "ê"},
		},
		{
			name:     "欸",
			rustFull: []string{"ai"},
			goFull:   []string{"ai", "ê", "xie", "ei"},
		},
	}
	for _, c := range cases {
		got := dedupStrings(NewSearchKeys(c.name).Full)
		if len(got) != len(c.goFull) {
			t.Errorf("%q Go full = %v, 期望 %v", c.name, got, c.goFull)
			continue
		}
		for i := range got {
			if got[i] != c.goFull[i] {
				t.Errorf("%q Go full = %v, 期望 %v", c.name, got, c.goFull)
				break
			}
		}
		// Rust 侧的值来自实测记录，与 Go 不同即分歧（防止记录被悄悄改动失效）。
		if len(c.rustFull) == len(c.goFull) {
			t.Errorf("%q rust 与 go 键集合相同，分歧记录已过期？", c.name)
		}
	}
}

// TestNonHanziCharsKeptInInitials 对应 to_initials 的行为：
// 没有拼音数据的字符原样保留，"Zed 编辑器" → "Zed bjq"。
// 注意：search_key.rs 的文档注释写的是 "Zbjzq"，与 voke-core 实际行为
// （实测 "Zed bjq"）不符，本测试以实测为准。
func TestNonHanziCharsKeptInInitials(t *testing.T) {
	if got := ToInitials("Zed 编辑器"); got != "Zed bjq" {
		t.Errorf(`ToInitials("Zed 编辑器") = %q, 期望 "Zed bjq"`, got)
	}
	if got := ToInitials("微信"); got != "wx" {
		t.Errorf(`ToInitials("微信") = %q, 期望 "wx"`, got)
	}
}
