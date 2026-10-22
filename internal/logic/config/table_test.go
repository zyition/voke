// BuildTable 的单测：前缀表是 §7.3.3 的唯一真相，这里锁死「条目集合 / 顺序 /
// activation」三件事，并用 engine.Scan 验一次「表 → 扫描」的衔接（picker 是普通一条，
// 与其它条目同路径；换前缀字符不需要动 config 以外的代码）。

package config

import (
	"reflect"
	"testing"

	"voke/internal/logic/engine"
)

func TestBuildTableShape(t *testing.T) {
	c := mustNormalize(t, fakeRegistry(), nil)
	table := c.BuildTable()

	// apps / windows 无前缀 → 不进表；其余五条按注册顺序进表（顺序即优先级）。
	want := engine.Table{
		{Prefix: "=", PluginID: "calculator", Activation: engine.Direct},
		{Prefix: "$", PluginID: "timestamp", Activation: engine.Indirect},
		{Prefix: "@b", PluginID: "base64", Activation: engine.Direct},
		{Prefix: "@r", PluginID: "random", Activation: engine.Indirect},
		{Prefix: "#", PluginID: engine.PickerPluginID, Activation: engine.Direct},
	}
	if !reflect.DeepEqual(table, want) {
		t.Fatalf("前缀表 = %+v，期望 %+v", table, want)
	}

	// 「插件列表」是表里的普通一条（决策②）：它的 PluginID 就是注册表给
	// picker 那条的 id，前缀来自 Descriptor（本包没有 "#" 字面量）。
	if table[len(table)-1].PluginID != engine.PickerPluginID {
		t.Errorf("picker 条目位置 = %+v，期望 PluginID = %q", table[len(table)-1], engine.PickerPluginID)
	}
}

func TestBuildTableSkipsDisabledAndPrefixless(t *testing.T) {
	c := mustNormalize(t, fakeRegistry(), []UserOverride{
		{ID: "base64", Enabled: bp(false)},                // 禁用 → 不进表
		{ID: "timestamp", Prefix: sp("")},                 // 显式无前缀 → 不进表
		{ID: "apps", Prefix: sp("("), IsGlobal: bp(true)}, // 原本无前缀，用户给了一个 → 进表
	})
	table := c.BuildTable()
	want := engine.Table{
		{Prefix: "(", PluginID: "apps", Activation: engine.Direct},
		{Prefix: "=", PluginID: "calculator", Activation: engine.Direct},
		{Prefix: "@r", PluginID: "random", Activation: engine.Indirect},
		{Prefix: "#", PluginID: engine.PickerPluginID, Activation: engine.Direct},
	}
	if !reflect.DeepEqual(table, want) {
		t.Fatalf("前缀表 = %+v，期望 %+v", table, want)
	}
	wantWarnings(t, c)
}

func TestBuildTableOrderIsRegistrationOrder(t *testing.T) {
	// 优先级靠注册顺序表达（engine.Scan 首个全等命中即返回），所以 config 不能排序。
	// 这里把 apps 排到 calculator 之后（用一个新注册表）确认表序跟着 Descriptor 序走。
	reg := []Descriptor{
		{ID: "second", DefaultEnabled: true, DefaultPrefix: "%", DefaultActivation: engine.Direct, DefaultWeight: 1.0, DefaultCount: 5},
		{ID: "first", DefaultEnabled: true, DefaultPrefix: "&", DefaultActivation: engine.Indirect, DefaultWeight: 1.0, DefaultCount: 5},
	}
	table := mustNormalize(t, reg, nil).BuildTable()
	want := engine.Table{
		{Prefix: "%", PluginID: "second", Activation: engine.Direct},
		{Prefix: "&", PluginID: "first", Activation: engine.Indirect},
	}
	if !reflect.DeepEqual(table, want) {
		t.Fatalf("前缀表 = %+v，期望 %+v", table, want)
	}
}

// 表 → engine.Scan 的衔接：证明「判定只在 engine 一处」（铁律②）成立，
// config 产出的表不需要任何额外特判就能驱动状态机。
func TestBuiltTableDrivesEngineScan(t *testing.T) {
	table := mustNormalize(t, fakeRegistry(), nil).BuildTable()

	tests := []struct {
		name  string
		input string
		want  engine.State
	}{
		{"Direct 全等命中 calculator", "=", engine.NewState(engine.Plugin, "calculator", "", "")},
		{"Direct 全等命中 base64（决策 a 的「全等才激活」）", "@b", engine.NewState(engine.Plugin, "base64", "", "")},
		{"列表条目命中 → PluginPicker，与别的条目同一条路径", "#", engine.NewState(engine.PluginPicker, "", "", "")},
		{"Indirect 要前缀 + 空格", "$ ", engine.NewState(engine.Plugin, "timestamp", "", "")},
		{"Indirect 裸前缀不切", "$", engine.NewState(engine.Global, "", "", "$")},
		{"多打一个字符不切（决策 a：不做前缀回落）", "@ba", engine.NewState(engine.Global, "", "", "@ba")},
		{"算式不切", "=1+1", engine.NewState(engine.Global, "", "", "=1+1")},
		{"无前缀插件不参与扫描", "win", engine.NewState(engine.Global, "", "", "win")},
	}
	for _, tt := range tests {
		if got := engine.Scan(table, engine.State{}, tt.input); !got.Equal(tt.want) {
			t.Errorf("%s：Scan(%q) = %+v，期望 %+v", tt.name, tt.input, got, tt.want)
		}
	}

	// 前缀来自同一张表（按 PluginID 查条目；栈改造后不再有 PrefixToken 这一生产概念）。
	prefixOf := func(id string) string {
		for _, e := range table {
			if e.PluginID == id {
				return e.Prefix
			}
		}
		return ""
	}
	if got := prefixOf("base64"); got != "@b" {
		t.Errorf("base64 前缀 = %q，期望 \"@b\"", got)
	}
	if got := prefixOf(engine.PickerPluginID); got != "#" {
		t.Errorf("picker 前缀 = %q，期望 \"#\"", got)
	}
}

func TestBuildTableReflectsConfiguredPrefix(t *testing.T) {
	// D5 的修点：换前缀字符只改配置。把 picker 入口从 "#" 换成 "??" 后，
	// "#" 就空出来给 base64 用，扫描语义随之改变，config / engine 都不需要改代码。
	c := mustNormalize(t, fakeRegistry(), []UserOverride{
		{ID: engine.PickerPluginID, Prefix: sp("??")},
		{ID: "base64", Prefix: sp("#")},
	})
	table := c.BuildTable()
	want := engine.Table{
		{Prefix: "=", PluginID: "calculator", Activation: engine.Direct},
		{Prefix: "$", PluginID: "timestamp", Activation: engine.Indirect},
		{Prefix: "#", PluginID: "base64", Activation: engine.Direct},
		{Prefix: "@r", PluginID: "random", Activation: engine.Indirect},
		{Prefix: "??", PluginID: engine.PickerPluginID, Activation: engine.Direct},
	}
	if !reflect.DeepEqual(table, want) {
		t.Fatalf("前缀表 = %+v，期望 %+v", table, want)
	}
	if got := engine.Scan(table, engine.State{}, "??"); !got.Equal(engine.NewState(engine.PluginPicker, "", "", "")) {
		t.Errorf("Scan(\"??\") = %+v，期望进 PluginPicker", got)
	}
	if got := engine.Scan(table, engine.State{}, "#"); !got.Equal(engine.NewState(engine.Plugin, "base64", "", "")) {
		t.Errorf("Scan(\"#\") = %+v，期望进 base64", got)
	}
	if got := engine.Scan(table, engine.State{}, "@ba"); !got.Equal(engine.NewState(engine.Global, "", "", "@ba")) {
		t.Errorf("Scan(\"@ba\") = %+v，期望留在 Global", got)
	}
}
