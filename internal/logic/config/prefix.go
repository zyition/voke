// prefix.go：前缀触发串的冲突判定（C2 的核心）与旧配置修复用的分析入口。
//
// 引擎的判定是「逐键全等扫描」（engine.Scan）：每敲一个字符，拿当前输入 q 与每条
// 前缀的**完整触发串**全等比较——Direct 是前缀本身，Indirect 是前缀 + 一个空格。
// 因此两个条目 A、B 是否冲突，不能只比前缀字面量，而要比展开后的触发串：
//
//	T(A) 是 T(B) 的前缀（或反之）⇔ 敲向 B 的过程中，输入必然先等于 T(A)，
//	A 抢先压栈、把后续字符当自己的查询词，B 永远无法从 Global 命中。
//
// 例：picker `#` Direct 与 base64 `#b` Indirect：
//
//	T(picker) 是 "#"，T(base64) 是 "#b "，"#" 是 "#b " 的前缀——敲 "#" 时 picker
//	已经进栈，再敲 "b " 只会进 picker 的过滤词，base64 不可达。
//
// 反过来，两个 Indirect 前缀 `@b` 与 `@bc` **不**冲突：触发串 "@b " 与 "@bc "
// 互不为前缀（第三个字符分别是空格与 'c'），两者都能通过完整输入命中。
//
// 「双向前缀」是必要的：长前缀先声明也会遮蔽短前缀（输入总是先经过短串），
// 谁先命中由表序决定，所以任一方向的前缀关系都算冲突；完全相等同样算冲突。
package config

import (
	"strings"

	"voke/internal/logic/engine"
)

// prefixClaim 是一条前缀声明（C2 报错与修复路径共用）。
type prefixClaim struct {
	id         string
	name       string
	prefix     string
	activation engine.Activation
}

// prefixesConflict 判断两条前缀是否会在逐键扫描中互相遮蔽。
// 空前缀（无前缀）不参与判定，永远不冲突。
func prefixesConflict(a, b prefixClaim) bool {
	ta := ActivationTarget(a.prefix, a.activation)
	tb := ActivationTarget(b.prefix, b.activation)
	if ta == "" || tb == "" {
		return false
	}
	return strings.HasPrefix(ta, tb) || strings.HasPrefix(tb, ta)
}

// PrefixConflictIDs 返回在给定用户覆盖下、**有效前缀**彼此冲突的插件 id 列表
// （按注册顺序、去重）。它只做分析，不修改配置也不报错；接线层用它修复旧配置：
// 把这些插件的 Prefix 覆盖丢掉再跑 Normalize，其余设置保持不动。
//
// 有效性口径必须与 Normalize 保持一致：含空白的前缀视为空（C3 置空），activation
// 无法解析时回落 Descriptor 默认（C7）。禁用插件同样参与——C2 的既定语义是
// 「禁用者仍占着前缀」，避免拨开开关那一刻静默改掉别人的路由。
func PrefixConflictIDs(descriptors []Descriptor, overrides []UserOverride) []string {
	ovByID := make(map[string]UserOverride, len(overrides))
	for _, ov := range overrides {
		ovByID[ov.ID] = ov // 同 id 后写覆盖先写，与 Normalize 一致
	}
	claims := make([]prefixClaim, 0, len(descriptors))
	for _, d := range descriptors {
		ov := ovByID[d.ID]
		prefix := derefString(ov.Prefix, d.DefaultPrefix)
		if prefix != "" && containsSpace(prefix) {
			prefix = ""
		}
		activation := d.DefaultActivation
		if ov.Activation != nil {
			if a, ok := parseActivation(*ov.Activation); ok {
				activation = a
			}
		}
		claims = append(claims, prefixClaim{id: d.ID, name: d.Name, prefix: prefix, activation: activation})
	}

	hit := make(map[string]bool)
	for i := 0; i < len(claims); i++ {
		for j := i + 1; j < len(claims); j++ {
			if prefixesConflict(claims[i], claims[j]) {
				hit[claims[i].id] = true
				hit[claims[j].id] = true
			}
		}
	}
	out := make([]string, 0, len(hit))
	for _, c := range claims {
		if hit[c.id] {
			out = append(out, c.id)
		}
	}
	return out
}

// activationChinese 给 C2 报错消息一个可读的激活方式（配置层不依赖 UI 的中文标签）。
func activationChinese(a engine.Activation) string {
	if a == engine.Indirect {
		return "间接"
	}
	return "直接"
}
