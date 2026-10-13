//! 设置窗口 UI（阶段 4）。
//!
//! ## 为什么是独立窗口
//!
//! gpui 没有运行时改窗口尺寸的 API（见 docs/HISTORY.md「背景事实」），搜索面板
//! 只有 680×N 那么大，装不下十来组配置项，所以设置页是**独立窗口**
//! （880×600、带系统标题栏、可缩放、创建即显示），面板一行代码都不改。
//!
//! ## 页面骨架
//!
//! 用 `gpui-component` 自带的 setting 框架（`gpui_kit::component::setting::*`，
//! 签名都对着 `gpui-component-0.7.1/src/setting/` 核实过）：
//!
//! ```text
//! Settings（RenderOnce，自带左侧搜索 + 导航 Sidebar）
//!   SettingPage「通用」  SettingGroup「启动」/「热键」/「关于」
//!   SettingPage「插件」  10 个 SettingGroup，每组五项：启用 / 全局 / 权重 / 别名 / 激活方式
//! ```
//!
//! ## 写值的统一纪律
//!
//! 每个 setter 都是「改 [`VokeSettings`] 里的值 → [`crate::config::commit`]」，
//! commit 内部落盘 + 把新配置下发到查询引擎，所以改动**立刻生效**，不需要重启。
//! 三条例外：
//!
//! - **开机自启**：先写注册表，失败就 `log::error` 并 return（不写配置）；
//! - **别名**：非法值（含空白 / 与别的插件重复）同样不写配置；
//! - **热键**：录制期间先注销旧全局热键（抑制态，避免按到旧组合键触发旧动作），
//!   录到新组合后 `HotkeyHandle::try_register` 注册**成功才**写配置；失败则回退
//!   注册原热键、配置不动并给出错误提示（见 [`SettingsView::record`]）。
//!
//! 所有字段都是**受控值**（get 闭包读的还是那份全局配置），所以"不写入"就等价于
//! "界面回弹"——不需要在 UI 里额外做撤销。
//!
//! ## 窗口生命周期：关闭 = 程序化销毁
//!
//! 设置窗**关闭即程序化销毁**（Zed 主窗同款姿势），不复用、不常驻：关闭时回收
//! 整套 gpui 窗口资源（880×600 逻辑像素 @125% 缩放下 8 纹理 ≈ 26.4MB + 图集，
//! 实测 26.5MB）。为什么必须走这条路、为什么必须 `return false` +
//! `remove_window()`，见 [`open`] 里 `on_window_should_close` 注册处的长注释
//! （含 gpui 源码逐行位置）。

use gpui_kit::component::ActiveTheme as _;
use gpui_kit::component::kbd::Kbd;
use gpui_kit::component::setting::{
    NumberFieldOptions, SettingField, SettingGroup, SettingItem, SettingPage, Settings,
};
use gpui_kit::{
    AnyElement, AnyWindowHandle, App, AppContext as _, Bounds, ClickEvent, Context, Entity,
    FocusHandle, InteractiveElement as _, IntoElement, KeyDownEvent, Keystroke, Modifiers,
    ParentElement as _, Render, SharedString, StatefulInteractiveElement as _, Styled as _, Window,
    WindowBounds, WindowKind, WindowOptions, div, px, size,
};
use voke_core::config::{Activation, HotkeySpec, ProviderSettings, ThemeMode};

use crate::ShellState;
use crate::config::VokeSettings;

/// 设置窗口的尺寸（与搜索面板互不干涉，见模块文档）。
const WINDOW_SIZE: (f32, f32) = (880., 600.);

/// 10 个插件的 `(id, 展示名, 描述)`。
///
/// 顺序必须与 `voke-core/src/config.rs` 的默认表以及
/// [`crate::search::set_providers`] 的注册顺序一致——它就是设置页里的展示顺序。
const PLUGINS: &[(&str, &str, &str)] = &[
    ("apps", "应用", "已安装的应用（开始菜单 + Store）"),
    ("windows", "窗口", "当前已打开的窗口"),
    ("awake", "保持唤醒", "临时阻止系统休眠"),
    ("terminal", "终端配置", "Windows Terminal 的 profile 列表"),
    ("workspaces", "工作区", "Zed / VSCode 的最近工作区"),
    // 下面五条与 `voke-core` 各 provider 的 description() 文案一致，不再写死前缀
    // （前缀是可配置的，见「激活别名」一行；把前缀写进描述迟早会与真实配置打架）。
    ("calculator", "计算器", "算术表达式求值，如 1+1"),
    ("timestamp", "时间戳", "时间戳 ⇄ 可读时间"),
    ("base64", "Base64", "base64 编解码（加密 / 解密）"),
    ("uuid", "UUID", "生成 UUID v4（可指定个数）"),
    ("random", "随机值", "随机整数 / 随机字符串"),
];

/// 权重的取值范围（与 `AppConfig::normalize` 的 clamp 区间一致）。
///
/// 取值与展示都量化到一位小数（见 [`quantize`]）：否则 `1.2f32 as f64` 会被显示成
/// `1.2000000476837158`。
const WEIGHT_MIN: f64 = 0.;
const WEIGHT_MAX: f64 = 3.;
const WEIGHT_STEP: f64 = 0.1;

/// 「一次生成个数」的取值范围（与 `AppConfig::normalize` 对 `count` 的 clamp 区间一致）。
const COUNT_MIN: f64 = 1.;
const COUNT_MAX: f64 = 50.;
const COUNT_STEP: f64 = 1.;

/// 「一次生成个数」的默认值：与 `voke_core::provider::ProviderConfig::default().count`
/// 保持一致（配置层 `ProviderSettings::count` 为 `None` 时由它回填）。
const COUNT_DEFAULT: u32 = 5;

/// 热键录制器的状态机。
#[derive(Clone, Copy, PartialEq, Eq)]
enum RecorderState {
    /// 未在录制：全局热键保持正常注册。
    Idle,
    /// 正在录制：全局热键已注销（抑制态），按任何组合键都只进本录制器。
    Recording,
    /// 正在尝试注册用户刚录下的组合键（等热键线程回执）。
    ///
    /// 这一步是同步的（[`HotkeyHandle::try_register`] 阻塞等热键线程做完
    /// `RegisterHotKey`），所以状态不会跨帧停留；存在的意义是让状态机语义完整，
    /// 把「还在等用户按键」与「已录到键、正在确认」在代码里显式区分开。
    Confirming,
}

/// 设置窗口的根视图。
///
/// 只持有一个焦点句柄，而且它不是 `Focusable` 用的那个：热键录制区必须**先拿到
/// 焦点**才收得到按键（gpui 的按键只分发到焦点元素那一支）。
pub struct SettingsView {
    hotkey_focus: FocusHandle,
    /// 热键录制器当前状态。
    recorder: RecorderState,
    /// 最近一次录制失败的提示（红字显示）；成功 / 取消 / 进入录制时清空。
    hotkey_error: Option<SharedString>,
}

impl SettingsView {
    fn new(window: &mut Window, cx: &mut Context<Self>) -> Self {
        let hotkey_focus = cx.focus_handle();

        // 窗口失焦（用户点到别处 / 切走）时若还在录制态，恢复原热键并退出录制，
        // 否则旧热键会一直处于注销态、用户再也唤不出面板。
        cx.observe_window_activation(window, |view, window, cx| {
            if !window.is_window_active() {
                log::info!("设置窗口失焦 -> 若在录制热键则恢复原热键");
                view.abort_recording(cx);
            }
        })
        .detach();

        // 录制区自身失焦（例如用户接着点了侧边栏其它项）也恢复：否则热键会停在
        // 注销态，而按键又不再进本录制器，录制无从继续。
        cx.on_blur(&hotkey_focus, window, |view, _window, cx| {
            view.abort_recording(cx);
        })
        .detach();

        Self {
            hotkey_focus,
            recorder: RecorderState::Idle,
            hotkey_error: None,
        }
    }

    /// 进入录制态：立即注销当前全局热键（阻塞到热键线程确认），此后按任何组合键
    /// 都只被本录制器捕获、不会触发旧热键动作。
    fn enter_recording(&mut self, cx: &mut Context<Self>) {
        if self.recorder != RecorderState::Idle {
            return;
        }
        #[cfg(windows)]
        {
            let handle = cx.global::<ShellState>().hotkey.as_ref();
            match handle {
                Some(handle) => {
                    if handle.suppress() {
                        log::info!("进入热键录制态：全局热键已注销");
                    } else {
                        log::error!("进入热键录制态：注销全局热键失败");
                    }
                }
                None => log::warn!("进入热键录制态：热键句柄缺失（无可注销的热键）"),
            }
        }
        self.recorder = RecorderState::Recording;
        self.hotkey_error = None;
        cx.notify();
    }

    /// 退出录制态并恢复注册配置里的原热键（Esc 取消 / 失焦 / 关窗都走这里）。
    fn abort_recording(&mut self, cx: &mut Context<Self>) {
        if self.recorder == RecorderState::Idle {
            return;
        }
        self.recorder = RecorderState::Idle;
        self.hotkey_error = None;
        restore_current_hotkey(cx);
        cx.notify();
    }

    /// 录下一个按键：只在录制态处理；Esc 取消，其余组合尝试注册，成功落库、
    /// 失败回退原热键并给出提示。
    fn record(&mut self, stroke: &Keystroke, cx: &mut Context<Self>) {
        let key = stroke.key.as_str();
        // 纯修饰键单独按下不算热键（否则"先按 Ctrl"这一步会被记成以 control 为主键）。
        if matches!(key, "shift" | "control" | "alt" | "platform" | "function") {
            return;
        }
        // 只有点击进入录制态后才录键。
        if self.recorder != RecorderState::Recording {
            return;
        }
        // 录制态下吞掉这个按键：不让它继续冒泡到窗口默认处理。带标题栏的设置窗口里
        // `Alt+Space` 会弹系统菜单，若不吞掉，录制默认热键时会同时弹出菜单。
        cx.stop_propagation();
        // Esc = 取消录制：恢复原热键，什么都不改。
        if key == "escape" {
            log::debug!("取消热键录制");
            self.abort_recording(cx);
            return;
        }

        let spec = HotkeySpec {
            ctrl: stroke.modifiers.control,
            alt: stroke.modifiers.alt,
            shift: stroke.modifiers.shift,
            win: stroke.modifiers.platform,
            key: key.to_string(),
        };

        // 尝试把录到的组合翻译成 Win32 热键并注册（阻塞等热键线程回执）。翻译不出
        // 虚拟键码的冷门键无法注册，保留录制态让用户换一个。非 Windows 构建没有
        // 全局热键，直接按「注册成功」走落库分支。
        self.recorder = RecorderState::Confirming;
        #[cfg(windows)]
        let registered = match voke_sys::hotkey_from_spec(&spec) {
            None => {
                log::warn!("按键 {key:?} 无法注册为全局热键，请换一个");
                self.recorder = RecorderState::Recording;
                self.hotkey_error = Some(SharedString::from(format!(
                    "“{spec}” 无法作为全局热键，请换一个组合键"
                )));
                cx.notify();
                return;
            }
            // 句柄缺失说明启动时没能起线程：无从注册，按成功落库、下次启动生效。
            Some(hotkey) => match cx.global::<ShellState>().hotkey.as_ref() {
                Some(handle) => handle.try_register(hotkey),
                None => {
                    log::error!("热键句柄缺失，新热键将在下次启动后生效");
                    true
                }
            },
        };
        #[cfg(not(windows))]
        let registered = true;

        if registered {
            self.recorder = RecorderState::Idle;
            self.hotkey_error = None;
            cx.global_mut::<VokeSettings>().set_hotkey(&spec);
            crate::config::commit(cx);
            log::info!("热键已改为 {spec}，配置已落盘");
        } else {
            // 注册失败（组合键已被其它程序占用等）：回退注册原热键，配置不写，
            // 给出明确错误提示。
            self.recorder = RecorderState::Idle;
            let old_label = cx
                .global::<VokeSettings>()
                .hotkey_spec()
                .as_ref()
                .map(ToString::to_string)
                .unwrap_or_default();
            restore_current_hotkey(cx);
            self.hotkey_error = Some(SharedString::from(format!(
                "“{spec}” 注册失败，可能已被其他程序占用；已恢复原热键 {old_label}"
            )));
            log::warn!("新热键 {spec} 注册失败，已回退原热键 {old_label}");
        }
        cx.notify();
    }
}

/// 按配置里的热键重新注册（录制取消 / 失败回退时用）。
///
/// 注册成功与否都只记日志——这几个路径的 UI 语义是「回到原状」，无需再打扰用户。
fn restore_current_hotkey(cx: &mut gpui_kit::App) {
    #[cfg(windows)]
    {
        let spec = cx.global::<VokeSettings>().hotkey_spec();
        let Some(hotkey) = spec.as_ref().and_then(voke_sys::hotkey_from_spec) else {
            log::warn!("恢复热键失败：配置里的热键无法翻译成 Win32 热键");
            return;
        };
        match cx.global::<ShellState>().hotkey.as_ref() {
            Some(handle) => {
                if handle.try_register(hotkey) {
                    log::info!("已恢复注册原热键");
                } else {
                    log::error!("恢复注册原热键失败（可能被其它程序占用）");
                }
            }
            None => log::warn!("热键句柄缺失，无法恢复注册原热键"),
        }
    }
    #[cfg(not(windows))]
    {
        let _ = cx;
    }
}

impl Render for SettingsView {
    fn render(&mut self, _window: &mut Window, cx: &mut Context<Self>) -> impl IntoElement {
        let view = cx.entity();
        Settings::new("voke-settings")
            .page(general_page(view.clone(), self.hotkey_focus.clone()))
            .page(plugins_page(cx, view))
            .into_any_element()
    }
}

/// 打开设置窗口；已经开着就把它显示并唤到前台（幂等）。
///
/// 托盘菜单、`Ctrl+,`、以及将来的其它入口都走这一个函数，所以"同时只开一个设置
/// 窗口"这条约束只在这里实现一次。
///
/// 设置窗口**关闭即程序化销毁**（见下面 `open_window` 构建闭包里注册的
/// `on_window_should_close`），不常驻内存。因此下面的幂等分支只会命中两种
/// 情况：短时间内二次打开（上一个窗口还没关掉）的竞态，以及句柄意外失效时的
/// 自愈（`handle.update` 失败 → 走创建分支）。
pub fn open(cx: &mut App) {
    let existing: Option<AnyWindowHandle> = cx.global::<ShellState>().settings_window;
    if let Some(handle) = existing
        && handle
            .update(cx, |_root, window, _cx| {
                // 复用仍活着的窗口时**不能只** `activate_window()`：它内部那次补
                // `initial_placement`（隐含显示）的流程是一次性 Cell，被 take 过
                // 就没了，对不可见窗口只剩 SetFocus/SetForegroundWindow、救不回来。
                // 所以沿用与 `main.rs` 面板 toggle 同一套做法：先显式
                // `ShowWindow(SW_SHOW)` 再 activate（窗口本就可见时前者是空操作，
                // 但路径一致、无需分支）。
                if let Some(hwnd) = crate::Shell::hwnd_of(window) {
                    voke_sys::show_window(hwnd);
                }
                window.activate_window();
            })
            .is_ok()
    {
        log::info!("设置窗口已存在，显示并激活它");
        return;
    }

    let options = WindowOptions {
        window_bounds: Some(WindowBounds::Windowed(Bounds::centered(
            None,
            size(px(WINDOW_SIZE.0), px(WINDOW_SIZE.1)),
            cx,
        ))),
        // 带系统标题栏：设置窗口是普通窗口，不需要像面板那样无边框 + 自管理显隐。
        titlebar: Some(gpui_kit::TitlebarOptions {
            title: Some("Voke 设置".into()),
            appears_transparent: false,
            traffic_light_position: None,
        }),
        kind: WindowKind::Normal,
        is_resizable: true,
        show: true,
        focus: true,
        ..Default::default()
    };

    match gpui_kit::open_window(options, cx, |window, cx| {
        let view = cx.new(|cx| SettingsView::new(window, cx));

        // 关闭 = 程序化销毁（Zed 主窗同款姿势：`zed.rs:493-500` 拦 WM_CLOSE 返
        // false → `multi_workspace.rs:555-581` 里 `window.remove_window()`），
        // 不开「隐藏复用」的倒车。
        //
        // 根因（gpui-pre-windows 拖放目标泄漏）：`register_drag_drop`
        // （platform window.rs:571 调用、1511 函数体）把 `Rc<WindowsWindowInner>`
        // 存进 OLE `IDropTarget`，而 `RevokeDragDrop` 被放在 `Drop for
        // WindowsWindow` 的延迟异步任务里（platform window.rs:610-631），执行时
        // HWND 已销毁必然失败，于是每关一次设置窗口就永久滞留一整套
        // `DirectXRenderer`（3 缓冲 swapchain + 4×MSAA 纹理 + 图集 ≈ 8·W·H·4 +
        // 5MB，880×600 @125% 缩放实测 26.5MB）。程序化销毁让 HWND 在 Drop 任务
        // 执行时仍然存活，`RevokeDragDrop` 成功，整套资源正常回收。
        //
        // 契约（gpui-pre-0.3.8，本机 registry 源码逐行核实）：
        // - 本回调**嵌套在 `update_window_erased` 内**运行（app.rs:3020-3078）：
        //   窗口被 `take` 出 map → 跑闭包 → `trail` 检查 `removed`，为真则同步
        //   摘除并 drop。所以回调里**不能**调 `cx.update_window(同一 handle)`
        //   （slot 已 take，嵌套调用拿到 None、静默失败），只能用闭包参数的
        //   `window.remove_window()`（window.rs:2323-2326，只置 `removed = true`），
        //   由外层 `trail` 落地。
        // - **必须返回 false**：平台侧 `handle_close_msg`（gpui-pre-windows
        //   events.rs:336-341）对 false 吞掉 WM_CLOSE；返 true → DefWindowProc
        //   **同步** DestroyWindow → Drop 任务后跑 → RevokeDragDrop 拿到死
        //   HWND → 重演泄漏。false 把 HWND 留给 Drop 任务，在存活期内完成
        //   RevokeDragDrop + DestroyWindow。
        //
        // 重入守卫：从闭包返回到 Drop 任务真正销毁 HWND 有毫秒级间隙，期间第二个
        // WM_CLOSE 一律吞掉返 false——绝不放行 DefWindowProc（会抢在 Drop 任务前
        // 销毁 HWND）。
        //
        // 捕获用弱引用：窗口既然真的销毁，没必要让平台回调盒强持根视图；
        // upgrade 失败（视图已不在）就跳过 abort_recording。Esc / Alt+F4 同样
        // 产生 WM_CLOSE，走同一回调，行为一致。
        let closing = voke_sys::CloseGuard::new();
        let view_for_close = view.downgrade();
        window.on_window_should_close(cx, move |window, cx| {
            if !closing.enter() {
                return false;
            }

            // 录制中的热键先退出并恢复原热键——必须赶在 remove_window 之前，
            // 否则窗口一关，旧热键就永久停在注销态、用户再也唤不出面板。
            if let Some(view) = view_for_close.upgrade() {
                view.update(cx, |view, cx| view.abort_recording(cx));
            }

            window.remove_window();
            // 句柄立即置空，下次 open() 走创建分支；即使漏了这步，复用分支的
            // `handle.update` 也会因窗口已被 trail 摘除而失败自愈（双保险）。
            cx.global_mut::<ShellState>().settings_window = None;
            log::info!("设置窗口 -> 程序化销毁（HWND 存活期内 RevokeDragDrop）");
            false
        });

        view
    }) {
        Ok((handle, _view)) => {
            cx.global_mut::<ShellState>().settings_window = Some(handle);
            log::info!("设置窗口已打开");
            // 量化「多一个 gpui 窗口」带来的常驻增量。窗口现在「关闭 = 程序化
            // 销毁」、每次开关都重建，所以这条快照**每轮开关都应出现一次**
            // （真机 20 轮验证清单的依据）；关闭后的回落由 main.rs 的
            // `on_window_closed` 快照「窗口已关闭」捕捉。
            voke_sys::log_snapshot("设置窗口已打开");
        }
        Err(err) => log::error!("打开设置窗口失败：{err}"),
    }
}

/// Page「通用」：外观 / 启动 / 热键 / 关于。
fn general_page(view: Entity<SettingsView>, hotkey_focus: FocusHandle) -> SettingPage {
    SettingPage::new("通用")
        .group(
            SettingGroup::new()
                .title("外观")
                .item(SettingItem::new("主题", theme_field())),
        )
        .group(
            SettingGroup::new()
                .title("启动")
                .item(SettingItem::new(
                    "开机自启",
                    SettingField::switch(
                        |cx| cx.global::<VokeSettings>().config.autostart,
                        set_autostart,
                    ),
                ))
                .footer(|_window, _cx| {
                    SharedString::from(if cfg!(windows) {
                        "写入 HKCU\\Software\\Microsoft\\Windows\\CurrentVersion\\Run"
                    } else {
                        "当前构建不支持开机自启"
                    })
                }),
        )
        .group(
            SettingGroup::new()
                .title("热键")
                .item(hotkey_item(view, hotkey_focus)),
        )
        .group(
            SettingGroup::new()
                .title("关于")
                .item(read_only_item("版本", |_cx| {
                    SharedString::from(env!("CARGO_PKG_VERSION"))
                }))
                .item(read_only_item("配置文件", config_path_label)),
        )
}

/// 只读文本项（版本号 / 配置路径这类只给用户看的东西）。
///
/// setting 框架里的 `SettingField` 都是可编辑控件，所以这里走
/// `SettingItem::render` 这个自定义 UI 逃生口。
fn read_only_item(
    title: &'static str,
    value: impl Fn(&App) -> SharedString + 'static,
) -> SettingItem {
    SettingItem::render(move |_options, _window, cx| {
        div()
            .flex()
            .flex_row()
            .items_center()
            .justify_between()
            .gap_3()
            .child(SharedString::from(title))
            .child(
                div()
                    .text_sm()
                    .text_color(cx.theme().muted_foreground)
                    .child(value(cx)),
            )
    })
}

/// 配置文件的落盘位置（拿不到路径说明这次会话没有持久化）。
fn config_path_label(cx: &App) -> SharedString {
    let text = match cx.global::<VokeSettings>().path.as_ref() {
        Some(path) => path.display().to_string(),
        None => "未落盘（取不到 %APPDATA%）".to_string(),
    };
    SharedString::from(text)
}

/// 开机自启开关的写路径：注册表改成功才落配置，失败就让它回弹。
fn set_autostart(on: bool, cx: &mut App) {
    #[cfg(windows)]
    if !voke_sys::autostart::set_enabled(on) {
        log::error!("开机自启写入失败，界面保持原值");
        return;
    }
    #[cfg(not(windows))]
    {
        log::warn!("非 Windows 构建没有开机自启，界面保持原值");
        return;
    }

    cx.global_mut::<VokeSettings>().set_autostart(on);
    crate::config::commit(cx);
}

/// 热键录制器：展示当前热键 + 点击进入录制、按下新键即尝试注册。
///
/// 交互（见模块文档与 [`SettingsView::enter_recording`]）：
/// 点击 → 注销旧全局热键并进入录制态 → 按新组合 → `try_register` 成功落库 /
/// 失败回退原热键并红字提示；Esc 或窗口失焦 / 关闭 → 取消并恢复原热键。
fn hotkey_item(view: Entity<SettingsView>, hotkey_focus: FocusHandle) -> SettingItem {
    SettingItem::render(move |_options, _window, cx| {
        let spec = cx.global::<VokeSettings>().hotkey_spec();
        // 录制器状态与错误文案存在 view 上：读一份快照再画。
        let (recorder, error) = {
            let view = view.read(cx);
            (view.recorder, view.hotkey_error.clone())
        };
        let hint = match recorder {
            RecorderState::Recording | RecorderState::Confirming => "请按下新的快捷键（Esc 取消）",
            RecorderState::Idle => "点击此处后按下新快捷键，Esc 取消",
        };
        let error_color = cx.theme().danger;
        let error_element = error.map(|text| {
            div()
                .text_xs()
                .text_color(error_color)
                .child(text)
                .into_any_element()
        });

        div()
            .flex()
            .flex_col()
            .gap_1()
            .child(
                div()
                    .track_focus(&hotkey_focus)
                    // `track_focus` 之后还得有 `id`：只有带 id 的 div 才是可交互元素，
                    // 才接得住 click / key（焦点与 id 缺一不可）。
                    .id("voke-hotkey-recorder")
                    .flex()
                    .flex_row()
                    .items_center()
                    .justify_between()
                    .gap_3()
                    .cursor_pointer()
                    .px_2()
                    .py_1()
                    .rounded(cx.theme().radius)
                    .bg(cx.theme().tokens.muted)
                    .on_click({
                        let focus = hotkey_focus.clone();
                        let view = view.clone();
                        move |_event: &ClickEvent, window, cx| {
                            window.focus(&focus, cx);
                            // 拿到焦点后进入录制态（注销旧热键，避免按到旧组合触发旧动作）。
                            view.update(cx, |view, cx| view.enter_recording(cx));
                        }
                    })
                    .capture_key_down({
                        let view = view.clone();
                        move |event: &KeyDownEvent, _window, cx| {
                            let stroke = event.keystroke.clone();
                            view.update(cx, |view, cx| view.record(&stroke, cx));
                        }
                    })
                    .child(hotkey_tag(&spec))
                    .child(
                        div()
                            .text_xs()
                            .text_color(cx.theme().muted_foreground)
                            .child(SharedString::from(hint)),
                    ),
            )
            .children(error_element)
    })
}

/// 把热键画成 VS Code 那种按键块；配置里没有有效热键时给提示而不是显示空块。
fn hotkey_tag(spec: &Option<HotkeySpec>) -> AnyElement {
    match spec {
        Some(spec) => Kbd::new(keystroke_of(spec)).into_any_element(),
        None => SharedString::from("（未设置）").into_any_element(),
    }
}

/// [`HotkeySpec`] → gpui [`Keystroke`]：两边字段一一对应，纯粹是搬运。
fn keystroke_of(spec: &HotkeySpec) -> Keystroke {
    Keystroke {
        modifiers: Modifiers {
            control: spec.ctrl,
            alt: spec.alt,
            shift: spec.shift,
            platform: spec.win,
            function: false,
        },
        key: spec.key.clone(),
        key_char: None,
    }
}

/// Page「插件」：每个插件一组，五项设置。
///
/// 需要 `cx` 是为了在构建描述时读 [`VokeSettings`]（「激活别名」一行要回显当前
/// 前缀）；需要 `view` 是为了别名改动后能 notify 设置页、让描述立刻刷新。
fn plugins_page(cx: &Context<SettingsView>, view: Entity<SettingsView>) -> SettingPage {
    let mut page = SettingPage::new("插件")
        .description("关掉「启用」后插件从结果和插件列表里彻底消失；别名是它的专属入口");
    for (id, name, description) in PLUGINS {
        page = page.group(plugin_group(cx, view.clone(), id, name, description));
    }
    page
}

fn plugin_group(
    cx: &Context<SettingsView>,
    view: Entity<SettingsView>,
    id: &str,
    name: &str,
    description: &str,
) -> SettingGroup {
    let group = SettingGroup::new()
        .title(name)
        .description(description)
        .item(SettingItem::new(
            "启用",
            SettingField::switch(
                {
                    let id = id.to_string();
                    move |cx| provider_of(cx, &id).enabled
                },
                {
                    let id = id.to_string();
                    move |on, cx| set_provider_field(cx, &id, |entry| entry.enabled = on)
                },
            ),
        ))
        .item(SettingItem::new(
            "参与全局搜索",
            SettingField::switch(
                {
                    let id = id.to_string();
                    move |cx| provider_of(cx, &id).is_global
                },
                {
                    let id = id.to_string();
                    move |on, cx| set_provider_field(cx, &id, |entry| entry.is_global = on)
                },
            ),
        ))
        .item(
            SettingItem::new("权重", weight_field(id.to_string()))
                .description("只影响同一插件内部的排序，不改插件之间的先后顺序"),
        );

    // 「一次生成个数」只对 random 有意义：它决定随机值插件一次吐出几条结果。
    // 其它 9 个插件不消费 `count`，给它们显示这一项只会误导，故按 id 条件加。
    let group = if id == "random" {
        group.item(
            SettingItem::new("一次生成个数", count_field(id.to_string()))
                .description("每次生成几条结果（仅随机值插件）"),
        )
    } else {
        group
    };

    group
        .item(
            SettingItem::new("激活别名", alias_field(id.to_string(), view))
                .description(alias_description(cx, id)),
        )
        .item(
            SettingItem::new("激活方式", activation_field(id.to_string()))
                .description("直接：输入前缀立刻进入；间接：前缀后再按一个空格才进入"),
        )
}

/// 读取某个 provider 的当前配置（配置里没有它时给零值而不是 panic）。
fn provider_of(cx: &App, id: &str) -> ProviderSettings {
    cx.global::<VokeSettings>()
        .config
        .providers
        .get(id)
        .cloned()
        .unwrap_or_default()
}

/// 改某个 provider 的一个字段并 commit。
///
/// 借用步数是刻意拆开的：`global_mut` 的可变借用必须先结束才能让 `commit` 拿到
/// `&mut App`。
fn set_provider_field(cx: &mut App, id: &str, f: impl FnOnce(&mut ProviderSettings)) {
    {
        let settings = cx.global_mut::<VokeSettings>();
        if let Some(entry) = settings.config.providers.get_mut(id) {
            f(entry);
        }
    }
    crate::config::commit(cx);
}

/// 权重输入框：`SettingField::number_input` 用 `f64`（框架定死的），落库转 `f32`。
fn weight_field(id: String) -> SettingField<f64> {
    SettingField::number_input(
        NumberFieldOptions {
            min: WEIGHT_MIN,
            max: WEIGHT_MAX,
            step: WEIGHT_STEP,
        },
        {
            let id = id.clone();
            move |cx| quantize(provider_of(cx, &id).weight as f64)
        },
        move |value, cx| {
            let value = quantize(value) as f32;
            set_provider_field(cx, &id, |entry| entry.weight = value);
        },
    )
}

/// 量化到一位小数（`(v * 10).round() / 10`）。理由见 [`WEIGHT_MIN`] 的注释。
fn quantize(value: f64) -> f64 {
    (value * 10.).round() / 10.
}

/// 「一次生成个数」输入框（random 专用，即 [`ProviderSettings::count`]）。
///
/// 与权重同款：`SettingField::number_input` 的读写都是 `f64`（框架定死），
/// 落库时转回 `u32`。`count` 在配置里是 `Option<u32>`——`None` 表示"没写"，
/// 展示时按 [`COUNT_DEFAULT`] 回填；用户一改就写入显式值。
fn count_field(id: String) -> SettingField<f64> {
    SettingField::number_input(
        NumberFieldOptions {
            min: COUNT_MIN,
            max: COUNT_MAX,
            step: COUNT_STEP,
        },
        {
            let id = id.clone();
            move |cx| provider_of(cx, &id).count.unwrap_or(COUNT_DEFAULT) as f64
        },
        move |value, cx| {
            // 输入框引擎已按 min/max clamp；这里再夹一次兜底，保证落库永远是 1..=50。
            let count = (value.round() as i64).clamp(COUNT_MIN as i64, COUNT_MAX as i64) as u32;
            set_provider_field(cx, &id, |entry| entry.count = Some(count));
        },
    )
}

/// 「激活别名」一行的描述：回显该插件**当前**真实前缀（留空 = 无专属入口）。
///
/// `SettingItem::description` 只收静态文本（没有动态闭包口子），所以每次设置页
/// 渲染时现算这一段；别名改动后 [`alias_field`] 会 notify，让描述当场刷新。
fn alias_description(cx: &App, id: &str) -> SharedString {
    match provider_of(cx, id).prefix {
        Some(prefix) => SharedString::from(format!("当前入口：{prefix}；留空表示无专属入口")),
        None => SharedString::from("当前无专属入口"),
    }
}

/// 激活别名输入框（即 [`ProviderSettings::prefix`]）。
///
/// 非法值**不写入**配置 → 下一次渲染时受控值回弹。空串是合法的：它表示"没有别名"。
/// 写入成功后 notify 设置页一次：描述里回显的"当前入口"依赖本字段，必须重画。
fn alias_field(id: String, view: Entity<SettingsView>) -> SettingField<SharedString> {
    SettingField::input(
        {
            let id = id.clone();
            move |cx| SharedString::from(provider_of(cx, &id).prefix.clone().unwrap_or_default())
        },
        move |value, cx| {
            let text = value.trim();
            // 含任何空白（不只是首尾）→ 拒绝：前缀要与查询紧接着写，不能有空格。
            if text.chars().any(char::is_whitespace) {
                log::warn!("激活别名不能含空白：{value:?}");
                return;
            }
            // 与别的插件撞了 → 拒绝（否则引擎的最长前缀匹配会分不清归谁）。
            if !text.is_empty()
                && cx.global::<VokeSettings>().config.providers.iter().any(
                    |(other_id, settings)| {
                        other_id != &id && settings.prefix.as_deref() == Some(text)
                    },
                )
            {
                log::warn!("激活别名 {text:?} 已被其它插件占用");
                return;
            }

            set_provider_field(cx, &id, |entry| {
                entry.prefix = if text.is_empty() {
                    None
                } else {
                    Some(text.to_string())
                };
            });

            // 描述里的"当前入口"跟着变，重画设置页让它刷新。
            view.update(cx, |_view, cx| cx.notify());
        },
    )
}

/// 激活方式下拉框的两个取值（与 [`Activation`] 一一对应，也是选项 value）。
const ACTIVATION_DIRECT: &str = "direct";
const ACTIVATION_INDIRECT: &str = "indirect";

/// 主题下拉框的三个取值（与 [`ThemeMode`] 一一对应，也是选项 value）。
const THEME_SYSTEM: &str = "system";
const THEME_DARK: &str = "dark";
const THEME_LIGHT: &str = "light";

/// 激活方式下拉框：直接（默认）/ 间接（前缀 + 空格）。
///
/// 间接是给 `#` 系插件用的——一输入前缀就路由会跟 `#` 插件列表入口冲突，
/// 所以它们默认要求前缀后再按一个空格。落库走与其它四项相同的
/// [`set_provider_field`] → `config::commit` 路径，改完立刻生效。
fn activation_field(id: String) -> SettingField<SharedString> {
    SettingField::dropdown(
        vec![
            (
                SharedString::from(ACTIVATION_DIRECT),
                SharedString::from("直接激活"),
            ),
            (
                SharedString::from(ACTIVATION_INDIRECT),
                SharedString::from("间接激活（前缀 + 空格）"),
            ),
        ],
        {
            let id = id.clone();
            move |cx| {
                let activation = provider_of(cx, &id)
                    .activation
                    .unwrap_or(Activation::Direct);
                SharedString::from(match activation {
                    Activation::Direct => ACTIVATION_DIRECT,
                    Activation::Indirect => ACTIVATION_INDIRECT,
                })
            }
        },
        move |value, cx| {
            let text: &str = &value;
            let activation = match text {
                ACTIVATION_INDIRECT => Activation::Indirect,
                _ => Activation::Direct,
            };
            set_provider_field(cx, &id, |entry| entry.activation = Some(activation));
        },
    )
}

/// 主题下拉框：跟随系统（默认）/ 深色 / 浅色。
///
/// setter 分三步：写 [`VokeSettings`] → [`crate::config::commit`]（落盘）→
/// [`crate::config::apply_theme`]（当场换配色，`Theme::change` 内部会刷新所有窗口，
/// 所以设置窗自身不用额外 `notify`）。
fn theme_field() -> SettingField<SharedString> {
    SettingField::dropdown(
        vec![
            (
                SharedString::from(THEME_SYSTEM),
                SharedString::from("跟随系统"),
            ),
            (SharedString::from(THEME_DARK), SharedString::from("深色")),
            (SharedString::from(THEME_LIGHT), SharedString::from("浅色")),
        ],
        |cx: &App| {
            SharedString::from(match cx.global::<VokeSettings>().theme() {
                ThemeMode::System => THEME_SYSTEM,
                ThemeMode::Dark => THEME_DARK,
                ThemeMode::Light => THEME_LIGHT,
            })
        },
        |value: SharedString, cx: &mut App| {
            let mode = match &*value {
                THEME_DARK => ThemeMode::Dark,
                THEME_LIGHT => ThemeMode::Light,
                _ => ThemeMode::System,
            };
            cx.global_mut::<VokeSettings>().set_theme(mode);
            crate::config::commit(cx);
            crate::config::apply_theme(mode, cx);
        },
    )
}
