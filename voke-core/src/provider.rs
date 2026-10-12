//! Provider 契约与查询上下文（§3）。
//!
//! 与文档的两点偏离，都是为了让 core 保持零 gpui 依赖（§1）：
//!
//! 1. `query` 返回 `BoxFuture<'static, Vec<Match>>` 而不是 gpui 的 `Task<Vec<Match>>`。
//!    app 层用 gpui 的 background executor 把这个 future spawn 成 `Task`。
//!    这样 core 的测试循环仍然不碰 GPUI 编译。
//! 2. 去掉 `execute(&self, item, ctx: &ActionCtx)`。执行意图以 [`Action`] 数据的形式
//!    挂在 [`Match`] 上，由 app 层统一 dispatch——避免把 UI 能力抽象成 trait 塞进 core。

use std::future::Future;
use std::pin::Pin;

use crate::action::Action;
use crate::config::Activation;

/// `query` 的返回类型：一个可发送的异步结果。
///
/// 用 `BoxFuture` 而不是 `async fn` 是为了让 trait 保持 object-safe（`dyn Provider`）。
pub type BoxFuture<'a, T> = Pin<Box<dyn Future<Output = T> + Send + 'a>>;

/// 一条结果的**交互类型**（方案 A 的核心）。
///
/// 根因：早先的 UI 只有"一行 = 一个可执行候选"这一种概念，于是"说明书"和
/// "进入下一级"都被硬塞进同一种行里，行为只能靠 `Action` 的变体猜。
/// 交互类型把它显式化：**行是什么、Enter/点击该干什么**由 provider 在查询期
/// 声明，UI 只按类型分发，不再有"面板行特判"这种例外。
///
/// 纯数据枚举，零 gpui/平台依赖（§1 的分层约束）。
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Interact {
    /// 执行动作（复制 / 打开应用 / 切换窗口 / 设置 awake）。
    ///
    /// **Enter 和鼠标点击都执行**（两条路径语义一致）。
    Execute,
    /// 进入下一级子模式（如 base64 的加密 / 解密）。
    ///
    /// 字符串是**该子模式的标识**（`"enc"` / `"dec"`），随后的查询把它通过
    /// [`QueryCtx::sub_mode`] 传回 provider。**Enter 和鼠标点击都进入**。
    EnterSubquery(String),
    /// 说明书 / 提示。
    ///
    /// **不渲染成可选行**：UI 既不把它放进键盘导航，也不给点击处理，只在结果区
    /// 下方（或空状态区）以弱化文字展示。用它的地方是各插件的"怎么用"文案
    /// ——以前那些文案被当成候选行，用户按 Enter 毫无反应，是"反人类"的主要来源。
    Hint,
}

impl Interact {
    /// 是否是提示行（不可选、不可点）。
    pub fn is_hint(&self) -> bool {
        matches!(self, Interact::Hint)
    }

    /// 是否可以被键盘导航 / 鼠标点击选中。
    pub fn is_selectable(&self) -> bool {
        !self.is_hint()
    }
}

/// 一条候选结果。
#[derive(Clone, Debug)]
pub struct Match {
    /// Provider 的 id，便于聚合时区分来源。
    pub provider_id: String,
    /// 主标题（应用名、窗口标题、计算结果…）。
    pub title: String,
    /// 副标题/描述，可为空。
    pub subtitle: String,
    /// 图标路径（本地文件），可为空。
    ///
    /// **为什么是 `Option<String>` 而不是图片本身**：core 不能依赖 gpui
    /// （§1 的"core 秒级测试循环"），也不该持有解码后的位图。这里只携带
    /// 一个由 sys 层落盘的 PNG 路径，app 层的 `img()` 直接吃路径
    /// （gpui-pre 的 `ImageAssetLoader` 支持 `Resource::Path`）。
    ///
    /// `None` 表示"没有图标可用"，UI 应回退到占位图标。
    pub icon: Option<String>,
    /// 选中后要执行的动作。
    ///
    /// `Hint` 行的动作恒为 [`Action::None`]（它根本没有"选中"这回事）。
    pub action: Action,
    /// Provider 给出的原始分（0.0–1.0），聚合时会乘上 provider 权重。
    pub score: f32,
    /// 这条结果的交互类型（见 [`Interact`]）。
    pub interact: Interact,
}

impl Match {
    /// 一条**可执行**结果（最常用形态）：Enter / 点击都执行 `action`。
    pub fn execute(
        provider_id: &str,
        title: impl Into<String>,
        subtitle: impl Into<String>,
        action: Action,
        score: f32,
    ) -> Self {
        Self {
            provider_id: provider_id.to_string(),
            title: title.into(),
            subtitle: subtitle.into(),
            icon: None,
            action,
            score,
            interact: Interact::Execute,
        }
    }

    /// 一条**提示**：不渲染成可选行，只在结果区下方展示。
    pub fn hint(provider_id: &str, title: impl Into<String>, subtitle: impl Into<String>) -> Self {
        Self {
            provider_id: provider_id.to_string(),
            title: title.into(),
            subtitle: subtitle.into(),
            icon: None,
            action: Action::None,
            // 提示行不参与排序（聚合时会被单独抽出来，保持 provider 声明顺序）。
            score: 0.0,
            interact: Interact::Hint,
        }
    }

    /// 一条**进入子模式**的条目：Enter / 点击都进入 `sub_mode`。
    pub fn subquery(
        provider_id: &str,
        title: impl Into<String>,
        subtitle: impl Into<String>,
        sub_mode: &str,
        score: f32,
    ) -> Self {
        Self {
            provider_id: provider_id.to_string(),
            title: title.into(),
            subtitle: subtitle.into(),
            icon: None,
            action: Action::None,
            score,
            interact: Interact::EnterSubquery(sub_mode.to_string()),
        }
    }
}

/// 查询上下文：传给 provider 的输入。
#[derive(Clone, Debug, Default)]
pub struct QueryCtx {
    /// 用户在输入框里的原文（可能还带前缀，如 "=1+1"）。
    pub raw: String,
    /// 已经剥掉前缀的查询词。
    pub query: String,
    /// 当前所处的**插件子模式**（`None` = 插件第一级，或根本不在插件里）。
    ///
    /// 值来自某条 [`Interact::EnterSubquery`] 条目的字符串，由 app 层在用户
    /// Enter/点击该条目时记下并回传（见 `QueryEngine::prepare_in_mode`）。
    pub sub_mode: Option<String>,
}

impl QueryCtx {
    /// 构造一个不带子模式的上下文（绝大多数 provider 单测与调用方用这个）。
    pub fn new(raw: impl Into<String>, query: impl Into<String>) -> Self {
        Self {
            raw: raw.into(),
            query: query.into(),
            sub_mode: None,
        }
    }

    /// 带子模式的上下文。
    pub fn with_sub_mode(
        raw: impl Into<String>,
        query: impl Into<String>,
        sub_mode: Option<&str>,
    ) -> Self {
        Self {
            raw: raw.into(),
            query: query.into(),
            sub_mode: sub_mode.map(str::to_string),
        }
    }
}

/// Provider 的配置（§3 的 TOML 对应结构）。
#[derive(Clone, Debug)]
pub struct ProviderConfig {
    pub enabled: bool,
    /// 是否参与全局搜索（不参与时只能靠前缀触发）。
    pub is_global: bool,
    /// 聚合时的权重，最终分 = weight × match_score × 频率加成。
    pub weight: f32,
    /// 专属触发前缀，如 `"="`、`"#"`、`"#b"`。
    ///
    /// **为什么是 `String` 而不是 `&str`**：方案 A 里 `#` 是"插件命名空间"，
    /// timestamp 直接占 `#`（顺手接住 `#1700000000`），而 base64 / uuid / random
    /// 用 `#b` / `#u` / `#r` 二级前缀避开互相抢占；多个前缀同时命中时
    /// **最长匹配优先**（见 `QueryEngine::prefix_owner`）。单字符类型表达不了这个。
    /// 用 `String` 是为了让前缀能被配置（TOML 里读出来的是 owned 值）覆写，
    /// 编译期 `'static` 字面量只是默认值。
    pub prefix: Option<String>,
    /// 前缀的激活方式（[`Activation::Direct`] = 输入即路由；
    /// [`Activation::Indirect`] = 前缀 + 空格才路由）。
    ///
    /// 路由决策在 engine 的 `prefix_owner` 里做，各 provider 不消费它，只是在
    /// `set_config` 里透传保存。
    pub activation: Activation,
    /// 单次查询生成的随机结果条数（目前仅 `random` 消费），默认 5。
    ///
    /// 走 [`AppConfig::normalize`] 后的 [`crate::config::ProviderSettings::count`]
    /// 回填：`None` → 5。各 provider 在 `set_config` 里透传保存，不消费它的
    /// 直接忽略（保持默认值即可）。
    pub count: u32,
}

impl Default for ProviderConfig {
    fn default() -> Self {
        Self {
            enabled: true,
            is_global: true,
            weight: 1.0,
            prefix: None,
            activation: Activation::Direct,
            count: 5,
        }
    }
}

/// 全局搜索模式的占位符（没有激活插件时）。
///
/// 放在 core 而不是 app：占位符是"当前处于哪种查询模式"的用户可见说明，
/// 与 [`Provider::placeholder`] 是同一套语义的三个分支（全局 / 插件 / 子模式）。
pub const GLOBAL_PLACEHOLDER: &str = "搜索应用、窗口";

/// Provider 契约。
///
/// MVP 用编译期静态注册（`Vec<Box<dyn Provider>>`），不做动态加载（§3）。
/// 未来接 gpui-shell 时这个 trait 保持不变，只换注册方式。
pub trait Provider: Send + Sync {
    /// 稳定 id，例如 "calculator"。用于配置和频率统计的键。
    fn id(&self) -> &'static str;

    /// 人类可读的插件名（子命令面板展示用，例如 "计算器"）。
    ///
    /// 与 [`Provider::id`] 分开：id 是机器键（配置节名、频率表的键，稳定且不可翻译），
    /// name 是给用户看的，可以随时改文案而不影响持久化数据。
    fn name(&self) -> &'static str;

    /// 插件选择列表（`#` 入口）的**额外过滤关键词**（小写 ASCII，如 `"t"`/`"sj"`/`"time"`）。
    ///
    /// 为什么需要它：列表过滤按 name 子串匹配，而 name 是中文（"时间戳"）——
    /// 用户打 `#t` 过滤不出时间戳（用户实测反馈）。别名让每个插件能被
    /// 拼音首字母（`sj`）、英文缩写（`ts`）或完整英文词（`time`）命中，
    /// 与 name 匹配叠加（并集），不改变 name 匹配本身。
    ///
    /// 默认空切片：不需要别名的插件（如 apps，name 本身可被中英文命中）不用实现。
    fn aliases(&self) -> &'static [&'static str] {
        &[]
    }

    /// 一句话描述（子命令面板展示用，说明这个插件是干什么的）。
    fn description(&self) -> &'static str;

    fn config(&self) -> &ProviderConfig;

    /// 用新的配置覆盖当前 provider 的配置（阶段 1：配置内核）。
    ///
    /// 默认空实现：core 测试里的 fake provider（不持有 `ProviderConfig` 字段）
    /// 不需要关心配置，留空即可。`QueryEngine::apply_config` 据此把 TOML 配置
    /// 逐条下发到真实 provider。真实 provider 各自覆盖成 `self.config = config;`。
    fn set_config(&mut self, config: ProviderConfig) {
        let _ = config;
    }

    /// 输入框占位符：用户**已进入本插件**时显示什么提示。
    ///
    /// - `sub_mode == None`：插件第一级（例如 base64 的"选择 加密 或 解密"）
    /// - `sub_mode == Some("enc")`：那一级的提示（"输入要加密的内容"）
    ///
    /// 默认实现给一句通用文案；需要更具体指导的插件（base64 / awake）自己覆盖。
    /// 由 app 层在"激活插件 / 进入子模式 / 退出"这几个时机读取并写进 `InputState`
    /// （`InputState::set_placeholder`），**渲染路径不查引擎**。
    fn placeholder(&self, sub_mode: Option<&str>) -> &'static str {
        let _ = sub_mode;
        "输入查询词"
    }

    /// 异步查询。慢 provider（窗口枚举）不阻塞快 provider 的渲染。
    fn query(&self, ctx: &QueryCtx) -> BoxFuture<'static, Vec<Match>>;
}

/// 把一个同步的查询函数包装成 [`BoxFuture`]，方便 provider 实现。
pub fn ready(matches: Vec<Match>) -> BoxFuture<'static, Vec<Match>> {
    Box::pin(async move { matches })
}
