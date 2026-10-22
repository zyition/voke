// 本轮测试替换：列表行行为 + 懒加载闸门接线（定位锚点的**纯算术**用例已随
// §7.2 步骤4 搬到 internal/logic/geometry，见文件中部说明）。
//
// 沿用 §11.13 的纪律：凡是**观感/交互**类断言都用 mygo 自带 Tester
// （ui.NewTester，headless.go:229）跑真实渲染/事件，而不是断言内部状态——
// 行视觉这种东西只有真渲染出来扫像素才算证据（「以为如此、实测并非如此」在
// 本轮又发生了一次：ui.Button 的边框是 styleButton 加的，不是 List 默认行为）。
//
// §7.2 步骤7b：验渲染/像素/交互帧的 NewTester 用例（row_visual_test.go 的像素用例 +
// 本文件的 TestRowSingleClickExecutesOnce / TestRowClickKeepsKeyboardNavigation /
// TestListEmptyRendersHintNoRows）已随渲染层一起搬进 internal/ui 同包（它们直测本包
// 未导出渲染件），断言与阈值原样搬、未削弱。本文件只留**接线**用例与喂接线的 helper。
package app

import (
	"testing"

	"voke/internal/logic/appsmodel"
)

// emptyHint 的四态文案断言已随 §7.2 步骤5 搬到
// internal/logic/panelstate/panelstate_test.go（直测生产 Model.EmptyHint、
// 注入时间基准）。这里不再有对应的测试 helper。

// resetLoadGate 把 logic 层的懒加载闸门（internal/logic/appsmodel）复位到
// 「未触发」，并把注入的枚举体换成测试用的空实现（不读磁盘、结果与开发机无关）。
//
// 闸门字段本身（loadMu / loaded / loadTrigger）已随 §7.2 下沉进 appsmodel，
// 同包才碰得到；本用例只经它导出的 ResetLoadGate / LoadTriggerCount 操作，
// 「连续按键只触发一次」这条不变量本身由 appsmodel 的 loadgate_test.go 直接
// 断未导出字段。
//
// engine_wiring_test.go 也复用本 helper（newWiredState 走它），故它必须留在 main 包。
func resetLoadGate(t *testing.T) {
	t.Helper()
	appsmodel.ResetLoadGate()
	old := appsmodel.Loader
	appsmodel.Loader = func() {}
	t.Cleanup(func() {
		appsmodel.Loader = old
		appsmodel.ResetLoadGate()
	})
}

// loadTriggerCount 返回本测试内真正发起过的枚举次数。
func loadTriggerCount() int { return appsmodel.LoadTriggerCount() }

// 面板定位算术的用例（TestPanelCenterAnchorMatchesLaunchers 及其 panelTopAt
// 助手）已随 §7.2 步骤4 搬到 internal/logic/geometry/geometry_test.go：那条
// 用例只验数学、不碰 ui.NewTester，属 logic 层可测范围。
//
// geometry.PanelTopLeft 与本包仍有一处**接线**关系：ready() 建窗用的尺寸
// 就是 geometry.PanelWidth / PanelHeight。至于「showPanel 是否真的在调
// geometry.PanelTopLeft」：无主循环时 PrimaryDisplay() 走 onMainValue 会
// **永久阻塞**（apps_test.go 头注记的同类坑），运行期拿不到 SetPosition 的
// 结果，故这条接线只能靠 panel.go 里唯一的调用点保证，**不**在此伪造一个
// 改坏实现也不会红的空转断言（§10.1.1 陷阱）。

// TestEmptyHintStates 的四态断言已随 §7.2 步骤5 搬到
// internal/logic/panelstate/panelstate_test.go（直测生产 Model.EmptyHint，
// 期望绝对值、时间基准注入）。

// TestOnQueryChangedWiresEnsureLoad 验的是**接线**：main 侧 st.onQueryChanged()
// 把 Model 返回的 ActionEnsureLoad 意图**真的**翻译成 appsmodel.EnsureLoaded，
// 且连续按键经 singleflight 闸门只枚举一次、清空查询不新增枚举。
//
// 「哪个查询该产出 EnsureLoad 意图、Loading 标志如何置位」这类**决策**断言已进
// panelstate 同包直测（TestOnQueryChangedReturnsEnsureLoadOnce）；本用例只补
// logic 层测不到的那一环——意图到 appsmodel 副作用的落地（§10.1.1：测生产函数、
// 不抄第二份）。Loader 已被 resetLoadGate 换成空实现，不读磁盘。
func TestOnQueryChangedWiresEnsureLoad(t *testing.T) {
	resetLoadGate(t)
	st := &appState{}
	if st.model.Loading {
		t.Fatal("初始状态不该在加载")
	}
	for _, q := range []string{"w", "wx", "wxl"} {
		st.model.Query = q
		st.onQueryChanged()
	}
	if !st.model.Loading {
		t.Fatal("非空查询应触发加载")
	}
	if got := loadTriggerCount(); got != 1 {
		t.Errorf("连续按键应只触发 1 次枚举，实际 %d 次", got)
	}
	st.model.Query = ""
	st.onQueryChanged()
	if got := loadTriggerCount(); got != 1 {
		t.Errorf("清空查询不应新增枚举，实际累计 %d 次", got)
	}
}
