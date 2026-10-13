//! 环境变量路径展开与子进程工作目录判定。
//!
//! ## 为什么需要这个模块
//!
//! Windows 的 `CreateProcessW(lpCurrentDirectory)` / `ShellExecuteW(lpDirectory)`
//! **不做 `%VAR%` 展开**（MSDN：lpCurrentDirectory 必须是完整路径，字符串原样使用）。
//! 而启动器生态里未展开的 `%VAR%` 随处可见：`.lnk` 的目标/参数/起始位置
//! 都允许存环境变量（真机实测：`Alacritty.lnk` 的参数是
//! `--working-directory "%USERPROFILE%"`、`Command Prompt.lnk` 的起始位置是
//! `%HOMEDRIVE%%HOMEPATH%`）。资源管理器双击 `.lnk` 时由 shell 链接处理器做展开；
//! 启动器一旦绕过 shell 直启解析出的 exe（voke 与 Wox 的"快路径"），
//! 就必须自己复刻这套展开语义——漏掉任何一处，子进程拿到的就是字面量路径。
//!
//! 对照主流启动器的共识做法（详见审计报告）：
//! - PT Run / Flow：把 `.lnk` 本体交给 shell 启动，借用 shell 的展开；
//! - Wox：读 lnk 的目标/参数/起始位置，`registry.ExpandString`（= 展开）+
//!   `os.Stat` 校验是目录，不合法则整体回退 shell 启动。
//!
//! voke 走解析直启路线，本模块提供第三个共识组件：**展开 → 校验 → 不合法不设**。
//!
//! ## 平台分层
//!
//! `%VAR%` 解析器是纯函数（[`expand_env_with`]，注入变量查询），任何平台编译并测试；
//! Windows 侧的 `.lnk` 解析继续用 `ExpandEnvironmentStringsW`（见
//! `crate::apps::expand_environment`），两者语义一致（未知变量原样保留）。
//! `~` 展开是纯函数（[`expand_tilde_with`]）。
//! [`prepare_working_dir`] 的存在性校验用 `std::path`，任何平台可测。

use std::path::Path;

/// 展开字符串里的 `%VAR%`，变量值由 `lookup` 提供。
///
/// 语义与 Win32 `ExpandEnvironmentStringsW` 对齐（这也是 `crate::apps` 的
/// `.lnk` 解析在 Windows 上实测过的行为）：
/// - `%%` 之间是变量名，大小写不敏感的判定交给 `lookup` 的实现
///   （Windows 的 `GetEnvironmentVariableW` / `ExpandEnvironmentStringsW`
///   天然大小写不敏感）；
/// - **未知变量原样保留**（`%NOPE%\x` → `%NOPE%\x`），便于日志定位是哪个变量没展开，
///   也与 ExpandEnvironmentStringsW 一致；
/// - 不成对的 `%` 原样保留（`abc%` → `abc%`）；
/// - 变量值为空串时替换为空串（与 ExpandEnvironmentStringsW 一致）。
///
/// 纯函数：`lookup` 注入后可在任何平台做表驱动单测。
pub fn expand_env_with(raw: &str, lookup: impl Fn(&str) -> Option<String>) -> String {
    let mut out = String::with_capacity(raw.len());
    let mut rest = raw;
    while let Some(start) = rest.find('%') {
        out.push_str(&rest[..start]);
        let after = &rest[start + 1..];
        match after.find('%') {
            Some(end) => {
                let key = &after[..end];
                match lookup(key) {
                    Some(val) => out.push_str(&val),
                    // 未知变量：原样保留（含两端的 %），便于排查。
                    None => {
                        out.push('%');
                        out.push_str(key);
                        out.push('%');
                    }
                }
                rest = &after[end + 1..];
            }
            None => {
                // 不成对的 %：从这里到结尾原样保留。
                out.push_str(&rest[start..]);
                return out;
            }
        }
    }
    out.push_str(rest);
    out
}

/// [`expand_env_path`] 用的 `~` 前缀展开（纯函数，供单测注入 home）。
///
/// 只认**开头**的 `~`（`~`、`~/x`、`~\x`）；中间的 `~` 是普通字符
/// （`C:\a~b` 不是家目录引用）。home 未知时原样保留。
fn expand_tilde_with(raw: &str, home: Option<&str>) -> String {
    let Some(home) = home else {
        return raw.to_string();
    };
    if raw == "~" {
        return home.to_string();
    }
    let Some(rest) = raw.strip_prefix("~/").or_else(|| raw.strip_prefix("~\\")) else {
        return raw.to_string();
    };
    if rest.is_empty() {
        home.to_string()
    } else {
        format!("{home}\\{rest}")
    }
}

/// 展开路径里的 `%VAR%` 与前导 `~`。
///
/// - `%VAR%`：Windows 上走 `crate::apps::expand_environment`
///   （`ExpandEnvironmentStringsW`，与 `.lnk` 解析同一实现，避免两套语义）；
///   非 Windows 用 [`expand_env_with`] + 进程环境（Linux 单测跑的就是这条）。
/// - `~`：展开为当前用户主目录（Windows 取 `USERPROFILE`，其余取 `HOME`）。
///   只处理前导 `~`，见 [`expand_tilde_with`]。
///
/// 展开不了的成分（未知变量、无 home）**原样保留**——调用方负责校验与降级
/// （见 [`prepare_working_dir`]），这里不静默丢弃。
pub fn expand_env_path(raw: &str) -> String {
    let expanded = crate::apps::expand_environment(raw);
    let home = home_dir();
    expand_tilde_with(&expanded, home.as_deref())
}

/// 子进程兜底启动工作目录（不依赖调用方 voke 自身 cwd）。
///
/// **背景**：安装包「立即运行」启动的 voke 可能继承安装程序目录作为 cwd；
/// 此时若走解包版 WT 兜底直启（`dispatcher::run_app_activation`），会把该
/// cwd 泄漏给 `wt.exe` → 其拉起的 pwsh（mise/scoop shim 报错）。自启动 / 菜单
/// 启动无此问题（它们的 cwd 本就是用户目录）。故兜底直启必须显式指定一个
/// **与 voke 自身 cwd 无关**的安全目录，而不是把空串（= 继承 voke cwd）传下去。
///
/// 取值优先级：
/// 1. `%USERPROFILE%`（经 [`expand_env_path`] 展开后须是真实目录）；
/// 2. 否则回退到 `exe` 所在目录（能从路径解析且确为目录时）；
/// 3. 都拿不到返回空串——交给 [`prepare_working_dir`] 判为"未声明"，
///    子进程继承默认 cwd（已无法再更安全，属极端兜底）。
pub fn safe_fallback_working_dir(exe: &str) -> String {
    let candidate = expand_env_path("%USERPROFILE%");
    if Path::new(&candidate).is_dir() {
        return candidate;
    }
    if let Some(parent) = Path::new(exe).parent() {
        let dir = parent.to_string_lossy().into_owned();
        if Path::new(&dir).is_dir() {
            return dir;
        }
    }
    String::new()
}

/// 当前用户主目录（`~` 展开用）。
fn home_dir() -> Option<String> {
    #[cfg(windows)]
    let key = "USERPROFILE";
    #[cfg(not(windows))]
    let key = "HOME";
    std::env::var(key).ok().filter(|v| !v.is_empty())
}

/// 把"声明的工作目录"整理成**可安全传给子进程的 cwd**（共识三步曲）。
///
/// 步骤（对应 Wox `shortcut_windows.go` 的展开 + `os.Stat` 校验）：
/// 1. trim；空串 → `None`（没声明，继承默认）；
/// 2. [`expand_env_path`] 展开 `%VAR%` / `~`；
/// 3. `Path::is_dir()` 校验**展开后**的路径确实是目录；
///    不存在/是文件/仍含未展开字面量 → `None` 并记 warn。
///
/// **绝不返回未展开或不存在路径**——调用方拿 `None` 就不设 cwd（继承默认），
/// 而不是把字面量 `%USERPROFILE%` 传给 `CreateProcessW`。
/// 返回 `Some` 时值是展开后且确认存在的目录。
pub fn prepare_working_dir(raw: &str) -> Option<String> {
    let trimmed = raw.trim();
    if trimmed.is_empty() {
        return None;
    }
    let expanded = expand_env_path(trimmed);
    if expanded != trimmed {
        log::debug!("工作目录已展开: {trimmed:?} -> {expanded:?}");
    }
    if Path::new(&expanded).is_dir() {
        Some(expanded)
    } else {
        // 记 warn 而不是 debug：这行是"为什么不设 cwd"的唯一线索，
        // 日志跨启动追加，用户反馈"启动目录不对"时靠它定位。
        log::warn!(
            "工作目录无效，不设置 cwd（子进程继承默认）: 原始={trimmed:?} 展开后={expanded:?}"
        );
        None
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    // ── expand_env_with：表驱动（任务要求的 a–f 用例）──

    /// a) 已知变量 → 展开；b) `%VAR%\Foo` 混合路径 → 只替换变量段；
    /// c) 未知变量 → 原样保留（Windows 语义，ExpandEnvironmentStringsW 同款）；
    /// d) 空串 → 空串；e) 已展开的真实路径 → 不变；
    /// f) 非法形态（不成对 %）→ 原样保留。
    #[test]
    fn expand_env_with_table() {
        let cases: &[(&str, &str, &str, &str)] = &[
            // (说明, 输入, 变量表, 期望)
            (
                "a) 已知变量",
                "%USERPROFILE%",
                "USERPROFILE=C:\\Users\\u",
                "C:\\Users\\u",
            ),
            (
                "b) 混合路径",
                "%APPDATA%\\Foo",
                "APPDATA=C:\\Users\\u\\AppData\\Roaming",
                "C:\\Users\\u\\AppData\\Roaming\\Foo",
            ),
            ("c) 未知变量原样保留", "%NOPE%\\x", "", "%NOPE%\\x"),
            ("d) 空串", "", "", ""),
            ("e) 真实路径不变", "C:\\a\\b.exe", "", "C:\\a\\b.exe"),
            ("f) 不成对 % 原样", "abc%", "", "abc%"),
            ("f2) 前缀不成对 %", "%abc", "", "%abc"),
            (
                "g) 多变量",
                "%HOMEDRIVE%%HOMEPATH%",
                "HOMEDRIVE=C:;HOMEPATH=\\Users\\u",
                "C:\\Users\\u",
            ),
            ("h) 变量值为空", "%EMPTY%\\x", "EMPTY=", "\\x"),
            ("i) 连续展开", "%A%和%B%", "A=甲;B=乙", "甲和乙"),
            (
                "j) 大小写不敏感由 lookup 决定",
                "%userprofile%",
                "USERPROFILE=C:\\U",
                "C:\\U",
            ),
        ];
        for (label, input, envs, expected) in cases {
            let table: Vec<(String, String)> = envs
                .split(';')
                .filter(|s| !s.is_empty())
                .map(|kv| {
                    let (k, v) = kv.split_once('=').unwrap();
                    (k.to_string(), v.to_string())
                })
                .collect();
            let lookup = |k: &str| -> Option<String> {
                table
                    .iter()
                    .find(|(key, _)| key.eq_ignore_ascii_case(k))
                    .map(|(_, v)| v.clone())
            };
            assert_eq!(
                expand_env_with(input, lookup),
                *expected,
                "{label}: {input}"
            );
        }
    }

    /// 三个连续 `%`：第一个变量吃掉一对后剩下的单独 % 原样保留。
    #[test]
    fn expand_env_with_odd_percent_count() {
        let lookup = |k: &str| k.eq_ignore_ascii_case("A").then(|| "1".to_string());
        assert_eq!(expand_env_with("%A%b%", lookup), "1b%");
        assert_eq!(expand_env_with("%%%A%%", lookup), "%%1%");
    }

    // ── expand_tilde_with ──

    #[test]
    fn tilde_expands_only_at_start() {
        let home = Some("C:\\Users\\u");
        assert_eq!(expand_tilde_with("~", home), "C:\\Users\\u");
        assert_eq!(expand_tilde_with("~/x", home), "C:\\Users\\u\\x");
        assert_eq!(expand_tilde_with("~\\x", home), "C:\\Users\\u\\x");
        // 中间的 ~ 是普通字符。
        assert_eq!(expand_tilde_with("C:\\a~b", home), "C:\\a~b");
        assert_eq!(expand_tilde_with("x~", home), "x~");
        // home 未知 → 原样保留。
        assert_eq!(expand_tilde_with("~/x", None), "~/x");
    }

    // ── prepare_working_dir：存在性校验（tempdir 真跑文件系统）──

    #[test]
    fn working_dir_empty_or_whitespace_is_none() {
        assert_eq!(prepare_working_dir(""), None);
        assert_eq!(prepare_working_dir("   "), None);
        assert_eq!(prepare_working_dir("\t"), None);
    }

    #[test]
    fn working_dir_existing_directory_is_some() {
        let tmp = tempfile::tempdir().unwrap();
        let got = prepare_working_dir(tmp.path().to_str().unwrap());
        assert_eq!(got.as_deref(), Some(tmp.path().to_str().unwrap()));
    }

    #[test]
    fn working_dir_missing_path_is_none() {
        let tmp = tempfile::tempdir().unwrap();
        let missing = tmp.path().join("no-such-dir");
        assert_eq!(prepare_working_dir(missing.to_str().unwrap()), None);
    }

    /// 指向**文件**（不是目录）也必须拒绝：cwd 只能是目录。
    #[test]
    fn working_dir_pointing_at_file_is_none() {
        let tmp = tempfile::tempdir().unwrap();
        let file = tmp.path().join("f.txt");
        std::fs::write(&file, b"x").unwrap();
        assert_eq!(prepare_working_dir(file.to_str().unwrap()), None);
    }

    /// 已知变量混合路径：展开后存在 → Some（展开后的值）；
    /// 展开后不存在 → None（绝不把未展开字面量传下去）。
    #[test]
    fn working_dir_expands_then_validates() {
        let tmp = tempfile::tempdir().unwrap();
        // 造一个真实存在的子目录，用它的完整路径模拟"展开结果"。
        let sub = tmp.path().join("sub");
        std::fs::create_dir(&sub).unwrap();

        // 未展开字面量（变量不存在）：展开保留字面量 → is_dir 失败 → None。
        assert_eq!(prepare_working_dir("%NOPE_DIR%\\sub"), None);
        // 展开后确实存在 → Some。
        let existing = sub.to_str().unwrap();
        assert_eq!(prepare_working_dir(existing), Some(existing.to_string()));
    }

    /// 首尾空白被 trim（lnk 的起始位置字段常见尾随空格）。
    #[test]
    fn working_dir_trims_whitespace() {
        let tmp = tempfile::tempdir().unwrap();
        let raw = format!("  {}  ", tmp.path().to_str().unwrap());
        let got = prepare_working_dir(&raw);
        assert_eq!(got.as_deref(), Some(tmp.path().to_str().unwrap()));
    }
}
