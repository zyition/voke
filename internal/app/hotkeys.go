// hotkeys.go：插件全局快捷键的注册、同步与直达。
//
// 与主唤隐热键分工：
//   - 主热键只负责面板显隐（togglePanel），拥有可恢复的“主会话”；
//   - 插件热键只负责“拉起并激活某个插件”，对齐 Wox / Flow Launcher 等启动器的
//     per-plugin hotkey：隐藏时打开并进入插件，已在该插件时只聚焦，其它状态则
//     切换过去；**不做再按一次隐藏**（隐藏仍归主热键 / Esc）。
//   - 插件热键是“立即执行”：每次都从干净的插件第一级进入，不读取也不覆盖主会话
//     快照（方案 C，见 session.go）。
//
// 配置来自 ProviderConfig.Hotkey；没有默认值，未启用插件不注册。注册 / 注销都
// 只在主线程（见 platform/hotkey.go 的线程约束）。
package app

import (
	"fmt"
	"log"

	"voke/internal/logic/config"
	"voke/internal/logic/engine"
	"voke/internal/platform"
)

// desiredPluginHotkeys 计算当前应注册的插件热键（纯函数，Linux 可测）：
// 只收“已启用 + 非空”的项；规范化后重复的按键，注册顺序靠前者生效，后者记错误。
// picker 是哨兵条目，没有可激活的插件页，不参与热键注册。
func desiredPluginHotkeys(cfg config.Configs) (map[string]string, map[string]string) {
	desired := make(map[string]string)
	errs := make(map[string]string)
	owners := make(map[string]string)
	for _, p := range cfg.Providers {
		if !p.Enabled || p.Hotkey == "" {
			continue
		}
		if p.ID == engine.PickerPluginID {
			errs[p.ID] = "该插件不支持全局快捷键"
			continue
		}
		key := config.CanonicalHotkey(p.Hotkey)
		if owner, ok := owners[key]; ok {
			errs[p.ID] = fmt.Sprintf("快捷键 %s 与插件「%s」重复，未注册", p.Hotkey, owner)
			continue
		}
		owners[key] = p.Name
		desired[p.ID] = p.Hotkey
	}
	return desired, errs
}

// syncPluginHotkeys 把实际注册表对齐到当前配置：撤掉不再需要的、注册新增的、
// 变更键的先撤后注。失败只影响该插件（记录到 pluginHotkeyErrs 供设置页展示），
// 不中断其它热键。调用点：ready（启动）、applySettings（配置保存后）。
func (st *appState) syncPluginHotkeys() {
	if st.hotkeysSuspended {
		return // 录制中所有全局热键已临时注销，结束后由 resumeHotkeys 统一重建
	}
	desired, errs := desiredPluginHotkeys(st.providers)
	if st.showHotkey != "" {
		mainKey := config.CanonicalHotkey(st.showHotkey)
		for id, acc := range desired {
			if config.CanonicalHotkey(acc) != mainKey {
				continue
			}
			errs[id] = fmt.Sprintf("快捷键 %s 已被显示/隐藏热键占用，未注册", acc)
			delete(desired, id)
		}
	}
	if st.pluginHotkeys == nil {
		st.pluginHotkeys = make(map[string]string)
	}

	// 先撤：插件被停用、键被改掉、或整项被移除。
	for id, acc := range st.pluginHotkeys {
		want, stillWanted := desired[id]
		if stillWanted && config.CanonicalHotkey(want) == config.CanonicalHotkey(acc) {
			continue
		}
		platform.UnregisterHotkey(acc)
		delete(st.pluginHotkeys, id)
	}

	// 再注：按注册顺序遍历，保证同一份配置的行为确定。
	for _, p := range st.providers.Providers {
		want, ok := desired[p.ID]
		if !ok {
			continue
		}
		if _, registered := st.pluginHotkeys[p.ID]; registered {
			continue
		}
		id := p.ID
		if err := platform.RegisterHotkey(want, func() { st.onPluginHotkey(id) }); err != nil {
			errs[id] = fmt.Sprintf("快捷键 %s 注册失败：%v", want, err)
			continue
		}
		st.pluginHotkeys[id] = want
	}
	for id, msg := range errs {
		log.Printf("voke 插件 %s 热键未生效：%s", id, msg)
	}
	st.pluginHotkeyErrs = errs
}

// suspendHotkeys 临时注销主热键与全部插件热键（设置页录制时用）。若不注销，
// 用户按下旧组合会被系统当成既有热键触发，录制框根本收不到。
func (st *appState) suspendHotkeys() {
	if st.hotkeysSuspended {
		return
	}
	st.hotkeysSuspended = true
	if st.mainHotkeyOn {
		platform.UnregisterHotkey(st.showHotkey)
		st.mainHotkeyOn = false
	}
	for id, acc := range st.pluginHotkeys {
		platform.UnregisterHotkey(acc)
		delete(st.pluginHotkeys, id)
	}
}

// resumeHotkeys 结束录制后重建全部全局热键：主热键（若此前注册过）与当前配置里
// 应注册的插件热键。applySettings 在录制期间会跳过插件同步，故这里必须再跑一次。
func (st *appState) resumeHotkeys() {
	if !st.hotkeysSuspended {
		return
	}
	st.hotkeysSuspended = false
	if !st.mainHotkeyOn && st.showHotkey != "" {
		if err := platform.RegisterHotkey(st.showHotkey, st.togglePanel); err != nil {
			log.Printf("voke 恢复全局唤起热键 %q 失败：%v", st.showHotkey, err)
		} else {
			st.mainHotkeyOn = true
		}
	}
	st.syncPluginHotkeys()
}

// rebindMainHotkey 应用新的主唤隐热键；失败时回滚旧键，绝不留新旧都没绑的空窗。
// 录制主热键时所有热键已挂起，因此这里注册成功也不会与插件键冲突（冲突在保存前校验）。
func (st *appState) rebindMainHotkey(newKey string) {
	if newKey == "" {
		return
	}
	if newKey == st.showHotkey {
		// 启动时注册失败过：后续任意一次保存都顺带重试（录制中由 resume 负责）。
		if !st.mainHotkeyOn && !st.hotkeysSuspended {
			if err := platform.RegisterHotkey(newKey, st.togglePanel); err != nil {
				log.Printf("voke 重试注册全局唤起热键 %q 失败：%v", newKey, err)
			} else {
				st.mainHotkeyOn = true
			}
		}
		return
	}
	old := st.showHotkey
	if st.mainHotkeyOn {
		platform.UnregisterHotkey(old)
		st.mainHotkeyOn = false
	}
	if err := platform.RegisterHotkey(newKey, st.togglePanel); err != nil {
		log.Printf("voke 改键注册 %q 失败，回滚到 %q：%v", newKey, old, err)
		platform.UnregisterHotkey(newKey)
		if old != "" {
			if rerr := platform.RegisterHotkey(old, st.togglePanel); rerr != nil {
				log.Printf("voke 回滚注册旧键 %q 也失败（下次启动按配置重建）：%v", old, rerr)
			} else {
				st.mainHotkeyOn = true
			}
		}
		return
	}
	st.showHotkey = newKey
	st.mainHotkeyOn = true
}

// onPluginHotkey 是全局热键回调（主线程）：按键时再确认一次插件仍启用且仍绑定。
func (st *appState) onPluginHotkey(id string) {
	p, ok := st.providers.Lookup(id)
	if !ok || !p.Enabled || p.Hotkey == "" {
		return
	}
	st.activatePlugin(id)
}

// activatePlugin 拉起并激活插件。主流启动器的插件热键语义是“调用/切换”而不是
// 显隐开关：隐藏时打开并进入插件；已经在该插件（含子模式）时只把焦点还给面板，
// 保留用户正在输入的过滤词；在其它插件或全局时切换过去（换新会话）。
func (st *appState) activatePlugin(id string) {
	if st.panel.IsVisible() && st.engineState.PluginID() == id {
		st.input.Focus()
		return
	}
	st.directActivatePlugin(id)
}

// directActivatePlugin 是插件热键 / 托盘 awake 的直达实现：先把主会话存好（若
// 当前属主是主会话），再进入该插件。隐藏时由 showPanelEntry 在 Show 之前准备
// 全新会话，避免先刷新旧会话再切走；已可见时就地换会话。
func (st *appState) directActivatePlugin(id string) {
	if !st.panel.IsVisible() {
		st.showPanelEntry(panelEntry{kind: entryPlugin, pluginID: id})
		return
	}
	st.beginPluginSession(id)
	st.enterPlugin(id)
	st.input.Focus()
}

// pluginHotkeyError 返回最近一次同步中该插件的热键错误（设置页就地展示）。
func (st *appState) pluginHotkeyError(id string) string {
	return st.pluginHotkeyErrs[id]
}
