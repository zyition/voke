// Package platform 是 §7.2.1 三层铁律里的 **platform 层**：一切平台专属调用
// （Win32 / syscall，以及步骤6b、8 接手的 mygo 模块）住这里。
//
// 分层纪律（照抄仓库既有的「core 不依赖 gpui/windows」铁律）：
//   - `internal/logic/**` 零 mygo、零 syscall，本包反过来**只碰平台侧**，
//     业务判断一概不收（决策逻辑在 logic，视图意图在 ui/app）；
//   - 平台专属文件必须配一个 `_stub.go` 占位（PoC 在根目录 `mem.go` /
//     `mem_stub.go` 立的规矩，§7.2.1 第 3 条），调用方（main / panel / apps）
//     因此不写任何平台分支，Linux 原生 `go build ./...` / `go test ./...` 保持绿；
//   - 跨平台纯 stdlib 的件（本包的 appdirs.go）**不加构建标签**，好让它在
//     Linux 原生被测到。
//
// 本包目前只有 §7.2 步骤6a 从 `package main` 抽出来的两件零 mygo 干净平台件：
// 应用目录 / 日志初始化（appdirs.go）与进程内存快照（mem_windows.go +
// mem_stub.go）。已陆续收进本层的还有 Shell 枚举/启动/图标（shell.go）、单实例
// 锁、热键注册、托盘构造（singleinstance.go / hotkey.go / tray.go，均纯转发）。
// 仍留在 package main 的只有与 mygo App 生命周期回调缠绕的装配（WhenReady /
// OnSecondInstance、Window.Update 回主线程接缝、面板窗口 show/hide）——属 step8。
package platform

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/zyition/voke/poc/voke-go/internal/logic/logcap"
)

const (
	// dataDirName 与生产 voke **共用**数据目录（%APPDATA%\voke\）：config.toml /
	// frequency.toml 采用与 Rust 相同的 TOML 结构，两版可互相读取（用户拍板，覆盖早期
	// 的隔离约定）。
	dataDirName = "voke"
	// logFileName 与生产一致：共享目录下的 voke.log（本项目将替换原版本，不再区分）。
	logFileName = "voke.log"
)

// logMaxLinesRaw 是日志行数上限的**构建期注入点**（§7.1 O9 接线层）。
//
// 对应生产 Rust 侧的 `VOKE_LOG_MAX_LINES` 三段式：
// `VOKE_LOG_MAX_LINES=20000 bash packaging/build-installer.sh` →
// `voke-app/build.rs` 读环境变量并 `cargo:rustc-env` 注入 →
// `main.rs::log_max_lines()` 解析（未设 / 非法 / 为 0 一律兜底 9999）。
//
// Go 没有 build.rs，等价姿势是 `-ldflags -X`，但**链接器只改写字符串变量**
// （实测 `-X main.n=9` 指向 int 变量直接链接失败：
// `main.n: cannot set with -X: not a var of type string (type:int)`），
// 所以注入点必须是 string、解析与兜底放在 [resolveLogMaxLines]（口径逐条对齐
// build.rs:54-68 + main.rs:1022-1029）。出特例包时才加这一段 ldflags：
//
//	go build -tags mygo_noinspector \
//	  -ldflags="-s -w -H windowsgui -X github.com/zyition/voke/poc/voke-go/internal/platform.logMaxLinesRaw=20000" \
//	  -o voke-go.exe .
//
// 交付构建命令（见 CONTRIBUTING.md）**不带**这条 -X：空串 = 未注入
// = 沿用 [logcap.DefaultMaxLines]（9999）。本地安装器脚本会把 VOKE_LOG_MAX_LINES
// 映射到这条 -X。
var logMaxLinesRaw string

// resolveLogMaxLines 把注入的原始字符串解析成 [InitLog] 要用的上限，并可能带回
// 一句中文警告（**不在这里落日志**：由 InitLog 在接管输出之后写，这样非法注入值
// 在日志文件里也留痕——GUI 子系统下 stderr 不可见，而 Rust 侧的等价提示是
// build.rs 的 cargo:warning，同样只出现在构建期）。
//
// 三分支与生产一致：
//   - 未注入（空串 / 全空白）→ 默认 [logcap.DefaultMaxLines]，无警告；
//   - 注入合法正整数 → 原样用，无警告；
//   - 注入了非法值（非数字 / 为 0 / 负数）→ 兜底默认 + 警告（不静默）。
func resolveLogMaxLines(raw string) (int, string) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return logcap.DefaultMaxLines, ""
	}
	n, err := strconv.Atoi(trimmed)
	if err != nil || n <= 0 {
		return logcap.DefaultMaxLines,
			"日志行数上限注入值 " + strconv.Quote(raw) +
				" 非法（非数字 / 为 0 / 负数），已用默认 " +
				strconv.Itoa(logcap.DefaultMaxLines) + " 行"
	}
	return n, ""
}

// logFile 是接线层对日志文件的要求：[logcap.File]（裁剪所需的
// Write/ReadAt/Truncate/Sync/Stat）+ [io.Closer]（收尾放句柄）。
// `*os.File` 天然满足；拆成接口是为了让 [newLogWriter] 的失败降级分支能在
// Linux 上用假件测到（真 *os.File 造不出「Stat 有内容、ReadAt 读不动」这个
// 状态），调用方一字不用改。
type logFile interface {
	logcap.File
	io.Closer
}

// rollingSink 把 [logcap.RollingLogWriter] 与它包着的日志文件凑成一个
// 可回收的 log 输出：log 包只要 io.Writer，而封顶逻辑刻意不提供 Close
// （logcap 只管裁剪，文件生命周期属接线层），所以这里补上 [rollingSink.Close]。
// 没有这条 Close，用例就没法在收尾时放掉 InitLog 打开的句柄，Windows 原生
// `go test` 里 t.TempDir 会因为文件仍被占用而删不掉。
type rollingSink struct {
	*logcap.RollingLogWriter
	file io.Closer
}

// Close 关掉底层日志文件句柄（封顶状态随之作废）。
func (s *rollingSink) Close() error { return s.file.Close() }

// newLogWriter 是「把已打开的日志文件包成封顶输出」的唯一接缝（§7.1 O9）。
//
// 成功 → [rollingSink]（跨启动追加 + 超上限从头部删一批 max(1,N/10) + 一行
// 中文标记，算法全在 internal/logic/logcap，本层只接线）；
// [logcap.New] 失败（数既有行数 / 读末尾字节出错，典型是忘了 O_RDWR）→
// **退回裸文件**：宁可不封顶，也绝不能因为封顶初始化失败就把日志整体搞没，
// 同时尽力落一条中文警告留痕。对应生产 `init_logging()` 的退化分支
// （voke-app/src/main.rs:981-1000，那里是「文件都写不了才退 stderr」）。
func newLogWriter(f logFile, maxLines int) io.Writer {
	w, err := logcap.New(f, maxLines)
	if err != nil {
		log.Printf("初始化日志行数上限失败（日志不封顶，继续追加同一文件）：%v", err)
		return f
	}
	return &rollingSink{RollingLogWriter: w, file: f}
}

// InitLog 初始化日志：数据目录 %APPDATA%\voke\（与生产共用），日志文件 voke-go.log，
// 跨启动追加不清空，总行数受 [logMaxLinesRaw]（默认 9999）上限的滚动删除控制
// —— §7.1 O9 接生产 `voke-sys/src/logcap.rs` 的同款语义。
// 失败只影响文件日志，不阻断启动。
func InitLog() {
	dir := DataDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("创建数据目录 %s 失败（继续，仅写 stderr）：%v", dir, err)
		return
	}
	// O_RDWR（不是 O_WRONLY）是**封顶的硬前提**：删头批要把整个文件读回来数行，
	// 打开时也要数一遍既有行数当跨启动基线，只写句柄的 ReadAt 会 EBADF。
	// O_APPEND 的「写永远追加到末尾」不受影响（ReadAt 不动写偏移），与生产
	// `open_log_file_append()` 的 create + append + read 三件套逐条对应。
	f, err := os.OpenFile(filepath.Join(dir, logFileName), os.O_CREATE|os.O_APPEND|os.O_RDWR, 0o644)
	if err != nil {
		log.Printf("打开日志文件失败（继续，仅写 stderr）：%v", err)
		return
	}
	maxLines, warn := resolveLogMaxLines(logMaxLinesRaw)
	log.SetOutput(newLogWriter(f, maxLines))
	if warn != "" {
		log.Print(warn)
	}
}

// DataDir 返回数据目录 %APPDATA%\voke\（与生产共用；配置/频率为同格式 TOML）。
// 非 Windows 环境（Linux 原生构建/测试）退回用户配置目录下的同名目录。
func DataDir() string {
	if appdata := os.Getenv("APPDATA"); appdata != "" {
		return filepath.Join(appdata, dataDirName)
	}
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, dataDirName)
	}
	return filepath.Join(os.TempDir(), dataDirName)
}
