package platform

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/glebarez/go-sqlite"
)

// mustSQL 建/开一个 SQLite 库并跑语句（fixture 用，readwrite）。
func mustSQL(t *testing.T, path string, stmts ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("exec %q：%v", s, err)
		}
	}
}

func TestZedDBPathsStableFirst(t *testing.T) {
	db := filepath.Join(t.TempDir(), "db")
	for _, ch := range []string{"0-preview", "0-stable", "0-global"} {
		mustSQL(t, filepath.Join(db, ch, zedDBFile), "CREATE TABLE workspaces (paths TEXT)")
	}
	// 空通道目录（无 db.sqlite）不进结果。
	if err := os.MkdirAll(filepath.Join(db, "0-empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := ZedDBPaths(db)
	if len(got) != 3 {
		t.Fatalf("应 3 个库（空通道挡掉），实得 %v", got)
	}
	if filepath.Base(filepath.Dir(got[0])) != "0-stable" {
		t.Errorf("stable 应排最前，实得 %q", got[0])
	}
}

func TestReadZedWorkspacesRows(t *testing.T) {
	lad := t.TempDir()
	zedDB := filepath.Join(lad, "Zed", "db", "0-stable", zedDBFile)
	mustSQL(t, zedDB,
		`CREATE TABLE remote_connections (id INTEGER PRIMARY KEY, kind TEXT, host TEXT, port INTEGER, user TEXT, distro TEXT, container_id TEXT)`,
		`CREATE TABLE workspaces (paths TEXT, timestamp TEXT, remote_connection_id INTEGER REFERENCES remote_connections(id))`,
		`INSERT INTO remote_connections (id, kind, host, port, user) VALUES (1, 'ssh', 'example.com', 22, 'root')`,
		// 本地工作区（remote_connection_id NULL）+ ssh 工作区，后者 timestamp 更新。
		`INSERT INTO workspaces (paths, timestamp, remote_connection_id) VALUES ('["/home/me/proj"]', '2026-01-01 00:00:00', NULL)`,
		`INSERT INTO workspaces (paths, timestamp, remote_connection_id) VALUES ('["/srv/app"]', '2026-06-01 00:00:00', 1)`,
	)
	t.Setenv("LOCALAPPDATA", lad)

	rows := ReadZedWorkspaces()
	if len(rows) != 2 {
		t.Fatalf("应 2 行（timestamp 降序），实得 %v", rows)
	}
	// 第一行 timestamp 最新 = ssh。
	if rows[0].Paths != `["/srv/app"]` || rows[0].Remote == nil || rows[0].Remote.Kind != "ssh" {
		t.Errorf("首行应是 ssh 工作区：%+v", rows[0])
	}
	if rows[0].Remote.Host == nil || *rows[0].Remote.Host != "example.com" ||
		rows[0].Remote.Port == nil || *rows[0].Remote.Port != 22 {
		t.Errorf("ssh remote 字段错：%+v", rows[0].Remote)
	}
	// 第二行本地：Remote nil。
	if rows[1].Paths != `["/home/me/proj"]` || rows[1].Remote != nil {
		t.Errorf("次行应是本地工作区（Remote nil）：%+v", rows[1])
	}
}

func TestReadVsCodeRecents(t *testing.T) {
	up := t.TempDir()
	ad := t.TempDir()
	vscdb := filepath.Join(up, ".vscode-shared", "sharedStorage", "state.vscdb")
	const recentJSON = `{"entries":[{"folderUri":"file:///w/alpha"},{"workspace":{"id":"x"}}]}`
	mustSQL(t, vscdb,
		`CREATE TABLE ItemTable (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB)`,
		`INSERT INTO ItemTable (key, value) VALUES ('history.recentlyOpenedPathsList', '`+recentJSON+`')`,
	)
	t.Setenv("USERPROFILE", up)
	t.Setenv("APPDATA", ad)

	got := ReadVsCodeRecents()
	if len(got) != 1 {
		t.Fatalf("应读到 1 个共享库 payload，实得 %v", got)
	}
	if got[0].JSON != recentJSON {
		t.Errorf("JSON 原文不符：%q", got[0].JSON)
	}
	if got[0].FileTimestamp == "" {
		t.Error("mtime 排序锚不应为空")
	}
}

func TestReadZedAndVsCodeNoEnv(t *testing.T) {
	// 无对应 env（Linux 常态 / 未装编辑器）→ 各自 nil，绝不 panic。
	t.Setenv("LOCALAPPDATA", "")
	t.Setenv("USERPROFILE", "")
	t.Setenv("APPDATA", "")
	if r := ReadZedWorkspaces(); r != nil {
		t.Errorf("无 LOCALAPPDATA 应 nil，实得 %v", r)
	}
	if v := ReadVsCodeRecents(); v != nil {
		t.Errorf("无 USERPROFILE/APPDATA 应 nil，实得 %v", v)
	}
}

// --- 编辑器 CLI 发现（纯路径 + env，Linux 用临时目录即可测）---

func TestZedCommandFound(t *testing.T) {
	lad := t.TempDir()
	zedExe := filepath.Join(lad, "Programs", "Zed", "bin", "Zed.exe")
	if err := os.MkdirAll(filepath.Dir(zedExe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(zedExe, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOCALAPPDATA", lad)
	c := ZedCommand()
	if c == nil || c.Exe != zedExe || len(c.PreArgs) != 0 {
		t.Errorf("Zed 命令应命中 bin\\Zed.exe 且无 pre_args：%+v", c)
	}
}

func TestVSCodeCommandWrapsCmd(t *testing.T) {
	lad := t.TempDir()
	cli := filepath.Join(lad, "Programs", "Microsoft VS Code", "bin", "code.cmd")
	if err := os.MkdirAll(filepath.Dir(cli), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cli, []byte("@echo off"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOCALAPPDATA", lad)
	t.Setenv("COMSPEC", "C:\\Windows\\System32\\cmd.exe")
	c := VSCodeCommand()
	if c == nil || c.Exe != "C:\\Windows\\System32\\cmd.exe" ||
		len(c.PreArgs) != 2 || c.PreArgs[0] != "/c" || c.PreArgs[1] != cli {
		t.Errorf("VS Code 命令应是 cmd.exe /c <code.cmd>：%+v", c)
	}
}

func TestEditorCommandsNotFound(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir()) // 空目录：无 Zed.exe / code.cmd
	if c := ZedCommand(); c != nil {
		t.Errorf("未装 Zed 应 nil：%+v", c)
	}
	if c := VSCodeCommand(); c != nil {
		t.Errorf("未装 VS Code 应 nil：%+v", c)
	}
}
