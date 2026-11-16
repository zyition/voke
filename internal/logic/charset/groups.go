package charset

import (
	"strconv"
	"strings"
)

// CompileGroups 把字符类正则按书写段拆成若干可抽样组，供「长度足够时每组至少出现
// 一次」使用。
//
// 分组规则：
//   - 每个范围（如 a-z）单独成组；
//   - 每个转义类（如 \d、\w、\p{Han}、[[:alpha:]]）单独成组；
//   - 所有零散字面字符（如 !@#$%^&*\-_=+）合并成一组，固定放在最后。
//
// 取反类（[^...]）或无法可靠分词时返回 nil，调用方应退回整集均匀随机。返回的每组
// 都是 Compile(pattern) 的子集，组内字符不重复；只有一组时「每组至少一个」没有意义，
// 调用方应跳过该路径。
func CompileGroups(pattern string) [][]rune {
	full, err := Compile(pattern)
	if err != nil {
		return nil
	}
	content, ok := classContent(pattern)
	if !ok {
		return nil
	}
	tokens, ok := splitClass(content)
	if !ok {
		return nil
	}

	inFull := make(map[rune]bool, len(full))
	for _, r := range full {
		inFull[r] = true
	}

	groups := make([][]rune, 0, len(tokens))
	literals := make([]rune, 0, 8)
	seenLiteral := make(map[rune]bool, 8)
	for _, tok := range tokens {
		if tok.kind == tokenLiteral {
			if inFull[tok.r] && !seenLiteral[tok.r] {
				seenLiteral[tok.r] = true
				literals = append(literals, tok.r)
			}
			continue
		}
		rs, err := compileContent(tok.expr)
		if err != nil {
			// 父 pattern 已编译成功，单个段为空（如 \b）时跳过即可；语法错误
			// 在父 pattern 校验阶段已被拒绝。
			continue
		}
		if g := intersectRunes(rs, inFull); len(g) > 0 {
			groups = append(groups, g)
		}
	}
	if len(literals) > 0 {
		groups = append(groups, literals)
	}
	return groups
}

// classContent 去掉外层方括号并拒绝取反类。
func classContent(pattern string) (string, bool) {
	p := strings.TrimSpace(pattern)
	if p == "" {
		return "", false
	}
	if strings.HasPrefix(p, "[") && strings.HasSuffix(p, "]") {
		p = p[1 : len(p)-1]
	}
	if p == "" || strings.HasPrefix(p, "^") {
		return "", false
	}
	return p, true
}

func intersectRunes(rs []rune, set map[rune]bool) []rune {
	out := make([]rune, 0, len(rs))
	for _, r := range rs {
		if set[r] {
			out = append(out, r)
		}
	}
	return out
}

type tokenKind uint8

const (
	tokenRange tokenKind = iota
	tokenClass
	tokenLiteral
)

type groupToken struct {
	kind tokenKind
	expr string
	r    rune
}

type classAtom struct {
	single bool
	r      rune
	expr   string
}

// splitClass 把字符类内容切成范围 / 转义类 / 字面三种 token。
func splitClass(content string) ([]groupToken, bool) {
	rs := []rune(content)
	out := make([]groupToken, 0, 8)
	for i := 0; i < len(rs); {
		start := i
		a, next, ok := readClassAtom(rs, i)
		if !ok {
			return nil, false
		}
		if a.single && next < len(rs) && rs[next] == '-' && next+1 < len(rs) {
			b, end, ok := readClassAtom(rs, next+1)
			if !ok || !b.single {
				return nil, false
			}
			out = append(out, groupToken{kind: tokenRange, expr: string(rs[start:end])})
			i = end
			continue
		}
		if a.single {
			out = append(out, groupToken{kind: tokenLiteral, r: a.r})
		} else {
			out = append(out, groupToken{kind: tokenClass, expr: a.expr})
		}
		i = next
	}
	return out, true
}

// readClassAtom 读取一个原子：单字符（含转义字面量）或转义类。
func readClassAtom(rs []rune, i int) (classAtom, int, bool) {
	if i >= len(rs) {
		return classAtom{}, i, false
	}
	// POSIX 字符类 [:alpha:]，原样作为一个转义类。
	if rs[i] == '[' && i+1 < len(rs) && rs[i+1] == ':' {
		j := i + 2
		for j+1 < len(rs) && !(rs[j] == ':' && rs[j+1] == ']') {
			j++
		}
		if j+1 >= len(rs) {
			return classAtom{}, i, false
		}
		return classAtom{expr: string(rs[i : j+2])}, j + 2, true
	}
	if rs[i] != '\\' {
		return classAtom{single: true, r: rs[i], expr: string(rs[i])}, i + 1, true
	}
	if i+1 >= len(rs) {
		return classAtom{}, i, false
	}
	c := rs[i+1]
	switch c {
	case 'd', 'D', 'w', 'W', 's', 'S', 'b', 'B', 'A', 'z':
		return classAtom{expr: string(rs[i : i+2])}, i + 2, true
	case 'p', 'P':
		j := i + 2
		if j < len(rs) && rs[j] == '{' {
			for j < len(rs) && rs[j] != '}' {
				j++
			}
			if j >= len(rs) {
				return classAtom{}, i, false
			}
			j++
		}
		return classAtom{expr: string(rs[i:j])}, j, true
	case 'Q':
		// \Q...\E 是整段引用，无法按单字符分词。
		return classAtom{}, i, false
	case 'x', 'u', 'U':
		if r, next, ok := decodeHexEscape(rs, i); ok {
			return classAtom{single: true, r: r, expr: string(rs[i:next])}, next, true
		}
		return classAtom{}, i, false
	}
	if c >= '0' && c <= '7' {
		// 八进制转义（\141 等）不参与分组，避免误判成字面数字。
		return classAtom{}, i, false
	}
	return classAtom{single: true, r: c, expr: string(rs[i : i+2])}, i + 2, true
}

// decodeHexEscape 解析 \xNN、\x{...}、\uNNNN、\UNNNNNNNN。
func decodeHexEscape(rs []rune, i int) (rune, int, bool) {
	j := i + 2
	if j < len(rs) && rs[j] == '{' {
		k := j + 1
		for k < len(rs) && rs[k] != '}' {
			k++
		}
		if k >= len(rs) {
			return 0, i, false
		}
		v, err := strconv.ParseUint(string(rs[j+1:k]), 16, 32)
		if err != nil {
			return 0, i, false
		}
		return rune(v), k + 1, true
	}
	n := 2
	switch rs[i+1] {
	case 'u':
		n = 4
	case 'U':
		n = 8
	}
	if j+n > len(rs) {
		return 0, i, false
	}
	v, err := strconv.ParseUint(string(rs[j:j+n]), 16, 32)
	if err != nil {
		return 0, i, false
	}
	return rune(v), j + n, true
}
