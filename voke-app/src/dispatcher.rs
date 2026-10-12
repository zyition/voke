//! `Action` 的执行层（§3 的"副作用集中在 app 的 dispatcher"）。
//!
//! core 里的 [`voke_core::Action`] 是**纯数据**，描述"选中这一项要做什么"；
//! 真正产生副作用的代码集中在这里，这样行为可审计。
//!
//! 已实现：
//! - [`voke_core::Action::Launch`]（应用/文档/目录/MSIX/外壳项）
//! - [`voke_core::Action::ActivateWindow`]（切到已打开窗口，M2）
//! - [`voke_core::Action::Copy`]（写剪贴板 + 隐藏窗口，M3）
//! - [`voke_core::Action::OpenUrl`]（gpui `open_url` + 隐藏窗口，M3）
//! - [`voke_core::Action::SetAwake`] / [`Action::CancelAwake`]（保持唤醒，M5）
//! - [`voke_core::Action::None`]（纯展示，什么都不做、**不隐藏**）
//!
//! **M5 的 `ActivatePlugin` 已随子命令面板整体删除**（方案 A 靠前缀激活插件，
//! 不需要一个"激活插件"的动作）。
//!
//! 副作用能力走 [`Host`] trait（剪贴板 / 打开 URL / 隐藏窗口），生产实现是
//! [`gpui_kit::App`]，测试用假的 `Host` 记录调用。抽这层是因为 gpui 的 `App`
//! 在 Linux 上（`voke-app` 的原生测试环境）根本构造不出来，而"哪个动作隐藏窗口、
//! 哪个动作写剪贴板"是纯决策逻辑，必须能被单测盯住。
//!
//! **审计点：启动/切换/复制前先隐藏 launcher 窗口**（见 [`launch`]、[`activate`]、
//! [`copy`] 与 [`open_url`]）。

use voke_core::Action;

/// dispatcher 需要的宿主能力。
///
/// 生产实现是 [`gpui_kit::App`]；测试实现记录调用序列。
/// 只放"需要 UI/系统能力"的三件事，业务决策（该不该隐藏、该不该清空输入）
/// 留在本模块，测起来不依赖任何平台。
pub trait Host {
    /// 把文本写进系统剪贴板（gpui 的 `App::write_to_clipboard`）。
    fn copy_to_clipboard(&self, text: &str);
    /// 用默认程序打开 URL（gpui 的 `App::open_url`）。
    fn open_url(&self, url: &str);
    /// 隐藏 launcher 窗口（Win32 `ShowWindow(SW_HIDE)`，见 `voke-sys`）。
    fn hide_window(&self, hwnd: isize);
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
        Action::Launch {
            path,
            args,
            elevated,
            no_console,
        } => {
            launch(path, args, *elevated, *no_console, hwnd, host);
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

/// 启动一个可执行文件 / 文档 / 目录 / MSIX 应用 / 特殊外壳项。
///
/// 顺序是有意的：**先隐藏窗口，再创建进程**。这是 launcher 的惯例
/// （PowerToys Run 同款），也是本项目唯一"隐藏先于进程创建"的审计点。
/// 反过来（先 spawn 再 hide）会让新进程先看到一个还盖在屏幕上的 launcher，
/// 且失焦回调可能再补一次 hide，行为不确定。
///
/// `no_console` 来自 [`Action::Launch::no_console`](voke_core::Action)，逐动作决定
/// 是否给子进程加 `CREATE_NO_WINDOW`（见 [`spawn`]）。**不是全局开关**：
/// apps 插件启动命令提示符/PowerShell 时它是 `false`，窗口照常出现。
fn launch(
    path: &str,
    args: &[String],
    elevated: bool,
    no_console: bool,
    hwnd: Option<isize>,
    host: &impl Host,
) {
    if elevated {
        // §5 的 runas verb 走 ShellExecuteExW，属于 voke-sys 的后续工作。
        log::warn!("dispatcher: elevated 启动尚未实现，降级为普通启动: {path}");
    }

    if let Some(hwnd) = hwnd {
        host.hide_window(hwnd);
    }

    match launch_kind(path) {
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
            // 剥掉 `msix://` 得裸 AUMID。
            let aumid = &path[MSIX_PREFIX.len()..];
            // explorer 是 GUI 子系统，没有控制台要藏。
            spawn(
                "explorer.exe",
                &[format!("shell:AppsFolder\\{aumid}")],
                path,
                false,
            );
        }
        // 特殊外壳项：`shell:::{GUID}`。实测 `explorer.exe "shell:::{52205FD8-...}"`
        // 能打开"主文件夹"窗口（也就是常说的"此电脑"）。
        //
        // 注意 `shell:::` 里的 `::` 与 GUID 的花括号都是 explorer 的参数语法细节，
        // 必须原样传递（整条作为一个参数，不要拆）。
        LaunchKind::Shell => {
            spawn("explorer.exe", &[path.to_string()], path, false);
        }
        LaunchKind::Path => launch_path(path, args, no_console),
    }
}

/// 启动普通路径：目录交给 explorer.exe（`Command::new` 直接跑目录会失败），
/// 其余直接 spawn。
fn launch_path(path: &str, args: &[String], no_console: bool) {
    // 目录：`Command::new("C:\\Users\\x\\Tools")` 在 Windows 上会
    // `ERROR_ACCESS_DENIED`（目录不是可执行文件），必须转交 explorer。
    if std::path::Path::new(path).is_dir() {
        log::info!("目标是目录，交给 explorer.exe 打开: {path}");
        spawn("explorer.exe", &[path.to_string()], path, false);
        return;
    }
    spawn(path, args, path, no_console);
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
fn spawn(exe: &str, args: &[String], display: &str, no_console: bool) {
    let mut command = std::process::Command::new(exe);
    command.args(args);
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
            "已启动 {display}（实际命令 {exe} {args:?}，PID={}）",
            child.id()
        ),
        Err(err) => log::error!("启动 {display} 失败（{exe} {args:?}）: {err}"),
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
