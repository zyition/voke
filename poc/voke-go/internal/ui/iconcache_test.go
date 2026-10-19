package ui

import "testing"

// TestIconCacheFIFO 锁定解码位图缓存的上限与淘汰判据：满后按插入序淘汰，
// 命中不会延长寿命（与 platform iconcache 同口径）；nil 作为负缓存保留。
func TestIconCacheFIFO(t *testing.T) {
	c := newIconCache(3)
	c.Put("a", nil) // nil = 已确认无图标，也应占位并区分未命中
	c.Put("b", nil)
	c.Put("c", nil)
	if _, ok := c.Get("a"); !ok {
		t.Fatal("负缓存应命中（ok=true）")
	}
	if _, ok := c.Get("missing"); ok {
		t.Fatal("未写入的 key 不应命中")
	}
	c.Put("d", nil) // 淘汰最旧的 a
	if _, ok := c.Get("a"); ok {
		t.Error("超出上限应淘汰最早插入的 a")
	}
	for _, k := range []string{"b", "c", "d"} {
		if _, ok := c.Get(k); !ok {
			t.Errorf("%s 应仍在缓存中", k)
		}
	}
	if got := c.Len(); got != 3 {
		t.Errorf("Len 应为 3，实得 %d", got)
	}
}

// TestIconCacheUpdateKeepsOrder 验证同一 key 重复 Put 只更新值、不重复插入，
// 不会把其他条目意外挤出。
func TestIconCacheUpdateKeepsOrder(t *testing.T) {
	c := newIconCache(2)
	c.Put("a", nil)
	c.Put("b", nil)
	c.Put("a", nil) // 更新不应新增 order 项
	c.Put("c", nil) // 淘汰 a（它仍是最早插入）
	if _, ok := c.Get("a"); ok {
		t.Error("重复 Put 不应延长 a 的寿命")
	}
	if _, ok := c.Get("b"); !ok {
		t.Error("b 应仍在缓存")
	}
	if _, ok := c.Get("c"); !ok {
		t.Error("c 应已写入")
	}
}

// TestIconCacheZeroValue 保证零值可用（max 回落到默认值、map 懒建），
// 避免测试 / 装配早期直接 &IconCache{} 时 panic。
func TestIconCacheZeroValue(t *testing.T) {
	var c IconCache
	if _, ok := c.Get("x"); ok {
		t.Error("零值缓存不应命中")
	}
	c.Put("x", nil)
	if _, ok := c.Get("x"); !ok {
		t.Error("零值缓存写入后应可命中")
	}
}
