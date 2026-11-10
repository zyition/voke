// exe.go：可执行文件发现（编辑器 / wt.exe 兜底路径用）的**纯路径部分**。
//
// 背景：VS Code / Zed / Windows Terminal 都可能装在非默认位置。主编排器的通行
// 做法是「常见安装根目录 + PATH + 系统注册的 App Paths」逐级查找（PowerToys Run
// 的 VSCodeWorkspaces 插件即按 PATH 找 code 命令），而不是要求用户在配置里逐条
// 写死 exe 路径。本文件提供与平台无关的查找原语；PATH/注册表读值在
// exe_path_windows.go，非 Windows 走 exe_path_stub.go（返回空，保持 Linux 单测
// 不依赖开发机 PATH、结果可复现）。
package platform

import (
	"os"
	"path/filepath"
	"strings"
)

// installRoots 返回应用可能落地的安装根目录：当前用户程序目录 + 两个系统
// Program Files。空值跳过、大小写不敏感去重（Windows 路径语义），顺序即优先级。
func installRoots() []string {
	var roots []string
	seen := make(map[string]struct{}, 3)
	for _, env := range []string{"LOCALAPPDATA", "ProgramFiles", "ProgramFiles(x86)"} {
		v := strings.TrimSpace(os.Getenv(env))
		if v == "" {
			continue
		}
		key := strings.ToLower(filepath.Clean(v))
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		roots = append(roots, v)
	}
	return roots
}

// firstExistingInRoots 按「候选相对路径优先、安装根目录其次」查找第一个真实
// 存在的文件：候选序表达稳定版 / Insiders 的优先级，根目录序表达用户安装 /
// 系统安装的优先级。都不存在返回 ""。
func firstExistingInRoots(roots []string, candidates [][]string) string {
	for _, suffix := range candidates {
		for _, root := range roots {
			p := root
			for _, part := range suffix {
				p = filepath.Join(p, part)
			}
			if fileExists(p) {
				return p
			}
		}
	}
	return ""
}

// findExecutableInDirs 在给定目录列表里按文件名查找第一个存在的文件。
// 名字要带扩展名（code.cmd / Code.exe），不做 PATHEXT 推断——调用方明确知道
// 自己要的是批处理还是 exe，猜错反而会选中不兼容的入口。
func findExecutableInDirs(dirs []string, names ...string) string {
	for _, dir := range dirs {
		dir = strings.TrimSpace(strings.Trim(dir, "\""))
		if dir == "" {
			continue
		}
		for _, name := range names {
			p := filepath.Join(dir, name)
			if fileExists(p) {
				return p
			}
		}
	}
	return ""
}
