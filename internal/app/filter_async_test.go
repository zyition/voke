// 异步过滤的接线测试：世代号丢弃过期结果、latest-wins 队列、频率快照失效。
package app

import (
	"path/filepath"
	"testing"

	"github.com/egoist/mygo"

	"voke/internal/logic/appsmodel"
	"voke/internal/logic/frequency"
	"voke/internal/logic/search"
)

// fakeRunner 记录投递的任务，由测试手动「完成」它们。
type fakeRunner struct {
	jobs  []filterJob
	apply func(filterJob, []appsmodel.AppEntry)
}

func (f *fakeRunner) submit(job filterJob) { f.jobs = append(f.jobs, job) }
func (f *fakeRunner) bind(_ *mygo.Window, apply func(filterJob, []appsmodel.AppEntry)) {
	f.apply = apply
}

// TestAsyncFilterDropsStaleResults：按键只投递、结果回来才落到 model；
// 期间又敲了字（世代号变了）→ 旧结果必须被丢弃。
func TestAsyncFilterDropsStaleResults(t *testing.T) {
	st := newWiredState(t, "Chrome", "Firefox")
	r := &fakeRunner{}
	st.runner = r
	r.bind(nil, st.applyFilterResult)

	typeInto(st, "chr")
	if len(r.jobs) != 1 {
		t.Fatalf("应投递 1 个过滤任务，实得 %d", len(r.jobs))
	}
	if len(st.model.Results) != 0 {
		t.Fatalf("结果还没回来，model 不该被改：%v", resultNames(st))
	}

	// 又敲一个字：世代号推进，旧任务作废。
	typeInto(st, "chro")
	if len(r.jobs) != 2 {
		t.Fatalf("应再投递一个任务，实得 %d", len(r.jobs))
	}
	old, cur := r.jobs[0], r.jobs[1]

	// 旧结果晚到 → 丢弃。
	st.applyFilterResult(old, search.FilterApps(old.query, old.apps, old.freq))
	if len(st.model.Results) != 0 {
		t.Fatalf("过期结果应被丢弃，实得 %v", resultNames(st))
	}
	// 最新结果到达 → 落到 model。
	st.applyFilterResult(cur, search.FilterApps(cur.query, cur.apps, cur.freq))
	if got := resultNames(st); got != "Chrome" {
		t.Fatalf("最新结果应生效，实得 %q", got)
	}
}

// TestAsyncFilterSkipsNonAppsModes：过滤在飞的时候切到 picker / provider 模式，
// 回来的 apps 结果不该污染这些模式的结果区。
func TestAsyncFilterSkipsNonAppsModes(t *testing.T) {
	st := newWiredState(t, "Chrome")
	r := &fakeRunner{}
	st.runner = r
	r.bind(nil, st.applyFilterResult)

	typeInto(st, "chr")
	job := r.jobs[0]
	typeInto(st, "#") // 进 picker：不走 apps 结果区
	st.applyFilterResult(job, search.FilterApps(job.query, job.apps, job.freq))
	if len(st.model.Results) != 0 {
		t.Fatalf("picker 模式下不该落 apps 结果：%v", resultNames(st))
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

// TestAsyncFilterQueueKeepsLatest：单槽队列满了就丢旧留新，submit 永不阻塞。
func TestAsyncFilterQueueKeepsLatest(t *testing.T) {
	f := &asyncFilter{jobs: make(chan filterJob, 1)} // 不启 run：手动检查队列
	f.submit(filterJob{gen: 1})
	f.submit(filterJob{gen: 2})
	f.submit(filterJob{gen: 3})
	select {
	case job := <-f.jobs:
		if job.gen != 3 {
			t.Fatalf("队列里应只剩最新任务（gen=3），实得 gen=%d", job.gen)
		}
	default:
		t.Fatal("队列里应有任务")
	}
}
