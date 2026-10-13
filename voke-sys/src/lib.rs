//! voke 的 Win32 / COM 系统集成层。
//!
//! 所有 `unsafe` 都隔离在这一层：.lnk 解析、UWP 枚举、窗口枚举与激活、
//! 全局热键、图标提取、保持唤醒。
//!
//! M0 阶段这里只验证 windows crate 能交叉编译；M0 PoC #1 加 RegisterHotKey；
//! C 块加 [`apps`]（开始菜单 .lnk 解析）。
//!
//! **平台策略**：[`apps`] 里的文件系统枚举与字符串工具是纯 Rust，任何平台都编译
//! 并测试（Linux 上 `cargo test -p voke-sys` 会真跑这些）；只有 COM 解析部分
//! `#[cfg(windows)]`。其余模块（hotkey、窗口控制）整体 `#[cfg(windows)]`。

pub mod apps;

// 环境变量路径展开（%VAR% / ~）与子进程工作目录判定（展开→校验→不合法不设）。
// 纯函数主体任何平台编译并测试（Linux 上 `cargo test -p voke-sys` 真跑），
// 见模块文档：这是"解析直启 exe"路线必须自己复刻的 shell 展开语义。
pub mod env;

// 文件关联启动（ShellExecuteW）：CreateProcessW 跑不了的非 PE 镜像目标
// （.msc / .cpl / .bat / .ps1 …）的官方出路。Windows 实现是 #[cfg(windows)]，
// 非 Windows 是"记日志 + 返回 false"的占位，所以模块本身不挂 cfg——
// `cargo test -p voke-sys`（Linux native）照常编译。
pub mod shell;

// voke 自己的落盘位置（配置 / 日志 / 图标缓存）。纯路径拼装，任何平台都编译；
// 只有读 %APPDATA% 的入口是 #[cfg(windows)]（见模块内部）。
pub mod paths;

// 进程内存快照（排查常驻内存用）。任何平台都编译，非 Windows 返回 None。
pub mod mem;

// 保持唤醒（M5 的 awake）：状态编码/剩余时间格式化是纯逻辑，任何平台可测；
// 只有真正调 SetThreadExecutionState 的线程是 #[cfg(windows)]（见模块内部）。
pub mod awake;

// 单实例保护（二次启动只激活已有实例）。Win32 实现全部在模块内部
// #[cfg(windows)]，非 Windows 是跨平台占位实现，所以模块本身不挂 cfg，
// `cargo test -p voke-sys`（Linux native）照常编译。
pub mod single_instance;

// 窗口模块**不能**整体挂 cfg(windows)：`WindowEntry` 与过滤/进程名等纯逻辑
// 在任何平台都能编译和测试（Linux 上就有实际覆盖），只有碰 Win32 的函数
// 才是 cfg(windows)（见模块内部）。最早整体挂 cfg 导致 Linux 单测碰不到过滤逻辑，
// 漏掉一个真实的大小写 bug。
pub mod windows;

// WT profiles（terminal provider 的 sys 半边）：路径拼接与 AUMID 推导是纯函数
// （任何平台可测），只有读文件是 #[cfg(windows)]（见模块内部）。解析/合并在
// voke-core。
pub mod terminal;

// MSIX/Store 应用的 App Activation（IApplicationActivationManager）：WT profile
// 的官方启动路径（对齐 PowerToys CmdPal 的 Windows Terminal 扩展）。Windows
// 实现是 #[cfg(windows)] 的 COM，非 Windows 是"记日志 + 返回 None"的占位——
// `cargo test -p voke-sys`（Linux native）照常编译。
pub mod appactivate;

// workspaces provider 的 sys 半边：**编辑器历史的只读 SQLite 读取**（Zed 的
// db.sqlite、VSCode 的 state.vscdb）。路径拼接与读库都不挂 cfg(windows)
// （Linux 上用临时 SQLite fixture 真跑测试），只有读 %LOCALAPPDATA% /
// %USERPROFILE% / %APPDATA% 的包装函数是 #[cfg(windows)]。
// 解析/过滤/排序去重在 voke-core。
pub mod workspace;

// 日志行数上限（max lines 容量控制）：纯 std::io 逻辑，任何平台都编译并测试
// （Linux 上 `cargo test -p voke-sys` 真跑删批/计数用例）——消费方 voke-app 的
// 测试在 Linux 因缺 fontconfig 跑不了，放这里才能被覆盖，详见模块文档。
pub mod logcap;

// 窗口「程序化关闭」的一次性闸门（重入守卫）：纯 Cell<bool> 状态机，任何平台
// 都编译并测试——同 logcap 的理由，消费方 voke-app 的测试在 Linux 跑不了，
// 放这里才能被覆盖，详见模块文档。
pub mod close_guard;

// 启动早期纠正 voke 自身工作目录（开机自启继承到 System32 的修复）。
// 判定是纯函数，任何平台编译并测试（Linux 上 `cargo test -p voke-sys` 真跑
// 表驱动单测）；真正改目录用 std::env::set_current_dir（跨平台，不引
// windows crate / cfg 分支），详见模块文档。
pub mod cwd;

#[cfg(windows)]
pub mod hotkey;

// 开机自启：写 HKCU\…\Run 注册表。纯 Win32，只在 Windows 上有意义，
// 整个模块（含它的单测）都被 cfg 挡掉。
#[cfg(windows)]
pub mod autostart;

// 系统主题（深色模式支持）：读 HKCU\…\Themes\Personalize\AppsUseLightTheme +
// 低频轮询监听变化。纯函数映射任何平台可测，只有读注册表 / 轮询线程是
// #[cfg(windows)]（见模块文档的选型理由）。
pub mod theme;

// 系统托盘（tray-icon 0.26，target 门控依赖）。必须在专用线程里跑 Win32
// 消息循环，见模块文档。
#[cfg(windows)]
pub mod tray;

#[cfg(windows)]
pub mod icon;

#[cfg(windows)]
pub use apps::{
    AppEntry, TargetKind, enum_store_apps, load_all_apps, load_start_menu_apps, resolve_all,
    resolve_lnk, with_com,
};

#[cfg(windows)]
pub use windows::{WindowEntry, activate_window, enum_windows};

#[cfg(windows)]
pub use hotkey::{
    ALT_SPACE, CTRL_ALT_SPACE, Hotkey, HotkeyHandle, OnPress, hotkey_from_spec,
    spawn_default_hotkey, spawn_hotkey,
};

// 自启走模块路径（`voke_sys::autostart::is_enabled`）：`is_enabled` 这个名字
// 放在 crate 根太泛了。

// 托盘：计划里 app 层就是 `voke_sys::spawn_tray(...)`，所以在根上平铺。
#[cfg(windows)]
pub use tray::{TrayCommand, default_icon, spawn_tray};

// 系统主题：app 层就是 `voke_sys::is_system_dark()` / `voke_sys::spawn_theme_watcher(...)`，
// 在根上平铺（与托盘同风格）。非 Windows 上 `is_system_dark` 恒为浅色、
// `spawn_theme_watcher` 是空实现。
pub use theme::{OnThemeChange, is_system_dark, light_value_is_dark, spawn_theme_watcher};

// 保持唤醒：任何平台都导（非 Windows 上是记状态 + 日志的占位实现，
// 这样 app 层的 awake provider 接线逻辑不需要额外 cfg 分支）。
pub use awake::{AwakeRequest, AwakeState, cancel_awake, current as current_awake, spawn_awake};

#[cfg(windows)]
pub use awake::local_offset_seconds;

// WT settings 读取 / wt.exe 路径 / WT 包 AUMID（纯函数部分任何平台可用）。
pub use terminal::{package_aumid, settings_path, settings_paths, wt_exe_path};
#[cfg(windows)]
pub use terminal::{read_settings_sources, wt_exe};

// MSIX/Store 应用的 App Activation（WT profile 启动走这条，对齐 CmdPal）：
// Windows 实现是 #[cfg(windows)] 的 COM，非 Windows 是占位实现——
// 与 shell_open 同风格，调用点不需要额外 cfg。
pub use appactivate::activate_app;

// 内存快照：路径都平铺在根上，app 层直接 `voke_sys::log_snapshot(...)`。
pub use mem::{log_snapshot, process_memory};

// 文件关联启动：app 层直接 `voke_sys::shell_open(...)`（与 awake/theme 同风格，
// 非 Windows 上是占位实现，调用点不需要额外 cfg）。
pub use shell::shell_open;

// 单实例保护：app 层直接 `voke_sys::single_instance_guard()` +
// `SingleInstance::Primary(signal) => signal.spawn_waiter(...)`。
pub use single_instance::{ShowSignal, SingleInstance, single_instance_guard};

// 编辑器历史库的路径定位 + 只读读取（纯函数部分任何平台可用，
// 只有走环境变量的入口是 #[cfg(windows)]）。
pub use workspace::{
    VSCODE_CLI_CANDIDATES, ZED_EXE_CANDIDATES, epoch_to_stamp, first_existing, vscode_db_paths,
    zed_db_dir, zed_db_paths,
};
#[cfg(windows)]
pub use workspace::{vscode_command, vscode_workspaces, zed_command, zed_workspaces};

// 日志行数上限：app 层就是 `RollingLogWriter::new(file, max)` 包住日志文件 +
// `DEFAULT_MAX_LINES` 兜底（与 awake/theme 同风格，平铺在根上）。
pub use logcap::{DEFAULT_MAX_LINES, RollingLogWriter};

// 窗口关闭重入守卫：app 层就是 `CloseGuard::new()` + 关闭回调里 `enter()`
// 一次性放行（与 awake/theme 同风格，平铺在根上）。
pub use close_guard::CloseGuard;

// 启动早期纠正自身工作目录：app 层直接 `voke_sys::fix_own_cwd()`（与
// awake/theme 同风格，平铺在根上）。判定函数 `should_fix_own_cwd` /
// `is_system_dir` 同样平铺导出供测试与复用。
pub use cwd::{fix_own_cwd, is_system_dir, should_fix_own_cwd};

/// 占位：M0 仅用于确认 crate 能被交叉编译到 windows-gnu。
#[cfg(windows)]
pub fn is_windows() -> bool {
    true
}

/// 隐藏指定 HWND 的窗口（`ShowWindow(SW_HIDE)`）。
///
/// 为什么不用 gpui 的 `App::hide()`：gpui-pre-windows 的 `Platform::hide()`
/// 是空实现（`fn hide(&self) {}`，见 platform.rs:714），调了窗口不会消失。
/// gpui 公开 API 又只有 `minimize_window()`（`SW_MINIMIZE`），对 PopUp 无边框
/// launcher 会产生怪异的最小化动画。这里绕过 gpui 直接调 Win32。
///
/// `hwnd` 用裸 `isize` 传入，是为了让 app 层不需要依赖 windows crate 的类型。
#[cfg(windows)]
pub fn hide_window(hwnd: isize) {
    use ::windows::Win32::Foundation::HWND;
    use ::windows::Win32::UI::WindowsAndMessaging::{SW_HIDE, ShowWindow};
    unsafe {
        let _ = ShowWindow(HWND(hwnd as *mut core::ffi::c_void), SW_HIDE);
    }
}

/// 显示并前置指定 HWND 的窗口。
///
/// 为什么不能只用 gpui 的 `Window::activate_window()`：它的 `activate()` 流程
/// 是 `set_window_placement()`（把初始化时 `show: false` 扣下的 placement 补上，
/// `SetWindowPlacement` 隐含显示）→ `SetForegroundWindow`。但
/// `initial_placement` 是 `Cell<Option<..>>`，**被 take 一次就没了**——
/// 本函数 SW_HIDE 之后再次 activate 只剩 SetFocus/SetForegroundWindow，
/// 对隐藏窗口无效。所以唤起必须显式 `ShowWindow(SW_SHOW)`。
///
/// SW_SHOW 本身不带激活语义，拿前台靠调用方接着调 gpui 的 `activate_window()`
/// （内部有模拟 Alt 键输入绕过 Windows 前台锁定的 ragebait trick）。
#[cfg(windows)]
pub fn show_window(hwnd: isize) {
    use ::windows::Win32::Foundation::HWND;
    use ::windows::Win32::UI::WindowsAndMessaging::{SW_SHOW, ShowWindow};
    unsafe {
        let _ = ShowWindow(HWND(hwnd as *mut core::ffi::c_void), SW_SHOW);
    }
}

/// 查询窗口是否可见（`IsWindowVisible`）。
#[cfg(windows)]
pub fn is_window_visible(hwnd: isize) -> bool {
    use ::windows::Win32::Foundation::HWND;
    use ::windows::Win32::UI::WindowsAndMessaging::IsWindowVisible;
    unsafe { IsWindowVisible(HWND(hwnd as *mut core::ffi::c_void)).as_bool() }
}

/// 查询窗口是否是当前前台窗口（`GetForegroundWindow` 比较 HWND）。
/// toggle 判断用：窗口可见但不在前台时，热键应该是唤起/置前而不是隐藏。
#[cfg(windows)]
pub fn is_foreground_window(hwnd: isize) -> bool {
    use ::windows::Win32::UI::WindowsAndMessaging::GetForegroundWindow;
    unsafe { GetForegroundWindow().0 as isize == hwnd }
}
