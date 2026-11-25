// clipdb.go：剪贴板历史的可选 SQLite 持久化（纯 Go 驱动，无 cgo）。
//
// 表只有一张 clips，一行一条历史；主键是内容哈希（与 cliphist.Item.ID 一致）。
// 写入方是 app 层的单线程队列（见 internal/app/clippersist.go），这里只提供
// 连接、整库读写与「只保留这些 id」的对账接口，不做业务淘汰。
//
// 不加密：SQLCipher / SEE 要么需要 cgo 与 OpenSSL，要么是商业扩展，都会破坏
// 现在的纯 Go 交叉构建；本机单用户场景沿用主流剪贴板工具（Ditto）的明文
// SQLite + 用户目录 ACL 方案。敏感标记 / 文件列表在采集层就被跳过，不落盘。
package platform

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "github.com/glebarez/go-sqlite" // database/sql 驱动 "sqlite"（modernc 纯 Go 移植）
)

// ClipRecord 是一条历史在库里的形状。时间用 UnixNano；ThumbPNG / Width / Height
// 一并存下，启动加载时不必重新解码原图。
type ClipRecord struct {
	ID         string
	Text       string
	HTML       string
	RTF        []byte
	PNG        []byte
	ThumbPNG   []byte
	Width      int
	Height     int
	Pinned     bool
	CreatedAt  int64 // UnixNano
	LastUsedAt int64 // UnixNano
}

// ClipDB 是剪贴板历史库的写连接（SetMaxOpenConns(1)，调用方串行使用）。
type ClipDB struct {
	db *sql.DB
}

// ClipDBPath：历史库文件路径（数据目录下的 clipboard.db）。
func ClipDBPath(dataDir string) string {
	return filepath.Join(dataDir, "clipboard.db")
}

const (
	clipSchemaTable = `
CREATE TABLE IF NOT EXISTS clips (
	id           TEXT PRIMARY KEY,
	text         TEXT NOT NULL DEFAULT '',
	html         TEXT NOT NULL DEFAULT '',
	rtf          BLOB,
	png          BLOB,
	thumb        BLOB,
	width        INTEGER NOT NULL DEFAULT 0,
	height       INTEGER NOT NULL DEFAULT 0,
	pinned       INTEGER NOT NULL DEFAULT 0,
	created_at   INTEGER NOT NULL DEFAULT 0,
	last_used_at INTEGER NOT NULL DEFAULT 0
);`
	clipSchemaIndex = `
CREATE INDEX IF NOT EXISTS clips_order ON clips(pinned DESC, last_used_at DESC);`
)

// OpenClipDB 打开 / 建立历史库并确保表结构存在。目录不存在时创建。
func OpenClipDB(path string) (*ClipDB, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("建剪贴板库目录失败：%w", err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("打开剪贴板库失败：%w", err)
	}
	db.SetMaxOpenConns(1)
	for _, stmt := range []string{
		`PRAGMA busy_timeout = 5000`,
		`PRAGMA synchronous = NORMAL`,
		clipSchemaTable,
		clipSchemaIndex,
		`PRAGMA user_version = 1`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			db.Close()
			return nil, fmt.Errorf("初始化剪贴板库失败：%w", err)
		}
	}
	return &ClipDB{db: db}, nil
}

// Load 读全部条目（置顶优先、最近使用优先）。顺序只影响加载观感，
// cliphist.Restore 会按时间重新整理。
func (c *ClipDB) Load() ([]ClipRecord, error) {
	rows, err := c.db.Query(`SELECT id, text, html, rtf, png, thumb, width, height,
		pinned, created_at, last_used_at FROM clips
		ORDER BY pinned DESC, last_used_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("查询剪贴板库失败：%w", err)
	}
	defer rows.Close()
	var out []ClipRecord
	for rows.Next() {
		var r ClipRecord
		var pinned int64
		if err := rows.Scan(&r.ID, &r.Text, &r.HTML, &r.RTF, &r.PNG, &r.ThumbPNG,
			&r.Width, &r.Height, &pinned, &r.CreatedAt, &r.LastUsedAt); err != nil {
			return nil, fmt.Errorf("读取剪贴板库失败：%w", err)
		}
		r.Pinned = pinned != 0
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历剪贴板库失败：%w", err)
	}
	return out, nil
}

const clipUpsertSQL = `INSERT INTO clips
	(id, text, html, rtf, png, thumb, width, height, pinned, created_at, last_used_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		text = excluded.text, html = excluded.html, rtf = excluded.rtf,
		png = excluded.png, thumb = excluded.thumb,
		width = excluded.width, height = excluded.height,
		pinned = excluded.pinned, created_at = excluded.created_at,
		last_used_at = excluded.last_used_at`

// Upsert 写入 / 更新一条。
func (c *ClipDB) Upsert(r ClipRecord) error {
	if _, err := c.db.Exec(clipUpsertSQL, clipArgs(r)...); err != nil {
		return fmt.Errorf("写入剪贴板条目失败：%w", err)
	}
	return nil
}

// ReplaceAll 用给定快照替换整库（单事务）。
func (c *ClipDB) ReplaceAll(records []ClipRecord) error {
	tx, err := c.db.Begin()
	if err != nil {
		return fmt.Errorf("开始剪贴板库事务失败：%w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM clips`); err != nil {
		return fmt.Errorf("清空剪贴板库失败：%w", err)
	}
	stmt, err := tx.Prepare(clipUpsertSQL)
	if err != nil {
		return fmt.Errorf("准备剪贴板库写入失败：%w", err)
	}
	defer stmt.Close()
	for _, r := range records {
		if _, err := stmt.Exec(clipArgs(r)...); err != nil {
			return fmt.Errorf("写入剪贴板条目失败：%w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交剪贴板库事务失败：%w", err)
	}
	return nil
}

// KeepOnly 删除不在 ids 里的行（内存淘汰 / 删除后对账，避免库无限增长）。
// ids 为空表示清空。
func (c *ClipDB) KeepOnly(ids []string) error {
	if len(ids) == 0 {
		_, err := c.db.Exec(`DELETE FROM clips`)
		if err != nil {
			return fmt.Errorf("清空剪贴板库失败：%w", err)
		}
		return nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	if _, err := c.db.Exec(`DELETE FROM clips WHERE id NOT IN (`+placeholders+`)`, args...); err != nil {
		return fmt.Errorf("对账剪贴板库失败：%w", err)
	}
	return nil
}

// Close 关闭连接。
func (c *ClipDB) Close() error { return c.db.Close() }

func clipArgs(r ClipRecord) []any {
	var pinned int64
	if r.Pinned {
		pinned = 1
	}
	return []any{
		r.ID, r.Text, r.HTML, r.RTF, r.PNG, r.ThumbPNG,
		r.Width, r.Height, pinned, r.CreatedAt, r.LastUsedAt,
	}
}
