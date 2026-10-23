// view.go：面板整帧视图的**装配接缝** + 查询/模式路由的意图执行器。
//
// mygo 是 immediate-mode UI：view 每帧整体重建，只读写 appState，
// 不持有跨帧的 Element/Context。
//
// §7.2 步骤7b：真正的**渲染层**（整帧布局 View、结果列表 AppsResults、单行 Row、
// 图标 Icon、插件列表 PickerResults / PickerRow、空态文案 EmptyHint）已抽进
// internal/ui（见 internal/ui/view.go / apps.go / picker.go）。本文件只留两件东西：
//
//  1. `view()`：把 appState 的 view 态（model / engineState / table / pickerRows 等）
//     与窗口/装配侧的副作用（显隐 / 退出 / 启动 / 插件表产出）**组装**成 internal/ui
//     的渲染入口所需输入，再调它——调用时序、返回值、副作用逐字保持（内部 ui 只画元素、
//     只发意图，真正的 hidePanel / platform.LaunchApp / appsmodel.EnsureLoaded /
//     rankPlugins 仍在下面的执行器里落地，见 §7.2.1「ui 只发意图、main 执行」）。
//  2. 意图执行器（onQueryChanged / applyEngineState / onEsc / moveSelection / open /
//     choose / clickRow / pickRow / confirmPicker / rebuildPicker / execLaunch /
//     execute / apply）与按模式现取现算的文案（placeholder / hintLine / pluginPending /
//     pendingHint / pickerHint）——**模式判定唯一来源是 engine**（铁律②），不进 internal/ui。
//
// 行为对栅（生产 voke 面板 search.rs 只读参考）：
//   - 720 DIP 宽面板，行高 48 DIP（search.rs:144 ROW_HEIGHT）；
//   - Enter 启动选中项，↑↓ 移动选中；**Esc 在任何模式下都直接隐藏面板**（CmdPal 对齐，
//     见本文件 onEsc）；逐级回退（SubMode→Plugin→PluginPicker→Global）由 **Backspace**
//     在输入框已空时承担（onEmptyBackspace → engine.BackspaceBack）；Ctrl+Backspace
//     一键清空查询并回到 Global（onResetInput，不隐藏面板）。
//   - 图标行：platform.AppIcon(path) 的 PNG，nil 时降级首字母占位（渲染在 internal/ui）。
package app

import (
	"errors"
	"log"
	"strings"
	"time"

	appui "voke/internal/ui"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"voke/internal/logic/appsmodel"
	"voke/internal/logic/engine"
	"voke/internal/logic/frequency"
	"voke/internal/logic/panelstate"
	"voke/internal/platform"
	"voke/internal/providers"
	"voke/internal/providers/terminal"
)

// view 是面板的整帧视图（WindowOptions.Content = ui.View(st.view)）。
//
// 本函数只做「读 appState、组 internal/ui 渲染输入、按 mode 选结果区」三件事，画元素
// 的具体过程都在 internal/ui。键盘路由 / 输入框 / 令牌 / 提示行的布局与原 view() 逐字
// 相同，只是搬进了 ui.View；副作用经回调回到本文件的执行器。
func (st *appState) view(c *ui.Context) {
	appui.View(c, st.viewInput())
}

// viewInput 把 appState 组装成 internal/ui 的整帧输入。单独成方法是为了让帧测能
// 改单个字段（如把 Input 置 nil 模拟焦点不在搜索框），复用的仍是生产装配。
func (st *appState) viewInput() appui.ViewInput {
	return appui.ViewInput{
		Hotkey:             hotkeyShow,
		Crumbs:             st.breadcrumbs(),
		Query:              &st.model.Query,
		Input:              &st.input,
		Placeholder:        st.placeholder(),
		RightHint:          st.hintLine(),
		OnEsc:              st.onEsc,
		OnMove:             st.moveSelection,
		OnOpen:             st.open,
		OnCtrlEnter:        st.ctrlEnterSelected,
		OnPinToggle:        st.toggleSelectedPin,
		OnDeleteSelected:   st.deleteSelected,
		OnEndTask:          st.endTaskSelected,
		OnQueryChanged:     st.onQueryChanged,
		OnBackspaceAtEmpty: st.onEmptyBackspace,
		OnResetInput:       st.onResetInput,
		OnOpenSettings:     st.openSettings,
		DiagDone:           &st.gpuDiagOnce,
		RenderResults:      st.renderResults,
	}
}

// renderResults 按 mode 三选一渲染结果区——这段 switch 与原 view() 里的分支逐字对应，
// 判定只看 engine 持有的模式（这里不出现任何前缀比较，铁律②），画元素交给 internal/ui。
//
//   - PluginPicker：出 picker 列表（数据源 pickerRows，rankPlugins 的产物映射成
//     appui.PluginRow，三个字段原样搬运、不重排不重判）；
//   - 表里有但没落地实现的插件（pluginPending）：明说未接入，绝不拿 apps 结果冒充；
//   - 其余（含 Global）：apps 结果列表。
func (st *appState) renderResults(c *ui.Context) {
	switch {
	case st.engineState.Mode() == engine.PluginPicker:
		rows := make([]appui.PluginRow, len(st.pickerRows))
		for i, r := range st.pickerRows {
			rows[i] = appui.PluginRow{ID: r.ID, Name: r.Name, Description: r.Description}
		}
		appui.PickerResults(c, rows, &st.pickerChosen, &st.pickerList, &st.pickerHover, st.open, st.pickRow, st.pickerHint())
	case st.providerActive():
		// 已落地 provider 的插件态：出 providers.RunOne 的排序结果（provRows 在
		// applyEngineState 刷新）；空态文案 provHint（解码失败提示 / 用法占位）。
		appui.ProviderResults(c, st.provRows, &st.provChosen, &st.provList, &st.provHover, st.icons, st.open, st.providerClick, st.providerAction, st.provHint)
	case st.globalActive():
		// 全局搜索且命中了 apps 以外的全局插件组 → 合并分组列表（apps 组在最前）。
		appui.GlobalResults(c, st.globalView(), &st.globalChosen, &st.globalList, &st.globalHover, st.icons, st.open, st.clickGlobal, st.globalAction)
	case st.pluginPending():
		// 表里有、既非 apps 也未落地实现（不在 pluginReg）的插件：明说未接入。
		appui.PendingResults(c, st.pendingHint())
	default:
		appui.AppsResults(c, &st.model, st.icons, &st.list, &st.appsHover, st.open, st.clickRow)
	}
}

// providerActive 报告当前是否落在「已落地实现的 provider 插件」上（Plugin 或 SubMode
// 且 PluginID 命中 pluginReg）。apps / picker 各有专属分支，不算 providerActive。
func (st *appState) providerActive() bool {
	m := st.engineState.Mode()
	if m != engine.Plugin && m != engine.SubMode {
		return false
	}
	if st.pluginReg == nil {
		return false
	}
	_, ok := st.pluginReg.Get(st.engineState.PluginID())
	return ok
}

// placeholder / hintLine 是输入框占位与底部提示的按模式文案。
//
// 底栏只保留**全局通用键**（↑↓ 选择 / Ctrl+Backspace 重置 / Esc 隐藏）与
// 当前模式的 Enter 语义：置顶、删除、关闭、结束任务、纯文本粘贴等行级动作
// 已有 action icon + tooltip，不再堆进底栏。一次性 notice 作为单条纯文案返回。
//
// Esc 在任何模式下都只隐藏、不清空；清空查询与退出插件另由 Ctrl+Backspace
// 手动触发（onResetInput），所以提示仍写「Esc 隐藏」。
func (st *appState) placeholder() string {
	switch st.engineState.Mode() {
	case engine.PluginPicker:
		return "搜索插件"
	case engine.Plugin, engine.SubMode:
		// 已注册插件：用其（子模式）占位符（如 base64「输入要加密/解密的内容」）。
		// 占位符为空则回落；apps / 未注册走下面的默认。
		if st.pluginReg != nil {
			if p, ok := st.pluginReg.Get(st.engineState.PluginID()); ok {
				if s := p.Placeholder(st.engineState.SubMode()); s != "" {
					return s
				}
			}
		}
	}
	return "搜索应用"
}

func (st *appState) hintLine() []appui.FooterHint {
	// 一次性操作提示优先（粘贴失败 / 置顶已满 / 结束任务失败），下一次唤起时按 TTL 清掉。
	if st.notice != "" {
		return []appui.FooterHint{{Label: st.notice}}
	}
	enter := "启动"
	switch {
	case st.engineState.Mode() == engine.PluginPicker:
		enter = "进入"
	case st.isClipboardPlugin():
		enter = "粘贴"
	case st.providerActive() && st.engineState.PluginID() == providerWindowsID:
		enter = "切换"
	case st.providerActive() && st.engineState.PluginID() == terminal.ID:
		enter = "打开"
	case st.providerActive():
		enter = "执行"
	}
	return []appui.FooterHint{
		{Keys: "↑↓", Label: "选择"},
		{Keys: "Enter", Label: enter},
		{Keys: "Ctrl+Backspace", Label: "重置"},
		{Keys: "Esc", Label: "隐藏"},
	}
}

// pluginPending 报告当前是否落在「表里有、但既非 apps 也未落地实现（不在 pluginReg）」
// 的插件上。apps 走自身路径、已注册 provider 走 providerActive 路径，都不算 pending；
// 只有「注册了前缀但还没写实现」的插件才落这里，明说未接入而非拿 apps 结果冒充。
func (st *appState) pluginPending() bool {
	m := st.engineState.Mode()
	if m == engine.Global || m == engine.PluginPicker {
		return false
	}
	if st.engineState.PluginID() == providerAppsID {
		return false
	}
	return !st.providerActive()
}

// pendingHint 是未落地插件的占位文案（把 id 带出来，方便真机/日志对照）。
func (st *appState) pendingHint() string {
	return "插件「" + st.engineState.PluginID() + "」尚未接入"
}

// pickerHint 是 picker 列表空态文案：「一个可选插件都没有」与「过滤词不中」
// 是两回事（前者是注册表只有 picker 自己，属配置问题，得说清）。
func (st *appState) pickerHint() string {
	if st.engineState.Query() == "" {
		return "没有已启用的插件"
	}
	return "没有匹配的插件"
}

// filter 委托 panelstate.Model.Filter，把返回的意图就地执行。
// 只在主线程调用（view 内或 Window.Update 回调内）。
func (st *appState) filter() {
	st.execute(st.model.Filter())
}

// onQueryChanged 是搜索框内容变化后的统一入口：**先问 engine 这是什么模式，
// 再决定把查询词交给谁**。
//
// 接线前这里只有一行 `st.execute(st.model.OnQueryChanged(time.Now()))`；接线后
// 多出来的只有「Scan + 按 mode 派发」，而 mode 恒为 Global 时（=不打前缀=今天
// 全部真实使用路径）派发落回 applyEngineState 的 default 分支，执行的就是
// 那一行本身（见 applyEngineState）。懒加载触发点因此仍在 panelstate.Model 里，
// 没挪、没抄第二份。
func (st *appState) onQueryChanged() {
	next := engine.Scan(st.table, st.engineState, st.model.Query)
	// 只问 Scan 这是什么模式并落位——**不再**用「prev 非空 → 本次空」的状态猜测去
	// 触发回退（那是特殊 hack，且会让输入为空时进的插件按 Backspace 退不动）。逐级
	// 回退统一交 onEmptyBackspace（由输入框 OnTextKey 在「空 + Backspace」按键时驱动）。
	st.applyEngineState(next)
}

// onEmptyBackspace 处理「输入框已空时按 Backspace」：沿 SubMode→Plugin→PluginPicker→
// Global 逐级回退一级；回到的上一层输入恒为空（父层过滤词在入栈时已丢弃）；已在
// Global 则原地不动。由 ui 层输入框的 OnTextKey 捕获该按键调入（非空 Backspace 交
// 编辑引擎正常删字符）。返回 true 消费此键——绝无关面板分支（关面板只由 Esc 负责，
// 见 engine.BackspaceBack 契约 close 恒 false）。
func (st *appState) onEmptyBackspace() bool {
	next, _ := engine.BackspaceBack(st.engineState)
	st.applyEngineState(next)
	return true
}

// onResetInput 处理 Ctrl+Backspace：把会话复位到「全新 Global」——清空查询、
// 退出插件、结果回顶，但**不隐藏面板**（Esc 仍是唯一隐藏键）。复位链直接复用
// resetAfterAction（与离开动作后的复位同一份），区别只在窗口保持可见；因此这里
// 不碰频率表，也不产生启动 / 复制等副作用。
func (st *appState) onResetInput() {
	st.resetAfterAction()
}

// applyEngineState 是模式状态的**唯一落点**：先同步输入框文本，再按 mode 派发。
//
// 同步方向是 State → 输入框（规则⑤：前缀不进输入框）。engine.Scan 命中前缀时
// 把前缀吞掉、Query 置空，这里把框也清成空串；实测 mygo 认这种外部写入
// （textinput.go:54 的 `ed.buf.s != *value` 分支，见 engine_wiring_test.go
// 的 TestTypingPrefixClearsInputBoxInRealFrame——打完 "#" 再打 "y"，
// 框里是 "y" 而不是 "#y"）。
//
// 派发三条路：
//   - PluginPicker：出 picker 列表（rankPlugins），**完全不碰 model** ——
//     picker 里的过滤词因此既不改 apps 的结果集，也不会触发 apps 的懒加载枚举
//     （§7.7 决策④：只有真要搜应用时才枚举）；
//   - Global / Plugin(apps)：把 Query 交给现有的 apps 过滤链路（逐字是接线前那行）；
//   - 未落地的插件（pluginPending）：不出 apps 结果，出占位文案（见 renderResults 的分支）。
func (st *appState) applyEngineState(next engine.State) {
	// 任何查询/作用域变化都让结果列表回到顶部：新一次过滤从第一行开始。
	// 用 defer 是为了覆盖下面各分支自己的 ScrollIntoView(0)——它只保证首行
	// 可见，滚动量很小时可能留下偏移；这里以 ScrollTo(0, Start) 钉死。
	if !st.engineState.Equal(next) {
		defer st.resetListScroll()
	}
	st.engineState = next
	if st.model.Query != next.Query() {
		st.model.Query = next.Query()
	}
	if next.Mode() == engine.PluginPicker {
		st.rebuildPicker()
		return
	}
	if st.providerActive() {
		// 已注册 provider：算它自己的结果，完全不碰 apps 的 model（懒加载也不触发）。
		st.refreshProviderResults()
		return
	}
	if st.pluginPending() {
		return // 结果区出占位文案，这里不动 apps
	}
	st.execute(st.model.OnQueryChanged(time.Now()))
	st.refreshGlobalAgg() // apps 结果更新后重算合并行（有全局插件命中才走合并渲染，否则 apps-only）
}

// refreshOnShow 在面板重新显示后按当前模式重跑一次查询，让隐藏期间变化的数据
// （窗口列表、awake 状态、插件过滤词命中的列表等）在用户敲第一个键之前就是新的。
// 对照 Rust 窗口激活观察者里的 `this.refresh(cx)`（search.rs:513-537）：唤回保留
// 会话，但结果要跟着世界走。
//
// 重掷护栏与 Rust refresh 同源且同样在调用侧先挡一次：当前插件会因查询重掷值
// （random / uuid / timestamp）且输入没变时，**整个刷新都不做**——刚复制、
// 正要复制的值不能被唤回重跑换掉，选中行与滚动位置也原样保留（Rust 在 refresh
// 入口直接 return）。`refreshProviderResults` 内部的同 key 护栏是第二道防线。
//
// 其余分支（picker / 全局 / apps 插件 / 确定性 provider）走与输入变化完全相同的
// applyEngineState 派发，不另抄判定；末尾把列表钉回顶部、选中复位到第 0 行，
// 对照 Rust set_outcome（search.rs:739-751）。
func (st *appState) refreshOnShow() {
	if st.providerActive() {
		p, _ := st.pluginReg.Get(st.engineState.PluginID())
		if p.RegeneratesOnQuery() && providerQueryKey(st.engineState) == st.provLastKey && len(st.provRows) > 0 {
			return
		}
	}
	st.applyEngineState(st.engineState)
	st.resetListScroll()
}

// refreshProviderResults 用当前 engineState（pluginID + subMode + query）跑一遍
// providers.RunOne（weight×score×frequency 排序），写 provRows / provHint，选中复位到
// 第 0 行（与 apps/picker 过滤词一变即回首行同姿势）。
func (st *appState) refreshProviderResults() {
	p, ok := st.pluginReg.Get(st.engineState.PluginID())
	if !ok {
		st.provRows, st.provChosen, st.provHint = nil, 0, ""
		st.provLastKey = ""
		return
	}
	key := providerQueryKey(st.engineState)
	// 会重掷结果的 provider（random/timestamp/uuid）：输入没变（面板重新显示 / 热更新
	// 等「没改却重跑」的路径）必须跳过重查，否则用户正要复制的值会被换成新的——
	// 对照 Rust `regenerates_on_query` 的调用侧护栏。确定性 provider 重跑结果一致，不必跳。
	if p.RegeneratesOnQuery() && key == st.provLastKey && len(st.provRows) > 0 {
		return
	}
	ctx := providers.QueryCtx{Raw: st.engineState.Query(), Query: st.engineState.Query(), SubMode: st.engineState.SubMode()}
	ranked, hints := providers.RunOne(p, ctx, st.freq)
	st.provLastKey = key
	st.provRows, st.provChosen, st.provHint = ranked, 0, ""
	if len(ranked) == 0 {
		if len(hints) > 0 {
			st.provHint = hints[0].Title // 例如 base64 解码失败的「解码失败」
		} else {
			st.provHint = p.Placeholder(st.engineState.SubMode()) // 空输入的用法提示
		}
	}
	if len(ranked) > 0 {
		st.provList.ScrollIntoView(st.provChosen)
	}
}

// breadcrumbs 把作用域栈映射成输入行的面包屑（栈底→栈顶，跳过 Global）。每段带显示名 +
// 取图标的插件 id + 是否当前层；ui 层按 PluginIconSVG 画图标、› 分隔、当前层高亮。
func (st *appState) breadcrumbs() []appui.Crumb {
	scs := st.engineState.Scopes()
	out := make([]appui.Crumb, 0, len(scs))
	for i, sc := range scs {
		if sc.Kind == engine.Global {
			continue
		}
		label, pid := sc.PluginID, sc.PluginID
		switch sc.Kind {
		case engine.PluginPicker:
			label, pid = "插件列表", engine.PickerPluginID
		case engine.Plugin:
			label = st.providerName(sc.PluginID)
		case engine.SubMode:
			label = sc.SubMode // 子命令标识（enc/dec 等），图标沿用所属插件
		}
		out = append(out, appui.Crumb{Label: label, ProviderID: pid, Current: i == len(scs)-1})
	}
	return out
}

// providerQueryKey 是 provider 一次查询的输入指纹（pluginID+subMode+query）。
// 会重掷结果的 provider 靠它判断「输入没变」→ 跳过重查。
func providerQueryKey(s engine.State) string {
	return s.PluginID() + "\x00" + s.SubMode() + "\x00" + s.Query()
}

// onEsc 是 Esc 的唯一入口：**任何模式下 Esc 一律直接隐藏面板**（CmdPal 对齐，用户拍板）。
// 逐级回退不再由 Esc 承担——那是 Backspace 的职责（见 onQueryChanged 里对
// engine.BackspaceBack 的调用）。隐藏**保留会话**：查询与插件页原样留在下一次唤起
// （Rust parity——重新显示不等于修改输入）；清空与退出插件只发生在「离开面板」的
// 动作成功之后（resetAfterAction）。
func (st *appState) onEsc() {
	st.hidePanel()
}

// moveSelection 移动选中行：picker 模式在自己的列表上走，其余（含 Global）
// 逐字是接线前的 `st.choose(st.model.Chosen ± delta)`。
func (st *appState) moveSelection(delta int) {
	if st.engineState.Mode() == engine.PluginPicker {
		st.choosePicker(st.pickerChosen + delta)
		return
	}
	if st.providerActive() {
		st.chooseProvider(st.provChosen + delta)
		return
	}
	if st.globalActive() {
		st.chooseGlobal(st.globalChosen + delta)
		return
	}
	st.choose(st.model.Chosen + delta)
}

// choose 移动选中行（钳在 [0, len-1]），并把「滚入视野」的意图执行到 st.list。
// 钳位与「空列表不滚动」的判断都在 panelstate.Model.Choose 里，本函数只落地副作用。
func (st *appState) choose(i int) {
	st.execute(st.model.Choose(i))
}

// choosePicker 是 picker 版的 choose：钳位 + 滚动，但只碰 picker 自己的状态。
// 空列表只复位选中、不滚动（与 panelstate.Model.Choose 同一口径）。
func (st *appState) choosePicker(i int) {
	if len(st.pickerRows) == 0 {
		st.pickerChosen = 0
		return
	}
	st.pickerChosen = max(0, min(i, len(st.pickerRows)-1))
	st.pickerList.ScrollIntoView(st.pickerChosen)
}

// clickRow 处理「单击某行」：委托 Model.ClickRow 得到启动意图，交由 execLaunch
// 执行（启动成功才清空+过滤+隐藏，失败保留面板）。选中行的设置已在 Model 内完成。
func (st *appState) clickRow(i int) {
	st.execLaunch(st.model.ClickRow(i))
}

// pickRow 单击 picker 的一行 = 选中它并进入它（决策⑤「单击即执行」在 picker
// 列表上的对应物：这里的「执行」是进插件第一级，不是启动）。
func (st *appState) pickRow(i int) {
	if i < 0 || i >= len(st.pickerRows) {
		return
	}
	st.pickerChosen = i
	st.confirmPicker()
}

// confirmPicker 把 picker 选中的插件 id 交给 engine.SelectPlugin 进第一级。
// 不在这里手拼 State（铁律②：模式构造只留在 engine）。
func (st *appState) confirmPicker() {
	if st.pickerChosen < 0 || st.pickerChosen >= len(st.pickerRows) {
		return
	}
	st.applyEngineState(engine.SelectPlugin(st.engineState, st.pickerRows[st.pickerChosen].ID))
}

// enterPlugin 是「程序化进入某插件第一级」的唯一入口——对照生产
// main.rs 的 `open_awake → view.activate_plugin("awake")`：不经输入框、不打前缀，
// 由外部动作（托盘状态条目点击）直接把模式切到指定插件。
//
// 它是**换会话**而不是压栈：对照 Rust activate_plugin_from_picker 的
// 「关 picker / 替换 active_plugin / 子模式清空 / 输入框清空」。先 resetSession
// 把栈清回 Global，再由 engine.SelectPlugin 压入目标插件，于是无论从哪个旧会话
// （Global / 任意插件 / 子模式）点托盘 awake，落点都是干净的 [Global, Plugin]；
// 若直接压栈，awake 会嵌套在旧插件之下，Backspace 退回去还能看到上一个插件。
// 构造仍只交给 engine（铁律②：模式构造只留在 engine，app 层不手拼 State）。
//
// 与 confirmPicker 的区别因此不只是 id 来源：用户在 `#` 列表里选中插件是
// **进入路径**，picker 帧要留在栈里供 Backspace 退回；程序化激活没有这条路径。
func (st *appState) enterPlugin(id string) {
	st.resetSession()
	st.applyEngineState(engine.SelectPlugin(st.engineState, id))
}

// rebuildPicker 按当前过滤词重出 picker 列表，并把选中复位到第 0 行
// （与 apps 侧 filter→Choose(0) 同一姿势：过滤词一变，选中的是「新列表的第一行」）。
func (st *appState) rebuildPicker() {
	st.pickerRows = rankPlugins(st.providers, st.engineState.Query())
	st.pickerChosen = 0
	if len(st.pickerRows) > 0 {
		st.pickerList.ScrollIntoView(st.pickerChosen)
	}
}

// open 是 Enter / ↵ / list.Submitted 的唯一入口，先问模式：
//
//   - PluginPicker：Enter 选定当前插件行 → 进该插件第一级（engine.SelectPlugin）；
//   - 未落地的插件：什么都不做（绝不能拿 apps 的陈旧结果去启动）；
//   - 其余（含 Global）：逐字是接线前的 `st.execLaunch(st.model.Open())`。
func (st *appState) open() {
	switch {
	case st.engineState.Mode() == engine.PluginPicker:
		st.confirmPicker()
	case st.providerActive():
		st.openProvider()
	case st.globalActive():
		st.openGlobal(st.globalChosen)
	case st.pluginPending():
		return
	default:
		st.execLaunch(st.model.Open())
	}
}

// providerClick 单击 provider 结果行：选中它再走与 Enter 同一条执行路（决策⑤单击即执行）。
func (st *appState) providerClick(i int) {
	if i < 0 || i >= len(st.provRows) {
		return
	}
	st.provChosen = i
	st.openProvider()
}

// chooseProvider 钳位 provider 选中行 + 滚入视野；空列表只复位。
func (st *appState) chooseProvider(i int) {
	if len(st.provRows) == 0 {
		st.provChosen = 0
		return
	}
	st.provChosen = max(0, min(i, len(st.provRows)-1))
	st.provList.ScrollIntoView(st.provChosen)
}

// openProvider 执行当前 provider 选中行的意图：委托 runProviderIntent（providerID 取当前
// 激活插件），保持与拆出前逐字一致的行为。
func (st *appState) openProvider() {
	if st.provChosen < 0 || st.provChosen >= len(st.provRows) {
		return
	}
	st.runProviderIntent(st.provRows[st.provChosen].Match, st.engineState.PluginID())
}

// providerAction 执行结果行尾的操作图标。action 由 provider 随 Match 给出，
// app 只按 Action 的纯数据意图执行；点击 icon 不改变默认动作。
func (st *appState) providerAction(i int, id string) {
	if i < 0 || i >= len(st.provRows) {
		return
	}
	m := st.provRows[i].Match
	for _, a := range m.Actions {
		if a.ID != id {
			continue
		}
		m.Action = a.Action
		st.runProviderIntent(m, m.ProviderID)
		return
	}
}

// ctrlEnterSelected 是 Ctrl+Enter 的统一入口：剪贴板历史优先走纯文本粘贴；
// 其它插件按当前选中行的 RowAction.Shortcut 分派（如终端“以管理员身份打开”）。
func (st *appState) ctrlEnterSelected() bool {
	if st.pasteSelectedPlain() {
		return true
	}
	return st.runSelectedRowShortcut("Ctrl+Enter")
}

// runSelectedRowShortcut 在当前选中行（插件态或全局合并态、非 apps 行）的
// RowAction 里找展示快捷键等于 shortcut 的项并执行。找不到返回 false，不抢占按键。
func (st *appState) runSelectedRowShortcut(shortcut string) bool {
	m, ok := st.selectedProviderMatch()
	if !ok {
		return false
	}
	for _, a := range m.Actions {
		if !strings.EqualFold(a.Shortcut, shortcut) {
			continue
		}
		m.Action = a.Action
		st.runProviderIntent(m, m.ProviderID)
		return true
	}
	return false
}

// selectedProviderMatch 取当前选中行的 provider Match：插件态直接取 provRows；
// 全局合并态只在选中非 apps 行时取 globalProvRows（apps 走自己的启动链路）。
func (st *appState) selectedProviderMatch() (providers.Match, bool) {
	if st.providerActive() {
		if st.provChosen < 0 || st.provChosen >= len(st.provRows) {
			return providers.Match{}, false
		}
		return st.provRows[st.provChosen].Match, true
	}
	if st.globalActive() {
		if st.globalChosen < 0 || st.globalChosen >= len(st.globalRows) {
			return providers.Match{}, false
		}
		r := st.globalRows[st.globalChosen]
		if r.apps || r.provIdx < 0 || r.provIdx >= len(st.globalProvRows) {
			return providers.Match{}, false
		}
		return st.globalProvRows[r.provIdx].Match, true
	}
	return providers.Match{}, false
}

// runProviderIntent 是 provider 结果行「执行」的唯一实现（IntentFor 分流）：
// Copy→写剪贴板+记频率+隐藏（**不复位**，下次唤回可再复制）；EnterSubMode→进
// 子模式并刷新；ActivatePlugin→切插件；PlatformAction→其余副作用（execPlatformAction，
// 成功后隐藏+复位，对照 Rust Action::should_reset_input）。providerID 供频率键（Global
// 合并列表里各行 providerID 不同，故参数化；插件态传 engineState.PluginID = 原行为）。
func (st *appState) runProviderIntent(m providers.Match, providerID string) {
	switch intent := providers.IntentFor(m); intent.Kind {
	case providers.IntentCopy:
		platform.WriteClipboard(intent.Text)
		// 只记「查找类且身份稳定」的 provider（见 frequency.Recordable）：
		// uuid/random/awake 这些结果是生成值，复制它们不该影响任何排序。
		if st.freq != nil && frequency.Recordable(providerID) {
			st.freq.Record(frequency.Key(providerID, m.Title))
			saveFrequency(st.freq) // 持久化由 env 路径决定；未设 = no-op
		}
		// 对照 Rust Copy：只隐藏、**不 reset_after_action**。输入与结果原样保留，
		// 用户下次唤回还能看到刚复制的值并再次复制（重跑会把值换掉）。
		st.hidePanel()
	case providers.IntentEnterSubMode:
		st.applyEngineState(engine.EnterSubMode(st.engineState, providerID, intent.Text))
	case providers.IntentActivatePlugin:
		st.applyEngineState(engine.SelectPlugin(st.engineState, intent.Text))
	case providers.IntentRefresh:
		// 「重新生成」行（random 二级首条）：用同一输入再掷一批。面板保留、输入不动
		// （对照生产 Action::Refresh：should_reset_input=false + 「让 app 用同一输入再掷一批」）。
		// 清 provLastKey 绕过 RegeneratesOnQuery 的「同输入跳过」护栏——那条是给「没改却重跑」
		// （热更 / 重新显示）防抖用的，这里是用户显式点了重掷，必须真再算一遍出新批次。
		st.provLastKey = ""
		st.refreshProviderResults()
	case providers.IntentPaste:
		st.pasteClipboardItem(intent.Ref, false)
	case providers.IntentPastePlain:
		st.pasteClipboardItem(intent.Ref, true)
	case providers.IntentPin:
		st.toggleClipboardPin(intent.Ref)
	case providers.IntentUnpin:
		st.toggleClipboardPin(intent.Ref)
	case providers.IntentDelete:
		st.deleteClipboardItem(intent.Ref)
	case providers.IntentEndTask:
		// 直接结束进程；成功后面板收起。
		if st.endTask(intent.Hwnd, m.Title) {
			st.hidePanel()
			st.resetAfterAction()
		}
	case providers.IntentCloseWindow:
		// WM_CLOSE 等价点关闭按钮：应用可能弹保存确认，故成功投递后面板也收起，
		// 避免浮层挡住确认框。
		if st.closeWindow(intent.Hwnd, m.Title) {
			st.hidePanel()
			st.resetAfterAction()
		}
	case providers.IntentPlatformAction:
		if st.execPlatformAction(m.Action) {
			if st.freq != nil && frequency.Recordable(providerID) {
				st.freq.Record(frequency.Key(providerID, m.Title))
				saveFrequency(st.freq)
			}
			// 保持唤醒 / 取消唤醒 / 切窗口 / 启动（含终端 profile）都属于 Rust
			// Action::should_reset_input 的「离开面板」类：先隐藏，再复位成干净顶层。
			st.hidePanel()
			st.resetAfterAction()
		}
		// 返回 false（窗口已消失 / 启动失败 / 未接入）→ 不隐藏，保留面板让用户重试。
	default: // IntentNothing：提示行，什么都不做
	}
}

// execPlatformAction 执行 provider 的副作用动作（对照 voke-app 的 dispatcher：core 只
// 声明意图、副作用在边界落地）。返回 true = 已生效（调用方据此记频率 + 隐藏面板）；
// false = 未生效（窗口没了 / 启动失败 / 动作类型尚未接入），面板保留。
//
// 平台相关调用都在 internal/platform 里，Windows 真实现、非 Windows 记日志占位，
// 因此本函数跨平台可测（Linux 上 SetAwake/CancelAwake 走 stub、ActivateWindow 恒 false）。
func (st *appState) execPlatformAction(a providers.Action) bool {
	switch a.Kind {
	case providers.ActionSetAwake:
		platform.SetAwake(a.Minutes, a.KeepScreen)
		st.refreshTrayMenu() // awake 变化即时刷新托盘状态条目（主线程，见 panel.go refreshTrayMenu）
		return true
	case providers.ActionCancelAwake:
		platform.CancelAwake()
		st.refreshTrayMenu()
		return true
	case providers.ActionActivateWindow:
		return platform.ActivateWindow(a.Hwnd)
	case providers.ActionLaunch:
		return platform.Launch(a.Path, a.Args, a.NoConsole) == nil
	case providers.ActionLaunchTerminalProfile:
		if a.Elevated {
			return st.launchTerminalElevated(a)
		}
		// WT profile：先 App Activation（对齐 CmdPal / 生产，可带 `--profile "<guid>"`），
		// 失败（如 WT 非 MSIX 安装的解包版）回退 wt.exe 直启 `-p <profile>`。对照
		// voke-app dispatcher 的 n_profile（activate_app → 失败兜底 CreateProcess）。
		if pid, ok := platform.ActivateApp(a.Aumid, terminal.ActivationArguments(a.Profile)); ok {
			log.Printf("voke：WT App Activation 已提交 aumid=%s profile=%s pid=%d", a.Aumid, a.Profile, pid)
			return true
		}
		if a.FallbackPath != "" {
			if err := platform.Launch(a.FallbackPath, []string{"-p", a.Profile}, true); err == nil {
				log.Printf("voke：App Activation 失败，已回退 wt.exe 直启 profile=%s", a.Profile)
				return true
			} else {
				log.Printf("voke：wt.exe 兜底直启也失败（%s）：%v", a.FallbackPath, err)
			}
		}
		log.Printf("voke：终端 profile 启动失败（激活与兜底均未成功）profile=%s", a.Profile)
		return false
	default:
		log.Printf("voke：provider 动作类型 %v 未接入执行", a.Kind)
		return false
	}
}

// launchTerminalElevated 以管理员身份打开 WT profile。App Activation 不能提权，
// 直接对 wt.exe 走 ShellExecuteEx(runas)，由 UAC 决定是否放行。
func (st *appState) launchTerminalElevated(a providers.Action) bool {
	exe := a.FallbackPath
	if exe == "" {
		exe = "wt.exe"
	}
	err := platform.RunElevated(exe, []string{"-p", a.Profile}, false, nil)
	if err != nil {
		if errors.Is(err, platform.ErrElevationCanceled) {
			st.showNoticePanel("已取消管理员权限请求")
		} else {
			log.Printf("voke：以管理员身份启动终端失败（%s profile=%s）：%v", exe, a.Profile, err)
			st.showNoticePanel("以管理员身份启动失败")
		}
		return false
	}
	log.Printf("voke：已请求以管理员身份启动终端 profile=%s", a.Profile)
	return true
}

// showNoticePanel 在提权交互被取消 / 失败后把面板拉回并显示提示。UAC 会抢焦点，
// OnBlur 可能排在消息队列里、回到主循环后才把面板藏起来，故稍后再确认一次可见性。
// 走 showPanelKeep：提示属于当前会话（可能是插件热键进来的剪贴板页），不切回主会话。
func (st *appState) showNoticePanel(msg string) {
	st.setNotice(msg)
	if st.panel == nil || st.panel.IsDestroyed() {
		return
	}
	st.showPanelKeep()
	time.AfterFunc(60*time.Millisecond, func() {
		mygo.RunOnMain(func() {
			if st.quitting || st.panel == nil || st.panel.IsDestroyed() || st.panel.IsVisible() {
				return
			}
			st.showPanelKeep()
			st.setNotice(msg)
		})
	})
}

// execLaunch 执行「可能含启动动作」的意图序列，保持原 clickRow/open 的时序：
//
//   - 遇到 ActionLaunch：取 Results[Index] 调 platform.LaunchApp；**成功**才
//     隐藏面板，再复位会话（对照 Rust dispatcher 先隐藏 + search.rs
//     reset_after_action：清查询 / 退插件 / 顶层列表预热），然后返回；
//     **失败**只记日志并立即返回——面板得以保留，用户可改查询重试。
//   - 其余意图（如未来可能出现的非启动前置动作）按序执行。
//
// 复位必须等 LaunchApp 真的成功之后提交：失败时状态一点都不能动（E-1）。
// 顺序也固定为「先隐藏、后复位」（Rust 注释明确 reset 的调用时机在窗口已被
// dispatcher 隐藏之后）：隐藏那一刻用户已看到动作生效，后台再把界面收拾干净。
//
// platform.LaunchApp 只在主线程调（背后 onMainValue，见 internal/platform/shell.go
// 的线程标注）；本函数的调用点（Enter / 单击）本来就在主线程。
func (st *appState) execLaunch(acts []panelstate.Action) {
	for _, a := range acts {
		if a.Kind == panelstate.ActionLaunch {
			app := st.model.Results[a.Index]
			if err := platform.LaunchApp(app); err != nil {
				log.Printf("启动 %s 失败：%v", app.Name, err)
				return
			}
			// 启动成功 → 记一次 apps 使用频率（对照生产 search.rs record_use("apps:<name>")），
			// 下次 FilterApps 的 rank 据此把它顶到前面。键前缀用 appsmodel.ProviderID，与
			// search.boostOf 的键必须一致。失败不记（与生产「选中执行才记」同）。
			if st.freq != nil {
				st.freq.Record(frequency.Key(appsmodel.ProviderID, app.Name))
				saveFrequency(st.freq)
			}
			st.hidePanel()
			st.resetAfterAction()
			return
		}
		st.apply(a)
	}
}

// execute 按序执行一组意图（不含启动——启动走 execLaunch 的成功门控）。
func (st *appState) execute(acts []panelstate.Action) {
	for _, a := range acts {
		st.apply(a)
	}
}

// apply 把单条「意图」翻译成对应的 UI/平台副作用。这是 panelstate 与 mygo 之间
// 唯一的边界：logic 层只说做什么，副作用在此落地。隐藏/复位不再经 Model 表达：
// 隐藏是各动作执行器成功后直接调的（execLaunch / runProviderIntent），
// show 仍只从 showPanel 走（§7.1.4）。
func (st *appState) apply(a panelstate.Action) {
	switch a.Kind {
	case panelstate.ActionEnsureLoad:
		appsmodel.EnsureLoaded()
	case panelstate.ActionScrollTo:
		st.list.ScrollIntoView(a.Index)
	}
}
