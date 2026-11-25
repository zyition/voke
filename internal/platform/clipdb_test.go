package platform

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestClipDBRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clipboard.db")
	db, err := OpenClipDB(path)
	if err != nil {
		t.Fatalf("打开库失败：%v", err)
	}
	defer db.Close()

	a := ClipRecord{
		ID: "a", Text: "hello", HTML: "<b>hi</b>",
		RTF: []byte{1, 2}, PNG: []byte{3, 4}, ThumbPNG: []byte{5},
		Width: 40, Height: 30, Pinned: true,
		CreatedAt: 100, LastUsedAt: 200,
	}
	b := ClipRecord{ID: "b", Text: "world", CreatedAt: 300, LastUsedAt: 400}
	for _, r := range []ClipRecord{a, b} {
		if err := db.Upsert(r); err != nil {
			t.Fatalf("Upsert(%s) 失败：%v", r.ID, err)
		}
	}
	// 同 id 更新覆盖旧行。
	a.Text = "hello2"
	a.Pinned = false
	if err := db.Upsert(a); err != nil {
		t.Fatalf("Upsert 更新失败：%v", err)
	}
	recs, err := db.Load()
	if err != nil {
		t.Fatalf("Load 失败：%v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("条数=%d，期望 2", len(recs))
	}
	got := map[string]ClipRecord{}
	for _, r := range recs {
		got[r.ID] = r
	}
	if r := got["a"]; r.Text != "hello2" || r.Pinned || r.Width != 40 ||
		!bytes.Equal(r.RTF, []byte{1, 2}) || !bytes.Equal(r.ThumbPNG, []byte{5}) ||
		r.CreatedAt != 100 || r.LastUsedAt != 200 {
		t.Fatalf("a 回读不一致：%+v", r)
	}

	// KeepOnly 删除不在列表里的行。
	if err := db.KeepOnly([]string{"a"}); err != nil {
		t.Fatalf("KeepOnly 失败：%v", err)
	}
	recs, _ = db.Load()
	if len(recs) != 1 || recs[0].ID != "a" {
		t.Fatalf("KeepOnly 后应只剩 a：%+v", recs)
	}
	// 空列表清空。
	if err := db.KeepOnly(nil); err != nil {
		t.Fatalf("KeepOnly(nil) 失败：%v", err)
	}
	if recs, _ = db.Load(); len(recs) != 0 {
		t.Fatalf("KeepOnly(nil) 后应为空：%+v", recs)
	}

	// ReplaceAll 落盘，关闭重开后仍在。
	if err := db.ReplaceAll([]ClipRecord{b}); err != nil {
		t.Fatalf("ReplaceAll 失败：%v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("关闭失败：%v", err)
	}
	db2, err := OpenClipDB(path)
	if err != nil {
		t.Fatalf("重开失败：%v", err)
	}
	defer db2.Close()
	recs, err = db2.Load()
	if err != nil || len(recs) != 1 || recs[0].ID != "b" || recs[0].Text != "world" {
		t.Fatalf("重开后数据不对：%+v err=%v", recs, err)
	}
}

func TestClipDBPathAndRemove(t *testing.T) {
	dir := t.TempDir()
	path := ClipDBPath(dir)
	if path != filepath.Join(dir, "clipboard.db") {
		t.Fatalf("ClipDBPath=%q", path)
	}
	db, err := OpenClipDB(path)
	if err != nil {
		t.Fatalf("打开失败：%v", err)
	}
	db.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("库文件应存在：%v", err)
	}
}
