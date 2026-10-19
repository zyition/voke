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
}

// pattern 对应 pattern::Pattern。
type pattern struct {
	atoms []atom
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
	a.ignoreCase = true
	a.normalize = normalize
	return a
}

// newPattern 对应 Pattern::new（空 needle 的 atom 被过滤）。
func newPattern(s string, kind atomKind) *pattern {
	p := &pattern{}
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
