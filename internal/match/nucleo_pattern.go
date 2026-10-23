// nucleo Pattern/Atom 构造的 Go 移植。
// 对应 nucleo-matcher 0.3.1 的 src/pattern.rs：
// Pattern::new + Atom::new（escape_whitespace=true），
// voke 固定使用 CaseMatching::Ignore + Normalization::Smart。
package match

import (
	"strings"
	"unicode"
)

// atomKind 对应 pattern::AtomKind（只移植 voke 用到的两种）。
type atomKind int

const (
	atomKindFuzzy atomKind = iota
	atomKindSubstring
)

// atom 对应 pattern::Atom（negative 恒为 false：Pattern::new 不做语法解析）。
type atom struct {
	kind       atomKind
	needle     []rune
	ignoreCase bool
	normalize  bool
	// raw 是 needle 的字符串形态（ASCII 分支已小写、非 ASCII 分支已折叠），
	// 只供 quality 判定用：匹配形态是纯字符串关系，不必再走一遍 DP。
	raw string
}

// pattern 对应 pattern::Pattern。
type pattern struct {
	atoms []atom
	// whole 是整串查询小写后的形态（保留原空白），wholeTight 是再去掉空白的形态。
	// 两者只供「整串」档位判定：whole 认 "visual studio" 这种带空格的前缀，
	// wholeTight 认 "vs code" 这种按词首拼出的形态。
	whole      string
	wholeTight string
}

// patternAtoms 对应 pattern::pattern_atoms：按未转义空格切分，
// 反斜杠转义（\ 后的空格不切分）。
func patternAtoms(s string) []string {
	var atoms []string
	sawBackslash := false
	start := 0
	for i, c := range s {
		split := false
		switch {
		case c == ' ' && !sawBackslash:
			split = true
		case c == '\\':
			sawBackslash = true
		default:
			sawBackslash = false
		}
		if split {
			atoms = append(atoms, s[start:i])
			start = i + 1 // ' ' 为单字节 ASCII
		}
	}
	atoms = append(atoms, s[start:])
	return atoms
}

// unescapeSpaces 对应 Atom::new 的 escape_whitespace 处理（ASCII 分支）：
// 首个 "\ " 之前的部分原样保留，其余 "\ " 替换为 ' '。
func unescapeSpaces(s string) string {
	const esc = "\\ "
	i := strings.Index(s, esc)
	if i < 0 {
		return s
	}
	var b strings.Builder
	b.WriteString(s[:i])
	for _, rem := range strings.Split(s[i+len(esc):], esc) {
		b.WriteByte(' ')
		b.WriteString(rem)
	}
	return b.String()
}

// newAtom 对应 Atom::new（needle 尚未按空白预切分）。
func newAtom(needle string, kind atomKind) atom {
	a := atom{kind: kind}
	if isASCIIString(needle) {
		// ASCII 分支：escape 处理后整体小写；ignore_case=true，
		// normalize 恒为 true（Smart，ASCII 分支不逐字检查）。
		s := unescapeSpaces(needle)
		s = strings.ToLower(s)
		a.needle = []rune(s)
		a.raw = s
		a.ignoreCase = true
		a.normalize = true
		return a
	}
	// 非 ASCII 分支：逐 grapheme 首字符（Go 侧近似，见 nucleo_chars.go 说明），
	// Ignore 大小写 → to_lower_case；Smart 归一化 → 所有字符归一化不变才算启用。
	chars := graphemeFirstRunes(needle)
	normalize := true
	for i, c := range chars {
		c = foldTable(c)
		if normTable(c) != c {
			normalize = false
		}
		chars[i] = c
	}
	a.needle = chars
	a.raw = string(chars)
	a.ignoreCase = true
	a.normalize = normalize
	return a
}

// newPattern 对应 Pattern::new（空 needle 的 atom 被过滤）。
func newPattern(s string, kind atomKind) *pattern {
	p := &pattern{whole: needleForm(s, false), wholeTight: needleForm(s, true)}
	for _, raw := range patternAtoms(s) {
		a := newAtom(raw, kind)
		if len(a.needle) == 0 {
			continue
		}
		p.atoms = append(p.atoms, a)
	}
	return p
}

// score 对应 Pattern::score：各 atom 得分求和（u16 → u32），任一未命中即未命中。
func (p *pattern) score(m *Matcher, haystack []rune) (uint32, bool) {
	if len(p.atoms) == 0 {
		return 0, true
	}
	var total uint32
	for _, a := range p.atoms {
		// Atom::score 会用 atom 的标志覆写 matcher 配置。
		m.cfg.ignoreCase = a.ignoreCase
		m.cfg.normalize = a.normalize
		var (
			s  uint16
			ok bool
		)
		if a.kind == atomKindFuzzy {
			s, ok = m.fuzzyMatch(haystack, a.needle)
		} else {
			s, ok = m.substringMatch(haystack, a.needle)
		}
		if !ok {
			return 0, false
		}
		total += uint32(s)
	}
	return total, true
}

// quality 返回该 pattern 对 key 的匹配形态：整串与逐词各判一次，取**较强**的那档。
//
//   - 整串负责「查询本身就是键的一部分」：`visual studio` 是 `Visual Studio 2022` 的前缀、
//     `vs code` 去空格后能按词首拼出 `Visual Studio Code`；
//   - 逐词取**最低**档负责「每个词都得沾边」：任一 atom 只到子串，整条就封顶在子串，
//     防止空格把判定放水。
//
// 两者取 max 而不是只用其中一种：只用整串会丢掉「空格分隔但整串不连续」的命中，
// 只用逐词会把 `visual studio` 从「前缀」压成「子串」（视觉上更差的消息）。
// 注意 max 不会放水：整串档位高于逐词最低档时，词与词的关系必然更强。
//
// 只在 p.score 命中后调用——形态是纯字符串关系，与 nucleo 的 DP 无关。
func (p *pattern) quality(text, lower string) MatchQuality {
	weakest := QualityExact
	for _, a := range p.atoms {
		if q := qualityFor(text, lower, a.raw); q < weakest {
			weakest = q
		}
	}
	if whole := wholeQuality(text, lower, p.whole, p.wholeTight); whole > weakest {
		return whole
	}
	return weakest
}

// isASCIIString 对应 str::is_ascii。
func isASCIIString(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// graphemeFirstRunes 近似 Rust 的 chars::graphemes：取每个「基础字符 +
// 后续组合用符号（Mn/Mc/Me）」簇的首字符。仅影响含组合符号的查询输入。
// 无基础字符的孤立组合符号按 Rust 语义自成一簇。
func graphemeFirstRunes(s string) []rune {
	var out []rune
	pending := rune(-1)
	for _, r := range s {
		if isCombiningMark(r) && pending >= 0 {
			out = append(out, pending)
			pending = -1
			continue
		}
		if pending >= 0 {
			out = append(out, pending)
		}
		pending = r
	}
	if pending >= 0 {
		out = append(out, pending)
	}
	return out
}

// isCombiningMark 对应 Unicode 的 Mn/Mc/Me 组合用符号类别。
func isCombiningMark(r rune) bool {
	return unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Mc, r) || unicode.Is(unicode.Me, r)
}
