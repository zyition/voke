// dispatch.go：把一条选中的 Match 翻译成**动作意图**（Intent）——这是 §7.2.2
// `app/dispatch.go`（对应生产 dispatcher.rs）想要的「逻辑声明做什么、副作用在边界
// 落地」的纯逻辑前半：给定 Interact/Action，Enter/点击该触发什么，用可测的纯函数定死。
//
// 与现有 apps 路径正交：apps 的启动仍走 panelstate.Action + view.execLaunch（不动）；
// 本文件只管 providers/ 这一路的非-apps 插件（base64/uuid/…）结果行的语义分流。
package providers

// IntentKind 是动作意图类别。
type IntentKind uint8

const (
	// IntentNothing：提示行（Hint），Enter/点击什么都不做。
	IntentNothing IntentKind = iota
	// IntentCopy：把 Text 写进剪贴板（对照 Action::Copy，最常用）。
	IntentCopy
	// IntentEnterSubMode：进入 Text 标识的子模式（对照 Interact::EnterSubquery，如 base64 的 enc/dec）。
	IntentEnterSubMode
	// IntentActivatePlugin：切换到 Text 指定的插件（对照 Interact 之外的 Action::ActivatePlugin，# 列表选中）。
	IntentActivatePlugin
	// IntentRefresh：用同一输入重掷一批（对照 Action::Refresh，random 二级首条「重新生成」）。
	// 不是平台副作用——它只是让视图对当前 (plugin, subMode, query) 再跑一遍 provider，
	// 面板保留、输入不动（生产 should_reset_input=false）。
	IntentRefresh
	// IntentPlatformAction：其余副作用（Launch/ActivateWindow/SetAwake/…），交 platform/dispatcher 按 Match.Action 落地。
	IntentPlatformAction
	// IntentPaste / IntentPastePlain：把 Ref 指向的剪贴板历史项粘贴到原窗口。
	IntentPaste
	IntentPastePlain
	// IntentPin / IntentUnpin / IntentDelete：置顶与删除历史项，面板保留。
	IntentPin
	IntentUnpin
	IntentDelete
)

// Intent 是一条可执行意图（纯数据）。
type Intent struct {
	Kind IntentKind
	Text string // Copy 文本 / 子模式标识 / 插件 id
	Ref  string // 插件内部条目引用（剪贴板 item id）
}

// IntentFor 把一条 Match 摊平成意图，对照生产行为：
//   - Hint → Nothing（不可选，本就不该走到这里，兜底返回 Nothing 不 panic）；
//   - EnterSubquery → EnterSubMode（Text = 子模式标识）；
//   - Execute 且 Action.Copy → Copy（Text = 复制内容）；
//   - Execute 且 Action.ActivatePlugin → ActivatePlugin；
//   - Execute 且 Action.Refresh → Refresh（用同一输入重掷一批，面板保留）；
//   - Execute 其它动作 → PlatformAction（由 platform/dispatcher 消费 Match.Action）。
func IntentFor(m Match) Intent {
	switch m.Interact.Kind {
	case InteractHint:
		return Intent{Kind: IntentNothing}
	case InteractEnterSubquery:
		return Intent{Kind: IntentEnterSubMode, Text: m.Interact.Sub}
	default: // InteractExecute
		return IntentForAction(m.Action)
	}
}

// IntentForAction 把单个 Action 摊平成意图；行内附加操作与默认动作共用。
func IntentForAction(a Action) Intent {
	switch a.Kind {
	case ActionCopy:
		return Intent{Kind: IntentCopy, Text: a.Text}
	case ActionActivatePlugin:
		return Intent{Kind: IntentActivatePlugin, Text: a.PluginID}
	case ActionRefresh:
		return Intent{Kind: IntentRefresh}
	case ActionPaste:
		return Intent{Kind: IntentPaste, Ref: a.Ref}
	case ActionPastePlain:
		return Intent{Kind: IntentPastePlain, Ref: a.Ref}
	case ActionPin:
		return Intent{Kind: IntentPin, Ref: a.Ref}
	case ActionUnpin:
		return Intent{Kind: IntentUnpin, Ref: a.Ref}
	case ActionDelete:
		return Intent{Kind: IntentDelete, Ref: a.Ref}
	default:
		return Intent{Kind: IntentPlatformAction}
	}
}
