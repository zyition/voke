// providers 契约模型的基础测试：钉死三种 Match 构造的字段/Interact 语义、
// hint/selectable 谓词、默认配置与 QueryCtx 表达。改坏构造或谓词会红。
package providers

import "testing"

func TestMatchConstructors(t *testing.T) {
	act := Action{Kind: ActionCopy, Text: "hello"}
	ex := MatchExecute("calc", "42", "= 6*7", act, 1.0)
	if ex.ProviderID != "calc" || ex.Title != "42" || ex.Action.Kind != ActionCopy ||
		ex.Action.Text != "hello" || ex.Score != 1.0 || ex.Interact.Kind != InteractExecute {
		t.Errorf("MatchExecute 字段不符：%+v", ex)
	}
	if ex.IsHint() || !ex.IsSelectable() {
		t.Error("Execute 行必须可选、非 hint")
	}

	hint := MatchHint("calc", "怎么用", "输入表达式")
	if !hint.IsHint() || hint.IsSelectable() {
		t.Error("Hint 行必须不可选")
	}
	if hint.Action.Kind != ActionNone || hint.Score != 0.0 {
		t.Errorf("Hint 行 Action 应为 None、Score 0：%+v", hint)
	}

	sub := MatchSubquery("b64", "加密", "→ Base64", "enc", 0.5)
	if sub.Interact.Kind != InteractEnterSubquery || sub.Interact.Sub != "enc" {
		t.Errorf("MatchSubquery 应带 EnterSubquery + sub=enc：%+v", sub.Interact)
	}
	if !sub.IsSelectable() || sub.IsHint() {
		t.Error("EnterSubquery 行可选")
	}
}

func TestDefaultConfig(t *testing.T) {
	c := DefaultProviderConfig()
	if !c.Enabled || !c.IsGlobal || c.Weight != 1.0 || c.Activation != "Direct" || c.Count != 5 {
		t.Errorf("默认配置不符（对照 Rust ProviderConfig::default）：%+v", c)
	}
}
