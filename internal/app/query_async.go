// 异步查询：provider 的 Query（含 Rank / Aggregate / apps 过滤）全部收拢到
// **唯一的查询线程**执行，渲染帧（主线程）只做 engine.Scan（纯字符串比较，微秒级）
// + 组装快照 + 投递，本帧立即回显输入；结果算好后经 Window.Update 回主线程替换
// （过期结果按世代号 + engineState 快照双重校验丢弃）。
//
// 为什么从「异步过滤」升级成「异步查询」：原先只有 apps 过滤走后台
// （filter_async.go），provider 的 Query 仍在主线程同步跑——插件态每个按键跑
// RunOne（vscode/zed 的 TTL 过期首键要同步开 SQLite 读库、terminal 要读
// settings.json），全局态每次落地跑 collectGlobalBatches + Aggregate。现在
// 这些全部进查询线程，主线程对 provider 只剩 ID/Name/Placeholder 这类纯读。
//
// 并发模型：
//   - 只有**一个**常驻查询 goroutine（进程生命周期内），不会每键起协程；
//   - 任务队列容量 1，满了就丢掉旧任务留最新的（latest-wins）：连续打字时
//     中间的查询根本不需要算完；
//   - worker 只碰 job 里的不可变快照（engineState / apps slice / freq 快照 /
//     全局 provider id 列表）与 bind 时捕获的注册表引用，不读 appState；
//   - `sync` 通道供低频**写**操作（reloadConfigs 的 SetConfig）插进查询线程
//     串行执行并等待完成——provider 实例的 config 字段因此始终单线程读写。
//
// provider 层零线程知识（见 providers.Provider 接口注释）：数据源闭包的新鲜度
// 与并发安全全部由装配层负责（winSnap / ttlFetch / cliphist 自身的锁 /
// AwakeState 的锁），provider 只做纯逻辑。
package app

import (
	"sync"

	"github.com/egoist/mygo"

	"voke/internal/logic/appsmodel"
	"voke/internal/logic/engine"
	"voke/internal/logic/frequency"
	"voke/internal/logic/search"
	"voke/internal/providers"
)

// queryJob 是一次查询的输入快照（主线程组装，查询线程只读）。
type queryJob struct {
	// gen 是投递世代号：任何使在途结果失效的事件（新投递 / apps 重扫 / 配置
	// 重载）都会推进它，落地的第一道校验。
	gen uint64
	// state 是投递时刻的 engine.State 快照（不可变作用域栈）。落地的第二道
	// 校验：期间用户改输入 / 切模式（State 不再相等）→ 丢弃。
	state engine.State
	// providerQuery 标明这次查询的种类：true = 插件态跑单个 provider（submitProviderQuery
	// 投递），false = apps 路径（Global 态 / apps 插件态，submitGlobalQuery 投递）。
	// 不能靠 state.Mode() 猜：apps 插件态也是 engine.Plugin，但走的是 apps 过滤。
	providerQuery bool
	// freq 是频率表只读快照（rank 的使用加成来源，见 freqSnapshot）。
	freq frequency.Store
	// keepChosen >= 0 时，插件态落地保持该选中行（剪贴板历史「采集 / 操作后
	// 刷新」用，行数变少时由 chooseProvider 钳位）；-1 = 复位到第 0 行。
	keepChosen int

	// 以下仅 Global 态填写：
	// apps 是应用全量列表快照（FilterApps 的输入）。
	apps []appsmodel.AppEntry
	// globals 是本次参与全局聚合的 provider id（注册序，主线程按
	// enabled && is_global 算好；查询线程不再读配置表）。空查询恒空
	// （空查询走 apps-only 路径，对照原 refreshGlobalAgg 的守卫）。
	globals []string
}

// queryResult 是查询线程算好的产物（经 Window.Update 回主线程落地）。
type queryResult struct {
	// 插件态（Plugin / SubMode）：
	ranked []providers.RankedMatch
	hints  []providers.Match
	// Global 态：
	apps []appsmodel.AppEntry // FilterApps 结果
	agg  providers.Aggregated // 非 apps 全局 provider 的分组聚合
}

// queryRunner 是查询任务的投递口。生产用 asyncQuery，测试 / 装配早期用 nil
// （submitQuery 就地同步算，语义相同、时机不同）。
type queryRunner interface {
	// submit 投递一次查询（latest-wins，永不阻塞）。
	submit(job queryJob)
	// sync 把 fn 排进查询线程串行执行并**等待完成**（低频写路径：SetConfig）。
	sync(fn func())
	// bind 注入查询实现与回主线程的落地接缝（装配期主线程调用）。
	bind(win *mygo.Window, process func(queryJob) queryResult, apply func(queryJob, queryResult))
}

// asyncQuery 是单查询线程 + 单槽队列 + 控制通道的查询器。
type asyncQuery struct {
	jobs chan queryJob
	ctrl chan func()

	mu      sync.RWMutex
	win     *mygo.Window
	process func(queryJob) queryResult
	apply   func(queryJob, queryResult)
}

func newAsyncQuery() *asyncQuery {
	q := &asyncQuery{
		jobs: make(chan queryJob, 1),
		ctrl: make(chan func()),
	}
	go q.run() // 唯一的常驻查询 goroutine，阻塞在 jobs/ctrl 上；进程退出即回收
	return q
}

// bind 注入查询实现与落地接缝（装配期主线程调用）。process 会在查询线程执行，
// 不得读写 appState（只允许闭包捕获的稳定引用，如 provider 注册表）。
func (q *asyncQuery) bind(win *mygo.Window, process func(queryJob) queryResult, apply func(queryJob, queryResult)) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.win, q.process, q.apply = win, process, apply
}

// submit 投递一次查询：队列只保留最新的任务（latest-wins）。
func (q *asyncQuery) submit(job queryJob) {
	select {
	case q.jobs <- job:
		return
	default:
	}
	// 队列已满：丢掉排队的旧任务，再放新的。查询线程手上正在跑的那次不打断
	// （它会正常算完，但结果按世代号 / State 校验作废），避免额外的取消机制。
	select {
	case <-q.jobs:
	default:
	}
	select {
	case q.jobs <- job:
	default:
	}
}

// sync 把 fn 交给查询线程执行并等待完成。fn 与 Query 在同一线程串行，因此
// 「改 provider 配置」与「读 provider 配置」不会交错（无锁即安全）。
// 设置保存是低频路径（用户点一次按钮），阻塞几十毫秒可接受。
func (q *asyncQuery) sync(fn func()) {
	done := make(chan struct{})
	q.ctrl <- func() { fn(); close(done) }
	<-done
}

func (q *asyncQuery) run() {
	for {
		select {
		case job := <-q.jobs:
			q.mu.RLock()
			win, process, apply := q.win, q.process, q.apply
			q.mu.RUnlock()
			if win == nil || process == nil || apply == nil {
				continue
			}
			res := process(job)
			// 只投递、不等待：主线程下一轮执行 apply（内含世代号 + State 校验）。
			win.Update(func() { apply(job, res) })
		case fn := <-q.ctrl:
			fn()
		}
	}
}

// processQuery 是查询线程的实现：按 job.state 的模式分派，只读 job 与 reg，
// 不碰 appState（reg 引用由 bind 时闭包捕获）。
func processQuery(reg *providers.Registry, job queryJob) queryResult {
	s := job.state
	ctx := providers.QueryCtx{Raw: s.Query(), Query: s.Query(), SubMode: s.SubMode()}
	if job.providerQuery {
		if reg != nil {
			if p, ok := reg.Get(s.PluginID()); ok {
				ranked, hints := providers.RunOne(p, ctx, job.freq)
				return queryResult{ranked: ranked, hints: hints}
			}
		}
		return queryResult{}
	}
	// Global：apps 过滤 + （非空查询时）逐全局 provider 现查现聚合。
	res := queryResult{apps: search.FilterApps(s.Query(), job.apps, job.freq)}
	if reg == nil || len(job.globals) == 0 {
		return res
	}
	batches := make([]providers.Batch, 0, len(job.globals))
	for _, id := range job.globals {
		p, ok := reg.Get(id)
		if !ok {
			continue
		}
		if ms := p.Query(ctx); len(ms) > 0 {
			batches = append(batches, providers.Batch{ProviderID: id, Matches: ms})
		}
	}
	res.agg = reg.Aggregate(batches, job.freq)
	return res
}
