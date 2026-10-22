// Package iconcache 是图标字节的进程内有界缓存（§7.2.1 三层铁律里的 logic 层）。
//
// 分层：本包**零 mygo、零 x/sys/windows、零 go-pinyin**，只依赖标准库，故
// `go test ./internal/logic/...` 在 Linux 原生可跑。取字节的具体后端
// （mygo Shell.FileIcon）由 platform 侧（apps.go）作为 fetch 回调注入——
// 本包只管「按 key 存字节 / 命中返字节 / 满则 FIFO 淘汰」这套与平台无关的
// 记账逻辑，原样从 apps.go 搬来（语义逐字保持）。
package iconcache

import (
	"log"
	"sync"
)

// Cache 是图标 PNG 的进程内有界缓存：map + FIFO 顺序表，达到上限淘汰
// 最旧一条。fetch 在锁外调用（FileIcon 是毫秒级 IO，别堵住其他图标的读取），
// 写回时二次检查，避免并发重复获取同一路径。失败**不进缓存**：路径全部来自
// InstalledApps，失败应极罕见，重复失败只是多几次快速失败调用。
type Cache struct {
	mu    sync.Mutex
	max   int
	items map[string][]byte
	order []string
	fetch func(path string) ([]byte, error)
}

// New 构造容量上限为 max、未命中经 fetch 现取的缓存。
func New(max int, fetch func(path string) ([]byte, error)) *Cache {
	return &Cache{max: max, items: make(map[string][]byte, max), fetch: fetch}
}

// Get 返回 path 对应的图标 PNG；未命中经 fetch 现取并放入缓存。失败返回 nil。
func (c *Cache) Get(path string) []byte {
	c.mu.Lock()
	png, ok := c.items[path]
	c.mu.Unlock()
	if ok {
		return png
	}
	png, err := c.fetch(path)
	if err != nil {
		log.Printf("取图标失败 %s：%v", path, err)
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, dup := c.items[path]; !dup {
		if len(c.order) >= c.max {
			delete(c.items, c.order[0])
			c.order = c.order[1:]
		}
		c.items[path] = png
		c.order = append(c.order, path)
	}
	return png
}
