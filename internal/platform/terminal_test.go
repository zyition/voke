package platform

import (
	"path/filepath"
	"testing"
)

// TestWTPackagesStableFirst：包序 stable 在前（合并去重优先级依赖它）。
func TestWTPackagesStableFirst(t *testing.T) {
	pkgs := WTPackages()
	if len(pkgs) != 2 || pkgs[0] != "Microsoft.WindowsTerminal_8wekyb3d8bbwe" ||
		pkgs[1] != "Microsoft.WindowsTerminalPreview_8wekyb3d8bbwe" {
		t.Fatalf("包序应 stable 前 preview 后：%v", pkgs)
	}
}

func TestPackageAUMID(t *testing.T) {
	if got := PackageAUMID("Microsoft.WindowsTerminal_8wekyb3d8bbwe"); got != "Microsoft.WindowsTerminal_8wekyb3d8bbwe!App" {
		t.Errorf("AUMID = %q", got)
	}
}

// TestWTSettingsPath / TestWTExePath：用同款 filepath.Join 构造期望（Linux 上 `\` 不是分隔符，
// 故必须分段拼、与实现同法，两平台一致）。
func TestWTSettingsPath(t *testing.T) {
	base := "C:/Users/y/AppData/Local"
	want := filepath.Join(base, "Packages", "Microsoft.WindowsTerminal_8wekyb3d8bbwe", "LocalState", "settings.json")
	if got := WTSettingsPath(base, "Microsoft.WindowsTerminal_8wekyb3d8bbwe"); got != want {
		t.Errorf("settings path = %q，want %q", got, want)
	}
}

func TestWTExePath(t *testing.T) {
	base := "C:/Users/y/AppData/Local"
	want := filepath.Join(base, "Microsoft", "WindowsApps", "wt.exe")
	if got := WTExePath(base); got != want {
		t.Errorf("wt.exe path = %q，want %q", got, want)
	}
}
