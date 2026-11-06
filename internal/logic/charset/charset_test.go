package charset

import (
	"strings"
	"testing"
)

func compile(t *testing.T, pattern string) []rune {
	t.Helper()
	out, err := Compile(pattern)
	if err != nil {
		t.Fatalf("Compile(%q) 报错：%v", pattern, err)
	}
	return out
}

func hasRune(rs []rune, want rune) bool {
	for _, r := range rs {
		if r == want {
			return true
		}
	}
	return false
}

func TestCompileRangesAreCaseSensitive(t *testing.T) {
	lower := compile(t, "a-f")
	if !hasRune(lower, 'a') || !hasRune(lower, 'f') || hasRune(lower, 'A') {
		t.Errorf("a-f 应只含小写：%q", string(lower))
	}
	upper := compile(t, "A-F")
	if !hasRune(upper, 'A') || hasRune(upper, 'a') {
		t.Errorf("A-F 应只含大写：%q", string(upper))
	}
}

func TestCompileAcceptsClassContentsAndFullClasses(t *testing.T) {
	for _, pattern := range []string{"a-z0-9", "[a-z0-9]"} {
		got := compile(t, pattern)
		if len(got) != 36 || !hasRune(got, 'z') || !hasRune(got, '9') || hasRune(got, '_') {
			t.Errorf("%q 编译结果不符（%d 个字符）：%q", pattern, len(got), string(got))
		}
	}
}

func TestCompileEscapedHyphen(t *testing.T) {
	got := compile(t, `a-c\-`)
	if !hasRune(got, '-') {
		t.Errorf("转义连字符应进入集合：%q", string(got))
	}
	if hasRune(got, ',') || hasRune(got, '.') {
		t.Errorf("`-` 不应被当成范围：%q", string(got))
	}
}

func TestCompileUnicodeLiterals(t *testing.T) {
	got := compile(t, "￥%…")
	for _, want := range []rune{'￥', '%', '…'} {
		if !hasRune(got, want) {
			t.Errorf("非 ASCII 字面字符 %q 应进入集合：%q", want, string(got))
		}
	}
	if hasRune(got, 'a') || hasRune(got, '0') {
		t.Errorf("不应混入 ASCII 字母数字：%q", string(got))
	}
}

func TestCompileRejectsBadOrEmptyPatterns(t *testing.T) {
	cases := []string{"", "   ", "z-a", `\x01`}
	for _, pattern := range cases {
		if _, err := Compile(pattern); err == nil {
			t.Errorf("Compile(%q) 应报错", pattern)
		}
	}
	if _, err := Compile(strings.Repeat("a", MaxPatternRunes+1)); err == nil {
		t.Error("超长正则应报错")
	}
}

func TestValidateName(t *testing.T) {
	ok := Spec{Name: "小写十六进制", Pattern: "a-f0-9"}
	if err := Validate(ok); err != nil {
		t.Errorf("合法配置不应报错：%v", err)
	}
	for _, name := range []string{"", "   ", "a\nb", strings.Repeat("名", MaxNameRunes+1)} {
		if err := Validate(Spec{Name: name, Pattern: "a-z"}); err == nil {
			t.Errorf("名称 %q 应判非法", name)
		}
	}
}

func TestSanitizeTrimsDedupesAndDropsInvalid(t *testing.T) {
	specs := []Spec{
		{Name: " 字母 ", Pattern: " a-z ", Enabled: true},
		{Name: "字母", Pattern: "A-Z", Enabled: true}, // 重复名（裁剪后）→ 丢弃
		{Name: "默认", Pattern: "a-z", Enabled: false},
		{Name: "", Pattern: "a-z"},
		{Name: "坏正则", Pattern: "z-a"},
	}
	got, warnings := Sanitize(specs)
	if len(got) != 2 {
		t.Fatalf("应保留 2 条：%+v", got)
	}
	if got[0].Name != "字母" || got[0].Pattern != "a-z" || !got[0].Enabled {
		t.Errorf("首条应裁剪空白并保留：%+v", got[0])
	}
	if got[1].Name != "默认" || got[1].Enabled {
		t.Errorf("停用条目也应保留：%+v", got[1])
	}
	if len(warnings) != 3 {
		t.Errorf("应产生 3 条警告（重复/空名/坏正则）：%v", warnings)
	}
}

func TestSanitizeNamesAreCaseSensitive(t *testing.T) {
	got, warnings := Sanitize([]Spec{
		{Name: "hex", Pattern: "a-f"},
		{Name: "Hex", Pattern: "A-F"},
	})
	if len(got) != 2 || len(warnings) != 0 {
		t.Errorf("大小写不同的名称应共存：%+v / %v", got, warnings)
	}
}
