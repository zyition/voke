//! voke —— launcher 入口。
//!
//! 窗口行为（M0 PoC 已验证 + 实测踩坑结论，见 docs/HISTORY.md）：
//! - 创建即隐藏（`show: false`），只有热键才唤起
//! - 唤起 = 显式 `ShowWindow(SW_SHOW)` + gpui `activate_window()` 抢前台
//!   （`activate_window` 的一次性 placement 被 take 后就不能再靠它显示窗口）。
//!   **ShowWindow 必须在不持 gpui App 借用时调用**（会同步泵消息，持借用时
//!   gpui 帧回调 re-enter 全部失败刷 ERROR，见 [`panel_toggle_win32`] 文档）
//! - 失焦 / Esc = `voke_sys::hide_window`（gpui 的 `App::hide()` 是空实现）
//!
//! 启动顺序：先把窗口建出来（UI 立刻可用），同时在后台线程枚举并解析开始菜单
//! `.lnk`（COM，几十到几百毫秒），解析完把数据注入搜索视图（见
//! [`search::set_providers`] 与 [`Shell::on_provider_ready`]）。
//!
//! M2 额外注入窗口 provider：数据源是**每次查询都重新 EnumWindows** 的闭包
//! （窗口状态实时变，不能缓存）。`EnumWindows` 是同步 OS 调用、几十到几百个窗口，
//! 所以它只在 `background_spawn` 驱动的查询路径上执行，永不碰 UI 线程。
//!
//! release 用 Windows GUI 子系统（无控制台）；debug 保留 console 便于本地看输出。
#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

mod badge;
mod config;
mod dispatcher;
mod icons;
mod search;
mod settings;

use std::sync::atomic::{AtomicBool, Ordering};

use gpui_kit::AsyncApp;
use gpui_kit::component::ActiveTheme as _;
use gpui_kit::{
    AnyWindowHandle, App, AppContext as _, Bounds, Context, Entity, Focusable as _, Global,
    IntoElement, ParentElement as _, Render, Styled as _, Subscription, Window, WindowBounds,
    WindowKind, WindowOptions, div,
};

/// 外壳级共享状态（`gpui` 全局）：热键句柄 + 设置窗口句柄。
///
/// **为什么不直接做成 [`Shell`] 的字段**：设置页只能在 `&mut App` 里工作——开
/// 窗口的命令可能来自托盘线程、`Ctrl+,`、托盘菜单，那些地方拿不到 `Entity<Shell>`；
/// 改完热键要立刻 `HotkeyHandle::set` 时同样没有 `Shell`。gpui 的 global 是唯一
/// 跨上下文可达的位置（`crate::config::VokeSettings` 也是这样挂的）。
#[derive(Default)]
pub struct ShellState {
    /// 已启动的热键线程句柄；设置页改键时用它当场重注册。
    #[cfg(windows)]
    pub hotkey: Option<voke_sys::HotkeyHandle>,
    /// 最近一次打开的设置窗口。设置窗口「关闭 = 程序化销毁」（见
    /// `settings::open`），关闭时立即置 `None`；这个句柄只在窗口存活期间有效。
    pub settings_window: Option<AnyWindowHandle>,
}

impl Global for ShellState {}

/// 面板是否已被唤起过一次：内存快照只在**首次**唤起时打，避免每次热键刷屏。
static PANEL_SHOWN_ONCE: AtomicBool = AtomicBool::new(false);

/// 应用列表扫描的**防抖窗口**。
///
/// 扫描改为「面板唤起时触发」而非周期轮询：零空闲开销，装完应用一唤起就可搜到。
/// 但连续唤起（例如快速开关面板）会带来一串重复请求，而枚举 + 图标是几百毫秒级
/// 的真实开销，所以扫描线程内记一个「上次完成时刻」，窗口内的请求直接忽略。
/// 60s 的取舍：用户装完应用后最迟下次唤起（通常远快于 60s）就会看到；同时把
/// 频繁唤起时的重扫压到一分钟后一次。启动首次索引不受此限（必须扫一次建缓存）。
const APPS_SCAN_DEBOUNCE: std::time::Duration = std::time::Duration::from_secs(60);

/// 面板唤起时投给 `voke-apps-index` 扫描线程的请求通道。
///
/// 为什么用进程级全局：两条唤起路径的上下文不同——热键/托盘走 [`toggle_panel`]
/// （只有 `&mut App`），点击任务栏图标走 `search` 的 `observe_window_activation`
/// （`SearchView` 里，拿不到 `Shell`）。挂成全局让两条路径都只调同一个
/// [`request_apps_scan`]，无需把 sender 一路透传。
/// 首轮索引由扫描线程自行执行，所以这里在 channel 建好后才有值（之前为 `None`）。
static APPS_SCAN_TX: std::sync::OnceLock<async_channel::Sender<()>> = std::sync::OnceLock::new();

/// 请求一次应用列表扫描（面板唤起时调用）。
///
/// 只负责非阻塞投递：`try_send` 在无界 channel 上不会阻塞 UI 线程；真正的防抖
/// （[`APPS_SCAN_DEBOUNCE`]）与签名比对都在扫描线程内做，所以重复请求是廉价且
/// 安全的。扫描线程尚未起好（`None`）时静默忽略。
pub(crate) fn request_apps_scan() {
    if let Some(tx) = APPS_SCAN_TX.get() {
        let _ = tx.try_send(());
    }
}

/// 托盘 / 热键要交给主线程做的事。
///
/// 两个来源统一成一条枚举 + 一条 async channel，主侧就只有一处消费循环：
/// 显隐、开设置、退出都是"回到 UI 线程再落地"的同一种需求。
enum ShellCommand {
    /// 切换搜索面板（热键与托盘「显示面板」共用一套 toggle）。
    TogglePanel,
    /// **无条件**显示并聚焦面板（不 toggle）。
    ///
    /// 用于「二次启动激活已有实例」：用户主动再启动一次就是想看到它，若此刻面板
    /// 恰在前台而被 toggle 隐藏就与意图相反，所以这条路径固定走 [`show_panel`]。
    ShowPanel,
    /// 显示面板并直达 awake 插件（托盘右键的「保持唤醒」状态条目）。
    OpenAwake,
    /// 打开设置窗口。
    OpenSettings,
    /// 系统深色 / 浅色主题发生变化（仅「跟随系统」时需要重切配色）。
    SystemThemeChanged,
    /// 退出进程。
    Quit,
}

/// 应用外壳：持有窗口内容与激活观察。
struct Shell {
    /// 搜索视图。构造时就建好（provider 数据稍后注入），这样第一帧就有输入框，
    /// 不会出现"窗口看起来是空的"。
    view: Entity<search::SearchView>,
    /// 本窗口的 HWND，热键唤起/隐藏都用它直接调 Win32。
    hwnd: Option<isize>,
    _subscriptions: Vec<Subscription>,
}

impl Shell {
    /// 拿本窗口的 HWND（热键 toggle / 失焦隐藏 / 启动前隐藏共用）。
    ///
    /// 注意 `Window` 的固有方法 `window_handle()` 返回 `AnyWindowHandle`，
    /// 会遮蔽 `HasWindowHandle` trait 方法，所以要写全限定语法。
    fn hwnd_of(window: &Window) -> Option<isize> {
        <Window as raw_window_handle::HasWindowHandle>::window_handle(window)
            .ok()
            .and_then(|handle| match handle.as_raw() {
                raw_window_handle::RawWindowHandle::Win32(win32) => Some(win32.hwnd.get()),
                _ => None,
            })
    }

    fn new(window: &mut Window, cx: &mut Context<Self>) -> Self {
        let hwnd = Self::hwnd_of(window);

        // #2：失焦即隐藏。不能用 `App::hide()`（gpui-pre-windows 空实现），
        // 直接拿 HWND 调 Win32 `ShowWindow(SW_HIDE)`。
        let activation = cx.observe_window_activation(window, |_this, window, _cx| {
            if !window.is_window_active()
                && let Some(hwnd) = Self::hwnd_of(window)
            {
                log::info!("窗口失焦 -> 隐藏");
                voke_sys::hide_window(hwnd);
            }
        });

        let view = cx.new(|cx| search::SearchView::new(hwnd, window, cx));

        Self {
            view,
            hwnd,
            _subscriptions: vec![activation],
        }
    }

    /// provider 数据就绪：让搜索视图重跑一次查询、并把占位符同步成全局文案。
    fn on_provider_ready(&mut self, cx: &mut Context<Self>) {
        self.view.update(cx, |view, cx| view.on_provider_ready(cx));
    }

    /// 强制重跑当前查询（应用列表热更新后调用，让面板立刻反映新数据）。
    ///
    /// 只在**数据真变了**时由索引线程的消费循环触发；面板重新显示（热键唤起）
    /// 不走这里——那件事不等于"用户修改了输入"（见 `search::SearchView::refresh`
    /// 的护栏：重掷类插件在热更新时也不重跑）。
    fn refresh(&mut self, cx: &mut Context<Self>) {
        self.view.update(cx, |view, cx| view.refresh(cx));
    }

    /// 托盘「保持唤醒」条目直达：让搜索视图按 id 激活 awake 插件。
    ///
    /// 需要 `Window`（激活路径会把焦点放回输入框），所以只能经 `handle.update`
    /// 拿到窗口时调用——与 `toggle_panel` 里 `activate_window` 同一种取窗方式。
    fn open_awake(&mut self, window: &mut Window, cx: &mut Context<Self>) {
        self.view
            .update(cx, |view, cx| view.activate_plugin("awake", window, cx));
    }

    /// 热键唤起窗口后：确保搜索视图的输入框拿到焦点（否则用户打的第一个字会丢）。
    ///
    /// 拿 `Window` 只能走 `update`（见 `main` 里的热键回调），所以那个回调直接
    /// 热键唤起窗口后：确保输入框拿到焦点（否则用户打的第一个字会丢）。
    ///
    /// 为什么聚焦的是**输入框自己的 focus handle**（而不是本 view 的）：
    /// `window.handle_input(...)` 只在 `InputBaseState::focus_handle` 已聚焦时
    /// 才注册文本输入处理器（`gpui-base` 的 `base/element.rs`），所以打字能进去
    /// 的前提就是它是焦点。
    fn search_input_handle(&self, cx: &App) -> gpui_kit::FocusHandle {
        self.view.read(cx).query_input_focus_handle(cx)
    }
}

impl Render for Shell {
    fn render(&mut self, window: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        // 窗口第一次渲染时聚焦输入框。不能在构造期聚焦：那时元素还没进入
        // 渲染树，`window.focus` 会被丢弃（gpui 的 focus 只对已注册的
        // focus handle 生效）。这里用 `contains_focused` 判断，避免每帧
        // 重复请求焦点——无条件 request 焦点会让用户无法用鼠标把焦点移开。
        let view = self.view.clone();
        let focus = view.read(cx).focus_handle(cx);
        if !focus.contains_focused(window, cx) {
            window.focus(&focus, cx);
        }

        div()
            .size_full()
            .flex()
            .flex_col()
            .bg(cx.theme().popover)
            .text_color(cx.theme().popover_foreground)
            .child(view)
    }
}

/// TogglePanel 的 **Win32 半段**：决策显隐并执行 `ShowWindow` / `HideWindow`。
///
/// 必须在**不持有 gpui App 借用**的上下文里调用（命令消费循环的 `cx.update`
/// 之外）：ShowWindow/HideWindow 是同步调用，会立刻泵一串窗口消息
/// （WM_SHOWWINDOW / WM_SIZE / WM_PAINT …），gpui-pre 的帧回调与尺寸回调就在
/// 这个泵里 re-enter，并经 `AsyncApp` 走 `AppCell::try_borrow_mut` 取 App——
/// 若此刻 App 的可变借用还在（旧实现把整段唤起塞在 `cx.update` 里），这些
/// 更新全部失败，每次唤起刷 4~6 条 ERROR「RefCell already borrowed」（来源：
/// gpui-pre `window.rs` 的 `on_request_frame` / `on_resize` 等 `.log_err()`）。
/// 提前到无借用处执行后，泵进来的 WM_PAINT 由 gpui 正常取到借用、正常画帧。
///
/// 返回 `true` 表示刚执行了唤起（显示），调用方需再跑 gpui 半段
/// [`show_panel`] 激活窗口；`false` 表示已隐藏或无需后续。
fn panel_toggle_win32(hwnd: isize) -> bool {
    if voke_sys::is_window_visible(hwnd) && voke_sys::is_foreground_window(hwnd) {
        log::info!("切换面板 -> 窗口在前台，隐藏");
        voke_sys::hide_window(hwnd);
        return false;
    }
    log::info!("切换面板 -> 唤起窗口");
    voke_sys::show_window(hwnd);
    true
}

/// **gpui 半段**唤起面板（抢前台 + 触发扫描），不做 toggle 判断。
///
/// 前置条件：Win32 侧的 `ShowWindow(SW_SHOW)` 已由 [`panel_toggle_win32`]（或
/// 消费循环的等价代码）在**不持 App 借用**时执行完毕。显式 ShowWindow 这一步
/// 不能省——`Window::activate_window()` 内部那个补 placement 的流程是一次性的，
/// 窗口被 `SW_HIDE` 之后它不会再显示窗口（详见模块头部踩坑结论与
/// docs/HISTORY.md）——只是挪到了消费循环的无借用阶段，原因见
/// [`panel_toggle_win32`] 的文档。
///
/// 托盘「保持唤醒」条目要的是"保证面板可见再进插件"——若走 toggle，面板恰好
/// 在前台时会被隐藏，插件就进不去了。故 Win32 显示对这条路径是**无条件**的。
fn show_panel(cx: &mut App, handle: AnyWindowHandle) {
    // 首次唤起时打一条内存快照（后续唤起不再刷屏）：用于区分「面板首次渲染
    // 带来的常驻增量」与「启动即常驻」。
    if !PANEL_SHOWN_ONCE.swap(true, Ordering::Relaxed) {
        voke_sys::log_snapshot("面板首次唤起");
    }
    if let Err(err) = handle.update(cx, |_root, window, _cx| window.activate_window()) {
        log::error!("唤起窗口失败: {err}");
    }
    // 唤起即触发应用列表扫描（异步、不阻塞渲染）：面板先渲染现有缓存，扫描
    // 完成后若签名变了才热替换。防抖在扫描线程内做，这里可放心重复投递。
    // （点击任务栏图标的另一条唤起路径在 `search::SearchView` 的窗口激活观察者里。）
    request_apps_scan();
}

/// 起热键线程：热键取自配置，句柄存进 [`ShellState`]，供设置页当场改键。
///
/// 配置里的热键串认不出来（比如用户手写了不存在的键名）时回落 PoC 的
/// Ctrl+Alt+Space——启动期宁可用一个能用的键，也不能不起热键。
#[cfg(windows)]
fn spawn_hotkey_thread(cx: &mut App, tx: async_channel::Sender<ShellCommand>) {
    let hotkey = cx
        .global::<crate::config::VokeSettings>()
        .hotkey_spec()
        .and_then(|spec| voke_sys::hotkey_from_spec(&spec))
        .unwrap_or_else(|| {
            log::warn!("配置里的热键无法注册，回落 Ctrl+Alt+Space");
            voke_sys::CTRL_ALT_SPACE
        });
    let handle = voke_sys::spawn_hotkey(
        hotkey,
        std::sync::Arc::new(move || {
            let _ = tx.try_send(ShellCommand::TogglePanel);
        }),
    );
    cx.global_mut::<ShellState>().hotkey = handle;
    log::info!("热键已注册，进入事件循环");
}

#[cfg(not(windows))]
fn spawn_hotkey_thread(_cx: &mut App, _tx: async_channel::Sender<ShellCommand>) {
    log::warn!("非 Windows 构建：没有全局热键");
}

/// 起托盘线程（仅 Windows）：图标是编译期内嵌的 `assets/voke-icon-final.png`。
///
/// 托盘自己有一条专用线程跑 Win32 消息循环（见 `voke-sys/src/tray.rs` 的模块
/// 文档），命令经 channel 回主线程。
#[cfg(windows)]
fn spawn_tray_thread(tx: async_channel::Sender<ShellCommand>) {
    // 图标是 2048² 的设计稿 PNG，解码 + 缩到 64² 会有一次几十 MB 的瞬时分配；
    // 前后各打一条快照，量化这次解码的峰值与它是否残留。
    voke_sys::log_snapshot("托盘图标解码前");
    let Some(icon) = voke_sys::default_icon() else {
        log::error!("托盘图标解码失败，跳过托盘");
        return;
    };
    voke_sys::log_snapshot("托盘图标解码后");
    voke_sys::spawn_tray(
        icon,
        std::sync::Arc::new(move |command| {
            let command = match command {
                voke_sys::TrayCommand::ShowPanel => ShellCommand::TogglePanel,
                voke_sys::TrayCommand::OpenAwake => ShellCommand::OpenAwake,
                voke_sys::TrayCommand::OpenSettings => ShellCommand::OpenSettings,
                voke_sys::TrayCommand::Quit => ShellCommand::Quit,
            };
            let _ = tx.try_send(command);
        }),
    );
}

#[cfg(not(windows))]
fn spawn_tray_thread(_tx: async_channel::Sender<ShellCommand>) {}

/// 起系统主题监听线程（仅 Windows）：低频轮询注册表，值变化时投递命令。
///
/// 选轮询而非 `WM_SETTINGCHANGE` 的理由见 `voke-sys/src/theme.rs` 模块文档
/// （消息窗口收不到广播消息）。回调只投递命令，真正的配色切换回主线程做
/// （与热键 / 托盘同一条 channel）。
fn spawn_theme_watcher_thread(tx: async_channel::Sender<ShellCommand>) {
    voke_sys::spawn_theme_watcher(std::sync::Arc::new(move || {
        let _ = tx.try_send(ShellCommand::SystemThemeChanged);
    }));
}

/// 启动日志里报一次自启动状态（注册表读取与 CWD 无关，自启动拉起时也准）。
#[cfg(windows)]
fn autostart_state() -> &'static str {
    if voke_sys::autostart::is_enabled() {
        "已开启"
    } else {
        "未开启"
    }
}

#[cfg(not(windows))]
fn autostart_state() -> &'static str {
    "当前构建不支持"
}

// 默认组件图标集之外、本应用额外用到的 Lucide 图标。
//
// `gpui_kit::assets::Assets`（默认资源源）只打包 `default-icons.txt` 里的一小组
// 图标，插件角标要用的 AppWindow / Calculator / Clock / Coffee / Dices /
// FingerprintPattern / Layers / LayoutGrid / LockKeyhole 都不在其中。这里用官方
// `icon_assets!` 宏把这几个单独嵌进来，再与默认 `Assets` 组合成 AppAssets——只
// 多出这几枚 SVG，避免换成 `AllAssets` 把整份 1800+ 枚图标都塞进二进制。
gpui_kit::assets::icon_assets!(
    ExtraIcons,
    [
        AppWindow,
        Calculator,
        Clock,
        Coffee,
        Dices,
        FingerprintPattern,
        Layers,
        LayoutGrid,
        LockKeyhole,
    ]
);

/// 应用资源源：先查额外图标，再回落默认组件图标。
struct AppAssets;

impl gpui_kit::AssetSource for AppAssets {
    fn load(&self, path: &str) -> gpui_kit::Result<Option<std::borrow::Cow<'static, [u8]>>> {
        if let Some(bytes) = gpui_kit::AssetSource::load(&ExtraIcons, path)? {
            return Ok(Some(bytes));
        }
        gpui_kit::AssetSource::load(&gpui_kit::assets::Assets, path)
    }

    fn list(&self, path: &str) -> gpui_kit::Result<Vec<gpui_kit::SharedString>> {
        let mut paths = gpui_kit::AssetSource::list(&gpui_kit::assets::Assets, path)?;
        paths.extend(gpui_kit::AssetSource::list(&ExtraIcons, path)?);
        paths.sort();
        paths.dedup();
        Ok(paths)
    }
}

fn main() {
    // 单实例保护放在**日志初始化之前**：二次启动若先 init_logging，那个
    // `File::create` 会把首实例正在写的 voke.log 截断。已有实例时这里直接发信号
    // 并退出，连日志都不碰（代价是二次启动本身不留日志，可接受）。
    #[cfg(windows)]
    let show_signal = match voke_sys::single_instance_guard() {
        voke_sys::SingleInstance::Primary(signal) => signal,
        voke_sys::SingleInstance::AlreadyRunning => {
            // 已向首实例发出「显示面板」信号，本进程的使命结束。
            return;
        }
    };

    // Windows GUI 子系统下 stderr 不可见，日志写文件便于事后查看。
    init_logging();
    log::info!("voke 启动，PID={}", std::process::id());

    // 启动早期纠正自身工作目录：开机自启（`HKCU\…\Run`）不指定工作目录，进程会
    // 继承到 `C:\Windows\System32`；任何继承 cwd 启动的子进程都会把它当起始目录，
    // 语义不合理，也曾是排查终端 profile 启动异常时的可疑变量。必须在日志初始化
    // 之后（本函数打的 info 才进日志）、且在任何相对路径被解析之前调用；用户从
    // 终端 / 资源管理器显式启动时的 cwd 是本人意图，这里不会动它。
    voke_sys::fix_own_cwd();
    // 第一基线：日志系统之外几乎还没分配任何东西。
    voke_sys::log_snapshot("进程启动");

    // panic 写进日志（Windows 下无终端可见）。
    std::panic::set_hook(Box::new(|info| {
        log::error!("panic: {info}");
    }));

    gpui_kit::application()
        .with_assets(AppAssets)
        .run(move |cx: &mut App| {
            gpui_kit::init(cx);
            cx.set_global(ShellState::default());
            // 配置要在建搜索面板**之前**挂好全局：面板第一帧就要按配置行事。
            // 此时 provider 还没注入，`search::apply_config` 会静默跳过，所以
            // provider 就绪后（下面）还要再补一次 `config::apply_current`。
            crate::config::init(cx);
            log::info!("开机自启：{}", autostart_state());
            log::info!("gpui 初始化完成，创建窗口");

            let options = WindowOptions {
                window_bounds: Some(WindowBounds::Windowed(Bounds::centered(
                    None,
                    // 窗口尺寸与结果区行数对齐（行高 × 可见行数 + 输入/提示高度）。
                    search::window_size(),
                    cx,
                ))),
                titlebar: None,          // 无系统标题栏
                kind: WindowKind::PopUp, // 置顶弹出形态（M0 #3）
                show: false,             // 创建即隐藏，等热键唤起
                ..Default::default()
            };

            let (handle, shell) = gpui_kit::open_window(options, cx, |window, cx| {
                cx.new(|cx| Shell::new(window, cx))
            })
            .expect("创建窗口失败");
            log::info!("窗口创建完成（show: false）");
            voke_sys::log_snapshot("窗口创建完成");

            // 全局 window-closed 观察者，保留用于诊断（7899651 加的内存快照）。
            //
            // 设置窗口已改成「关闭 = 程序化销毁」（见 `settings::open`），所以
            // 每关一次设置窗口这里都会触发一次、打出一条「窗口已关闭」内存快照
            // ——正好用来验证关闭后常驻内存回落到面板基线（真机 20 轮验证清单的
            // 依据之一）。搜索面板窗只隐藏不销毁，不会触发本条。
            // `Subscription` 必须留着才持续有效——detach 让它存活到进程结束。
            //
            // **注意时序**：本回调在 gpui `update_window_erased` 的 trail 摘除
            // （app.rs:3055）里同步触发，此刻平台窗的 `Drop` 任务（RevokeDragDrop
            // → DestroyWindow → DirectXRenderer/纹理随 WindowsWindowInner 释放，
            // gpui-pre-windows window.rs:610-631）**尚未执行**，快照里还含着整套
            // renderer（约 26.5MB）——这条曲线「不降」不代表泄漏。所以下面再补
            // +2s / +10s 两个延迟快照：renderer 释放走的是主线程执行器的异步任务，
            // 毫秒级完成后 +2s 必然已落下，回落应在这两条里显形。
            cx.on_window_closed(|cx, _id| {
                voke_sys::log_snapshot("窗口已关闭");
                let executor = cx.background_executor().clone();
                let timer_executor = executor.clone();
                executor
                    .spawn(async move {
                        timer_executor
                            .timer(std::time::Duration::from_secs(2))
                            .await;
                        voke_sys::log_snapshot("窗口已关闭 +2s（renderer 应已随 Drop 任务释放）");
                        timer_executor
                            .timer(std::time::Duration::from_secs(8))
                            .await;
                        voke_sys::log_snapshot("窗口已关闭 +10s（观察残余是否继续回落）");
                    })
                    .detach();
            })
            .detach();

            let hwnd: Option<isize> = shell.read(cx).hwnd;
            let shell_handle = shell.clone();
            // 命令消费循环里「直达 awake」也要拿到 Shell 实体（它被下面的 apps
            // 消费循环 `move` 走了），所以这里再留一份克隆。
            let shell_handle_cmd = shell.clone();

            // 托盘命令与热键共用一条 channel：两者都是"别的线程 -> 主线程"的同一种
            // 需求，合并后只需要一处 `cx.spawn` 消费循环。
            let (cmd_tx, cmd_rx) = async_channel::unbounded::<ShellCommand>();

            // 单实例：首实例起一条线程循环等「二次启动」信号，收到就**无条件**显示
            // 并聚焦面板（不是 toggle——用户主动再启动一次就是想看到它）。回调跑在
            // 等待线程上，只投递命令回主线程（与热键/托盘同一条 channel）。
            #[cfg(windows)]
            {
                let tx = cmd_tx.clone();
                show_signal.spawn_waiter(std::sync::Arc::new(move || {
                    let _ = tx.try_send(ShellCommand::ShowPanel);
                }));
            }

            // #1：热键线程 -> gpui 主线程的桥。热键线程只投递命令，
            // 真正的 UI 操作回到主线程做。
            //
            // 热键取自配置（`config::init` 已经把热键串解析成 spec），落到具体线程的
            // 句柄存进 [`ShellState`]：设置页改键时拿它调 `set` 就能立刻换键。
            spawn_hotkey_thread(cx, cmd_tx.clone());

            // 托盘：Windows 下才有（tray-icon 是 target 门控依赖）。它的消息循环跑在
            // 自己的线程上，命令同样经 channel 回主线程。
            spawn_tray_thread(cmd_tx.clone());

            // 系统主题监听：低频轮询注册表，系统深色 / 浅色变化时经同一条 channel
            // 回主线程重切配色（仅「跟随系统」模式真正生效）。
            spawn_theme_watcher_thread(cmd_tx);

            cx.spawn(move |cx: &mut AsyncApp| {
                let cx = cx.clone();
                async move {
                    while let Ok(command) = cmd_rx.recv().await {
                        // ── 阶段一：Win32 显隐（**必须在不持 App 借用时执行**）──
                        // ShowWindow/HideWindow 同步泵窗口消息，gpui 的帧/尺寸回调
                        // 在泵里 re-enter 并经 AsyncApp `try_borrow_mut` 取 App；若
                        // 持着 `cx.update` 的可变借用，这些更新必然失败，每次唤起刷
                        // 4~6 条 ERROR「RefCell already borrowed」（详见
                        // [`panel_toggle_win32`] 文档）。显隐动作故此提到 `cx.update`
                        // 外面做：泵消息时 App 无借用，re-enter 的帧回调正常完成。
                        let wake_hwnd = match command {
                            ShellCommand::TogglePanel => match hwnd {
                                Some(h) => panel_toggle_win32(h).then_some(h),
                                None => {
                                    log::error!("无 HWND，无法切换面板");
                                    None
                                }
                            },
                            // 二次启动激活 / 托盘直达保持唤醒：固定显示，不做 toggle。
                            ShellCommand::ShowPanel | ShellCommand::OpenAwake => match hwnd {
                                Some(h) => {
                                    voke_sys::show_window(h);
                                    Some(h)
                                }
                                None => {
                                    if matches!(command, ShellCommand::ShowPanel) {
                                        log::error!("无 HWND，无法响应二次启动激活");
                                    } else {
                                        log::error!("无 HWND，无法直达保持唤醒插件");
                                    }
                                    None
                                }
                            },
                            _ => None,
                        };

                        // ── 阶段二：gpui 侧操作（UI 操作必须回主线程）──
                        // `ShellCommand` 是从热键线程 / 托盘线程 / 主题监听线程投过来的。
                        cx.update(|cx: &mut App| match command {
                            ShellCommand::TogglePanel | ShellCommand::ShowPanel => {
                                if wake_hwnd.is_some() {
                                    show_panel(cx, handle);
                                }
                            }
                            ShellCommand::OpenAwake => {
                                // 先显示面板（不做 toggle，保证可见，阶段一已完成
                                // Win32 显示），再激活插件：激活路径依赖面板可见时机，
                                // 与热键唤起后重查同一套时序。
                                if wake_hwnd.is_some() {
                                    show_panel(cx, handle);
                                }
                                // `handle.update` 的根是类型擦除的 `AnyView`，拿不到
                                // Shell 的方法；用留存的实体句柄再 update 一层拿到 `Shell`，
                                // 同时借用外层给的 `window`（激活路径要把焦点放回输入框）。
                                let view = shell_handle_cmd.clone();
                                if let Err(err) = handle.update(cx, |_root, window, cx| {
                                    view.update(cx, |shell, cx| shell.open_awake(window, cx));
                                }) {
                                    log::error!("直达保持唤醒插件失败: {err}");
                                }
                            }
                            ShellCommand::OpenSettings => crate::settings::open(cx),
                            ShellCommand::SystemThemeChanged => {
                                crate::config::on_system_theme_changed(cx)
                            }
                            ShellCommand::Quit => cx.quit(),
                        });
                    }
                }
            })
            .detach();

            // 开始菜单索引：**常驻后台线程**（COM 解析 + 图标提取不能做在 UI 线程）。
            //
            // **触发模型：启动即索引一次 + 面板唤起时触发重扫 + 扫描线程内 60s 防抖**
            // （用户拍板，替代原 30s 周期轮询）：
            // - voke 启动后在后台索引一次（不等第一次拉起面板），建好初始缓存；
            // - 之后**没有后台定时器**：只有面板被唤起（热键/托盘走
            //   [`panel_toggle_win32`] + [`show_panel`]、
            //   点击任务栏图标走 `search` 的窗口激活观察者）时才经 [`request_apps_scan`]
            //   投一个请求；
            // - 扫描线程记「上次完成时刻」，[`APPS_SCAN_DEBOUNCE`] 内的重复请求直接
            //   忽略——避免连续开关面板把几百毫秒的枚举+图标调用打成一串。
            // 零空闲开销，且装完应用一唤起即可见：扫描异步跑，面板先渲染现有缓存，
            // 完成后签名比对、变了才热替换。数据是 `Vec<AppEntry>`（纯 String），
            // 经 async channel 跨线程安全地回主线程。
            //
            // **为什么不引入文件系统 watcher**（调研结论，见 docs/BACKLOG.md）：
            // 1. Store/MSIX 应用**没有 `.lnk`**，只存在于 `Shell:AppsFolder`
            //    （Windows Terminal 就是典型）——只监听开始菜单目录的 watcher 抓不到
            //    它们的安装/卸载，仍得靠重扫覆盖全部来源；
            // 2. `ReadDirectoryChangesW`（已核对 windows-0.62.2 签名 + MS 文档）在改动
            //    突发时会因缓冲区不足**失败并返回 `ERROR_NOTIFY_ENUM_DIR`**，MS 文档
            //    明示此时应"整目录重扫"——等于仍要写重扫 + 防抖 + 缓冲区解析的 unsafe；
            // 3. `SHChangeNotifyRegister` 要求一个跑消息循环的窗口，且对"直接往开始菜单
            //    目录写文件"的事件覆盖不可靠。
            // 枚举本身毫秒级，"唤起触发"已足够及时，纯 std、零新依赖。
            let (apps_tx, apps_rx) = async_channel::unbounded::<Vec<voke_core::AppEntry>>();
            // 唤起路径 -> 扫描线程的请求通道；sender 挂全局供两条唤起路径调用。
            let (scan_tx, scan_rx) = async_channel::unbounded::<()>();
            let _ = APPS_SCAN_TX.set(scan_tx);
            std::thread::Builder::new()
                .name("voke-apps-index".into())
                .spawn(move || {
                    // 扫描一次：枚举 + 图标 + 签名比对，集合真变了才推送。
                    // 返回 `false` = channel 已关闭（进程退出），线程该结束。
                    let run_scan = |last_sig: &mut Option<Vec<(String, String)>>| -> bool {
                        // 一次拿全：开始菜单 `.lnk` + `Shell:AppsFolder`（MSIX/Store）。
                        // 后者是 Windows Terminal 这类应用的**唯一**来源（没有 .lnk）。
                        let mut entries = voke_sys::apps::load_all_apps();
                        // 图标提取也在后台做（上百次 COM 位图操作，别拖慢第一帧）。
                        // `extract_icon_png` 自带落盘缓存：已缓存条目只做一次文件
                        // 存在性检查，重扫时不重复解码位图。
                        icons::prepare_icons(&mut entries);
                        let core_entries = search::to_core_entries(entries);
                        let sig = search::apps_signature(&core_entries);
                        // 只在条目集合真变了（名字/路径变化）时才推送，避免无谓地
                        // 重建 provider 索引 + 刷新面板。
                        if last_sig.as_ref() != Some(&sig) {
                            log::info!(
                                "应用索引更新：{} 条（{}）",
                                core_entries.len(),
                                if last_sig.is_none() {
                                    "启动首次"
                                } else {
                                    "唤起触发重扫后热更新"
                                }
                            );
                            // channel 关闭（进程退出）时发送失败：结束线程。
                            if apps_tx.send_blocking(core_entries).is_err() {
                                return false;
                            }
                            *last_sig = Some(sig);
                        }
                        true
                    };

                    // 启动首次索引：必须扫一次建缓存（不受防抖限制）。
                    let mut last_sig: Option<Vec<(String, String)>> = None;
                    if !run_scan(&mut last_sig) {
                        return;
                    }
                    let mut last_scan_at = std::time::Instant::now();

                    // 之后只由唤起请求驱动；防抖窗口内的重复请求直接丢弃。
                    while scan_rx.recv_blocking().is_ok() {
                        if last_scan_at.elapsed() < APPS_SCAN_DEBOUNCE {
                            continue;
                        }
                        if !run_scan(&mut last_sig) {
                            break;
                        }
                        last_scan_at = std::time::Instant::now();
                    }
                })
                .expect("创建应用索引线程失败");

            // M2：窗口 provider 的数据源。**每次查询都重新枚举**——
            // 窗口开关/改名/切前台每秒都在发生，缓存即出错（见 voke-core 的 windows 模块文档）。
            //
            // 闭包在 **background executor** 上被调用（`spawn_query` 用
            // `background_spawn` 驱动整次查询），所以这里直接调 `EnumWindows` 是安全的：
            // 它不会跑在 UI 线程上。
            //
            // 图标提取只在**首次**枚举时走过 IShellItemImageFactory 的落盘缓存，
            // 后续命中同一批 PNG（缓存键 = 进程名 + 路径哈希），开销可忽略；
            // 所以每轮都提一次是划算的——新启动的进程也能拿到图标。
            let window_fetch: voke_core::WindowFetcher = std::sync::Arc::new(|| {
                let mut windows = voke_sys::enum_windows();
                // debug 级：每次查询（每次按键）都会调这里，info 会刷屏；
                // 排查"窗口没出现/少出现"时用 RUST_LOG=voke=debug 打开。
                log::debug!("窗口 provider 查询：枚举到 {} 个可切换窗口", windows.len());
                icons::prepare_window_icons(&mut windows);
                search::to_core_windows(windows)
            });

            cx.spawn(move |cx: &mut AsyncApp| {
                let cx = cx.clone();
                async move {
                    if let Ok(entries) = apps_rx.recv().await {
                        log::info!("收到 {} 条应用条目，注入 provider", entries.len());
                        // 写进查询用的全局引擎（查询跑在 background executor，
                        // 拿不到 gpui 上下文）。
                        // M5 起一次注册全部 8 个 provider（apps / windows /
                        // awake / timestamp / base64 / uuid / random / calculator）。
                        // 时区偏移注入这里：非 Windows 上 `local_offset_seconds`
                        // 不在 lib.rs 导出，所以用一个恒返 0 的闭包兑底
                        // （timestamp 会显示成 UTC，这是非 Windows 构建的预期行为）。
                        #[cfg(windows)]
                        let offset: std::sync::Arc<
                            dyn Fn() -> i32 + Send + Sync,
                        > = std::sync::Arc::new(voke_sys::local_offset_seconds);
                        #[cfg(not(windows))]
                        let offset: std::sync::Arc<
                            dyn Fn() -> i32 + Send + Sync,
                        > = std::sync::Arc::new(|| 0);
                        search::set_providers(entries, Some(window_fetch), offset);
                        // 应用索引（含拼音表）已建好：量化「引擎索引」这一步的常驻增量。
                        voke_sys::log_snapshot("引擎索引完成");
                        // provider 这才注入完：把读到的配置（各插件的 enabled /
                        // is_global / weight / prefix）真正下发一次。启动时
                        // `config::init` 那一次因为 `ENGINE` 还没建而落了空。
                        cx.update(|cx: &mut App| crate::config::apply_current(cx));
                        // M3/M5 路由/面板自检（`VOKE_SELFTEST=1` 时跑）：Windows 上
                        // 合成键盘输入进不了 gpui 输入框，所以路由验证下沉到进程内。
                        if selftest_requested() {
                            let failures = search::routing_selftest();
                            log::info!("路由/面板自检结束，失败项 = {failures}");
                            // 剪贴板与保持唤醒自检要用真实的 gpui `App`，
                            // 所以放在 `cx.update` 里。
                            cx.update(|cx: &mut App| {
                                let clipboard_failures = clipboard_selftest(cx);
                                log::info!("剪贴板自检结束，失败项 = {clipboard_failures}");
                                let awake_failures = awake_selftest(cx);
                                log::info!("保持唤醒自检结束，失败项 = {awake_failures}");
                            });
                        }
                        cx.update(|cx: &mut App| {
                            shell_handle.update(cx, |shell, cx| {
                                shell.on_provider_ready(cx);
                            });
                        });
                        // provider 数据注入 + 面板就绪：稳态前的最后一条快照。
                        voke_sys::log_snapshot("provider 就绪");
                    }

                    // 首轮之后：安装 / 卸载应用的**增量热更新**。索引线程只在
                    // 条目集合真变了时才推送，所以这里不会空转。
                    while let Ok(entries) = apps_rx.recv().await {
                        log::info!("应用列表热更新：{} 条，就地替换 provider", entries.len());
                        cx.update(|cx: &mut App| {
                            search::update_apps(entries);
                            // 新 provider 带的是代码默认配置：替换后立刻把用户配置
                            // 再下发一次，否则改过的 enabled / weight 等会丢。
                            crate::config::apply_current(cx);
                            // 面板此刻可能正开着：重跑当前查询，让新应用/消失的应用
                            // 立刻反映到列表里。
                            shell_handle.update(cx, |shell, cx| shell.refresh(cx));
                        });
                    }
                }
            })
            .detach();

            // run 闭包即将把控制权交给事件循环：所有常驻线程/任务都已拉起。
            voke_sys::log_snapshot("启动完成");
        });
}

/// 是否要求跑启动自检（`VOKE_SELFTEST=1`）。
///
/// 用环境变量而不是命令行参数：`Start-Process -WorkingDirectory` 已经在用
/// 环境变量传 `RUST_LOG`，多一个同类开关不增加启动路径的复杂度。
fn selftest_requested() -> bool {
    matches!(std::env::var("VOKE_SELFTEST").ok().as_deref(), Some("1"))
}

/// M3 剪贴板自检：走 [`dispatcher::execute`] 的真实 `Copy` 路径 + 真实 gpui 剪贴板。
///
/// 这一步能验证 dispatcher 的 `Host` 生产实现（`impl Host for gpui_kit::App`）
/// 真的把文本写进了系统剪贴板——单测里的 `FakeHost` 只能证明"调了 copy"，
/// 证明不了 gpui 的 `write_to_clipboard` 在 Windows 上能用。
///
/// **为什么要重试读回**：gpui-pre-windows 的读路径是
/// `ClipboardGuard::open()`（`OpenClipboard`）+ 枚举格式，而剪贴板是**独占**资源
/// ——任何剪贴板管理器/输入法/聊天软件都可能在同一瞬间持有它，此时
/// `OpenClipboard` 失败、读回拿到 `None`。实测就撞上过这个 flake（写入日志正常、
/// 读回 None）。写入是同步的（`write_to_clipboard` 在同一线程内
/// `EmptyClipboard` + `SetClipboardData`），所以重试读回是安全的。
///
/// `hwnd` 传 `None`：自检不该把窗口隐掉。
fn clipboard_selftest(cx: &mut App) -> usize {
    let expected = "voke-m3-selftest-42";
    let handled = dispatcher::execute(
        &voke_core::Action::Copy {
            text: expected.to_string(),
        },
        None,
        &*cx,
    );
    if !handled {
        log::error!("剪贴板自检 FAIL：dispatcher 报告 Copy 未处理");
        return 1;
    }
    // 写→读回：`ClipboardItem::text()` 把所有 String 条目拼起来。
    for attempt in 1..=10 {
        match cx.read_from_clipboard().and_then(|item| item.text()) {
            Some(text) if text == expected => {
                log::info!("剪贴板自检 ok：写入并读回 {text:?}（第 {attempt} 次尝试）");
                return 0;
            }
            other => {
                if attempt == 10 {
                    log::error!("剪贴板自检 FAIL：读回 {other:?}，期望 {expected:?}");
                    return 1;
                }
                // 剪贴板被别人占着的概率性情况：等一下再试。
                std::thread::sleep(std::time::Duration::from_millis(150));
            }
        }
    }
    1
}

/// M5 保持唤醒自检：真实走上 `SetThreadExecutionState` 专用线程 + 状态回读。
///
/// 这是 awake 插件唯一能自动化验证的部分（合成输入进不了输入框，见
/// `routing_selftest` 的文档）。验证链：
///
/// 1. `dispatcher::execute(SetAwake{30})` 返回 `true`（自检传 `hwnd=None`，不触发隐藏）
/// 2. `voke_sys::current_awake()` 读到状态，且到期时间 ≈ now + 30 分钟、不保持屏幕
/// 3. 引擎查 `awake` 时提示区出现「保持唤醒中」、可执行项里有「取消保持唤醒」
/// 4. `dispatcher::execute(CancelAwake)` 后状态回到 `None`
/// 5. 再查 `awake`：回到时长候选列表（「30 分钟」）
///
/// 在非 Windows 构建上 voke-sys 的 awake 是占位实现（只记状态、不真的阻止休眠），
/// 所以这条自检在 Windows 上才有完整意义——它本来也只在 Windows 上跑。
fn awake_selftest(cx: &mut App) -> usize {
    let mut failures = 0;

    // 先清干净（幂等，也避免上一轮残留影响判断）。
    dispatcher::execute(&voke_core::Action::CancelAwake, None, &*cx);
    if voke_sys::current_awake().is_some() {
        log::error!("awake 自检 FAIL：取消后状态仍为激活");
        failures += 1;
    }

    // 1. 开启 30 分钟（不保持屏幕）。
    if !dispatcher::execute(
        &voke_core::Action::SetAwake {
            minutes: Some(30),
            keep_screen: false,
        },
        None,
        &*cx,
    ) {
        log::error!("awake 自检 FAIL：SetAwake 未被 dispatcher 处理");
        return failures + 1;
    }

    // 2. 状态回读（这是 `SetThreadExecutionState` 真的生效的证据链之一：
    //    线程起来后立刻写入状态，这里读的就是那份状态）。
    let now = voke_sys::awake::now_secs() as i64;
    match voke_sys::current_awake() {
        None => {
            log::error!("awake 自检 FAIL：开启后状态仍为 None（线程可能立刻退出了）");
            failures += 1;
        }
        Some(state) => {
            let expected = now + 30 * 60;
            let delta = state
                .expires_at
                .map(|at| (at as i64 - expected).abs())
                .unwrap_or(i64::MAX);
            if delta > 5 {
                log::error!(
                    "awake 自检 FAIL：到期时间偏差 {delta}s（expires_at={:?}，期望≈{expected}）",
                    state.expires_at
                );
                failures += 1;
            }
            if state.keep_screen {
                log::error!("awake 自检 FAIL：请求的是不保持屏幕，实际却带了");
                failures += 1;
            }
            log::info!(
                "awake 自检 ok：状态已写入（expires_at={:?}，剩余 {}s，keep_screen={}）",
                state.expires_at,
                state.expires_at.map(|at| at as i64 - now).unwrap_or(-1),
                state.keep_screen
            );
        }
    }

    // 3. provider 侧看到的就是"已激活"：**第一条是可选中的取消项**（Execute +
    //    CancelAwake），后续是两个进入二级的方向。
    //
    //    早期实现里"保持唤醒中（还剩 xx）"是一条不可选的 Hint，自检据此断言提示
    //    区；provider 后来把它升级成了可选中的取消项，这条断言跟着改——判据取
    //    `Action::CancelAwake` 而不是文案，避免文案迭代再次把自检带偏。
    let outcome = search::run_query_blocking("awake", None, None, false);
    let titles: Vec<String> = outcome.rows.iter().map(|m| m.inner.title.clone()).collect();
    let cancel_title = outcome
        .rows
        .iter()
        .find(|r| {
            r.inner.provider_id == voke_core::AwakeProvider::ID
                && matches!(r.inner.action, voke_core::Action::CancelAwake)
        })
        .map(|r| r.inner.title.clone());
    // 不在这条分支里 return：第 4 步的取消必须照跑，自检不能把系统留在"持续唤醒"。
    match cancel_title {
        Some(title) if title.contains("保持唤醒中") => {
            log::info!("awake 自检 ok：候选 = {titles:?}");
        }
        Some(title) => {
            log::error!("awake 自检 FAIL：取消项标题不带状态文案：{title:?}");
            failures += 1;
        }
        None => {
            log::error!("awake 自检 FAIL：激活后没有取消项：{titles:?}");
            failures += 1;
        }
    }

    // 4. 取消。
    if !dispatcher::execute(&voke_core::Action::CancelAwake, None, &*cx) {
        log::error!("awake 自检 FAIL：CancelAwake 未被 dispatcher 处理");
        failures += 1;
    }

    // 5. 回到时长候选列表。
    let outcome = search::run_query_blocking("awake", None, None, false);
    let titles: Vec<String> = outcome.rows.iter().map(|m| m.inner.title.clone()).collect();
    let hints: Vec<String> = outcome.hints.iter().map(|m| m.title.clone()).collect();
    if !titles.iter().any(|t| t == "30 分钟") {
        log::error!("awake 自检 FAIL：取消后未回到时长候选：{titles:?}");
        failures += 1;
    } else if hints.iter().any(|t| t.contains("保持唤醒中")) {
        log::error!("awake 自检 FAIL：取消后仍显示「保持唤醒中」：{hints:?}");
        failures += 1;
    } else {
        log::info!("awake 自检 ok：取消后候选 = {titles:?}，提示 = {hints:?}");
    }

    if voke_sys::current_awake().is_some() {
        log::error!("awake 自检 FAIL：取消后状态未清");
        failures += 1;
    }

    failures
}

/// 日志写到 voke.log，方便在 Windows 侧事后排查。
///
/// 路径取 `voke_sys::paths::log_path()`（`%APPDATA%\voke\voke.log`）而不是
/// 进程工作目录：开机自启动时工作目录由系统决定（常见 `C:\Windows\system32`），
/// 那里既不该写也没权限写——以前是直接 `File::create("voke.log").expect(...)`，
/// 自启动场景下必然 panic。拿不到专用目录时降级回工作目录而不是让进程起不来。
///
/// **跨启动追加 + 行数上限滚动删除**：文件以追加模式打开（`create + append
/// + read`，不再每次启动 `File::create` 截断清空——`.read(true)` 是删头批
/// 读回全文所需，纯 append 是 O_WRONLY），历史日志跨重启保留；总行数受
/// `VOKE_LOG_MAX_LINES`（构建期注入，默认 9999，见 `build.rs`）控制，超限时
/// 从头部删一批最旧的行（`max(1, N/10)`）并落一行中文标记，详见
/// `voke_sys::logcap`。RUST_LOG 优先、缺省 Info 的逻辑不变。
fn init_logging() {
    // env_logger 未设 RUST_LOG 时默认 Error，会吞掉 info 级日志
    // （且 log 宏惰性求值，连日志文件都不会被创建），故显式设默认级别 Info。
    //
    // **但默认级别只能在 RUST_LOG 缺省时施加**：以前写法是
    // `from_default_env().filter_level(Info)`，而 `from_default_env` 先解析了
    // RUST_LOG， `.filter_level` 又把**全局**默认级别覆盖成 Info——于是
    // `RUST_LOG=debug` 完全失效，debug 日志无法打开（实测：设了环境变量
    // 日志里仍没有一行 DEBUG）。现在先看 RUST_LOG 是否存在，存在就尊重它，
    // 否则才用 Info 兜底。
    let mut builder = env_logger::Builder::new();
    match std::env::var("RUST_LOG") {
        Ok(filters) if !filters.trim().is_empty() => {
            builder.parse_filters(&filters);
        }
        _ => {
            builder.filter_level(log::LevelFilter::Info);
        }
    }
    // 专用目录写不了（权限/环境变量缺失）时退回工作目录，再不行就没有文件日志。
    //
    // 追加模式打开（**不是 File::create**）：日志跨启动保留，总量交给
    // RollingLogWriter 按行数上限从头部滚动删除。打不开 / 数既有行数失败才
    // 退化成 stderr（Windows GUI 子系统下不可见），但进程必须继续启动——
    // 日志不是核心功能，不该拖垮 launcher。
    let file = open_log_file_append();
    let max_lines = log_max_lines();
    match file.map(|f| voke_sys::RollingLogWriter::new(f, max_lines)) {
        Some(Ok(writer)) => {
            let _ = builder
                .target(env_logger::Target::Pipe(Box::new(writer)))
                .try_init();
        }
        _ => {
            // 两个位置都写不了（或数行失败）：日志退化成 stderr，进程继续启动。
            let _ = builder.try_init();
        }
    }
}

/// 以**追加模式**打开日志文件：先试专用目录（`%APPDATA%\voke\`），再退工作目录。
///
/// `.read(true)` 不可省：超限删头批要读回全文数行，而纯 append 打开是
/// O_WRONLY、读会 EBADF；O_APPEND 的「写永远追加到末尾」语义不受影响。
fn open_log_file_append() -> Option<std::fs::File> {
    use std::fs::OpenOptions;
    let open = |path: &std::path::Path| {
        OpenOptions::new()
            .create(true)
            .append(true)
            .read(true)
            .open(path)
            .ok()
    };
    voke_sys::paths::log_path()
        .as_deref()
        .and_then(open)
        .or_else(|| open(std::path::Path::new("voke.log")))
}

/// 日志行数上限：构建期由 `build.rs` 从 `VOKE_LOG_MAX_LINES` 注入
/// （未设/非法/为 0 默认 9999）；这里解析失败同样兜底
/// [`voke_sys::DEFAULT_MAX_LINES`]，两处数字必须一致。
fn log_max_lines() -> usize {
    option_env!("VOKE_LOG_MAX_LINES")
        .and_then(|s| s.parse::<usize>().ok())
        .filter(|&n| n > 0)
        .unwrap_or(voke_sys::DEFAULT_MAX_LINES)
}
