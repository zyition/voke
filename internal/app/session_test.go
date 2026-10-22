// session_test.go：方案 C 的会话隔离——主入口快照恢复、插件热键临时会话、
// resetAfterAction 的分支归属、停用插件回退。
//
// 这些用例直调 session.go 的生产方法（save/restore/prepare/begin）与
// resetAfterAction，不碰窗口 / 输入框 / 主循环，故 Linux 原生可跑。
package app

import (
	"strings"
	"testing"

	"voke/internal/logic/engine"
	"voke/internal/providers"
)

func newSessionState(t *testing.T) *appState {
	t.Helper()
	st := newWiredState(t)
	st.pluginReg = builtinProviders()
	return st
}

func providerTitles(rows []providers.RankedMatch) string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Title)
	}
	return strings.Join(out, "|")
}

// TestMainSessionSurvivesPluginHotkey 是需求里的原始序列：
// 主入口进 uuid → 隐藏 → 插件热键进剪贴板 → 隐藏 → 主入口必须回到 uuid，
// 且 uuid 的上一批值不得因恢复而重掷。
func TestMainSessionSurvivesPluginHotkey(t *testing.T) {
	st := newSessionState(t)

	// 主入口：进 uuid 并输入个数 3，拿到一批真实 UUID。
	st.enterPlugin("uuid")
	st.model.Query = "3"
	st.onQueryChanged()
	if st.engineState.PluginID() != "uuid" || st.engineState.Query() != "3" {
		t.Fatalf("前置条件不成立：engineState 应为 uuid/3，实得 %+v", st.engineState)
	}
	if len(st.provRows) != 3 {
		t.Fatalf("uuid 输入 3 应出 3 条，实得 %d", len(st.provRows))
	}
	mainValues := providerTitles(st.provRows)

	// 主入口隐藏：保存主快照。
	st.saveMainSession()
	if !st.mainSession.set || st.mainSession.state.PluginID() != "uuid" {
		t.Fatalf("隐藏时应保存 uuid 主快照，实得 %+v", st.mainSession)
	}

	// 插件热键：隐藏态直达剪贴板。必须先保存主会话，再全新进入插件。
	st.preparePluginSession("clipboard")
	if st.engineState.PluginID() != "clipboard" || st.engineState.Query() != "" || st.model.Query != "" {
		t.Fatalf("插件热键应全新进入 clipboard，实得 state=%+v box=%q", st.engineState, st.model.Query)
	}
	if st.mainSession.state.PluginID() != "uuid" {
		t.Fatalf("插件热键不得覆盖主快照，实得 %s", st.mainSession.state.PluginID())
	}

	// 插件会话里输入过滤词并隐藏：插件会话是临时的，不写主快照。
	st.model.Query = "foo"
	st.onQueryChanged()
	st.saveMainSession() // 属主是插件 → no-op
	if st.mainSession.state.PluginID() != "uuid" || st.mainSession.state.Query() != "3" {
		t.Fatalf("插件会话隐藏不得污染主快照，实得 %+v", st.mainSession.state)
	}

	// 主入口重新显示：回到 uuid/3，并保留隐藏前那批值。
	st.restoreMainSession()
	if st.engineState.PluginID() != "uuid" || st.engineState.Query() != "3" || st.model.Query != "3" {
		t.Fatalf("主入口应恢复 uuid/3，实得 state=%+v box=%q", st.engineState, st.model.Query)
	}
	if got := providerTitles(st.provRows); got != mainValues {
		t.Fatalf("恢复不得重掷 uuid：\n旧 %q\n新 %q", mainValues, got)
	}

	// showPanel 的 refreshOnShow 也不能换掉重掷 provider 的值。
	st.refreshOnShow()
	if got := providerTitles(st.provRows); got != mainValues {
		t.Fatalf("refreshOnShow 不得重掷 uuid：\n旧 %q\n新 %q", mainValues, got)
	}
	if st.owner.kind != ownerMain {
		t.Fatalf("恢复后属主应为主会话，实得 %+v", st.owner)
	}
}

// TestPluginHotkeySessionIsFreshEachTime 锁插件热键的“立即执行”语义：
// 同一插件热键再次按下时，上次的过滤词不会回来。
func TestPluginHotkeySessionIsFreshEachTime(t *testing.T) {
	st := newSessionState(t)
	st.enterPlugin("uuid")
	st.saveMainSession()

	st.preparePluginSession("clipboard")
	st.model.Query = "foo"
	st.onQueryChanged()
	if st.engineState.Query() != "foo" {
		t.Fatalf("前置条件：插件会话应带过滤词 foo，实得 %q", st.engineState.Query())
	}

	st.preparePluginSession("clipboard") // 再次按插件热键
	if st.engineState.PluginID() != "clipboard" || st.engineState.Query() != "" || st.model.Query != "" {
		t.Fatalf("插件热键应每次全新进入，实得 state=%+v box=%q", st.engineState, st.model.Query)
	}
	if st.mainSession.state.PluginID() != "uuid" {
		t.Fatalf("插件热键重复触发不得覆盖主快照，实得 %s", st.mainSession.state.PluginID())
	}
}

// TestPluginResetDoesNotClearMainSession：插件热键会话里的“离开面板”动作
// （启动 / 切窗等）会 resetAfterAction，但主快照必须原样保留。
func TestPluginResetDoesNotClearMainSession(t *testing.T) {
	st := newSessionState(t)
	st.enterPlugin("uuid")
	st.saveMainSession()

	st.preparePluginSession("clipboard")
	st.resetAfterAction() // 属主是插件 → 不覆盖主快照

	if st.mainSession.state.PluginID() != "uuid" {
		t.Fatalf("插件会话复位后主快照应仍是 uuid，实得 %s", st.mainSession.state.PluginID())
	}
	st.restoreMainSession()
	if st.engineState.PluginID() != "uuid" {
		t.Fatalf("主入口应回到 uuid，实得 %s", st.engineState.PluginID())
	}
}

// TestMainResetAfterActionClearsSnapshot：主会话里的离开动作成功后，下次主入口
// 必须从干净 Global 开始，不能把旧插件页带回来。
func TestMainResetAfterActionClearsSnapshot(t *testing.T) {
	st := newSessionState(t)
	st.enterPlugin("uuid")
	st.saveMainSession()

	st.resetAfterAction() // 属主是主会话 → 用复位后的干净状态覆盖快照
	if !st.mainSession.set || st.mainSession.state.Mode() != engine.Global || st.mainSession.state.Query() != "" {
		t.Fatalf("主会话复位后快照应为干净 Global，实得 %+v", st.mainSession.state)
	}

	st.restoreMainSession()
	if st.engineState.Mode() != engine.Global || st.model.Query != "" {
		t.Fatalf("主入口应恢复干净 Global，实得 state=%+v box=%q", st.engineState, st.model.Query)
	}
}

// TestRestoreMainSessionWithoutSnapshotIsCleanGlobal：首次主入口唤起没有快照，
// 不能保留当前活会话（例如启动时或插件会话残留）。
func TestRestoreMainSessionWithoutSnapshotIsCleanGlobal(t *testing.T) {
	st := newSessionState(t)
	st.enterPlugin("uuid")
	if st.engineState.Mode() == engine.Global {
		t.Fatal("前置条件：应先在插件页")
	}

	st.restoreMainSession()
	if st.engineState.Mode() != engine.Global || st.model.Query != "" {
		t.Fatalf("无快照时应回干净 Global，实得 state=%+v box=%q", st.engineState, st.model.Query)
	}
}

// TestRestoreMainSessionFallsBackWhenPluginDisabled：快照里的插件已被停用，
// 主入口不能借快照把它复活。
func TestRestoreMainSessionFallsBackWhenPluginDisabled(t *testing.T) {
	st := newSessionState(t)
	st.enterPlugin("uuid")
	st.saveMainSession()

	for i := range st.providers.Providers {
		if st.providers.Providers[i].ID == "uuid" {
			st.providers.Providers[i].Enabled = false
		}
	}

	st.restoreMainSession()
	if st.engineState.Mode() != engine.Global || st.model.Query != "" {
		t.Fatalf("停用插件的快照应回退干净 Global，实得 state=%+v box=%q", st.engineState, st.model.Query)
	}
	if st.providerActive() {
		t.Error("回退后不应仍落在 provider 插件页")
	}
}
