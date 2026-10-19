// awake provider 测试：以 `voke-core/awake.rs` 为 oracle——一级（未激活两方向 /
// 已激活取消项+方向 / 快捷数字 / 越界 / 非数字）、二级（system/screen 5 档预设 /
// 自定义单条 / 越界 / 非数字）、剩余时间标签各区间、状态现读、方向「当前」标记。
package awake

import (
	"strings"
	"testing"

	"github.com/zyition/voke/poc/voke-go/internal/providers"
)

const nowSecs = 1_700_000_000

func ptr(i int64) *int64 { return &i }

func provider(status *Status) *Provider {
	return NewWithSource(func() *Status { return status }, func() int64 { return nowSecs })
}

func query(p *Provider, q string) []providers.Match {
	return p.Query(providers.QueryCtx{Raw: q, Query: q})
}

func queryMode(p *Provider, q, mode string) []providers.Match {
	return p.Query(providers.QueryCtx{Raw: q, Query: q, SubMode: mode})
}

func titles(ms []providers.Match) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Title)
	}
	return out
}

func TestMetadata(t *testing.T) {
	p := provider(nil)
	if p.ID() != "awake" || p.Name() != "保持唤醒" || p.Description() != "阻止系统休眠（可选连屏幕一起）" {
		t.Errorf("元数据不符：%s/%s/%s", p.ID(), p.Name(), p.Description())
	}
	c := p.Config()
	if !c.IsGlobal {
		t.Error("awake 应 is_global（直接输 awake 命中）")
	}
	if c.Prefix != "" || c.Activation != "Direct" {
		t.Errorf("配置不符（无前缀/Direct）：%+v", c)
	}
}

func TestLevel1EmptyListsTwoDirections(t *testing.T) {
	hits := query(provider(nil), "")
	if len(hits) != 2 {
		t.Fatalf("未激活一级应 2 方向：%v", titles(hits))
	}
	if hits[0].Title != "仅系统唤醒" || hits[1].Title != "连屏幕一起保持" {
		t.Errorf("方向标题错误：%v", titles(hits))
	}
	if hits[0].Interact.Kind != providers.InteractEnterSubquery || hits[0].Interact.Sub != "system" {
		t.Errorf("首方向应 EnterSubquery(system)：%+v", hits[0].Interact)
	}
	if hits[1].Interact.Kind != providers.InteractEnterSubquery || hits[1].Interact.Sub != "screen" {
		t.Errorf("次方向应 EnterSubquery(screen)：%+v", hits[1].Interact)
	}
	for _, h := range hits {
		if h.Action.Kind != providers.ActionNone {
			t.Errorf("方向动作应 None：%+v", h.Action)
		}
	}
}

func TestLevel1ActiveCancelThenDirections(t *testing.T) {
	p := provider(&Status{ExpiresAt: ptr(nowSecs + 25*60), KeepScreen: true})
	hits := query(p, "")
	if len(hits) != 3 {
		t.Fatalf("已激活应 取消项 + 2 方向：%v", titles(hits))
	}
	if hits[0].Interact.Kind != providers.InteractExecute || hits[0].Action.Kind != providers.ActionCancelAwake {
		t.Errorf("首条应为 CancelAwake Execute：%+v", hits[0])
	}
	for _, want := range []string{"保持唤醒中", "屏幕打开", "还剩 25 分钟"} {
		if !strings.Contains(hits[0].Title, want) {
			t.Errorf("取消项标题应含 %q：%q", want, hits[0].Title)
		}
	}
	if !strings.Contains(hits[0].Subtitle, "选中取消") {
		t.Errorf("取消项副标题：%q", hits[0].Subtitle)
	}
	if hits[1].Interact.Sub != "system" || hits[2].Interact.Sub != "screen" {
		t.Errorf("后两条仍是方向：%+v", hits[1:])
	}
}

func TestLevel1ActiveTitleScreenScope(t *testing.T) {
	screen := query(provider(&Status{ExpiresAt: ptr(nowSecs + 25*60), KeepScreen: true}), "")
	if !strings.Contains(screen[0].Title, "屏幕打开") || strings.Contains(screen[0].Title, "屏幕关闭") {
		t.Errorf("screen 态标题应「屏幕打开」：%q", screen[0].Title)
	}
	system := query(provider(&Status{ExpiresAt: ptr(nowSecs + 25*60), KeepScreen: false}), "")
	if !strings.Contains(system[0].Title, "屏幕关闭") || strings.Contains(system[0].Title, "屏幕打开") {
		t.Errorf("system 态标题应「屏幕关闭」：%q", system[0].Title)
	}
}

func TestLevel1DirectionMarksCurrent(t *testing.T) {
	screen := query(provider(&Status{ExpiresAt: ptr(nowSecs + 25*60), KeepScreen: true}), "")
	if screen[1].Title != "仅系统唤醒" || screen[2].Title != "连屏幕一起保持（当前）" {
		t.Errorf("screen 当前态标记错误：%v", titles(screen))
	}
	system := query(provider(&Status{ExpiresAt: ptr(nowSecs + 25*60), KeepScreen: false}), "")
	if system[1].Title != "仅系统唤醒（当前）" || system[2].Title != "连屏幕一起保持" {
		t.Errorf("system 当前态标记错误：%v", titles(system))
	}
}

func TestLevel1QuickNumeric(t *testing.T) {
	hits := query(provider(nil), "45")
	if len(hits) != 1 {
		t.Fatalf("快捷数字应 1 条：%v", titles(hits))
	}
	if hits[0].Action.Kind != providers.ActionSetAwake || hits[0].Action.KeepScreen {
		t.Errorf("应 SetAwake(45, keep_screen=false)：%+v", hits[0].Action)
	}
	if hits[0].Action.Minutes == nil || *hits[0].Action.Minutes != 45 {
		t.Errorf("分钟数应 45：%+v", hits[0].Action)
	}
	if !strings.Contains(hits[0].Title, "45 分钟") || !strings.Contains(hits[0].Subtitle, "连屏幕请进入") {
		t.Errorf("标题 / 副标题错误：%q / %q", hits[0].Title, hits[0].Subtitle)
	}
}

func TestLevel1QuickNumericOutOfRange(t *testing.T) {
	for _, bad := range []string{"0", "1441", "99999"} {
		hits := query(provider(nil), bad)
		if len(hits) != 1 || hits[0].Action.Kind != providers.ActionNone || !hits[0].IsHint() {
			t.Errorf("%q 越界应只一条 Hint：得 %v", bad, titles(hits))
			continue
		}
		if !strings.Contains(hits[0].Title, "时长超出范围") {
			t.Errorf("%q 标题错误：%q", bad, hits[0].Title)
		}
	}
}

func TestLevel1InvalidKeyword(t *testing.T) {
	hits := query(provider(nil), "abc")
	if len(hits) != 3 {
		t.Fatalf("2 导航 + 1 提示：%v", titles(hits))
	}
	if hits[0].Interact.Sub != "system" || hits[1].Interact.Sub != "screen" {
		t.Errorf("导航项错误：%+v", hits[:2])
	}
	if !hits[2].IsHint() || hits[2].Title != "无效输入" || !strings.Contains(hits[2].Subtitle, "1–1440") {
		t.Errorf("错误提示错误：%+v", hits[2])
	}
}

func TestLevel2SystemPresets(t *testing.T) {
	hits := queryMode(provider(nil), "", "system")
	want := []string{"30 分钟", "1 小时", "2 小时", "8 小时", "永久"}
	if got := titles(hits); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("system 预设 = %v，想要 %v", got, want)
	}
	for _, h := range hits {
		if h.Action.Kind != providers.ActionSetAwake || h.Action.KeepScreen {
			t.Errorf("system 预设应 SetAwake(keep_screen=false)：%+v", h.Action)
		}
		if !strings.Contains(h.Subtitle, "保持系统唤醒") || strings.Contains(h.Subtitle, "+ 屏幕") {
			t.Errorf("副标题错误：%q", h.Subtitle)
		}
	}
}

func TestLevel2ScreenPresetsKeepScreen(t *testing.T) {
	hits := queryMode(provider(nil), "", "screen")
	if len(hits) != 5 {
		t.Fatalf("screen 预设应 5 档：%v", titles(hits))
	}
	for _, h := range hits {
		if h.Action.Kind != providers.ActionSetAwake || !h.Action.KeepScreen {
			t.Errorf("screen 预设应 keep_screen=true：%+v", h.Action)
		}
		if !strings.Contains(h.Subtitle, "保持系统唤醒 + 屏幕") {
			t.Errorf("副标题应含屏幕范围：%q", h.Subtitle)
		}
	}
}

func TestLevel2NumericSingleCustom(t *testing.T) {
	hits := queryMode(provider(nil), "90", "system")
	if len(hits) != 1 {
		t.Fatalf("自定义应只 1 条：%v", titles(hits))
	}
	if hits[0].Action.Kind != providers.ActionSetAwake || hits[0].Action.Minutes == nil || *hits[0].Action.Minutes != 90 {
		t.Errorf("应 SetAwake(90)：%+v", hits[0].Action)
	}
	if !strings.Contains(hits[0].Title, "90 分钟") {
		t.Errorf("标题错误：%q", hits[0].Title)
	}
}

func TestLevel2OutOfRangeAndInvalid(t *testing.T) {
	if hits := queryMode(provider(nil), "99999", "system"); len(hits) != 1 || !hits[0].IsHint() || hits[0].Title != "时长超出范围" {
		t.Errorf("越界提示错误：%+v", hits)
	}
	hits := queryMode(provider(nil), "abc", "system")
	if len(hits) != 1 || !hits[0].IsHint() || hits[0].Title != "无效输入" || hits[0].Subtitle != "仅支持分钟数（1–1440）" {
		t.Errorf("无效提示错误：%+v", hits)
	}
}

func TestPlaceholder(t *testing.T) {
	p := provider(nil)
	if !strings.Contains(p.Placeholder(""), "亮屏方式") ||
		!strings.Contains(p.Placeholder("system"), "分钟数") ||
		!strings.Contains(p.Placeholder("screen"), "连屏幕") {
		t.Error("占位符应随级别变化")
	}
}

func TestRemainingLabels(t *testing.T) {
	mk := func(secs int64) Status { return Status{ExpiresAt: ptr(nowSecs + secs)} }
	cases := []struct {
		s    Status
		want string
	}{
		{mk(30), "还剩 30 秒"},
		{mk(25 * 60), "还剩 25 分钟"},
		{mk(3600), "还剩 1 小时"},
		{mk(5400), "还剩 1 小时 30 分钟"},
		{Status{}, "永久"},
	}
	for _, c := range cases {
		if got := c.s.remainingLabel(nowSecs); got != c.want {
			t.Errorf("remainingLabel = %q，想要 %q", got, c.want)
		}
	}
}

func TestPermanentAndExpiredLabels(t *testing.T) {
	hits := query(provider(&Status{}), "")
	if !strings.Contains(hits[0].Title, "永久") || !strings.Contains(hits[0].Subtitle, "选中取消") {
		t.Errorf("永久态取消项错误：%q", hits[0].Title)
	}
	hits = query(provider(&Status{ExpiresAt: ptr(nowSecs - 10)}), "")
	if !strings.Contains(hits[0].Title, "即将结束") {
		t.Errorf("已到期应显示即将结束：%q", hits[0].Title)
	}
}

func TestStatusReadEveryQuery(t *testing.T) {
	calls := 0
	p := NewWithSource(func() *Status { calls++; return nil }, func() int64 { return nowSecs })
	_ = query(p, "")
	_ = query(p, "")
	if calls != 2 {
		t.Errorf("状态应每次查询现读：calls=%d", calls)
	}
}

func TestNoCopyActions(t *testing.T) {
	for _, h := range query(provider(nil), "") {
		if h.Action.Kind == providers.ActionCopy {
			t.Errorf("awake 不该产生 Copy 动作：%+v", h.Action)
		}
	}
}

// TestMenuLabel 覆盖托盘状态条目全部分支（oracle：voke-sys awake.rs 的
// menu_labels_cover_all_states）——未激活 / 分钟 / 永久 / 小时+分钟 / 秒 / 到期。
func TestMenuLabel(t *testing.T) {
	cases := []struct {
		st   *Status
		want string
	}{
		{nil, "保持唤醒：未激活"},
		{&Status{ExpiresAt: ptr(nowSecs + 25*60), KeepScreen: true}, "保持唤醒：还剩 25 分钟 · 屏幕打开"},
		{&Status{ExpiresAt: ptr(nowSecs + 25*60), KeepScreen: false}, "保持唤醒：还剩 25 分钟 · 屏幕关闭"},
		{&Status{ExpiresAt: nil, KeepScreen: true}, "保持唤醒：永久 · 屏幕打开"},
		{&Status{ExpiresAt: nil, KeepScreen: false}, "保持唤醒：永久 · 屏幕关闭"},
		{&Status{ExpiresAt: ptr(nowSecs + 5400), KeepScreen: false}, "保持唤醒：还剩 1 小时 30 分钟 · 屏幕关闭"},
		{&Status{ExpiresAt: ptr(nowSecs + 30), KeepScreen: false}, "保持唤醒：还剩 30 秒 · 屏幕关闭"},
		{&Status{ExpiresAt: ptr(nowSecs), KeepScreen: false}, "保持唤醒：即将结束 · 屏幕关闭"},
	}
	for _, c := range cases {
		if got := MenuLabel(c.st, nowSecs); got != c.want {
			t.Errorf("MenuLabel(%+v) = %q，期望 %q", c.st, got, c.want)
		}
	}
}
