// 匹配形态（档位）判定：字符串关系是纯字符串运算，与 nucleo 的 DP 无关，
// 因此单独成文件、不复用评分路径。
//
// 档位只回答「命中得有多贴题」，不参与打分本身：nucleo 的分只在档内排序
// （见 internal/logic/rank 的 TierBonus）。
//
// 性能约定：本文件的函数**不分配内存**。搜索键的小写形态在索引期算好
// （SearchKeys.refs），缩写判定走流式切词 + uint64 位掩码，够一次按键对上千条
// 候选逐个判档（此前每条候选要 ~10 次分配，是打字卡顿的主因之一）。
package match

import "strings"

// qualityFor 判定一条搜索键与 needle 的匹配形态。
// text 是键原文（缩写判定要看大小写来切驼峰），lower 是它的小写形态，
// needle 已小写、已去空白。
//
// 顺序即强弱：全等 > 前缀 > 词首序列（Acronym）> 子串 > 其它（Fuzzy，
// 由 nucleo 的命中与否决定）。
func qualityFor(text, lower, needle string) MatchQuality {
	if needle == "" {
		return QualityFuzzy
	}
	switch {
	case lower == needle:
		return QualityExact
	case len(lower) >= len(needle) && lower[:len(needle)] == needle:
		return QualityPrefix
	case acronymMatch(text, needle):
		return QualityAcronym
	case strings.Contains(lower, needle):
		return QualitySubstring
	default:
		return QualityFuzzy
	}
}

// acronymMaxNeedle 是缩写判定支持的最大查询长度：状态用 uint64 位掩码表示
// 「needle[:j] 已被消费」，超过 63 个字符直接不判（那种长度的查询本身已经足够精确）。
const acronymMaxNeedle = 63

// acronymMatch 报告 needle 能否由 text 的若干**词首**按序拼成：
// 把 text 切成词（非 ASCII 字母数字 + 驼峰边界），needle 的连续片段依次各取一个词的
// 非空前缀，直到 needle 用完（词可以不全部用上）。
//
//	"vsc"    + "Visual Studio Code" → v|s|c            ✓
//	"vscode" + "Visual Studio Code" → v|s|code         ✓（同一规则顺带支持续写）
//	"code"   + "Visual Studio Code" → code（第 3 个词） ✓ 词首命中
//	"mail"   + "gmail"              → ✗（单词名字没有词首序列，交回子串判定）
//
// 只有 ASCII 字母数字组成、且至少两个词的名字参与；中文段落没有「词首」概念，
// 中文走拼音键。实现是流式的：边扫词边推进位掩码，不切词、不分配。
func acronymMatch(text, needle string) bool {
	if needle == "" || len(needle) > acronymMaxNeedle {
		return false
	}
	var (
		reach uint64 = 1 // 第 j 位为 1 表示 needle[:j] 已被消费
		words int
	)
	for i := 0; i < len(text); {
		// 跳过分隔符 / 非 ASCII 字节，找词首。
		for i < len(text) && !isASCIIAlnum(text[i]) {
			i++
		}
		if i >= len(text) {
			break
		}
		start := i
		i++
		for i < len(text) && isASCIIAlnum(text[i]) {
			// 驼峰边界：小写→大写 另起一个词（下轮循环接着切）。
			if isLowerASCII(text[i-1]) && isUpperASCII(text[i]) {
				break
			}
			i++
		}
		words++
		reach = extendReach(reach, needle, text[start:i])
		if reach&(1<<len(needle)) != 0 && words >= 2 {
			// 已经能拼出 needle 且词数够：后面还有词也不影响结论。
			return true
		}
	}
	return words >= 2 && reach&(1<<len(needle)) != 0
}

// extendReach 用 word 的连续前缀推进可达位置集合。
func extendReach(reach uint64, needle, word string) uint64 {
	next := reach // 这个词可以不用
	n := len(needle)
	for j := 0; j < n; j++ {
		if reach&(1<<uint(j)) == 0 {
			continue
		}
		for l := 1; l <= len(word) && j+l <= n; l++ {
			if toLowerASCII(word[l-1]) != needle[j+l-1] {
				break
			}
			next |= 1 << uint(j+l)
		}
	}
	return next
}

// wholeQuality 取「整串查询」对搜索键的最高档位：带原空白的形态认前缀 / 子串，
// 去空白的形态认词首拼出（"vs code" → "vscode" → v|s|code）。
func wholeQuality(text, lower, whole, tight string) MatchQuality {
	q := qualityFor(text, lower, whole)
	if tight != whole {
		if tq := qualityFor(text, lower, tight); tq > q {
			q = tq
		}
	}
	return q
}

// needleForm 把查询串归一成小写形态；dropSpace 时再去掉空白。
// 去空白的版本用于词首判定（"vs code" 与 "vscode" 应当等价），
// 保留空白的版本用于前缀 / 子串判定（"visual studio" 要能整串命中前缀）。
func needleForm(s string, dropSpace bool) string {
	var b []rune
	for _, r := range s {
		if dropSpace && (r == ' ' || r == '\t' || r == '\n' || r == '\r') {
			continue
		}
		if r < 0x80 {
			r = rune(toLowerASCII(byte(r)))
		}
		b = append(b, r)
	}
	return string(b)
}

// isASCIIAlnum 报告 c（ASCII 字节）是否为字母或数字。非 ASCII 字节一律视为分隔符。
func isASCIIAlnum(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isLowerASCII(c byte) bool { return c >= 'a' && c <= 'z' }
func isUpperASCII(c byte) bool { return c >= 'A' && c <= 'Z' }

// toLowerASCII 只折叠 ASCII 大写，够用且不引入 unicode 表的开销。
func toLowerASCII(c byte) byte {
	if isUpperASCII(c) {
		return c + ('a' - 'A')
	}
	return c
}
