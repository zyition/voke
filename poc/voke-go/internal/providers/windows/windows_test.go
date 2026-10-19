// windows provider 测试：以 `voke-core/windows.rs` 为 oracle——title / process_name
// 分别打分取 max、空句柄 / 空标题剔除、空查询全返回 score=1.0 不截断、非空排序截断、
// 中文标题拼音命中、每次查询现枚举、搜索键记忆化且容量有界。
package windows

import (
	"fmt"
	"testing"

	"github.com/zyition/voke/poc/voke-go/internal/match"
	"github.com/zyition/voke/poc/voke-go/internal/providers"
)

func mkEntry(title, process string, hwnd int64) Entry {
	return Entry{Hwnd: hwnd, Title: title, ProcessName: process, ProcessPath: `C:\apps\` + process}
}

func sampleEntries() []Entry {
	return []Entry{
		mkEntry("main.rs - voke - Visual Studio Code", "Code.exe", 101),
		mkEntry("voke — 搜索", "voke.exe", 102),
		mkEntry("微信", "WeChat.exe", 103),
		mkEntry("Windows Terminal", "WindowsTerminal.exe", 104),
	}
}

func providerWith(entries []Entry) *Provider {
	return New(func() []Entry { return append([]Entry{}, entries...) })
}

func sample() *Provider { return providerWith(sampleEntries()) }

func query(p *Provider, q string) []providers.Match {
	return p.Query(providers.QueryCtx{Raw: q, Query: q})
}

func titles(ms []providers.Match) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Title)
	}
	return out
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func TestMetadata(t *testing.T) {
	p := sample()
	if p.ID() != "windows" || p.Name() != "窗口" || p.Description() != "切换到已打开的窗口" {
		t.Errorf("元数据不符：%s/%s/%s", p.ID(), p.Name(), p.Description())
	}
	c := p.Config()
	if c.Weight != 1.0 || !c.Enabled || !c.IsGlobal {
		t.Errorf("配置不符（weight=1.0 enabled is_global）：%+v", c)
	}
	if c.Prefix != "" {
		t.Errorf("窗口不能被前缀门控，实得 %q", c.Prefix)
	}
}

func TestMatchesWindowTitle(t *testing.T) {
	if got := titles(query(sample(), "windows terminal")); !contains(got, "Windows Terminal") {
		t.Errorf("应命中 Windows Terminal：%v", got)
	}
}

func TestMatchesProcessName(t *testing.T) {
	// "code.exe" 只在进程名里。
	if got := titles(query(sample(), "code.exe")); !contains(got, "main.rs - voke - Visual Studio Code") {
		t.Errorf("进程名 code.exe 应命中 VS Code：%v", got)
	}
	if got := titles(query(sample(), "wechat")); !contains(got, "微信") {
		t.Errorf("进程名 wechat.exe 应命中微信：%v", got)
	}
}

func TestActionIsActivateWindow(t *testing.T) {
	hits := query(sample(), "windows terminal")
	if len(hits) == 0 {
		t.Fatal("应有命中")
	}
	if hits[0].ProviderID != "windows" || hits[0].Subtitle != "WindowsTerminal.exe" {
		t.Errorf("窗口行字段错误：%+v", hits[0])
	}
	if hits[0].Action.Kind != providers.ActionActivateWindow || hits[0].Action.Hwnd != 104 {
		t.Errorf("应 ActivateWindow(104)：%+v", hits[0].Action)
	}
}

func TestIconSourceIsProcessPath(t *testing.T) {
	entries := sampleEntries()
	hits := query(providerWith(entries), "windows terminal")
	if len(hits) == 0 {
		t.Fatal("应有命中")
	}
	// O4 模型：Match.Icon 携「图标来源路径」= 进程 exe（渲染层经 platform.AppIcon 懒取位图 +
	// 缓存），不再是旧的落盘 PNG 路径。
	if hits[0].Icon != entries[3].ProcessPath {
		t.Fatalf("图标来源应是进程路径 %q，实得 %q", entries[3].ProcessPath, hits[0].Icon)
	}
	// 受保护进程取不到路径（ProcessPath 空）→ Match.Icon 空 → 渲染层不画图标（无降级首字母）。
	prot := []Entry{{Hwnd: 200, Title: "Secure App", ProcessName: "Secure.exe"}}
	if h := query(providerWith(prot), "secure"); len(h) == 0 || h[0].Icon != "" {
		t.Errorf("无进程路径的条目 Icon 应为空串：%+v", h)
	}
}

func TestEmptyQueryReturnsAll(t *testing.T) {
	hits := query(sample(), "")
	if len(hits) != 4 {
		t.Fatalf("空查询应全返回（引擎重排、provider 不截断）：%v", titles(hits))
	}
	for _, h := range hits {
		if h.Score != 1.0 {
			t.Errorf("空查询 score 应 1.0：%+v", h)
		}
		if h.Action.Kind != providers.ActionActivateWindow || h.Action.Hwnd <= 0 {
			t.Errorf("窗口动作应 ActivateWindow(hwnd>0)：%+v", h.Action)
		}
	}
}

func TestUnrelatedQueryNothing(t *testing.T) {
	if got := query(sample(), "zzzqqqxxx"); len(got) != 0 {
		t.Errorf("无关查询应空：%v", titles(got))
	}
}

func TestInvalidEntriesDropped(t *testing.T) {
	p := providerWith([]Entry{
		mkEntry("", "Code.exe", 1),
		mkEntry("   ", "Code.exe", 2),
		mkEntry("Valid", "Code.exe", 0),
		mkEntry("OK Window", "Code.exe", 3),
	})
	if got := query(p, ""); len(got) != 1 {
		t.Fatalf("空标题 / 空句柄应被剔除：%v", titles(got))
	}
	if h := query(p, "ok"); len(h) != 1 || h[0].Action.Hwnd != 3 {
		t.Errorf("应只剩 OK Window(3)：%+v", h)
	}
}

func TestFetcherCalledEveryQuery(t *testing.T) {
	n := 0
	p := New(func() []Entry {
		n++
		return []Entry{mkEntry("Dynamic Window", "dyn.exe", int64(100+n))}
	})
	if h := query(p, "dynamic"); len(h) == 0 || h[0].Action.Hwnd != 101 {
		t.Fatalf("首次枚举应得 hwnd 101：%+v", h)
	}
	if h := query(p, "dynamic"); len(h) == 0 || h[0].Action.Hwnd != 102 {
		t.Fatalf("次应得 hwnd 102（证明现读）：%+v", h)
	}
	if n != 2 {
		t.Errorf("每次查询都应调闭包：calls=%d", n)
	}
}

func TestSearchKeysMemoized(t *testing.T) {
	p := sample()
	query(p, "windows terminal")
	after := p.keys.Len()
	if after == 0 {
		t.Fatal("首次查询应把搜索键写入缓存")
	}
	query(p, "windows terminal")
	if p.keys.Len() != after {
		t.Errorf("同一批窗口重复查询不该新增缓存：%d → %d", after, p.keys.Len())
	}
}

func TestKeyCacheBoundedWhenTitlesChange(t *testing.T) {
	i := 0
	p := New(func() []Entry {
		i++
		return []Entry{mkEntry(fmt.Sprintf("tab-%d - 浏览器", i), "browser.exe", 1)}
	})
	for k := 0; k < 2000; k++ {
		if hits := query(p, "tab"); len(hits) != 1 {
			t.Fatalf("每次都要用最新标题：%+v", hits)
		}
	}
	if p.keys.Len() > match.CacheCapacity {
		t.Errorf("缓存必须 ≤ 上限，实得 %d", p.keys.Len())
	}
}

func TestResultsSortedDescendingAndCapped(t *testing.T) {
	entries := make([]Entry, 80)
	for i := range entries {
		entries[i] = mkEntry(fmt.Sprintf("doc%02d.txt - 编辑器", i), "editor.exe", int64(1000+i))
	}
	hits := query(providerWith(entries), "doc")
	if len(hits) != maxResults {
		t.Fatalf("应截断到 maxResults=%d：%d", maxResults, len(hits))
	}
	for i := 1; i < len(hits); i++ {
		if !(hits[i-1].Score >= hits[i].Score) {
			t.Errorf("分数必须降序：%v !>= %v", hits[i-1].Score, hits[i].Score)
		}
	}
}

func TestEmptyQueryNotCapped(t *testing.T) {
	entries := make([]Entry, 80)
	for i := range entries {
		entries[i] = mkEntry(fmt.Sprintf("win%02d", i), "app.exe", int64(2000+i))
	}
	if got := query(providerWith(entries), ""); len(got) != 80 {
		t.Errorf("空查询不截断，应 80：%d", len(got))
	}
}

func TestMatchesChineseByPinyin(t *testing.T) {
	p := sample()
	for _, q := range []string{"微信", "weixin", "wx"} {
		if got := titles(query(p, q)); !contains(got, "微信") {
			t.Errorf("%q 应命中微信：%v", q, got)
		}
	}
}

func TestEmptyProviderUsable(t *testing.T) {
	p := NewEmpty()
	if len(query(p, "x")) != 0 || len(query(p, "")) != 0 {
		t.Error("空数据源应可用且返回空")
	}
}
