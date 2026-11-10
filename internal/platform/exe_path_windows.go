//go:build windows

// exe_path_windows.go：PATH 与 App Paths 的 **Windows 读值**。
//
// 为什么不只读 os.Getenv("PATH")：voke 是常驻进程，进程启动后再装/改 PATH 不会
// 进到已运行进程的环境块。这里直接读注册表里的用户 / 机器 PATH（Windows 的
// 「当前值」），再与进程 PATH 合并——新装的编辑器不必重启 voke 才被找到。
//
// App Paths 是 Windows 的标准应用定位约定（HKCU 优先于 HKLM），VS Code 安装器
// 会登记 Code.exe；PATH 里没有、又不在常见安装目录时它是最后一道自动发现。
package platform

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

const (
	userEnvKey    = "Environment"
	machineEnvKey = `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`
	appPathsKey   = `SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\`
)

// pathDirs 返回去重后的 PATH 目录：用户 PATH → 机器 PATH → 进程 PATH。
// 注册表值可能含 %SystemRoot% 这类变量，先展开再拆分。
func pathDirs() []string {
	var dirs []string
	seen := make(map[string]struct{}, 32)
	add := func(raw string) {
		for _, d := range filepath.SplitList(expandPercent(raw)) {
			d = strings.TrimSpace(strings.Trim(d, "\""))
			if d == "" {
				continue
			}
			key := strings.ToLower(filepath.Clean(d))
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			dirs = append(dirs, d)
		}
	}
	if v, ok := registryString(registry.CURRENT_USER, userEnvKey, "Path"); ok {
		add(v)
	}
	if v, ok := registryString(registry.LOCAL_MACHINE, machineEnvKey, "Path"); ok {
		add(v)
	}
	add(os.Getenv("PATH"))
	return dirs
}

// appPathExe 查 App Paths\<name> 的默认值（exe 全路径）；未登记 / 文件不存在返回 ""。
func appPathExe(name string) string {
	for _, root := range []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE} {
		v, ok := registryString(root, appPathsKey+name, "")
		if !ok {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), "\"")
		if p := expandPercent(v); fileExists(p) {
			return p
		}
	}
	return ""
}

func registryString(root registry.Key, path, name string) (string, bool) {
	k, err := registry.OpenKey(root, path, registry.QUERY_VALUE)
	if err != nil {
		return "", false
	}
	defer k.Close()
	v, _, err := k.GetStringValue(name)
	if err != nil || strings.TrimSpace(v) == "" {
		return "", false
	}
	return v, true
}

// expandPercent 展开 Windows 风格的 %VAR%（os.ExpandEnv 只认 $VAR）。
// 未知变量按空串处理，与 CreateProcess 的环境展开一致。
func expandPercent(s string) string {
	var b strings.Builder
	for {
		i := strings.IndexByte(s, '%')
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		j := strings.IndexByte(s[i+1:], '%')
		if j < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		b.WriteString(os.Getenv(s[i+1 : i+1+j]))
		s = s[i+1+j+1:]
	}
}
