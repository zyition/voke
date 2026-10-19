// §7.3.6 a（决策 a / §10.3 已定案 6）的 alias bonus 单测。
//
// 三件事各自有专门用例守着，都是「改坏实现就变红」的断言：
//   A. 部分匹配**不激活**：`#ba` 让 engine.Scan 留在 Global（table_test.go 也有一条，
//      这里再验排序层没有偷偷替它切模式）。
//   B. 部分匹配**被加分托底**：`#ba` 的 base64 行 Fuzzy = 0（本来会被 fuzzy 判掉），
//      靠 bonus 留在 Fuzzy tier —— 断言它出现在结果里，且分数 = 绝对值 0.27。
//   C. bonus **生效在过滤之前**：B 那条就是；另有一条「去掉 bonus 就消失」的对照行，
//      保证不是「反正都能过 fuzzy」的空转断言。
//
// 变异测试记录（每条都实测过：改坏 → 红 → 还原；括号里是实测红掉的用例）：
//   M1 Rank 改成「先按 Fuzzy>0 过滤、再算 bonus」（即 bonus 生效在过滤**之后**）
//      → TestRankBonusBeforeFilter（只剩 1 行 apps，base64 / picker 消失）
//      + TestRankExactAliasBeatsFuzzy + TestRankWeightAppliesToFuzzyChannelOnly 红
//   M2 C5 的 clamp 上限 3.0 → 30.0 → TestNormalizeWeightClampedSilently 红
//      （weight 9 → 9、3.0001 → 3.0001）
//   M3 C2 唯一性的报错出口关掉 → TestNormalizePrefixCollisionNamesBothPlugins 红
//      + TestNormalizePrefixlessAndDisabledStillValid（禁用项仍占前缀那条）红
//   M4 FuzzyTierFloor 0.05 → 0（托底失效）→ TestRankBonusBeforeFilter（0.22 ≠ 0.27）
//      + TestRankWeightAppliesToFuzzyChannelOnly 红
//   M5 ActivationTarget 永远返回空串（AliasKeys 丢掉触发目标串）→
//      TestActivationTargetAndAliasKeys + TestAliasBonusValues + 三条 Rank 用例一起红

package config

import (
	"reflect"
	"testing"

	"github.com/zyition/voke/poc/voke-go/internal/logic/engine"
)

func TestActivationTargetAndAliasKeys(t *testing.T) {
	if got := ActivationTarget("#b", engine.Direct); got != "#b" {
		t.Errorf("Direct 目标串 = %q，期望 \"#b\"", got)
	}
	if got := ActivationTarget("ba", engine.Indirect); got != "ba " {
		t.Errorf("Indirect 目标串 = %q，期望 \"ba \"（带一个空格，同 CmdPal SearchPrefix）", got)
	}
	if got := ActivationTarget("", engine.Indirect); got != "" {
		t.Errorf("无前缀的目标串 = %q，期望空串", got)
	}

	// 键 = 触发目标串 + 各 alias；**不**拼 prefix+alias（"#bbase64" 不是任何插件声明过的写法）。
	if got := AliasKeys("#b", engine.Direct, []string{"b", "b64"}); !reflect.DeepEqual(got, []string{"#b", "b", "b64"}) {
		t.Errorf("AliasKeys = %q", got)
	}
	// 前缀已被模式吃掉时（PluginPicker 里只剩过滤词），传空前缀 → 只剩裸 alias。
	if got := AliasKeys("", engine.Direct, []string{"b", "b64"}); !reflect.DeepEqual(got, []string{"b", "b64"}) {
		t.Errorf("AliasKeys（无前缀）= %q", got)
	}
	if got := AliasKeys("ba", engine.Indirect, []string{"b", ""}); !reflect.DeepEqual(got, []string{"ba ", "b"}) {
		t.Errorf("AliasKeys（Indirect + 空 alias）= %q，期望丢掉空串", got)
	}
}

func TestAliasBonusValues(t *testing.T) {
	keys := AliasKeys("#b", engine.Direct, []string{"b", "b64"}) // ["#b","b","b64"]
	tests := []struct {
		name        string
		query       string
		wantBonus   float32
		wantExact   bool
		wantMatched string
	}{
		{"全等触发键", "#b", 0.52, true, "#b"},
		{"继续打字 → 部分匹配（决策 a 的那一档）", "#ba", 0.22, false, "#b"},
		{"部分匹配更长的输入", "#b64x", 0.22, false, "#b"},
		{"alias 全等", "b64", 0.53, true, "b64"},
		{"毫不相干", "#zz", 0, false, ""},
		{"空查询不加分（本来就该全出）", "  ", 0, false, ""},
		{"大小写不敏感", "#BA", 0.22, false, "#b"},
	}
	for _, tt := range tests {
		bonus, exact, matched := AliasBonus(tt.query, keys)
		if !almost(bonus, tt.wantBonus) || exact != tt.wantExact || matched != tt.wantMatched {
			t.Errorf("%s：AliasBonus(%q) = (%v,%v,%q)，期望 (%v,%v,%q)",
				tt.name, tt.query, bonus, exact, matched, tt.wantBonus, tt.wantExact, tt.wantMatched)
		}
	}

	// picker 那条（前缀 "#"、无 alias）也沾 `#ba` 的前缀关系，但加分更少：
	// 靠长度档天然让更具体的键排前面，不需要另立「最长匹配优先」第二套规则。
	pickerKeys := AliasKeys("#", engine.Direct, nil)
	if bonus, _, matched := AliasBonus("#ba", pickerKeys); !almost(bonus, 0.21) || matched != "#" {
		t.Errorf("picker 键对 \"#ba\" 的加分 = %v/%q，期望 0.21 / \"#\"", bonus, matched)
	}

	// Indirect 的键带尾随空格，trim 过的查询串永远不可能与它全等 → 只拿部分匹配档。
	indirectKeys := AliasKeys("ba", engine.Indirect, []string{"b64"})
	if bonus, exact, matched := AliasBonus("ba", indirectKeys); exact || !almost(bonus, 0.23) || matched != "ba " {
		t.Errorf("Indirect 裸前缀：(%v,%v,%q)，期望部分匹配 0.23 / false / \"ba \"", bonus, exact, matched)
	}
}

// TestRankBonusBeforeFilter 是决策 a 的核心用例：`#ba` 不激活，但 base64 行
// 靠 alias bonus 在**过滤之前**被托底进 Fuzzy tier。
func TestRankBonusBeforeFilter(t *testing.T) {
	cands := []Candidate{
		{PluginID: "base64", Title: "Base64", Prefix: "#b", Activation: engine.Direct,
			Aliases: []string{"b", "b64"}, Weight: 1.0, Fuzzy: 0}, // fuzzy 判不掉的一行
		{PluginID: engine.PickerPluginID, Title: "列出所有插件", Prefix: "#", Activation: engine.Direct,
			Weight: 1.0, Fuzzy: 0}, // 同族前缀，沾边但键更短
		{PluginID: "apps", Title: "计算器", Prefix: "", Activation: engine.Direct,
			Aliases: []string{"calc"}, Weight: 1.0, Fuzzy: 0.4}, // 真正的 fuzzy 命中
		{PluginID: "windows", Title: "窗口", Prefix: "=", Activation: engine.Direct,
			Aliases: []string{"win", "ck"}, Weight: 1.0, Fuzzy: 0}, // 既不 fuzzy 也不沾 alias
	}

	out := Rank(cands, "#ba")

	// 没有 bonus 的话，base64 / picker 都会因 Fuzzy == 0 被过滤掉：
	// 它们在这儿，就是「加分先于过滤」的证据（把这段删掉→ 变异 M1 会红）。
	if len(out) != 3 {
		t.Fatalf("结果行数 = %d（%v），期望 3：apps / base64 / __picker，windows 该被过滤",
			len(out), rankedIDs(out))
	}
	if got := rankedIDs(out); !reflect.DeepEqual(got, []string{"apps", "base64", engine.PickerPluginID}) {
		t.Errorf("排序结果 = %v，期望 [apps base64 __picker]（0.4 > 0.27 > 0.26）", got)
	}

	// 托底行的分值写绝对值：FuzzyTierFloor(0.05) + AliasPartialBonus(0.2) + 2 字符长度档(0.02)。
	b64 := out[1]
	if b64.Tier != TierFuzzy || !almost(b64.Bonus, 0.22) || !almost(b64.Score, 0.27) || b64.MatchedKey != "#b" {
		t.Errorf("base64 托底行 = %+v，期望 Tier=Fuzzy Bonus≈0.22 Score≈0.27 MatchedKey=\"#b\"", b64)
	}
	if b64.Fuzzy != 0 {
		t.Errorf("base64 的 fuzzy 原始分被改了？%v", b64.Fuzzy)
	}
	picker := out[2]
	if !almost(picker.Score, 0.26) || picker.MatchedKey != "#" {
		t.Errorf("picker 行 = %+v，期望 Score≈0.26 MatchedKey=\"#\"", picker)
	}

	// 没有任何一行拿到 TierExact：`#ba` 与哪个键都不全等 → 排序层没有偷偷「当它触发了」。
	for _, r := range out {
		if r.Tier == TierExact {
			t.Errorf("行 %s 被判成 TierExact，但 \"%s\" 并不全等任何 alias 键", r.PluginID, "#ba")
		}
	}
	// 配套：模式那边确实没动（触发只认全等）。
	table := mustNormalize(t, fakeRegistry(), nil).BuildTable()
	if got := engine.Scan(table, engine.State{}, "#ba"); got.Mode() != engine.Global {
		t.Errorf("engine.Scan(\"#ba\") 的 Mode = %v，期望 Global（不激活）", got.Mode())
	}
}

func TestRankExactAliasBeatsFuzzy(t *testing.T) {
	cands := []Candidate{
		{PluginID: "strong-fuzzy", Title: "Calendar", Prefix: "", Activation: engine.Direct,
			Aliases: []string{"calc"}, Weight: 1.0, Fuzzy: 0.9},
		{PluginID: "base64", Title: "Base64", Prefix: "#b", Activation: engine.Direct,
			Aliases: []string{"b", "b64"}, Weight: 1.0, Fuzzy: 0},
	}
	out := Rank(cands, "#b")

	// 分级优先于分数：TierExact 的 base64 走在 fuzzy 0.9 的前面。
	if len(out) != 2 || out[0].PluginID != "base64" || out[0].Tier != TierExact {
		t.Fatalf("结果 = %+v，期望 base64(TierExact) 在前", out)
	}
	if !almost(out[0].Score, 0.52) || out[0].MatchedKey != "#b" {
		t.Errorf("base64 全等行 = %+v，期望 Score≈0.52 MatchedKey=\"#b\"", out[0])
	}
	if out[1].Tier != TierFuzzy || !almost(out[1].Score, 0.9) {
		t.Errorf("fuzzy 行 = %+v，期望 Tier=Fuzzy Score≈0.9", out[1])
	}

	// TierExact **不等于**已激活：`#b` 之所以进插件是 engine 的全等扫描，
	// 而 "b64"（alias 全等，本包给它 TierExact）压根不是任何前缀，模式仍是 Global。
	table := mustNormalize(t, fakeRegistry(), nil).BuildTable()
	exactOut := Rank(cands, "b64")
	// strong-fuzzy 那行 fuzzy 0.9 仍在（alias 不沾边 → TierFuzzy），base64 靠 alias 全等领先。
	if len(exactOut) != 2 || exactOut[0].PluginID != "base64" || exactOut[0].Tier != TierExact {
		t.Fatalf("Rank(\"b64\") = %+v，期望 base64(TierExact) 在前、共 2 行", exactOut)
	}
	if !almost(exactOut[0].Score, 0.53) || exactOut[0].MatchedKey != "b64" {
		t.Errorf("Rank(\"b64\") 的 base64 = %+v，期望 Score≈0.53 MatchedKey=\"b64\"", exactOut[0])
	}
	if exactOut[1].PluginID != "strong-fuzzy" || exactOut[1].Tier != TierFuzzy {
		t.Errorf("Rank(\"b64\") 第二行 = %+v，期望 strong-fuzzy / TierFuzzy", exactOut[1])
	}
	if got := engine.Scan(table, engine.State{}, "b64"); got.Mode() != engine.Global {
		t.Errorf("engine.Scan(\"b64\") = %+v，期望留在 Global（加分不加触发）", got)
	}
}

func TestRankWeightAppliesToFuzzyChannelOnly(t *testing.T) {
	cands := []Candidate{
		{PluginID: "hi", Prefix: "", Activation: engine.Direct, Weight: 3.0, Fuzzy: 0.5},
		{PluginID: "zero", Prefix: "", Activation: engine.Direct, Weight: 0.0, Fuzzy: 0.5},
		{PluginID: "base64", Prefix: "#b", Activation: engine.Direct, Aliases: []string{"b"}, Weight: 0.0, Fuzzy: 0},
	}
	out := Rank(cands, "#ba")

	// 0 权重 = 排到最后，不是被丢掉（过滤看 fuzzy 的原始判定）。
	if got := rankedIDs(out); !reflect.DeepEqual(got, []string{"hi", "base64", "zero"}) {
		t.Fatalf("结果 = %v，期望 [hi base64 zero]", got)
	}
	if !almost(out[0].Score, 1.5) {
		t.Errorf("hi 分数 = %v，期望 1.5（0.5 × 3）", out[0].Score)
	}
	// bonus 不乘 weight：weight 0 的托底行照样拿满 0.27。
	if !almost(out[1].Score, 0.27) {
		t.Errorf("base64 分数 = %v，期望 0.27（bonus 不受 weight 清零）", out[1].Score)
	}
	if !almost(out[2].Score, 0.0) {
		t.Errorf("zero 分数 = %v，期望 0（0.5 × 0）", out[2].Score)
	}
}

func TestRankEmptyQueryKeepsAllInOrder(t *testing.T) {
	cands := []Candidate{
		{PluginID: "base64", Prefix: "#b", Activation: engine.Direct, Aliases: []string{"b"}, Weight: 1.0, Fuzzy: 0},
		{PluginID: "windows", Prefix: "=", Activation: engine.Direct, Weight: 1.0, Fuzzy: 0},
		{PluginID: "apps", Prefix: "", Activation: engine.Direct, Weight: 1.0, Fuzzy: 0},
	}
	out := Rank(cands, "   ")
	// 空查询全出，并且保持输入序（同分 + SortStableFunc）。
	if got := rankedIDs(out); !reflect.DeepEqual(got, []string{"base64", "windows", "apps"}) {
		t.Fatalf("空查询该全出且保持输入序，实得 %v", got)
	}
	for _, r := range out {
		if r.Bonus != 0 || r.MatchedKey != "" || !almost(r.Score, 0) {
			t.Errorf("空查询不该产生 alias 加分、也不该丢行：%+v", r)
		}
	}

	// 对照：随便打两个不相干的字符，这三行会**全部**被过滤掉——
	// 说明上面「全出」是空查询那条分支给的，不是这些行本来就能过 fuzzy。
	if dropped := Rank(cands, "zz"); len(dropped) != 0 {
		t.Errorf("Rank(zz) = %v，期望 0 行（fuzzy 判不掉且 alias 不沾边）", rankedIDs(dropped))
	}
}

func TestConfigsCandidateFor(t *testing.T) {
	c := mustNormalize(t, fakeRegistry(), nil)

	got, ok := c.CandidateFor("base64", "Base64", 0)
	if !ok {
		t.Fatal("CandidateFor(base64) 失败")
	}
	want := Candidate{
		PluginID: "base64", Title: "Base64", Prefix: "@b", Activation: engine.Direct,
		Aliases: []string{"b", "b64"}, Weight: 1.0, Fuzzy: 0,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Candidate = %+v，期望 %+v", got, want)
	}

	if _, ok := c.CandidateFor("nope", "x", 0); ok {
		t.Error("未注册的 id 该返回 false")
	}
}

// rankedIDs 把结果里的 PluginID 抽出来方便断言（失败信息能一眼看清顺序）。
func rankedIDs(rs []Ranked) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.PluginID)
	}
	return out
}
