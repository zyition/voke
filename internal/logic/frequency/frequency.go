// Package frequency 实现「越用越准」的使用频率学习与排序加成。
//
// 分层（§7.2.1）：纯逻辑，零 mygo、零 IO，Linux 原生可测。落盘（TOML 读写）属
// app 层，不在本包——本包只管「次数→加成」的算术、记录资格判定与「记一次」的状态。
//
// 加成的量纲刻意做了约束：**有界**。它与匹配质量一起进 rank.Final 的加法式，
// 上限 0.12 必须小于「两档档位差之和」，否则使用习惯可以把不相干的条目顶到精确
// 命中之前（旧实现是无界乘数 1+0.5·ln(n+1)，1000 次 = ×4.45）。
package frequency

import "math"

// Key 是频率表条目的稳定标识：provider id + 结果标题，足以区分不同来源的同名
// 条目。逐字对照 Rust 的 frequency_key："{provider_id}:{title}"。
func Key(providerID, title string) string {
	return providerID + ":" + title
}

// 使用加成的形状：UsageBonus(n) = min(UsageCap, UsageScale·ln(1+n))。
//
// 取值依据（docs/notes/ranking.md §1）：相邻档位差 0.10，上限 0.12 只够顶掉**相邻
// 一档**，两档差 0.20 永远够不着。曲线让前几次使用就有感知、随后迅速平缓：
//
//	1 次 0.017 · 5 次 0.045 · 31 次 0.086 · 100 次 0.115 · ≥147 次封顶 0.12
const (
	// UsageCap 是使用加成上限。
	UsageCap = 0.12
	// UsageScale 是对数曲线的斜率。
	UsageScale = 0.025
)

// UsageBonus 把累计使用次数换算成排序加分（0 次 = 0）。单调不减、有界。
func UsageBonus(useCount uint32) float64 {
	if useCount == 0 {
		return 0
	}
	return math.Min(UsageCap, UsageScale*math.Log(float64(useCount)+1))
}

// Store 是「条目 key → 累计使用次数」的频率表，对照 Rust 的 FrequencyStore。
type Store map[string]uint32

// New 返回一个空频率表。
func New() Store { return Store{} }

// Record 给 key 记一次使用（+1），对照生产 record_use（选中执行后调用）。
// 调用方必须先过 Recordable（见 internal/app 的记录点）。
func (s Store) Record(key string) { s[key]++ }

// Count 返回 key 的累计使用次数（缺省 0）。
func (s Store) Count(key string) uint32 { return s[key] }

// UsageOf 取 key 当前次数对应的排序加成（未记录过 → 0）。
func (s Store) UsageOf(key string) float64 { return UsageBonus(s[key]) }

// recordableProviders 是**允许**累计使用习惯的 provider 白名单（默认拒绝）。
//
// 只收「查找类、且结果身份稳定」的 provider：apps（应用名）、workspaces（工作区名）、
// terminal（终端 profile 名）。刻意不收的：
//   - awake / uuid / random / calculator / base64 / timestamp：结果是**生成值**或一次性
//     命令，没有「同一个条目」可言，记了只会把表撑脏（真实样本里出现过
//     random:8USAgNEtFt、uuid:5cf3be2c-…、awake:保持唤醒中 · 屏幕打开（还剩 1 小时 12 分钟））；
//   - windows：键是窗口标题（… - Google Chrome、终端里的分支名），每次都在变，
//     累计不到一起，还会挤占真正的查找类条目；
//   - clipboard：历史条目本身就是临时的。
//
// 未知 id（未来插件）默认拒绝——这张表影响排序，默认安全优先。
var recordableProviders = map[string]bool{
	"apps":       true,
	"workspaces": true,
	"terminal":   true,
}

// Recordable 报告某 provider 的结果是否应记入使用习惯表。
func Recordable(providerID string) bool { return recordableProviders[providerID] }

// ProviderOf 从频率键（"provider:title"）里取 provider id。
func ProviderOf(key string) string {
	for i := 0; i < len(key); i++ {
		if key[i] == ':' {
			return key[:i]
		}
	}
	return key
}

// Prune 删掉不该继续累计的历史条目（旧版本记下的 awake/uuid/random/windows 等），
// 返回剩余表与删除条数。就地修改并返回同一个 map，便于调用方决定是否落盘。
func Prune(store Store) (Store, int) {
	dropped := 0
	for k := range store {
		if !Recordable(ProviderOf(k)) {
			delete(store, k)
			dropped++
		}
	}
	return store, dropped
}
