// session.go：主入口会话快照与面板入口来源。
//
// 主热键与插件热键共用同一个面板和 engineState，但语义不同：
//   - 主入口（主热键 / 托盘双击 / 显示面板 / 二次启动）是「回到上次所在页」；
//   - 插件热键是「立即进入该插件」，是一次性命令，不该污染主入口的会话。
//
// 方案 C：只给主入口记快照。主入口隐藏时捕获，再唤起时恢复；插件热键每次都从
// 干净的 [Global, Plugin] 进入，隐藏后也不保存它的过滤词 / 选中项。于是
// 「主键进 uuid → 隐藏 → 插件键进剪贴板 → 隐藏 → 主键」会回到 uuid。
//
// 快照只存无法重算的部分：engine.State（不可变作用域栈）与「会重掷」provider 的
// 上一批结果。apps / 窗口 / 剪贴板 / picker 等实时或可重算数据不在快照里，恢复后
// 由 refreshOnShow 重跑，避免把世界冻结在隐藏前。
package app

import (
	"voke/internal/logic/engine"
	"voke/internal/providers"
)

// sessionOwnerKind 标识当前活会话归属。零值即主会话，故 appState 零值可直接用。
type sessionOwnerKind uint8

const (
	ownerMain sessionOwnerKind = iota
	ownerPlugin
)

type sessionOwner struct {
	kind     sessionOwnerKind
	pluginID string // 仅 ownerPlugin 有效
}

func (o sessionOwner) isMain() bool { return o.kind == ownerMain }

// mainSession 是主入口的会话快照。
type mainSession struct {
	set   bool
	state engine.State
	// 会重掷的 provider（uuid / random / timestamp）必须连上一批结果一起恢复，
	// 否则重新查询会换值——对照 refreshOnShow 的 RegeneratesOnQuery 护栏。
	regenValid   bool
	regenRows    []providers.RankedMatch
	regenHint    string
	regenLastKey string
	regenChosen  int
}

// panelEntryKind 是 showPanel 的入口来源。只有隐藏→显示的跃迁会用到它。
type panelEntryKind uint8

const (
	// entryMain：主入口，恢复主快照。
	entryMain panelEntryKind = iota
	// entryKeep：内部回显（粘贴失败 / 提权取消），保持当前会话与属主。
	entryKeep
	// entryPlugin：插件热键 / 托盘 awake 直达，保存主会话后全新进入插件。
	entryPlugin
)

type panelEntry struct {
	kind     panelEntryKind
	pluginID string // 仅 entryPlugin 有效
}

func (st *appState) ownerIsMain() bool { return st.owner.isMain() }

// saveMainSession 捕获主入口会话。插件会话不写主快照，故属主必须是 main。
func (st *appState) saveMainSession() {
	if !st.ownerIsMain() {
		return
	}
	snap := mainSession{set: true, state: st.engineState}
	if st.providerActive() {
		if p, ok := st.pluginReg.Get(st.engineState.PluginID()); ok &&
			p.RegeneratesOnQuery() && len(st.provRows) > 0 {
			snap.regenValid = true
			snap.regenRows = append([]providers.RankedMatch(nil), st.provRows...)
			snap.regenHint = st.provHint
			snap.regenLastKey = st.provLastKey
			snap.regenChosen = st.provChosen
		}
	}
	st.mainSession = snap
}

// mainStateUsable 校验快照里的插件页是否仍然可用：插件被停用或移除后，不能借
// 主快照把它复活。Global / PluginPicker 恒可用。
func (st *appState) mainStateUsable(s engine.State) bool {
	switch s.Mode() {
	case engine.Plugin, engine.SubMode:
		cfg, ok := st.providers.Lookup(s.PluginID())
		if !ok || !cfg.Enabled || st.pluginReg == nil {
			return false
		}
		_, registered := st.pluginReg.Get(s.PluginID())
		return registered
	default:
		return true
	}
}

// restoreMainSession 把主快照装回活会话（主入口隐藏→显示时调用）。没有快照或
// 快照已失效 → 干净 Global；不能保留当前插件会话，否则插件热键状态又会漏进主入口。
func (st *appState) restoreMainSession() {
	st.owner = sessionOwner{}
	st.resetSession()
	if !st.mainSession.set || !st.mainStateUsable(st.mainSession.state) {
		st.model.Query = ""
		return
	}
	st.engineState = st.mainSession.state
	st.model.Query = st.engineState.Query()
	if st.mainSession.regenValid && st.providerActive() {
		st.provRows = append([]providers.RankedMatch(nil), st.mainSession.regenRows...)
		st.provHint = st.mainSession.regenHint
		st.provLastKey = st.mainSession.regenLastKey
		st.provChosen = st.mainSession.regenChosen
	}
}

// beginPluginSession 把属主切到插件直达：若当前是主会话，先把主会话存好。
// 只改属主，不动作用域栈；隐藏路径由 preparePluginSession 继续清栈。
func (st *appState) beginPluginSession(id string) {
	if st.ownerIsMain() {
		st.saveMainSession()
	}
	st.owner = sessionOwner{kind: ownerPlugin, pluginID: id}
}

// preparePluginSession 在隐藏→显示的插件热键路径上准备一个全新的插件会话。
// 这里只落状态，不跑 provider 查询——随后 showPanel 的 refreshOnShow 会按新状态
// 统一派发一次，避免先刷新旧会话再切走的浪费与闪帧。
func (st *appState) preparePluginSession(id string) {
	st.beginPluginSession(id)
	st.resetSession()
	st.engineState = engine.SelectPlugin(st.engineState, id)
	st.model.Query = st.engineState.Query()
}
