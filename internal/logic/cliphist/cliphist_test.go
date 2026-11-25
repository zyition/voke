package cliphist

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
	"time"
)

func testPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 120, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func add(t *testing.T, s *Store, p Payload, at int) Item {
	t.Helper()
	it, err := s.Add(p, time.Unix(int64(at), 0))
	if err != nil {
		t.Fatalf("Add 失败：%v", err)
	}
	return it
}

func texts(items []Item) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Text
	}
	return out
}

func TestAddDedupAndFIFO(t *testing.T) {
	s := New(2)
	add(t, s, Payload{Text: "a"}, 1)
	add(t, s, Payload{Text: "b"}, 2)
	if got := strings.Join(texts(s.Items()), ","); got != "b,a" {
		t.Fatalf("顺序=%s，期望 b,a", got)
	}
	add(t, s, Payload{Text: "a"}, 3) // 重复内容移到最前，不新增
	if s.Len() != 2 {
		t.Fatalf("去重后 Len=%d，期望 2", s.Len())
	}
	if got := strings.Join(texts(s.Items()), ","); got != "a,b" {
		t.Fatalf("重复后顺序=%s，期望 a,b", got)
	}
	add(t, s, Payload{Text: "c"}, 4) // 淘汰最旧 b
	if got := strings.Join(texts(s.Items()), ","); got != "c,a" {
		t.Fatalf("FIFO 后顺序=%s，期望 c,a", got)
	}
}

func TestPinNotCountedAndLimit(t *testing.T) {
	s := New(1)
	a := add(t, s, Payload{Text: "a"}, 1)
	if !s.Pin(a.ID) {
		t.Fatal("首次置顶应成功")
	}
	add(t, s, Payload{Text: "b"}, 2) // 普通项上限 1
	if s.PinnedLen() != 1 || s.UnpinnedLen() != 1 {
		t.Fatalf("pinned=%d unpinned=%d，期望 1/1", s.PinnedLen(), s.UnpinnedLen())
	}
	add(t, s, Payload{Text: "c"}, 3) // b 被淘汰，a 保留
	if got := strings.Join(texts(s.Items()), ","); got != "a,c" {
		t.Fatalf("顺序=%s，期望 a,c", got)
	}

	s2 := New(100)
	for i := 0; i < MaxPinned; i++ {
		it := add(t, s2, Payload{Text: string(rune('a' + i))}, i)
		if !s2.Pin(it.ID) {
			t.Fatalf("第 %d 条置顶失败", i+1)
		}
	}
	if s2.CanPin() {
		t.Fatal("10 条置顶后 CanPin 应为 false")
	}
	extra := add(t, s2, Payload{Text: "extra"}, 20)
	if s2.Pin(extra.ID) {
		t.Fatal("超过 10 条置顶应失败")
	}
}

func TestUnpinTrims(t *testing.T) {
	s := New(1)
	a := add(t, s, Payload{Text: "a"}, 1)
	s.Pin(a.ID)
	add(t, s, Payload{Text: "b"}, 2)
	add(t, s, Payload{Text: "c"}, 3) // b 被淘汰
	if !s.Unpin(a.ID) {
		t.Fatal("取消置顶失败")
	}
	if s.PinnedLen() != 0 || s.UnpinnedLen() != 1 {
		t.Fatalf("pinned=%d unpinned=%d，期望 0/1", s.PinnedLen(), s.UnpinnedLen())
	}
	if got := texts(s.Items()); len(got) != 1 || got[0] != "a" {
		t.Fatalf("取消置顶后 items=%v，期望 [a]", got)
	}
}

func TestRichCapDropsRichKeepsText(t *testing.T) {
	s := New(10)
	it := add(t, s, Payload{Text: "hello", HTML: strings.Repeat("x", MaxRichBytes)}, 1)
	if !it.HasText() || it.HasRich() {
		t.Fatalf("超限富文本应只保留 Text：text=%v rich=%v", it.HasText(), it.HasRich())
	}
	if it.Text != "hello" {
		t.Fatalf("Text=%q", it.Text)
	}
}

func TestImageCapDropsImageKeepsText(t *testing.T) {
	s := New(10)
	it := add(t, s, Payload{Text: "hello", PNG: make([]byte, MaxImageBytes+1)}, 1)
	if it.HasImage() || !it.HasText() || it.Text != "hello" {
		t.Fatalf("超限图片应丢弃图片保留文本：image=%v text=%q", it.HasImage(), it.Text)
	}
	if _, err := s.Add(Payload{PNG: make([]byte, MaxImageBytes+1)}, time.Unix(2, 0)); err != ErrTooLarge {
		t.Fatalf("超限图片-only err=%v，期望 ErrTooLarge", err)
	}
}

func TestImageThumbnailAndPlainAction(t *testing.T) {
	s := New(10)
	it := add(t, s, Payload{Text: "pic", HTML: "<b>pic</b>", PNG: testPNG(t, 96, 48)}, 1)
	if !it.HasImage() || it.Width != 96 || it.Height != 48 {
		t.Fatalf("图片尺寸/标记错误：image=%v %dx%d", it.HasImage(), it.Width, it.Height)
	}
	if len(it.ThumbPNG) == 0 {
		t.Fatal("没有生成缩略图")
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(it.ThumbPNG))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Width != ThumbSize || cfg.Height != ThumbSize {
		t.Fatalf("缩略图应为 %d×%d 正方形画布（等比居中，避免 UI 拉伸），得到 %dx%d",
			ThumbSize, ThumbSize, cfg.Width, cfg.Height)
	}
	if !it.HasPlainAction() {
		t.Fatal("富文本/图片 + 文本应显示纯文本粘贴")
	}
	if (Item{Text: "plain"}).HasPlainAction() {
		t.Fatal("纯文本不应显示纯文本粘贴")
	}
}

func TestDeleteFreesBytes(t *testing.T) {
	s := New(10)
	it := add(t, s, Payload{Text: "hello", HTML: "<b>hello</b>"}, 1)
	before := s.Bytes()
	if !s.Delete(it.ID) {
		t.Fatal("删除失败")
	}
	if s.Len() != 0 || s.Bytes() != 0 || before == 0 {
		t.Fatalf("删除后 len=%d bytes=%d before=%d", s.Len(), s.Bytes(), before)
	}
	if s.Delete(it.ID) {
		t.Fatal("重复删除应返回 false")
	}
}

func TestSetMaxEntriesTrims(t *testing.T) {
	s := New(10)
	for i := 0; i < 5; i++ {
		add(t, s, Payload{Text: string(rune('a' + i))}, i)
	}
	if removed := s.SetMaxEntries(2); removed != 3 {
		t.Fatalf("removed=%d，期望 3", removed)
	}
	if got := strings.Join(texts(s.Items()), ","); got != "e,d" {
		t.Fatalf("顺序=%s，期望 e,d", got)
	}
	if s.SetMaxEntries(9999) != 0 || s.MaxEntries() != MaxMaxEntries {
		t.Fatalf("上限应夹到 %d，实际 %d", MaxMaxEntries, s.MaxEntries())
	}
}

func TestRestoreOrdersDedupesAndPins(t *testing.T) {
	base := time.Unix(100, 0)
	s := New(5)
	s.Restore([]Item{
		{Text: "old", LastUsedAt: base},
		{Text: "new", LastUsedAt: base.Add(20 * time.Second)},
		{Text: "pinned", Pinned: true, LastUsedAt: base.Add(5 * time.Second)},
		{Text: "new", LastUsedAt: base.Add(30 * time.Second)}, // 同内容：取较新的 LastUsedAt
		{Text: "pinned", Pinned: true, LastUsedAt: base.Add(6 * time.Second)},
		{Text: "", LastUsedAt: base}, // 空内容跳过
	})
	if got := strings.Join(texts(s.Items()), ","); got != "pinned,new,old" {
		t.Fatalf("顺序=%s，期望 pinned,new,old", got)
	}
	if s.Len() != 3 || s.PinnedLen() != 1 {
		t.Fatalf("Len=%d Pinned=%d，期望 3/1", s.Len(), s.PinnedLen())
	}
	items := s.Items()
	if !items[0].Pinned || items[0].ID == "" {
		t.Fatalf("置顶条目应有 id 且 pinned：%+v", items[0])
	}
	if !items[1].LastUsedAt.Equal(base.Add(30 * time.Second)) {
		t.Fatalf("去重应保留较新的 LastUsedAt：%v", items[1].LastUsedAt)
	}
	if items[1].Bytes == 0 {
		t.Fatal("Restore 应重算 Bytes")
	}
}

func TestRestoreTrimsAndMergesPinned(t *testing.T) {
	base := time.Unix(200, 0)
	s := New(1)
	s.Restore([]Item{
		{Text: "a", LastUsedAt: base.Add(1 * time.Second)},
		{Text: "b", LastUsedAt: base.Add(2 * time.Second)},
		{Text: "c", LastUsedAt: base.Add(3 * time.Second)},
		{Text: "d", LastUsedAt: base.Add(4 * time.Second)},
		{Text: "b", Pinned: true, LastUsedAt: base.Add(5 * time.Second)},
	})
	if got := strings.Join(texts(s.Items()), ","); got != "b,d" {
		t.Fatalf("顺序=%s，期望 b,d（b 置顶且取并集，d 最新；c/a 超条数）", got)
	}
	it := s.Items()[0]
	if !it.Pinned || !it.LastUsedAt.Equal(base.Add(5*time.Second)) {
		t.Fatalf("去重应合并 pinned 与更新时间：%+v", it)
	}
}

func TestRestoreReplacesExisting(t *testing.T) {
	s := New(5)
	add(t, s, Payload{Text: "memory-only"}, 1)
	s.Restore([]Item{{Text: "from-disk", LastUsedAt: time.Unix(50, 0)}})
	if got := strings.Join(texts(s.Items()), ","); got != "from-disk" {
		t.Fatalf("Restore 应整体替换，实际 %s", got)
	}
}
