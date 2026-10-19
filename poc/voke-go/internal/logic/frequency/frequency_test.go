// logic/frequency 的测试：以生产 Rust（voke-core engine.rs 的
// `frequency_boost_grows_with_use`）为 oracle，钉死对数加成语义 + 键格式 +
// 记次行为。改坏 Boost 公式 / Key 拼接 / Record 递增都会红。
package frequency

import "testing"

// TestBoostOracle 对照 Rust 同名用例：0 次=1.0、单调递增、对数（非线性）增长。
func TestBoostOracle(t *testing.T) {
	if got := Boost(0); got != 1.0 {
		t.Errorf("Boost(0) = %v，想要 1.0", got)
	}
	if !(Boost(1) > 1.0) {
		t.Errorf("Boost(1) 应 >1.0，实得 %v", Boost(1))
	}
	if !(Boost(10) > Boost(1)) {
		t.Errorf("Boost(10) 应 > Boost(1)，实得 %v / %v", Boost(10), Boost(1))
	}
	// 对数增长：100 次远小于 1 次的 20 倍（线性增长会踩中此条）。
	if !(Boost(100) < Boost(1)*20.0) {
		t.Errorf("增长应是对数而非线性：Boost(100)=%v，Boost(1)×20=%v", Boost(100), Boost(1)*20.0)
	}
	// 单调不减。
	for i := uint32(1); i <= 200; i++ {
		if Boost(i) < Boost(i-1) {
			t.Fatalf("Boost 在 %d 处非单调（%v < %v）", i, Boost(i), Boost(i-1))
		}
	}
}

func TestKeyFormat(t *testing.T) {
	if got := Key("apps", "微信"); got != "apps:微信" {
		t.Errorf("Key = %q，想要 %q（须与 Rust `\"{provider_id}:{}\"` 一致）", got, "apps:微信")
	}
	// 不同 provider 的同名条目必须分到不同键（Rust 注释的初衷）。
	if Key("apps", "x") == Key("calc", "x") {
		t.Error("同标题不同 provider 应得不同 key")
	}
}

func TestStoreRecordAndBoost(t *testing.T) {
	s := New()
	if s.Count("apps:微信") != 0 || s.BoostOf("apps:微信") != 1.0 {
		t.Fatal("空表应有缺省 0 次 / 1.0 加成")
	}
	s.Record("apps:微信")
	s.Record("apps:微信")
	s.Record("apps:微信")
	if s.Count("apps:微信") != 3 {
		t.Errorf("记 3 次后 Count = %d，想要 3", s.Count("apps:微信"))
	}
	if want := Boost(3); s.BoostOf("apps:微信") != want {
		t.Errorf("BoostOf = %v，想要 Boost(3)=%v", s.BoostOf("apps:微信"), want)
	}
}
