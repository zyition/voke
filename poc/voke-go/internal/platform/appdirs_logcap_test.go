// 本文件测 §7.1 O9 的**接线层**：platform.InitLog 把日志文件交给
// internal/logic/logcap 的 RollingLogWriter，于是 voke-go.log 真的具备生产
// voke 的容量控制（跨启动追加 + 超上限从头部删一批 max(1,N/10) + 一行中文
// 标记计入行数）。裁剪算法本身的场景①—⑧已在 internal/logic/logcap 逐条验过，
// 这里只验「接上了没、上限传对了没、句柄开对了没、接不上时日志还在不在」。
//
// 与 logcap 包用例的分工：那边测 `New` + `Write`；这边测 [newLogWriter] 与
// [InitLog] 这两个生产函数，期望值一律写死（不从被测实现反推），并且故意
// **不复用被测的 scanLines** 数行——数行口径写错时要能被外部视角发现。
package platform

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zyition/voke/poc/voke-go/internal/logic/logcap"
)

// mark20 是 maxLines=20、每批删 2 行时的滚动标记行文案（与 logcap.markLine
// 逐字同值，这里按生产口径**手抄字面量**当期望值，不从被测码反推）。
const mark20 = "---- 日志超过 20 行上限，已删除最早 2 行 ----"

// injectMaxLines 把构建期注入点设成 raw（等价 -ldflags -X ...logMaxLinesRaw=raw），
// 用例结束复原。本包用例不并行，改包级变量是安全的。
func injectMaxLines(t *testing.T, raw string) {
	t.Helper()
	prev := logMaxLinesRaw
	logMaxLinesRaw = raw
	t.Cleanup(func() { logMaxLinesRaw = prev })
}

// bareLogLines 关掉日期/时间前缀，让 log.Print("line-3") 落进文件的就是
// "line-3" 一行，方便逐字比对；用例结束复原原 flags。
func bareLogLines(t *testing.T) {
	t.Helper()
	prev := log.Flags()
	log.SetFlags(0)
	t.Cleanup(func() { log.SetFlags(prev) })
}

// diskLines 独立从盘上读回全部行（口径：\n 分行、行尾一个 \r 去掉、未以 \n
// 收尾的末尾段也算一行）。**故意不复用被测的 logcap.scanLines**。
func diskLines(t *testing.T, path string) []string {
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
	if start < len(raw) {
		out = append(out, string(raw[start:]))
	}
	return out
}

// openAppend 按**生产 InitLog 的打开方式**建句柄：O_CREATE + O_APPEND +
// O_RDWR（删头批要读回全文，纯 O_WRONLY 会 EBADF）。
func openAppend(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("打开日志文件失败 %s：%v", path, err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// logPathUnder 拼出 t.TempDir 当 APPDATA 时的日志文件路径。
func logPathUnder(root string) string {
	return filepath.Join(root, dataDirName, logFileName)
}

// TestResolveLogMaxLines 钉死上限解析的三分支绝对值（口径对齐生产
// voke-app/build.rs:54-68 与 main.rs:1022-1029）：未注入 / 非法 / 为 0 都落
// 默认 9999，合法正整数原样用，且非法时必带回一句中文警告（不静默）。
func TestResolveLogMaxLines(t *testing.T) {
	cases := []struct {
		raw      string
		wantN    int
		wantWarn string
	}{
		// 未注入（交付构建命令的默认形态）→ 9999，与 logcap.DefaultMaxLines 同值。
		{raw: "", wantN: 9999},
		{raw: "   ", wantN: 9999},
		// 合法注入。
		{raw: "20000", wantN: 20000},
		{raw: " 12345 ", wantN: 12345},
		{raw: "1", wantN: 1},
		// 非法：为 0 / 负数 / 非数字 → 兜底 9999 + 警告。
		{raw: "0", wantN: 9999, wantWarn: `日志行数上限注入值 "0" 非法（非数字 / 为 0 / 负数），已用默认 9999 行`},
		{raw: "-3", wantN: 9999, wantWarn: `日志行数上限注入值 "-3" 非法（非数字 / 为 0 / 负数），已用默认 9999 行`},
		{raw: "abc", wantN: 9999, wantWarn: `日志行数上限注入值 "abc" 非法（非数字 / 为 0 / 负数），已用默认 9999 行`},
		{raw: "9999.5", wantN: 9999, wantWarn: `日志行数上限注入值 "9999.5" 非法（非数字 / 为 0 / 负数），已用默认 9999 行`},
	}
	for _, c := range cases {
		gotN, gotWarn := resolveLogMaxLines(c.raw)
		if gotN != c.wantN {
			t.Errorf("resolveLogMaxLines(%q) 上限 = %d，期望 %d", c.raw, gotN, c.wantN)
		}
		if gotWarn != c.wantWarn {
			t.Errorf("resolveLogMaxLines(%q) 警告 = %q，期望 %q", c.raw, gotWarn, c.wantWarn)
		}
	}
	// 默认值与 logic 层常量必须一致（生产侧「三处数字一致」的那条纪律）。
	if logcap.DefaultMaxLines != 9999 {
		t.Errorf("logcap.DefaultMaxLines = %d，期望 9999", logcap.DefaultMaxLines)
	}
}

// TestNewLogWriterCapsAtMaxLines 直测接缝 [newLogWriter]：max=20 写 21 行，
// 期望文件是**逐字算死**的 20 行——删掉最旧的 2 行（batch = max(1, 20/10) = 2）、
// 落 1 行中文标记（**计入行数**）、再追加本次那行。接到不封顶的裸 writer
// 就会是 21 行且没有标记行。
func TestNewLogWriterCapsAtMaxLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), logFileName)
	f := openAppend(t, path)

	w := newLogWriter(f, 20)
	sink, ok := w.(*rollingSink)
	if !ok {
		t.Fatalf("newLogWriter 没接到封顶 sink，实际 %T", w)
	}
	for i := 0; i < 21; i++ {
		if _, err := fmt.Fprintln(w, fmt.Sprintf("line-%d", i)); err != nil {
			t.Fatalf("写日志失败：%v", err)
		}
	}

	want := []string{
		"line-2", "line-3", "line-4", "line-5", "line-6",
		"line-7", "line-8", "line-9", "line-10", "line-11",
		"line-12", "line-13", "line-14", "line-15", "line-16",
		"line-17", "line-18", "line-19", mark20, "line-20",
	}
	if got := diskLines(t, path); !slices.Equal(got, want) {
		t.Errorf("封顶后文件内容 = %d 行 %#v\n想 要 %d 行 %#v", len(got), got, len(want), want)
	}
	if sink.Lines() != 20 {
		t.Errorf("sink.Lines() = %d，期望 20（标记行计入行数）", sink.Lines())
	}
	if sink.RollCount() != 1 {
		t.Errorf("sink.RollCount() = %d，期望 1（第 21 行触发一次删头批）", sink.RollCount())
	}
}

// TestNewLogWriterAppendsAcrossReopen 验接缝侧的「跨启动追加」基线：第二轮
// 打开必须**读回**上一轮的 3 行作起点（不是从 0 数起、更不是截断重来），
// 且这个量级远不到 20 行上限，一行都不该被删。
func TestNewLogWriterAppendsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), logFileName)

	first := newLogWriter(openAppend(t, path), 20)
	for i := 0; i < 3; i++ {
		if _, err := fmt.Fprintln(first, fmt.Sprintf("old-%d", i)); err != nil {
			t.Fatalf("第一轮写日志失败：%v", err)
		}
	}

	second, ok := newLogWriter(openAppend(t, path), 20).(*rollingSink)
	if !ok {
		t.Fatalf("第二轮 newLogWriter 没接到封顶 sink")
	}
	if got := second.Lines(); got != 3 {
		t.Errorf("第二轮读到的既有行数 = %d，期望 3（跨启动基线）", got)
	}
	if _, err := fmt.Fprintln(second, "new-3"); err != nil {
		t.Fatalf("第二轮写日志失败：%v", err)
	}
	if second.RollCount() != 0 {
		t.Errorf("未超上限却滚动了 %d 次，期望 0", second.RollCount())
	}
	want := []string{"old-0", "old-1", "old-2", "new-3"}
	if got := diskLines(t, path); !slices.Equal(got, want) {
		t.Errorf("追加后文件内容 = %#v，期望 %#v", got, want)
	}
}

// TestInitLogCapsViaInjection 端到端直测生产函数 [InitLog]：注入上限 20 后连写
// 25 行，文件**必须**停在逐字算死的 20 行，且第 21 行之后每写一行都触发一批
// 删除（共 5 批）。这一条同时钉住三件事：
//   - 接的是封顶 writer（回退成裸 f 就是 25 行、零标记行）；
//   - 上限真的从 logMaxLinesRaw 解析出来传进了 logcap.New（硬编码 9999 或
//     传 0 都不会得到这个内容）；
//   - 删除要读回全文，句柄必须 O_RDWR（O_WRONLY 时第 21 行起 ReadAt 报
//     EBADF、写日志静默失败，文件停在 line-19 结尾）。
func TestInitLogCapsViaInjection(t *testing.T) {
	root := t.TempDir()
	t.Setenv("APPDATA", root)
	isolateLog(t)
	injectMaxLines(t, "20")
	bareLogLines(t)

	InitLog()
	sink, ok := log.Writer().(*rollingSink)
	if !ok {
		t.Fatalf("InitLog 没把 log 输出接到封顶 sink，实际 %T", log.Writer())
	}
	// O_RDWR 的正面证据：拿到底层句柄直接 ReadAt 首个字节，不能报 EBADF。
	file, ok := sink.file.(*os.File)
	if !ok {
		t.Fatalf("封顶 sink 没带着 *os.File，实际 %T", sink.file)
	}

	var head [1]byte
	if _, err := file.ReadAt(head[:], 0); err == nil {
		t.Errorf("空文件 ReadAt 竟成功读到 %q", head[0])
	} else if !errors.Is(err, io.EOF) {
		t.Fatalf("空文件 ReadAt 只该报 EOF，实际 %v（说明句柄不是 O_RDWR）", err)
	}

	// 25 条全走生产路径 log.Print（flags 已清，落盘就是一行 "line-N"）。
	for i := 0; i < 25; i++ {
		log.Print(fmt.Sprintf("line-%d", i))
	}
	// 写满之后 ReadAt 必须真读得动（封顶删头批要读回全文）。
	if n, err := file.ReadAt(head[:], 0); err != nil || n != 1 {
		t.Errorf("InitLog 打开的句柄 ReadAt 失败（封顶要读回全文，必须 O_RDWR）：n=%d err=%v", n, err)
	}

	// 手算死期望：第 21 条起每写一条都触发一批（batch=2），5 条共删 10 行、
	// 落 5 行标记，标记紧跟它那一轮幸存内容之后、新写的这行之前。
	want := []string{
		"line-10", "line-11", "line-12", "line-13", "line-14",
		"line-15", "line-16", "line-17", "line-18", "line-19",
		mark20, "line-20", mark20, "line-21", mark20,
		"line-22", mark20, "line-23", mark20, "line-24",
	}
	if got := diskLines(t, logPathUnder(root)); !slices.Equal(got, want) {
		t.Errorf("InitLog 封顶后的文件内容 = %d 行 %#v\n想 要 %d 行 %#v", len(got), got, len(want), want)
	}
	if sink.Lines() != 20 {
		t.Errorf("sink.Lines() = %d，期望 20", sink.Lines())
	}
	if sink.RollCount() != 5 {
		t.Errorf("sink.RollCount() = %d，期望 5（第 21–25 行各触发一批）", sink.RollCount())
	}
}

// TestInitLogInvalidInjectionUsesDefaultAndWarns 验「注入了非法值」时端到端仍
// 落在默认 9999：日志照写、一行不删，而中文警告进的是**日志文件本身**
// （GUI 子系统下 stderr 不可见，留痕才有排查价值）。
func TestInitLogInvalidInjectionUsesDefaultAndWarns(t *testing.T) {
	root := t.TempDir()
	t.Setenv("APPDATA", root)
	isolateLog(t)
	injectMaxLines(t, "0")
	bareLogLines(t)

	InitLog()
	for i := 0; i < 3; i++ {
		log.Print(fmt.Sprintf("line-%d", i))
	}

	want := []string{
		`日志行数上限注入值 "0" 非法（非数字 / 为 0 / 负数），已用默认 9999 行`,
		"line-0", "line-1", "line-2",
	}
	got := diskLines(t, logPathUnder(root))
	if !slices.Equal(got, want) {
		t.Errorf("非法注入时的文件内容 = %d 行 %#v\n想 要 %d 行 %#v", len(got), got, len(want), want)
	}
}

// TestNewLogWriterFallsBackToUncappedWhenCapInitFails 验失败降级：
// logcap.New 数既有行数失败（假件模拟「只写句柄读不动」）时，[newLogWriter]
// 必须退回**裸文件本身**——宁可不封顶，也绝不能让日志整体失效——并落一条
// 中文警告留痕。
func TestNewLogWriterFallsBackToUncappedWhenCapInitFails(t *testing.T) {
	var stderr bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&stderr)
	log.SetFlags(0)
	defer func() { log.SetOutput(prevOut); log.SetFlags(prevFlags) }()

	bad := &brokenFile{size: 12}
	got := newLogWriter(bad, 20)
	if got != io.Writer(bad) {
		t.Errorf("封顶初始化失败时应退回裸文件（不封顶但不断日志），实际 %T", got)
	}
	if _, err := fmt.Fprintln(got, "line-0"); err != nil {
		t.Fatalf("降级后写日志失败：%v", err)
	}
	if written := bad.written.Bytes(); !bytes.Contains(written, []byte("line-0\n")) {
		t.Errorf("降级后日志没写进文件，实际收到 %q", written)
	}
	warn := stderr.String()
	if !strings.Contains(warn, "初始化日志行数上限失败") || !strings.Contains(warn, "不封顶") {
		t.Errorf("降级时该落一条中文警告，实际输出：%q", warn)
	}
}

// brokenFile 是「Stat 说有内容、ReadAt 一律失败」的假日志文件，专门用来造
// logcap.New 的初始化失败分支（真 *os.File 在 Linux 上造不出这个状态：
// 要么真能读、要么根本开不起来）。对应生产侧 O_WRONLY 打开后 read 报 EBADF。
type brokenFile struct {
	size    int64
	written bytes.Buffer
}

func (b *brokenFile) Write(p []byte) (int, error) { return b.written.Write(p) }
func (b *brokenFile) ReadAt([]byte, int64) (int, error) {
	return 0, errors.New("read 系统调用失败：bad file descriptor")
}
func (b *brokenFile) Truncate(int64) error { return nil }
func (b *brokenFile) Sync() error          { return nil }
func (b *brokenFile) Close() error         { return nil }
func (b *brokenFile) Stat() (fs.FileInfo, error) {
	return fakeFileInfo{size: b.size}, nil
}

// fakeFileInfo 只填 [logcap.File] 用得上的 Size，其余给固定值。
type fakeFileInfo struct{ size int64 }

func (fakeFileInfo) Name() string       { return logFileName }
func (i fakeFileInfo) Size() int64      { return i.size }
func (fakeFileInfo) Mode() fs.FileMode  { return 0o644 }
func (fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (fakeFileInfo) IsDir() bool        { return false }
func (fakeFileInfo) Sys() any           { return nil }
