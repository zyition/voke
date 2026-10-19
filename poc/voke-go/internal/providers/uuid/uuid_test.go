// uuid provider 测试：以 `voke-core/uuid.rs` 行为为 oracle——v4 格式合法、个数解析、
// 多条序号副标题与递减分、不识别形态返回空。生成随机，只断言**格式**与**数量**，
// 不断言具体值（避免脆弱）。
package uuid

import (
	"fmt"
	"strings"
	"testing"

	"github.com/zyition/voke/poc/voke-go/internal/providers"
)

// assertV4 校验 8-4-4-4-12 hex、版本位 '4'、variant 位 ∈ {8,9,a,b}。
func assertV4(t *testing.T, s string) {
	t.Helper()
	p := strings.Split(s, "-")
	if len(p) != 5 || len(p[0]) != 8 || len(p[1]) != 4 || len(p[2]) != 4 || len(p[3]) != 4 || len(p[4]) != 12 {
		t.Fatalf("UUID 段长格式不符：%q", s)
	}
	if p[2][0] != '4' {
		t.Errorf("版本位应为 4：%q", s)
	}
	switch p[3][0] {
	case '8', '9', 'a', 'b':
	default:
		t.Errorf("variant 位应为 8/9/a/b：%q", s)
	}
	if strings.ContainsAny(strings.Join(p, ""), "ghijklmnopqrstuvwxyzGHIJKLMNOPQRSTUVWXYZ") {
		t.Errorf("含非法 hex 字符：%q", s)
	}
}

func TestQueryCounts(t *testing.T) {
	cases := []struct {
		query string
		want  int
	}{
		{"", 1}, {"uuid", 1}, {"3", 3}, {"n:2", 2}, {"count: 4", 4},
		{"999999", 20}, // 上限
		{"0", 0}, {"abc", 0}, {"n:", 0},
		{"-1", 0}, {"1.5", 0}, {"x:3", 0}, // Rust oracle invalid_count 用例（"-1" 曾触发负容量 panic）
	}
	p := New()
	for _, c := range cases {
		hits := p.Query(providers.QueryCtx{Query: c.query})
		if len(hits) != c.want {
			t.Errorf("query %q：结果 %d 条，想要 %d", c.query, len(hits), c.want)
		}
	}
}

func TestQueryRowsAreValidV4CopyWithOrdinals(t *testing.T) {
	p := New()
	hits := p.Query(providers.QueryCtx{Query: "3"})
	if len(hits) != 3 {
		t.Fatalf("想要 3 条，实得 %d", len(hits))
	}
	seen := map[string]bool{}
	for i, m := range hits {
		assertV4(t, m.Title)
		if seen[m.Title] {
			t.Errorf("第 %d 条与前面重复：%q", i, m.Title)
		}
		seen[m.Title] = true
		if m.Action.Kind != providers.ActionCopy || m.Action.Text != m.Title {
			t.Errorf("第 %d 条应是 Copy(title)：%+v", i, m.Action)
		}
		if want := fmt.Sprintf("UUID v4（%d / 3）", i+1); m.Subtitle != want {
			t.Errorf("第 %d 条副标题 = %q，想要 %q", i, m.Subtitle, want)
		}
		// 递减分保证稳定顺序（首条最高）。
		if m.Score > 1.0 {
			t.Errorf("第 %d 条分数应 ≤1.0：%v", i, m.Score)
		}
		if i > 0 && !(m.Score < hits[i-1].Score) {
			t.Errorf("分数应严格递减保序：第 %d 条 %v !< 前一条 %v", i, m.Score, hits[i-1].Score)
		}
	}
}

func TestSingleHasNoOrdinal(t *testing.T) {
	p := New()
	hits := p.Query(providers.QueryCtx{Query: ""})
	if len(hits) != 1 {
		t.Fatal("想要 1 条")
	}
	if hits[0].Subtitle != "UUID v4" {
		t.Errorf("单条副标题应无序号，实得 %q", hits[0].Subtitle)
	}
	assertV4(t, hits[0].Title)
}

func TestMetadataMatchesRust(t *testing.T) {
	p := New()
	if p.ID() != "uuid" || p.Name() != "UUID" || p.Description() != "生成 UUID v4（可指定个数）" {
		t.Errorf("元数据不符：%s/%s/%s", p.ID(), p.Name(), p.Description())
	}
	if len(p.Aliases()) != 1 || p.Aliases()[0] != "u" {
		t.Errorf("别名应为 [u]：%v", p.Aliases())
	}
	c := p.Config()
	if c.IsGlobal || c.Prefix != "#u" || c.Activation != "Indirect" {
		t.Errorf("配置不符（is_global=false/prefix=#u/Indirect）：%+v", c)
	}
}
