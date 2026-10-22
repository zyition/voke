package terminal

import "testing"

// TestActivationArguments 逐字对齐 Rust `profile_activation_arguments`：guid / 显示名
// 都原样裹进双引号，不转义（`%q` 会转义，这里必须不是它）。
func TestActivationArguments(t *testing.T) {
	cases := []struct{ in, want string }{
		{`{61c54bbd-c2c6-5271-96e7-009a8ff7455f}`, `--profile "{61c54bbd-c2c6-5271-96e7-009a8ff7455f}"`},
		{"Ubuntu", `--profile "Ubuntu"`},
		{"", `--profile ""`},
	}
	for _, c := range cases {
		if got := ActivationArguments(c.in); got != c.want {
			t.Errorf("ActivationArguments(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}
