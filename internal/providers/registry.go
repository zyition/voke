// registry.go：插件注册表 + 结果排序管线（对照 `voke-core/engine.rs` 的
// `QueryEngine::rank` / `aggregate`）——把 provider 的原始 Match 汇成
// 「组内按 final_score 降序」的可执行结果 + 单独抽出的提示行。
//
// final_score = provider.weight × (match.score + 档位加分 + 有界使用加成)，
// 由 internal/logic/rank 统一计算（见 docs/notes/ranking.md）。
// 纯逻辑、零 mygo、零 IO；frequency 表由调用方（app 层从磁盘加载）注入。
// apps 走的是它自己的 panelstate 路径，不经本注册表（§10.2 第 12 条：apps 暂不并入）。
package providers

import (
	"sort"

	"voke/internal/logic/frequency"
	"voke/internal/logic/rank"
)

// Registry 是按注册顺序持有 provider 的表（id → Provider；注册顺序 = 分组展示顺序）。
type Registry struct {
	order []string
	byID  map[string]Provider
}

// NewRegistry 依序注册；同 id 后来者就地替换、保留原位置（对照 Rust replace_provider）。
func NewRegistry(ps ...Provider) *Registry {
	r := &Registry{byID: map[string]Provider{}}
	for _, p := range ps {
		if _, dup := r.byID[p.ID()]; !dup {
			r.order = append(r.order, p.ID())
		}
		r.byID[p.ID()] = p
	}
	return r
}

// Get 按 id 取 provider。
func (r *Registry) Get(id string) (Provider, bool) {
	p, ok := r.byID[id]
	return p, ok
}

// IDs 返回注册顺序的 id 列表（副本）。
func (r *Registry) IDs() []string { return append([]string(nil), r.order...) }

// RankedMatch 是一条排好序的结果（原始 Match + 最终分）。
type RankedMatch struct {
	Match
	FinalScore float64
}

// Rank 计算最终排序分并按降序**稳定**排序（同分保持原序，对照 Rust 的 sort_by +
// 稳定排序）。最终分由 internal/logic/rank 统一给出：
//
//	Final = weight × (nucleo 分 + 档位加分 + 有界使用加成)
//
// freq 为 nil 时使用加成为 0。
func Rank(providerID string, weight float64, ms []Match, freq frequency.Store) []RankedMatch {
	out := make([]RankedMatch, 0, len(ms))
	for _, m := range ms {
		out = append(out, RankedMatch{
			Match: m,
			FinalScore: rank.Final(m.Quality, m.Score, weight,
				rank.UsageOf(freq, providerID, m.Title)),
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].FinalScore > out[j].FinalScore })
	return out
}

// RunOne 跑单个 provider 并聚合：可执行结果按 Rank 排序，提示行按声明顺序单列
// （不进排序，对照 aggregate 的 hint 抽取）。
func RunOne(p Provider, ctx QueryCtx, freq frequency.Store) (ranked []RankedMatch, hints []Match) {
	var exec []Match
	for _, m := range p.Query(ctx) {
		if m.IsHint() {
			hints = append(hints, m)
		} else {
			exec = append(exec, m)
		}
	}
	return Rank(p.ID(), p.Config().Weight, exec, freq), hints
}

// Batch 是一个 provider 的一次查询产出（对照 Rust `ProviderBatch`）。
type Batch struct {
	ProviderID string
	Matches    []Match
}

// Group 是聚合后一个 provider 的分组（组内已按 final_score 降序）。
type Group struct {
	ProviderID string
	Matches    []RankedMatch
}

// Aggregated 是分组聚合的结果：按 provider 分组 + 单独抽出的提示行（对照 Rust
// `Aggregated`）。groups 保持传入 batches 的顺序（= 注册顺序）；hints 保持各 provider
// 的声明顺序、不参与排序、不进任何组（它们只是说明书）。
type Aggregated struct {
	Groups []Group
	Hints  []Match
}

// Aggregate 把多个 provider 的结果分组成一份可渲染的列表——对照 Rust
// `QueryEngine::aggregate`（方案 A 的排序形态）：
//   - 每个 provider 用**其配置 weight**（注册表里查不到按 1.0 兜底）调 Rank；
//   - `Interact::Hint` 条目抽到 Aggregated.Hints（跨批按声明顺序累积，不进组不排序）；
//   - 某 provider 只有提示、没有可执行结果 → 不产生组；
//   - groups 顺序 = batches 传入顺序（调用方按注册顺序传）。
//
// 为什么分组而非全局混排：全局混排会把某插件的时长/候选插进应用结果中间，分组后
// 「同一插件的候选」始终相邻、视觉可辨。freq 为 nil 时 boost 恒 1.0（同 Rank）。
func (r *Registry) Aggregate(batches []Batch, freq frequency.Store) Aggregated {
	var agg Aggregated
	for _, b := range batches {
		weight := 1.0
		if p, ok := r.Get(b.ProviderID); ok {
			weight = p.Config().Weight
		}
		var exec []Match
		for _, m := range b.Matches {
			if m.IsHint() {
				agg.Hints = append(agg.Hints, m)
			} else {
				exec = append(exec, m)
			}
		}
		if len(exec) == 0 {
			continue
		}
		agg.Groups = append(agg.Groups, Group{
			ProviderID: b.ProviderID,
			Matches:    Rank(b.ProviderID, weight, exec, freq),
		})
	}
	return agg
}
