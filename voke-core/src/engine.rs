//! 查询引擎：前缀路由 → 分发 → 分组聚合 → 排序（§2）。
//!
//! 关键设计点（对齐 §2）：
//! - **前缀路由**：query 命中某 provider 的专属前缀时只查那个 provider，
//!   **最长前缀优先**（`#bZm9v` 归 base64 而不是 timestamp，见 [`QueryEngine::prefix_owner`]）
//! - **分组聚合**：`aggregate` 按 provider 分组（组内按分降序、组间保持注册顺序），
//!   不再全局混排；[`Interact::Hint`](crate::Interact::Hint) 被单独抽出来
//! - **快慢解耦**：`provider.query` 返回 `BoxFuture`，慢 provider（窗口枚举）
//!   不阻塞快 provider 的渲染
//! - **`#` 插件列表**：裸 `#` 不再路由到 timestamp，而是列出所有 enabled 插件
//!   （[`QueryEngine::plugin_picker_matches`]），Enter 选中后激活；`=` 也不再直进
//!   calculator。前缀路由只剩"已激活插件剥前缀"一条路（`prepare_in_mode`）

use std::collections::HashMap;
use std::future::Future;
use std::pin::Pin;

use crate::action::Action;
use crate::config::{Activation, AppConfig};
use crate::provider::{Interact, Match, Provider, ProviderConfig, QueryCtx};

/// `#` 是插件列表的入口：裸 `#`（或 `# <过滤词>`）列出所有 enabled 的插件，
/// Enter / 点击选中后才激活（[`Action::ActivatePlugin`]）。二级前缀（`#b` 等）
/// 不再直接路由——`#` 开头的输入全部先落在插件列表上（过滤它），选中后
/// 输入框被写成插件的前缀（如 `#b `），之后的查询才由 [`QueryEngine::prefix_owner`]
/// 按**已激活**插件剥前缀。
///
/// **为什么撤掉二级前缀直进**：`detect_plugin` 一看到 `#`/`=` 开头就激活
/// timestamp/calculator，后续输入全被它吞掉——`#b` 的 `b` 变成了 timestamp 的
/// 查询文本，base64 永远组不起来（用户实测确认）。列表方案下两条路统一：
/// `#` → 列表 → 打 `b` 过滤出 Base64 → Enter。`=` 同理：先列表选中，输入框
/// 变 `= ` 后再打表达式。
///
/// 这些常量仍是每个插件自己的前缀（激活后剥前缀用），只是不再充当触发入口。
pub const PREFIX_CALCULATOR: &str = "=";
pub const PREFIX_TIMESTAMP: &str = "#";
pub const PREFIX_BASE64: &str = "#b";
pub const PREFIX_UUID: &str = "#u";
pub const PREFIX_RANDOM: &str = "#r";

/// 当前模式的输入框占位符。
///
/// 三个分支对应三种查询模式（方案 A 要求占位符随状态变，用户永远知道"我现在
/// 在哪儿、该输入什么"）：
///
/// - 没有激活插件 → [`GLOBAL_PLACEHOLDER`]（全局搜索/计算）
/// - 激活插件、没进子模式 → 插件自己的第一级提示（如 base64："选择 加密 或 解密"）
/// - 激活插件且在子模式里 → 那一级的提示（如 base64 enc："输入要加密的内容"）
///
/// 由 app 层在激活/进子模式/退出这几个时机调用并写进 `InputState::set_placeholder`，
/// **渲染路径不查引擎**（避免在 render 里竞争引擎锁）。
pub fn placeholder_for(provider: Option<&dyn Provider>, sub_mode: Option<&str>) -> &'static str {
    match provider {
        Some(provider) => provider.placeholder(sub_mode),
        None => crate::provider::GLOBAL_PLACEHOLDER,
    }
}

/// 一次查询的最终结果条目（已排序）。
#[derive(Clone, Debug)]
pub struct RankedMatch {
    pub inner: Match,
    /// 最终分 = provider.weight × match_score × 频率加成。
    pub final_score: f32,
}

/// 使用的频率记录（§4：本地 TOML，条目量级几百，无需数据库）。
///
/// key 是 provider 自己定义的稳定标识，例如 "app:微信" 或 "calc:1+1"。
///
/// **必须持久化才有意义**：这份数据是"越用越准"的唯一来源，进程一退出就归零的
/// 话，所谓的"学习性排序"只在单次会话里有效。落盘的文件路径与读写时机由 app 层
/// 决定（core 不碰 IO），这里只给出**纯函数**的序列化外包（`frequency_to_toml`
/// / `frequency_from_toml`）。
pub type FrequencyStore = HashMap<String, u32>;

/// 把频率表序列化成 TOML 文本（core 不碰 IO，写盘在 app 层）。
///
/// 先转成 `BTreeMap` 再序列化：`HashMap` 的迭代顺序每次都不一样，直接写会让落盘
/// 文件在每次执行后整体重排（diff 全量噪声、也没法人工看）。
pub fn frequency_to_toml(store: &FrequencyStore) -> Result<String, toml::ser::Error> {
    let ordered: std::collections::BTreeMap<&str, u32> =
        store.iter().map(|(k, v)| (k.as_str(), *v)).collect();
    toml::to_string(&ordered)
}

/// 反序列化频率表。**坏文本 / 缺字段一律回落空表 + 日志**，不让一个损坏文件
/// 把排序整体搞坏（与 `AppConfig::from_toml` 同一套纪律）。
pub fn frequency_from_toml(text: &str) -> FrequencyStore {
    let ordered: std::collections::BTreeMap<String, u32> = match toml::from_str(text) {
        Ok(ordered) => ordered,
        Err(err) => {
            log::warn!("频率数据不是合法 TOML，本次按空表处理：{err}");
            return FrequencyStore::new();
        }
    };
    ordered.into_iter().collect()
}

/// 频率加成：用次数取对数，避免高频条目把分数拉爆。
///
/// 1 次 = 1.0，其后每次约 +0.3，10 次约 1.95，100 次约 3.9。
pub fn frequency_boost(use_count: u32) -> f32 {
    if use_count == 0 {
        return 1.0;
    }
    1.0 + (f64::from(use_count) + 1.0).ln() as f32 * 0.5
}

/// 查询引擎：持有 provider 注册表与频率数据。
pub struct QueryEngine {
    providers: Vec<Box<dyn Provider>>,
    frequency: FrequencyStore,
}

/// 一个 provider 的一批结果，供 app 层增量渲染。
pub struct ProviderBatch {
    pub provider_id: String,
    pub matches: Vec<Match>,
}

/// 一次路由的结果：目标 provider 列表 + 已剥好前缀的查询上下文。
///
/// 把「路由」和「剥前缀」绑在一起返回，是为了让所有调用方（引擎自己的
/// [`QueryEngine::dispatch`] 与 app 层的查询循环）拿到**同一份**语义：
/// 前缀命中某个 provider 时 `ctx.query` 是剥掉前缀后的查询词
/// （`"=1+1"` → `"1+1"`），否则 `ctx.query` 等于原文。
///
/// 早先 app 层直接调 [`QueryEngine::route`] 再自己拼 `QueryCtx`，结果就是
/// 前缀虽然路由到了 calculator，calculator 却收到带 `=` 的原文——前缀路由等于没接。
pub struct Routing<'a> {
    /// 本轮的查询目标（注册顺序）。
    pub providers: Vec<&'a dyn Provider>,
    pub ctx: QueryCtx,
    /// 本轮是全局搜索还是被路由到单个插件。app 层据此维护 `active_plugin`
    /// （见 `voke-app/src/search.rs`）。
    pub route: RouteKind,
}

/// 本轮路由的形态。
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum RouteKind {
    /// 没有前缀命中（`providers` = 所有全局 provider）；也可能是空输入。
    Global,
    /// 被路由到**单个**插件（`providers` 恰好一个）。
    Plugin,
}

/// 分组聚合的结果。
///
/// `groups` 的每一项是"一个 provider 的可执行结果"，**组间保持注册顺序**
/// （apps → windows → awake → …），组内按分降序。`hints` 是所有 `Interact::Hint`
/// 条目，按 provider 注册顺序排列，UI 只在结果区下方展示它们（不可选）。
///
/// **为什么按 provider 分组而不是全局混排**：方案 A 的另一半。全局混排会把
/// awake 的时长候选插到应用结果中间（用户要搜"微信"，第三行却是"8 小时"），
/// 也会让"同一插件的若干候选"散开。分组后 awake 的条目聚在一起、
/// 应用区仍是应用区，视觉上能一眼看出"这几行来自哪个插件"。
#[derive(Default)]
pub struct Aggregated {
    /// 每个 provider 一组（该 provider 本轮有可执行结果时才有这一组）。
    pub groups: Vec<ProviderGroup>,
    /// 提示行（不参与排序、不可选）。
    pub hints: Vec<Match>,
}

/// 一个 provider 的一组可执行结果（组内已按最终分降序）。
pub struct ProviderGroup {
    pub provider_id: String,
    pub matches: Vec<RankedMatch>,
}

/// 一个 provider 的查询任务：`(provider_id, future)`，由调用方并发驱动
/// （见 [`QueryEngine::dispatch`]）。抽成别名避免签名过度复杂。
type QueryTask<'a> = (
    &'static str,
    Pin<Box<dyn Future<Output = Vec<Match>> + Send + 'a>>,
);

impl QueryEngine {
    pub fn new(providers: Vec<Box<dyn Provider>>) -> Self {
        Self {
            providers,
            frequency: FrequencyStore::new(),
        }
    }

    /// 替换频率数据（app 层从磁盘加载后注入）。
    pub fn set_frequency(&mut self, frequency: FrequencyStore) {
        self.frequency = frequency;
    }

    /// 当前频率表（app 层落盘时取快照）。
    pub fn frequency(&self) -> &FrequencyStore {
        &self.frequency
    }

    /// 按 id **就地**替换一个 provider（没有同 id 时追加到末尾）。
    ///
    /// 供 app 层做「热更新」：开始菜单重扫后重建一个 `AppsProvider`，用它换掉
    /// 旧实例。**命中同 id 时就地替换，保留原注册位置**——分组展示顺序 = 注册
    /// 顺序（apps 恒在最前），原地替换才不会打乱分组。
    ///
    /// 新 provider 的配置是它自己的代码默认值，**调用方应当在替换后再下发一次
    /// [`QueryEngine::apply_config`]**，否则用户改过的 enabled / weight 等会丢。
    ///
    /// 返回是否命中了已有 id（`false` 表示这是一次新增，而非替换）。
    pub fn replace_provider(&mut self, provider: Box<dyn Provider>) -> bool {
        match self.providers.iter_mut().find(|p| p.id() == provider.id()) {
            Some(slot) => {
                *slot = provider;
                true
            }
            None => {
                self.providers.push(provider);
                false
            }
        }
    }

    /// 把 [`AppConfig`] 下发到所有 provider：按 id 取 [`ProviderSettings`]，
    /// 构造 [`ProviderConfig`] 调各 provider 的 `set_config`。
    ///
    /// 找不到对应配置节（理论上 `normalize` 已补满）或配置里没有该 id 的 provider
    /// 时，该 provider 保持原样，不报错。这样配置只描述「差异」，缺省即沿用代码默认值。
    pub fn apply_config(&mut self, cfg: &AppConfig) {
        for provider in self.providers.iter_mut() {
            if let Some(settings) = cfg.providers.get(provider.id()) {
                provider.set_config(ProviderConfig {
                    enabled: settings.enabled,
                    is_global: settings.is_global,
                    weight: settings.weight,
                    prefix: settings.prefix.clone(),
                    // `normalize` 已回填；缺省（未 normalize 的配置）按 Direct 兜底。
                    activation: settings.activation.unwrap_or(Activation::Direct),
                    // `count` 仅在 random 消费；没写（`None`）用默认 5。
                    count: settings.count.unwrap_or(5),
                });
            }
        }
    }

    /// 记录一次使用（选中执行后调用）。
    pub fn record_use(&mut self, key: &str) {
        *self.frequency.entry(key.to_string()).or_insert(0) += 1;
    }

    /// 前缀路由：找出 `query` 开头最长匹配的**专属前缀**持有者。
    ///
    /// **最长匹配优先**（不是一个字符）：`#bZm9v` 同时匹配 `#`（timestamp）与
    /// `#b`（base64），取更长者 base64——否则二级前缀等于没用。
    ///
    /// **激活方式**（[`Activation`]）决定匹配条件：
    /// - `Direct`：`query.starts_with(prefix)`（输入即路由）。
    /// - `Indirect`：`query.starts_with(prefix + ' ')`（前缀后还得有一个空格，
    ///   否则「裸前缀」与 Direct 没区别）。这样 `#` / `#b`（无空格）不路由，
    ///   `# ` / `#b `（带空格）才分别命中 timestamp / base64。
    ///
    /// 只在 `query` 非空时路由；空输入返回 `None`（展示全局常用项）。
    fn prefix_owner(&self, query: &str) -> Option<&dyn Provider> {
        if query.is_empty() {
            return None;
        }
        self.enabled_providers()
            .filter_map(|p| {
                p.config()
                    .prefix
                    .as_deref()
                    .map(|prefix| (prefix, p.config().activation, p))
            })
            .filter(|(prefix, activation, _)| prefix_matches(*activation, prefix, query))
            .max_by_key(|(prefix, _, _)| prefix.chars().count())
            .map(|(_, _, p)| p)
    }

    /// 前缀路由（公开形态，语义与 [`QueryEngine::prepare`] 完全一致）。
    ///
    /// 命中专属前缀 → 只返回那一个 provider（最长前缀优先）；
    /// 否则返回所有全局 provider。空输入也走全局（展示常用项）。
    pub fn route(&self, query: &str) -> Vec<&dyn Provider> {
        match self.prefix_owner(query) {
            Some(provider) => vec![provider],
            None => self.global_providers(),
        }
    }

    /// 参与全局搜索的 provider（enabled && is_global）。
    fn global_providers(&self) -> Vec<&dyn Provider> {
        self.enabled_providers()
            .filter(|p| p.config().is_global)
            .collect()
    }

    /// **插件选择列表**（`#` 入口的唯一数据源）。
    ///
    /// 每个 enabled 的插件一条 [`Match`]：标题 = `Provider::name()`，副标题 =
    /// `Provider::description()`，交互 = [`Interact::EnterSubquery`]（选中的动作
    /// 由 [`Action::ActivatePlugin`] 携带；`EnterSubquery` 的字符串是插件 id，
    /// app 层在进入时把它设成 `active_plugin`）。
    ///
    /// `filter` 是去掉 `#` 与空格后的剩余输入（`"#b "` → `"b"`），按插件
    /// **name 的子串或任一别名子串**过滤（`b` → Base64，`t` → 时间戳）；空过滤器
    /// 列出全部。过滤不中时返回空列表（不回退到全部：列表很短，打错字重打
    /// 比误选成本更低，且错误提示由 app 层的空状态负责）。
    ///
    /// **别名匹配**：name 是中文（"时间戳"），ASCII 输入（`t`/`sj`/`time`）
    /// 永远打不中它——用户实测反馈过。每个插件通过 [`Provider::aliases`]
    /// 声明拼音首字母 / 英文缩写 / 完整英文词，与 name 匹配取**并集**。
    /// 别名是完整列表，不隐含前缀语义：`"ts"` 不会命中只声明了 `"t"` 的插件，
    /// 想让 `ts` 命中就把它写进列表（见 timestamp 的别名单）。
    ///
    /// 分数按列表序递减，保持注册顺序稳定；不查频率表（列表就是全部插件，
    /// 没有排序意义）。
    pub fn plugin_picker_matches(&self, filter: &str) -> Vec<Match> {
        let filter = filter.trim().to_ascii_lowercase();
        self.enabled_providers()
            .enumerate()
            .filter(|(_, p)| {
                if filter.is_empty() {
                    return true;
                }
                let name_matches = p.name().to_ascii_lowercase().contains(&filter);
                // 别名全小写 ASCII（Provider::aliases 的文档约束），直接子串比对。
                let alias_matches = p.aliases().iter().any(|alias| alias.contains(&filter));
                name_matches || alias_matches
            })
            .map(|(i, p)| {
                let prefix = p.config().prefix.as_deref().unwrap_or("");
                Match {
                    provider_id: p.id().to_string(),
                    title: p.name().to_string(),
                    subtitle: p.description().to_string(),
                    icon: None,
                    action: Action::ActivatePlugin {
                        plugin_id: p.id().to_string(),
                        // 前缀带一个空格（`#b `），激活后用户直接打内容；
                        // 无前缀的插件（awake）给空串。
                        prefix: if prefix.is_empty() {
                            String::new()
                        } else {
                            format!("{prefix} ")
                        },
                    },
                    // 递减分保持注册顺序（组内 rank 会再排一次，但顺序不变）。
                    score: 1.0 - i as f32 * 0.02,
                    interact: Interact::EnterSubquery(p.id().to_string()),
                }
            })
            .collect()
    }

    /// 所有启用的 provider（注册顺序）。
    fn enabled_providers(&self) -> impl Iterator<Item = &dyn Provider> {
        self.providers
            .iter()
            .filter(|p| p.config().enabled)
            .map(|p| p.as_ref())
    }

    /// 查一个 provider（按 id），只返回启用的。
    pub fn provider(&self, id: &str) -> Option<&dyn Provider> {
        self.enabled_providers().find(|p| p.id() == id)
    }

    /// 该插件（按 id）的查询是否**每次执行都产出新值**（random / uuid /
    /// timestamp，见 [`Provider::regenerates_on_query`]）。
    ///
    /// app 层在"输入没变却想重跑查询"的路径（应用列表热更新）上用它决定要不要
    /// 跳过：重掷类插件一跳，用户已复制/正要复制的值才不会被悄悄换掉。
    ///
    /// 插件不存在或已禁用时返回 `false`——那种情况下根本没有可被换掉的值，
    /// 按"可以重跑"处理。
    pub fn plugin_regenerates_on_query(&self, id: &str) -> bool {
        self.provider(id).is_some_and(|p| p.regenerates_on_query())
    }

    /// 第一个专属前缀命中 `query` 的启用 provider，与 [`QueryEngine::route`] 同一判据。
    ///
    /// app 层用它把"用户直接打了前缀"翻译成"现在在哪个插件里"。
    pub fn provider_by_prefix(&self, query: &str) -> Option<&dyn Provider> {
        self.prefix_owner(query)
    }

    /// 当前模式的输入框占位符（app 层在激活/进子模式/退出时调用）。
    pub fn placeholder(&self, plugin_id: Option<&str>, sub_mode: Option<&str>) -> &'static str {
        placeholder_for(plugin_id.and_then(|id| self.provider(id)), sub_mode)
    }

    /// 路由 + 剥前缀，一次拿全（无激活插件时的常规路径）。
    ///
    /// 这是前缀语义的**唯一出口**：
    /// - `"=1+1"` → `providers = [calculator]`，`ctx.query = "1+1"`
    /// - `"= 1+1"` → 同上（前缀后允许一个空格）
    /// - `"#bZm9v"` → `providers = [base64]`，`ctx.query = "Zm9v"`（最长前缀优先）
    /// - `"weixin"` → 所有 `is_global` 且 enabled 的 provider，`ctx.query` = 原文
    /// - `""` → 所有全局 provider（展示常用项），`ctx.query = ""`
    ///
    /// 命中与否还取决于该前缀的 [`Activation`]：Indirect 前缀要求后面带一个空格
    /// （如默认的 `#` 系），所以 `"#bZm9v"` 只在 base64 为 Direct 时命中；
    /// Indirect 下要写成 `"#b Zm9v"`。剥前缀后 `ctx.query` 一律不含前缀与那个空格。
    pub fn prepare(&self, raw: &str) -> Routing<'_> {
        let owner = self.prefix_owner(raw);
        match owner {
            Some(provider) => Routing {
                providers: vec![provider],
                ctx: QueryCtx::new(
                    raw,
                    strip_one_prefix(raw, provider.config().prefix.as_deref().unwrap()),
                ),
                route: RouteKind::Plugin,
            },
            None => Routing {
                providers: self.global_providers(),
                ctx: QueryCtx::new(raw, raw),
                route: RouteKind::Global,
            },
        }
    }

    /// 带**激活插件 + 子模式**的路由（方案 A 的主入口）。
    ///
    /// 优先级（简单到可以预见，没有 M5 那些"什么情况算面板"的特判）：
    ///
    /// 1. **插件激活**（`active_plugin` 有值且确实存在）：只查该插件。
    ///    输入框里可能还残留着它自己的前缀（用户手打的，或激活时写入的），
    ///    前缀被剥掉；子模式透传进 [`QueryCtx::sub_mode`]。
    /// 2. **常规路径**：无激活插件时的前缀路由 / 全局搜索（见 [`QueryEngine::prepare`]）。
    ///
    /// `active_plugin` 是失效 id（插件被删/禁用）时自动落到常规路径，
    /// 不把查询卡死。
    ///
    /// **子模式与激活插件的关系**：`sub_mode` 只在激活插件存在时才有意义
    /// （它是插件的内部状态）。无论是否进了子模式，**前缀都会被剥掉**
    /// ——sub_mode 下用户输的是纯内容（如 `hello`），不是 `#b hello`。
    pub fn prepare_in_mode(
        &self,
        raw: &str,
        active_plugin: Option<&str>,
        sub_mode: Option<&str>,
    ) -> Routing<'_> {
        if let Some(provider) = active_plugin.and_then(|id| self.provider(id)) {
            let query = match provider.config().prefix.as_deref() {
                Some(prefix) => strip_one_prefix(raw, prefix),
                None => raw.to_string(),
            };
            return Routing {
                providers: vec![provider],
                ctx: QueryCtx::with_sub_mode(raw, query, sub_mode),
                route: RouteKind::Plugin,
            };
        }
        self.prepare(raw)
    }

    /// 收集所有目标 provider 的查询任务，交给调用方并发驱动。
    ///
    /// 返回 `(provider_id, future)`，调用方用自己的 executor 驱动（app 层用 gpui
    /// 的 background executor），谁先完成就先渲染谁，实现"慢查询不阻塞快查询"。
    pub fn dispatch<'a>(&'a self, raw: &str) -> Vec<QueryTask<'a>> {
        let Routing { providers, ctx, .. } = self.prepare(raw);
        providers
            .into_iter()
            .map(|p| (p.id(), p.query(&ctx)))
            .collect()
    }

    /// 给一个 provider 的结果打分并降序排序（组内排序）。
    ///
    /// `weight` 来自 provider 配置，`score` 是 provider 给的原始分。
    pub fn rank(&self, provider_id: &str, weight: f32, matches: Vec<Match>) -> Vec<RankedMatch> {
        let mut out: Vec<RankedMatch> = matches
            .into_iter()
            .map(|m| {
                let boost = frequency_boost(
                    self.frequency
                        .get(&frequency_key(provider_id, &m))
                        .copied()
                        .unwrap_or(0),
                );
                let final_score = weight * m.score * boost;
                RankedMatch {
                    inner: m,
                    final_score,
                }
            })
            .collect();
        out.sort_by(|a, b| {
            b.final_score
                .partial_cmp(&a.final_score)
                .unwrap_or(std::cmp::Ordering::Equal)
        });
        out
    }

    /// 分组聚合（方案 A 的排序形态）：**按 provider 分组，组内按分降序，
    /// 组间保持传入顺序**（调用方按注册顺序传，见 `run_query`）。
    ///
    /// - [`Interact::Hint`] 条目被单独抽出（**不进任何组、不参与排序**），
    ///   保持 provider 声明顺序——它们只是说明书
    /// - 一个 provider 若只有提示、没有可执行结果，就不产生组
    ///
    /// 为什么需要它：早先 app 层把所有 provider 的结果全局混排，awake 的时长
    /// 候选会插在应用结果中间；分组后"同一插件的候选"始终相邻，视觉上可辨认。
    pub fn aggregate(&self, batches: Vec<ProviderBatch>) -> Aggregated {
        let mut groups: Vec<ProviderGroup> = Vec::new();
        let mut hints: Vec<Match> = Vec::new();
        for batch in batches {
            let weight = self
                .provider(&batch.provider_id)
                .map(|p| p.config().weight)
                .unwrap_or(1.0);
            let mut executable: Vec<Match> = Vec::new();
            for m in batch.matches {
                if m.interact.is_hint() {
                    hints.push(m);
                } else {
                    executable.push(m);
                }
            }
            if executable.is_empty() {
                continue;
            }
            groups.push(ProviderGroup {
                provider_id: batch.provider_id.clone(),
                matches: self.rank(&batch.provider_id, weight, executable),
            });
        }
        Aggregated { groups, hints }
    }
}

/// 频率统计的键：provider id + 条目标题，足以区分不同来源的同名条目。
fn frequency_key(provider_id: &str, m: &Match) -> String {
    format!("{provider_id}:{}", m.title)
}

/// `query` 是否命中 `prefix`（按激活方式判定）。
///
/// - `Direct`：以 `prefix` 开头即可。
/// - `Indirect`：必须以 `prefix + ' '` 开头——前缀后**恰好一个空格**才算激活。
fn prefix_matches(activation: Activation, prefix: &str, query: &str) -> bool {
    match activation {
        Activation::Direct => query.starts_with(prefix),
        Activation::Indirect => query
            .strip_prefix(prefix)
            .is_some_and(|rest| rest.starts_with(' ')),
    }
}

/// 剥掉开头的 `prefix`，以及其后的一个可选空格（`"= 1+1"` → `"1+1"`）。
///
/// 前缀不匹配时原样返回（插件激活后用户把前缀删了的情况）。
fn strip_one_prefix(raw: &str, prefix: &str) -> String {
    let Some(rest) = raw.strip_prefix(prefix) else {
        return raw.to_string();
    };
    rest.strip_prefix(' ').unwrap_or(rest).to_string()
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::action::Action;
    use crate::provider::{BoxFuture, ProviderConfig, ready};

    /// 测试用 provider：把 `(标题, 分数)` 列表原样返回成 `Execute` 条目。
    ///
    /// `hints` / `sub_modes` 用于覆盖 `Hint` 与 `EnterSubquery` 两种交互类型
    /// （它们在聚合阶段有特殊处理，必须能被单测盯住）。
    struct Dummy {
        id: &'static str,
        config: ProviderConfig,
        items: Vec<(&'static str, f32)>,
        hints: Vec<&'static str>,
        sub_modes: Vec<(&'static str, &'static str)>,
        /// 插件列表过滤的别名（[`Provider::aliases`]），默认空。
        aliases: &'static [&'static str],
    }

    impl Provider for Dummy {
        fn id(&self) -> &'static str {
            self.id
        }
        fn name(&self) -> &'static str {
            self.id
        }
        fn aliases(&self) -> &'static [&'static str] {
            self.aliases
        }
        fn description(&self) -> &'static str {
            "测试用 provider"
        }
        fn config(&self) -> &ProviderConfig {
            &self.config
        }
        fn query(&self, _ctx: &QueryCtx) -> BoxFuture<'static, Vec<Match>> {
            let id = self.id;
            let mut matches: Vec<Match> = self
                .items
                .iter()
                .map(|(title, score)| Match::execute(id, *title, "", Action::None, *score))
                .collect();
            for title in &self.hints {
                matches.push(Match::hint(id, *title, ""));
            }
            for (title, mode) in &self.sub_modes {
                matches.push(Match::subquery(id, *title, "", mode, 0.9));
            }
            ready(matches)
        }
    }

    fn dummy(
        id: &'static str,
        weight: f32,
        prefix: Option<&'static str>,
        items: Vec<(&'static str, f32)>,
    ) -> Box<dyn Provider> {
        dummy_with_activation(id, weight, prefix, Activation::Direct, items)
    }

    /// 与 [`dummy`] 同，但显式指定激活方式（测 Indirect 路由用）。
    fn dummy_with_activation(
        id: &'static str,
        weight: f32,
        prefix: Option<&'static str>,
        activation: Activation,
        items: Vec<(&'static str, f32)>,
    ) -> Box<dyn Provider> {
        Box::new(Dummy {
            id,
            config: ProviderConfig {
                enabled: true,
                is_global: true,
                weight,
                prefix: prefix.map(str::to_string),
                activation,
                count: 5,
            },
            items,
            hints: Vec::new(),
            sub_modes: Vec::new(),
            aliases: &[],
        })
    }

    fn dummy_with_hints(
        id: &'static str,
        is_global: bool,
        items: Vec<(&'static str, f32)>,
        hints: Vec<&'static str>,
    ) -> Box<dyn Provider> {
        Box::new(Dummy {
            id,
            config: ProviderConfig {
                enabled: true,
                is_global,
                weight: 1.0,
                prefix: None,
                activation: Activation::Direct,
                count: 5,
            },
            items,
            hints,
            sub_modes: Vec::new(),
            aliases: &[],
        })
    }

    #[test]
    fn frequency_boost_grows_with_use() {
        assert_eq!(frequency_boost(0), 1.0);
        assert!(frequency_boost(1) > 1.0);
        assert!(frequency_boost(10) > frequency_boost(1));
        // 对数增长：100 次不会是 1 次的 100 倍。
        assert!(frequency_boost(100) < frequency_boost(1) * 20.0);
    }

    /// 频率表必须能落盘又原样读回来——否则"越用越准"每次重启都归零。
    #[test]
    fn frequency_survives_a_toml_round_trip() {
        let mut store = FrequencyStore::new();
        store.insert("apps:微信".to_string(), 7);
        store.insert("calc:1+1".to_string(), 2);

        let text = frequency_to_toml(&store).expect("序列化频率表");
        let back = frequency_from_toml(&text);
        assert_eq!(back, store);
    }

    /// 落盘顺序稳定：同一个表序列化两次必须一字不差（否则每次执行都会重写整个文件）。
    #[test]
    fn frequency_toml_has_stable_key_order() {
        let mut store = FrequencyStore::new();
        for i in 0..32 {
            store.insert(format!("apps:app{i:02}"), i);
        }
        let first = frequency_to_toml(&store).expect("序列化");
        let second = frequency_to_toml(&store).expect("序列化");
        assert_eq!(first, second);
    }

    /// 损坏的频率文件不能让排序整体失效：回落到空表（= 没有加成，等价于首次使用），
    /// 由 app 层在下一次记录时重写。
    #[test]
    fn broken_frequency_file_falls_back_to_empty() {
        assert!(frequency_from_toml("这不是 TOML = = =").is_empty());
        assert!(frequency_from_toml("").is_empty());
        // 值不是 u32（比如被手工改坏）：同样按空表处理。
        assert!(frequency_from_toml("apps:微信 = \"很多次\"").is_empty());
    }

    #[test]
    fn prefix_routes_to_single_provider() {
        let engine = QueryEngine::new(vec![
            dummy("calc", 1.0, Some("="), vec![("1+1", 1.0)]),
            dummy("apps", 1.0, None, vec![("微信", 1.0)]),
        ]);
        let routed: Vec<_> = engine.route("=1+1").iter().map(|p| p.id()).collect();
        assert_eq!(routed, vec!["calc"]);
    }

    #[test]
    fn global_query_hits_all_global_providers() {
        let engine = QueryEngine::new(vec![
            dummy("calc", 1.0, Some("="), vec![]),
            dummy("apps", 1.0, None, vec![]),
        ]);
        let routed: Vec<_> = engine.route("wx").iter().map(|p| p.id()).collect();
        assert_eq!(routed, vec!["calc", "apps"]);
    }

    /// 方案 A 的核心前缀规则：`#` 是命名空间，多个插件共享它时**最长匹配优先**。
    ///
    /// 这条测试盯着一个很容易写错的地方：`#bZm9v` 同时匹配 `#`（timestamp）与
    /// `#b`（base64），必须由 base64 接住，否则二级前缀形同虚设。
    #[test]
    fn longest_prefix_wins() {
        let engine = QueryEngine::new(vec![
            dummy("timestamp", 1.0, Some("#"), vec![]),
            dummy("base64", 1.0, Some("#b"), vec![]),
            dummy("uuid", 1.0, Some("#u"), vec![]),
            dummy("random", 1.0, Some("#r"), vec![]),
        ]);

        let owner = |raw: &str| engine.provider_by_prefix(raw).map(|p| p.id());
        assert_eq!(
            owner("#1700000000"),
            Some("timestamp"),
            "裸 `#` 归 timestamp"
        );
        assert_eq!(owner("#bZm9v"), Some("base64"));
        assert_eq!(owner("#u"), Some("uuid"));
        assert_eq!(owner("#r 1-100"), Some("random"));
        // 前缀后允许一个空格；空格不算内容。
        assert_eq!(owner("#b Zm9v"), Some("base64"));
        // 不是前缀的输入没有 owner。
        assert_eq!(owner("weixin"), None);
        assert_eq!(owner(""), None);
    }

    /// Indirect 激活：只有「前缀 + 恰好一个空格」才路由，否则跟没前缀一样。
    ///
    /// 这条盯着「裸前缀不得路由」——否则 `#` / `#b` 一输入就进插件，与 Direct
    /// 毫无区别，也会跟 `#` 插件列表入口抢输入。
    #[test]
    fn indirect_prefix_requires_trailing_space() {
        let engine = QueryEngine::new(vec![dummy_with_activation(
            "timestamp",
            1.0,
            Some("#"),
            Activation::Indirect,
            vec![("ts", 1.0)],
        )]);
        let owner = |raw: &str| engine.provider_by_prefix(raw).map(|p| p.id());

        // 无空格（含"裸前缀"与"前缀+内容"）：不路由。
        assert_eq!(owner("#"), None, "只有前缀、还没空格 → 不激活");
        assert_eq!(owner("#1700000000"), None, "没打空格不算激活");
        // 有空格：路由。
        assert_eq!(owner("# "), Some("timestamp"));
        assert_eq!(owner("# 1700000000"), Some("timestamp"));

        // 剥前缀：`# ` → 空查询；`# 2024` → "2024"。
        let routing = engine.prepare("# ");
        assert_eq!(routing.providers[0].id(), "timestamp");
        assert_eq!(routing.ctx.query, "", "只有前缀 + 空格时查询为空");
        assert_eq!(routing.route, RouteKind::Plugin);

        let routing = engine.prepare("# 2024");
        assert_eq!(routing.ctx.query, "2024", "剥掉 `#` 与其后的一个空格");
        assert_eq!(routing.ctx.raw, "# 2024", "raw 始终是原文");

        // 无空格的 `#` 退回全局搜索，不激活插件。
        let routing = engine.prepare("#");
        assert_eq!(
            routing.route,
            RouteKind::Global,
            "裸 `#` 不激活 Indirect 插件"
        );
    }

    /// Indirect 下最长匹配仍然优先：`#b ` 归 base64，`# ` 归 timestamp。
    #[test]
    fn indirect_longest_prefix_still_wins() {
        let engine = QueryEngine::new(vec![
            dummy_with_activation("timestamp", 1.0, Some("#"), Activation::Indirect, vec![]),
            dummy_with_activation("base64", 1.0, Some("#b"), Activation::Indirect, vec![]),
        ]);
        let owner = |raw: &str| engine.provider_by_prefix(raw).map(|p| p.id());

        assert_eq!(owner("# "), Some("timestamp"));
        assert_eq!(owner("#b "), Some("base64"), "`#b ` 命中更长的 base64");
        assert_eq!(owner("#b 2024"), Some("base64"));
        // `#b`（无空格）两个都不匹配：timestamp 要 `# `、base64 要 `#b `。
        assert_eq!(owner("#b"), None);

        let routing = engine.prepare("#b Zm9v");
        assert_eq!(routing.providers[0].id(), "base64");
        assert_eq!(routing.ctx.query, "Zm9v", "剥掉 `#b` + 一个空格");
        let routing = engine.prepare("#b 2024");
        assert_eq!(routing.ctx.query, "2024");
    }

    /// Direct 与 Indirect 混用：Direct 的 `=` 无空格也路由（不回归），
    /// Indirect 的 `#` 必须带空格。
    #[test]
    fn direct_and_indirect_coexist() {
        let engine = QueryEngine::new(vec![
            dummy_with_activation("calc", 1.0, Some("="), Activation::Direct, vec![]),
            dummy_with_activation("timestamp", 1.0, Some("#"), Activation::Indirect, vec![]),
        ]);
        let owner = |raw: &str| engine.provider_by_prefix(raw).map(|p| p.id());
        // Direct：输入即路由（`=` / `=1+1` / `= 1+1` 都命中）。
        assert_eq!(owner("="), Some("calc"));
        assert_eq!(owner("=1+1"), Some("calc"));
        assert_eq!(owner("= 1+1"), Some("calc"));
        // Indirect：必须带空格。
        assert_eq!(owner("#"), None);
        assert_eq!(owner("# "), Some("timestamp"));
    }

    /// 最长匹配也体现在 `prepare` 的剥前缀上：剥掉的是**最长那个**前缀。
    #[test]
    fn prepare_strips_the_longest_prefix() {
        let engine = QueryEngine::new(vec![
            dummy("timestamp", 1.0, Some("#"), vec![("ts", 1.0)]),
            dummy("base64", 1.0, Some("#b"), vec![("b64", 1.0)]),
        ]);

        let routing = engine.prepare("#bZm9v");
        assert_eq!(routing.providers[0].id(), "base64");
        assert_eq!(routing.ctx.query, "Zm9v", "剥掉的是 `#b` 而不是 `#`");
        assert_eq!(routing.route, RouteKind::Plugin);
        assert_eq!(routing.ctx.sub_mode, None, "常规路由不带子模式");

        // `=` 前缀 + 一个可选空格（M3 的既有行为，必须保持）。
        let engine2 = QueryEngine::new(vec![dummy("calc", 1.0, Some("="), vec![])]);
        assert_eq!(engine2.prepare("=1+1").ctx.query, "1+1");
        assert_eq!(engine2.prepare("= 1+1").ctx.query, "1+1");
        assert_eq!(engine2.prepare("=").ctx.query, "");
        assert_eq!(engine2.prepare("1+1").ctx.query, "1+1", "无前缀时原文透传");
    }

    /// `prepare` 是 app 层唯一的入口：路由与剥前缀必须一起给出。
    ///
    /// 这条测试盯着 M3 的真实 bug——app 层以前只调 `route`、自己把 `raw` 塞进
    /// `ctx.query`，导致 calculator 收到带 `=` 的原文。
    #[test]
    fn prepare_routes_and_strips_prefix_together() {
        let engine = QueryEngine::new(vec![
            dummy("calc", 1.0, Some("="), vec![("2", 1.0)]),
            dummy("apps", 1.2, None, vec![("微信", 1.0)]),
        ]);

        // 前缀命中：只剩 calculator，且 query 已剥掉 `=`。
        let routing = engine.prepare("=1+1");
        assert_eq!(
            routing.providers.iter().map(|p| p.id()).collect::<Vec<_>>(),
            vec!["calc"]
        );
        assert_eq!(routing.ctx.query, "1+1");
        assert_eq!(routing.ctx.raw, "=1+1", "raw 始终是原文");
        assert_eq!(engine.prepare("= 1+1").ctx.query, "1+1");

        // 无前缀：走全局，query 保持原文。
        let routing = engine.prepare("weixin");
        assert_eq!(routing.route, RouteKind::Global);
        assert_eq!(
            routing.providers.iter().map(|p| p.id()).collect::<Vec<_>>(),
            vec!["calc", "apps"]
        );
        assert_eq!(routing.ctx.query, "weixin");

        // 空输入：走全局，query 为空（展示常用项）。
        let routing = engine.prepare("");
        assert_eq!(routing.providers.len(), 2);
        assert_eq!(routing.ctx.query, "");

        // 孤立的 `=`（后面没内容）：仍然路由到 calculator，query 为空串。
        let routing = engine.prepare("=");
        assert_eq!(routing.providers.len(), 1);
        assert_eq!(routing.ctx.query, "");
    }

    /// 前缀字母不该误伤全局搜索：查询词恰好以某 provider 的前缀开头时按前缀走，
    /// 这是设计要求（§2），但**没有前缀的 provider**（prefix=None）永远不进
    /// 这条分支——否则 "apps" 这类 provider 会把所有输入都吞掉。
    #[test]
    fn providers_without_prefix_never_hijack_routing() {
        let engine = QueryEngine::new(vec![
            dummy("apps", 1.2, None, vec![]),
            dummy("calc", 1.0, Some("="), vec![]),
        ]);
        assert_eq!(engine.prepare("=1").providers.len(), 1);
        assert_eq!(engine.prepare("weixin").providers.len(), 2);
    }

    /// 方案 A 的激活路径：`active_plugin` 有值时**只**查它，且：
    ///
    /// - 输入框里残留的它自己的前缀被剥掉
    /// - 前缀被用户删了也照样能查
    /// - `sub_mode` 原样透传进 `QueryCtx`
    #[test]
    fn activation_routes_to_single_plugin_and_passes_sub_mode() {
        let engine = QueryEngine::new(vec![
            dummy("timestamp", 1.0, Some("#"), vec![("ts", 1.0)]),
            dummy("base64", 1.0, Some("#b"), vec![("b64", 1.0)]),
            dummy("apps", 1.2, None, vec![("微信", 1.0)]),
        ]);

        // 激活 base64，第一级（未选子模式）：列表由 provider 自己决定，
        // 前缀剥掉、sub_mode 为 None。
        let routing = engine.prepare_in_mode("#b Zm9v", Some("base64"), None);
        assert_eq!(
            routing.providers.iter().map(|p| p.id()).collect::<Vec<_>>(),
            vec!["base64"]
        );
        assert_eq!(routing.ctx.query, "Zm9v");
        assert_eq!(routing.ctx.sub_mode, None);

        // 进了 enc 子模式：用户输纯内容，**没有前缀**也能查。
        let routing = engine.prepare_in_mode("hello", Some("base64"), Some("enc"));
        assert_eq!(routing.ctx.query, "hello");
        assert_eq!(routing.ctx.sub_mode.as_deref(), Some("enc"));
        assert_eq!(routing.route, RouteKind::Plugin);

        // 子模式下前缀仍会被剥（用户手打了前缀也不碍事）。
        let routing = engine.prepare_in_mode("#bhello", Some("base64"), Some("dec"));
        assert_eq!(routing.ctx.query, "hello");
        assert_eq!(routing.ctx.sub_mode.as_deref(), Some("dec"));

        // 其它插件的前缀在激活态下不改变路由（active_plugin 优先）。
        let routing = engine.prepare_in_mode("=1+1", Some("base64"), None);
        assert_eq!(routing.providers[0].id(), "base64");
        assert_eq!(routing.ctx.query, "=1+1");
    }

    /// 无前缀插件的激活：输入框留空也能把它查出来。
    #[test]
    fn activation_of_prefixless_plugin_keeps_raw_query() {
        let engine = QueryEngine::new(vec![dummy("awake", 1.0, None, vec![("30 分钟", 1.0)])]);
        let routing = engine.prepare_in_mode("", Some("awake"), None);
        assert_eq!(
            routing.providers.iter().map(|p| p.id()).collect::<Vec<_>>(),
            vec!["awake"]
        );
        assert_eq!(routing.ctx.query, "");
    }

    /// 激活的 id 失效（插件被删/禁用）时退回常规路由，不把查询卡死。
    #[test]
    fn unknown_active_plugin_falls_back_to_normal_routing() {
        let engine = QueryEngine::new(vec![dummy("apps", 1.2, None, vec![])]);
        let routing = engine.prepare_in_mode("weixin", Some("ghost"), None);
        assert_eq!(routing.providers.len(), 1);
        assert_eq!(routing.ctx.query, "weixin");
        assert_eq!(routing.route, RouteKind::Global);
        // 失效激活态 + 前缀输入：前缀路由照常生效。
        let engine2 = QueryEngine::new(vec![
            dummy("calc", 1.0, Some("="), vec![]),
            dummy("apps", 1.2, None, vec![]),
        ]);
        let routing = engine2.prepare_in_mode("=1+1", Some("ghost"), None);
        assert_eq!(routing.providers[0].id(), "calc");
        assert_eq!(routing.ctx.query, "1+1");
    }

    /// `provider_by_prefix` 的优先级必须与 `route` 完全一致（同一批 `max_by_key`），
    /// 否则会出现"结果来自 A 但 UI 令牌显示 B"。
    #[test]
    fn provider_by_prefix_agrees_with_route() {
        let engine = QueryEngine::new(vec![
            dummy("timestamp", 1.0, Some("#"), vec![]),
            dummy("base64", 1.0, Some("#b"), vec![]),
            dummy("calc", 1.0, Some("="), vec![]),
        ]);
        assert_eq!(engine.provider_by_prefix("=1").unwrap().id(), "calc");
        assert_eq!(engine.provider_by_prefix("#bZm9v").unwrap().id(), "base64");
        // `#` 只被 timestamp 声明，裸 `#x` 归它。
        assert_eq!(
            engine.provider_by_prefix("#1700000000").unwrap().id(),
            "timestamp"
        );
        let routed = engine.route("#1700000000");
        assert_eq!(
            routed[0].id(),
            engine.provider_by_prefix("#1700000000").unwrap().id()
        );
        assert!(engine.provider_by_prefix("z").is_none());
    }

    /// 占位符随模式变（方案 A 的要求：用户永远知道该输入什么）。
    #[test]
    fn placeholder_reflects_mode() {
        let engine = QueryEngine::new(vec![dummy("base64", 1.0, Some("#b"), vec![])]);
        // 没有激活插件 → 全局占位符。
        assert_eq!(
            engine.placeholder(None, None),
            crate::provider::GLOBAL_PLACEHOLDER
        );
        // 激活 base64：测试 Dummy 用默认实现。
        assert_eq!(engine.placeholder(Some("base64"), None), "输入查询词");
        assert_eq!(
            engine.placeholder(Some("ghost"), None),
            crate::provider::GLOBAL_PLACEHOLDER
        );
    }

    /// **方案 A 的排序形态**：按 provider 分组，组内按分降序，组间保持传入顺序。
    #[test]
    fn aggregate_groups_by_provider_keeping_provider_order() {
        let engine = QueryEngine::new(vec![
            dummy("apps", 1.2, None, vec![]),
            dummy("windows", 1.0, None, vec![]),
        ]);
        let aggregated = engine.aggregate(vec![
            ProviderBatch {
                provider_id: "apps".into(),
                matches: vec![
                    Match::execute("apps", "微信", "", Action::None, 0.4),
                    Match::execute("apps", "网易云", "", Action::None, 0.9),
                ],
            },
            // windows 的分数整体更高，但**不能**插到 apps 的前面/中间。
            ProviderBatch {
                provider_id: "windows".into(),
                matches: vec![
                    Match::execute("windows", "A", "", Action::None, 0.5),
                    Match::execute("windows", "B", "", Action::None, 1.0),
                ],
            },
        ]);

        let ids: Vec<&str> = aggregated
            .groups
            .iter()
            .map(|g| g.provider_id.as_str())
            .collect();
        assert_eq!(
            ids,
            vec!["apps", "windows"],
            "组间顺序 = 传入顺序（注册顺序）"
        );

        // 组内按最终分降序（apps 权重 1.2）。
        let apps: Vec<&str> = aggregated.groups[0]
            .matches
            .iter()
            .map(|m| m.inner.title.as_str())
            .collect();
        assert_eq!(apps, vec!["网易云", "微信"]);
        let windows: Vec<&str> = aggregated.groups[1]
            .matches
            .iter()
            .map(|m| m.inner.title.as_str())
            .collect();
        assert_eq!(windows, vec!["B", "A"]);

        assert!(aggregated.hints.is_empty());
    }

    /// `Hint` 条目被抽出来、**不进任何组**、也不参与排序。
    #[test]
    fn aggregate_separates_hints_from_selectable_rows() {
        let engine = QueryEngine::new(vec![dummy_with_hints(
            "timestamp",
            false,
            vec![("1700000000", 1.0)],
            vec!["输入 10 位（秒）或 13 位（毫秒）时间戳"],
        )]);
        let batch = ProviderBatch {
            provider_id: "timestamp".into(),
            matches: vec![
                Match::execute("timestamp", "1700000000", "", Action::None, 1.0),
                Match::hint(
                    "timestamp",
                    "输入 10 位（秒）或 13 位（毫秒）时间戳",
                    "例：now",
                ),
            ],
        };
        let aggregated = engine.aggregate(vec![batch]);
        assert_eq!(aggregated.groups.len(), 1);
        assert_eq!(aggregated.groups[0].matches.len(), 1, "提示行不进组");
        assert_eq!(aggregated.hints.len(), 1);
        assert_eq!(
            aggregated.hints[0].title,
            "输入 10 位（秒）或 13 位（毫秒）时间戳"
        );
        assert!(aggregated.hints[0].interact.is_hint());
        assert_eq!(aggregated.hints[0].action, Action::None);
    }

    /// 只有提示、没有可执行结果的 provider 不产生组（否则会出现空分组）。
    #[test]
    fn aggregate_skips_providers_with_only_hints() {
        let engine = QueryEngine::new(vec![dummy("base64", 1.0, Some("#b"), vec![])]);
        let aggregated = engine.aggregate(vec![ProviderBatch {
            provider_id: "base64".into(),
            matches: vec![Match::hint("base64", "怎么用", "例：hello")],
        }]);
        assert!(aggregated.groups.is_empty());
        assert_eq!(aggregated.hints.len(), 1);
    }

    #[test]
    fn weight_and_frequency_affect_ranking() {
        let mut engine = QueryEngine::new(vec![dummy("apps", 2.0, None, vec![])]);
        let matches = vec![Match::execute("apps", "微信", "", Action::None, 0.5)];
        let ranked = engine.rank("apps", 2.0, matches.clone());
        // 2.0 * 0.5 * 1.0 = 1.0
        assert_eq!(ranked[0].final_score, 1.0);

        engine.record_use("apps:微信");
        let ranked2 = engine.rank("apps", 2.0, matches);
        assert!(ranked2[0].final_score > ranked[0].final_score);
    }

    #[test]
    fn ranks_descending() {
        let engine = QueryEngine::new(vec![dummy("apps", 1.0, None, vec![])]);
        let matches: Vec<Match> = (1..=5)
            .map(|i| {
                Match::execute(
                    "apps",
                    format!("item{i}"),
                    "",
                    Action::None,
                    i as f32 / 10.0,
                )
            })
            .collect();
        let ranked = engine.rank("apps", 1.0, matches);
        let scores: Vec<f32> = ranked.iter().map(|r| r.final_score).collect();
        assert_eq!(scores, vec![0.5, 0.4, 0.3, 0.2, 0.1]);
    }

    /// `dispatch` 走的是 `prepare`（无激活插件），并且 provider 的 future 立即就绪。
    #[test]
    fn dispatch_runs_ready_futures() {
        let engine = QueryEngine::new(vec![dummy("calc", 1.0, Some("="), vec![("2", 1.0)])]);
        let dispatched = engine.dispatch("=1+1");
        assert_eq!(dispatched.len(), 1);
        let (id, future) = dispatched.into_iter().next().unwrap();
        assert_eq!(id, "calc");
        let matches = futures_lite_block_on(future);
        assert_eq!(matches[0].title, "2");
        assert_eq!(matches[0].interact, crate::provider::Interact::Execute);
    }

    /// 测试里最小的同步执行器：只在测试用，避免引入完整 runtime。
    fn futures_lite_block_on<F: Future>(future: F) -> F::Output {
        // provider 都是立即就绪的，poll 一次即可。
        let mut f = Box::pin(future);
        let mut cx = std::task::Context::from_waker(std::task::Waker::noop());
        match f.as_mut().poll(&mut cx) {
            std::task::Poll::Ready(v) => v,
            std::task::Poll::Pending => panic!("测试里的 provider 应当立即就绪"),
        }
    }

    /// `#` 插件列表：裸 `#` 列出全部 enabled 插件，每条都是
    /// `EnterSubquery(插件id)` + `ActivatePlugin`，且保持注册顺序。
    #[test]
    fn plugin_picker_lists_all_enabled_providers_in_order() {
        let engine = QueryEngine::new(vec![
            dummy("apps", 1.0, None, vec![]),
            dummy("calculator", 1.0, Some("="), vec![]),
            dummy("base64", 1.0, Some("#b"), vec![]),
            Box::new(Dummy {
                id: "disabled",
                config: ProviderConfig {
                    enabled: false,
                    is_global: true,
                    weight: 1.0,
                    prefix: None,
                    activation: Activation::Direct,
                    count: 5,
                },
                items: vec![],
                hints: vec![],
                sub_modes: vec![],
                aliases: &[],
            }),
        ]);

        let matches = engine.plugin_picker_matches("");
        let titles: Vec<&str> = matches.iter().map(|m| m.title.as_str()).collect();
        assert_eq!(
            titles,
            vec!["apps", "calculator", "base64"],
            "禁用的不出现，保持注册顺序"
        );

        for m in &matches {
            assert_eq!(
                m.interact,
                Interact::EnterSubquery(m.provider_id.clone()),
                "{}",
                m.title
            );
            let Action::ActivatePlugin { plugin_id, prefix } = &m.action else {
                panic!(
                    "{} 的动作必须是 ActivatePlugin，实际 {:?}",
                    m.title, m.action
                );
            };
            assert_eq!(plugin_id, &m.provider_id);
            match m.provider_id.as_str() {
                "calculator" => assert_eq!(prefix, "= ", "有前缀的插件带上前缀 + 空格"),
                "base64" => assert_eq!(prefix, "#b "),
                "apps" => assert_eq!(prefix, "", "无前缀的插件给空串"),
                _ => {}
            }
            // 副标题 = 描述（Dummy 固定返回"测试用 provider"）。
            assert!(!m.subtitle.is_empty());
        }
    }

    /// `#` 列表按 name 过滤：前缀与子串都命中，不区分大小写。
    #[test]
    fn plugin_picker_filters_by_name() {
        let engine = QueryEngine::new(vec![
            dummy("calculator", 1.0, Some("="), vec![]),
            dummy("base64", 1.0, Some("#b"), vec![]),
            dummy("timestamp", 1.0, Some("#"), vec![]),
        ]);
        // Dummy 的 name() 返回 id 本身，所以这里用 id 过滤。
        let got = |filter: &str| -> Vec<String> {
            engine
                .plugin_picker_matches(filter)
                .iter()
                .map(|m| m.provider_id.clone())
                .collect()
        };
        assert_eq!(got("b"), vec!["base64"]);
        assert_eq!(got("B"), vec!["base64"], "不区分大小写");
        assert_eq!(got("calc"), vec!["calculator"]);
        assert_eq!(got("alc"), vec!["calculator"], "子串也命中");
        assert_eq!(got("time"), vec!["timestamp"]);
        assert!(got("zzz").is_empty(), "过滤不中返回空列表，不回退到全部");
        assert_eq!(got("").len(), 3, "空过滤器列出全部");
    }

    /// **别名过滤**（用户实测反馈：name 是中文，`t` 打不中"时间戳"）。
    ///
    /// 过滤词命中 name **或任一别名**都算命中：`t`/`sj`/`time` 过滤出 timestamp，
    /// `sj` 同时命中"时间戳"与"随机值"（时间/随机的拼音首字母同形）。
    #[test]
    fn plugin_picker_filters_by_alias() {
        let make =
            |id: &'static str, prefix: Option<&'static str>, aliases: &'static [&'static str]| {
                Box::new(Dummy {
                    id,
                    config: ProviderConfig {
                        enabled: true,
                        is_global: true,
                        weight: 1.0,
                        prefix: prefix.map(str::to_string),
                        activation: Activation::Direct,
                        count: 5,
                    },
                    items: vec![],
                    hints: vec![],
                    sub_modes: vec![],
                    aliases,
                }) as Box<dyn Provider>
            };
        let engine = QueryEngine::new(vec![
            make("timestamp", Some("#"), &["t", "sj", "ts", "time"]),
            make("random", Some("#r"), &["r", "sj", "rand"]),
            make("calculator", Some("="), &[]),
        ]);

        let got = |filter: &str| -> Vec<String> {
            engine
                .plugin_picker_matches(filter)
                .iter()
                .map(|m| m.provider_id.clone())
                .collect()
        };
        // `t` → timestamp（别名）+ calculator（Dummy 的 name = id，含 t）。
        assert_eq!(got("t"), vec!["timestamp", "calculator"]);
        // `sj` → 时间 + 随机（两个插件的拼音首字母同形；仅别名路径能命中）。
        assert_eq!(got("sj"), vec!["timestamp", "random"]);
        // `ts` → timestamp；`rand` → random。
        assert_eq!(got("ts"), vec!["timestamp"]);
        assert_eq!(got("rand"), vec!["random"]);
        assert!(got("zzz").is_empty(), "谁都不过滤中");
        // 别名是完整列表、子串匹配：别名 "ts" 能被 "t" 过滤命中，但
        // 只声明了 "ts" 的插件不被 "time" 命中。
        let engine2 = QueryEngine::new(vec![make("x", None, &["ts"])]);
        let got2 = |filter: &str| -> Vec<String> {
            engine2
                .plugin_picker_matches(filter)
                .iter()
                .map(|m| m.provider_id.clone())
                .collect()
        };
        assert_eq!(got2("t"), vec!["x"], "别名子串匹配：t 命中 ts");
        assert!(got2("time").is_empty(), "time 不是 ts 的子串");
        assert_eq!(got2("TS"), vec!["x"], "过滤词小写化，别名也应小写比对");
    }

    /// 列表选中（ActivatePlugin）与既有 `prepare_in_mode` 的衔接：
    /// 激活后输入框被写成前缀（如 `#b `），prepare_in_mode 必须把前缀剥掉。
    #[test]
    fn picker_activation_feeds_prepare_in_mode() {
        let engine = QueryEngine::new(vec![
            dummy("apps", 1.0, None, vec![]),
            dummy("base64", 1.0, Some("#b"), vec![]),
        ]);
        // 模拟 app 层：从列表拿到 ActivatePlugin，用它的 prefix 与 plugin_id。
        let matches = engine.plugin_picker_matches("b");
        let Action::ActivatePlugin { plugin_id, prefix } = &matches[0].action else {
            panic!("列表条目必须是 ActivatePlugin");
        };
        assert_eq!(plugin_id, "base64");
        assert_eq!(prefix, "#b ");

        // 激活后用户输入 `#b hello`：前缀被剥、只查 base64。
        let routing = engine.prepare_in_mode("#b hello", Some("base64"), None);
        assert_eq!(routing.providers[0].id(), "base64");
        assert_eq!(routing.ctx.query, "hello");
    }

    /// `#` 开头的输入在**未激活**状态下不再路由到任何插件（列表接管了它）；
    /// 已激活插件的剥前缀行为不变（列表不影响激活后的路径）。
    #[test]
    fn picker_is_query_path_orthogonal_to_prefix_routing() {
        let engine = QueryEngine::new(vec![
            dummy("timestamp", 1.0, Some("#"), vec![]),
            dummy("apps", 1.0, None, vec![]),
        ]);
        // 未激活：`#` 开头依然命中前缀路由（引擎层保持通用，UI 层拦它）。
        // 这里只验证引擎自己的一致性：provider_by_prefix 与 prepare 同源。
        assert_eq!(
            engine.provider_by_prefix("#x").map(|p| p.id()),
            Some("timestamp")
        );
        // 已激活 timestamp 时，`#` 开头的输入照常被查（剥前缀后交给插件）。
        let routing = engine.prepare_in_mode("#1700000000", Some("timestamp"), None);
        assert_eq!(routing.providers[0].id(), "timestamp");
        assert_eq!(routing.ctx.query, "1700000000");
    }

    /// 阶段 1 的防漏改测试：把 10 个**真实** provider 建出来，`apply_config` 下发
    /// `enabled=false` 后逐个断言真的生效。任何一个 provider 忘了覆盖
    /// `set_config`（仍走 trait 默认空实现），这条测试就会挂——正好拦截文档要求的
    /// 「防漏改」目标。
    #[test]
    fn all_providers_honor_set_config() {
        use crate::apps::AppsProvider;
        use crate::awake::AwakeProvider;
        use crate::base64::Base64Provider;
        use crate::calculator::CalculatorProvider;
        use crate::random::RandomProvider;
        use crate::terminal::TerminalProvider;
        use crate::timestamp::TimestampProvider;
        use crate::uuid::UuidProvider;
        use crate::windows::WindowsProvider;
        use crate::workspace::WorkspaceProvider;
        use std::sync::Arc;

        let providers: Vec<Box<dyn Provider>> = vec![
            Box::new(AppsProvider::new(vec![])),
            Box::new(WindowsProvider::new(Arc::new(Vec::new))),
            Box::new(AwakeProvider::new(Arc::new(|| None))),
            Box::new(TerminalProvider::new(Arc::new(Vec::new), "")),
            Box::new(WorkspaceProvider::new(Arc::new(Vec::new), None, None)),
            Box::new(CalculatorProvider::new()),
            Box::new(TimestampProvider::new()),
            Box::new(Base64Provider::new()),
            Box::new(UuidProvider::new()),
            Box::new(RandomProvider::new()),
        ];

        let mut engine = QueryEngine::new(providers);
        // 默认表把所有 enabled 设成 false，构造一份「全部禁用」的配置。
        let mut cfg = AppConfig::default();
        for s in cfg.providers.values_mut() {
            s.enabled = false;
        }
        engine.apply_config(&cfg);

        // 逐个断言 10 个 provider 都真的被 set_config 改到了 enabled=false。
        assert_eq!(engine.providers.len(), 10);
        for p in &engine.providers {
            assert!(
                !p.config().enabled,
                "{} 未响应 set_config（忘了覆盖）",
                p.id()
            );
        }
    }

    /// 「唤起不重算」判据表（用户实测 UX）：10 个真实 provider 里哪些"同一条
    /// 查询重跑一次就把用户可复制的内容换掉"。random / uuid / timestamp 为
    /// true，其余（含确定性的 base64 / calculator）为 false。app 层靠它在
    /// 应用列表热更新等"输入没变却重跑"的路径上跳过重掷类插件；规则漂移
    /// （新增插件忘了表态）立刻在这里红。
    #[test]
    fn plugin_regenerates_on_query_covers_all_builtin_providers() {
        use crate::apps::AppsProvider;
        use crate::awake::AwakeProvider;
        use crate::base64::Base64Provider;
        use crate::calculator::CalculatorProvider;
        use crate::random::RandomProvider;
        use crate::terminal::TerminalProvider;
        use crate::timestamp::TimestampProvider;
        use crate::uuid::UuidProvider;
        use crate::windows::WindowsProvider;
        use crate::workspace::WorkspaceProvider;
        use std::sync::Arc;

        let providers: Vec<Box<dyn Provider>> = vec![
            Box::new(AppsProvider::new(vec![])),
            Box::new(WindowsProvider::new(Arc::new(Vec::new))),
            Box::new(AwakeProvider::new(Arc::new(|| None))),
            Box::new(TerminalProvider::new(Arc::new(Vec::new), "")),
            Box::new(WorkspaceProvider::new(Arc::new(Vec::new), None, None)),
            Box::new(CalculatorProvider::new()),
            Box::new(TimestampProvider::new()),
            Box::new(Base64Provider::new()),
            Box::new(UuidProvider::new()),
            Box::new(RandomProvider::new()),
        ];
        let engine = QueryEngine::new(providers);

        // (provider id, 该插件的查询是否每次执行都产出新值)
        let cases: &[(&str, bool)] = &[
            ("apps", false),
            ("windows", false),
            ("awake", false),
            ("terminal", false),
            ("workspaces", false),
            ("calculator", false),
            ("base64", false),
            ("timestamp", true),
            ("uuid", true),
            ("random", true),
        ];
        for (id, expected) in cases {
            assert_eq!(
                engine.plugin_regenerates_on_query(id),
                *expected,
                "规则漂移：{id} 的重掷分类变了"
            );
        }
        // 不存在的插件：没有值可被换掉，按"可以重跑"处理。
        assert!(!engine.plugin_regenerates_on_query("nonexistent"));
    }

    /// 热更新用：`replace_provider` 命中同 id 时就地换、保留注册位置，数据也真的换了。
    ///
    /// 这条盯着两件事：①同 id 换掉后**不改变注册顺序**（分组顺序 = 注册顺序，
    /// apps 必须还在最前）；②换上去的是新实例的数据，不是旧的。
    #[test]
    fn replace_provider_swaps_in_place_and_uses_new_data() {
        let mut engine = QueryEngine::new(vec![
            dummy("apps", 1.0, None, vec![("旧应用", 1.0)]),
            dummy("windows", 1.0, None, vec![("窗口", 1.0)]),
        ]);

        let replaced = engine.replace_provider(dummy("apps", 1.0, None, vec![("新应用", 1.0)]));
        assert!(replaced, "命中同 id 应返回 true");
        assert_eq!(engine.providers.len(), 2, "就地替换不新增条目");
        assert_eq!(
            engine.route("x").iter().map(|p| p.id()).collect::<Vec<_>>(),
            vec!["apps", "windows"],
            "替换后注册顺序必须保持"
        );

        // 换上去的 provider 真的提供了新数据。
        let provider = engine.provider("apps").expect("apps 仍存在");
        let mut future = provider.query(&QueryCtx::new("", ""));
        let mut cx = std::task::Context::from_waker(std::task::Waker::noop());
        let matches = match future.as_mut().poll(&mut cx) {
            std::task::Poll::Ready(v) => v,
            std::task::Poll::Pending => panic!("dummy provider 应当立即就绪"),
        };
        assert_eq!(matches[0].title, "新应用");
    }

    /// 没有同 id 时追加到末尾并返回 `false`（允许调用方据返回值判断是新增）。
    #[test]
    fn replace_provider_appends_when_id_is_new() {
        let mut engine = QueryEngine::new(vec![dummy("apps", 1.0, None, vec![])]);
        let replaced = engine.replace_provider(dummy("windows", 1.0, None, vec![]));
        assert!(!replaced, "新 id 应返回 false");
        assert_eq!(
            engine.route("").iter().map(|p| p.id()).collect::<Vec<_>>(),
            vec!["apps", "windows"]
        );
    }
}
