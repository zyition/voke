//! 文件关联启动（`ShellExecuteExW`）——`CreateProcessW` 跑不了的目标的官方出路。
//!
//! `std::process::Command` 在 Windows 上就是 `CreateProcessW`，它**只能加载
//! PE 镜像**：`.msc`（`services.msc` → `mmc.exe`）、`.cpl`（`appwiz.cpl` →
//! `control.exe`）、`.bat` / `.cmd` / `.ps1` / `.msi` / 各类脚本、`.lnk`
//! 快捷方式本体这些"系统里注册了打开方式、但不是可执行镜像"的目标，一律以
//! `ERROR_BAD_EXE_FORMAT`（193）失败。资源管理器双击这类文件走的是 shell 的
//! 文件关联，本模块把这条路径封出来给 app 层用（`.lnk` 走 shell 链接处理器：
//! 目标解析 / 参数 / 起始位置 / 环境变量展开全部由 shell 负责，与双击同义）。
//!
//! 平台分层与仓库其他 sys 模块一致：Windows 实现整体 `#[cfg(windows)]`，
//! 非 Windows 是"记日志 + 返回 `false`"的占位（`cargo test -p voke-sys` 在
//! WSL 原生环境保持绿；app 层在非 Windows 上也不会真触发启动）。
//!
//! **不处理 UWP/AUMID**：形如 `Microsoft.X_8wekyb3d8bbwe!App` 的应用模型 ID
//! 不是文件路径，`ShellExecuteW` 的文件形式吃不下（需要
//! `IApplicationActivationManager`）。voke 的 Store 应用走另一条已验证的路：
//! `msix://` 前缀 → `explorer.exe shell:AppsFolder\<AUMID>`（见 app 层
//! dispatcher 的 `LaunchKind::Msix` 分支）。

/// 用默认打开方式（`verb = "open"`）启动 `target`。
///
/// - `target`：文件路径，例如 `C:\Windows\System32\services.msc`。
///   **不要**传 AUMID 或 `shell:::{GUID}`（见模块文档）。
/// - `parameters`：可选的命令行参数串（对应 `ShellExecuteW` 的
///   `lpParameters`），由调用方预先拼好（见 app 层 dispatcher 的
///   `join_shell_parameters`）。
/// - `working_dir`：可选的工作目录（对应 `lpDirectory`）。**调用方必须先做
///   展开 + 存在性校验**（见 [`crate::env::prepare_working_dir`]）——
///   `ShellExecuteW` 不展开 `lpDirectory` 里的 `%VAR%`，传字面量或死路径
///   会直接失败（或让子进程拿到非法 cwd）。传 `None` / `Some("")` 时
///   不设目录，子进程继承本进程 cwd（永远合法，是安全默认）。
///
/// 返回 `true` 只表示 **shell 接受了启动请求**——不等于目标进程一定跑得起来
/// （关联程序自身的失败 ShellExecuteW 不等待、也不报告，与双击行为一致）。
pub fn shell_open(target: &str, parameters: Option<&str>, working_dir: Option<&str>) -> bool {
    #[cfg(windows)]
    {
        shell_execute_w(target, parameters, working_dir)
    }
    #[cfg(not(windows))]
    {
        // 非 Windows 没有 shell 关联这个概念；占位实现只记日志，
        // 让 app 层的调用点不需要额外挂 cfg。
        let _ = (parameters, working_dir);
        log::info!("非 Windows 平台：shell_open 占位实现，未真正启动 {target}");
        false
    }
}

/// Windows 实现：
/// `ShellExecuteExW(SHELLEXECUTEINFOW { lpVerb="open", lpFile=target,
/// lpParameters=parameters, lpDirectory=working_dir, nShow=SW_SHOWNORMAL,
/// fMask=SEE_MASK_NOCLOSEPROCESS })`。
///
/// 为什么用 Ex 而不是 `ShellExecuteW`：`SEE_MASK_NOCLOSEPROCESS` 让成功时
/// `hProcess` 带上目标进程句柄——启动日志能记下 PID（与 `CreateProcessW`
/// 直启的日志对齐，排查"到底起没起来"时少一层猜）。句柄用完即关
/// （launcher 不等子进程退出）。失败时 SE_ERR 伪值在 `hInstApp` 里
/// （≤ 32），与 `ShellExecuteW` 的返回值同一套约定。
///
/// `hwnd` 传 `None`（无父窗口）：voke 的 launcher 窗口在启动前已经隐藏，
/// 关联程序若弹 UAC 或错误框也不该挂在我们的窗口下。
#[cfg(windows)]
fn shell_execute_w(target: &str, parameters: Option<&str>, working_dir: Option<&str>) -> bool {
    use ::windows::Win32::Foundation::CloseHandle;
    use ::windows::Win32::UI::Shell::{
        SEE_MASK_NOCLOSEPROCESS, SHELLEXECUTEINFOW, ShellExecuteExW,
    };
    use ::windows::Win32::UI::WindowsAndMessaging::SW_SHOWNORMAL;
    use ::windows::core::{HSTRING, PCWSTR};

    let file = HSTRING::from(target);
    // 空参数用空串而不是空指针：两条路 ShellExecuteExW 都接受，空串的推理更稳。
    let params = HSTRING::from(parameters.unwrap_or_default());
    let verb = HSTRING::from("open");
    // 目录：空/空白串按未提供处理（保持 HSTRING 存活到调用结束，PCWSTR 只借用它）。
    let dir_hstring = working_dir
        .map(str::trim)
        .filter(|d| !d.is_empty())
        .map(HSTRING::from);
    let dir_pc: Option<PCWSTR> = dir_hstring.as_ref().map(|h| PCWSTR(h.as_ptr()));
    let dir: Option<&PCWSTR> = dir_pc.as_ref();

    let mut sei = SHELLEXECUTEINFOW {
        cbSize: std::mem::size_of::<SHELLEXECUTEINFOW>() as u32,
        fMask: SEE_MASK_NOCLOSEPROCESS,
        lpVerb: PCWSTR(verb.as_ptr()),
        lpFile: PCWSTR(file.as_ptr()),
        lpParameters: PCWSTR(params.as_ptr()),
        lpDirectory: dir.copied().unwrap_or_else(PCWSTR::null),
        nShow: SW_SHOWNORMAL.0,
        ..Default::default()
    };
    // SAFETY: sei 是本函数完整初始化的结构体（cbSize/fMask 已设，其余 Default
    // 零初始化）；所有 PCWSTR 都指向上面仍存活的 HSTRING / None（空指针）。
    let ok = unsafe { ShellExecuteExW(&mut sei) }.is_ok();
    if !ok {
        // 失败时 hInstApp 是 ≤32 的 SE_ERR_* 伪值（与 ShellExecuteW 返回值同约定）。
        let code = sei.hInstApp.0 as usize;
        log::error!(
            "ShellExecuteExW 启动失败: {target}（SE_ERR={code} {}）",
            se_err_hint(code)
        );
        return false;
    }
    // SEE_MASK_NOCLOSEPROCESS：成功时 sei.hProcess 可能是目标进程句柄
    // （文档类目标被已在运行的实例接管、或激活的是壳层项时为 NULL）。
    let pid = pid_of(sei.hProcess);
    // 句柄是我们的了，必须关——不等子进程（launcher 只负责拉起）。
    if !sei.hProcess.is_invalid() {
        // SAFETY: hProcess 来自 ShellExecuteExW 的成功返回（本进程拥有）。
        // 返回值故意忽略：CloseHandle 的 BOOL 失败没有可处置动作
        // （句柄泄露由内核兜底，日志层已记过启动结果）。
        let _ = unsafe { CloseHandle(sei.hProcess) };
    }
    match pid {
        Some(pid) => log::info!("ShellExecuteExW 已提交启动请求: {target}（PID={pid}）"),
        None => log::info!(
            "ShellExecuteExW 已提交启动请求: {target}（无进程句柄：目标可能被已有实例接管）"
        ),
    }
    true
}

/// 从进程句柄取 PID；无效/空句柄返回 `None`。
#[cfg(windows)]
fn pid_of(process: ::windows::Win32::Foundation::HANDLE) -> Option<u32> {
    use ::windows::Win32::System::Threading::GetProcessId;
    if process.is_invalid() {
        return None;
    }
    // SAFETY: 调用方保证是有效的进程句柄（ShellExecuteExW 成功返回的）。
    Some(unsafe { GetProcessId(process) })
}

/// `SE_ERR_*` 码的一句话解释（只列最常见的几个，其余只打码）。
///
/// 常量值取自 `windows-0.62.2` 的 `Win32::UI::Shell`（与 Win32 `shellapi.h`
/// 一致），不是凭记忆写的。
#[cfg(windows)]
fn se_err_hint(code: usize) -> &'static str {
    use ::windows::Win32::UI::Shell::{
        SE_ERR_ACCESSDENIED, SE_ERR_DLLNOTFOUND, SE_ERR_FNF, SE_ERR_NOASSOC, SE_ERR_OOM, SE_ERR_PNF,
    };
    match code as u32 {
        SE_ERR_FNF => "文件不存在",
        SE_ERR_PNF => "路径不存在",
        SE_ERR_ACCESSDENIED => "拒绝访问",
        SE_ERR_OOM => "内存不足",
        SE_ERR_NOASSOC => "没有注册打开方式",
        SE_ERR_DLLNOTFOUND => "关联程序依赖的 DLL 缺失",
        _ => "详见 ShellExecuteW 文档",
    }
}
