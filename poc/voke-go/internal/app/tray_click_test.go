package app

import (
	"testing"
	"time"
)

// TestIsTrayDoubleClick 钉住托盘双击识别窗口：零值不触发、窗口内（含边界）
// 触发、超出窗口或时钟倒退不触发。
func TestIsTrayDoubleClick(t *testing.T) {
	t0 := time.Unix(1000, 0)
	if isTrayDoubleClick(time.Time{}, t0) {
		t.Error("首次点击（prev 零值）不应判定为双击")
	}
	if !isTrayDoubleClick(t0, t0.Add(200*time.Millisecond)) {
		t.Error("200ms 内的第二次点击应判定为双击")
	}
	if !isTrayDoubleClick(t0, t0.Add(trayDoubleClickWindow)) {
		t.Error("恰好 500ms 应判定为双击（闭区间）")
	}
	if isTrayDoubleClick(t0, t0.Add(trayDoubleClickWindow+time.Millisecond)) {
		t.Error("超过 500ms 不应判定为双击")
	}
	if isTrayDoubleClick(t0, t0.Add(-time.Millisecond)) {
		t.Error("时钟倒退不应判定为双击")
	}
}
