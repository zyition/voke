// workspaces.go：workspaces provider 的 sys 半边——**只读**打开编辑器历史 SQLite 库并取原始行
// （对照 voke-sys/src/workspace.rs）。解析 / 脏数据过滤 / 排序 / 去重不在本层，那是
// internal/providers/workspaces 的纯逻辑（Workspaces / ZedEntries / VsCodeEntries，已测）；
// 本层只「把库里的字节变成结构化行」。
//
// 与 terminal/windows 的 sys 件不同：这里**不挂 build 标签**——驱动是纯 Go 的
// github.com/glebarez/go-sqlite（modernc 移植），Linux 与 Windows 都能编能跑，故
// 「建真 SQLite fixture 读回」的测试在 WSL 就有真覆盖（这正是当初绕开 cgo mattn/go-sqlite3
// 选纯 Go 驱动的理由：交叉编译无痛 + Linux 可测）。
package platform

import (
	"database/sql"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	_ "github.com/glebarez/go-sqlite" // database/sql 驱动 "sqlite"（modernc 纯 Go 移植）
)

// benignSkipLogged 去重「每次查询都会重演」的良性跳过告警（如 Zed 库缺 workspaces 表）：
// 每个 key 只在本进程首次命中时落一行。生产 Rust 侧这类是 `log::debug!`（正常日志级别过滤掉），
// Go 的 stdlib log 无级别，若照原样 `log.Printf` 会在每次唤起面板时刷屏——这里以「每路径一次」
// 逼近生产「正常日志里不出现这堆噪音」的可观测结果，同时保留首行可诊断。
var benignSkipLogged sync.Map

func warnBenignSkipOnce(key, format string, args ...any) {
	if _, loaded := benignSkipLogged.LoadOrStore(key, struct{}{}); loaded {
		return
	}
	log.Printf(format, args...)
}

const (
	zedStableChannel = "0-stable"
	zedDBFile        = "db.sqlite"
)

// ZedRemote 是 Zed remote_connections 一行（LEFT JOIN 未接上时整块为 nil）。
type ZedRemote struct {
	Kind        string
	Host        *string
	Port        *int64
	User        *string
	Distro      *string
	ContainerID *string
}

// ZedRow 是 Zed workspaces LEFT JOIN remote_connections 的一行原始数据。
type ZedRow struct {
	Paths     string
	Timestamp string
	Remote    *ZedRemote
}

// VsCodeRecents 是 VS Code state.vscdb 里 recentlyOpenedPathsList 的 JSON 原文 + 库 mtime。
type VsCodeRecents struct {
	JSON          string
	FileTimestamp string
}

// ZedDBDir：%LOCALAPPDATA%\Zed\db（纯拼路径）。
func ZedDBDir(localAppData string) string {
	return filepath.Join(localAppData, "Zed", "db")
}

// ZedDBPaths：db\*\db.sqlite 全部通道，**stable 在前**（同路径去重时保留靠前者）。
// 目录不存在 / 无 db.sqlite 的通道跳过。
func ZedDBPaths(zedDB string) []string {
	entries, err := os.ReadDir(zedDB)
	if err != nil {
		return nil
	}
	dirs := make([]string, 0, len(entries))
	for _, e := range entries {
		dirs = append(dirs, filepath.Join(zedDB, e.Name()))
	}
	sort.Strings(dirs) // 名字序保证稳定
	// stable 提到最前：key=false（是 stable）排在 true 前。
	sort.SliceStable(dirs, func(i, j int) bool {
		return filepath.Base(dirs[i]) == zedStableChannel
	})
	var out []string
	for _, d := range dirs {
		if p := filepath.Join(d, zedDBFile); fileExists(p) {
			out = append(out, p)
		}
	}
	return out
}

// VsCodeDBPaths：候选 state.vscdb，**共享库在前**（≥1.118 的真实来源），再兜底 globalStorage。
// 纯拼路径、不查存在性（不存在由读取方跳过）。
func VsCodeDBPaths(userProfile, appData string) []string {
	shared := filepath.Join(userProfile, ".vscode-shared", "sharedStorage", "state.vscdb")
	global := filepath.Join(appData, "Code", "User", "globalStorage", "state.vscdb")
	return []string{shared, global}
}

// ReadZedWorkspaces 读全部 Zed 通道库的 workspaces 行（stable 在前）。LOCALAPPDATA 缺失→nil。
func ReadZedWorkspaces() []ZedRow {
	lad := os.Getenv("LOCALAPPDATA")
	if lad == "" {
		return nil
	}
	return zedRowsFrom(ZedDBPaths(ZedDBDir(lad)))
}

// ReadVsCodeRecents 读 VS Code 候选库的最近打开 JSON（有键者返回）。
func ReadVsCodeRecents() []VsCodeRecents {
	up, ad := os.Getenv("USERPROFILE"), os.Getenv("APPDATA")
	if up == "" && ad == "" {
		return nil
	}
	var out []VsCodeRecents
	for _, p := range VsCodeDBPaths(up, ad) {
		if pr, ok := vscodeRecentsIn(p); ok {
			out = append(out, pr)
		}
	}
	return out
}

func zedRowsFrom(paths []string) []ZedRow {
	var out []ZedRow
	for _, p := range paths {
		out = append(out, zedRowsIn(p)...)
	}
	return out
}

const zedSQL = `SELECT w.paths, w.timestamp, r.kind, r.host, r.port, r.user, r.distro, r.container_id
    FROM workspaces w
    LEFT JOIN remote_connections r ON r.id = w.remote_connection_id
    WHERE w.paths IS NOT NULL
    ORDER BY w.timestamp DESC`

// zedRowsIn：只读打开一个 Zed 库跑 ZED_SQL；库打不开 / 缺表 / 非 SQLite 一律返回空（不 panic）。
func zedRowsIn(path string) []ZedRow {
	db := openReadOnly(path)
	if db == nil {
		return nil
	}
	defer db.Close()
	rows, err := db.Query(zedSQL)
	if err != nil {
		// 缺表 / 空库 / 非 SQLite：良性，每次查询都会重演 → 每路径只记一次（对照 Rust debug）。
		warnBenignSkipOnce("zed:"+path, "voke：Zed 历史库缺表或读取失败，跳过：%s：%v", path, err)
		return nil
	}
	defer rows.Close()
	var out []ZedRow
	for rows.Next() {
		var (
			pathsS, tsS         sql.NullString
			kindS, hostS, userS sql.NullString
			distroS, containerS sql.NullString
			portS               sql.NullInt64
		)
		if err := rows.Scan(&pathsS, &tsS, &kindS, &hostS, &portS, &userS, &distroS, &containerS); err != nil {
			continue
		}
		r := ZedRow{Paths: pathsS.String, Timestamp: tsS.String}
		if kindS.Valid { // r.kind 非 NULL ⇒ 有 remote_connection（LEFT JOIN 接上了）
			r.Remote = &ZedRemote{
				Kind:        kindS.String,
				Host:        nullStrPtr(hostS),
				Port:        nullInt64Ptr(portS),
				User:        nullStrPtr(userS),
				Distro:      nullStrPtr(distroS),
				ContainerID: nullStrPtr(containerS),
			}
		}
		out = append(out, r)
	}
	return out
}

const vscodeSQL = `SELECT value FROM ItemTable WHERE key = 'history.recentlyOpenedPathsList'`

// vscodeRecentsIn：只读打开一个 state.vscdb，取 recentlyOpenedPathsList 原文 + mtime。
// 无此键 / 打不开 → ok=false。
func vscodeRecentsIn(path string) (VsCodeRecents, bool) {
	db := openReadOnly(path)
	if db == nil {
		return VsCodeRecents{}, false
	}
	defer db.Close()
	var raw string
	if err := db.QueryRow(vscodeSQL).Scan(&raw); err != nil {
		return VsCodeRecents{}, false // 库里没这个键（老 globalStorage）等，静默跳过
	}
	return VsCodeRecents{JSON: raw, FileTimestamp: fileStamp(path)}, true
}

// openReadOnly：只读打开 SQLite 库，两级退化——先 `mode=ro`（能看 WAL、需 -shm 可写），
// 拿不到再 `immutable=1&mode=ro`（完全不动文件、看不到 WAL、时间戳可能略旧）。退化成「数据
// 略旧」好过「一条没有」。非文件 / 全失败返回 nil（常驻 launcher 绝不因此挂）。
func openReadOnly(path string) *sql.DB {
	if !fileExists(path) {
		return nil
	}
	base := readOnlyURI(path)
	for _, uri := range []string{base + "?mode=ro", base + "?immutable=1&mode=ro"} {
		db, err := sql.Open("sqlite", uri)
		if err != nil {
			continue
		}
		if err = db.Ping(); err != nil { // 强制此刻真开文件
			db.Close()
			continue
		}
		return db
	}
	log.Printf("voke：编辑器历史库无法只读打开，跳过：%s", path)
	return nil
}

// readOnlyURI：路径转 SQLite file: URI（`\`→`/`；% 先转义再 ?/#，避免二次解码）。
func readOnlyURI(path string) string {
	norm := strings.ReplaceAll(path, `\`, "/")
	norm = strings.ReplaceAll(norm, "%", "%25")
	norm = strings.ReplaceAll(norm, "?", "%3f")
	norm = strings.ReplaceAll(norm, "#", "%23")
	return "file:" + norm
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// fileStamp：库文件 mtime → "YYYY-MM-DD HH:MM:SS"（UTC，作跨库排序锚）。取不到返回空串。
func fileStamp(path string) string {
	st, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return st.ModTime().UTC().Format("2006-01-02 15:04:05")
}

func nullStrPtr(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	v := s.String
	return &v
}

func nullInt64Ptr(i sql.NullInt64) *int64 {
	if !i.Valid {
		return nil
	}
	v := i.Int64
	return &v
}

// ── 编辑器 CLI 发现（openAction 用编辑器打开工作区；对照 workspace.rs 的
// zed_command / vscode_command / first_existing）──────────────────────────────

// EditorCmd 是一条打开工作区的外部命令（neutral，main 侧映射成 workspaces.EditorCommand）。
// PreArgs 是为 Windows：VS Code 的 `code.cmd` 是批处理，CreateProcess 跑 .cmd 须经
// `cmd.exe /c <code.cmd>`，故 VS Code 命令 exe=COMSPEC、pre_args=["/c", 该 .cmd 全路径]。
type EditorCmd struct {
	Exe     string
	PreArgs []string
}

// Zed 的 GUI/CLI exe 候选（相对 %LOCALAPPDATA%）：bin\Zed.exe 是 CLI（控制台子系统，调用方
// 配 CREATE_NO_WINDOW），根目录 Zed.exe 是 GUI 主程序。Programs\Zed 下实测存在两者。
var zedExeCandidates = [][]string{
	{"Programs", "Zed", "bin", "Zed.exe"},
	{"Programs", "Zed", "Zed.exe"},
}

// VS Code CLI 候选（相对 %LOCALAPPDATA%）：是批处理 `bin\code.cmd`（非 exe）。stable / Insiders 两版。
var vscodeCliCandidates = [][]string{
	{"Programs", "Microsoft VS Code", "bin", "code.cmd"},
	{"Programs", "Microsoft VS Code - Insiders", "bin", "code.cmd"},
}

// firstExisting：候选里第一个真实存在的文件（都不存在返回 ""）。纯路径逻辑，Linux 可测。
func firstExisting(root string, candidates [][]string) string {
	for _, suffix := range candidates {
		p := root
		for _, part := range suffix {
			p = filepath.Join(p, part)
		}
		if fileExists(p) {
			return p
		}
	}
	return ""
}

// ZedCommand：Zed CLI 绝对路径命令；没装（LOCALAPPDATA 缺失 / 无候选文件）返回 nil。
func ZedCommand() *EditorCmd {
	lad := os.Getenv("LOCALAPPDATA")
	if lad == "" {
		return nil
	}
	if exe := firstExisting(lad, zedExeCandidates); exe != "" {
		return &EditorCmd{Exe: exe}
	}
	return nil
}

// VSCodeCommand：VS Code CLI 命令（包 cmd.exe /c code.cmd，理由见 EditorCmd）；没装返回 nil。
// exe 取 COMSPEC（缺省 cmd.exe）。
func VSCodeCommand() *EditorCmd {
	lad := os.Getenv("LOCALAPPDATA")
	if lad == "" {
		return nil
	}
	cli := firstExisting(lad, vscodeCliCandidates)
	if cli == "" {
		return nil
	}
	comspec := os.Getenv("COMSPEC")
	if comspec == "" {
		comspec = "cmd.exe"
	}
	return &EditorCmd{Exe: comspec, PreArgs: []string{"/c", cli}}
}
