// registry/RunOne/Rank 的测试。用外部测试包 providers_test（base64 子包 import 了
// providers，若在本包内 import base64 会成环）。以 base64 为真实 provider，验证排序、
// 频率加成重排、提示行分离——对照 engine.rs 的 rank/aggregate 语义。
package providers_test

import (
	"testing"

	"voke/internal/logic/frequency"
	"voke/internal/providers"
	"voke/internal/providers/base64"
)

func TestRegistryGetAndIDs(t *testing.T) {
	b := base64.New()
	r := providers.NewRegistry(b)
	if got := r.IDs(); len(got) != 1 || got[0] != "base64" {
		t.Errorf("IDs = %v，想要 [base64]", got)
	}
	if p, ok := r.Get("base64"); !ok || p.Name() != "Base64" {
		t.Errorf("Get(base64) = %v,%v", p, ok)
	}
	if _, ok := r.Get("nope"); ok {
		t.Error("未注册 id 不该命中")
	}
}

func TestRunOneLevel1RankedDesc(t *testing.T) {
	ranked, hints := providers.RunOne(base64.New(), providers.QueryCtx{}, nil)
	if len(hints) != 0 {
		t.Errorf("第一级不该有提示行：%+v", hints)
	}
	if len(ranked) != 2 {
		t.Fatalf("第一级应 2 行，实得 %d", len(ranked))
	}
	// weight=1.0、freq=nil → final = score：加密 1.0 > 解密 0.9。
	if ranked[0].Title != "加密" || ranked[0].FinalScore != 1.0 {
		t.Errorf("首行应为 加密/1.0：%+v", ranked[0])
	}
	if ranked[1].Title != "解密" || ranked[1].FinalScore != 0.9 {
		t.Errorf("次行应为 解密/0.9：%+v", ranked[1])
	}
}

// TestFrequencyBoostReorders 证频率真的进了排序：给「解密」记一次使用，其
// final = 0.9 × Boost(1)=0.9×1.3466≈1.212 > 加密的 1.0 → 顺序翻转。
func TestFrequencyBoostReorders(t *testing.T) {
	freq := frequency.New()
	freq.Record(frequency.Key("base64", "解密"))
	ranked, _ := providers.RunOne(base64.New(), providers.QueryCtx{}, freq)
	if len(ranked) != 2 || ranked[0].Title != "解密" {
		t.Fatalf("记一次使用后「解密」应升到首位，实得 %+v", ranked)
	}
	if !(ranked[0].FinalScore > ranked[1].FinalScore) {
		t.Errorf("降序不成立：%v !> %v", ranked[0].FinalScore, ranked[1].FinalScore)
	}
}

func TestRunOneSeparatesHints(t *testing.T) {
	// dec 子模式喂非法 base64 → provider 出一条提示行、无结果行。
	ranked, hints := providers.RunOne(base64.New(), providers.QueryCtx{SubMode: base64.SubModeDec, Query: "!!!"}, nil)
	if len(ranked) != 0 {
		t.Errorf("非法解码不该有可执行结果：%+v", ranked)
	}
	if len(hints) != 1 || !hints[0].IsHint() {
		t.Errorf("应抽出 1 条提示行：%+v", hints)
	}
}

func execMatch(id, title string, score float64) providers.Match {
	return providers.MatchExecute(id, title, "", providers.Action{Kind: providers.ActionCopy, Text: title}, score)
}

func hintMatch(id, title string) providers.Match {
	return providers.MatchHint(id, title, "说明")
}

// TestAggregateGroupsOrderAndHints 对照 engine.rs::aggregate：groups 按传入 batches 顺序、
// 每组内 Rank 降序；跨批的 Hint 按声明顺序累积到 Hints、不进任何组；只有提示的 provider
// 不产生组；注册表里查不到的 id 按 weight=1.0 兜底。
func TestAggregateGroupsOrderAndHints(t *testing.T) {
	b := base64.New()
	b.SetConfig(providers.ProviderConfig{Enabled: true, Weight: 2.0}) // 放大权重看 final
	r := providers.NewRegistry(b)

	batches := []providers.Batch{
		{ProviderID: "base64", Matches: []providers.Match{
			execMatch("base64", "e1", 0.5), hintMatch("base64", "h1"), execMatch("base64", "e2", 1.0),
		}},
		{ProviderID: "ghost", Matches: []providers.Match{hintMatch("ghost", "h2")}},        // 无 exec → 不成组
		{ProviderID: "ghost2", Matches: []providers.Match{execMatch("ghost2", "g1", 0.3)}}, // 未注册 → weight 1.0
	}
	agg := r.Aggregate(batches, nil)

	if len(agg.Groups) != 2 {
		t.Fatalf("应 2 组（base64 + ghost2），ghost 只有提示不成组：%+v", agg.Groups)
	}
	if agg.Groups[0].ProviderID != "base64" || agg.Groups[1].ProviderID != "ghost2" {
		t.Errorf("groups 顺序 = [%s,%s]，想要 [base64,ghost2]", agg.Groups[0].ProviderID, agg.Groups[1].ProviderID)
	}
	// base64 weight=2.0：e2=2.0×1.0=2.0 在 e1=2.0×0.5=1.0 前。
	g0 := agg.Groups[0].Matches
	if len(g0) != 2 || g0[0].Title != "e2" || g0[0].FinalScore != 2.0 || g0[1].Title != "e1" || g0[1].FinalScore != 1.0 {
		t.Errorf("base64 组内应按 weight×score 降序 [e2=2.0,e1=1.0]：%+v", g0)
	}
	// ghost2 未注册 → weight 兜底 1.0 → final = 1.0×0.3。
	if f := agg.Groups[1].Matches[0].FinalScore; f != 0.3 {
		t.Errorf("未注册 id weight 兜底 1.0，final 应 0.3：%v", f)
	}
	// hints 跨批按声明顺序：h1（批0）→ h2（批1）。
	if len(agg.Hints) != 2 || agg.Hints[0].Title != "h1" || agg.Hints[1].Title != "h2" {
		t.Errorf("hints 顺序 = %+v，想要 [h1,h2]", agg.Hints)
	}
}

// TestAggregateAppliesFrequencyBoostWithinGroup 证 Aggregate 里的 Rank 也吃频率：给
// ghost2 的 g1 记一次使用，boost 把它抬过 base64 组的相对值不影响，但组内若有两条应重排。
func TestAggregateAppliesFrequencyBoost(t *testing.T) {
	r := providers.NewRegistry(base64.New())
	freq := frequency.New()
	freq.Record(frequency.Key("base64", "low")) // 低分的记一次 → 升到高分以上
	batches := []providers.Batch{
		{ProviderID: "base64", Matches: []providers.Match{
			execMatch("base64", "low", 0.4), execMatch("base64", "high", 0.5),
		}},
	}
	agg := r.Aggregate(batches, freq)
	g := agg.Groups[0].Matches
	if g[0].Title != "low" {
		t.Errorf("记一次使用后 low 应因频率加成升到组首：%+v", g)
	}
}
