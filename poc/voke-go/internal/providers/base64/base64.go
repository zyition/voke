// Package base64 是 base64 编解码插件——1:1 对照生产 `voke-core/src/base64.rs`。
//
// 分层：纯逻辑（编解码 + 出 Match），零 mygo、零 IO，Linux 原生可测。执行 Copy
// 动作（写剪贴板）在 app 层 dispatcher。路由（`@b` 前缀、Indirect 激活、子模式标识）
// 由 config/engine 既有机制承载，本包只声明 provider 自身的元数据与查询产出。
package base64

import (
	b64 "encoding/base64"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/zyition/voke/poc/voke-go/internal/providers"
)

// ID 与子模式标识，对照 Rust `Base64Provider::ID` / `SUB_MODE_ENC` / `SUB_MODE_DEC`。
const (
	ID         = "base64"
	SubModeEnc = "enc"
	SubModeDec = "dec"
	// prefix 是二级前缀 `@b`。`#` 归插件列表 picker 独占：它若配成 Direct，
	// 展开后的触发串 "#" 是任何 "#…" 的前缀，会遮蔽同命名空间的所有前缀（C2），
	// 所以二级前缀统一放在 `@` 下。Indirect 激活：输入 `@b ` 才路由进来。
	prefix = "@b"
)

// Provider 实现 providers.Provider。
type Provider struct {
	config providers.ProviderConfig
}

// 编译期断言：base64.Provider 必须满足 providers.Provider 契约（接口漂移即编译失败）。
var _ providers.Provider = (*Provider)(nil)

// New 返回带默认配置的 base64 provider（is_global=false、prefix=@b、Indirect）。
func New() *Provider {
	return &Provider{config: providers.ProviderConfig{
		Enabled: true, IsGlobal: false, Weight: 1.0,
		Prefix: prefix, Activation: "Indirect", Count: 5,
	}}
}

func (p *Provider) ID() string                           { return ID }
func (p *Provider) Name() string                         { return "Base64" }
func (p *Provider) Aliases() []string                    { return []string{"b", "b64"} }
func (p *Provider) Description() string                  { return "base64 编解码（加密 / 解密）" }
func (p *Provider) Config() providers.ProviderConfig     { return p.config }
func (p *Provider) SetConfig(c providers.ProviderConfig) { p.config = c }

// Placeholder 对照 Rust `placeholder`：按子模式给不同提示。
func (p *Provider) Placeholder(subMode string) string {
	switch subMode {
	case SubModeEnc:
		return "输入要加密的内容（例：hello → aGVsbG8=）"
	case SubModeDec:
		return "输入要解密的 base64（例：aGVsbG8= → hello）"
	default:
		return "选择 加密 或 解密"
	}
}

// Query 对照 Rust `query`：第一级（无子模式）只列两个方向、不猜；enc/dec 出真结果。
// RegeneratesOnQuery：base64 编解码确定性 → false（同输入重跑结果一致）。
func (p *Provider) RegeneratesOnQuery() bool { return false }

func (p *Provider) Query(ctx providers.QueryCtx) []providers.Match {
	if ctx.SubMode == "" {
		return []providers.Match{
			providers.MatchSubquery(ID, "加密", "把文本编码成 base64", SubModeEnc, 1.0),
			providers.MatchSubquery(ID, "解密", "把 base64 解码成文本", SubModeDec, 0.9),
		}
	}
	return Convert(ctx.SubMode, ctx.Query)
}

// Convert 移植 Rust `convert`：在某个子模式下做一次转换，返回结果行或提示行。
// 未知 mode 按编码兜底（不 panic），逐字对照生产。
func Convert(mode, query string) []providers.Match {
	q := strings.TrimSpace(query)

	if mode == SubModeDec {
		if q == "" {
			return nil // 用法已挪进占位符；空输入不再给提示行。
		}
		if text, ok := decodeToUTF8(q); ok {
			return []providers.Match{execResult(text, fmt.Sprintf("base64 解码（%d 字节）", len(text)), 1.0)}
		}
		// 解码失败给提示而非空列表（否则「解密」却无反馈像卡住）。
		return []providers.Match{hintRow("解码失败", "不是合法的 base64，或解出的字节不是 UTF-8 文本", 1.0)}
	}

	// 编码分支（含未知 mode 兜底）。
	if q == "" {
		return nil
	}
	enc := b64.StdEncoding.EncodeToString([]byte(q))
	return []providers.Match{execResult(enc, fmt.Sprintf("base64 编码（%d 字节）", len(q)), 1.0)}
}

// decodeToUTF8 解码标准 base64 并要求结果为合法 UTF-8；对照 Rust `decode(...).and_then(from_utf8)`。
// 先剥掉所有 ASCII 空白（空格 / \t / \n / \v / \f / \r），与 Rust `decode` 里
// `filter(|b| !b.is_ascii_whitespace())` 一致——粘贴换行的 base64（常见于 76 列折行）
// 才能解出，否则 StdEncoding 会因内部空白直接判非法。
func decodeToUTF8(s string) (string, bool) {
	raw, err := b64.StdEncoding.DecodeString(stripASCIISpace(s))
	if err != nil {
		return "", false
	}
	if !utf8.Valid(raw) {
		return "", false
	}
	return string(raw), true
}

// stripASCIISpace 移除 Rust `u8::is_ascii_whitespace` 认定的 6 个空白字节。
func stripASCIISpace(s string) string {
	if !strings.ContainsAny(s, " \t\n\v\f\r") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if !isASCIISpaceByte(s[i]) {
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

func isASCIISpaceByte(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\v', '\f', '\r':
		return true
	}
	return false
}

func execResult(title, subtitle string, score float64) providers.Match {
	return providers.MatchExecute(ID, title, subtitle, providers.Action{Kind: providers.ActionCopy, Text: title}, score)
}

func hintRow(title, subtitle string, score float64) providers.Match {
	m := providers.MatchHint(ID, title, subtitle)
	m.Score = score
	return m
}
