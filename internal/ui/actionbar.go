// actionbar.go：结果行尾的通用操作图标条。
//
// 只负责渲染与回调，不认识剪贴板或任何具体插件：actions 由 provider 按
// 条目能力给出，app 收到 ID 后执行。图标不可获得 Tab 焦点（OnClick 只置
// clickable），键盘操作仍走窗口快捷键，避免每个 icon 都成为 Tab 站。
package ui

import (
	"sync"

	goui "github.com/egoist/mygo/ui"

	"voke/internal/providers"
	"voke/internal/ui/theme"
)

// ActionSlotWidth 返回 n 个操作图标的固定预留宽度。
func ActionSlotWidth(n int) float32 { return float32(n * theme.ActionSlot) }

// ActionBar 渲染行尾操作图标；maxActions 用于在所有行上预留同一宽度，
// 避免选中/悬浮时才出现图标导致标题重排。visible=false 时只占位。
func ActionBar(c *goui.Context, actions []providers.RowAction, maxActions int, visible bool, onAction func(id string)) {
	if maxActions <= 0 {
		return
	}
	width := ActionSlotWidth(maxActions)
	if !visible {
		goui.Box(c).Width(width).Shrink(0)
		return
	}
	t := c.Theme()
	goui.Row(c).Width(width).Shrink(0).Gap(0).AlignItems(goui.Center).Justify(goui.End).Children(func() {
		for _, a := range actions {
			a := a
			icon := a.Icon
			if icon == "" {
				icon = a.ID
			}
			e := goui.Icon(c, actionIconSVG(icon)).
				Size(theme.ActionIconSize, theme.ActionIconSize).
				Padding(4).Radius(4).TextColor(t.TextMuted).Label(a.Label)
			if e.Hovered() {
				e.TextColor(t.Text).Background(t.SurfaceHover)
			}
			tip := a.Tooltip
			if tip == "" {
				tip = a.Label
			}
			if tip != "" {
				e.Tooltip(tip)
			}
			e.OnClick(func() {
				if onAction != nil {
					onAction(a.ID)
				}
			})
		}
	})
}

var (
	actionSVGOnce sync.Once
	actionSVGs    map[string]*goui.SVG
)

func actionIconSVG(key string) *goui.SVG {
	actionSVGOnce.Do(func() {
		actionSVGs = map[string]*goui.SVG{
			"paste":        goui.MustParseSVG(lucideClipboardPaste),
			"paste-plain":  goui.MustParseSVG(lucideType),
			"pin":          goui.MustParseSVG(lucidePin),
			"unpin":        goui.MustParseSVG(lucidePinOff),
			"delete":       goui.MustParseSVG(lucideTrash2),
			"end-task":     goui.MustParseSVG(lucidePower),
			"close-window": goui.MustParseSVG(lucideX),
			"shield":       goui.MustParseSVG(lucideShield),
		}
	})
	if s, ok := actionSVGs[key]; ok {
		return s
	}
	if s, ok := actionSVGs["paste"]; ok {
		return s
	}
	return goui.MustParseSVG(lucideType)
}

var (
	lucideClipboardPaste = []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M15 2H9a1 1 0 0 0-1 1v2c0 .6.4 1 1 1h6c.6 0 1-.4 1-1V3c0-.6-.4-1-1-1Z"/><path d="M8 4H6a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V6a2 2 0 0 0-2-2h-2"/><path d="M16 4h2a2 2 0 0 1 2 2v2"/><path d="M8 4H6a2 2 0 0 0-2 2v2"/></svg>`)
	lucideType           = []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="4 7 4 4 20 4 20 7"/><line x1="9" x2="15" y1="20" y2="20"/><line x1="12" x2="12" y1="4" y2="20"/></svg>`)
	lucidePin            = []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 17v5"/><path d="M9 10.76a2 2 0 0 1-1.11 1.79l-1.78.9A2 2 0 0 0 5 15.24V16a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1v-.76a2 2 0 0 0-1.11-1.79l-1.78-.9A2 2 0 0 1 15 10.76V7a1 1 0 0 1 1-1 2 2 0 0 0 0-4H8a2 2 0 0 0 0 4 1 1 0 0 1 1 1z"/></svg>`)
	lucidePinOff         = []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 17v5"/><path d="M15 9.34V7a1 1 0 0 1 1-1 2 2 0 0 0 0-4H8.89"/><path d="M9 9v1.76a2 2 0 0 1-1.11 1.79l-1.78.9A2 2 0 0 0 5 15.24V16a1 1 0 0 0 1 1h11"/><path d="m2 2 20 20"/></svg>`)
	lucidePower          = []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 2v10"/><path d="M18.4 6.6a9 9 0 1 1-12.77.04"/></svg>`)
	lucideX              = []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M18 6 6 18"/><path d="m6 6 12 12"/></svg>`)
	lucideShield         = []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M20 13c0 5-3.5 7.5-7.66 8.95a1 1 0 0 1-.67-.01C7.5 20.5 4 18 4 13V6a1 1 0 0 1 1-1c2 0 4.5-1.2 6.24-2.72a1.17 1.17 0 0 1 1.52 0C14.51 3.81 17 5 19 5a1 1 0 0 1 1 1z"/></svg>`)
	lucideTrash2         = []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M3 6h18"/><path d="M19 6v14c0 1-1 2-2 2H7c-1 0-2-1-2-2V6"/><path d="M8 6V4c0-1 1-2 2-2h4c1 0 2 1 2 2v2"/><line x1="10" x2="10" y1="11" y2="17"/><line x1="14" x2="14" y1="11" y2="17"/></svg>`)
)
