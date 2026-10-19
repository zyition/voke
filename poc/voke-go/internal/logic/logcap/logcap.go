// Package logcap 是日志行数上限：RollingLogWriter —— 「max lines」语义的日志
// 容量控制。生产 Rust 实现 `voke-sys/src/logcap.rs` 的 1:1 移植（§7.1 O9
// 「同款实现、同逻辑同单测」），算法逐条对齐原文件，含行号出处。
//
// ## 语义：跨启动追加 + 超限从头部删一批（logcap.rs:3-16）
//
//   - **跨启动追加**：文件以追加模式打开，重启不清空，历史日志保留，
//     总量由行数上限控制。
//   - **超限删头批**：一次写入会使行数超过上限 N 时，先从文件**头部删掉一批
//     最旧的行**（批量 = `max(1, N/10)`，9999 → 100 行，logcap.rs:109），
//     落一行中文滚动标记（**计入行数**，logcap.rs:155-160），再照常追加本行。
//     删批而非逐行删，避免每写一行就整文件重写。
//   - 效果是**永远大致保留最近的现场**，同时不像「超限就地截断重写」那样把
//     更早的历史一次性全部抹掉。
//
// 上限由调用方传入（logcap.rs:71）；构建期注入（Go 侧是
// `-ldflags -X`，对应 Rust 的 `VOKE_LOG_MAX_LINES` + `build.rs`）与
// 「未设 / 非法 / 为 0 → 兜底」都留在**接线层**，本包只管裁剪本身。
// 兜底值见 [DefaultMaxLines]，与 Rust 侧三处一致的 9999 同口径。
//
// ## 为什么放 logic 层（对应 logcap.rs:18-24「为什么放在 voke-sys」）
//
// 消费方在 UI / platform 侧（mygo 的日志句柄），那些测试在 Linux 原生跑不了；
// 若把裁剪逻辑写在消费方，删批 / 计数逻辑将**永远没有单测覆盖**——而行数算错
// 一丝，排查时看到的就是错的现场。下沉到 logic 层后本包**零 mygo、零
// x/sys/windows、零第三方**，只 import 标准库，`go test ./internal/logic/logcap/`
// 在 Linux 原生可跑（含真文件读写）。
//
// ## 行处理口径：写和查都用行级 API，不硬编码换行（logcap.rs:26-34）
//
//   - **写**：统一 [fmt.Fprintln] / 补全短写的 [printlnFull]（行级写法，对应
//     Rust 的 `writeln!`），不手拼 `"\n"`。
//   - **读 / 数行 / 按行分割**：统一包内 [scanLines]（对应
//     `std::io::BufRead::lines()`）：未以 `\n` 收尾的末尾段算一行，`\r\n`
//     只去掉一个 `\r`。数既有行数与删批读全文走**同一个** scanLines，
//     杜绝两处口径漂移。
//   - 仅两处字节级判断（logcap.rs:80-86、112-119）：待写内容是 `[]byte`
//     （不保证 UTF-8，行级解码会出错），只能数字节里的 `\n`；打开时判断
//     文件末尾是否残留未完成行同理。
//
// ## 与 Rust 的三处**受控**差异（都在退化路径上，不改正常口径）
//
//  1. 空 buf / 0 字节写：Rust 的 `buf_line_delta(&[])` 在 `partial == true`
//     时算 `0 + 0 - 1`，`usize` 直接下溢（debug 构建 panic）。Go 里同一笔算出
//     `delta == -1`，会被 `delta > 0` 与 `n > 0` 两道判据吸收，不会污染计数；
//     额外那条空 buf 早返回是**显式契约**（空写 = 无操作），不是行为必需
//     ——变异掉它本包用例仍绿（见 [RollingLogWriter.Write] 内注释）。
//     对应 logcap.rs:120-125、183-190。
//  2. 读回不做 UTF-8 校验：Rust 的 `BufRead::lines()` 遇非法 UTF-8 报错，
//     Go 的字符串是字节切片，原样保留。日志写入侧恒为 UTF-8，正常不会走到。
//  3. 删头批的写回按「没写全就接着写」补全（[printlnFull]）：Rust 的
//     `writeln!` 走 `write_fmt`，内部按 `write_all` 语义消化短写；Go 的
//     `fmt.Fprintln` **不重试**，碰上短写的底层 writer 会静默丢掉行尾字节，
//     而删头批是整文件重写——丢的就是历史日志。
package logcap

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"strings"
)

// DefaultMaxLines 是默认行数上限，与 Rust 的 `DEFAULT_MAX_LINES`
// （logcap.rs:46）逐字同值。接线层（ldflags 注入 / 解析兜底）与它必须一致，
// 改时一起改。
//
// 选 9999 的依据（logcap.rs:42-45）：Info 级一天通常几百行，一行约 40–120
// 字节，9999 行 ≈ **0.6–1 MB**——留足一次 debug 排查量（含 DEBUG 全开），
// 又远不到会让人犹豫「要不要先清日志」的体积。
const DefaultMaxLines = 9999

// File 是 [RollingLogWriter] 对底层日志文件的最小要求，`*os.File` 天然满足。
// 拆开来看（对应 Rust 直接用 `File` 的 `Seek`/`read_exact`/`set_len`/`metadata`，
// logcap.rs:81-86、130-131、141、74）：
//
//   - [io.Writer]：追加本行（依赖调用方的 `O_APPEND` 语义，与 Rust 同一契约）。
//   - [io.ReaderAt]：删头批时读回全文、打开时看末尾字节。**刻意用 ReadAt
//     而非 Seek+Read**：ReadAt 不移动写偏移，纯逻辑侧不用操心游标状态。
//   - Truncate：删头批前清空文件（Rust `set_len(0)`）。
//   - Sync：[RollingLogWriter.Flush] 落盘。
//   - Stat：取文件长度作为「是否为空文件」的判据（Rust `metadata()?.len()`）。
type File interface {
	io.Writer
	io.ReaderAt
	Truncate(size int64) error
	Sync() error
	Stat() (fs.FileInfo, error)
}

// RollingLogWriter 是「最多 N 行」的日志 writer：只追加，超限从头部删一批
// 最旧的行（logcap.rs:48-64）。
//
// 持有一个以**追加模式**打开的文件：调用方负责用
// `os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_RDWR, 0o644)` 打开，本结构
// 不负责打开。`O_RDWR`（对应 Rust 的 `.read(true)`）不可省：删头批要读回全文，
// 纯 append 打开读会 EBADF；O_APPEND 的「写永远追加到末尾」语义不受影响
// （logcap.rs:51-53）。打开时先数一遍既有行数作为跨启动基线。
type RollingLogWriter struct {
	file     File
	maxLines int
	// lines 是当前行数（与 scanLines 口径一致：未以 \n 收尾的末尾段算一行）。
	lines int
	// partial 是文件末尾是否残留未以 \n 收尾的部分行（下一笔写入开头与它同行）。
	partial bool
	// rolls 是累计滚动删除（删头批）次数，排查「日志怎么只剩这些」时看这个。
	rolls uint64
}

// New 包装一个已打开的（追加模式）日志文件，并数出既有行数
// （logcap.rs:66-95）。
//
// 既有内容只读不改（跨启动追加的基线）。
func New(file File, maxLines int) (*RollingLogWriter, error) {
	w := &RollingLogWriter{file: file, maxLines: maxLines}
	fi, err := file.Stat()
	if err != nil {
		return nil, err
	}
	size := fi.Size()
	if size > 0 {
		all, err := scanLines(file, size)
		if err != nil {
			return nil, err
		}
		w.lines = len(all)
		// 字节级判断（stdlib 没有「最后一行是否完整」的行级 API）：读最后一个
		// 字节看是否以 \n 收尾，与写入侧的部分行计数保持同一口径（logcap.rs:80-86）。
		var last [1]byte
		if _, err := file.ReadAt(last[:], size-1); err != nil {
			return nil, err
		}
		w.partial = last[0] != '\n'
	}
	return w, nil
}

// Lines 返回当前行数（排查用，logcap.rs:97-100）。
func (w *RollingLogWriter) Lines() int { return w.lines }

// RollCount 返回累计滚动删除次数（排查用，logcap.rs:102-105）。
func (w *RollingLogWriter) RollCount() uint64 { return w.rolls }

// batchSize 是一批删多少行：总上限的 10%，至少 1 行（9999 → 100，10 → 1）。
// 与 Rust `batch_size()` 严格同式：`std::cmp::max(1, max_lines / 10)`
// （logcap.rs:107-110）——整除**向下**取整，故 N=19 → 1、N=20 → 2、N=9 → 1。
func (w *RollingLogWriter) batchSize() int {
	return max(1, w.maxLines/10)
}

// bufLineDelta 算一段待写字节的行级贡献：`(新增行数, 写完后是否残留部分行)`
// （logcap.rs:112-125）。
//
// 与 scanLines 口径一致：未以 `\n` 收尾的末尾段算一行，且「上一笔残留的部分
// 行 + 本笔开头」属于同一行、只计一次（Rust 写作
// `complete + partial_after - partial_before`）。
//
// 字节级判断的理由（logcap.rs:117-119）：待写内容是 `[]byte`，不保证 UTF-8，
// 过不了行级解码；日志写入侧数 `\n` 只能数字节。
func (w *RollingLogWriter) bufLineDelta(buf []byte) (int, bool) {
	complete := bytes.Count(buf, []byte{'\n'})
	partialAfter := len(buf) > 0 && buf[len(buf)-1] != '\n'
	delta := complete
	if partialAfter {
		delta++
	}
	if w.partial {
		delta--
	}
	return delta, partialAfter
}

// markLine 生成滚动标记行的文案，与 Rust 逐字一致（logcap.rs:155-159）：
//
//	"---- 日志超过 {max_lines} 行上限，已删除最早 {dropped} 行 ----"
//
// dropped 必须是**实际**删除数而非请求数（见 [pruneLines]）。
func markLine(maxLines, dropped int) string {
	return fmt.Sprintf("---- 日志超过 %d 行上限，已删除最早 %d 行 ----", maxLines, dropped)
}

// pruneLines 是本包的纯裁剪核心：给定既有全部行 all、想删的 drop 行、上限
// maxLines，产出「**应写回的完整行序列**」（剩余行 + 一行中文标记）与实际
// 删除数（logcap.rs:149-160 的算术，去掉 I/O）。
//
// drop 超过实际行数时删完全部（`min` 天然饱和，对应 `drop.min(all.len())`，
// logcap.rs:135-149），**不死循环**；标记行按实际删除数报告（单笔写入超过
// 上限时 drop 可能大于总行数）。标记行排在剩余内容之后、也排在即将追加的本行
// 之前，且**计入行数**——写回后的行数就是 `len(out)`。
func pruneLines(all []string, drop, maxLines int) (out []string, dropped int) {
	dropped = min(drop, len(all))
	remaining := all[dropped:]
	out = make([]string, 0, len(remaining)+1)
	out = append(out, remaining...)
	return append(out, markLine(maxLines, dropped)), dropped
}

// pruneHead 从文件头部删掉 drop 行最旧的内容，然后把剩余行 + 一行中文滚动
// 标记写回（标记计入行数），并更新计数（logcap.rs:127-164）。
//
// 实现：读全文 → 纯函数 [pruneLines] 算出要写回什么 → `Truncate(0)` 清空 →
// 用 [printlnFull]（行级 + 补全短写）逐行写回。标记行直接写在底层文件上
// （不走本结构的 [RollingLogWriter.Write]），**不会递归触发删除**；且删除在
// 写入前一步到位，不存在「写一批再删一批」。
//
// `drop == 0` 直接返回：一次都不删就不该留标记行。
func (w *RollingLogWriter) pruneHead(drop int) error {
	if drop == 0 {
		return nil
	}
	fi, err := w.file.Stat()
	if err != nil {
		return err
	}
	var all []string
	if size := fi.Size(); size > 0 {
		all, err = scanLines(w.file, size)
		if err != nil {
			return err
		}
	}
	out, _ := pruneLines(all, drop, w.maxLines)
	if err := w.file.Truncate(0); err != nil {
		return err
	}
	for _, line := range out {
		if err := printlnFull(w.file, line); err != nil {
			return err
		}
	}
	w.lines = len(out)
	w.partial = false
	w.rolls++
	return nil
}

// printlnFull 行级写一整行并**反复补写直到落全**（对应 Rust 的
// `writeln!(self.file, ...)`——`write_fmt` 内部按 `write_all` 语义消化短写，
// logcap.rs:152-159）。行尾由 [fmt.Fprintln] 决定，本函数只负责「没写全就接着
// 写」，不手拼换行符。
//
// 为什么不能直接 `fmt.Fprintln(w.file, line)` 了事：Go 的 io.Writer 契约允许
// 「n < len(p) 且 err == nil」的宽 writer（少见但存在），直接 Fprintln 会
// **静默丢掉尾部字节**——删头批是把整个文件重写一遍，丢的就是历史日志。
func printlnFull(w io.Writer, line string) error {
	var b bytes.Buffer
	if _, err := fmt.Fprintln(&b, line); err != nil {
		return err
	}
	for p := b.Bytes(); len(p) > 0; {
		n, err := w.Write(p)
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		p = p[n:]
	}
	return nil
}

// Write 追加一笔日志字节，必要时先删头批（logcap.rs:167-191）。
//
// 先用整段预估行数贡献；短写时按实际落盘量修正（logcap.rs:184-185）。
//
// 只在**本笔真的新增行**（delta > 0）时检查上限——这一点是必须的
// （logcap.rs:170-174）：`fmt.Fprintln` / `fmt.Fprintf` 可能把一行拆成多笔
// write（内容一笔、换行另一笔），「补完成分行」的那笔 delta=0——若也检查，
// 状态已因标记行略超上限（小上限时）就会再次触发删除，把标记行插进行中间
// 还多出一个空行。
func (w *RollingLogWriter) Write(buf []byte) (int, error) {
	if len(buf) == 0 {
		// 空写一笔不改任何状态。Rust 里同一笔会算 `0 + 0 - usize(partial)`，
		// `partial == true` 时直接下溢（debug panic）；Go 这边算出 -1 后会被
		// 下面的 `delta > 0` 与 `n > 0` 两道判据吸收，所以这条早返回**不是行为
		// 必需**（把它变异掉，本包用例仍绿），而是把「空写 = 无操作」写成显式
		// 契约——别顺手删掉那两道判据，那才会让行数静默减 1。
		return 0, nil
	}
	delta, _ := w.bufLineDelta(buf)
	if delta > 0 && w.lines+delta > w.maxLines {
		// 一批 = max(1, N/10)；若一批不足以把「剩余 + 本次写入」压回上限内
		// （只可能发生在 N < 20 的小上限），按需多删，保证有界、不会每写
		// 一行就删一批导致文件无限涨（logcap.rs:176-181）。
		drop := max(w.batchSize(), w.lines+delta-w.maxLines)
		if err := w.pruneHead(drop); err != nil {
			return 0, err
		}
	}
	n, err := w.file.Write(buf)
	if err != nil {
		return n, err
	}
	if n > 0 {
		// 只按真正落盘的字节更新计数（短写时 n < len(buf)，logcap.rs:184-189）。
		d, partialAfter := w.bufLineDelta(buf[:n])
		w.lines += d
		w.partial = partialAfter
	}
	return n, nil
}

// Flush 落盘（logcap.rs:193-195）。
func (w *RollingLogWriter) Flush() error { return w.file.Sync() }

// scanLines 用行级口径读出 [0, size) 里的全部行（对应
// `std::io::BufRead::lines()`，logcap.rs:76-79、144-148）。
//
// 口径：以 `\n` 分行，行尾的 `\r` 去掉（`\r\n` 只去掉一个）；**未以 `\n`
// 收尾的末尾段算一行**；空文件零行。
func scanLines(f File, size int64) ([]string, error) {
	br := bufio.NewReader(io.NewSectionReader(f, 0, size))
	var out []string
	for {
		chunk, err := br.ReadString('\n')
		if len(chunk) > 0 {
			out = append(out, trimLineEnd(chunk))
		}
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// trimLineEnd 去掉一行的行尾换行：先吃 `\n`，再吃紧邻的一个 `\r`。
func trimLineEnd(chunk string) string {
	if !strings.HasSuffix(chunk, "\n") {
		return chunk
	}
	return strings.TrimSuffix(chunk[:len(chunk)-1], "\r")
}

// 编译期确认：本结构的写入语义就是标准 io.Writer，接线层可以直接把它交给
// log / fmt 一类的行级写入方（对应 Rust 的 `impl Write for RollingLogWriter`）。
var _ io.Writer = (*RollingLogWriter)(nil)
