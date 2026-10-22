package clipboard

import (
	"strings"
	"testing"
	"time"

	"github.com/zyition/voke/poc/voke-go/internal/logic/cliphist"
	"github.com/zyition/voke/poc/voke-go/internal/providers"
)

func testProvider(items ...cliphist.Item) *Provider {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	return New(func() []cliphist.Item { return items }, func() time.Time { return now })
}

func TestQueryEmptyReturnsAllInStoreOrder(t *testing.T) {
	p := testProvider(
		cliphist.Item{ID: "p", Text: "置顶内容", Pinned: true, LastUsedAt: time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC)},
		cliphist.Item{ID: "a", Text: "最近内容", LastUsedAt: time.Date(2026, 10, 22, 11, 59, 0, 0, time.UTC)},
	)
	rows := p.Query(providers.QueryCtx{})
	if len(rows) != 2 || rows[0].Action.Ref != "p" || rows[1].Action.Ref != "a" {
		t.Fatalf("空查询应保持快照顺序，得到 %+v", rows)
	}
	if !strings.HasPrefix(rows[0].Subtitle, "已置顶 · ") {
		t.Fatalf("置顶项副标题应带标记：%q", rows[0].Subtitle)
	}
	if rows[0].Action.Kind != providers.ActionPaste {
		t.Fatalf("默认动作应为粘贴，得到 %v", rows[0].Action.Kind)
	}
}

func TestQueryFiltersAndScores(t *testing.T) {
	p := testProvider(
		cliphist.Item{ID: "a", Text: "https://example.com/alpha"},
		cliphist.Item{ID: "b", Text: "beta line"},
		cliphist.Item{ID: "c", Text: "图片", Width: 10, Height: 20, PNG: []byte{1}, ThumbPNG: []byte{2}},
	)
	rows := p.Query(providers.QueryCtx{Query: "EXAMPLE"})
	if len(rows) != 1 || rows[0].Action.Ref != "a" {
		t.Fatalf("大小写不敏感子串匹配失败：%+v", rows)
	}
	rows = p.Query(providers.QueryCtx{Query: "HTTPS"})
	if len(rows) != 1 || rows[0].Score <= 0.9 {
		t.Fatalf("命中开头应给高分，得到 %+v", rows)
	}
	rows = p.Query(providers.QueryCtx{Query: "图片"})
	if len(rows) != 1 || rows[0].Action.Ref != "c" {
		t.Fatalf("图片类型词应可搜索：%+v", rows)
	}
}

func TestImageOnlyOmitsPlainPaste(t *testing.T) {
	p := testProvider(cliphist.Item{ID: "img", PNG: []byte{1}, ThumbPNG: []byte{2}, Width: 8, Height: 6})
	rows := p.Query(providers.QueryCtx{})
	if len(rows) != 1 {
		t.Fatalf("应有一条图片结果，得到 %d", len(rows))
	}
	for _, a := range rows[0].Actions {
		if a.ID == "paste-plain" {
			t.Fatalf("纯图片不应有纯文本粘贴动作：%+v", rows[0].Actions)
		}
	}
	if rows[0].Title != "图片 · 8×6" {
		t.Fatalf("图片标题应带尺寸：%q", rows[0].Title)
	}
	if len(rows[0].PreviewPNG) == 0 || rows[0].PreviewKey != "img" {
		t.Fatalf("图片应带缩略图与缓存键：%+v", rows[0])
	}
}

func TestRichTextHasPlainPasteAndPinState(t *testing.T) {
	p := testProvider(cliphist.Item{
		ID: "r", Text: "hello", HTML: "<b>hello</b>", Pinned: true,
		LastUsedAt: time.Date(2026, 10, 22, 11, 30, 0, 0, time.UTC),
	})
	rows := p.Query(providers.QueryCtx{})
	var hasPlain, hasUnpin bool
	for _, a := range rows[0].Actions {
		if a.ID == "paste-plain" {
			hasPlain = true
		}
		if a.ID == "unpin" {
			hasUnpin = true
		}
	}
	if !hasPlain || !hasUnpin {
		t.Fatalf("富文本应有纯文本粘贴与取消置顶：%+v", rows[0].Actions)
	}
	if !strings.Contains(rows[0].Subtitle, "富文本") || !strings.Contains(rows[0].Subtitle, "30 分钟前") {
		t.Fatalf("副标题应含类型与相对时间：%q", rows[0].Subtitle)
	}
}

func TestCompactTitle(t *testing.T) {
	long := strings.Repeat("a", maxTitleRunes+20)
	got := compact("a\n\nb\t"+long, maxTitleRunes)
	if strings.Contains(got, "\n") || !strings.HasPrefix(got, "a b ") {
		t.Fatalf("空白应压平：%q", got)
	}
	if n := len([]rune(got)); n != maxTitleRunes {
		t.Fatalf("截断后应为 %d 个字符（含省略号），得到 %d", maxTitleRunes, n)
	}
}
