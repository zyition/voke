// 本文件实现 §7.3.3 的扫描与状态转移：判定逻辑只有这一处（铁律②）。

package engine

import (
	"strings"
	"unicode"
)

// Scan 是「一次线性全等扫描」的转移函数：给定前缀表、当前状态与输入框原文，
// 返回下一个状态。**按 §7.3.3 规则的执行顺序实现**：
//
//  1. 栈顶已激活（Plugin / PluginPicker / SubMode）→ 不再判前缀，input 直接当**该层**
//     的查询词（规则 4）。子命令/插件/列表各自的过滤词互不干扰。
//  2. 否则（在 Global）对 table 做一次线性扫描，比较条件是**全等**（不是 HasPrefix）：
//     Direct 比 q == Prefix；Indirect 比 q == Prefix + " "（规则 1、2）。
//     命中即**压栈**：前缀被消费、不进输入框；被消费的 Global 帧残词清空。
//     「列出所有插件」是表里的普通条目（命中它压 PluginPicker 帧）。
//  3. 无命中 → 在 Global 帧写下查询词（保留原文，含首尾空格）。
//
// 关于 q 的裁剪：文档伪码写 `trim(query)`，但 Indirect 需要「前缀 + 空格」全等，
// 若用 TrimSpace 会把那个必需的空格削掉、使 Indirect 永远不可达。因此这里只裁**前导**
// 空白（TrimLeftFunc），保留尾部——这样 Direct 与 Indirect 都可达且互不越界。
func Scan(table Table, cur State, input string) State {
	if cur.Mode().Activated() {
		return cur.withQuery(input)
	}

	q := strings.TrimLeftFunc(input, unicode.IsSpace)
	for _, e := range table {
		if e.Prefix == "" {
			// 无前缀插件不参与前缀扫描，否则会劫持空输入。
			continue
		}
		var hit bool
		switch e.Activation {
		case Direct:
			hit = q == e.Prefix
		case Indirect:
			hit = q == e.Prefix+" "
		}
		if !hit {
			continue
		}
		// 命中即压栈；前缀已消费，压栈会同时清空 Global 帧的残词。
		if e.PluginID == PickerPluginID {
			return cur.push(Scope{Kind: PluginPicker})
		}
		return cur.push(Scope{Kind: Plugin, PluginID: e.PluginID})
	}

	return cur.withQuery(input)
}

// SelectPlugin 从插件列表选中/激活某插件：在**当前栈顶之上**压入该插件第一级。
//
// 关键：若当前栈顶是 PluginPicker（用户经 `#` 列表选中），picker 帧留在栈里——
// 于是退格出栈自然回到列表（严格 LIFO、按进入路径回退）；picker 的过滤词由
// push 在入栈时丢弃，所以回到列表时是空输入、全量列表。若是从 Global 直接激活，
// 则栈为 [Global, Plugin]，出栈回 Global。
func SelectPlugin(cur State, pluginID string) State {
	return cur.push(Scope{Kind: Plugin, PluginID: pluginID})
}

// EnterSubMode 在**当前栈顶之上**压入子命令层（父帧=所属插件保留在栈里）。
// Query 置空与父层过滤词丢弃都由 push 保证；D1 的「只设 sub_mode、不设插件」
// 在栈结构里不可能发生。
func EnterSubMode(cur State, pluginID, subMode string) State {
	return cur.push(Scope{Kind: SubMode, PluginID: pluginID, SubMode: subMode})
}

// BackspaceBack 处理退格键的**逐级回退**语义（严格 LIFO，用户拍板：Esc 只负责隐藏面板、
// 把回退链整个交给 Backspace）：
//
//   - 栈顶 Query 非空 → 只清空该层查询词，不动栈。
//   - 栈顶 Query 已空 → **弹出一层**；上一层在入栈时已被清空，故落在空输入。
//     到栈底（Global）原地不动。
//   - Backspace **永不关面板**（close 恒 false）——关面板只由 Esc 负责。
func BackspaceBack(cur State) (next State, close bool) {
	if cur.Query() != "" {
		return cur.withQuery(""), false
	}
	if len(cur.scopes) <= 1 { // 栈底（Global，可能未物化）：原地不动
		return cur, false
	}
	out := append([]Scope(nil), cur.scopes[:len(cur.scopes)-1]...)
	return State{scopes: out}, false
}
