package app

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"testing"
	"time"

	"github.com/zyition/voke/poc/voke-go/internal/logic/appcfg"
	"github.com/zyition/voke/poc/voke-go/internal/logic/cliphist"
	"github.com/zyition/voke/poc/voke-go/internal/logic/engine"
	clipboardprovider "github.com/zyition/voke/poc/voke-go/internal/providers/clipboard"
)

func encodeTestPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// newClipboardState 造一个只含剪贴板历史的 appState：不碰 mygo 窗口 / 剪贴板。
func newClipboardState(t *testing.T, payloads ...cliphist.Payload) *appState {
	t.Helper()
	st := &appState{hist: cliphist.New(cliphist.DefaultMaxEntries)}
	for _, p := range payloads {
		if _, err := st.hist.Add(p, time.Now()); err != nil {
			t.Fatalf("塞历史失败：%v", err)
		}
	}
	return st
}

func selectClipboardRow(st *appState, ref string) {
	for i, r := range st.provRows {
		if r.Action.Ref == ref {
			st.provChosen = i
			return
		}
	}
}

func TestClipboardPinAndDeleteRefreshRows(t *testing.T) {
	st := newClipboardState(t,
		cliphist.Payload{Text: "first"},
		cliphist.Payload{Text: "second"},
	)
	st.pluginReg = builtinProvidersWithClipboard(st.clipboardFetch)
	st.engineState = engine.SelectPlugin(engine.State{}, clipboardprovider.ID)

	items := st.hist.Items()
	if len(items) != 2 {
		t.Fatalf("历史应有 2 条，得到 %d", len(items))
	}
	// 新项在前：first 比 second 晚加入，故 Items()[1] 是 first。
	target := items[1].ID
	st.refreshProviderResults()
	selectClipboardRow(st, target)
	st.toggleSelectedPin()
	if it, ok := st.hist.Find(target); !ok || !it.Pinned {
		t.Fatalf("Ctrl+Shift+P 后条目应已置顶：%+v", it)
	}
	if len(st.provRows) != 2 || st.provRows[0].Action.Ref != target {
		t.Fatalf("置顶项应排在第一行：%+v", st.provRows)
	}

	// 删除置顶项后只剩一条，且不再指向被删条目。
	st.provChosen = 0
	st.deleteSelected()
	if _, ok := st.hist.Find(target); ok {
		t.Fatal("删除后条目仍在历史里")
	}
	if len(st.provRows) != 1 {
		t.Fatalf("删除后应只剩一行，得到 %d", len(st.provRows))
	}
}

func TestClipboardPinLimitNotice(t *testing.T) {
	st := newClipboardState(t)
	for i := 0; i < cliphist.MaxPinned; i++ {
		it, err := st.hist.Add(cliphist.Payload{Text: fmt.Sprintf("pin-%d", i)}, time.Now().Add(time.Duration(i)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if !st.hist.Pin(it.ID) {
			t.Fatalf("第 %d 条置顶失败", i)
		}
	}
	extra, err := st.hist.Add(cliphist.Payload{Text: "extra"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	st.toggleClipboardPin(extra.ID)
	if st.clipNotice == "" || st.clipNoticeAt.IsZero() {
		t.Fatal("置顶已满时应给出提示")
	}
	if it, _ := st.hist.Find(extra.ID); it.Pinned {
		t.Fatal("置顶已满时不应改变条目状态")
	}
}

func TestPasteSelectedPlainRejectsImageOnly(t *testing.T) {
	st := newClipboardState(t)
	it, err := st.hist.Add(cliphist.Payload{PNG: encodeTestPNG(t)}, time.Now())
	if err != nil {
		t.Fatalf("图片条目应可入历史：%v", err)
	}
	st.pluginReg = builtinProvidersWithClipboard(st.clipboardFetch)
	st.engineState = engine.SelectPlugin(engine.State{}, clipboardprovider.ID)
	st.refreshProviderResults()
	selectClipboardRow(st, it.ID)
	if !st.pasteSelectedPlain() {
		t.Fatal("剪贴板插件态应消费 Ctrl+Enter")
	}
	if st.clipNotice != "该条目没有文本，无法纯文本粘贴" {
		t.Fatalf("图片项 Ctrl+Enter 应提示无文本，得到 %q", st.clipNotice)
	}
}

func TestPasteSelectedPlainIgnoredOutsideClipboard(t *testing.T) {
	st := newClipboardState(t, cliphist.Payload{Text: "x"})
	st.engineState = engine.SelectPlugin(engine.State{}, "base64")
	if st.pasteSelectedPlain() {
		t.Fatal("非剪贴板插件不应消费 Ctrl+Enter")
	}
}

// 取消置顶走同一入口时也要刷新（动作图标点击路径）。
func TestClipboardActionUnpinViaIntent(t *testing.T) {
	st := newClipboardState(t, cliphist.Payload{Text: "x"})
	it := st.hist.Items()[0]
	if !st.hist.Pin(it.ID) {
		t.Fatal("准备置顶失败")
	}
	st.pluginReg = builtinProvidersWithClipboard(st.clipboardFetch)
	st.engineState = engine.SelectPlugin(engine.State{}, clipboardprovider.ID)
	st.refreshProviderResults()
	if len(st.provRows) != 1 {
		t.Fatalf("应有 1 行，得到 %d", len(st.provRows))
	}
	m := st.provRows[0].Match
	for _, a := range m.Actions {
		if a.ID == "unpin" {
			m.Action = a.Action
			st.runProviderIntent(m, m.ProviderID)
			break
		}
	}
	if got, _ := st.hist.Find(it.ID); got.Pinned {
		t.Fatal("取消置顶动作后条目仍为置顶")
	}
}

func TestClipboardMaxEntriesFallbacks(t *testing.T) {
	cases := []struct {
		name string
		in   appcfg.Loaded
		want int
	}{
		{"未写", appcfg.Loaded{}, cliphist.DefaultMaxEntries},
		{"合法值", appcfg.Loaded{HasClipboardMaxEntries: true, ClipboardMaxEntries: 250}, 250},
		{"0 越界回落默认", appcfg.Loaded{HasClipboardMaxEntries: true, ClipboardMaxEntries: 0}, cliphist.DefaultMaxEntries},
		{"超上限回落默认", appcfg.Loaded{HasClipboardMaxEntries: true, ClipboardMaxEntries: 5000}, cliphist.DefaultMaxEntries},
	}
	for _, c := range cases {
		if got := clipboardMaxEntries(c.in); got != c.want {
			t.Errorf("%s：得到 %d，想要 %d", c.name, got, c.want)
		}
	}
}
