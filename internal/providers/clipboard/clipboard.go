// Package clipboard 是剪贴板历史插件（`@c` 前缀、Direct 激活）——把
// internal/logic/cliphist 的内存历史渲染成可搜索的结果行。
//
// 分层：纯逻辑（文案派生 + 搜索打分 + 出 Match），零 mygo、零 IO；真正的采集、
// 写回与按键模拟在 platform + app 层。历史快照由 app 注入闭包（每次查询现取）。
package clipboard

import (
	"fmt"
	"strings"
	"time"

	"voke/internal/i18n"
	"voke/internal/logic/cliphist"
	"voke/internal/providers"
)

const (
	ID = "clipboard"
	// prefix 是二级前缀 `@c`（与其它插件统一放在 `@` 命名空间下）。
	prefix = "@c"

	// maxTitleRunes 是行标题的最大字符数：单行渲染，过长会被截断得很难看。
	maxTitleRunes = 120
)

// Fetcher 返回当前历史快照（置顶在前、普通在后）。app 层保证主线程调用。
type Fetcher func() []cliphist.Item

// Provider 实现 providers.Provider。
type Provider struct {
	config providers.ProviderConfig
	fetch  Fetcher
	now    func() time.Time
}

var _ providers.Provider = (*Provider)(nil)

// New 构造 provider；fetch 为 nil 时恒空（测试 / 非 Windows 占位）。
func New(fetch Fetcher, now func() time.Time) *Provider {
	if fetch == nil {
		fetch = func() []cliphist.Item { return nil }
	}
	if now == nil {
		now = time.Now
	}
	return &Provider{
		config: providers.ProviderConfig{
			Enabled: true, IsGlobal: false, Weight: 1.0,
			Prefix: prefix, Activation: "Direct", Count: 5,
		},
		fetch: fetch,
		now:   now,
	}
}

// NewEmpty 返回空数据源实例。
func NewEmpty() *Provider { return New(nil, nil) }

func (p *Provider) ID() string { return ID }
func (p *Provider) Name() string {
	return i18n.L("剪贴板", "Clipboard")
}
func (p *Provider) Aliases() []string { return []string{"clip", "nbt", "jqb"} }
func (p *Provider) Description() string {
	return i18n.L("剪贴板历史（文本 / 富文本 / 图片）", "Clipboard history (text / rich text / images)")
}
func (p *Provider) Config() providers.ProviderConfig     { return p.config }
func (p *Provider) SetConfig(c providers.ProviderConfig) { p.config = c }

func (p *Provider) Placeholder(string) string {
	return i18n.L("搜索剪贴板历史（Enter 粘贴，Ctrl+Enter 纯文本）",
		"Search clipboard history (Enter pastes, Ctrl+Enter pastes as plain text)")
}

// RegeneratesOnQuery=false：同一查询重跑结果一致（历史变化由面板重开/操作后刷新）。
func (p *Provider) RegeneratesOnQuery() bool { return false }

// Query 按输入过滤历史。空输入返回全部（置顶在前、普通按最近使用），非空按
// 大小写不敏感的子串匹配打分并降序。历史条数本身有上限，不再截断。
func (p *Provider) Query(ctx providers.QueryCtx) []providers.Match {
	items := p.fetch()
	now := p.now()
	query := strings.TrimSpace(ctx.Query)

	out := make([]providers.Match, 0, len(items))
	for _, it := range items {
		score, ok := scoreItem(it, query)
		if !ok {
			continue
		}
		out = append(out, p.toMatch(it, now, score))
	}
	if query != "" {
		// 同分保持历史顺序（置顶/最近使用），与 Rank 的稳定排序叠加后仍然确定。
		for i := 1; i < len(out); i++ {
			for j := i; j > 0 && out[j].Score > out[j-1].Score; j-- {
				out[j], out[j-1] = out[j-1], out[j]
			}
		}
	}
	return out
}

// scoreItem 把所有可搜索字段拼成一个 haystack（文本 + 类型词），要求查询里的
// 每个空白分隔 token 都出现；命中开头给更高分。
func scoreItem(it cliphist.Item, query string) (float64, bool) {
	if query == "" {
		return 1.0, true
	}
	hay := strings.ToLower(strings.Join([]string{
		it.Text, kindLabel(it), "剪贴板", "clipboard",
	}, " "))
	for _, token := range strings.Fields(strings.ToLower(query)) {
		if !strings.Contains(hay, token) {
			return 0, false
		}
	}
	first := strings.Fields(strings.ToLower(query))[0]
	if strings.HasPrefix(strings.ToLower(it.Text), first) {
		return 0.95, true
	}
	return 0.75, true
}

func (p *Provider) toMatch(it cliphist.Item, now time.Time, score float64) providers.Match {
	m := providers.Match{
		ProviderID: ID,
		Title:      titleOf(it),
		Subtitle:   subtitleOf(it, now),
		Action:     providers.Action{Kind: providers.ActionPaste, Ref: it.ID},
		Score:      score,
		Interact:   providers.InteractSpec{Kind: providers.InteractExecute},
	}
	if len(it.ThumbPNG) > 0 {
		m.PreviewPNG = it.ThumbPNG
		m.PreviewKey = it.ID
	}
	m.Actions = actionsOf(it)
	return m
}

// actionsOf 按条目类型给行尾操作：图片没有「纯文本粘贴」，纯文本没有该按钮
// （再降级成同样的文本是噪音）。置顶态决定 pin / unpin 二选一。
func actionsOf(it cliphist.Item) []providers.RowAction {
	paste := providers.RowAction{
		ID: "paste", Label: i18n.L("粘贴", "Paste"), Shortcut: "Enter",
		Tooltip: i18n.L("粘贴 · Enter", "Paste · Enter"),
		Action:  providers.Action{Kind: providers.ActionPaste, Ref: it.ID},
	}
	out := []providers.RowAction{paste}
	if it.HasPlainAction() {
		out = append(out, providers.RowAction{
			ID: "paste-plain", Label: i18n.L("纯文本粘贴", "Paste as plain text"), Shortcut: "Ctrl+Enter",
			Tooltip: i18n.L("纯文本粘贴 · Ctrl+Enter", "Paste as plain text · Ctrl+Enter"),
			Action:  providers.Action{Kind: providers.ActionPastePlain, Ref: it.ID},
		})
	}
	if it.Pinned {
		out = append(out, providers.RowAction{
			ID: "unpin", Label: i18n.L("取消置顶", "Unpin"), Shortcut: "Ctrl+Shift+T",
			Tooltip: i18n.L("取消置顶 · Ctrl+Shift+T", "Unpin · Ctrl+Shift+T"),
			Action:  providers.Action{Kind: providers.ActionUnpin, Ref: it.ID},
		})
	} else {
		out = append(out, providers.RowAction{
			ID: "pin", Label: i18n.L("置顶", "Pin"), Shortcut: "Ctrl+Shift+T",
			Tooltip: i18n.L("置顶 · Ctrl+Shift+T", "Pin · Ctrl+Shift+T"),
			Action:  providers.Action{Kind: providers.ActionPin, Ref: it.ID},
		})
	}
	out = append(out, providers.RowAction{
		ID: "delete", Label: i18n.L("删除", "Delete"), Shortcut: "Ctrl+Delete",
		Tooltip: i18n.L("删除 · Ctrl+Delete", "Delete · Ctrl+Delete"),
		Action:  providers.Action{Kind: providers.ActionDelete, Ref: it.ID},
	})
	return out
}

// titleOf 派生单行标题：文本优先（压平空白并截断），图片用尺寸，富文本兜底。
func titleOf(it cliphist.Item) string {
	if s := compact(it.Text, maxTitleRunes); s != "" {
		return s
	}
	if it.HasImage() {
		if it.Width > 0 && it.Height > 0 {
			return fmt.Sprintf(i18n.L("图片 · %d×%d", "Image · %d×%d"), it.Width, it.Height)
		}
		return i18n.L("图片", "Image")
	}
	if it.HTML != "" {
		return i18n.L("HTML 内容", "HTML content")
	}
	if len(it.RTF) > 0 {
		return i18n.L("富文本内容", "Rich text")
	}
	return i18n.L("空内容", "Empty")
}

// compact 把多行/连续空白压成单个空格，并按 rune 截断（带省略号）。
func compact(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return ""
	}
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit-1]) + "…"
}

// subtitleOf = 置顶标记 + 类型 + 大小 + 相对时间。
func subtitleOf(it cliphist.Item, now time.Time) string {
	parts := []string{kindLabel(it), humanBytes(it.Bytes), relativeTime(it.LastUsedAt, now)}
	if it.Pinned {
		return i18n.L("已置顶 · ", "Pinned · ") + strings.Join(parts, " · ")
	}
	return strings.Join(parts, " · ")
}

func kindLabel(it cliphist.Item) string {
	switch {
	case it.HasImage() && !it.HasText():
		return i18n.L("图片", "Image")
	case it.HasImage():
		return i18n.L("图片+文本", "Image + text")
	case it.HasRich():
		return i18n.L("富文本", "Rich text")
	default:
		return i18n.L("文本", "Text")
	}
}

func humanBytes(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/(1024))
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
}

func relativeTime(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return i18n.L("刚刚", "just now")
	case d < time.Hour:
		return fmt.Sprintf(i18n.L("%d 分钟前", "%d min ago"), int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf(i18n.L("%d 小时前", "%d h ago"), int(d/time.Hour))
	default:
		return fmt.Sprintf(i18n.L("%d 天前", "%d d ago"), int(d/(24*time.Hour)))
	}
}
