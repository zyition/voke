package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zyition/voke/poc/voke-go/internal/logic/frequency"
)

// TestImportProductionFrequency：只读合并生产 frequency.toml，逐键取 max（只增不减）；
// 已更高的本侧计数不被覆盖。用临时 APPDATA 指到假的生产目录，不碰真机数据。
func TestImportProductionFrequency(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "voke"), 0o755); err != nil {
		t.Fatal(err)
	}
	prod := "\"apps:钉钉\" = 5\n\"apps:微信\" = 1\n\"apps:终端\" = 25\n"
	if err := os.WriteFile(filepath.Join(dir, "voke", "frequency.toml"), []byte(prod), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("APPDATA", dir)
	t.Setenv(freqPathEnv, "") // 不启用本侧路径覆盖（否则不导入）

	store := frequency.New()
	store["apps:微信"] = 3 // 本侧更高 → 不应被拉低

	if n := importProductionFrequency(store); n != 2 { // 钉钉、终端 变化；微信保持
		t.Errorf("导入变化键数 = %d，期望 2", n)
	}
	if store["apps:钉钉"] != 5 || store["apps:终端"] != 25 || store["apps:微信"] != 3 {
		t.Errorf("导入结果不对：%v", store)
	}
	// 再次导入应幂等（无变化）。
	if n := importProductionFrequency(store); n != 0 {
		t.Errorf("二次导入应无变化，实际 %d", n)
	}
}
