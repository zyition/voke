//! 插件角标：结果行左侧的"这是哪个插件给的"标识。
//!
//! ## 为什么需要它
//!
//! 方案 A 把结果**按 provider 分组**（见 `voke_core::QueryEngine::aggregate`），
//! 分组只有配上可见标识才有意义——否则用户只知道"这五行在一起"，不知道
//! 它们为什么在一起。角标用 `provider_id` 的**稳定哈希**映射到一个固定颜色，
//! 再叠一枚语义化 Lucide 图标（见 [`plugin_icon`]）：同一个插件在任何一次查询里
//! 都是同一块底色 + 同一个图标，不同插件一眼可分。
//!
//! ## 颜色从哪来（Design Guides 的"tokens before values"）
//!
//! 应用 UI 不该出现裸 hex/rgb。这里没有用 `cx.theme()` 的语义色（accent/primary
//! 那些是**状态色**，把它们当分类色用会破坏"语义色只表达语义"的约定），
//! 而是**在产品自己的 token 层**里定义色相：
//!
//! - 色相是**数据**（分类身份），所以允许是固定值；明度/饱和度仍随主题走
//!   （深色主题用更高的明度，保证与背景对比度足够）
//! - 文字色在浅色主题下用白、深色主题下用深色，由主题模式决定，不写死
//! - **色相取自哈希的细粒度映射**（[`HUE_BUCKETS`] 个桶）而不是一个小调色板：
//!   调色板只有几种颜色时，两个插件撞色是必然的（实测 6 色调色板下
//!   `uuid` 与 `awake` 就撞了）——而"不同插件一眼可分"正是角标存在的唯一理由。
//!   细粒度色相既保持"同一 id 永远同色"，又几乎不会撞色。
//! - 哈希结果会**避开语义色的色相带**（红 = danger、绿 = success），
//!   角标不该被误读成状态
//!
//! 这正是 Design Guides 允许的例外："Raw colors belong only inside theme
//! definitions or in audited data/raster content whose color is itself the data."
//! 这个模块就是那个 audited 位置——所有分类色只在这里出现一次。

use gpui_kit::assets::IconName;
use gpui_kit::component::ActiveTheme as _;
use gpui_kit::{App, Hsla};

/// 色相分辨率：哈希对 3600 取模，得到 0.1° 的色相步进。
///
/// 8 个已注册插件在这个分辨率下几乎不可能撞色。
const HUE_BUCKETS: u64 = 3600;

/// 要避开的语义色色相带（以 0–1 的色相为单位，含边界）。
///
/// 红（`danger`）与绿（`success`）是语义色，角标落在那里会被误读成状态。
const AVOID_HUES: &[(f32, f32)] = &[(0.95, 1.0), (0.0, 0.04), (0.30, 0.42)];

/// 一个插件的角标样式（底色色相）。
#[derive(Clone, Copy, Debug, PartialEq)]
pub struct PluginBadge {
    /// 色相（0–1），实际颜色由 [`badge_background`] 按主题算出。
    pub hue: f32,
}

/// 按 provider id 取角标底色（同一个 id 永远得到同一块颜色）。
///
/// 哈希是自己写的 FNV-1a：`provider_id` 只有个位数个、进程生命周期内固定，
/// 用 `DefaultHasher` 也行，但标准库明确不保证跨版本稳定——角标颜色不该
/// 因为换了 Rust 版本就变，所以显式定死一个简单哈希。
pub fn plugin_badge(provider_id: &str) -> PluginBadge {
    let hash = fnv1a(provider_id.as_bytes());
    PluginBadge {
        hue: hue_from_hash(hash),
    }
}

/// `provider_id` → 角标图标（Lucide）。
///
/// 集中一处维护：结果行角标与模式令牌共用同一张表，保证同一个插件在
/// 任何位置都是同一个图标。apps/windows 有真实图标（PNG），不走这里；
/// 未登记的 id 用 [`IconName::Blocks`] 兜底（"一个插件"的通用语义）。
const PLUGIN_ICONS: &[(&str, IconName)] = &[
    ("awake", IconName::Coffee),
    ("terminal", IconName::SquareTerminal),
    ("workspaces", IconName::LayoutGrid),
    ("calculator", IconName::Calculator),
    ("timestamp", IconName::Clock),
    ("base64", IconName::LockKeyhole),
    ("uuid", IconName::FingerprintPattern),
    ("random", IconName::Dices),
    ("apps", IconName::AppWindow),
    ("windows", IconName::Layers),
];

/// 取某个插件的角标图标；未登记 / 空 id 用通用图标兜底。
pub fn plugin_icon(provider_id: &str) -> IconName {
    PLUGIN_ICONS
        .iter()
        .find(|(id, _)| *id == provider_id)
        .map(|(_, icon)| *icon)
        .unwrap_or(IconName::Blocks)
}

/// 把哈希映射成一个色相（0–1），跳过语义色带。
fn hue_from_hash(hash: u64) -> f32 {
    let mut hue = (hash % HUE_BUCKETS) as f32 / HUE_BUCKETS as f32;
    // 命中语义色带就整体平移一段（确定性的，同一个 id 每次结果一致）。
    // 平移量取 0.08：足以跳出上面那几条带（最宽的一条是 0.12）。
    for _ in 0..8 {
        let in_avoided = AVOID_HUES.iter().any(|(lo, hi)| {
            // 色相是环形的：0.95–1.0 与 0.0–0.04 是"同一段红"。
            if lo > hi {
                hue >= *lo || hue <= *hi
            } else {
                hue >= *lo && hue <= *hi
            }
        });
        if !in_avoided {
            break;
        }
        hue = (hue + 0.08).fract();
    }
    hue
}

/// 角标背景色（随主题调整明度）。
pub fn badge_background(badge: PluginBadge, cx: &App) -> Hsla {
    if cx.theme().mode.is_dark() {
        // 深色主题：低明度 + 中饱和，避免刺眼。
        Hsla {
            h: badge.hue,
            s: 0.55,
            l: 0.38,
            a: 1.0,
        }
    } else {
        Hsla {
            h: badge.hue,
            s: 0.62,
            l: 0.46,
            a: 1.0,
        }
    }
}

/// 角标文字色：与 [`badge_background`] 的明度配对，保证对比度。
pub fn badge_foreground(cx: &App) -> Hsla {
    if cx.theme().mode.is_dark() {
        // 深底浅字
        Hsla {
            h: 0.0,
            s: 0.0,
            l: 0.97,
            a: 1.0,
        }
    } else {
        // 浅底白字
        Hsla {
            h: 0.0,
            s: 0.0,
            l: 1.0,
            a: 1.0,
        }
    }
}

/// FNV-1a 64 位（简单、无依赖、跨版本稳定）。
fn fnv1a(bytes: &[u8]) -> u64 {
    let mut hash: u64 = 0xcbf2_9ce4_8422_2325;
    for byte in bytes {
        hash ^= u64::from(*byte);
        hash = hash.wrapping_mul(0x0000_0100_0000_01b3);
    }
    hash
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::HashMap;

    #[test]
    fn badge_is_stable_for_the_same_id() {
        for id in ["apps", "windows", "awake", "calculator", "base64"] {
            assert_eq!(plugin_badge(id), plugin_badge(id), "{id}");
        }
    }

    /// 当前注册表的 8 个插件都必须拿到互不相同的颜色（否则角标失去了区分作用）。
    ///
    /// 这条测试盯着一个真实撞过的 bug：早先用 6 色调色板时 `uuid` 与 `awake`
    /// 拿到了同一块颜色。
    #[test]
    fn known_plugins_get_distinct_colors() {
        let ids = [
            "apps",
            "windows",
            "awake",
            "timestamp",
            "base64",
            "uuid",
            "random",
            "calculator",
        ];
        let mut by_hue: HashMap<u32, &str> = HashMap::new();
        for id in ids {
            let badge = plugin_badge(id);
            let key = (badge.hue * 100_000.0) as u32;
            if let Some(other) = by_hue.insert(key, id) {
                panic!("{id} 与 {other} 拿到了同一块角标颜色（hue={}）", badge.hue);
            }
        }
    }

    /// 角标颜色避开语义色带（红 / 绿），以免被误读成状态。
    #[test]
    fn badge_hues_avoid_semantic_colors() {
        for id in [
            "apps",
            "windows",
            "awake",
            "timestamp",
            "base64",
            "uuid",
            "random",
            "calculator",
            "something-else",
            "x",
        ] {
            let hue = plugin_badge(id).hue;
            assert!((0.0..1.0).contains(&hue), "{id}: hue={hue}");
            for (lo, hi) in AVOID_HUES {
                let inside = if lo > hi {
                    hue >= *lo || hue <= *hi
                } else {
                    hue >= *lo && hue <= *hi
                };
                assert!(!inside, "{id} 的色相 {hue} 落在语义色带 {lo}–{hi}");
            }
        }
    }

    /// 已登记插件都有专属图标，未登记的 id 落到通用兜底图标。
    #[test]
    fn known_plugins_have_dedicated_icons() {
        for id in [
            "awake",
            "terminal",
            "workspaces",
            "calculator",
            "timestamp",
            "base64",
            "uuid",
            "random",
        ] {
            assert_ne!(plugin_icon(id), IconName::Blocks, "{id} 应有专属图标");
        }
        assert_eq!(plugin_icon("something-else"), IconName::Blocks);
        assert_eq!(plugin_icon(""), IconName::Blocks);
    }

    #[test]
    fn empty_id_is_handled() {
        // 颜色仍然落在合法范围内。
        assert!((0.0..1.0).contains(&plugin_badge("").hue));
    }

    /// FNV-1a 的已知测试向量（保证"稳定哈希"这个承诺真的成立）。
    #[test]
    fn fnv1a_matches_known_vectors() {
        assert_eq!(fnv1a(b""), 0xcbf2_9ce4_8422_2325);
        assert_eq!(fnv1a(b"a"), 0xaf63_dc4c_8601_ec8c);
        assert_eq!(fnv1a(b"foobar"), 0x85944171f73967e8);
    }
}
