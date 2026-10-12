//! Windows Terminal 的设置文件定位与读取（terminal provider 的 sys 半边）。
//!
//! 分层约定（与 `windows` 模块同款）：**路径拼接是纯函数**，任何平台都能编译、
//! 都能在 Linux 上单测；只有真正读文件的 [`read_settings_contents`] 是
//! `#[cfg(windows)]`（它依赖 `%LOCALAPPDATA%` 的语义与真实文件系统布局）。
//!
//! 解析 / 过滤 / 合并去重**不在这一层**——那些是纯逻辑，在
//! `voke_core::terminal`（`profiles_from_settings`），这里只负责"把文件内容读出来"。

use std::path::{Path, PathBuf};

/// stable 版的包目录名（`%LOCALAPPDATA%\Packages\<这里>\LocalState\settings.json`）。
pub const STABLE_PACKAGE: &str = "Microsoft.WindowsTerminal_8wekyb3d8bbwe";
/// Preview 版的包目录名。
pub const PREVIEW_PACKAGE: &str = "Microsoft.WindowsTerminalPreview_8wekyb3d8bbwe";

/// wt.exe 的 App Execution Alias 所在的子路径（相对 `%LOCALAPPDATA%`）。
const WT_ALIAS_SUFFIX: &str = r"Microsoft\WindowsApps\wt.exe";

/// 两个 settings.json 的候选路径，**stable 在前**（合并去重时 stable 优先，
/// 顺序就是 `voke_core::terminal::profiles_from_settings` 的优先级语义）。
///
/// 纯函数：只拼路径，不检查存在性（不存在的文件由读取方跳过）。
pub fn settings_paths(localappdata: &Path) -> Vec<PathBuf> {
    [STABLE_PACKAGE, PREVIEW_PACKAGE]
        .iter()
        .map(|pkg| {
            localappdata
                .join("Packages")
                .join(pkg)
                .join("LocalState")
                .join("settings.json")
        })
        .collect()
}

/// wt.exe 的完整路径（App Execution Alias）。
///
/// 用完整路径而不是裸 `wt.exe`：alias 目录通常在 PATH 里，但用户可能改过
/// PATH；完整路径让启动行为与 PATH 配置无关。
pub fn wt_exe_path(localappdata: &Path) -> PathBuf {
    localappdata.join(WT_ALIAS_SUFFIX)
}

/// 读出所有存在的 settings.json 的**原文**（stable 在前；不存在 / 读失败的跳过）。
///
/// 返回原文而不是解析结果：JSONC 容错解析与合并去重是 voke-core 的纯逻辑，
/// 这一层只做 IO。读失败（ACL、被占用）不 panic，记 warn 跳过——
/// 与"坏 JSON 不 panic"同一条搜索路径纪律。
///
/// 每次调用都现读（不缓存）：profile 会被用户在 WT 设置里增删改。
#[cfg(windows)]
pub fn read_settings_contents() -> Vec<String> {
    let Ok(localappdata) = std::env::var("LOCALAPPDATA") else {
        log::warn!("LOCALAPPDATA 未设置，WT profiles 不可用");
        return Vec::new();
    };
    let mut contents = Vec::new();
    for path in settings_paths(Path::new(&localappdata)) {
        match std::fs::read_to_string(&path) {
            Ok(text) => contents.push(text),
            Err(err) if err.kind() == std::io::ErrorKind::NotFound => {
                // 没装对应版本是常态（很多人只装 stable 或只装 Preview），不值得记日志。
                log::debug!("WT settings.json 不存在，跳过: {}", path.display());
            }
            Err(err) => {
                log::warn!(
                    "读取 WT settings.json 失败，跳过: {}: {err}",
                    path.display()
                );
            }
        }
    }
    contents
}

/// wt.exe 的完整路径（`%LOCALAPPDATA%\Microsoft\WindowsApps\wt.exe`）。
///
/// `LOCALAPPDATA` 缺失时退回裸 `"wt.exe"`（交给 CreateProcess 走 PATH 查找，
/// alias 目录默认在 PATH 里，多数情况仍然能启动）。
#[cfg(windows)]
pub fn wt_exe() -> String {
    match std::env::var("LOCALAPPDATA") {
        Ok(dir) => wt_exe_path(Path::new(&dir)).to_string_lossy().into_owned(),
        Err(_) => "wt.exe".to_string(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn settings_paths_are_stable_first() {
        let base = Path::new(r"C:\Users\y\AppData\Local");
        let paths = settings_paths(base);
        assert_eq!(paths.len(), 2);
        // 逐级 join 构造期望值：Linux 上 `\` 不是分隔符，不能用一条带 `\` 的字符串当期望。
        let expected = |pkg: &str| {
            base.join("Packages")
                .join(pkg)
                .join("LocalState")
                .join("settings.json")
        };
        assert_eq!(paths[0], expected(STABLE_PACKAGE), "stable 必须在前");
        assert_eq!(paths[1], expected(PREVIEW_PACKAGE));
    }

    #[test]
    fn wt_exe_path_points_to_app_execution_alias() {
        let base = Path::new(r"C:\Users\y\AppData\Local");
        assert_eq!(wt_exe_path(base), base.join(WT_ALIAS_SUFFIX));
    }
}
