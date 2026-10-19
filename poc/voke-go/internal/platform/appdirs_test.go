package platform

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// 本文件只测 appdirs.go 的跨平台纯逻辑（os + path/filepath）。内存快照
// SnapshotMem / StartMemTicker 的 Windows 侧要 K32GetProcessMemoryInfo，
// Linux 原生跑不到（非 Windows 只有「当前平台不提供」的占位分支），
// 故不在此写 Linux 断言，免得造出假测。

// isolateLog 把全局 log 的输出接过来，用例结束时复原并关掉 InitLog 打开的
// 句柄，并返回接管前的 writer 供断言。InitLog 与原 main.go 的 initLog 逐字
// 一致：句柄只交给 log 包、自己不留副本，所以用例得从 log.Writer() 拿回来
// 关——不关的话 Windows 原生 `go test` 里 t.TempDir 的清理会删不掉还被占用
// 的日志文件。
//
// §7.1 O9 接线后 log 输出是 [rollingSink]（封顶），不再是裸 *os.File：两种都
// 要能收尾关掉，否则 TempDir 清理照样失败；*os.File 分支保留是因为
// [newLogWriter] 的退化路径给出的就是不封顶的裸文件。
func isolateLog(t *testing.T) io.Writer {
	t.Helper()
	prev := log.Writer()
	t.Cleanup(func() {
		cur := log.Writer()
		log.SetOutput(prev)
		switch w := cur.(type) {
		case *rollingSink:
			_ = w.Close()
		case *os.File:
			if !isSameFile(w, prev) {
				w.Close()
			}
		}
	})
	return prev
}

// isSameFile 判断 w 是否就是 f：只有两边都是 *os.File 时才谈得上同一，
// 其它实现（测试框架换掉的 writer 之类）一律视为不同，避免拿不可比较的
// 动态类型比接口值直接 panic。
func isSameFile(f *os.File, w io.Writer) bool {
	other, ok := w.(*os.File)
	return ok && other == f
}

// TestDataDirUsesAppData 验 Windows 口径：APPDATA 存在时数据目录就是
// %APPDATA%\voke\，绝对路径（与生产共用；配置/频率为同格式 TOML，可互相读取）。
func TestDataDirUsesAppData(t *testing.T) {
	root := filepath.FromSlash("/fake-appdata-roaming")
	t.Setenv("APPDATA", root)

	want := filepath.FromSlash("/fake-appdata-roaming/voke")
	if got := DataDir(); got != want {
		t.Errorf("DataDir() = %q，期望 %q", got, want)
	}
	if !filepath.IsAbs(DataDir()) {
		t.Errorf("DataDir() 必须是绝对路径，拿到 %q", DataDir())
	}
}

// TestDataDirFallsBackWithoutAppData 验非 Windows（或未设 APPDATA）的回退分支：
// 先用户配置目录下的同名目录，再退临时目录；两种退路都保持绝对路径 + voke-go。
func TestDataDirFallsBackWithoutAppData(t *testing.T) {
	t.Setenv("APPDATA", "")

	// 非 Windows 上 os.UserConfigDir 读 XDG_CONFIG_HOME，钉一个确定值；
	// Windows 上它读 %APPDATA%（这里为空）→ 返回 error → 按实现退回 TempDir 分支。
	root := filepath.FromSlash("/fake-xdg-config")
	if runtime.GOOS == "windows" {
		root = os.TempDir()
	} else {
		t.Setenv("XDG_CONFIG_HOME", root)
	}

	want := filepath.Join(root, "voke")
	got := DataDir()
	if got != want {
		t.Errorf("DataDir() = %q，期望 %q", got, want)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("DataDir() 必须是绝对路径，拿到 %q", got)
	}
}

// TestInitLogWritesUnderAppDataDir 验日志落点与「log 输出真的接到了文件」：
// %APPDATA%\voke-go\voke-go.log，父目录自动建出来。
func TestInitLogWritesUnderAppDataDir(t *testing.T) {
	root := t.TempDir()
	t.Setenv("APPDATA", root)
	isolateLog(t)

	InitLog()

	const marker = "voke-platform-initlog"
	log.Print(marker)

	logPath := filepath.Join(root, dataDirName, logFileName)
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("读日志文件 %s 失败：%v", logPath, err)
	}
	if !strings.Contains(string(data), marker) {
		t.Errorf("日志文件 %s 里没有 marker，实际内容：%q", logPath, string(data))
	}
	// 目录名与生产一致（共享数据；日志文件名仍区分，见 logFileName）。
	if dir := filepath.Dir(logPath); filepath.Base(dir) != "voke" {
		t.Errorf("日志父目录名 = %q，期望 voke（%s）", filepath.Base(dir), dir)
	}
}

// TestInitLogAppendsAcrossCalls 验「跨启动追加不清空」：InitLog 再跑一次
// （等价于进程重启后再次初始化）不能把已有日志截掉。
//
// §7.1 O9 接线后顺带把「第二次初始化真的数到了既有行数」钉成绝对值断言：
// 接到的是封顶 sink（不是裸文件），且 sink 的起始行数 = 上一轮留下的 1 行
// ——这条只有 O_RDWR 打开且 logcap.New 读回全文成功才可能成立。
func TestInitLogAppendsAcrossCalls(t *testing.T) {
	root := t.TempDir()
	t.Setenv("APPDATA", root)

	prev := log.Writer()
	defer log.SetOutput(prev)

	markers := []string{"第一轮-启动前的旧日志", "第二轮-本次追加"}
	// 每轮 sink 报出的「本次初始化读到的既有行数」，期望 0 → 1（跨启动基线）。
	wantBaseline := []int{0, 1}
	for i, marker := range markers {
		InitLog()
		log.Print(marker)
		// 每轮的句柄用完即关，别攒着占住 TempDir 里的文件。
		sink, ok := log.Writer().(*rollingSink)
		if !ok {
			t.Fatalf("第 %d 轮 InitLog 没把 log 输出接到封顶 sink，实际 %T", i+1, log.Writer())
		}
		if got := sink.Lines() - 1; got != wantBaseline[i] {
			t.Errorf("第 %d 轮初始化读到的既有行数 = %d，期望 %d（跨启动基线）", i+1, got, wantBaseline[i])
		}
		if err := sink.Close(); err != nil {
			t.Fatalf("关闭日志句柄失败：%v", err)
		}
	}

	data, err := os.ReadFile(filepath.Join(root, dataDirName, logFileName))
	if err != nil {
		t.Fatalf("读日志文件失败：%v", err)
	}
	got := string(data)
	for _, marker := range markers {
		if !strings.Contains(got, marker) {
			t.Errorf("追加后的日志缺了 %q，实际内容：%q", marker, got)
		}
	}
	if n := strings.Count(got, "\n"); n != len(markers) {
		t.Errorf("日志行数 = %d，期望 %d（每轮一行），实际内容：%q", n, len(markers), got)
	}
	// 远低于上限（默认 9999）时不该有任何滚动删除的痕迹。
	if strings.Contains(got, "日志超过") {
		t.Errorf("未超上限却出现滚动标记行，实际内容：%q", got)
	}
}

// TestInitLogSurvivesUnwritableDir 验失败不阻断启动：APPDATA 指向一个普通
// 文件时建目录必然失败，InitLog 只记日志就返回，既不 panic 也不接管 log 输出
// （否则会把日志写进一个开不了的文件、后续全丢）。
func TestInitLogSurvivesUnwritableDir(t *testing.T) {
	root := t.TempDir()
	blocker := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("占位：APPDATA 指向普通文件"), 0o644); err != nil {
		t.Fatalf("造阻塞文件失败：%v", err)
	}
	t.Setenv("APPDATA", blocker)

	// 先把输出挪到 io.Discard 再接管：失败分支本来就该往 stderr 记一条
	// 「创建数据目录失败」，不接掉会把测试输出刷屏；io.Discard 是可比的
	// 指针型 sink，正好用来断言「输出没被替换」。
	log.SetOutput(io.Discard)
	prev := isolateLog(t)

	InitLog() // 不该 panic，也不该改写 log 输出

	if _, err := os.Stat(filepath.Join(blocker, dataDirName)); err == nil {
		t.Errorf("APPDATA 不可用时不该建出数据目录")
	}
	if entries, err := os.ReadDir(root); err != nil {
		t.Fatalf("读回测试根目录失败：%v", err)
	} else if len(entries) != 1 || entries[0].Name() != filepath.Base(blocker) {
		t.Errorf("建目录失败时不该在 %s 下留下别的东西，实际 %v", root, entries)
	}
	// 输出没被替换：失败分支只记一条日志就 return（换成开不了的句柄会把
	// 后续日志全丢掉）。
	if log.Writer() != prev {
		t.Errorf("建目录失败时 InitLog 不该替换 log 输出，实际接到 %T", log.Writer())
	}
}
