//! MSIX/Store 应用的 App Activation（`IApplicationActivationManager`）。
//!
//! ## 为什么需要它
//!
//! Store 版应用（WT、PowerShell、终端…）的 exe 在
//! `C:\Program Files\WindowsApps\` 缓存目录，路径带版本号且受 ACL 限制，
//! `CreateProcessW` 直启会失败或绕过包身份。官方出路有两条：
//! - `explorer.exe shell:AppsFolder\<AUMID>`（dispatcher 的 `LaunchKind::Msix`
//!   分支，**不能带参数**）；
//! - 本模块的 `IApplicationActivationManager::ActivateApplication`
//!   （**可以带命令行参数**）。
//!
//! 唤醒 WT profile 就必须带参数（`--profile "<guid>"`），所以走本模块——
//! 对齐 PowerToys CmdPal 的 Windows Terminal 扩展
//! （microsoft/PowerToys@`b76d14e3`，`Commands/LaunchProfileCommand.cs`：
//! `appManager.ActivateApplication(appUserModelId, queryArguments,
//! ActivateOptions.None, out var unusedPid)`）。
//!
//! 真机背景（2026-10-13 `voke.log`）：经 voke `CreateProcess` 直启 wt.exe 的
//! App Execution Alias 启动 pwsh profile 时，pwsh 的 `$PROFILE` 调 scoop shim
//! 起 mise.exe 失败；同一 profile 经 shell 激活
//! （`explorer.exe shell:AppsFolder\…`）则正常。改用 App Activation 后
//! WT 由激活服务拉起（父进程不再是启动器），与 CmdPal / 双击行为一致。
//!
//! ## 平台分层
//!
//! Windows 实现整体 `#[cfg(windows)]`（COM）；非 Windows 是"记日志 + 返回
//! `None`"的占位（`cargo test -p voke-sys` 在 WSL 原生环境保持绿；
//! app 层在非 Windows 上也不会真触发启动）。
//!
//! COM 初始化复用 [`crate::apps::with_com`]（`CoInitializeEx(MTA)` 作用域）。

/// 激活一个 Store 应用（AUMID），可带命令行参数。返回新进程的 PID
/// （激活失败 / 非 Windows 返回 `None`）。
///
/// - `aumid`：Application User Model ID，如
///   `Microsoft.WindowsTerminal_8wekyb3d8bbwe!App`。
/// - `arguments`：传给应用的命令行参数串（WT 用 `--profile "<guid>"`，
///   拼装见 `voke_core::terminal::profile_activation_arguments`）。
///   传空串表示无参数激活（等同 `shell:AppsFolder` 双击）。
///
/// **语义**：WT 已在运行时，激活请求会转给它（按 `windowingBehavior`
/// 开新窗口/新标签），不会起第二个进程——与 CmdPal 同款。
pub fn activate_app(aumid: &str, arguments: &str) -> Option<u32> {
    #[cfg(windows)]
    {
        activate_app_windows(aumid, arguments)
    }
    #[cfg(not(windows))]
    {
        // 非 Windows 没有 Store 应用这个概念；占位实现只记日志，
        // 让 app 层的调用点不需要额外挂 cfg。
        let _ = arguments;
        log::info!("非 Windows 平台：activate_app 占位实现，未真正激活 {aumid}");
        None
    }
}

/// Windows 实现：`CoCreateInstance(CLSID_ApplicationActivationManager)` →
/// `ActivateApplication(aumid, arguments, AO_NONE, &mut pid)`。
///
/// CLSCTX 用 `INPROC_SERVER`——CmdPal 的 `LaunchProfileCommand` 用的就是
/// `CLSCTX.InProcServer`（生产验证过的路径）。失败时调用方回退
/// wt.exe 直启，所以不在此处重试其它 CLSCTX。
#[cfg(windows)]
fn activate_app_windows(aumid: &str, arguments: &str) -> Option<u32> {
    use windows::Win32::System::Com::{CLSCTX_INPROC_SERVER, CoCreateInstance};
    use windows::Win32::UI::Shell::{
        AO_NONE, ApplicationActivationManager, IApplicationActivationManager,
    };
    use windows::core::HSTRING;

    crate::apps::with_com(|| {
        // SAFETY: CLSID 取自 windows-0.62.2 的 `Win32::UI::Shell`
        // （`ApplicationActivationManager` = 45ba127d-10a8-46ea-8ab7-56ea9078943c，
        // 与 CmdPal 引用的 `CLSID.ApplicationActivationManager` 一致）；
        // 接口 IID 2e941141-7f97-4756-ba1d-9decde894a3d 由 crate 的
        // `define_interface!` 保证。punkouter 传 None（不聚合）。
        let manager: IApplicationActivationManager = match unsafe {
            CoCreateInstance(&ApplicationActivationManager, None, CLSCTX_INPROC_SERVER)
        } {
            Ok(m) => m,
            Err(err) => {
                log::error!("创建 IApplicationActivationManager 失败（aumid={aumid}）: {err}");
                return None;
            }
        };

        let aumid_h = HSTRING::from(aumid);
        let args_h = HSTRING::from(arguments);
        // SAFETY: 两个入参都是本函数构造的合法 HSTRING（借用到调用结束）；
        // AO_NONE = 0（无激活选项）。
        match unsafe { manager.ActivateApplication(&aumid_h, &args_h, AO_NONE) } {
            Ok(pid) => {
                log::info!("App Activation 已提交: {aumid}（参数 {arguments:?}，PID={pid}）");
                Some(pid)
            }
            Err(err) => {
                log::error!("ActivateApplication 失败: {aumid}（参数 {arguments:?}）: {err}");
                None
            }
        }
    })
}
