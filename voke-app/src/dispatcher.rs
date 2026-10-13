//! `Action` 的执行层（§3 的"副作用集中在 app 的 dispatcher"）。
//!
//! core 里的 [`voke_core::Action`] 是**纯数据**，描述"选中这一项要做什么"；
//! 真正产生副作用的代码集中在这里，这样行为可审计。
//!
//! 已实现：
//! - [`voke_core::Action::Launch`]（应用/文档/目录/MSIX/外壳项/`.lnk`——`.lnk` 走
//!   `ShellExecuteExW` 本体执行，见 [`launch_path`]）
//! - [`voke_core::Action::LaunchTerminalProfile`]（WT profile：App Activation，
//!   对齐 PowerToys CmdPal，见 [`launch_terminal_profile`]）
//! - [`voke_core::Action::ActivateWindow`]（切到已打开窗口，M2）
//! - [`voke_core::Action::Copy`]（写剪贴板 + 隐藏窗口，M3）
//! - [`voke_core::Action::OpenUrl`]（gpui `open_url` + 隐藏窗口，M3）
//! - [`voke_core::Action::SetAwake`] / [`Action::CancelAwake`]（保持唤醒，M5）
//! - [`voke_core::Action::None`]（纯展示，什么都不做、**不隐藏**）
//!
//! **M5 的 `ActivatePlugin` 已随子命令面板整体删除**（方案 A 靠前缀激活插件，
//! 不需要一个"激活插件"的动作）。
//!
//! 副作用能力走 [`Host`] trait（剪贴板 / 打开 URL / 隐藏窗口 / 延迟执行启动），
//! 生产实现是 [`gpui_kit::App`]，测试用假的 `Host` 记录调用。抽这层是因为 gpui 的
//! `App` 在 Linux 上（`voke-app` 的原生测试环境）根本构造不出来，而"哪个动作隐藏
//! 窗口、哪个动作写剪贴板"是纯决策逻辑，必须能被单测盯住。
//!
//! **审计点：启动/切换/复制前先隐藏 launcher 窗口**（见 [`launch`]、[`activate`]、
//! [`copy`] 与 [`open_url`]）。
//!
//! ## 两阶段纪律：会泵消息的 Win32 调用不得在 App 借作用域内执行
//!
//! `execute` 运行在 gpui 事件分发的借作用域内（按键/点击经
//! `AsyncApp::update_window` 持有 `AppCell` 可变借用，`gpui-pre-0.3.8` 的
//! `app/async_context.rs:93`）。**凡会同步泵送 Windows 消息的 Win32 API
//! （`ShellExecuteExW` / `ShowWindow` / `MessageBox` …）都不得在这一阶段调用**：
//! 嵌套消息泵会重入 gpui 的 wndproc，重入帧回调再取 App 时
//! `try_borrow_mut` 失败刷 ERROR，走到硬 `borrow_mut` 的路径直接 panic——
//! 而 panic 出在 `extern "C"` wndproc 里不可跨 FFI unwind，只能 abort 整进程
//! （用户实测：`.lnk` 条目启动即崩溃，根因即此）。
//!
//! 因此本模块对启动副作用一律**两阶段**：
//! 1. **决策阶段**（借作用域内，同步）：路由判定 + 参数收集 + 全部 debug 日志，
//!    产出**纯数据**的启动请求（[`ShellExecRequest`] / [`AppActivationRequest`]），
//!    经 [`Host::defer_shell_exec`] / [`Host::defer_app_activation`] 暂存；
//! 2. **执行阶段**（借作用域之外）：生产实现用 `App::spawn` 把请求投到**前台
//!    执行器**——任务排入主线程消息队列（`PostMessageW`），在消息循环的独立回合
//!    运行，此刻无任何 App 借用，`ShellExecuteExW` 泵消息重入也能正常取到 App。
//!    执行结果日志（成功/PID/失败）在这一阶段落。
//!
//! `Command::spawn`（`CreateProcessW`）**不泵消息**，保持同步直启（纯 exe /
//! explorer 激活目录等路线行为不变）。隐藏窗口（`ShowWindow(SW_HIDE)`）沿用
//! 现有同步顺序，实测稳定。此范式与 `main.rs` 的 [`panel_toggle_win32`]
//! （commit `78d6290`，唤起时 4 条 ERROR 的同款修复）一致。

use voke_core::Action;

/// dispatcher 需要的宿主能力。
///
/// 生产实现是 [`gpui_kit::App`]；测试实现记录调用序列。
/// 只放"需要 UI/系统能力"的几件事，业务决策（该不该隐藏、该不该清空输入）
/// 留在本模块，测起来不依赖任何平台。
pub trait Host {
    /// 把文本写进系统剪贴板（gpui 的 `App::write_to_clipboard`）。
    fn copy_to_clipboard(&self, text: &str);
    /// 用默认程序打开 URL（gpui 的 `App::open_url`）。
    fn open_url(&self, url: &str);
    /// 隐藏 launcher 窗口（Win32 `ShowWindow(SW_HIDE)`，见 `voke-sys`）。
    fn hide_window(&self, hwnd: isize);
    /// 把 shell 关联启动请求**推迟到当前 gpui 更新之后**执行。
    ///
    /// 决策阶段（借作用域内）只许收集请求；`ShellExecuteExW` 会泵消息，
    /// 必须等更新结束后再跑，详见模块文档「两阶段纪律」。
    fn defer_shell_exec(&self, request: ShellExecRequest);
    /// 把 WT App Activation 请求**推迟到当前 gpui 更新之后**执行。
    ///
    /// 同上：`IApplicationActivationManager::ActivateApplication` 是跨进程
    /// 阻塞调用，同样不得在借作用域内执行。
    fn defer_app_activation(&self, request: AppActivationRequest);
}

/// 「shell 关联启动」请求（纯数据：决策阶段的产出、执行阶段的输入）。
///
/// 字段与 `ShellExecuteExW` 的入参一一对应：`lpFile` / `lpParameters` /
/// `lpDirectory`。目标解析 / 参数拼装 / 工作目录展开校验全部在决策阶段完成，
/// 执行阶段只负责"把请求交给 shell"。
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ShellExecRequest {
    /// `lpFile`：`.lnk` 本体 / URI / 非 PE 目标路径。
    pub target: String,
    /// `lpParameters`：可选的命令行参数串（`None` = 不传）。
    pub parameters: Option<String>,
    /// `lpDirectory`：可选的工作目录（调用方已展开 + 存在性校验）。
    pub working_dir: Option<String>,
}

/// 「WT App Activation」请求（纯数据）。
///
/// 执行阶段先试 `ActivateApplication`，失败回退 `wt.exe -p <profile>` 直启
/// （`Command::spawn`，不泵消息，回退本身也整体在执行阶段）。
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct AppActivationRequest {
    /// WT 包的 AUMID。
    pub aumid: String,
    /// 激活参数字串（`--profile "<guid>"`）。
    pub arguments: String,
    /// profile 标识（guid 或显示名，回退直启时作 `-p` 参数）。
    pub profile: String,
    /// 激活失败时的兜底直启路径（wt.exe 别名）。
    pub fallback_path: String,
}

/// 生产实现：gpui 的 `App`。
///
/// 剪贴板用 gpui 的 [`ClipboardItem::new_string`]，**不要**自己调 Win32 剪贴板：
/// gpui-pre-windows 已经把 `write_to_clipboard` 接到系统剪贴板
/// （`gpui-pre-windows-0.3.8/src/platform.rs:1012`，转调其 `clipboard.rs` 的
/// 实现），重复实现只会多一份不一致的路径。
impl Host for gpui_kit::App {
    fn copy_to_clipboard(&self, text: &str) {
        self.write_to_clipboard(gpui_kit::ClipboardItem::new_string(text.to_string()));
    }

    fn open_url(&self, url: &str) {
        // 全限定调用：本 trait 也有同名方法，不写全限定会解析到 trait 方法上（无限递归）。
        gpui_kit::App::open_url(self, url);
    }

    fn hide_window(&self, hwnd: isize) {
        voke_sys::hide_window(hwnd);
    }

    fn defer_shell_exec(&self, request: ShellExecRequest) {
        // 为什么用 `App::spawn`（gpui-pre-0.3.8 registry 源码核实，勿改）：
        // `App::spawn`（app.rs:2177）→ `ForegroundExecutor::spawn`
        // （executor.rs:354，注释即 "they run in order on the main thread"）
        // → `LocalExecutor::spawn`（gpui-pre-scheduler executor.rs:76）只
        // `runnable.schedule()` 入队、**不会同步 poll** →
        // `PlatformScheduler::schedule_local`（platform_scheduler.rs:109）
        // → `WindowsDispatcher::dispatch_on_main_thread`（gpui-pre-windows
        // dispatcher.rs:120）→ `PostMessageW(WM_GPUI_TASK_DISPATCHED_ON_MAIN_THREAD)`
        // → 主线程消息循环 `run_foreground_task`（platform.rs:1247）。
        // 即：任务在消息循环的独立回合、主线程、无任何 AppCell 借用时执行，
        // `ShellExecuteExW` 内部泵消息重入 wndproc 时 gpui 回调能正常取到 App。
        // （`cx.defer` 不可用：其 Effect 在 `flush_effects` 里跑，仍在
        // `AsyncApp::update_window` 持有的借作用域内，app.rs:1877/1257/1242。）
        //
        // Task 丢弃即取消，必须 detach。
        self.spawn(|_cx: &mut gpui_kit::AsyncApp| async move {
            run_shell_exec(request);
        })
        .detach();
    }

    fn defer_app_activation(&self, request: AppActivationRequest) {
        // 同 `defer_shell_exec`：投到前台执行器，在 App 借作用域之外执行。
        self.spawn(|_cx: &mut gpui_kit::AsyncApp| async move {
            run_app_activation(request);
        })
        .detach();
    }
}

/// **执行阶段**：真正调 `ShellExecuteExW`（由前台执行器在 App 借作用域之外调用）。
///
/// guard 日志是本函数的存在意义标记：这条 INFO 出现即说明启动执行确实发生在
/// 借作用域之外；若哪天它又出现在借作用域内（回归），日志时序会立刻暴露。
fn run_shell_exec(request: ShellExecRequest) {
    log::info!(
        "启动执行发生在 App 借作用域之外（defer 守卫）: lpFile={:?}",
        request.target
    );
    if voke_sys::shell_open(
        &request.target,
        request.parameters.as_deref(),
        request.working_dir.as_deref(),
    ) {
        log::info!(
            "已通过 shell 执行 {}（参数 {:?}，cwd {:?}）",
            request.target,
            request.parameters,
            request.working_dir
        );
    } else {
        log::error!(
            "通过 shell 执行 {} 失败（目标不存在或没有注册打开方式）",
            request.target
        );
    }
}

/// **执行阶段**：WT App Activation + 失败回退直启（同样在借作用域之外）。
fn run_app_activation(request: AppActivationRequest) {
    log::info!(
        "启动执行发生在 App 借作用域之外（defer 守卫）: App Activation aumid={}",
        request.aumid
    );
    log_child_env_snapshot();
    if let Some(pid) = voke_sys::activate_app(&request.aumid, &request.arguments) {
        log::info!(
            "已通过 App Activation 启动 WT profile {:?}（aumid={}，PID={pid}）",
            request.profile,
            request.aumid
        );
        return;
    }
    // WT 不是以 MSIX 包安装（解包版）时激活失败：退回别名直启（旧行为）。
    // `Command::spawn` 不泵消息，留在本阶段同步执行即可。
    log::warn!(
        "App Activation 启动失败，回退 wt.exe 直启: aumid={:?} profile={:?}",
        request.aumid,
        request.profile
    );
    spawn(
        &request.fallback_path,
        &["-p".to_string(), request.profile.clone()],
        &request.fallback_path,
        false,
        // 兜底直启不继承 voke 自身 cwd（安装包「立即运行」时 cwd 可能是安装程序
        // 目录），显式给一个与 voke 无关的安全目录，避免泄漏给 wt.exe → pwsh。
        &voke_sys::env::safe_fallback_working_dir(&request.fallback_path),
    );
}

/// 子进程环境快照（**脱敏** debug 日志，只在 WT profile 启动点打）。
///
/// **为什么需要**：`ActivateApplication` 不能带 cwd / 环境块，WT（及其拉起的
/// pwsh）最终拿到什么，取决于激活链路是否继承本进程上下文——2026-10-13 真机
/// 取证「pwsh `$PROFILE` 调 scoop shim 起 mise.exe 失败」时，日志里没有任何
/// 一条记录反映 voke 自己的 cwd / PATH，下一轮定位只能靠猜。这条日志把最小
/// 环境事实固定下来：cwd + PATH 条目数 + 两个关键目录的有无。
///
/// **脱敏约定**：cwd 打全路径（路径本身非敏感）；PATH **绝不整条打印**，
/// 只打条目数与 `scoop\shims` / `Microsoft\WindowsApps` 的有无。
fn log_child_env_snapshot() {
    let cwd = std::env::current_dir().ok();
    let path = std::env::var("PATH").unwrap_or_default();
    let entries: Vec<&str> = path.split(';').filter(|s| !s.trim().is_empty()).collect();
    let path_has = |suffix: &str| {
        entries.iter().any(|e| {
            e.trim()
                .trim_end_matches(['\\', '/'])
                .to_ascii_lowercase()
                .ends_with(&suffix.to_ascii_lowercase())
        })
    };
    log::debug!(
        "App Activation 环境快照（子进程若继承本进程上下文）: cwd={cwd:?} \
         PATH 条目数={} 含 scoop\\shims={} 含 WindowsApps={}",
        entries.len(),
        path_has(r"scoop\shims"),
        path_has(r"Microsoft\WindowsApps"),
    );
}

/// 隐藏 launcher 窗口（Esc / 执行后的统一出口）。
///
/// 保留这个自由函数供 Esc 路径直接用（那里拿不到 gpui `App`）。
pub fn hide_launcher(hwnd: Option<isize>) {
    if let Some(hwnd) = hwnd {
        voke_sys::hide_window(hwnd);
    }
}

/// 执行一个动作。返回是否真的产生了副作用（`false` 表示动作尚未实现或本就是空动作）。
///
/// `hwnd` 是 launcher 窗口句柄，用于启动/切换/复制前隐藏。
pub fn execute(action: &Action, hwnd: Option<isize>, host: &impl Host) -> bool {
    match action {
        Action::Launch { .. } => {
            launch(action, hwnd, host);
            true
        }
        // WT profile：App Activation（对齐 CmdPal），失败回退 wt.exe 直启。
        Action::LaunchTerminalProfile { .. } => {
            launch_terminal_profile(action, hwnd, host);
            true
        }
        Action::ActivateWindow { hwnd: target } => {
            activate(*target, hwnd, host);
            true
        }
        Action::Copy { text } => {
            copy(text, hwnd, host);
            true
        }
        Action::OpenUrl { url } => {
            open_url(url, hwnd, host);
            true
        }
        // 保持系统唤醒（M5）。`SetAwake` 是
        // `SetThreadExecutionState` 线程的启动/替换（`voke-sys/src/awake.rs`）。
        //
        // **结果落地后隐藏窗口**（与 `copy` 同款"设一下就走"）：状态已经生效，
        // 用户这一趟要办的事就完了——launcher 一次只干一件事，操作完留在屏幕上
        // 反而多一次手动 Esc。`CancelAwake` 同理。
        Action::SetAwake {
            minutes,
            keep_screen,
        } => {
            set_awake(*minutes, *keep_screen);
            if let Some(hwnd) = hwnd {
                host.hide_window(hwnd);
            }
            true
        }
        Action::CancelAwake => {
            cancel_awake();
            if let Some(hwnd) = hwnd {
                host.hide_window(hwnd);
            }
            true
        }
        // 重新生成 / 重新查询（"重掷"）。核心语义：**不隐藏面板、不改输入、不动路由**，
        // 只让上层视图重跑一次当前查询。dispatcher 本身没有任何系统副作用；返回 `true`
        // 是给 `confirm_index` 的信号——让它落到"重查当前文本"的分支（见那里对
        // `had_side_effect` 的用法），而不是当成空动作 `return` 掉。
        //
        // random 二级里 Enter「重新生成」走这里：输入与 sub_mode 保持不变，
        // 结果区重掷一批新值，面板不消失。
        Action::Refresh => true,
        // 纯展示型条目：什么都不做，**也不隐藏窗口**——用户可能只是想看结果
        // （例如 awake 的倒计时状态行，它是 `Interact::Hint`）。
        Action::None => false,
        other => {
            log::warn!("dispatcher: 动作尚未实现，忽略: {other:?}");
            false
        }
    }
}

/// 保持系统唤醒（M5）。
///
/// 真正的 Win32 调用在 `voke-sys/src/awake.rs` 的专用线程里（`SetThreadExecutionState`
/// 是 per-thread 语义，必须常驻线程持有）；这里只是转发 + 日志。非 Windows 平台上
/// sys 层是"记状态 + 打日志"的占位实现，所以这条路径在任何平台都安全。
fn set_awake(minutes: Option<u32>, keep_screen: bool) {
    let request = voke_sys::AwakeRequest {
        minutes,
        keep_screen,
    };
    log::info!(
        "保持唤醒：请求 {}（{}）",
        request.label(),
        if keep_screen {
            "系统 + 屏幕"
        } else {
            "仅系统"
        }
    );
    voke_sys::spawn_awake(request);
}

/// 取消保持唤醒（M5）。幂等：没在保持时也安全。
fn cancel_awake() {
    log::info!("保持唤醒：取消");
    voke_sys::cancel_awake();
}

/// 写剪贴板（M3 的 Copy）。
///
/// 顺序：**先写剪贴板再隐藏**。写剪贴板是同步的平台调用，必须先落地；
/// 隐藏会让 launcher 失焦，之后再有副作用就不受我们的控制了。
fn copy(text: &str, hwnd: Option<isize>, host: &impl Host) {
    host.copy_to_clipboard(text);
    log::info!("已复制到剪贴板：{} 字", text.chars().count());
    if let Some(hwnd) = hwnd {
        host.hide_window(hwnd);
    }
}

/// 用默认程序打开 URL（M3 的 OpenUrl）。
///
/// 顺序与 [`launch`] 一致：**先隐藏窗口再打开**。gpui 的 `open_url` 内部是
/// `ShellExecuteW`（`gpui-pre-windows-0.3.8/src/platform.rs:774` 起，走 background
/// executor），实际弹出浏览器发生在我们 hide 之后；先 hide 让行为确定，
/// 也避免 launcher 短暂盖在浏览器窗口上方。
fn open_url(url: &str, hwnd: Option<isize>, host: &impl Host) {
    if url.is_empty() {
        log::warn!("dispatcher: OpenUrl 收到空 URL，忽略");
        return;
    }
    if let Some(hwnd) = hwnd {
        host.hide_window(hwnd);
    }
    log::info!("用默认程序打开 URL: {url}");
    host.open_url(url);
}

/// 激活一个已打开的窗口（M2 的窗口切换）。
///
/// 顺序有意与 [`launch`] 一致：**先隐藏 launcher 再切目标窗口**：
/// launcher 是 `WindowKind::PopUp` 置顶窗口，不先隐藏的话会盖在目标窗口上方，
/// 看起来像"什么都没发生"。而且切换过程本身会让 launcher 失焦，
/// 失焦回调也会补一次 hide——先 hide 让行为确定（只有一次、在我们控制下）。
///
/// 目标窗口已关闭时 [`voke_sys::activate_window`] 返回 `false`：只记日志，
/// 不回退启动进程。回退需要窗口→启动命令的映射，而 provider 给的动作里只有 hwnd，
/// 拿不到进程路径（`WindowEntry.process_path` 没进 `Action`，因为 core 的
/// `Action` 要保持最小）。等真实使用中"切不到"成为高频抱怨再加。
fn activate(target: isize, launcher_hwnd: Option<isize>, host: &impl Host) {
    if let Some(hwnd) = launcher_hwnd {
        host.hide_window(hwnd);
    }
    if voke_sys::activate_window(target) {
        log::info!("已切换到窗口 hwnd={target}");
    } else {
        // 窗口可能刚被关闭，或前台锁定没抢下来（alt spoof 也没成功）。
        log::warn!("切换到窗口 hwnd={target} 失败（窗口可能已关闭，或前台锁定）");
    }
}

/// 启动目标的形态。与 `voke_sys::TargetKind` 一一对应，但**不依赖 voke-sys**：
/// dispatcher 在非 Windows 平台也要能编译（`voke-sys` 是 windows-only 依赖）。
///
/// 判别只看 `path` 的字符串前缀，所以能在任何平台测试。
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum LaunchKind {
    /// MSIX/Store 应用：`msix://<AUMID>`。
    Msix,
    /// 特殊外壳项：`shell:::{GUID}` 或 `shell:AppsFolder\...`。
    Shell,
    /// 普通文件/目录路径。
    Path,
}

/// `msix://` 前缀（与 `voke_sys::apps::MSIX_PREFIX` 相同，这里是本地常量以避免
/// dispatcher 依赖 sys 层）。
pub const MSIX_PREFIX: &str = "msix://";

/// 按 `path` 判别启动方式。
pub fn launch_kind(path: &str) -> LaunchKind {
    if path.starts_with(MSIX_PREFIX) {
        LaunchKind::Msix
    } else if path.starts_with("shell:") {
        LaunchKind::Shell
    } else {
        LaunchKind::Path
    }
}

/// 启动一个可执行文件 / 文档 / 目录 / MSIX 应用 / 特殊外壳项 / `.lnk` 快捷方式。
///
/// 顺序是有意的：**先隐藏窗口，再创建进程**。这是 launcher 的惯例
/// （PowerToys Run 同款），也是本项目唯一"隐藏先于进程创建"的审计点。
/// 反过来（先 spawn 再 hide）会让新进程先看到一个还盖在屏幕上的 launcher，
/// 且失焦回调可能再补一次 hide，行为不确定。
///
/// `working_dir` 来自 [`Action::Launch::working_dir`](voke_core::Action)（lnk 的
/// "起始位置"，解析期已展开）。MSIX / 外壳项分支交给 shell 激活、用不上它；
/// 普通路径分支由 [`spawn`] 做"展开 → 校验存在 → 不合法不设"的最终把关
/// （见 [`voke_sys::env::prepare_working_dir`]）。
///
/// `no_console` 来自 [`Action::Launch::no_console`](voke_core::Action)，逐动作决定
/// 是否给子进程加 `CREATE_NO_WINDOW`（见 [`spawn`]）。**不是全局开关**：
/// apps 插件启动命令提示符/PowerShell 时它是 `false`，窗口照常出现。
///
/// `lnk_path` 来自 [`Action::Launch::lnk_path`](voke_core::Action)：非空表示条目
/// 来自 `.lnk`，[`launch_path`] 会把快捷方式本体交 shell 执行（双击同款）。
///
/// 收 `&Action` 而不是 8 个平铺参数：启动决策的全部输入本就在动作里，
/// 平铺只会多一处"字段漂移"风险（clippy 的 too_many_arguments 也是这么看的）。
fn launch(action: &Action, hwnd: Option<isize>, host: &impl Host) {
    // execute 已判过变体；这里的 else 只是让编译器满意，不应当发生。
    let Action::Launch {
        path,
        args,
        working_dir,
        elevated,
        no_console,
        lnk_path,
    } = action
    else {
        return;
    };

    if *elevated {
        // §5 的 runas verb 走 ShellExecuteExW，属于 voke-sys 的后续工作。
        log::warn!("dispatcher: elevated 启动尚未实现，降级为普通启动: {path}");
    }

    if let Some(hwnd) = hwnd {
        host.hide_window(hwnd);
    }

    let kind = launch_kind(path);
    log::debug!("启动决策: kind={kind:?} path={path:?} lnk_path={lnk_path:?}");
    match kind {
        // MSIX/Store 应用：`msix://<AUMID>` -> `explorer.exe shell:AppsFolder\<AUMID>`。
        //
        // 为什么走 explorer 而不直接跑 exe：MSIX 应用的 exe 在
        // `C:\Program Files\WindowsApps\<pkg>\...`，路径带缓存版本号且受 ACL 限制，
        // 直接 spawn 会失败或绕过包身份（拿不到正确 AUMID/激活）。交给 shell
        // 激活是官方路径。
        //
        // 实测（2026-10）：`explorer.exe shell:AppsFolder\Microsoft.WindowsTerminal_8wekyb3d8bbwe!App`
        // 成功拉起 WindowsTerminal 进程（0 -> 1）。
        LaunchKind::Msix => {
            // 剥掉 `msix://` 得裸 AUMID。shell 激活不看工作目录（working_dir 忽略）。
            let aumid = &path[MSIX_PREFIX.len()..];
            // explorer 是 GUI 子系统，没有控制台要藏。
            spawn(
                "explorer.exe",
                &[format!("shell:AppsFolder\\{aumid}")],
                path,
                false,
                "",
            );
        }
        // 特殊外壳项：`shell:::{GUID}`。实测 `explorer.exe "shell:::{52205FD8-...}"`
        // 能打开"主文件夹"窗口（也就是常说的"此电脑"）。
        //
        // 注意 `shell:::` 里的 `::` 与 GUID 的花括号都是 explorer 的参数语法细节，
        // 必须原样传递（整条作为一个参数，不要拆）。
        LaunchKind::Shell => {
            // `shell:::{GUID}` 必须整条作为一个参数传给 explorer（不要拆）。
            let arg = path.clone();
            spawn("explorer.exe", &[arg], path, false, "");
        }
        LaunchKind::Path => launch_path(path, args, working_dir, *no_console, lnk_path, host),
    }
}

/// 启动 Windows Terminal 的某个 profile：App Activation（CmdPal 同款），
/// 失败回退 wt.exe 直启。
///
/// 顺序与 [`launch`] 一致：**先隐藏窗口再激活**。激活不可带工作目录 /
/// `CREATE_NO_WINDOW`（WT 自己管窗口），所以这两个标记在这里用不上。
///
/// **两阶段**：决策（参数拼装 + debug 日志）在借作用域内完成，激活本身
/// （`ActivateApplication`，跨进程阻塞调用）经 [`Host::defer_app_activation`]
/// 推迟到借作用域之外执行，回退直启随执行阶段一起。
fn launch_terminal_profile(action: &Action, hwnd: Option<isize>, host: &impl Host) {
    // execute 已判过变体；这里的 else 只是让编译器满意，不应当发生。
    let Action::LaunchTerminalProfile {
        aumid,
        profile,
        fallback_path,
    } = action
    else {
        return;
    };
    if let Some(hwnd) = hwnd {
        host.hide_window(hwnd);
    }
    let arguments = voke_core::terminal::profile_activation_arguments(profile);
    log::debug!(
        "WT profile 启动决策: App Activation aumid={aumid:?} arguments={arguments:?} \
         （失败回退 {fallback_path:?} -p {profile:?}）"
    );
    host.defer_app_activation(AppActivationRequest {
        aumid: aumid.clone(),
        arguments,
        profile: profile.clone(),
        fallback_path: fallback_path.clone(),
    });
}

/// 启动普通路径。路线由 [`launch_route`] 决策（纯函数，可测）：
///
/// - **Shell**：`.lnk` 本体（双击同款，shell 链接处理器负责一切）、URI
///   （scheme 关联）、非 PE 镜像的可启动目标（`.msc` / `.cpl` / `.bat` …）——
///   全部 `ShellExecuteExW`（[`voke_sys::shell_open`]，**经
///   [`Host::defer_shell_exec`] 推迟到 App 借作用域之外执行**）；
/// - **Explorer**：目录（`Command::new("C:\dir")` 在 Windows 上会
///   `ERROR_ACCESS_DENIED`，目录不是可执行文件）；
/// - **Direct**：纯 `.exe` / 无扩展名裸命令，`CreateProcessW` 直启快路径
///   （不泵消息，同步执行，行为不变）。
///
/// 入口先做一次**防御性环境变量展开**（`voke_sys::env::expand_env_path`）：
/// sys 层解析 `.lnk` 时已展开目标路径，但任何带 `%VAR%` 的新入口
/// （未来配置项、终端 commandline 等）都会漏到这条路上——在这里统一展开一次，
/// 保证后续 `is_dir` / 扩展名判定 / `CreateProcessW` 看到的都是真实路径。
fn launch_path(
    path: &str,
    args: &[String],
    working_dir: &str,
    no_console: bool,
    lnk_path: &str,
    host: &impl Host,
) {
    let exe = voke_sys::env::expand_env_path(path);
    if exe != path {
        log::info!("启动目标已展开环境变量: {path:?} -> {exe:?}");
    }
    let is_dir = std::path::Path::new(&exe).is_dir();
    let route = launch_route(&exe, lnk_path, is_dir);
    log::debug!("启动路线决策: route={route:?} path={exe:?} lnk_path={lnk_path:?} is_dir={is_dir}");
    match route {
        LaunchRoute::Explorer => {
            log::info!("目标是目录，交给 explorer.exe 打开: {exe}");
            spawn("explorer.exe", &[exe], path, false, "");
        }
        LaunchRoute::Shell => {
            // `.lnk` 来源：lnk 本体是 lpFile，**不传参数**——lnk 里存的目标 /
            // 参数 / 起始位置由 shell 链接处理器负责（再传一遍解析出的 args
            // 会重复）。其余 shell 目标（.msc / URI…）才拼 lpParameters。
            let lnk_expanded =
                (!lnk_path.is_empty()).then(|| voke_sys::env::expand_env_path(lnk_path));
            let (target, params) = match &lnk_expanded {
                Some(lnk) => {
                    log::info!(
                        "条目来自 .lnk，把快捷方式本体交给 shell 执行: {lnk}（不再解析直启 {exe}）"
                    );
                    (lnk.as_str(), None)
                }
                None => (exe.as_str(), join_shell_parameters(args)),
            };
            // 关联启动同样带上展开+校验后的工作目录（ShellExecuteExW 的
            // lpDirectory 不做 %VAR% 展开，传入前必须保证是真实目录）。
            let dir = voke_sys::env::prepare_working_dir(working_dir);
            log::debug!(
                "shell 执行参数: lpFile={target:?} lpParameters={params:?} \
                 lpDirectory 原始={working_dir:?} 最终={dir:?}"
            );
            // **两阶段**：到这里决策就结束了（目标/参数/cwd 全部落定）。真正的
            // `ShellExecuteExW` 会泵消息，必须在 App 借作用域之外执行——只把纯
            // 数据请求交给宿主暂存，执行（含结果日志）发生在 defer 之后。
            // 用户实测崩溃根因：旧代码在这里同步调 `shell_open`，嵌套消息泵重入
            // gpui wndproc → 硬 borrow_mut panic → extern "C" 不可 unwind → abort。
            host.defer_shell_exec(ShellExecRequest {
                target: target.to_string(),
                parameters: params,
                working_dir: dir,
            });
        }
        LaunchRoute::Direct => spawn(&exe, args, path, no_console, working_dir),
    }
}

/// `launch_path` 的路线决策。**纯函数**（任意平台可测）：不碰文件系统，
/// 只按 `path` / `lnk_path` / `is_dir`（由调用方判定）分流。
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum LaunchRoute {
    /// 目录：交 explorer.exe。
    Explorer,
    /// shell 执行（`ShellExecuteExW`）：`.lnk` 本体 / URI / 非 PE 可启动目标。
    Shell,
    /// `CreateProcessW` 直启（纯 exe / 裸命令）。
    Direct,
}

fn launch_route(path: &str, lnk_path: &str, is_dir: bool) -> LaunchRoute {
    // ① `.lnk` 来源：快捷方式本体交 shell——双击同款语义（shell 链接处理器
    //    负责目标解析 / 参数 / 起始位置 / 环境变量展开，见 env.rs 模块文档的
    //    "主流启动器共识"一节）。解析出的 target/args 只供副标题与日志用。
    if !lnk_path.is_empty() {
        return LaunchRoute::Shell;
    }
    // ② 目录：CreateProcessW 吃不下（ERROR_ACCESS_DENIED）。
    if is_dir {
        return LaunchRoute::Explorer;
    }
    // ③ URI（`scheme:…`，如 `https://` / `ms-settings:`）：shell 按协议
    //    注册表打开，CreateProcessW 同样吃不下。
    if looks_like_uri(path) {
        return LaunchRoute::Shell;
    }
    // ④ 非 PE 镜像的可启动目标（.msc / .cpl / .bat / .lnk …）。
    if needs_shell_association(path) {
        return LaunchRoute::Shell;
    }
    LaunchRoute::Direct
}

/// 路径是不是 URI（`scheme:...`）——shell 按协议关联打开，`CreateProcessW` 吃不下。
///
/// 判定：第一个 `:` 之前是 ≥2 个 ASCII 字母/数字/`+`/`-`/`.` 的 scheme，
/// 且**排除盘符**（`C:\` 的单字母 scheme 是 Windows 路径，不是 URI）。
/// 纯函数，任意平台可测。
fn looks_like_uri(path: &str) -> bool {
    let Some(colon) = path.find(':') else {
        return false;
    };
    let scheme = &path[..colon];
    // 盘符（C: / d:）不是 URI；`shell:::` / `msix://` 在到达这里之前就被
    // LaunchKind 分派掉了，走到这里的 URI 至少 2 字符（http / ms-settings / steam…）。
    scheme.len() >= 2
        && scheme
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || matches!(b, b'+' | b'-' | b'.'))
}

/// `CreateProcessW`（`std::process::Command`）能直接加载的扩展名——这些目标
/// 继续走 [`spawn`]，**行为不回退**。
///
/// 取的是 `voke_sys::apps::EXECUTABLE_EXTS`（与 PowerToys Run 对齐的
/// "可启动扩展名白名单"）里"确实是可执行镜像"的部分：`exe` / `com` / `bin`
/// 是镜像本体，`pif`（程序信息文件）与 `scr`（屏保，PE 改名）CreateProcessW
/// 自己就会解析。白名单里其余扩展名（`msc` / `cpl` / `bat` / `cmd` / `ps1` /
/// `msi` / 脚本类…）都不是镜像，全部改走文件关联。
const DIRECT_EXECUTABLE_EXTS: &[&str] = &["exe", "com", "bin", "pif", "scr"];

/// 目标是否需要走 shell 文件关联（[`voke_sys::shell_open`]）启动。
///
/// 纯函数、任意平台可测：只做扩展名判定，不碰文件系统（目录在
/// [`launch_route`] 里先行处理，裸命令的 PATH 查找是 `spawn` 的职责）。
/// 扩展名解析复用 `voke_sys::apps::target_extension`（大小写不敏感），
/// 与 sys 层收录条目时的判定同源，避免两边规则漂移。
fn needs_shell_association(path: &str) -> bool {
    match voke_sys::apps::target_extension(path) {
        Some(ext) => {
            // `.lnk` 不是可执行镜像（CreateProcessW 会 ERROR_BAD_EXE_FORMAT）：
            // 交 shell 的链接处理器（资源管理器双击 .lnk 同款）。apps provider
            // 的条目走 lnk_path 分支；这里兜住"目标本身就是 .lnk"的入口
            // （嵌套快捷方式等）。
            ext == "lnk"
                || (voke_sys::apps::EXECUTABLE_EXTS.contains(&ext.as_str())
                    && !DIRECT_EXECUTABLE_EXTS.contains(&ext.as_str()))
        }
        // 无扩展名：裸命令（`ping`）或目录，CreateProcessW 能处理。
        None => false,
    }
}

/// 把参数数组拼成 `ShellExecuteW` 的单个 `lpParameters` 串。
///
/// 纯函数（任意平台可测）。CreateProcess 侧 Rust 的 `Command` 会自己做 argv
/// 引号转义；shell 侧没有这层，这里按"含空白的参数加引号"的最小规则拼。
/// apps provider 的条目基本不带参数（带参数的是 `wt.exe` / `cmd.exe`，都走
/// exe 直启路径），所以这条主要兜底 `.lnk` 里存了参数的关联型目标。
fn join_shell_parameters(args: &[String]) -> Option<String> {
    if args.is_empty() {
        return None;
    }
    let mut out = String::new();
    for (i, arg) in args.iter().enumerate() {
        if i > 0 {
            out.push(' ');
        }
        if arg.is_empty() || arg.contains([' ', '\t']) {
            out.push('"');
            out.push_str(arg);
            out.push('"');
        } else {
            out.push_str(arg);
        }
    }
    Some(out)
}

/// Windows 的 `CREATE_NO_WINDOW`（`winbase.h`）：进程带这个 flag 启动时
/// 不分配控制台窗口——用来消掉"为跑批处理而套的 `cmd.exe /c`"闪出的黑框。
const CREATE_NO_WINDOW: u32 = 0x0800_0000;

/// 由动作的 `no_console` 标记决定 spawn 时给子进程的 `creation_flags`。
///
/// 抽成**纯函数**是为了能在任意平台单测这段决策（Linux 上也能断言"哪个标记
/// 对应哪个 flag"）；真正的 `creation_flags` 调用只在 `#[cfg(windows)]` 里发生。
/// `None` = 不加任何 flag（默认，窗口照常出现）。
fn console_creation_flags(no_console: bool) -> Option<u32> {
    no_console.then_some(CREATE_NO_WINDOW)
}

/// 统一的 spawn + 日志（`display` 只用于日志，区分"启动谁"与"实际执行什么"）。
///
/// `no_console` 为 `true` 时（且平台是 Windows）加 `CREATE_NO_WINDOW`，把子进程
/// 的控制台窗口藏掉。非 Windows 平台没有这个概念，参数被有意忽略。
///
/// **工作目录**（`working_dir` 是 `.lnk` 起始位置，解析期已展开、未校验）：
/// 交给 [`voke_sys::env::prepare_working_dir`] 做"展开 → `is_dir` 校验"，
/// 返回 `Some` 才 `current_dir`；`None`（未声明 / 展开后不存在）**不设置**，
/// 子进程继承 voke 的默认 cwd——绝不把字面量 `%USERPROFILE%` 或死路径传给
/// `CreateProcessW`（Windows 不展开 lpCurrentDirectory，传了就是非法 cwd，
/// 实测 alacritty 会报 `Invalid working directory`）。
///
/// **环境变量**：子进程完整继承父进程环境（std 默认，全仓**禁用 `env_clear`**）。
/// `PATH` / `USERPROFILE` / `APPDATA` 等都是子程序（尤其 scoop shim、shell、
/// 脚本解释器）的必需品，筛选只会制造"在启动器里启动就异常"的玄学问题；
/// 有特殊注入需求用 `envs` 追加，不要做减法。
fn spawn(exe: &str, args: &[String], display: &str, no_console: bool, working_dir: &str) {
    let cwd = voke_sys::env::prepare_working_dir(working_dir);
    // 决策点日志：cwd 的原始声明值 vs 最终采用值（None = 校验失败回退不设）。
    // 跨启动追加的 voke.log 是"启动目录不对"类反馈的唯一定位线索。
    log::debug!("spawn 决策: exe={exe:?} args={args:?} cwd 原始={working_dir:?} 最终={cwd:?}");
    let mut command = std::process::Command::new(exe);
    command.args(args);
    if let Some(dir) = &cwd {
        command.current_dir(dir);
    }
    #[cfg(windows)]
    if let Some(flags) = console_creation_flags(no_console) {
        // `creation_flags` 来自 `std::os::windows::process::CommandExt`，
        // 与 `args` 可链式叠加（它返回 `&mut Command`）。
        use std::os::windows::process::CommandExt;
        command.creation_flags(flags);
    }
    #[cfg(not(windows))]
    {
        // 非 Windows 无控制台子系统之分，flag 用不上；仍消费一次决策函数，
        // 让它在非 Windows 构建里也被"使用"（避免 dead_code 警告）。
        let _ = console_creation_flags(no_console);
    }
    match command.spawn() {
        Ok(child) => log::info!(
            "已启动 {display}（实际命令 {exe} {args:?}，PID={}，cwd {cwd:?}）",
            child.id()
        ),
        Err(err) => log::error!("启动 {display} 失败（{exe} {args:?}，cwd {cwd:?}）: {err}"),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::cell::RefCell;

    /// 测试用的假宿主：把每个副作用记成一条字符串，方便断言"发生了什么、顺序如何"。
    ///
    /// 只有这个假实现在测试里被用到，所以不必关心平台——Windows 上跑
    /// `cargo test -p voke-app` 就能覆盖这些决策（Linux 原生构造不出 gpui `App`）。
    #[derive(Default)]
    struct FakeHost {
        calls: RefCell<Vec<String>>,
    }

    impl FakeHost {
        fn calls(&self) -> Vec<String> {
            self.calls.borrow().clone()
        }
    }

    impl Host for FakeHost {
        fn copy_to_clipboard(&self, text: &str) {
            self.calls.borrow_mut().push(format!("copy:{text}"));
        }
        fn open_url(&self, url: &str) {
            self.calls.borrow_mut().push(format!("open_url:{url}"));
        }
        fn hide_window(&self, hwnd: isize) {
            self.calls.borrow_mut().push(format!("hide:{hwnd}"));
        }
        fn defer_shell_exec(&self, request: ShellExecRequest) {
            // 只记录"被登记为延迟执行"这一事实（含目标）：执行阶段在真宿主
            // （gpui App + 前台执行器）上发生，测试环境不存在。
            self.calls
                .borrow_mut()
                .push(format!("defer_shell:{}", request.target));
        }
        fn defer_app_activation(&self, request: AppActivationRequest) {
            self.calls
                .borrow_mut()
                .push(format!("defer_activation:{}", request.aumid));
        }
    }

    const HWND: Option<isize> = Some(4242);

    /// **逐动作隐藏控制台**的决策：只有 `no_console=true` 才拿到
    /// `CREATE_NO_WINDOW`，默认（`false`）不加任何 flag——apps 插件启动
    /// cmd/pwsh 的窗口才不会被误伤。纯函数，任意平台可测（不真的 spawn）。
    #[test]
    fn console_flag_is_opt_in_and_defaults_to_none() {
        assert_eq!(console_creation_flags(true), Some(CREATE_NO_WINDOW));
        assert_eq!(console_creation_flags(false), None);
        // 常量必须是 Win32 的 `CREATE_NO_WINDOW`（0x08000000），别写错位。
        assert_eq!(CREATE_NO_WINDOW, 0x0800_0000);
    }

    /// 三种启动形态的判别（dispatcher 分派的唯一输入）。
    #[test]
    fn launch_kind_discriminates_all_three_forms() {
        assert_eq!(
            launch_kind("msix://Microsoft.WindowsTerminal_8wekyb3d8bbwe!App"),
            LaunchKind::Msix
        );
        assert_eq!(
            launch_kind("shell:::{52205FD8-5DFB-447D-801A-D0B52F2E83E1}"),
            LaunchKind::Shell
        );
        assert_eq!(
            launch_kind(r"C:\Program Files\VideoLAN\VLC\vlc.exe"),
            LaunchKind::Path
        );
        assert_eq!(launch_kind(r"C:\Users\testuser\Tools"), LaunchKind::Path);
        // `shell:AppsFolder\...` 也是 shell 形态
        assert_eq!(
            launch_kind("shell:AppsFolder\\Microsoft.WindowsCalculator_8wekyb3d8bbwe!App"),
            LaunchKind::Shell
        );
    }

    /// **文件关联判定的核心矩阵**：CreateProcessW 能直接加载的目标（exe /
    /// 裸命令 / 未知扩展名）保持 `spawn` 不走关联；白名单里的非镜像目标
    /// （.msc / .cpl / .bat / .cmd / .ps1 / .msi / 脚本类 / .lnk）必须走
    /// `ShellExecuteExW`——这就是 services.msc 起不来的修复面。
    #[test]
    fn shell_association_covers_non_pe_targets_only() {
        // 必须走文件关联（CreateProcessW 会 ERROR_BAD_EXE_FORMAT 的类型）。
        for p in [
            r"C:\Windows\System32\services.msc",
            r"C:\Windows\System32\appwiz.cpl",
            r"C:\a\run.bat",
            r"C:\a\run.cmd",
            r"C:\a\s.ps1",
            r"C:\a\setup.msi",
            r"C:\a\s.vbs",
            r"C:\a\s.appref-ms",
            // .lnk 不是可执行镜像：交 shell 链接处理器（双击 .lnk 同款）。
            r"C:\Users\x\Desktop\Tool.lnk",
            r"C:\a\s.LNK",
            "services.msc",
        ] {
            assert!(needs_shell_association(p), "应走文件关联: {p}");
        }
        // 保持 CreateProcessW 直启（行为不回退）：exe、无扩展名裸命令、
        // 未知扩展名（不在白名单，维持现状），以及 pif / scr 这类
        // CreateProcessW 自己能解析的镜像家族。
        for p in [
            r"C:\Program Files\VideoLAN\VLC\vlc.exe",
            "wt.exe",
            "ping",
            r"C:\a\thing.xyz",
            r"C:\a\s.pif",
            r"C:\a\s.scr",
        ] {
            assert!(!needs_shell_association(p), "应保持直启: {p}");
        }
        // 扩展名大小写不敏感（`target_extension` 统一小写化）。
        assert!(needs_shell_association(r"C:\a\SERVICES.MSC"));
        assert!(!needs_shell_association(r"C:\a\VLC.EXE"));
    }

    /// 路线决策矩阵：`.lnk` 来源（lnk_path 非空）一律走 shell 本体；
    /// 否则目录 → explorer、URI / 非 PE 目标 → shell、纯 exe → 直启。
    #[test]
    fn launch_route_covers_all_four_branches() {
        // ① .lnk 来源：即使目标是 exe、参数齐全，也把 lnk 本体交 shell。
        assert_eq!(
            launch_route(r"C:\a\alacritty.exe", r"C:\d\Alacritty.lnk", false),
            LaunchRoute::Shell,
            ".lnk 来源必须走 shell 本体（双击同款），不解析直启"
        );
        // ② 目录（优先级低于 lnk、高于 URI/扩展名判定）。
        assert_eq!(
            launch_route(r"C:\Users\x\Tools", "", true),
            LaunchRoute::Explorer
        );
        assert_eq!(
            launch_route(r"C:\a\services.msc", "", false),
            LaunchRoute::Shell
        );
        assert_eq!(
            launch_route("https://example.com", "", false),
            LaunchRoute::Shell
        );
        assert_eq!(
            launch_route("ms-settings:display", "", false),
            LaunchRoute::Shell
        );
        assert_eq!(
            launch_route(r"C:\a\vlc.exe", "", false),
            LaunchRoute::Direct
        );
        assert_eq!(launch_route("ping", "", false), LaunchRoute::Direct);
        assert_eq!(
            launch_route(r"C:\a\thing.xyz", "", false),
            LaunchRoute::Direct
        );
    }

    /// URI 判定：scheme ≥2 字符且全是 scheme 字符；**盘符不是 URI**
    /// （`C:\…` 必须留在直启路线上，误判成 URI 会让普通 exe 走 shell）。
    #[test]
    fn looks_like_uri_excludes_drive_letters() {
        for uri in [
            "https://example.com",
            "http://a.b/c?d=1",
            "ms-settings:display",
            "steam://run/123",
            "mailto:a@b.c",
            "obsidian://open?vault=x",
        ] {
            assert!(looks_like_uri(uri), "应识别为 URI: {uri}");
        }
        for path in [
            r"C:\Windows\notepad.exe",
            r"d:\a\b.exe",
            r"\\server\share\x.exe",
            r"C:\a\thing.xyz",
            "ping",
            "",
            "no-colon-here",
        ] {
            assert!(!looks_like_uri(path), "不应识别为 URI: {path}");
        }
    }

    /// 参数拼装：空数组 → `None`（不传 lpParameters）；含空白的参数加引号。
    #[test]
    fn shell_parameters_join_quotes_whitespace() {
        assert_eq!(join_shell_parameters(&[]).as_deref(), None);
        assert_eq!(
            join_shell_parameters(&["a".into(), "b".into()]).as_deref(),
            Some("a b")
        );
        assert_eq!(
            join_shell_parameters(&["a b".into(), "c".into()]).as_deref(),
            Some("\"a b\" c")
        );
        assert_eq!(
            join_shell_parameters(&[String::new()]).as_deref(),
            Some("\"\"")
        );
    }

    /// **回归防线（P0 崩溃修复）**：`.lnk` 条目的启动决策在借作用域内完成
    /// （先隐藏、再登记请求），`ShellExecuteExW` **不在 `execute` 内联执行**——
    /// 只登记一条 `defer_shell`（纯数据请求），执行被推迟到借作用域之外。
    /// 旧代码在这里同步调 `shell_open`：其内部消息泵重入 gpui wndproc，
    /// 硬 `borrow_mut` panic 出在 extern "C" 里不可 unwind → 进程 abort。
    #[test]
    fn lnk_launch_defers_shell_execution_instead_of_running_inline() {
        let host = FakeHost::default();
        assert!(execute(
            &Action::Launch {
                path: r"C:\Users\x\scoop\apps\alacritty\0.17.0\alacritty.exe".into(),
                args: vec!["--working-directory".into(), r"C:\Users\x".into()],
                working_dir: r"C:\Users\x\scoop\apps\alacritty\0.17.0".into(),
                elevated: false,
                no_console: false,
                lnk_path: r"C:\Users\x\AppData\Roaming\Microsoft\Windows\Start Menu\Alacritty.lnk"
                    .into(),
            },
            HWND,
            &host
        ));
        assert_eq!(
            host.calls(),
            vec![
                "hide:4242".to_string(),
                // lpFile 是 lnk 本体（双击同款），不是解析出的 exe。
                format!(
                    "defer_shell:{}",
                    r"C:\Users\x\AppData\Roaming\Microsoft\Windows\Start Menu\Alacritty.lnk"
                ),
            ],
            "Launch 必须只做决策 + 登记延迟执行，不得内联跑 ShellExecuteExW"
        );
    }

    /// 同上的非 PE 目标形态（`.msc` / URI 同理）：决策同步、执行延迟。
    #[test]
    fn non_pe_target_defers_shell_execution() {
        let host = FakeHost::default();
        assert!(execute(
            &Action::launch_with(r"C:\Windows\System32\services.msc".to_string(), vec![]),
            HWND,
            &host
        ));
        assert_eq!(
            host.calls(),
            vec![
                "hide:4242".to_string(),
                format!("defer_shell:{}", r"C:\Windows\System32\services.msc"),
            ]
        );
    }

    /// URI 条目同样只登记延迟执行（`ms-settings:` 等协议走 shell 关联）。
    #[test]
    fn uri_target_defers_shell_execution() {
        let host = FakeHost::default();
        assert!(execute(
            &Action::launch_with("ms-settings:display".to_string(), vec![]),
            HWND,
            &host
        ));
        assert_eq!(
            host.calls(),
            vec![
                "hide:4242".to_string(),
                "defer_shell:ms-settings:display".to_string()
            ]
        );
    }

    /// WT profile：App Activation（跨进程阻塞调用）同样不得在借作用域内执行——
    /// 只登记 `defer_activation`，激活与回退直启整体推迟。
    #[test]
    fn terminal_profile_defers_app_activation() {
        let host = FakeHost::default();
        assert!(execute(
            &Action::LaunchTerminalProfile {
                aumid: "Microsoft.WindowsTerminal_8wekyb3d8bbwe!App".into(),
                profile: "{574e775e-4f2a-5b96-ac1e-a2962a402336}".into(),
                fallback_path: r"C:\a\wt.exe".into(),
            },
            HWND,
            &host
        ));
        assert_eq!(
            host.calls(),
            vec![
                "hide:4242".to_string(),
                "defer_activation:Microsoft.WindowsTerminal_8wekyb3d8bbwe!App".to_string(),
            ]
        );
    }

    /// 剥 `msix://` 前缀后必须得到**裸 AUMID**：dispatcher 要把它拼进
    /// `shell:AppsFolder\<AUMID>`，多一个字符都会启动失败。
    #[test]
    fn msix_prefix_strips_to_bare_aumid() {
        let path = "msix://Microsoft.WindowsTerminal_8wekyb3d8bbwe!App";
        assert_eq!(launch_kind(path), LaunchKind::Msix);
        let aumid = &path[MSIX_PREFIX.len()..];
        assert_eq!(aumid, "Microsoft.WindowsTerminal_8wekyb3d8bbwe!App");
        assert!(!aumid.contains("msix"));
        assert_eq!(
            format!("shell:AppsFolder\\{aumid}"),
            "shell:AppsFolder\\Microsoft.WindowsTerminal_8wekyb3d8bbwe!App"
        );
    }

    /// **M3 的核心行为**：Copy 先写剪贴板、再隐藏窗口（顺序不能反）。
    #[test]
    fn copy_writes_clipboard_then_hides() {
        let host = FakeHost::default();
        assert!(execute(&Action::Copy { text: "42".into() }, HWND, &host));
        assert_eq!(host.calls(), vec!["copy:42", "hide:4242"]);
    }

    /// 没有 HWND（非 Windows 构建/取句柄失败）时仍然要写剪贴板，不能丢副作用。
    #[test]
    fn copy_without_hwnd_still_writes_clipboard() {
        let host = FakeHost::default();
        assert!(execute(&Action::Copy { text: "x".into() }, None, &host));
        assert_eq!(host.calls(), vec!["copy:x"]);
    }

    /// OpenUrl：先隐藏、再 open_url（与 launch 同款审计点）。
    #[test]
    fn open_url_hides_then_opens() {
        let host = FakeHost::default();
        assert!(execute(
            &Action::OpenUrl {
                url: "https://example.com".into()
            },
            HWND,
            &host
        ));
        assert_eq!(
            host.calls(),
            vec!["hide:4242", "open_url:https://example.com"]
        );
    }

    /// 空 URL 不做任何副作用（避免 `ShellExecuteW("")` 弹错误对话框）。
    #[test]
    fn open_url_with_empty_url_is_a_noop() {
        let host = FakeHost::default();
        assert!(execute(
            &Action::OpenUrl { url: String::new() },
            HWND,
            &host
        ));
        assert!(host.calls().is_empty());
    }

    /// `Action::None` 是纯展示项：不隐藏窗口、不产生任何副作用，
    /// 返回值必须是 `false`（`SearchView::confirm` 据此决定不清空输入）。
    #[test]
    fn none_action_does_nothing_and_does_not_hide() {
        let host = FakeHost::default();
        assert!(!execute(&Action::None, HWND, &host));
        assert!(host.calls().is_empty());
    }

    /// `Action::Refresh` 是"重掷"信号：**没有任何系统副作用、不隐藏窗口**，
    /// 但必须返回 `true`——`confirm_index` 用这个返回值区分"落到重查分支"与
    /// "空动作直接 return"，random 的重新生成全靠它。
    #[test]
    fn refresh_reports_signal_without_side_effects() {
        let host = FakeHost::default();
        assert!(execute(&Action::Refresh, HWND, &host));
        assert!(
            host.calls().is_empty(),
            "Refresh 不该有系统副作用，实际 {:?}",
            host.calls()
        );
    }

    /// 尚未实现的动作返回 `false`，且不留任何副作用。
    #[test]
    fn unimplemented_actions_report_no_side_effect() {
        let host = FakeHost::default();
        // `Reveal` 还没接线（§5 的 reveal_path 是后续工作）。
        assert!(!execute(
            &Action::Reveal {
                path: r"C:\a".into()
            },
            HWND,
            &host
        ));
        assert!(host.calls().is_empty());
    }

    /// 保持唤醒的动作：状态落地后**隐藏窗口**（与其他有副作用的动作一致，
    /// 用户这一趟只干一件事）。这里只断言**决策**（产生了 hide）——真正的
    /// `SetThreadExecutionState` 在 voke-sys 里，非 Windows 平台是占位实现。
    #[test]
    fn awake_actions_hide_the_launcher() {
        let host = FakeHost::default();
        assert!(execute(
            &Action::SetAwake {
                minutes: Some(30),
                keep_screen: false
            },
            HWND,
            &host
        ));
        assert_eq!(host.calls(), vec!["hide:4242"]);

        let host = FakeHost::default();
        assert!(execute(&Action::CancelAwake, HWND, &host));
        assert_eq!(host.calls(), vec!["hide:4242"]);
    }

    /// 没有 HWND 时 awake 仍要生效（写状态），只是没得可隐藏。
    #[test]
    fn awake_actions_without_hwnd_still_apply() {
        let host = FakeHost::default();
        assert!(execute(
            &Action::SetAwake {
                minutes: Some(30),
                keep_screen: false
            },
            None,
            &host
        ));
        assert!(host.calls().is_empty(), "无 HWND 时不应有隐藏调用");
    }
}
