// 窗口快照：把「枚举顶层窗口」从输入路径挪到后台。
//
// 为什么必须挪：platform.EnumerateWindows 是 EnumWindows + 逐窗取标题 / 进程名 /
// exe 路径，窗口多时阻塞几十到几百毫秒。它原先跑在 Global 态每次按键的帧内
// （onQueryChanged → refreshGlobalAgg → windows.Query），等于让输入回显去等枚举。
//
// 现在主线程只读一份快照（读一下 slice 头，微秒级）。刷新只有两个触发点，
// **查询不再触发枚举**：
//   - 面板唤起时（showPanelEntry）；
//   - 面板可见期间的定时刷新（app 层的 windowTimer，隐藏即停）。
//
// 每次刷新单飞（busy 守卫），goroutine 跑完即退出，不会累积。
package app

import (
	"sync"
	"time"

	"github.com/egoist/mygo"

	"voke/internal/platform"
	"voke/internal/providers/windows"
)

// windowSnapshot 是主线程读、后台写的窗口列表。
//
// 线程约定：
//   - get 只在主线程调用（provider Query 的调用点）；
//   - 后台只写 entries/at/busy/gen，全部在 mu 内；
//   - 回主线程只经 Window.Update（与 apps.go 的 bindApps 同款纪律）。
type windowSnapshot struct {
	mu      sync.Mutex
	entries []windows.Entry
	at      time.Time
	busy    bool

	winMu sync.RWMutex
	win   *mygo.Window
	apply func()
}

func newWindowSnapshot() *windowSnapshot {
	return &windowSnapshot{}
}

// bind 注入回主线程的接缝（窗口 + 重算回调）。装配期主线程调用。
func (s *windowSnapshot) bind(win *mygo.Window, apply func()) {
	s.winMu.Lock()
	defer s.winMu.Unlock()
	s.win, s.apply = win, apply
}

// get 返回当前快照。**纯读**：不触发枚举（枚举只在唤起与定时刷新时发生），
// 数据源查询因此不会被枚举拖住。
func (s *windowSnapshot) get() []windows.Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.entries
}

// refreshAsync 投递一次后台刷新：已有刷新在跑就忽略（单飞）。
// 只在主线程调用（唤起 / 定时器回调）。
func (s *windowSnapshot) refreshAsync() {
	s.mu.Lock()
	if s.busy {
		s.mu.Unlock()
		return
	}
	s.busy = true
	s.mu.Unlock()
	go s.refresh()
}

// refresh 是后台刷新体：枚举 → 换快照 → 回主线程请重算。跑完必清 busy。
func (s *windowSnapshot) refresh() {
	defer func() {
		s.mu.Lock()
		s.busy = false
		s.mu.Unlock()
	}()

	infos := platform.EnumerateWindows()
	entries := make([]windows.Entry, len(infos))
	for i, w := range infos {
		entries[i] = windows.Entry{
			Hwnd: w.Hwnd, Title: w.Title,
			ProcessName: w.ProcessName, ProcessPath: w.ProcessPath,
		}
	}

	s.mu.Lock()
	s.entries, s.at = entries, time.Now()
	s.mu.Unlock()

	s.winMu.RLock()
	win, apply := s.win, s.apply
	s.winMu.RUnlock()
	if win != nil && apply != nil {
		win.Update(apply) // 只投递、不等待（主线程下一轮执行）
	}
}
