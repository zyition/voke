package platform

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

// TestWarnBenignSkipOnce 钉死「每路径只记一次」的去重：同一 key 多次调用只落一行，
// 防的是未来改动把 Zed 缺表那类「每次查询都重演」的良性跳过重新退回 log.Printf 刷屏
// （commit f1f40f8 修的正是这个）。变异：若 warnBenignSkipOnce 去掉 LoadOrStore 去重，本例变红。
func TestWarnBenignSkipOnce(t *testing.T) {
	const key = "test:benign-skip-once"
	const key2 = key + "#2"
	// 两个 key 进出都要清干净：-count=2 让本例在同一进程跑两遍，包级 benignSkipLogged
	// 跨遍存活——只清 key 而漏清 key2，第二遍的 key2 会命中上一遍留下的旧值、记 0 行
	// （正是 -race -count=2 -shuffle 下暴露的测试隔离缺陷，非被测代码缺陷）。
	benignSkipLogged.Delete(key)
	benignSkipLogged.Delete(key2)
	t.Cleanup(func() {
		benignSkipLogged.Delete(key)
		benignSkipLogged.Delete(key2)
	})

	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(orig) })

	for i := 0; i < 3; i++ {
		warnBenignSkipOnce(key, "benign skip %s", "pathA")
	}
	// 另一个 key 应各自独立记一次。
	warnBenignSkipOnce(key2, "benign skip %s", "pathB")

	if got := strings.Count(buf.String(), "benign skip pathA"); got != 1 {
		t.Errorf("同 key 3 次调用应只记 1 行，实记 %d 行：%q", got, buf.String())
	}
	if got := strings.Count(buf.String(), "benign skip pathB"); got != 1 {
		t.Errorf("不同 key 应各自记 1 行，实记 %d 行：%q", got, buf.String())
	}
}
