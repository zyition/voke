package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"voke/internal/logic/appcfg"
	"voke/internal/logic/cliphist"
)

// withTempClipDB 把持久化路径指向临时目录，避免碰到真实 %APPDATA%。
func withTempClipDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "clipboard.db")
	old := clipDBPath
	clipDBPath = func() string { return path }
	t.Cleanup(func() { clipDBPath = old })
	return path
}

// TestClipboardPersistenceRoundtrip 验「写入 → 刷盘 → 重开读回」以及置顶状态、
// 当前会话与旧库合并都保留。
func TestClipboardPersistenceRoundtrip(t *testing.T) {
	path := withTempClipDB(t)
	base := time.Unix(1000, 0)

	st := &appState{hist: cliphist.New(10)}
	st.startClipboardPersistence()
	if st.clipPersist == nil {
		t.Fatal("开启持久化后 clipPersist 应为非 nil")
	}
	it, err := st.hist.Add(cliphist.Payload{Text: "persist-me"}, base)
	if err != nil {
		t.Fatal(err)
	}
	st.persistClipUpsert(it)
	pinned, err := st.hist.Add(cliphist.Payload{Text: "pin-me"}, base.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !st.hist.Pin(pinned.ID) {
		t.Fatal("置顶失败")
	}
	st.persistClipItem(pinned.ID)
	st.stopClipboardPersistence(false)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("关闭后库文件应保留：%v", err)
	}

	// 重启：先有当前会话条目，再加载旧库 → 两者合并。
	st2 := &appState{hist: cliphist.New(10)}
	live, err := st2.hist.Add(cliphist.Payload{Text: "session-only"}, base.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	_ = live
	st2.startClipboardPersistence()
	defer st2.stopClipboardPersistence(false)

	items := st2.hist.Items()
	if len(items) != 3 {
		t.Fatalf("合并后应有 3 条，实际 %d：%+v", len(items), items)
	}
	byText := map[string]cliphist.Item{}
	for _, item := range items {
		byText[item.Text] = item
	}
	if _, ok := byText["persist-me"]; !ok {
		t.Fatal("旧库条目 persist-me 丢失")
	}
	if p, ok := byText["pin-me"]; !ok || !p.Pinned {
		t.Fatalf("旧库置顶条目丢失 / 未保持置顶：%+v", p)
	}
	if _, ok := byText["session-only"]; !ok {
		t.Fatal("当前会话条目被覆盖")
	}
}

// TestClipboardPersistenceDisableDeletesDB 验关闭开关会删除库文件（含旁文件）。
func TestClipboardPersistenceDisableDeletesDB(t *testing.T) {
	path := withTempClipDB(t)
	st := &appState{hist: cliphist.New(10)}
	st.startClipboardPersistence()
	it, err := st.hist.Add(cliphist.Payload{Text: "x"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	st.persistClipUpsert(it)
	st.stopClipboardPersistence(true)
	for _, suffix := range []string{"", "-journal", "-wal", "-shm"} {
		if _, err := os.Stat(path + suffix); err == nil {
			t.Fatalf("关闭持久化后不应留下 %s%s", path, suffix)
		}
	}
}

// TestClipboardPersistenceMaxEntriesReconcile 覆盖条数上限：内存淘汰后，
// 后台对账把库里多余的行删掉；重启加载的条数与内存一致。
func TestClipboardPersistenceMaxEntriesReconcile(t *testing.T) {
	path := withTempClipDB(t)
	st := &appState{hist: cliphist.New(10)}
	st.startClipboardPersistence()
	defer st.stopClipboardPersistence(false)
	base := time.Unix(5000, 0)
	items := make([]cliphist.Item, 0, 6)
	for i := range 6 {
		it, err := st.hist.Add(cliphist.Payload{Text: string(rune('a' + i))}, base.Add(time.Duration(i)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		st.persistClipUpsert(it)
		items = append(items, it)
	}
	// 置顶最老的一条：它不占普通条数。
	if !st.hist.Pin(items[0].ID) {
		t.Fatal("置顶失败")
	}
	st.persistClipItem(items[0].ID)
	if removed := st.hist.SetMaxEntries(3); removed != 2 {
		t.Fatalf("SetMaxEntries 淘汰 %d 条，期望 2", removed)
	}
	st.persistClipReconcile()
	st.stopClipboardPersistence(false)

	db, recs, err := openClipDB(path)
	if err != nil {
		t.Fatalf("重开库失败：%v", err)
	}
	db.Close()
	if len(recs) != 4 {
		t.Fatalf("库内应为 3 普通 + 1 置顶，实得 %d：%+v", len(recs), recs)
	}
	got := map[string]bool{}
	for _, r := range recs {
		got[r.ID] = r.Pinned
	}
	for _, want := range []struct {
		id     string
		pinned bool
	}{
		{items[0].ID, true},  // 最老但置顶
		{items[5].ID, false}, // 最新
		{items[4].ID, false},
		{items[3].ID, false},
	} {
		pinned, ok := got[want.id]
		if !ok || pinned != want.pinned {
			t.Errorf("库内条目 %s 缺失或置顶不符：ok=%v pinned=%v，期望 %v", want.id[:8], ok, pinned, want.pinned)
		}
	}
	if _, ok := got[items[1].ID]; ok {
		t.Error("被条数上限淘汰的条目仍在库里")
	}

	// 重启加载：内存条数与库一致（4 条，其中 1 条置顶）。
	st2 := &appState{hist: cliphist.New(10)}
	st2.startClipboardPersistence()
	defer st2.stopClipboardPersistence(false)
	if st2.hist.Len() != 4 || st2.hist.PinnedLen() != 1 {
		t.Fatalf("重启后 Len=%d Pinned=%d，期望 4/1", st2.hist.Len(), st2.hist.PinnedLen())
	}
}

// TestClipboardPersistDefaultAndExplicitOff 锁默认值：缺键 = 开启；显式 false
// 才删除残留库，避免配置损坏回落默认时误删历史。
func TestClipboardPersistDefaultAndExplicitOff(t *testing.T) {
	if !clipboardPersistEnabled(appcfg.Loaded{}) {
		t.Fatal("缺 [clipboard] persist 时应默认开启")
	}
	if clipboardPersistEnabled(appcfg.Loaded{HasClipboardPersist: true, ClipboardPersist: false}) {
		t.Fatal("显式 false 应关闭")
	}

	path := withTempClipDB(t)
	if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	st := &appState{hist: cliphist.New(10)}
	st.initClipboardPersistence(appcfg.Loaded{})
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("缺键（默认开启）不应删除库文件：%v", err)
	}
	st.initClipboardPersistence(appcfg.Loaded{HasClipboardPersist: true, ClipboardPersist: false})
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("显式关闭应删除库文件，stat err=%v", err)
	}
}
