//! 日志行数上限：`RollingLogWriter` —— 「max lines」语义的日志容量控制。
//!
//! ## 语义：跨启动追加 + 超限从头部删一批
//!
//! 日志始终是单个 `%APPDATA%\voke\voke.log`，**只追加、不轮转文件**：
//!
//! - **跨启动追加**：文件以追加模式打开（`OpenOptions::append`），重启不清空，
//!   历史日志保留，总量由行数上限控制。
//! - **超限删头批**：一次写入会使行数超过上限 N 时，先从文件**头部删掉一批
//!   最旧的行**（批量 = `max(1, N/10)`，9999 → 100 行），落一行中文滚动标记
//!   （计入行数），再照常追加本行。删批而非逐行删，避免每写一行就整文件重写。
//! - 效果是**永远大致保留最近的现场**，同时不像「超限就地截断重写」那样把
//!   更早的历史一次性全部抹掉。
//!
//! 上限来自构建期环境变量 `VOKE_LOG_MAX_LINES`（见 `voke-app/build.rs`，
//! 未设/非法/为 0 默认 9999），运行期不可改。
//!
//! ## 为什么放在 voke-sys
//!
//! 这里是纯逻辑（只依赖 `std::io`，不碰 Win32），但**消费方在 `voke-app`**。
//! `voke-app` 的测试在 Linux native 因缺 fontconfig 跑不了（见 AGENTS.md），
//! 若把 writer 放 app 层，删批/计数逻辑将**永远没有单测覆盖**——而行数算错
//! 一丝，排查时看到的就是错的现场。下沉到 voke-sys 后 Linux 原生
//! `cargo test -p voke-sys` 能真跑这些用例，与其它「纯逻辑下沉」模块同思路。
//!
//! ## 行处理口径：写和查都用 std 行级 API（用户明确要求，勿硬编码换行）
//!
//! - **写**：统一 `writeln!` / `write!` 宏（`std::io::Write` 的行级写法），
//!   不手拼 `"\n"` / `b"\n"` / `push('\n')`。
//! - **读/数行/按行分割**：统一 `std::io::BufRead::lines()`（数既有行数、
//!   删批时读全文、测试里读回校验）。
//! - 仅两处字节级判断，用 `b'\n'` u8 常量并在注释说明理由：写入缓冲区是
//!   `&[u8]`（不保证 UTF-8，`BufRead::lines()` 会直接报错），只能数字节里的
//!   `\n`；打开时判断文件末尾是否残留未完成行同理。

use std::fs::File;
use std::io::{self, BufRead, BufReader, Read, Seek, SeekFrom, Write};

/// 默认行数上限。与 `voke-app/build.rs` 的兜底、`main.rs` 的解析失败兜底
/// 必须保持一致（三处都写死同一个数，改时一起改）。
///
/// 选 9999 的依据：Info 级一天通常几百行（启动几条 + 唤起/搜索/自检零星几条），
/// 一行约 40–120 字节（时间戳 ~30B + 级别/模块 ~20B + 消息 10–70B），
/// 9999 行 ≈ **0.6–1 MB**——留足一次 debug 排查量（含 DEBUG 全开时的量），
/// 又远不到会让人犹豫「要不要先清日志」的体积。
pub const DEFAULT_MAX_LINES: usize = 9999;

/// 「最多 N 行」的日志 writer：只追加，超限从头部删一批最旧的行。
///
/// 持有一个以**追加模式**打开的 [`File`]（调用方负责用
/// `OpenOptions::new().create(true).append(true).read(true)` 打开，本结构不
/// 负责打开）。`.read(true)` 不可省：删头批要读回全文，而纯 append 打开是
/// O_WRONLY，读会 EBADF；O_APPEND 的「写永远追加到末尾」语义不受影响。
/// 打开时先用 [`BufRead::lines()`] 数一遍既有行数作为跨启动基线。
pub struct RollingLogWriter {
    file: File,
    max_lines: usize,
    /// 当前行数（与 `BufRead::lines()` 口径一致：未以 `\n` 收尾的末尾段算一行）。
    lines: usize,
    /// 文件末尾是否残留未以 `\n` 收尾的部分行（下一笔写入开头与它同行）。
    partial: bool,
    /// 累计滚动删除（删头批）次数，排查「日志怎么只剩这些」时看这个。
    rolls: u64,
}

impl RollingLogWriter {
    /// 包装一个已打开的（追加模式）日志文件，并数出既有行数。
    ///
    /// 既有内容只读不改（跨启动追加的基线）；文件含非法 UTF-8 时
    /// `BufRead::lines()` 会报错——env_logger 的输出恒为 UTF-8，正常不会发生。
    pub fn new(file: File, max_lines: usize) -> io::Result<Self> {
        let mut lines = 0usize;
        let mut partial = false;
        if file.metadata()?.len() > 0 {
            let reader = BufReader::new(&file);
            for line in reader.lines() {
                line?;
                lines += 1;
            }
            // 字节级判断（std 没有「最后一行是否完整」的行级 API）：读最后一个
            // 字节看是否以 b'\n' 收尾，与写入侧的部分行计数保持同一口径。
            let mut last = [0u8; 1];
            let mut src = &file;
            src.seek(SeekFrom::End(-1))?;
            src.read_exact(&mut last)?;
            partial = last[0] != b'\n';
        }
        Ok(Self {
            file,
            max_lines,
            lines,
            partial,
            rolls: 0,
        })
    }

    /// 当前行数（排查用）。
    pub fn lines(&self) -> usize {
        self.lines
    }

    /// 累计滚动删除次数（排查用）。
    pub fn roll_count(&self) -> u64 {
        self.rolls
    }

    /// 一批删多少行：总上限的 10%，至少 1 行（9999 → 100，10 → 1）。
    fn batch_size(&self) -> usize {
        std::cmp::max(1, self.max_lines / 10)
    }

    /// 一段待写字节的行级贡献：`(新增行数, 写完后是否残留部分行)`。
    ///
    /// 与 [`BufRead::lines`] 口径一致：未以 `\n` 收尾的末尾段算一行，且
    /// 「上一笔残留的部分行 + 本笔开头」属于同一行、只计一次
    /// （`complete + partial_after - partial_before`）。
    ///
    /// 字节级判断的理由：待写内容是 `&[u8]`，不保证 UTF-8，过不了
    /// `BufRead::lines()`；日志写入侧数 `\n` 只能数字节（u8 常量）。
    fn buf_line_delta(&self, buf: &[u8]) -> (usize, bool) {
        let complete = buf.iter().filter(|&&b| b == b'\n').count();
        let partial_after = !buf.is_empty() && *buf.last().unwrap() != b'\n';
        let delta = complete + usize::from(partial_after) - usize::from(self.partial);
        (delta, partial_after)
    }

    /// 从文件头部删掉 `drop` 行最旧的内容，然后把剩余行 + 一行中文滚动标记
    /// 写回（标记计入行数），更新计数。
    ///
    /// 实现：`seek(0)` + [`BufReader::lines`] 读全文 → 丢掉最旧的 `drop` 行 →
    /// `set_len(0)` 清空 → 用 `writeln!` 逐行写回剩余内容 + 标记行。
    /// 标记行直接写在 `self.file` 上（不走本结构的 `Write::write`），**不会
    /// 递归触发删除**；且删除在写入前一步到位，不存在「写一批再删一批」。
    ///
    /// `drop` 超过实际行数时删完全部（`skip` 天然饱和），不死循环；标记行按
    /// **实际**删除数报告（单笔写入超过上限时 `drop` 可能大于总行数）。
    fn prune_head(&mut self, drop: usize) -> io::Result<()> {
        if drop == 0 {
            return Ok(());
        }
        self.file.seek(SeekFrom::Start(0))?;
        let mut all: Vec<String> = Vec::new();
        {
            let reader = BufReader::new(&self.file);
            for line in reader.lines() {
                all.push(line?);
            }
        }
        let dropped = drop.min(all.len());
        let remaining = &all[dropped..];
        self.file.set_len(0)?;
        for line in remaining {
            writeln!(self.file, "{line}")?;
        }
        writeln!(
            self.file,
            "---- 日志超过 {} 行上限，已删除最早 {} 行 ----",
            self.max_lines, dropped
        )?;
        self.lines = remaining.len() + 1;
        self.partial = false;
        self.rolls += 1;
        Ok(())
    }
}

impl Write for RollingLogWriter {
    fn write(&mut self, buf: &[u8]) -> io::Result<usize> {
        // 先用整段预估行数贡献；短写时按实际落盘量修正（见下方重算分支）。
        let (delta, _) = self.buf_line_delta(buf);
        // 只在**本笔真的新增行**（delta > 0）时检查上限。这一点是必须的：
        // `writeln!` / `write_fmt` 会把一行拆成多笔 write（内容一笔、换行另
        // 一笔），"补完成分行"的那笔 delta=0——若也检查，状态已因标记行略超
        // 上限（小上限时）就会再次触发删除，把标记行插进行中间还多出一个空行。
        if delta > 0 && self.lines + delta > self.max_lines {
            // 一批 = max(1, N/10)；若一批不足以把「剩余 + 本次写入」压回上限内
            // （只可能发生在 N < 20 的小上限），按需多删，保证有界、不会每写
            // 一行就删一批导致文件无限涨。
            let batch = self.batch_size();
            let need = self.lines + delta - self.max_lines;
            self.prune_head(std::cmp::max(batch, need))?;
        }
        let n = self.file.write(buf)?;
        // 只按真正落盘的字节更新计数（短写时 n < buf.len()）。
        let (delta, partial_after) = self.buf_line_delta(&buf[..n]);
        self.lines += delta;
        if n > 0 {
            self.partial = partial_after;
        }
        Ok(n)
    }

    fn flush(&mut self) -> io::Result<()> {
        self.file.flush()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::path::{Path, PathBuf};
    use std::process;

    fn temp_path(tag: &str) -> PathBuf {
        std::env::temp_dir().join(format!("voke-logcap-{}-{tag}.log", process::id()))
    }

    /// 测试里模拟 voke-app 的打开方式：创建 + **追加** + 可读
    /// （删头批要读回全文，纯 append 是 O_WRONLY 读会 EBADF）。
    fn open_append(path: &Path) -> File {
        std::fs::OpenOptions::new()
            .create(true)
            .append(true)
            .read(true)
            .open(path)
            .unwrap()
    }

    /// 读回全部行（与实现同口径：`BufRead::lines()`）。
    fn read_lines(path: &Path) -> Vec<String> {
        let file = File::open(path).unwrap();
        BufReader::new(file)
            .lines()
            .map(|l| l.unwrap())
            .collect::<Vec<_>>()
    }

    /// env_logger 风格：一条日志一次 `writeln!`（完整一行）。
    fn write_log<W: Write>(w: &mut W, s: &str) {
        writeln!(w, "{s}").unwrap();
    }

    /// 写 `total` 行（line-0 .. line-{total-1}），返回最终文件行内容。
    fn roll_scenario(path: &Path, max: usize, total: usize) -> Vec<String> {
        let _ = std::fs::remove_file(path);
        let mut w = RollingLogWriter::new(open_append(path), max).unwrap();
        for i in 0..total {
            write_log(&mut w, &format!("line-{i}"));
        }
        w.flush().unwrap();
        read_lines(path)
    }

    #[test]
    fn append_across_restarts_keeps_history() {
        // 场景①：跨「启动」不清空——第二次以追加模式打开同一文件，旧内容仍在，
        // 且 new() 数到的基线包含旧行（上限跨重启生效）。
        let path = temp_path("append");
        let _ = std::fs::remove_file(&path);
        {
            // 第一次「启动」：写 3 行后退出（drop writer）。
            let mut w = RollingLogWriter::new(open_append(&path), 100).unwrap();
            for i in 0..3 {
                write_log(&mut w, &format!("run1-line-{i}"));
            }
            w.flush().unwrap();
        }
        {
            // 第二次「启动」：旧内容仍在，计数含旧 3 行。
            let w = RollingLogWriter::new(open_append(&path), 100).unwrap();
            assert_eq!(w.lines(), 3, "重启后应数到旧 3 行");
        }
        let mut w = RollingLogWriter::new(open_append(&path), 100).unwrap();
        write_log(&mut w, "run2-line-0");
        w.flush().unwrap();
        assert_eq!(
            read_lines(&path),
            ["run1-line-0", "run1-line-1", "run1-line-2", "run2-line-0"]
        );
        let _ = std::fs::remove_file(&path);
    }

    #[test]
    fn under_limit_appends_verbatim() {
        // 场景②：未超限纯追加、顺序原样、无标记。
        let path = temp_path("verbatim");
        let _ = std::fs::remove_file(&path);
        let mut w = RollingLogWriter::new(open_append(&path), 10).unwrap();
        for i in 0..3 {
            write_log(&mut w, &format!("line-{i}"));
        }
        w.flush().unwrap();
        assert_eq!(read_lines(&path), ["line-0", "line-1", "line-2"]);
        assert_eq!(w.roll_count(), 0);
        assert_eq!(w.lines(), 3);
        assert!(!std::fs::read_to_string(&path).unwrap().contains("已删除"));
        let _ = std::fs::remove_file(&path);
    }

    #[test]
    fn over_limit_drops_oldest_batch_from_head() {
        // 场景③：超限删头批——最旧行消失、最新行在、有滚动标记、总行数回落。
        let path = temp_path("roll");
        let lines = roll_scenario(&path, 10, 11);
        // 10 行写满后第 11 行触发：删最早 1 行 → 9 旧 + 标记 + 最新 = 11 行。
        assert_eq!(lines.len(), 11);
        assert!(!lines.iter().any(|l| l == "line-0"), "最旧一行应被删");
        assert!(lines.contains(&"line-10".to_string()), "最新行应在");
        assert!(
            lines.iter().any(|l| l.contains("已删除最早 1 行")),
            "应有滚动标记：{lines:?}"
        );
        let _ = std::fs::remove_file(&path);
    }

    #[test]
    fn batch_size_is_max_one_tenth() {
        // 场景④：批量 = max(1, N/10)——N=10 删 1、N=100 删 10。
        let path10 = temp_path("batch10");
        let l10 = roll_scenario(&path10, 10, 11);
        assert!(
            l10.iter().any(|l| l.contains("已删除最早 1 行")),
            "N=10 应一批删 1 行：{l10:?}"
        );
        let _ = std::fs::remove_file(&path10);

        let path100 = temp_path("batch100");
        let l100 = roll_scenario(&path100, 100, 101);
        assert!(
            l100.iter().any(|l| l.contains("已删除最早 10 行")),
            "N=100 应一批删 10 行：{l100:?}"
        );
        assert!(!l100.iter().any(|l| *l == "line-9"), "最早 10 行应消失");
        assert!(l100.contains(&"line-10".to_string()), "第 11 行应还在");
        assert_eq!(l100.len(), 92, "90 旧 + 标记 + 最新");
        let _ = std::fs::remove_file(&path100);
    }

    #[test]
    fn limit_one_does_not_panic() {
        // 场景⑤：上限 1 不 panic、不死循环、内容有界（标记行 + 最新一行）。
        let path = temp_path("limit1");
        let _ = std::fs::remove_file(&path);
        let mut w = RollingLogWriter::new(open_append(&path), 1).unwrap();
        for i in 0..5 {
            write_log(&mut w, &format!("line-{i}"));
        }
        w.flush().unwrap();
        let lines = read_lines(&path);
        assert!(lines.len() <= 2, "上限 1 也应受控：{lines:?}");
        assert_eq!(*lines.last().unwrap(), "line-4", "最新一行应在");
        let _ = std::fs::remove_file(&path);
    }

    #[test]
    fn no_prune_on_every_write_after_batch() {
        // 场景⑥：删批后连续写不会每写一行就删一批。
        let path = temp_path("steady");
        let _ = std::fs::remove_file(&path);
        let mut w = RollingLogWriter::new(open_append(&path), 100).unwrap();
        for i in 0..100 {
            write_log(&mut w, &format!("line-{i}"));
        }
        assert_eq!(w.roll_count(), 0, "写满未超不删");
        write_log(&mut w, "line-100"); // 101 > 100：触发，删 10 行
        assert_eq!(w.roll_count(), 1);
        // 删批后 92 行，再写 8 行到 100 都不触发。
        for i in 101..109 {
            write_log(&mut w, &format!("line-{i}"));
        }
        assert_eq!(w.roll_count(), 1, "删批后不该每写一行就删一批");
        write_log(&mut w, "line-109"); // 101 > 100：才再次触发
        assert_eq!(w.roll_count(), 2);
        let _ = std::fs::remove_file(&path);
    }

    #[test]
    fn writes_without_newline_are_counted() {
        // 场景⑦：无换行写入也计数，且与 `BufRead::lines()` 读回口径一致。
        let path = temp_path("partial");
        let _ = std::fs::remove_file(&path);
        let mut w = RollingLogWriter::new(open_append(&path), 3).unwrap();
        w.write_all(b"abc").unwrap(); // 未完成行，计 1 行
        assert_eq!(w.lines(), 1);
        writeln!(w, "def").unwrap(); // 与 "abc" 同属一行（部分行被补完）
        assert_eq!(w.lines(), 1, "写入口径与读回一致：还是一行");
        assert_eq!(read_lines(&path), ["abcdef"]);
        writeln!(w, "ghi").unwrap();
        writeln!(w, "jkl").unwrap();
        assert_eq!(w.lines(), 3);
        writeln!(w, "mno").unwrap(); // 4 > 3：删头批
        assert_eq!(w.roll_count(), 1);
        let lines = read_lines(&path);
        assert!(!lines.iter().any(|l| l == "abcdef"), "最旧行应被删");
        assert_eq!(*lines.last().unwrap(), "mno", "最新内容应在");
        assert!(lines.iter().any(|l| l.contains("已删除")));
        let _ = std::fs::remove_file(&path);
    }

    #[test]
    fn default_max_lines_matches_build_script() {
        // 兜底值三处一致（本文件 / voke-app/build.rs / main.rs），改时一起改。
        assert_eq!(DEFAULT_MAX_LINES, 9999);
    }
}
