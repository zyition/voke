//! uuid provider：生成一个 UUID v4 并复制（§7 的 M4 插件，对齐 PTR 的
//! "Value Generator / UUID"）。
//!
//! ## 触发方式（M5：子命令面板激活）
//!
//! 输入 `#` → 子命令面板 → 选「UUID」→ 输入框变成 `# `，列表里立刻出现一条
//! **当场生成**的 UUID（Enter 即复制）。也保留 `#` 前缀，直接打 `#uuid` 也能命中。
//! `is_global = false`：UUID 不该参与全局模糊搜索。
//!
//! ## 行为
//!
//! | 激活后输入 | 结果 |
//! |---|---|
//! | 空 / `uuid` | 1 条 UUID（用法见输入框占位符） |
//! | `3` / `n:3` / `count:3` | 3 条 UUID（上限 [`MAX_COUNT`]，带序号副标题） |
//! | 其它 | 空列表（不认识的形态不猜） |
//!
//! 每次 query 都重新生成（与窗口 provider 同款理由：结果必须跟着输入变）——
//! 所以每敲一个键，列表里的 UUID 都会换一批。
//!
//! 生成动作是 [`Action::Copy`]：选中即复制，这正是这个插件存在的意义。

use crate::action::Action;
use crate::engine::PREFIX_UUID;
use crate::entropy::{Entropy, SystemEntropy, uuid_v4};
use crate::provider::{BoxFuture, Interact, Match, Provider, ProviderConfig, QueryCtx, ready};

/// 一次最多生成多少个（防止 `#uuid 999999` 这种输入把结果区/内存打爆）。
pub const MAX_COUNT: usize = 20;

/// uuid provider。
pub struct UuidProvider {
    config: ProviderConfig,
    entropy: Box<dyn Entropy>,
}

impl UuidProvider {
    /// provider 的稳定 id。
    pub const ID: &'static str = "uuid";

    pub fn new() -> Self {
        Self::with_entropy(Box::new(SystemEntropy))
    }

    /// 注入熵源（测试用 [`crate::entropy::SeededEntropy`] 保证可复现）。
    pub fn with_entropy(entropy: Box<dyn Entropy>) -> Self {
        Self {
            config: ProviderConfig {
                enabled: true,
                is_global: false,
                weight: 1.0,
                prefix: Some(PREFIX_UUID.to_string()),
                // `#` 系：默认间接激活（前缀 + 空格），见 config::default_activation。
                activation: crate::config::Activation::Indirect,
                count: 5,
            },
            entropy,
        }
    }

    /// 生成一个新 UUID（供外部直接调用）。
    pub fn generate(&self) -> String {
        uuid_v4(self.entropy.as_ref())
    }
}

impl Default for UuidProvider {
    fn default() -> Self {
        Self::new()
    }
}

impl Provider for UuidProvider {
    fn id(&self) -> &'static str {
        Self::ID
    }

    fn name(&self) -> &'static str {
        "UUID"
    }

    /// 别名：`u` = 单字母快捷（`uuid`/`uni` 由 name 子串语义覆盖）。
    fn aliases(&self) -> &'static [&'static str] {
        &["u"]
    }

    fn description(&self) -> &'static str {
        "生成 UUID v4（可指定个数）"
    }

    fn config(&self) -> &ProviderConfig {
        &self.config
    }

    fn set_config(&mut self, config: ProviderConfig) {
        self.config = config;
    }

    fn placeholder(&self, _sub_mode: Option<&str>) -> &'static str {
        // uuid 无子模式：激活后 Enter 直接生成并复制一条；个数提示进占位符。
        "生成 UUID，输入个数可批量如 3"
    }

    /// 每次 query 都生成新 v4：面板重新显示 / 热更新等"输入没变却重跑"的
    /// 路径必须跳过（见 [`crate::provider::Provider::regenerates_on_query`]）。
    fn regenerates_on_query(&self) -> bool {
        true
    }

    fn query(&self, ctx: &QueryCtx) -> BoxFuture<'static, Vec<Match>> {
        let query = ctx.query.trim();
        let count = requested_count(query);
        let Some(count) = count else {
            return ready(Vec::new());
        };
        let matches: Vec<Match> = (0..count)
            .map(|i| {
                let value = uuid_v4(self.entropy.as_ref());
                Match {
                    provider_id: Self::ID.to_string(),
                    title: value.clone(),
                    // 多条时标明序号，单条时留空（不制造无意义的视觉噪声）。
                    subtitle: if count > 1 {
                        format!("UUID v4（{} / {count}）", i + 1)
                    } else {
                        "UUID v4".to_string()
                    },
                    icon: None,
                    action: Action::Copy { text: value },
                    // 一次生成多条时，越靠前的越可能被选中——给递减分保持稳定顺序。
                    score: 1.0 - i as f32 * 0.001,
                    // Enter / 点击都生成并复制。
                    interact: Interact::Execute,
                }
            })
            .collect();
        ready(matches)
    }
}

/// 解析"要几个 UUID"：空查询 / `uuid` / 纯数字 / `n:<数字>` 都接受。
///
/// 返回 `None` 表示输入形态不识别（例如 `#uuid abc`），则不返回任何结果。
fn requested_count(query: &str) -> Option<usize> {
    let query = query.trim().to_ascii_lowercase();
    if query.is_empty() || query == "uuid" {
        return Some(1);
    }
    let digits = query
        .strip_prefix("n:")
        .or_else(|| query.strip_prefix("count:"))
        .unwrap_or(&query);
    let count: usize = digits.trim().parse().ok()?;
    // 0 个没有意义；上界防爆。
    if count == 0 {
        return None;
    }
    Some(count.min(MAX_COUNT))
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::entropy::SeededEntropy;
    use std::collections::HashSet;
    use std::future::Future;

    fn block_on<F: Future>(f: F) -> F::Output {
        let mut f = Box::pin(f);
        let mut cx = std::task::Context::from_waker(std::task::Waker::noop());
        match f.as_mut().poll(&mut cx) {
            std::task::Poll::Ready(v) => v,
            std::task::Poll::Pending => panic!("uuid 的 future 应当立即就绪"),
        }
    }

    fn seeded() -> UuidProvider {
        UuidProvider::with_entropy(Box::new(SeededEntropy::new(12345)))
    }

    fn uuids(provider: &UuidProvider, q: &str) -> Vec<Match> {
        let ctx = QueryCtx::new(format!("#u{q}"), q.to_string());
        block_on(provider.query(&ctx))
    }

    #[test]
    fn is_prefix_only_and_not_global() {
        let p = seeded();
        assert_eq!(p.id(), "uuid");
        assert_eq!(p.name(), "UUID");
        assert!(!p.description().is_empty());
        assert_eq!(p.config().prefix, Some("#u".to_string()));
        assert!(!p.config().is_global, "UUID 不得进全局结果");
    }

    /// 空查询（刚激活）：一条可复制的 UUID（用法提示已挪进占位符）。
    #[test]
    fn empty_query_generates_one_uuid() {
        let p = seeded();
        let hits = uuids(&p, "");
        assert_eq!(hits.len(), 1, "只生成一条 UUID，无用法提示行");
        assert_eq!(hits[0].subtitle, "UUID v4");
        assert_eq!(hits[0].icon, None);
        assert_eq!(
            hits[0].action,
            Action::Copy {
                text: hits[0].title.clone()
            }
        );
    }

    #[test]
    fn generated_uuid_has_canonical_shape() {
        let p = seeded();
        // 空查询会多出一行用法提示（不可复制），只校验可复制行。
        let generated = ["", "uuid", "3"]
            .iter()
            .flat_map(|q| uuids(&p, q))
            .filter(|m| m.interact == Interact::Execute);
        for value in generated {
            let text = &value.title;
            assert_eq!(text.len(), 36, "{text}");
            let parts: Vec<&str> = text.split('-').collect();
            assert_eq!(
                parts.iter().map(|p| p.len()).collect::<Vec<_>>(),
                vec![8, 4, 4, 4, 12]
            );
            assert_eq!(&parts[2][..1], "4", "版本位必须是 4: {text}");
            assert!(
                matches!(&parts[3][..1], "8" | "9" | "a" | "b"),
                "变体位必须是 RFC4122: {text}"
            );
            assert!(
                text.chars()
                    .all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || c == '-')
            );
        }
    }

    #[test]
    fn action_copies_the_generated_uuid() {
        let p = seeded();
        let hits = uuids(&p, "");
        let Action::Copy { text } = &hits[0].action else {
            panic!("uuid 的动作必须是 Copy");
        };
        assert_eq!(text, &hits[0].title);
    }

    #[test]
    fn count_query_generates_multiple_distinct_uuids() {
        let p = seeded();
        let hits = uuids(&p, "5");
        assert_eq!(hits.len(), 5);
        let unique: HashSet<&str> = hits.iter().map(|m| m.title.as_str()).collect();
        assert_eq!(unique.len(), 5, "同一批 UUID 不能重复");
        assert!(hits[0].subtitle.contains("1 / 5"));
        assert!(hits[4].subtitle.contains("5 / 5"));
        // 非空查询不追加提示行
        assert!(hits.iter().all(|m| !m.interact.is_hint()));
        // 分数递减，保证顺序稳定
        for pair in hits.windows(2) {
            assert!(pair[0].score >= pair[1].score);
        }
    }

    #[test]
    fn count_aliases_work_and_are_capped() {
        let p = seeded();
        assert_eq!(uuids(&p, "n:3").len(), 3);
        assert_eq!(uuids(&p, "count:2").len(), 2);
        assert_eq!(uuids(&p, "  4  ").len(), 4);
        // 超过上限被截断，而不是 OOM
        assert_eq!(uuids(&p, "999999").len(), MAX_COUNT);
    }

    #[test]
    fn invalid_count_produces_no_match() {
        let p = seeded();
        for bad in ["abc", "0", "-1", "1.5", "n:", "x:3"] {
            assert_eq!(uuids(&p, bad).len(), 0, "{bad:?} 不该产生结果");
        }
    }

    #[test]
    fn fresh_query_regenerates() {
        // 每次 query 都应当是新值（不能用缓存的老 UUID）。
        let p = UuidProvider::with_entropy(Box::new(SystemEntropy));
        let first = uuids(&p, "")[0].title.clone();
        let second = uuids(&p, "")[0].title.clone();
        assert_ne!(first, second, "每次查询必须重新生成");
    }

    /// 生成行的标题与复制文本必须一致（Enter 复制的就是屏幕上那串）。
    #[test]
    fn match_title_equals_copied_text() {
        let p = seeded();
        for q in ["", "uuid", "3"] {
            for hit in uuids(&p, q)
                .into_iter()
                .filter(|m| m.interact == Interact::Execute)
            {
                let Action::Copy { text } = &hit.action else {
                    unreachable!()
                };
                assert_eq!(text, &hit.title, "{q}");
            }
        }
    }

    #[test]
    fn seeded_entropy_makes_generation_reproducible() {
        // 两个同种子的 provider 产出同一串（测试基础设施可用性验证）。
        let a = uuids(&seeded(), "");
        let b = uuids(&seeded(), "");
        assert_eq!(a[0].title, b[0].title);
    }
}
