// cwd.go：启动早期纠正 voke-go **自身**的工作目录（对照 voke-sys/src/cwd.rs）。
//
// 为什么需要：开机自启走 `HKCU\…\Run` 时注册表项不指定工作目录，进程会继承到系统目录
// （实测 cwd=`C:\Windows\System32`）。后果：任何继承 cwd 启动的子进程（os/exec 直启的
// 编辑器 / wt.exe 兜底 / shell 命令）都把 System32 当起始目录，语义不合理。用户从终端 /
// 资源管理器显式启动时的 cwd 是本人意图，绝不能动——所以只纠正「系统目录」与「读取失败」两类。
//
// 分层：判定是纯函数（shouldFixOwnCwd / isSystemDir），Linux 原生可表驱动单测；真正改目录的
// FixOwnCwd 用 os.Getwd / os.Chdir（跨平台、无 windows crate / cfg 分支），非 Windows 也能
// 干净编译。与 logcap / selftest 同款「纯逻辑下沉到可测层」的理由。
package platform

import (
	"log"
	"os"
	"path/filepath"
	"strings"
)

// isSystemDir 判断给定路径是否落在 Windows 系统目录内（大小写 / 盘符不敏感，同时兼容
// `\` 与 `/` 分隔）。命中：`X:\Windows` 及 `System32`/`SysWOW64` 等其任意子目录。
// 判据取 `windows` 组件出现在首段（无盘符相对写法）或第二段（紧跟盘符）；更深位置
// （如 `C:\Program Files\WindowsApps`）不算系统目录，不误伤。非 Windows 路径恒 false。
func isSystemDir(path string) bool {
	comps := strings.FieldsFunc(strings.ToLower(path), func(r rune) bool {
		return r == '\\' || r == '/'
	})
	for i, c := range comps {
		if c == "windows" {
			return i == 0 || i == 1
		}
	}
	return false
}

// shouldFixOwnCwd 报告当前工作目录是否需要被纠正：readOK=false（读取失败，目录可能已被
// 删除）→ true；落在系统目录 → true；其它（用户显式指定的 cwd）→ false，尊重意图。
func shouldFixOwnCwd(current string, readOK bool) bool {
	if !readOK {
		return true
	}
	return isSystemDir(current)
}

// FixOwnCwd 在启动早期纠正自身工作目录。调用时机：InitLog 之后、任何相对路径解析之前
// （早于日志初始化会丢日志）。目标优先级：%USERPROFILE%（须存在）→ 当前 exe 所在目录
// （须存在）；都失败只 warn、绝不 panic。
func FixOwnCwd() {
	current, err := os.Getwd()
	if !shouldFixOwnCwd(current, err == nil) {
		log.Printf("工作目录正常，未调整: %s", current)
		return
	}
	source := current
	if err != nil {
		source = "<读取失败>"
	}

	target := pickCwdTarget()
	if target == "" {
		log.Printf("voke 工作目录需纠正（源自 %s）但无可用目标目录，保持原样", source)
		return
	}
	if chErr := os.Chdir(target); chErr != nil {
		log.Printf("voke 纠正工作目录 %s→%s 失败：%v（保持原样）", source, target, chErr)
		return
	}
	log.Printf("voke 启动时将工作目录从 %s 修正为 %s", source, target)
}

// pickCwdTarget 按优先级选一个确实存在的目标目录：%USERPROFILE% → exe 所在目录；
// 都没有返回空串（调用方据此放弃纠正、不 panic）。
func pickCwdTarget() string {
	if up := os.Getenv("USERPROFILE"); up != "" && isDir(up) {
		return up
	}
	if exe, err := os.Executable(); err == nil {
		if dir := filepath.Dir(exe); dir != "" && isDir(dir) {
			return dir
		}
	}
	return ""
}

// isDir 报告路径是否为确实存在的目录。
func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
