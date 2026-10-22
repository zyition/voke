// Package workspaces 提供编辑器（Zed / VSCode）最近工作区历史的搜索与打开——
// 1:1 对照生产 `voke-core/src/workspace.rs`。
//
// 分层：纯逻辑（URI 解码 + 位置分类 + 脏数据过滤 + 显示名 + 排序去重 + provider
// 打分），零 mygo、零 SQLite、零文件系统。只读打开编辑器 SQLite 在 voke-sys，解析
// 语义全在这里可测（Linux 原生单测）。数据源是注入的枚举闭包（带 TTL 缓存，避免每
// 次按键打库），打分复用 internal/match。执行 Launch（用记录它的编辑器打开 / 退回
// 资源管理器）在 app 层 dispatcher。第五条 is_global、无前缀。
package workspaces

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"voke/internal/match"
	"voke/internal/providers"
)

const (
	ID = "workspaces"

	maxResults = 50
	// defaultTTL 缓存有效期（比目录扫描那版短：只读几个小库，代价低）。
	defaultTTL = 5 * time.Second
)

// EditorKind 是记录这条工作区历史的编辑器。
type EditorKind int

const (
	EditorZed EditorKind = iota
	EditorVsCode
)

func editorLabel(e EditorKind) string {
	if e == EditorVsCode {
		return "VSCode"
	}
	return "Zed"
}

// Location 是工作区实际在哪儿（不硬编码 WSL，认不出的落 other）。
type Location struct {
	Kind     string // local / wsl / ssh / container / other
	User     string // "" = 无
	Host     string // ssh
	Port     int64
	HasPort  bool
	Distro   string // wsl（"" = 无）
	ID       string // container id（"" = 无）
	RawKind  string // other：原始 kind
	Label    string // other：可选标签
	HasLabel bool
}

func localLoc() Location                  { return Location{Kind: "local"} }
func wslLoc(user, distro string) Location { return Location{Kind: "wsl", User: user, Distro: distro} }
func containerLoc(id string) Location     { return Location{Kind: "container", ID: id} }
func otherLocNoLabel(kind string) Location {
	return Location{Kind: "other", RawKind: kind}
}
func otherLoc(kind, label string) Location {
	return Location{Kind: "other", RawKind: kind, Label: label, HasLabel: label != ""}
}
func sshLoc(user, host string, port *int64) Location {
	l := Location{Kind: "ssh", User: user, Host: host}
	if port != nil {
		l.Port, l.HasPort = *port, true
	}
	return l
}

// label 是副标题里的位置标记（本地 / WSL distro / SSH user@host:port / 容器 / 其它）。
func (l Location) label() string {
	switch l.Kind {
	case "local":
		return "本地"
	case "wsl":
		if l.Distro != "" {
			return "WSL " + l.Distro
		}
		return "WSL"
	case "ssh":
		base := l.Host
		if l.User != "" {
			base = l.User + "@" + l.Host
		}
		if l.HasPort {
			return fmt.Sprintf("SSH %s:%d", base, l.Port)
		}
		return "SSH " + base
	case "container":
		if l.ID != "" {
			return "容器 " + l.ID
		}
		return "容器"
	default:
		if l.HasLabel {
			return "其它 " + l.Label
		}
		return "其它 " + l.RawKind
	}
}

// Entry 是一条工作区历史。OpenArgs 空 = 该位置不被编辑器 CLI 支持（退回资源管理器）。
type Entry struct {
	Name      string
	Path      string
	Editor    EditorKind
	Location  Location
	Timestamp string
	OpenArgs  []string
}

// ZedRemoteRow 是 sys 从 Zed remote_connections 读出的一行（Option 用指针表达）。
type ZedRemoteRow struct {
	Kind        string
	Host        *string
	Port        *int64
	User        *string
	Distro      *string
	ContainerID *string
}

// ZedWorkspaceRow 是 sys 从 Zed workspaces 读出的一行（remote_connections 已 LEFT JOIN）。
type ZedWorkspaceRow struct {
	Paths     string
	Timestamp string
	Remote    *ZedRemoteRow
}

// VsCodeRecentPayload 是 sys 从 state.vscdb 读出的 recentlyOpenedPathsList 原文 + 库 mtime。
type VsCodeRecentPayload struct {
	JSON          string
	FileTimestamp string
}

// EditorCommand 是打开工作区的外部命令。PreArgs 是为 Windows 批处理包装
// （code.cmd 须经 `cmd.exe /c` 跑）。
type EditorCommand struct {
	Exe     string
	PreArgs []string
}

// Fetcher 是工作区数据源。
type Fetcher func() []Entry

// ── 纯字符串工具 ──

func nonEmpty(v *string) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(*v)
}

func nonNullText(v *string) string {
	s := nonEmpty(v)
	if strings.EqualFold(s, "null") {
		return ""
	}
	return s
}

func hexDigit(b byte) (byte, bool) {
	switch {
	case b >= '0' && b <= '9':
		return b - '0', true
	case b >= 'a' && b <= 'f':
		return b - 'a' + 10, true
	case b >= 'A' && b <= 'F':
		return b - 'A' + 10, true
	}
	return 0, false
}

// PercentDecode 解码 %XX；`+` 当字面量；非法/残缺序列原样保留（不 panic）。
func PercentDecode(input string) string {
	bytesIn := []byte(input)
	out := make([]byte, 0, len(bytesIn))
	for i := 0; i < len(bytesIn); {
		if bytesIn[i] == '%' && i+2 < len(bytesIn) {
			if hi, ok1 := hexDigit(bytesIn[i+1]); ok1 {
				if lo, ok2 := hexDigit(bytesIn[i+2]); ok2 {
					out = append(out, hi*16+lo)
					i += 3
					continue
				}
			}
		}
		out = append(out, bytesIn[i])
		i++
	}
	return string(out)
}

func lastSegment(path string) string {
	trimmed := strings.TrimRight(path, "/\\")
	if trimmed == "" {
		return path
	}
	if seg := lastRsplit(trimmed); seg != "" {
		return seg
	}
	return trimmed
}

// lastRsplit 取按 / 或 \ 分隔的最后一段。
func lastRsplit(s string) string {
	if i := strings.LastIndexAny(s, "/\\"); i >= 0 {
		return s[i+1:]
	}
	return s
}

// DisplayName 取路径最后一段；`.code-workspace` 去扩展名。
func DisplayName(path string) string {
	seg := lastSegment(path)
	if stem := strings.TrimSuffix(seg, ".code-workspace"); stem != "" && stem != seg {
		return stem
	}
	return seg
}

// normalizeDriveLetter：`/c:/...` → `C:/...`（VSCode 本地 URI 盘符小写统一成大写）。
func normalizeDriveLetter(path string) string {
	b := []byte(path)
	if len(b) >= 3 && b[0] == '/' && isASCIILetter(b[1]) && b[2] == ':' {
		up := b[1]
		if up >= 'a' && up <= 'z' {
			up -= 'a' - 'A'
		}
		return string(up) + ":" + path[3:]
	}
	return path
}

func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// splitFirstSegment：把第一段当发行版名，剩下的当路径返回。
func splitFirstSegment(path string) (string, string) {
	trimmed := strings.TrimLeft(path, "/\\")
	if i := strings.IndexAny(trimmed, "/\\"); i >= 0 {
		return nonEmptyStr(trimmed[:i]), trimmed[i:]
	}
	return nonEmptyStr(trimmed), ""
}

func nonEmptyStr(s string) string {
	s = strings.TrimSpace(s)
	return s
}

// looksLikeFile：最后一段带扩展名（含 `.`、后缀 1–8 ASCII 字母数字、非隐藏目录）。
func looksLikeFile(path string) bool {
	seg := lastRsplit(path)
	if seg == "" || strings.HasPrefix(seg, ".") {
		return false
	}
	i := strings.LastIndex(seg, ".")
	if i < 0 {
		return false
	}
	stem, ext := seg[:i], seg[i+1:]
	if stem == "" || len(ext) < 1 || len(ext) > 8 {
		return false
	}
	for j := 0; j < len(ext); j++ {
		c := ext[j]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

func isSystemRoot(path string) bool {
	lower := strings.ToLower(path)
	win := strings.ReplaceAll(lower, "/", "\\")
	if win == `c:\` {
		return true
	}
	for _, p := range []string{`c:\windows`, `c:\program files`, `c:\programdata`, `c:\$recycle.bin`} {
		if strings.HasPrefix(win, p) {
			return true
		}
	}
	for _, p := range []string{"/proc", "/sys", "/dev", "/boot", "/etc"} {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return lower == "/"
}

// IsDirtyPath：空串 / 系统目录 / 看起来是文件。只做字符串判据（不 stat）。
func IsDirtyPath(path string) bool {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return true
	}
	return isSystemRoot(trimmed) || looksLikeFile(trimmed)
}

// ── VSCode：URI 与 entries JSON ──

func splitURI(uri string) (scheme, authority, path string, ok bool) {
	i := strings.Index(uri, "://")
	if i < 0 {
		return "", "", "", false
	}
	scheme, rest := uri[:i], uri[i+3:]
	if scheme == "" || strings.ContainsAny(scheme, "/\\") {
		return "", "", "", false
	}
	if j := strings.Index(rest, "/"); j >= 0 {
		return scheme, rest[:j], rest[j:], true
	}
	return scheme, rest, "", true
}

// VsCodeLocation：remoteAuthority → 位置类型。
func VsCodeLocation(remoteAuthority string) Location {
	raw := strings.TrimSpace(remoteAuthority)
	if raw == "" {
		return localLoc()
	}
	kind, spec := raw, ""
	if i := strings.Index(raw, "+"); i >= 0 {
		kind, spec = raw[:i], raw[i+1:]
	}
	spec = nonEmptyStr(PercentDecode(spec))
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "wsl":
		return wslLoc("", spec)
	case "ssh-remote":
		var user, hostPort string
		if i := strings.LastIndex(spec, "@"); i >= 0 {
			user, hostPort = nonEmptyStr(spec[:i]), spec[i+1:]
		} else {
			user, hostPort = "", spec
		}
		var port *int64
		host := hostPort
		if i := strings.LastIndex(hostPort, ":"); i >= 0 {
			p := hostPort[i+1:]
			if p != "" && allDigits(p) {
				host = hostPort[:i]
				if v, err := strconv.ParseInt(p, 10, 64); err == nil {
					port = &v
				}
			}
		}
		return sshLoc(user, host, port)
	case "dev-container", "attached-container", "docker":
		return containerLoc(spec)
	default:
		return otherLoc(strings.ToLower(strings.TrimSpace(kind)), spec)
	}
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// VsCodeURI：URI → (位置类型, 可展示路径)。非 URI 原样当本地路径。
func VsCodeURI(uri string) (Location, string) {
	scheme, authority, rawPath, ok := splitURI(uri)
	if !ok {
		return localLoc(), uri
	}
	path := PercentDecode(rawPath)
	switch scheme {
	case "vscode-remote":
		return VsCodeLocation(PercentDecode(authority)), path
	case "file":
		host := strings.ToLower(authority)
		switch {
		case host == "":
			return localLoc(), normalizeDriveLetter(path)
		case host == "wsl.localhost" || host == "wsl$":
			distro, rest := splitFirstSegment(path)
			return wslLoc("", distro), rest
		default:
			return otherLoc("unc", authority), path
		}
	default:
		return otherLocNoLabel(scheme), path
	}
}

type vsRecentPayload struct {
	Entries []vsRecentEntry `json:"entries"`
}

type vsRecentEntry struct {
	FolderURI *string `json:"folderUri"`
	FileURI   *string `json:"fileUri"`
	Workspace *struct {
		ConfigPath *string `json:"configPath"`
	} `json:"workspace"`
	RemoteAuthority *string `json:"remoteAuthority"`
}

const (
	vsKindFolder = iota
	vsKindWorkspace
	vsKindFile
)

func vscodeEntryKind(e vsRecentEntry) (int, string, bool) {
	if s := nonEmpty(e.FolderURI); s != "" {
		return vsKindFolder, s, true
	}
	if e.Workspace != nil {
		if s := nonEmpty(e.Workspace.ConfigPath); s != "" {
			return vsKindWorkspace, s, true
		}
	}
	if e.FileURI != nil {
		return vsKindFile, "", true
	}
	return 0, "", false
}

func vscodeOpenFlag(kind int) string {
	if kind == vsKindWorkspace {
		return "--file-uri"
	}
	return "--folder-uri"
}

func vscodeOpenArgs(kind int, uri string) []string {
	if strings.TrimSpace(uri) == "" {
		return nil
	}
	return []string{vscodeOpenFlag(kind), uri}
}

func vscodeEntry(entry vsRecentEntry, fileTimestamp string) (Entry, bool) {
	kind, uri, ok := vscodeEntryKind(entry)
	if !ok || kind == vsKindFile {
		return Entry{}, false
	}
	fromURI, path := VsCodeURI(uri)
	location := fromURI
	if auth := nonEmpty(entry.RemoteAuthority); auth != "" {
		location = VsCodeLocation(auth)
	}
	if IsDirtyPath(path) {
		return Entry{}, false
	}
	return Entry{
		Name:      DisplayName(path),
		Path:      path,
		Editor:    EditorVsCode,
		Location:  location,
		Timestamp: fileTimestamp,
		OpenArgs:  vscodeOpenArgs(kind, uri),
	}, true
}

func parseRecentEntries(jsonStr string) []vsRecentEntry {
	var payload vsRecentPayload
	if err := json.Unmarshal([]byte(jsonStr), &payload); err != nil {
		return nil
	}
	return payload.Entries
}

// VsCodeEntries：VSCode 历史 → 工作区条目（过滤 / 排序 / 去重）。
func VsCodeEntries(payloads []VsCodeRecentPayload) []Entry {
	var out []Entry
	for _, payload := range payloads {
		for _, e := range parseRecentEntries(payload.JSON) {
			if entry, ok := vscodeEntry(e, payload.FileTimestamp); ok {
				out = append(out, entry)
			}
		}
	}
	return finalize(out)
}

// ── Zed ──

func zedRowPaths(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	var arr []string
	if err := json.Unmarshal([]byte(trimmed), &arr); err == nil {
		var out []string
		for _, p := range arr {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	var out []string
	for _, line := range strings.Split(trimmed, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	if len(out) == 0 {
		return []string{trimmed}
	}
	return out
}

// ZedLocation：Zed remote_connections 一行 → 位置类型（认不出的落 other）。
func ZedLocation(remote ZedRemoteRow) Location {
	kind := strings.ToLower(strings.TrimSpace(remote.Kind))
	host := nonNullText(remote.Host)
	user := nonNullText(remote.User)
	distro := nonEmpty(remote.Distro)
	containerID := nonEmpty(remote.ContainerID)
	switch kind {
	case "wsl":
		return wslLoc(user, distro)
	case "ssh":
		if host != "" {
			return sshLoc(user, host, remote.Port)
		}
		return otherLoc(kind, user)
	case "dev-container", "docker", "container":
		return containerLoc(containerID)
	default:
		label := host
		if label == "" {
			label = distro
		}
		if label == "" {
			label = containerID
		}
		return otherLoc(kind, label)
	}
}

func zedOpenArgs(location Location, path string) []string {
	switch location.Kind {
	case "local":
		return []string{path}
	case "wsl":
		if location.Distro == "" {
			return nil
		}
		target := location.Distro
		if location.User != "" {
			target = location.User + "@" + location.Distro
		}
		return []string{"--wsl", target, path}
	case "ssh":
		if location.Host == "" {
			return nil
		}
		auth := ""
		if location.User != "" {
			auth = location.User + "@"
		}
		port := ""
		if location.HasPort {
			port = ":" + strconv.FormatInt(location.Port, 10)
		}
		p := path
		if !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
		return []string{"ssh://" + auth + location.Host + port + p}
	default:
		return nil
	}
}

func zedRowEntries(row ZedWorkspaceRow) []Entry {
	location := localLoc()
	if row.Remote != nil {
		location = ZedLocation(*row.Remote)
	}
	var out []Entry
	for _, path := range zedRowPaths(row.Paths) {
		if IsDirtyPath(path) {
			continue
		}
		out = append(out, Entry{
			Name:      DisplayName(path),
			Path:      path,
			Editor:    EditorZed,
			Location:  location,
			Timestamp: row.Timestamp,
			OpenArgs:  zedOpenArgs(location, path),
		})
	}
	return out
}

// ZedEntries：Zed 历史 → 工作区条目（过滤 / 排序 / 去重）。
func ZedEntries(rows []ZedWorkspaceRow) []Entry {
	var out []Entry
	for _, row := range rows {
		out = append(out, zedRowEntries(row)...)
	}
	return finalize(out)
}

// Workspaces：两个来源合并的统一出口（解析 → 脏数据过滤 → 排序 → 去重）。
func Workspaces(zed []ZedWorkspaceRow, vscode []VsCodeRecentPayload) []Entry {
	var out []Entry
	for _, row := range zed {
		out = append(out, zedRowEntries(row)...)
	}
	for _, payload := range vscode {
		for _, e := range parseRecentEntries(payload.JSON) {
			if entry, ok := vscodeEntry(e, payload.FileTimestamp); ok {
				out = append(out, entry)
			}
		}
	}
	return finalize(out)
}

func finalize(entries []Entry) []Entry {
	var kept []Entry
	for _, e := range entries {
		if !IsDirtyPath(e.Path) {
			kept = append(kept, e)
		}
	}
	sortWorkspaces(kept)
	return dedupeWorkspaces(kept)
}

// sortWorkspaces：时间戳降序、空时间戳排最后、同组保持插入顺序（稳定排序）。
func sortWorkspaces(entries []Entry) {
	sort.SliceStable(entries, func(a, b int) bool {
		ae, be := entries[a].Timestamp == "", entries[b].Timestamp == ""
		switch {
		case ae && be:
			return false
		case ae:
			return false // 空的排后面
		case be:
			return true
		default:
			return entries[a].Timestamp > entries[b].Timestamp
		}
	})
}

// dedupeWorkspaces：同 (编辑器, 路径) 去重，保留排序靠前（更新）的那条。
func dedupeWorkspaces(entries []Entry) []Entry {
	type key struct {
		editor EditorKind
		path   string
	}
	seen := map[key]bool{}
	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		k := key{e.Editor, e.Path}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, e)
	}
	return out
}

// ── provider ──

type indexedWorkspace struct {
	entry    Entry
	nameKeys *match.SearchKeys
	pathKeys *match.SearchKeys
}

// Provider 实现 providers.Provider。
type Provider struct {
	config providers.ProviderConfig
	fetch  Fetcher
	zed    *EditorCommand
	code   *EditorCommand
	ttl    time.Duration

	mu         sync.Mutex
	cacheValid bool
	cacheAt    time.Time
	cacheData  []Entry

	keys *match.SearchKeysCache
}

var _ providers.Provider = (*Provider)(nil)

// New 用数据源 + 两个编辑器命令构造（默认 TTL）。
func New(fetch Fetcher, zed, code *EditorCommand) *Provider {
	return WithTTL(fetch, zed, code, defaultTTL)
}

// WithTTL 显式指定 TTL（测试用 0 = 每次重读）。
func WithTTL(fetch Fetcher, zed, code *EditorCommand, ttl time.Duration) *Provider {
	if fetch == nil {
		fetch = func() []Entry { return nil }
	}
	return &Provider{
		config: providers.ProviderConfig{
			Enabled: true, IsGlobal: true, Weight: 1.0,
			Prefix: "", Activation: "Direct", Count: 5,
		},
		fetch: fetch, zed: zed, code: code, ttl: ttl,
		keys: match.NewSearchKeysCache(),
	}
}

// NewEmpty 返回空数据源实例（非 Windows 平台 / 未读到编辑器历史占位）。
func NewEmpty() *Provider { return WithTTL(func() []Entry { return nil }, nil, nil, defaultTTL) }

func (p *Provider) ID() string        { return ID }
func (p *Provider) Name() string      { return "工作区" }
func (p *Provider) Aliases() []string { return []string{"ws", "gzq"} }
func (p *Provider) Description() string {
	return "Zed / VSCode 最近打开的工作区，用记录它的编辑器打开"
}
func (p *Provider) Config() providers.ProviderConfig     { return p.config }
func (p *Provider) SetConfig(c providers.ProviderConfig) { p.config = c }

func (p *Provider) Placeholder(string) string {
	return "输入工作区名过滤，Enter 用对应编辑器打开"
}

// entries：TTL 内用缓存，过期重读并更新缓存。
func (p *Provider) entries() []Entry {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cacheValid && time.Since(p.cacheAt) < p.ttl {
		return append([]Entry{}, p.cacheData...)
	}
	entries := p.fetch()
	p.cacheData = append([]Entry{}, entries...)
	p.cacheAt = time.Now()
	p.cacheValid = true
	return entries
}

// Query：空查询全返回 score=1.0（保留排序），非空按 name/path 取最高分排序、截断。
// RegeneratesOnQuery：工作区历史带 TTL 缓存现读，结果跟随数据而非重跑即变 → false。
func (p *Provider) RegeneratesOnQuery() bool { return false }

func (p *Provider) Query(ctx providers.QueryCtx) []providers.Match {
	workspaces := p.indexAll()

	query := strings.TrimSpace(ctx.Query)
	if query == "" {
		out := make([]providers.Match, 0, len(workspaces))
		for i := range workspaces {
			out = append(out, p.toMatch(workspaces[i], 1.0))
		}
		return out
	}

	m := match.NewMatcher()
	out := make([]providers.Match, 0, len(workspaces))
	for i := range workspaces {
		if s, ok := p.score(m, query, workspaces[i]); ok {
			out = append(out, p.toMatch(workspaces[i], float64(s)))
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Score > out[b].Score })
	if len(out) > maxResults {
		out = out[:maxResults]
	}
	return out
}

func (p *Provider) indexAll() []indexedWorkspace {
	entries := p.entries()
	out := make([]indexedWorkspace, 0, len(entries))
	for _, e := range entries {
		out = append(out, indexedWorkspace{
			entry:    e,
			nameKeys: p.keys.Get(e.Name),
			pathKeys: p.keys.Get(e.Path),
		})
	}
	return out
}

func (p *Provider) score(m *match.Matcher, query string, w indexedWorkspace) (float32, bool) {
	name, okN := match.ScoreEntry(m, query, w.nameKeys)
	path, okP := match.ScoreEntry(m, query, w.pathKeys)
	switch {
	case okN && okP:
		return maxF32(name.Score, path.Score), true
	case okN:
		return name.Score, true
	case okP:
		return path.Score, true
	default:
		return 0, false
	}
}

// openAction：用记录它的编辑器打开（Launch{exe, pre_args + open_args}）；编辑器不可用
// 或位置不支持（open_args 空）→ Launch{path}，dispatcher 转交资源管理器。
func (p *Provider) openAction(entry Entry) providers.Action {
	var cmd *EditorCommand
	if entry.Editor == EditorZed {
		cmd = p.zed
	} else {
		cmd = p.code
	}
	if cmd != nil && len(entry.OpenArgs) > 0 {
		args := append(append([]string{}, cmd.PreArgs...), entry.OpenArgs...)
		return providers.Action{Kind: providers.ActionLaunch, Path: cmd.Exe, Args: args, NoConsole: true}
	}
	return providers.Action{Kind: providers.ActionLaunch, Path: entry.Path, NoConsole: false}
}

func (p *Provider) toMatch(w indexedWorkspace, score float64) providers.Match {
	subtitle := fmt.Sprintf("%s · %s · %s", editorLabel(w.entry.Editor), w.entry.Location.label(), w.entry.Path)
	return providers.Match{
		ProviderID: ID,
		Title:      w.entry.Name,
		Subtitle:   subtitle,
		Action:     p.openAction(w.entry),
		Score:      score,
		Interact:   providers.InteractSpec{Kind: providers.InteractExecute},
	}
}

func maxF32(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}
