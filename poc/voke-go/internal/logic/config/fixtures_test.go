// 测试夹具：真实 provider（internal/providers/）尚未落地（§10.2 待决策 5），
// 归一化与前缀表用这张 fake 注册表覆盖；alias bonus 的候选也用 fake。

package config

import (
	"testing"

	"github.com/zyition/voke/poc/voke-go/internal/logic/engine"
)

func bp(v bool) *bool       { return &v }
func sp(v string) *string   { return &v }
func fp(v float64) *float64 { return &v }
func ip(v int) *int         { return &v }

// fakeRegistry 的形状对齐 §7.3.4 的 TOML 样例与生产 config.rs:178-189 的默认表，
// 但前缀一律是**符号键**（§7.3.6b「按符号键、不占用字母」），picker 那条按文档
// 样例拿 "#"。**注册顺序即优先级顺序**，多处断言依赖它，改顺序要一并改断言。
//
// 前缀分配：apps / windows 无前缀（参与全局搜索）、calculator "=" Direct、
// timestamp "$" Indirect、base64 "@b" Direct、random "@r" Indirect、__picker "#" Direct。
func fakeRegistry() []Descriptor {
	return []Descriptor{
		{
			ID: "apps", Name: "应用", Description: "搜索并启动开始菜单应用",
			Aliases:        []string{"app", "yy"},
			DefaultEnabled: true, DefaultIsGlobal: true,
			DefaultPrefix: "", DefaultActivation: engine.Direct,
			DefaultWeight: 1.2, DefaultCount: 5,
		},
		{
			ID: "windows", Name: "窗口", Description: "切换到已打开的窗口",
			Aliases:        []string{"win", "ck"},
			DefaultEnabled: true, DefaultIsGlobal: true,
			DefaultPrefix: "", DefaultActivation: engine.Direct,
			DefaultWeight: 1.0, DefaultCount: 5,
		},
		{
			ID: "calculator", Name: "计算器", Description: "算术表达式求值，如 1+1",
			Aliases:        []string{"calc", "jsq"},
			DefaultEnabled: true, DefaultIsGlobal: false,
			DefaultPrefix: "=", DefaultActivation: engine.Direct,
			DefaultWeight: 1.0, DefaultCount: 5,
		},
		{
			ID: "timestamp", Name: "时间戳", Description: "时间戳 ⇄ 可读时间",
			Aliases:        []string{"t", "ts", "time"},
			DefaultEnabled: true, DefaultIsGlobal: false,
			DefaultPrefix: "$", DefaultActivation: engine.Indirect,
			DefaultWeight: 1.0, DefaultCount: 5,
		},
		{
			ID: "base64", Name: "Base64", Description: "base64 编解码（加密 / 解密）",
			Aliases:        []string{"b", "b64"},
			DefaultEnabled: true, DefaultIsGlobal: false,
			DefaultPrefix: "@b", DefaultActivation: engine.Direct,
			DefaultWeight: 1.0, DefaultCount: 5,
		},
		{
			ID: "random", Name: "随机数", Description: "随机整数 / 随机字符串",
			Aliases:        []string{"r", "rand"},
			DefaultEnabled: true, DefaultIsGlobal: false,
			DefaultPrefix: "@r", DefaultActivation: engine.Indirect,
			DefaultWeight: 1.0, DefaultCount: 6,
		},
		{
			ID: engine.PickerPluginID, Name: "列出所有插件", Description: "按名称 / 别名过滤插件",
			DefaultEnabled: true, DefaultIsGlobal: false,
			DefaultPrefix: "#", DefaultActivation: engine.Direct,
			DefaultWeight: 1.0, DefaultCount: 5,
		},
	}
}

// fakeIDs 是 fakeRegistry 的 id 串（注册顺序），报错消息断言要用它。
const fakeIDs = "apps、windows、calculator、timestamp、base64、random、__picker"

// mustNormalize 跑 Normalize 并要求成功，失败直接 Fatal（把 err 原文打出来）。
func mustNormalize(t *testing.T, descs []Descriptor, ovs []UserOverride) Configs {
	t.Helper()
	c, err := Normalize(descs, ovs)
	if err != nil {
		t.Fatalf("Normalize 意外失败：%v", err)
	}
	return c
}

// mustLookup 取某个插件的归一结果。
func mustLookup(t *testing.T, c Configs, id string) ProviderConfig {
	t.Helper()
	p, ok := c.Lookup(id)
	if !ok {
		t.Fatalf("归一结果里没有插件 %s（实得 id：%v）", id, c.IDs())
	}
	return p
}

// wantWarnings 断言警告列表**逐条全等**（顺序也定死：先重复覆盖，再按注册顺序，
// 每个插件内 activation → weight → count → prefix）。
func wantWarnings(t *testing.T, c Configs, want ...string) {
	t.Helper()
	if len(c.Warnings) != len(want) {
		t.Fatalf("警告条数 = %d %q，期望 %d 条 %q", len(c.Warnings), c.Warnings, len(want), want)
	}
	for i, w := range want {
		if c.Warnings[i] != w {
			t.Errorf("第 %d 条警告 = %q，期望 %q", i, c.Warnings[i], w)
		}
	}
}

// almost 给 float32 断言用（加分里混了 0.01 档，二进制浮点不可能精确相等，
// 但期望值仍是**绝对值**，不是从被测代码反推的）。
func almost(got, want float32) bool {
	d := got - want
	if d < 0 {
		d = -d
	}
	return d < 1e-6
}
