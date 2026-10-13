//! 搜索 UI：输入框 + 分组结果列表 + 键盘导航（方案 A）。
//!
//! 数据流：
//!
//! ```text
//! 输入框 InputEvent::Change
//!   → spawn_query：在 background executor 上驱动 core 的 BoxFuture
//!   → QueryEngine::prepare_in_mode（激活插件 + 子模式）+ aggregate（分组排序）
//!   → 回主线程写 self.rows / self.hints + cx.notify()
//!   → v_virtual_list 只渲染可见结果行；提示（hints）另渲染在输入框下方条带里
//! Enter / 点击 → 按 Match.interact 分发（见 confirm_index）
//! ```
//!
//! **方案 A 的核心：一切按 `interact` 分发，没有特判。**
//!
//! | `interact` | Enter | 鼠标点击 |
//! |---|---|---|
//! | [`Interact::Execute`] | 执行 action | 执行 action |
//! | [`Interact::EnterSubquery`] | 进入该子模式 | 进入该子模式 |
//! | [`Interact::Hint`] | 不可选（不在列表里） | 不可点（不在列表里） |
//!
//! **执行后的输入框复位**（用户实测 UX，判据是 core 的纯函数
//! `Action::should_reset_input`，见 `confirm_index`）：
//!
//! - **离开面板去做别的事**（启动应用 / 切换窗口 / 打开 URL / 保持唤醒）→
//!   清空输入、退出插件、结果列表复位（`reset_after_action`）；
//! - **产出可复制内容**（Copy：随机值 / UUID / 时间戳 / base64 编解码 /
//!   计算器结果）→ 输入与结果**原样保留**，供用户再看一眼、再次复制；
//! - `Refresh`（random 的「重新生成」）→ 不改输入，重跑一次当前查询。
//!
//! **面板重新显示 ≠ 用户修改了输入**（用户实测 UX）：热键/托盘重新唤起时
//! 窗口激活观察者聚焦输入框，并**仅当当前插件不会因查询重掷值时才刷新**
//! （判据是 core 纯函数 `QueryEngine::plugin_regenerates_on_query`）——`#w`
//! 等窗口列表没有后台热更新、只是每次查询实时枚举，靠这次唤回刷新保证隐藏
//! 期间新开/退出的窗口在用户敲第一个键前出现；random / uuid / timestamp
//! 唤回不刷新，上次的值原样留在结果里，直接可复制。应用列表另走「重扫 →
//! 签名变化才热更新」，热更新的重跑也被重掷护栏挡住；awake 倒计时由常驻
//! ticker 自己走。用户改输入仍照常重算（`on_query_changed`）。
//!
//! **多级状态**（`active_plugin` + `sub_mode` + `picker`）：
//!
//! ```text
//! 全局搜索 ──打 `#`──▶ 插件选择列表（列全部插件，继续打字过滤）
//!                          │ Enter / 点击
//!                          ▼
//!                   插件第一级（如 base64 列 加密/解密 两条 EnterSubquery）
//!                          │ Enter / 点击
//!                          ▼
//!                 sub_mode = "enc"（占位符变成"输入要加密的内容"）
//! ```
//!
//! **前缀是令牌，不是输入内容**（本模块的核心契约）：激活插件时输入框被
//! **清空**，前缀（如 `=` / `#b`）以**不可编辑的令牌**渲染在输入框旁边
//! （[`SearchView::render_mode_token`]）。因此"用户把前缀删了"这种事不会发生
//! ——输入框里从头到尾只有**插件查询本身**。这也意味着退出插件**与输入内容
//! 无关**，只走 Esc / 空输入 Backspace（见下表）；绝不做"输入里没有前缀就
//! 判成离开插件"的判断（那正是用户实测 bug 的根源）。
//!
//! **Esc / Backspace 的二级语义**（两条都往上一级退，但到顶行为不同）：
//!
//! | 状态 | Esc | 输入框为空时按 Backspace |
//! |---|---|---|
//! | 有 `sub_mode` | 退到插件第一级 | 退到插件第一级 |
//! | 有 `active_plugin` | 退到全局搜索 | 退到全局搜索 |
//! | `picker` 开着 | 关列表回全局 | 关列表回全局 |
//! | 都没有（最外层） | **隐藏窗口** | 什么都不做（停在空输入） |
//!
//! 输入框**有内容**时 Backspace 是普通的删除字符（交给编辑引擎），
//! 聚焦保持在输入框——这是用户明确要求的语义。
//!
//! **状态所有权**（Coding Guides "State ownership"）：
//! - 查询词、结果、选中项、滚动位置、`active_plugin`、`sub_mode` 归本 view
//! - 输入框的可编辑文本归 `Entity<InputState>`（gpui 的编辑引擎）
//! - provider 数据与频率表归 [`ENGINE`]（进程单例）：查询跑在 background
//!   executor 上拿不到 gpui 上下文
//! - 副作用（隐藏窗口、启动进程、保持唤醒）全部走 [`crate::dispatcher`]
//!
//! 实现方式说明（都对着 gpui-base 0.7.1 / gpui-pre 0.3.8 源码确认过）：
//! - 单行 `InputState` **不注册** `up`/`down` 动作：`InputBaseState::render`
//!   只在 `is_multi_line()` 时才挂 `MoveUp`/`MoveDown`，所以方向键不会被动
//!   编辑引擎消费。`Enter` 会 emit `InputEvent::PressEnter` 并 `cx.propagate()`，
//!   `Escape` 也 `cx.propagate()`（默认 `clean_on_escape` 为 false）。
//! - `Backspace` 绑在输入框自己的 `key_context("Input")` 上（`gpui-base` 的
//!   `init` 里 `KeyBinding::new("backspace", Backspace, Some("Input"))`），
//!   所以**输入框有内容时我们收不到 key_down**（keybinding 已经消费掉了）；
//!   只有输入为空时 `InputBaseState::backspace` 会 `cx.propagate()`
//!   （源码注释明确写着"Nothing to delete at the start of the text. Propagate
//!   so an ancestor can act on it"）才轮到我们。这正是我们要的语义，
//!   不需要额外判断光标位置。
//! - 因此监听器挂在**根 div** 上（bubble 阶段接住这些键）。`on_key_down` 来自
//!   `InteractiveElement`，而该 trait 只被 `Div`/`Img`/`Svg`/`UniformList`
//!   实现——`VirtualList` **没有**实现，所以不能把监听器挂在列表元素上。

use std::rc::Rc;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::{Mutex, OnceLock};

use gpui_kit::assets::IconName;
use gpui_kit::component::{
    ActiveTheme as _, Icon, VirtualListScrollHandle,
    input::{Input, InputEvent, InputState},
    v_virtual_list,
};
use gpui_kit::{
    AnyElement, AppContext as _, ClickEvent, Context, Entity, FocusHandle, Focusable, ImageSource,
    InteractiveElement as _, IntoElement, KeyDownEvent, ParentElement as _, Pixels, Render,
    ScrollStrategy, SharedString, Size, StatefulInteractiveElement as _, Styled as _,
    StyledImage as _, Subscription, Task, Window, div, img, px, size,
};
use voke_core::{
    Action, AppConfig, AppEntry, AppsProvider, CalculatorProvider, Interact, Match, QueryEngine,
    RankedMatch, RouteKind, WindowFetcher, WindowsProvider,
};

use crate::badge;
use crate::dispatcher;

// 配置状态层（阶段 3）在顶层模块 `crate::config`（`main.rs` 里 `mod config`）：
// 进程内配置全局 + 读盘/写盘/下发引擎。本模块只留查询相关的入口。

/// gpui 的 `FluentBuilder`（`when`/`when_some` 等条件修饰）在 gpui-pre 里定义，
/// 但既没被 `gpui-pre` 也没被 `gpui-kit` re-export。它的实现是 blanket
/// `impl<T: IntoElement> FluentBuilder for T`——也就是说**每个 IntoElement 都
/// 有这些方法**，只是 trait 本身不可见。这里本地定义同名 trait，方法名/签名与
/// gpui 源码逐字对齐，编译器会自动为所有元素接上。
trait FluentBuilder: Sized {
    fn when(self, condition: bool, then: impl FnOnce(Self) -> Self) -> Self {
        if condition { then(self) } else { self }
    }

    /// 与 gpui 的 `FluentBuilder::when_some` 逐字对齐（同样的理由：trait 不可见）。
    fn when_some<T>(self, option: Option<T>, then: impl FnOnce(Self, T) -> Self) -> Self {
        match option {
            Some(value) => then(self, value),
            None => self,
        }
    }
}

impl<T: IntoElement> FluentBuilder for T {}

/// 结果行高。虚拟列表要求每项自带尺寸（`item_sizes: Rc<Vec<Size<Pixels>>>`），
/// 这里是定高；将来在行里改成多行时改这一处即可。
const ROW_HEIGHT: Pixels = px(48.);

/// 行内图标的渲染尺寸（也就是插件角标色块的边长）。
///
/// 从 16px 提到 24px：44px 行高下 16px 图标视觉偏小（用户反馈），
/// 24px 在 48px 行高里比例更均衡，也不挡文字（文字侧有 gap_3）。
const ROW_ICON_SIZE: Pixels = px(24.);

/// 插件角标色块的圆角：比卡片圆角（`theme().radius`）小，色块在小尺寸下更清楚。
const BADGE_RADIUS: Pixels = px(3.);

/// 结果区最多显示的行数。
const MAX_VISIBLE_ROWS: f32 = 8.;

/// 插件选择列表模式的输入框占位符。
const PICKER_PLACEHOLDER: &str = "输入过滤插件，Enter 选择";

/// 自检路径（没有 executor）poll 一次查询的**上限次数**。
///
/// 内置 provider 的 future 一 poll 就 `Ready`，这里的上限只是给"契约被改坏"
/// 兜底：到顶后放弃该轮自检并记日志，而不是把进程卡在死循环里。
const SELFTEST_POLL_LIMIT: usize = 200;

/// 输入区 + 底部提示行的高度（窗口高度 = 行高 × 行数 + 这一块）。
const CHROME_HEIGHT: Pixels = px(88.);

/// 输入框下方提示条带（`Interact::Hint`）的高度。
///
/// 条带只在有提示时占位；但窗口尺寸是建窗时定死的，所以这里**恒定预留**一段，
/// 保证有提示时结果区仍能完整放下 [`MAX_VISIBLE_ROWS`] 行（无提示时结果区略高，
/// 多出的空白由虚拟列表自然吸收）。
const HINT_STRIP_HEIGHT: Pixels = px(24.);

/// 进程级查询引擎（provider 注册表 + 频率表）。
///
/// 放在全局而不是 view 里：`spawn_query` 把整次查询挪到 background executor，
/// 那里拿不到 gpui 的上下文，只能通过全局访问。`QueryEngine` 本身
/// `Send + Sync`（`Provider: Send + Sync`），外面套 `Mutex` 只是因为
/// `record_use` 需要 `&mut`。
static ENGINE: OnceLock<Mutex<QueryEngine>> = OnceLock::new();

/// 应用索引是否已注入（决定空状态是"还没索引完"还是"没索引到"）。
///
/// 与 [`APP_COUNT`] 分开：应用列表会热更新（安装/卸载），条目数会变，但"是否
/// 已注入过"只翻一次。以前两者合用 `OnceLock<usize>`，热更新时改不了计数。
static APPS_READY: OnceLock<()> = OnceLock::new();

/// 已索引的应用条目数（空状态文案用）。热更新时会变，所以是 atomic。
static APP_COUNT: AtomicUsize = AtomicUsize::new(0);

/// 最近一次窗口枚举的条数（窗口数会变，所以是 atomic 而不是 `OnceLock`）。
static WINDOW_COUNT: AtomicUsize = AtomicUsize::new(0);

/// main 在后台线程解析完 `.lnk` 后注入 provider 数据。
///
/// 只生效一次（`OnceLock`）：`AppsProvider` 内部已建好拼音索引，重建索引
/// 是启动路径上最贵的一步，不该在每次查询时重复。
///
/// `windows` 是窗口 provider 的数据源（每次查询都调它 → `EnumWindows`）。
/// 传 `None` 表示本平台不支持窗口切换（非 Windows 构建）。
///
/// `local_offset_seconds` 是本地时区偏移（秒），供 timestamp 插件展示本地时间；
/// 非 Windows 平台传 `|| 0`（等价 UTC）。
///
/// **方案 A 注册表**（顺序 = 分组展示顺序，也是分组之间的稳定次序）：
///
/// | id | 前缀 | is_global | 说明 |
/// |---|---|---|---|
/// | apps | — | ✓ | 开始菜单应用 |
/// | windows | — | ✓ | 已打开窗口 |
/// | awake | — | ✓ | 保持唤醒（输入 awake 直接命中） |
/// | terminal | — | ✓ | WT profiles（App Activation + `--profile <guid>` 启动） |
/// | workspaces | — | ✓ | 编辑器最近工作区（Zed / VSCode 历史，用记录它的编辑器打开） |
/// | calculator | `=` | ✗ | 内联计算器 |
/// | timestamp | `#` | ✗ | 时间戳换算 |
/// | base64 | `#b` | ✗ | base64 编解码（子模式 enc/dec） |
/// | uuid | `#u` | ✗ | UUID v4 |
/// | random | `#r` | ✗ | 随机值 |
///
/// 前缀互不抢占（最长匹配优先，见 `voke_core::QueryEngine::prefix_owner`），
/// 所以不再需要 M5 的子命令面板。
pub fn set_providers(
    apps: Vec<AppEntry>,
    windows: Option<WindowFetcher>,
    local_offset_seconds: std::sync::Arc<dyn Fn() -> i32 + Send + Sync>,
) {
    let apps = AppsProvider::new(apps);
    let _ = APPS_READY.set(());
    APP_COUNT.store(apps.len(), Ordering::Relaxed);

    let mut providers: Vec<Box<dyn voke_core::Provider>> = vec![Box::new(apps)];
    if let Some(fetch) = windows {
        providers.push(Box::new(WindowsProvider::new(fetch)));
    }
    // 保持唤醒。状态源读的是 `voke-sys` 的原子状态（每次查询现读，不缓存）。
    providers.push(Box::new(voke_core::AwakeProvider::new(
        std::sync::Arc::new(|| {
            voke_sys::current_awake().map(|state| voke_core::AwakeStatus {
                expires_at: state.expires_at.map(|at| at as i64),
                keep_screen: state.keep_screen,
            })
        }),
    )));
    // WT profiles。数据源每次查询现读 settings.json（stable + Preview 合并，
    // 解析/去重是 voke-core 的纯逻辑）；与窗口枚举同款，跑在 background executor 上。
    // 启动走 App Activation + `--profile "<guid>"`（对齐 PowerToys CmdPal 的
    // Windows Terminal 扩展），不用 CreateProcess 直启 wt.exe 别名。
    // 非 Windows 平台注入空数据源（编译用，无实际功能）。
    #[cfg(windows)]
    let terminal_fetch: voke_core::ProfileFetcher = std::sync::Arc::new(|| {
        voke_core::profiles_from_settings(&voke_sys::read_settings_sources())
    });
    #[cfg(not(windows))]
    let terminal_fetch: voke_core::ProfileFetcher = std::sync::Arc::new(Vec::new);
    #[cfg(windows)]
    let wt_path = voke_sys::wt_exe();
    #[cfg(not(windows))]
    let wt_path = "wt.exe".to_string();
    providers.push(Box::new(voke_core::TerminalProvider::new(
        terminal_fetch,
        wt_path,
    )));
    // 编辑器最近工作区历史（Zed 的 `db.sqlite` + VSCode 的 `state.vscdb`）。
    //
    // 数据源是**只读 SQLite**（`mode=ro`，绝不写编辑器的库）。Zed 的库 29MB 还带
    // 17MB 的 WAL，不能每次按键全量读——provider 内部做 TTL 缓存（默认 5 秒），
    // 重读跑在 background executor 上。
    //
    // 两个编辑器的打开命令由 sys 解析（`voke_sys::zed_command` /
    // `voke_sys::vscode_command`）：没装那个编辑器时是 `None`，provider 把它的
    // 条目退化成 Launch 路径本身，dispatcher 的 launch_path 再把目录转交
    // explorer.exe（远程路径在 Windows 上找不到，只记一条错误日志）。
    #[cfg(windows)]
    let workspace_fetch: voke_core::WorkspaceFetcher = std::sync::Arc::new(|| {
        voke_core::workspaces_from_history(
            &voke_sys::zed_workspaces(),
            &voke_sys::vscode_workspaces(),
        )
    });
    #[cfg(not(windows))]
    let workspace_fetch: voke_core::WorkspaceFetcher = std::sync::Arc::new(Vec::new);
    #[cfg(windows)]
    let (zed_command, vscode_command) = (voke_sys::zed_command(), voke_sys::vscode_command());
    #[cfg(not(windows))]
    let (zed_command, vscode_command) = (None, None);
    providers.push(Box::new(voke_core::WorkspaceProvider::new(
        workspace_fetch,
        zed_command,
        vscode_command,
    )));

    // `=` 前缀的内联计算器。
    providers.push(CalculatorProvider::boxed());
    // `#` 命名空间下的四个插件（最长前缀优先，互不抢占）。
    providers.push(Box::new(
        voke_core::TimestampProvider::with_clock_and_offset(
            Box::new(voke_core::SystemClock),
            Box::new(move || local_offset_seconds()),
        ),
    ));
    providers.push(Box::new(voke_core::Base64Provider::new()));
    providers.push(Box::new(voke_core::UuidProvider::new()));
    providers.push(Box::new(voke_core::RandomProvider::new()));
    let mut engine = QueryEngine::new(providers);
    // 频率数据（"哪些结果被用过多少次"）必须跨会话存在，否则"越用越准"每次
    // 重启都从零开始。磁盘 → 引擎只在这一处注入一次。
    engine.set_frequency(crate::config::load_frequency());
    let _ = ENGINE.set(Mutex::new(engine));
}

/// 应用列表**热更新**：把 ENGINE 里的 apps provider 就地换成新索引。
///
/// 后台索引线程重扫开始菜单后（安装/卸载应用）调用它，**不重启 voke 即可搜到新
/// 应用、卸载的应用自动消失**。换用 [`QueryEngine::replace_provider`] 就地替换，
/// 保留注册位置（apps 恒在分组最前）。
///
/// 两点调用约定：
/// - **替换后必须再下发一次配置**（调用方接着调 `crate::config::apply_current`），
///   新 provider 带的是代码默认配置，不重下发会丢掉用户改过的 enabled / weight；
/// - 引擎尚未注入时静默忽略（首轮注入必先于任何热更新到达）。
pub fn update_apps(entries: Vec<AppEntry>) {
    let Some(engine) = ENGINE.get() else {
        return;
    };
    let apps = AppsProvider::new(entries);
    APP_COUNT.store(apps.len(), Ordering::Relaxed);
    let mut engine = engine
        .lock()
        .unwrap_or_else(|poisoned| poisoned.into_inner());
    engine.replace_provider(Box::new(apps));
}

/// 应用集合的**变更签名**：排序后的 `(显示名, 启动路径)` 列表。
///
/// 后台重扫线程用它判断"这一轮和上一轮有没有实质变化"——只有变化才重建 provider
/// 并推给主线程，避免每轮重扫都白重建一遍索引（建拼音索引是启动路径上最贵的一步）。
/// 显式排序是为让签名与枚举顺序无关（枚举顺序可能因文件系统而变，但那不算变化）。
pub fn apps_signature(entries: &[AppEntry]) -> Vec<(String, String)> {
    let mut sig: Vec<(String, String)> = entries
        .iter()
        .map(|e| (e.name.clone(), e.path.clone()))
        .collect();
    sig.sort();
    sig
}

fn provider_len() -> Option<usize> {
    APPS_READY.get()?;
    Some(APP_COUNT.load(Ordering::Relaxed))
}

/// 最近一次枚举到的窗口数（`0` 表示还没枚举过或系统里没有可切换窗口）。
pub fn window_count() -> usize {
    WINDOW_COUNT.load(Ordering::Relaxed)
}

/// app 层枚举完窗口后回写数量（供空状态文案展示）。
pub fn note_window_count(count: usize) {
    WINDOW_COUNT.store(count, Ordering::Relaxed);
}

/// 在引擎锁内执行 `f`。未注入 provider 时返回 `None`。
fn with_engine<R>(f: impl FnOnce(&QueryEngine) -> R) -> Option<R> {
    let engine = ENGINE.get()?;
    // 中毒的锁（某次查询 panic）不该让后续查询永久失败：数据是只读的
    // provider 索引 + 计数表，继续用是安全的。
    let guard = engine
        .lock()
        .unwrap_or_else(|poisoned| poisoned.into_inner());
    Some(f(&guard))
}

/// 把配置下发到查询引擎（阶段 3）。
///
/// 锁住进程单例 `ENGINE`，调 `QueryEngine::apply_config`：按 id 取
/// `ProviderSettings` 构造 `ProviderConfig` 调各 provider 的 `set_config`。
/// provider 未注入时静默跳过（启动早期还没数据）。
pub fn apply_config(cfg: &AppConfig) {
    let Some(engine) = ENGINE.get() else {
        return;
    };
    let mut engine = engine
        .lock()
        .unwrap_or_else(|poisoned| poisoned.into_inner());
    engine.apply_config(cfg);
}

/// 未激活插件时，`#`/`# ` 开头的输入进入**插件选择列表**（`#` 是它的专属入口，
/// 不再直进 timestamp；`=` 也不再直进 calculator——所有插件都从列表进）。
/// 判据用原始输入并兼容前导空格：`"#b"` → 过滤词 `"b"`，`"#"`/`"# "` → 空过滤词。
///
/// **为什么不再“打前缀直进插件”**（原 `detect_plugin` 的语义）：前缀激活是
/// 单向的——`detect_plugin("#b")` 返回 base64，但 `"#"` 先命中 timestamp，
/// 后续的 `b` 全被 timestamp 当查询文本吞掉，base64 永远组不起来（用户实测
/// 确认的致命 bug）。列表方案下 `#` 是所有插件的统一入口，语义单一、无序。
fn picker_filter(raw: &str) -> Option<String> {
    let rest = raw.trim_start().strip_prefix('#')?;
    Some(rest.trim_start().to_string())
}

/// `raw` 是否已形成一条**前缀路由**（Direct 命中，或 Indirect 的「前缀 + 空格」）。
///
/// 用于收紧 `#` 插件列表的拦截条件：`#` / `#b`（还没空格）仍进列表，而
/// `# ` / `#b `（带空格）已经能路由到 timestamp / base64，就不该再被列表抢走。
/// 引擎未注入时返回 `false`（列表照常工作）。
fn prefix_route_formed(raw: &str) -> bool {
    with_engine(|engine| engine.provider_by_prefix(raw).is_some()).unwrap_or(false)
}

/// 左侧插件角标：小色块背景 + 居中 Lucide 图标（底色由 provider_id 稳定哈希决定）。
///
/// 自由函数（不依赖 view）：行首 icon slot 二选一时（`render_leading_icon`），
/// 无图标的插件行就用它填充那个 slot；模式令牌的小色块也复用同一套配色 + 图标。
fn badge_block(provider_id: &str, cx: &Context<SearchView>) -> impl IntoElement {
    let badge = badge::plugin_badge(provider_id);
    let bg = badge::badge_background(badge, cx);
    let fg = badge::badge_foreground(cx);
    let icon = badge::plugin_icon(provider_id);
    div()
        .flex_shrink_0()
        .size(ROW_ICON_SIZE)
        .rounded(BADGE_RADIUS)
        .bg(bg)
        .flex()
        .items_center()
        .justify_center()
        // `size_4()` = 1rem = 16px，24px 色块内留 4px 边距；颜色用角标前景色。
        .child(Icon::new(icon).size_4().text_color(fg))
}

/// 搜索界面。
pub struct SearchView {
    /// 输入框状态（gpui 的编辑引擎）。文本的唯一来源就是它。
    query_input: Entity<InputState>,
    /// 结果行（已按 provider 分组、组内降序；**不含提示行**）。
    rows: Vec<RankedMatch>,
    /// 提示（`Interact::Hint`）：渲染在**输入框正下方的独立条带**里，
    /// 不进虚拟列表、不可选、不可点。
    hints: Vec<Match>,
    /// 选中项下标（只索引 `rows`）。`rows` 为空时是 `None`。
    selected: Option<usize>,
    /// 虚拟列表每项的尺寸（必需）。现在只含结果行（提示条带不在列表里）。
    item_sizes: Rc<Vec<Size<Pixels>>>,
    /// 滚动位置归它自己（Coding Guides "Lists, tables, and large data"）。
    scroll_handle: VirtualListScrollHandle,
    /// 本窗口的 HWND，用于 Esc / 启动前隐藏（gpui 的 hide 是空实现，
    /// 见 voke-sys 的 `hide_window` 文档）。
    hwnd: Option<isize>,
    /// 进行中的查询。发起新查询时替换它（丢弃旧 Task = 取消旧查询）。
    _query_task: Option<Task<()>>,
    /// 已发起查询的输入文本，用于丢弃过期结果（"后发先至"保护）。
    applied_query: String,
    /// 已发起查询时的激活插件（同样用于丢弃过期结果）。
    applied_plugin: Option<String>,
    /// 已发起查询时的子模式（同上）。
    applied_sub_mode: Option<String>,
    /// 已发起查询时的列表模式（同上，"后发先至"保护的一员）。
    applied_picker: bool,
    /// provider 是否已经注入完成（决定空状态是"没索引到"还是"还没索引完"）。
    apps_ready: bool,
    /// **当前激活的插件**（打专属前缀进入，或从结果里进入）。
    ///
    /// `None` = 全局搜索。`Some(id)` 时查询只走该插件
    /// （见 `voke_core::QueryEngine::prepare_in_mode`）。
    active_plugin: Option<String>,
    /// 激活插件的专属前缀（用于渲染前缀令牌 + 判断"用户是否已离开前缀"）。
    ///
    /// 配置化后前缀是 `Option<String>`（来自 `ProviderConfig`），不再是 `&'static`。
    active_prefix: Option<String>,
    /// 激活插件的展示名（渲染路径不查引擎，所以存一份展示快照）。
    active_plugin_name: String,
    /// **插件内的子模式**（如 base64 的 `"enc"` / `"dec"`）。
    ///
    /// `None` = 插件第一级（还没选方向）。由 `EnterSubquery` 条目进入，
    /// 经 `QueryCtx::sub_mode` 传给 provider，并决定输入框占位符。
    sub_mode: Option<String>,
    /// **插件选择列表模式**（`#` 入口的特殊状态）。
    ///
    /// `true` 时输入框里的内容是列表的过滤词（如 `#b` 的 `b`），结果区展示
    /// 所有 enabled 插件的列表（见 `run_query` 的 picker 分支），Enter / 点击
    /// 选中条目会激活对应插件。与 `active_plugin` 互斥（选中即退出本模式）。
    picker: bool,
    focus_handle: FocusHandle,
    /// awake 倒计时刷新任务（见 [`SearchView::sync_awake_ticker`]）。
    _awake_ticker: Option<Task<()>>,
    _subscriptions: Vec<Subscription>,
}

impl SearchView {
    pub fn new(hwnd: Option<isize>, window: &mut Window, cx: &mut Context<Self>) -> Self {
        let query_input = cx.new(|cx| {
            InputState::new(window, cx).placeholder(voke_core::provider::GLOBAL_PLACEHOLDER)
        });

        // 订阅必须存进 view，否则构造结束就被丢弃（skills 明确警告过这一点）。
        let subscription = cx.subscribe_in(
            &query_input,
            window,
            |this, _state, event: &InputEvent, window, cx| {
                if matches!(event, InputEvent::Change) {
                    this.on_query_changed(window, cx);
                }
            },
        );

        // 唤起后聚焦输入框的**确定时机**：窗口从 SW_HIDE 恢复时，
        // `activate_window()` 是在另一个 task 里异步抢前台的——热键回调里同步
        // `window.focus` 会在窗口真正激活**之前**执行，被丢弃（实测 GetFocus=0）。
        // 这里订阅窗口激活回调：窗口 active 的那一刻再聚焦，时机才是对的。
        let activation = cx.observe_window_activation(window, |this, window, cx| {
            if window.is_window_active() {
                let focus = this.query_input_focus_handle(cx);
                window.focus(&focus, cx);
                // 唤回时的刷新策略（2026-10-12 拍板）：**仅当当前插件不会因查询
                // 重掷值时才刷新**。窗口列表（`#w` 等）没有后台热更新、只是每次
                // 查询实时枚举，隐藏期间新开/退出的窗口要靠这次刷新才在用户敲
                // 第一个键前出现；而 random / uuid / timestamp 每次查询都产出
                // 新值，唤回重跑会把用户刚复制的值换掉——直接跳过。判据与
                // `refresh()` 的护栏同源：core 纯函数
                // `QueryEngine::plugin_regenerates_on_query`。
                let safe_to_refresh = this.active_plugin.as_deref().is_none_or(|id| {
                    !with_engine(|engine| engine.plugin_regenerates_on_query(id)).unwrap_or(false)
                });
                if safe_to_refresh {
                    this.refresh(cx);
                }
                // 应用列表的实时性与刷新策略无关：唤回照样触发后台重扫，集合
                // 真变了才走热更新（热更新里的重跑被 `refresh()` 的重掷护栏
                // 挡住重掷类插件）；awake 倒计时由常驻每秒 ticker 自己走，与
                // 显隐无关。用户改输入仍照常重算（`on_query_changed`），
                // 「重新生成」按钮仍走 `Action::Refresh` 显式重掷。
                crate::request_apps_scan();
            }
        });

        let mut this = Self {
            query_input,
            rows: Vec::new(),
            hints: Vec::new(),
            selected: None,
            item_sizes: Rc::new(Vec::new()),
            scroll_handle: VirtualListScrollHandle::new(),
            hwnd,
            _query_task: None,
            applied_query: String::new(),
            applied_plugin: None,
            applied_sub_mode: None,
            applied_picker: false,
            apps_ready: provider_len().is_some(),
            active_plugin: None,
            active_prefix: None,
            active_plugin_name: String::new(),
            sub_mode: None,
            picker: false,
            focus_handle: cx.focus_handle(),
            _awake_ticker: None,
            _subscriptions: vec![subscription, activation],
        };
        // 空 query 走一次：engine 的空输入语义是"展示全部，靠频率加成排序"。
        this.spawn_query(String::new(), cx, true);
        this
    }

    /// provider 注入完成后由 main 调用：重跑当前查询并重绘。
    ///
    /// **必须强制重查**（`force = true`）：注入前的首查是在 engine 还没注册
    /// provider 时跑的（返回空），而当时的 `applied_query` 已经是空串。若这里
    /// 走普通的去重路径，`"" == ""` 会直接 return，注入后的全量列表永远不会出现
    /// ——只有用户真的打字才会触发查询。
    pub fn on_provider_ready(&mut self, cx: &mut Context<Self>) {
        self.apps_ready = true;
        let query = self.query_input.read(cx).value().to_string();
        self.spawn_query(query, cx, true);
        cx.notify();
    }

    /// 强制重跑**当前输入**的查询（无视去重）。
    ///
    /// 触发点：
    /// 1. 应用列表后台重扫到变化时（`main` 的热更新消费循环）——让新装/卸载的
    ///    应用立刻反映到列表里；
    /// 2. 面板唤回时（窗口激活观察者；调用前已自行判过「当前插件不会因查询
    ///    重掷值」，这里面的护栏是第二道防线）。
    ///
    /// **重掷类插件不重跑**（护栏，判据是 core 的纯函数
    /// [`QueryEngine::plugin_regenerates_on_query`]）：random / uuid /
    /// timestamp 的每次查询都会产出新值，热更新时重跑只会把用户刚复制、
    /// 正要复制的值悄悄换掉。跳过没有损失——热更新的目的是让新应用可见，
    /// 而重跑一个 random 查询本来也变不出任何应用；等用户退回全局搜索时
    /// （`deactivate_plugin`）自然就看到了。
    ///
    /// 走的是常规查询路径（`spawn_query` 在 background executor 上跑），
    /// 不会阻塞 UI 线程。
    pub fn refresh(&mut self, cx: &mut Context<Self>) {
        if let Some(active) = self.active_plugin.as_deref() {
            if with_engine(|engine| engine.plugin_regenerates_on_query(active)).unwrap_or(false) {
                log::debug!("跳过刷新：当前插件 {active} 的查询会重掷值（应用热更新不打扰）");
                return;
            }
        }
        let query = self.query_input.read(cx).value().to_string();
        self.spawn_query(query, cx, true);
    }

    /// 输入框自己的 focus handle（热键唤起后由 `Shell` 调 `window.focus`）。
    ///
    /// 为什么不是 view 的 `focus_handle`：`window.handle_input(...)` 只在
    /// **输入框自己的** `focus_handle` 已聚焦时才注册文本输入处理器
    /// （`gpui-base` 的 `base/element.rs::paint`），所以打字能进去的前提就是它。
    pub fn query_input_focus_handle(&self, cx: &gpui_kit::App) -> FocusHandle {
        self.query_input.focus_handle(cx)
    }

    /// 当前输入框文本（唯一来源是 `InputState`）。
    fn query_text(&self, cx: &Context<Self>) -> String {
        self.query_input.read(cx).value().to_string()
    }

    /// 输入变化：取当前文本发起查询，并维护激活插件 / 子模式 / 插件列表模式。
    ///
    /// 路由语义（`#` 插件列表方案，用户拍板）：
    ///
    /// 1. **已激活插件**：只查它。退出**与输入内容无关**（前缀是令牌不进输入
    ///    框），只走 Esc / 空输入 Backspace（`step_back`）。
    /// 2. **`#` 开头、未激活**：进入**插件选择列表**（`picker = true`），
    ///    不查任何 provider；继续打字是过滤列表，Enter 选中插件才激活。
    /// 3. 其余（含 `=` 开头——`=` 也不再直进 calculator）：常规前缀路由 /
    ///    全局搜索（引擎的 `prepare` 语义不变，只是 app 层不再用它直进插件）。
    fn on_query_changed(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let raw = self.query_text(cx);

        // ── 已激活插件 ──
        if self.active_plugin.is_some() {
            // 激活后输入框里**只有插件查询本身**，前缀是旁边的不可编辑令牌
            // （`render_mode_token`），从不进入输入框文本。
            //
            // 因此这里**不做**"输入里没有前缀就退出插件"的判断——那正是用户
            // 实测 bug 的根源：激活时把前缀写进输入框（`# `），用户删掉它再输
            // 别的内容，就被误判成"离开前缀"而踢回全局搜索。退出只走 Esc /
            // 空输入 Backspace（`step_back`），与输入内容无关；无前缀插件
            // （active_prefix = None）同理，本就没有前缀边界可言。
            self.spawn_query(raw, cx, false);
            return;
        }

        // ── 未激活：`#` 开头且**尚未形成前缀路由** → 插件选择列表 ──
        //
        // `#` 系插件默认是 Indirect（`#` + 空格才激活）：`#` / `#b`（还没空格）
        // 进列表；一旦打了空格（`# ` / `#b `）就形成路由，交给引擎直进
        // timestamp / base64，列表不再拦截。
        if picker_filter(&raw).is_some() && !prefix_route_formed(&raw) {
            if !self.picker {
                log::debug!("输入 `#` → 插件选择列表");
                self.picker = true;
                self.sub_mode = None;
                self.sync_placeholder(window, cx);
            }
            self.spawn_query(raw, cx, false);
            return;
        }

        // ── 未激活、非 `#` 输入：常规路由（全局搜索 / 普通查询）──
        if self.picker {
            // 刚从列表退出来（用户删掉了 `#`）：占位符随之还原。
            self.picker = false;
            self.sync_placeholder(window, cx);
        }
        self.spawn_query(raw, cx, false);
    }

    /// 在 background executor 上驱动 core 的 `BoxFuture`，回到主线程写结果。
    ///
    /// 桥接方式对齐 docs/HISTORY.md 的架构决策 1：core 用 `BoxFuture`（零 gpui 依赖），
    /// app 层用 `background_spawn` 把它变成 `Task`。
    ///
    /// **去重**：`raw == applied_query` 且激活态/子模式/列表模式都没变时直接返回；
    /// `force = true` 跳过去重（provider 注入后的强制重查）。
    ///
    /// **过期结果丢弃**：结果落地时比对本函数发起时的四元组（含 picker），
    /// 任何一个变了就丢掉这一批（慢 provider 的结果不能覆盖新查询的结果）。
    fn spawn_query(&mut self, raw: String, cx: &mut Context<Self>, force: bool) {
        if !force
            && raw == self.applied_query
            && self.active_plugin == self.applied_plugin
            && self.sub_mode == self.applied_sub_mode
            && self.picker == self.applied_picker
        {
            return;
        }
        self.applied_query = raw.clone();
        self.applied_plugin = self.active_plugin.clone();
        self.applied_sub_mode = self.sub_mode.clone();
        self.applied_picker = self.picker;
        let active = self.active_plugin.clone();
        let sub_mode = self.sub_mode.clone();
        let picker = self.picker;

        let task = cx.spawn(async move |this, cx: &mut gpui_kit::AsyncApp| {
            let query = raw.clone();
            let active_for_query = active.clone();
            let sub_for_query = sub_mode.clone();
            let outcome = cx
                .background_spawn(async move {
                    run_query(
                        &query,
                        active_for_query.as_deref(),
                        sub_for_query.as_deref(),
                        picker,
                    )
                    .await
                })
                .await;

            let _ = this.update(cx, |this, cx| {
                if this.applied_query != raw
                    || this.applied_plugin != active
                    || this.applied_sub_mode != sub_mode
                    || this.applied_picker != picker
                {
                    log::debug!(
                        "丢弃过期查询结果: {raw:?}（active={active:?}, sub={sub_mode:?}, picker={picker}）"
                    );
                    return;
                }
                this.set_outcome(outcome);
                // 结果落地后检查倒计时：进入"保持唤醒中"就起定时器，
                // 取消了就停（`sync_awake_ticker` 自己幂等）。
                this.sync_awake_ticker(cx);
                cx.notify();
            });
        });
        self._query_task = Some(task);
    }

    /// 写入结果、重置选中、重建行尺寸。
    fn set_outcome(&mut self, outcome: QueryOutcome) {
        self.selected = if outcome.rows.is_empty() {
            None
        } else {
            Some(0)
        };
        // 虚拟列表只装结果行：提示（hints）渲染在输入框下方的独立条带里，不占列表项。
        let sizes: Vec<Size<Pixels>> = vec![size(px(0.), ROW_HEIGHT); outcome.rows.len()];
        self.item_sizes = Rc::new(sizes);
        self.rows = outcome.rows;
        self.hints = outcome.hints;
        self.scroll_handle.scroll_to_item(0, ScrollStrategy::Top);
    }

    /// awake 倒计时：保持唤醒中时每秒重跑一次当前查询，让"还剩 xx"自己走。
    ///
    /// **为什么需要它**：awake 的剩余时间是**现读** `voke-sys` 原子状态算出来的
    /// （`AwakeProvider` 不缓存）。不重跑查询的话，屏幕上那行"还剩 25 分钟"会一直
    /// 冻在设置那一刻——直到用户再敲一个键。任务里明确要求倒计时自己刷新。
    ///
    /// 只在"正在保持"时跑：没在保持时这个定时器没有任何意义，空转只是白烧 CPU。
    /// 每次重跑查询都会重评估一次，所以取消/到期后下一批结果就会把定时器停掉。
    fn sync_awake_ticker(&mut self, cx: &mut Context<Self>) {
        if !self.awake_is_active() {
            // 丢弃 Task = 取消定时器（gpui 的 Task 在 drop 时取消）。
            self._awake_ticker = None;
            return;
        }
        if self._awake_ticker.is_some() {
            return;
        }
        log::debug!("保持唤醒中 -> 启动每秒倒计时刷新");
        self._awake_ticker = Some(cx.spawn(async move |this, cx: &mut gpui_kit::AsyncApp| {
            loop {
                cx.background_executor()
                    .timer(std::time::Duration::from_secs(1))
                    .await;
                let keep_going = this.update(cx, |this, cx| {
                    if !this.awake_is_active() {
                        // 已不再保持：停掉循环（Task 会被下一轮 `sync_awake_ticker` 清掉）。
                        return false;
                    }
                    // 绕过去重强制重跑（输入没变，但状态变了）。
                    let query = this.query_text(cx);
                    this.spawn_query(query, cx, true);
                    true
                });
                match keep_going {
                    Ok(true) => continue,
                    _ => break,
                }
            }
        }));
    }

    /// 当前是否正在保持系统唤醒：看最新一批结果里有没有 awake 的**取消项**。
    ///
    /// 判据是 awake 的取消动作（`Action::CancelAwake`）而不是标题文案：provider
    /// 只在**已激活**时才产出这一行（见 `voke-core/src/awake.rs` 的
    /// `query_level1`），而标题里的剩余时间、范围措辞会随迭代变化——拿 action
    /// 判不会因为改文案而失效（以前判的是一条 Hint，provider 早已把状态行改成
    /// 可选中的取消项，那条判据恒 false，倒计时从不刷新）。
    ///
    /// 不直接读 `voke_sys`：这个 view 在非 Windows 构建上也要能编译/测试，
    /// 而 provider 已经持有状态源，结果区就是它的投影。
    fn awake_is_active(&self) -> bool {
        self.rows
            .iter()
            .any(|ranked| matches!(ranked.inner.action, voke_core::Action::CancelAwake))
    }

    /// 移动选中项并让它滚进可见区（只在 `rows` 范围内）。
    fn move_selection(&mut self, delta: isize, cx: &mut Context<Self>) {
        if self.rows.is_empty() {
            return;
        }
        let count = self.rows.len() as isize;
        let current = self.selected.unwrap_or(0) as isize;
        let next = (current + delta).clamp(0, count - 1) as usize;
        self.selected = Some(next);
        // 键盘导航必须在模型坐标里滚，而不是"当前渲染了哪些行"。
        self.scroll_handle
            .scroll_to_item(next, ScrollStrategy::Nearest);
        cx.notify();
    }

    /// Enter：执行当前选中项。按 `interact` 分发。
    fn confirm(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let Some(index) = self.selected else {
            log::info!("Enter：当前没有可执行结果，忽略");
            return;
        };
        self.confirm_index(index, window, cx);
    }

    /// 执行**第 index 行**：Enter 与鼠标点击走同一条路径（方案 A 的统一）。
    ///
    /// 分发规则就是 `Match.interact`：
    /// - `Execute` → 走 dispatcher（副作用集中在那里）
    /// - `EnterSubquery(mode)` → 进入子模式（纯 view 状态 + 输入框，需要 Window）
    /// - `Hint` → 不可选，理论上到不了这里；只记一条日志
    ///
    /// **执行后的输入框复位规则**（用户实测 UX，判据是
    /// [`Action::should_reset_input`](voke_core::Action::should_reset_input)）：
    /// - 「离开面板去做别的事」（启动 / 切窗 / 开 URL / 保持唤醒）→ 复位：
    ///   清空输入、退出插件、结果列表回顶层（`reset_after_action`）。
    /// - 「产出可复制内容」（Copy：随机值 / UUID / 时间戳 / base64 / 计算器）
    ///   → **什么都不做**：输入与结果原样保留（面板已被 dispatcher 隐藏，
    ///   下次唤回即见，可再次复制）。
    /// - `Refresh`（random 的「重新生成」）→ 不改输入，用同一查询重跑一次。
    fn confirm_index(&mut self, index: usize, window: &mut Window, cx: &mut Context<Self>) {
        let Some(ranked) = self.rows.get(index) else {
            return;
        };
        let action = ranked.inner.action.clone();
        let title = ranked.inner.title.clone();
        let provider_id = ranked.inner.provider_id.clone();
        let interact = ranked.inner.interact.clone();

        // ── 插件选择列表：选中 = 激活该插件，不走下面的子模式/执行路径 ──
        // 列表条目的 `interact` 也是 `EnterSubquery`（字符串是插件 id），
        // 必须先拦住：它的语义是"激活插件"，不是"进入插件的子模式"。
        if self.picker {
            if let Interact::EnterSubquery(plugin_id) = interact {
                self.activate_plugin_from_picker(plugin_id, window, cx);
            }
            return;
        }

        match interact {
            Interact::EnterSubquery(mode) => {
                log::info!("进入子模式 {mode:?}（{provider_id} / {title}）");
                self.enter_sub_mode(mode, window, cx);
                return;
            }
            Interact::Hint => {
                log::debug!("Hint 行不可执行，忽略（{title}）");
                return;
            }
            Interact::Execute => {}
        }

        log::info!("执行选中项 [{index}] {title}");

        // 记一次使用：频率加成会在下一次 rank 时生效（§4 的使用频率）。
        //
        // **必须紧接着落盘**：频率表是"越用越准"的唯一来源，只留在内存里的话，
        // 进程一退出（关机、更新、崩溃）就全部归零，用户会觉得 voke 完全不认识
        // 自己。写盘纪律与配置一致：失败只记日志，不影响这次执行。
        let frequency_snapshot = if let Some(engine) = ENGINE.get() {
            let mut engine = engine
                .lock()
                .unwrap_or_else(|poisoned| poisoned.into_inner());
            engine.record_use(&format!("{provider_id}:{title}"));
            Some(engine.frequency().clone())
        } else {
            None
        };
        if let Some(frequency) = frequency_snapshot {
            crate::config::save_frequency(&frequency);
        }

        // 副作用能力由 gpui 的 `App` 提供（`Context<T>` Deref 到 `App`）。
        //
        // **纪律：本调用运行在 gpui 事件分发的 App 借作用域内**（按键/点击经
        // `AsyncApp::update_window` 持有 `AppCell` 可变借用）。`dispatcher::execute`
        // 内部对会泵消息的 Win32 API（`ShellExecuteExW` / App Activation）一律两阶段
        // 处理：借作用域内只做决策 + 登记请求，真正执行推迟到前台执行器（借作用域
        // 之外）。**不得在本作用域内新增任何同步泵消息的 Win32 调用**——嵌套消息泵
        // 会重入 gpui wndproc，硬 `borrow_mut` panic 出在 extern "C" 里不可 unwind，
        // 直接 abort 整进程（2026-10-13 用户实测 `.lnk` 启动崩溃，详见
        // `dispatcher` 模块文档「两阶段纪律」）。
        let hwnd = self.hwnd;
        let had_side_effect = dispatcher::execute(&action, hwnd, &**cx);
        if !had_side_effect {
            return;
        }

        // 「离开面板去做别的事」的动作（启动 / 切窗 / 开 URL / Reveal / 保持
        // 唤醒）：窗口已由 dispatcher 先隐藏，这里把界面复位成干净的顶层状态
        // ——清空输入、退出插件、结果列表复位，保证下次唤回不是残留的插件页
        // 或上次的查询。一次只干一件事，干完就收。
        //
        // 判据集中在 core 的 `Action::should_reset_input`（纯函数 + 单测），
        // 这里不维护第二份动作清单。
        if action.should_reset_input() {
            self.reset_after_action(window, cx);
            return;
        }

        // 「产出可复制内容」的 Copy：**什么都不做**——输入与结果原样保留。
        // 注意不能顺手重跑查询：random 的每次查询都会重新生成，重跑会把刚
        // 复制掉的值换掉，用户就没法"再看一眼 / 再复制一次"了。想要新值是
        // random 二级里首条显式的「重新生成」（Action::Refresh），它仍走
        // 下面的重跑分支。
        //
        // `Refresh`：不改输入、不动路由，用同一份查询再跑一次（random 重掷）。
        if matches!(action, voke_core::Action::Refresh) {
            let query = self.query_text(cx);
            self.spawn_query(query, cx, true);
        }
    }

    /// 按 provider id **程序化**激活插件（托盘「保持唤醒」条目直达）。
    ///
    /// 与 [`Self::activate_plugin_from_picker`] 的差别只在入口：这里不需要用户先
    /// 在 `#` 列表里选中，直接按 id 激活。激活动作完全复用同一条路径（退出列表
    /// 模式、置 `active_plugin`、清空输入、重查第一级、回焦输入框）；id 不存在
    /// （如插件被禁用）时也只记日志、不改状态。
    pub fn activate_plugin(
        &mut self,
        plugin_id: &str,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        self.activate_plugin_from_picker(plugin_id.to_string(), window, cx);
    }

    /// 从 `#` 插件列表选中并激活一个插件（picker 模式的唯一出口）。
    ///
    /// `plugin_id` 来自列表条目的 `EnterSubquery` 字符串；前缀与展示名从引擎
    /// 现取（列表数据与激活数据同源）。动作：
    ///
    /// 1. 退出列表模式，`active_plugin` = 插件 id，子模式清空（插件第一级）
    /// 2. **输入框清空**：前缀是输入框**旁边**的不可编辑令牌
    ///    （`render_mode_token`），**不写进输入框文本**。写进去正是用户实测的
    ///    bug：输入框里留着 `#`，删掉它或删掉后再输入别的内容就被判成"离开
    ///    前缀"而踢回全局搜索。激活时插件查询为空。
    /// 3. 立即查一次插件第一级（空查询语义：base64 列加密/解密、awake 列时长…）
    /// 4. 焦点保持在输入框（用户马上要打内容）
    fn activate_plugin_from_picker(
        &mut self,
        plugin_id: String,
        window: &mut Window,
        cx: &mut Context<Self>,
    ) {
        // 需要展示名 + 裸前缀（&'static，来自 `ProviderConfig`）：前缀只用于
        // 渲染旁边的令牌，不再写进输入框。
        let Some((name, bare_prefix)) = with_engine(|engine| {
            let provider = engine.provider(&plugin_id)?;
            Some((
                provider.name().to_string(),
                provider.config().prefix.clone(),
            ))
        })
        .flatten() else {
            log::warn!("插件列表选中了不存在的插件 {plugin_id}（可能刚被禁用）");
            return;
        };

        log::info!("插件列表选中 → 激活 {plugin_id}（{name}），输入框清空（前缀走令牌渲染）");
        self.picker = false;
        self.active_plugin = Some(plugin_id);
        self.active_plugin_name = name;
        self.active_prefix = bare_prefix;
        self.sub_mode = None;
        self.query_input.update(cx, |state, cx| {
            state.set_value("", window, cx);
        });
        self.sync_placeholder(window, cx);
        // `set_value` 不触发 `InputEvent::Change`，所以显式重查一次插件第一级。
        self.spawn_query(String::new(), cx, true);
        self.focus_input(window, cx);
    }

    /// 进入子模式（`EnterSubquery` 的唯一入口，Enter 与点击共用）。
    ///
    /// 清空输入框：用户刚表达的是"我要加密"，接下来要输的是**内容**。激活插件时
    /// 输入框本就是空的（前缀是令牌，不进输入框），这里再清一次是防"用户在第一级
    /// 已打了字再选方向"的残留——子模式应从干净的空输入开始。
    fn enter_sub_mode(&mut self, mode: String, window: &mut Window, cx: &mut Context<Self>) {
        self.sub_mode = Some(mode);
        self.query_input.update(cx, |state, cx| {
            state.set_value("", window, cx);
        });
        self.sync_placeholder(window, cx);
        // `set_value` 不触发 `InputEvent::Change`，所以显式重查一次。
        self.spawn_query(String::new(), cx, true);
        // 焦点保持在输入框（用户马上要打字）。
        self.focus_input(window, cx);
    }

    /// 退出子模式，回到插件第一级。返回是否真的退出了（Esc / Backspace 用）。
    fn exit_sub_mode(&mut self, window: &mut Window, cx: &mut Context<Self>) -> bool {
        if self.sub_mode.take().is_none() {
            return false;
        }
        log::info!("退出子模式 → 回到插件第一级");
        self.query_input.update(cx, |state, cx| {
            state.set_value("", window, cx);
        });
        self.sync_placeholder(window, cx);
        self.spawn_query(String::new(), cx, true);
        true
    }

    /// Esc / 空输入 Backspace 的退出链：退子模式 → 退插件（清空输入）→ 关列表。
    ///
    /// 返回 `true` 表示这次按键被"往上退一级"消费了；`false` 表示已经在最外层。
    /// 返回 false 后的行为由调用方决定：**Esc 才隐藏窗口**，Backspace 到顶
    /// 保持顶级空输入不动（见 `on_key` 的 backspace 分支）。
    fn step_back(&mut self, window: &mut Window, cx: &mut Context<Self>) -> bool {
        if self.exit_sub_mode(window, cx) {
            return true;
        }
        if self.deactivate_plugin(window, cx) {
            return true;
        }
        // `#` 插件列表是全局之下的一个层级：Esc 先关列表（退到全局搜索），
        // 再按一次才隐藏窗口。
        self.close_picker(window, cx)
    }

    /// 关闭插件选择列表，回到全局搜索。返回是否真的关了。
    ///
    /// 清空输入框：列表模式下输入框里是过滤词（`#b` 的 `b`），它是列表的内部
    /// 状态，不是全局搜索词——Esc 是"我要离开"，清空才符合直觉。
    fn close_picker(&mut self, window: &mut Window, cx: &mut Context<Self>) -> bool {
        if !self.picker {
            return false;
        }
        log::info!("关闭插件选择列表 -> 回到全局搜索");
        self.picker = false;
        self.sub_mode = None;
        self.query_input.update(cx, |state, cx| {
            state.set_value("", window, cx);
        });
        self.sync_placeholder(window, cx);
        self.spawn_query(String::new(), cx, true);
        true
    }

    /// 取消插件激活，回到常规搜索。返回是否真的取消了。
    fn deactivate_plugin(&mut self, window: &mut Window, cx: &mut Context<Self>) -> bool {
        if self.active_plugin.is_none() {
            return false;
        }
        log::info!("取消插件激活 -> 回到全局搜索");
        self.active_plugin = None;
        self.active_prefix = None;
        self.active_plugin_name.clear();
        self.sub_mode = None;
        self.picker = false;
        self.query_input.update(cx, |state, cx| {
            state.set_value("", window, cx);
        });
        self.sync_placeholder(window, cx);
        self.spawn_query(String::new(), cx, true);
        true
    }

    /// 把 placeholder 同步成当前模式的说法。
    ///
    /// 只在**状态变化时**调用（激活 / 进子模式 / 退出 / 注入完成），
    /// 不在 `render` 里调用——render 应当只读（Coding Guides）。
    fn sync_placeholder(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let text = self.placeholder_text();
        self.query_input.update(cx, |state, cx| {
            state.set_placeholder(text, window, cx);
        });
    }

    /// 当前模式对应的占位符（查引擎的 `Provider::placeholder`）。
    ///
    /// 插件列表模式优先：它的占位符不是某个 provider 的，而是列表自己的
    /// 过滤提示（"输入过滤插件"）。
    fn placeholder_text(&self) -> &'static str {
        if self.picker {
            return PICKER_PLACEHOLDER;
        }
        let plugin_id = self.active_plugin.as_deref();
        let sub_mode = self.sub_mode.as_deref();
        with_engine(|engine| engine.placeholder(plugin_id, sub_mode))
            .unwrap_or(voke_core::provider::GLOBAL_PLACEHOLDER)
    }

    /// 把焦点放到输入框（进入子模式后）。
    fn focus_input(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        let handle = self.query_input.focus_handle(cx);
        window.focus(&handle, cx);
    }

    /// 「离开面板」类动作执行完后的界面复位：退出插件 + 清空输入 + 回到顶层列表。
    ///
    /// 调用时机在窗口**已被 `dispatcher` 隐藏之后**（见 `confirm_index` 里
    /// `Action::should_reset_input` 为真的分支——启动 / 切窗 / 开 URL /
    /// 保持唤醒等）。末尾那次空查询是有意的：它把顶层结果预置好，下次唤起的
    /// 第一帧就是干净的全局列表，不会闪一下残留的插件结果；同时也把
    /// `active_plugin` 从视图中清掉（倒计时刷新等插件态就此停下）。
    fn reset_after_action(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        if self.query_input.read(cx).value().is_empty() && self.active_plugin.is_none() {
            return;
        }
        self.picker = false;
        self.active_plugin = None;
        self.active_prefix = None;
        self.active_plugin_name.clear();
        self.sub_mode = None;
        self.query_input.update(cx, |state, cx| {
            state.set_value("", window, cx);
        });
        self.sync_placeholder(window, cx);
        self.spawn_query(String::new(), cx, true);
    }

    /// Esc / 空输入 Backspace：隐藏窗口（不是关闭；窗口常驻，热键再唤起）。
    fn hide(&mut self) {
        log::info!("Esc -> 隐藏窗口");
        dispatcher::hide_launcher(self.hwnd);
    }

    /// 空列表时的提示：区分"还没索引完 / 没索引到 / 这次没命中"。
    ///
    /// 提示行（`hints`）已经在列表里另外渲染，这里不重复它们。
    fn render_empty(&self, cx: &Context<Self>) -> impl IntoElement {
        let (title, detail) = if self.picker {
            // 列表模式：过滤词不中任何插件。
            (
                "没有匹配的插件".to_string(),
                "检查拼写，或清掉过滤词查看全部插件".to_string(),
            )
        } else if self.active_plugin.is_some() && self.sub_mode.is_none() {
            // 插件第一级还没有输入（例如 base64 刚进来）且 provider 没给条目。
            (
                "选择一个方向".to_string(),
                "用 ↑↓ 选择，Enter 进入".to_string(),
            )
        } else {
            match provider_len() {
                None => (
                    "正在索引开始菜单应用…".to_string(),
                    "首次启动需要解析快捷方式，稍等片刻".to_string(),
                ),
                Some(0) => (
                    "没有索引到应用".to_string(),
                    "开始菜单里没找到可用快捷方式，详见 voke.log".to_string(),
                ),
                Some(_) if self.applied_query.trim().is_empty() => (
                    "开始输入以搜索".to_string(),
                    format!(
                        "已索引 {} 个应用 · {} 个窗口",
                        provider_len().unwrap_or(0),
                        window_count()
                    ),
                ),
                Some(_) => (
                    format!("没有匹配「{}」的结果", self.applied_query.trim()),
                    "换个关键词，或用 # 打开插件列表".to_string(),
                ),
            }
        };
        let _ = self.apps_ready;
        div()
            .flex_none()
            .py_6()
            .w_full()
            .flex()
            .flex_col()
            .items_center()
            .justify_center()
            .gap_1()
            .child(
                div()
                    .text_sm()
                    .text_color(cx.theme().foreground)
                    .child(SharedString::from(title)),
            )
            .child(
                div()
                    .text_xs()
                    .text_color(cx.theme().muted_foreground)
                    .child(SharedString::from(detail)),
            )
    }

    /// 渲染一行结果（含左侧插件角标）。选中态用主题的 `list_active` 语义色。
    fn render_row(&self, index: usize, cx: &Context<Self>) -> impl IntoElement {
        let Some(ranked) = self.rows.get(index) else {
            return div().into_any_element();
        };
        let selected = self.selected == Some(index);
        let title = ranked.inner.title.clone();
        let subtitle = ranked.inner.subtitle.clone();
        let provider_id = ranked.inner.provider_id.clone();
        // 行身份用 view 的实体 id 作命名空间，避免与子树里其它列表撞 id。
        let namespace = cx.entity_id();
        // **icon slot 二选一**：有真图标的行（apps/windows）只显示 PNG，
        // 没有的行（插件）在同一个 slot 显示插件角标——不再叠加、不再
        // 额外占一个独立位置。
        let leading =
            self.render_leading_icon(ranked.inner.icon.as_deref(), &provider_id, selected, cx);

        let row_id = (
            gpui_kit::ElementId::from(("voke-result-row", namespace)),
            index.to_string(),
        );
        div()
            // ElementId 支持 (ElementId, T) 嵌套；先用 (&str, EntityId) 得到
            // 命名空间 id，再挂行号做子 id。
            .id(row_id)
            .flex()
            .flex_row()
            .items_center()
            .gap_3()
            .w_full()
            .h(ROW_HEIGHT)
            .px_3()
            .cursor_pointer()
            .rounded(cx.theme().radius)
            .when(selected, |this| this.bg(cx.theme().list_active))
            // hover：把鼠标所在行变成选中行（键盘与鼠标共用同一个 `selected`）。
            // 以前 hover 只改背景、`selected` 不动，于是"键盘选中的行"和"鼠标指着
            // 的行"同时高亮成两块不同颜色的选中色——用户要判断 Enter 会执行哪一
            // 条，只能靠颜色分辨。统一成一条：鼠标经过即选中。
            //
            // gpui 的 hover listener 默认"键盘输入会抑制 hover，直到下一次鼠标移
            // 动"（见 gpui `InteractiveElement::on_hover` 的文档），所以键盘导航不
            // 会被静止的鼠标指针抢走选中项。
            .on_hover(cx.listener(move |this, hovered: &bool, _window, cx| {
                if *hovered && this.selected != Some(index) && index < this.rows.len() {
                    this.selected = Some(index);
                    cx.notify();
                }
            }))
            // **方案 A：点击 = Enter**，按 `interact` 分发（见 confirm_index）。
            .on_click(cx.listener(move |this, _event: &ClickEvent, window, cx| {
                this.on_row_click(index, window, cx);
            }))
            .child(leading)
            .child(
                div()
                    .flex_1()
                    .min_w_0()
                    .flex()
                    .flex_col()
                    .child(div().truncate().text_sm().child(SharedString::from(title)))
                    .when(!subtitle.is_empty(), |this| {
                        this.child(
                            div()
                                .truncate()
                                .text_xs()
                                .text_color(cx.theme().muted_foreground)
                                .child(SharedString::from(subtitle)),
                        )
                    }),
            )
            .into_any_element()
    }

    /// 渲染**输入框正下方的提示条带**（`Interact::Hint`）。
    ///
    /// 提示不是候选项，所以不进虚拟列表、不参与选中、不带插件色块：它只是一条
    /// "解释当前状态"的说明行（如计算器报错、awake 倒计时），用最弱的文字层级
    /// 贴在输入框下方。无提示时返回 `None`，条带高度为 0（不留白）。
    ///
    /// 多条提示时只取**第一条非空**的：错误 / 状态提示通常唯一，逐条堆叠会把
    /// 输入区撑高、挤压结果区；真要区分多条时再扩展这里的策略。
    fn render_hint_bar(&self, cx: &Context<Self>) -> Option<AnyElement> {
        let hint = self
            .hints
            .iter()
            .find(|m| !m.title.is_empty() || !m.subtitle.is_empty())?;
        let text = if hint.subtitle.is_empty() {
            hint.title.clone()
        } else {
            format!("{} · {}", hint.title, hint.subtitle)
        };
        Some(
            div()
                .flex_none()
                .w_full()
                .px_3()
                .py_1()
                .flex()
                .flex_row()
                .items_center()
                .gap_2()
                .child(
                    Icon::new(IconName::Info)
                        .size_4()
                        .text_color(cx.theme().muted_foreground),
                )
                .child(
                    div()
                        .flex_1()
                        .min_w_0()
                        .truncate()
                        .text_xs()
                        .text_color(cx.theme().muted_foreground)
                        .child(SharedString::from(text)),
                )
                .into_any_element(),
        )
    }

    /// 鼠标点击一行：选中它，然后**按该行的 `interact` 执行**（与 Enter 完全一致）。
    ///
    /// 方案 A 删掉了 M5 的"面板行点一下即激活、普通行只选中"特判——那种
    /// "同一个动作在不同行语义不同"正是任务里说的反人类之处。现在只有一条规则：
    /// 点 = Enter。
    fn on_row_click(&mut self, index: usize, window: &mut Window, cx: &mut Context<Self>) {
        if index >= self.rows.len() {
            return;
        }
        self.selected = Some(index);
        self.confirm_index(index, window, cx);
    }

    /// 渲染行首图标：**icon slot 二选一**。
    ///
    /// - **有图标路径**（apps/windows，sys 层已把系统图标落成 32×32 PNG，见
    ///   `voke_sys::icon`）：只显示 PNG，**不叠加角标**。用 gpui 的 `img()`
    ///   加载。**必须传 `PathBuf`/`&Path`（`Resource::Path`），不能传
    ///   `&str`/`String`**：`ImageSource::from(&str)` 会先做
    ///   `url::Url::from_str` 判 URI，而 `C:\Users\...\a.png` 恰好能被解析成
    ///   scheme = `c` 的 URL，于是走 `Resource::Uri` 分支（HTTP 客户端 + `%5C`
    ///   转义），加载必然失败。这个坑已实测确认。PNG 缺失/损坏时
    ///   `with_fallback` 回退到主题色占位图标，不会留空白。
    /// - **无图标路径**（插件结果）：在 icon 同一个 slot 显示**插件角标**
    ///   （provider_id 哈希色块 + name 首字符）——不再在 icon 旁另外顶一个
    ///   独立角标（用户要求：二选一，一个 slot 不放两个标识）。
    fn render_leading_icon(
        &self,
        icon: Option<&str>,
        provider_id: &str,
        selected: bool,
        cx: &Context<Self>,
    ) -> AnyElement {
        if let Some(path) = icon.filter(|p| !p.is_empty()) {
            let source: ImageSource = ImageSource::from(std::path::PathBuf::from(path));
            // 行图标不需要交互状态，但 `with_fallback` 走的是
            // `StatefulInteractiveElement` 那条路，所以必须有稳定 id。
            let fallback_color = if selected {
                cx.theme().accent_foreground
            } else {
                cx.theme().muted_foreground
            };
            return img(source)
                .id(gpui_kit::ElementId::from(path.to_string()))
                .size(ROW_ICON_SIZE)
                .flex_shrink_0()
                .with_fallback(move || {
                    Icon::new(IconName::Frame)
                        .size_4()
                        .text_color(fallback_color)
                        .into_any_element()
                })
                .into_any_element();
        }
        badge_block(provider_id, cx).into_any_element()
    }

    /// 键盘导航（挂在根 div 上，理由见模块文档）。
    ///
    /// **Backspace 的语义**（用户明确要求）：
    /// - 输入框**有内容**时：`InputBaseState::backspace` 会消费掉这个键
    ///   （它绑在 `key_context("Input")` 上），我们根本收不到 key_down，
    ///   所以"删除字符 + 聚焦保持在输入框"是免费得到的。
    /// - 输入框**为空**时：`backspace` 源码显式 `cx.propagate()`（"Nothing to
    ///   delete at the start of the text. Propagate so an ancestor can act on
    ///   it."），于是这个 key_down 冒泡到根 div —— 我们把它当"往上一级"用：
    ///   退子模式 → 退插件 → 关列表；到顶后**什么都不做**（不隐藏窗口，
    ///   隐藏窗口是 Esc 的职责）。
    fn on_key(&mut self, event: &KeyDownEvent, window: &mut Window, cx: &mut Context<Self>) {
        // 键位设计（阶段 3，§2）：最小集导航键 + 组合键别名。
        // 先显式分派可控组合键，其余组合键一律放行（交给编辑引擎 / 将来扩展）。
        let m = &event.keystroke.modifiers;
        // `Ctrl+N` / `Ctrl+P` 等价 `↓` / `↑`（排除 Alt/Win 以免抢其它语义）。
        if m.control && !m.alt && !m.platform && event.keystroke.key == "n" {
            self.move_selection(1, cx);
            return;
        }
        if m.control && !m.alt && !m.platform && event.keystroke.key == "p" {
            self.move_selection(-1, cx);
            return;
        }
        // `Ctrl+,` 打开设置窗口（阶段 4）。
        //
        // 逗号属于标点键，`keystroke.key` 取什么值不直观，这里是**查过源码**的：
        // gpui-pre-windows 的 `events.rs::parse_normal_key` 先走 `parse_immutable`
        // （只有功能键有名），其余交给 `keyboard.rs::get_keystroke_key`
        // ——未按 Shift 时它用 `MapVirtualKeyW(MAPVK_VK_TO_CHAR)` 取逗号键的字符
        // 再转小写，即 `","`；按了 Shift 才会换成 `"<"`。下面多匹配几种候选只是
        // 防御（不同 gpui / 键盘布局实现细节留一道后手），主路径是 `","`。
        if m.control && is_comma_key(&event.keystroke.key) {
            crate::settings::open(cx);
            return;
        }
        // 其余带修饰键的组合（Alt+字母、Ctrl+Shift、Win 等）一律放行，不拦截。
        if m.control || m.alt || m.platform || m.function {
            return;
        }
        match event.keystroke.key.as_str() {
            // `Tab` / `Shift+Tab` 等价 `↓` / `↑`（gpui 未给 Tab 注册 KeyBinding，
            // `on_key_down` 能可靠收到 `key == "tab"`）。
            "tab" => {
                if m.shift {
                    self.move_selection(-1, cx);
                } else {
                    self.move_selection(1, cx);
                }
            }
            "up" => self.move_selection(-1, cx),
            "down" => self.move_selection(1, cx),
            "enter" => self.confirm(window, cx),
            "escape" => self.escape(window, cx),
            // 只有输入框为空时才会到这里（见方法文档）。
            // **Backspace 到顶不隐藏窗口**（用户实测反馈）：删空就停在顶级空
            // 输入；隐藏窗口是 Esc 的职责，只有 Esc 在 step_back 返回 false
            // 时才 hide（见 `escape`）。这里什么都不做即可。
            "backspace" => {
                self.step_back(window, cx);
            }
            _ => {}
        }
    }

    /// Esc 的优先级：先退子模式 / 取消插件激活（回到全局搜索），
    /// 已经在全局搜索时才隐藏窗口。
    fn escape(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        if self.step_back(window, cx) {
            return;
        }
        self.hide();
    }

    /// 输入框旁的**模式令牌**：让"我现在在哪个插件里"始终可见。
    ///
    /// 为什么必须有它：`#b` 类前缀插件激活后输入框里可能没有前缀了（进子模式时
    /// 会清空），如果没有这块提示，用户看到一堆奇怪候选会完全失去方位感。
    /// 它同时承担"当前模式"的展示职责（Design Guides：state must be visible）。
    fn render_mode_token(&self, cx: &Context<Self>) -> Option<AnyElement> {
        self.active_plugin.as_ref()?;
        let name = self.active_plugin_name.clone();
        let prefix = self.active_prefix.clone().unwrap_or_default();
        // 子模式也在令牌里显示（如 `#b Base64 · 加密`），与占位符互为补充。
        let sub = self.sub_mode.clone();
        let plugin_id = self.active_plugin.as_deref().unwrap_or_default();
        let badge = badge::plugin_badge(plugin_id);
        let badge_bg = badge::badge_background(badge, cx);
        let badge_fg = badge::badge_foreground(cx);
        let badge_icon = badge::plugin_icon(plugin_id);

        Some(
            div()
                .id("voke-active-plugin")
                .flex()
                .flex_row()
                .items_center()
                .gap_1()
                .flex_shrink_0()
                .px_2()
                .py_0p5()
                .rounded(cx.theme().radius)
                .bg(cx.theme().accent)
                .text_color(cx.theme().accent_foreground)
                .text_xs()
                // 小色块 + 居中图标，与结果行角标同一套配色 / 图标（只是更小）。
                .child(
                    div()
                        .size(px(18.))
                        .rounded(BADGE_RADIUS)
                        .bg(badge_bg)
                        .flex()
                        .items_center()
                        .justify_center()
                        .child(Icon::new(badge_icon).size_4().text_color(badge_fg)),
                )
                .child(SharedString::from(format!("{prefix}{name}")))
                .when_some(sub, |this, mode| {
                    this.child(SharedString::from(format!("· {mode}")))
                })
                .into_any_element(),
        )
    }
}

/// 这个键是不是逗号键（见 [`SearchView::on_key`] 里 `Ctrl+,` 的说明）。
///
/// 主判据是 `","`（查 gpui-pre-windows 源码得出的取值），另外几个写法是防御性候选。
fn is_comma_key(key: &str) -> bool {
    matches!(key, "," | "comma" | "oem_comma")
}

impl Focusable for SearchView {
    fn focus_handle(&self, _cx: &gpui_kit::App) -> FocusHandle {
        self.focus_handle.clone()
    }
}

impl Render for SearchView {
    fn render(&mut self, _window: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let rows_count = self.rows.len();
        let hints_count = self.hints.len();
        let item_sizes = self.item_sizes.clone();
        let view = cx.entity();

        let mode_token = self.render_mode_token(cx);
        let hint_bar = self.render_hint_bar(cx);
        // 非全局模式（插件列表 / 激活插件 / 子模式）统一提示"返回"；
        // 只有最外层全局搜索才提示"隐藏"与 `# 插件列表` 入口。
        let hint = if self.picker || self.active_plugin.is_some() {
            "↑↓ 选择 · Enter 执行 · Esc 返回"
        } else {
            "↑↓ 选择 · Enter 执行 · Esc 隐藏 · # 插件列表"
        };

        div()
            .id("voke-search")
            .track_focus(&self.focus_handle)
            .on_key_down(cx.listener(Self::on_key))
            .size_full()
            .flex()
            .flex_col()
            .bg(cx.theme().popover)
            .text_color(cx.theme().popover_foreground)
            // 输入区：底部一条分隔线，把"输入"和"结果"分成两个层次。
            .child(
                div()
                    .flex_none()
                    .px_3()
                    .py_2()
                    .border_b_1()
                    .border_color(cx.theme().border)
                    .flex()
                    .flex_row()
                    .items_center()
                    .gap_2()
                    // 当前模式（激活的插件 / 子模式）必须常驻可见。
                    .when_some(mode_token, |this, token| this.child(token))
                    .child(
                        div().flex_1().min_w_0().child(
                            Input::new(&self.query_input).appearance(false).prefix(
                                Icon::new(IconName::Search)
                                    .size_4()
                                    .text_color(cx.theme().muted_foreground),
                            ),
                        ),
                    ),
            )
            // 提示条带：紧贴输入框下方，只有在有提示时才占位（无提示 = 高度 0）。
            // 不进虚拟列表，所以既不参与键盘选中，也不随结果滚动。
            .when_some(hint_bar, |this, bar| this.child(bar))
            // 结果区：虚拟列表只渲染可见结果行；为空时给解释性空状态。
            .child(
                div()
                    .relative()
                    .flex_1()
                    .min_h_0()
                    .overflow_hidden()
                    .when(rows_count == 0 && hints_count == 0, |this| {
                        // 空状态：本模式下 provider 若有提示（hints），它会显示在上面的
                        // 条带里，比一句泛泛的空状态更有用，所以这里不重复渲染。
                        this.child(self.render_empty(cx))
                    })
                    .when(rows_count > 0, |this| {
                        this.child(
                            v_virtual_list(
                                view,
                                "voke-results",
                                item_sizes,
                                move |this, visible_range, _window, cx| {
                                    visible_range
                                        .map(|ix| this.render_row(ix, cx).into_any_element())
                                        .collect::<Vec<_>>()
                                },
                            )
                            .py_1()
                            .track_scroll(&self.scroll_handle),
                        )
                    }),
            )
            // 底部提示行：键盘路径必须可见（Design Guides 的 keyboard path）。
            .child(
                div()
                    .flex_none()
                    .px_3()
                    .py_1()
                    .border_t_1()
                    .border_color(cx.theme().border)
                    .text_xs()
                    .text_color(cx.theme().muted_foreground)
                    .child(SharedString::from(hint)),
            )
    }
}

/// 一次查询的结果：**分组排好序的结果行** + **提示行**。
#[derive(Default)]
pub struct QueryOutcome {
    /// 结果行（按 provider 分组、组内降序、组间保持注册顺序）。
    pub rows: Vec<RankedMatch>,
    /// 提示行（`Interact::Hint`），渲染在结果行之后，不可选不可点。
    pub hints: Vec<Match>,
}

/// 跑一次查询并分组排序（在 background executor 上 **`await`**）。
///
/// `active_plugin` / `sub_mode` 是 UI 的多级状态，传给
/// [`QueryEngine::prepare_in_mode`]：激活插件时只查它，`sub_mode` 透传进
/// `QueryCtx` 让 provider 知道自己在哪一级。
///
/// **`#` 插件列表**（`picker = true`）是本函数的第三个模式：不查任何 provider，
/// 只生成 `QueryEngine::plugin_picker_matches` 的列表（每个插件一条
/// `EnterSubquery` 行，动作 = `ActivatePlugin`）。它没有异步 provider 参与，
/// 直接同步算完（列表本身就是 O(插件数)，微不足道）。
///
/// **窗口 provider 的枚举也在这条后台路径上**：`WindowsProvider::query` 每次都会
/// 调注入的闭包（内部是 `EnumWindows`），几十到几百个窗口，绝不能放 UI 线程。
/// `spawn_query` 用 `cx.background_spawn` 驱动本函数，所以这里面的 await 发生在
/// background executor 上，不阻塞 UI。
///
/// **provider 的 future 是真 `await`，不再要求"一次 poll 必定 Ready"**：
/// 早期的 `ready_wait`（已删除）对 `Pending` 直接 panic，等于把异步 provider 这条
/// 路堵死——将来任何真正慢的 provider（文件搜索、网络、进程间调用）都会在第一次
/// poll 就崩。现在流程拆成两段：先在引擎锁内取出本轮的 future（`'static`，不借
/// 引擎），出锁后逐个 await，最后再回锁聚合。
pub async fn run_query(
    raw: &str,
    active_plugin: Option<&str>,
    sub_mode: Option<&str>,
    picker: bool,
) -> QueryOutcome {
    // 插件选择列表：所有 enabled 插件的 `name + description`，按输入过滤。
    // 它不是 provider 查询，也没有提示行；与普通查询共用同一套渲染/选中路径。
    //
    // 【关键】picker 的过滤词是**剥掉 `#` 之后**的部分：`"#"` → `""`（列全部），
    // `"#b"` → `"b"`（过滤 base64）。传原文 `"#"` 会让 `name.contains("#")`
    // 永远 false —— 列表永远为空。
    if picker {
        let filter = raw
            .trim_start()
            .strip_prefix('#')
            .unwrap_or(raw)
            .trim_start();
        let rows = with_engine(|engine| engine.plugin_picker_matches(filter))
            .unwrap_or_default()
            .into_iter()
            .map(|m| RankedMatch {
                inner: m,
                final_score: 1.0,
            })
            .collect();
        return QueryOutcome {
            rows,
            hints: Vec::new(),
        };
    }

    let mut window_count: Option<usize> = None;

    // ── 第一段：在引擎锁内确定路由，并取出各 provider 的 future ──
    // future 是 `'static`（core 的 `Provider::query` 契约），可以被带出锁外 await：
    // 这样 provider 的计算不会占用引擎锁，也不会跨 await 持有 `MutexGuard`。
    let tasks: Vec<(String, voke_core::provider::BoxFuture<'static, Vec<Match>>)> =
        with_engine(|engine| {
            // **路由的唯一入口就是 `prepare_in_mode`**：它一起给出目标 provider
            // 列表与已剥前缀的 `QueryCtx`（`"=1+1"` → calculator + query `"1+1"`；
            // 激活插件 → 只查它 + 透传 sub_mode；无前缀/空输入 → 所有全局 provider）。
            let routing = engine.prepare_in_mode(raw, active_plugin, sub_mode);
            let ctx = &routing.ctx;
            // debug 级：每次按键都会跑这里（info 会刷屏）。验证路由时开
            // `RUST_LOG=voke=debug` 看这一行。
            if routing.route == RouteKind::Plugin {
                log::debug!(
                    "查询 {raw:?}（active={active_plugin:?}, sub={:?}）→ plugin={:?} stripped={:?}",
                    ctx.sub_mode,
                    routing.providers.iter().map(|p| p.id()).collect::<Vec<_>>(),
                    ctx.query
                );
            }

            // 按注册顺序派发，后面的 await 保持同一顺序 → 组间顺序仍是注册顺序。
            routing
                .providers
                .into_iter()
                .map(|provider| (provider.id().to_string(), provider.query(ctx)))
                .collect()
        })
        .unwrap_or_default();

    // ── 第二段：逐个 await ──
    // 内置 provider 目前都是同步算完用 `ready` 包起来的，这里的 await 一 poll 就过；
    // 将来的慢 provider 则由 background executor 真正调度。
    let mut batches: Vec<voke_core::ProviderBatch> = Vec::with_capacity(tasks.len());
    for (provider_id, future) in tasks {
        let matches: Vec<Match> = future.await;
        if provider_id == WindowsProvider::ID {
            // 非空查询时 provider 会截断，拿到的不是真实窗口总数；
            // 这个计数只用于空状态的提示语，量级正确即可。
            window_count = Some(window_count.unwrap_or(0).max(matches.len()));
        }
        batches.push(voke_core::ProviderBatch {
            provider_id,
            matches,
        });
    }

    let aggregated = with_engine(|engine| engine.aggregate(batches)).unwrap_or_default();
    // 本轮真跑过 windows provider 就如实回写（包括 0：窗口全关了）。
    if let Some(count) = window_count {
        note_window_count(count);
    }
    let rows = aggregated
        .groups
        .into_iter()
        .flat_map(|group| group.matches)
        .collect();
    QueryOutcome {
        rows,
        hints: aggregated.hints,
    }
}

/// **自检专用**：poll 一个 future 一次，就绪返回 `Some`，未就绪返回 `None`。
///
/// 自检跑在同步上下文里（没有 executor）。内置 provider 都是 `ready` 包出来的，
/// 一次 poll 必然完成；返回 `None` 的场景只可能是 provider 契约被改坏了，调用方
/// 退化成空结果，后续断言会如实报出 FAIL。
fn poll_once<F: std::future::Future>(future: F) -> Option<F::Output> {
    let mut future = Box::pin(future);
    let mut cx = std::task::Context::from_waker(std::task::Waker::noop());
    match future.as_mut().poll(&mut cx) {
        std::task::Poll::Ready(value) => Some(value),
        std::task::Poll::Pending => None,
    }
}

/// 自检专用：在**同步上下文**里跑一次查询（正常查询走 [`spawn_query`] 的异步链路）。
///
/// `VOKE_SELFTEST=1` 触发的进程内自检（本文件的 `routing_selftest` 与 `main` 的
/// awake 自检）没有 executor 可用，只能自己 poll [`run_query`] 返回的 future。
/// 这里用「noop waker + 有限次忙轮询」代替引入一个 runtime：内置 provider 都是
/// `ready` 包出来的，第一次 poll 就结束；万一某个 provider 真的 `Pending`，轮询到
/// 上限就放弃本轮（返回空结果 + 一条 error 日志），既不卡死也不 panic。
pub fn run_query_blocking(
    raw: &str,
    active_plugin: Option<&str>,
    sub_mode: Option<&str>,
    picker: bool,
) -> QueryOutcome {
    let mut future = Box::pin(run_query(raw, active_plugin, sub_mode, picker));
    // `run_query` 内部会逐个 await provider；这里没有 executor，只能反复 poll。
    // 内置 provider 一次就完成，上限只是防"契约改坏导致死循环"。
    for _ in 0..SELFTEST_POLL_LIMIT {
        if let Some(outcome) = poll_once(&mut future) {
            return outcome;
        }
        std::thread::yield_now();
    }
    log::error!("自检查询 {raw:?} 未能完成（有 provider 返回 Pending），本轮自检无结果");
    QueryOutcome::default()
}

/// 把 sys 层的条目转成 core 的类型。
///
/// 两边字段逐字对齐（见 `voke-core/src/apps.rs` 的 `AppEntry` 文档），
/// 所以转换是一行结构体字面量，没有映射逻辑。
pub fn to_core_entries(entries: Vec<voke_sys::AppEntry>) -> Vec<AppEntry> {
    entries
        .into_iter()
        .map(|e| AppEntry {
            name: e.name,
            path: e.path,
            icon: e.icon,
            args: e.args,
            working_dir: e.working_dir,
            app_id: e.app_id,
            lnk_path: e.lnk_path,
        })
        .collect()
}

/// 把 sys 层的窗口条目转成 core 的类型（同样是逐字段字面量）。
pub fn to_core_windows(entries: Vec<voke_sys::WindowEntry>) -> Vec<voke_core::WindowEntry> {
    entries
        .into_iter()
        .map(|e| voke_core::WindowEntry {
            hwnd: e.hwnd,
            title: e.title,
            process_name: e.process_name,
            process_path: e.process_path,
            icon: e.icon,
        })
        .collect()
}

/// 窗口尺寸：宽度固定，高度按"输入 + 提示条带 + N 行结果 + 底部提示行"算出来。
///
/// [`HINT_STRIP_HEIGHT`] 恒定计入：窗口高度建窗时定死，不预留的话有提示时
/// 结果区会被挤掉近一行。
pub fn window_size() -> Size<Pixels> {
    size(
        px(680.),
        ROW_HEIGHT * MAX_VISIBLE_ROWS + CHROME_HEIGHT + HINT_STRIP_HEIGHT,
    )
}

/// `"2023-11-15 06:13:20 +08:00"` 这种本地时间行（以 `+HH:MM` / `-HH:MM` 结尾）。
///
/// 只看**末尾 6 个字符**的形状，不用 `contains`——日期里的 `1970-01` 也含 `-0`，
/// 拿 `contains` 判会把 UTC 行误算成"本地行"。
fn has_tz_offset_suffix(title: &str) -> bool {
    let Some(tail) = title.get(title.len().saturating_sub(6)..) else {
        return false;
    };
    let b = tail.as_bytes();
    if b.len() != 6 || (b[0] != b'+' && b[0] != b'-') || b[3] != b':' {
        return false;
    }
    b[1].is_ascii_digit() && b[2].is_ascii_digit() && b[4].is_ascii_digit() && b[5].is_ascii_digit()
}

/// 方案 A 的**自检**：在真实进程里跑一遍 `prepare_in_mode → query → aggregate`。
///
/// 为什么需要它：Windows 上验证"前缀路由 / 子模式 / 分组"本来该靠键盘输入，但
/// `SendInput` 注入的字符进不了 gpui 输入框（唤起后注入，日志里一条查询都不发生
/// ——合成输入到不了焦点窗口的编辑队列，这是 M0 起就有的会话隔离限制）。
/// 于是把验证点下沉成进程内自检：用**真的** `ENGINE`（apps + windows + awake +
/// calculator + 4 个 `#` 插件）跑几条查询，断言路由、剥前缀、结果形态、
/// 分组顺序与 `interact` 类型。
///
/// 由环境变量 `VOKE_SELFTEST=1` 触发（默认不跑，不污染正常路径），
/// 结果写 info 日志（可在 voke.log 里查证），返回值是失败项数。
pub fn routing_selftest() -> usize {
    let mut failures = 0;

    // ── 1. 路由 + 剥前缀 + 剥子模式 ──
    // (原始输入, 激活插件, 子模式, 期望 provider id 列表, 期望剥前缀后的 query, 期望命中的标题)
    type Case = (
        &'static str,
        Option<&'static str>,
        Option<&'static str>,
        &'static [&'static str],
        &'static str,
        Option<&'static str>,
    );
    let cases: &[Case] = &[
        // calculator（`=`）：**仅在激活态下**剥前缀查（未激活的 `=` 输入在
        // app 层是全局搜索，引擎层不再被用来直进插件，这里测激活后的路径）
        (
            "=1+1",
            Some("calculator"),
            None,
            &["calculator"],
            "1+1",
            Some("2"),
        ),
        (
            "= 2*3+4",
            Some("calculator"),
            None,
            &["calculator"],
            "2*3+4",
            Some("10"),
        ),
        (
            "1+1",
            Some("calculator"),
            None,
            &["calculator"],
            "1+1",
            Some("2"),
        ),
        // 激活后前缀残留也要被剥掉（列表激活时会写入 `#b ` 形态）
        (
            "#1700000000",
            Some("timestamp"),
            None,
            &["timestamp"],
            "1700000000",
            None,
        ),
        ("#b Zm9v", Some("base64"), None, &["base64"], "Zm9v", None),
        ("#u", Some("uuid"), None, &["uuid"], "", None),
        ("#r 1-100", Some("random"), None, &["random"], "1-100", None),
        // 激活 + 子模式（base64 enc / dec）
        (
            "hello",
            Some("base64"),
            Some("enc"),
            &["base64"],
            "hello",
            Some("aGVsbG8="),
        ),
        (
            "aGVsbG8=",
            Some("base64"),
            Some("dec"),
            &["base64"],
            "aGVsbG8=",
            Some("hello"),
        ),
        // 激活插件、第一级（无子模式）：base64 出两条 EnterSubquery
        ("", Some("base64"), None, &["base64"], "", Some("加密")),
        // awake（无前缀，靠名字全局命中）
        ("", Some("awake"), None, &["awake"], "", Some("30 分钟")),
        ("30", Some("awake"), None, &["awake"], "30", Some("30 分钟")),
        // terminal（无前缀，激活后只查它；profile 列表依赖实机 settings.json，标题不断言）
        ("", Some("terminal"), None, &["terminal"], "", None),
        (
            "ubuntu",
            Some("terminal"),
            None,
            &["terminal"],
            "ubuntu",
            None,
        ),
        // workspaces（无前缀，激活后只查它；条目来自实机编辑器历史库，标题不断言）
        ("", Some("workspaces"), None, &["workspaces"], "", None),
        (
            "voke",
            Some("workspaces"),
            None,
            &["workspaces"],
            "voke",
            None,
        ),
        // Indirect（未激活，active=None 走引擎的前缀路由）：
        // `#` / `#b`（无空格）不路由；`# ` / `#b `（前缀 + 空格）才路由。
        (
            "#",
            None,
            None,
            &["apps", "windows", "awake", "terminal", "workspaces"],
            "",
            None,
        ),
        ("# ", None, None, &["timestamp"], "", None),
        ("#b Zm9v", None, None, &["base64"], "Zm9v", None),
    ];
    for (raw, active, sub, expected_providers, expected_query, expected_title) in cases {
        let Some((actual_providers, actual_query)) = with_engine(|engine| {
            let routing = engine.prepare_in_mode(raw, *active, *sub);
            (
                routing.providers.iter().map(|p| p.id()).collect::<Vec<_>>(),
                routing.ctx.query.clone(),
            )
        }) else {
            log::error!("自检：ENGINE 尚未注入，跳过");
            return 1;
        };

        let mut ok = actual_providers == *expected_providers && actual_query == *expected_query;
        if !ok {
            log::error!(
                "自检 FAIL {raw:?}（active={active:?}, sub={sub:?}）: providers={actual_providers:?} query={actual_query:?}（期望 {expected_providers:?} / {expected_query:?}）"
            );
        }

        if let Some(expected_title) = expected_title {
            let titles: Vec<String> = run_query_blocking(raw, *active, *sub, false)
                .rows
                .into_iter()
                .map(|m| m.inner.title)
                .collect();
            if !titles.iter().any(|t| t == expected_title) {
                log::error!("自检 FAIL {raw:?}: 结果 {titles:?}，期望含 {expected_title:?}");
                ok = false;
            }
        }

        if ok {
            log::info!(
                "自检 ok {raw:?}（active={active:?}, sub={sub:?}）: providers={actual_providers:?} query={actual_query:?}"
            );
        } else {
            failures += 1;
        }
    }

    // ── 2. 交互类型（方案 A 的核心不变式）──
    {
        let checks: &[(&str, Option<&str>, Option<&str>, usize, usize)] = &[
            // (raw, active, sub, 期望 Execute 条数, 期望 Hint 条数)
            //
            // base64 第一级：两条 EnterSubquery（既不是 Execute 也不是 Hint）
            ("", Some("base64"), None, 0, 0),
            // base64 enc 非空：一条 Execute（结果）
            ("hello", Some("base64"), Some("enc"), 1, 0),
            // base64 enc 空：一条 Hint
            ("", Some("base64"), Some("enc"), 0, 1),
            // base64 dec 失败：一条 Hint（而不是空列表）
            ("!!!", Some("base64"), Some("dec"), 0, 1),
            // timestamp 空：默认展示 now 的两条 Execute + 末尾一行用法 Hint
            ("", Some("timestamp"), None, 2, 1),
            // timestamp now：两条 Execute（秒 + 毫秒）
            ("now", Some("timestamp"), None, 2, 0),
            // timestamp 10 位：两条 Execute（本地 + UTC；偏移 0 时合并为 1 条，见第 6 节）
            ("1700000000", Some("timestamp"), None, 2, 0),
            // random 空：6 条 Execute + 1 条 Hint
            ("", Some("random"), None, 6, 1),
            // uuid 空：1 条 Execute + 1 条 Hint
            ("", Some("uuid"), None, 1, 1),
            // awake 未保持：5 条时长 Execute + 1 条 Hint
            ("", Some("awake"), None, 5, 1),
            // calculator 命中：1 条 Execute
            ("1+1", Some("calculator"), None, 1, 0),
        ];
        for (raw, active, sub, expected_execute, expected_hint) in checks {
            let outcome = run_query_blocking(raw, *active, *sub, false);
            let execute = outcome
                .rows
                .iter()
                .filter(|m| m.inner.interact == Interact::Execute)
                .count();
            let hints = outcome.hints.len();
            // 可选的 `rows` 里不允许出现 Hint（它们必须被 aggregate 抽走）。
            let leaked = outcome
                .rows
                .iter()
                .filter(|m| m.inner.interact.is_hint())
                .count();
            if execute != *expected_execute || hints != *expected_hint || leaked != 0 {
                log::error!(
                    "自检 FAIL 交互类型 {raw:?}（active={active:?}, sub={sub:?}）: Execute={execute}（期望 {expected_execute}）Hint={hints}（期望 {expected_hint}）rows 里混进 Hint={leaked}"
                );
                failures += 1;
            } else {
                log::info!(
                    "自检 ok 交互类型 {raw:?}（active={active:?}, sub={sub:?}）: Execute={execute} Hint={hints}"
                );
            }
        }

        // base64 第一级两条必须是 EnterSubquery，且标识正确。
        let outcome = run_query_blocking("", Some("base64"), None, false);
        let modes: Vec<&str> = outcome
            .rows
            .iter()
            .filter_map(|m| match &m.inner.interact {
                Interact::EnterSubquery(mode) => Some(mode.as_str()),
                _ => None,
            })
            .collect();
        if modes != vec!["enc", "dec"] {
            log::error!("自检 FAIL base64 第一级子模式应为 [enc, dec]，实际 {modes:?}");
            failures += 1;
        } else if outcome.rows.len() != 2 {
            log::error!(
                "自检 FAIL base64 第一级应恰好 2 条 EnterSubquery，实际 {} 条",
                outcome.rows.len()
            );
            failures += 1;
        } else {
            log::info!("自检 ok base64 第一级子模式: {modes:?}");
        }
    }

    // ── 3. 全局搜索：分组排序 + 非全局插件不得泄漏 ──
    {
        for raw in ["weixin", "chrome"] {
            let providers = with_engine(|engine| {
                engine
                    .prepare_in_mode(raw, None, None)
                    .providers
                    .iter()
                    .map(|p| p.id().to_string())
                    .collect::<Vec<String>>()
            })
            .unwrap_or_default();
            let leaked: Vec<String> = providers
                .iter()
                .filter(|id| {
                    ["calculator", "timestamp", "base64", "uuid", "random"].contains(&id.as_str())
                })
                .cloned()
                .collect();
            if !leaked.is_empty() {
                log::error!("自检 FAIL {raw:?}: 非全局插件不得参与全局结果 {leaked:?}");
                failures += 1;
            } else {
                log::info!("自检 ok {raw:?}: 全局 providers={providers:?}（不含非全局插件）");
            }
        }

        // 分组必须按注册顺序：apps 组永远在 windows 组之前（即使 windows 分更高）。
        let aggregated = with_engine(|engine| {
            let routing = engine.prepare_in_mode("e", None, None);
            let ctx = &routing.ctx;
            let batches: Vec<voke_core::ProviderBatch> = routing
                .providers
                .into_iter()
                .map(|p| voke_core::ProviderBatch {
                    provider_id: p.id().to_string(),
                    matches: poll_once(p.query(ctx)).unwrap_or_default(),
                })
                .collect();
            engine.aggregate(batches)
        });
        match aggregated {
            None => return 1,
            Some(aggregated) => {
                let group_ids: Vec<&str> = aggregated
                    .groups
                    .iter()
                    .map(|g| g.provider_id.as_str())
                    .collect();
                // 注册顺序是 apps → windows → awake → ...（只看前两个足够验证规则）。
                let expected_prefix: Vec<&str> = vec!["apps", "windows"];
                let actual_prefix: Vec<&str> = group_ids
                    .iter()
                    .take(expected_prefix.len())
                    .copied()
                    .collect();
                if actual_prefix != expected_prefix {
                    log::error!(
                        "自检 FAIL 分组顺序应为 {:?} 开头，实际 {group_ids:?}",
                        expected_prefix
                    );
                    failures += 1;
                } else if group_ids.windows(2).any(|w| w[0] == w[1]) {
                    log::error!("自检 FAIL 同一 provider 出现了多个分组: {group_ids:?}");
                    failures += 1;
                } else {
                    log::info!("自检 ok 分组顺序: {group_ids:?}");
                }
            }
        }
    }

    // ── 4. 占位符随模式变 ──
    {
        let checks: &[(Option<&str>, Option<&str>, &str)] = &[
            (None, None, voke_core::provider::GLOBAL_PLACEHOLDER),
            (Some("base64"), None, "选择 加密 或 解密"),
            (Some("base64"), Some("enc"), "输入要加密的内容"),
            (Some("base64"), Some("dec"), "输入要解密的 base64"),
            (Some("calculator"), None, "输入计算式，如 1+1"),
            (Some("terminal"), None, "输入配置名过滤，Enter 用 wt 启动"),
            (
                Some("workspaces"),
                None,
                "输入工作区名过滤，Enter 用对应编辑器打开",
            ),
        ];
        for (plugin, sub, expected) in checks {
            let actual =
                with_engine(|engine| engine.placeholder(*plugin, *sub)).unwrap_or_default();
            if actual != *expected {
                log::error!(
                    "自检 FAIL 占位符（{plugin:?}, {sub:?}）= {actual:?}，期望 {expected:?}"
                );
                failures += 1;
            } else {
                log::info!("自检 ok 占位符（{plugin:?}, {sub:?}）= {actual:?}");
            }
        }
    }

    // ── 5. `#` 插件列表：列出全部、按 name 过滤、激活动作携带前缀 ──
    {
        let all = with_engine(|engine| engine.plugin_picker_matches("")).unwrap_or_default();
        let titles: Vec<&str> = all.iter().map(|m| m.title.as_str()).collect();
        let expected_all = [
            "应用",
            "窗口",
            "保持唤醒",
            "终端配置",
            "工作区",
            "计算器",
            "时间戳",
            "Base64",
            "UUID",
            "随机值",
        ];
        if titles == expected_all {
            log::info!("自检 ok 插件列表（裸 #）: {titles:?}");
        } else {
            log::error!("自检 FAIL 插件列表（裸 #）: {titles:?}，期望 {expected_all:?}");
            failures += 1;
        }

        for m in &all {
            let ok = match &m.interact {
                Interact::EnterSubquery(id) => {
                    id == &m.provider_id
                        && matches!(&m.action, Action::ActivatePlugin { plugin_id, .. } if plugin_id == &m.provider_id)
                }
                _ => false,
            };
            if !ok {
                log::error!(
                    "自检 FAIL 插件列表条目 {:?}: interact={:?} action={:?}",
                    m.title,
                    m.interact,
                    m.action
                );
                failures += 1;
            }
        }
        if !all.is_empty() {
            log::info!(
                "自检 ok 插件列表条目交互类型（{} 条都是 EnterSubquery + ActivatePlugin）",
                all.len()
            );
        }

        // 过滤：`#b` → Base64；`#时间` → 时间戳；打错字 → 空。
        let got = |filter: &str| -> Vec<String> {
            with_engine(|engine| engine.plugin_picker_matches(filter))
                .unwrap_or_default()
                .iter()
                .map(|m| m.provider_id.clone())
                .collect()
        };
        // **别名过滤**（用户实测反馈的回归锚点）：`t`/`sj`/`time` 均能过滤出
        // 时间戳；`sj` 同时命中"时间戳"与"随机值"（时间/随机的拼音首字母同形）；
        // `b` 命中 Base64（它的 name 与别名都以 b 开头，且无其它插件冲突）。
        let got_b = got("b");
        if got_b == vec!["base64".to_string()] {
            log::info!("自检 ok 插件列表别名过滤 b: b→base64");
        } else {
            log::error!("自检 FAIL 插件列表别名过滤 b: {got_b:?}");
            failures += 1;
        }
        let got_t = got("t");
        let got_sj = got("sj");
        let got_time = got("time");
        if got_t.contains(&"timestamp".to_string())
            && got_time == vec!["timestamp".to_string()]
            && got_sj.contains(&"timestamp".to_string())
            && got_sj.contains(&"random".to_string())
        {
            log::info!(
                "自检 ok 插件列表别名过滤 t/sj/time: t→{got_t:?} sj→{got_sj:?} time→{got_time:?}"
            );
        } else {
            log::error!(
                "自检 FAIL 插件列表别名过滤 t/sj/time: t→{got_t:?} sj→{got_sj:?} time→{got_time:?}"
            );
            failures += 1;
        }
        if got("时间") == vec!["timestamp".to_string()] {
            log::info!("自检 ok 插件列表过滤 时间 → timestamp");
        } else {
            log::error!("自检 FAIL 插件列表过滤 时间: {:?}", got("时间"));
            failures += 1;
        }
        if !got("zzz").is_empty() {
            log::error!("自检 FAIL 插件列表过滤 zzz 应为空: {:?}", got("zzz"));
            failures += 1;
        } else {
            log::info!("自检 ok 插件列表过滤 zzz → 空");
        }

        // 列表选中后的完整链路：ActivatePlugin 携带的前缀 + 激活态查询。
        let (pid, prefix) = with_engine(|engine| {
            engine
                .plugin_picker_matches("base")
                .first()
                .map(|m| match &m.action {
                    Action::ActivatePlugin { plugin_id, prefix } => {
                        (plugin_id.clone(), prefix.clone())
                    }
                    _ => unreachable!(),
                })
        })
        .flatten()
        .expect("列表过滤 b64 应命中 base64");
        if pid == "base64" && prefix == "#b " {
            log::info!("自检 ok 列表选中 base64: prefix={prefix:?}");
        } else {
            log::error!("自检 FAIL 列表选中 base64: pid={pid} prefix={prefix:?}");
            failures += 1;
        }
        // 激活后第一级就是“加密/解密”两条（用户 `#` → b → Enter 的终点形态）。
        let titles: Vec<String> = run_query_blocking("", Some("base64"), None, false)
            .rows
            .into_iter()
            .map(|m| m.inner.title)
            .collect();
        if titles == vec!["加密", "解密"] {
            log::info!("自检 ok 激活 base64 第一级: {titles:?}");
        } else {
            log::error!("自检 FAIL 激活 base64 第一级: {titles:?}");
            failures += 1;
        }
    }

    // ── 6. timestamp 的输出形态（用户实测反馈后的新规格回归）──
    //
    // 10 位输入 → 恰好一单位（秒）：本地行在前（偏移非 0 时 2 条，偏移 0 时
    // 合并为 1 条），**每条副标题带原始 ts 值**；13 位同理由毫秒除以 1000。
    {
        let outcome = run_query_blocking("1700000000", Some("timestamp"), None, false);
        let rows = &outcome.rows;
        let titles: Vec<&str> = rows.iter().map(|m| m.inner.title.as_str()).collect();
        let local_rows = titles.iter().filter(|t| has_tz_offset_suffix(t)).count();
        let utc_rows = titles.iter().filter(|t| t.ends_with("UTC")).count();
        let subtitle_ok = rows
            .iter()
            .all(|m| m.inner.subtitle.contains("1700000000") && m.inner.subtitle.contains("按秒"));
        let shape_ok = (titles.len() == 2 && local_rows == 1 && utc_rows == 1)
            || (titles.len() == 1 && utc_rows == 1 && local_rows == 0);
        if shape_ok && subtitle_ok {
            log::info!("自检 ok timestamp 10 位形态: {titles:?}");
        } else {
            log::error!(
                "自检 FAIL timestamp 10 位形态: {titles:?}（本地行 {local_rows}，UTC 行 {utc_rows}，副标题带 ts={subtitle_ok}）"
            );
            failures += 1;
        }

        // 13 位 → 按毫秒（除 1000）解读，形态同上。
        let outcome = run_query_blocking("1700000000000", Some("timestamp"), None, false);
        let ms_ok = outcome.rows.iter().all(|m| {
            m.inner.subtitle.contains("1700000000000") && m.inner.subtitle.contains("按毫秒")
        }) && !outcome.rows.is_empty();
        if ms_ok {
            log::info!(
                "自检 ok timestamp 13 位（毫秒）形态: {:?}",
                outcome
                    .rows
                    .iter()
                    .map(|m| &m.inner.title)
                    .collect::<Vec<_>>()
            );
        } else {
            log::error!(
                "自检 FAIL timestamp 13 位（毫秒）形态: {:?}",
                outcome
                    .rows
                    .iter()
                    .map(|m| (&m.inner.title, &m.inner.subtitle))
                    .collect::<Vec<_>>()
            );
            failures += 1;
        }

        // 非法输入（乱码 / 非法日期 / 错误位数）：不出换算，只有 Hint。
        // 注：`2026-10-09` 一类**合法**日期串已由决策 4 支持（见下），
        // 这里换成越界日期继续验证"非法 → 仅 Hint"。
        for bad in ["abc", "2026-13-45", "170000000", "17000000000"] {
            let outcome = run_query_blocking(bad, Some("timestamp"), None, false);
            let hints_only = outcome.rows.is_empty() && outcome.hints.len() == 1;
            if hints_only {
                log::info!("自检 ok timestamp 非法输入 {bad:?} → 仅 Hint");
            } else {
                log::error!(
                    "自检 FAIL timestamp 非法输入 {bad:?}: rows={:?} hints={:?}",
                    outcome
                        .rows
                        .iter()
                        .map(|m| &m.inner.title)
                        .collect::<Vec<_>>(),
                    outcome.hints.iter().map(|m| &m.title).collect::<Vec<_>>()
                );
                failures += 1;
            }
        }

        // 日期串 → 时间戳（决策 4）：按本地时区解释，出按秒 / 按毫秒两行。
        // 具体 ts 值随本地偏移变化（实机 UTC+8），所以只断言**形态不变式**：
        // 两行都是纯数字、毫秒 = 秒 × 1000、副标题分别带「按秒」/「按毫秒」。
        let outcome = run_query_blocking("2026-10-10 12:30:45", Some("timestamp"), None, false);
        let rows = &outcome.rows;
        let date_ok = rows.len() == 2
            && rows
                .iter()
                .all(|m| m.inner.title.bytes().all(|b| b.is_ascii_digit()))
            && rows[1].inner.title == format!("{}000", rows[0].inner.title)
            && rows[0].inner.subtitle.contains("按秒")
            && rows[1].inner.subtitle.contains("按毫秒");
        if date_ok {
            log::info!(
                "自检 ok timestamp 日期串形态: {:?}",
                rows.iter().map(|m| &m.inner.title).collect::<Vec<_>>()
            );
        } else {
            log::error!(
                "自检 FAIL timestamp 日期串形态: {:?}",
                rows.iter()
                    .map(|m| (&m.inner.title, &m.inner.subtitle))
                    .collect::<Vec<_>>()
            );
            failures += 1;
        }
    }

    // ── 7. terminal 的候选形态（实机 settings.json 驱动）──
    //
    // profile 列表来自真实文件，条数/名字无法预言（没装 WT 就是 0 条），
    // 所以这里断言的是**形态不变式**：每条都是 Execute，动作都是
    // `wt.exe -p "<标题>"`，且参数里的 profile 名与标题一致。
    {
        let outcome = run_query_blocking("", Some("terminal"), None, false);
        let bad: Vec<&str> = outcome
            .rows
            .iter()
            .filter(|m| {
                m.inner.interact != Interact::Execute
                    || !matches!(
                        &m.inner.action,
                        Action::Launch { path, args, elevated, no_console, .. }
                            if !*elevated
                                && !*no_console
                                && path.ends_with("wt.exe")
                                && args.len() == 2
                                && args[0] == "-p"
                                && args[1] == m.inner.title
                    )
            })
            .map(|m| m.inner.title.as_str())
            .collect();
        let leaked_hints = outcome
            .rows
            .iter()
            .filter(|m| m.inner.interact.is_hint())
            .count();
        if bad.is_empty() && leaked_hints == 0 {
            log::info!(
                "自检 ok terminal 候选形态：{} 条 profile（全部 Execute + wt -p 启动）",
                outcome.rows.len()
            );
        } else {
            log::error!(
                "自检 FAIL terminal 候选形态: 形态错误 {bad:?}，rows 混入 Hint={leaked_hints}"
            );
            failures += 1;
        }

        // default 置顶：多于 1 条时第一条必须带「默认配置」标记或等于实机 default
        // ——无法预言实机 default，改为断言「带默认标记的行若存在则在第一行」。
        let default_pos = outcome
            .rows
            .iter()
            .position(|m| m.inner.subtitle.contains("默认配置"));
        if let Some(pos) = default_pos
            && pos != 0
        {
            log::error!("自检 FAIL terminal default 置顶: 默认配置在第 {pos} 行");
            failures += 1;
        }
    }

    // ── 8. workspaces 的候选形态（mock 编辑器历史驱动）──
    //
    // 真实条目来自实机编辑器库（装没装编辑器、记了多少条都不可预言），所以这里
    // **用 mock 数据另建一个只含 workspaces 的引擎**，断言的是
    // 「一条编辑器历史 → 标题 / 副标题标记 / 打开命令 flag」这条链路：
    //
    // | 形态 | 副标题 | 打开命令 |
    // |---|---|---|
    // | Zed 本地 | `Zed · 本地 · …` | `<zed.exe> <路径>`（**不带** `--wsl`）|
    // | Zed WSL | `Zed · WSL openSUSE-Tumbleweed · …` | `<zed.exe> --wsl testuser@openSUSE-Tumbleweed <路径>` |
    // | Zed 容器 | `Zed · 容器 abc123 · …` | 空参数 → Launch 路径本身（dispatcher → explorer）|
    // | VSCode 文件夹 | `VSCode · … · …` | `cmd.exe /c code.cmd --folder-uri <uri>`（`no_console=true` 隐藏控制台）|
    // | VSCode `.code-workspace` | `VSCode · … · …` | `cmd.exe /c code.cmd --file-uri <uri>`（同上）|
    //
    // Zed 的前两行同样带 `no_console=true`：`zed_command()` 解析到的 `bin\Zed.exe`
    // 是控制台子系统的 CLI（PE Subsystem = 3），不藏会闪控制台窗口。
    //
    // 单文件记录（`CBS.log` / `fileUri`）必须**不出现**在结果里。
    {
        const ZED_EXE: &str = r"C:\Users\testuser\AppData\Local\Programs\Zed\bin\Zed.exe";
        const CODE_CMD: &str =
            r"C:\Users\testuser\AppData\Local\Programs\Microsoft VS Code\bin\code.cmd";
        // 形态与本机 `%USERPROFILE%\.vscode-shared\sharedStorage\state.vscdb` 一致
        // （`%2B` = `+`，`%3A` = `:`）。`fileUri` 那条是真数据里就有的噪音。
        const VSCODE_JSON: &str = r#"{"entries":[
            {"folderUri":"vscode-remote://wsl%2Bopensuse-tumbleweed/home/testuser/remote-proj","remoteAuthority":"wsl+openSUSE-Tumbleweed"},
            {"folderUri":"file:///c%3A/Users/testuser/code/alpha","remoteAuthority":""},
            {"workspace":{"configPath":"file:///c%3A/Users/testuser/code/my.code-workspace"}},
            {"fileUri":"file:///c%3A/Users/testuser/Downloads/export_result.json"}
        ]}"#;

        // 条目由 core 的**纯函数**从原始编辑器行造出来（不是手摆 `WorkspaceEntry`），
        // 这样「SQL 行 / entries JSON → 解析 → 脏数据过滤 → 排序去重」这一段
        // 在自检里也真跑了一遍。
        let entries = voke_core::workspaces_from_history(
            &[
                voke_core::ZedWorkspaceRow {
                    paths: "/home/testuser/Work/projects/personal/voke".into(),
                    timestamp: "2026-10-08 09:41:31".into(),
                    remote: Some(voke_core::ZedRemoteRow {
                        kind: "wsl".into(),
                        user: Some("testuser".into()),
                        distro: Some("openSUSE-Tumbleweed".into()),
                        ..Default::default()
                    }),
                },
                voke_core::ZedWorkspaceRow {
                    paths: r"C:\Users\testuser\code\beta".into(),
                    timestamp: "2026-10-07 08:00:00".into(),
                    remote: None,
                },
                voke_core::ZedWorkspaceRow {
                    paths: "/workspaces/app".into(),
                    timestamp: "2026-10-06 00:00:00".into(),
                    remote: Some(voke_core::ZedRemoteRow {
                        kind: "dev-container".into(),
                        container_id: Some("abc123".into()),
                        ..Default::default()
                    }),
                },
                // 本机真实脏数据：单文件被记进最近工作区
                voke_core::ZedWorkspaceRow {
                    paths: r"C:\Windows\Logs\CBS\CBS.log".into(),
                    timestamp: "2026-10-08 03:49:18".into(),
                    remote: None,
                },
            ],
            &[voke_core::VsCodeRecentPayload {
                json: VSCODE_JSON.to_string(),
                // VSCode 的 entries 不带单条时间戳 → 空串（排序排最后，
                // 组内保持 entries 数组顺序 = 最近使用顺序）。
                file_timestamp: String::new(),
            }],
        );

        let engine =
            voke_core::QueryEngine::new(vec![Box::new(voke_core::WorkspaceProvider::new(
                std::sync::Arc::new(move || entries.clone()),
                Some(voke_core::EditorCommand {
                    exe: ZED_EXE.to_string(),
                    pre_args: Vec::new(),
                }),
                Some(voke_core::EditorCommand {
                    exe: "cmd.exe".to_string(),
                    pre_args: vec!["/c".to_string(), CODE_CMD.to_string()],
                }),
            ))]);
        let routing = engine.prepare_in_mode("", Some("workspaces"), None);
        let rows: Vec<voke_core::Match> = routing
            .providers
            .into_iter()
            .flat_map(|provider| {
                let matches = poll_once(provider.query(&routing.ctx)).unwrap_or_default();
                engine
                    .rank(provider.id(), provider.config().weight, matches)
                    .into_iter()
                    .map(|ranked| ranked.inner)
            })
            .collect();

        let zed = |args: &[&str]| {
            Action::launch_with_hidden(
                ZED_EXE.to_string(),
                args.iter().map(|arg| (*arg).to_string()).collect(),
            )
        };
        // 两个编辑器的 CLI 都是控制台子系统（VSCode 经 `cmd.exe /c code.cmd`、
        // Zed 起控制台子系统的 `bin\Zed.exe`），动作都带 `no_console`（dispatcher
        // 加 `CREATE_NO_WINDOW`），所以期望都用 `launch_with_hidden` 构造。
        let code = |flag: &str, uri: &str| {
            Action::launch_with_hidden(
                "cmd.exe".to_string(),
                vec![
                    "/c".to_string(),
                    CODE_CMD.to_string(),
                    flag.to_string(),
                    uri.to_string(),
                ],
            )
        };
        let expected: Vec<(&str, &str, Action)> = vec![
            (
                "voke",
                "Zed · WSL openSUSE-Tumbleweed · /home/testuser/Work/projects/personal/voke",
                zed(&[
                    "--wsl",
                    "testuser@openSUSE-Tumbleweed",
                    "/home/testuser/Work/projects/personal/voke",
                ]),
            ),
            (
                "beta",
                r"Zed · 本地 · C:\Users\testuser\code\beta",
                zed(&[r"C:\Users\testuser\code\beta"]),
            ),
            // 容器：Zed CLI 没有"打开已记录的容器路径"这个入口 → 退回资源管理器
            (
                "app",
                "Zed · 容器 abc123 · /workspaces/app",
                Action::launch("/workspaces/app"),
            ),
            (
                "remote-proj",
                "VSCode · WSL openSUSE-Tumbleweed · /home/testuser/remote-proj",
                code(
                    "--folder-uri",
                    "vscode-remote://wsl%2Bopensuse-tumbleweed/home/testuser/remote-proj",
                ),
            ),
            (
                "alpha",
                "VSCode · 本地 · C:/Users/testuser/code/alpha",
                code("--folder-uri", "file:///c%3A/Users/testuser/code/alpha"),
            ),
            (
                "my",
                "VSCode · 本地 · C:/Users/testuser/code/my.code-workspace",
                code(
                    "--file-uri",
                    "file:///c%3A/Users/testuser/code/my.code-workspace",
                ),
            ),
        ];
        let expected_refs: Vec<(&str, &str, &Action)> = expected
            .iter()
            .map(|(title, sub, action)| (*title, *sub, action))
            .collect();
        let actual: Vec<(&str, &str, &Action)> = rows
            .iter()
            .map(|m| (m.title.as_str(), m.subtitle.as_str(), &m.action))
            .collect();
        let not_execute: Vec<&str> = rows
            .iter()
            .filter(|m| m.interact != Interact::Execute)
            .map(|m| m.title.as_str())
            .collect();
        if actual == expected_refs && not_execute.is_empty() {
            log::info!(
                "自检 ok workspaces 候选形态：{} 条（标题 / 副标题标记 / 打开命令 flag 全对，脏数据已过滤）",
                rows.len()
            );
        } else {
            log::error!(
                "自检 FAIL workspaces 候选形态:\n  实际 {actual:?}\n  期望 {expected_refs:?}\n  非 Execute={not_execute:?}"
            );
            failures += 1;
        }
    }

    // ── 9. 实机数据源证据（真实 sys reader，只记录不判定）──
    //
    // §8 用 mock 数据验证了「行 → 条目 → 候选形态」链路；这一节让真实进程用
    // `voke_sys::workspace` 的**真实 reader** 读两个编辑器的历史库，把
    // 「库路径 → 条数 → 样本」打进日志/stdout，作为用户报的「搜不到结果」
    // 有没有修好的实机证据（Windows 上合成键盘输入进不了 gpui 输入框，
    // 人肉看列表替代不了日志证据）。
    //
    // 刻意**零断言**：编辑器卸载 / 换库位置 / 历史为空都是合法状态，不因此
    // 记失败——条数多少由人对照期望解读。sys 层把打开失败吞成空列表，所以
    // 路径存在性在这里显式报出来，避免「0 条」无因可查。
    #[cfg(windows)]
    workspace_realdata_report();

    if failures == 0 {
        log::info!(
            "自检全部通过：方案 A 前缀路由（=/#/#b/#u/#r）+ 子模式 + 分组聚合 + 交互类型 + terminal + workspaces + 实机数据源证据（§9 只记录不判定）"
        );
    } else {
        log::error!("自检失败项 = {failures}");
    }
    failures
}

/// §9 的实现：真实读取 + 如实记录。只在 Windows 上编译（sys 层的环境变量
/// 包装是 `#[cfg(windows)]`）。
///
/// 绝不 panic：环境变量缺失、路径不存在、库打不开都走降级文案——sys 层
/// 本来就把读取错误吞成空列表（读 `log::debug!`），所以这里负责把
/// 「库文件到底在不在」显式报出来，0 条时才有因可查。
///
/// stdout 可能没有有效句柄（GUI 子系统 / `Start-Process` 无重定向），
/// 用 `writeln!` + 丢弃 `Result`（`println!` 在写失败时会 panic，不能用）。
#[cfg(windows)]
fn workspace_realdata_report() {
    use std::io::Write as _;
    use std::path::Path;

    let mut lines: Vec<String> = Vec::new();

    // ── Zed：先报库文件定位（哪些通道库真实存在），再报行数 ──
    let zed_dir = std::env::var("LOCALAPPDATA")
        .ok()
        .map(|la| voke_sys::zed_db_dir(Path::new(&la)));
    match &zed_dir {
        None => lines.push("Zed：LOCALAPPDATA 未设置，无法定位历史库".into()),
        Some(dir) => {
            let paths = voke_sys::zed_db_paths(dir);
            if paths.is_empty() {
                lines.push(format!("Zed：{} 下没有可读的通道库", dir.display()));
            } else {
                for p in &paths {
                    lines.push(format!("Zed 历史库：{}", p.display()));
                }
            }
        }
    }
    let zed_rows = voke_sys::zed_workspaces();
    lines.push(format!("Zed 读到 {} 条 workspaces 行", zed_rows.len()));

    // ── VSCode：共享库在前（≥1.118），兜底库在后；不存在的库显式报出来 ──
    let vscode_paths = match (std::env::var("USERPROFILE"), std::env::var("APPDATA")) {
        (Ok(up), Ok(ad)) => Some(voke_sys::vscode_db_paths(Path::new(&up), Path::new(&ad))),
        _ => None,
    };
    match &vscode_paths {
        None => lines.push("VSCode：USERPROFILE / APPDATA 未设置，无法定位历史库".into()),
        Some(paths) => {
            for p in paths {
                if p.is_file() {
                    lines.push(format!("VSCode 历史库：{}", p.display()));
                } else {
                    lines.push(format!("VSCode 历史库不存在（跳过）：{}", p.display()));
                }
            }
        }
    }
    let vscode_payloads = voke_sys::vscode_workspaces();
    lines.push(format!(
        "VSCode 读到 {} 个历史库 payload（有 key 的库才算）",
        vscode_payloads.len()
    ));

    // ── 条目数：走 core 的纯函数（provider 真实用的那套过滤 / 去重），不另写解析 ──
    let zed_entries = voke_core::workspaces_from_history(&zed_rows, &[]);
    let vscode_entries = voke_core::workspaces_from_history(&[], &vscode_payloads);
    let entries = voke_core::workspaces_from_history(&zed_rows, &vscode_payloads);
    lines.push(format!(
        "core 过滤/去重后：Zed 条目 {}，VSCode 条目 {}，合并 {}（脏数据与跨库重复会被滤掉）",
        zed_entries.len(),
        vscode_entries.len(),
        entries.len(),
    ));
    lines.push("前 8 条样本（name · path · 记录时间）:".into());
    for e in entries.iter().take(8) {
        lines.push(format!("  {} · {} · {}", e.name, e.path, e.timestamp));
    }
    lines.push("完成：以上为实机真实读取结果（零断言，如实记录）".into());

    for line in lines {
        log::info!("自检 §9 {line}");
        let _ = writeln!(std::io::stdout(), "自检 §9 {line}");
    }
}
