//! apps provider：开始菜单应用搜索（§3 的 `[provider.apps]`）。
//!
//! 数据流（对齐 §2）：
//!
//! ```text
//! voke-sys 枚举 .lnk → resolve_all() → Vec<AppEntry>
//!        │  （进程启动时做一次，后台线程）
//!        ▼
//! AppsProvider::new(entries)  ── 索引期：每个条目 SearchKeys::from_name(name)
//!        │
//!        ▼
//! query(ctx) ── nucleo 打分 → Vec<Match>（Action::Launch）
//! ```
//!
//! 职责边界：
//!
//! - 本模块**零 numpy、零 gpui、零 windows**：`AppEntry` 是本 crate 自己的类型，
//!   `voke-sys` 负责把系统数据填进同样的字段（两边字段名逐字对齐，app 层直接
//!   构造即可）。
//! - 枚举 `.lnk` 那一步（IO + COM）**不在这里**：core 不做副作用，只吃数据。
//!   重扫/刷新由 app 层决定何时再调 `voke-sys` 并重建 provider。
//! - 频率排序也不在这里：`QueryEngine::rank` 已经用 `FrequencyStore` 做频率加成，
//!   本 provider 只给"匹配分"。

use std::collections::HashSet;

use nucleo_matcher::{Config, Matcher};

use crate::action::Action;
use crate::matcher::score_entry;
use crate::provider::{BoxFuture, Match, Provider, ProviderConfig, QueryCtx, ready};
use crate::search_key::SearchKeys;

/// 从系统层拿到的应用条目。
///
/// 字段与 `voke_sys::AppEntry` 逐字对齐（同样 4 个字段、同样类型、同样顺序），
/// 但**故意不共享类型**：core 不能依赖 sys（sys 只在 windows target 下存在，
/// 且带 unsafe/COM）。app 层的转换是一行结构体字面量。
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct AppEntry {
    /// 显示名（`IShellLinkW::GetDescription` 或文件名去 `.lnk`）。
    pub name: String,
    /// 可执行文件/文档/目录路径。
    pub path: String,
    /// 图标路径，可为空（UI 应回退到 `path` 的图标）。
    ///
    /// sys 层解析期这里是"图标来源规格"；app 层把它落成 PNG 后**写回真实文件路径**
    /// （见 `voke-app/src/main.rs` 的 `prepare_icons`），此后它才对 UI 有意义。
    pub icon: String,
    /// 命令行参数。
    pub args: Vec<String>,
    /// MSIX/Store 应用的 AUMID（非 MSIX 条目为空串）。
    ///
    /// 与 `voke_sys::AppEntry::app_id` 逐字对齐；`path` 会带 `msix://` 前缀，
    /// 这个字段留的是裸值，便于日志与去重规则（"是否真 MSIX 包"看它有没有 `!`）。
    pub app_id: String,
}

/// 带搜索键的条目：索引期一次生成，查询期只读。
#[derive(Clone, Debug)]
struct IndexedApp {
    entry: AppEntry,
    keys: SearchKeys,
}

/// 应用 provider。
///
/// 内部只持不可变数据（索引 + 配置），因此天然 `Send + Sync`，
/// 不需要锁，也不需要 `Arc`（app 层要跨线程共享就自己包 `Arc`）。
pub struct AppsProvider {
    config: ProviderConfig,
    apps: Vec<IndexedApp>,
}

impl AppsProvider {
    /// provider 的稳定 id（`FrequencyStore` 的键前缀、配置节的键）。
    pub const ID: &'static str = "apps";

    /// 单次查询返回的最大条数。
    ///
    /// 非空查询限流：结果列表只渲染前面几十条，没必要把 500 个弱匹配全传给 UI
    /// （排序、序列化、渲染都是白给的）。空查询**不**限流，见 [`AppsProvider::query`]。
    pub const MAX_RESULTS: usize = 50;

    /// 用一批条目建索引。
    ///
    /// 会顺手去掉 (显示名, 路径) 完全相同的重复项——虽然 `voke-sys` 已按开始菜单
    /// 相对路径去重，但"全用户 + 当前用户装了两份同名应用、路径还不同"的情形仍会
    /// 产生同名条目。这里只去**完全相同**的，同名不同路径（例如两个版本的
    /// "Visual Studio Code"）保留，它们确实是不同目标。
    pub fn new(entries: Vec<AppEntry>) -> Self {
        let mut seen: HashSet<(String, String)> = HashSet::new();
        let apps = entries
            .into_iter()
            .filter(|e| !e.name.is_empty() && !e.path.is_empty())
            .filter(|e| seen.insert((e.name.to_lowercase(), e.path.to_lowercase())))
            .map(|entry| IndexedApp {
                keys: SearchKeys::from_name(&entry.name),
                entry,
            })
            .collect();
        Self {
            config: ProviderConfig {
                enabled: true,
                is_global: true,
                // §3 配置文件里 apps 的权重是 1.2：应用比其它全局 provider 略微优先。
                weight: 1.2,
                // 无触发字符：应用要靠"直接打字"命中，不能要求前缀（§3）。
                prefix: None,
                // 无前缀 → Direct（前缀路由语义用不到它）。
                activation: crate::config::Activation::Direct,
                count: 5,
            },
            apps,
        }
    }

    /// 已索引的条目数（供 UI/日志展示）。
    pub fn len(&self) -> usize {
        self.apps.len()
    }

    pub fn is_empty(&self) -> bool {
        self.apps.is_empty()
    }

    /// 把一条索引条目变成 [`Match`]。
    ///
    /// 动作在 query 阶段就定死为 `Launch { path, args, elevated: false }`
    /// （§3 的"provider 在查询期确定动作"）。`elevated` 恒为 false ——
    /// Ctrl+Shift+Enter 的管理员启动由 app 层在执行时改写这个字段，
    /// 不需要 provider 感知修饰键（provider 拿不到键盘状态）。
    fn to_match(&self, app: &IndexedApp, score: f32) -> Match {
        Match {
            provider_id: Self::ID.to_string(),
            title: app.entry.name.clone(),
            // 副标题放目标路径：同名应用（不同版本/不同安装位置）靠它区分。
            subtitle: app.entry.path.clone(),
            // 图标路径：优先用 sys 层给的（已落盘的 PNG），没有就不给
            // （UI 回退到占位图标，而不是去猜 exe 路径——`shell:::{GUID}`
            // 与 `msix://` 都不是文件路径，猜不出来）。
            icon: (!app.entry.icon.is_empty()).then(|| app.entry.icon.clone()),
            action: Action::Launch {
                path: app.entry.path.clone(),
                args: app.entry.args.clone(),
                elevated: false,
                // apps 插件启动的是应用自身（含命令提示符/PowerShell/终端），
                // 窗口该有就有，**不能**隐藏。
                no_console: false,
            },
            score,
            // 应用行：Enter / 点击都是启动。
            interact: crate::provider::Interact::Execute,
        }
    }
}

impl Default for AppsProvider {
    fn default() -> Self {
        Self::new(Vec::new())
    }
}

impl Provider for AppsProvider {
    fn id(&self) -> &'static str {
        Self::ID
    }

    fn name(&self) -> &'static str {
        "应用"
    }

    /// 别名：`app` = 英文缩写；`yy` = 拼音首字母（应用）。
    /// `ap`/`y` 由子串语义自动覆盖，不单列。
    fn aliases(&self) -> &'static [&'static str] {
        &["app", "yy"]
    }

    fn description(&self) -> &'static str {
        "搜索并启动开始菜单应用"
    }

    fn config(&self) -> &ProviderConfig {
        &self.config
    }

    fn set_config(&mut self, config: ProviderConfig) {
        self.config = config;
    }

    /// 查询实现。
    ///
    /// 空 query 的语义**跟随 engine 的既有设计**（`QueryEngine::route` 的文档：
    /// "空输入走全局（此时展示常用项）"）：空查询时把所有条目以 `score = 1.0`
    /// 原样返回，由 `QueryEngine::rank` 的频率加成把常用项排到前面，
    /// 截断交给 app 层的 Top-N。这里**不自己**按频率排序——频率数据在 engine 里，
    /// provider 不该有第二份。
    ///
    /// 非空 query 走 nucleo：ASCII query 打全拼/首字母/原名，汉字 query 只打原名
    /// （细节都在 [`crate::matcher`]）。打分为纯 CPU、条目量级几百，
    /// 实测在亚毫秒级，所以同步算完后用 [`ready`] 包成 future
    /// ——`Matcher` 不实现 `Clone`，挪进 future 需要 `Arc<Mutex<_>>`，
    /// 而这点耗时配不上那把锁。
    fn query(&self, ctx: &QueryCtx) -> BoxFuture<'static, Vec<Match>> {
        let query = ctx.query.trim();

        if query.is_empty() {
            let matches = self
                .apps
                .iter()
                .map(|app| self.to_match(app, 1.0))
                .collect();
            return ready(matches);
        }

        // 整个查询共用一个 Matcher：它的内部缓冲（打分矩阵）跨条目复用，
        // 避免每个条目重新分配。
        let mut matcher = Matcher::new(Config::DEFAULT);
        let mut matches: Vec<Match> = self
            .apps
            .iter()
            .filter_map(|app| {
                // score_entry 已把 nucleo 的原始分归一化到 0.0–1.0。
                score_entry(&mut matcher, query, &app.keys).map(|s| self.to_match(app, s.score))
            })
            .collect();

        // 先按分排序再截断：nucleo 的分是"绝对分"，排序不依赖遍历顺序，
        // 但截断必须在排序之后。
        matches.sort_by(|a, b| {
            b.score
                .partial_cmp(&a.score)
                .unwrap_or(std::cmp::Ordering::Equal)
        });
        matches.truncate(Self::MAX_RESULTS);
        ready(matches)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::future::Future;

    fn entry(name: &str, path: &str) -> AppEntry {
        AppEntry {
            name: name.to_string(),
            path: path.to_string(),
            icon: String::new(),
            args: Vec::new(),
            app_id: String::new(),
        }
    }

    /// 测试用的样例索引：中文应用（拼音）+ 英文应用 + 带参数的应用。
    fn sample() -> AppsProvider {
        AppsProvider::new(vec![
            entry("微信", r"C:\Program Files\Tencent\WeChat\WeChat.exe"),
            entry(
                "网易云音乐",
                r"C:\Program Files\Netease\CloudMusic\cloudmusic.exe",
            ),
            entry(
                "Visual Studio Code",
                r"C:\Users\x\AppData\Local\Programs\VSCode\Code.exe",
            ),
            AppEntry {
                name: "Windows Terminal".into(),
                path: r"C:\Program Files\WindowsApps\wt.exe".into(),
                icon: String::new(),
                args: vec!["-p".into(), "Ubuntu".into()],
                app_id: String::new(),
            },
        ])
    }

    /// 样 future 都是 ready 的，poll 一次即可（与 engine.rs 的测试同一手法）。
    fn block_on<F: Future>(f: F) -> F::Output {
        let mut f = Box::pin(f);
        let mut cx = std::task::Context::from_waker(std::task::Waker::noop());
        match f.as_mut().poll(&mut cx) {
            std::task::Poll::Ready(v) => v,
            std::task::Poll::Pending => panic!("apps provider 的 future 应当立即就绪"),
        }
    }

    fn query(provider: &AppsProvider, q: &str) -> Vec<Match> {
        let ctx = QueryCtx::new(q.to_string(), q.to_string());
        block_on(provider.query(&ctx))
    }

    fn titles(matches: &[Match]) -> Vec<String> {
        matches.iter().map(|m| m.title.clone()).collect()
    }

    #[test]
    fn id_and_config_match_architecture_doc() {
        let p = sample();
        assert_eq!(p.id(), "apps");
        assert_eq!(p.config().weight, 1.2, "§3 里 apps 的权重是 1.2");
        assert!(p.config().enabled && p.config().is_global);
        assert_eq!(p.config().prefix, None, "应用不能被前缀门控");
    }

    #[test]
    fn matches_hanzi_name_directly() {
        let p = sample();
        let hits = query(&p, "微信");
        assert_eq!(titles(&hits), vec!["微信"]);
    }

    #[test]
    fn matches_by_pinyin_initials() {
        let p = sample();
        let hits = query(&p, "wx");
        assert!(
            titles(&hits).contains(&"微信".to_string()),
            "wx 应命中微信: {:?}",
            titles(&hits)
        );
    }

    #[test]
    fn matches_by_full_pinyin() {
        let p = sample();
        let hits = query(&p, "weixin");
        assert!(
            titles(&hits).contains(&"微信".to_string()),
            "{:?}",
            titles(&hits)
        );
        // 网易云音乐 → wangyiyunyinyue / wyyyy
        let hits = query(&p, "wangyiyunyinyue");
        assert!(
            titles(&hits).contains(&"网易云音乐".to_string()),
            "{:?}",
            titles(&hits)
        );
        let hits = query(&p, "wyyyy");
        assert!(
            titles(&hits).contains(&"网易云音乐".to_string()),
            "{:?}",
            titles(&hits)
        );
    }

    #[test]
    fn matches_english_name_without_spaces() {
        let p = sample();
        let hits = query(&p, "vscode");
        assert!(
            titles(&hits).contains(&"Visual Studio Code".to_string()),
            "{:?}",
            titles(&hits)
        );
    }

    #[test]
    fn unrelated_query_returns_nothing() {
        let p = sample();
        assert!(query(&p, "zzzqqqxxx").is_empty());
    }

    #[test]
    fn empty_query_returns_everything_with_neutral_score() {
        let p = sample();
        let hits = query(&p, "");
        assert_eq!(hits.len(), 4, "空查询交给引擎按频率重排，provider 不截断");
        assert!(hits.iter().all(|m| m.score == 1.0));
        // 空白 query 视同空
        let hits = query(&p, "   ");
        assert_eq!(hits.len(), 4);
    }

    #[test]
    fn empty_query_from_prefix_stripping_is_also_neutral() {
        // ctx.query 为空但 raw 非空（前缀被剥掉后的情形）
        let p = sample();
        let ctx = QueryCtx::new("=x", String::new());
        assert_eq!(block_on(p.query(&ctx)).len(), 4);
    }

    #[test]
    fn action_is_launch_with_path_and_args() {
        let p = sample();
        let hits = query(&p, "windows terminal");
        let m = hits
            .iter()
            .find(|m| m.title == "Windows Terminal")
            .expect("wt 应命中");
        assert_eq!(m.provider_id, "apps");
        assert_eq!(m.subtitle, r"C:\Program Files\WindowsApps\wt.exe");
        assert_eq!(
            m.action,
            Action::Launch {
                path: r"C:\Program Files\WindowsApps\wt.exe".to_string(),
                args: vec!["-p".to_string(), "Ubuntu".to_string()],
                elevated: false,
                no_console: false,
            }
        );
    }

    #[test]
    fn args_are_empty_for_plain_apps() {
        let p = sample();
        let hits = query(&p, "微信");
        assert_eq!(
            hits[0].action,
            Action::Launch {
                path: r"C:\Program Files\Tencent\WeChat\WeChat.exe".to_string(),
                args: vec![],
                elevated: false,
                no_console: false,
            }
        );
    }

    #[test]
    fn results_are_sorted_descending_and_capped() {
        // 造 80 个都命中 "app" 的条目，验证限流 + 降序。
        let entries: Vec<AppEntry> = (0..80)
            .map(|i| entry(&format!("app{i:02}"), &format!("C:\\a\\app{i:02}.exe")))
            .collect();
        let p = AppsProvider::new(entries);
        let hits = query(&p, "app");
        assert_eq!(hits.len(), AppsProvider::MAX_RESULTS);
        let scores: Vec<f32> = hits.iter().map(|m| m.score).collect();
        for pair in scores.windows(2) {
            assert!(pair[0] >= pair[1], "必须降序: {scores:?}");
        }
    }

    #[test]
    fn empty_query_is_not_capped() {
        let entries: Vec<AppEntry> = (0..80)
            .map(|i| entry(&format!("app{i:02}"), &format!("C:\\a\\app{i:02}.exe")))
            .collect();
        let p = AppsProvider::new(entries);
        assert_eq!(query(&p, "").len(), 80);
    }

    #[test]
    fn duplicate_name_and_path_is_deduped() {
        let p = AppsProvider::new(vec![
            entry("微信", r"C:\WeChat.exe"),
            entry("微信", r"C:\WeChat.exe"),       // 完全重复
            entry("微信", r"C:\Other\WeChat.exe"), // 同名不同路径：保留
        ]);
        assert_eq!(p.len(), 2);
        assert_eq!(query(&p, "微信").len(), 2);
    }

    #[test]
    fn entries_without_name_or_path_are_dropped() {
        let p = AppsProvider::new(vec![
            entry("", r"C:\a.exe"),
            entry("NoPath", ""),
            entry("OK", r"C:\ok.exe"),
        ]);
        assert_eq!(p.len(), 1);
    }

    /// 图标路径要原样带到 `Match.icon`，空值则是 `None`（UI 回退占位图标）。
    #[test]
    fn icon_path_is_carried_into_match() {
        let p = AppsProvider::new(vec![
            AppEntry {
                name: "带图标".into(),
                path: r"C:\a.exe".into(),
                icon: r"C:\cache\icons\a.png".into(),
                args: vec![],
                app_id: String::new(),
            },
            entry("无图标", r"C:\b.exe"),
        ]);
        let hits = query(&p, "带图标");
        let with = hits
            .iter()
            .find(|m| m.title == "带图标")
            .expect("带图标 应命中");
        assert_eq!(with.icon.as_deref(), Some(r"C:\cache\icons\a.png"));
        let hits = query(&p, "无图标");
        assert_eq!(hits[0].title, "无图标");
        assert_eq!(hits[0].icon, None);
    }

    /// MSIX 条目：`path` 带 `msix://` 前缀，`app_id` 是裸 AUMID，两者都要保留。
    #[test]
    fn msix_entry_keeps_prefix_and_aumid() {
        let p = AppsProvider::new(vec![AppEntry {
            name: "终端".into(),
            path: "msix://Microsoft.WindowsTerminal_8wekyb3d8bbwe!App".into(),
            icon: r"C:\cache\icons\wt.png".into(),
            args: vec![],
            app_id: "Microsoft.WindowsTerminal_8wekyb3d8bbwe!App".into(),
        }]);
        let hits = query(&p, "终端");
        assert_eq!(hits.len(), 1);
        assert_eq!(
            hits[0].action,
            Action::Launch {
                path: "msix://Microsoft.WindowsTerminal_8wekyb3d8bbwe!App".to_string(),
                args: vec![],
                elevated: false,
                no_console: false,
            },
            "dispatcher 要靠 msix:// 识别并走 shell:AppsFolder 启动"
        );
    }

    #[test]
    fn empty_provider_is_usable() {
        let p = AppsProvider::default();
        assert!(p.is_empty());
        assert!(query(&p, "wx").is_empty());
        assert!(query(&p, "").is_empty());
    }
}
