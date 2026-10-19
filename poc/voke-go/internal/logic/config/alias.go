// 本文件实现 §7.3.6 a 已决策的 alias bonus 排序钩子（照抄 CmdPal 的 MainListRanker
// 语义）：**触发只认全等**（那是 engine 的事），部分匹配 alias 的输入**不激活**任何
// 模式，只在排序层给该条目加分、把它托底到 Fuzzy tier，保证不被 fuzzy 过滤掉。
//
// 与 CmdPal 的对应关系（§7.3.7 核实的结论）：
//   - 触发层：`AliasManager.cs:48` 的 `TryGetValue(searchText)` —— 全等查表，零
//     StartsWith。voke 侧对应 `engine.Scan` 的全等扫描，本文件**不做触发**。
//   - 排序层：`MainListRanker.cs:98-109` 的 alias 前缀加分 —— 只防止条目被 fuzzy
//     过滤掉。voke 侧就是下面的 `Rank`。
//
// 「bonus 必须在过滤之前生效」（§7.3.6 a 的实现要点）落在代码结构上：Rank 是
// **单趟**计算——每行先算 fuzzy 通道分、再算 alias bonus、合成 tier / score，
// **过滤判定读的是已经加过分之后的 tier**（见 Rank 里的 `if r.Tier == TierDropped
// { continue }`，它在加分之后）。先过滤再加分的写法会让 `#ba` 这种「fuzzy 判不掉」
// 的行永远救不回来，alias_test.go 用一条专门的用例守住这个顺序。

package config

import (
	"cmp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/zyition/voke/poc/voke-go/internal/logic/engine"
)

// Tier 是排序层的分级（分数区间 + 是否被过滤）。
//
// 零值 TierDropped 是「没资格出现」，所以 Rank 的输出切片里根本不含它——
// 忘记赋级的行会被当成「被过滤」，而不是静默出现在结果里。
type Tier uint8

const (
	// TierDropped 表示 fuzzy 判不掉且 alias 也不沾边 → 被过滤掉。
	TierDropped Tier = iota
	// TierFuzzy 表示进了 Fuzzy tier：fuzzy 自己命中，**或**靠 alias bonus 托底进来。
	TierFuzzy
	// TierExact 表示与某个 alias 键**全等**，排在 Fuzzy 之前。
	// 注意：这只是排序分级，**不代表已进入该插件模式**——模式只由 engine.Scan 决定
	// （§7.3.4 铁律②），别把 TierExact 当成触发。
	TierExact
)

// String 仅用于日志与测试失败输出。
func (t Tier) String() string {
	switch t {
	case TierFuzzy:
		return "Fuzzy"
	case TierExact:
		return "Exact"
	default:
		return "Dropped"
	}
}

// alias bonus 的取值。绝对值写死，方便测试断言与真机手感调参。
const (
	// AliasExactBonus 是 alias 键全等的加分。
	AliasExactBonus = 0.5
	// AliasPartialBonus 是部分匹配（互为前缀）的加分——决策 a 里 `#ba` 那一档。
	AliasPartialBonus = 0.2
	// FuzzyTierFloor 是被 bonus 救回来的行的最低分：保证它落在 Fuzzy tier 里、
	// 不与真正的 fuzzy 命中同分（0.05 < AliasPartialBonus，所以托底行永远排在
	// 任何「fuzzy 命中 + bonus」的行之后）。
	FuzzyTierFloor = 0.05
	// keyLengthStep 是「匹配的 alias 键越长、加分越多」的步长：
	// `#ba` 同时与 `#`（picker 条）和 `#b`（base64 条）构成前缀关系，靠这一档
	// 让更具体的键排在前面，不需要另立「最长匹配优先」的第二套规则。
	keyLengthStep = 0.01
)

// Candidate 是排序钩子的一条候选（真实 provider 数据由调用方填；
// 单测用 fake，不依赖任何 provider 落地）。
type Candidate struct {
	// PluginID 是条目所属插件（picker 列表条目用 engine.PickerPluginID）。
	PluginID string
	// Title 是显示名（调用方自己的文案，本包不消费它，只为排序结果能回指条目）。
	Title string
	// Prefix 是**还没被输入吃掉**的前缀。
	// 例子：全局模式下查询串是 `#ba` → 传 "#b"；已经在 PluginPicker 里、
	// 前缀已由模式吞掉、只剩过滤词 `ba` → 传 ""。
	Prefix string
	// Activation 与 Prefix 配对，决定 alias 键的挂法（Indirect 的键带一个尾随空格，
	// 同 CmdPal `CommandAlias.cs:18` 的 SearchPrefix）。
	Activation engine.Activation
	// Aliases 是 Descriptor.Aliases（全小写 ASCII 的约定由配置层保证）。
	Aliases []string
	// Weight 是**归一后**的权重（0..3）。只乘 fuzzy 通道分，不乘 bonus：
	// bonus 是「别让条目消失」的兜底，不是可被权重清零的排名信号。
	Weight float64
	// Fuzzy 是 fuzzy 引擎给的原始分（0..1）。0 表示「fuzzy 判不掉」，
	// 也就是没有 bonus 就会被过滤掉的那种行。
	Fuzzy float32
}

// Ranked 是排序钩子输出的一行（内嵌 Candidate，调用方可直接回指条目）。
type Ranked struct {
	Candidate
	// MatchedKey 是加分依据的 alias 键（无加分为空串），供日志与测试断言。
	MatchedKey string
	// Bonus 是 alias 加分（含 keyLengthStep 档）。
	Bonus float32
	// Score 是最终排序分：fuzzy × weight + bonus（bonus>0 时 fuzzy 通道至少 FuzzyTierFloor）。
	Score float32
	// Tier 是分级，排序先按它、再按 Score。
	Tier Tier
}

// ActivationTarget 返回该条目的触发目标串（与 engine.Scan 的比较目标同形：
// Direct 是前缀本身，Indirect 是前缀 + 一个空格）。
//
// 本包只用它拼 alias 键做**排序加分**；真正的触发判定在 engine，别在这里再判一次。
func ActivationTarget(prefix string, a engine.Activation) string {
	if prefix == "" {
		return ""
	}
	if a == engine.Indirect {
		return prefix + " "
	}
	return prefix
}

// AliasKeys 拼出一条候选的全部 alias 键：触发目标串本身 + 每个 alias。
//
// **不**拼 prefix+alias（如 "#bbase64"）：voke 的 alias 是「匹配用的额外键」，
// 前缀只是路由入口，两者串起来不是任何插件声明过的写法。
func AliasKeys(prefix string, a engine.Activation, aliases []string) []string {
	keys := make([]string, 0, len(aliases)+1)
	if t := ActivationTarget(prefix, a); t != "" {
		keys = append(keys, t)
	}
	for _, alias := range aliases {
		if alias == "" {
			continue
		}
		keys = append(keys, alias)
	}
	return keys
}

// AliasBonus 算一条候选对 query 的 alias 加分（决策 a 的纯函数核心）。
//
// 返回 (bonus, exact, matchedKey)：
//   - query 与某个 alias 键**全等** → AliasExactBonus + 长度档，exact = true；
//   - 互为前缀（query 是键的真前缀，如 `ba` 对 `base64`；**或**键是 query 的前缀，
//     即用户在键后继续打字，如 `#ba` 对 `#b`）→ AliasPartialBonus + 长度档；
//   - 都不沾边 → bonus = 0。
//
// 空 query（trim 后为空）不加分：没有查询词时列表本来就该全出（见 Rank）。
// 多个键同时沾边取**最长**的那个（长度档已把优先级体现在分数上）。
func AliasBonus(query string, keys []string) (float32, bool, string) {
	q := normalizeQuery(query)
	if q == "" {
		return 0, false, ""
	}
	best := ""
	for _, k := range keys {
		// 键只做小写化、**不** trim：Indirect 的键带着那个有意义的尾随空格，
		// 而 trim 过的查询串永远不可能与它全等 → Indirect 条目只会落到「部分匹配」档。
		// 这不是漏判：真正比 Indirect 目标串的是 engine（它只裁前导空白、保留尾部），
		// 排序层没有资格回头改写了触发条件（铁律②）。
		nk := strings.ToLower(k)
		if nk == "" {
			continue
		}
		if nk == q {
			// 全等最高优先，直接收，不再看后面的键。
			return AliasExactBonus + lengthStep(nk), true, nk
		}
		if strings.HasPrefix(q, nk) || strings.HasPrefix(nk, q) {
			if len(nk) > len(best) {
				best = nk
			}
		}
	}
	if best == "" {
		return 0, false, ""
	}
	return AliasPartialBonus + lengthStep(best), false, best
}

// Rank 是排序钩子：打分（含 alias bonus）→ 过滤 → 分级排序。
//
// **顺序是这条函数的全部要点**：tier / score 在同一个循环里算完，过滤判定
// （`Tier == TierDropped`）看的是加过分之后的结果，所以 bonus 生效在过滤之前
// （§7.3.6 a「bonus 必须在过滤之前生效」）。
//
// 排序：先按 Tier 降序（Exact > Fuzzy），同档按 Score 降序，再按输入顺序稳定
// （用 SortStableFunc，注册顺序 / provider 原始序不该被排序打乱）。
func Rank(cands []Candidate, query string) []Ranked {
	q := normalizeQuery(query)
	out := make([]Ranked, 0, len(cands))
	for _, c := range cands {
		r := Ranked{Candidate: c}
		// fuzzy 通道分：weight 已在 C5 归一到 0..3，这里只防手搓 Candidate 传负数。
		w := c.Weight
		if w < MinWeight {
			w = MinWeight
		}
		raw := c.Fuzzy * float32(w)
		// 过滤资格看 fuzzy 的**原始**判定（Fuzzy > 0），不看加权后的分：
		// weight 只折进排序分，0 权重的意思是「排到最后」，不是「丢掉」。
		hit := c.Fuzzy > 0

		bonus, exact, matched := AliasBonus(q, AliasKeys(c.Prefix, c.Activation, c.Aliases))
		r.MatchedKey, r.Bonus = matched, bonus

		switch {
		case q == "":
			// 空查询：不过滤任何行（同生产 plugin_picker_matches 的空过滤器语义）。
			r.Tier, r.Score = TierFuzzy, raw
		case exact:
			r.Tier, r.Score = TierExact, raw+bonus
		case bonus > 0:
			// 部分匹配：托底进 Fuzzy tier。fuzzy 没命中（raw 本来要被判掉）时抬到
			// FuzzyTierFloor，再叠 bonus——**这一分支就是决策 a 的全部兜底**。
			if !hit && raw < FuzzyTierFloor {
				raw = FuzzyTierFloor
			}
			r.Tier, r.Score = TierFuzzy, raw+bonus
		default:
			if hit {
				r.Tier, r.Score = TierFuzzy, raw
			} else {
				r.Tier = TierDropped
			}
		}

		if r.Tier == TierDropped {
			continue // 过滤发生在上一步加分之后
		}
		out = append(out, r)
	}

	slices.SortStableFunc(out, func(a, b Ranked) int {
		if a.Tier != b.Tier {
			return cmp.Compare(b.Tier, a.Tier) // Exact 前于 Fuzzy
		}
		return cmp.Compare(b.Score, a.Score)
	})
	return out
}

// CandidateFor 用归一后的配置填一条候选（接线层少抄一遍字段）。
// 插件不在配置里（未注册 / 被 C1 拒过）返回 false；禁用插件仍会返回，
// 由调用方决定要不要给它出条目。
func (c Configs) CandidateFor(pluginID, title string, fuzzy float32) (Candidate, bool) {
	p, ok := c.Lookup(pluginID)
	if !ok {
		return Candidate{}, false
	}
	return Candidate{
		PluginID:   p.ID,
		Title:      title,
		Prefix:     p.Prefix,
		Activation: p.Activation,
		Aliases:    p.Aliases,
		Weight:     p.Weight,
		Fuzzy:      fuzzy,
	}, true
}

// normalizeQuery 统一做 trim + 小写（§7.3.7 记的 CmdPal 大小写不一致问题，
// 在 voke 侧靠「比较前显式规范化」消掉）。
func normalizeQuery(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// lengthStep 把「匹配的 alias 键越长」折成加分档。
func lengthStep(key string) float32 {
	return float32(utf8.RuneCountInString(key)) * keyLengthStep
}
