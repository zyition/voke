// Package terminal 提供 Windows Terminal profile 的搜索与启动——1:1 对照生产
// `voke-core/src/terminal.rs`（对齐 PowerToys CmdPal 的 Windows Terminal 扩展）。
//
// 分层：纯逻辑（JSONC 预处理 + 解析 + 合并去重 + provider 打分），零 mygo、零 IO、
// 不碰文件系统。读盘（LOCALAPPDATA / Packages）在 voke-sys，合并语义全在这里可测。
// 数据源是注入的枚举闭包（每次查询重读 settings.json，profile 会被用户增删改、缓存即过期）；
// 打分复用 internal/match。执行 LaunchTerminalProfile（App Activation + --profile "<guid>"）
// 在 app 层 dispatcher。第三条… is_global、无前缀（直接打 profile 名命中，对齐 PTR Terminal）。
package terminal

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/zyition/voke/poc/voke-go/internal/match"
	"github.com/zyition/voke/poc/voke-go/internal/providers"
)

const (
	ID = "terminal"

	maxResults = 50
	// 空 commandline 时的兜底副标题。
	noCommandlineSubtitle = "Windows Terminal 配置"
)

// Profile 是一条 WT profile（合并去重后的形态）。
type Profile struct {
	Name        string
	Commandline string
	GUID        string
	IsDefault   bool
	AUMID       string // 来源包 AUMID（profilesFromSettings 按文件盖章）
}

// SettingsSource 是一份 settings.json 原文 + 所属 WT 包的 AUMID。
type SettingsSource struct {
	Contents string
	AUMID    string
}

// Fetcher 是 profile 数据源：每次调用都应返回当前的合并后列表。
type Fetcher func() []Profile

// indexedProfile 是带缓存搜索键的 profile（name / commandline 各一份）。
type indexedProfile struct {
	entry       Profile
	nameKeys    *match.SearchKeys
	commandKeys *match.SearchKeys
}

// Provider 实现 providers.Provider。
type Provider struct {
	config providers.ProviderConfig
	fetch  Fetcher
	wtPath string // wt.exe 别名路径（App Activation 失败兜底直启用）
	keys   *match.SearchKeysCache
}

var _ providers.Provider = (*Provider)(nil)

// New 用数据源 + wt.exe 路径构造。
func New(fetch Fetcher, wtPath string) *Provider {
	if fetch == nil {
		fetch = func() []Profile { return nil }
	}
	return &Provider{
		config: providers.ProviderConfig{
			Enabled: true, IsGlobal: true, Weight: 1.0,
			Prefix: "", Activation: "Direct", Count: 5,
		},
		fetch:  fetch,
		wtPath: wtPath,
		keys:   match.NewSearchKeysCache(),
	}
}

// NewEmpty 返回空数据源实例（非 Windows 平台 / 未读配置占位）。
func NewEmpty() *Provider { return New(func() []Profile { return nil }, "") }

func (p *Provider) ID() string        { return ID }
func (p *Provider) Name() string      { return "终端配置" }
func (p *Provider) Aliases() []string { return []string{"wt", "terminal"} }
func (p *Provider) Description() string {
	return "用 Windows Terminal 打开某个配置（profile）"
}
func (p *Provider) Config() providers.ProviderConfig     { return p.config }
func (p *Provider) SetConfig(c providers.ProviderConfig) { p.config = c }

func (p *Provider) Placeholder(string) string { return "输入配置名过滤，Enter 用 wt 启动" }

// Query 每次重读数据源；空查询全返回 score=1.0（保 default 置顶 + stable 优先），
// 非空按 name/commandline 取最高分排序、截断 maxResults。
// RegeneratesOnQuery：profile 每次现读配置，结果跟随数据而非重跑即变 → false。
func (p *Provider) RegeneratesOnQuery() bool { return false }

func (p *Provider) Query(ctx providers.QueryCtx) []providers.Match {
	profiles := p.indexAll()

	query := strings.TrimSpace(ctx.Query)
	if query == "" {
		out := make([]providers.Match, 0, len(profiles))
		for i := range profiles {
			out = append(out, p.toMatch(profiles[i], 1.0))
		}
		return out
	}

	m := match.NewMatcher()
	out := make([]providers.Match, 0, len(profiles))
	for i := range profiles {
		if s, ok := p.score(m, query, profiles[i]); ok {
			out = append(out, p.toMatch(profiles[i], float64(s)))
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Score > out[b].Score })
	if len(out) > maxResults {
		out = out[:maxResults]
	}
	return out
}

func (p *Provider) indexAll() []indexedProfile {
	entries := p.fetch()
	out := make([]indexedProfile, 0, len(entries))
	for _, e := range entries {
		out = append(out, indexedProfile{
			entry:       e,
			nameKeys:    p.keys.Get(e.Name),
			commandKeys: p.keys.Get(e.Commandline),
		})
	}
	return out
}

func (p *Provider) score(m *match.Matcher, query string, prof indexedProfile) (float32, bool) {
	name, okN := match.ScoreEntry(m, query, prof.nameKeys)
	command, okC := match.ScoreEntry(m, query, prof.commandKeys)
	switch {
	case okN && okC:
		return maxF32(name.Score, command.Score), true
	case okN:
		return name.Score, true
	case okC:
		return command.Score, true
	default:
		return 0, false
	}
}

// toMatch：动作 LaunchTerminalProfile{aumid, profile, fallback_path}，profile 用
// guid（缺失退化显示名）。副标题：commandline（空则兜底），default 再加「默认配置 · 」。
func (p *Provider) toMatch(prof indexedProfile, score float64) providers.Match {
	profileID := prof.entry.GUID
	if profileID == "" {
		profileID = prof.entry.Name
	}
	subtitle := prof.entry.Commandline
	if subtitle == "" {
		subtitle = noCommandlineSubtitle
	}
	if prof.entry.IsDefault {
		subtitle = fmt.Sprintf("默认配置 · %s", subtitle)
	}
	return providers.Match{
		ProviderID: ID,
		Title:      prof.entry.Name,
		Subtitle:   subtitle,
		Action: providers.Action{
			Kind:         providers.ActionLaunchTerminalProfile,
			Aumid:        prof.entry.AUMID,
			Profile:      profileID,
			FallbackPath: p.wtPath,
		},
		Score:    score,
		Interact: providers.InteractSpec{Kind: providers.InteractExecute},
	}
}

// StripJSONCComments 剥掉 `//` 行注释与 `/* */` 块注释，字符串字面量原样保留；
// 注释替换成等长空白（换行保留，行号不漂）。对照 Rust `strip_jsonc_comments`。
func StripJSONCComments(input string) string {
	var out strings.Builder
	out.Grow(len(input))
	runes := []rune(input)
	inString := false
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		if inString {
			out.WriteRune(c)
			switch c {
			case '\\':
				if i+1 < len(runes) {
					i++
					out.WriteRune(runes[i])
				}
			case '"':
				inString = false
			}
			continue
		}
		switch {
		case c == '"':
			inString = true
			out.WriteRune(c)
		case c == '/' && i+1 < len(runes) && runes[i+1] == '/':
			out.WriteRune(' ')
			for i+1 < len(runes) && runes[i+1] != '\n' {
				i++
				out.WriteRune(' ')
			}
		case c == '/' && i+1 < len(runes) && runes[i+1] == '*':
			out.WriteRune(' ')
			i++ // 吃掉 '*'
			out.WriteRune(' ')
			prev := rune(0)
			for i+1 < len(runes) {
				i++
				n := runes[i]
				if prev == '*' && n == '/' {
					out.WriteRune(' ')
					break
				}
				if n == '\n' {
					out.WriteRune('\n')
				} else {
					out.WriteRune(' ')
				}
				prev = n
			}
		default:
			out.WriteRune(c)
		}
	}
	return out.String()
}

// parseSettings 从一份 settings.json 原文解析 profile 列表（未合并、未置顶）。
// 先直接解析，失败则去注释重试；仍失败或缺 profiles → 空列表（绝不 panic）。
func parseSettings(raw string) []Profile {
	var v map[string]any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		if err2 := json.Unmarshal([]byte(StripJSONCComments(raw)), &v); err2 != nil {
			return nil
		}
	}

	var defaultGUID string
	if s, ok := v["defaultProfile"].(string); ok {
		defaultGUID = s
	}

	list := extractProfileList(v["profiles"])
	out := make([]Profile, 0, len(list))
	for _, item := range list {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name := strings.TrimSpace(asString(entry["name"]))
		if name == "" {
			continue
		}
		if hidden, ok := entry["hidden"].(bool); ok && hidden {
			continue
		}
		guid := asString(entry["guid"])
		prof := Profile{
			Name:        name,
			Commandline: asString(entry["commandline"]),
			GUID:        guid,
			IsDefault:   guid != "" && guid == defaultGUID,
		}
		out = append(out, prof)
	}
	return out
}

// extractProfileList：现行 profiles 是对象 {defaults, list}；远古版本直接是数组。
func extractProfileList(profiles any) []any {
	switch p := profiles.(type) {
	case map[string]any:
		if list, ok := p["list"].([]any); ok {
			return list
		}
	case []any:
		return p
	}
	return nil
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// ProfilesFromSettings 把若干份 WT 设置（stable 在前、Preview 在后）合并去重、default 置顶，
// 每个条目盖上来源文件的 AUMID。对照 Rust `profiles_from_settings`。
func ProfilesFromSettings(sources []SettingsSource) []Profile {
	var merged []Profile
	for _, src := range sources {
		for _, prof := range parseSettings(src.Contents) {
			prof.AUMID = src.AUMID
			dup := false
			for _, existing := range merged {
				if existing.Name == prof.Name {
					dup = true
					break
				}
			}
			if dup {
				continue
			}
			merged = append(merged, prof)
		}
	}
	// default 置顶（stable 排序，非 default 相对顺序不变）。
	sort.SliceStable(merged, func(i, j int) bool {
		return merged[i].IsDefault && !merged[j].IsDefault
	})
	return merged
}

// ProfileActivationArguments 是 WT profile 的 App Activation 参数字串
// （CmdPal 同款：`--profile "<profile>"`，引号保留——显示名可能含空格）。
func ProfileActivationArguments(profile string) string {
	return fmt.Sprintf("--profile \"%s\"", profile)
}

func maxF32(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}
