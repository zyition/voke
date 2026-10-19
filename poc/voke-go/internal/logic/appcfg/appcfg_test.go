// appcfg 的测试：解码语义对照 `voke-core/config.rs` 的 ConfigPatch / from_toml（此处 JSON）
// ——指针=没写、activation/theme 宽松、覆盖按 id 稳定序、Encode↔FromJSON 往返、坏 JSON 报错。
package appcfg

import (
	"strings"
	"testing"

	"github.com/zyition/voke/poc/voke-go/internal/logic/config"
	"github.com/zyition/voke/poc/voke-go/internal/logic/engine"
	"github.com/zyition/voke/poc/voke-go/internal/logic/frequency"
)

func ptr[T any](v T) *T { return &v }

func TestFrequencyRoundtrip(t *testing.T) {
	store := frequency.New()
	store["base64:加密"] = 3
	store["apps:Chrome"] = 7
	data, err := FrequencyToJSON(store)
	if err != nil {
		t.Fatal(err)
	}
	back := FrequencyFromJSON(data)
	if back["base64:加密"] != 3 || back["apps:Chrome"] != 7 {
		t.Errorf("频率表往返错：%+v", back)
	}
}

func TestFrequencyFromJSONBadOrEmpty(t *testing.T) {
	for _, bad := range []string{"", "{ not json", "null"} {
		if s := FrequencyFromJSON([]byte(bad)); len(s) != 0 {
			t.Errorf("%q 应回落空表：%+v", bad, s)
		}
	}
}

func sampleDescriptors() []config.Descriptor {
	return []config.Descriptor{
		{ID: "apps", Name: "应用", Description: "d", DefaultEnabled: true, DefaultIsGlobal: true,
			DefaultActivation: engine.Direct, DefaultWeight: 1.2, DefaultCount: 5},
		{ID: "base64", Name: "Base64", Description: "d", Aliases: []string{"b"},
			DefaultEnabled: true, DefaultIsGlobal: false, DefaultPrefix: "#b",
			DefaultActivation: engine.Indirect, DefaultWeight: 1.0, DefaultCount: 5},
	}
}

func TestMergeAppliesOverrides(t *testing.T) {
	_, cfg, table, err := Merge(sampleDescriptors(), []byte("[providers.base64]\nenabled = false\nweight = 2.5\n"))
	if err != nil {
		t.Fatalf("Merge 失败：%v", err)
	}
	b, ok := cfg.Lookup("base64")
	if !ok || b.Enabled {
		t.Errorf("base64 应被覆盖成 disabled：%+v", b)
	}
	if b.Weight != 2.5 {
		t.Errorf("base64 weight 应覆盖为 2.5：%v", b.Weight)
	}
	// disabled 的 base64 不进前缀表 → 只剩 apps（无前缀也不进）→ 空表。
	if len(table) != 0 {
		t.Errorf("base64 关掉后前缀表应空：%+v", table)
	}
}

func TestMergeBadJSONFallsBackToDefaults(t *testing.T) {
	_, cfg, table, err := Merge(sampleDescriptors(), []byte("{ broken"))
	if err == nil {
		t.Error("坏 JSON 应返回 decodeErr（供落日志，绝不静默）")
	}
	// 但归一产物仍须是有效默认（面板照常起）：base64 默认启用 + #b 进表。
	b, ok := cfg.Lookup("base64")
	if !ok || !b.Enabled || b.Prefix != "#b" {
		t.Errorf("坏配置应回落默认：%+v", b)
	}
	if len(table) != 1 || table[0].Prefix != "#b" {
		t.Errorf("默认前缀表应含 #b：%+v", table)
	}
}

func TestFromJSONPartialOnlyCount(t *testing.T) {
	loaded, err := FromJSON([]byte(`{"providers":{"random":{"count":8}}}`))
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if len(loaded.Overrides) != 1 {
		t.Fatalf("应 1 覆盖：%+v", loaded.Overrides)
	}
	ov := loaded.Overrides[0]
	if ov.ID != "random" || ov.Count == nil || *ov.Count != 8 {
		t.Errorf("只写 count：%+v", ov)
	}
	// 没写的字段必须保持 nil（回落 Descriptor 默认，不能被零值劫持）。
	if ov.Enabled != nil || ov.IsGlobal != nil || ov.Weight != nil || ov.Prefix != nil || ov.Activation != nil {
		t.Errorf("未写键应为 nil：%+v", ov)
	}
}

func TestFromJSONBadJSONErrors(t *testing.T) {
	if _, err := FromJSON([]byte("{ not json")); err == nil {
		t.Error("坏 JSON 应返回 error（调用方回落默认）")
	}
}

func TestFromJSONEmpty(t *testing.T) {
	loaded, err := FromJSON([]byte(`{}`))
	if err != nil || loaded.HasHotkey || loaded.HasAutostart || len(loaded.Overrides) != 0 {
		t.Errorf("空配置应全默认：%+v err=%v", loaded, err)
	}
}

func TestActivationLenient(t *testing.T) {
	loaded, _ := FromJSON([]byte(`{"providers":{"base64":{"activation":" Indirect "},"uuid":{"activation":"xxx"},"random":{"activation":"DIRECT"}}}`))
	byID := map[string]config.UserOverride{}
	for _, ov := range loaded.Overrides {
		byID[ov.ID] = ov
	}
	if byID["base64"].Activation == nil || *byID["base64"].Activation != "indirect" {
		t.Errorf("宽松 direct/indirect：base64 应规范成 indirect：%+v", byID["base64"])
	}
	if byID["uuid"].Activation != nil {
		t.Errorf("未知 activation 应丢弃（nil）：%+v", byID["uuid"])
	}
	if byID["random"].Activation == nil || *byID["random"].Activation != "direct" {
		t.Errorf("DIRECT 大写应识别：%+v", byID["random"])
	}
}

func TestThemeLenient(t *testing.T) {
	if l, _ := FromJSON([]byte(`{"theme":"Dark"}`)); l.Theme != ThemeDark {
		t.Errorf("theme Dark 大小写宽松应识别：%q", l.Theme)
	}
	if l, _ := FromJSON([]byte(`{"theme":"neon","hotkey":"alt+space"}`)); l.Theme != "" {
		t.Errorf("未知 theme 应丢弃为空串：%q", l.Theme)
	}
}

func TestOverridesSortedStable(t *testing.T) {
	loaded, _ := FromJSON([]byte(`{"providers":{"uuid":{},"base64":{},"apps":{}}}`))
	var ids []string
	for _, ov := range loaded.Overrides {
		ids = append(ids, ov.ID)
	}
	if strings.Join(ids, ",") != "apps,base64,uuid" {
		t.Errorf("覆盖应按 id 升序（Normalize 确定性）：%v", ids)
	}
}

func TestHotkeyCaptured(t *testing.T) {
	loaded, _ := FromJSON([]byte(`{"hotkey":"ctrl+alt+space","autostart":true}`))
	if !loaded.HasHotkey || loaded.Hotkey != "ctrl+alt+space" {
		t.Errorf("hotkey 未捕获：%+v", loaded)
	}
	if !loaded.HasAutostart || !loaded.Autostart {
		t.Errorf("autostart 未捕获：%+v", loaded)
	}
}

func TestEncodeFromTOMLRoundtrip(t *testing.T) {
	overrides := []config.UserOverride{
		{ID: "random", Count: ptr(12)},
		{ID: "calculator", Enabled: ptr(false), Activation: ptr("direct")},
	}
	data, err := Encode(Top{Hotkey: "alt+space", Theme: ThemeDark}, overrides)
	if err != nil {
		t.Fatalf("Encode 失败：%v", err)
	}
	if !strings.Contains(string(data), `theme = "dark"`) {
		t.Errorf("落盘 theme 应小写：\n%s", data)
	}
	back, err := FromTOML(data)
	if err != nil {
		t.Fatalf("回读失败：%v\n%s", err, data)
	}
	if back.Theme != ThemeDark || back.Hotkey != "alt+space" {
		t.Errorf("顶层往返不稳：%+v", back)
	}
	if len(back.Overrides) != 2 || back.Overrides[0].ID != "calculator" || back.Overrides[1].ID != "random" {
		t.Fatalf("覆盖往返（升序）错：%+v", back.Overrides)
	}
	calc := back.Overrides[0]
	if calc.Enabled == nil || *calc.Enabled || calc.Activation == nil || *calc.Activation != "direct" {
		t.Errorf("calculator 覆盖往返错：%+v", calc)
	}
	if calc.Weight != nil || calc.Prefix != nil { // 没写的键读回应仍是 nil
		t.Errorf("未写键不该被落盘再读出零值：%+v", calc)
	}
	if rnd := back.Overrides[1]; rnd.Count == nil || *rnd.Count != 12 {
		t.Errorf("random count 往返错：%+v", rnd)
	}
}

// TestEncodeFromJSONRoundtripAllProviderFields 覆盖设置窗「插件配置」保存路径真正写入的
// 五个字段：enabled / is_global / weight / prefix / activation —— 断言 Encode 落盘、FromJSON
// 读回逐字段等值，且**空串 prefix 稳定回成 &""**（而非被 omitempty 吞成 nil），这正是设置窗
// 「不改即幂等写回」依赖的前提。
func TestEncodeFromTOMLRoundtripAllProviderFields(t *testing.T) {
	overrides := []config.UserOverride{
		{ID: "base64", Enabled: ptr(true), IsGlobal: ptr(false), Weight: ptr(1.5), Prefix: ptr("#b"), Activation: ptr("indirect")},
		{ID: "awake", Enabled: ptr(true), IsGlobal: ptr(true), Weight: ptr(1.0), Prefix: ptr(""), Activation: ptr("direct")},
	}
	data, err := Encode(Top{Hotkey: "alt+space", Autostart: true, Theme: ThemeSystem}, overrides)
	if err != nil {
		t.Fatalf("Encode 失败：%v", err)
	}
	for _, frag := range []string{`is_global = true`, `weight = 1.5`, `prefix = "#b"`, `activation = "indirect"`} {
		if !strings.Contains(string(data), frag) {
			t.Errorf("落盘缺 %s：\n%s", frag, data)
		}
	}
	back, err := FromTOML(data)
	if err != nil {
		t.Fatalf("回读失败：%v\n%s", err, data)
	}
	// id 升序：awake 在前、base64 在后。
	if len(back.Overrides) != 2 || back.Overrides[0].ID != "awake" || back.Overrides[1].ID != "base64" {
		t.Fatalf("往返覆盖（升序）错：%+v", back.Overrides)
	}
	awake, base := back.Overrides[0], back.Overrides[1]
	if awake.IsGlobal == nil || !*awake.IsGlobal || awake.Weight == nil || *awake.Weight != 1.0 ||
		awake.Enabled == nil || !*awake.Enabled || awake.Activation == nil || *awake.Activation != "direct" {
		t.Errorf("awake 覆盖往返错：%+v", awake)
	}
	// 空串前缀与生产 config.toml 一致**省略该键** → 读回 nil（Normalize 用默认）。
	if awake.Prefix != nil {
		t.Errorf("awake 空前缀应省略（读回 nil），实际 %+v", awake.Prefix)
	}
	if base.Prefix == nil || *base.Prefix != "#b" || base.Weight == nil || *base.Weight != 1.5 ||
		base.IsGlobal == nil || *base.IsGlobal || base.Activation == nil || *base.Activation != "indirect" {
		t.Errorf("base64 覆盖往返错：%+v", base)
	}
}
