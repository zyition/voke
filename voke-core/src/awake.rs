//! awake provider：保持系统唤醒（§5 的 Awake 模块，M4 唯一有状态的插件）。
//!
//! ## 触发方式
//!
//! `is_global = true`：直接输入 `awake` 就能命中（对齐 §3 的配置示例
//! `[provider.awake] is_global = true`）。也**没有前缀**——它的触发词就是它的名字。
//! 子命令面板里同样能激活它（激活后输入框留空，列表直接就是候选时长）。
//!
//! ## 有状态：状态从哪来
//!
//! 真正的状态（`SetThreadExecutionState` 是否在生效）在 **sys 线程**里，core 拿不到
//! 也不该拿（core 零平台依赖）。所以这里注入一个**状态读取闭包**
//! （[`AwakeStatusFetcher`]），与 `WindowsProvider` 的窗口枚举闭包同款思路：
//!
//! ```text
//! app 层注入 Arc<dyn Fn() -> Option<AwakeStatus>>   ← 读 voke_sys::awake 的原子状态
//!          │
//!          ▼
//! AwakeProvider::query(ctx)
//!          │  未激活 → 候选时长列表（30/60/120/480 分钟 + 永久 + 自定义提示）
//!          │  已激活 → 当前剩余时间（展示行）+「取消保持唤醒」
//! ```
//!
//! 查询时现读（不缓存）：状态会被"到期自动结束"和"用户取消"改变，缓存即出错。
//!
//! ## 交互（两级子模式）
//!
//! 用 `awake` 直接进入**一级**：列出两个方向（`EnterSubquery`，进入后选时长档位）——
//! 「仅系统唤醒」（`sub_mode = "system"`）与「连屏幕一起保持」（`sub_mode = "screen"`）。
//! 一级空输入只给这两个方向；直接打分钟数（1–1440）走**快捷路径**：直接出一条
//! 「仅系统唤醒」的可执行结果，不需要先进入二级。
//!
//! 已激活时，一级第一条从「状态提示」改成**可选中取消项**（选中即 `CancelAwake`），
//! 其余仍是两个方向。
//!
//! 进入二级后（`sub_mode` 已定，亮屏与否由它决定）：
//!
//! | 二级输入 | 候选 |
//! |---|---|
//! | 空 | 5 档：30 分钟、1 小时、2 小时、8 小时、永久（各一行，Enter 即生效） |
//! | `90` 等纯数字 | 仅一条自定义时长（不列预设档位） |
//! | 越界 / 非数字 | 一条错误提示（`Hint`，进提示区） |
//!
//! "自定义时长"就是直接打分钟数——不为它单独做一个输入步骤。

use std::sync::Arc;

use crate::action::Action;
use crate::clock::{Clock, SystemClock};
use crate::provider::{BoxFuture, Interact, Match, Provider, ProviderConfig, QueryCtx, ready};

/// 保持唤醒的当前状态（由 sys 层提供）。
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct AwakeStatus {
    /// 到期时刻（unix 秒）；`None` = 永久。
    pub expires_at: Option<i64>,
    /// 是否连屏幕一起保持。
    pub keep_screen: bool,
}

impl AwakeStatus {
    /// 剩余秒数；永久返回 `None`；已到期返回 `Some(0)`。
    pub fn remaining_secs(&self, now: i64) -> Option<i64> {
        self.expires_at.map(|at| (at - now).max(0))
    }

    /// 人类可读的剩余时间。
    pub fn remaining_label(&self, now: i64) -> String {
        match self.remaining_secs(now) {
            None => "永久".to_string(),
            Some(secs) if secs <= 0 => "即将结束".to_string(),
            Some(secs) if secs < 60 => format!("还剩 {secs} 秒"),
            Some(secs) if secs < 3600 => format!("还剩 {} 分钟", secs / 60),
            Some(secs) => {
                let (h, m) = (secs / 3600, (secs % 3600) / 60);
                if m == 0 {
                    format!("还剩 {h} 小时")
                } else {
                    format!("还剩 {h} 小时 {m} 分钟")
                }
            }
        }
    }

    /// 副标题里的保持范围文案（方案 A 的措辞修正："仅系统" → "保持系统唤醒"）。
    pub fn screen_label(&self) -> &'static str {
        scope_label(self.keep_screen)
    }
}

/// 状态读取闭包（app 层注入）。
///
/// `Arc<dyn Fn>` 而不是 trait object：provider 要能塞进 `Vec<Box<dyn Provider>>`，
/// 且"读一个快照"这个动作没有别的语义需要抽象。
pub type AwakeStatusFetcher = Arc<dyn Fn() -> Option<AwakeStatus> + Send + Sync>;

/// 预置时长（分钟）——决策 6 的 30/60/120/480。
pub const PRESET_MINUTES: &[u32] = &[30, 60, 120, 480];

/// 单次自定义时长的上限（24 小时）。更大没有实际意义，还会被误输入打出来。
pub const MAX_CUSTOM_MINUTES: u32 = 24 * 60;

/// awake provider。
pub struct AwakeProvider {
    config: ProviderConfig,
    /// 状态来源（不缓存：到期/取消都会改变它）。
    fetch: AwakeStatusFetcher,
    clock: Box<dyn Clock>,
}

impl AwakeProvider {
    /// provider 的稳定 id。
    pub const ID: &'static str = "awake";

    /// 用状态源构造（生产）。
    pub fn new(fetch: AwakeStatusFetcher) -> Self {
        Self::with_clock(fetch, Box::new(SystemClock))
    }

    /// 注入时钟（测试用固定时钟）。
    pub fn with_clock(fetch: AwakeStatusFetcher, clock: Box<dyn Clock>) -> Self {
        Self {
            config: ProviderConfig {
                enabled: true,
                // §3 的配置示例：直接输入 awake 即命中，无需前缀。
                is_global: true,
                weight: 1.0,
                prefix: None,
                activation: crate::config::Activation::Direct,
                count: 5,
            },
            fetch,
            clock,
        }
    }

    /// 无状态源的空实现（测试/非 Windows 构建占位）。
    pub fn always_off() -> Self {
        Self::new(Arc::new(|| None))
    }

    /// 一条候选：时长选项。
    fn option_match(
        &self,
        minutes: Option<u32>,
        keep_screen: bool,
        score: f32,
        note: &str,
    ) -> Match {
        let label = match minutes {
            None => "永久".to_string(),
            Some(m) if m % 60 == 0 && m >= 60 => format!("{} 小时", m / 60),
            Some(m) => format!("{m} 分钟"),
        };
        let scope = scope_label(keep_screen);
        Match {
            provider_id: Self::ID.to_string(),
            title: label,
            subtitle: if note.is_empty() {
                scope.to_string()
            } else {
                format!("{scope} · {note}")
            },
            icon: None,
            action: Action::SetAwake {
                minutes,
                keep_screen,
            },
            score,
            interact: Interact::Execute,
        }
    }

    /// 一级：已激活时第一条是可选中的取消项，随后是两个进入二级的方向；
    /// 空输入只给这两个方向；纯数字走快捷路径；其它输入给错误提示。
    fn query_level1(&self, status: Option<AwakeStatus>, now: i64, query: &str) -> Vec<Match> {
        let mut out: Vec<Match> = Vec::new();

        // 已激活：把"还剩 xx"状态行升级为可选中的取消项（选中即恢复正常休眠）。
        // 标题用「屏幕打开 / 屏幕关闭」**结果描述**当前保持范围，与托盘条目同口径
        // （进二级的「连屏幕一起保持」是动作描述，不混用）。
        if let Some(status) = status {
            let scope = if status.keep_screen {
                "屏幕打开"
            } else {
                "屏幕关闭"
            };
            out.push(Match::execute(
                Self::ID,
                format!("保持唤醒中 · {scope}（{}）", status.remaining_label(now)),
                "选中取消，恢复正常休眠",
                Action::CancelAwake,
                1.0,
            ));
        }

        if query.is_empty() {
            // 空：只给两个方向（已激活时上方还有取消项）。
            self.push_directions(&mut out, status.map(|s| s.keep_screen));
            return out;
        }

        // 快捷路径：纯数字直接出一条"仅系统唤醒"的可执行结果。
        if let Ok(minutes) = query.parse::<u32>() {
            if (1..=MAX_CUSTOM_MINUTES).contains(&minutes) {
                return vec![self.option_match(
                    Some(minutes),
                    false,
                    1.0,
                    "连屏幕请进入「连屏幕一起保持」",
                )];
            }
            // 越界：单独一条错误提示（不带导航，避免误以为还能进二级）。
            let mut hint = Match::hint(
                Self::ID,
                "时长超出范围",
                format!("请输入 1–{MAX_CUSTOM_MINUTES} 分钟"),
            );
            hint.score = 1.0;
            return vec![hint];
        }

        // 非数字非关键词：导航项留在上方 + 错误提示。
        self.push_directions(&mut out, status.map(|s| s.keep_screen));
        out.push(Match::hint(
            Self::ID,
            "无效输入",
            "输入分钟数（1–1440），或选择上方选项",
        ));
        out
    }

    /// 两个进入二级的方向（EnterSubquery，app 记下 sub_mode 后回传）。
    ///
    /// `active_screen`：`Some(true)` = 当前激活方向是 screen、`Some(false)` =
    /// system、`None` = 未激活。激活态下给当前生效的方向加「当前」标记，
    /// 让用户一眼看清现在连的是哪种。
    fn push_directions(&self, out: &mut Vec<Match>, active_screen: Option<bool>) {
        let mark = |title: &str, is_current: bool| -> String {
            if is_current {
                format!("{title}（当前）")
            } else {
                title.to_string()
            }
        };
        let sys_current = active_screen == Some(false);
        let scr_current = active_screen == Some(true);
        out.push(Match::subquery(
            Self::ID,
            mark("仅系统唤醒", sys_current),
            "进入后选时长档位（30 分钟 ~ 永久）",
            "system",
            0.9,
        ));
        out.push(Match::subquery(
            Self::ID,
            mark("连屏幕一起保持", scr_current),
            "进入后选时长档位，连屏幕一起保持",
            "screen",
            0.8,
        ));
    }

    /// 二级（sub_mode 已定亮屏与否）：空输入列 5 档预设；纯数字出单条自定义时长；
    /// 越界 / 非数字给错误提示。
    fn query_level2(
        &self,
        _status: Option<AwakeStatus>,
        _now: i64,
        sub_mode: &str,
        query: &str,
    ) -> Vec<Match> {
        let keep_screen = sub_mode == "screen";

        if query.is_empty() {
            let mut out: Vec<Match> = PRESET_MINUTES
                .iter()
                .enumerate()
                .map(|(i, m)| self.option_match(Some(*m), keep_screen, 1.0 - i as f32 * 0.02, ""))
                .collect();
            // 永久（无时限）。
            out.push(self.option_match(
                None,
                keep_screen,
                1.0 - PRESET_MINUTES.len() as f32 * 0.02,
                "",
            ));
            return out;
        }

        if let Ok(minutes) = query.parse::<u32>() {
            if (1..=MAX_CUSTOM_MINUTES).contains(&minutes) {
                // 只出 1 条自定义时长，不列预设档位。
                return vec![self.option_match(Some(minutes), keep_screen, 1.0, "")];
            }
            let mut hint = Match::hint(
                Self::ID,
                "时长超出范围",
                format!("请输入 1–{MAX_CUSTOM_MINUTES} 分钟"),
            );
            hint.score = 1.0;
            return vec![hint];
        }

        vec![Match::hint(Self::ID, "无效输入", "仅支持分钟数（1–1440）")]
    }
}

impl Provider for AwakeProvider {
    fn id(&self) -> &'static str {
        Self::ID
    }

    fn name(&self) -> &'static str {
        "保持唤醒"
    }

    /// 别名：`awake` = 英文词（全局搜索也靠它命中）；`a` = 单字母快捷。
    /// **不加 b 开头的拼音首字母**（bcjx 之类）：别名是子串匹配，
    /// `#b` 必须唯一落在 Base64 上（bcjx.contains("b") 同样会命中）。
    fn aliases(&self) -> &'static [&'static str] {
        &["awake", "a"]
    }

    fn description(&self) -> &'static str {
        "阻止系统休眠（可选连屏幕一起）"
    }

    fn config(&self) -> &ProviderConfig {
        &self.config
    }

    fn set_config(&mut self, config: ProviderConfig) {
        self.config = config;
    }

    fn placeholder(&self, sub_mode: Option<&str>) -> &'static str {
        match sub_mode {
            // 二级（已进入某方向）：只输分钟数，或选上方档位。
            Some("system") => "输入分钟数（1–1440），或选上方档位",
            Some("screen") => "输入分钟数（1–1440），或选上方档位（连屏幕一起保持）",
            // 一级：先选亮屏方式，或直接打分钟数快捷设置。
            _ => "选择亮屏方式，或输入分钟数快捷设置",
        }
    }

    fn query(&self, ctx: &QueryCtx) -> BoxFuture<'static, Vec<Match>> {
        let now = self.clock.now_secs();
        let status = (self.fetch)();
        let query = ctx.query.trim();

        match ctx.sub_mode.as_deref() {
            // 二级：亮屏与否由 sub_mode 决定。
            Some("system") | Some("screen") => {
                ready(self.query_level2(status, now, ctx.sub_mode.as_deref().unwrap(), query))
            }
            // 一级（含未知 sub_mode 兜底）：导航 + 快捷路径。
            _ => ready(self.query_level1(status, now, query)),
        }
    }
}

/// 保持范围的副标题文案。
///
/// 方案 A 的措辞修正：原文案"仅系统"容易被读成"只唤醒系统"（没意义），
/// 改成动词短语"保持系统唤醒"——它说的就是插件名那件事。
fn scope_label(keep_screen: bool) -> &'static str {
    if keep_screen {
        "保持系统唤醒 + 屏幕"
    } else {
        "保持系统唤醒"
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::clock::FixedClock;
    use std::future::Future;

    fn block_on<F: Future>(f: F) -> F::Output {
        let mut f = Box::pin(f);
        let mut cx = std::task::Context::from_waker(std::task::Waker::noop());
        match f.as_mut().poll(&mut cx) {
            std::task::Poll::Ready(v) => v,
            std::task::Poll::Pending => panic!("awake 的 future 应当立即就绪"),
        }
    }

    const NOW: i64 = 1_700_000_000;

    fn provider(status: Option<AwakeStatus>) -> AwakeProvider {
        AwakeProvider::with_clock(Arc::new(move || status), Box::new(FixedClock(NOW)))
    }

    /// 一级查询（sub_mode 为空）。
    fn query(p: &AwakeProvider, q: &str) -> Vec<Match> {
        let ctx = QueryCtx::new(q.to_string(), q.to_string());
        block_on(p.query(&ctx))
    }

    /// 二级查询（带 sub_mode）。
    fn query_mode(p: &AwakeProvider, q: &str, mode: &str) -> Vec<Match> {
        let ctx = QueryCtx::with_sub_mode(q.to_string(), q.to_string(), Some(mode));
        block_on(p.query(&ctx))
    }

    fn kinds(matches: &[Match]) -> Vec<Action> {
        matches.iter().map(|m| m.action.clone()).collect()
    }

    #[test]
    fn id_and_config_match_architecture_doc() {
        let p = provider(None);
        assert_eq!(p.id(), "awake");
        assert_eq!(p.name(), "保持唤醒");
        assert!(!p.description().is_empty());
        // §3 的 `[provider.awake] is_global = true`：输入 awake 直接命中。
        assert!(p.config().is_global);
        assert_eq!(p.config().prefix, None, "awake 靠名字触发，不设前缀");
        assert_eq!(p.config().weight, 1.0);
    }

    /// 一级空输入（未激活）：只列两个进入二级的方向。
    #[test]
    fn level1_empty_lists_two_directions() {
        let p = provider(None);
        let hits = query(&p, "");
        assert_eq!(
            hits.len(),
            2,
            "{:?}",
            hits.iter().map(|m| &m.title).collect::<Vec<_>>()
        );
        assert_eq!(hits[0].title, "仅系统唤醒");
        assert_eq!(hits[1].title, "连屏幕一起保持");
        assert_eq!(
            hits[0].interact,
            Interact::EnterSubquery("system".to_string())
        );
        assert_eq!(
            hits[1].interact,
            Interact::EnterSubquery("screen".to_string())
        );
        assert!(hits.iter().all(|m| m.action == Action::None));
    }

    /// 一级空输入（已激活）：第一条是可选中的取消项，随后两个方向。
    #[test]
    fn level1_active_lists_cancel_then_directions() {
        let status = AwakeStatus {
            expires_at: Some(NOW + 25 * 60),
            keep_screen: true,
        };
        let p = provider(Some(status));
        let hits = query(&p, "");
        assert_eq!(hits.len(), 3);
        // 第一条：取消项（Execute + CancelAwake），标题带剩余时间、并标明屏幕状态。
        assert_eq!(hits[0].interact, Interact::Execute);
        assert_eq!(hits[0].action, Action::CancelAwake);
        assert!(hits[0].title.contains("保持唤醒中"), "{}", hits[0].title);
        assert!(hits[0].title.contains("屏幕打开"), "{}", hits[0].title);
        assert!(hits[0].title.contains("还剩 25 分钟"), "{}", hits[0].title);
        assert!(
            hits[0].subtitle.contains("选中取消"),
            "{}",
            hits[0].subtitle
        );
        // 后两条仍是方向。
        assert_eq!(
            hits[1].interact,
            Interact::EnterSubquery("system".to_string())
        );
        assert_eq!(
            hits[2].interact,
            Interact::EnterSubquery("screen".to_string())
        );
    }

    /// 激活态首条标题区分屏幕状态：屏幕打开 / 屏幕关闭一眼可辨。
    #[test]
    fn level1_active_title_shows_screen_scope() {
        let screen = provider(Some(AwakeStatus {
            expires_at: Some(NOW + 25 * 60),
            keep_screen: true,
        }));
        let hits = query(&screen, "");
        assert!(hits[0].title.contains("屏幕打开"), "{}", hits[0].title);
        assert!(!hits[0].title.contains("屏幕关闭"), "{}", hits[0].title);

        let system = provider(Some(AwakeStatus {
            expires_at: Some(NOW + 25 * 60),
            keep_screen: false,
        }));
        let hits = query(&system, "");
        assert!(hits[0].title.contains("屏幕关闭"), "{}", hits[0].title);
        assert!(!hits[0].title.contains("屏幕打开"), "{}", hits[0].title);
    }

    /// 激活态下，两个方向条目中当前生效的那条带「当前」标记。
    #[test]
    fn level1_active_direction_marks_current() {
        // 当前是 screen。
        let screen = provider(Some(AwakeStatus {
            expires_at: Some(NOW + 25 * 60),
            keep_screen: true,
        }));
        let hits = query(&screen, "");
        assert_eq!(hits[1].title, "仅系统唤醒");
        assert_eq!(hits[2].title, "连屏幕一起保持（当前）");

        // 当前是 system。
        let system = provider(Some(AwakeStatus {
            expires_at: Some(NOW + 25 * 60),
            keep_screen: false,
        }));
        let hits = query(&system, "");
        assert_eq!(hits[1].title, "仅系统唤醒（当前）");
        assert_eq!(hits[2].title, "连屏幕一起保持");
    }

    /// 未激活时两个方向都不带「当前」标记。
    #[test]
    fn level1_inactive_directions_unmarked() {
        let p = provider(None);
        let hits = query(&p, "");
        assert_eq!(hits[0].title, "仅系统唤醒");
        assert_eq!(hits[1].title, "连屏幕一起保持");
    }

    /// 一级快捷路径：纯数字直接出一条「仅系统唤醒」可执行结果。
    #[test]
    fn level1_quick_numeric_is_system_only() {
        let p = provider(None);
        let hits = query(&p, "45");
        assert_eq!(hits.len(), 1);
        assert_eq!(
            hits[0].action,
            Action::SetAwake {
                minutes: Some(45),
                keep_screen: false
            }
        );
        assert!(hits[0].title.contains("45 分钟"), "{}", hits[0].title);
        assert!(
            hits[0].subtitle.contains("保持系统唤醒"),
            "{}",
            hits[0].subtitle
        );
        assert!(
            hits[0].subtitle.contains("连屏幕请进入"),
            "应提示如何切到连屏幕：{}",
            hits[0].subtitle
        );
    }

    /// 一级纯数字越界：只给错误提示（不产生 SetAwake）。
    #[test]
    fn level1_quick_numeric_out_of_range() {
        let p = provider(None);
        for bad in ["0", "1441", "99999"] {
            let hits = query(&p, bad);
            assert_eq!(hits.len(), 1, "{bad}");
            assert_eq!(hits[0].action, Action::None, "{bad} 不该产生 SetAwake");
            assert!(hits[0].interact.is_hint(), "{bad} 应是提示行");
            assert!(hits[0].title.contains("时长超出范围"), "{bad}");
        }
    }

    /// 一级非数字：错误提示，且导航项留在上方。
    #[test]
    fn level1_invalid_keyword_shows_hint_above_nav() {
        let p = provider(None);
        let hits = query(&p, "abc");
        // 2 个导航 + 1 条错误提示。
        assert_eq!(hits.len(), 3);
        assert_eq!(
            hits[0].interact,
            Interact::EnterSubquery("system".to_string())
        );
        assert_eq!(
            hits[1].interact,
            Interact::EnterSubquery("screen".to_string())
        );
        assert!(hits[2].interact.is_hint());
        assert_eq!(hits[2].title, "无效输入");
        assert!(hits[2].subtitle.contains("1–1440"));
    }

    /// 二级（system）空输入：5 档预设，全部「仅系统唤醒」。
    #[test]
    fn level2_system_empty_lists_presets() {
        let p = provider(None);
        let hits = query_mode(&p, "", "system");
        let titles: Vec<&str> = hits.iter().map(|m| m.title.as_str()).collect();
        assert_eq!(
            titles,
            vec!["30 分钟", "1 小时", "2 小时", "8 小时", "永久"]
        );
        for hit in &hits {
            match &hit.action {
                Action::SetAwake { keep_screen, .. } => assert!(!*keep_screen, "system 不带屏幕"),
                other => panic!("二级预设必须是 SetAwake，实际 {other:?}"),
            }
            assert!(hit.subtitle.contains("保持系统唤醒"), "{}", hit.subtitle);
            assert!(!hit.subtitle.contains("+ 屏幕"), "{}", hit.subtitle);
        }
    }

    /// 二级（screen）空输入：5 档预设，全部「连屏幕一起保持」。
    #[test]
    fn level2_screen_empty_keeps_screen() {
        let p = provider(None);
        let hits = query_mode(&p, "", "screen");
        assert_eq!(hits.len(), 5);
        for hit in &hits {
            match &hit.action {
                Action::SetAwake { keep_screen, .. } => assert!(*keep_screen, "screen 应带屏幕"),
                other => panic!("二级预设必须是 SetAwake，实际 {other:?}"),
            }
            assert!(
                hit.subtitle.contains("保持系统唤醒 + 屏幕"),
                "{}",
                hit.subtitle
            );
        }
    }

    /// 二级纯数字：只出 1 条自定义时长（不列预设）。
    #[test]
    fn level2_numeric_single_custom() {
        let p = provider(None);
        let hits = query_mode(&p, "90", "system");
        assert_eq!(hits.len(), 1);
        assert_eq!(
            hits[0].action,
            Action::SetAwake {
                minutes: Some(90),
                keep_screen: false
            }
        );
        assert!(hits[0].title.contains("90 分钟"), "{}", hits[0].title);
    }

    /// 二级纯数字越界：错误提示。
    #[test]
    fn level2_numeric_out_of_range_hint() {
        let p = provider(None);
        let hits = query_mode(&p, "99999", "system");
        assert_eq!(hits.len(), 1);
        assert!(hits[0].interact.is_hint());
        assert_eq!(hits[0].title, "时长超出范围");
    }

    /// 二级非数字：错误提示（与一级文案不同）。
    #[test]
    fn level2_invalid_hint() {
        let p = provider(None);
        let hits = query_mode(&p, "abc", "system");
        assert_eq!(hits.len(), 1);
        assert!(hits[0].interact.is_hint());
        assert_eq!(hits[0].title, "无效输入");
        assert_eq!(hits[0].subtitle, "仅支持分钟数（1–1440）");
    }

    /// 占位符随级别 / 子模式变化。
    #[test]
    fn placeholder_distinguishes_levels() {
        let p = provider(None);
        assert!(p.placeholder(None).contains("亮屏方式"));
        assert!(p.placeholder(Some("system")).contains("分钟数"));
        assert!(
            p.placeholder(Some("screen")).contains("连屏幕"),
            "{}",
            p.placeholder(Some("screen"))
        );
    }

    #[test]
    fn remaining_labels_cover_ranges() {
        let mk = |secs| AwakeStatus {
            expires_at: Some(NOW + secs),
            keep_screen: false,
        };
        assert_eq!(mk(30).remaining_label(NOW), "还剩 30 秒");
        assert_eq!(mk(25 * 60).remaining_label(NOW), "还剩 25 分钟");
        assert_eq!(mk(3600).remaining_label(NOW), "还剩 1 小时");
        assert_eq!(mk(5400).remaining_label(NOW), "还剩 1 小时 30 分钟");
        assert_eq!(
            AwakeStatus {
                expires_at: None,
                keep_screen: false
            }
            .remaining_label(NOW),
            "永久"
        );
    }

    /// 已激活且永久：取消项标题带"永久"。
    #[test]
    fn permanent_status_label() {
        let status = AwakeStatus {
            expires_at: None,
            keep_screen: false,
        };
        let p = provider(Some(status));
        let hits = query(&p, "");
        assert!(hits[0].title.contains("永久"), "{}", hits[0].title);
        assert!(
            hits[0].subtitle.contains("选中取消"),
            "{}",
            hits[0].subtitle
        );
    }

    /// 已到期的状态按"还没结束"处理（显示即将结束），而不是负数。
    #[test]
    fn expired_status_clamps_to_zero() {
        let status = AwakeStatus {
            expires_at: Some(NOW - 10),
            keep_screen: false,
        };
        let hits = query(&provider(Some(status)), "");
        assert!(hits[0].title.contains("即将结束"), "{}", hits[0].title);
    }

    /// 每个状态源都在每次查询时被现读（不能缓存）。
    #[test]
    fn status_is_read_on_every_query() {
        use std::sync::atomic::{AtomicUsize, Ordering};
        let calls = Arc::new(AtomicUsize::new(0));
        let calls_in = calls.clone();
        let p = AwakeProvider::with_clock(
            Arc::new(move || {
                calls_in.fetch_add(1, Ordering::SeqCst);
                None
            }),
            Box::new(FixedClock(NOW)),
        );
        let _ = query(&p, "");
        let _ = query(&p, "");
        assert_eq!(calls.load(Ordering::SeqCst), 2);
    }

    /// 每个时长候选的标题就是用户看到的（不复制别的值）。
    #[test]
    fn set_awake_matches_carry_no_copy_action() {
        for hit in query(&provider(None), "") {
            let _ = kinds(std::slice::from_ref(&hit));
            assert!(
                !matches!(hit.action, Action::Copy { .. }),
                "awake 不该产生 Copy 动作"
            );
        }
    }
}
