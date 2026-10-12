//! nucleo 模糊匹配（§4）。
//!
//! 索引期已经把条目展开成多个搜索键，查询期只需要：
//! - query 为纯 ASCII → 对所有 ASCII 键（全拼 + 首字母串）跑模糊匹配，取最高分
//! - query 含汉字 → 只对原名匹配
//!
//! 这样 `wx`、`weixin`、`微信` 三种输入都能命中，全程不经过输入法。

use nucleo_matcher::{
    Config, Matcher, Utf32Str,
    pattern::{AtomKind, CaseMatching, Normalization, Pattern},
};

use crate::search_key::{SearchKeys, has_non_ascii};

/// `Pattern::score` 返回 u32，这里归一化成 0.0–1.0 便于与权重相乘。
///
/// 饱和值取自实测量级：完整匹配约 166（"weixin" vs "weixin"），
/// 首字母串命中约 62（"wx" vs "wx"），弱匹配约 48。取 200 让满匹配接近 1.0，
/// 同时保留区分度——拍脑袋取 u32::MAX 会把所有分数压到 0.0 附近，排序失效。
const SCORE_SATURATION: f32 = 200.0;

/// 一次匹配的结果。
#[derive(Clone, Copy, Debug, PartialEq)]
pub struct MatchScore {
    /// 归一化到 0.0–1.0 的匹配分，0 表示未命中。
    pub score: f32,
}

/// 对一个条目打分。
///
/// `matcher` 由调用方持有并复用（内部有分配缓冲，跨调用复用能省掉重复分配）。
pub fn score_entry(matcher: &mut Matcher, query: &str, keys: &SearchKeys) -> Option<MatchScore> {
    if query.is_empty() {
        return None;
    }
    let pattern = build_pattern(query, !has_non_ascii(query))?;

    let score = if has_non_ascii(query) {
        // 汉字 query 只对原名匹配。
        score_one(matcher, &pattern, &keys.name)?
    } else {
        // ASCII query 对全部键取最高分：首字母串天然获得前缀高分。
        keys.ascii_keys()
            .filter_map(|k| score_one(matcher, &pattern, k))
            // f32 没有 Ord，用 partial_cmp 折叠取最大值。
            .fold(None::<f32>, |acc, s| {
                Some(match acc {
                    Some(prev) if prev >= s => prev,
                    _ => s,
                })
            })?
    };
    Some(MatchScore { score })
}

/// 用 nucleo 对单个 haystack 打分，返回归一化的 0.0–1.0。
fn score_one(matcher: &mut Matcher, pattern: &Pattern, haystack: &str) -> Option<f32> {
    if haystack.is_empty() {
        return None;
    }
    let chars: Vec<char> = haystack.chars().collect();
    let raw = pattern.score(Utf32Str::Unicode(&chars), matcher)?;
    // 量级差异很大（fuzzy 与 substring 不同），饱和裁剪后再归一化。
    Some((raw as f32 / SCORE_SATURATION).clamp(0.0, 1.0))
}

/// 构造 nucleo 模式。
///
/// `fuzzy` 为 false 时走子串匹配（汉字 query 用，避免把汉字拆成单字乱匹配）。
fn build_pattern(query: &str, fuzzy: bool) -> Option<Pattern> {
    let kind = if fuzzy {
        AtomKind::Fuzzy
    } else {
        AtomKind::Substring
    };
    let pattern = Pattern::new(query, CaseMatching::Ignore, Normalization::Smart, kind);
    // Pattern::new 会把输入按空白拆词；空查询或纯空白不产生 atom。
    (!pattern.atoms.is_empty()).then_some(pattern)
}

/// 便捷入口：一次性建 matcher（批量查询时应复用 [`Matcher`]，避免重复分配）。
pub fn score_entries(entries: &[(String, SearchKeys)], query: &str) -> Vec<(String, MatchScore)> {
    let mut matcher = Matcher::new(Config::DEFAULT);
    entries
        .iter()
        .filter_map(|(id, keys)| score_entry(&mut matcher, query, keys).map(|s| (id.clone(), s)))
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn keys(name: &str) -> SearchKeys {
        SearchKeys::from_name(name)
    }

    #[test]
    fn matches_by_pinyin_initials() {
        let k = keys("微信");
        let mut m = Matcher::new(Config::DEFAULT);
        assert!(score_entry(&mut m, "wx", &k).is_some());
    }

    #[test]
    fn matches_by_full_pinyin() {
        let k = keys("微信");
        let mut m = Matcher::new(Config::DEFAULT);
        assert!(score_entry(&mut m, "weixin", &k).is_some());
    }

    #[test]
    fn matches_by_hanzi() {
        let k = keys("微信");
        let mut m = Matcher::new(Config::DEFAULT);
        assert!(score_entry(&mut m, "微信", &k).is_some());
    }

    #[test]
    fn heteronym_both_readings_match() {
        let k = keys("音乐");
        let mut m = Matcher::new(Config::DEFAULT);
        assert!(score_entry(&mut m, "yinyue", &k).is_some());
        assert!(score_entry(&mut m, "yinle", &k).is_some());
    }

    #[test]
    fn empty_query_matches_nothing() {
        let k = keys("微信");
        let mut m = Matcher::new(Config::DEFAULT);
        assert!(score_entry(&mut m, "", &k).is_none());
    }

    #[test]
    fn unrelated_query_misses() {
        let k = keys("微信");
        let mut m = Matcher::new(Config::DEFAULT);
        assert!(score_entry(&mut m, "zzzqqqxxx", &k).is_none());
    }

    #[test]
    fn prefix_scores_higher_than_scattered() {
        let k = keys("网易云音乐");
        let mut m = Matcher::new(Config::DEFAULT);
        let prefix = score_entry(&mut m, "wyyy", &k).unwrap().score;
        let scattered = score_entry(&mut m, "wyy", &k).unwrap().score;
        assert!(prefix > scattered, "{prefix} should beat {scattered}");
    }

    #[test]
    fn batch_scores_entries() {
        let entries = vec![
            ("wechat".to_string(), keys("微信")),
            ("qq".to_string(), keys("QQ音乐")),
        ];
        let hits = score_entries(&entries, "wx");
        assert_eq!(hits.len(), 1);
        assert_eq!(hits[0].0, "wechat");
    }
}
