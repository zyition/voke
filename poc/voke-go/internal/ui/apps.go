// apps 结果区渲染：虚拟化列表 + 单行（图标/标题/副行）+ 图标缓存 + 空态文案。
//
// 逐字搬自 package main/view.go 的 `appsResults()` / `row()` / `icon()` / `emptyHint()`。
// 原读 `st.model` / `st.icons` / `st.list` 的地方改成读入参（`m` / `icons` / `ls`）、
// 原调 `st.open()` / `st.clickRow(i)` 的地方改成调回调，绑定、行数、空态文案、
// list.Submitted → onOpen 全部逐字未动（所以 row_visual 的像素断言仍是这段代码的回归网）。
package ui

import (
	"log"
	"time"

	"github.com/zyition/voke/poc/voke-go/internal/logic/appsmodel"
	"github.com/zyition/voke/poc/voke-go/internal/logic/panelstate"
	"github.com/zyition/voke/poc/voke-go/internal/platform"
	"github.com/zyition/voke/poc/voke-go/internal/ui/theme"

	goui "github.com/egoist/mygo/ui"
)

// appsResults 渲染 apps 结果列表（Global / Plugin(apps) 走这里，即接线前那一段）。
//
// 接线只做了一件事：把这段**原样**搬进方法，由 view() 里的 mode 分支决定要不要
// 调它。绑定、行数、空态文案、list.Submitted → onOpen 全部逐字未动。
//
// 结果列表：虚拟化 List，只建可见行（ui/list.go:11-33）。
//
// 特效取舍（PoC 文档 §11.5，2026-10-16 用户逐条拍板）：
//   - E5 hover：**不做自实现**。mygo 的 ui.List 其实自带行 hover
//     （ui/list.go:599-602 给行节点挂 styleFn，命中且非选中时铺
//     t.SurfaceHover；驱动重绘的机制在 ui/input.go:154-179
//     setHover——指针一动就比对 hover 链、变了就 requestFrame，
//     且只对带 flagHover 的元素要帧）。所以列表行的高亮 hover
//     是白送的、无需指针轮询启发式；真正的成本在「键盘输入后
//     抑制 hover」那套状态（要记住最后一次输入是键还是指针），
//     收益不抵成本，本轮不做。
//   - E10/E11/E12 动画（滚动/行入场出场/颜色过渡）：**砍掉**。
//     硬阻塞是隐藏 ≠ 停帧：rt.next() 里只有
//     `case moving && rt.host.occluded(): rt.held = true`
//     （ui/runtime.go:585-586）会停帧，而 windowHost.occluded()
//     要求 surface 实现 platform.OccludableSurface
//     （ui/window.go:229-232）——全仓只有 darwin/surface.go:274
//     与 web/surface.go:582 实现 Occluded()，**Windows 没有**，
//     故 occluded() 在 Windows 上恒 false，held 分支永不成立：
//     只要 rt.animating，面板隐藏后照样 requestFrame 跑 CPU。
//     mygo 侧无配置项可关，除非 vendor 改帧循环。故本处刻意
//     不设 ListState.FollowAnimation（ui/list.go:65）、不开行
//     入场出场动画、不做颜色过渡。
//   - E4 元素阴影：只留给 tooltip 与设置窗分组头，不给列表行，
//     约束写在 Row() 的注释里。
func AppsResults(c *goui.Context, m *panelstate.Model, icons *IconCache, ls *goui.ListState, onOpen func(), onClick func(i int)) {
	t := c.Theme()
	ls.Selected = &m.Chosen
	ls.Key = func(i int) any { return m.Results[i].Path }
	ls.Label = func(i int) string { return m.Results[i].Name }
	list := goui.List(c, ls, len(m.Results), func(i int) { Row(c, m, icons, i, onClick) }).Grow(1).Padding(6).Children(func() {
		if len(m.Results) == 0 {
			goui.Text(c, EmptyHint(m)).TextColor(t.TextMuted).Padding(16)
		}
	})
	// 双击与 Enter 都收敛到同一条路（list.Submitted）。单击执行已改到
	// Row() 内部的 Button.Clicked()，见该处「单击即执行」的注释。
	if list.Submitted() {
		onOpen()
	}
}

// Row 渲染一行结果：图标（PNG 或首字母占位）+ 名称 + 路径副行。
//
// ## 边框圆角的来源与去除（2026-10-16 真机反馈第二轮，用户原话：
// 「没注意每个选项行的边框和圆角是什么时候加的，好丑……全框起来然后框
// 又连着太难看了」）
//
// 定因结论：**边框不是 mygo 的 List 默认行为，是我们自己调 `ui.Button`
// 引进来的**。`ui.Button` = `ButtonBase` + 主题外观（api_gen.go:744-753
// → widgets.go:135 coreButton → :140 button → :152 styleButton），而
// styleButton 无条件做三件事：
//
//	b.Padding(...).Gap(...).Radius(t.Radius)   // widgets.go:154 → 圆角 6
//	b.Background(base).TextColor(fg)          // widgets.go:167 → Surface 底
//	if border.A > 0 { b.Border(1, border) }   // widgets.go:168-170 → 1px 描边
//
// 描边色是 `t.Border`（浅色 #d9d9de / 深色 #3f3f46，theme.go:126/:151），
// 恒不透明（Alpha>0），所以**每一行**都被画上一圈 1px 灰框；圆角 6 又让
// 每行成了圆角矩形，行高 48 紧挨着 48 → 上下两行的框边贴在一起，正是
// 用户说的「框又连着」。已用 Tester 渲染实测验证：`ui.Button` 在 y=0 画出
// 一行 #d9d9de 的边框像素，同位置 `ui.ButtonBase` 全白（见文档 §11.5）。
//
// 注意 `ui.List` **本身**不画描边：行节点只在 hover 且非选中时铺
// `t.SurfaceHover`（list.go:599-602），选中时才由 `w.Selected(on)` 上一个
// 不透明 `t.Accent` 底（list.go:582 → table.go:561-570）。所以框的唯一来源
// 就是上面那个 `ui.Button`。
//
// **修法：换 `ui.ButtonBase`**（base.go:25-29：coreRow + Focusable +
// flagClickable|flagHover，**不套任何主题外观**），行的高亮自己画。
//
// ## 三态配色（只有这三态，没有第四种）
//
//	静止  Transparent  → 什么都不画，行与面板底色融为一体
//	hover SurfaceHover → 圆角药丸（主题色，浅色 #e9e9ec / 深色 #323236）
//	选中 Selection     → 圆角药丸，Over 到不透明（沿用 §11.5 E7 定案）
//
// 为什么不给 hover 行加「框+高亮」：用户说「hover 的行框起来+高亮」和
// 「hover 上去高亮就行」都算过关，但一圈描边在 8 行密排的列表里会重新
// 变成「一排小盒子」，正是他嫌弃的东西。**药丸（圆角底色）本身就是 Win11
// 设置项、开始菜单、CmdPal 条目的通用语言**，比描边克制。
//
// 为什么 hover 用主题的 `SurfaceHover` 而不是自造色：切浅/深色主题时自动
// 跟随，不必自己维护第二套配色——§11.5 已定的「文字色只用 t.Text /
// t.TextMuted」是同一个理由。
//
// ## 行间必须留白（用户点名的硬要求）
//
// 行槽（theme.RowSlotH=48）里药丸只占 40（RowSlotH - RowGap），上下各留 4，
// 左右各缩进 RowInsetX。于是相邻两行的药丸**永远不接触**，静止态更是完全
// 没有边界线——「一坨」的观感从根上消失。
//
// 为什么不把行高从 48 缩到 40 来腾间隙：48 与生产 voke 的 ROW_HEIGHT
// 对齐（search.rs:144），是列表行距的基准，改它会让 PoC 与生产在另一处
// 悄悄分叉。间隙从**槽内**出，不动行距。
//
// ## 为什么不用「浅色不拉满的分割线」
//
// 查过 mygo 的分割线能力：`ui.Divider` 是 `coreBox` 铺 `t.Border` 的 1 DIP
// 实心条（widgets.go:39-46），**没有任何 padding / 宽度 / 颜色参数**——
// 想要「内缩的浅线」只能自己拿 `ui.Box(c).Height(1).MarginX(...)` 拼一条，
// 等于绕开控件自绘。另一个能力 `Element.Dividers(width, color)`
// （element.go:1034-1041）会在 List 行间画线，但它是**横贯整条列表**的
// （paint.go:532-552，lo/hi 取列表左右内边界），正是用户说的「拉满」，且
// 行数一多，7 条横线会把列表切成格子、更吵。故本轮**不加分割线**，只靠
// 药丸 + 间隙建立层次。若真机觉得行间太空，再补一条内缩细线。
//
// ## 特效预算（§11.5 E4 / E13-E15，不得违反）
//
// 本路径**不开** ui.Shadow（元素阴影是 GPU 降采样 + 模糊 pass，
// internal/gpu/d3d11/d3d11.go:298/:317，8~140 行不值得）、**不用**
// Element.Blur / Element.Effect（每帧渲进 GPU 位图再叠一道）、**不用**
// 渐变。重写期若要加，先回看本段与 §11.5 的预算表。
//
// ## 单击即执行（2026-10-16 改造，本轮样式重做后仍成立）
//
// 整行包一层可点击元素、Clicked() 里直接启动。为什么不是「监听
// ListState.Changed()」或「在行节点上自己判点击」——三条路都实测过，
// 结论如下（原始探针见文档 §11.13，数据来自 mygo 自带 Tester 脚手架）：
//
//  1. 行节点**不暴露**自己的点击态。mygo 内部确实算出了它
//     （ui/list.go:573 `rowBuild{clicked: ..., double: ...}`），
//     但 rowBuild 与 Context.row 都是未导出字段，应用层读不到；
//     ListRow（list_builder.go:4-12）也只给 Selected/ListFocused。
//  2. ListState.Changed() 会被**键盘导航**污染。点击行会让 mygo 把焦点
//     抢给列表（value_input.go:325 f.owner.Focus()），此后 ↑↓ 由列表的
//     navigate() 消费并同样打 Changed ——实测点一次行再按 ↓，Changed 立刻
//     又响一次。若拿它当「单击」信号，键盘选中就会误启动。本文件末尾每帧
//     回抢焦点（in.Input.Focus）正是为了让 ↑↓ 不再进列表，但那只是**缓解**：
//     焦点抢夺发生在下一帧的事件分发里，仍不该把「选中变了」当成「用户点了」。
//  3. 行的可点击元素必须是命中点的**祖先**，不能是兄弟。mygo 的命中链从
//     最内层命中元素起、只沿 parent 向上走（input.go:141-146
//     `for s := h.st; s != nil; s = rt.states[s.parent]`），兄弟节点根本不在
//     链上。实测把可点击元素与行内容平级摆放，Clicked() 恒为 false；把内容
//     用 Children 塞进去，点击立刻正常。
//
// 本轮把 ui.Button 换成 ui.ButtonBase **不破坏以上任何一条**：coreButtonBase
// 与 coreButton 共用同一个函数体（base.go:25-29），flagClickable /
// flagHover / Focusable 全都保留，只是去掉了主题外观。命中链仍是「可点击
// 元素是行内容的祖先」，因为内容照样放在它的 Children 里。唯一新增的是
// 外层那层铺底色的 Row——它是 ButtonBase 的子元素，在命中链上位于按钮与
// 内容之间，不影响向上查找。
//
// 综上：ui.ButtonBase 包住整行内容是唯一可靠的官方姿势，选中高亮仍由
// 选中行驱动——单击里既设选中又执行，见 main 侧 clickRow（经 onClick 回调）。
func Row(c *goui.Context, m *panelstate.Model, icons *IconCache, i int, onClick func(i int)) {
	app := m.Results[i]
	t := c.Theme()
	// 三态配色：选中 > hover > 静止，见上方「三态配色」。
	bg := goui.Transparent
	text, muted := t.Text, t.TextMuted
	if i == m.Chosen {
		// 必须 Over(Background) 压成不透明：外层行节点自带一个不透明 Accent
		// 底（list.go:582 → table.go:561-570），而我们的药丸比行槽小一圈，
		// 只盖住中间那块，见下方「遮罩」注释。
		bg = t.Selection.Over(t.Background)
	}
	// 外层可点击元素：铺满整个行槽（Height(theme.RowSlotH)+Grow(1)），不带任何背景
	// 与圆角——它的职责只有「接收点击」与「遮住 mygo 行节点自带的高亮」。
	// Grow(1) 让它铺满整行宽度：行节点是 Shrink(0) 的盒（list.go:555），
	// 不 Grow 的话只占内容宽度，行右侧留白处点不动。
	btn := goui.ButtonBase(c).Height(theme.RowSlotH).Grow(1).AlignItems(goui.Center).
		Background(t.Background)
	if btn.Clicked() {
		onClick(i)
	}
	// 为什么外层要铺 t.Background 这层「遮罩」：mygo 的行节点在选中时会自己
	// 上不透明 Accent 底、在 hover 时铺 SurfaceHover，且铺满整个行槽
	//（list.go:582/:599-602）。我们的药丸内缩了、盖不满行槽，不遮的话选中行
	// 会在药丸四周露出一圈蓝色边、hover 行会露出一圈灰边——这是「框」的
	// 另一种更隐蔽的来源。这里铺不透明面板底色盖掉它；视觉上行槽本身仍是
	// 面板底色（root 铺的就是 t.Background，view.go:114），看不出多了一层。
	btn.Children(func() {
		// 药丸：行槽内缩 theme.RowInsetX / theme.RowGap，圆角用主题 Radius。
		// hover 也在这里自己判：ButtonBase 带 flagHover（base.go:27），且
		// mygo 的 setHover 会对带 flagHover 的元素要帧重绘（input.go:154-179），
		// 所以指针一动高亮就跟着亮，不需要指针轮询启发式。
		pill := bg
		if pill == goui.Transparent && btn.Hovered() {
			pill = t.SurfaceHover
		}
		goui.Row(c).Grow(1).Height(float32(theme.RowSlotH - theme.RowGap)).MarginX(theme.RowInsetX).
			Radius(t.Radius).Background(pill).PaddingX(12).Gap(12).AlignItems(goui.Center).
			Children(func() {
				if b := Icon(icons, app); b != nil {
					goui.Image(c, b).Size(theme.IconSize, theme.IconSize)
				} else {
					// platform.AppIcon 返回 nil（或解码失败）时降级为首字母占位，
					// 与官方 launcher 示例同款（examples/launcher/main.go:132）。
					goui.Avatar(c, app.Name, nil).Size(theme.IconSize, theme.IconSize)
				}
				goui.Column(c).Grow(1).Gap(1).Children(func() {
					goui.Text(c, app.Name).TextColor(text).SingleLine().FontSize(theme.TitleSize)
					goui.Text(c, app.Path).FontSize(theme.DetailSize).TextColor(muted).SingleLine()
				})
			})
	})
}

// Icon 返回 app 的图标位图，按 Path 缓存（官方 launcher 同款：
// 只取可见行的图标、按 path 保留，examples/launcher/main.go:175-189）。
// PNG 字节由 internal/platform 的 AppIcon 提供（背后是 mygo Shell.FileIcon，
// 已在平台接缝把 §7.1.2 O4-b 的「假成功」零长返回归一成 nil）；
// 返回 nil 或解码失败时缓存 nil，行渲染降级为首字母占位。
//
// 这里的 `len(data) > 0` 与上面那层归一是同一判据的两道保险，都不可省：
// FileIcon 走 onMainValue，只在主线程按「行首次可见」懒取（O4 的硬约束），
// 一旦把零长当图片交给 ui.DecodeBitmap 就是一帧解码失败日志。
//
// 缓存由 appState 装配（NewIconCache，256 条 LRU，隐藏 10s 后裁到 64 条）：
// 只主线程用，无锁。
func Icon(icons *IconCache, app appsmodel.AppEntry) *goui.Bitmap {
	return iconForPath(icons, app.Path, app.Name)
}

// iconForPath 是 apps 行与「带图标的 provider 行」（如窗口切换器按进程路径取图标）共用的
// 懒取 + 缓存核心（O4：FileIcon 走 onMainValue、只许主线程、行首次可见才取，禁批量预取）。
// 命中缓存即返（含负缓存 nil）；AppIcon 零长 / 解码失败 → 缓存并返回 nil，调用方据此降级。
func iconForPath(icons *IconCache, path, label string) *goui.Bitmap {
	if b, ok := icons.Get(path); ok {
		return b
	}
	var b *goui.Bitmap
	if data := platform.AppIcon(path); len(data) > 0 {
		decoded, err := goui.DecodeBitmap(data)
		if err != nil {
			log.Printf("解码 %s 的图标失败：%v", label, err)
		} else {
			b = decoded
		}
	}
	icons.Put(path, b)
	return b
}

// EmptyHint 是 apps 结果为空时的占位文案（四态），逐字委托
// panelstate.Model.EmptyHint；时间基准在此取 time.Now()（对应原实现里的
// time.Since(st.loadAt)）。picker / 未接入插件各有自己的文案（由 main 侧的
// pickerHint / pendingHint 提供），不走这条 apps 语义。
func EmptyHint(m *panelstate.Model) string {
	return m.EmptyHint(time.Now())
}
