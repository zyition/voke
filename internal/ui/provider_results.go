// provider_results.go：providers 插件（base64/uuid/…）的结果区渲染——通用行列表，
// 读 []providers.RankedMatch + 提示行，画「标题 + 副标题」药丸行（选中/hover 三态
// 与 apps 行同款视觉，只是没有图标），点击/Enter 经回调交回 main 侧执行 Intent。
//
// §7.2.1：ui 层只画元素 + 发意图（onOpen/onClick 回调），不判模式、不执行副作用。
// 本函数**新增、独立于 apps 路径**（AppsResults 一字未改），所以接入它不会动到
// 已被真机验证的 Global/apps 渲染。
package ui

import (
	"log"

	"voke/internal/providers"
	"voke/internal/ui/theme"

	goui "github.com/egoist/mygo/ui"
)

// ProviderRow 是一条可执行结果（供渲染，携带原始 Match）。
type ProviderRow struct {
	Title    string
	Subtitle string
	// IsHint 为真时渲染为不可选提示行（其实提示行走 ProviderResults 的空态分支，
	// 这里保留字段以备「结果与提示混排」的未来 provider，见下 Note）。
	IsHint bool
}

// Note（已知取舍，记决策点）：当前 base64/uuid 的 convert 要么只出结果行、要么只出
// 提示行，不混排，故提示行复用「列表空态」区展示。若将来某 provider 同时产出结果与
// 提示（如 awake 的说明书 + 时长候选），需把 hints 作为独立非选中区渲染在列表下方。

// ProviderResults 渲染插件结果列表 + 空态/提示文案。
//   - ls/chosen：选择态由调用方持有（每插件一套），ListState 驱动键盘导航；
//   - onOpen：Enter / list.Submitted；onClick(i)：单击第 i 行（选中+执行都交给回调）；
//   - hintText：无结果时展示的文案（提示行或占位说明）；
//   - onAction：行尾操作图标被点击（i = 行号，id = 动作 id）；actions 由 provider
//     按条目类型给出，UI 不判断「图片没有纯文本粘贴」这类语义；
//   - icons：与 apps 行共用的进程内图标缓存（按 Match.Icon 里的路径懒取；纯计算型 provider
//     的 Match.Icon 为空串 → 该行不画图标，布局与原「标题+副标题」逐字不变）；
//   - hs：hover 即选中的差分状态（调用方每列表一份）。
func ProviderResults(c *goui.Context, rows []providers.RankedMatch, chosen *int, ls *goui.ListState, hs *HoverSelect, icons *IconCache, onOpen func(), onClick func(i int), onAction func(i int, id string), hintText string) {
	t := c.Theme()
	ls.Selected = chosen
	ls.Key = func(i int) any { return i }
	ls.Label = func(i int) string { return rows[i].Title }
	maxActions := 0
	for _, r := range rows {
		if n := len(r.Actions); n > maxActions {
			maxActions = n
		}
	}
	hovered := -1
	list := goui.List(c, ls, len(rows), func(i int) {
		providerRow(c, rows[i], *chosen, maxActions, icons, onClick, onAction, i, &hovered)
	}).Grow(1).Padding(6).Children(func() {
		if len(rows) == 0 && hintText != "" {
			goui.Text(c, hintText).TextColor(t.TextMuted).Padding(16)
		}
	})
	applyHoverSelect(c, hs, hovered, chosen)
	if list.Submitted() {
		onOpen()
	}
}

// providerRow 画一行：可选图标（Match.Icon 非空时）+ 标题 + 副标题。三态配色与 hover
// 逻辑与 apps Row 同口径。Match.Icon 为空串（base64/uuid 等纯计算 provider）→ 不画图标，
// 行布局与改造前逐字一致。
func providerRow(c *goui.Context, m providers.RankedMatch, chosen, maxActions int, icons *IconCache, onClick func(i int), onAction func(i int, id string), i int, hovered *int) {
	t := c.Theme()
	bg := goui.Transparent
	if i == chosen {
		bg = t.Selection.Over(t.Background)
	}
	btn := goui.ButtonBase(c).Height(theme.RowSlotH).Grow(1).AlignItems(goui.Center).Background(t.Background)
	if btn.Clicked() {
		onClick(i)
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
				var bmp *goui.Bitmap
				if len(m.PreviewPNG) > 0 {
					bmp = previewFor(icons, m)
				}
				if bmp == nil && m.Icon != "" {
					bmp = iconForPath(icons, m.Icon, m.Title)
				}
				if bmp != nil {
					goui.Image(c, bmp).Size(theme.IconSize, theme.IconSize)
				} else {
					// 无 PNG（含纯计算型 provider 的空 Icon 与取图失败）→ 插件角标矢量图标，
					// 对齐 Rust badge::plugin_icon；不再用文本首字母色块，也不再留空槽。
					goui.Icon(c, PluginIconSVG(m.ProviderID)).Size(theme.IconSize, theme.IconSize).TextColor(t.Text)
				}
				goui.Column(c).Grow(1).Gap(1).Children(func() {
					goui.Text(c, m.Title).TextColor(t.Text).SingleLine().FontSize(theme.TitleSize)
					if m.Subtitle != "" {
						goui.Text(c, m.Subtitle).FontSize(theme.DetailSize).TextColor(t.TextMuted).SingleLine()
					}
				})
				// 只跟随选中行：快捷键作用于选中行，hover 行会先变成选中行
				// （HoverSelect），两者不再各亮一套图标。
				ActionBar(c, m.Actions, maxActions, i == chosen, func(id string) {
					if onAction != nil {
						onAction(i, id)
					}
				})
			})
	})
}

// previewFor 解码内存缩略图，并复用有界图标缓存；缓存键由 provider 提供
// （剪贴板 item id），避免每帧重复解码。
func previewFor(icons *IconCache, m providers.RankedMatch) *goui.Bitmap {
	if icons == nil || len(m.PreviewPNG) == 0 || m.PreviewKey == "" {
		return nil
	}
	if b, ok := icons.Get(m.PreviewKey); ok {
		return b
	}
	b, err := goui.DecodeBitmap(m.PreviewPNG)
	if err != nil {
		log.Printf("解码结果行缩略图失败：%v", err)
		icons.Put(m.PreviewKey, nil)
		return nil
	}
	icons.Put(m.PreviewKey, b)
	return b
}
