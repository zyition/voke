// Package ui 是面板的**视图/渲染层**（PoC 文档 §7.2 步骤7b / §7.2.1 三层铁律的 ui 层）。
//
// 本包从 package main 的 view.go 抽出**只服务渲染**的那一截：整帧视图 `View`、
// 结果列表 `AppsResults` / 单行 `Row` / 图标 `Icon`、插件列表 `PickerResults` /
// 单行 `PickerRow`，以及只产出「渲染文本」的 `EmptyHint`。行为逐字不变。
//
// ## 分层（§7.2.1）
//
//   - 本层**只读 logic 状态**（`panelstate.Model` / `appsmodel.AppEntry`）、**只画元素**、
//     **只发意图**（把点击/回车/退格回调转交给持有者），绝不自己判定业务、绝不建窗口；
//   - **不碰 `mygo.App` 生命周期 / 窗口创建 / 托盘 / 热键 / 单实例**（那是步骤8，留 main）：
//     面板显隐、退出、启动、插件表产出都由 main 侧以回调形式注入（见 `ViewInput` 与各渲染
//     函数的 `onOpen` / `onClick` / `onPick` 参数）；调用时序、返回值、副作用逐字保持；
//   - 允许 import mygo 的 `ui` 面（渲染层本就该在能碰 mygo 的层）与 `logic/*` + `internal/ui/theme`。
//
// mygo 的 ui 包在本包内以 `goui` 别名引用——本包自身名为 `ui`，若再引入未导出的同名
// `ui` 会和 mygo 的 `ui` 撞名，故必须起别名。
package ui

import (
	"log"

	goui "github.com/egoist/mygo/ui"

	"voke/internal/ui/theme"
)

// ViewInput 是整帧视图 `View` 需要的**全部输入**：渲染要读的 view 态以值/指针传入，
// 需要触达窗口/装配的副作用以回调传入。main 侧的 `appState.view` 负责组装本结构。
//
// 各字段的语义与原 package main/view.go 里 `view()` 直接读 `st.X` / 调 `st.X()` 一一对应，
// 搬动只是把「读哪个字段、调哪个方法」换成「读哪个入参、调哪个回调」，一帧里的执行顺序不变。
// Crumb 是面包屑的一段：某作用域的显示名 + 取图标的插件 id + 是否当前层。
type Crumb struct {
	Label      string
	ProviderID string // 取 Lucide 角标图标（PluginIconSVG）；picker 用其 id 落兜底图标
	Current    bool
}

type ViewInput struct {
	// Hotkey 是唤/隐热键的显示串（原 `hotkeyShow` 常量），渲染底部提示行左侧文案用。
	Hotkey string
	// Crumbs 是作用域栈的面包屑（栈底→栈顶，Global 不在内）：输入行左侧按层级渲染
	// 「图标 + 名称」，段间用 › 分隔，当前层高亮。空切片时输入行布局与 Global 逐字相同。
	Crumbs []Crumb
	// Query 指向搜索框绑定的字符串（原 `&st.model.Query`）。
	Query *string
	// Input 是搜索框句柄指针（原 `&st.input`）：每帧按回焦点用。
	Input *goui.Handle
	// Placeholder 是输入框占位/无障碍文案（原 `st.placeholder()`）。
	Placeholder string
	// RightHint 是底部提示行右侧的按键说明（原 `st.hintLine()`）：
	// 只放全局通用键与当前模式的 Enter 语义，行级操作留给 action icon。
	RightHint []FooterHint

	// OnEsc / OnMove / OnOpen / OnQueryChanged 是键盘路由落点（原 `st.onEsc` /
	// `st.moveSelection` / `st.open` / `st.onQueryChanged`）。OnMove 收 delta（↓=+1 / ↑=-1）。
	OnEsc  func()
	OnMove func(delta int)
	OnOpen func()
	// OnCtrlEnter 是 Ctrl+Enter：按当前插件分派——剪贴板纯文本粘贴、终端
	// “以管理员身份打开”等。返回 true 表示已消费。
	OnCtrlEnter func() bool
	// OnPinToggle / OnDeleteSelected 是 Ctrl+Shift+T / Ctrl+Delete：置顶只对剪贴板
	// 生效，删除/关闭由 main 侧按当前行的 RowAction.Shortcut 分派（剪贴板删除 /
	// 窗口关闭），UI 只转交按键。
	OnPinToggle      func()
	OnDeleteSelected func()
	// OnEndTask 是 Ctrl+Shift+Delete：当前选中窗口行的「结束任务」。同样由 main
	// 侧判断是否消费（非窗口行无操作）。
	OnEndTask      func()
	OnQueryChanged func()
	// OnBackspaceAtEmpty 在「输入框已空时按 Backspace」触发（经输入框 OnTextKey 捕获，
	// 非空时交编辑引擎正常删字符）。返回 true 表示已被消费（逐级回退），编辑器不再处理；
	// 退到顶级应原地不动、绝不关面板。这样回退链单一由 Backspace 按键驱动，无「删到空的
	// 那一刻自动弹出」之类的状态猜测 hack。
	OnBackspaceAtEmpty func() bool
	// OnResetInput 在 Ctrl+Backspace 触发：清空查询并把作用域退回 Global
	// （不隐藏面板）。输入框聚焦时经 OnTextKey 拦截（编辑器会把它当删词），
	// 焦点不在输入框时经窗口级 Shortcut 路由；两路互斥，复位语义在 app 侧。
	OnResetInput func()
	// OnOpenSettings 是 Ctrl+, 打开设置窗（对照生产 search.rs on_key 的 Ctrl+, →
	// settings::open）。开窗口 / 建窗是装配层的事，本层只把快捷键转成回调。
	OnOpenSettings func()

	// RenderResults 是结果区三选一（Picker / 未接入插件 / apps）的渲染闭包，由 main 侧
	// 按 `st.engineState.Mode` 现取现算（判定逻辑与 view 分支逐字一致）后传进来，本层不
	// 自己判模式。参数 c 是这一帧的 mygo Context。
	RenderResults func(c *goui.Context)

	// DiagDone 是「设备类型诊断只打一次」的闸门指针（原 `&st.gpuDiagOnce`），首帧绘制时落位。
	DiagDone *bool
}

// View 是面板的整帧视图（WindowOptions.Content = ui.View(st.view)，见 main/panel.go）。
//
// 逐字搬自 package main/view.go 的 `view()`：root 底色、一次性设备诊断 Draw、键盘路由、
// 搜索行（模式令牌 + 输入框）、分隔线、结果区三选一、分隔线、底部提示行、每帧按回焦点，
// 顺序一个都不改。原读 `st.X` 的地方改成读 `in.X`、原调 `st.X()` 的地方改成调 `in.X()`，
// 副作用（显隐/退出/启动/插件表产出）经回调回到 main 执行器。
func View(c *goui.Context, in ViewInput) {
	t := c.Theme()
	root := c.Root()
	if c.Vibrancy() {
		root.Background(goui.Transparent) // 有毛玻璃材质时透出
	} else {
		root.Background(t.Background)
	}
	// 一次性设备类型诊断（诊断脚手架，2026-10-16 GPU 归因加）。
	//
	// 为什么必须挂在 Draw 回调里、而不是 view 回调里直接问：
	// ui/paint.go:87-90 的 Painter.Accelerated() 走
	// p.rt.host.(interface{ accelerated() bool })，只有 ui/window.go:428
	// 的 *windowHost 实现；且其 accelerated() 里
	// 「if h.framing && !h.gpuTried { h.makeGPU() }」——**首帧的 GPU 设备
	// 是在绘制途中现造的**，而不是 present 时（见 window.go:256-257）。
	// 也就是说，首帧 Draw 里问到的 accelerated() 正是设备刚造完、
	// warp 标志刚定下来的那一刻，是全生命周期里唯一能区分
	// 硬件 GPU / WARP 软件光栅化的时机。view 回调（build 阶段）拿不到
	// Painter，问不到。
	//
	// 语义：Accelerated()==true 表示这一帧画在**硬件 GPU** 上；
	// ==false 表示画在 CPU 上——在 Windows 上就是 D3D11 的 WARP 软件
	// 光栅化（device.go:38-48 的 hardware→warp 回退），也可能是
	// MYGO_GPU=0 或根本没有 GPU 设备。详见设备诊断结论。
	//
	// 为什么不能复用 shownOnce：shownOnce 在 showPanel() 里 Show() 之前
	// 就置位（panel.go:170-173），而 Draw 发生在其后的首帧，届时它已是
	// true，一次都不会打。故闸门（in.DiagDone，原 st.gpuDiagOnce）独立于此。
	root = root.Draw(func(p *goui.Painter, r goui.Rect) {
		if in.DiagDone == nil || *in.DiagDone {
			return
		}
		*in.DiagDone = true
		log.Printf("设备诊断：Accelerated=%t（true=硬件 GPU，false=WARP 软件光栅化）", p.Accelerated())
	})

	// 键盘路由说明（mygo 源码核实，见 PoC 文档 §8）：
	//   - ↑/↓/Esc 不被聚焦中的 TextInput 消费（editor.wants 对单行输入
	//     只认 Enter/Backspace/方向编辑/Home/End/Delete，ui/editor_navigation.go:27-61），
	//     以 shortcut 形式抵达本视图；
	//   - Enter 被输入框消费为 Submitted（editor_navigation.go:276-284 单行
	//     markSubmitted），不到 shortcut——所以启动走输入框的 Submitted()，
	//     列表获焦时另走 list.Submitted()，两路互补；
	//   - IME composing 期间按键归输入法（文档承诺 + Windows IMM32 实现，
	//     结论记入 PoC 文档 §8.6）：composing 中按 Enter/Esc 选字/取消组句，
	//     根本不会以 shortcut 抵达这里，无需也不应自行过滤。
	//
	// §7.3.3 接线后：四个键都先**问模式**再落地，判定唯一来源是 engine（main 侧的
	// onEsc / moveSelection / open 三个入口）；本层只把按键路由转交回调，不判模式。
	if c.Shortcut(0, goui.KeyEscape) {
		in.OnEsc()
	}
	if c.Shortcut(0, goui.KeyDown) {
		in.OnMove(1)
	}
	if c.Shortcut(0, goui.KeyUp) {
		in.OnMove(-1)
	}
	if c.Shortcut(0, goui.KeyEnter) {
		in.OnOpen()
	}
	// Ctrl+Backspace：清空查询并回到 Global（不隐藏）。窗口级路由覆盖
	// 「面板在前台、焦点还没回到输入框」的场景；输入框聚焦时这条不会触发，
	// 由 TextInput 的 OnTextKey 接管——编辑器会先把这个组合键当删词吃掉。
	if c.Shortcut(goui.Ctrl, goui.KeyBackspace) {
		in.OnResetInput()
	}
	// 生产 panel 键位对齐（voke-app/src/search.rs on_key 的最小集 + 组合键别名）：
	// Ctrl+N / Ctrl+P 等价 ↓ / ↑；Tab / Shift+Tab 亦等价 ↓ / ↑；Ctrl+, 打开设置窗。
	// 用 goui.Ctrl（生产判 m.control，是 Ctrl 而非平台 super Cmd）。
	// ⚠️ Tab 在 mygo 里默认参与焦点遍历，能否抵达本视图 shortcut 待真机确认；抵达不了
	// 时 Ctrl+N/P 与 ↑/↓ 仍能完成同一导航（降级不影响主流程）。
	if c.Shortcut(goui.Ctrl, goui.KeyN) {
		in.OnMove(1)
	}
	if c.Shortcut(goui.Ctrl, goui.KeyP) {
		in.OnMove(-1)
	}
	if c.Shortcut(goui.Shift, goui.KeyTab) {
		in.OnMove(-1)
	} else if c.Shortcut(0, goui.KeyTab) {
		in.OnMove(1)
	}
	if c.Shortcut(goui.Ctrl, goui.KeyComma) {
		in.OnOpenSettings()
	}
	// Ctrl+Enter 按插件分派（剪贴板纯文本 / 终端管理员打开 / 行内动作快捷键）。
	// 注意它**不能**依赖输入框的 Submitted()：mygo 的 editor.wants 对 KeyEnter
	// 只在 mods==0 时返回 true（Shift 被剥掉），带 Ctrl 的 Enter 直接走快捷键路由，
	// 不会进入输入框。故这里必须挂窗口级 Shortcut。
	if c.Shortcut(goui.Ctrl, goui.KeyEnter) {
		in.OnCtrlEnter()
	}
	if c.Shortcut(goui.Ctrl|goui.Shift, goui.KeyT) {
		in.OnPinToggle()
	}
	// Delete 在输入框聚焦时由编辑器消费（wants 对 KeyDelete 恒真），所以主路径是
	// 下面的 OnTextKey；这两条窗口级路由只覆盖焦点不在输入框的帧，与 Ctrl+Backspace
	// 的双路处理同款。Ctrl+Delete = 删除/关闭当前项，Ctrl+Shift+Delete = 结束任务。
	if c.Shortcut(goui.Ctrl|goui.Shift, goui.KeyDelete) {
		in.OnEndTask()
	}
	if c.Shortcut(goui.Ctrl, goui.KeyDelete) {
		in.OnDeleteSelected()
	}

	goui.Column(c).Fill().Children(func() {
		// 搜索行：TextInput 绑定 in.Query，变化即过滤。
		//
		// 唤起懒加载的触发点就在这里（PoC 文档 §11.12）：只有查询**变为非空**
		// 才由 main 侧的 onQueryChanged 调 appsmodel.EnsureLoaded() 起后台枚举。
		// 唤起时 query 为空，一个字都不打，因此唤起既不枚举也不建索引。
		goui.Row(c).Padding(16, 18).Gap(12).AlignItems(goui.Center).Children(func() {
			// 规则⑤（§7.3.3）：**前缀不进输入框**；作用域栈以面包屑（图标+名称+›）呈现，
			// 由 main 侧把 engine 的作用域栈映射成 in.Crumbs 传入（本层只画）。
			//
			// 面包屑为空时**一个子元素都不加**：Global（=不在任何插件作用域）那条链路的
			// 输入行布局因此与接线前逐字相同（行高 56 是 row_visual_test 里
			// listAreaTop=57 的依据，动了它一堆像素断言立刻红）。
			if len(in.Crumbs) > 0 {
				// 单框紧凑面包屑：每层各取自己的图标；祖先层**只显示图标**，只有当前层
				// 再加文本（图标紧贴该层文本左侧）。实际两种形态：
				//   [插件列表图标] › [插件图标] 插件名        （经 # 列表进入）
				//   [插件图标] 插件名                          （前缀直达）
				goui.Row(c).Gap(5).AlignItems(goui.Center).
					Background(t.Surface).Radius(t.Radius).Padding(3, 8).Children(func() {
					for i, cr := range in.Crumbs {
						if i > 0 {
							goui.Text(c, "›").FontSize(theme.DetailSize).TextColor(t.TextMuted)
						}
						col := t.TextMuted
						if cr.Current {
							col = t.Text
						}
						goui.Icon(c, PluginIconSVG(cr.ProviderID)).Size(theme.DetailSize, theme.DetailSize).TextColor(col)
						if cr.Current {
							goui.Text(c, cr.Label).SingleLine().FontSize(theme.DetailSize).TextColor(col)
						}
					}
				})
			}
			input := goui.TextInputBase(c, in.Query).Bind(in.Input).Grow(1).FontSize(theme.InputSize).
				Placeholder(in.Placeholder).Label(in.Placeholder).
				OnTextKey(func(k goui.TextKey) bool {
					// Delete 系动作：编辑器对 KeyDelete 的 wants 恒为 true（带 Ctrl
					// 也一样），必须先在这里截住，否则会被当成「删除后一个词」吃掉。
					// Ctrl+Shift+Delete = 结束任务；Ctrl+Delete = 删除/关闭当前项。
					if k.Key == goui.KeyDelete {
						switch k.Mods {
						case goui.Ctrl | goui.Shift:
							if in.OnEndTask != nil {
								in.OnEndTask()
								return true
							}
						case goui.Ctrl:
							if in.OnDeleteSelected != nil {
								in.OnDeleteSelected()
								return true
							}
						}
					}
					// Ctrl+Backspace：一键重置（清查询 + 回 Global，不隐藏）。
					// 输入框聚焦时编辑器会把它当「删前一个词」（mygo keymap.go:305）
					// 先吃掉，所以必须在这里拦；焦点不在输入框时走上方的窗口级
					// c.Shortcut。必须排在「空输入 Backspace」之前，否则空输入时
					// 会被逐级回退分支先吃掉。
					if k.Key == goui.KeyBackspace && k.Mods == goui.Ctrl && in.OnResetInput != nil {
						in.OnResetInput()
						return true
					}
					// 只在「空输入 + Backspace」时接管做逐级回退；其余键（含非空
					// Backspace 的正常删字符）返回 false 交回编辑引擎，行为不变。
					// 单行输入默认 leaveEmptyBackspace=false ⇒ wants(Backspace) 恒真，
					// 故空输入的 Backspace 也会走到这里（editor_navigation.go:40）。
					if k.Key == goui.KeyBackspace && k.Text() == "" && in.OnBackspaceAtEmpty != nil {
						return in.OnBackspaceAtEmpty()
					}
					return false
				})
			if input.Changed() {
				in.OnQueryChanged()
			}
			if input.Submitted() {
				// 只有无修饰键（或仅 Shift）的 Enter 会被编辑器接收；Ctrl+Enter
				// 走上方的窗口级 Shortcut（见该处注释）。
				in.OnOpen()
			}
		})
		goui.Divider(c)
		// 结果区按 mode 三选一——判定在 main 侧（view 分支），本层只执行选中的渲染闭包。
		in.RenderResults(c)
		goui.Divider(c)
		// 底部提示行：左侧主热键，右侧全局/模式快捷键，键帽化 + 组间竖线。
		goui.Row(c).Padding(8, 16).Gap(16).AlignItems(goui.Center).Children(func() {
			goui.Row(c).Grow(1).AlignItems(goui.Center).Children(func() {
				FooterHotkey(c, in.Hotkey)
			})
			FooterHints(c, in.RightHint)
		})
	})
	// 每帧把键盘焦点按回搜索框（唤起懒加载之外，本轮单击改造的必要配套）。
	//
	// 为什么必须每帧做：单击行的可点击元素是 ui.Button，而 mygo 的按钮
	// **可获焦点**（base.go:25 coreButtonBase 里的 .Focusable()）。点击它
	// 会把焦点从搜索框抢到按钮上，此后 ↑↓ 就不再抵达本视图的
	// c.Shortcut(0, goui.KeyDown)——实测（见文档 §11.13）点击后应用层收到
	// 的 ↑↓ 次数为 0，键盘导航直接失灵。
	//
	// 为什么每帧重复调用是安全的：ui.Handle.Focus 走引擎的 focusOn
	// （access.go:562-572），其中 `if rt.focused != s.id` 才重置
	// focusVisible 与 blinkStart，焦点没变时是空操作；已实测焦点保持
	// 在搜索框、↑↓ 与输入均正常。
	//
	// 放在 Column 之后（而不是之前）：焦点变更要在本帧控件构建完后生效，
	// 放前面会被随后的列表构建覆盖掉。
	in.Input.Focus()
}
