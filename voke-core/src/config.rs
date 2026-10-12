//! 配置模型（阶段 1：voke-core 配置内核，§4）。
//!
//! 落盘文件是 `%APPDATA%\voke\config.toml`（绝对路径，由 voke-sys 的
//! `config_path()` 给出，自启动时 CWD 不确定，见 docs/HISTORY.md「gnu debug
//! 产物」一节）。本文件只负责**内存里的配置结构 + 解析 / 校验 / 合并**，不碰文件系统。
//!
//! 设计要点：
//! - 读盘走 [`ConfigPatch`]（每字段 `Option`）与 [`AppConfig::default`] 逐字段合并，
//!   保证「只写一项也能工作」——用户只改了一个字段的 TOML 也能被正确加载，
//!   未写字段沿用默认值。
//! - 部分配置之所以必须可用，是因为 provider 的完整 [`ProviderSettings`] 若直接
//!   反序列化，缺省字段会落到 `Option`/`bool`/`f32` 的「零值」（`false`/`0.0`/`None`）
//!   而非各 provider 的**真实**默认值，从而把 calculator 的 `=` 前缀、apps 的 `1.2`
//!   权重等当成没配置而抹掉。`ConfigPatch` 用 `Option` 区分「没写」与「写了」，
//!   只有写了的字段才覆盖默认。

use std::collections::{BTreeMap, HashSet};

use serde::{Deserialize, Deserializer, Serialize};

/// provider 前缀的**激活方式**。
///
/// - [`Activation::Direct`]：输入前缀立刻路由进插件（`=` → calculator）。
/// - [`Activation::Indirect`]：输入「前缀 + 恰好一个空格」才路由
///   （`#` 系插件默认如此，避免与裸 `#` 的插件列表入口冲突）。
///
/// 序列化为小写串（`"direct"` / `"indirect"`），落盘 TOML 可读。
#[derive(Serialize, Deserialize, Clone, Copy, Debug, PartialEq, Eq)]
#[serde(rename_all = "lowercase")]
pub enum Activation {
    Direct,
    Indirect,
}

/// 按 provider 的（生效）前缀判定默认激活方式：以 `#` 开头 → Indirect。
///
/// 为什么以 `#` 为界：裸 `#` 是插件选择列表的入口（见 `search.rs` 的
/// `picker_filter`），若 `#` / `#b` 等一输入就路由，会与列表入口抢输入。
/// 10 个内置 provider 的默认值因此是：timestamp / base64 / uuid / random
/// = Indirect，其余（无前缀或 `=`）= Direct。
pub fn default_activation(prefix: Option<&str>) -> Activation {
    match prefix {
        Some(p) if p.starts_with('#') => Activation::Indirect,
        _ => Activation::Direct,
    }
}

/// `activation` 的宽松反序列化：未知取值只记日志并按「没写」处理（`None`），
/// 由 [`AppConfig::normalize`] 回填默认，**不**让一个笔误废掉整份配置。
fn deserialize_activation_lenient<'de, D>(deserializer: D) -> Result<Option<Activation>, D::Error>
where
    D: Deserializer<'de>,
{
    let raw = Option::<String>::deserialize(deserializer)?;
    Ok(raw.and_then(|s| match s.to_ascii_lowercase().as_str() {
        "direct" => Some(Activation::Direct),
        "indirect" => Some(Activation::Indirect),
        other => {
            log::warn!("未知的 activation 取值 {other:?}，按默认处理");
            None
        }
    }))
}

/// 界面主题模式（深色模式支持）。
///
/// - [`ThemeMode::System`]（默认）：跟随系统深色 / 浅色设置；
/// - [`ThemeMode::Dark`] / [`ThemeMode::Light`]：强制深色 / 浅色。
///
/// 序列化为小写串（`"system"` / `"dark"` / `"light"`），落盘形如 `theme = "dark"`。
/// 注意：本枚举是**总体的**（没有非法取值），`theme` 字段无需 `normalize` 回填——
/// 未知串由 [`deserialize_theme_lenient`] 落成「没写」，缺省即 [`ThemeMode::System`]。
#[derive(Serialize, Deserialize, Clone, Copy, Debug, PartialEq, Eq, Default)]
#[serde(rename_all = "lowercase")]
pub enum ThemeMode {
    #[default]
    System,
    Dark,
    Light,
}

/// `theme` 的宽松反序列化：未知取值只记日志并按「没写」处理（`None`），
/// 由默认值（[`ThemeMode::System`]）兜底，**不**让一个笔误废掉整份配置。
fn deserialize_theme_lenient<'de, D>(deserializer: D) -> Result<Option<ThemeMode>, D::Error>
where
    D: Deserializer<'de>,
{
    let raw = Option::<String>::deserialize(deserializer)?;
    Ok(raw.and_then(|s| match s.to_ascii_lowercase().as_str() {
        "system" => Some(ThemeMode::System),
        "dark" => Some(ThemeMode::Dark),
        "light" => Some(ThemeMode::Light),
        other => {
            log::warn!("未知的 theme 取值 {other:?}，按默认处理");
            None
        }
    }))
}

/// 全部配置（TOML 顶层）。
#[derive(Serialize, Deserialize, Clone, Debug, PartialEq)]
pub struct AppConfig {
    /// 唤起面板的全局热键，形如 `"alt+space"`（见 [`HotkeySpec`]）。
    #[serde(default)]
    pub hotkey: String,
    /// 是否开机自启（注册表 `HKCU\...\Run`，阶段 2 落地）。
    #[serde(default)]
    pub autostart: bool,
    /// 界面主题模式（默认跟随系统）。
    #[serde(default)]
    pub theme: ThemeMode,
    /// 每个 provider 的配置，键是 [`Provider`] 的 id（如 `"calculator"`）。
    #[serde(default)]
    pub providers: BTreeMap<String, ProviderSettings>,
}

/// 单个 provider 的配置（TOML 的 `[providers.<id>]` 节）。
#[derive(Serialize, Deserialize, Clone, Debug, PartialEq, Default)]
pub struct ProviderSettings {
    /// 是否启用（关掉后从结果里完全消失）。
    #[serde(default)]
    pub enabled: bool,
    /// 是否参与全局搜索（`false` 时只能靠前缀触发）。
    #[serde(default)]
    pub is_global: bool,
    /// 聚合时的权重（只影响组内排序，见 docs/plans 文档 §7）。
    #[serde(default)]
    pub weight: f32,
    /// 专属触发前缀，如 `"="` / `"#b"`；`None` 表示无前缀（全局直接搜）。
    ///
    /// 空串 `""` 在解析时等价于 `None`（[`AppConfig::normalize`] 会归一化）。
    #[serde(default)]
    pub prefix: Option<String>,
    /// 前缀的激活方式（`None` = 用户没写，[`AppConfig::normalize`] 回填默认）。
    ///
    /// 用 `Option` 区分「没写」（回填按前缀判定的默认）与「明确选了」。
    #[serde(default)]
    pub activation: Option<Activation>,
    /// 单次查询生成的随机结果条数（目前仅 `random` 消费）。
    ///
    /// `None` = 没写，由 [`ProviderConfig::count`] 用默认值（5）回填。写 0 或超大
    /// 会被 [`AppConfig::normalize`] 夹紧到 `1..=50`。
    #[serde(default)]
    pub count: Option<u32>,
}

/// 一条热键的语义拆解：`Ctrl + Alt + Space` ⇒ `{ ctrl, alt, key="space" }`。
///
/// 分层约束：本结构纯数据、零 windows 依赖，阶段 2 的 sys 层会把它翻译成
/// `RegisterHotKey` 的修饰键 + 虚拟键码。
#[derive(Serialize, Deserialize, Clone, Debug, PartialEq)]
pub struct HotkeySpec {
    #[serde(default)]
    pub ctrl: bool,
    #[serde(default)]
    pub alt: bool,
    #[serde(default)]
    pub shift: bool,
    /// Windows 键（Win / Super / Meta）。
    #[serde(default)]
    pub win: bool,
    /// 主键名（小写，如 `"space"` / `"a"` / `"f1"`）。
    #[serde(default)]
    pub key: String,
}

/// 默认热键：`Alt+Space`（用户拍板，接管 PowerToys Run 的位置）。
///
/// 注意：`Alt+Space` 是 Windows 的**系统窗口菜单键**——`RegisterHotKey` 全局注册
/// 成功后会在全系统吃掉它（其它程序的标题栏左上角菜单打不开了）。这是有意为之，
/// 用户已知情。且本默认值**只对全新配置生效**：已有用户的 `config.toml` 里落盘的
/// 旧值不会被自动改写（不做迁移逻辑）。
const DEFAULT_HOTKEY: &str = "alt+space";

/// 10 个 provider 的默认配置表（顺序即注册顺序，决定「前缀全局唯一」冲突时
/// 谁优先）。必须与各 provider `new()` 里的硬编码一致——下面的
/// `default_has_full_provider_table` 测试锁死。
const DEFAULT_PROVIDERS: &[(&str, bool, bool, f32, Option<&str>)] = &[
    ("apps", true, true, 1.2, None),
    ("windows", true, true, 1.0, None),
    ("awake", true, true, 1.0, None),
    ("terminal", true, true, 1.0, None),
    ("workspaces", true, true, 1.0, None),
    ("calculator", true, false, 1.0, Some("=")),
    ("timestamp", true, false, 1.0, Some("#")),
    ("base64", true, false, 1.0, Some("#b")),
    ("uuid", true, false, 1.0, Some("#u")),
    ("random", true, false, 1.0, Some("#r")),
];

impl Default for HotkeySpec {
    fn default() -> Self {
        // 默认即 Alt+Space；解析失败回退时也用它（手写全字段，避免递归调用 Default）。
        HotkeySpec::parse(DEFAULT_HOTKEY).unwrap_or(HotkeySpec {
            ctrl: false,
            alt: true,
            shift: false,
            win: false,
            key: "space".into(),
        })
    }
}

impl Default for AppConfig {
    fn default() -> Self {
        let mut providers = BTreeMap::new();
        for (id, enabled, is_global, weight, prefix) in DEFAULT_PROVIDERS {
            providers.insert(
                id.to_string(),
                ProviderSettings {
                    enabled: *enabled,
                    is_global: *is_global,
                    weight: *weight,
                    prefix: prefix.map(|p| p.to_string()),
                    // 默认激活方式由前缀判定（`#` 系 → Indirect）。
                    activation: Some(default_activation(*prefix)),
                    count: None,
                },
            );
        }
        Self {
            hotkey: DEFAULT_HOTKEY.to_string(),
            autostart: false,
            theme: ThemeMode::System,
            providers,
        }
    }
}

impl HotkeySpec {
    /// 解析 `"ctrl+alt+space"` 之类的热键串。
    ///
    /// 规则：`+` 分隔，最后一个非修饰键是 `key`，其余是修饰键（`ctrl` / `alt` /
    /// `shift` / `win`，大小写不敏感，别名 `control`/`super`/`meta`/`windows` 兼容）。
    /// 没有主键、出现两个主键、串为空 → 返回 `None`。
    pub fn parse(s: &str) -> Option<Self> {
        // 注意：这里不能调用 `HotkeySpec::default()`，否则会和 `Default` 里的
        // `HotkeySpec::parse(DEFAULT_HOTKEY)` 形成无限递归。直接构造一个零值。
        let mut spec = HotkeySpec {
            ctrl: false,
            alt: false,
            shift: false,
            win: false,
            key: String::new(),
        };
        let mut key: Option<String> = None;
        for raw in s.split('+') {
            let tok = raw.trim();
            if tok.is_empty() {
                continue;
            }
            match tok.to_ascii_lowercase().as_str() {
                "ctrl" | "control" => spec.ctrl = true,
                "alt" => spec.alt = true,
                "shift" => spec.shift = true,
                "win" | "super" | "meta" | "windows" => spec.win = true,
                other => {
                    if key.is_some() {
                        // 出现了第二个主键，非法。
                        return None;
                    }
                    key = Some(other.to_string());
                }
            }
        }
        let key = key?;
        if key.is_empty() {
            return None;
        }
        spec.key = key;
        Some(spec)
    }
}

impl std::fmt::Display for HotkeySpec {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        let mut parts: Vec<&str> = Vec::new();
        if self.ctrl {
            parts.push("ctrl");
        }
        if self.alt {
            parts.push("alt");
        }
        if self.shift {
            parts.push("shift");
        }
        if self.win {
            parts.push("win");
        }
        parts.push(&self.key);
        write!(f, "{}", parts.join("+"))
    }
}

/// 部分配置（TOML 反序列化的目标）。每字段 `Option`：未写的字段是 `None`，
/// 不会覆盖默认值。读盘时用它与 [`AppConfig::default`] 合并。
#[derive(Serialize, Deserialize, Clone, Debug, PartialEq, Default)]
pub struct ConfigPatch {
    pub hotkey: Option<String>,
    pub autostart: Option<bool>,
    /// 未知取值经宽松反序列化落到 `None`（等同「没写」），由默认值（System）兜底。
    #[serde(default, deserialize_with = "deserialize_theme_lenient")]
    pub theme: Option<ThemeMode>,
    pub providers: Option<BTreeMap<String, ProviderSettingsPatch>>,
}

/// 单个 provider 的部分配置（与 [`ConfigPatch`] 同理）。
///
/// `prefix` 用 `Option<Option<String>>`：`None` = 没写，`Some(None)` = 显式清空，
/// `Some(Some("x"))` = 写了 `x`。
#[derive(Serialize, Deserialize, Clone, Debug, PartialEq, Default)]
pub struct ProviderSettingsPatch {
    pub enabled: Option<bool>,
    pub is_global: Option<bool>,
    pub weight: Option<f32>,
    pub prefix: Option<Option<String>>,
    /// 未知取值经宽松反序列化落到 `None`（等同「没写」），由 `normalize` 回填默认。
    #[serde(default, deserialize_with = "deserialize_activation_lenient")]
    pub activation: Option<Activation>,
    /// 单次查询生成的随机结果条数（目前仅 `random` 消费），`None` = 没写。
    #[serde(default)]
    pub count: Option<u32>,
}

impl AppConfig {
    /// 从 TOML 文本读配置：解析成 [`ConfigPatch`] 后与默认值合并，再做校验。
    ///
    /// 坏 TOML（解析失败）→ 回落到默认配置（保证程序永远有一份可用配置）。
    pub fn from_toml(text: &str) -> AppConfig {
        let patch = match toml::from_str::<ConfigPatch>(text) {
            Ok(p) => p,
            Err(e) => {
                log::warn!("配置解析失败，使用默认配置：{e}");
                return AppConfig::default();
            }
        };
        let mut cfg = AppConfig::default();
        cfg.merge_patch(patch);
        cfg.normalize();
        cfg
    }

    /// 序列化回 TOML（用于写盘）。
    pub fn to_toml(&self) -> Result<String, String> {
        toml::to_string(self).map_err(|e| e.to_string())
    }

    /// 把部分配置合并进自身：只有 `patch` 里「写了」的字段才覆盖默认值。
    fn merge_patch(&mut self, patch: ConfigPatch) {
        if let Some(hotkey) = patch.hotkey {
            self.hotkey = hotkey;
        }
        if let Some(autostart) = patch.autostart {
            self.autostart = autostart;
        }
        if let Some(theme) = patch.theme {
            self.theme = theme;
        }
        if let Some(providers) = patch.providers {
            for (id, p) in providers {
                let entry = self.providers.entry(id.clone()).or_default();
                if let Some(v) = p.enabled {
                    entry.enabled = v;
                }
                if let Some(v) = p.is_global {
                    entry.is_global = v;
                }
                if let Some(v) = p.weight {
                    entry.weight = v;
                }
                if let Some(v) = p.prefix {
                    entry.prefix = v;
                }
                if let Some(v) = p.activation {
                    entry.activation = Some(v);
                }
                if let Some(v) = p.count {
                    entry.count = Some(v);
                }
            }
        }
    }

    /// 校验并回填：
    /// - 缺失的 provider 用默认值补齐（部分配置必须能工作）；
    /// - `weight` clamp 到 `0.0..=3.0`（顺带处理 NaN）；
    /// - `prefix` 空串归一为 `None`、含空白或以 `#` 开头的非法值清空并记日志；
    /// - `prefix` 全局唯一（冲突时保留先出现者，后者清空）；
    /// - `activation` 为 `None`（没写）时按生效前缀回填（`#` 系 → Indirect）；
    /// - `hotkey` 必须能被 [`HotkeySpec::parse`] 解析，否则回落到默认热键。
    ///
    /// `theme` 无需在此校验：枚举取值是**总体的**（没有非法态），默认值
    /// [`ThemeMode::System`] 由 [`AppConfig::default`] 给出，未知串已在读盘时
    /// 由 [`deserialize_theme_lenient`] 落成「没写」。
    pub fn normalize(&mut self) {
        // 1. 回填缺失 provider 用默认值。
        let defaults = AppConfig::default().providers;
        for (id, def) in &defaults {
            self.providers
                .entry(id.clone())
                .or_insert_with(|| def.clone());
        }

        // 2. 逐个 provider 做 weight / prefix 的基础校验。
        for s in self.providers.values_mut() {
            if !s.weight.is_finite() {
                s.weight = 1.0;
            }
            s.weight = s.weight.clamp(0.0, 3.0);

            // `count` 夹紧到 1..=50：`None`（没写）留着给 ProviderConfig 回填默认，
            // 写 0 / 超大则归一，避免 random 一次吐出几万条把结果区打爆。
            if let Some(c) = s.count {
                s.count = Some(c.clamp(1, 50));
            }

            let mut invalid = false;
            if let Some(p) = s.prefix.clone() {
                if p.is_empty() {
                    s.prefix = None; // 空串 = 无前缀。
                } else if p.chars().any(|c| c.is_whitespace()) {
                    invalid = true; // 前缀不能含空白。
                }
                // 注：本实现**不**拒绝以 `#` 开头的前缀。计划文档 §4 的校验规则写了
                // "prefix 不能以 # 开头"，但同文档 §4 的**默认表（锁死）**和 §7 的
                // 引擎路由都依赖 `#`/`#b`/`#u`/`#r` 这组前缀（最长匹配），且
                // `QueryEngine::prefix_owner` 已正确把它们路由到对应插件、裸 `#` 由
                // app 层的 picker_filter 接管为插件列表。若在此处拒掉 `#` 前缀，
                // `from_toml` 会把默认配置里的全部 `#` 前缀清空，破坏路由与 241 项既有
                // 测试。故以默认表 + 引擎语义为准，保留 `#` 前缀。
            }
            if invalid {
                log::warn!("非法的 provider 前缀 {:?}，已忽略", s.prefix);
                s.prefix = None;
            }
        }

        // 3. 前缀全局唯一：按注册顺序遍历，保留先出现者。
        let order: Vec<String> = DEFAULT_PROVIDERS
            .iter()
            .map(|(id, ..)| id.to_string())
            .collect();
        let mut seen: HashSet<String> = HashSet::new();
        for id in &order {
            let Some(s) = self.providers.get_mut(id) else {
                continue;
            };
            let Some(p) = s.prefix.clone() else {
                continue;
            };
            if seen.contains(&p) {
                log::warn!("前缀 {p:?} 被多个 provider 声明，{id} 保留为空");
                s.prefix = None;
            } else {
                seen.insert(p);
            }
        }

        // 4. activation 回填：`None`（用户没写）→ 按该 provider 的前缀判定默认
        //    （`#` 系前缀 → Indirect，其余 → Direct）。放在前缀校验/去重**之后**，
        //    这样判定用的是最终生效的前缀。回填后存 `Some`，`to_toml` 能落全。
        for s in self.providers.values_mut() {
            if s.activation.is_none() {
                s.activation = Some(default_activation(s.prefix.as_deref()));
            }
        }

        // 5. hotkey 必须可解析，否则回落默认。
        if HotkeySpec::parse(&self.hotkey).is_none() {
            log::warn!("非法热键 {:?}，回落到默认热键", self.hotkey);
            self.hotkey = DEFAULT_HOTKEY.to_string();
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::QueryEngine;

    #[test]
    fn parse_basic_hotkey() {
        let spec = HotkeySpec::parse("ctrl+alt+space").expect("应能解析");
        assert!(spec.ctrl && spec.alt);
        assert!(!spec.shift && !spec.win);
        assert_eq!(spec.key, "space");
    }

    #[test]
    fn parse_all_modifiers_and_alias() {
        let spec = HotkeySpec::parse("Win+Shift+Ctrl+a").expect("应能解析");
        assert!(spec.ctrl && spec.shift && spec.win && !spec.alt);
        assert_eq!(spec.key, "a");
        // 别名等价。
        let spec2 = HotkeySpec::parse("super+control+b").expect("应能解析");
        assert!(spec2.win && spec2.ctrl);
        assert_eq!(spec2.key, "b");
    }

    #[test]
    fn parse_rejects_garbage() {
        assert!(HotkeySpec::parse("").is_none());
        assert!(HotkeySpec::parse("   ").is_none());
        assert!(HotkeySpec::parse("ctrl+alt").is_none(), "没有主键");
        assert!(HotkeySpec::parse("ctrl+a+b").is_none(), "两个主键");
    }

    #[test]
    fn hotkey_display_roundtrip() {
        let spec = HotkeySpec::parse("ctrl+alt+space").unwrap();
        assert_eq!(spec.to_string(), "ctrl+alt+space");
        let spec2 = HotkeySpec::parse(&spec.to_string()).unwrap();
        assert_eq!(spec, spec2);
    }

    #[test]
    fn default_has_full_provider_table() {
        let cfg = AppConfig::default();
        assert_eq!(cfg.providers.len(), 10);
        assert_eq!(cfg.hotkey, "alt+space");
        assert!(!cfg.autostart);

        let get = |id: &str| cfg.providers.get(id).expect("应当存在").clone();
        let apps = get("apps");
        assert!(apps.enabled && apps.is_global);
        assert_eq!(apps.weight, 1.2);
        assert_eq!(apps.prefix, None);

        let calc = get("calculator");
        assert!(calc.enabled && !calc.is_global);
        assert_eq!(calc.weight, 1.0);
        assert_eq!(calc.prefix.as_deref(), Some("="));

        let ts = get("timestamp");
        assert_eq!(ts.prefix.as_deref(), Some("#"));
        let b64 = get("base64");
        assert_eq!(b64.prefix.as_deref(), Some("#b"));
        let uuid = get("uuid");
        assert_eq!(uuid.prefix.as_deref(), Some("#u"));
        let random = get("random");
        assert_eq!(random.prefix.as_deref(), Some("#r"));
        for id in ["windows", "awake", "terminal", "workspaces"] {
            assert_eq!(get(id).prefix, None, "{id} 默认无前缀");
        }
    }

    #[test]
    fn normalize_clamps_weight() {
        let mut cfg = AppConfig::default();
        cfg.providers.get_mut("apps").unwrap().weight = 9.9;
        cfg.providers.get_mut("windows").unwrap().weight = -2.0;
        cfg.normalize();
        assert_eq!(cfg.providers["apps"].weight, 3.0);
        assert_eq!(cfg.providers["windows"].weight, 0.0);
    }

    /// `count` 被夹紧到 1..=50（0 与超大都归一）。
    #[test]
    fn normalize_clamps_count() {
        let mut cfg = AppConfig::default();
        cfg.providers.get_mut("random").unwrap().count = Some(0);
        cfg.providers.get_mut("uuid").unwrap().count = Some(999);
        cfg.normalize();
        assert_eq!(cfg.providers["random"].count, Some(1));
        assert_eq!(cfg.providers["uuid"].count, Some(50));
    }

    /// 只写 `count` 不写别的字段，其余配置不受影响（部分配置语义）。
    #[test]
    fn from_toml_only_count_keeps_other_fields() {
        let cfg = AppConfig::from_toml("[providers.random]\ncount = 8\n");
        let r = &cfg.providers["random"];
        assert_eq!(r.count, Some(8));
        assert_eq!(r.prefix.as_deref(), Some("#r"), "prefix 不应被抹掉");
        assert_eq!(r.weight, 1.0);
        assert!(!r.is_global);
        // 其它 provider 的 count 仍是 None（由 ProviderConfig 回填默认 5）。
        assert_eq!(cfg.providers["uuid"].count, None);
    }

    /// `apply_config` 把 `count` 下发到 `ProviderConfig`：`None` → 默认 5，写了 → 回填。
    #[test]
    fn apply_config_backfills_and_passes_count() {
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

        let providers: Vec<Box<dyn crate::provider::Provider>> = vec![
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
        let mut cfg = AppConfig::default();
        cfg.providers.get_mut("random").unwrap().count = Some(12);
        engine.apply_config(&cfg);
        // random 被显式设成 12。
        let random = engine.provider("random").expect("random 应在引擎里");
        assert_eq!(random.config().count, 12);
        // uuid 没写 count → 默认 5。
        let uuid = engine.provider("uuid").expect("uuid 应在引擎里");
        assert_eq!(uuid.config().count, 5);
    }

    #[test]
    fn normalize_rejects_bad_prefix() {
        let mut cfg = AppConfig::default();
        // 含空白 → 清空。
        cfg.providers.get_mut("apps").unwrap().prefix = Some(" a".into());
        // 空串 → 归一为 None。
        cfg.providers.get_mut("terminal").unwrap().prefix = Some(String::new());
        cfg.normalize();
        assert_eq!(cfg.providers["apps"].prefix, None);
        assert_eq!(cfg.providers["terminal"].prefix, None);
        // 合法前缀不受影响（含 `#` 家族：它们由引擎最长匹配路由，必须保留）。
        assert_eq!(cfg.providers["calculator"].prefix.as_deref(), Some("="));
        assert_eq!(cfg.providers["timestamp"].prefix.as_deref(), Some("#"));
        assert_eq!(cfg.providers["base64"].prefix.as_deref(), Some("#b"));
    }

    #[test]
    fn normalize_keeps_first_prefix_on_collision() {
        let mut cfg = AppConfig::default();
        // 让 timestamp 也用 "="，与 calculator 冲突；calculator 在默认表里排在 timestamp
        // 之前（注册顺序 = 先出现者优先），所以 calculator 保留 "="、timestamp 被清空。
        cfg.providers.get_mut("timestamp").unwrap().prefix = Some("=".into());
        cfg.normalize();
        assert_eq!(cfg.providers["calculator"].prefix.as_deref(), Some("="));
        assert_eq!(
            cfg.providers["timestamp"].prefix, None,
            "冲突的后出现者被清空"
        );
    }

    #[test]
    fn normalize_falls_back_on_bad_hotkey() {
        let mut cfg = AppConfig {
            hotkey: "not+a+valid+key".into(),
            ..AppConfig::default()
        };
        cfg.normalize();
        assert_eq!(cfg.hotkey, "alt+space");
    }

    /// 默认热键 `alt+space` 能被解析、且 `Display` 原样输出（新配置落盘形态正确）。
    #[test]
    fn default_hotkey_is_alt_space() {
        let spec = HotkeySpec::parse(DEFAULT_HOTKEY).expect("默认热键应可解析");
        assert!(spec.alt && !spec.ctrl && !spec.shift && !spec.win);
        assert_eq!(spec.key, "space");
        assert_eq!(spec.to_string(), "alt+space");
        assert_eq!(HotkeySpec::default(), spec);
    }

    /// 问题 0 核实：默认热键 `alt+space` **只对全新配置生效**。
    ///
    /// - 配置里没写 `hotkey`（全新安装 / 老配置从未落盘过热键）→ 用默认值 `alt+space`；
    /// - 老用户配置里已落盘旧值（如 `ctrl+alt+space`）→ `from_toml` **原样读取**，
    ///   不做迁移（`normalize` 只在热键非法时才回落默认，合法旧值一律保留）。
    #[test]
    fn default_hotkey_only_applies_to_fresh_config() {
        // 没写 hotkey：默认 alt+space。
        assert_eq!(
            AppConfig::from_toml("autostart = true\n").hotkey,
            "alt+space"
        );
        // 老配置里落盘的旧值被原样保留，不迁移。
        assert_eq!(
            AppConfig::from_toml("hotkey = \"ctrl+alt+space\"\n").hotkey,
            "ctrl+alt+space"
        );
    }

    #[test]
    fn from_toml_only_hotkey_keeps_defaults() {
        // 只写了热键：其余字段（含 10 个 provider 的真实默认值）必须完整保留。
        let cfg = AppConfig::from_toml("hotkey = \"alt+space\"\n");
        assert_eq!(cfg.hotkey, "alt+space");
        assert_eq!(cfg.providers.len(), 10);
        // calculator 的 = 前缀不能被「没写」抹掉。
        assert_eq!(cfg.providers["calculator"].prefix.as_deref(), Some("="));
        assert_eq!(cfg.providers["apps"].weight, 1.2);
        assert!(!cfg.providers["calculator"].is_global);
    }

    #[test]
    fn from_toml_partial_provider_keeps_other_fields() {
        // 只改了 calculator.enabled=false：prefix/weight/is_global 仍来自默认。
        let cfg = AppConfig::from_toml("[providers.calculator]\nenabled = false\n");
        let calc = &cfg.providers["calculator"];
        assert!(!calc.enabled);
        assert_eq!(calc.prefix.as_deref(), Some("="), "prefix 不应被抹掉");
        assert_eq!(calc.weight, 1.0);
        assert!(!calc.is_global);
    }

    #[test]
    fn from_toml_missing_provider_gets_default() {
        // 配置里没有任何 provider 节，但必须补满 10 个默认 provider。
        let cfg = AppConfig::from_toml("autostart = true\n");
        assert!(cfg.autostart);
        assert_eq!(cfg.providers.len(), 10);
        assert_eq!(cfg.providers["random"].prefix.as_deref(), Some("#r"));
    }

    #[test]
    fn from_toml_bad_toml_falls_back_to_default() {
        // 坏 TOML → 回落默认（10 个 provider 齐全、热键为 alt+space）。
        let cfg = AppConfig::from_toml("this is = = not valid toml @@@");
        assert_eq!(cfg.providers.len(), 10);
        assert_eq!(cfg.hotkey, "alt+space");
        assert_eq!(cfg.providers["calculator"].prefix.as_deref(), Some("="));
    }

    /// 默认激活方式：`#` 系（timestamp / base64 / uuid / random）→ Indirect，
    /// 其余（无前缀或 `=`）→ Direct。
    #[test]
    fn default_activation_matches_prefix_namespace() {
        let cfg = AppConfig::default();
        for id in ["timestamp", "base64", "uuid", "random"] {
            assert_eq!(
                cfg.providers[id].activation,
                Some(Activation::Indirect),
                "{id} 前缀以 # 开头，默认应为 Indirect"
            );
        }
        for id in [
            "apps",
            "windows",
            "awake",
            "terminal",
            "workspaces",
            "calculator",
        ] {
            assert_eq!(
                cfg.providers[id].activation,
                Some(Activation::Direct),
                "{id} 默认应为 Direct"
            );
        }
    }

    /// `normalize` 把 `activation == None`（用户没写）按最终前缀回填。
    #[test]
    fn normalize_backfills_activation_from_prefix() {
        let mut cfg = AppConfig::default();
        cfg.providers.get_mut("base64").unwrap().activation = None;
        cfg.providers.get_mut("calculator").unwrap().activation = None;
        cfg.normalize();
        assert_eq!(
            cfg.providers["base64"].activation,
            Some(Activation::Indirect),
            "# 系前缀 → Indirect"
        );
        assert_eq!(
            cfg.providers["calculator"].activation,
            Some(Activation::Direct),
            "`=` 前缀 → Direct"
        );
    }

    /// 部分配置：只写 calculator 的 activation，其余字段与别的 provider 都不受影响。
    #[test]
    fn from_toml_partial_activation_keeps_other_fields() {
        let cfg = AppConfig::from_toml("[providers.calculator]\nactivation = \"indirect\"\n");
        let calc = &cfg.providers["calculator"];
        assert_eq!(calc.activation, Some(Activation::Indirect));
        assert_eq!(calc.prefix.as_deref(), Some("="), "prefix 不应被抹掉");
        assert_eq!(calc.weight, 1.0);
        assert!(!calc.is_global);
        // 没写的 provider 仍是默认值。
        assert_eq!(
            cfg.providers["base64"].activation,
            Some(Activation::Indirect)
        );
        assert_eq!(cfg.providers["base64"].prefix.as_deref(), Some("#b"));
    }

    /// 坏 activation 取值只让该字段按默认处理，**不**废掉整份配置（宽松反序列化）。
    #[test]
    fn from_toml_bad_activation_falls_back_to_default() {
        let cfg = AppConfig::from_toml(
            "hotkey = \"alt+space\"\n[providers.base64]\nactivation = \"xxx\"\n",
        );
        assert_eq!(cfg.hotkey, "alt+space", "坏 activation 不应废掉其它配置");
        assert_eq!(
            cfg.providers["base64"].activation,
            Some(Activation::Indirect),
            "未知取值 → 回填默认（# 系 → Indirect）"
        );
        assert_eq!(
            cfg.providers["base64"].prefix.as_deref(),
            Some("#b"),
            "prefix 保留"
        );
    }

    /// activation 走 to_toml → from_toml 应保持稳定。
    #[test]
    fn activation_toml_roundtrip_is_stable() {
        let mut cfg = AppConfig::default();
        cfg.providers.get_mut("calculator").unwrap().activation = Some(Activation::Indirect);
        cfg.providers.get_mut("base64").unwrap().activation = Some(Activation::Direct);
        let text = cfg.to_toml().expect("序列化不应失败");
        let back = AppConfig::from_toml(&text);
        assert_eq!(
            back.providers["calculator"].activation,
            Some(Activation::Indirect)
        );
        assert_eq!(
            back.providers["base64"].activation,
            Some(Activation::Direct)
        );
        assert_eq!(cfg, back);
    }

    /// 默认主题模式是「跟随系统」。
    #[test]
    fn default_theme_is_system() {
        assert_eq!(AppConfig::default().theme, ThemeMode::System);
    }

    /// 三种合法取值都能被解析（含大小写不敏感）。
    #[test]
    fn from_toml_parses_theme_modes() {
        assert_eq!(
            AppConfig::from_toml("theme = \"system\"\n").theme,
            ThemeMode::System
        );
        assert_eq!(
            AppConfig::from_toml("theme = \"dark\"\n").theme,
            ThemeMode::Dark
        );
        assert_eq!(
            AppConfig::from_toml("theme = \"light\"\n").theme,
            ThemeMode::Light
        );
        assert_eq!(
            AppConfig::from_toml("theme = \"Dark\"\n").theme,
            ThemeMode::Dark
        );
    }

    /// 非法 theme 取值只让该字段回落默认（System），**不**废掉整份配置。
    #[test]
    fn from_toml_bad_theme_falls_back_to_default() {
        let cfg = AppConfig::from_toml("hotkey = \"alt+space\"\ntheme = \"neon\"\n");
        assert_eq!(cfg.theme, ThemeMode::System, "未知取值 → 默认跟随系统");
        assert_eq!(cfg.hotkey, "alt+space", "坏 theme 不应废掉其它配置");
        assert_eq!(cfg.providers["calculator"].prefix.as_deref(), Some("="));
    }

    /// 部分合并：只写 theme，其余顶层字段与 provider 表都保持默认。
    #[test]
    fn from_toml_only_theme_keeps_other_fields() {
        let cfg = AppConfig::from_toml("theme = \"dark\"\n");
        assert_eq!(cfg.theme, ThemeMode::Dark);
        assert_eq!(cfg.hotkey, "alt+space");
        assert!(!cfg.autostart);
        assert_eq!(cfg.providers.len(), 10);
        assert_eq!(cfg.providers["apps"].weight, 1.2);
    }

    /// theme 走 to_toml → from_toml 应保持稳定。
    #[test]
    fn theme_toml_roundtrip_is_stable() {
        let cfg = AppConfig {
            theme: ThemeMode::Dark,
            ..AppConfig::default()
        };
        let text = cfg.to_toml().expect("序列化不应失败");
        assert!(text.contains("theme = \"dark\""), "落盘应是小写串：{text}");
        let back = AppConfig::from_toml(&text);
        assert_eq!(back.theme, ThemeMode::Dark);
        assert_eq!(cfg, back);
    }

    #[test]
    fn toml_roundtrip_is_stable() {
        let cfg = AppConfig::default();
        let text = cfg.to_toml().expect("序列化不应失败");
        let back = AppConfig::from_toml(&text);
        assert_eq!(cfg, back);
    }

    #[test]
    fn config_patch_serializes_partial() {
        // 写盘再读回一个只含热键的极简配置，应等价于默认值 + 改热键。
        let toml_text = "hotkey = \"alt+space\"\n";
        let cfg = AppConfig::from_toml(toml_text);
        let out = cfg.to_toml().expect("序列化不应失败");
        let cfg2 = AppConfig::from_toml(&out);
        assert_eq!(cfg, cfg2);
    }
}
