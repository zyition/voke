// reloadConfigs 的 headless 集成测试：设置窗「保存」依赖的正是这条路径——读回刚写的
// config.toml → Normalize → BuildTable → applyProviderConfigs 下发到 live 注册表。整条链
// 全是纯逻辑 + 纯 provider 实例（无 mygo），Linux 原生可测。覆盖到位即补上此前 save→
// reload 端到端无测的缺口。
package app

import (
	"path/filepath"
	"testing"

	"github.com/zyition/voke/poc/voke-go/internal/logic/appcfg"
	"github.com/zyition/voke/poc/voke-go/internal/logic/config"
	"github.com/zyition/voke/poc/voke-go/internal/logic/engine"
)

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
