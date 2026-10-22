// search_key.rs 测试的 1:1 移植（Rust 8 个用例）。
package match

import (
	"fmt"
	"testing"
)

func TestDetectsNonASCII(t *testing.T) {
	if !HasNonASCII("微信") {
		t.Error(`HasNonASCII("微信") 应为 true`)
	}
	if HasNonASCII("weixin") {
		t.Error(`HasNonASCII("weixin") 应为 false`)
	}
}

func TestBuildsPinyinAndInitials(t *testing.T) {
	if got := ToPinyin("微信"); got != "weixin" {
		t.Errorf(`ToPinyin("微信") = %q, 期望 "weixin"`, got)
	}
	if got := ToInitials("微信"); got != "wx" {
		t.Errorf(`ToInitials("微信") = %q, 期望 "wx"`, got)
	}
}

func TestBuildsKeysForChineseName(t *testing.T) {
	keys := NewSearchKeys("微信")
	if keys.Name != "微信" {
		t.Errorf("Name = %q, 期望 %q", keys.Name, "微信")
	}
	if !containsString(keys.Full, "weixin") {
		t.Errorf("Full = %v, 应包含 weixin", keys.Full)
	}
	if !containsString(keys.Initials, "wx") {
		t.Errorf("Initials = %v, 应包含 wx", keys.Initials)
	}
}

// 「音乐」的「乐」有 le/yue 两读，两个读音都应生成全拼键。
func TestExpandsHeteronym(t *testing.T) {
	keys := NewSearchKeys("音乐")
	if !containsString(keys.Full, "yinyue") {
		t.Errorf("Full = %v, 应包含 yinyue", keys.Full)
	}
	if !containsString(keys.Full, "yinle") {
		t.Errorf("Full = %v, 应包含 yinle", keys.Full)
	}
}

func TestHandlesASCIIName(t *testing.T) {
	keys := NewSearchKeys("chrome")
	if keys.Name != "chrome" {
		t.Errorf("Name = %q, 期望 chrome", keys.Name)
	}
	// 纯 ASCII 条目靠原名匹配，不额外造拼音键。
	if len(keys.Full) != 0 {
		t.Errorf("Full = %v, 期望为空", keys.Full)
	}
	if len(keys.Initials) != 0 {
		t.Errorf("Initials = %v, 期望为空", keys.Initials)
	}
}

// 每个字都多音的极端输入不应让键数量爆炸。
func TestVariantCountIsBounded(t *testing.T) {
	keys := NewSearchKeys("重重重重重重重重重")
	if len(keys.Full) > variantLimit {
		t.Errorf("len(Full) = %d, 应 <= %d", len(keys.Full), variantLimit)
	}
}

// ── SearchKeysCache ──

func TestCacheHitsSameInputWithoutRebuilding(t *testing.T) {
	cache := NewSearchKeysCache()
	first := cache.Get("微信")
	second := cache.Get("微信")
	// 同一个指针 = 第二次没重算（重算会造新对象）。
	if first != second {
		t.Error("同名两次 Get 应返回同一实例")
	}
	if first.Name != "微信" {
		t.Errorf("Name = %q, 期望 微信", first.Name)
	}
	if cache.Len() != 1 {
		t.Errorf("Len = %d, 同名只占一条应为 1", cache.Len())
	}
	// 不同名字各自一条，内容不受缓存影响。
	other := cache.Get("chrome")
	if other.Name != "chrome" {
		t.Errorf("Name = %q, 期望 chrome", other.Name)
	}
	if first == other {
		t.Error("不同名字不应返回同一实例")
	}
	if cache.Len() != 2 {
		t.Errorf("Len = %d, 期望 2", cache.Len())
	}
}

// 窗口标题会变、可能含 URL —— 缓存必须有界，不能跟着标题无限长。
func TestCacheIsBoundedNotUnbounded(t *testing.T) {
	cache := NewSearchKeysCache()
	for i := 0; i < 5000; i++ {
		// 每条都是不同的 URL / 标题，真实场景里就是标签页换来换去。
		_ = cache.Get(fmt.Sprintf("https://example.com/tab/%d", i))
	}
	if cache.Len() > CacheCapacity {
		t.Errorf("Len = %d, 缓存必须 <= 容量上限 %d", cache.Len(), CacheCapacity)
	}
	// 清空后仍然可用（不能因为淘汰就永久失效）。
	keys := cache.Get("微信")
	if keys.Name != "微信" {
		t.Errorf("Name = %q, 期望 微信", keys.Name)
	}
}
