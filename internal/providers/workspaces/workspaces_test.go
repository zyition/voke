// workspaces provider 测试：以 `voke-core/workspace.rs` 为 oracle——percent_decode、
// display_name、脏数据过滤、VSCode URI/entries（本地/WSL/SSH/容器/未知/非 URI）、
// Zed 行（位置分类、多根、ssh url、字面量 null host 退其它、容器无 open_args）、
// 排序时间戳降序空排后、去重 (编辑器,路径)、provider 动作（用记录它的编辑器打开 /
// 退回路径）、副标题标记、TTL 缓存。
package workspaces

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"voke/internal/providers"
)

func sp(s string) *string { return &s }
func ip(i int64) *int64   { return &i }

func TestPercentDecode(t *testing.T) {
	if got := PercentDecode("wsl%2Bopensuse-tumbleweed"); got != "wsl+opensuse-tumbleweed" {
		t.Errorf("%q", got)
	}
	if got := PercentDecode("file:///c%3A/Users/x"); got != "file:///c:/Users/x" {
		t.Errorf("%q", got)
	}
	if got := PercentDecode("a+b"); got != "a+b" {
		t.Errorf("`+` 应字面量：%q", got)
	}
	if got := PercentDecode("100%20ok%2"); got != "100 ok%2" {
		t.Errorf("残缺 %%2 应原样：%q", got)
	}
	if got := PercentDecode("100%2"); got != "100%2" {
		t.Errorf("%q", got)
	}
}

func TestDisplayName(t *testing.T) {
	if got := DisplayName("/home/testuser/Work/projects/personal/voke"); got != "voke" {
		t.Errorf("%q", got)
	}
	if got := DisplayName(`C:\Users\testuser\code\beta`); got != "beta" {
		t.Errorf("%q", got)
	}
	if got := DisplayName("/home/testuser/proj/"); got != "proj" {
		t.Errorf("%q", got)
	}
	if got := DisplayName(`C:\x\code\my.code-workspace`); got != "my" {
		t.Errorf(".code-workspace 应去扩展名：%q", got)
	}
	if got := DisplayName("/x/.code-workspace"); got != ".code-workspace" {
		t.Errorf("极端：保底非空：%q", got)
	}
}

func TestDirtyPaths(t *testing.T) {
	for _, bad := range []string{"", "   ", `C:\Windows\Logs\CBS\CBS.log`, `c:\windows\system32`, "/proc/self", `C:\`} {
		if !IsDirtyPath(bad) {
			t.Errorf("%q 应判脏", bad)
		}
	}
	for _, bad := range []string{"/home/testuser/e2e-report.txt", "/home/testuser/.config/opencode/opencode.jsonc"} {
		if !IsDirtyPath(bad) {
			t.Errorf("%q 文件应判脏", bad)
		}
	}
	for _, good := range []string{"/home/testuser/Work/projects/personal/voke", "/home/testuser/qt-comfyui-minimax-h3", "/home/testuser/proj/.git"} {
		if IsDirtyPath(good) {
			t.Errorf("%q 不该判脏", good)
		}
	}
}

func TestVsCodeURI(t *testing.T) {
	loc, path := VsCodeURI("file:///c%3A/Users/testuser/AppData/Roaming/Rime")
	if loc.Kind != "local" || path != "C:/Users/testuser/AppData/Roaming/Rime" {
		t.Errorf("本地 windows：%+v %q", loc, path)
	}
	loc, path = VsCodeURI("file:///home/testuser/proj")
	if loc.Kind != "local" || path != "/home/testuser/proj" {
		t.Errorf("本地 posix：%+v %q", loc, path)
	}
	loc, path = VsCodeURI("file://wsl.localhost/openSUSE-Tumbleweed/home/testuser/x")
	if loc.Kind != "wsl" || loc.Distro != "openSUSE-Tumbleweed" || path != "/home/testuser/x" {
		t.Errorf("WSL via unc：%+v %q", loc, path)
	}
	loc, path = VsCodeURI("vscode-remote://wsl%2Bopensuse-tumbleweed/home/testuser/x")
	if loc.Kind != "wsl" || loc.Distro != "opensuse-tumbleweed" || path != "/home/testuser/x" {
		t.Errorf("WSL via remote scheme：%+v %q", loc, path)
	}
	loc, path = VsCodeURI("vscode-remote://ssh-remote%2Bme%40box/home/me/p")
	if loc.Kind != "ssh" || loc.User != "me" || loc.Host != "box" || loc.HasPort || path != "/home/me/p" {
		t.Errorf("SSH：%+v %q", loc, path)
	}
	loc, _ = VsCodeURI("vscode-remote://dev-container%2Bdeadbeef/workspace/src")
	if loc.Kind != "container" || loc.ID != "deadbeef" {
		t.Errorf("容器：%+v", loc)
	}
	loc, path = VsCodeURI("codespaces://foo/bar")
	if loc.Kind != "other" || loc.RawKind != "codespaces" || loc.HasLabel || path != "/bar" {
		t.Errorf("未知 scheme 落 other：%+v %q", loc, path)
	}
	loc, path = VsCodeURI(`C:\x\y`)
	if loc.Kind != "local" || path != `C:\x\y` {
		t.Errorf("非 URI 原样本地：%+v %q", loc, path)
	}
}

const vscodeJSON = `{"entries":[
    {"folderUri":"vscode-remote://wsl%2Bopensuse-tumbleweed/home/testuser/Work/projects/personal/voke","remoteAuthority":"wsl+openSUSE-Tumbleweed"},
    {"folderUri":"file:///c%3A/Users/testuser/code/beta","remoteAuthority":""},
    {"workspace":{"configPath":"file:///c%3A/Users/testuser/code/my.code-workspace"}},
    {"fileUri":"file:///c%3A/Users/testuser/AppData/Roaming/Rime/user.yaml","remoteAuthority":""},
    {"folderUri":"vscode-remote://ssh-remote%2Bme%40box/home/me/remote-proj","remoteAuthority":"ssh-remote+me@box"},
    {"folderUri":"vscode-remote://dev-container%2Bdeadbeef/workspace/src","remoteAuthority":"dev-container+deadbeef"}
]}`

func vscodePayloads() []VsCodeRecentPayload {
	return []VsCodeRecentPayload{{JSON: vscodeJSON, FileTimestamp: "2026-06-05 14:09:00"}}
}

func TestVsCodeEntriesCoverAllShapes(t *testing.T) {
	entries := VsCodeEntries(vscodePayloads())
	if len(entries) != 5 {
		t.Fatalf("fileUri 忽略 → 5 条：%+v", entries)
	}
	names := []string{}
	for _, e := range entries {
		names = append(names, e.Name)
	}
	want := []string{"voke", "beta", "my", "remote-proj", "src"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("names = %v，想要 %v", names, want)
	}
	if entries[0].Location.Kind != "wsl" || entries[0].Location.Distro != "openSUSE-Tumbleweed" {
		t.Errorf("WSL 位置错：%+v", entries[0].Location)
	}
	if strings.Join(entries[0].OpenArgs, " ") != "--folder-uri vscode-remote://wsl%2Bopensuse-tumbleweed/home/testuser/Work/projects/personal/voke" {
		t.Errorf("WSL open_args：%+v", entries[0].OpenArgs)
	}
	if entries[1].Path != "C:/Users/testuser/code/beta" || entries[1].Location.Kind != "local" {
		t.Errorf("本地盘符大写：%+v", entries[1])
	}
	if entries[2].OpenArgs[0] != "--file-uri" {
		t.Errorf(".code-workspace 走 --file-uri：%+v", entries[2].OpenArgs)
	}
	if entries[3].Location.Kind != "ssh" || entries[3].Location.User != "me" || entries[3].Location.Host != "box" {
		t.Errorf("SSH：%+v", entries[3].Location)
	}
	if entries[4].Location.Kind != "container" || entries[4].Location.ID != "deadbeef" {
		t.Errorf("容器：%+v", entries[4].Location)
	}
}

func TestVsCodeBadJSONNoPanic(t *testing.T) {
	for _, p := range []VsCodeRecentPayload{{JSON: "{ not json"}, {JSON: "{}"}, {}} {
		if len(VsCodeEntries([]VsCodeRecentPayload{p})) != 0 {
			t.Errorf("%q 应空列表不 panic", p.JSON)
		}
	}
	if len(VsCodeEntries(nil)) != 0 {
		t.Error("空 payloads 应空")
	}
}

func TestZedRowsByLocation(t *testing.T) {
	rows := []ZedWorkspaceRow{
		{Paths: "/home/testuser/Work/projects/personal/voke", Timestamp: "2026-10-08 09:41:31", Remote: &ZedRemoteRow{Kind: "wsl", User: sp("testuser"), Distro: sp("openSUSE-Tumbleweed")}},
		{Paths: `C:\Users\testuser\code\beta`, Timestamp: "2026-10-07 08:00:00", Remote: nil},
		{Paths: "/home/testuser/misc", Timestamp: "2026-08-03 13:12:06", Remote: &ZedRemoteRow{Kind: "wsl", Distro: sp("openSUSE-Tumbleweed")}},
	}
	entries := ZedEntries(rows)
	if len(entries) != 3 {
		t.Fatalf("3 条：%+v", entries)
	}
	if entries[0].Name != "voke" {
		t.Errorf("时间降序首条应 voke：%+v", entries[0])
	}
	if strings.Join(entries[0].OpenArgs, " ") != "--wsl testuser@openSUSE-Tumbleweed /home/testuser/Work/projects/personal/voke" {
		t.Errorf("voke open_args：%+v", entries[0].OpenArgs)
	}
	if strings.Join(entries[1].OpenArgs, " ") != `C:\Users\testuser\code\beta` {
		t.Errorf("本地 open_args：%+v", entries[1].OpenArgs)
	}
	if strings.Join(entries[2].OpenArgs, " ") != "--wsl openSUSE-Tumbleweed /home/testuser/misc" {
		t.Errorf("无 user 的 wsl：%+v", entries[2].OpenArgs)
	}
}

func TestZedDirtyRowsDropped(t *testing.T) {
	rows := []ZedWorkspaceRow{
		{Paths: `C:\Windows\Logs\CBS\CBS.log`, Timestamp: "2026-10-08 03:49:18"},
		{},
	}
	if len(ZedEntries(rows)) != 0 {
		t.Error("脏行应全过滤")
	}
}

func TestZedMultiRootPaths(t *testing.T) {
	json := ZedWorkspaceRow{Paths: `["/home/x/alpha", "/home/x/beta"]`, Timestamp: "2026-10-01 00:00:00"}
	if got := names(ZedEntries([]ZedWorkspaceRow{json})); strings.Join(got, ",") != "alpha,beta" {
		t.Errorf("JSON 数组多根：%v", got)
	}
	nl := ZedWorkspaceRow{Paths: "/home/x/gamma\n/home/x/delta", Timestamp: "2026-10-01 00:00:00"}
	if got := names(ZedEntries([]ZedWorkspaceRow{nl})); strings.Join(got, ",") != "gamma,delta" {
		t.Errorf("换行多根：%v", got)
	}
}

func TestZedSSHBuiltAsURL(t *testing.T) {
	row := ZedWorkspaceRow{
		Paths: "/home/me/proj", Timestamp: "2026-10-02 00:00:00",
		Remote: &ZedRemoteRow{Kind: "ssh", Host: sp("box"), Port: ip(2222), User: sp("me")},
	}
	if strings.Join(ZedEntries([]ZedWorkspaceRow{row})[0].OpenArgs, " ") != "ssh://me@box:2222/home/me/proj" {
		t.Errorf("ssh url 错误：%+v", ZedEntries([]ZedWorkspaceRow{row})[0].OpenArgs)
	}
}

func TestZedSSHLiteralNullHostFallsBackToOther(t *testing.T) {
	row := ZedWorkspaceRow{
		Paths: "/home/me/proj", Timestamp: "2026-10-02 00:00:00",
		Remote: &ZedRemoteRow{Kind: "ssh", Host: sp("null")},
	}
	entries := ZedEntries([]ZedWorkspaceRow{row})
	if len(entries) != 1 {
		t.Fatalf("条目仍列出：%+v", entries)
	}
	if entries[0].Location.Kind != "other" || entries[0].Location.RawKind != "ssh" || entries[0].Location.HasLabel {
		t.Errorf("拼不出 ssh:// → other：%+v", entries[0].Location)
	}
	if len(entries[0].OpenArgs) != 0 {
		t.Errorf("绝不生成 ssh://null/…：%+v", entries[0].OpenArgs)
	}
}

func TestZedUnsupportedLocationNoOpenArgs(t *testing.T) {
	row := ZedWorkspaceRow{
		Paths: "/work/src", Timestamp: "2026-10-02 00:00:00",
		Remote: &ZedRemoteRow{Kind: "docker", ContainerID: sp("abc123")},
	}
	e := ZedEntries([]ZedWorkspaceRow{row})[0]
	if e.Location.Kind != "container" || e.Location.ID != "abc123" {
		t.Errorf("容器仍列出：%+v", e.Location)
	}
	if len(e.OpenArgs) != 0 {
		t.Errorf("不支持打开 → 空 open_args：%+v", e.OpenArgs)
	}
}

func TestSortTimestampDescEmptyLast(t *testing.T) {
	entries := []Entry{entry("old", "2026-01-01 00:00:00"), entry("vscode", ""), entry("new", "2026-10-08 09:41:31"), entry("mid", "2026-06-01 00:00:00")}
	sortWorkspaces(entries)
	if got := names(entries); strings.Join(got, ",") != "new,mid,old,vscode" {
		t.Errorf("排序错误：%v", got)
	}
}

func TestDedupeSameEditorPath(t *testing.T) {
	entries := []Entry{
		entry("voke", "2026-10-08 00:00:00"),
		entry("voke", "2026-09-08 00:00:00"),
		func() Entry { e := entry("voke", "2026-10-07 00:00:00"); e.Editor = EditorVsCode; return e }(),
	}
	if len(dedupeWorkspaces(entries)) != 2 {
		t.Errorf("同编辑器去重、跨编辑器保留：%+v", dedupeWorkspaces(entries))
	}
}

func TestProviderMetadata(t *testing.T) {
	p := providerWith(nil)
	if p.ID() != "workspaces" || p.Name() != "工作区" || strings.Join(p.Aliases(), ",") != "ws,gzq" {
		t.Errorf("元数据不符：%s/%s/%v", p.ID(), p.Name(), p.Aliases())
	}
	c := p.Config()
	if !c.Enabled || !c.IsGlobal || c.Weight != 1.0 || c.Prefix != "" {
		t.Errorf("配置不符：%+v", c)
	}
}

func TestEmptyQueryFullScore(t *testing.T) {
	hits := query(providerWith([]Entry{entry("alpha", ""), entry("beta", "")}), "")
	if got := matchTitles(hits); strings.Join(got, ",") != "alpha,beta" {
		t.Errorf("空查询按序全返回：%v", got)
	}
	for _, h := range hits {
		if h.Score != 1.0 || h.Interact.Kind != providers.InteractExecute {
			t.Errorf("空查询行 score 1.0 Execute：%+v", h)
		}
	}
}

func TestSubtitleMarkers(t *testing.T) {
	p := providerWith([]Entry{
		{Name: "voke", Path: "/home/testuser/voke", Editor: EditorZed, Location: wslLoc("testuser", "openSUSE-Tumbleweed"), Timestamp: "2026-10-08 09:41:31"},
		{Name: "beta", Path: `C:\Users\testuser\code\beta`, Editor: EditorVsCode, Location: localLoc()},
	})
	hits := query(p, "")
	if find(hits, "voke").Subtitle != "Zed · WSL openSUSE-Tumbleweed · /home/testuser/voke" {
		t.Errorf("Zed 副标题：%q", find(hits, "voke").Subtitle)
	}
	if find(hits, "beta").Subtitle != `VSCode · 本地 · C:\Users\testuser\code\beta` {
		t.Errorf("VSCode 副标题：%q", find(hits, "beta").Subtitle)
	}
}

func TestActionUsesRecordingEditor(t *testing.T) {
	p := providerWith([]Entry{
		{Name: "beta", Path: `C:\Users\testuser\code\beta`, Editor: EditorVsCode, Location: localLoc(), OpenArgs: []string{"--folder-uri", "file:///C:/Users/testuser/code/beta"}},
		{Name: "remote", Path: "/home/me/remote", Editor: EditorZed, Location: sshLoc("me", "box", nil), Timestamp: "2026-10-08 00:00:00", OpenArgs: []string{"ssh://me@box/home/me/remote"}},
	})
	hits := query(p, "")
	code := find(hits, "beta").Action
	if code.Kind != providers.ActionLaunch || code.Path != "cmd.exe" || code.NoConsole != true ||
		strings.Join(code.Args, " ") != `/c C:\vscode\bin\code.cmd --folder-uri file:///C:/Users/testuser/code/beta` {
		t.Errorf("VSCode 经 cmd.exe /c：%+v", code)
	}
	zed := find(hits, "remote").Action
	if zed.Kind != providers.ActionLaunch || zed.Path != "zed" || zed.NoConsole != true ||
		strings.Join(zed.Args, " ") != "ssh://me@box/home/me/remote" {
		t.Errorf("Zed 打开：%+v", zed)
	}
}

func TestWithoutEditorFallsBackToPath(t *testing.T) {
	p := WithTTL(func() []Entry { return []Entry{entry("alpha", "")} }, nil, nil, 0)
	a := query(p, "alpha")[0].Action
	if a.Kind != providers.ActionLaunch || a.Path != "/home/testuser/alpha" || len(a.Args) != 0 || a.NoConsole {
		t.Errorf("无编辑器 → Launch 路径本身：%+v", a)
	}
}

func TestUnsupportedLocationFallsBackToPath(t *testing.T) {
	p := providerWith([]Entry{{Name: "src", Path: "/work/src", Editor: EditorZed, Location: containerLoc(""), Timestamp: "2026-10-08 00:00:00"}})
	a := query(p, "src")[0].Action
	if a.Path != "/work/src" || len(a.Args) != 0 || a.NoConsole {
		t.Errorf("open_args 空 → Launch 路径：%+v", a)
	}
}

func TestMatchesByNameAndPath(t *testing.T) {
	p := providerWith([]Entry{entry("voke", "2026-10-08 00:00:00"), entry("other", "2026-10-07 00:00:00")})
	if len(query(p, "vok")) != 1 {
		t.Error("vok 应 1 命中")
	}
	if findMatch(query(p, "testuser"), "other") == nil {
		t.Error("路径片段 testuser 应命中 other")
	}
	if len(query(p, "zzzqqq")) != 0 {
		t.Error("无关查询应空")
	}
}

func TestSortedDescAndCapped(t *testing.T) {
	entries := make([]Entry, 80)
	for i := range entries {
		entries[i] = entry(fmt.Sprintf("ws%02d", i), "2026-10-08 00:00:00")
	}
	hits := query(providerWith(entries), "ws")
	if len(hits) != maxResults {
		t.Fatalf("截断到 %d：%d", maxResults, len(hits))
	}
	for i := 1; i < len(hits); i++ {
		if !(hits[i-1].Score >= hits[i].Score) {
			t.Error("分数应降序")
		}
	}
}

func TestTTLCacheBehavior(t *testing.T) {
	calls := 0
	fetch := func() []Entry { calls++; return []Entry{entry("alpha", "")} }
	p := WithTTL(fetch, nil, nil, time.Hour)
	query(p, "a")
	query(p, "b")
	if calls != 1 {
		t.Errorf("TTL 内不重读：calls=%d", calls)
	}

	calls2 := 0
	fetch2 := func() []Entry { calls2++; return nil }
	p2 := WithTTL(fetch2, nil, nil, 0)
	query(p2, "a")
	query(p2, "b")
	if calls2 != 2 {
		t.Errorf("TTL=0 每次重读：calls=%d", calls2)
	}
}

func TestExpiredCacheReturnsFresh(t *testing.T) {
	calls := 0
	fetch := func() []Entry {
		calls++
		name := "first"
		if calls > 1 {
			name = "second"
		}
		return []Entry{entry(name, "")}
	}
	p := WithTTL(fetch, nil, nil, 0)
	if query(p, "")[0].Title != "first" {
		t.Error("首次 first")
	}
	if query(p, "")[0].Title != "second" {
		t.Error("过期后必须拿新数据")
	}
}

// ── 测试小工具 ──

func entry(name, timestamp string) Entry {
	return Entry{Name: name, Path: "/home/testuser/" + name, Editor: EditorZed, Location: localLoc(), Timestamp: timestamp}
}

func providerWith(entries []Entry) *Provider {
	cp := append([]Entry{}, entries...)
	return WithTTL(func() []Entry { return cp },
		&EditorCommand{Exe: "zed"},
		&EditorCommand{Exe: "cmd.exe", PreArgs: []string{"/c", `C:\vscode\bin\code.cmd`}},
		0)
}

func query(p *Provider, q string) []providers.Match {
	return p.Query(providers.QueryCtx{Raw: q, Query: q})
}

func names(es []Entry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.Name
	}
	return out
}

func matchTitles(ms []providers.Match) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Title
	}
	return out
}

func find(ms []providers.Match, title string) providers.Match {
	for _, m := range ms {
		if m.Title == title {
			return m
		}
	}
	return providers.Match{}
}

func findMatch(ms []providers.Match, title string) *providers.Match {
	for i := range ms {
		if ms[i].Title == title {
			return &ms[i]
		}
	}
	return nil
}
