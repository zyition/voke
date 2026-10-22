// nucleo 评分常量与 calculate_score / DP 的 Go 移植。
// 对应 nucleo-matcher 0.3.1 的 src/score.rs、src/matrix.rs、src/fuzzy_optimal.rs。
package match

// 分值常量，对应 score.rs（原样移植）。
const (
	scoreMatch               uint16 = 16
	penaltyGapStart          uint16 = 3
	penaltyGapExtension      uint16 = 1
	prefixBonusScale         uint16 = 2
	bonusBoundary            uint16 = scoreMatch / 2                        // 8
	maxPrefixBonus           uint16 = bonusBoundary                         // 8
	bonusCamel123            uint16 = bonusBoundary - penaltyGapStart       // 5
	bonusNonWord             uint16 = bonusBoundary                         // 8
	bonusConsecutive         uint16 = penaltyGapStart + penaltyGapExtension // 4
	bonusFirstCharMultiplier uint16 = 2
)

// MatrixSlab 的边界常量，对应 matrix.rs。
const (
	maxMatrixSize  = 100 * 1024 // 100KB matrix cells
	maxHaystackLen = 1 << 16    // haystack.len() > u16::MAX 时退化为 greedy
	maxNeedleLen   = 2048
	// matcherDataSize 对应 size_of::<MatcherData>()：
	// [char;2048] + [u8;2048] + [u16;2048] + [ScoreCell;2048] + [u8;100*1024]。
	matcherDataSize = 2048*4 + 2048 + 2048*2 + 2048*8 + 100*1024
)

// matrixLayoutSize 对应 MatrixLayout::new 的 layout 大小（含段间对齐填充，
// 不含尾部填充——Rust 的 Layout::extend 只做段间 padding）。
func matrixLayoutSize(h, n int) int {
	size := 0
	pad := func(align int) {
		if size%align != 0 {
			size += align - size%align
		}
	}
	pad(4) // haystack: [char; h]
	size += h * 4
	size += h // bonus: [u8; h]
	pad(2)    // rows: [u16; n]
	size += n * 2
	pad(8) // score: [ScoreCell; h+1-n]
	size += (h + 1 - n) * 8
	size += (h + 1 - n) * n // matrix: [MatrixCell; (h+1-n)*n]
	return size
}

// satSub 对应 Rust 的 saturating_sub。
func satSub(a, b uint16) uint16 {
	if a < b {
		return 0
	}
	return a - b
}

// scoreCell 对应 fuzzy_optimal.rs 的 ScoreCell（score u16 + consecutive_bonus u8 + matched）。
type scoreCell struct {
	score            uint16
	consecutiveBonus uint8
	matched          bool
}

// unmatched 对应 fuzzy_optimal.rs 的 UNMATCHED 哨兵值。
var unmatched = scoreCell{score: 0, consecutiveBonus: 0, matched: true}

// nextMCell 对应 fuzzy_optimal.rs 的 next_m_cell。
func nextMCell(pScore, bonus uint16, mCell scoreCell) scoreCell {
	if mCell == unmatched {
		return scoreCell{
			score:            pScore + bonus + scoreMatch,
			matched:          false,
			consecutiveBonus: uint8(bonus),
		}
	}
	consecutiveBonus := uint16(mCell.consecutiveBonus)
	if consecutiveBonus < bonusConsecutive {
		consecutiveBonus = bonusConsecutive
	}
	if bonus >= bonusBoundary && bonus > consecutiveBonus {
		consecutiveBonus = bonus
	}
	scoreMatchV := mCell.score + max(consecutiveBonus, bonus)
	scoreSkip := pScore + bonus
	if scoreMatchV > scoreSkip {
		return scoreCell{
			score:            scoreMatchV + scoreMatch,
			matched:          true,
			consecutiveBonus: uint8(consecutiveBonus),
		}
	}
	return scoreCell{
		score:            scoreSkip + scoreMatch,
		matched:          false,
		consecutiveBonus: uint8(bonus),
	}
}

// pScoreFn 对应 fuzzy_optimal.rs 的 p_score（返回分数与是否来自 match 路径；
// 后者仅 indices 重建用，score 移植里不用）。
func pScoreFn(prevPScore, prevMScore uint16) uint16 {
	scoreMatchV := satSub(prevMScore, penaltyGapStart)
	scoreSkip := satSub(prevPScore, penaltyGapExtension)
	if scoreMatchV > scoreSkip {
		return scoreMatchV
	}
	return scoreSkip
}

// calculateScore 对应 Matcher::calculate_score（INDICES=false）。
// 注意：首字符的分类用原字符（Rust 同样如此），其余字符走
// char_class_and_normalize；调用方负责传入已按路径语义归一化过的 haystack。
func (m *Matcher) calculateScore(haystack, needle []rune, start, end int) uint16 {
	var prevClass charClass
	if start > 0 {
		prevClass = m.classOf(haystack[start-1])
	} else {
		prevClass = m.cfg.initialCharClass
	}
	inGap := false
	consecutive := 1

	// 展开第一次迭代（首字符乘数在这里应用最自然）。
	class := m.classOf(haystack[start])
	firstBonus := m.cfg.bonusFor(prevClass, class)
	score := scoreMatch + firstBonus*bonusFirstCharMultiplier
	prevClass = class
	// needle 迭代器：循环前已消费 needle[0] 与 needle[1]（len==1 时只有 needle[0]），
	// 之后每命中一个字符再消费一项；消费完则 needleChar 保持最后一项。
	needleChar := needle[0]
	cursor := 2
	if len(needle) == 1 {
		cursor = 1
	} else {
		needleChar = needle[1]
	}

	for i := start + 1; i < end; i++ {
		c, cls := m.charClassAndNormalize(haystack[i])
		if c == needleChar {
			bonus := m.cfg.bonusFor(prevClass, cls)
			if consecutive != 0 {
				if bonus >= bonusBoundary && bonus > firstBonus {
					firstBonus = bonus
				}
				bonus = max(max(bonus, firstBonus), bonusConsecutive)
			} else {
				firstBonus = bonus
			}
			score += scoreMatch + bonus
			inGap = false
			consecutive++
			if cursor < len(needle) {
				needleChar = needle[cursor]
				cursor++
			}
		} else {
			penalty := penaltyGapStart
			if inGap {
				penalty = penaltyGapExtension
			}
			score = satSub(score, penalty)
			inGap = true
			consecutive = 0
		}
		prevClass = cls
	}
	if m.cfg.preferPrefix {
		if start != 0 {
			penalty := penaltyGapStart + penaltyGapStart*uint16(min(start-1, maxHaystackLen-1))
			score += satSub(maxPrefixBonus, penalty/prefixBonusScale)
		} else {
			score += maxPrefixBonus
		}
	}
	return score
}
