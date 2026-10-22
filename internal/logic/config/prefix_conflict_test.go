// prefix.go 的单测：冲突判定的算法边界，以及旧配置修复用的 PrefixConflictIDs。
//
// 这些用例锁住三件容易写错的事：
//   - 判的是**展开后的触发串**（Indirect 带一个空格），不是前缀字面量；
//   - 任何一个方向的前缀关系都算冲突（短串先命中会遮蔽长串）；
//   - 两个 Indirect 前缀只在触发串构成前缀关系时冲突（@b 与 @bc 合法共存）。
package config

import (
	"reflect"
	"testing"

	"voke/internal/logic/engine"
)

func TestPrefixesConflictTriggerExpansion(t *testing.T) {
	claim := func(prefix string, activation engine.Activation) prefixClaim {
		return prefixClaim{prefix: prefix, activation: activation}
	}
	tests := []struct {
		name string
		a, b prefixClaim
		want bool
	}{
		{"相同 Direct", claim("=", engine.Direct), claim("=", engine.Direct), true},
		{"相同字面 Direct 与 Indirect", claim("@b", engine.Direct), claim("@b", engine.Indirect), true},
		{"短 Direct 遮蔽长 Direct", claim("#", engine.Direct), claim("#b", engine.Direct), true},
		{"短 Direct 遮蔽长 Indirect", claim("#", engine.Direct), claim("#b", engine.Indirect), true},
		{"同字面 Direct 遮蔽 Indirect（ab 是 ab␠ 的前缀）", claim("ab", engine.Direct), claim("ab", engine.Indirect), true},
		{"两个 Indirect 只差后缀不冲突", claim("@b", engine.Indirect), claim("@bc", engine.Indirect), false},
		{"短 Indirect 与长 Direct 不冲突（空格与字母错开）", claim("ab", engine.Indirect), claim("abc", engine.Direct), false},
		{"更长 Direct 不遮蔽更短 Indirect（abc 与 ab␠）", claim("abc", engine.Direct), claim("ab", engine.Indirect), false},
		{"无前缀关系", claim("=", engine.Direct), claim("@b", engine.Direct), false},
		{"空前缀不冲突", claim("", engine.Direct), claim("#", engine.Direct), false},
		{"两个空前缀不冲突", claim("", engine.Indirect), claim("", engine.Direct), false},
	}
	for _, tt := range tests {
		if got := prefixesConflict(tt.a, tt.b); got != tt.want {
			t.Errorf("%s：prefixesConflict(a,b) = %v，期望 %v", tt.name, got, tt.want)
		}
		// 冲突是对称的：任一方向的前缀关系都算。
		if got := prefixesConflict(tt.b, tt.a); got != tt.want {
			t.Errorf("%s：prefixesConflict(b,a) = %v，期望 %v", tt.name, got, tt.want)
		}
	}
}

func TestPrefixConflictIDs(t *testing.T) {
	// 默认注册表本身必须无冲突（这次把二级前缀从 #x 迁到 @x 就是为此）。
	if got := PrefixConflictIDs(fakeRegistry(), nil); len(got) != 0 {
		t.Fatalf("默认注册表不该有前缀冲突，实得 %v", got)
	}

	// 用户实际遇到的形状：picker "#" 与 base64 "#b"（Indirect）。
	// 返回注册顺序（base64 在 picker 之前），去重。
	got := PrefixConflictIDs(fakeRegistry(), []UserOverride{
		{ID: engine.PickerPluginID, Prefix: sp("#")},
		{ID: "base64", Prefix: sp("#b"), Activation: sp("indirect")},
	})
	want := []string{"base64", engine.PickerPluginID}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("冲突 id = %v，期望 %v", got, want)
	}

	// 三方：picker 同时遮蔽 #b 与 #bc；#b 与 #bc 两个 Indirect 互不冲突，
	// 但都会因 picker 被列入。返回顺序仍是注册顺序。
	got = PrefixConflictIDs(fakeRegistry(), []UserOverride{
		{ID: engine.PickerPluginID, Prefix: sp("#")},
		{ID: "base64", Prefix: sp("#b"), Activation: sp("indirect")},
		{ID: "random", Prefix: sp("#bc"), Activation: sp("indirect")},
	})
	want = []string{"base64", "random", engine.PickerPluginID}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("三方冲突 id = %v，期望 %v", got, want)
	}

	// 禁用的 picker 仍占着前缀（C2 的既定语义）。
	got = PrefixConflictIDs(fakeRegistry(), []UserOverride{
		{ID: engine.PickerPluginID, Prefix: sp("#"), Enabled: bp(false)},
		{ID: "base64", Prefix: sp("#b"), Activation: sp("indirect")},
	})
	wantDisabled := []string{"base64", engine.PickerPluginID}
	if !reflect.DeepEqual(got, wantDisabled) {
		t.Errorf("禁用插件仍应参与冲突分析，实得 %v，期望 %v", got, wantDisabled)
	}

	// 含空白的前缀按 C3 视为空，不参与冲突。
	if got := PrefixConflictIDs(fakeRegistry(), []UserOverride{
		{ID: "base64", Prefix: sp(" ")},
	}); len(got) != 0 {
		t.Errorf("含空白前缀应被忽略，实得冲突 %v", got)
	}

	// activation 覆盖会改变展开串：picker 改成 Indirect 后 "# " 不再遮蔽 "@b"/"@r"。
	if got := PrefixConflictIDs(fakeRegistry(), []UserOverride{
		{ID: engine.PickerPluginID, Prefix: sp("#"), Activation: sp("indirect")},
	}); len(got) != 0 {
		t.Errorf("picker 改为 Indirect 后不该再有冲突，实得 %v", got)
	}
}
