// iconcache 的同包测试：TestIconCacheEvictsFIFO 直接读未导出字段
// c.mu / c.items / c.order 来断言「map 与顺序表同步收缩」，只有同包才碰得到，
// 故随 iconCache 类型一起从 apps_test.go 搬进来（不改造成外部黑盒）。
package iconcache

import (
	"fmt"
	"sync"
	"testing"
)

// TestIconCacheEvictsFIFO 验证缓存达到上限后按 FIFO 淘汰最旧条目，
// 且 map 与顺序表同步收缩（不会只删一半）。
func TestIconCacheEvictsFIFO(t *testing.T) {
	var calls int
	c := New(3, func(path string) ([]byte, error) {
		calls++
		return []byte(path), nil
	})
	for _, p := range []string{"a", "b", "c"} {
		if got := c.Get(p); string(got) != p {
			t.Fatalf("Get(%q) = %q", p, got)
		}
	}
	if calls != 3 {
		t.Fatalf("3 条应各 fetch 一次：calls=%d", calls)
	}
	// 重复读取已缓存条目：不应再触发 fetch。
	for _, p := range []string{"a", "b", "c"} {
		_ = c.Get(p)
	}
	if calls != 3 {
		t.Fatalf("已缓存条目重复读取不应触发 fetch：calls=%d", calls)
	}
	// 第 4 条触发 FIFO 淘汰：最旧的 "a" 出列。
	_ = c.Get("d")
	if calls != 4 {
		t.Fatalf("第 4 条应触发一次 fetch：calls=%d", calls)
	}
	// "a" 已出列 → 重新 fetch（缓存满，顺带淘汰当下的最旧 "b"）。
	_ = c.Get("a")
	if calls != 5 {
		t.Fatalf("被淘汰的 a 应重新 fetch：calls=%d", calls)
	}
	// 缓存条数恒等于上限，map 与顺序表同步。
	c.mu.Lock()
	items, order := len(c.items), len(c.order)
	c.mu.Unlock()
	if items != 3 || order != 3 {
		t.Fatalf("缓存应恒为 3 条：items=%d order=%d", items, order)
	}
	// 重新插入的 "a" 与未被淘汰的 "c"/"d" 都在缓存里（命中不再 fetch）。
	for _, p := range []string{"a", "c", "d"} {
		before := calls
		_ = c.Get(p)
		if calls != before {
			t.Fatalf("%q 应命中缓存", p)
		}
	}
}

// TestIconCacheFetchFailureNotCached 验证 fetch 失败返回 nil 且不进缓存
// （下次仍会重试，不把失败钉死）。
func TestIconCacheFetchFailureNotCached(t *testing.T) {
	var calls int
	c := New(8, func(string) ([]byte, error) {
		calls++
		return nil, fmt.Errorf("boom")
	})
	if got := c.Get("x"); got != nil {
		t.Fatalf("失败应返回 nil，实际 %q", got)
	}
	_ = c.Get("x")
	if calls != 2 {
		t.Fatalf("失败不应进缓存，应重试：calls=%d", calls)
	}
}

// TestCacheEvictsByInsertionNotAccess 锁定淘汰判据是**插入序**而非命中序：
// 反复命中最旧的 a 不会把它排到最后。误改成 LRU（命中即 append 到队尾）后，
// 本用例会在「a 被挤出去、应当重新 fetch」这一步红——这也是它与
// TestIconCacheEvictsFIFO 的分工：那条查容量与两侧同步，这条查淘汰认谁。
func TestCacheEvictsByInsertionNotAccess(t *testing.T) {
	var fetches []string
	c := New(2, func(path string) ([]byte, error) {
		fetches = append(fetches, path)
		return []byte(path), nil
	})
	_ = c.Get("a")
	_ = c.Get("b")
	for i := 0; i < 5; i++ { // 热读 a：命中，不进 fetch、也不改插入序
		before := len(fetches)
		if got := c.Get("a"); string(got) != "a" {
			t.Fatalf("热读 a 应命中缓存，实际 %q", got)
		}
		if len(fetches) != before {
			t.Fatalf("第 %d 次热读 a 不该触发 fetch：fetches=%v", i, fetches)
		}
	}
	// 缓存满（a、b），插入 c 淘汰的是**最旧插入的 a**（LRU 会淘汰 b）。
	_ = c.Get("c")
	c.mu.Lock()
	order := append([]string(nil), c.order...)
	c.mu.Unlock()
	if len(order) != 2 || order[0] != "b" || order[1] != "c" {
		t.Fatalf("淘汰应认插入序：队列 %v，期望 [b c]", order)
	}
	// a 已出列 → 再读必须重新 fetch（命中序淘汰的实现会在这里返回旧值不 fetch）。
	_ = c.Get("a")
	if len(fetches) != 4 || fetches[3] != "a" {
		t.Fatalf("被淘汰的 a 应重新 fetch：fetches=%v", fetches)
	}
}

// TestCacheConcurrentInsertKeepsMapAndOrderInSync 验证并发写入下
// 「map 与顺序表长度相等、且队列里的 key 都在 map 里」这条不变量成立
// （fetch 在锁外跑，写回靠锁内二次检查；淘汰漏删任何一侧都会在这里红）。
func TestCacheConcurrentInsertKeepsMapAndOrderInSync(t *testing.T) {
	const (
		maxKeys = 4
		total   = 40
	)
	c := New(maxKeys, func(path string) ([]byte, error) { return []byte(path), nil })
	var wg sync.WaitGroup
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = c.Get(fmt.Sprintf("k%02d", i))
		}(i)
	}
	wg.Wait()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.items) != maxKeys || len(c.order) != maxKeys {
		t.Fatalf("缓存应有界且两侧同步：items=%d order=%d，期望各 %d",
			len(c.items), len(c.order), maxKeys)
	}
	for _, k := range c.order {
		if _, ok := c.items[k]; !ok {
			t.Fatalf("顺序表里的 %q 不在 map 中（淘汰删了一半）", k)
		}
	}
}
