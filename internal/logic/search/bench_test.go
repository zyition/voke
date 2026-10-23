// FilterApps 的基准：守住「一次按键对上千条候选打分」的成本与分配量。
// 变动过滤链（匹配、档位、排序）后跑：
//
//	go test -run XXX -bench FilterApps -benchmem ./internal/logic/search
//
// 基线（1200 条**互不相同**的候选，Linux/amd64，Core Ultra 5 338H）：
//
//	FilterApps-6              ~800µs/op   104KB/op   16 allocs/op
//	FilterAppsSingleLetter-6  ~950µs/op   143KB/op   16 allocs/op
//
// 「互不相同」很重要：搜索键缓存装不下时（见 match.CacheCapacity）命中率归零，
// 同样的 1200 条候选会退化成 5.1ms / 4.1MB / 17 万次分配。
package search

import (
	"fmt"
	"testing"

	"voke/internal/logic/appsmodel"
	"voke/internal/logic/frequency"
)

// benchApps 造 n 条名字：中英混合 + 多词，覆盖名 / 全拼 / 首字母三类搜索键。
func benchApps(n int) []appsmodel.AppEntry {
	base := []string{
		"Visual Studio Code", "Google Chrome", "Windows PowerShell", "微信", "钉钉",
		"网易云音乐", "Task Manager", "Inno Setup Compiler", "VLC media player",
		"腾讯会议", "File Explorer", "Microsoft Edge",
	}
	out := make([]appsmodel.AppEntry, 0, n)
	for i := 0; len(out) < n; i++ {
		name := base[i%len(base)]
		if i >= len(base) {
			name = fmt.Sprintf("%s %d", name, i/len(base))
		}
		out = append(out, appsmodel.AppEntry{Name: name, Path: "p", ID: fmt.Sprintf("id-%d", i)})
	}
	return out
}

func benchFreq(apps []appsmodel.AppEntry) frequency.Store {
	f := frequency.New()
	for i, a := range apps {
		if i%20 == 0 {
			f[frequency.Key(appsmodel.ProviderID, a.Name)] = uint32(i%30 + 1)
		}
	}
	return f
}

func BenchmarkFilterApps(b *testing.B) {
	apps := benchApps(1200)
	freq := benchFreq(apps)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		FilterApps("vis", apps, freq)
	}
}

// BenchmarkFilterAppsSingleLetter 是短查询的最坏情况：命中面最大、档位判定跑得最多。
func BenchmarkFilterAppsSingleLetter(b *testing.B) {
	apps := benchApps(1200)
	freq := benchFreq(apps)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		FilterApps("a", apps, freq)
	}
}
