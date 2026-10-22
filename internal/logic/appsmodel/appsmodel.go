// Package appsmodel 是应用条目模型 + 「只加载一次」的懒加载闸门
// （§7.2.1 三层铁律里的 logic 层）。
//
// 分层：本包**零 mygo、零 x/sys/windows、零 go-pinyin**，只依赖标准库，故
// `go test ./internal/logic/...` 在 Linux 原生可跑。真正会读磁盘的枚举
// （mygo Shell.InstalledApps → apps.go 的 loadAppsAsync）留在 platform 侧，
// 经本包的注入字段 [Loader] 接进来——本包只负责「该不该触发、触发几次」，
// 不知道枚举怎么发生、结果怎么回主线程。
package appsmodel

import (
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ProviderID 是 apps 的稳定插件 id，同时是频率表的键前缀（对照生产
// `record_use("apps:<name>")` / `frequency_key("apps", title)`）。放 logic 层是因为
// search（apps 排序）与 app（启动记录）两侧要用同一个键做「越用越靠前」，而 search
// 属 logic 不能反向 import app 层的 providerAppsID/appsProviderID。app 层那两个常量
// 的值与此一致（有测试钉住），键对不上会让启动记了频率却加不到排序上。
const ProviderID = "apps"

// AppEntry 是一条应用条目。契约结构（panel.go / view.go 依赖），勿改字段名。
//
// Windows 上字段语义（mygo internal/windows/apps.go:26-28 + modules.go:75-89）：
//   - Path 恒为 shell:AppsFolder\<ID> 形态的 shell 项，FileIcon 画它、
//     LaunchApp 启它；
//   - ID 是 AUMID；桌面应用没有 AUMID 时是已知文件夹下的路径。
type AppEntry struct {
	Name string // 显示名
	Path string // 启动路径（AppsFolder 条：shell:AppsFolder\<ID>；.lnk 条：.lnk 本体）
	ID   string // AUMID（桌面应用为已知文件夹路径；.lnk 条为空）
	// Target 仅 .lnk 条有效：解析出的原始启动目标（如 C:\Windows\regedit.exe）。
	// 只用于跨来源去重（同一程序的 .lnk 与 AppsFolder 两条只留一条），不参与匹配/启动。
	Target string
}

// Loader 是实际执行枚举的函数，由 platform 侧注入（apps.go 的 init 把
// loadAppsAsync 塞进来）。
//
// 取代原来的 `var appsEnumerate = loadAppsAsync` 直接函数引用：logic 不能引
// mygo，枚举体只能作字段注入。它仍是**可替换变量**而非硬编码调用——纯逻辑
// 测试里换成空实现，既避免测试去读磁盘（mygo 的 Shell 面在 Linux 上也有后端，
// 不隔离的话单测会真去枚举开发机的应用，结果随机器而变、不可复现），也让
// 「触发次数」成为可断言的返回值而非副作用。
var Loader func()

// loadMu 保护闸门状态（触发去重 + 唤醒重扫防抖）。
var loadMu sync.Mutex

// loaded：懒加载一次性闸门——EnsureLoaded 只在从未触发过时发起，避免每次按键
// 都重跑（连续按键由它 + inFlight 去重）。成功后保持 true（生产失败也不在此重试，
// 重扫交给 MaybeRefreshOnWake 的唤醒路径）。
var loaded bool

// inFlight：有一次 Loader 正在跑（首载或唤醒重扫）。防止上一轮没结束又起一轮，
// 也防止唤醒重扫并发。由 Loader 完成时调 MarkLoaded 清除。
var inFlight bool

// lastLoadAt：上一次**发起**扫描的时刻（唤醒重扫的 60s 防抖锚）。零值 = 从未扫过
// （纯未查询的唤醒不触发首扫，保持 §11.12 的懒加载）。
var lastLoadAt time.Time

// loadTrigger 是「真正发起过扫描」的计数，仅测试用（见 LoadTriggerCount）。
var loadTrigger atomic.Int32

// startLoadLocked 在持有 loadMu 时发起一次后台枚举：置 inFlight/lastLoadAt/计数并
// `go Loader()`。调用方负责各自的闸门判断（EnsureLoaded 看 loaded、唤醒重扫看防抖）。
func startLoadLocked(now time.Time) {
	inFlight = true
	lastLoadAt = now
	loadTrigger.Add(1)
	go Loader()
}

// EnsureLoaded 按需触发应用枚举：**仅在从未发起过首载时**生效（懒加载一次性闸门）。
//
// 这是「唤起懒加载」的唯一首载入口（PoC 文档 §11.12）：唤起本身不调，用户首次
// 产生非空查询时由 view.go 调。之后的重扫交给 MaybeRefreshOnWake（生产「唤起重扫 +
// 60s 防抖」的 Go 对照）。
func EnsureLoaded() {
	loadMu.Lock()
	defer loadMu.Unlock()
	if loaded || inFlight {
		return
	}
	loaded = true
	startLoadLocked(time.Now())
}

// MarkLoaded 由枚举完成回调调用（成功或失败都算一次「加载尝试完成」）：清 inFlight，
// 让后续唤醒重扫得以再起一轮。loaded 保持 true（首载闸门是一次性的）。
func MarkLoaded() {
	loadMu.Lock()
	inFlight = false
	loadMu.Unlock()
}

// MaybeRefreshOnWake 是「面板唤起时请求重扫 + debounce 防抖」——对照生产 main.rs
// 的 APPS_SCAN_DEBOUNCE 模型（启动即索引一次 + 唤起触发重扫 + 线程内防抖）。装/卸应用后，
// 用户下次唤起（距上次扫描 ≥ debounce）就能看到列表更新，无需重启启动器。
//
// 三道人挡：① 有扫描在跑（inFlight）不重扫；② 从未扫过（lastLoadAt 零值）不重扫——
// 保住 §11.12 的懒加载（纯唤醒、从不查询的会话不强行枚举）；③ 距上次发起 < debounce 不重扫。
// 真的发起时返回 true（调用方无需处理结果：Loader 完成后经 bindApps 回调换 Apps）。
func MaybeRefreshOnWake(now time.Time, debounce time.Duration) bool {
	loadMu.Lock()
	defer loadMu.Unlock()
	if inFlight || lastLoadAt.IsZero() || now.Sub(lastLoadAt) < debounce {
		return false
	}
	startLoadLocked(now)
	return true
}

// LoadTriggerCount 返回真正发起过的枚举次数（仅测试读，见 loadTrigger）。
func LoadTriggerCount() int { return int(loadTrigger.Load()) }

// ResetLoadGate 把闸门复位到「未触发」。
//
// 用途：单测每例开头擦干净（loaded/inFlight/lastLoadAt/计数全清），否则被上一例的
// 状态吞掉触发。生产不调。
func ResetLoadGate() {
	loadMu.Lock()
	loaded, inFlight, lastLoadAt = false, false, time.Time{}
	loadMu.Unlock()
	loadTrigger.Store(0)
}

// DedupeApps 去掉 (名称, 路径) 完全重复的条目（大小写不敏感，Windows 路径语义）。
//
// 对照生产 voke 的两条去重规则（voke-sys/src/apps.rs:592-604）：
//   - 规则 1（完全重复项去掉）原样保留——AppsFolder 内部理论上仍有重复项；
//   - 规则 2（AUMID 注册的 Win32 应用与某条 .lnk 同名时跳过）**不适用**：
//     mygo 的枚举是 AppsFolder 单一来源，不存在「.lnk 那条指向真 exe 更直接」
//     的对照物；同名不同 ID 的条目宁可保留，差异清单交 V3 真机比对解释。
func DedupeApps(apps []AppEntry) []AppEntry {
	seen := make(map[string]struct{}, len(apps))
	out := make([]AppEntry, 0, len(apps))
	for _, a := range apps {
		key := strings.ToLower(a.Name) + "\x00" + strings.ToLower(a.Path)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, a)
	}
	return out
}
