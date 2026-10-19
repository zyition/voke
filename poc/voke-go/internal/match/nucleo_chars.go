// nucleo-matcher 0.3.1 评分算法的 Go 移植（仅 score 路径，不含 indices）。
//
// 对应 Rust 侧 voke-core/src/matcher.rs 使用的 nucleo_matcher 0.3.1：
// Pattern::new(query, CaseMatching::Ignore, Normalization::Smart, kind)
// + Matcher{Config::DEFAULT}。移植范围与取舍：
//
//   - 只移植 voke 实际走到的路径：haystack 恒为 Utf32Str::Unicode
//     （matcher.rs 的 score_one 用 haystack.chars().collect() 后传
//     Utf32Str::Unicode），needle 可为 Ascii/Unicode 两种。
//     Ascii haystack 的快路径（prefilter_ascii / substring_match_ascii 等）
//     在 voke 不可达，未移植。
//   - INDICES=false：score_one 只用分不用高亮下标，matrix_cells /
//     reconstruct_optimal_path 未移植。
//   - grapheme 切分：Rust 用 unicode-segmentation 取每个 grapheme 首字符
//     构造 needle；Go 标准库无 grapheme 分割，这里用「基础字符 + 后续
//     组合用符号（Mn/Mc/Me）」近似，仅影响含组合符号的查询。
//   - Config::DEFAULT 的 prefer_prefix=false 且 voke 不开启，
//     calculate_score 的 prefer_prefix 分支按原样移植但不可达。
//   - 字符分类用 Go unicode 表近似 Rust 的 Unicode 属性
//     （is_lowercase/is_uppercase/is_alphabetic/is_whitespace），
//     对 Latin/中日韩文字完全一致，生僻符号类别可能不同（只影响分值，
//     不影响命中与否）。
package match

import (
	"strings"
	"unicode"
)

// charClass 对应 nucleo chars::CharClass（枚举顺序即序值，bonus_for 依赖大小比较）。
type charClass int

const (
	classWhitespace charClass = iota
	classNonWord
	classDelimiter
	classLower
	classUpper
	classLetter
	classNumber
)

// matcherConfig 对应 nucleo Config，字段默认值取 Config::DEFAULT。
type matcherConfig struct {
	delimiterChars         string // Config::DEFAULT: "/,:;|"
	bonusBoundaryWhite     uint16 // BONUS_BOUNDARY + 2
	bonusBoundaryDelimiter uint16 // BONUS_BOUNDARY + 1
	initialCharClass       charClass
	// normalize / ignoreCase 由每个 atom 在打分前覆写（Atom::score 的行为）。
	normalize    bool
	ignoreCase   bool
	preferPrefix bool
}

func defaultMatcherConfig() matcherConfig {
	return matcherConfig{
		delimiterChars:         "/,:;|",
		bonusBoundaryWhite:     bonusBoundary + 2,
		bonusBoundaryDelimiter: bonusBoundary + 1,
		initialCharClass:       classWhitespace,
		normalize:              true,
		ignoreCase:             true,
		preferPrefix:           false,
	}
}

// bonusFor 对应 Config::bonus_for。
func (c *matcherConfig) bonusFor(prev, class charClass) uint16 {
	if class > classDelimiter {
		// 非词字符到词字符的转换
		switch prev {
		case classWhitespace:
			return c.bonusBoundaryWhite
		case classDelimiter:
			return c.bonusBoundaryDelimiter
		case classNonWord:
			return bonusBoundary
		}
	}
	if prev == classLower && class == classUpper ||
		prev != classNumber && class == classNumber {
		// camelCase 字母 123
		return bonusCamel123
	} else if class == classWhitespace {
		return c.bonusBoundaryWhite
	} else if class == classNonWord {
		return bonusNonWord
	}
	return 0
}

// classOf 对应 Char::char_class。
func (m *Matcher) classOf(r rune) charClass {
	if r < 128 {
		c := byte(r)
		switch {
		case c >= 'a' && c <= 'z':
			return classLower
		case c >= 'A' && c <= 'Z':
			return classUpper
		case c >= '0' && c <= '9':
			return classNumber
		case c == ' ' || c == '\t' || c == '\n' || c == '\x0C' || c == '\r':
			return classWhitespace
		case strings.IndexByte(m.cfg.delimiterChars, c) >= 0:
			return classDelimiter
		default:
			return classNonWord
		}
	}
	switch {
	case unicode.IsLower(r):
		return classLower
	case isUpperFold(r):
		return classUpper
	case unicode.IsNumber(r):
		return classNumber
	case unicode.IsLetter(r):
		return classLetter
	case unicode.IsSpace(r):
		return classWhitespace
	default:
		return classNonWord
	}
}

// charClassAndNormalize 对应 Char::char_class_and_normalize
// （分类用原字符，归一化/大小写折叠后的字符用于比较）。
func (m *Matcher) charClassAndNormalize(r rune) (rune, charClass) {
	class := m.classOf(r)
	if r < 128 {
		if m.cfg.ignoreCase && class == classUpper {
			r += 32
		}
		return r, class
	}
	caseFold := class == classUpper
	if m.cfg.normalize {
		r = normTable(r)
		caseFold = true
	}
	if caseFold && m.cfg.ignoreCase {
		r = foldTable(r)
	}
	return r, class
}

// normalizeRune 对应 Char::normalize（prefilter / substring / exact 路径使用）。
// 与 charClassAndNormalize 的差别按 Rust 原样保留：ignore_case 时无条件折叠。
func (m *Matcher) normalizeRune(r rune) rune {
	if m.cfg.normalize {
		r = normTable(r)
	}
	if m.cfg.ignoreCase {
		r = foldTable(r)
	}
	return r
}

// normTable 对应 chars::normalize::normalize（DATA1+DATA2+DATA3 稠密表的 map 化）。
func normTable(r rune) rune {
	if v, ok := normalizeData1[r]; ok {
		return v
	}
	if v, ok := normalizeData2[r]; ok {
		return v
	}
	if v, ok := normalizeData3[r]; ok {
		return v
	}
	return r
}

// foldTable 对应 chars::to_lower_case（CASE_FOLDING_SIMPLE 二分查找的 map 化）。
func foldTable(r rune) rune {
	if v, ok := caseFoldingSimple[r]; ok {
		return v
	}
	return r
}

// isUpperFold 对应 chars::is_upper_case（字符是否为 CASE_FOLDING_SIMPLE 的键）。
func isUpperFold(r rune) bool {
	_, ok := caseFoldingSimple[r]
	return ok
}
