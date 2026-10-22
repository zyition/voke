// platform 层 Shell / Screen 调用件（shell.go）的 headless 测试。
//
// 只测**不碰 mygo 运行时也能断言**的两件：
//   - AppIcon 的 nil 归一（§7.1.2 O4-b 的「假成功」）——把取图后端 [iconFetch]
//     换成假件，测的是生产接缝本身（view.go 调的就是它），不是复刻一份逻辑；
//   - iconPixelSizeFor 的像素算术（DIP × 全场最大缩放，下限 1）。
//
// 为什么不给 InstalledApps / LaunchApp / 真 FileIcon 写断言：三者都落到 mygo 的
// onMainValue（loop.go:94-108/:110），没有主循环时投递完就 `<-done` 死等；而
// needsApp（app.go:128-131）在主线程且 App 未初始化时直接 panic。要在单测里跑
// 它们等于把整个 GUI 拉起来，PoC 不造这种假测——真机行为交 §6 的 V4（图标）与
// V2/V7（启动）。它们在 Linux 原生能编译过、能被 vet 查，是本层的 headless 上限。
package platform

import (
	"errors"
	"testing"

	"github.com/egoist/mygo"
	"voke/internal/logic/iconcache"
)

// fakeIconFetch 把取图后端换成假件**并重置全局图标缓存**，用例结束双双复原。
// 生产路径永远走真 FileIcon。
//
// 必须重置 appIconCache：它是包级单例、跨用例存活。光靠「每条用例用自己的
// path」只挡得住同一次运行内互不串；但 `go test -count=2`（或 CI 重跑）会用**相同
// 的 path 再跑一遍**，run-1 的缓存条目还在 → 后端调 0 次 → 「只取一次」断言假红。
// 换后端时一并给一个冷缓存，才让每条数调用的断言与执行次数/顺序无关、可重复。
func fakeIconFetch(t *testing.T, fn func(path string) ([]byte, error)) {
	t.Helper()
	prev := iconFetch
	prevCache := appIconCache
	iconFetch = fn
	appIconCache = iconcache.New(iconCacheMax, func(path string) ([]byte, error) { return iconFetch(path) })
	t.Cleanup(func() {
		iconFetch = prev
		appIconCache = prevCache
	})
}

// TestAppIconNormalizesFalseSuccess 守 §7.1.2 O4-b：mygo 在事件循环关停后跨线程
// 调用**静默丢弃、返回零值**（loop.go:19-27 的 post 返回 false、:94-108 的 onMain
// 直接 return，不设默认值），Shell.FileIcon 于是可能给出「假成功」——零长字节 +
// nil 错误。平台接缝必须把它归一成「没有图标」（nil），不能把零长切片当图片递出去。
//
// 两个子例覆盖假成功的两种形态（nil 切片 / 非 nil 的空切片）。断言的是
// **got == nil** 而不是 len(got) == 0：后者删掉归一化也不会红，等于空转断言。
func TestAppIconNormalizesFalseSuccess(t *testing.T) {
	for _, tc := range []struct {
		name string
		png  []byte
	}{
		{"FileIcon 返回 nil,nil", nil},
		{"FileIcon 返回零长非nil切片,nil", []byte{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeIconFetch(t, func(string) ([]byte, error) { return tc.png, nil })
			if got := AppIcon(`shell:AppsFolder\FakeSuccess.` + tc.name); got != nil {
				t.Fatalf("假成功要归一成 nil（无图标），实得 %#v（len=%d）", got, len(got))
			}
		})
	}
}

// TestAppIconFalseSuccessStaysCached 锁定「假成功按无图标缓存、不每次重取」这条
// **搬迁前就存在**的行为：iconcache 只在 fetch 返回 error 时不落缓存（见
// internal/logic/iconcache 的 TestIconCacheFetchFailureNotCached），而假成功没有
// error，所以照样进缓存、第二次命中不再问后端。
//
// 为什么要单独钉这颗钉子：把它「修」成失败即重取看着更严谨，实际是让渲染每帧都
// 打一次 FileIcon——那是 onMainValue（主线程往返、无超时），正是 §7.1.2 O4 明令
// 禁止的取图姿势。真要改，得连文档与取图纪律一起改，不许在搬迁这一步顺手改。
func TestAppIconFalseSuccessStaysCached(t *testing.T) {
	calls := 0
	fakeIconFetch(t, func(string) ([]byte, error) {
		calls++
		return nil, nil
	})
	path := `shell:AppsFolder\FakeSuccess.Cached`
	for i := 0; i < 3; i++ {
		if got := AppIcon(path); got != nil {
			t.Fatalf("第 %d 次取图应返回 nil，实得 %#v", i+1, got)
		}
	}
	if calls != 1 {
		t.Fatalf("假成功应只向后端取一次（其余命中缓存），实得 %d 次", calls)
	}
}

// TestAppIconPassesBytesThrough 是「生产路径没接错」的那条硬断言：后端给出真实
// 字节时 AppIcon 原样返回、并落进缓存（第二次不再问后端）。搬迁如果把 AppIcon
// 接到别的缓存/别的后端上，这里会红。
func TestAppIconPassesBytesThrough(t *testing.T) {
	calls := 0
	want := []byte("PNG-BYTES")
	fakeIconFetch(t, func(string) ([]byte, error) {
		calls++
		return want, nil
	})
	path := `shell:AppsFolder\Real.Hit`
	for i := 0; i < 2; i++ {
		if got := AppIcon(path); string(got) != string(want) {
			t.Fatalf("第 %d 次 AppIcon(%q) = %q，想要 %q", i+1, path, got, want)
		}
	}
	if calls != 1 {
		t.Fatalf("两次取图应只向后端要一次（第二次命中缓存），实得 %d 次", calls)
	}
}

// TestAppIconFetchErrorDegradesToNil 锁错误分支：后端报错时 AppIcon 返回 nil
// （调用方降级首字母占位），错误本身由 iconcache 记日志，不打到返回值里。
func TestAppIconFetchErrorDegradesToNil(t *testing.T) {
	fakeIconFetch(t, func(string) ([]byte, error) {
		return nil, errors.New("假后端报错")
	})
	if got := AppIcon(`shell:AppsFolder\Fake.Error`); got != nil {
		t.Fatalf("取图失败应返回 nil，实得 %#v", got)
	}
}

// displaysOf 用纯数据拼一组显示器（mygo.Display 是普通结构体，modules.go:201-212，
// 不经 Screen.Displays 就不碰主循环）。
func displaysOf(scales ...float64) []mygo.Display {
	out := make([]mygo.Display, 0, len(scales))
	for _, s := range scales {
		out = append(out, mygo.Display{ScaleFactor: s})
	}
	return out
}

// TestIconPixelSizeForPicksMaxScale 锁取像素尺寸的两步算术：全场**最大**缩放
// （不是首个、也不是平均）、DIP × 缩放向下取整。期望值按 24 DIP 独立算出
// （24 / 36 / 48 / 60），不引被测常量当阈值。
//
// 「低于 1.0 的缩放不参与取大」这条同时说明实现里 `n < 1` 那个下限为什么测不到：
// 缩放初值就是 1.0，DIP 固定 24，结果不可能小于 24——它是**防御性**兜底
// （防的是将来把 DIP 调成 0 / 负数后向 FileIcon 递一个非法 size，
// modules.go:64-66 会直接报错），不是本场景的真实分支，硬造用例就成了假测。
func TestIconPixelSizeForPicksMaxScale(t *testing.T) {
	for _, tc := range []struct {
		name     string
		displays []mygo.Display
		want     int
	}{
		{"没有显示器信息按 100%", nil, 24},
		{"单屏 100%", displaysOf(1), 24},
		{"三屏取最大 250%", displaysOf(1, 2.5, 1.75), 60},
		{"低于 1.0 的缩放不参与取大", displaysOf(0.5), 24},
		{"零/负缩放不污染最大值", displaysOf(2, 0, -1), 48},
		{"缩放相同取谁都一样", displaysOf(1.5, 1.5), 36},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := iconPixelSizeFor(tc.displays); got != tc.want {
				t.Fatalf("iconPixelSizeFor = %d，想要 %d", got, tc.want)
			}
		})
	}
}
