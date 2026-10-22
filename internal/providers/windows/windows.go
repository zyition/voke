// Package windows 搜索 / 切换已打开窗口——1:1 对照生产 `voke-core/src/windows.rs`
// （§5 Window Walker，对齐 PowerToys Run 的 Window Walker）。
//
// 分层：纯逻辑（枚举结果打分 + 出 Match），零 mygo、零 IO。窗口枚举是 OS 调用 +
// 副作用，由 app 层注入闭包（每次查询现调、不缓存：窗口状态每秒都在变），执行
// ActivateWindow 在 app 层 dispatcher。打分复用 internal/match（nucleo 1:1 移植 +
// 拼音键），与 apps 同一套匹配引擎；条目每次重建，搜索键走 SearchKeysCache 记忆化。
// 权重 1.0 < apps 1.2：同名时优先启动应用而不是切窗口。
package windows

import (
	"sort"
	"strings"

	"voke/internal/match"
	"voke/internal/providers"
)

const (
	ID = "windows"

	// maxResults 单次非空查询返回上限（同 apps：只渲染前几十条）。空查询不限流。
	maxResults = 50

	// EndTaskActionID 是「结束任务」行内操作的稳定 id（UI 图标键、app 侧查找共用）。
	EndTaskActionID = "end-task"
	// CloseWindowActionID 是「关闭窗口」行内操作的稳定 id（WM_CLOSE，等价点关闭按钮）。
	CloseWindowActionID = "close-window"
)

// Entry 是从系统层拿到的一条窗口。字段与 Rust `WindowEntry` 逐字对齐（故意不共享
// 类型：core 不依赖 sys）；app 层转换是一行结构体字面量。
type Entry struct {
	Hwnd        int64 // 句柄数值
	Title       string
	ProcessName string
	ProcessPath string
	// Icon 是图标 PNG 落盘路径，枚举期为空、app 层提取后写回（空 = 无图标）。
	Icon string
}

// Fetcher 是窗口数据源：每次调用都应返回**当前**窗口列表（app 层保证后台线程）。
type Fetcher func() []Entry

// indexedWindow 是带缓存搜索键的窗口（title / process_name 各一份）。
type indexedWindow struct {
	entry       Entry
	titleKeys   *match.SearchKeys
	processKeys *match.SearchKeys
}

// Provider 实现 providers.Provider。
type Provider struct {
	config providers.ProviderConfig
	fetch  Fetcher
	keys   *match.SearchKeysCache
}

var _ providers.Provider = (*Provider)(nil)

// New 用数据源构造（生产注入真实枚举闭包；测试注入假闭包返回造好的数据）。
func New(fetch Fetcher) *Provider {
	if fetch == nil {
		fetch = func() []Entry { return nil }
	}
	return &Provider{
		config: providers.ProviderConfig{
			Enabled: true, IsGlobal: true, Weight: 1.0,
			Prefix: "", Activation: "Direct", Count: 5,
		},
		fetch: fetch,
		keys:  match.NewSearchKeysCache(),
	}
}

// NewEmpty 返回空数据源实例（非 Windows 平台 / 未枚举占位）。
func NewEmpty() *Provider { return New(func() []Entry { return nil }) }

func (p *Provider) ID() string                           { return ID }
func (p *Provider) Name() string                         { return "窗口" }
func (p *Provider) Aliases() []string                    { return []string{"win", "ck"} }
func (p *Provider) Description() string                  { return "切换到已打开的窗口" }
func (p *Provider) Config() providers.ProviderConfig     { return p.config }
func (p *Provider) SetConfig(c providers.ProviderConfig) { p.config = c }

func (p *Provider) Placeholder(string) string { return "输入窗口标题或进程名" }

// Query 每次重新枚举；空查询全返回（score=1.0，交给引擎按频率重排、不截断），
// 非空按 title/process_name 取最高分排序、截断 maxResults。
// RegeneratesOnQuery：窗口每次查询现枚举（调用方在后台线程重取），结果跟随实际
// 窗口状态而非「重跑即变」的随机 → false（Rust 未 override）。
func (p *Provider) RegeneratesOnQuery() bool { return false }

func (p *Provider) Query(ctx providers.QueryCtx) []providers.Match {
	windows := p.indexAll()

	query := strings.TrimSpace(ctx.Query)
	if query == "" {
		out := make([]providers.Match, 0, len(windows))
		for i := range windows {
			out = append(out, p.toMatch(windows[i], 1.0))
		}
		return out
	}

	m := match.NewMatcher()
	out := make([]providers.Match, 0, len(windows))
	for i := range windows {
		if s, ok := p.score(m, query, windows[i]); ok {
			out = append(out, p.toMatch(windows[i], float64(s)))
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Score > out[b].Score })
	if len(out) > maxResults {
		out = out[:maxResults]
	}
	return out
}

// indexAll：枚举 + 入口拦掉空标题 / 空句柄 + 建（缓存的）搜索键。
func (p *Provider) indexAll() []indexedWindow {
	entries := p.fetch()
	out := make([]indexedWindow, 0, len(entries))
	for _, e := range entries {
		if strings.TrimSpace(e.Title) == "" || e.Hwnd == 0 {
			continue
		}
		out = append(out, indexedWindow{
			entry:       e,
			titleKeys:   p.keys.Get(e.Title),
			processKeys: p.keys.Get(e.ProcessName),
		})
	}
	return out
}

// score：title 与 process_name 分开建键、各打分取 max（拼接会让拼音串互相污染）。
func (p *Provider) score(m *match.Matcher, query string, w indexedWindow) (float32, bool) {
	title, okT := match.ScoreEntry(m, query, w.titleKeys)
	process, okP := match.ScoreEntry(m, query, w.processKeys)
	switch {
	case okT && okP:
		return maxF32(title.Score, process.Score), true
	case okT:
		return title.Score, true
	case okP:
		return process.Score, true
	default:
		return 0, false
	}
}

// toMatch：窗口行 = 主标题 title、副标题 process_name、动作 ActivateWindow{hwnd}。
func (p *Provider) toMatch(w indexedWindow, score float64) providers.Match {
	return providers.Match{
		ProviderID: ID,
		Title:      w.entry.Title,
		Subtitle:   w.entry.ProcessName,
		// Icon 携「图标来源路径」= 进程 exe 路径（O4：Go 侧不落盘 PNG，渲染层按此路径经
		// platform.AppIcon→Shell.FileIcon 懒取位图、进程内缓存）。受保护进程取不到路径时
		// ProcessPath 为空串 → Icon 空 → 该行不画图标（与纯计算 provider 行同款降级）。
		Icon:     w.entry.ProcessPath,
		Action:   providers.Action{Kind: providers.ActionActivateWindow, Hwnd: uintptr(w.entry.Hwnd)},
		Score:    score,
		Interact: providers.InteractSpec{Kind: providers.InteractExecute},
		Actions: []providers.RowAction{
			// 关闭窗口放在左侧、结束任务保持最右：老用户对电源图标的肌肉记忆不变。
			{
				ID: CloseWindowActionID, Label: "关闭窗口", Shortcut: "Ctrl+Delete",
				Tooltip: "关闭窗口 · Ctrl+Delete",
				Action:  providers.Action{Kind: providers.ActionCloseWindow, Hwnd: uintptr(w.entry.Hwnd)},
			},
			{
				ID: EndTaskActionID, Label: "结束任务", Shortcut: "Ctrl+Shift+Delete",
				Tooltip: "结束任务 · Ctrl+Shift+Delete",
				Action:  providers.Action{Kind: providers.ActionEndTask, Hwnd: uintptr(w.entry.Hwnd)},
			},
		},
	}
}

func maxF32(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}
