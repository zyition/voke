// Package rank 是「排序分」的唯一内核：把匹配档位、nucleo 分、provider 权重与
// 使用习惯合成**一个标量**，供 apps 过滤、数据型 provider 与插件 picker 共用。
//
// 为什么单独成包：这三样信号原先在 search.FilterApps、providers.Registry.Rank、
// config.Rank 里各算一套（乘数、顺序、量纲都不同，插件 picker 干脆不吃使用习惯）。
// 收敛到一处后，「调整使用习惯的影响」只改 frequency.UsageBonus，「调整档位的
// 影响」只改 TierBonus，「调整权重」只改配置——三者互不牵连。
//
// 公式（见 docs/notes/ranking.md §1）：
//
//	Final = Weight × (Score + TierBonus[Quality] + Usage)
//
// 量纲约束（由测试守住）：
//   - 相邻档位差 0.10，Usage 上限 0.12 → 长期高频 + 分数接近时，可顶掉**相邻一档**；
//   - 两档差 0.20 > 0.12 → 永远顶不了两档（"gma" → Gmail 不会被乱条目反超）。
//
// 分层：纯逻辑，零 mygo / 零平台调用，Linux 原生可测。
package rank

import (
	"cmp"
	"slices"

	"voke/internal/logic/frequency"
	"voke/internal/match"
)

// tierStep 是相邻档位之间的固定差，TierBonus 与「使用加成上限」都以此为刻度。
const tierStep = 0.10

// TierBonus 把匹配档位折算成加分：每档 0.10，Fuzzy 为 0。
//
// 档位为什么是加分而不是主键：档位做硬约束会让使用习惯对跨档完全失效——一个每天
// 开 30 次的程序匹配只输一档却永远翻不了身，这与「尽可能命中用户想要的」冲突。
// 加分形式保留了两者：匹配质量决定基线，使用习惯只能在同一量级内翻盘。
func TierBonus(q match.MatchQuality) float64 {
	switch q {
	case match.QualityExact:
		return 4 * tierStep
	case match.QualityPrefix:
		return 3 * tierStep
	case match.QualityAcronym:
		return 2 * tierStep
	case match.QualitySubstring:
		return tierStep
	default: // match.QualityFuzzy 及未知值
		return 0
	}
}

// Signal 是一条候选参与排序的全部信号。
type Signal struct {
	// Quality 是匹配档位（字符串关系），见 match.MatchQuality。
	Quality match.MatchQuality
	// Score 是 nucleo 归一化分 0–1，只在同一档内有区分意义。
	// 用 float64 承载：provider 的 Match.Score 就是 float64，来回转 float32 会引入
	// 0.8999… 这类尾差，让「同分」判定失真。
	Score float64
	// Weight 是 provider 的静态权重（配置，>0；apps 恒 1.0）。
	Weight float64
	// Usage 是使用习惯加分，取值来自 frequency.UsageBonus（已封顶 0.12）。
	Usage float64
}

// Final 返回该候选的排序分（越大越靠前）。
func (s Signal) Final() float64 {
	return s.Weight * (s.Score + TierBonus(s.Quality) + s.Usage)
}

// Final 是 [Signal.Final] 的函数形式，给「不方便就地构造结构体」的调用方用
// （providers.Registry.Rank、config 的别名排序）。
func Final(quality match.MatchQuality, score, weight, usage float64) float64 {
	return Signal{Quality: quality, Score: score, Weight: weight, Usage: usage}.Final()
}

// Less 报告 a 是否应排在 b 之前：Final 降序。
//
// 单标量排序（而不是「档位 → 分数」的字典序）是刻意的：档位已经折进了 Final，
// 字典序会让使用习惯在跨档时彻底失效。
func Less(a, b Signal) bool { return a.Final() > b.Final() }

// SortBy 按 score 给出的排序分**稳定降序**排列（同分保持原序：枚举序 / provider 声明序）。
//
// score 对每项只调用一次（decorate-sort-undecorate）：排序分里含使用加成的
// 字符串键拼接 + map 查找，放进比较器会被算 O(n log n) 次，一次按键白烧上万次分配。
func SortBy[T any](items []T, score func(T) float64) {
	type entry struct {
		idx int
		v   float64
	}
	es := make([]entry, len(items))
	for i := range items {
		es[i] = entry{idx: i, v: score(items[i])}
	}
	slices.SortStableFunc(es, func(a, b entry) int { return cmp.Compare(b.v, a.v) })
	tmp := make([]T, len(items))
	for i, e := range es {
		tmp[i] = items[e.idx]
	}
	copy(items, tmp)
}

// UsageOf 取某条目的使用习惯加分；freq 为 nil（未装配 / 纯逻辑测试）→ 0。
// 键前缀必须与记录侧一致（frequency.Key(providerID, title)）。
func UsageOf(freq frequency.Store, providerID, title string) float64 {
	if freq == nil {
		return 0
	}
	return freq.UsageOf(frequency.Key(providerID, title))
}
