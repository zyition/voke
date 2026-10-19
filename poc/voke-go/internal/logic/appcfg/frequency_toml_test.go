package appcfg

import (
	"strings"
	"testing"

	"github.com/zyition/voke/poc/voke-go/internal/logic/frequency"
)

// TestFrequencyTOMLRoundTrip：本侧落盘格式与生产 frequency.toml 同形（平铺 `"key" = n`），
// 且经 TOML 编解码往返不丢键/次数（含中文、空格、中点、冒号）。
func TestFrequencyTOMLRoundTrip(t *testing.T) {
	store := frequency.New()
	store["apps:微信"] = 3
	store["apps:Task Manager"] = 15
	store["awake:保持唤醒中 · 屏幕打开（还剩 46 分钟）"] = 1

	data, err := FrequencyToTOML(store)
	if err != nil {
		t.Fatalf("FrequencyToTOML: %v", err)
	}
	if !strings.Contains(string(data), `"apps:微信" = 3`) {
		t.Errorf("TOML 形态与生产不一致，实际：\n%s", data)
	}
	got := FrequencyFromTOML(data)
	if len(got) != len(store) {
		t.Fatalf("往返条数 %d != %d：%v", len(got), len(store), got)
	}
	for k, v := range store {
		if got[k] != v {
			t.Errorf("键 %q 往返 = %d，期望 %d", k, got[k], v)
		}
	}
}

// TestFrequencyFromTOMLRustFile：直接解析生产 voke 的 frequency.toml 片段。
func TestFrequencyFromTOMLRustFile(t *testing.T) {
	data := []byte("\"apps:Alacritty\" = 23\n\"apps:微信\" = 1\n\"apps:钉钉\" = 5\n")
	got := FrequencyFromTOML(data)
	if got["apps:Alacritty"] != 23 || got["apps:微信"] != 1 || got["apps:钉钉"] != 5 {
		t.Errorf("解析生产 TOML 失败：%v", got)
	}
	if len(got) != 3 {
		t.Errorf("条数 = %d，期望 3", len(got))
	}
}

// TestFrequencyFromTOMLBad：坏数据回落空表、不 panic。
func TestFrequencyFromTOMLBad(t *testing.T) {
	if got := FrequencyFromTOML([]byte("this is = = not toml")); len(got) != 0 {
		t.Errorf("坏 TOML 应回落空表，得到 %v", got)
	}
	if got := FrequencyFromTOML(nil); len(got) != 0 {
		t.Errorf("空数据应回落空表，得到 %v", got)
	}
}
