// Package awake 保持系统唤醒（可选连屏幕一起）——1:1 对照生产 `voke-core/src/awake.rs`。
//
// 分层：纯逻辑（候选时长 + 状态展示 + 出 Match），零 mygo、零 IO，Linux 原生可测。
// 真正的唤醒状态在 sys 线程，core 拿不到——注入一个状态读取闭包（fetch，每次查询现读
// 不缓存：到期 / 取消都会改它）与时钟闭包，与 timestamp 的时钟注入同款。执行
// SetAwake/CancelAwake 副作用在 app 层 dispatcher。两级子模式（system/screen）复用
// provider 路由范式；一级带纯数字快捷路径直接出一条时长。
package awake

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"voke/internal/i18n"
	"voke/internal/providers"
)

// presetMinutes 预置时长（分钟）——决策 6 的 30/60/120/480。
var presetMinutes = []int{30, 60, 120, 480}

// maxCustomMinutes 单次自定义时长上限（24 小时）。
const maxCustomMinutes = 24 * 60

const (
	ID = "awake"

	subModeSystem = "system"
	subModeScreen = "screen"
)

// Status 是保持唤醒的当前状态（由 sys 层提供）。ExpiresAt=nil 表示永久。
type Status struct {
	ExpiresAt  *int64 // 到期时刻（unix 秒）；nil = 永久
	KeepScreen bool
}

// remainingSecs 剩余秒数；永久返回 nil；已到期返回 0。
func (s Status) remainingSecs(now int64) *int64 {
	if s.ExpiresAt == nil {
		return nil
	}
	r := *s.ExpiresAt - now
	if r < 0 {
		r = 0
	}
	return &r
}

// remainingLabel 人类可读的剩余时间。
func (s Status) remainingLabel(now int64) string {
	secs := s.remainingSecs(now)
	if secs == nil {
		return i18n.L("永久", "Forever")
	}
	v := *secs
	switch {
	case v <= 0:
		return i18n.L("即将结束", "Ending")
	case v < 60:
		return fmt.Sprintf(i18n.L("还剩 %d 秒", "%ds left"), v)
	case v < 3600:
		return fmt.Sprintf(i18n.L("还剩 %d 分钟", "%d min left"), v/60)
	default:
		h, m := v/3600, (v%3600)/60
		if m == 0 {
			return fmt.Sprintf(i18n.L("还剩 %d 小时", "%dh left"), h)
		}
		return fmt.Sprintf(i18n.L("还剩 %d 小时 %d 分钟", "%dh %dm left"), h, m)
	}
}

// Fetcher 是状态读取闭包（app 层注入；每次查询现读）。
type Fetcher func() *Status

// Provider 实现 providers.Provider。
type Provider struct {
	config providers.ProviderConfig
	fetch  Fetcher
	now    func() int64 // 当前 unix 秒
}

var _ providers.Provider = (*Provider)(nil)

// New 返回默认配置的 awake provider（PoC：无 sys 状态源 ⇒ 恒未激活 + 系统时钟）。
// 生产 / 测试用 NewWithSource 注入真实状态与时钟。
func New() *Provider {
	return NewWithSource(func() *Status { return nil }, func() int64 { return time.Now().Unix() })
}

// NewWithSource 注入状态源与时钟（测试用固定时钟 / 固定状态）。
func NewWithSource(fetch Fetcher, clock func() int64) *Provider {
	return &Provider{
		config: providers.ProviderConfig{
			Enabled: true,
			// 默认 `@a` 直接进入；用户配置可覆盖。
			IsGlobal: false, Weight: 1.0, Prefix: "@a", Activation: "Direct", Count: 5,
		},
		fetch: fetch,
		now:   clock,
	}
}

func (p *Provider) ID() string { return ID }
func (p *Provider) Name() string {
	return i18n.L("保持唤醒", "Keep Awake")
}
func (p *Provider) Aliases() []string { return []string{"awake", "a"} }
func (p *Provider) Description() string {
	return i18n.L("阻止系统休眠（可选连屏幕一起）", "Prevent system sleep (optionally keep the screen on)")
}
func (p *Provider) Config() providers.ProviderConfig     { return p.config }
func (p *Provider) SetConfig(c providers.ProviderConfig) { p.config = c }

// Placeholder 对照 Rust：一级先选亮屏方式或直接输分钟数；二级只输分钟数。
func (p *Provider) Placeholder(subMode string) string {
	switch subMode {
	case subModeSystem:
		return i18n.L("输入分钟数（1–1440），或选上方档位", "Enter minutes (1–1440) or pick a preset above")
	case subModeScreen:
		return i18n.L("输入分钟数（1–1440），或选上方档位（连屏幕一起保持）",
			"Enter minutes (1–1440) or pick a preset above (keeps the screen on)")
	default:
		return i18n.L("选择亮屏方式，或输入分钟数快捷设置", "Choose whether to keep the screen on, or enter minutes")
	}
}

// Query 对照 Rust：sub_mode 决定级别；每次查询现读状态。
// RegeneratesOnQuery：awake 候选是静态时长档位 → false（Rust 未 override，取默认）。
func (p *Provider) RegeneratesOnQuery() bool { return false }

func (p *Provider) Query(ctx providers.QueryCtx) []providers.Match {
	now := p.now()
	status := p.fetch()
	q := strings.TrimSpace(ctx.Query)
	switch ctx.SubMode {
	case subModeSystem, subModeScreen:
		return p.queryLevel2(q, ctx.SubMode)
	default:
		return p.queryLevel1(status, now, q)
	}
}

// optionMatch 构造一条时长候选（Execute + SetAwake）。minutes=nil 即永久。
func (p *Provider) optionMatch(minutes *int, keepScreen bool, score float64, note string) providers.Match {
	var label string
	if minutes == nil {
		label = i18n.L("永久", "Forever")
	} else if m := *minutes; m%60 == 0 && m >= 60 {
		label = fmt.Sprintf(i18n.L("%d 小时", "%d h"), m/60)
	} else {
		label = fmt.Sprintf(i18n.L("%d 分钟", "%d min"), *minutes)
	}
	scope := scopeLabel(keepScreen)
	subtitle := scope
	if note != "" {
		subtitle = fmt.Sprintf("%s · %s", scope, note)
	}
	return providers.MatchExecute(ID, label, subtitle,
		providers.Action{Kind: providers.ActionSetAwake, Minutes: minutes, KeepScreen: keepScreen}, score)
}

// queryLevel1：已激活首条取消项 → 空输入两方向 → 纯数字快捷一条 → 越界 / 非数字提示。
func (p *Provider) queryLevel1(status *Status, now int64, query string) []providers.Match {
	var out []providers.Match

	var activeScreen *bool
	if status != nil {
		scope := i18n.L("屏幕关闭", "Screen may sleep")
		if status.KeepScreen {
			scope = i18n.L("屏幕打开", "Screen stays on")
		}
		out = append(out, providers.MatchExecute(ID,
			fmt.Sprintf(i18n.L("保持唤醒中 · %s（%s）", "Keeping awake · %s (%s)"), scope, status.remainingLabel(now)),
			i18n.L("选中取消，恢复正常休眠", "Select to cancel and restore normal sleep"),
			providers.Action{Kind: providers.ActionCancelAwake}, 1.0))
		s := status.KeepScreen
		activeScreen = &s
	}

	if query == "" {
		return p.pushDirections(out, activeScreen)
	}

	if minutes, err := strconv.Atoi(query); err == nil {
		if minutes >= 1 && minutes <= maxCustomMinutes {
			return []providers.Match{p.optionMatch(&minutes, false, 1.0,
				i18n.L("连屏幕请进入「连屏幕一起保持」", "To keep the screen on, choose “Keep screen on”"))}
		}
		return []providers.Match{hintRow(i18n.L("时长超出范围", "Duration out of range"),
			fmt.Sprintf(i18n.L("请输入 1–%d 分钟", "Enter 1–%d minutes"), maxCustomMinutes), 1.0)}
	}

	// 非数字：导航留在上方 + 错误提示。
	out = p.pushDirections(out, activeScreen)
	return append(out, hintRow(i18n.L("无效输入", "Invalid input"),
		i18n.L("输入分钟数（1–1440），或选择上方选项", "Enter minutes (1–1440) or choose an option above"), 0.0))
}

// pushDirections 追加两个进入二级的方向（EnterSubquery）；当前生效的方向带「当前」。
func (p *Provider) pushDirections(out []providers.Match, activeScreen *bool) []providers.Match {
	mark := func(title string, isCurrent bool) string {
		if isCurrent {
			return title + i18n.L("（当前）", " (current)")
		}
		return title
	}
	sysCurrent := activeScreen != nil && !*activeScreen
	scrCurrent := activeScreen != nil && *activeScreen
	out = append(out,
		providers.MatchSubquery(ID, mark(i18n.L("仅系统唤醒", "System only"), sysCurrent),
			i18n.L("进入后选时长档位（30 分钟 ~ 永久）", "Pick a duration (30 min – forever)"), subModeSystem, 0.9),
		providers.MatchSubquery(ID, mark(i18n.L("连屏幕一起保持", "Keep screen on"), scrCurrent),
			i18n.L("进入后选时长档位，连屏幕一起保持", "Pick a duration and keep the screen on"), subModeScreen, 0.8),
	)
	return out
}

// queryLevel2：空输入 5 档预设；纯数字一条自定义；越界 / 非数字提示（亮屏与否由 subMode 定）。
func (p *Provider) queryLevel2(query, subMode string) []providers.Match {
	keepScreen := subMode == subModeScreen
	if query == "" {
		out := make([]providers.Match, 0, len(presetMinutes)+1)
		for i, m := range presetMinutes {
			mm := m
			out = append(out, p.optionMatch(&mm, keepScreen, 1.0-float64(i)*0.02, ""))
		}
		out = append(out, p.optionMatch(nil, keepScreen, 1.0-float64(len(presetMinutes))*0.02, ""))
		return out
	}

	if minutes, err := strconv.Atoi(query); err == nil {
		if minutes >= 1 && minutes <= maxCustomMinutes {
			return []providers.Match{p.optionMatch(&minutes, keepScreen, 1.0, "")}
		}
		return []providers.Match{hintRow(i18n.L("时长超出范围", "Duration out of range"),
			fmt.Sprintf(i18n.L("请输入 1–%d 分钟", "Enter 1–%d minutes"), maxCustomMinutes), 1.0)}
	}
	return []providers.Match{hintRow(i18n.L("无效输入", "Invalid input"),
		i18n.L("仅支持分钟数（1–1440）", "Only minutes (1–1440) are supported"), 0.0)}
}

// scopeLabel 保持范围的副标题文案（方案 A：动词短语「保持系统唤醒」）。
func scopeLabel(keepScreen bool) string {
	if keepScreen {
		return i18n.L("保持系统唤醒 + 屏幕", "Keep system and screen awake")
	}
	return i18n.L("保持系统唤醒", "Keep system awake")
}

// MenuLabel 托盘「保持唤醒」状态条目的文案（1:1 对照 voke-sys/src/awake.rs 的
// menu_label）：status=nil（未激活）→「保持唤醒：未激活」；否则
// 「保持唤醒：{剩余} · {屏幕打开|屏幕关闭}」。剩余部分复用 Status.remainingLabel
// （与生产同口径：永久 / 即将结束 / 「还剩 …」）。
//
// 屏幕措辞刻意与插件内的「动作描述」（连屏幕一起保持 / 仅系统唤醒）区分开：这里
// 「屏幕打开 / 屏幕关闭」是**结果描述**（屏幕会不会被保持），与插件一级那条状态展示行同口径。
func MenuLabel(status *Status, now int64) string {
	if status == nil {
		return i18n.L("保持唤醒：未激活", "Keep Awake: off")
	}
	scope := i18n.L("屏幕关闭", "Screen may sleep")
	if status.KeepScreen {
		scope = i18n.L("屏幕打开", "Screen stays on")
	}
	return fmt.Sprintf(i18n.L("保持唤醒：%s · %s", "Keep Awake: %s · %s"), status.remainingLabel(now), scope)
}

func hintRow(title, subtitle string, score float64) providers.Match {
	m := providers.MatchHint(ID, title, subtitle)
	m.Score = score
	return m
}
