//! voke 自己的落盘位置（配置、日志、图标缓存）。
//!
//! ## 为什么必须绝对路径
//!
//! 开机自启动走注册表 `Run` 键，拉起进程时的工作目录由系统决定（实测常见
//! `C:\Windows\system32`），**不是** exe 所在目录。任何用相对路径的落盘
//! （`voke.log`、`icons/`）在自启动场景下都会写错地方，甚至直接失败——
//! `main` 里 `File::create("voke.log").expect(...)` 在 system32 上就是 panic。
//!
//! 所以这里统一给出 `%APPDATA%\voke` 下的绝对路径，调用方拿不到时再退回
//! 进程工作目录（权限不足 / 环境变量缺失，仍然要能跑）。
//!
//! ## 平台策略
//!
//! 与其它模块一致：只有碰环境变量的入口是 `#[cfg(windows)]`，非 Windows
//! 上全部返回 `None`（`cargo test -p voke-sys` 在 WSL 里照跑）。

use std::path::PathBuf;

/// voke 目录名（位于 `%APPDATA%` 下）。
///
/// 只在 Windows 的 `app_dir()` 里真正用到；非 Windows 侧仅测试引用，
/// 故放行 dead_code（与 awake 的纯逻辑函数同处理）。
#[cfg_attr(not(windows), allow(dead_code))]
const APP_DIR: &str = "voke";

/// 配置文件名。
const CONFIG_FILE: &str = "config.toml";

/// 使用频率数据文件名（`provider_id:标题 → 次数`）。
///
/// 与 `config.toml` 分开：配置是**用户手写的**（要能读、能注），频率数据是被
/// 程序每次执行都改写的，混在一个文件里会让用户的手写注释/排版在第一次启动后
/// 被重写掉。
const FREQUENCY_FILE: &str = "frequency.toml";

/// 日志文件名。
const LOG_FILE: &str = "voke.log";

/// 图标缓存目录名。
const ICON_DIR: &str = "icons";

/// voke 的专用目录（`%APPDATA%\voke`）。
///
/// 目录不存在时尝试创建；创建失败返回 `None`（调用方回退到工作目录）。
/// 只在这里创建一次，后续 `config_path()` / `log_path()` 都基于它。
#[cfg(windows)]
pub fn app_dir() -> Option<PathBuf> {
    let Ok(appdata) = std::env::var("APPDATA") else {
        log::warn!("APPDATA 未设置，voke 数据目录不可用");
        return None;
    };
    let dir = PathBuf::from(appdata).join(APP_DIR);
    if let Err(err) = std::fs::create_dir_all(&dir) {
        log::warn!("创建 {} 失败：{err}", dir.display());
        return None;
    }
    Some(dir)
}

/// 非 Windows 没有 `%APPDATA%` 语义，一律 `None`。
#[cfg(not(windows))]
pub fn app_dir() -> Option<PathBuf> {
    None
}

/// 配置文件路径（`%APPDATA%\voke\config.toml`）。
pub fn config_path() -> Option<PathBuf> {
    app_dir().map(|dir| dir.join(CONFIG_FILE))
}

/// 使用频率数据路径（`%APPDATA%\voke\frequency.toml`）。
///
/// 这里读进来的是"哪些结果被用过多少次"，跨会话保持——重启不清零。
pub fn frequency_path() -> Option<PathBuf> {
    app_dir().map(|dir| dir.join(FREQUENCY_FILE))
}

/// 日志文件路径（`%APPDATA%\voke\voke.log`）。
pub fn log_path() -> Option<PathBuf> {
    app_dir().map(|dir| dir.join(LOG_FILE))
}

/// 图标缓存目录（`%APPDATA%\voke\icons`）。
///
/// 拿不到 `app_dir()` 时返回 `None`，由调用方回退到工作目录。
pub fn icon_dir() -> Option<PathBuf> {
    app_dir().map(|dir| dir.join(ICON_DIR))
}

#[cfg(test)]
mod tests {
    use super::*;

    /// 文件名常量不带路径分隔符：拼出来的路径永远只有一个层级。
    #[test]
    fn file_names_have_no_separators() {
        assert!(!CONFIG_FILE.contains(['/', '\\']));
        assert!(!FREQUENCY_FILE.contains(['/', '\\']));
        assert!(!LOG_FILE.contains(['/', '\\']));
        assert!(!APP_DIR.contains(['/', '\\']));
        assert!(!ICON_DIR.contains(['/', '\\']));
    }

    /// 非 Windows 上三个入口都返回 `None`（Linux 侧不参与，纯编译期保证）。
    #[cfg(not(windows))]
    #[test]
    fn none_on_non_windows() {
        assert!(app_dir().is_none());
        assert!(config_path().is_none());
        assert!(frequency_path().is_none());
        assert!(log_path().is_none());
        assert!(icon_dir().is_none());
    }
}
