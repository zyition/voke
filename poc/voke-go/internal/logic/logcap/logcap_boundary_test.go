// Rust 原测没覆盖到的边界（任务红线8「补边界」）：N 很小 / 恰好 N / 刚超 N /
// 空日志 / 标记行计入后总数 / 单笔写入大于上限 / 非法上限。
//
// 期望值一律**写死绝对值**（手推自 logcap.rs 的算式），不从被测常量或
// batchSize() 反推，避免同义反复。
package logcap

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBatchSizeTable 逐值钉死 `max(1, N/10)`（logcap.rs:107-110）：整除向下、
// 至少 1，N=19 仍是 1、N=20 才变 2。
func TestBatchSizeTable(t *testing.T) {
	cases := []struct {
		maxLines int
		want     int
	}{
		{0, 1}, {1, 1}, {9, 1}, {10, 1}, {11, 1}, {19, 1},
		{20, 2}, {21, 2}, {29, 2}, {30, 3}, {99, 9}, {100, 10},
		{9999, 999}, {10000, 1000},
	}
	for _, c := range cases {
		// batchSize 只读 maxLines，无需真文件。
		w := &RollingLogWriter{maxLines: c.maxLines}
		if got := w.batchSize(); got != c.want {
			t.Errorf("batchSize(N=%d) = %d，想要 %d", c.maxLines, got, c.want)
		}
	}
}

// TestMarkLineExactCopy 钉死标记行的中文文案与数字位序（logcap.rs:155-159）：
// 第一个数是**上限**、第二个是**实际删除数**，写反或改字都会红。
func TestMarkLineExactCopy(t *testing.T) {
	if got := markLine(100, 10); got != "---- 日志超过 100 行上限，已删除最早 10 行 ----" {
		t.Errorf("markLine(100,10) = %q", got)
	}
	if got := markLine(1, 0); got != "---- 日志超过 1 行上限，已删除最早 0 行 ----" {
		t.Errorf("markLine(1,0) = %q", got)
	}
}

// TestPruneLinesKeepsNewestAndAppendsMarker 是纯裁剪核心的正常路径：
// 删最旧 10 行、剩余顺序原样、标记行**排在末尾**且计入总数
// （对应 logcap.rs:149-160 的 `remaining.len() + 1`）。
func TestPruneLinesKeepsNewestAndAppendsMarker(t *testing.T) {
	all := []string{"l0", "l1", "l2", "l3", "l4", "l5", "l6", "l7", "l8", "l9", "l10", "l11"}
	out, dropped := pruneLines(all, 10, 100)
	want := []string{"l10", "l11", "---- 日志超过 100 行上限，已删除最早 10 行 ----"}
	if dropped != 10 {
		t.Errorf("dropped = %d，想要 10", dropped)
	}
	equalLines(t, out, want)
	if len(out) != 3 {
		t.Errorf("写回后行数 = %d，想要 3（剩余 + 标记）", len(out))
	}
}

// TestPruneLinesSaturatesWhenDropExceedsTotal：drop 超过实际行数时删完全部，
// 标记行按**实际**删除数报告（`drop.min(all.len())`，logcap.rs:135-149）——
// 单笔写入超过上限时 drop 会大于总行数，这条不守住就会报出虚数甚至负数。
func TestPruneLinesSaturatesWhenDropExceedsTotal(t *testing.T) {
	all := []string{"only-1", "only-2"}
	out, dropped := pruneLines(all, 99, 5)
	if dropped != 2 {
		t.Errorf("dropped = %d，想要 2（饱和到实际行数）", dropped)
	}
	equalLines(t, out, []string{"---- 日志超过 5 行上限，已删除最早 2 行 ----"})
}

// TestPruneLinesOnEmptyFileLeavesOnlyMarker：空日志被要求删头批时，只剩标记行
// （`skip` 天然饱和、不死循环，logcap.rs:135-136）。drop==0 由 pruneHead 的
// 早返回挡住，不会走到这里。
func TestPruneLinesOnEmptyFileLeavesOnlyMarker(t *testing.T) {
	out, dropped := pruneLines(nil, 7, 3)
	if dropped != 0 {
		t.Errorf("dropped = %d，想要 0", dropped)
	}
	equalLines(t, out, []string{"---- 日志超过 3 行上限，已删除最早 0 行 ----"})
}

// TestNewOnEmptyFileCountsZero：空日志基线为 0 行、无残留部分行。
func TestNewOnEmptyFileCountsZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.log")
	w, err := New(openAppend(t, path), 10)
	if err != nil {
		t.Fatalf("New 失败：%v", err)
	}
	if got := w.Lines(); got != 0 {
		t.Errorf("空文件 Lines = %d，想要 0", got)
	}
	if w.partial {
		t.Error("空文件不该有残留部分行")
	}
	if got := w.RollCount(); got != 0 {
		t.Errorf("空文件 rolls = %d，想要 0", got)
	}
}

// TestNewCountsUnterminatedLastLineAsOne：上次「启动」末尾是半行（进程被砍在
// 一行中间），重启后按行级口径算 1 行且标记为 partial，下一笔写入与它同行
// （logcap.rs:80-86 的末尾字节判断）。
func TestNewCountsUnterminatedLastLineAsOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tail.log")
	if err := os.WriteFile(path, []byte("half-0\nhalf-1-tail"), 0o644); err != nil {
		t.Fatalf("预置日志失败：%v", err)
	}
	w, err := New(openAppend(t, path), 10)
	if err != nil {
		t.Fatalf("New 失败：%v", err)
	}
	if got := w.Lines(); got != 2 {
		t.Errorf("末尾半行要计入，Lines = %d，想要 2", got)
	}
	if !w.partial {
		t.Error("末尾未以换行收尾，partial 应为 true")
	}
	writeLog(t, w, "-cont") // 补完那一行
	if got := w.Lines(); got != 2 {
		t.Errorf("补完同一行仍该是 2，得 %d", got)
	}
	equalLines(t, fileLines(t, path), []string{"half-0", "half-1-tail-cont"})
}

// TestExactlyAtLimitDoesNotPrune：恰好写满 N 行不删（`>` 写成 `>=` 就会红）。
func TestExactlyAtLimitDoesNotPrune(t *testing.T) {
	path := filepath.Join(t.TempDir(), "exact.log")
	lines := rollScenario(t, path, 5, 5)
	equalLines(t, lines, []string{"line-0", "line-1", "line-2", "line-3", "line-4"})
	w, err := New(openAppend(t, path), 5)
	if err != nil {
		t.Fatalf("New 失败：%v", err)
	}
	if got := w.Lines(); got != 5 {
		t.Errorf("写满 Lines = %d，想要 5", got)
	}
}

// TestJustOverLimitPrunesOnce：刚超 N 一行触发一次删除，总行数 = N+1
// （多出的正是**计入行数**的那行标记；批大小 1，因为 N=5 < 10）。
func TestJustOverLimitPrunesOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "justover.log")
	w, err := New(openAppend(t, path), 5)
	if err != nil {
		t.Fatalf("New 失败：%v", err)
	}
	for i := 0; i < 6; i++ {
		writeLog(t, w, fmt.Sprintf("line-%d", i))
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("flush 失败：%v", err)
	}
	equalLines(t, fileLines(t, path), []string{
		"line-1", "line-2", "line-3", "line-4",
		"---- 日志超过 5 行上限，已删除最早 1 行 ----",
		"line-5",
	})
	if got := w.RollCount(); got != 1 {
		t.Errorf("rolls = %d，想要 1", got)
	}
	if got := w.Lines(); got != 6 {
		t.Errorf("Lines = %d，想要 6（与文件同口径）", got)
	}
}

// TestSmallLimitSecondRollDropsMore：小上限（N=10，批大小 1）第二次滚动时
// 「剩余 + 标记」已比上限多 1，于是按 need 多删 1 行（logcap.rs:176-181 的
// `max(batch, need)`）。need 少算 1 或标记不计入行数，这里的绝对内容都会红。
//
// 注意**第一次的标记行会留在文件里**（第二轮只删最旧的 2 行内容），所以尾部
// 是「旧标记 + line-10 + 新标记 + line-11」——这是 Rust 的同款形态
// （logcap.rs:345-365 场景⑥ 也在描述这件事）。
func TestSmallLimitSecondRollDropsMore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tworoll.log")
	w, err := New(openAppend(t, path), 10)
	if err != nil {
		t.Fatalf("New 失败：%v", err)
	}
	for i := 0; i < 12; i++ {
		writeLog(t, w, fmt.Sprintf("line-%d", i))
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("flush 失败：%v", err)
	}
	equalLines(t, fileLines(t, path), []string{
		"line-3", "line-4", "line-5", "line-6", "line-7", "line-8", "line-9",
		"---- 日志超过 10 行上限，已删除最早 1 行 ----",
		"line-10",
		"---- 日志超过 10 行上限，已删除最早 2 行 ----",
		"line-11",
	})
	if got := w.RollCount(); got != 2 {
		t.Errorf("rolls = %d，想要 2", got)
	}
	if got := w.Lines(); got != 11 {
		t.Errorf("Lines = %d，想要 11（上限 10 + 一行标记）", got)
	}
}

// TestSingleWriteBiggerThanLimitStaysBounded：一笔写入含 10 行而上限只有 3
// ——drop 请求（need=7）大于文件实际行数，删完（0 行）只落标记，本行照常
// 追加；下一笔再按 need 多删，总量有界（logcap.rs:135-136、176-181）。
func TestSingleWriteBiggerThanLimitStaysBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bigwrite.log")
	w, err := New(openAppend(t, path), 3)
	if err != nil {
		t.Fatalf("New 失败：%v", err)
	}
	if _, err := w.Write([]byte("a\nb\nc\nd\ne\nf\ng\nh\ni\nj\n")); err != nil {
		t.Fatalf("写失败：%v", err)
	}
	equalLines(t, fileLines(t, path), []string{
		"---- 日志超过 3 行上限，已删除最早 0 行 ----",
		"a", "b", "c", "d", "e", "f", "g", "h", "i", "j",
	})
	if got := w.Lines(); got != 11 {
		t.Errorf("单笔超限时 Lines = %d，想要 11", got)
	}
	writeLog(t, w, "k") // 12 > 3：按 need=9 多删，删掉「标记 + a..h」共 9 行
	equalLines(t, fileLines(t, path), []string{
		"i", "j",
		"---- 日志超过 3 行上限，已删除最早 9 行 ----",
		"k",
	})
	if got := w.Lines(); got != 4 {
		t.Errorf("Lines = %d，想要 4", got)
	}
	if got := w.RollCount(); got != 2 {
		t.Errorf("rolls = %d，想要 2", got)
	}
}

// TestZeroLimitStaysBounded：上限 0（非法值，兜底属接线层）既不 panic 也不
// 死循环，总量稳在「标记 + 最新」两行（logcap.rs:107-110 的 `max(1, ...)`
// 保证批大小至少 1）。
func TestZeroLimitStaysBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "zero.log")
	w, err := New(openAppend(t, path), 0)
	if err != nil {
		t.Fatalf("New 失败：%v", err)
	}
	for i := 0; i < 4; i++ {
		writeLog(t, w, fmt.Sprintf("line-%d", i))
	}
	lines := fileLines(t, path)
	if len(lines) > 2 {
		t.Errorf("上限 0 也该有界：%#v", lines)
	}
	equalLines(t, lines, []string{
		"---- 日志超过 0 行上限，已删除最早 2 行 ----",
		"line-3",
	})
}

// TestLinesMatchesFileEveryStep：写入侧计数与行级读回**逐步骤**同口径
// （logcap.rs:26-34 立的那条口径规矩）。独立实现的 fileLines 做对照，
// 任何一处行数算错都会红。
func TestLinesMatchesFileEveryStep(t *testing.T) {
	path := filepath.Join(t.TempDir(), "steady-step.log")
	w, err := New(openAppend(t, path), 7)
	if err != nil {
		t.Fatalf("New 失败：%v", err)
	}
	for i := 0; i < 25; i++ {
		writeLog(t, w, fmt.Sprintf("s-%d", i))
		if got, want := w.Lines(), len(fileLines(t, path)); got != want {
			t.Fatalf("第 %d 行后 Lines = %d，文件 %d", i, got, want)
		}
		if got := w.Lines(); got > 8 { // 上限 7 + 标记行的 1
			t.Fatalf("第 %d 行后行数失控：%d", i, got)
		}
	}
	if got := w.Lines(); got != 8 {
		t.Errorf("收尾 Lines = %d，想要 8", got)
	}
}

// TestSplitWriteDoesNotReprune：状态已因标记行略超上限时，「只补换行」那笔
// delta=0，必须**不**再触发删除（logcap.rs:170-174 的 `delta > 0` 守卫）。
// 去掉守卫就会把标记行插进行中间并多出一个标记。
func TestSplitWriteDoesNotReprune(t *testing.T) {
	path := filepath.Join(t.TempDir(), "split.log")
	w, err := New(openAppend(t, path), 3)
	if err != nil {
		t.Fatalf("New 失败：%v", err)
	}
	for i := 0; i < 3; i++ {
		writeLog(t, w, fmt.Sprintf("line-%d", i))
	}
	if _, err := w.Write([]byte("line-3")); err != nil { // 触发删批，lines 落到 4
		t.Fatalf("写失败：%v", err)
	}
	if got := w.Lines(); got != 4 {
		t.Fatalf("删批 + 本行后 Lines = %d，想要 4", got)
	}
	if _, err := w.Write([]byte("\n")); err != nil { // delta=0：不该再删
		t.Fatalf("写失败：%v", err)
	}
	equalLines(t, fileLines(t, path), []string{
		"line-1", "line-2",
		"---- 日志超过 3 行上限，已删除最早 1 行 ----",
		"line-3",
	})
	if got := w.RollCount(); got != 1 {
		t.Errorf("补换行不该再滚，rolls = %d，想要 1", got)
	}
	if got := w.Lines(); got != 4 {
		t.Errorf("补换行不改行数，Lines = %d，想要 4", got)
	}
}

// TestEmptyWriteIsNoOp：空 buf 既不动文件也不动计数——钉的是「空写 = 无操作」
// 这条契约（包注释「受控差异」1）。
//
// 说清楚覆盖边界：把 `len(buf) == 0` 那条早返回变异掉，本用例**仍绿**
// （变异记录 M0 存活）——因为 `delta > 0` 与 `n > 0` 两道判据已经把这笔吸收了。
// 早返回是显式契约 + 防「以后有人动那两道判据」，不是行为必需。
func TestEmptyWriteIsNoOp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "emptywrite.log")
	w, err := New(openAppend(t, path), 3)
	if err != nil {
		t.Fatalf("New 失败：%v", err)
	}
	writeLog(t, w, "keep")
	if _, err := w.Write(nil); err != nil {
		t.Fatalf("空写不该报错：%v", err)
	}
	if _, err := w.Write([]byte{}); err != nil {
		t.Fatalf("空写不该报错：%v", err)
	}
	if got := w.Lines(); got != 1 {
		t.Errorf("空写后 Lines = %d，想要 1", got)
	}
	equalLines(t, fileLines(t, path), []string{"keep"})

	// 残留半行 + 空写：计数不能被打扰。
	if _, err := w.Write([]byte("tail")); err != nil {
		t.Fatalf("写失败：%v", err)
	}
	if _, err := w.Write([]byte{}); err != nil {
		t.Fatalf("空写不该报错：%v", err)
	}
	if got := w.Lines(); got != 2 {
		t.Errorf("半行后空写 Lines = %d，想要 2", got)
	}
	writeLog(t, w, "-more")
	equalLines(t, fileLines(t, path), []string{"keep", "tail-more"})
}

// oneByteFile 让每次底层写入只落 1 字节，逼出「短写按实际落盘量重算计数」
// 那条分支（logcap.rs:184-189：`let (delta, partial_after) =
// self.buf_line_delta(&buf[..n])`）。Rust 原测用真文件、拿不到短写，
// 这里用同一份真文件套一层只写一字的 Writer。
type oneByteFile struct{ *os.File }

func (f oneByteFile) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return f.File.Write(p[:1])
}

// writeFull 按 io.Writer 契约处理短写：反复写直到全部落盘。
func writeFull(t *testing.T, w *RollingLogWriter, s string) {
	t.Helper()
	b := []byte(s)
	for len(b) > 0 {
		n, err := w.Write(b)
		if err != nil {
			t.Fatalf("写失败：%v", err)
		}
		if n <= 0 {
			t.Fatalf("短写不推进（还剩 %d 字节）", len(b))
		}
		b = b[n:]
	}
}

// TestShortWriteCountsOnlyWrittenBytes：一笔 6 字节、两行的写入被拆成 6 次
// 单字节落盘，计数必须与逐字节实际落盘量一致（不是一开始那个整段预估），
// 收尾仍是 2 行、无残留部分行。
func TestShortWriteCountsOnlyWrittenBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "short.log")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("打开失败：%v", err)
	}
	defer func() { _ = f.Close() }()
	w, err := New(oneByteFile{f}, 10)
	if err != nil {
		t.Fatalf("New 失败：%v", err)
	}
	writeFull(t, w, "ab\ncd\n")
	if got := w.Lines(); got != 2 {
		t.Errorf("短写后 Lines = %d，想要 2", got)
	}
	if w.partial {
		t.Error("以换行收尾，partial 应为 false")
	}
	equalLines(t, fileLines(t, path), []string{"ab", "cd"})
}

// TestShortWritePruneAtLimit：短写 + 超限并存时，删头批只看整段预估
// （logcap.rs:169 那一笔预算），落盘后按实际字节累加，最终形态与一次性写入
// 完全一致。
func TestShortWritePruneAtLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shortprune.log")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("打开失败：%v", err)
	}
	defer func() { _ = f.Close() }()
	w, err := New(oneByteFile{f}, 2)
	if err != nil {
		t.Fatalf("New 失败：%v", err)
	}
	writeFull(t, w, "ab\ncd\n") // 恰好写满 2 行
	if got := w.RollCount(); got != 0 {
		t.Fatalf("写满不该删，rolls = %d", got)
	}
	writeFull(t, w, "ef\n") // 第 3 行触发
	equalLines(t, fileLines(t, path), []string{
		"cd",
		"---- 日志超过 2 行上限，已删除最早 1 行 ----",
		"ef",
	})
	if got := w.Lines(); got != 3 {
		t.Errorf("Lines = %d，想要 3", got)
	}
	if got := w.RollCount(); got != 1 {
		t.Errorf("rolls = %d，想要 1", got)
	}
}

// TestCRLFLineEndsCountAsOneLine：`\r\n` 结尾算一行且读回只剩内容
// （口径对齐 `BufRead::lines()`，logcap.rs:30）。
func TestCRLFLineEndsCountAsOneLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crlf.log")
	w, err := New(openAppend(t, path), 10)
	if err != nil {
		t.Fatalf("New 失败：%v", err)
	}
	if _, err := w.Write([]byte("a\r\nb\r\n")); err != nil {
		t.Fatalf("写失败：%v", err)
	}
	if got := w.Lines(); got != 2 {
		t.Errorf("Lines = %d，想要 2", got)
	}
	equalLines(t, fileLines(t, path), []string{"a", "b"})
}

// TestPruneClearsPartialFlag：删头批时末尾还是半行（上一笔没写换行），写回后
// 文件是完整收尾的，`partial` 必须清零（logcap.rs:161）——不清就会把下一笔的
// 行数少算 1，计数与文件从此错开。
func TestPruneClearsPartialFlag(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prune-partial.log")
	w, err := New(openAppend(t, path), 1)
	if err != nil {
		t.Fatalf("New 失败：%v", err)
	}
	if _, err := w.Write([]byte("abc")); err != nil { // 半行，lines=1、partial=true
		t.Fatalf("写失败：%v", err)
	}
	if got := w.Lines(); got != 1 {
		t.Fatalf("半行 Lines = %d，想要 1", got)
	}
	if _, err := w.Write([]byte("\ndef\n")); err != nil { // delta=1 → 超限删头批
		t.Fatalf("写失败：%v", err)
	}
	equalLines(t, fileLines(t, path), []string{
		"---- 日志超过 1 行上限，已删除最早 1 行 ----",
		"",
		"def",
	})
	if got := w.Lines(); got != 3 {
		t.Errorf("删批写回后计数 = %d，想要 3（与文件同口径）", got)
	}
	if w.partial {
		t.Error("写回以换行收尾，partial 应已清零")
	}
}

// TestPruneRewritesCRLFAsLF：`\r\n` 的既有日志被删批重写时按行级口径读回、
// 只写 `\n` 回去（logcap.rs:152-153 的 `writeln!`），所以重写过的部分不再带
// `\r`；本笔追加的内容原样落盘（不去 `\r`）。整段原文逐字钉死。
func TestPruneRewritesCRLFAsLF(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crlfprune.log")
	w, err := New(openAppend(t, path), 3)
	if err != nil {
		t.Fatalf("New 失败：%v", err)
	}
	writeFull(t, w, "a\r\nb\r\n") // 两行，未超限
	writeFull(t, w, "c\r\n")      // 写满 3 行
	if got := w.RollCount(); got != 0 {
		t.Fatalf("写满不该删，rolls = %d", got)
	}
	writeFull(t, w, "d\r\n") // 第 4 行触发删头批
	if err := w.Flush(); err != nil {
		t.Fatalf("flush 失败：%v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读回失败：%v", err)
	}
	want := "b\nc\n---- 日志超过 3 行上限，已删除最早 1 行 ----\nd\r\n"
	if string(raw) != want {
		t.Errorf("原始字节 = %q\n想 要 %q", raw, want)
	}
	equalLines(t, fileLines(t, path), []string{
		"b", "c",
		"---- 日志超过 3 行上限，已删除最早 1 行 ----",
		"d",
	})
}

// TestSteadyStateMarkerCadence：长跑后的稳态形态（N=40 → 批大小 4）。
//
// 期望值不是反推出来的，是按 logcap.rs 的算式独立手算 / 脚本模拟得到：
// 写满 40 行后每 4 行滚一次，每次「删 4 行 + 落 1 行标记」净减 3 行，
// 于是稳定成「标记 + 3 条内容」的循环节——200 行写完后 54 次滚动、
// 文件 38 行、其中 10 行是标记。**标记永不连续**（删批在写入前一步到位，
// logcap.rs:131-133），这是「日志怎么只剩这些」时最该看到的形状。
func TestSteadyStateMarkerCadence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "markers.log")
	w, err := New(openAppend(t, path), 40)
	if err != nil {
		t.Fatalf("New 失败：%v", err)
	}
	for i := 0; i < 200; i++ {
		writeLog(t, w, fmt.Sprintf("m-%d", i))
	}
	lines := fileLines(t, path)
	if len(lines) != 38 {
		t.Errorf("总行数 = %d，想要 38", len(lines))
	}
	if got := w.RollCount(); got != 54 {
		t.Errorf("rolls = %d，想要 54", got)
	}
	if got := w.Lines(); got != len(lines) {
		t.Errorf("Lines = %d 与文件 %d 不同口径", got, len(lines))
	}
	marks := 0
	for i, l := range lines {
		if !strings.HasPrefix(l, "---- 日志超过 40 行上限，已删除最早 4 行 ----") {
			continue
		}
		marks++
		if i+1 < len(lines) && strings.HasPrefix(lines[i+1], "---- 日志超过 ") {
			t.Errorf("标记连续堆叠：%#v", lines)
		}
	}
	if marks != 10 {
		t.Errorf("标记行数 = %d，想要 10：%#v", marks, lines)
	}
	if lines[len(lines)-1] != "m-199" {
		t.Errorf("现场尾部应是最新行，得 %q", lines[len(lines)-1])
	}
	// 相邻两个标记之间固定夹 3 条内容行；最后一个标记之后至少留最新那 1 行。
	prev := -1
	for i, l := range lines {
		if !strings.HasPrefix(l, "---- 日志超过 ") {
			continue
		}
		if prev >= 0 && i-prev-1 != 3 {
			t.Errorf("标记夹的内容行 = %d，想要 3（第 %d 行处）：%#v", i-prev-1, i, lines)
		}
		prev = i
	}
	if len(lines)-1-prev < 1 {
		t.Errorf("最后一条标记后应还留着最新行：%#v", lines)
	}
}
