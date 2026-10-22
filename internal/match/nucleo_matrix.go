// nucleo 匹配调度与 DP 矩阵的 Go 移植。
// 对应 nucleo-matcher 0.3.1 的 src/lib.rs（fuzzy/substring/exact 分发）、
// src/fuzzy_optimal.rs（DP）、src/fuzzy_greedy.rs（退化路径）、
// src/prefilter.rs、src/exact.rs。
//
// 本文件只覆盖 voke 用到的 (Unicode haystack, Ascii/Unicode needle) 组合，
// 理由见 nucleo_chars.go 头部说明。
package match

// fuzzyMatch 对应 Matcher::fuzzy_matcher_impl（INDICES=false，haystack 为 Unicode）。
func (m *Matcher) fuzzyMatch(haystack, needle []rune) (uint16, bool) {
	if len(needle) > len(haystack) {
		return 0, false
	}
	if len(needle) == 0 {
		return 0, true
	}
	if len(needle) == len(haystack) {
		return m.exactMatchImpl(haystack, needle, 0, len(haystack))
	}
	if len(needle) == 1 {
		start, _, ok := m.prefilterNonASCII(haystack, needle, true)
		if !ok {
			return 0, false
		}
		return m.substringMatch1NonASCII(haystack, needle[0], start), true
	}
	start, end, ok := m.prefilterNonASCII(haystack, needle, false)
	if !ok {
		return 0, false
	}
	if len(needle) == end-start {
		return m.exactMatchImpl(haystack, needle, start, end)
	}
	return m.fuzzyMatchOptimal(haystack, needle, start, start+1, end)
}

// substringMatch 对应 Matcher::substring_match_impl（INDICES=false，haystack 为 Unicode）。
func (m *Matcher) substringMatch(haystack, needle []rune) (uint16, bool) {
	if len(needle) > len(haystack) {
		return 0, false
	}
	if len(needle) == 0 {
		return 0, true
	}
	if len(needle) == len(haystack) {
		return m.exactMatchImpl(haystack, needle, 0, len(haystack))
	}
	if len(needle) == 1 {
		start, _, ok := m.prefilterNonASCII(haystack, needle, true)
		if !ok {
			return 0, false
		}
		return m.substringMatch1NonASCII(haystack, needle[0], start), true
	}
	start, _, ok := m.prefilterNonASCII(haystack, needle, false)
	if !ok {
		return 0, false
	}
	return m.substringMatchNonASCII(haystack, needle, start)
}

// exactMatchImpl 对应 Matcher::exact_match_impl：两侧都归一化后逐字比较，
// 相等则对 [start, end) 区域算分。
func (m *Matcher) exactMatchImpl(haystack, needle []rune, start, end int) (uint16, bool) {
	if len(needle) != end-start {
		return 0, false
	}
	for i := 0; i < len(needle); i++ {
		if m.normalizeRune(haystack[start+i]) != m.normalizeRune(needle[i]) {
			return 0, false
		}
	}
	return m.calculateScore(haystack, needle, start, end), true
}

// prefilterNonASCII 对应 Matcher::prefilter_non_ascii。
// 返回 (start, end, ok)：onlyGreedy 时 end 无意义（Rust 同样丢弃）。
func (m *Matcher) prefilterNonASCII(haystack, needle []rune, onlyGreedy bool) (int, int, bool) {
	limit := len(haystack) - len(needle) + 1
	start := -1
	for i := 0; i < limit; i++ {
		if m.normalizeRune(haystack[i]) == needle[0] {
			start = i
			break
		}
	}
	if start < 0 {
		return 0, 0, false
	}
	if onlyGreedy {
		if len(haystack)-start < len(needle) {
			return 0, 0, false
		}
		return start, start + 1, true
	}
	// needle.last() 在 haystack[start+1..] 中从后往前第一个匹配位置 +1。
	last := needle[len(needle)-1]
	end := -1
	for i := len(haystack) - 1; i > start; i-- {
		if m.normalizeRune(haystack[i]) == last {
			end = i + 1
			break
		}
	}
	if end < 0 {
		return 0, 0, false
	}
	if end-start < len(needle) {
		return 0, 0, false
	}
	return start, end, true
}

// substringMatch1NonASCII 对应 exact.rs 的 substring_match_1_non_ascii：
// 单字符 needle 取加分最高的位置（prev_class 只在命中时更新，与 Rust 一致）。
func (m *Matcher) substringMatch1NonASCII(haystack []rune, needleChar rune, start int) uint16 {
	var maxScore uint16
	prevClass := m.cfg.initialCharClass
	if start > 0 {
		prevClass = m.classOf(haystack[start-1])
	}
	for i := start; i < len(haystack); i++ {
		c, class := m.charClassAndNormalize(haystack[i])
		if c != needleChar {
			continue
		}
		bonus := m.cfg.bonusFor(prevClass, class)
		prevClass = class
		score := bonus*bonusFirstCharMultiplier + scoreMatch
		if score > maxScore {
			maxScore = score
			if bonus >= m.cfg.bonusBoundaryWhite {
				break
			}
		}
	}
	return maxScore
}

// substringMatchNonASCII 对应 exact.rs 的 substring_match_non_ascii。
// 注意：候选起点范围是 [start, len(needle)-1)，与 Rust 的 `haystack[start..end]`
// （end = len-needle.len()，右开）一致——最后一个可能的起点被排除，
// 这是 nucleo 0.3.1 的既有行为，1:1 保留。
func (m *Matcher) substringMatchNonASCII(haystack, needle []rune, start int) (uint16, bool) {
	var maxScore uint16
	maxPos := 0
	prevClass := m.cfg.initialCharClass
	if start > 0 {
		prevClass = m.classOf(haystack[start-1])
	}
	end := len(haystack) - len(needle)
	for i := start; i < end; i++ {
		c, class := m.charClassAndNormalize(haystack[i])
		if c != needle[0] {
			continue
		}
		bonus := m.cfg.bonusFor(prevClass, class)
		prevClass = class
		score := bonus*bonusFirstCharMultiplier + scoreMatch
		if score > maxScore && m.sliceEqNorm(haystack[i+1:i+len(needle)], needle[1:]) {
			maxPos = i
			maxScore = score
			if bonus >= m.cfg.bonusBoundaryWhite {
				break
			}
		}
	}
	if maxScore == 0 {
		return 0, false
	}
	// maxPos 是绝对位置（Rust 的 max_pos 是相对索引，那边用 start + max_pos）。
	return m.calculateScore(haystack, needle, maxPos, maxPos+len(needle)), true
}

// sliceEqNorm 比较两段字符逐一归一化后是否相等（对应 Rust 的 map(normalize).eq）。
func (m *Matcher) sliceEqNorm(a, b []rune) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if m.normalizeRune(a[i]) != m.normalizeRune(b[i]) {
			return false
		}
	}
	return true
}

// fuzzyMatchOptimal 对应 Matcher::fuzzy_match_optimal（INDICES=false）。
// haystack 搜索区间为 [start, end)；greedyEnd 仅 greedy 退化时使用。
func (m *Matcher) fuzzyMatchOptimal(haystack, needle []rune, start, greedyEnd, end int) (uint16, bool) {
	h := end - start
	n := len(needle)
	// MatrixSlab::alloc 的边界条件，不满足则退化为 greedy 匹配。
	if h*n > maxMatrixSize || h > maxHaystackLen || n > maxNeedleLen ||
		matrixLayoutSize(h, n) > matcherDataSize {
		return m.fuzzyMatchGreedy(haystack, needle, start, greedyEnd)
	}

	// setup：复制 haystack 并就地归一化，算好每格 bonus，
	// 顺带求出每个 needle 字符的贪心首个匹配位置 rowOffs。
	hs := make([]rune, h)
	copy(hs, haystack[start:end])
	bonus := make([]uint16, h)
	rowOffs := make([]int, n)
	currentRow := make([]scoreCell, h+1-n)

	prevClass := m.cfg.initialCharClass
	if start > 0 {
		prevClass = m.classOf(haystack[start-1])
	}
	matched := false
	needleIdx := 0
	for i := 0; i < h; i++ {
		c, class := m.charClassAndNormalize(hs[i])
		hs[i] = c
		bonus[i] = m.cfg.bonusFor(prevClass, class)
		prevClass = class
		if c == needle[needleIdx] {
			if needleIdx+1 < n {
				rowOffs[needleIdx] = i
				needleIdx++
			} else if !matched {
				rowOffs[needleIdx] = i
				matched = true
			}
		}
	}
	if !matched {
		return 0, false
	}

	// 第一行（needle[0]）与 populate_matrix（needle[1..] 各行）。
	m.scoreRow(currentRow, hs, bonus, 0, rowOffs[1], 0, needle[0], needle[1], 0, true)
	for k := 1; k < n-1; k++ {
		m.scoreRow(currentRow, hs, bonus, rowOffs[k], rowOffs[k+1], k, needle[k], needle[k+1], 0, false)
	}

	// 最后一行取最大分（Rust 的 max_by_key 在并列时取最后一个，>= 保持一致）。
	relLast := rowOffs[n-1] + 1 - n
	best := uint16(0)
	for _, cell := range currentRow[relLast:] {
		if cell.score >= best {
			best = cell.score
		}
	}
	return best, true
}

// scoreRow 对应 fuzzy_optimal.rs 的 MatcherDataView::score_row（INDICES=false）。
// currentRow 同时充当「上一行读、本行写」的对角线缓冲，与 Rust 的原地复用一致。
func (m *Matcher) scoreRow(currentRow []scoreCell, haystack []rune, bonus []uint16,
	rowOff, nextRowOff, needleIdx int, needleChar, nextNeedleChar rune, prefixBonus uint16, firstRow bool,
) {
	nextRowOff-- // Rust: next_row_off -= 1
	relativeRowOff := rowOff - needleIdx
	nextRelativeRowOff := nextRowOff - needleIdx

	// 跳过列：next_needle_char 首个可能位置之前的列。
	var prevPScore, prevMScore uint16
	for i := rowOff; i < nextRowOff; i++ {
		p := pScoreFn(prevPScore, prevMScore)
		var mCell scoreCell
		if firstRow {
			mCell = unmatched
			if haystack[i] == needleChar {
				mCell = scoreCell{
					score:            bonus[i]*bonusFirstCharMultiplier + scoreMatch + prefixBonus/prefixBonusScale,
					matched:          false,
					consecutiveBonus: uint8(bonus[i]),
				}
			}
			prefixBonus = satSub(prefixBonus, penaltyGapExtension)
		} else {
			mCell = currentRow[relativeRowOff+(i-rowOff)]
		}
		prevPScore = p
		prevMScore = mCell.score
	}

	// 主列：haystack[nextRowOff..] 的 windows(2)。
	// Rust 用 zip 链接三个迭代器并按最短截断：windows(2) 长
	// len(haystack)-nextRowOff-1，currentRow[nextRelativeRowOff..] 长
	// len(currentRow)-nextRelativeRowOff（后者通常更短，是限制侧）。
	count := len(haystack) - nextRowOff - 1
	if rem := len(currentRow) - nextRelativeRowOff; rem < count {
		count = rem
	}
	for j := 0; j < count; j++ {
		i := nextRowOff + j
		p := pScoreFn(prevPScore, prevMScore)
		var mCell scoreCell
		if firstRow {
			mCell = unmatched
			if haystack[i] == needleChar {
				mCell = scoreCell{
					score:            bonus[i]*bonusFirstCharMultiplier + scoreMatch + prefixBonus/prefixBonusScale,
					matched:          false,
					consecutiveBonus: uint8(bonus[i]),
				}
			}
			prefixBonus = satSub(prefixBonus, penaltyGapExtension)
		} else {
			mCell = currentRow[nextRelativeRowOff+j]
		}
		idx := nextRelativeRowOff + j
		if haystack[i+1] == nextNeedleChar {
			currentRow[idx] = nextMCell(p, bonus[i+1], mCell)
		} else {
			currentRow[idx] = unmatched
		}
		prevPScore = p
		prevMScore = mCell.score
	}
}

// fuzzyMatchGreedy 对应 Matcher::fuzzy_match_greedy_（haystack 为 Unicode 时的路径）。
// 先在 [end, len) 里向前贪心补齐 needle 尾部，再在 [start, end) 里反向压缩起点，
// 最后对压缩后的连续区域算分。分数可能非最优（nucleo 原样）。
func (m *Matcher) fuzzyMatchGreedy(haystack, needle []rune, start, end int) (uint16, bool) {
	firstCharEnd := end // H::ASCII && N::ASCII 为 false，故 first_char_end = end

	// 'nonascii 块：H::ASCII || N::ASCII 恒真（Unicode haystack）。
	if len(needle) > 1 {
		cursor := 1
		needleChar := needle[1]
		done := false
		for i := firstCharEnd; i < len(haystack); i++ {
			if m.normalizeRune(haystack[i]) != needleChar {
				continue
			}
			cursor++
			if cursor >= len(needle) {
				end = i + 1
				done = true
				break
			}
			needleChar = needle[cursor]
		}
		if !done {
			return 0, false
		}
	}

	// 反向贪心，把起点尽量往后压。
	cursor := len(needle) - 1
	needleChar := needle[cursor]
	for i := end - 1; i >= start; i-- {
		if m.normalizeRune(haystack[i]) != needleChar {
			continue
		}
		cursor--
		if cursor < 0 {
			start = i
			break
		}
		needleChar = needle[cursor]
	}
	return m.calculateScore(haystack, needle, start, end), true
}
