// iconcache.go：解码后图标位图的有界 FIFO 缓存（internal/ui 层，主线程专用）。
//
// 为什么要有界：图标 PNG 字节（platform.AppIcon 背后的 iconcache）本来就有 512 条
// 上限，但解码后的 *goui.Bitmap 之前是**无界 map**。真机 155 个应用、200% 缩放
// （48px 图标）一轮约 1.4MB；长会话里窗口 provider 还会带入不同 exe 路径，数量
// 没有自然上界。这里补上与 PNG 缓存同量级的 256 条上限，满则按插入序淘汰最旧条目。
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

// IconCache 是按路径缓存解码图标位图的有界 FIFO。
type IconCache struct {
	max   int
	items map[string]*goui.Bitmap
	order []string
}

// NewIconCache 构造生产用默认容量的缓存。
func NewIconCache() *IconCache { return newIconCache(iconCacheMax) }

// newIconCache 用指定容量构造（测试用）。
func newIconCache(max int) *IconCache {
	if max <= 0 {
		max = iconCacheMax
	}
	return &IconCache{max: max, items: make(map[string]*goui.Bitmap, max)}
}

// Get 取缓存；第二个返回值区分「缓存了 nil」与「未命中」。
func (c *IconCache) Get(path string) (*goui.Bitmap, bool) {
	if c == nil {
		return nil, false
	}
	b, ok := c.items[path]
	return b, ok
}

// Put 写入缓存（b 可为 nil = 负缓存）。已存在的 key 只更新值，不重复记账。
func (c *IconCache) Put(path string, b *goui.Bitmap) {
	if c == nil {
		return
	}
	if c.max <= 0 {
		c.max = iconCacheMax
	}
	if c.items == nil {
		c.items = make(map[string]*goui.Bitmap, c.max)
	}
	if _, ok := c.items[path]; ok {
		c.items[path] = b
		return
	}
	if len(c.order) >= c.max {
		delete(c.items, c.order[0])
		c.order = c.order[1:]
	}
	c.items[path] = b
	c.order = append(c.order, path)
}

// Len 返回当前条目数（诊断 / 测试用）。
func (c *IconCache) Len() int {
	if c == nil {
		return 0
	}
	return len(c.items)
}
