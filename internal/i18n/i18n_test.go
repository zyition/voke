package i18n

import "testing"

func TestLDefaultsToChinese(t *testing.T) {
	SetLanguage(Chinese)
	if got := L("中文", "English"); got != "中文" {
		t.Errorf("默认应为中文，实得 %q", got)
	}
	if Current() != Chinese {
		t.Errorf("Current() = %q，想要 zh", Current())
	}
}

func TestSetLanguageSwitchesAndReportsChange(t *testing.T) {
	t.Cleanup(func() { SetLanguage(Chinese) })
	SetLanguage(Chinese)
	if !SetLanguage(English) {
		t.Error("从中文切到英文应报告变化")
	}
	if got := L("中文", "English"); got != "English" {
		t.Errorf("英文模式应返回英文，实得 %q", got)
	}
	if SetLanguage(English) {
		t.Error("重复设置同一语言不应报告变化")
	}
	if !SetLanguage(Chinese) {
		t.Error("切回中文应报告变化")
	}
}

func TestLEmptyEnglishFallsBackToChinese(t *testing.T) {
	t.Cleanup(func() { SetLanguage(Chinese) })
	SetLanguage(English)
	if got := L("只有中文", ""); got != "只有中文" {
		t.Errorf("英文缺失时应回落中文，实得 %q", got)
	}
}
