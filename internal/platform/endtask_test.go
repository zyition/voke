package platform

import (
	"os"
	"testing"
)

func TestEndTaskDeniedReason(t *testing.T) {
	own := uint32(os.Getpid())
	cases := []struct {
		name string
		pid  uint32
		exe  string
		want bool // true = 应拒绝
	}{
		{"自身进程", own, "voke.exe", true},
		{"PID 0", 0, "system", true},
		{"核心系统进程（大小写）", 1234, "CSRSS.EXE", true},
		{"lsass", 1234, "lsass.exe", true},
		{"取不到映像", 1234, "", true},
		{"普通应用", 1234, "chrome.exe", false},
		{"explorer 允许（Windows 会重建 shell）", 1234, "explorer.exe", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := endTaskDeniedReason(c.pid, c.exe) != ""
			if got != c.want {
				t.Fatalf("endTaskDeniedReason(%d, %q) 拒绝=%v，期望 %v", c.pid, c.exe, got, c.want)
			}
		})
	}
}
