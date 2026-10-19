// Package uuid 生成 UUID v4 并复制——1:1 对照生产 `voke-core/src/uuid.rs`。
//
// 纯逻辑零 mygo。生成用标准库 crypto/rand（不引第三方 uuid 依赖），v4 版本位 +
// variant 位按 RFC 4122 设置。requested_count 的解析（空/uuid→1、数字/n:/count:→
// 批量、上限 20、不认识的形态返回无结果）逐字对照生产。
package uuid

import (
	"crypto/rand"
	"fmt"
	"strconv"
	"strings"

	"github.com/zyition/voke/poc/voke-go/internal/providers"
)

const (
	ID       = "uuid"
	maxCount = 20
	prefix   = "#u" // 二级前缀，Indirect（`#u ` 触发），对齐 base64 的 #b 模式。
)

// Provider 实现 providers.Provider。
type Provider struct {
	config providers.ProviderConfig
}

var _ providers.Provider = (*Provider)(nil)

// New 返回默认配置的 uuid provider（is_global=false、prefix=#u、Indirect）。
func New() *Provider {
	return &Provider{config: providers.ProviderConfig{
		Enabled: true, IsGlobal: false, Weight: 1.0,
		Prefix: prefix, Activation: "Indirect", Count: 5,
	}}
}

func (p *Provider) ID() string                           { return ID }
func (p *Provider) Name() string                         { return "UUID" }
func (p *Provider) Aliases() []string                    { return []string{"u"} }
func (p *Provider) Description() string                  { return "生成 UUID v4（可指定个数）" }
func (p *Provider) Config() providers.ProviderConfig     { return p.config }
func (p *Provider) SetConfig(c providers.ProviderConfig) { p.config = c }

// Placeholder 对照 Rust：uuid 无子模式，个数提示进占位符。
func (p *Provider) Placeholder(string) string { return "生成 UUID，输入个数可批量如 3" }

// Query 每次重新生成（对照 Rust：结果必须跟着输入变）。count 解析失败 → 无结果。
// RegeneratesOnQuery：uuid 每次查询重掷新值 → true（输入没变却重跑必须跳过）。
func (p *Provider) RegeneratesOnQuery() bool { return true }

func (p *Provider) Query(ctx providers.QueryCtx) []providers.Match {
	count, ok := requestedCount(ctx.Query)
	if !ok {
		return nil
	}
	out := make([]providers.Match, 0, count)
	for i := 0; i < count; i++ {
		v := Generate()
		sub := "UUID v4"
		if count > 1 { // 多条标序号；单条留空（不制造视觉噪声）。
			sub = fmt.Sprintf("UUID v4（%d / %d）", i+1, count)
		}
		// 越靠前越可能被选中 → 递减分保持稳定顺序。
		out = append(out, providers.MatchExecute(ID, v, sub,
			providers.Action{Kind: providers.ActionCopy, Text: v}, 1.0-float64(i)*0.001))
	}
	return out
}

// Generate 产出一条 UUID v4（供外部直接调用，对照 Rust `generate`）。
func Generate() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand 失败极罕见（系统熵源故障）；不 panic，退回可辨识的非法串由上层
		// 当普通文本处理。真机 dispatcher 复制它也无害。
		return "uuid-entropy-error"
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10xx
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// requestedCount 解析「要几个 UUID」，对照 Rust `requested_count`：
// 空 / `uuid` → 1；纯数字 / `n:<n>` / `count:<n>` → n（0 或不可解析 → 无结果；上限 maxCount）。
func requestedCount(query string) (int, bool) {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" || q == "uuid" {
		return 1, true
	}
	digits := q
	if s := strings.TrimPrefix(q, "n:"); s != q {
		digits = s
	} else if s := strings.TrimPrefix(q, "count:"); s != q {
		digits = s
	}
	n, err := strconv.Atoi(strings.TrimSpace(digits))
	// Rust 用 parse::<usize>()：负号（"-1"）解析失败即拒；Go 的 Atoi 会吃下负号，
	// 必须显式挡掉 n<=0，否则负 count 会让下面的 make 因负容量 panic。
	if err != nil || n <= 0 {
		return 0, false
	}
	if n > maxCount {
		n = maxCount
	}
	return n, true
}
