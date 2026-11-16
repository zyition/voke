package charset

import (
	"testing"
)

func wantRunes(t *testing.T, got []rune, want string) {
	t.Helper()
	if string(got) != want {
		t.Errorf("分组 = %q，想要 %q", string(got), want)
	}
}

func TestCompileGroupsSplitsRangesAndMergesLiterals(t *testing.T) {
	groups := CompileGroups("0-9&a-z!")
	if len(groups) != 3 {
		t.Fatalf("应 3 组（0-9 / a-z / &!），实得 %d：%q", len(groups), groups)
	}
	wantRunes(t, groups[0], "0123456789")
	wantRunes(t, groups[1], "abcdefghijklmnopqrstuvwxyz")
	wantRunes(t, groups[2], "&!")
}

func TestCompileGroupsDefaultSymbolSet(t *testing.T) {
	groups := CompileGroups("a-zA-Z0-9!@#$%^&*\\-_=+")
	if len(groups) != 4 {
		t.Fatalf("应 4 组（a-z / A-Z / 0-9 / 字面量），实得 %d", len(groups))
	}
	for i, want := range []int{26, 26, 10, 12} {
		if len(groups[i]) != want {
			t.Errorf("第 %d 组大小 = %d，想要 %d", i, len(groups[i]), want)
		}
	}
	wantRunes(t, groups[3], "!@#$%^&*-_=+")
}

func TestCompileGroupsBracketedAndHex(t *testing.T) {
	for _, tc := range []struct {
		pattern string
		want    []string
	}{
		{"[a-zA-Z0-9]", []string{"abcdefghijklmnopqrstuvwxyz", "ABCDEFGHIJKLMNOPQRSTUVWXYZ", "0123456789"}},
		{"0-9a-f", []string{"0123456789", "abcdef"}},
		{"A-F0-9", []string{"ABCDEF", "0123456789"}},
	} {
		groups := CompileGroups(tc.pattern)
		if len(groups) != len(tc.want) {
			t.Errorf("%q 应 %d 组，实得 %d：%q", tc.pattern, len(tc.want), len(groups), groups)
			continue
		}
		for i := range tc.want {
			wantRunes(t, groups[i], tc.want[i])
		}
	}
}

func TestCompileGroupsEscapedHyphenIsLiteral(t *testing.T) {
	groups := CompileGroups("a-z\\-")
	if len(groups) != 2 {
		t.Fatalf("应 2 组，实得 %d：%q", len(groups), groups)
	}
	wantRunes(t, groups[0], "abcdefghijklmnopqrstuvwxyz")
	wantRunes(t, groups[1], "-")
}

func TestCompileGroupsDedupesRepeatedLiterals(t *testing.T) {
	groups := CompileGroups("a-z!!")
	if len(groups) != 2 {
		t.Fatalf("应 2 组，实得 %d：%q", len(groups), groups)
	}
	wantRunes(t, groups[1], "!")
}

func TestCompileGroupsClassAtomsStayTogether(t *testing.T) {
	t.Run("digit+literal", func(t *testing.T) {
		groups := CompileGroups("\\d!")
		if len(groups) != 2 {
			t.Fatalf("应 2 组，实得 %d：%q", len(groups), groups)
		}
		wantRunes(t, groups[0], "0123456789")
		wantRunes(t, groups[1], "!")
	})
	t.Run("word is one group", func(t *testing.T) {
		groups := CompileGroups("\\w")
		if len(groups) != 1 || len(groups[0]) != 63 {
			t.Fatalf("\\w 应为一组 63 个字符，实得 %d 组：%q", len(groups), groups)
		}
	})
	t.Run("posix class is one group", func(t *testing.T) {
		groups := CompileGroups("[[:alpha:]]")
		if len(groups) != 1 || len(groups[0]) != 52 {
			t.Fatalf("[[:alpha:]] 应为一组 52 个字符，实得 %d 组：%q", len(groups), groups)
		}
	})
}

func TestCompileGroupsHexEscapedRange(t *testing.T) {
	groups := CompileGroups("\\x30-\\x39")
	if len(groups) != 1 {
		t.Fatalf("应 1 组，实得 %d：%q", len(groups), groups)
	}
	wantRunes(t, groups[0], "0123456789")
}

func TestCompileGroupsOnlyLiteralsIsOneGroup(t *testing.T) {
	groups := CompileGroups("!@#")
	if len(groups) != 1 {
		t.Fatalf("应 1 组，实得 %d：%q", len(groups), groups)
	}
	wantRunes(t, groups[0], "!@#")
}

func TestCompileGroupsNegatedFallsBack(t *testing.T) {
	for _, pattern := range []string{"[^a]", "^a"} {
		if groups := CompileGroups(pattern); groups != nil {
			t.Errorf("%q 应退回不分组，实得 %q", pattern, groups)
		}
	}
}

func TestCompileGroupsInvalidPatternFallsBack(t *testing.T) {
	if groups := CompileGroups("z-a"); groups != nil {
		t.Errorf("非法 pattern 应返回 nil，实得 %q", groups)
	}
}

func TestCompileGroupsAreSubsetsOfFullSet(t *testing.T) {
	for _, pattern := range []string{
		"a-zA-Z0-9!@#$%^&*\\-_=+",
		"0-9&a-z!",
		"[a-z0-9!]",
		"\\d\\w",
		"[[:alpha:]]",
	} {
		full, err := Compile(pattern)
		if err != nil {
			t.Fatalf("Compile(%q) 报错：%v", pattern, err)
		}
		inFull := make(map[rune]bool, len(full))
		for _, r := range full {
			inFull[r] = true
		}
		groups := CompileGroups(pattern)
		if len(groups) == 0 {
			t.Errorf("%q 应有分组", pattern)
			continue
		}
		union := make(map[rune]bool, len(full))
		for _, g := range groups {
			for _, r := range g {
				if !inFull[r] {
					t.Errorf("%q 的分组出现全集外字符 %q", pattern, r)
				}
				union[r] = true
			}
		}
		if len(union) != len(full) {
			t.Errorf("%q 的分组并集大小 = %d，全集大小 = %d", pattern, len(union), len(full))
		}
	}
}
