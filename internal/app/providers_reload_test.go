// reloadConfigs 的 headless 集成测试：设置窗「保存」依赖的正是这条路径——读回刚写的
// config.toml → Normalize → BuildTable → applyProviderConfigs 下发到 live 注册表。整条链
// 全是纯逻辑 + 纯 provider 实例（无 mygo），Linux 原生可测。覆盖到位即补上此前 save→
// reload 端到端无测的缺口。
package app

import (
	"os"
	"path/filepath"
	"testing"

	"voke/internal/logic/appcfg"
	"voke/internal/logic/charset"
	"voke/internal/logic/config"
	"voke/internal/logic/engine"
	"voke/internal/providers"
)

// TestSaveConfigLeavesNoTempFiles：原子写成功后目录里只剩 config.toml——临时文件
// 已 rename 走（或被清理），不会在数据目录里越攒越多；重复保存同样干净。
func TestSaveConfigLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	t.Setenv(configPathEnv, p)
	for i := 0; i < 2; i++ {
		if err := saveConfig(appcfg.Top{Hotkey: "ctrl+alt+space"}, nil); err != nil {
			t.Fatalf("第 %d 次 saveConfig 失败：%v", i+1, err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读目录失败：%v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "config.toml" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("目录里应只有 config.toml，实得 %v", names)
	}
	if b, err := os.ReadFile(p); err != nil || len(b) == 0 {
		t.Fatalf("config.toml 内容异常：len=%d err=%v", len(b), err)
	}
}

func TestReloadConfigsAppliesSavedOverrides(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	t.Setenv(configPathEnv, p)

	st := &appState{pluginReg: builtinProviders()}
	off := false
	if err := saveConfig(appcfg.Top{Hotkey: "ctrl+alt+space"},
		[]config.UserOverride{{ID: "base64", Enabled: &off}}); err != nil {
		t.Fatalf("saveConfig 失败：%v", err)
	}

	st.reloadConfigs()

	// 归一后的 providers 反映落盘覆盖：base64 禁用。
	if pc, ok := st.providers.Lookup("base64"); !ok || pc.Enabled {
		t.Errorf("reload 后 st.providers base64 应禁用：ok=%v %+v", ok, pc)
	}
	// live 注册表实例被 applyProviderConfigs 下发 enabled=false。
	if prov, ok := st.pluginReg.Get("base64"); !ok || prov.Config().Enabled {
		t.Errorf("live base64 实例应 enabled=false：ok=%v", ok)
	}
	// 禁用者不进 BuildTable：@b 前缀不再命中 base64。
	if got := engine.Scan(st.table, engine.State{}, "@b"); got.Mode() == engine.Plugin && got.PluginID() == "base64" {
		t.Errorf("禁用后 @b 不该仍路由到 base64：%+v", got)
	}
	// 顶层 hotkey 回存到 st.cfg（下次 seedSettings 一致）。
	if st.cfg.Hotkey != "ctrl+alt+space" {
		t.Errorf("reload 应回存顶层 hotkey 到 st.cfg，实得 %q", st.cfg.Hotkey)
	}
}

func TestReloadConfigsAppliesRandomCharsets(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	t.Setenv(configPathEnv, p)

	st := &appState{pluginReg: builtinProviders()}
	specs := []charset.Spec{{Name: "大写十六进制", Pattern: "A-F0-9", Enabled: true}}
	if err := saveConfig(appcfg.Top{Hotkey: "ctrl+alt+space"},
		[]config.UserOverride{{ID: "random", Charsets: specs}}); err != nil {
		t.Fatalf("saveConfig 失败：%v", err)
	}

	st.reloadConfigs()

	prov, ok := st.pluginReg.Get("random")
	if !ok {
		t.Fatal("注册表里没有 random")
	}
	got := prov.Config().Charsets
	if len(got) != 1 || got[0].Name != "大写十六进制" || got[0].Pattern != "A-F0-9" {
		t.Fatalf("live random 实例未收到新字符集：%+v", got)
	}
	hits := prov.Query(providers.QueryCtx{})
	if len(hits) != 2 || hits[1].Title != "大写十六进制" || hits[1].Interact.Sub != "cs:大写十六进制" {
		t.Errorf("一级列表应改用新字符集：%+v", hits)
	}
}

// TestReloadInvalidatesChangedProviderCache：字符集等 provider 配置变化后，重掷类
// provider 的「输入没变就不重查」缓存必须失效，否则唤回面板仍显示旧字符集 / 旧值；
// 无关插件的配置变化则不应打断当前结果。
func TestReloadInvalidatesChangedProviderCache(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	t.Setenv(configPathEnv, p)

	st := &appState{pluginReg: builtinProviders()}
	st.reloadConfigs() // 先落一份纯默认 st.providers

	st.engineState = engine.NewState(engine.Plugin, "random", "", "")
	key := providerQueryKey(st.engineState)
	st.provLastKey = key
	st.provRows = []providers.RankedMatch{{Match: providers.Match{ProviderID: "random", Title: "旧值"}}}
	st.mainSession = mainSession{
		set: true, state: st.engineState, regenValid: true,
		regenRows: st.provRows, regenLastKey: key,
	}

	// 无关插件变化：随机值缓存保持。
	off := false
	if err := saveConfig(appcfg.Top{Hotkey: "ctrl+alt+space"},
		[]config.UserOverride{{ID: "base64", Enabled: &off}}); err != nil {
		t.Fatal(err)
	}
	st.reloadConfigs()
	if st.provLastKey != key || !st.mainSession.regenValid {
		t.Error("无关插件配置变化不应让随机值缓存失效")
	}

	// 随机值字符集变化：当前结果与主会话快照都要失效，下一次 refreshOnShow 重查。
	specs := []charset.Spec{{Name: "新字符集", Pattern: "A-F", Enabled: true}}
	if err := saveConfig(appcfg.Top{Hotkey: "ctrl+alt+space"},
		[]config.UserOverride{{ID: "random", Charsets: specs}}); err != nil {
		t.Fatal(err)
	}
	st.reloadConfigs()
	if st.provLastKey != "" {
		t.Errorf("字符集变化后应清除查询指纹，实得 %q", st.provLastKey)
	}
	if st.mainSession.regenValid {
		t.Error("字符集变化后主会话的旧结果快照应失效")
	}
	st.refreshOnShow()
	if len(st.provRows) == 0 || st.provRows[0].Title == "旧值" {
		t.Errorf("refreshOnShow 应重查并丢掉旧结果：%+v", st.provRows)
	}
	found := false
	for _, row := range st.provRows {
		if row.Title == "新字符集" {
			found = true
		}
	}
	if !found {
		t.Errorf("重查结果应包含新字符集：%+v", st.provRows)
	}
}

// TestInvalidateChangedProviderResultsCoversMainSession：当前面板在 Global 时，
// 主会话里缓存的随机值快照也要按变化的插件 id 失效，不能只看当前 engineState。
func TestInvalidateChangedProviderResultsCoversMainSession(t *testing.T) {
	st := &appState{
		engineState: engine.NewState(engine.Global, "", "", ""),
		mainSession: mainSession{
			set: true, state: engine.NewState(engine.Plugin, "random", "", ""),
			regenValid: true, regenRows: []providers.RankedMatch{{Match: providers.Match{Title: "旧"}}},
		},
		provLastKey: "keep-me",
	}
	st.invalidateChangedProviderResults(map[string]bool{"base64": true})
	if !st.mainSession.regenValid || st.provLastKey != "keep-me" {
		t.Error("无关插件变化不应清缓存")
	}
	st.invalidateChangedProviderResults(map[string]bool{"random": true})
	if st.mainSession.regenValid || len(st.mainSession.regenRows) != 0 {
		t.Error("随机值变化应清掉主会话的旧结果快照")
	}
	if st.provLastKey != "keep-me" {
		t.Error("当前不在随机值插件里，不应清当前查询指纹")
	}
}
