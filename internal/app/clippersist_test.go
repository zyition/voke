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
