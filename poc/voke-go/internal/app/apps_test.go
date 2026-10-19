// apps.go（应用子系统的装配侧）的接线测试。
//
// 原本住在这里的纯逻辑用例已随 §7.2.1 分层下沉：
//   - dedupeApps 去重 → internal/logic/appsmodel（dedupe_test.go）
//   - iconCache FIFO  → internal/logic/iconcache（iconcache_test.go，
//     要读未导出字段 c.mu/c.items/c.order 故必须同包）
//   - 加载闸门        → internal/logic/appsmodel（loadgate_test.go）
//
// §7.2 步骤6b 又把直接碰 mygo 的那一截抽进 internal/platform/shell.go，其
// headless 可测部分（AppIcon 的 nil 归一、iconPixelSizeFor 的算术）在那边的
// shell_test.go。
//
// 本文件只留「装配接缝对不对」：恰恰是 logic / platform 包测不到、炸了又会让面板
// 永远停在「正在枚举应用…」的那部分。
//
// 注意别在这里调 platform.AppIcon：它未命中时 → Shell.FileIcon → 内部还要
// Screen.Displays()，两者都走 onMainValue，无主循环时**永久阻塞**
// （row_visual_test.go 为此特意预置空图标缓存绕开）。
package app

import (
	"testing"

	"github.com/zyition/voke/poc/voke-go/internal/logic/appsmodel"
)

// TestLoaderIsInjected 锁定 apps.go 的 init 接线：logic 层的闸门只会
// `go Loader()`，注入缺失就是 go nil() 直接把进程炸掉。
//
// 为什么这条值得单独存在：视图层的用例每例开头都用 resetLoadGate 把 Loader
// 换成空实现，**探不到注入有没有坏**；删掉 apps.go 的 init，它们照样全绿，
// 真机上却是一按字母就崩。本用例不碰 resetLoadGate，改坏即红。
func TestLoaderIsInjected(t *testing.T) {
	if appsmodel.Loader == nil {
		t.Fatal("appsmodel.Loader 未注入：apps.go 的 init 漏了，枚举一触发就 panic")
	}
}
