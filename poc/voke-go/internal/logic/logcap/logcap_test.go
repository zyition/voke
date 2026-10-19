// 生产 Rust `voke-sys/src/logcap.rs` 尾部 `#[cfg(test)] mod tests`
// （logcap.rs:198-395）的逐条等价移植：场景①—⑧一一对应，断言口径不变，
// 并在此基础上把 Rust 的「存在性」断言补成**绝对内容**断言
// （期望值写死，不从被测常量反推）。
package logcap

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// openAppend 模拟接线层的打开方式：创建 + **追加** + 可读写
// （删头批要读回全文，纯 append 是 O_WRONLY 读会 EBADF），对应 Rust 的
// `open_append`（logcap.rs:208-217）。
func openAppend(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("打开日志文件失败 %s：%v", path, err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// fileLines 独立读回全部行（口径同 Rust 的 `read_lines` / `BufRead::lines()`：
// 未以 \n 收尾的末尾段算一行，logcap.rs:219-226）。
//
// **故意不复用被测的 scanLines**：用被测函数验被测函数就是同义反复，
// 数行口径写错时两边会一起错。
func fileLines(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读回日志失败 %s：%v", path, err)
	}
	var out []string
	start := 0
	for i := 0; i < len(raw); i++ {
		if raw[i] == '\n' {
			out = append(out, strings.TrimSuffix(string(raw[start:i]), "\r"))
			start = i + 1
		}
	}
	if start < len(raw) { // 末尾未以 \n 收尾的部分行也算一行
		out = append(out, string(raw[start:]))
	}
	return out
}

// equalLines 断言文件行内容逐字相等。
func equalLines(t *testing.T, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("文件内容 = %d 行 %#v\n想 要 %d 行 %#v", len(got), got, len(want), want)
	}
}

// writeLog 一条日志一次行级写入（完整一行），对应 Rust 的 `write_log` /
// `writeln!`（logcap.rs:228-230）。
func writeLog(t *testing.T, w *RollingLogWriter, s string) {
	t.Helper()
	if _, err := fmt.Fprintln(w, s); err != nil {
		t.Fatalf("写日志失败：%v", err)
	}
}

// rollScenario 写 total 行（line-0 .. line-{total-1}），返回最终文件行内容，
// 对应 Rust 的 `roll_scenario`（logcap.rs:232-242）。
func rollScenario(t *testing.T, path string, max, total int) []string {
	t.Helper()
	w, err := New(openAppend(t, path), max)
	if err != nil {
		t.Fatalf("New 失败：%v", err)
	}
	for i := 0; i < total; i++ {
		writeLog(t, w, fmt.Sprintf("line-%d", i))
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("flush 失败：%v", err)
	}
	return fileLines(t, path)
}

// 场景①（logcap.rs:244-271）：跨「启动」不清空——第二次以追加模式打开同一
// 文件，旧内容仍在，且 New() 数到的基线包含旧行（上限跨重启生效）。
func TestAppendAcrossRestartsKeepsHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "append.log")
	w1, err := New(openAppend(t, path), 100)
	if err != nil {
		t.Fatalf("New 失败：%v", err)
	}
	for i := 0; i < 3; i++ {
		writeLog(t, w1, fmt.Sprintf("run1-line-%d", i))
	}
	if err := w1.Flush(); err != nil {
		t.Fatalf("flush 失败：%v", err)
	}

	// 第二次「启动」：旧内容仍在，计数含旧 3 行。
	w2, err := New(openAppend(t, path), 100)
	if err != nil {
		t.Fatalf("New 失败：%v", err)
	}
	if got := w2.Lines(); got != 3 {
		t.Errorf("重启后应数到旧 3 行，得 %d", got)
	}

	w3, err := New(openAppend(t, path), 100)
	if err != nil {
		t.Fatalf("New 失败：%v", err)
	}
	writeLog(t, w3, "run2-line-0")
	if err := w3.Flush(); err != nil {
		t.Fatalf("flush 失败：%v", err)
	}
	equalLines(t, fileLines(t, path), []string{"run1-line-0", "run1-line-1", "run1-line-2", "run2-line-0"})
	if got := w3.RollCount(); got != 0 {
		t.Errorf("跨启动追加不该触发删除，rolls = %d", got)
	}
}

// 场景②（logcap.rs:273-288）：未超限纯追加、顺序原样、无标记。
func TestUnderLimitAppendsVerbatim(t *testing.T) {
	path := filepath.Join(t.TempDir(), "verbatim.log")
	w, err := New(openAppend(t, path), 10)
	if err != nil {
		t.Fatalf("New 失败：%v", err)
	}
	for i := 0; i < 3; i++ {
		writeLog(t, w, fmt.Sprintf("line-%d", i))
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("flush 失败：%v", err)
	}
	equalLines(t, fileLines(t, path), []string{"line-0", "line-1", "line-2"})
	if got := w.RollCount(); got != 0 {
		t.Errorf("未超限 rolls = %d，想要 0", got)
	}
	if got := w.Lines(); got != 3 {
		t.Errorf("未超限 Lines = %d，想要 3", got)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读回日志失败：%v", err)
	}
	if strings.Contains(string(raw), "已删除") {
		t.Errorf("未超限不该出现滚动标记：%s", raw)
	}
}

// 场景③（logcap.rs:290-304）：超限删头批——最旧行消失、最新行在、有滚动
// 标记、总行数回落。
//
// 10 行写满后第 11 行触发：删最早 1 行 → 9 旧 + 标记 + 最新 = 11 行
// （比上限多 1，就是那行标记——Rust 原测断言 `lines.len() == 11`）。
func TestOverLimitDropsOldestBatchFromHead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roll.log")
	lines := rollScenario(t, path, 10, 11)
	want := []string{
		"line-1", "line-2", "line-3", "line-4", "line-5",
		"line-6", "line-7", "line-8", "line-9",
		"---- 日志超过 10 行上限，已删除最早 1 行 ----",
		"line-10",
	}
	equalLines(t, lines, want)
	if len(lines) != 11 {
		t.Errorf("总行数 = %d，想要 11", len(lines))
	}
	if slices.Contains(lines, "line-0") {
		t.Error("最旧一行 line-0 应被删")
	}
}

// 场景④（logcap.rs:306-327）：批量 = max(1, N/10)——N=10 删 1、N=100 删 10。
func TestBatchSizeIsMaxOneTenth(t *testing.T) {
	l10 := rollScenario(t, filepath.Join(t.TempDir(), "batch10.log"), 10, 11)
	if !slices.Contains(l10, "---- 日志超过 10 行上限，已删除最早 1 行 ----") {
		t.Errorf("N=10 应一批删 1 行：%#v", l10)
	}

	l100 := rollScenario(t, filepath.Join(t.TempDir(), "batch100.log"), 100, 101)
	if !slices.Contains(l100, "---- 日志超过 100 行上限，已删除最早 10 行 ----") {
		t.Errorf("N=100 应一批删 10 行：%#v", l100)
	}
	if slices.Contains(l100, "line-9") {
		t.Error("最早 10 行（含 line-9）应消失")
	}
	if !slices.Contains(l100, "line-10") {
		t.Error("第 11 行 line-10 应还在")
	}
	if len(l100) != 92 {
		t.Errorf("总行数 = %d，想要 92（90 旧 + 标记 + 最新）", len(l100))
	}
}

// 场景⑤（logcap.rs:329-343）：上限 1 不 panic、不死循环、内容有界
// （标记行 + 最新一行）。
func TestLimitOneDoesNotPanic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "limit1.log")
	w, err := New(openAppend(t, path), 1)
	if err != nil {
		t.Fatalf("New 失败：%v", err)
	}
	for i := 0; i < 5; i++ {
		writeLog(t, w, fmt.Sprintf("line-%d", i))
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("flush 失败：%v", err)
	}
	lines := fileLines(t, path)
	if len(lines) > 2 {
		t.Errorf("上限 1 也应受控：%#v", lines)
	}
	equalLines(t, lines, []string{
		"---- 日志超过 1 行上限，已删除最早 2 行 ----",
		"line-4",
	})
	if got := w.RollCount(); got != 4 {
		t.Errorf("上限 1、写 5 行应滚 4 次，得 %d", got)
	}
}

// 场景⑥（logcap.rs:345-365）：删批后连续写不会每写一行就删一批。
func TestNoPruneOnEveryWriteAfterBatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "steady.log")
	w, err := New(openAppend(t, path), 100)
	if err != nil {
		t.Fatalf("New 失败：%v", err)
	}
	for i := 0; i < 100; i++ {
		writeLog(t, w, fmt.Sprintf("line-%d", i))
	}
	if got := w.RollCount(); got != 0 {
		t.Errorf("写满未超不删，rolls = %d", got)
	}
	writeLog(t, w, "line-100") // 101 > 100：触发，删 10 行
	if got := w.RollCount(); got != 1 {
		t.Fatalf("第 101 行应触发一次删除，rolls = %d", got)
	}
	if got := w.Lines(); got != 92 {
		t.Errorf("删批后追加完应为 92 行（90 旧 + 标记 + 最新），得 %d", got)
	}
	// 删批后 92 行，再写 8 行到 100 都不触发（Rust 原测写到 109 前不触发）。
	for i := 101; i < 109; i++ {
		writeLog(t, w, fmt.Sprintf("line-%d", i))
	}
	if got := w.RollCount(); got != 1 {
		t.Errorf("删批后不该每写一行就删一批，rolls = %d", got)
	}
	writeLog(t, w, "line-109") // 101 > 100：才再次触发
	if got := w.RollCount(); got != 2 {
		t.Errorf("写满后第 10 行才再触发，rolls = %d", got)
	}
}

// 场景⑦（logcap.rs:367-388）：无换行写入也计数，且与行级读回口径一致。
func TestWritesWithoutNewlineAreCounted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "partial.log")
	w, err := New(openAppend(t, path), 3)
	if err != nil {
		t.Fatalf("New 失败：%v", err)
	}
	if _, err := w.Write([]byte("abc")); err != nil { // 未完成行，计 1 行
		t.Fatalf("写失败：%v", err)
	}
	if got := w.Lines(); got != 1 {
		t.Fatalf("未完成行应计 1 行，得 %d", got)
	}
	writeLog(t, w, "def") // 与 "abc" 同属一行（部分行被补完）
	if got := w.Lines(); got != 1 {
		t.Errorf("写入口径与读回一致：还是一行，得 %d", got)
	}
	equalLines(t, fileLines(t, path), []string{"abcdef"})

	writeLog(t, w, "ghi")
	writeLog(t, w, "jkl")
	if got := w.Lines(); got != 3 {
		t.Fatalf("写了 3 行，Lines = %d", got)
	}
	writeLog(t, w, "mno") // 4 > 3：删头批
	if got := w.RollCount(); got != 1 {
		t.Fatalf("超限应滚 1 次，rolls = %d", got)
	}
	lines := fileLines(t, path)
	equalLines(t, lines, []string{
		"ghi", "jkl",
		"---- 日志超过 3 行上限，已删除最早 1 行 ----",
		"mno",
	})
	if slices.Contains(lines, "abcdef") {
		t.Error("最旧行 abcdef 应被删")
	}
	if lines[len(lines)-1] != "mno" {
		t.Errorf("最新内容应在末尾，得 %q", lines[len(lines)-1])
	}
	if got := w.Lines(); got != len(lines) {
		t.Errorf("计数与文件行数应同口径：Lines = %d，文件 %d", got, len(lines))
	}
}

// 场景⑧（logcap.rs:390-394）：兜底值三处一致（本包 / build.rs / main.rs，
// Go 侧对应接线层的 ldflags 兜底），改时一起改。
func TestDefaultMaxLinesMatchesBuildScript(t *testing.T) {
	if DefaultMaxLines != 9999 {
		t.Errorf("DefaultMaxLines = %d，想要 9999", DefaultMaxLines)
	}
}
