// global_results.go：全局搜索的**合并分组列表**渲染（apps 组 + 其它 is_global 插件组，
// 一条可导航列表、组间以组名小标题分隔）。行视觉与 apps / provider 行同口径（选中 > hover
// 三态、图标懒取 + 首字母降级）。只画元素 + 发意图（onOpen/onClick），不判模式、不执行。
package ui

import (
	goui "github.com/egoist/mygo/ui"

	"github.com/zyition/voke/poc/voke-go/internal/ui/theme"
)

// GlobalRow 是一条合并行的渲染输入（main 侧从 globalRow + 各来源映射而来）。
type GlobalRow struct {
	Title      string
	Subtitle   string
	Icon       string // 图标来源路径（apps=启动路径、provider=其 Icon）；空=用插件角标矢量图标
	ProviderID string // 来源插件 id：无 PNG 时据此取 Lucide 角标（对齐 Rust badge::plugin_icon）
	GroupLabel string // 组标题（provider 显示名 / 「应用」）
	NewGroup   bool   // 是否组首（渲染标题行）
}

// GlobalResults 渲染合并列表；ls/chosen 驱动单列表键盘导航，onOpen=Enter，onClick(i)=单击。
func GlobalResults(c *goui.Context, rows []GlobalRow, chosen *int, ls *goui.ListState, icons *map[string]*goui.Bitmap, onOpen func(), onClick func(i int)) {
	ls.Selected = chosen
	ls.Key = func(i int) any { return i }
	ls.Label = func(i int) string { return rows[i].Title }
	list := goui.List(c, ls, len(rows), func(i int) { globalRow(c, rows[i], *chosen, icons, onClick, i) }).
		Grow(1).Padding(6)
	if list.Submitted() {
		onOpen()
	}
}

// globalRow 画一条合并行：可选组标题行 + 「图标 + 标题 + 副标题」药丸行。
func globalRow(c *goui.Context, r GlobalRow, chosen int, icons *map[string]*goui.Bitmap, onClick func(i int), i int) {
	t := c.Theme()
	btn := goui.ButtonBase(c).Grow(1).AlignItems(goui.Center).Background(t.Background)
	if btn.Clicked() {
		onClick(i)
	}
	btn.Children(func() {
		goui.Column(c).Grow(1).Children(func() {
			if r.NewGroup && r.GroupLabel != "" {
				goui.Text(c, r.GroupLabel).FontSize(theme.DetailSize).TextColor(t.TextMuted).
					MarginX(theme.RowInsetX).Padding(4, 2)
			}
			pill := goui.Transparent
			if i == chosen {
				pill = t.Selection.Over(t.Background)
			} else if btn.Hovered() {
				pill = t.SurfaceHover
			}
			goui.Row(c).Grow(1).Height(float32(theme.RowSlotH - theme.RowGap)).MarginX(theme.RowInsetX).
				Radius(t.Radius).Background(pill).PaddingX(12).Gap(12).AlignItems(goui.Center).
				Children(func() {
					var bmp *goui.Bitmap
					if r.Icon != "" {
						bmp = iconForPath(icons, r.Icon, r.Title)
					}
					if bmp != nil {
						goui.Image(c, bmp).Size(theme.IconSize, theme.IconSize)
					} else {
						// 无 PNG（provider 空 Icon / 取图失败）→ 插件角标矢量图标（对齐 Rust
						// badge::plugin_icon），不再用文本首字母色块。
						goui.Icon(c, PluginIconSVG(r.ProviderID)).Size(theme.IconSize, theme.IconSize).TextColor(t.Text)
					}
					goui.Column(c).Grow(1).Gap(1).Children(func() {
						goui.Text(c, r.Title).TextColor(t.Text).SingleLine().FontSize(theme.TitleSize)
						if r.Subtitle != "" {
							goui.Text(c, r.Subtitle).FontSize(theme.DetailSize).TextColor(t.TextMuted).SingleLine()
						}
					})
				})
		})
	})
}
