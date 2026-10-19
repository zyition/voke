// shell.go：§7.2 步骤6b 从 `package main` 的 apps.go 抽出的 **mygo Shell /
// Screen 调用件**——应用枚举、启动、图标取字节与取像素尺寸。抽完之后 main 侧
// 不再直接写 `mygo.Shell.*` / `mygo.Screen.Displays()`，只作调用方；真正落到
// 平台的那一截只住本文件（§7.2.1「platform 是唯一允许 import mygo 的层」）。
//
// 与 logic 层的分工（本层不掺编排，也不改任何算术/去重/闸门）：
//   - 条目模型 / 去重 / 懒加载闸门 → internal/logic/appsmodel（本文件只借它的
//     AppEntry 类型当 LaunchApp 的入参，闸门与去重一概不碰）；
//   - 图标的 map + FIFO 淘汰记账 → internal/logic/iconcache（本文件只提供
//     「向 FileIcon 现取」这一个后端）；
//   - 枚举之后「回主线程刷新」的接缝（Window.Update + appsWin/appsApply）与
//     界面装配（bindApps）持 *mygo.Window、靠 App 生命周期回调注入，**与本层
//     调用无耦合但和 App 装配强耦合，留步骤8**——apps.go 里那三样原样不动。
//
// 构建标签：本文件**不加**（照 apps.go 原样）。mygo 的 Shell / Screen 面每个
// 平台都有后端（internal/{windows,linux,darwin}/ 的 apps.go、icon.go、system.go），
// Linux 原生 `go build ./...` / `go test ./...` 保持绿；运行时行为仍只面向
// Windows（PoC 不上 Linux，V8 只验证交叉构建）。
//
// 线程模型逐函数标注（§7.1.3「platform 层每个平台调用都要标一句主线程 / 可后台」；
// 行号按 mygo v0.5.5 模块缓存源码核实）：
//
//	InstalledApps()      **可后台**（mygo 里唯一后台安全的平台调用）  modules.go:103-120 + internal/windows/apps.go:29-35
//	LaunchApp(app)       **只许主线程**（onMainValue 投递后阻塞等值）  modules.go:124-130 → loop.go:94-108/:110
//	AppIcon(path)        **只许主线程**（未命中走 Shell.FileIcon）     modules.go:62-73 → loop.go:110
//	iconPixelSize()      **只许主线程**（Screen.Displays）            modules.go:223-241 → loop.go:110
//
// ⚠️ 后三者所在的 onMainValue 路径在**没有主循环**的进程里会永久阻塞（loop.go:94-108
// 投递完就 `<-done`，无超时），而 needsApp（app.go:128-131）在主线程且 App 未
// 初始化时直接 panic。故本层的 headless 单测只断言「不碰运行时也能算」的两件
// （AppIcon 的 nil 归一、iconPixelSizeFor 的算术），真机行为交 §6 的 V4。
package platform

import (
	"sync"

	"github.com/egoist/mygo"
	"github.com/zyition/voke/poc/voke-go/internal/logic/appsmodel"
	"github.com/zyition/voke/poc/voke-go/internal/logic/iconcache"
)

const (
	// iconDIPSize 是列表行图标的 DIP 尺寸（与 view.go 的 iconSize 同值，
	// 两处必须成体系看）。取 32 的依据：
	// 生产 voke 的落盘图标缓存统一是 32×32 PNG（voke-sys/src/icon.rs 开头
	// 模块文档），同尺寸便于 V4 与现网图标逐一对照；实际向 FileIcon 请求的
	// 像素尺寸 = DIP × 最大显示器缩放（见 iconPixelSize）。
	iconDIPSize = 24
	// iconCacheMax 是图标进程内缓存的条目上限。生产 voke 现网落盘缓存
	// 322 个 PNG，PoC 应用量同级；512 留一倍余量，超出按 FIFO 淘汰，
	// 保证列表滚动时缓存有界（防 Immediate-mode 每帧查图把内存推高）。
	iconCacheMax = 512
)

// InstalledApps 枚举机器上已安装的应用（Windows 侧就是开始菜单的「所有应用」：
// shell:AppsFolder 的 COM，桌面快捷方式与 packaged 应用都在内，§7.1.2 O5 因此
// 砍掉了生产 Rust 侧的 .lnk 解析 + 双来源合并）。
//
// **可后台**——这是 mygo 里唯一后台安全的平台调用：非主线程调用时它自己
// `App.waitReady()`（modules.go:105-107），后端跑在自建的 COM 线程套间里
// （internal/windows/apps.go:29-35：`runtime.LockOSThread` +
// `CoInitializeEx(…, APTDATETHREADED)`），读磁盘几十到几百毫秒但**不占主线程**
// （modules.go:101-102 原文要求「call it from a goroutine of its own」）。
// 结果回主线程只经 Window.Update，那是调用方（apps.go 的 loadAppsAsync）的事。
//
// 返回原样的 []mygo.InstalledApp：映射成 AppEntry、去重、耗时打点都留在调用方，
// 与本层搬走之前的写法逐字一致（apps.go 里那段循环一个字段都没改）。
func InstalledApps() ([]mygo.InstalledApp, error) {
	return mygo.Shell.InstalledApps()
}

// LaunchApp 启动一个应用，已运行则把它唤到前台（mygo Shell.LaunchApp →
// OpenApplication，内部是 ShellExecute 默认谓词，internal/windows/apps.go:73）。
//
// **只许主线程**：modules.go:129 走 onMainValue（loop.go:110 → :94-108）——投递
// 给主循环并**阻塞等返回值、无超时**。调用点是 view.go 的 execLaunch（Enter 启动
// 选中项），本来就在主线程（键盘回调 / Window.Update 回调内）。
//
// 入参映射与原 apps.go 的 launchApp 逐字相同；空 Path 不在本层预检——mygo 自己
// 返回「the app has no path」错误（modules.go:126-128），调用方据此保留面板
// （启动失败不清空、不隐藏，E-1 行为在 execLaunch 里守）。
func LaunchApp(app appsmodel.AppEntry) error {
	return mygo.Shell.LaunchApp(mygo.InstalledApp{Name: app.Name, Path: app.Path, ID: app.ID})
}

// iconFetch 是「向 mygo Shell.FileIcon 现取一张图标」的后端。它是个**可替换
// 变量**而不是常量（同款先例：appsmodel.Loader）：生产路径就是这一个真实现，
// headless 单测把它换成假件才能断言 AppIcon 的 nil 归一（§7.1.2 O4-b）——
// 不换成假件就得跑真 FileIcon，而它走 onMainValue，无主循环时永久阻塞。
//
// **只许主线程**（modules.go:62-73 → onMainValue）。
var iconFetch = func(path string) ([]byte, error) {
	return mygo.Shell.FileIcon(path, iconPixelSize())
}

// appIconCache 是进程内唯一的图标缓存（PoC 不落盘，隔离表见 PoC 文档 §3）。
// 淘汰与并发记账在 logic 层的 iconcache，这里只注入取图后端。
//
// 注意后端是**经一层闭包**调 iconFetch 的：iconcache.New 存的是构造时求值到的
// 那个函数值，直接把 iconFetch 当参数传进去就把引用焊死了，测试替换不到。
var appIconCache = iconcache.New(iconCacheMax, func(path string) ([]byte, error) {
	return iconFetch(path)
})

// AppIcon 取应用图标的 PNG 字节：先查进程内有界缓存，未命中经 [iconFetch]
// （Shell.FileIcon）现取（单次数毫秒级，V4 观测项）。
//
// **只许主线程**，且**只许「行首次可见才取」**（§7.1.2 O4 的硬约束）：FileIcon
// 走 onMainValue，批量预取会串行阻塞主线程且无超时，所以调用点是 view.go 的
// appState.icon（渲染可见行时按 Path 懒取 + 双缓存）。
//
// 返回 nil 一律表示「没有图标」，调用方据此降级为首字母占位（官方 launcher
// 示例同款，examples/launcher/main.go:130-132）。
//
// **这条 nil 判断不可省**（§7.1.2 O4-b）：mygo 在事件循环关停后的跨线程调用会
// **静默丢弃、返回零值**——post 见 loop.go:19-27 返回 false，onMain 见
// loop.go:94-108 直接 return，**不设默认值**——于是 FileIcon 可能给出
// `(nil, nil)` 这种「假成功」，返回值本身零长却没带任何错误。缓存存的是后端原样
// 返回的字节，接缝这里不归一，「无图标」就有 nil 与零长切片两种表示：当前调用方
// 靠 `len(data) > 0` 侥幸挡住，将来谁把返回值直接递给 ui.DecodeBitmap 就会拿
// 零长切片当图片。故在平台边界统一成 nil 一种。
func AppIcon(path string) []byte {
	png := appIconCache.Get(path)
	if len(png) == 0 {
		return nil
	}
	return png
}

// iconPixelSize 返回向 FileIcon 请求的像素尺寸：DIP × 最大显示器缩放。
// mygo 文档要求「size in DIPs times the display's scale factor for a sharp
// icon」（modules.go:55-58），launcher 示例同款取全场最大缩放
// （examples/launcher/main.go:177-181）。只解析一次：显示器热插拔不重算
// （PoC 可接受；真要做可监听 Screen.OnChanged，当前无此需求）。
//
// **只许主线程**：Screen.Displays 走 onMainValue（modules.go:223-241）。
var iconPixelSize = sync.OnceValue(func() int {
	return iconPixelSizeFor(mygo.Screen.Displays())
})

// iconPixelSizeFor 是上面那段的纯算术部分（全场最大缩放 → DIP × 缩放 → 下限 1）。
// 拆出来的唯一理由是 headless 可断言：Displays() 要主循环，而这一步只要一组
// mygo.Display 值（纯数据，modules.go:201-212，不碰运行时）。语义与 apps.go
// 原实现逐字一致——缩放初值 1.0，故 0 / 负缩放不参与取大；乘算向下取整后不足 1
// 兜到 1（FileIcon 对 size ≤ 0 直接报错，modules.go:64-66）。
func iconPixelSizeFor(displays []mygo.Display) int {
	scale := 1.0
	for _, d := range displays {
		if d.ScaleFactor > scale {
			scale = d.ScaleFactor
		}
	}
	n := int(float64(iconDIPSize) * scale)
	if n < 1 {
		n = 1
	}
	return n
}
