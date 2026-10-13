//! 启动早期纠正 voke **自身**的工作目录。
//!
//! ## 为什么需要这个模块
//!
//! 开机自启走 `HKCU\…\Run` 时，注册表项不指定工作目录，进程会**继承到系统目录**
//! （真机实测 `cwd=Some("C:\Windows\System32")`）。后果：任何继承 cwd 启动的子进程
//! （比如直启的 exe、shell 命令）都会把 `C:\Windows\System32` 当起始目录，语义上
//! 不合理，也曾是排查终端 profile 启动异常时的可疑变量。
//!
//! 用户从终端 / 资源管理器显式启动时的 cwd 是**用户自己的意图**，绝不能动——否则会
//! 破坏「在当前目录打开」这类预期。所以只修正「系统目录」与「读取失败」两类异常。
//!
//! ## 平台分层
//!
//! 判定是纯函数（[`should_fix_own_cwd`] / [`is_system_dir`]），任何平台编译并测试
//! （Linux 上 `cargo test -p voke-sys` 真跑表驱动单测）。真正改目录的
//! [`fix_own_cwd`] 用 `std::env::set_current_dir`（跨平台，不引入 windows crate /
//! `#[cfg(windows)]` 分支），所以非 Windows 侧也能干净编译。消费方 voke-app 的测试
//! 在 Linux native 因缺 fontconfig 跑不了，纯逻辑下沉到 voke-sys 才能被覆盖（与
//! `logcap.rs` / `env.rs` / `close_guard.rs` 同款理由）。

use std::path::Path;

/// 判断 voke 自身的当前工作目录**是否需要被纠正**。
///
/// 返回 `true` 表示应改到目标目录（见 [`fix_own_cwd` 的目标优先级]）：
///
/// - `current` 为 `None`：进程 cwd 读取失败（可能已被删除）→ 需要纠正；
/// - `current` 是系统目录（`C:\Windows\System32`、`C:\Windows` 等，
///   大小写 / 盘符不敏感）→ 需要纠正；
/// - 其它（用户从终端 / 资源管理器显式启动）→ 不动，尊重用户的显式意图。
///
/// 纯函数：`current` 注入后可在任何平台做表驱动单测。
///
/// [`fix_own_cwd` 的目标优先级]: fix_own_cwd
pub fn should_fix_own_cwd(current: Option<&Path>) -> bool {
    match current {
        // cwd 读取失败：进程的工作目录可能已被删除，无法判断，保险起见改掉。
        None => true,
        // 落在系统目录内（开机自启继承语义）→ 纠正。
        Some(p) => is_system_dir(p),
    }
}

/// 判断给定路径是否落在 Windows 系统目录内（大小写 / 盘符不敏感）。
///
/// 命中范围（忽略盘符、大小写，并同时兼容 `/` 与 `\` 分隔）：
/// - `C:\Windows`（或任何盘符下的 `Windows`）；
/// - `C:\Windows\System32`、`C:\Windows\SysWOW64` 及其任意子目录。
///
/// 用于识别「开机自启继承到系统目录」这一异常。非 Windows 路径（无
/// `windows` 组件）一律返回 `false`，函数本身跨平台可编译、可测。
pub fn is_system_dir(path: &Path) -> bool {
    // 归一化：转小写，并同时按 `\` 与 `/` 切分（Windows 上是 `\`，但单测里
    // 用字面量 `C:\Windows\…` 在 Linux 上会被当作单段，必须手动切分）。
    let normalized = path.to_string_lossy().to_lowercase();
    let comps: Vec<&str> = normalized
        .split(['\\', '/'])
        .filter(|c| !c.is_empty())
        .collect();
    // `windows` 出现在首段（无盘符的相对写法）或第二段（紧跟盘符）即命中；
    // 更深位置（如 `C:\Program Files\WindowsApps`）不算系统目录，不误伤。
    matches!(
        comps.iter().position(|c| *c == "windows"),
        Some(0) | Some(1)
    )
}

/// 在启动早期纠正 voke 自身的工作目录。
///
/// **调用时机**：必须在日志初始化之后、任何相对路径被解析之前调用（见
/// `voke-app/src/main.rs`）。早于日志初始化调用会让本函数打的 `log::info!`
/// 丢失，无法在真机确认。
///
/// **判定**（见 [`should_fix_own_cwd`]）：
/// - 读取 cwd 失败（已被删除）→ 改；
/// - cwd 是系统目录 → 改；
/// - 其它 → 不动（尊重用户显式指定的工作目录）。
///
/// **目标目录优先级**：
/// 1. `%USERPROFILE`（用 `std::env::var("USERPROFILE")` 取，且须确实存在）；
/// 2. 退而求其次：当前 exe 所在目录（`std::env::current_exe()` 的 parent）；
/// 3. 都失败只打 `log::warn!` 不动，**绝不 panic**。
///
/// 实现用 `std::env::set_current_dir`（跨平台，不引 windows crate / cfg 分支）；
/// 若你有更好理由用 `SetCurrentDirectoryW`，必须挂在 `#[cfg(windows)]` 下以保证
/// voke-sys 的 Linux 测试仍绿——这里选择 std 已满足需求。
pub fn fix_own_cwd() {
    let current = std::env::current_dir().ok();
    if !should_fix_own_cwd(current.as_deref()) {
        log::info!(
            "工作目录正常，未调整: {}",
            current
                .map(|p| p.display().to_string())
                .unwrap_or_else(|| "<读取失败>".to_string())
        );
        return;
    }

    // 来源描述：读取失败时展示占位文案。
    let source = current
        .as_deref()
        .map(|p| p.display().to_string())
        .unwrap_or_else(|| "<读取失败>".to_string());

    // 目标优先级：%USERPROFILE% → exe 所在目录；两者都需确实存在才采用。
    let target = std::env::var("USERPROFILE")
        .ok()
        .filter(|v| !v.is_empty() && Path::new(v).is_dir())
        .or_else(|| {
            std::env::current_exe()
                .ok()
                .and_then(|p| p.parent().map(|p| p.to_string_lossy().into_owned()))
                .filter(|v| !v.is_empty() && Path::new(v).is_dir())
        });

    match target {
        Some(target) => {
            if std::env::set_current_dir(&target).is_ok() {
                log::info!("启动时将工作目录从 {source} 修正为 {target}");
            } else {
                // 改目录失败（极少，通常权限问题）：保持原样，不影响启动。
                log::warn!("工作目录修正失败（目标 {target}），保持原样: 来源 {source}");
            }
        }
        None => {
            // %USERPROFILE% 与 exe 目录都拿不到：只告警不动，绝不 panic。
            log::warn!("无法确定工作目录修正目标，保持原样: 来源 {source}");
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// 表驱动验证 [`should_fix_own_cwd`] 的判定：
    /// - System32 → 需修正；`C:\Windows` → 需修正；盘符/大小写不敏感；
    /// - 用户目录 → 不动；exe 目录 → 不动；
    /// - cwd 读取失败（`None`）→ 需修正。
    #[test]
    fn 是否修正自身_cwd_判定() {
        let cases: &[(&str, Option<&str>, bool)] = &[
            ("System32 需修正", Some(r"C:\Windows\System32"), true),
            ("C:\\Windows 需修正", Some(r"C:\Windows"), true),
            ("小写 + 其它盘符也算", Some(r"D:\WINDOWS\system32"), true),
            ("SysWOW64 需修正", Some(r"C:\Windows\SysWOW64"), true),
            ("用户目录不动", Some(r"C:\Users\x"), false),
            (
                "exe 目录不动",
                Some(r"C:\Users\x\AppData\Local\Programs\voke"),
                false,
            ),
            ("cwd 读取失败需修正", None, true),
        ];
        for (label, cwd, expected) in cases {
            let path = cwd.map(Path::new);
            assert_eq!(should_fix_own_cwd(path), *expected, "{label}: cwd={cwd:?}");
        }
    }

    /// 直接验证 [`is_system_dir`] 的归一化与位置判定（不被 `Some` 包裹干扰）。
    #[test]
    fn 系统目录识别_归一化() {
        assert!(is_system_dir(Path::new(r"C:\Windows")));
        assert!(is_system_dir(Path::new(r"C:/Windows/System32")));
        assert!(is_system_dir(Path::new(r"d:\WINDOWS")));
        // 更深位置不算系统目录，避免误伤 Program Files\WindowsApps 之类。
        assert!(!is_system_dir(Path::new(r"C:\Program Files\WindowsApps")));
        assert!(!is_system_dir(Path::new(r"C:\Users\x")));
    }
}
