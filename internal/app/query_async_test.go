// 异步查询的接线测试：世代号 + State 双重校验丢弃过期结果、latest-wins 队列、
// 频率快照失效、provider 结果落地（keepChosen / 空态 hint）、TTL 数据源与唤起失效。
package app

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo"

	"voke/internal/logic/appsmodel"
	"voke/internal/logic/engine"
	"voke/internal/logic/frequency"
	"voke/internal/logic/testutil"
	"voke/internal/providers"
)

// fakeRunner 记录投递的任务，由测试手动「完成」它们。
type fakeRunner struct {
	jobs    []queryJob
	apply   func(queryJob, queryResult)
	process func(queryJob) queryResult
	synced  []func()
}

func (f *fakeRunner) submit(job queryJob) { f.jobs = append(f.jobs, job) }
func (f *fakeRunner) sync(fn func())      { f.synced = append(f.synced, fn); fn() }
func (f *fakeRunner) bind(_ *mygo.Window, process func(queryJob) queryResult, apply func(queryJob, queryResult)) {
	f.process, f.apply = process, apply
}

// complete 用生产 processQuery 算结果并走生产 applyQueryResult 落地，模拟一次
// 「查询线程算完 → Window.Update 回主线程」的完整往返。
func complete(st *appState, reg *providers.Registry, job queryJob) {
	st.applyQueryResult(job, processQuery(reg, job))
}

// TestAsyncQueryDropsStaleResults：按键只投递、结果回来才落到 model；
// 期间又敲了字（世代号推进 + State 变化）→ 旧结果必须被丢弃。
func TestAsyncQueryDropsStaleResults(t *testing.T) {
	st := newWiredState(t, "Chrome", "Firefox")
	r := &fakeRunner{}
	st.runner = r

	typeInto(st, "chr")
	if len(r.jobs) != 1 {
		t.Fatalf("应投递 1 个查询任务，实得 %d", len(r.jobs))
	}
	if len(st.model.Results) != 0 {
		t.Fatalf("结果还没回来，model 不该被改：%v", resultNames(st))
	}

	// 又敲一个字：世代号推进、State 变化，旧任务作废。
	typeInto(st, "chro")
	if len(r.jobs) != 2 {
		t.Fatalf("应再投递一个任务，实得 %d", len(r.jobs))
	}
	old, cur := r.jobs[0], r.jobs[1]

	complete(st, nil, old) // 旧结果晚到 → 丢弃
	if len(st.model.Results) != 0 {
		t.Fatalf("过期结果应被丢弃，实得 %v", resultNames(st))
	}
	complete(st, nil, cur) // 最新结果到达 → 落到 model
	if got := resultNames(st); got != "Chrome" {
		t.Fatalf("最新结果应生效，实得 %q", got)
	}
}

// TestStateMismatchDropsResults：世代号相同但模式/输入变了（例如查询在飞时切到
// picker，picker 分支不投递、gen 不变），靠 State.Equal 第二道校验丢弃。
func TestStateMismatchDropsResults(t *testing.T) {
	st := newWiredState(t, "Chrome")
	r := &fakeRunner{}
	st.runner = r

	typeInto(st, "chr")
	job := r.jobs[0]
	typeInto(st, "#") // 进 picker：不走 apps 结果区，gen 不变但 State 变了

	complete(st, nil, job)
	if len(st.model.Results) != 0 {
		t.Fatalf("State 不匹配，apps 结果不该落地：%v", resultNames(st))
	}
}

// TestAppRescanDropsInFlightQuery：重扫装上新的应用快照后，基于旧快照、还在飞的
// 查询任务不得把新列表覆盖回去（applyAppsSnapshot 必须推进 queryGen）。
func TestAppRescanDropsInFlightQuery(t *testing.T) {
	st := newWiredState(t, "Chrome")
	r := &fakeRunner{}
	st.runner = r

	typeInto(st, "chr")
	old := r.jobs[0]

	// 重扫完成：新快照里只有 Chromium，重投一次查询。
	st.applyAppsSnapshot(testutil.MkApps("Chromium"))
	cur := r.jobs[len(r.jobs)-1]
	complete(st, nil, cur)
	if got := resultNames(st); got != "Chromium" {
		t.Fatalf("重扫后应显示新快照的结果，实得 %q", got)
	}

	// 旧任务晚到：快照世代已推进 → 必须丢弃。
	complete(st, nil, old)
	if got := resultNames(st); got != "Chromium" {
		t.Fatalf("旧快照结果覆盖了新列表：实得 %q", got)
	}
}

// TestFreqSnapshotInvalidatedByRecord：后台读的是快照，主线程记一次使用后
// 快照必须重建，否则「刚用过的应用」在下一键里不生效。
func TestFreqSnapshotInvalidatedByRecord(t *testing.T) {
	t.Setenv(freqPathEnv, filepath.Join(t.TempDir(), "frequency.toml")) // 不写真实数据目录
	st := newWiredState(t, "Chrome")
	st.freq = frequency.New()

	before := st.freqSnapshot()
	if before == nil || len(before) != 0 {
		t.Fatalf("空表快照应非 nil 且为空：%v", before)
	}

	st.recordUse(appsmodel.ProviderID, "Chrome")
	after := st.freqSnapshot()
	if after[frequency.Key(appsmodel.ProviderID, "Chrome")] != 1 {
		t.Fatalf("记一次使用后快照应包含该键：%v", after)
	}
	if before[frequency.Key(appsmodel.ProviderID, "Chrome")] != 0 {
		t.Errorf("旧快照不该被就地修改（后台可能还在读它）")
	}
}

// TestAsyncQueryQueueKeepsLatest：单槽队列满了就丢旧留新，submit 永不阻塞。
func TestAsyncQueryQueueKeepsLatest(t *testing.T) {
	q := &asyncQuery{jobs: make(chan queryJob, 1)} // 不启 run：手动检查队列
	q.submit(queryJob{gen: 1})
	q.submit(queryJob{gen: 2})
	q.submit(queryJob{gen: 3})
	select {
	case job := <-q.jobs:
		if job.gen != 3 {
			t.Fatalf("队列里应只剩最新任务（gen=3），实得 gen=%d", job.gen)
		}
	default:
		t.Fatal("队列里应有任务")
	}
}

// TestProcessQueryGlobalAggregates：Global 态的 processQuery 会把 enabled &&
// is_global 且在注册表里的 provider（默认 calculator）现查现聚合进 agg.Groups。
func TestProcessQueryGlobalAggregates(t *testing.T) {
	st := newGlobalState(t)
	table := st.providers.BuildTable()
	state := engine.Scan(table, engine.State{}, "1+1")
	job := queryJob{state: state, globals: st.globalProviderIDs()}

	res := processQuery(st.pluginReg, job)
	ids := groupIDs(res.agg.Groups)
	if !containsID(ids, "calculator") {
		t.Errorf("1+1 应命中 calculator 组：%v", ids)
	}
	if containsID(ids, "base64") || containsID(ids, "awake") {
		t.Errorf("非全局插件不该进组：%v", ids)
	}
}

// TestProcessQueryEmptyQuerySkipsGlobals：空查询时 globals 为空，processQuery
// 不跑任何全局 provider（apps-only 路径）。
func TestProcessQueryEmptyQuerySkipsGlobals(t *testing.T) {
	st := newGlobalState(t)
	job := queryJob{state: engine.State{}, apps: testutil.MkApps("Chrome")}
	res := processQuery(st.pluginReg, job)
	if len(res.agg.Groups) != 0 {
		t.Errorf("空查询不该有全局组：%v", groupIDs(res.agg.Groups))
	}
	if len(res.apps) != 1 {
		t.Errorf("apps 过滤仍应执行，实得 %d 条", len(res.apps))
	}
}

// TestProcessQueryPluginMode：插件态只跑目标 provider（RunOne 排序），不跑 apps 过滤。
func TestProcessQueryPluginMode(t *testing.T) {
	st := newGlobalState(t)
	table := st.providers.BuildTable()
	state := engine.Scan(table, engine.State{}, "@b ") // 进 base64 插件第一级
	if state.Mode() != engine.Plugin {
		t.Fatalf("应进插件态，实得 %s", state.Mode())
	}
	res := processQuery(st.pluginReg, queryJob{state: state, providerQuery: true})
	if res.apps != nil {
		t.Errorf("插件态不该算 apps 结果：%v", res.apps)
	}
	// base64 一级有「加密 / 解密」两个子模式候选。
	if len(res.ranked) == 0 {
		t.Error("base64 插件应产出候选行")
	}
}

// TestApplyQueryResultKeepsChosen：keepChosen >= 0 时插件态落地保持选中行
// （剪贴板历史操作后刷新用）；-1 复位到第 0 行。
func TestApplyQueryResultKeepsChosen(t *testing.T) {
	st := newGlobalState(t)
	table := st.providers.BuildTable()
	state := engine.Scan(table, engine.State{}, "@b ")
	st.engineState = state
	st.queryGen = 7

	ranked := []providers.RankedMatch{
		{Match: providers.MatchExecute("base64", "a", "", providers.Action{}, 1)},
		{Match: providers.MatchExecute("base64", "b", "", providers.Action{}, 1)},
		{Match: providers.MatchExecute("base64", "c", "", providers.Action{}, 1)},
	}
	job := queryJob{gen: 7, state: state, providerQuery: true, keepChosen: 2}
	st.applyQueryResult(job, queryResult{ranked: ranked})
	if st.provChosen != 2 || len(st.provRows) != 3 {
		t.Fatalf("keepChosen 应保持选中 2、结果 3 条，实得 chosen=%d rows=%d", st.provChosen, len(st.provRows))
	}

	// -1 → 复位到第 0 行。
	job2 := queryJob{gen: 7, state: state, providerQuery: true, keepChosen: -1}
	st.applyQueryResult(job2, queryResult{ranked: ranked})
	if st.provChosen != 0 {
		t.Errorf("keepChosen=-1 应复位选中到第 0 行，实得 %d", st.provChosen)
	}
}

// TestSubmitQueryKeepingSelectionCarriesAnchor：后台刷新（窗口快照 / 应用重扫）
// 投递查询时必须带上当前选中行的锚点，否则落地会复位到第 0 行。
func TestSubmitQueryKeepingSelectionCarriesAnchor(t *testing.T) {
	st := newGlobalState(t)
	st.engineState = engine.NewState(engine.Plugin, "terminal", "", "")
	st.queryGen = 1
	st.provRows = []providers.RankedMatch{
		{Match: providers.MatchExecute("terminal", "a", "", providers.Action{}, 1)},
		{Match: providers.MatchExecute("terminal", "b", "", providers.Action{}, 1)},
	}
	st.provChosen = 1
	r := &fakeRunner{}
	st.runner = r

	st.submitQueryKeepingSelection()
	if len(r.jobs) != 1 {
		t.Fatalf("应投递 1 个任务，实得 %d", len(r.jobs))
	}
	sel := r.jobs[0].sel
	if sel == nil || sel.index != 1 {
		t.Fatalf("后台刷新应带选中锚点 index=1：%+v", sel)
	}
	if want := providerRowKey(st.provRows[1].Match); sel.key != want {
		t.Errorf("锚点身份键 = %q，want %q", sel.key, want)
	}
}

// TestApplyQueryResultRestoresProviderSelection：结果行重排后按身份键找回原选中，
// 而不是复位到第 0 行。
func TestApplyQueryResultRestoresProviderSelection(t *testing.T) {
	st := newGlobalState(t)
	state := engine.NewState(engine.Plugin, "terminal", "", "")
	st.engineState = state
	st.queryGen = 1

	mk := func(title string) providers.RankedMatch {
		return providers.RankedMatch{Match: providers.MatchExecute("terminal", title, "", providers.Action{}, 1)}
	}
	st.provRows = []providers.RankedMatch{mk("a"), mk("b"), mk("c")}
	st.provChosen = 1
	sel := st.captureSelection()

	reordered := []providers.RankedMatch{mk("c"), mk("a"), mk("b")}
	st.applyQueryResult(queryJob{gen: 1, state: state, providerQuery: true, keepChosen: -1, sel: sel},
		queryResult{ranked: reordered})
	if st.provChosen != 2 {
		t.Errorf("应按身份恢复选中到 b（新列表下标 2），实得 %d", st.provChosen)
	}
}

// TestApplyQueryResultRestoresAppsSelection：apps-only 列表（空查询 / 重扫）同样
// 按名称 + 路径恢复选中；条目消失时退回原下标钳位，不回 0。
func TestApplyQueryResultRestoresAppsSelection(t *testing.T) {
	st := newGlobalState(t)
	state := engine.NewState(engine.Global, "", "", "")
	st.engineState = state
	st.queryGen = 1
	app := func(name string) appsmodel.AppEntry {
		return appsmodel.AppEntry{Name: name, Path: "shell:AppsFolder\\" + strings.ToLower(name)}
	}
	st.model.Results = []appsmodel.AppEntry{app("Chrome"), app("Chromium"), app("Firefox")}
	st.model.Chosen = 1
	sel := st.captureSelection()

	st.applyQueryResult(queryJob{gen: 1, state: state, sel: sel},
		queryResult{apps: []appsmodel.AppEntry{app("Firefox"), app("Chrome"), app("Chromium")}})
	if st.model.Chosen != 2 {
		t.Errorf("应按名称 + 路径恢复选中到 Chromium（新下标 2），实得 %d", st.model.Chosen)
	}

	// 条目消失：原下标 2 仍在范围内 → 保持 2，而不是回 0。
	st.queryGen = 2
	st.model.Chosen = 2
	sel = st.captureSelection()
	st.applyQueryResult(queryJob{gen: 2, state: state, sel: sel},
		queryResult{apps: testutil.MkApps("A", "B", "C")})
	if st.model.Chosen != 2 {
		t.Errorf("同身份条目消失应按原下标钳位到 2，实得 %d", st.model.Chosen)
	}
}

// TestApplyQueryResultEmptyProviderHint：结果为空时 provHint 用 hints[0]。
func TestApplyQueryResultEmptyProviderHint(t *testing.T) {
	st := newGlobalState(t)
	table := st.providers.BuildTable()
	state := engine.Scan(table, engine.State{}, "@b ")
	st.engineState = state
	st.queryGen = 1
	st.applyQueryResult(queryJob{gen: 1, state: state, providerQuery: true, keepChosen: -1},
		queryResult{hints: []providers.Match{providers.MatchHint("base64", "解码失败", "")}})
	if st.provHint != "解码失败" {
		t.Errorf("有 hint 时应用 hint 标题，实得 %q", st.provHint)
	}
}

// ── 装配层 TTL 数据源（provider 已不持缓存，测试搬到这里）──

func TestTtlFetchCachesWithinTTL(t *testing.T) {
	calls := 0
	fetch := ttlFetch(func() int { calls++; return calls }, nil)
	if a, b := fetch(), fetch(); a != 1 || b != 1 {
		t.Errorf("TTL 内应复用同一份数据：%d, %d", a, b)
	}
	if calls != 1 {
		t.Errorf("TTL 内只应加载一次：calls=%d", calls)
	}
}

func TestTtlFetchInvalidateForcesReload(t *testing.T) {
	calls := 0
	snaps := &ttlSnapSet{}
	fetch := ttlFetch(func() int { calls++; return calls }, snaps)
	fetch() // calls=1，命中缓存
	if got := fetch(); got != 1 {
		t.Errorf("失效前应命中缓存：%d", got)
	}
	snaps.invalidateAll()
	if got := fetch(); got != 2 {
		t.Errorf("唤起失效后应重取（得 2）：%d", got)
	}
	if calls != 2 {
		t.Errorf("失效后应只多加载一次：calls=%d", calls)
	}
}

// TestTtlFetchThreadSafe：invalidate 与 fetch 并发不数据竞争、不把数据源写坏。
func TestTtlFetchThreadSafe(t *testing.T) {
	snaps := &ttlSnapSet{}
	fetch := ttlFetch(func() int { return 42 }, snaps)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = fetch()
				snaps.invalidateAll()
			}
		}()
	}
	wg.Wait()
	if got := fetch(); got != 42 {
		t.Errorf("并发后数据源仍应可用：%d", got)
	}
}

// TestAsyncQuerySyncRunsSerially：sync 把 fn 排进查询线程执行并等待完成。
func TestAsyncQuerySyncRunsSerially(t *testing.T) {
	q := newAsyncQuery()
	done := make(chan struct{})
	go func() {
		called := false
		q.sync(func() { called = true })
		if !called {
			t.Error("sync 应在返回前执行 fn")
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("sync 未按时完成（查询线程没处理 ctrl）")
	}
}
