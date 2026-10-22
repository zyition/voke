// picker 结果区渲染：「已启用插件」列表 + 单行（名称/描述副行）+ 未接入插件占位。
//
// 逐字搬自 package main/view.go 的 `pickerResults()` / `pickerRow()`，以及 `view()` 里
// 「未接入插件」那条 Column/Text 占位分支。原读 `st.pickerRows` / `st.pickerChosen` /
// `st.pickerList` 改成读入参、原调 `st.open()` / `st.pickRow(i)` 改成调回调。
package ui

import (
	goui "github.com/egoist/mygo/ui"

	"voke/internal/ui/theme"
)

// PluginRow 是 picker 模式列表的一行（id 用于 SelectPlugin，两行文本用于渲染）。
//
// 这是渲染层需要的一行数据的**形状**；main 侧的装配产物（providers.pluginRow，带 ID /
// Name / Description）在 view 分支里逐帧映射成它——映射只搬运三个字段，判定与顺序
// 一律沿用 main 侧的 rankPlugins / moveSelection / pickRow，本层不重排、不重判。
type PluginRow struct {
	ID          string
	Name        string
	Description string
}

// PickerResults 渲染 PluginPicker 模式的「已启用插件」列表（§7.3.3 规则 3：
// 插件列表只是前缀表里普通一条被命中后的样子，出什么列表由归一后的配置决定）。
//
// 数据源是 rows（rankPlugins 的产物），**不是** model.Results：apps 的
// 选中行 / 结果集 / 懒加载标志一个都不碰，于是「退出 picker 回到 Global」时
// apps 列表仍是它自己的那一套时序。
//
// chosen 是指向持有者 picker 选中字段的指针（原 `&st.pickerChosen`）：List 的 Selected
// 绑到它，键盘导航在 main 侧改这个字段即可实时反映到本帧高亮。
//
// 行视觉与 apps 行同款（药丸 + 间隙 + 单击即执行，§7.5.1 / 决策⑤），差别只在
// 没有图标：插件行没有磁盘图标可取（取图标要走 platform.AppIcon，背后是
// Shell.FileIcon，白交一次平台调用）。
func PickerResults(c *goui.Context, rows []PluginRow, chosen *int, ls *goui.ListState, hs *HoverSelect, onOpen func(), onPick func(i int), emptyText string) {
	t := c.Theme()
	ls.Selected = chosen
	// 同 apps.go：不设 Key，避免 mygo 的 key-follow 在过滤词换掉旧选中行时
	// 把 chosen 写成 -1。picker 的选中本来就随重建复位到第 0 行。
	ls.Label = func(i int) string { return rows[i].Name }
	hovered := -1
	list := goui.List(c, ls, len(rows), func(i int) { PickerRow(c, rows[i], i == *chosen, i, func() { onPick(i) }, &hovered) }).
		Grow(1).Padding(6).Children(func() {
		if len(rows) == 0 {
			goui.Text(c, emptyText).TextColor(t.TextMuted).Padding(16)
		}
	})
	applyHoverSelect(c, hs, hovered, chosen)
	if list.Submitted() {
		onOpen()
	}
}

// PickerRow 渲染 picker 的一行：名称 + 描述副行（描述里带上该插件的前缀，
// 让用户看得见「这个插件也能用 = / @b 那样的前缀直达」）。
//
// 三态配色与 Row() 一致（选中 Selection / hover SurfaceHover / 静止 Transparent），
// 外层同样铺 t.Background 遮住 mygo 行节点自带的整槽高亮（§11.5 遮罩理由）。
func PickerRow(c *goui.Context, plugin PluginRow, selected bool, i int, onClick func(), hovered *int) {
	t := c.Theme()
	bg := goui.Transparent
	if selected {
		bg = t.Selection.Over(t.Background)
	}
	btn := goui.ButtonBase(c).Height(theme.RowSlotH).Grow(1).AlignItems(goui.Center).
		Background(t.Background)
	if btn.Clicked() {
		onClick()
	}
	btn.Children(func() {
		isHover := btn.Hovered()
		if isHover && hovered != nil {
			*hovered = i
		}
		pill := bg
		if pill == goui.Transparent && isHover {
			pill = t.SurfaceHover
		}
		goui.Row(c).Grow(1).Height(float32(theme.RowSlotH - theme.RowGap)).MarginX(theme.RowInsetX).
			Radius(t.Radius).Background(pill).PaddingX(12).Gap(12).AlignItems(goui.Center).
			Children(func() {
				// 插件角标：Lucide 矢量图标（对齐 Rust badge::plugin_icon，picker 每行都取
				// 自己 id 的图标；apps/windows 在此也用 AppWindow/Layers，而非磁盘 PNG）。
				goui.Icon(c, PluginIconSVG(plugin.ID)).Size(theme.IconSize, theme.IconSize).TextColor(t.Text)
				goui.Column(c).Grow(1).Gap(1).Children(func() {
					goui.Text(c, plugin.Name).TextColor(t.Text).SingleLine().FontSize(theme.TitleSize)
					goui.Text(c, plugin.Description).FontSize(theme.DetailSize).TextColor(t.TextMuted).SingleLine()
				})
			})
	})
}

// PendingResults 渲染「表里有、但本轮没落地实现」的插件占位（§10.2 待决策 5：
// 只接 apps + picker）：明说未接入，绝不拿 apps 的结果冒充它的答案。
//
// 逐字搬自原 `view()` 里 pluginPending() 分支的 Column/Text：文案由 main 侧
// pendingHint() 现算后经 text 传入。
func PendingResults(c *goui.Context, text string) {
	t := c.Theme()
	goui.Column(c).Fill().Children(func() {
		goui.Text(c, text).TextColor(t.TextMuted).Padding(16)
	})
}
