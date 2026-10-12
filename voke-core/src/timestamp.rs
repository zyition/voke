//! timestamp provider：unix 时间戳 ⇄ 可读时间（§7 的 M4 插件，对齐 PTR 的
//! "Value Generator / Unix Timestamp" 一类输入型插件）。
//!
//! ## 触发方式（M5 起：子命令面板激活）
//!
//! 输入 `#` 打开子命令面板 → 选「时间戳」→ 输入框自动变成 `# `，之后的输入
//! 只查本插件（见 [`crate::QueryEngine::prepare_with`]）。它同时保留 `#` 前缀，
//! 所以直接打 `#1700000000` 也能命中（`#` 下注册顺序第一个就是本插件）。
//! `is_global = false`：时间戳不该参与全局模糊搜索（数字串会污染应用/窗口结果）。
//!
//! ## 输出形态（用户实测反馈后重定）
//!
//! **只有 10 位 / 13 位纯数字、或 `now` 产生换算结果**，且每条结果的副标题
//! 都带上原始 ts 值与单位——用户要能同时看清"这个 ts 对应哪个本地时间、
//! 哪个 UTC"：
//!
//! | 输入 | 候选行（每行 Enter 即复制其标题） |
//! |---|---|
//! | `1700000000`（10 位） | 本地时间一行（偏移非 0 时）+ UTC 一行，副标题带 ts 与单位 |
//! | `1700000000000`（13 位） | 先除 1000，其余同上（按毫秒解读，不再另出"按秒"行） |
//! | `now` | 两行：当前 ts（秒）/（毫秒），副标题带对应可读时间 |
//! | 空 | 同 `now` 的两行（激活即见当前时间，不用先打 now）+ 一行用法 Hint |
//! | 日期串（`2026-10-10` / `2026-10-10 12:30:45` / `2026-10-10T12:30:45`） | 按**本地时间**解释，两行：ts（秒）/ ts（毫秒），副标题带可读本地时间 + 单位 |
//! | 其它位数 / 乱码 / 非法日期 | 只有用法 Hint（[`Interact::Hint`]），不出换算 |
//!
//! 时区偏移为 0 时（测试环境 / 本就配成 UTC）"本地"与"UTC"两行文本相同，
//! 这时只输出一份，不制造重复项。
//!
//! ## 时区
//!
//! 纯 core 不读系统时区（那要 `windows` crate 或时区数据库），所以偏移由外部注入
//! （[`LocalOffset`]）：生产是 `voke-sys::local_offset_seconds()`，测试用
//! [`FixedOffset`]。**接口是"给偏移"，不是"给时区名"**：本地时间只差一个偏移，
//! 拿偏移足够，且让 core 保持零平台依赖。
//!
//! ## 日期字符串 → 时间戳（`docs/BACKLOG.md` 决策 4，已实现）
//!
//! 接受保守的一组格式，**按本地时间解释**（与决策 3 "要本地时间" 一致，用注入的
//! [`LocalOffset`] 反推 UTC）。解析入口是纯函数 [`parse_date_time`]（可单测），
//! 不认识的输入 / 越界日期一律退回用法 Hint（不 panic、不返回空列表）。
//!
//! - 日期：`2026-10-10`、`2026/10/10`、`2026.10.10`（三处必须是同一种分隔符）
//! - 时间：可选，用 `T` 或空格连接，`HH:MM`（秒可省）或 `HH:MM:SS`
//! - 例：`2026-10-10 12:30`、`2026-10-10 12:30:45`、`2026-10-10T12:30:45`
//! - 只有时间没有日期（如 `12:30`）**不支持**：没有日期无从算时间戳
//!
//! 逆向算法复用 [`days_from_civil`]（与 [`civil_from_days`] 互为逆运算）。

use crate::action::Action;
use crate::clock::{Clock, SystemClock};
use crate::engine::PREFIX_TIMESTAMP;
use crate::provider::{BoxFuture, Interact, Match, Provider, ProviderConfig, QueryCtx, ready};

/// 当前时钟（重导出，保持 `vote_core::timestamp::Clock` 的既有路径可用）。
pub use crate::clock::FixedClock;

/// 本地时区相对 UTC 的偏移（秒）。
///
/// 用 `Fn() -> i32` 而不是常量：偏移随夏令时变化，每次查询现取比启动时缓存更准
/// （这也是 `voke_sys::local_offset_seconds()` 的现实行为）。
pub type LocalOffset = Box<dyn Fn() -> i32 + Send + Sync>;

/// 固定偏移（测试用，例如 UTC+8 = `8 * 3600`）。
#[derive(Clone, Copy, Debug)]
pub struct FixedOffset(pub i32);

impl FixedOffset {
    /// 包成 provider 要的闭包形态。
    pub fn boxed(self) -> LocalOffset {
        Box::new(move || self.0)
    }
}

/// 一次转换的结果：主标题 + 副标题（副标题给补充信息，可为空）。
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Conversion {
    pub title: String,
    pub subtitle: String,
    /// 选中后复制的文本。通常是 `title`；纯提示行是 `None`（动作 → [`Action::None`]）。
    pub copy: Option<String>,
}

impl Conversion {
    /// 一行"可复制的结果"。
    fn copyable(title: impl Into<String>, subtitle: impl Into<String>) -> Self {
        let title = title.into();
        Self {
            copy: Some(title.clone()),
            title,
            subtitle: subtitle.into(),
        }
    }

    /// 一行"纯提示"（不可复制，Enter 无副作用）。
    fn hint(title: impl Into<String>, subtitle: impl Into<String>) -> Self {
        Self {
            title: title.into(),
            subtitle: subtitle.into(),
            copy: None,
        }
    }

    /// 转成引擎用的 [`Match`]。`score` 由调用方给（候选顺序由它决定）。
    ///
    /// `copy` 有值 → `Execute`（Enter / 点击都复制）；没有 → `Hint`（不可选，
    /// 只在结果区下方展示）。这个映射是 timestamp 自己的不变式：
    /// 它只产生"可复制的换算结果"与"怎么用"两类行。
    fn into_match(self, score: f32) -> Match {
        match self.copy {
            Some(text) => Match {
                provider_id: TimestampProvider::ID.to_string(),
                title: self.title,
                subtitle: self.subtitle,
                icon: None,
                action: Action::Copy { text },
                score,
                interact: Interact::Execute,
            },
            None => {
                let mut m = Match::hint(TimestampProvider::ID, self.title, self.subtitle);
                m.score = score;
                m
            }
        }
    }
}

/// timestamp provider。
pub struct TimestampProvider {
    config: ProviderConfig,
    clock: Box<dyn Clock>,
    offset: LocalOffset,
}

impl TimestampProvider {
    /// provider 的稳定 id。
    pub const ID: &'static str = "timestamp";

    /// 10 位以内的纯数字按**秒**解释。
    ///
    /// 判据：`|n|` 小于 1e11 视作秒（覆盖 1970–5138 年），否则视作毫秒。
    /// 这个分界让 `1700000000`（秒）与 `1700000000000`（毫秒）都能正确识别，
    /// 且不会把 11 位以上的秒级时间戳误判（那种值本身已超出合理范围）。
    ///
    /// 旧版 `conversions` 靠它把同一输入同时按秒/毫秒两解读出四行；新规格
    /// 改为**位数定单位**（10 位 = 秒，13 位 = 毫秒），此常量保留为公开
    /// API（有外部引用），供外部判“这个数字按秒还是按毫秒”时对齐同一分界。
    pub const MS_THRESHOLD: i128 = 100_000_000_000;

    /// 生产构造：系统时钟 + UTC（偏移 0）。
    ///
    /// app 层用 [`TimestampProvider::with_clock_and_offset`] 注入真实本地时区。
    pub fn new() -> Self {
        Self::with_clock(Box::new(SystemClock))
    }

    /// 注入时钟，偏移固定为 0（测试用固定时钟）。
    pub fn with_clock(clock: Box<dyn Clock>) -> Self {
        Self::with_clock_and_offset(clock, Box::new(|| 0))
    }

    /// 注入时钟与时区偏移（生产：`SystemClock` + `voke_sys::local_offset_seconds`）。
    pub fn with_clock_and_offset(clock: Box<dyn Clock>, offset: LocalOffset) -> Self {
        Self {
            config: ProviderConfig {
                enabled: true,
                // 不进全局：`#` 前缀专用（与其它 value-generator 型插件共用）。
                is_global: false,
                weight: 1.0,
                prefix: Some(PREFIX_TIMESTAMP.to_string()),
                // `#` 系：默认间接激活（前缀 + 空格），见 config::default_activation。
                activation: crate::config::Activation::Indirect,
                count: 5,
            },
            clock,
            offset,
        }
    }

    /// 用固定时钟构造（测试便捷入口，偏移 0）。
    pub fn fixed(now_secs: i64) -> Self {
        Self::with_clock(Box::new(FixedClock(now_secs)))
    }
}

impl Default for TimestampProvider {
    fn default() -> Self {
        Self::new()
    }
}

impl std::fmt::Debug for TimestampProvider {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("TimestampProvider")
            .field("id", &Self::ID)
            .finish_non_exhaustive()
    }
}

impl Provider for TimestampProvider {
    fn id(&self) -> &'static str {
        Self::ID
    }

    fn name(&self) -> &'static str {
        "时间戳"
    }

    /// 别名（用户实测要求 `#t` / `#sj` / `#time` 都能过滤出时间戳）：
    /// `t` = 单字母快捷；`sj` = 拼音首字母（时间）；`ts` = 时间戳英文缩写；
    /// `time` = 完整英文词（`timestamp` 由 `time` 子串覆盖）。
    fn aliases(&self) -> &'static [&'static str] {
        &["t", "sj", "ts", "time"]
    }

    fn description(&self) -> &'static str {
        "时间戳 ⇄ 可读时间"
    }

    fn config(&self) -> &ProviderConfig {
        &self.config
    }

    fn set_config(&mut self, config: ProviderConfig) {
        self.config = config;
    }

    fn placeholder(&self, _sub_mode: Option<&str>) -> &'static str {
        // timestamp 无子模式：输时间戳、now、日期串，或留空看 now。
        "输入时间戳（如 1700000000）、now 或日期（2026-10-10 12:30）"
    }

    fn query(&self, ctx: &QueryCtx) -> BoxFuture<'static, Vec<Match>> {
        let now = self.clock.now_secs();
        let offset = (self.offset)();
        let matches = conversions(now, offset, &ctx.query)
            .into_iter()
            .enumerate()
            .map(|(i, c)| {
                // 候选依次递减，保证顺序稳定（第一条默认可选）。
                c.into_match(1.0 - i as f32 * 0.02)
            })
            .collect();
        ready(matches)
    }
}

/// 换算入口：把 `query` 解析成一组候选（可能为空 = 不认识的输入）。
///
/// `now_secs` / `offset_secs` 由调用方给（生产是系统时钟 + sys 层的本地偏移，
/// 测试是 [`FixedClock`] / [`FixedOffset`]）。
///
/// **输出规格**（用户实测反馈后重定）：空查询、10 位 / 13 位纯数字、`now`，或
/// 可解析的日期串产生换算结果；每条可复制行（`按秒`/`按毫秒`）的**副标题带原始
/// ts 值或可读时间**，让用户同时看到 ts 与可读时间的对应关系。空查询 = 默认展示
/// `now` 的结果 + 一行用法 Hint（用户反馈：激活后输入框是空的，应当直接看到当前
/// 时间，而不是一句说明书）。其它位数 / 乱码 / 非法日期只给用法 Hint。
pub fn conversions(now_secs: i64, offset_secs: i32, query: &str) -> Vec<Conversion> {
    let query = query.trim();

    // 空查询：**默认按 now 展示**（用户实测反馈：激活后输入框是空的，第一眼
    // 应该看到当前时间戳，而不是一句说明书）——出 now 的两行 + 用法 Hint。
    // 真正输 `now` 的路径在下面，不受影响。
    if query.is_empty() {
        // 空查询默认展示 now 的两行（用法说明已挪进占位符）。
        return now_rows(now_secs, offset_secs);
    }

    // "now"：当前时间戳（秒 + 毫秒），副标题带对应可读时间——
    // 用户复制走的是 ts 值（标题），同时能瞥见它是什么时间。
    if query.eq_ignore_ascii_case("now") {
        return now_rows(now_secs, offset_secs);
    }

    // 非纯数字（日期串含 `-`/`/`/`.`/`:`/`T`/空格，或乱码 / 只有时间）：先试日期
    // 解析（决策 4），解析失败退回空结果——用法说明已挪进占位符。
    if !query.bytes().all(|b| b.is_ascii_digit()) {
        if let Some(dt) = parse_date_time(query) {
            return date_rows(dt, offset_secs);
        }
        return vec![];
    }

    // 纯数字：**位数定单位**——10 位按秒、13 位按毫秒（先除 1000）。
    // 其它位数（含负数、前导符号）不猜：用户实测反馈过，两种解读各出两行
    // 的旧形态让列表变成噪音，而“按秒还是按毫秒”应由输入长度无歧义地决定。
    let secs: i64 = match query.len() {
        10 => match query.parse() {
            Ok(v) => v,
            Err(_) => return vec![],
        },
        13 => match query.parse::<i64>() {
            // 13 位最大 9_999_999_999_999，i64 除法不溢出。
            Ok(v) => v / 1000,
            Err(_) => return vec![],
        },
        _ => {
            // 位数纠偏提示：明确告诉用户"是 N 位"，而不是给一段用法说明。
            return vec![Conversion::hint(
                format!("{query} 是 {} 位", query.len()),
                "换算只认 10 位（秒）/ 13 位（毫秒）",
            )];
        }
    };

    let unit = if query.len() == 10 { "秒" } else { "毫秒" };
    let tag = format!("{query}（按{unit}）");
    let mut out = Vec::new();
    // 偏移为 0 时"本地"与"UTC"是同一串文本，只给一份，不造重复行。
    if offset_secs != 0 {
        out.push(Conversion::copyable(
            format_timestamp_offset(secs, offset_secs),
            format!("{tag} → 本地时间"),
        ));
    }
    out.push(Conversion::copyable(
        format_timestamp(secs),
        format!("{tag} → UTC"),
    ));
    out
}

/// `now` / 空查询共用的两行：当前 ts（秒）/（毫秒），副标题带对应可读时间。
fn now_rows(now_secs: i64, offset_secs: i32) -> Vec<Conversion> {
    let local = format_timestamp_offset(now_secs, offset_secs);
    vec![
        Conversion::copyable(now_secs.to_string(), format!("当前时间戳（秒）· {local}")),
        Conversion::copyable(
            (now_secs as i128 * 1000).to_string(),
            format!("当前时间戳（毫秒）· {local}"),
        ),
    ]
}

/// 日期串 → 时间戳：输入按**本地时间**解释，出"按秒 / 按毫秒"两行。
///
/// 与 [`now_rows`] 同构：标题是要复制的 ts 值，副标题带可读时间 + 单位。可读时间
/// 用本地偏移格式化（与输入一致，能一眼核对解析对不对）。UTC 由本地偏移反推：
/// `utc = 本地秒 − offset`。
fn date_rows(dt: CivilDateTime, offset_secs: i32) -> Vec<Conversion> {
    let local_secs = days_from_civil(dt.year, dt.month, dt.day) * 86_400
        + dt.hour as i64 * 3600
        + dt.minute as i64 * 60
        + dt.second as i64;
    // 输入按本地时间解释 → UTC = 本地 − 偏移（决策 3：本地时间语义）。
    let utc_secs = local_secs - offset_secs as i64;
    let local = format_timestamp_offset(utc_secs, offset_secs);
    vec![
        Conversion::copyable(utc_secs.to_string(), format!("{local} · 按秒")),
        Conversion::copyable(
            (utc_secs as i128 * 1000).to_string(),
            format!("{local} · 按毫秒"),
        ),
    ]
}

/// 把 unix 秒格式化成 `YYYY-MM-DD HH:MM:SS UTC`。
pub fn format_timestamp(secs: i64) -> String {
    format_timestamp_offset(secs, 0)
}

/// 把 unix 秒按给定时区偏移（秒）格式化。
///
/// 偏移只影响显示，`secs` 始终是 UTC 时间戳。生产传
/// `voke_sys::local_offset_seconds()`（见 [`LocalOffset`]）。
pub fn format_timestamp_offset(secs: i64, offset_secs: i32) -> String {
    let shifted = secs.saturating_add(offset_secs as i64);
    let days = shifted.div_euclid(86_400);
    let secs_of_day = shifted.rem_euclid(86_400);
    let (y, m, d) = civil_from_days(days);
    let (hh, mm, ss) = (
        secs_of_day / 3600,
        (secs_of_day % 3600) / 60,
        secs_of_day % 60,
    );
    let suffix = if offset_secs == 0 {
        "UTC".to_string()
    } else {
        let sign = if offset_secs < 0 { '-' } else { '+' };
        let abs = offset_secs.unsigned_abs();
        format!("{sign}{:02}:{:02}", abs / 3600, (abs % 3600) / 60)
    };
    format!("{y:04}-{m:02}-{d:02} {hh:02}:{mm:02}:{ss:02} {suffix}")
}

/// 把 "1970-01-01 起的天数" 换算成公历年月日（Howard Hinnant 的 `civil_from_days`）。
///
/// 纯整数算法，无时区/闰秒数据库，覆盖 `i64` 能表示的日期范围。
/// 出处：`http://howardhinnant.github.io/date_algorithms.html`（公有领域算法）。
fn civil_from_days(z: i64) -> (i64, u32, u32) {
    // 以 0000-03-01 为纪元（把闰年放在年末，简化闰年处理）。
    let z = z + 719_468;
    let era = if z >= 0 { z } else { z - 146_096 } / 146_097;
    let doe = z - era * 146_097; // [0, 146096]
    let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365; // [0, 399]
    let y = yoe + era * 400;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100); // [0, 365]
    let mp = (5 * doy + 2) / 153; // [0, 11]
    let d = (doy - (153 * mp + 2) / 5 + 1) as u32; // [1, 31]
    let m = if mp < 10 { mp + 3 } else { mp - 9 } as u32; // [1, 12]
    (if m <= 2 { y + 1 } else { y }, m, d)
}

/// 反向：公历年月日 → "1970-01-01 起的天数"（日期字符串解析用它算时间戳）。
fn days_from_civil(y: i64, m: u32, d: u32) -> i64 {
    let y = if m <= 2 { y - 1 } else { y };
    let era = if y >= 0 { y } else { y - 399 } / 400;
    let yoe = y - era * 400; // [0, 399]
    let mp = if m > 2 { m - 3 } else { m + 9 } as i64; // [0, 11]
    let doy = (153 * mp + 2) / 5 + d as i64 - 1; // [0, 365]
    let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy; // [0, 146096]
    era * 146_097 + doe - 719_468
}

/// 解析出的"本地墙钟时间"（尚未叠加时区偏移）。
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
struct CivilDateTime {
    year: i64,
    month: u32,
    day: u32,
    hour: u32,
    minute: u32,
    second: u32,
}

/// 解析日期字符串（时间可选）为本地墙钟时间；不认识 / 越界一律 `None`。
///
/// 纯函数（不碰时钟/时区），可单测。支持的格式见模块文档「日期字符串」一节：
/// 日期三处同一种分隔符（`-` / `/` / `.`），可选时间用 `T` 或空格连接，
/// `HH:MM`（秒可省）或 `HH:MM:SS`。年份限 4 位（`0001`–`9999`），月/日/时/分/秒
/// 各 2 位并做范围校验（含闰年）；**只有时间没有日期**（如 `12:30`）返回 `None`。
fn parse_date_time(input: &str) -> Option<CivilDateTime> {
    let s = input.trim();
    // 时间部分用 `T` 或空格分出来（都取第一次出现处）。
    let (date_s, time_s) = match s.split_once(['T', ' ']) {
        Some((d, t)) => (d.trim(), t.trim()),
        None => (s, ""),
    };
    let (year, month, day) = parse_date(date_s)?;
    // 时间缺省 = 当天 00:00:00（只有日期时）。
    let (hour, minute, second) = if time_s.is_empty() {
        (0, 0, 0)
    } else {
        parse_time(time_s)?
    };
    Some(CivilDateTime {
        year,
        month,
        day,
        hour,
        minute,
        second,
    })
}

/// 解析 `YYYY<sep>MM<sep>DD`（`sep` ∈ `-` / `/` / `.`，三处必须一致且各段位数精确）。
fn parse_date(s: &str) -> Option<(i64, u32, u32)> {
    let sep = s.chars().find(|c| matches!(c, '-' | '/' | '.'))?;
    let mut parts = s.split(sep);
    let (y, m, d) = (parts.next()?, parts.next()?, parts.next()?);
    // 多余分隔符（`2026-10-10-`）或混用（`2026-10/10`）都会在这里被挡下。
    if parts.next().is_some() {
        return None;
    }
    if y.len() != 4 || m.len() != 2 || d.len() != 2 {
        return None;
    }
    if !y.bytes().all(|b| b.is_ascii_digit())
        || !m.bytes().all(|b| b.is_ascii_digit())
        || !d.bytes().all(|b| b.is_ascii_digit())
    {
        return None;
    }
    let year: i64 = y.parse().ok()?;
    let month: u32 = m.parse().ok()?;
    let day: u32 = d.parse().ok()?;
    if !(1..=9999).contains(&year) || !(1..=12).contains(&month) {
        return None;
    }
    if day < 1 || day > days_in_month(year, month) {
        return None;
    }
    Some((year, month, day))
}

/// 解析 `HH:MM`（秒可省）或 `HH:MM:SS`，各段必须恰好 2 位并在合法范围内。
fn parse_time(s: &str) -> Option<(u32, u32, u32)> {
    let mut parts = s.split(':');
    let h = parts.next()?;
    let m = parts.next()?;
    let sec = parts.next();
    if parts.next().is_some() {
        return None;
    }
    let sec = sec.unwrap_or("00");
    if h.len() != 2 || m.len() != 2 || sec.len() != 2 {
        return None;
    }
    if !h.bytes().all(|b| b.is_ascii_digit())
        || !m.bytes().all(|b| b.is_ascii_digit())
        || !sec.bytes().all(|b| b.is_ascii_digit())
    {
        return None;
    }
    let hour: u32 = h.parse().ok()?;
    let minute: u32 = m.parse().ok()?;
    let second: u32 = sec.parse().ok()?;
    if hour > 23 || minute > 59 || second > 59 {
        return None;
    }
    Some((hour, minute, second))
}

/// 公历闰年（4 年一闰、100 年不闰、400 年再闰）。
fn is_leap_year(year: i64) -> bool {
    (year % 4 == 0 && year % 100 != 0) || year % 400 == 0
}

/// 给定年月的天数；月份非法返回 0（调用方已先校验月份范围）。
fn days_in_month(year: i64, month: u32) -> u32 {
    match month {
        1 | 3 | 5 | 7 | 8 | 10 | 12 => 31,
        4 | 6 | 9 | 11 => 30,
        2 if is_leap_year(year) => 29,
        2 => 28,
        _ => 0,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::future::Future;

    fn block_on<F: Future>(f: F) -> F::Output {
        let mut f = Box::pin(f);
        let mut cx = std::task::Context::from_waker(std::task::Waker::noop());
        match f.as_mut().poll(&mut cx) {
            std::task::Poll::Ready(v) => v,
            std::task::Poll::Pending => panic!("timestamp 的 future 应当立即就绪"),
        }
    }

    /// 模拟激活后输入框里的 `# ` 被剥掉、只剩查询词的调用（偏移 0）。
    fn ts(now: i64, q: &str) -> Vec<Match> {
        let p = TimestampProvider::fixed(now);
        let ctx = QueryCtx::new(format!("#{q}"), q.to_string());
        block_on(p.query(&ctx))
    }

    /// UTC+8 的 provider（验证本地时间行真的出现）。
    fn ts_utc8(now: i64, q: &str) -> Vec<Match> {
        let p = TimestampProvider::with_clock_and_offset(
            Box::new(FixedClock(now)),
            FixedOffset(8 * 3600).boxed(),
        );
        let ctx = QueryCtx::new(format!("#{q}"), q.to_string());
        block_on(p.query(&ctx))
    }

    fn titles(matches: &[Match]) -> Vec<String> {
        matches.iter().map(|m| m.title.clone()).collect()
    }

    #[test]
    fn is_prefix_only_and_not_global() {
        let p = TimestampProvider::fixed(0);
        assert_eq!(p.id(), "timestamp");
        assert_eq!(p.name(), "时间戳");
        assert!(!p.description().is_empty());
        assert_eq!(p.config().prefix, Some("#".to_string()));
        assert!(!p.config().is_global, "时间戳不得进全局结果");
        assert_eq!(p.config().weight, 1.0);
    }

    #[test]
    fn epoch_zero_is_formatted_correctly() {
        assert_eq!(format_timestamp(0), "1970-01-01 00:00:00 UTC");
    }

    #[test]
    fn known_timestamps_match() {
        // 1000000000 = 2001-09-09 01:46:40 UTC
        assert_eq!(format_timestamp(1_000_000_000), "2001-09-09 01:46:40 UTC");
        // 1700000000 = 2023-11-14 22:13:20 UTC
        assert_eq!(format_timestamp(1_700_000_000), "2023-11-14 22:13:20 UTC");
        // 2000-01-01 00:00:00 UTC（Y2K，闰年边界附近）
        assert_eq!(format_timestamp(946_684_800), "2000-01-01 00:00:00 UTC");
    }

    #[test]
    fn leap_day_is_handled() {
        // 2024-02-29 00:00:00 UTC
        assert_eq!(format_timestamp(1_709_164_800), "2024-02-29 00:00:00 UTC");
    }

    #[test]
    fn negative_timestamp_before_epoch() {
        assert_eq!(format_timestamp(-1), "1969-12-31 23:59:59 UTC");
        assert_eq!(format_timestamp(-86_400), "1969-12-31 00:00:00 UTC");
    }

    #[test]
    fn civil_conversion_roundtrips() {
        for days in [-1_000_000i64, -719_468, -1, 0, 1, 10_957, 20_000, 1_000_000] {
            let (y, m, d) = civil_from_days(days);
            assert_eq!(days_from_civil(y, m, d), days, "roundtrip 失败: {days}");
        }
    }

    /// 10 位输入按**秒**解读，**只出一种单位**（用户实测反馈：两种解读各出
    /// 两行的旧形态是噪音）。副标题带原始 ts 值与单位，偏移 0 时只出 UTC 一行。
    #[test]
    fn ten_digit_input_interpreted_as_seconds_only() {
        let hits = ts(0, "1700000000");
        assert_eq!(titles(&hits), vec!["2023-11-14 22:13:20 UTC"]);
        assert!(
            hits[0].subtitle.contains("1700000000"),
            "{}",
            hits[0].subtitle
        );
        assert!(hits[0].subtitle.contains("按秒"), "{}", hits[0].subtitle);
    }

    /// 13 位输入按**毫秒**解读（先除 1000），同样只出一种单位。
    #[test]
    fn thirteen_digit_input_interpreted_as_milliseconds_only() {
        let hits = ts(0, "1700000000000");
        assert_eq!(titles(&hits), vec!["2023-11-14 22:13:20 UTC"]);
        assert!(
            hits[0].subtitle.contains("1700000000000"),
            "{}",
            hits[0].subtitle
        );
        assert!(hits[0].subtitle.contains("按毫秒"), "{}", hits[0].subtitle);
    }

    /// 有本地时区偏移时：**本地在前、UTC 在后**，副标题都带原始 ts ——
    /// 用户要能同时看清 ts ↔ 本地时间 ↔ UTC 三者的对应关系。
    #[test]
    fn local_offset_produces_local_rows() {
        let hits = ts_utc8(0, "1700000000");
        assert_eq!(
            titles(&hits),
            vec![
                "2023-11-15 06:13:20 +08:00", // 本地（在前）
                "2023-11-14 22:13:20 UTC",
            ]
        );
        for hit in &hits {
            assert!(hit.subtitle.contains("1700000000"), "{}", hit.subtitle);
        }
        assert!(
            hits[0].subtitle.contains("本地时间"),
            "{}",
            hits[0].subtitle
        );
        assert!(hits[1].subtitle.contains("UTC"), "{}", hits[1].subtitle);
    }

    #[test]
    fn now_returns_current_timestamp() {
        let hits = ts(1_700_000_000, "now");
        assert_eq!(hits.len(), 2);
        assert_eq!(hits[0].title, "1700000000");
        assert_eq!(hits[1].title, "1700000000000");
        // 副标题带对应可读时间（用户要求：now 也能看到 ts ↔ 时间对应）。
        assert!(
            hits[0].subtitle.contains("2023-11-14 22:13:20"),
            "{}",
            hits[0].subtitle
        );
        assert!(hits[0].subtitle.contains("秒"), "{}", hits[0].subtitle);
        assert!(hits[1].subtitle.contains("毫秒"), "{}", hits[1].subtitle);
        assert_eq!(
            hits[0].action,
            Action::Copy {
                text: "1700000000".to_string()
            }
        );
        // 大小写不敏感
        assert_eq!(ts(1_700_000_000, "NOW")[0].title, "1700000000");
    }

    /// **空查询 = 默认展示 now 的结果**（用户实测反馈：激活后输入框是空的，
    /// 第一眼应看到当前 ts 与可读时间），不再附带用法 Hint（已挪进占位符）。
    /// 真正输 `now` 的行为不变（见下方测试）。
    #[test]
    fn empty_query_defaults_to_now() {
        let hits = ts(1_700_000_000, "");
        // 与 now 完全一致：秒 + 毫秒，都是可复制的 Execute，共 2 行。
        assert_eq!(hits.len(), 2, "{:?}", titles(&hits));
        assert_eq!(&titles(&hits)[..], &titles(&ts(1_700_000_000, "now"))[..]);
        for hit in &hits {
            assert_eq!(hit.interact, Interact::Execute, "{:?}", hit.title);
            assert!(
                matches!(hit.action, Action::Copy { .. }),
                "{:?}",
                hit.action
            );
        }
    }
    /// 输 `now` 显式要求当前时间戳（与空查询的默认展示同一形态）。
    #[test]
    fn now_is_the_way_to_get_the_current_timestamp() {
        let hits = ts(1_700_000_000, "now");
        assert_eq!(titles(&hits), vec!["1700000000", "1700000000000"]);
        assert!(hits.iter().all(|m| m.interact == Interact::Execute));
    }

    /// 非法输入（乱码 / 非法日期 / 错误位数）不产生换算结果，也不给用法提示
    /// （用法说明已挪进占位符），直接返回空列表。
    ///
    /// 用户实测反馈：旧形态对任意非空数字都给"两种解读 × 两个时区"四行，
    /// 列表变成噪音。新规格只有 10/13 位纯数字、now、或可解析日期串出结果。
    #[test]
    fn invalid_inputs_produce_no_hint() {
        for bad in [
            "abc",
            "1.5e3", // 非纯数字
            "17000000000x",
            "0x10",
            "++",
            "-1700000000", // 带符号
            "2026-13-01",  // 月 13 越界
            "2025-02-30",  // 2 月 30 非法
            "12:30",       // 只有时间没有日期
        ] {
            let hits = ts(0, bad);
            assert_eq!(hits.len(), 0, "{bad:?} 应无结果行，实际 {hits:?}");
        }
    }

    /// 位数纠偏提示保留：错位数给一条 Hint，明确"是 N 位"（用法说明已挪进占位符）。
    #[test]
    fn wrong_digit_count_gives_correction_hint() {
        for bad in [
            "170000000",
            "17000000000",
            "170000000000",
            "17000000000000",
            "0",
        ] {
            let hits = ts(0, bad);
            assert_eq!(hits.len(), 1, "{bad:?} 应恰好一条纠偏提示，实际 {hits:?}");
            assert!(hits[0].interact.is_hint(), "{bad:?} 不该是可选项");
            assert_eq!(hits[0].action, Action::None, "{bad:?}");
            assert!(hits[0].title.contains("位"), "{bad:?}: {}", hits[0].title);
        }
    }

    /// 只有恰好 10 位 / 13 位才出换算（位数定单位，无歧义）。
    #[test]
    fn only_10_or_13_digits_convert() {
        // 10 位 → 秒。
        let hits = ts(0, "9999999999");
        assert!(hits[0].subtitle.contains("按秒"), "{}", hits[0].subtitle);
        // 13 位 → 毫秒（先除 1000）：1700000000000 ms = 1700000000 s。
        let hits = ts(0, "1700000000000");
        assert!(hits[0].subtitle.contains("按毫秒"), "{}", hits[0].subtitle);
        assert_eq!(hits[0].title, "2023-11-14 22:13:20 UTC");
    }

    #[test]
    fn zero_seconds_is_valid_input() {
        // "0" 补齐语义是 10 位吗？不——"0" 只有 1 位，按位数规则**不出换算**。
        // 这是有意的：epoch 附近的时间戳用户会写成完整的 10 位（"0000000000"）。
        let hits = ts(0, "0");
        assert_eq!(hits.len(), 1);
        assert!(hits[0].interact.is_hint());
        // 完整 10 位的 0 照常换算。
        let hits = ts(0, "0000000000");
        assert_eq!(hits[0].title, "1970-01-01 00:00:00 UTC");
    }

    #[test]
    fn timezone_offset_shifts_display_only() {
        // UTC+8：同一时间戳显示成 8 小时后的本地时间
        let text = format_timestamp_offset(1_700_000_000, 8 * 3600);
        assert_eq!(text, "2023-11-15 06:13:20 +08:00");
        // 负偏移
        let text = format_timestamp_offset(1_700_000_000, -5 * 3600);
        assert_eq!(text, "2023-11-14 17:13:20 -05:00");
    }

    /// 可复制行的标题与复制文本必须一致（Enter 拿到的就是看到的那串）。
    #[test]
    fn copyable_rows_title_equals_copied_text() {
        for q in ["1700000000", "1700000000000", "now", ""] {
            for hit in ts(1_700_000_000, q) {
                if let Action::Copy { text } = &hit.action {
                    assert_eq!(&hit.title, text, "{q} / {}", hit.title);
                }
            }
        }
    }

    /// 提示行不可选、不产生副作用（`Hint` 不变式）：位数纠偏提示同样不可复制，
    /// now 那两行则必须是可复制的 Execute。
    #[test]
    fn hint_rows_are_not_copyable() {
        let hits = ts(0, "0");
        assert!(hits[0].interact.is_hint(), "位数纠偏是 Hint");
        assert_eq!(hits[0].action, Action::None);
        let now_hits = ts(0, "");
        assert!(now_hits.iter().all(|m| m.interact == Interact::Execute));
    }

    /// 可复制行的交互类型必须是 `Execute`（Enter / 点击都复制）。
    #[test]
    fn copyable_rows_are_execute() {
        for q in ["1700000000", "1700000000000", "now"] {
            let hits = ts(1_700_000_000, q);
            assert!(
                hits.iter().all(|m| m.interact == Interact::Execute),
                "{q}: {:?}",
                hits.iter().map(|m| &m.interact).collect::<Vec<_>>()
            );
        }
    }

    #[test]
    fn placeholder_is_a_usage_hint() {
        let p = TimestampProvider::fixed(0);
        assert!(p.placeholder(None).contains("时间戳"));
    }

    // ── 决策 4：日期字符串 → 时间戳 ──

    /// 纯解析函数直测：只有时间 / now / 纯数字都不是日期串。
    #[test]
    fn parse_date_time_accepts_only_full_dates() {
        assert_eq!(
            parse_date_time("2026-10-10 12:30:45"),
            Some(CivilDateTime {
                year: 2026,
                month: 10,
                day: 10,
                hour: 12,
                minute: 30,
                second: 45
            })
        );
        // 外围空白被 trim。
        assert!(parse_date_time("  2026-10-10  ").is_some());
        for bad in ["12:30", "now", "1700000000", "", "2026-10-10T12"] {
            assert!(parse_date_time(bad).is_none(), "{bad:?} 不该被当成日期串");
        }
    }

    /// 支持的日期分隔符（`-` / `/` / `.`）与可选时间形态，全部走 provider 全链路：
    /// 两行（秒 + 毫秒），秒行标题反格式化后应等于输入的本地 UTC 读法。
    #[test]
    fn supported_date_formats_convert_to_timestamp() {
        // (输入, offset 0 下该时刻的可读 UTC)
        let cases = [
            ("2026-10-10", "2026-10-10 00:00:00 UTC"),
            ("2026/10/10", "2026-10-10 00:00:00 UTC"),
            ("2026.10.10", "2026-10-10 00:00:00 UTC"),
            ("2026-10-10 12:30", "2026-10-10 12:30:00 UTC"),
            ("2026/10/10 12:30:45", "2026-10-10 12:30:45 UTC"),
            ("2026.10.10T12:30:45", "2026-10-10 12:30:45 UTC"),
            ("2026-10-10T12:30", "2026-10-10 12:30:00 UTC"),
        ];
        for (input, readable_utc) in cases {
            let hits = ts(0, input);
            assert_eq!(hits.len(), 2, "{input}: {:?}", titles(&hits));
            let secs: i64 = hits[0]
                .title
                .parse()
                .unwrap_or_else(|_| panic!("{input} 秒行标题应为数字"));
            assert_eq!(format_timestamp(secs), readable_utc, "{input}");
            // 毫秒行 = 秒 × 1000。
            assert_eq!(hits[1].title, format!("{}000", hits[0].title), "{input}");
            // 副标题带单位说明。
            assert!(
                hits[0].subtitle.contains("按秒"),
                "{input}: {}",
                hits[0].subtitle
            );
            assert!(
                hits[1].subtitle.contains("按毫秒"),
                "{input}: {}",
                hits[1].subtitle
            );
            // 两行都可复制（Execute + Copy）。
            for hit in &hits {
                assert_eq!(hit.interact, Interact::Execute, "{input}");
                assert!(matches!(hit.action, Action::Copy { .. }), "{input}");
            }
        }
    }

    /// 只有日期时时间缺省为 00:00:00。
    #[test]
    fn date_only_defaults_to_midnight() {
        assert_eq!(
            parse_date_time("2026-10-10"),
            Some(CivilDateTime {
                year: 2026,
                month: 10,
                day: 10,
                hour: 0,
                minute: 0,
                second: 0
            })
        );
    }

    /// 日期串按**本地时间**解释（决策 3）：本地偏移非 0 与 0 两种情形。
    #[test]
    fn date_string_is_interpreted_as_local_time() {
        // UTC+8：本地 `2023-11-15 06:13:20` = UTC `2023-11-14 22:13:20` = 1700000000。
        let hits = ts_utc8(0, "2023-11-15 06:13:20");
        assert_eq!(hits.len(), 2, "{:?}", titles(&hits));
        assert_eq!(hits[0].title, "1700000000");
        // 副标题用本地偏移格式化（+08:00），可一眼核对解析对不对。
        assert!(hits[0].subtitle.contains("+08:00"), "{}", hits[0].subtitle);
        assert!(
            hits[0].subtitle.contains("2023-11-15 06:13:20"),
            "{}",
            hits[0].subtitle
        );
        // 偏移 0：本地 = UTC 的写法。
        let hits = ts(0, "2023-11-14 22:13:20");
        assert_eq!(hits[0].title, "1700000000");
        assert!(hits[0].subtitle.contains("UTC"), "{}", hits[0].subtitle);
    }

    /// 闰年：`2024-02-29` 合法；非闰年 `2023-02-29` 非法 → 仅 Hint。
    #[test]
    fn leap_year_dates_are_validated() {
        let hits = ts(0, "2024-02-29");
        assert_eq!(hits.len(), 2, "{:?}", titles(&hits));
        assert_eq!(
            format_timestamp(hits[0].title.parse().unwrap()),
            "2024-02-29 00:00:00 UTC"
        );

        let hits = ts(0, "2023-02-29");
        assert_eq!(hits.len(), 0, "非闰年 2/29 应无结果: {:?}", titles(&hits));
    }

    /// 非法日期 / 越界 / 只有时间 / 格式不认识的乱码 → 空列表（用法说明已挪进占位符），
    /// 不 panic、不出空列表之外的内容。
    #[test]
    fn invalid_date_strings_produce_no_hint() {
        for bad in [
            "2026-13-01",          // 月 13
            "2026-00-10",          // 月 0
            "2026-10-00",          // 日 0
            "2026-10-32",          // 日 32
            "2026-02-30",          // 2 月 30
            "2023-02-29",          // 非闰年 2/29
            "0000-01-01",          // 年 0 越界
            "10000-01-01",         // 年 5 位
            "999-01-01",           // 年 3 位
            "2026-1-1",            // 月/日非 2 位
            "2026-10/10",          // 分隔符混用
            "2026-10-10-",         // 多余分隔符
            "2026-10-10 25:00",    // 时 25
            "2026-10-10 12:60",    // 分 60
            "2026-10-10 12:30:61", // 秒 61
            "2026-10-10 1:30",     // 时非 2 位
            "12:30",               // 只有时间，没有日期
            "2026-10-10T12",       // 缺分钟
        ] {
            let hits = ts(0, bad);
            assert_eq!(hits.len(), 0, "{bad:?} 应无结果: {:?}", titles(&hits));
        }
    }

    /// 日期行的标题（复制的文本）与副标题信息一致：毫秒 = 秒 × 1000。
    #[test]
    fn date_rows_seconds_and_millis_are_consistent() {
        let hits = ts(0, "2026-10-10 08:00:00");
        assert_eq!(hits.len(), 2);
        let secs: i128 = hits[0].title.parse().unwrap();
        let millis: i128 = hits[1].title.parse().unwrap();
        assert_eq!(millis, secs * 1000);
        for hit in &hits {
            if let Action::Copy { text } = &hit.action {
                assert_eq!(&hit.title, text, "标题应与复制文本一致");
            } else {
                panic!("日期行应可复制: {:?}", hit.action);
            }
        }
    }
}
