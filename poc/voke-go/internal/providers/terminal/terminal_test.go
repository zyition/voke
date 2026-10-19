// terminal provider 测试：以 `voke-core/terminal.rs` 为 oracle——JSONC 剥注释（保留
// 字符串内 URL/转义）、解析 / hidden 过滤 / 无 name 丢弃、缺 profiles 空列表、坏 JSON
// 不 panic、AUMID 按来源盖章、同名去重 stable 优先、default 置顶、provider 打分与动作
// （guid 优先 / 退化显示名）、副标题 commandline + 默认标记、排序截断、每次现读。
package terminal

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/zyition/voke/poc/voke-go/internal/providers"
)

const stableJSON = `{
    "defaultProfile": "{61c54bbd-c2c6-5271-96e7-009a87ff44bf}",
    "profiles": {
        "defaults": { "font": { "face": "Cascadia Mono" } },
        "list": [
            { "guid": "{61c54bbd-c2c6-5271-96e7-009a87ff44bf}", "name": "Windows PowerShell", "commandline": "powershell.exe" },
            { "guid": "{0caa0dad-35be-5f56-a8ff-afceeeaa6101}", "name": "命令提示符", "commandline": "cmd.exe" },
            { "guid": "{b453ae62-4e3d-5e58-b989-0a998ec441b8}", "name": "Azure Cloud Shell", "hidden": true, "source": "Windows.Terminal.Azure" },
            { "guid": "{2c4de342-38b7-51cf-b940-2309a097f518}", "name": "Ubuntu", "source": "Windows.Terminal.Wsl" },
            { "guid": "{novice}", "commandline": "noname.exe" }
        ]
    }
}`

const previewJSON = `{
    "profiles": {
        "list": [
            { "guid": "{aaaa}", "name": "Ubuntu", "commandline": "wsl.exe -d Ubuntu" },
            { "guid": "{bbbb}", "name": "PowerShell Preview", "commandline": "pwsh.exe" }
        ]
    }
}`

const (
	stableAUMID  = "Microsoft.WindowsTerminal_8wekyb3d8bbwe!App"
	previewAUMID = "Microsoft.WindowsTerminalPreview_8wekyb3d8bbwe!App"
)

func src(contents, aumid string) SettingsSource {
	return SettingsSource{Contents: contents, AUMID: aumid}
}

func providerWith(entries []Profile) *Provider {
	cp := append([]Profile{}, entries...)
	return New(func() []Profile { return cp }, `C:\wt\wt.exe`)
}

func mergedProvider() *Provider {
	return providerWith(ProfilesFromSettings([]SettingsSource{src(stableJSON, stableAUMID), src(previewJSON, previewAUMID)}))
}

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

func TestStripLineAndBlockComments(t *testing.T) {
	input := "{\n // comment\n \"a\": 1, /* block\n multi */ \"b\": 2 }"
	stripped := StripJSONCComments(input)
	if strings.Contains(stripped, "comment") || strings.Contains(stripped, "block") {
		t.Errorf("注释应被剥掉：%q", stripped)
	}
	if strings.Count(stripped, "\n") != strings.Count(input, "\n") {
		t.Errorf("换行应保留以维持行号")
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(stripped), &v); err != nil || v["a"].(float64) != 1 || v["b"].(float64) != 2 {
		t.Errorf("剥完应可解析：%q err=%v", stripped, err)
	}
}

func TestStripPreservesStrings(t *testing.T) {
	input := `{ "url": "https://example.com//a", "q": "a\"//b" }`
	stripped := StripJSONCComments(input)
	var v map[string]any
	if err := json.Unmarshal([]byte(stripped), &v); err != nil {
		t.Fatalf("应可解析：%v", err)
	}
	if v["url"] != "https://example.com//a" || v["q"] != `a"//b` {
		t.Errorf("字符串内内容应原样保留：%+v", v)
	}
}

func TestParsesNormalProfiles(t *testing.T) {
	names := profileNames(ProfilesFromSettings([]SettingsSource{src(stableJSON, stableAUMID)}))
	for _, want := range []string{"Windows PowerShell", "命令提示符", "Ubuntu"} {
		if !contains(names, want) {
			t.Errorf("应含 %q：%v", want, names)
		}
	}
}

func TestHiddenAndNamelessFiltered(t *testing.T) {
	profiles := ProfilesFromSettings([]SettingsSource{src(stableJSON, stableAUMID)})
	names := profileNames(profiles)
	if contains(names, "Azure Cloud Shell") {
		t.Errorf("hidden 的必须过滤：%v", names)
	}
	for _, p := range profiles {
		if p.Name == "" {
			t.Error("不该出现空名条目")
		}
	}
	if len(profiles) != 3 {
		t.Errorf("stable 应 3 条（PowerShell/命令提示符/Ubuntu），实得 %d：%v", len(profiles), names)
	}
}

func TestMissingProfilesEmpty(t *testing.T) {
	if len(ProfilesFromSettings([]SettingsSource{src("{}", stableAUMID)})) != 0 {
		t.Error("空对象应无 profile")
	}
	if len(ProfilesFromSettings([]SettingsSource{src(`{"defaultProfile": "{x}"}`, stableAUMID)})) != 0 {
		t.Error("缺 profiles 字段应无 profile")
	}
}

func TestBrokenJSONNoPanic(t *testing.T) {
	for _, bad := range []string{"{ not json", ""} {
		if len(ProfilesFromSettings([]SettingsSource{src(bad, stableAUMID)})) != 0 {
			t.Errorf("%q 坏 JSON 应空列表不 panic", bad)
		}
	}
	if len(ProfilesFromSettings(nil)) != 0 {
		t.Error("空来源应空列表")
	}
}

func TestJSONCParsesViaFallback(t *testing.T) {
	jsonc := `{
        // 默认用 PowerShell
        "profiles": {
            "list": [
                /* 块注释 */
                { "name": "pwsh", "commandline": "pwsh.exe" }
            ]
        }
    }`
	profiles := ProfilesFromSettings([]SettingsSource{src(jsonc, stableAUMID)})
	if len(profiles) != 1 || profiles[0].Name != "pwsh" {
		t.Errorf("带注释应经兜底解析：%+v", profiles)
	}
}

func TestAUMIDStampedFromSource(t *testing.T) {
	merged := ProfilesFromSettings([]SettingsSource{src(stableJSON, stableAUMID), src(previewJSON, previewAUMID)})
	ubuntu := find(merged, "Ubuntu")
	if ubuntu == nil || ubuntu.AUMID != stableAUMID {
		t.Errorf("同名去重保留 stable 来源：%+v", ubuntu)
	}
	prev := find(merged, "PowerShell Preview")
	if prev == nil || prev.AUMID != previewAUMID {
		t.Errorf("Preview 独有条目盖 Preview AUMID：%+v", prev)
	}
	for _, p := range merged {
		if p.AUMID == "" {
			t.Errorf("每个 profile 都应有 AUMID：%+v", p)
		}
	}
}

func TestMergeDedupesStableFirst(t *testing.T) {
	merged := ProfilesFromSettings([]SettingsSource{src(stableJSON, stableAUMID), src(previewJSON, previewAUMID)})
	names := profileNames(merged)
	if len(names) != 4 {
		t.Fatalf("stable 3 + preview 独有 1 = 4：%v", names)
	}
	if count(names, "Ubuntu") != 1 {
		t.Errorf("同名必须去重：%v", names)
	}
	if ubuntu := find(merged, "Ubuntu"); ubuntu == nil || ubuntu.Commandline != "" {
		t.Errorf("同名去重 stable 优先（stable Ubuntu 无 commandline）：%+v", ubuntu)
	}
}

func TestDefaultPinnedFirst(t *testing.T) {
	merged := ProfilesFromSettings([]SettingsSource{src(stableJSON, stableAUMID), src(previewJSON, previewAUMID)})
	if merged[0].Name != "Windows PowerShell" || !merged[0].IsDefault {
		t.Fatalf("defaultProfile 应置顶：%+v", merged[0])
	}
	if count2(merged, func(p Profile) bool { return p.IsDefault }) != 1 {
		t.Error("应恰好 1 个 default")
	}
	rest := profileNames(merged[1:])
	want := []string{"命令提示符", "Ubuntu", "PowerShell Preview"}
	if strings.Join(rest, ",") != strings.Join(want, ",") {
		t.Errorf("非 default 相对顺序不变：%v，想要 %v", rest, want)
	}
}

func TestPreviewOnly(t *testing.T) {
	merged := ProfilesFromSettings([]SettingsSource{src(previewJSON, previewAUMID)})
	if len(merged) != 2 {
		t.Fatalf("preview 单独应 2 条：%+v", merged)
	}
	for _, p := range merged {
		if p.IsDefault {
			t.Errorf("preview 无 defaultProfile，应全非 default：%+v", p)
		}
	}
}

func TestProviderMetadata(t *testing.T) {
	p := mergedProvider()
	if p.ID() != "terminal" || p.Name() != "终端配置" {
		t.Errorf("元数据不符：%s/%s", p.ID(), p.Name())
	}
	if strings.Join(p.Aliases(), ",") != "wt,terminal" {
		t.Errorf("别名应 wt,terminal：%v", p.Aliases())
	}
	c := p.Config()
	if !c.Enabled || !c.IsGlobal || c.Weight != 1.0 || c.Prefix != "" {
		t.Errorf("配置不符（enabled/is_global/1.0/无前缀）：%+v", c)
	}
}

func TestEmptyQueryMergeOrder(t *testing.T) {
	hits := query(mergedProvider(), "")
	want := []string{"Windows PowerShell", "命令提示符", "Ubuntu", "PowerShell Preview"}
	if got := titles(hits); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("空查询按合并序全返回：%v，想要 %v", got, want)
	}
	for _, h := range hits {
		if h.Score != 1.0 {
			t.Errorf("空查询 score 应 1.0：%+v", h)
		}
	}
}

func TestActionIsWtAppActivationWithGUID(t *testing.T) {
	hits := query(mergedProvider(), "ubuntu")
	if len(hits) != 1 {
		t.Fatalf("ubuntu 应 1 命中：%+v", hits)
	}
	a := hits[0].Action
	if a.Kind != providers.ActionLaunchTerminalProfile || a.Aumid != stableAUMID ||
		a.Profile != "{2c4de342-38b7-51cf-b940-2309a097f518}" || a.FallbackPath != `C:\wt\wt.exe` {
		t.Errorf("应 LaunchTerminalProfile{stable, guid, wt.exe}：%+v", a)
	}
	if hits[0].Interact.Kind != providers.InteractExecute {
		t.Errorf("应 Execute：%+v", hits[0].Interact)
	}
}

func TestActionFallsBackToNameWhenNoGUID(t *testing.T) {
	p := providerWith([]Profile{{Name: "老配置", Commandline: "cmd.exe", AUMID: stableAUMID}})
	hits := query(p, "老配置")
	if len(hits) != 1 {
		t.Fatalf("应 1 命中：%+v", hits)
	}
	if hits[0].Action.Profile != "老配置" {
		t.Errorf("guid 缺失应退化显示名：%+v", hits[0].Action)
	}
}

func TestActivationArgumentsForm(t *testing.T) {
	if got := ProfileActivationArguments("{574e775e-4f2a-5b96-ac1e-a2962a402336}"); got != `--profile "{574e775e-4f2a-5b96-ac1e-a2962a402336}"` {
		t.Errorf("参数串错误：%q", got)
	}
	if got := ProfileActivationArguments("PowerShell 7"); got != `--profile "PowerShell 7"` {
		t.Errorf("含空格显示名：%q", got)
	}
}

func TestMatchesByNameAndPinyin(t *testing.T) {
	p := mergedProvider()
	for _, q := range []string{"powershell", "PowerShell"} {
		if !contains(titles(query(p, q)), "Windows PowerShell") {
			t.Errorf("%q 应命中 Windows PowerShell", q)
		}
	}
	if !contains(titles(query(p, "mlts")), "命令提示符") {
		t.Errorf("拼音首字母 mlts 应命中命令提示符：%v", titles(query(p, "mlts")))
	}
}

func TestMatchesByCommandline(t *testing.T) {
	if got := titles(query(mergedProvider(), "cmd.exe")); !contains(got, "命令提示符") {
		t.Errorf("cmd.exe 应命中命令提示符：%v", got)
	}
}

func TestSubtitleCommandlineAndDefault(t *testing.T) {
	hits := query(mergedProvider(), "")
	ps := findMatch(hits, "Windows PowerShell")
	if ps == nil || !strings.Contains(ps.Subtitle, "powershell.exe") || !strings.Contains(ps.Subtitle, "默认配置") {
		t.Errorf("PS 副标题应带 commandline + 默认标记：%+v", ps)
	}
	ubuntu := findMatch(hits, "Ubuntu")
	if ubuntu == nil || ubuntu.Subtitle == "" {
		t.Errorf("无 commandline 应有兜底副标题：%+v", ubuntu)
	}
}

func TestUnrelatedNothingAndEmptyUsable(t *testing.T) {
	if len(query(mergedProvider(), "zzzqqqxxx")) != 0 {
		t.Error("无关查询应空")
	}
	p := providerWith(nil)
	if len(query(p, "x")) != 0 || len(query(p, "")) != 0 {
		t.Error("空数据源应可用")
	}
}

func TestFetcherCalledEveryQuery(t *testing.T) {
	calls := 0
	p := New(func() []Profile { calls++; return nil }, "wt.exe")
	query(p, "a")
	query(p, "b")
	if calls != 2 {
		t.Errorf("每次查询都应重读数据源：calls=%d", calls)
	}
}

func TestSortedDescendingAndCapped(t *testing.T) {
	entries := make([]Profile, 80)
	for i := range entries {
		entries[i] = Profile{Name: fmt.Sprintf("profile%02d", i), Commandline: "cmd.exe", AUMID: stableAUMID}
	}
	hits := query(providerWith(entries), "profile")
	if len(hits) != maxResults {
		t.Fatalf("应截断到 %d：%d", maxResults, len(hits))
	}
	for i := 1; i < len(hits); i++ {
		if !(hits[i-1].Score >= hits[i].Score) {
			t.Errorf("分数必须降序")
		}
	}
}

// ── 小工具 ──

func profileNames(ps []Profile) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Name
	}
	return out
}

func find(ps []Profile, name string) *Profile {
	for i := range ps {
		if ps[i].Name == name {
			return &ps[i]
		}
	}
	return nil
}

func findMatch(ms []providers.Match, title string) *providers.Match {
	for i := range ms {
		if ms[i].Title == title {
			return &ms[i]
		}
	}
	return nil
}

func count(ss []string, want string) int {
	n := 0
	for _, s := range ss {
		if s == want {
			n++
		}
	}
	return n
}

func count2(ps []Profile, pred func(Profile) bool) int {
	n := 0
	for _, p := range ps {
		if pred(p) {
			n++
		}
	}
	return n
}
