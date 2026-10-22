// Package frequency 实现「越用越准」的使用频率学习与排序加成——1:1 对照生产
// Rust 侧 `voke-core/src/engine.rs` 的 `frequency_boost` / `record_use` /
// `frequency_key` / `FrequencyStore`。
//
// 分层（§7.2.1）：纯逻辑，零 mygo、零 IO、Linux 原生可测。落盘（TOML/JSON 读写）
// 属 app/config 层，不在本包——本包只管「次数→加成」的算术与「记一次」的状态。
// 与生产同纪律：core 不碰 IO，序列化外包给调用方。
package frequency

import "math"

// Key 是频率表条目的稳定标识：provider id + 结果标题，足以区分不同来源的同名
// 条目。逐字对照 Rust `frequency_key`：`format!("{provider_id}:{}", m.title)`。
func Key(providerID, title string) string {
	return providerID + ":" + title
}

// Boost 把累计使用次数换算成排序乘数：0 次 = 1.0，其后按对数增长
// （`1.0 + ln(n+1) × 0.5`），使高频条目不会把匹配分拉爆。逐式对照 Rust
// `frequency_boost`（生产为 f32，此处随 Go 侧 config 统一用 float64）。
func Boost(useCount uint32) float64 {
	if useCount == 0 {
		return 1.0
	}
	return 1.0 + math.Log(float64(useCount)+1.0)*0.5
}

// Store 是「条目 key → 累计使用次数」的频率表，对照 Rust
// `FrequencyStore = HashMap<String, u32>`。
type Store map[string]uint32

// New 返回一个空频率表。
func New() Store { return Store{} }

// Record 给 key 记一次使用（+1），对照 Rust `QueryEngine::record_use`（选中执行后调用）。
func (s Store) Record(key string) { s[key]++ }

// Count 返回 key 的累计使用次数（缺省 0）。
func (s Store) Count(key string) uint32 { return s[key] }

// BoostOf 取 key 当前次数对应的排序加成（未记录过 → Boost(0) = 1.0）。
// 排序侧最终分 = provider.weight × match_score × BoostOf(key)（对照 Rust `rank`）。
func (s Store) BoostOf(key string) float64 { return Boost(s[key]) }
