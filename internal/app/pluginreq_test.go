package app

import (
	"testing"

	"voke/internal/logic/config"
	"voke/internal/logic/engine"
)

// TestApplyPluginAvailability 锁环境依赖的停用语义：
//   - 依赖缺失 → 启用态被改成停用，且不进前缀表；
//   - 依赖满足 → 保持启用；
//   - 用户本来就没启用 → 不因探测再写一遍，也不进表；
//   - 没有声明依赖的插件不受影响。
func TestApplyPluginAvailability(t *testing.T) {
	descs := []config.Descriptor{
		{ID: "present", DefaultEnabled: true, DefaultPrefix: "@p", DefaultActivation: engine.Direct, DefaultWeight: 1},
		{ID: "missing", DefaultEnabled: true, DefaultPrefix: "@m", DefaultActivation: engine.Direct, DefaultWeight: 1},
		{ID: "offline", DefaultEnabled: true, DefaultPrefix: "@o", DefaultActivation: engine.Direct, DefaultWeight: 1},
		{ID: "plain", DefaultEnabled: true, DefaultPrefix: "@x", DefaultActivation: engine.Direct, DefaultWeight: 1},
	}
	off := false
	cfg, err := config.Normalize(descs, []config.UserOverride{{ID: "offline", Enabled: &off}})
	if err != nil {
		t.Fatalf("Normalize 失败：%v", err)
	}
	reqs := map[string]pluginRequirement{
		"present": {software: "Present", url: "https://present", probe: func() bool { return true }},
		"missing": {software: "Missing", url: "https://missing", probe: func() bool { return false }},
		"offline": {software: "Offline", url: "https://offline", probe: func() bool { return false }},
	}

	got, table := applyPluginAvailability(cfg, reqs)
	wantEnabled := map[string]bool{"present": true, "missing": false, "offline": false, "plain": true}
	for id, want := range wantEnabled {
		p, ok := got.Lookup(id)
		if !ok {
			t.Fatalf("%s 不在配置里", id)
		}
		if p.Enabled != want {
			t.Errorf("%s Enabled = %v，want %v", id, p.Enabled, want)
		}
	}

	inTable := make(map[string]bool, len(table))
	for _, e := range table {
		inTable[e.PluginID] = true
	}
	wantTable := map[string]bool{"present": true, "plain": true}
	for id, want := range wantTable {
		if inTable[id] != want {
			t.Errorf("%s 在前缀表 = %v，want %v（表：%+v）", id, inTable[id], want, table)
		}
	}
	if inTable["missing"] || inTable["offline"] {
		t.Errorf("依赖缺失 / 未启用的插件不该进前缀表：%+v", table)
	}
}
