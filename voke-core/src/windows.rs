//! windows provider：已打开窗口的搜索与切换（§5 的 Window Walker，
//! 对齐 PowerToys Run 的 Window Walker 插件）。
//!
//! ## 数据流
//!
//! ```text
//! app 层注入闭包：Fn() -> Vec<WindowEntry>   ← 每次调用都真去 EnumWindows
//!         │  （provider 不缓存：窗口状态每秒都在变）
//!         ▼
//! WindowsProvider::query(ctx)
//!         │  title / process_name 各建一份 SearchKeys
//!         ▼
//! nucleo 打分（复用 crate::matcher）→ Vec<Match>（Action::ActivateWindow { hwnd }）
//! ```
//!
//! ## 为什么数据源是"注入的闭包"而不是直接调 voke-sys
//!
//! core 必须零 windows 依赖（§1 的"core 秒级测试循环"），而且窗口枚举是
//! **OS 调用 + 副作用**，不该出现在纯逻辑层。注入闭包让：
//! - core 能在 Linux/WSL 上测试（假闭包返回造好的数据）
//! - app 层决定"什么时候枚举、在哪个线程枚举"（必须后台线程，见下）
//!
//! ## 为什么每次 query 都重新枚举（不缓存）
//!
//! 窗口的打开/关闭/改名/切换前台频率极高，缓存一算命中的就是过期数据——
//! 用户切到某个窗口后它立刻"消失"在列表里，是比慢更糟的体验。
//! 枚举本身在后台线程上做（app 层的 `background_spawn`），不阻塞 UI。
//!
//! **条目不缓存，但搜索键缓存**：`SearchKeys` 只取决于字符串本身，同一个标题
//! 每次按键都重算一遍拼音展开是纯浪费（200 条目实测 1374 µs → 16 µs）。
//! 见 [`SearchKeysCache`]——它有容量上限，标题换了也不会无界增长。
//!
//! ## 与 apps provider 的排序关系
//!
//! 权重 1.0 < apps 的 1.2（§3/§5）：同名时优先启动应用而不是切到某个窗口。

use std::sync::Arc;

use nucleo_matcher::{Config, Matcher};

use crate::action::Action;
use crate::matcher::score_entry;
use crate::provider::{BoxFuture, Match, Provider, ProviderConfig, QueryCtx, ready};
use crate::search_key::{SearchKeys, SearchKeysCache};

/// 从系统层拿到的窗口条目。
///
/// 字段与 `voke_sys::WindowEntry` 逐字对齐（同样字段、同样类型、同样顺序），
/// 但故意不共享类型：core 不能依赖 sys（sys 只在 windows target 存在）。
/// app 层的转换是一行结构体字面量。
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct WindowEntry {
    /// 窗口句柄的数值。
    pub hwnd: isize,
    /// 窗口标题。
    pub title: String,
    /// 进程可执行文件名（副标题 + 搜索键的一部分）。
    pub process_name: String,
    /// 进程可执行文件完整路径（图标来源）。
    pub process_path: String,
    /// 图标 PNG 的落盘路径，可为空。
    ///
    /// 枚举期为空；app 层用 `process_path` 提取图标后**写回真实文件路径**
    /// （与 `AppEntry::icon` 同款约定，见 `voke-app/src/icons.rs`）。
    pub icon: String,
}

/// 窗口数据源：每次调用都应当返回**当前**的窗口列表。
///
/// 用 `Arc<dyn Fn>` 而不是泛型参数：`WindowsProvider` 要能塞进
/// `Vec<Box<dyn Provider>>`，泛型会让类型参数泄漏到整个注册表。
/// 闭包必须 `Send + Sync`（provider 契约要求跨线程）。
pub type WindowFetcher = Arc<dyn Fn() -> Vec<WindowEntry> + Send + Sync>;

/// 带搜索键的窗口（条目每次查询重建，搜索键走 [`SearchKeysCache`]——见模块文档）。
struct IndexedWindow {
    entry: WindowEntry,
    title_keys: Arc<SearchKeys>,
    process_keys: Arc<SearchKeys>,
}

/// 窗口 provider。
pub struct WindowsProvider {
    config: ProviderConfig,
    /// 数据源。provider 自己不假设它在哪个线程跑（调用方保证后台线程）。
    fetch: WindowFetcher,
    /// 搜索键记忆化（容量有上限，见类型文档）。
    keys: SearchKeysCache,
}

impl WindowsProvider {
    /// provider 的稳定 id（`FrequencyStore` 的键前缀、配置节的键）。
    pub const ID: &'static str = "windows";

    /// 单次非空查询返回的最大条数（理由同 apps：结果列表只渲染前几十条）。
    /// 空查询不限流，见 [`WindowsProvider::query`]。
    pub const MAX_RESULTS: usize = 50;

    /// 用数据源构造。
    pub fn new(fetch: WindowFetcher) -> Self {
        Self {
            config: ProviderConfig {
                enabled: true,
                is_global: true,
                // §3/§5：比 apps 的 1.2 低——应用优先于窗口。
                weight: 1.0,
                // 无触发字符：窗口也是靠"直接打字"命中（对齐 PTR）。
                prefix: None,
                activation: crate::config::Activation::Direct,
                count: 5,
            },
            fetch,
            keys: SearchKeysCache::new(),
        }
    }

    /// 把一条索引条目变成 [`Match`]。
    ///
    /// 动作在 query 阶段确定（§3）：`ActivateWindow { hwnd }`。
    /// 窗口已消失的回退（重新启动对应进程）由 dispatcher 统一处理，
    /// provider 不感知——它拿到的就是"查询那一刻还活着"的窗口。
    fn to_match(&self, window: &IndexedWindow, score: f32) -> Match {
        Match {
            provider_id: Self::ID.to_string(),
            // 标题是主标题；进程名是副标题（区分同名窗口，如多个 "main.rs"）。
            title: window.entry.title.clone(),
            subtitle: window.entry.process_name.clone(),
            icon: (!window.entry.icon.is_empty()).then(|| window.entry.icon.clone()),
            action: Action::ActivateWindow {
                hwnd: window.entry.hwnd,
            },
            score,
            // 窗口行：Enter / 点击都是切过去。
            interact: crate::provider::Interact::Execute,
        }
    }

    /// 为一个窗口建索引（title 与 process_name 各一份搜索键，走缓存）。
    fn index(&self, entry: WindowEntry) -> IndexedWindow {
        let title_keys = self.keys.get(&entry.title);
        let process_keys = self.keys.get(&entry.process_name);
        IndexedWindow {
            title_keys,
            process_keys,
            entry,
        }
    }

    /// 对一条窗口打分：title 与 process_name 取最高分。
    ///
    /// 为什么分开建键而不是把两串拼起来：`SearchKeys` 的拼音展开是逐字的，
    /// 拼接会让"标题的拼音"与"进程名的拼音"互相污染（例如标题是中文、进程名
    /// 是英文时首字母串会跨界）。分开算再取 max，语义干净。
    fn score(&self, matcher: &mut Matcher, query: &str, window: &IndexedWindow) -> Option<f32> {
        let title = score_entry(matcher, query, &window.title_keys).map(|s| s.score);
        let process = score_entry(matcher, query, &window.process_keys).map(|s| s.score);
        match (title, process) {
            (Some(a), Some(b)) => Some(a.max(b)),
            (Some(a), None) => Some(a),
            (None, Some(b)) => Some(b),
            (None, None) => None,
        }
    }
}

impl Provider for WindowsProvider {
    fn id(&self) -> &'static str {
        Self::ID
    }

    fn name(&self) -> &'static str {
        "窗口"
    }

    /// 别名：`win` = 英文缩写；`ck` = 拼音首字母（窗口）。
    fn aliases(&self) -> &'static [&'static str] {
        &["win", "ck"]
    }

    fn description(&self) -> &'static str {
        "切换到已打开的窗口"
    }

    fn config(&self) -> &ProviderConfig {
        &self.config
    }

    fn set_config(&mut self, config: ProviderConfig) {
        self.config = config;
    }

    /// 查询实现。
    ///
    /// **每次调用都重新枚举**（`(self.fetch)()`）：窗口状态实时变化，缓存即出错，
    /// 见模块文档。枚举是 OS 调用、几十到几百个窗口，调用方必须保证在后台线程执行
    /// （app 层的 `background_spawn`）。
    ///
    /// 空 query 的语义跟随 engine 的既有设计（与 apps provider 一致）：
    /// 返回全部窗口、`score = 1.0`，由 `QueryEngine::rank` 的频率加成排序，
    /// provider 自己不按频率排、也不截断。
    fn query(&self, ctx: &QueryCtx) -> BoxFuture<'static, Vec<Match>> {
        let windows: Vec<IndexedWindow> = (self.fetch)()
            .into_iter()
            // 空标题/空句柄的条目没有可操作性，入口拦掉。
            .filter(|e| !e.title.trim().is_empty() && e.hwnd != 0)
            .map(|e| self.index(e))
            .collect();

        let query = ctx.query.trim();
        if query.is_empty() {
            let matches = windows.iter().map(|w| self.to_match(w, 1.0)).collect();
            return ready(matches);
        }

        let mut matcher = Matcher::new(Config::DEFAULT);
        let mut matches: Vec<Match> = windows
            .iter()
            .filter_map(|w| {
                self.score(&mut matcher, query, w)
                    .map(|s| self.to_match(w, s))
            })
            .collect();
        // 先排序再截断（与 apps provider 同款）。
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

    fn entry(title: &str, process: &str, hwnd: isize) -> WindowEntry {
        WindowEntry {
            hwnd,
            title: title.to_string(),
            process_name: process.to_string(),
            process_path: format!(r"C:\apps\{process}"),
            icon: String::new(),
        }
    }

    /// 一批按窗口标题/进程名区分的样例。
    fn sample_entries() -> Vec<WindowEntry> {
        vec![
            entry("main.rs - voke - Visual Studio Code", "Code.exe", 101),
            entry("voke — 搜索", "voke.exe", 102),
            entry("微信", "WeChat.exe", 103),
            entry("Windows Terminal", "WindowsTerminal.exe", 104),
        ]
    }

    fn provider_with(entries: Vec<WindowEntry>) -> WindowsProvider {
        WindowsProvider::new(Arc::new(move || entries.clone()))
    }

    fn sample() -> WindowsProvider {
        provider_with(sample_entries())
    }

    fn block_on<F: Future>(f: F) -> F::Output {
        let mut f = Box::pin(f);
        let mut cx = std::task::Context::from_waker(std::task::Waker::noop());
        match f.as_mut().poll(&mut cx) {
            std::task::Poll::Ready(v) => v,
            std::task::Poll::Pending => panic!("windows provider 的 future 应当立即就绪"),
        }
    }

    fn query(provider: &WindowsProvider, q: &str) -> Vec<Match> {
        let ctx = QueryCtx::new(q.to_string(), q.to_string());
        block_on(provider.query(&ctx))
    }

    fn titles(matches: &[Match]) -> Vec<String> {
        matches.iter().map(|m| m.title.clone()).collect()
    }

    #[test]
    fn id_and_config_match_architecture_doc() {
        let p = sample();
        assert_eq!(p.id(), "windows");
        // §3/§5：窗口权重 1.0，低于 apps 的 1.2（应用优先）。
        assert_eq!(p.config().weight, 1.0);
        assert!(p.config().enabled && p.config().is_global);
        assert_eq!(p.config().prefix, None, "窗口不能被前缀门控");
    }

    #[test]
    fn matches_window_title() {
        let p = sample();
        let hits = query(&p, "windows terminal");
        assert_eq!(titles(&hits), vec!["Windows Terminal"]);
    }

    #[test]
    fn matches_process_name() {
        let p = sample();
        // "code" 只出现在进程名里，标题里没有。
        let hits = query(&p, "code.exe");
        assert!(
            titles(&hits).contains(&"main.rs - voke - Visual Studio Code".to_string()),
            "{:?}",
            titles(&hits)
        );
        // 不带扩展名也要命中（nucleo 子串匹配）。
        let hits = query(&p, "wechat");
        assert!(
            titles(&hits).contains(&"微信".to_string()),
            "进程名 wechat.exe 应命中微信窗口: {:?}",
            titles(&hits)
        );
    }

    #[test]
    fn action_is_activate_window_with_hwnd() {
        let p = sample();
        let hits = query(&p, "windows terminal");
        assert_eq!(hits[0].provider_id, "windows");
        assert_eq!(hits[0].subtitle, "WindowsTerminal.exe");
        assert_eq!(hits[0].action, Action::ActivateWindow { hwnd: 104 });
    }

    #[test]
    fn icon_path_is_carried_into_match() {
        let mut entries = sample_entries();
        entries[3].icon = r"C:\cache\icons\WindowsTerminal-deadbeef.png".into();
        let p = provider_with(entries);
        let hits = query(&p, "windows terminal");
        assert_eq!(
            hits[0].icon.as_deref(),
            Some(r"C:\cache\icons\WindowsTerminal-deadbeef.png")
        );
        // 没有图标的条目 => None（UI 回退占位图标）。
        let hits = query(&p, "main.rs");
        assert_eq!(hits[0].icon, None);
    }

    #[test]
    fn empty_query_returns_everything_with_neutral_score() {
        let p = sample();
        let hits = query(&p, "");
        assert_eq!(hits.len(), 4, "空查询交给引擎按频率重排，provider 不截断");
        assert!(hits.iter().all(|m| m.score == 1.0));
        assert!(
            hits.iter()
                .all(|m| matches!(m.action, Action::ActivateWindow { hwnd } if hwnd > 0))
        );
    }

    #[test]
    fn unrelated_query_returns_nothing() {
        let p = sample();
        assert!(query(&p, "zzzqqqxxx").is_empty());
    }

    #[test]
    fn invalid_entries_are_dropped() {
        let p = provider_with(vec![
            entry("", "Code.exe", 1),      // 空标题
            entry("   ", "Code.exe", 2),   // 空白标题
            entry("Valid", "Code.exe", 0), // 空句柄
            entry("OK Window", "Code.exe", 3),
        ]);
        assert_eq!(query(&p, "").len(), 1);
        assert_eq!(
            query(&p, "ok")[0].action,
            Action::ActivateWindow { hwnd: 3 }
        );
    }

    /// 每次 query 都必须重新调闭包（窗口状态实时变，不能缓存）。
    #[test]
    fn fetcher_is_called_on_every_query() {
        use std::sync::atomic::{AtomicUsize, Ordering};
        let calls = Arc::new(AtomicUsize::new(0));
        let calls_in = calls.clone();
        let p = WindowsProvider::new(Arc::new(move || {
            let n = calls_in.fetch_add(1, Ordering::SeqCst);
            // 每次返回不同句柄，验证结果确实来自最新一次枚举。
            vec![entry("Dynamic Window", "dyn.exe", 100 + n as isize + 1)]
        }));
        let first = query(&p, "dynamic");
        assert_eq!(first[0].action, Action::ActivateWindow { hwnd: 101 });
        let second = query(&p, "dynamic");
        assert_eq!(second[0].action, Action::ActivateWindow { hwnd: 102 });
        assert_eq!(calls.load(Ordering::SeqCst), 2);
    }

    /// 连续两次查询同一批窗口：搜索键命中缓存，缓存条目数不增长。
    #[test]
    fn search_keys_are_memoized_across_queries() {
        let p = sample();
        query(&p, "windows terminal");
        let after_first = p.keys.len();
        assert!(after_first > 0, "第一次查询应当把搜索键写进缓存");
        query(&p, "windows terminal");
        assert_eq!(
            p.keys.len(),
            after_first,
            "同一批窗口重复查询不该新增缓存条目"
        );
    }

    /// 标题每次都变（浏览器标签页）时缓存必须仍然有界。
    #[test]
    fn search_key_cache_stays_bounded_when_titles_change() {
        use std::sync::atomic::{AtomicUsize, Ordering};
        let n = Arc::new(AtomicUsize::new(0));
        let n_in = n.clone();
        let p = WindowsProvider::new(Arc::new(move || {
            let i = n_in.fetch_add(1, Ordering::SeqCst);
            vec![entry(&format!("tab-{i} - 浏览器"), "browser.exe", 1)]
        }));
        for _ in 0..2000 {
            let hits = query(&p, "tab");
            assert_eq!(hits.len(), 1, "每次都要用最新的标题");
        }
        assert!(
            p.keys.len() <= SearchKeysCache::CAPACITY,
            "缓存必须 <= 上限，实际 {}",
            p.keys.len()
        );
    }

    #[test]
    fn results_are_sorted_descending_and_capped() {
        let entries: Vec<WindowEntry> = (0..80)
            .map(|i| entry(&format!("doc{i:02}.txt - 编辑器"), "editor.exe", 1000 + i))
            .collect();
        let p = provider_with(entries);
        let hits = query(&p, "doc");
        assert_eq!(hits.len(), WindowsProvider::MAX_RESULTS);
        let scores: Vec<f32> = hits.iter().map(|m| m.score).collect();
        for pair in scores.windows(2) {
            assert!(pair[0] >= pair[1], "必须降序: {scores:?}");
        }
    }

    #[test]
    fn empty_query_is_not_capped() {
        let entries: Vec<WindowEntry> = (0..80)
            .map(|i| entry(&format!("win{i:02}"), "app.exe", 2000 + i))
            .collect();
        let p = provider_with(entries);
        assert_eq!(query(&p, "").len(), 80);
    }

    /// 中文窗口标题也能用拼音命中（复用 SearchKeys 的多音字展开）。
    #[test]
    fn matches_chinese_title_by_pinyin() {
        let p = sample();
        for q in ["微信", "weixin", "wx"] {
            let hits = query(&p, q);
            assert!(
                titles(&hits).contains(&"微信".to_string()),
                "{q} 应命中微信窗口: {:?}",
                titles(&hits)
            );
        }
    }

    /// 空数据源可用（还没枚举到窗口 / 非 Windows 平台占位）。
    #[test]
    fn empty_provider_is_usable() {
        let p = provider_with(Vec::new());
        assert!(query(&p, "x").is_empty());
        assert!(query(&p, "").is_empty());
    }
}
