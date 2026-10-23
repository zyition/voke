// logic/frequency 的测试：钉死「次数 → 有界加成」的形状、键格式、记录资格与清洗。
package frequency

import (
	"math"
	"testing"
)

// TestUsageBonusShape：0 次 = 0、单调不减、有界（≤ UsageCap）、对数而非线性。
func TestUsageBonusShape(t *testing.T) {
	if got := UsageBonus(0); got != 0 {
		t.Errorf("UsageBonus(0) = %v，想要 0", got)
	}
	if !(UsageBonus(1) > 0) {
		t.Errorf("UsageBonus(1) 应 >0，实得 %v", UsageBonus(1))
	}
	for i := uint32(1); i <= 2000; i++ {
		if UsageBonus(i) < UsageBonus(i-1) {
			t.Fatalf("UsageBonus 在 %d 处非单调（%v < %v）", i, UsageBonus(i), UsageBonus(i-1))
		}
	}
	// 有界：任意次数都不得越过上限（上限必须小于两档差，见 ranking.md）。
	if got := UsageBonus(1 << 30); math.Abs(got-UsageCap) > 1e-9 {
		t.Errorf("UsageBonus(2^30) = %v，想要封顶 %v", got, UsageCap)
	}
	// 对数增长：100 次的加成不到 1 次的 8 倍（线性会踩中）。
	if !(UsageBonus(100) < UsageBonus(1)*8) {
		t.Errorf("增长应是对数而非线性：100 次 %v，1 次 %v", UsageBonus(100), UsageBonus(1))
	}
	// 曲线上的关键点（文档 §1 写死的刻度）。
	for _, c := range []struct {
		n    uint32
		want float64
	}{
		{1, 0.017}, {5, 0.045}, {31, 0.086}, {100, 0.115},
	} {
		if got := UsageBonus(c.n); math.Abs(got-c.want) > 1e-3 {
			t.Errorf("UsageBonus(%d) = %.4f，期望 ≈%.3f", c.n, got, c.want)
		}
	}
}

// TestUsageBonusNeverCrossesTwoTiers：使用加成必须落在「一档 < 加成 < 两档」之间，
// 保证「用得多」最多顶掉一档（档位差来自 rank.TierBonus）。
func TestUsageBonusNeverCrossesTwoTiers(t *testing.T) {
	const tierStep = 0.10 // rank.TierBonus 的相邻差
	if UsageCap >= 2*tierStep {
		t.Fatalf("UsageCap=%v 必须 < 两档差 %v", UsageCap, 2*tierStep)
	}
	if UsageCap <= tierStep {
		t.Fatalf("UsageCap=%v 应大于一档差 %v（否则使用习惯对跨档完全无效）", UsageCap, tierStep)
	}
}

func TestKeyFormat(t *testing.T) {
	if got := Key("apps", "微信"); got != "apps:微信" {
		t.Errorf("Key = %q，想要 %q", got, "apps:微信")
	}
	if Key("apps", "x") == Key("calc", "x") {
		t.Error("同标题不同 provider 应得不同 key")
	}
	if got := ProviderOf("terminal:PowerShell"); got != "terminal" {
		t.Errorf("ProviderOf = %q，想要 terminal", got)
	}
}

func TestRecordable(t *testing.T) {
	for _, id := range []string{"apps", "workspaces", "terminal"} {
		if !Recordable(id) {
			t.Errorf("%q 应可记录", id)
		}
	}
	// 生成值 / 一次性命令 / 易变标题：都不该记（真实样本里都出现过脏键）。
	for _, id := range []string{"awake", "uuid", "random", "calculator", "base64", "timestamp", "windows", "clipboard", "unknown-plugin"} {
		if Recordable(id) {
			t.Errorf("%q 不应可记录", id)
		}
	}
}

func TestStoreRecordAndUsage(t *testing.T) {
	s := New()
	if s.Count("apps:微信") != 0 || s.UsageOf("apps:微信") != 0 {
		t.Fatal("空表应有缺省 0 次 / 0 加成")
	}
	for i := 0; i < 3; i++ {
		s.Record("apps:微信")
	}
	if s.Count("apps:微信") != 3 {
		t.Errorf("记 3 次后 Count = %d，想要 3", s.Count("apps:微信"))
	}
	if want := UsageBonus(3); s.UsageOf("apps:微信") != want {
		t.Errorf("UsageOf = %v，想要 %v", s.UsageOf("apps:微信"), want)
	}
}

// TestPrune：旧版本记下的非查找类条目在加载时被清掉，查找类保留。
func TestPrune(t *testing.T) {
	s := Store{
		"apps:微信":                        5,
		"terminal:PowerShell":            3,
		"uuid:5cf3be2c-bec6-43bf-a4ad-1": 1,
		"random:8USAgNEtFt":              1,
		"awake:保持唤醒中 · 屏幕关闭（还剩 3 小时）": 1,
		"windows:(1) 主页 / X - Chrome": 4,
	}
	got, dropped := Prune(s)
	if dropped != 4 {
		t.Errorf("dropped = %d，想要 4", dropped)
	}
	if len(got) != 2 || got["apps:微信"] != 5 || got["terminal:PowerShell"] != 3 {
		t.Errorf("清洗后 = %v，期望只剩 apps/terminal 两条", got)
	}
	if _, again := Prune(got); again != 0 {
		t.Errorf("再次清洗应为 0 条，实得 %d", again)
	}
}
