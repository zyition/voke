// appsmodel 的同包测试（去重规则）。从 apps_test.go 随 DedupeApps 一起搬进来。
package appsmodel

import (
	"testing"
)

// TestDedupeAppsRemovesExactDuplicates 验证 (名称, 路径) 完全重复的条目
// 被去掉，且大小写不同视为同一路径（Windows 路径语义，对照
// voke-sys/src/apps.rs 的 dedup_ignores_relative_path_case 测试）。
func TestDedupeAppsRemovesExactDuplicates(t *testing.T) {
	in := []AppEntry{
		{Name: "A", Path: "shell:AppsFolder\\a", ID: "a"},
		{Name: "a", Path: "shell:AppsFolder\\A", ID: "a"}, // 同一条（大小写差异）
		{Name: "B", Path: "shell:AppsFolder\\b", ID: "b"},
		{Name: "A", Path: "shell:AppsFolder\\other", ID: "other"}, // 同名不同路径：保留
	}
	got := DedupeApps(in)
	if len(got) != 3 {
		t.Fatalf("去重后期望 3 条，实际 %d：%+v", len(got), got)
	}
	// 输入切片不被改动（DedupeApps 返回新切片）。
	if len(in) != 4 {
		t.Fatalf("输入切片被改动：%d", len(in))
	}
}

// TestDedupeAppsEmpty 验证空/ nil 输入不炸。
func TestDedupeAppsEmpty(t *testing.T) {
	if got := DedupeApps(nil); len(got) != 0 {
		t.Fatalf("nil 输入应返回空，实际 %+v", got)
	}
}

// TestDedupeAppsKeepsFirstOccurrenceOrder 验证去重只删「后到的重复项」，
// 且保留首次出现的那条（ID 取第一条的）——枚举序是 filterApps 稳定排序的
// 基准，改成「留最后一条」会让同分条目在列表里换位置。
func TestDedupeAppsKeepsFirstOccurrenceOrder(t *testing.T) {
	got := DedupeApps([]AppEntry{
		{Name: "微信", Path: "shell:AppsFolder\\wx", ID: "first"},
		{Name: "计算器", Path: "shell:AppsFolder\\jsq", ID: "calc"},
		{Name: "微信", Path: "shell:AppsFolder\\WX", ID: "second"},
	})
	if len(got) != 2 {
		t.Fatalf("应剩 2 条，实际 %d：%+v", len(got), got)
	}
	if got[0].Name != "微信" || got[0].ID != "first" || got[1].ID != "calc" {
		t.Fatalf("应保留首次出现的那条且顺序不变，实际 %+v", got)
	}
}
