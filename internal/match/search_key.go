// 搜索键预处理，1:1 对应 voke-core/src/search_key.rs。
//
// 索引时对每个条目预生成搜索键，查询时零汉字转换开销：
//
//	"微信"       → name="微信", full=["weixin","weishen"], initials=["wx","ws"]
//	"网易云音乐" → name="网易云音乐", full=["wangyiyunyinle","wangyiyunyinyue"], initials=["wyyyl","wyyyy"]
//
// 词首缩写（"Visual Studio Code" → "vsc"）**不建键**：它是纯字符串规则，由
// quality.go 的 acronymMatch 在查询期判定，既支持 "vsc" 也支持续写 "vscode"。
//
// 查询时：
//   - query 为纯 ASCII → 对所有 keys 跑 nucleo 模糊匹配（首字母串天然获得全等或前缀档）
//   - query 含汉字 → 只对原名匹配
//
// 索引期一次生成的前提是「条目稳定」；条目每次查询都重新读的 provider
// （窗口 / 工作区 / 终端）要走 SearchKeysCache 复用，否则每次按键都重算。
package match

import (
	"strings"
	"sync"
)

// HasNonASCII 对应 search_key.rs 的 has_non_ascii：
// 是否含有非 ASCII 字符（用于判断 query 走汉字匹配还是拼音匹配路径）。
func HasNonASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return true
		}
	}
	return false
}

// ToPinyin 对应 search_key.rs 的 to_pinyin：
// 把一串汉字转成不带声调的全拼，例如 "微信" → "weixin"。
// 没有拼音数据的字符（标点、拉丁字母）被跳过。
func ToPinyin(input string) string {
	var b []rune
	for _, c := range input {
		if p, ok := firstReading(c); ok {
			b = append(b, []rune(p)...)
		}
	}
	return string(b)
}

// ToInitials 对应 search_key.rs 的 to_initials：
// 取每个汉字拼音的首字母，例如 "微信" → "wx"。
// 逐字取首字符；没有拼音数据的字符原样保留，
// 这样 "Zed 编辑器" 的首字母串是 "Zbjzq" 而不是丢掉开头。
func ToInitials(input string) string {
	var b []rune
	for _, c := range input {
		if p, ok := firstReading(c); ok {
			b = append(b, []rune(p)[0])
		} else {
			b = append(b, c)
		}
	}
	return string(b)
}

// SearchKeys 对应 search_key.rs 的 SearchKeys：索引期一次生成，查询期只读。
type SearchKeys struct {
	// Name 条目原名，汉字 query 只对它匹配。
	Name string
	// Full 全拼变体（含多音字展开），ASCII query 匹配。
	Full []string
	// Initials 首字母串变体，ASCII query 匹配（"wx" 命中"微信"）。
	Initials []string
	// refs 是查询期真正遍历的键（原名 + 全拼 + 首字母），索引期算好小写形态，
	// 让档位判定在每次按键时零分配（键不变，小写也不变）。
	refs []keyRef
}

// keyRef 是一条搜索键及其小写形态。小写形态只服务于档位判定：
// 全等 / 前缀 / 子串都能在已小写的串上做，不必每个候选再 ToLower 一次。
type keyRef struct {
	text  string
	lower string
}

// variantLimit 对应 SearchKeys::VARIANT_LIMIT：多音字展开组数上限，
// 超出后每个字只取第一个读音。
const variantLimit = 8

// pinyinVariant 是 pinyin_variants 里的 (全拼, 首字母) 对。
type pinyinVariant struct {
	full    string
	initial string
}

// pinyinVariants 对应 search_key.rs 的 pinyin_variants：
// 为每个字的所有读音生成一组全拼与首字母串（笛卡儿展开），
// 组数超 limit 时退化为只取每个字的第一个读音。
func pinyinVariants(input string, limit int) (full, initials []string) {
	// 每个字的候选读音；非汉字或没有拼音数据的位置沿用原字符。
	perChar := make([][]pinyinVariant, 0, len(input))
	for _, c := range input {
		var choices []pinyinVariant
		if rs := readings(c); len(rs) > 0 {
			choices = make([]pinyinVariant, 0, len(rs))
			for _, p := range rs {
				// plain 恒非空；Rust 的 unwrap_or(c) 分支防御性保留。
				initial := string(c)
				if p != "" {
					initial = string([]rune(p)[0])
				}
				choices = append(choices, pinyinVariant{full: p, initial: initial})
			}
		}
		if len(choices) == 0 {
			// 非汉字或没有拼音数据：原样保留该字符，首字母取自身。
			choices = []pinyinVariant{{full: string(c), initial: string(c)}}
		}
		perChar = append(perChar, choices)
	}

	out := []pinyinVariant{{full: "", initial: ""}}
	for _, choices := range perChar {
		if len(out)*len(choices) > limit {
			// 组数太多时退化为只取每个字的第一个读音，避免索引膨胀。
			first := choices[0]
			degraded := make([]pinyinVariant, len(out))
			for i, v := range out {
				degraded[i] = pinyinVariant{
					full:    v.full + first.full,
					initial: v.initial + first.initial,
				}
			}
			out = degraded
			continue
		}
		next := make([]pinyinVariant, 0, len(out)*len(choices))
		for _, v := range out {
			for _, ch := range choices {
				next = append(next, pinyinVariant{
					full:    v.full + ch.full,
					initial: v.initial + ch.initial,
				})
			}
		}
		out = next
	}
	for _, v := range out {
		full = append(full, v.full)
		initials = append(initials, v.initial)
	}
	return full, initials
}

// NewSearchKeys 对应 SearchKeys::from_name。
func NewSearchKeys(name string) *SearchKeys {
	fullVariants, initialVariants := pinyinVariants(name, variantLimit)
	keys := &SearchKeys{Name: name}
	// 非汉字条目（如 "chrome"）的「全拼」就是它本身，不算额外的键。
	for _, v := range fullVariants {
		if v != "" && v != name {
			keys.Full = append(keys.Full, v)
		}
	}
	for _, v := range initialVariants {
		if v != "" && v != name && !containsString(keys.Full, v) {
			keys.Initials = append(keys.Initials, v)
		}
	}
	keys.refs = make([]keyRef, 0, 1+len(keys.Full)+len(keys.Initials))
	for _, s := range keys.ASCIIKeys() {
		keys.refs = append(keys.refs, keyRef{text: s, lower: strings.ToLower(s)})
	}
	return keys
}

// ASCIIKeys 对应 SearchKeys::ascii_keys：原名 + 全拼 + 首字母串。
// 原名必须包含在内：纯 ASCII 条目（"Visual Studio Code"）没有拼音变体，
// 靠原名匹配 "vscode"；词首缩写不在这里建键（见 quality.go 的 acronymMatch）。
//
// 查询期遍历的是 refs（带小写形态，索引期算好）；本方法只给测试 / 诊断用。
func (k *SearchKeys) ASCIIKeys() []string {
	keys := make([]string, 0, 1+len(k.Full)+len(k.Initials))
	keys = append(keys, k.Name)
	keys = append(keys, k.Full...)
	keys = append(keys, k.Initials...)
	return keys
}

func containsString(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// SearchKeysCache 对应 search_key.rs 的 SearchKeysCache：
// 键 = 源字符串，值 = 建好的搜索键的有界记忆化缓存。
//
// ## 为什么需要
//
// 窗口 / 工作区 / 终端这几个 provider 的条目每次查询都要重新读（窗口标题、
// 工作区路径随时会变，缓存条目即过期），但搜索键只取决于字符串本身——
// 同一个标题每次按键都重算一遍 NewSearchKeys（多音字展开 + 若干字符串分配）
// 是纯浪费。Rust 侧 release 实测 200 条目（400 份键）：重算 1374 µs/轮 →
// 记忆化 16 µs/轮（≈86×）。
//
// ## 淘汰策略：容量上限 + 超限时整表清空
//
// 键是源字符串（窗口标题可能是浏览器标签、可能含 URL），不能无界增长。
// 到 CacheCapacity 就整表清空重来：表本来就只有几百条，一次全清比维护
// LRU 顺序便宜，而且天然不会残留「已经关掉的窗口」的标题。
//
// ## 并发
//
// Rust 侧用 Mutex（provider 要求 Send + Sync）；Go 侧同样加锁，
// 命中时返回同一个指针（对应 Rust 的 Arc 命中只做一次原子加）。
type SearchKeysCache struct {
	mu    sync.Mutex
	items map[string]*SearchKeys
}

// CacheCapacity 是搜索键缓存的上限（条数）。
//
// 必须大于「一台机器上可能出现的不同名字数」：apps 动辄上千条（AppsFolder + 开始菜单），
// 缓存一旦装不下就会走「整表清空」的淘汰，命中率归零、每次按键重算全部拼音键
// （实测 1200 个不同名字：5.1ms/键，比 500µs 的正常水平高一个数量级）。
// 每条 SearchKeys 约 200–400B（含拼音变体），4096 条上限约 1MB 量级。
const CacheCapacity = 4096

// NewSearchKeysCache 对应 SearchKeysCache::new。
func NewSearchKeysCache() *SearchKeysCache {
	return &SearchKeysCache{items: make(map[string]*SearchKeys)}
}

// Get 对应 SearchKeysCache::get：取（命中）/ 建（未命中）一份搜索键。
func (c *SearchKeysCache) Get(name string) *SearchKeys {
	c.mu.Lock()
	defer c.mu.Unlock()
	if keys, ok := c.items[name]; ok {
		return keys
	}
	keys := NewSearchKeys(name)
	if len(c.items) >= CacheCapacity {
		c.items = make(map[string]*SearchKeys)
	}
	c.items[name] = keys
	return keys
}

// Len 对应 SearchKeysCache::len（单测 / 诊断用）。
func (c *SearchKeysCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}

// IsEmpty 对应 SearchKeysCache::is_empty。
func (c *SearchKeysCache) IsEmpty() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items) == 0
}
