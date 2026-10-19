package platform

import (
	"os"
	"testing"
)

func TestIsSystemDir(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{`C:\Windows`, true},
		{`C:\Windows\System32`, true},
		{`C:\Windows\SysWOW64`, true},
		{`c:\windows\system32\drivers`, true}, // 大小写不敏感 + 子目录
		{`C:/Windows/System32`, true},         // 正斜杠同样切分
		{`\Windows\System32`, true},           // 无盘符：windows 在首段
		{`/windows`, true},
		{`C:\Program Files\WindowsApps`, false}, // windows 在更深位置，不误伤
		{`C:\Users\loiac`, false},
		{`/home/loiac/proj`, false},
		{``, false},
	}
	for _, c := range cases {
		if got := isSystemDir(c.path); got != c.want {
			t.Errorf("isSystemDir(%q)=%v，想要 %v", c.path, got, c.want)
		}
	}
}

func TestShouldFixOwnCwd(t *testing.T) {
	cases := []struct {
		current string
		readOK  bool
		want    bool
	}{
		{`C:\Windows\System32`, true, true}, // 系统目录 → 纠正
		{`/home/loiac/proj`, true, false},   // 用户显式 cwd → 不动
		{``, false, true},                   // 读取失败 → 保险纠正
		{`/whatever`, false, true},          // 读取失败一律纠正，即便 current 看着正常
	}
	for _, c := range cases {
		if got := shouldFixOwnCwd(c.current, c.readOK); got != c.want {
			t.Errorf("shouldFixOwnCwd(%q, readOK=%v)=%v，想要 %v", c.current, c.readOK, got, c.want)
		}
	}
}

func TestPickCwdTargetPrefersUserprofile(t *testing.T) {
	dir := t.TempDir() // 确实存在的目录
	t.Setenv("USERPROFILE", dir)
	if got := pickCwdTarget(); got != dir {
		t.Errorf("USERPROFILE 存在时应优先返回它，实得 %q", got)
	}
	// USERPROFILE 指向不存在的路径 → 回落到 exe 所在目录（存在且非空即可，不钉死具体值）。
	t.Setenv("USERPROFILE", dir+string(os.PathSeparator)+"does-not-exist")
	if got := pickCwdTarget(); got == "" {
		t.Error("USERPROFILE 无效时应回落到 exe 目录，实得空串")
	}
}
