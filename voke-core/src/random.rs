//! random provider：随机数 / 随机字符串（§7 的 M4 插件，对齐 PTR 的
//! "Value Generator / Random"）。
//!
//! ## 触发方式（M5：子命令面板激活）
//!
//! 输入 `#` → 子命令面板 → 选「随机值」→ 输入框变成 `# `，之后只查本插件。
//! 它保留 `#` 前缀，所以 `#1-100` 也能直接命中（`#` 下第一个注册者胜）。
//! `is_global = false`：随机值不该参与全局模糊搜索。
//!
//! ## 交互（两级子模式）
//!
//! 用 `#r` 直接进入**一级**：列出四个随机类型（`EnterSubquery`，进入后生成
//! 该类型的多条结果）——「随机整数」（`sub_mode = "int"`）、「随机字符串」
//! （`sub_mode = "str"`）、「含特殊字符」（`sub_mode = "sym"`）、「十六进制」
//! （`sub_mode = "hex"`）。一级是**纯选择器**：不接受直接输入生成，非空输入只
//! 给一条「请先选择随机类型」的提示；参数（范围 / 长度）都在二级里填。
//!
//! 进入二级后（例如 `#r ` 后选「随机整数」），列表**第一条固定为「重新生成」**
//! （`Action::Refresh`，让 app 用同一输入再掷一批）；空输入按该类型预置参数生成
//! `count` 条；输入参数（int 给范围、str/sym/hex 给长度）则按参数生成 `count` 条。
//! 每条结果都是 `Action::Copy`（标题即值），且**每次查询重新生成**。
//!
//! | 二级输入（int） | 生成 |
//! |---|---|
//! | 空 | `count` 个 `[1, 100]` 整数 |
//! | `5-10` | `count` 个 `[5, 10]` 整数 |
//! | `abc` | 一条错误提示（`Hint`） |

use crate::action::Action;
use crate::engine::PREFIX_RANDOM;
use crate::entropy::{Entropy, SystemEntropy};
use crate::provider::{BoxFuture, Match, Provider, ProviderConfig, QueryCtx, ready};

/// 随机字符串的默认长度。
pub const DEFAULT_STRING_LEN: usize = 16;

/// 随机字符串的长度上限（防 `#rand str:99999` 把结果区打爆）。
pub const MAX_STRING_LEN: usize = 256;

/// 随机整数的默认上界（只输 `#rand` 时的范围）。
pub const DEFAULT_INT_MAX: i64 = 100;

/// 随机整数的范围宽度上限（防 `#rand 1-999999999999` 这类超大区间）。
pub const MAX_RANGE_SPAN: i64 = 1_000_000_000;

/// 字符表：大小写字母 + 数字（62 个，适合当密码/ID 占位）。
const ALNUM: &[u8] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789";

/// 字符表：在 [`ALNUM`] 基础上加常见符号（"含特殊字符"候选）。
const WITH_SYMBOLS: &[u8] =
    b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789!@#$%^&*-_=+";

/// 字符表：十六进制。
const HEX: &[u8] = b"0123456789abcdef";

/// 随机字符串用的字符表（决策 5：先只提供这三张，够覆盖常见用途）。
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Alphabet {
    /// 大小写字母 + 数字。
    Alphanumeric,
    /// 额外含 `!@#$%^&*-_=+`。
    WithSymbols,
    /// `0-9a-f`。
    Hex,
}

impl Alphabet {
    /// 该字符表对应的字节集。
    fn bytes(self) -> &'static [u8] {
        match self {
            Alphabet::Alphanumeric => ALNUM,
            Alphabet::WithSymbols => WITH_SYMBOLS,
            Alphabet::Hex => HEX,
        }
    }

    /// 副标题里的说明文案。
    pub fn label(self) -> &'static str {
        match self {
            Alphabet::Alphanumeric => "字母 + 数字",
            Alphabet::WithSymbols => "字母 + 数字 + 特殊字符",
            Alphabet::Hex => "十六进制",
        }
    }
}

/// 一次查询的结果类型。
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Request {
    /// 在 `[low, high]` 内取一个整数。
    Int { low: i64, high: i64 },
    /// 取一个定长随机字符串。
    String { len: usize, alphabet: Alphabet },
}

/// random provider。
pub struct RandomProvider {
    config: ProviderConfig,
    entropy: Box<dyn Entropy>,
}

impl RandomProvider {
    /// provider 的稳定 id。
    pub const ID: &'static str = "random";

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
                prefix: Some(PREFIX_RANDOM.to_string()),
                // `#` 系：默认间接激活（前缀 + 空格），见 config::default_activation。
                activation: crate::config::Activation::Indirect,
                count: 5,
            },
            entropy,
        }
    }

    /// 在 `[low, high]` 内取一个随机整数（含两端）。
    pub fn random_int(&self, low: i64, high: i64) -> i64 {
        let (low, high) = if low <= high {
            (low, high)
        } else {
            (high, low)
        };
        let span = (high as i128 - low as i128 + 1).min(MAX_RANGE_SPAN as i128) as i64;
        if span <= 1 {
            return low;
        }
        low + (self.random_u64() % span as u64) as i64
    }

    /// 生成一个定长随机字符串（默认字符表 = 大小写字母 + 数字）。
    pub fn random_string(&self, len: usize) -> String {
        self.random_string_with(len, Alphabet::Alphanumeric)
    }

    /// 生成一个定长随机字符串（指定字符表）。
    pub fn random_string_with(&self, len: usize, alphabet: Alphabet) -> String {
        let table = alphabet.bytes();
        let len = len.min(MAX_STRING_LEN);
        let mut out = String::with_capacity(len);
        for _ in 0..len {
            let idx = (self.random_u64() % table.len() as u64) as usize;
            out.push(table[idx] as char);
        }
        out
    }

    /// 按 [`Request`] 生成一个值（值，副标题）。
    fn generate(&self, request: Request) -> (String, String) {
        match request {
            Request::Int { low, high } => (
                self.random_int(low, high).to_string(),
                format!("随机整数 [{low}, {high}]"),
            ),
            Request::String { len, alphabet } => (
                self.random_string_with(len, alphabet),
                format!("随机字符串（{len} 位 · {}）", alphabet.label()),
            ),
        }
    }

    /// 取一个 64 位随机数。
    ///
    /// 只用 64 位取模会有极轻微的偏差（62/68/16 不整除 2^64），对"随机数/随机串"
    /// 这个定位完全可接受；不引 `rand` 的 `Uniform` 分摊这个复杂度。
    fn random_u64(&self) -> u64 {
        let mut bytes = [0u8; 8];
        self.entropy.fill(&mut bytes);
        u64::from_le_bytes(bytes)
    }

    /// 一级：四个进入二级的方向；空输入只给方向；非空输入不解析生成，
    /// 只给一条「请先选择随机类型」的提示（一级是纯选择器）。
    fn query_level1(&self, query: &str) -> Vec<Match> {
        let out = vec![
            Match::subquery(
                Self::ID,
                "随机整数",
                "默认 1–100，进入后可选范围",
                "int",
                1.0,
            ),
            Match::subquery(
                Self::ID,
                "随机字符串",
                "默认 16 位字母+数字，进入后可选长度",
                "str",
                0.95,
            ),
            Match::subquery(
                Self::ID,
                "含特殊字符",
                "默认 16 位，字母+数字+特殊字符",
                "sym",
                0.9,
            ),
            Match::subquery(Self::ID, "十六进制", "默认 32 位，0-9a-f", "hex", 0.85),
        ];
        if query.is_empty() {
            return out;
        }
        // 非空输入：一级不解析、不生成，提示用户先选类型。
        vec![Match::hint(
            Self::ID,
            "请先选择随机类型",
            "进入上方类型后再设置参数（范围 / 长度）",
        )]
    }

    /// 二级（sub_mode 决定类型）：首条固定「重新生成」，随后按预置 / 参数生成 `count` 条。
    fn query_level2(&self, sub_mode: &str, query: &str) -> Vec<Match> {
        let count = self.config.count.max(1) as usize;
        let mut out: Vec<Match> = Vec::with_capacity(count + 1);

        // 第一条固定「重新生成」：Execute + Refresh，副标题带数量。
        let refresh_subtitle = if count > 1 {
            format!("再生成 {count} 个")
        } else {
            "重新生成".to_string()
        };
        out.push(Match::execute(
            Self::ID,
            "重新生成",
            refresh_subtitle,
            Action::Refresh,
            1.0,
        ));

        // 该类型的预置参数（空输入时用）。
        let default_request = match sub_mode {
            "int" => Request::Int { low: 1, high: 100 },
            "str" => Request::String {
                len: DEFAULT_STRING_LEN,
                alphabet: Alphabet::Alphanumeric,
            },
            "sym" => Request::String {
                len: DEFAULT_STRING_LEN,
                alphabet: Alphabet::WithSymbols,
            },
            "hex" => Request::String {
                len: 32,
                alphabet: Alphabet::Hex,
            },
            // 未知 sub_mode 兜底：只有重新生成项。
            _ => return out,
        };

        if query.is_empty() {
            self.append_many(&mut out, default_request, count);
            return out;
        }

        // 解析参数（失败给错误提示，但保留首条「重新生成」）。
        let request = match sub_mode {
            "int" => match parse(query) {
                Some(Request::Int { low, high }) => Request::Int { low, high },
                _ => {
                    out.push(Match::hint(Self::ID, "无法解析", "示例：5-10 或 24"));
                    return out;
                }
            },
            "str" | "sym" | "hex" => match parse_length_param(query) {
                Some(len) => match sub_mode {
                    "str" => Request::String {
                        len,
                        alphabet: Alphabet::Alphanumeric,
                    },
                    "sym" => Request::String {
                        len,
                        alphabet: Alphabet::WithSymbols,
                    },
                    _ => Request::String {
                        len,
                        alphabet: Alphabet::Hex,
                    },
                },
                None => {
                    out.push(Match::hint(Self::ID, "无法解析", "示例：5-10 或 24"));
                    return out;
                }
            },
            _ => return out,
        };
        self.append_many(&mut out, request, count);
        out
    }

    /// 按 `request` 生成 `count` 条结果（标题即值，动作 Copy），追加到 `out`。
    fn append_many(&self, out: &mut Vec<Match>, request: Request, count: usize) {
        for i in 0..count {
            let (value, subtitle) = self.generate(request);
            out.push(Match::execute(
                Self::ID,
                value.clone(),
                subtitle,
                Action::Copy { text: value },
                1.0 - (i as f32 + 1.0) * 0.02,
            ));
        }
    }
}

impl Default for RandomProvider {
    fn default() -> Self {
        Self::new()
    }
}

impl Provider for RandomProvider {
    fn id(&self) -> &'static str {
        Self::ID
    }

    fn name(&self) -> &'static str {
        "随机值"
    }

    /// 别名：`r` = 单字母快捷；`sj` = 拼音首字母（随机）；`rand` = 英文缩写
    /// （`random` 由 name 子串语义覆盖）。
    fn aliases(&self) -> &'static [&'static str] {
        &["r", "sj", "rand"]
    }

    fn description(&self) -> &'static str {
        "随机整数 / 随机字符串"
    }

    fn config(&self) -> &ProviderConfig {
        &self.config
    }

    fn set_config(&mut self, config: ProviderConfig) {
        self.config = config;
    }

    fn placeholder(&self, sub_mode: Option<&str>) -> &'static str {
        match sub_mode {
            Some("int") => "输入范围如 5-10（默认 1–100），或选「重新生成」",
            Some("str") => "输入长度如 24（默认 16），或选「重新生成」",
            Some("sym") => "输入长度如 24（默认 16），或选「重新生成」",
            Some("hex") => "输入长度如 64（默认 32），或选「重新生成」",
            // 一级：纯选择器，只选类型，不接收任何直接输入。
            _ => "选择随机类型",
        }
    }

    fn query(&self, ctx: &QueryCtx) -> BoxFuture<'static, Vec<Match>> {
        let query = ctx.query.trim();
        match ctx.sub_mode.as_deref() {
            // 二级：亮屏与否由 sub_mode 决定。
            Some("int") | Some("str") | Some("sym") | Some("hex") => {
                ready(self.query_level2(ctx.sub_mode.as_deref().unwrap(), query))
            }
            // 一级（含未知 sub_mode 兜底）。
            _ => ready(self.query_level1(query)),
        }
    }
}

/// 解析查询成一个 [`Request`]；不认识的形态返回 `None`（无结果）。
pub fn parse(query: &str) -> Option<Request> {
    let query = query.trim();
    if query.is_empty() {
        return Some(Request::Int {
            low: 0,
            high: DEFAULT_INT_MAX,
        });
    }

    let lower = query.to_ascii_lowercase();

    // 命名字符表：`str` / `sym` / `hex`（可带 `:长度`）。
    for (names, alphabet) in [
        (&["str", "string", "alnum"][..], Alphabet::Alphanumeric),
        (&["sym", "special", "symbols"][..], Alphabet::WithSymbols),
        (&["hex"][..], Alphabet::Hex),
    ] {
        for name in names {
            if lower == *name {
                return Some(Request::String {
                    len: DEFAULT_STRING_LEN,
                    alphabet,
                });
            }
            if let Some(rest) = lower.strip_prefix(name).and_then(|r| r.strip_prefix(':')) {
                return parse_len(rest.trim()).map(|len| Request::String { len, alphabet });
            }
        }
    }

    parse_range(query).map(|(low, high)| Request::Int { low, high })
}

/// 解析范围：`100`（0–100）、`1-100`、`1..100`、`-50-50`。
fn parse_range(query: &str) -> Option<(i64, i64)> {
    // `1..100` / `1..=100`
    if let Some((a, b)) = query.split_once("..") {
        let low: i64 = a.trim().parse().ok()?;
        let high: i64 = b.trim().trim_start_matches('=').trim().parse().ok()?;
        return validate(low, high);
    }

    // 单个数字 → 0..n
    if let Ok(n) = query.parse::<i64>() {
        if n < 0 {
            // `#rand -5` 想要"负数的随机数"而不是 `0`–`-5`。
            // 语义不明确（见模块文档的歧义提醒），按 `-5..0` 处理更符合直觉。
            return validate(n, 0);
        }
        return validate(0, n);
    }

    // `a-b`：难点是负数端点（`-50-50`）与 `-50--10`。
    // 从**第二个字符**开始找第一个 `-`，它必然是分隔符：
    // - `-50-50`  → 下标 3 的 `-` 是分隔符 → a="-50", b="50"
    // - `-50--10` → 下标 3 的 `-` 是分隔符 → a="-50", b="-10"
    // - `1-100`   → 下标 1 的 `-` 是分隔符 → a="1", b="100"
    let search_from = if query.starts_with('-') { 2 } else { 1 };
    let split_at = query
        .get(search_from..)?
        .find('-')
        .map(|i| i + search_from)?;
    let low: i64 = query[..split_at].trim().parse().ok()?;
    let high: i64 = query[split_at + 1..].trim().parse().ok()?;
    validate(low, high)
}

/// 校验并归一化范围：交换倒序端点，拒绝超大跨度。
fn validate(low: i64, high: i64) -> Option<(i64, i64)> {
    let (low, high) = if low <= high {
        (low, high)
    } else {
        (high, low)
    };
    let span = (high as i128 - low as i128).unsigned_abs();
    if span > MAX_RANGE_SPAN as u128 {
        return None;
    }
    Some((low, high))
}

/// 解析字符串长度（1..=[`MAX_STRING_LEN`]）。
fn parse_len(text: &str) -> Option<usize> {
    let len: usize = text.parse().ok()?;
    if len == 0 {
        return None;
    }
    Some(len.min(MAX_STRING_LEN))
}

/// 二级 str/sym/hex 下的长度参数解析：容忍遗留的 `str:` 之类前缀，再交给
/// [`parse_len`]（0 / 超长会被它归一或拒绝）。
fn parse_length_param(query: &str) -> Option<usize> {
    let q = query.trim();
    let digits = q.split_once(':').map(|(_, r)| r.trim()).unwrap_or(q);
    parse_len(digits)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::entropy::SeededEntropy;
    use crate::provider::Interact;
    use std::future::Future;

    fn block_on<F: Future>(f: F) -> F::Output {
        let mut f = Box::pin(f);
        let mut cx = std::task::Context::from_waker(std::task::Waker::noop());
        match f.as_mut().poll(&mut cx) {
            std::task::Poll::Ready(v) => v,
            std::task::Poll::Pending => panic!("random 的 future 应当立即就绪"),
        }
    }

    fn seeded() -> RandomProvider {
        RandomProvider::with_entropy(Box::new(SeededEntropy::new(2024)))
    }

    /// 用指定 count 构造 provider（测试「一次生成 N 条」）。
    fn with_count(c: u32) -> RandomProvider {
        let mut p = seeded();
        let mut cfg = p.config().clone();
        cfg.count = c;
        p.set_config(cfg);
        p
    }

    /// 模拟激活后输入框里的 `# ` 被剥掉的调用（一级）。
    fn rand(provider: &RandomProvider, q: &str) -> Vec<Match> {
        let ctx = QueryCtx::new(format!("#r{q}"), q.to_string());
        block_on(provider.query(&ctx))
    }

    /// 二级调用（带 sub_mode）。
    fn rand_mode(provider: &RandomProvider, q: &str, mode: &str) -> Vec<Match> {
        let ctx = QueryCtx::with_sub_mode(format!("#r{q}"), q.to_string(), Some(mode));
        block_on(provider.query(&ctx))
    }

    fn titles(matches: &[Match]) -> Vec<String> {
        matches.iter().map(|m| m.title.clone()).collect()
    }

    #[test]
    fn is_prefix_only_and_not_global() {
        let p = seeded();
        assert_eq!(p.id(), "random");
        assert_eq!(p.name(), "随机值");
        assert!(!p.description().is_empty());
        assert_eq!(p.config().prefix, Some("#r".to_string()));
        assert!(!p.config().is_global, "随机值不得进全局结果");
        // count 默认 5。
        assert_eq!(p.config().count, 5);
    }

    /// 一级空输入：列出四个进入二级的方向。
    #[test]
    fn level1_empty_lists_four_types() {
        let p = seeded();
        let hits = rand(&p, "");
        assert_eq!(hits.len(), 4, "{:?}", titles(&hits));
        let expected = [
            ("随机整数", "int"),
            ("随机字符串", "str"),
            ("含特殊字符", "sym"),
            ("十六进制", "hex"),
        ];
        for (hit, (title, mode)) in hits.iter().zip(expected) {
            assert_eq!(hit.title, title);
            assert_eq!(hit.interact, Interact::EnterSubquery(mode.to_string()));
            assert_eq!(hit.action, Action::None);
        }
    }

    /// 一级非空输入：一级是纯选择器，不解析生成，只给一条提示。
    #[test]
    fn level1_direct_input_shows_hint() {
        let p = with_count(3);
        let hits = rand(&p, "1-100");
        // 只有一条提示，不产生任何结果。
        assert_eq!(hits.len(), 1, "一级不应生成结果");
        assert!(hits[0].interact.is_hint(), "应是提示行");
        assert_eq!(hits[0].title, "请先选择随机类型");
        assert_eq!(hits[0].action, Action::None);
        // 默认 count 下同样只给一条提示（不会生成 5 条）。
        assert_eq!(rand(&seeded(), "1-100").len(), 1);
    }

    /// 一级非空输入覆盖各形态：一律只给提示，绝不误生成结果。
    #[test]
    fn level1_any_nonempty_is_pure_selector() {
        let p = seeded();
        for q in [
            "1-100",
            "1..100",
            "1..=100",
            "100-1",
            "-50-50",
            "100",
            "str",
            "string",
            "STR",
            "str:8",
            "str:99999",
            "sym",
            "hex:40",
        ] {
            let hits = rand(&p, q);
            assert_eq!(hits.len(), 1, "{q:?} 一级只给提示");
            assert!(hits[0].interact.is_hint(), "{q:?}");
            assert_eq!(hits[0].title, "请先选择随机类型", "{q:?}");
            assert!(
                !matches!(hits[0].action, Action::Copy { .. }),
                "{q:?} 一级不该生成 Copy 结果"
            );
        }
    }

    /// 一级无法识别的输入：同样只给一条提示，不退回四个方向导航。
    #[test]
    fn level1_unrecognized_shows_hint() {
        let p = seeded();
        for bad in [
            "abc",
            "str:",
            "str:0",
            "str:-1",
            "1-",
            "-",
            "a-b",
            "1-2-3",
            "1.5-2.5",
            "1..",
            "1-999999999999999",
        ] {
            let hits = rand(&p, bad);
            assert_eq!(hits.len(), 1, "{bad:?} 应只剩一条提示，实际 {hits:?}");
            assert!(hits[0].interact.is_hint(), "{bad:?}");
            assert_eq!(hits[0].title, "请先选择随机类型", "{bad:?}");
        }
    }

    /// 二级（int）空输入：首条「重新生成」+ count 个 [1,100] 整数。
    #[test]
    fn level2_int_empty_generates_count() {
        let p = with_count(4);
        let hits = rand_mode(&p, "", "int");
        assert_eq!(hits.len(), 5, "1 条重新生成 + 4 条结果");
        assert_eq!(hits[0].action, Action::Refresh);
        for hit in &hits[1..] {
            let Action::Copy { text } = &hit.action else {
                panic!("结果必须是 Copy");
            };
            let value: i64 = text.parse().unwrap();
            assert!((1..=100).contains(&value), "值 {value} 越界");
        }
    }

    /// 二级首条固定「重新生成」：副标题带数量（count>1）或不带（count==1）。
    #[test]
    fn level2_refresh_first_with_count_label() {
        let p = with_count(5);
        let hits = rand_mode(&p, "", "int");
        assert_eq!(hits[0].title, "重新生成");
        assert_eq!(hits[0].action, Action::Refresh);
        assert!(
            hits[0].subtitle.contains("再生成 5 个"),
            "{}",
            hits[0].subtitle
        );

        let p = with_count(1);
        let hits = rand_mode(&p, "", "int");
        assert_eq!(hits[0].subtitle, "重新生成");
    }

    /// 二级（int）给参数：按范围生成 count 条。
    #[test]
    fn level2_int_param_uses_range() {
        let p = with_count(3);
        let hits = rand_mode(&p, "5-10", "int");
        assert_eq!(hits.len(), 4, "1 重新生成 + 3 结果");
        for hit in &hits[1..] {
            let value: i64 = hit.title.parse().unwrap();
            assert!((5..=10).contains(&value), "值 {value} 不在 [5,10]");
            assert!(hit.subtitle.contains("[5, 10]"), "{}", hit.subtitle);
        }
    }

    /// 二级 str/sym/hex 给长度：按长度生成 count 条。
    #[test]
    fn level2_string_param_uses_length() {
        let p = with_count(2);
        let hits = rand_mode(&p, "24", "str");
        assert_eq!(hits.len(), 3);
        for hit in &hits[1..] {
            assert_eq!(hit.title.len(), 24);
            assert!(hit.title.bytes().all(|b| b.is_ascii_alphanumeric()));
        }
        let hits = rand_mode(&p, "20", "sym");
        for hit in &hits[1..] {
            assert_eq!(hit.title.len(), 20);
            assert!(hit.title.bytes().all(|b| WITH_SYMBOLS.contains(&b)));
        }
        let hits = rand_mode(&p, "40", "hex");
        for hit in &hits[1..] {
            assert_eq!(hit.title.len(), 40);
            assert!(hit.title.bytes().all(|b| b.is_ascii_hexdigit()));
        }
    }

    /// 二级参数解析失败：错误提示，且首条「重新生成」仍在。
    #[test]
    fn level2_parse_failure_hint() {
        let p = seeded();
        for (mode, q) in [("int", "abc"), ("str", "abc"), ("hex", "x:y")] {
            let hits = rand_mode(&p, q, mode);
            assert_eq!(hits.len(), 2, "{mode}/{q}");
            assert_eq!(hits[0].action, Action::Refresh, "重新生成应仍在首位");
            assert!(hits[1].interact.is_hint(), "末行是错误提示");
            assert_eq!(hits[1].title, "无法解析");
            assert!(hits[1].subtitle.contains("示例"), "{}", hits[1].subtitle);
        }
    }

    /// 占位符随级别 / 子模式变化。
    #[test]
    fn placeholder_distinguishes_levels() {
        let p = seeded();
        assert!(p.placeholder(None).contains("随机类型"));
        assert!(p.placeholder(Some("int")).contains("范围"));
        assert!(p.placeholder(Some("str")).contains("长度"));
        assert!(p.placeholder(Some("hex")).contains("64"));
    }

    /// 一级占位符是纯选择提示，不带任何示例（如 "1-100"）。
    #[test]
    fn level1_placeholder_has_no_example() {
        let p = seeded();
        let ph = p.placeholder(None);
        assert_eq!(ph, "选择随机类型");
        assert!(!ph.contains("1-100"), "一级不该出现示例");
        assert!(!ph.contains("直接输入"), "一级不接受直接输入");
    }

    // ── 保留的纯函数 / 生成质量测试 ──

    /// 特殊字符表确实含符号（用种子固定的一次长串采样）。
    #[test]
    fn symbol_alphabet_contains_symbols() {
        let p = seeded();
        let value = p.random_string_with(256, Alphabet::WithSymbols);
        assert!(
            value.bytes().any(|b| !b.is_ascii_alphanumeric()),
            "256 位里至少该出现一个特殊字符：{value}"
        );
        // 十六进制表不含 g-z
        let hex = p.random_string_with(256, Alphabet::Hex);
        assert!(hex.bytes().all(|b| b.is_ascii_hexdigit()));
    }

    #[test]
    fn random_int_stays_within_bounds_over_many_draws() {
        let p = seeded();
        let mut seen = std::collections::HashSet::new();
        for _ in 0..200 {
            let value = p.random_int(1, 6);
            assert!((1..=6).contains(&value));
            seen.insert(value);
        }
        assert!(seen.len() > 1, "随机源似乎没在变化: {seen:?}");
    }

    #[test]
    fn degenerate_range_returns_the_only_value() {
        let p = seeded();
        assert_eq!(p.random_int(7, 7), 7);
        assert_eq!(p.random_int(5, 5), 5);
        assert_eq!(p.random_int(9, 9), 9);
    }

    #[test]
    fn string_uses_alphanumeric_alphabet_only() {
        let p = seeded();
        let value = p.random_string(64);
        assert_eq!(value.len(), 64);
        assert!(value.bytes().all(|b| b.is_ascii_alphanumeric()), "{value}");
    }

    #[test]
    fn string_is_not_constant() {
        let p = seeded();
        let a = p.random_string(32);
        let b = p.random_string(32);
        assert_ne!(a, b, "连续生成的随机串不能相同");
    }

    #[test]
    fn action_copies_the_generated_value() {
        // 一级不再生成，改在二级验证「结果即 Copy 动作、标题即值」。
        let p = seeded();
        // int / str / sym / hex 各取参数，结果首条（跳过「重新生成」）应是 Copy。
        for (mode, q) in [
            ("int", "5-10"),
            ("str", "12"),
            ("sym", "12"),
            ("hex", "12"),
            ("int", "7"),
        ] {
            let hits = rand_mode(&p, q, mode);
            let hit = &hits[1];
            let Action::Copy { text } = &hit.action else {
                panic!("random 的动作必须是 Copy：{mode}/{q}");
            };
            assert_eq!(text, &hit.title, "{mode}/{q}");
        }
    }

    #[test]
    fn fresh_query_regenerates() {
        let p = RandomProvider::new();
        // 二级 int 空输入的结果区（跳过首条重新生成）。
        assert_ne!(
            rand_mode(&p, "", "int")[1].title,
            rand_mode(&p, "", "int")[1].title,
            "每次查询必须重新生成"
        );
    }

    #[test]
    fn seeded_generation_is_reproducible() {
        // 一级不再生成，改在二级验证固定种子下的可复现性。
        assert_eq!(
            rand_mode(&seeded(), "5-10", "int")[1].title,
            rand_mode(&seeded(), "5-10", "int")[1].title
        );
        assert_eq!(
            rand_mode(&seeded(), "20", "str")[1].title,
            rand_mode(&seeded(), "20", "str")[1].title
        );
        assert_eq!(
            rand_mode(&seeded(), "", "int")[1].title,
            rand_mode(&seeded(), "", "int")[1].title,
            "二级结果在固定种子下也可复现"
        );
    }
}
