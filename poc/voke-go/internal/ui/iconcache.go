// iconcache.go：解码后图标位图的有界 LRU 缓存（internal/ui 层，主线程专用）。
//
// 为什么要有界：图标 PNG 字节（platform.AppIcon 背后的 iconcache）本来就有 256
// 条上限，但解码后的 *goui.Bitmap 之前是**无界 map**。真机 155 个应用、200% 缩放
// （48px 图标）一轮约 1.4MB；长会话里窗口 provider 还会带入不同 exe 路径，数量
// 没有自然上界。这里补上 256 条上限，满则淘汰**最久未使用**的条目。
//
// 与 Wox 的隐藏裁剪配套：面板隐藏 10 秒后由 app 层调用 Trim(64) 把缓存压到 64 条
// （见 internal/app/panel.go），下次唤起只重取被裁掉的可见图标。LRU 而非 FIFO：
// 裁剪应保留最近仍在用的图标（例如刚搜索过的应用），而不是按插入顺序误伤。
//
// 语义与 platform 的 iconcache 保持同一取舍：**nil 也缓存**（Get 返回 ok=true），
// 表示「已确认无图标 / 解码失败」，避免每帧重复 FileIcon；Get 未命中才是 ok=false。
// 只在主线程（渲染与 Window.Update 回调）读写，不加锁。
package ui

import goui "github.com/egoist/mygo/ui"

// iconCacheMax 是解码位图缓存条目上限。当前 155 个应用在 48px 下约 1.4MB；
// 256 覆盖全部应用并给窗口 provider 的 exe 图标留余量，最坏约 2.3MB（48px；
// 更高 DPI 下按像素平方增长，但有界）。
const iconCacheMax = 256

// iconCacheEntry 记录位图与最近使用序号（LRU 判据）。
type iconCacheEntry struct {
	bmp  *goui.Bitmap
	used uint64
}

// IconCache 是按路径缓存解码图标位图的有界 LRU。
type IconCache struct {
	max   int
	clock uint64
	items map[string]*iconCacheEntry
}

// NewIconCache 构造生产用默认容量的缓存。
func NewIconCache() *IconCache { return newIconCache(iconCacheMax) }

// newIconCache 用指定容量构造（测试用）。
func newIconCache(max int) *IconCache {
	if max <= 0 {
		max = iconCacheMax
	}
	return &IconCache{max: max, items: make(map[string]*iconCacheEntry, max)}
}

func (c *IconCache) touch(e *iconCacheEntry) {
	c.clock++
	e.used = c.clock
}

// Get 取缓存并刷新 LRU；第二个返回值区分「缓存了 nil」与「未命中」。
func (c *IconCache) Get(path string) (*goui.Bitmap, bool) {
	if c == nil {
		return nil, false
	}
	e, ok := c.items[path]
	if !ok {
		return nil, false
	}
	c.touch(e)
	return e.bmp, true
}

// Put 写入缓存（b 可为 nil = 负缓存）。已存在的 key 只更新值并刷新 LRU。
func (c *IconCache) Put(path string, b *goui.Bitmap) {
	if c == nil {
		return
	}
	if c.max <= 0 {
		c.max = iconCacheMax
	}
	if c.items == nil {
		c.items = make(map[string]*iconCacheEntry, c.max)
	}
	if e, ok := c.items[path]; ok {
		e.bmp = b
		c.touch(e)
		return
	}
	if len(c.items) >= c.max {
		c.evictColdest()
	}
	c.clock++
	c.items[path] = &iconCacheEntry{bmp: b, used: c.clock}
}

// Trim 把缓存压到不超过 max 条，返回移除数量；用于面板隐藏后的延迟裁剪。
func (c *IconCache) Trim(max int) int {
	if c == nil || max < 0 {
		return 0
	}
	removed := 0
	for len(c.items) > max {
		c.evictColdest()
		removed++
	}
	return removed
}

// evictColdest 移除 used 最小的条目；容量/裁剪时才调用，O(n) 可接受。
func (c *IconCache) evictColdest() {
	var oldest string
	var oldestUsed uint64
	first := true
	for key, e := range c.items {
		if first || e.used < oldestUsed {
			oldest, oldestUsed, first = key, e.used, false
		}
	}
	if !first {
		delete(c.items, oldest)
	}
}

// Len 返回当前条目数（诊断 / 测试用）。
func (c *IconCache) Len() int {
	if c == nil {
		return 0
	}
	return len(c.items)
}
