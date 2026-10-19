package ui

import "testing"

// TestPluginIconsParse 钉死：所有内置 Lucide SVG 都能被 mygo 解析（MustParseSVG 对坏
// SVG 会 panic，这条在单测里先炸出来，免得真机首次渲染插件行时才崩）。同时核对已知
// 插件各取其图、未知/空 id 落 Blocks 兜底。改坏任一 SVG 字符串 → 本例红（红线⑧）。
func TestPluginIconsParse(t *testing.T) {
	known := []string{"awake", "terminal", "workspaces", "calculator", "timestamp", "base64", "uuid", "random", "apps", "windows"}
	fallback := PluginIconSVG("no-such-plugin")
	if fallback == nil {
		t.Fatal("兜底图标解析成 nil")
	}
	for _, id := range known {
		s := PluginIconSVG(id)
		if s == nil {
			t.Fatalf("%q 图标解析成 nil", id)
		}
		if s == fallback {
			t.Errorf("%q 与兜底 Blocks 同一指针，说明未在表里命中", id)
		}
	}
	if PluginIconSVG("") != fallback {
		t.Errorf("空 id 应落 Blocks 兜底")
	}
}
