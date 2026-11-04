package app

import "testing"

// TestPointerSampleMovedTo 锁定「首帧只建基准、相邻采样才比较、reset 后重新建基准」。
func TestPointerSampleMovedTo(t *testing.T) {
	var p pointerSample
	if p.movedTo(10, 20) {
		t.Fatal("首次采样只应建立基准，不应算作移动")
	}
	if p.movedTo(10, 20) {
		t.Fatal("位置未变不应算作移动")
	}
	if !p.movedTo(11, 20) {
		t.Fatal("位置变化应算作移动")
	}
	if p.movedTo(11, 20) {
		t.Fatal("移动后位置停住，下一帧不应继续算移动")
	}
	p.reset()
	if p.movedTo(99, 99) {
		t.Fatal("reset 后应重新建立基准，唤起首帧不应算移动")
	}
}
