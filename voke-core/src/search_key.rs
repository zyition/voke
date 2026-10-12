//! 拼音搜索键预处理（§4 拼音匹配策略）。
//!
//! 索引时对每个条目预生成搜索键，查询时零汉字转换开销：
//!
//! ```text
//! "微信"       → keys: ["微信", "weixin", "wx"]
//! "网易云音乐" → keys: ["网易云音乐", "wangyiyunyinyue", "wyyyy"]
//! ```
//!
//! 查询时：
//! - query 为纯 ASCII → 对所有 keys 跑 nucleo 模糊匹配（首字母串天然获得前缀高分）
//! - query 含汉字 → 只对原名匹配
//!
//! 索引期一次生成的前提是"条目稳定"；条目每次查询都重新读的 provider
//! （窗口 / 工作区 / 终端）要走 [`SearchKeysCache`] 复用，否则每次按键都重算。

use std::collections::HashMap;
use std::sync::{Arc, Mutex};

use pinyin::{ToPinyin, ToPinyinMulti};

/// 是否含有非 ASCII 字符（用于判断 query 走汉字匹配还是拼音匹配路径）。
pub fn has_non_ascii(s: &str) -> bool {
    !s.is_ascii()
}

/// 把一串汉字转成不带声调的全拼，例如 "微信" → "weixin"。
pub fn to_pinyin(input: &str) -> String {
    input.to_pinyin().flatten().map(|p| p.plain()).collect()
}

/// 取每个汉字拼音的首字母，例如 "微信" → "wx"。
///
/// 逐字取首字符；没有拼音数据的字符（标点、拉丁字母）原样保留，
/// 这样 "Zed 编辑器" 的首字母串是 "Zbjzq" 而不是丢掉开头。
pub fn to_initials(input: &str) -> String {
    input
        .chars()
        .map(|c| {
            c.to_pinyin()
                .and_then(|p| p.plain().chars().next())
                .map(|ch| ch.to_string())
                .unwrap_or_else(|| c.to_string())
        })
        .collect()
}

/// 多音字展开的结果：全拼变体与首字母串变体一一对应。
///
/// 例如 "音乐" 的 "乐" 有 le/yue 两读，得到
/// full = ["yinyue", "yinle"]、initials = ["yy", "yl"]。
struct Variants {
    full: Vec<String>,
    initials: Vec<String>,
}

/// 多音字展开：为每个字的所有读音生成一组全拼与首字母串。
///
/// 首字母串在**每个字**上取首字符（"微信" → "wx"），而不是从拼接后的全拼里筛字母。
/// 组数随多音字数量指数增长，所以这里限制上限。
fn pinyin_variants(input: &str, limit: usize) -> Variants {
    // 每个字的候选读音；非汉字或没有拼音数据的位置沿用原字符。
    // 每个字候选的 (全拼, 首字母)；非汉字或没有拼音数据的位置沿用原字符。
    let per_char: Vec<Vec<(String, String)>> = input
        .chars()
        .map(|c| {
            let readings: Vec<(String, String)> = match c.to_pinyin_multi() {
                Some(multi) => (0..multi.count())
                    .filter_map(|i| multi.get_opt(i))
                    .map(|p| {
                        let plain = p.plain();
                        let initial = plain.chars().next().unwrap_or(c).to_string();
                        (plain.to_string(), initial)
                    })
                    .collect(),
                None => Vec::new(),
            };
            if readings.is_empty() {
                // 非汉字或没有拼音数据：原样保留该字符，首字母取自身。
                vec![(c.to_string(), c.to_string())]
            } else {
                readings
            }
        })
        .collect();

    let mut out: Vec<(String, String)> = vec![(String::new(), String::new())];
    for choices in per_char {
        if out.len() * choices.len() > limit {
            // 组数太多时退化为只取每个字的第一个读音，避免索引膨胀。
            out = out
                .into_iter()
                .map(|(f, i)| {
                    let (rf, ri) = &choices[0];
                    (format!("{f}{rf}"), format!("{i}{ri}"))
                })
                .collect();
            continue;
        }
        let mut next = Vec::with_capacity(out.len() * choices.len());
        for (f, i) in &out {
            for (rf, ri) in &choices {
                next.push((format!("{f}{rf}"), format!("{i}{ri}")));
            }
        }
        out = next;
    }
    let (full, initials) = out.into_iter().unzip();
    Variants { full, initials }
}

/// 搜索键：索引期一次生成，查询期只读。
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct SearchKeys {
    /// 条目原名，汉字 query 只对它匹配。
    pub name: String,
    /// 全拼变体（含多音字展开），ASCII query 匹配。
    pub full: Vec<String>,
    /// 首字母串变体，ASCII query 匹配（"wx" 命中"微信"）。
    pub initials: Vec<String>,
}

impl SearchKeys {
    /// 多音字展开组数的上限，超出后每个字只取第一个读音。
    const VARIANT_LIMIT: usize = 8;

    pub fn from_name(name: &str) -> Self {
        let variants = pinyin_variants(name, Self::VARIANT_LIMIT);
        // 非汉字条目（如 "chrome"）的"全拼"就是它本身，不算额外的键。
        let full: Vec<String> = variants
            .full
            .into_iter()
            .filter(|v| !v.is_empty() && v != name)
            .collect();
        let initials: Vec<String> = variants
            .initials
            .into_iter()
            .filter(|v| !v.is_empty() && v != name && !full.contains(v))
            .collect();
        Self {
            name: name.to_string(),
            full,
            initials,
        }
    }

    /// 所有可用于 ASCII query 的键（原名 + 全拼 + 首字母串）。
    ///
    /// 原名必须包含在内：纯 ASCII 条目（"Visual Studio Code"）没有拼音变体，
    /// 靠原名匹配 "vscode"。
    pub fn ascii_keys(&self) -> impl Iterator<Item = &str> {
        std::iter::once(self.name.as_str())
            .chain(self.full.iter().map(|s| s.as_str()))
            .chain(self.initials.iter().map(|s| s.as_str()))
    }
}

/// `SearchKeys` 记忆化缓存：键 = 源字符串，值 = 建好的搜索键。
///
/// ## 为什么需要
///
/// 窗口 / 工作区 / 终端这几个 provider 的**条目**每次查询都要重新读（窗口标题、
/// 工作区路径随时会变，缓存条目即过期），但**搜索键只取决于字符串本身**——
/// 同一个标题每次按键都重算一遍 `from_name`（多音字展开 + 若干 String 分配）
/// 是纯浪费。本机 release 实测 200 条目（400 份键）：重算 1374 µs/轮 →
/// 记忆化 16 µs/轮（≈86×）。
///
/// ## 淘汰策略：容量上限 + 超限时整表清空
///
/// 键是源字符串（窗口标题可能是浏览器标签、可能含 URL），**不能无界增长**。
/// 到 [`Self::CAPACITY`] 就整表清空重来：表本来就只有几百条，一次全清比维护
/// LRU 顺序便宜，而且天然不会残留"已经关掉的窗口"的标题。
///
/// ## 并发
///
/// `Provider::query(&self)` 只给 `&self`，所以内部用 `Mutex`（不是 `RefCell`：
/// provider 要求 `Send + Sync`）。值用 `Arc` —— 命中时只需一次原子加，
/// 不用克隆里面的 `String` / `Vec<String>`。
#[derive(Debug, Default)]
pub struct SearchKeysCache {
    inner: Mutex<HashMap<String, Arc<SearchKeys>>>,
}

impl SearchKeysCache {
    /// 容量上限（条目数）。达到后清空重来，见类型文档。
    pub const CAPACITY: usize = 512;

    pub fn new() -> Self {
        Self::default()
    }

    /// 取（命中）/ 建（未命中）一份搜索键。
    pub fn get(&self, name: &str) -> Arc<SearchKeys> {
        let mut guard = self.lock();
        if let Some(keys) = guard.get(name) {
            return keys.clone();
        }
        let keys = Arc::new(SearchKeys::from_name(name));
        if guard.len() >= Self::CAPACITY {
            guard.clear();
        }
        guard.insert(name.to_string(), keys.clone());
        keys
    }

    /// 当前缓存的条目数（单测 / 诊断用）。
    pub fn len(&self) -> usize {
        self.lock().len()
    }

    pub fn is_empty(&self) -> bool {
        self.lock().is_empty()
    }

    /// 锁被污染（某个持锁线程 panic）时不清空、继续用里面的数据：
    /// 这里存的只是可重算的缓存，没有一致性风险。
    fn lock(&self) -> std::sync::MutexGuard<'_, HashMap<String, Arc<SearchKeys>>> {
        self.inner
            .lock()
            .unwrap_or_else(|poisoned| poisoned.into_inner())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn detects_non_ascii() {
        assert!(has_non_ascii("微信"));
        assert!(!has_non_ascii("weixin"));
    }

    #[test]
    fn builds_pinyin_and_initials() {
        assert_eq!(to_pinyin("微信"), "weixin");
        assert_eq!(to_initials("微信"), "wx");
    }

    #[test]
    fn builds_keys_for_chinese_name() {
        let keys = SearchKeys::from_name("微信");
        assert_eq!(keys.name, "微信");
        assert!(keys.full.contains(&"weixin".to_string()));
        assert!(keys.initials.contains(&"wx".to_string()));
    }

    #[test]
    fn expands_heteronym() {
        // "乐" 有 le / yue 两读，应同时可命中。
        let keys = SearchKeys::from_name("音乐");
        assert!(keys.full.contains(&"yinyue".to_string()));
        assert!(keys.full.contains(&"yinle".to_string()));
    }

    #[test]
    fn handles_ascii_name() {
        let keys = SearchKeys::from_name("chrome");
        assert_eq!(keys.name, "chrome");
        // 纯 ASCII 条目靠原名匹配，不额外造拼音键。
        assert!(keys.full.is_empty());
        assert!(keys.initials.is_empty());
    }

    #[test]
    fn variant_count_is_bounded() {
        // 每个字都多音的极端输入不应让键数量爆炸。
        let keys = SearchKeys::from_name("重重重重重重重重重");
        assert!(keys.full.len() <= SearchKeys::VARIANT_LIMIT);
    }

    // ── SearchKeysCache ──

    #[test]
    fn cache_hits_same_input_without_rebuilding() {
        let cache = SearchKeysCache::new();
        let first = cache.get("微信");
        let second = cache.get("微信");
        // 同一个 Arc = 第二次没重算（重算会造新对象）。
        assert!(Arc::ptr_eq(&first, &second));
        assert_eq!(first.name, "微信");
        assert_eq!(cache.len(), 1, "同名只占一条");
        // 不同名字各自一条，内容不受缓存影响。
        let other = cache.get("chrome");
        assert_eq!(other.name, "chrome");
        assert!(!Arc::ptr_eq(&first, &other));
        assert_eq!(cache.len(), 2);
    }

    /// 窗口标题会变、可能含 URL —— 缓存必须**有界**，不能跟着标题无限长。
    #[test]
    fn cache_is_bounded_not_unbounded() {
        let cache = SearchKeysCache::new();
        for i in 0..5000 {
            // 每条都是不同的 URL / 标题，真实场景里就是标签页换来换去。
            let _ = cache.get(&format!("https://example.com/tab/{i}"));
        }
        assert!(
            cache.len() <= SearchKeysCache::CAPACITY,
            "缓存必须 <= 容量上限，实际 {}",
            cache.len()
        );
        // 清空后仍然可用（不能因为淘汰就永久失效）。
        let keys = cache.get("微信");
        assert_eq!(keys.name, "微信");
    }
}
