package ui

import "testing"

// TestIconCacheLRUEvictsColdest 锁定容量淘汰判据是「最近使用」而非插入序：
// 满后访问过的条目应保留，最久未用的被淘汰；nil 作为负缓存也参与 LRU。
func TestIconCacheLRUEvictsColdest(t *testing.T) {
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
	c.Put("d", nil) // 满：淘汰最久未用的 b（a 刚被 Get 刷新）
	if _, ok := c.Get("b"); ok {
		t.Error("超出上限应淘汰最久未使用的 b")
	}
	for _, k := range []string{"a", "c", "d"} {
		if _, ok := c.Get(k); !ok {
			t.Errorf("%s 应仍在缓存中", k)
		}
	}
	if got := c.Len(); got != 3 {
		t.Errorf("Len 应为 3，实得 %d", got)
	}
}

// TestIconCacheTrim 模拟隐藏后的裁剪：保留最近使用的 max 条，其余移除。
func TestIconCacheTrim(t *testing.T) {
	c := newIconCache(5)
	for _, k := range []string{"a", "b", "c", "d", "e"} {
		c.Put(k, nil)
	}
	c.Get("a") // 刷新 a
	c.Get("d") // 刷新 d
	if removed := c.Trim(2); removed != 3 {
		t.Fatalf("应移除 3 条，实得 %d", removed)
	}
	if got := c.Len(); got != 2 {
		t.Fatalf("裁剪后应为 2 条，实得 %d", got)
	}
	for _, k := range []string{"a", "d"} {
		if _, ok := c.Get(k); !ok {
			t.Errorf("最近使用的 %s 应保留", k)
		}
	}
	if removed := c.Trim(2); removed != 0 {
		t.Errorf("未超上限时 Trim 不应移除，实得 %d", removed)
	}
}

// TestIconCacheUpdateKeepsEntry 验证同一 key 重复 Put 只更新值并刷新 LRU，
// 不会新增条目或把其他条目误挤出去。
func TestIconCacheUpdateKeepsEntry(t *testing.T) {
	c := newIconCache(2)
	c.Put("a", nil)
	c.Put("b", nil)
	c.Put("a", nil) // 更新并刷新 a
	c.Put("c", nil) // 淘汰最久未用的 b
	if _, ok := c.Get("b"); ok {
		t.Error("重复 Put 后 b 仍是最冷条目，应被淘汰")
	}
	if _, ok := c.Get("a"); !ok {
		t.Error("a 应仍在缓存")
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
	if removed := c.Trim(0); removed != 1 {
		t.Errorf("Trim(0) 应移除唯一一条，实得 %d", removed)
	}
}
