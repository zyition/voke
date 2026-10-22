// base64 provider 测试：以生产 `voke-core/base64.rs` 的测试为 oracle，钉死
// 第一级方向行、enc/dec 转换、解码失败提示、空输入 nil。
package base64

import (
	"testing"

	"voke/internal/providers"
)

func TestQueryLevel1ListsTwoDirections(t *testing.T) {
	p := New()
	hits := p.Query(providers.QueryCtx{Query: ""})
	if len(hits) != 2 {
		t.Fatalf("第一级应有 2 行，实得 %d", len(hits))
	}
	if hits[0].Title != "加密" || hits[0].Interact.Kind != providers.InteractEnterSubquery ||
		hits[0].Interact.Sub != SubModeEnc || hits[0].Score != 1.0 {
		t.Errorf("第一行（加密）不符：%+v", hits[0])
	}
	if hits[1].Title != "解密" || hits[1].Interact.Sub != SubModeDec || hits[1].Score != 0.9 {
		t.Errorf("第二行（解密）不符：%+v", hits[1])
	}
	// 方向行的 Action 必须是 None（它只是进子模式，不执行副作用）。
	for _, m := range hits {
		if m.Action.Kind != providers.ActionNone {
			t.Errorf("方向行 Action 应为 None：%+v", m.Action)
		}
	}
}

func TestEncode(t *testing.T) {
	hits := Convert(SubModeEnc, "hello")
	if len(hits) != 1 {
		t.Fatalf("enc 应出 1 行，实得 %d", len(hits))
	}
	m := hits[0]
	if m.Title != "aGVsbG8=" || m.Action.Kind != providers.ActionCopy || m.Action.Text != "aGVsbG8=" ||
		m.Interact.Kind != providers.InteractExecute {
		t.Errorf("编码结果不符：%+v", m)
	}
}

func TestDecodeRoundTrip(t *testing.T) {
	hits := Convert(SubModeDec, "aGVsbG8=")
	if len(hits) != 1 {
		t.Fatalf("dec 应出 1 行，实得 %d", len(hits))
	}
	m := hits[0]
	if m.Title != "hello" || m.Action.Kind != providers.ActionCopy || m.Action.Text != "hello" {
		t.Errorf("解码结果不符：%+v", m)
	}
}

func TestDecodeIgnoresInternalWhitespace(t *testing.T) {
	// 对照 Rust `whitespace_is_ignored_when_decoding`：粘贴折行 / 含空格的 base64 应能解出。
	for _, tc := range []struct{ in, want string }{
		{"Zm9v\nYmFy", "foobar"},
		{"aGVs bG8=", "hello"},
		{"aGVs\tbG8=", "hello"},
	} {
		hits := Convert(SubModeDec, tc.in)
		if len(hits) != 1 || hits[0].Title != tc.want {
			t.Errorf("dec(%q) 应解出 %q，实得 %+v", tc.in, tc.want, hits)
		}
	}
}

func TestDecodeInvalidIsHintNotSilence(t *testing.T) {
	hits := Convert(SubModeDec, "!!!not-base64!!!")
	if len(hits) != 1 {
		t.Fatalf("解码失败应给 1 行提示，实得 %d", len(hits))
	}
	m := hits[0]
	if !m.IsHint() || m.Title != "解码失败" {
		t.Errorf("非法输入应是「解码失败」提示行：%+v", m)
	}
}

func TestEmptyInputYieldsNothing(t *testing.T) {
	if got := Convert(SubModeEnc, "   "); len(got) != 0 {
		t.Errorf("空 enc 输入应无行，实得 %d", len(got))
	}
	if got := Convert(SubModeDec, ""); len(got) != 0 {
		t.Errorf("空 dec 输入应无行，实得 %d", len(got))
	}
}

func TestMetadataMatchesRust(t *testing.T) {
	p := New()
	if p.ID() != "base64" || p.Name() != "Base64" ||
		p.Description() != "base64 编解码（加密 / 解密）" {
		t.Errorf("元数据不符：%s / %s / %s", p.ID(), p.Name(), p.Description())
	}
	c := p.Config()
	if c.IsGlobal || !c.Enabled || c.Prefix != "@b" || c.Activation != "Indirect" {
		t.Errorf("默认配置不符（is_global=false/prefix=@b/Indirect）：%+v", c)
	}
	if p.Placeholder(SubModeEnc) == p.Placeholder("") {
		t.Error("子模式占位符应与第一级不同")
	}
}
