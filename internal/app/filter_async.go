// 异步过滤：输入只负责改查询，结果由后台算好再回主线程。
//
// 为什么：原来的 onQueryChanged 在**渲染帧内**跑 FilterApps 再画结果，输入回显要等
// 过滤跑完——键盘事件、输入框文本、结果三者被绑在同一帧的一个同步调用里。
// 现在按键只做 engine.Scan（纯字符串比较，微秒级）+ 投递任务，本帧立即回显；
// 结果算好后经 Window.Update 回主线程替换（过期结果按世代号丢弃）。
//
// 并发模型：
//   - 只有**一个**常驻 worker goroutine（进程生命周期内），不会每键起协程；
//   - 任务队列容量 1，满了就丢掉旧任务留最新的（latest-wins）：连续打字时
//     中间的查询根本不需要算完；
//   - worker 只碰 job 里的不可变快照（apps slice / freq 快照），不读 appState。
package app

import (
	"sync"

	"github.com/egoist/mygo"

	"voke/internal/logic/appsmodel"
	"voke/internal/logic/frequency"
	"voke/internal/logic/search"
)

// filterJob 是一次过滤的输入快照。
type filterJob struct {
	gen   uint64
	query string
	apps  []appsmodel.AppEntry
	freq  frequency.Store
}

// filterRunner 是过滤任务的投递口。生产用 asyncFilter，测试 / 装配早期用
// nil（appState.beginFilter 直接同步算，语义相同、时机不同）。
type filterRunner interface {
	submit(job filterJob)
	bind(win *mygo.Window, apply func(filterJob, []appsmodel.AppEntry))
}

// asyncFilter 是单 worker + 单槽队列的过滤器。
type asyncFilter struct {
	jobs chan filterJob

	mu    sync.RWMutex
	win   *mygo.Window
	apply func(filterJob, []appsmodel.AppEntry)
}

func newAsyncFilter() *asyncFilter {
	f := &asyncFilter{jobs: make(chan filterJob, 1)}
	go f.run() // 唯一的常驻 goroutine，阻塞在 jobs 上；进程退出即回收
	return f
}

// bind 注入回主线程的接缝（装配期主线程调用）。
func (f *asyncFilter) bind(win *mygo.Window, apply func(filterJob, []appsmodel.AppEntry)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.win, f.apply = win, apply
}

// submit 投递一次过滤：队列只保留最新的任务（latest-wins）。
func (f *asyncFilter) submit(job filterJob) {
	select {
	case f.jobs <- job:
		return
	default:
	}
	// 队列已满：丢掉排队的旧任务，再放新的。worker 手上正在跑的那次不打断
	// （它会正常算完，但结果按世代号作废），避免额外的取消机制。
	select {
	case <-f.jobs:
	default:
	}
	select {
	case f.jobs <- job:
	default:
	}
}

func (f *asyncFilter) run() {
	for job := range f.jobs {
		results := search.FilterApps(job.query, job.apps, job.freq)
		f.mu.RLock()
		win, apply := f.win, f.apply
		f.mu.RUnlock()
		if win == nil || apply == nil {
			continue
		}
		// 只投递、不等待：主线程下一轮执行 apply（内含世代号校验）。
		win.Update(func() { apply(job, results) })
	}
}
