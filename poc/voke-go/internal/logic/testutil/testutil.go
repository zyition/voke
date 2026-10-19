// Package testutil 集中放跨包共享的测试构造 helper（§7.2 步骤3）。
//
// 为什么要有它：MkApps 原本住 view_test.go，被 main 侧的 row_visual_test.go /
// panel_behavior_test.go 和 internal/logic/search 的用例同时依赖。搜索逻辑
// 下沉进 search 包后，main 与 search 两侧都要用它——**禁止各抄一份 mkApps**
// （违 §10.1.1「测试不得另立第二份实现/工具」），故抽成一处导出符号。
//
// 分层：本包属 logic 层，零 mygo、零 x/sys/windows；只依赖标准库与
// internal/logic/appsmodel（AppEntry 类型来源）。它只在 _test.go 里被引用，
// 不进生产调用路径。
package testutil

import (
	"fmt"

	"github.com/zyition/voke/poc/voke-go/internal/logic/appsmodel"
)

// MkApps 构造测试条目（Path/ID 只作标识，不参与匹配）。
func MkApps(names ...string) []appsmodel.AppEntry {
	out := make([]appsmodel.AppEntry, len(names))
	for i, n := range names {
		out[i] = appsmodel.AppEntry{Name: n, Path: fmt.Sprintf("shell:AppsFolder\\%d", i), ID: fmt.Sprintf("%d", i)}
	}
	return out
}
