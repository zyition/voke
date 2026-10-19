// selftest.go 的 headless 自证：awakeSelftest 的状态链在 Linux 也能全跑（Go 侧独立跟踪
// 激活态），env 开关的三分支。跑真机才有的收益（SetThreadExecutionState 真的置位）不在
// 单测断言范围——那是 V 的事；单测守的是「自检这套判据不是空转」：改坏 expiresAt 写入或
// provider 取消项漂成文案，awakeSelftest 的失败计数就该 >0。
package app

import (
	"testing"

	"github.com/zyition/voke/poc/voke-go/internal/platform"
)

func TestAwakeSelftestPasses(t *testing.T) {
	platform.CancelAwake() // 别被上一用例残留的状态带偏
	if f := awakeSelftest(); f != 0 {
		t.Errorf("awakeSelftest 状态链应全过（失败项 0），实得 %d（详见上面 awake 自检 FAIL 日志）", f)
	}
	// 自检收尾必须 CancelAwake，留全局未激活，不污染后续用例。
	if active, _, _ := platform.AwakeState(); active {
		t.Error("自检结束应保持未激活")
	}
}

func TestSelftestRequestedEnv(t *testing.T) {
	cases := []struct {
		val  string
		want bool
	}{
		{"1", true},
		{"0", false},
		{"", false},
		{"true", false}, // 只认 "1"，与生产 matches!(.., Some("1")) 一致
	}
	for _, c := range cases {
		t.Setenv(selftestEnv, c.val)
		if got := selftestRequested(); got != c.want {
			t.Errorf("%s=%q 时 selftestRequested=%v，想要 %v", selftestEnv, c.val, got, c.want)
		}
	}
}
