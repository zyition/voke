// footer.go：启动器底部提示行的键帽渲染。
//
// 底栏只展示全局通用键（↑↓ / Enter / Ctrl+Backspace / Esc）与当前模式的
// Enter 语义；行级操作交给 action icon 与 tooltip，避免底栏堆满组合键。
// 键帽里的单键优先用 Lucide 图标（Enter / 退格 / 空格）：图标宽度稳定、
// 不依赖字体回退；Ctrl / Esc / 箭头没有共识图标，仍用文字。
package ui

import (
	"strings"
	"sync"

	goui "github.com/egoist/mygo/ui"

	"voke/internal/ui/theme"
)

// FooterHint 是底部提示行的一组「按键 + 动作说明」。Keys 为空时只渲染
// Label（一次性 notice）；Keys 用规范串（如 "Ctrl+Backspace"），渲染时
// 按 "+" 拆成键帽内的多个 token。
type FooterHint struct {
	Keys  string
	Label string
}

// FooterHotkey 渲染底栏左侧的主热键（键帽 + 显示/隐藏说明）。
func FooterHotkey(c *goui.Context, hotkey string) {
	footerGroup(c, FooterHint{Keys: hotkey, Label: "显示/隐藏"})
}

// FooterHints 渲染底栏右侧的提示组；组间 1 DIP 竖线分隔。
func FooterHints(c *goui.Context, hints []FooterHint) {
	if len(hints) == 0 {
		return
	}
	t := c.Theme()
	goui.Row(c).Gap(10).AlignItems(goui.Center).Children(func() {
		for i, h := range hints {
			if i > 0 {
				goui.Box(c).Width(1).Height(12).Shrink(0).
					Background(t.TextMuted.Alpha(0.4))
			}
			footerGroup(c, h)
		}
	})
}

// footerGroup 画「键帽 + 说明」；Keys 为空时退化成纯文案（notice）。
func footerGroup(c *goui.Context, h FooterHint) {
	t := c.Theme()
	goui.Row(c).Gap(6).AlignItems(goui.Center).Children(func() {
		if h.Keys != "" {
			// 组合键拆成独立键帽（[Ctrl] [⌫]），比一顶大键帽里塞 "+" 更接近
			// 键盘的视觉，也让文字键与图标键的宽度/高度自然对齐。
			goui.Row(c).Gap(3).AlignItems(goui.Center).Children(func() {
				for _, tok := range strings.Split(h.Keys, "+") {
					footerKeycap(c, t, tok)
				}
			})
		}
		if h.Label != "" {
			goui.Text(c, h.Label).FontSize(theme.FooterSize).TextColor(t.TextMuted)
		}
	})
}

// footerKeycap 画单个键帽：圆角浅底 + 一个按键 token。固定高 17
// （footer 上下 padding 16 + 17 = 33，保持既有底栏高度、8 行可见不变），
// 只有图标（Enter）与含文字的键帽高度一致。
func footerKeycap(c *goui.Context, t *goui.Theme, tok string) {
	goui.Row(c).Height(17).AlignItems(goui.Center).Justify(goui.Center).
		Padding(0, 5).Radius(4).Background(t.SurfaceHover).Children(func() {
		footerKeyToken(c, t, tok)
	})
}

// footerKeyToken 画键帽里的一个键：Enter / Backspace / Space 用 Lucide，
// 其余（Ctrl、Esc、↑↓、F5 等）用文字。
func footerKeyToken(c *goui.Context, t *goui.Theme, tok string) {
	switch strings.ToLower(strings.TrimSpace(tok)) {
	case "enter":
		goui.Icon(c, footerIconSVG("enter")).Size(14, 14).TextColor(t.Text)
	case "backspace":
		goui.Icon(c, footerIconSVG("backspace")).Size(14, 14).TextColor(t.Text)
	case "space":
		goui.Icon(c, footerIconSVG("space")).Size(14, 14).TextColor(t.Text)
	default:
		goui.Text(c, tok).FontSize(theme.DetailSize).TextColor(t.Text)
	}
}

var (
	footerSVGOnce sync.Once
	footerSVGs    map[string]*goui.SVG
)

func footerIconSVG(key string) *goui.SVG {
	footerSVGOnce.Do(func() {
		footerSVGs = map[string]*goui.SVG{
			"enter":     goui.MustParseSVG(footerLucideEnter),
			"backspace": goui.MustParseSVG(footerLucideBackspace),
			"space":     goui.MustParseSVG(footerLucideSpace),
		}
	})
	return footerSVGs[key]
}

// Lucide v0.544.0（ISC）：corner-down-left / delete / space。
var (
	footerLucideEnter     = []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M20 4v7a4 4 0 0 1-4 4H4"/><path d="m9 10-5 5 5 5"/></svg>`)
	footerLucideBackspace = []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M10 5a2 2 0 0 0-1.344.519l-6.328 5.74a1 1 0 0 0 0 1.481l6.328 5.741A2 2 0 0 0 10 19h10a2 2 0 0 0 2-2V7a2 2 0 0 0-2-2z"/><path d="m12 9 6 6"/><path d="m18 9-6 6"/></svg>`)
	footerLucideSpace     = []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M22 17v1c0 .5-.5 1-1 1H3c-.5 0-1-.5-1-1v-1"/></svg>`)
)
