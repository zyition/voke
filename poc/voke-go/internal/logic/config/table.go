// 本文件把归一后的配置产出成 §7.3.3 的前缀表（engine.Table 的唯一来源）。

package config

import "github.com/zyition/voke/poc/voke-go/internal/logic/engine"

// BuildTable 产出前缀表。**顺序即优先级**（engine.Scan 做首个全等命中就返回），
// 所以这里严格保持注册顺序，不排序、不去重（去重是 C2 的职责，它已经报错过了）。
//
// 跳过两类条目（与 engine 的语义一致，不是本包另立规则）：
//   - Enabled == false：禁用插件不进表，它的字符因此空出来给别人；
//   - Prefix == ""：无前缀插件不参与前缀扫描（engine.Scan 里也有一道空串 continue，
//     这里是「不生成条目」而非「生成空条目」，表更短、也免得 picker 令牌拿到空串）。
//
// 「列出所有插件」是**普通一条**（决策②）：它的 Descriptor.ID 就是
// engine.PickerPluginID、默认前缀由注册表带进来，这里没有任何针对 picker 的分支。
// 命中它之后 mode = PluginPicker 由 engine 决定，config 不参与判定。
//
// 注：C2 的触发串互斥对**禁用**插件同样成立（禁用者仍占着自己的前缀），
// 目的是「拨开开关的那一刻不会静默改掉别人的路由」。
func (c Configs) BuildTable() engine.Table {
	t := make(engine.Table, 0, len(c.Providers))
	for _, p := range c.Providers {
		if !p.Enabled || p.Prefix == "" {
			continue
		}
		t = append(t, engine.PrefixEntry{
			Prefix:     p.Prefix,
			PluginID:   p.ID,
			Activation: p.Activation,
		})
	}
	return t
}
