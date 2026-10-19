// 拼音数据层：封装 github.com/mozillazg/go-pinyin，输出对齐 Rust pinyin 0.11。
//
// 对应 voke-core/src/search_key.rs 用到的 pinyin 0.11 API：
//   - ToPinyin for char      -> firstReading（取第一个读音）
//   - ToPinyinMulti for char -> readings（Heteronym 模式，全部读音）
//
// 数据对齐处理（go-pinyin Normal 风格输出 → Rust plain 输出，差异详情记录在
// docs/notes/matching.md）：
//   - go-pinyin 把 ü 写作 v（ḿ 等鼻音两边一致），这里还原 v -> ü，
//     保证 ASCII 查询 "lv" 与 Rust 侧一样打不中 "lüse" 键；
//   - go-pinyin 的 phoneticSymbol 表缺 ê 系列预组合声调字符（ế/ề/ệ/ễ/ể），
//     显式回落为 ê；
//   - 组合用声调符号（如 m̀ 的 U+0300）直接剔除。
package match

import (
	"strings"
	"unicode"

	"github.com/mozillazg/go-pinyin"
)

// heteronymArgs 对应 Rust 的 ToPinyinMulti：多音字模式，不带声调。
var heteronymArgs = pinyin.Args{Style: pinyin.Normal, Heteronym: true}

// firstReadingArgs 对应 Rust 的 ToPinyin：只取每个字的第一个读音。
var firstReadingArgs = pinyin.Args{Style: pinyin.Normal, Heteronym: false}

// precomposedToneFix 把 go-pinyin 漏剥声调的 ê 系列预组合字符回落为 ê。
var precomposedToneFix = map[rune]rune{
	'ế': 'ê', 'ề': 'ê', 'ệ': 'ê', 'ễ': 'ê', 'ể': 'ê',
	'Ế': 'ê', 'Ề': 'ê', 'Ệ': 'ê', 'Ễ': 'ê', 'Ể': 'ê',
}

// normalizeReading 把 go-pinyin 的 Normal 风格读音对齐到 Rust plain 输出。
func normalizeReading(s string) string {
	needFix := false
	for _, r := range s {
		if r == 'v' || unicode.Is(unicode.Mn, r) {
			needFix = true
			break
		}
		if _, ok := precomposedToneFix[r]; ok {
			needFix = true
			break
		}
	}
	if !needFix {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == 'v':
			b.WriteRune('ü')
		case unicode.Is(unicode.Mn, r):
			// 剔除组合用声调符号
		default:
			if fixed, ok := precomposedToneFix[r]; ok {
				b.WriteRune(fixed)
			} else {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

// readings 返回 r 的全部拼音读音（Heteronym 模式，已对齐 Rust plain）。
// 没有拼音数据时返回 nil（对应 Rust 的 to_pinyin_multi() 返回 None）。
// 注意：
//   - go-pinyin 的 applyStyle 会按「加风格后的字符串」去重，Rust 多音字表
//     则保留重复读音（如 "中" -> ["zhong","zhong"]）；本函数在归一化后再
//     去重一次（ê 声调修复会把 'ế'/'ề' 等合并成 'ê'），输出等价于 Rust
//     读音集合的去重形式。差异影响见 §10-9。
func readings(r rune) []string {
	pys := pinyin.SinglePinyin(r, heteronymArgs)
	if len(pys) == 0 {
		return nil
	}
	out := make([]string, 0, len(pys))
	for _, p := range pys {
		p = normalizeReading(p)
		if !containsString(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// firstReading 返回 r 的第一个读音（对应 Rust 的 to_pinyin() + plain()）。
func firstReading(r rune) (string, bool) {
	pys := pinyin.SinglePinyin(r, firstReadingArgs)
	if len(pys) == 0 {
		return "", false
	}
	return normalizeReading(pys[0]), true
}
