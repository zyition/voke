//! workspaces provider：编辑器（Zed / VSCode）的**最近工作区历史**。
//!
//! ## 为什么不再扫描目录
//!
//! 上一版（`f5c71b3`）是目录扫描：在 `~/source ~/projects ~/code ~/dev ~/repos`
//! 下找含 `.git` / `.vscode` / `.zed` 的目录。实机搜不到东西，根因查明——
//! 这些默认根在用户 C: 盘**一个都不存在**，真实项目几乎全在 WSL 里。
//!
//! 换方向：**编辑器自己就在 SQLite 里记了最近工作区**（Zed 的 `db.sqlite`、
//! VSCode 的 `state.vscdb`），直接读它们的历史。好处是既有路径又有位置
//! （本地 / WSL / SSH / 容器），不需要猜。
//!
//! ## 两个维度
//!
//! 条目 = **编辑器维度**（[`EditorKind`]：Zed / VSCode）× **位置维度**
//! （[`LocationKind`]：本地 / WSL / SSH / 容器 / 其它）。位置类型**完全来自
//! 编辑器各自的数据**，不硬编码成 WSL。
//!
//! ## 数据流
//!
//! ```text
//! voke-sys 只读打开编辑器 SQLite（mode=ro，绝不写原库）
//!         │  Vec<ZedWorkspaceRow>              Vec<VsCodeRecentPayload>
//!         ▼                                    ▼
//! voke_core::workspace::workspaces()（纯函数）
//!         │  URI 解码 → 位置分类 → 脏数据过滤 → 显示名 → 排序去重
//!         ▼
//! app 层注入 fetch 闭包 → WorkspaceProvider::query → nucleo 打分
//!         │
//!         ▼
//! Vec<Match>（Action::Launch { path: <记录它的编辑器>, args: [...] }；
//!             编辑器不可用 / 位置类型不支持 → Launch 路径本身，
//!             dispatcher 把目录转交 explorer.exe，即"退回资源管理器"）
//! ```
//!
//! ## 分层
//!
//! - **本文件全是纯逻辑 + serde_json**：不碰文件、不碰 SQLite、不碰 Windows API，
//!   Linux 上直接单测。
//! - 只读 SQLite 与 `%LOCALAPPDATA%` / `%USERPROFILE%` 解析在 `voke-sys/src/workspace.rs`
//!   （sys 依赖 core 复用本文件的结构体，core 不依赖 sys）。
//! - 打开命令的可执行文件路径由 app 层注入（core 只拼参数）。

use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use nucleo_matcher::{Config, Matcher};
use serde::Deserialize;

use crate::action::Action;
use crate::matcher::score_entry;
use crate::provider::{BoxFuture, Match, Provider, ProviderConfig, QueryCtx, ready};
use crate::search_key::{SearchKeys, SearchKeysCache};

// ══════════════════════════════════════════════════════════════════
// 共享数据结构体
//
// 定义在这里（而不是 sys）是为了让 sys 只做 IO：sys 依赖 core 复用这些类型，
// core 保持零 rusqlite / 零 windows。
// ════════════════════════════════════════════════════════════════

/// 编辑器维度：记录这条工作区历史的是哪个编辑器。
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub enum EditorKind {
    /// `%LOCALAPPDATA%\Zed\db\*\db.sqlite`
    Zed,
    /// `state.vscdb` → `history.recentlyOpenedPathsList`
    VsCode,
}

impl EditorKind {
    /// 副标题里显示的编辑器标记（与 UI 文案对齐，改这里就够了）。
    pub fn label(&self) -> &'static str {
        match self {
            Self::Zed => "Zed",
            Self::VsCode => "VSCode",
        }
    }
}

/// 位置维度：这个工作区**实际在哪儿**。
///
/// 取值以**编辑器各自的数据**为准（Zed 的 `remote_connections.kind`、VSCode 的
/// `remoteAuthority` 与 URI scheme），**不硬编码成 WSL**：真实数据里既有本地也有
/// WSL / SSH，还可能是 dev container / docker。
///
/// 认不出来的落 [`LocationKind::Other`]（UI 上标"其它"、动作退回资源管理器），
/// 而不是猜一个可能错的位置。
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum LocationKind {
    /// 编辑器本机（Windows 路径，如 `C:\Users\x\code\proj`）。
    Local,
    /// WSL 发行版里（Linux 风格路径）。
    Wsl {
        user: Option<String>,
        distro: Option<String>,
    },
    /// SSH 远端。
    Ssh {
        user: Option<String>,
        host: String,
        port: Option<i64>,
    },
    /// 容器：dev container / docker / podman。
    Container { id: Option<String> },
    /// 其它 / 未识别。保留原始 kind，便于日后按真实数据补支持。
    Other { kind: String, label: Option<String> },
}

impl LocationKind {
    pub fn is_local(&self) -> bool {
        matches!(self, Self::Local)
    }

    /// 副标题里的位置标记，如 `本地` / `WSL openSUSE-Tumbleweed` / `SSH me@host:2222`。
    pub fn label(&self) -> String {
        match self {
            Self::Local => "本地".to_string(),
            Self::Wsl { distro, .. } => match distro {
                Some(d) => format!("WSL {d}"),
                None => "WSL".to_string(),
            },
            Self::Ssh { user, host, port } => {
                let base = match user {
                    Some(u) => format!("{u}@{host}"),
                    None => host.clone(),
                };
                match port {
                    Some(p) => format!("SSH {base}:{p}"),
                    None => format!("SSH {base}"),
                }
            }
            Self::Container { id } => match id {
                Some(i) => format!("容器 {i}"),
                None => "容器".to_string(),
            },
            Self::Other { kind, label } => match label {
                Some(l) => format!("其它 {l}"),
                None => format!("其它 {kind}"),
            },
        }
    }
}

/// 一条工作区历史条目。
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct WorkspaceEntry {
    /// 显示名（路径最后一段；`.code-workspace` 去掉扩展名）。
    pub name: String,
    /// 可展示路径（副标题末尾；也是"退回资源管理器"时的目标）。
    pub path: String,
    pub editor: EditorKind,
    pub location: LocationKind,
    /// 编辑器记录的原始时间戳（`YYYY-MM-DD HH:MM:SS`，可直接字符串比较）。
    /// VSCode 的 entries JSON **不带**时间戳 → 空串（排序时排最后，
    /// 组内保持文件里 entries 数组的顺序，那个顺序本身就是最近使用顺序）。
    pub timestamp: String,
    /// 打开这条工作区要追加给编辑器命令的参数。
    /// **空 = 该位置类型不被编辑器 CLI 支持** → 动作退回 Launch 路径本身。
    pub open_args: Vec<String>,
}

/// sys 层从 Zed `remote_connections` 读出的一行（原样搬运，语义在这里解释）。
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct ZedRemoteRow {
    /// `kind`：本机实测为 `wsl` / `ssh`。
    pub kind: String,
    pub host: Option<String>,
    pub port: Option<i64>,
    pub user: Option<String>,
    pub distro: Option<String>,
    pub container_id: Option<String>,
}

/// sys 层从 Zed `workspaces` 读出的一行（`remote_connections` 已 LEFT JOIN 好）。
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct ZedWorkspaceRow {
    /// `paths` 列原文（单根是裸路径；多根可能是 JSON 数组或换行分隔）。
    pub paths: String,
    pub timestamp: String,
    pub remote: Option<ZedRemoteRow>,
}

/// sys 层从 VSCode `state.vscdb` 读出的 `history.recentlyOpenedPathsList` 原文。
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct VsCodeRecentPayload {
    pub json: String,
    /// 库文件的修改时间（`YYYY-MM-DD HH:MM:SS`），只当排序锚点用。
    pub file_timestamp: String,
}

/// 打开工作区要用的外部命令。
///
/// `pre_args` 是为了 Windows 上的**批处理包装**：VSCode 的 CLI 是
/// `bin/code.cmd`，而 `Command::spawn`（CreateProcess）跑不了 `.cmd`
/// ——必须包一层 `cmd.exe /c <code.cmd 绝对路径>`。这是 Windows 的既有约束，
/// 不是本项目发明的：所以解析结果要么是"可直接 spawn 的 exe"，要么是
/// "cmd.exe + /c + 脚本绝对路径"。
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct EditorCommand {
    /// 可执行文件（全路径或 PATH 上的名字）。
    pub exe: String,
    /// 固定前缀参数。
    pub pre_args: Vec<String>,
}

/// 工作区数据源：`Arc<dyn Fn>` 让 provider 能塞进 `Vec<Box<dyn Provider>>`，
/// 闭包必须 `Send + Sync`（与 `WindowsProvider` 的 `WindowFetcher` 同款）。
pub type WorkspaceFetcher = Arc<dyn Fn() -> Vec<WorkspaceEntry> + Send + Sync>;

// ════════════════════════════════════════════════════════════════
// 纯字符串处理（放在 core：Linux 上随时可测）
// ════════════════════════════════════════════════════════════════

/// 去空白后非空的 `Option<String>`（SQLite 里空串与 NULL 都要当"没有"）。
fn non_empty(value: Option<String>) -> Option<String> {
    value
        .map(|v| v.trim().to_string())
        .filter(|v| !v.is_empty())
}

/// 在 [`non_empty`] 之上再挡掉**字符串** `"null"`。
///
/// 不是臆造的防御：本机 Zed 库的 `remote_connections` 里就有一条
/// `kind='ssh'`、而 `host` 是 TEXT 类型的 `'null'`（不是 SQL NULL，
/// `typeof(host)` = `text`）的记录。不挡掉就会拼出 `ssh://null/…` 这种
/// 必然失败的命令。
///
/// 只用于**会进命令行**的字段（host / user）——distro、container_id
/// 那些只用于展示，值为 `"null"` 顶多难看，不会让动作失败。
fn non_null_text(value: Option<String>) -> Option<String> {
    non_empty(value).filter(|v| !v.eq_ignore_ascii_case("null"))
}

fn hex_digit(byte: u8) -> Option<u8> {
    match byte {
        b'0'..=b'9' => Some(byte - b'0'),
        b'a'..=b'f' => Some(byte - b'a' + 10),
        b'A'..=b'F' => Some(byte - b'A' + 10),
        _ => None,
    }
}

/// 百分号解码（`%XX` → 字节）。
///
/// **`+` 不当空格**：URI 的 path 段里 `+` 是字面量（VSCode 用 `%2B` 编码
/// `wsl+distro` 里的加号，把它解成空格会毁掉路径）。
/// 非法/残缺的 `%` 序列原样保留（搜索路径纪律：不因脏数据 panic）。
pub fn percent_decode(input: &str) -> String {
    let bytes = input.as_bytes();
    let mut out: Vec<u8> = Vec::with_capacity(bytes.len());
    let mut i = 0;
    while i < bytes.len() {
        if bytes[i] == b'%'
            && i + 2 < bytes.len()
            && let (Some(hi), Some(lo)) = (hex_digit(bytes[i + 1]), hex_digit(bytes[i + 2]))
        {
            out.push(hi * 16 + lo);
            i += 3;
            continue;
        }
        out.push(bytes[i]);
        i += 1;
    }
    String::from_utf8_lossy(&out).into_owned()
}

/// 取路径的最后一段（`/` 与 `\` 都算分隔符——Zed 记 Windows 路径用 `\`，
/// VSCode URI 解码后是 `/`）。根路径（`/`）返回 `"/"`。
fn last_segment(path: &str) -> String {
    let trimmed = path.trim_end_matches(['/', '\\']);
    if trimmed.is_empty() {
        return path.to_string();
    }
    match trimmed.rsplit(['/', '\\']).next() {
        Some(seg) if !seg.is_empty() => seg.to_string(),
        _ => trimmed.to_string(),
    }
}

/// 显示名：路径最后一段；`.code-workspace` 去掉扩展名（VSCode 的 workspace 文件
/// 显示成文件名而不是"xxx.code-workspace"）。
///
/// 例：`/home/x/voke` → `voke`；`C:\x\code\my.code-workspace` → `my`。
pub fn display_name(path: &str) -> String {
    let seg = last_segment(path);
    match seg.strip_suffix(".code-workspace") {
        Some(stem) if !stem.is_empty() => stem.to_string(),
        _ => seg,
    }
}

/// `/c:/Users/x` → `C:/Users/x`（VSCode 的本地 URI 盘符是小写的，
/// 解码后统一成大写，展示与去重都稳定）。不是盘符形态的原样返回。
fn normalize_drive_letter(path: &str) -> String {
    let bytes = path.as_bytes();
    if bytes.len() >= 3 && bytes[0] == b'/' && bytes[1].is_ascii_alphabetic() && bytes[2] == b':' {
        let mut out = String::with_capacity(path.len());
        out.push(bytes[1].to_ascii_uppercase() as char);
        out.push(':');
        out.push_str(&path[3..]);
        return out;
    }
    path.to_string()
}

/// `file://wsl.localhost/<distro>/home/x` 这类：把第一段当发行版名，
/// 剩下的当路径返回。
fn split_first_segment(path: &str) -> (Option<String>, String) {
    let trimmed = path.trim_start_matches(['/', '\\']);
    match trimmed.find(['/', '\\']) {
        Some(pos) => (
            non_empty(Some(trimmed[..pos].to_string())),
            trimmed[pos..].to_string(),
        ),
        None => (non_empty(Some(trimmed.to_string())), String::new()),
    }
}

/// 最后一段是否像**文件名**（带扩展名）。
///
/// 脏数据过滤用：Zed 会把 `C:\Windows\Logs\CBS\CBS.log`、
/// `.../e2e-report.txt`、`~/.config/opencode/opencode.jsonc` 这类**单文件**
/// 也记进最近工作区，而工作区应当是目录。
///
/// 判据：最后一段含 `.`、后缀 1–8 个 ASCII 字母数字，且不是以 `.` 开头的
/// 隐藏目录（`.git` / `.vscode` 之类）。会误伤名字里带点的**目录**
/// （如 `my.project`），权衡后接受：真实项目目录带扩展名的极少，
/// 而把日志文件当工作区列出来的噪音更烦人。
fn looks_like_file(path: &str) -> bool {
    let seg = match path.rsplit(['/', '\\']).next() {
        Some(seg) => seg,
        None => return false,
    };
    if seg.is_empty() || seg.starts_with('.') {
        return false;
    }
    match seg.rsplit_once('.') {
        Some((stem, ext)) => {
            !stem.is_empty()
                && (1..=8).contains(&ext.len())
                && ext.bytes().all(|b| b.is_ascii_alphanumeric())
        }
        None => false,
    }
}

/// 是否是系统目录（绝无可能是用户工作区）。
///
/// Windows 与 POSIX 两套前缀**分开比**：`\` 归一化会把 `/proc` 变成 `\proc`，
/// 而 POSIX 前缀也认不出 `C:\Windows`，所以各比一次。
fn is_system_root(path: &str) -> bool {
    let lower = path.to_ascii_lowercase();
    let win = lower.replace('/', "\\");
    // 裸盘符根（`C:\`）只能**全等**匹配——当前缀比会把整块 C: 盘都判成脏数据。
    if win == "c:\\" {
        return true;
    }
    let windows = [
        "c:\\windows",
        "c:\\program files",
        "c:\\programdata",
        "c:\\$recycle.bin",
    ];
    if windows.iter().any(|p| win.starts_with(p)) {
        return true;
    }
    let posix = ["/proc", "/sys", "/dev", "/boot", "/etc"];
    posix.iter().any(|p| lower.starts_with(p)) || lower == "/"
}

/// 脏数据：空串 / 系统目录 / 看起来是文件。
///
/// 只做**字符串判据**——WSL / SSH 路径在 Windows 进程里 stat 不到，
/// 靠文件系统校验反而会把真条目剔掉。
pub fn is_dirty_path(path: &str) -> bool {
    let trimmed = path.trim();
    if trimmed.is_empty() {
        return true;
    }
    is_system_root(trimmed) || looks_like_file(trimmed)
}

// ════════════════════════════════════════════════════════════════
// VSCode：URI 与 entries JSON
// ════════════════════════════════════════════════════════════════

/// 拆 `<scheme>://<authority><path>`；不是这个形态返回 `None`。
fn split_uri(uri: &str) -> Option<(&str, &str, &str)> {
    let (scheme, rest) = uri.split_once("://")?;
    if scheme.is_empty() || scheme.contains(['/', '\\']) {
        return None;
    }
    match rest.find('/') {
        Some(pos) => Some((scheme, &rest[..pos], &rest[pos..])),
        None => Some((scheme, rest, "")),
    }
}

/// VSCode 的 `remoteAuthority` → 位置类型。
///
/// | `remoteAuthority` | 位置 | 出处 |
/// |---|---|---|
/// | 空 | 本地 | 本机实测（本地条目就是空串） |
/// | `wsl+<distro>` | WSL | 本机实测 9 条（大小写混用都有：
///   `wsl+openSUSE-Tumbleweed` 与 `wsl+opensuse-tumbleweed`） |
/// | `ssh-remote+<host>` / `ssh-remote+<user>@<host>` | SSH | vscode-remote 官方命名，
///   **本机无样本** |
/// | `dev-container+<id>` / `attached-container+<id>` | 容器 | 官方命名，**本机无样本** |
/// | 其它 `<kind>+<spec>` | 其它（保留 kind） | 兜底 |
pub fn vscode_location(remote_authority: &str) -> LocationKind {
    let raw = remote_authority.trim();
    if raw.is_empty() {
        return LocationKind::Local;
    }
    let (kind, spec) = raw.split_once('+').unwrap_or((raw, ""));
    let spec = non_empty(Some(percent_decode(spec)));
    match kind.trim().to_ascii_lowercase().as_str() {
        "wsl" => LocationKind::Wsl {
            user: None,
            distro: spec,
        },
        "ssh-remote" => {
            let raw_spec = spec.unwrap_or_default();
            let (user, host_port) = match raw_spec.rsplit_once('@') {
                Some((u, h)) => (non_empty(Some(u.to_string())), h),
                None => (None, raw_spec.as_str()),
            };
            let (host, port) = match host_port.rsplit_once(':') {
                // `[::1]:22` → host = `[::1]`；`[::1]`（无端口）不会落进这里，
                // 因为 `1]` 不是纯数字。
                Some((h, p)) if !p.is_empty() && p.bytes().all(|b| b.is_ascii_digit()) => {
                    (h, p.parse::<i64>().ok())
                }
                _ => (host_port, None),
            };
            LocationKind::Ssh {
                user,
                host: host.to_string(),
                port,
            }
        }
        "dev-container" | "attached-container" | "docker" => LocationKind::Container { id: spec },
        other => LocationKind::Other {
            kind: other.to_string(),
            label: spec,
        },
    }
}

/// URI → (位置类型, 可展示路径)。
///
/// | URI | 位置 | 路径 | 出处 |
/// |---|---|---|---|
/// | `file:///c%3A/Users/x/y` | 本地 | `C:/Users/x/y` | 本机 4 条（都是 `fileUri`，
///   被忽略；`folderUri` 无本地样本） |
/// | `file:///home/x/y` | 本地 | `/home/x/y` | POSIX 形态，**本机无样本** |
/// | `file://wsl.localhost/<distro>/home/x` | WSL | `/home/x` | 本机实测 2 条，
///   **不带 `remoteAuthority`**（所以只能靠 URI 自己判位置） |
/// | `vscode-remote://wsl%2B<distro>/home/x` | WSL | `/home/x` | 本机实测 9 条 |
/// | `vscode-remote://ssh-remote%2B<host>/home/x` | SSH | `/home/x` | 官方命名，
///   **本机无样本** |
/// | `vscode-remote://dev-container%2B<id>/path` | 容器 | `/path` | 官方命名，
///   **本机无样本** |
pub fn vscode_uri(uri: &str) -> (LocationKind, String) {
    let Some((scheme, authority, raw_path)) = split_uri(uri) else {
        // 不是 URI（脏数据 / 未知形态）：原样当本地路径处理，不 panic。
        return (LocationKind::Local, uri.to_string());
    };
    let path = percent_decode(raw_path);
    match scheme {
        "vscode-remote" => (vscode_location(&percent_decode(authority)), path),
        "file" => {
            let host = authority.to_ascii_lowercase();
            if host.is_empty() {
                (LocationKind::Local, normalize_drive_letter(&path))
            } else if host == "wsl.localhost" || host == "wsl$" {
                let (distro, rest) = split_first_segment(&path);
                (LocationKind::Wsl { user: None, distro }, rest)
            } else {
                // UNC（`file://server/share/...`）等：留着 authority，落「其它」。
                (
                    LocationKind::Other {
                        kind: "unc".to_string(),
                        label: Some(authority.to_string()),
                    },
                    path,
                )
            }
        }
        other => (
            LocationKind::Other {
                kind: other.to_string(),
                label: None,
            },
            path,
        ),
    }
}

#[derive(Deserialize)]
struct RecentPathsPayload {
    entries: Option<Vec<RecentEntry>>,
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
struct RecentEntry {
    folder_uri: Option<String>,
    file_uri: Option<String>,
    workspace: Option<RecentWorkspace>,
    remote_authority: Option<String>,
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
struct RecentWorkspace {
    config_path: Option<String>,
}

/// VSCode 一条 entry 的三种形态。
enum VsCodeEntryKind {
    /// `folderUri`：文件夹。
    Folder(String),
    /// `workspace.configPath`：`.code-workspace` 工作区文件。
    Workspace(String),
    /// `fileUri`：单文件。**忽略**（工作区应当是目录）。
    File,
}

fn vscode_entry_kind(entry: &RecentEntry) -> Option<VsCodeEntryKind> {
    if let Some(uri) = entry
        .folder_uri
        .as_deref()
        .map(str::trim)
        .filter(|s| !s.is_empty())
    {
        return Some(VsCodeEntryKind::Folder(uri.to_string()));
    }
    if let Some(uri) = entry
        .workspace
        .as_ref()
        .and_then(|w| w.config_path.as_deref())
        .map(str::trim)
        .filter(|s| !s.is_empty())
    {
        return Some(VsCodeEntryKind::Workspace(uri.to_string()));
    }
    if entry.file_uri.is_some() {
        return Some(VsCodeEntryKind::File);
    }
    None
}

/// VSCode 一条 entry → 工作区条目（脏数据返回 `None`）。
fn vscode_entry(entry: &RecentEntry, file_timestamp: &str) -> Option<WorkspaceEntry> {
    let (kind, remote_authority) = match vscode_entry_kind(entry)? {
        VsCodeEntryKind::Folder(uri) => (VsCodeOpenKind::Folder, uri),
        VsCodeEntryKind::Workspace(uri) => (VsCodeOpenKind::Workspace, uri),
        VsCodeEntryKind::File => return None,
    };
    // 路径与位置都从 URI 里解（`file://` 自己就能判本地 / WSL）；
    // 但当条目带了 `remoteAuthority` 时**优先信它**——那是未编码的原文，
    // 比 URI authority 里的 `%2B` 之类更可靠。
    let (from_uri, path) = vscode_uri(&remote_authority);
    let location = match entry.remote_authority.as_deref().map(str::trim) {
        Some(authority) if !authority.is_empty() => vscode_location(authority),
        _ => from_uri,
    };
    if is_dirty_path(&path) {
        return None;
    }
    let name = display_name(&path);
    Some(WorkspaceEntry {
        name,
        path,
        editor: EditorKind::VsCode,
        location,
        timestamp: file_timestamp.to_string(),
        open_args: kind.open_args(&remote_authority),
    })
}

/// VSCode 打开方式：`code --folder-uri <uri>` / `code --file-uri <uri>`
/// （对齐 PowerToys Run 的 VSCodeWorkspaces 插件）。
enum VsCodeOpenKind {
    Folder,
    Workspace,
}

impl VsCodeOpenKind {
    fn open_args(&self, uri: &str) -> Vec<String> {
        if uri.trim().is_empty() {
            return Vec::new();
        }
        vec![
            match self {
                Self::Folder => "--folder-uri",
                Self::Workspace => "--file-uri",
            }
            .to_string(),
            uri.to_string(),
        ]
    }
}

/// 解析一份 `history.recentlyOpenedPathsList` JSON 的 entries。
///
/// **绝不 panic**：坏 JSON / 缺 `entries` / 未知形态都返回空列表。
fn parse_recent_entries(json: &str) -> Vec<RecentEntry> {
    let Ok(payload) = serde_json::from_str::<RecentPathsPayload>(json) else {
        log::debug!("VSCode recentlyOpenedPathsList 解析失败，跳过");
        return Vec::new();
    };
    payload.entries.unwrap_or_default()
}

/// VSCode 历史 → 工作区条目（含过滤 / 排序 / 去重）。
pub fn vscode_entries(payloads: &[VsCodeRecentPayload]) -> Vec<WorkspaceEntry> {
    let mut out = Vec::new();
    for payload in payloads {
        for entry in parse_recent_entries(&payload.json) {
            out.extend(vscode_entry(&entry, &payload.file_timestamp));
        }
    }
    finalize(out)
}

// ════════════════════════════════════════════════════════════════
// Zed：workspaces 行 → 条目
// ════════════════════════════════════════════════════════════════

/// 把 Zed `paths` 列拆成一个或多个路径。
///
/// 本机样本**全是单根**（没有多根工作区样本），所以兼容两种可能的序列化形态：
/// JSON 数组（`["/a","/b"]`）与换行分隔（`/a\n/b`）；都解析不出来时把整串
/// 当单根兜底。空串返回空列表。
fn zed_row_paths(raw: &str) -> Vec<String> {
    let trimmed = raw.trim();
    if trimmed.is_empty() {
        return Vec::new();
    }
    if let Ok(paths) = serde_json::from_str::<Vec<String>>(trimmed) {
        let out: Vec<String> = paths
            .into_iter()
            .map(|p| p.trim().to_string())
            .filter(|p| !p.is_empty())
            .collect();
        if !out.is_empty() {
            return out;
        }
    }
    let out: Vec<String> = trimmed
        .lines()
        .map(|l| l.trim().to_string())
        .filter(|l| !l.is_empty())
        .collect();
    if out.is_empty() {
        return vec![trimmed.to_string()];
    }
    out
}

/// Zed `remote_connections` 一行 → 位置类型。
///
/// `kind` 是自由文本，这里按**实测 + 官方命名**认 `wsl` / `ssh` /
/// `dev-container` / `docker`，其余落 `Other`（UI 标"其它"，动作退回资源管理器）。
pub fn zed_location(remote: &ZedRemoteRow) -> LocationKind {
    let kind = remote.kind.trim().to_ascii_lowercase();
    // host / user 会进命令行，所以要挡掉字符串 `"null"`（见 `non_null_text`）。
    let host = non_null_text(remote.host.clone());
    let user = non_null_text(remote.user.clone());
    let distro = non_empty(remote.distro.clone());
    let container_id = non_empty(remote.container_id.clone());
    match kind.as_str() {
        "wsl" => LocationKind::Wsl { user, distro },
        // 没有 host 的 ssh 记录拼不出 `ssh://` URL（本机就有一条
        // `kind='ssh'` 但 `host` 是字符串 `'null'` 的脏记录，且当前没有被任何
        // workspaces 行引用）→ 落「其它」，不要生成残缺命令。
        "ssh" => match host {
            Some(host) => LocationKind::Ssh {
                user,
                host,
                port: remote.port,
            },
            None => LocationKind::Other { kind, label: user },
        },
        "dev-container" | "docker" | "container" => LocationKind::Container { id: container_id },
        other => LocationKind::Other {
            kind: other.to_string(),
            label: host.or(distro).or(container_id),
        },
    }
}

/// Zed 打开方式（每条都标注了**来源**，没查到出处的就不编）：
/// - 本地：`zed <win 路径>`（本机 `%LOCALAPPDATA%\Programs\Zed\bin\zed` 包装脚本）
/// - WSL：`zed --wsl <user>@<distro> <linux 路径>`（同上，脚本原文是
///   `"$ZED_PATH/zed.exe" --wsl "$WSL_USER@$WSL_DISTRO_NAME" "$@"`）。
///   `user` 缺失时退化成 `--wsl <distro>`（**未实测**：本机所有 wsl 记录都带 user；
///   实测请见 docs/BACKLOG.md 的人肉待验清单）
/// - SSH：`zed ssh://[<user>@]<host>[:<port>]/<path>`（**zed.dev「Remote Development」
///   文档原文**："you can run `zed ssh://[<user>@]<host>[:<port>]/<path>` to open a
///   remote folder/file directly"）
/// - 容器 / 其它：**没有查到**官方的"打开已记录的容器路径"入口（zed.dev 的
///   Remote Development 页只讲 SSH，没有 docker / dev container 的 CLI 或 URL），
///   所以不编 flag → 空参数 → 动作退回资源管理器。
fn zed_open_args(location: &LocationKind, path: &str) -> Vec<String> {
    match location {
        LocationKind::Local => vec![path.to_string()],
        LocationKind::Wsl { user, distro } => {
            let Some(distro) = distro else {
                return Vec::new();
            };
            let target = match user {
                Some(u) => format!("{u}@{distro}"),
                None => distro.clone(),
            };
            vec!["--wsl".to_string(), target, path.to_string()]
        }
        LocationKind::Ssh { user, host, port } => {
            if host.is_empty() {
                return Vec::new();
            }
            let auth = match user {
                Some(u) => format!("{u}@"),
                None => String::new(),
            };
            let port = match port {
                Some(p) => format!(":{p}"),
                None => String::new(),
            };
            let p = if path.starts_with('/') {
                path.to_string()
            } else {
                format!("/{path}")
            };
            vec![format!("ssh://{auth}{host}{port}{p}")]
        }
        LocationKind::Container { .. } | LocationKind::Other { .. } => Vec::new(),
    }
}

/// Zed 一行 → 若干条目（多根时一行为多个条目；脏数据跳过）。
fn zed_row_entries(row: &ZedWorkspaceRow) -> Vec<WorkspaceEntry> {
    let location = match &row.remote {
        Some(remote) => zed_location(remote),
        None => LocationKind::Local,
    };
    zed_row_paths(&row.paths)
        .into_iter()
        .filter(|path| !is_dirty_path(path))
        .map(|path| WorkspaceEntry {
            name: display_name(&path),
            path: path.clone(),
            editor: EditorKind::Zed,
            location: location.clone(),
            timestamp: row.timestamp.clone(),
            open_args: zed_open_args(&location, &path),
        })
        .collect()
}

/// Zed 历史 → 工作区条目（含过滤 / 排序 / 去重）。
pub fn zed_entries(rows: &[ZedWorkspaceRow]) -> Vec<WorkspaceEntry> {
    let mut out = Vec::new();
    for row in rows {
        out.extend(zed_row_entries(row));
    }
    finalize(out)
}

// ════════════════════════════════════════════════════════════════
// 统一出口：过滤 → 排序 → 去重
// ════════════════════════════════════════════════════════════════

/// 编辑器历史的统一出口：解析 → 脏数据过滤 → 排序 → 去重。
///
/// app 层把两个 sys reader 的结果一起喂进来即可（见 `voke-app/src/search.rs`）。
pub fn workspaces(zed: &[ZedWorkspaceRow], vscode: &[VsCodeRecentPayload]) -> Vec<WorkspaceEntry> {
    let mut out = Vec::new();
    for row in zed {
        out.extend(zed_row_entries(row));
    }
    for payload in vscode {
        for entry in parse_recent_entries(&payload.json) {
            out.extend(vscode_entry(&entry, &payload.file_timestamp));
        }
    }
    finalize(out)
}

fn finalize(mut entries: Vec<WorkspaceEntry>) -> Vec<WorkspaceEntry> {
    entries.retain(|e| !is_dirty_path(&e.path));
    sort_workspaces(&mut entries);
    dedupe_workspaces(entries)
}

/// 排序：**时间戳降序**，空时间戳（VSCode）排最后；同组内保持插入顺序。
///
/// 两个来源的组内顺序本身都有意义：Zed 来自 SQL `ORDER BY timestamp DESC`，
/// VSCode 的 entries 数组就是最近使用顺序。所以这里用**稳定排序**，
/// 只跨组比较时间戳，不额外按名字重排（避免打乱 VSCode 的 recency）。
pub fn sort_workspaces(entries: &mut [WorkspaceEntry]) {
    entries.sort_by(
        |a, b| match (a.timestamp.is_empty(), b.timestamp.is_empty()) {
            (true, true) => std::cmp::Ordering::Equal,
            (true, false) => std::cmp::Ordering::Greater,
            (false, true) => std::cmp::Ordering::Less,
            (false, false) => b.timestamp.cmp(&a.timestamp),
        },
    );
}

/// 同路径去重：键 = (编辑器, 路径)。
///
/// 同一条路径被 Zed 和 VSCode 都记过时**两条都留**——它们要用各自的编辑器打开，
/// 合并掉等于吞掉一个入口。同一编辑器内的重复（例如 Zed 里两条 remote id 不同但
/// 路径相同的记录）保留排序后靠前的那条（= 更新的那条）。
pub fn dedupe_workspaces(entries: Vec<WorkspaceEntry>) -> Vec<WorkspaceEntry> {
    let mut seen: std::collections::HashSet<(EditorKind, String)> =
        std::collections::HashSet::new();
    entries
        .into_iter()
        .filter(|e| seen.insert((e.editor, e.path.clone())))
        .collect()
}

// ════════════════════════════════════════════════════════════════
// provider
// ════════════════════════════════════════════════════════════════

/// 带搜索键的工作区（条目每次查询重建，搜索键走 [`SearchKeysCache`]，
/// 与 windows/terminal 同款）。
struct IndexedWorkspace {
    entry: WorkspaceEntry,
    name_keys: Arc<SearchKeys>,
    path_keys: Arc<SearchKeys>,
}

/// 缓存的一次读取结果。
struct CachedFetch {
    at: Instant,
    entries: Vec<WorkspaceEntry>,
}

/// workspaces provider。
pub struct WorkspaceProvider {
    config: ProviderConfig,
    /// 数据源（生产 = `voke_sys` 的两个 sqlite reader 之和；调用方保证后台线程）。
    fetch: WorkspaceFetcher,
    /// 打开 Zed 历史用的命令；`None` = Zed 不可用 → 退回资源管理器。
    zed: Option<EditorCommand>,
    /// 打开 VSCode 历史用的命令；`None` = VSCode 不可用 → 退回资源管理器。
    code: Option<EditorCommand>,
    /// 缓存有效期：TTL 内不重读（见下）。
    ttl: Duration,
    /// TTL 缓存。`Mutex` 只是因为 query 收 `&self`；查询本来就是串行的。
    cache: Mutex<Option<CachedFetch>>,
    /// 搜索键记忆化（容量有上限，见类型文档）。
    keys: SearchKeysCache,
}

impl WorkspaceProvider {
    /// provider 的稳定 id。
    pub const ID: &'static str = "workspaces";

    /// 单次非空查询返回的最大条数（理由同 apps/windows/terminal）。
    pub const MAX_RESULTS: usize = 50;

    /// 默认缓存有效期。
    ///
    /// 比目录扫描那一版（10 秒）短：这里只读几个几 KB 的小库，代价低，
    /// 但"刚打开的新工作区立刻能搜到"更有价值。5 秒内不重复打 SQLite。
    pub const DEFAULT_TTL: Duration = Duration::from_secs(5);

    /// 用数据源 + 两个编辑器命令构造（默认 TTL）。
    pub fn new(
        fetch: WorkspaceFetcher,
        zed: Option<EditorCommand>,
        code: Option<EditorCommand>,
    ) -> Self {
        Self::with_ttl(fetch, zed, code, Self::DEFAULT_TTL)
    }

    /// 显式指定 TTL（测试用：`Duration::ZERO` = 每次查询都重读）。
    pub fn with_ttl(
        fetch: WorkspaceFetcher,
        zed: Option<EditorCommand>,
        code: Option<EditorCommand>,
        ttl: Duration,
    ) -> Self {
        Self {
            config: ProviderConfig {
                enabled: true,
                // 直接全局搜（与 windows/terminal 同级）。
                is_global: true,
                weight: 1.0,
                prefix: None,
                activation: crate::config::Activation::Direct,
                count: 5,
            },
            fetch,
            zed,
            code,
            ttl,
            cache: Mutex::new(None),
            keys: SearchKeysCache::new(),
        }
    }

    /// 取工作区列表：TTL 内用缓存，过期重读并更新缓存。
    fn entries(&self) -> Vec<WorkspaceEntry> {
        let mut guard = self
            .cache
            .lock()
            .unwrap_or_else(|poisoned| poisoned.into_inner());
        if let Some(cached) = guard.as_ref()
            && cached.at.elapsed() < self.ttl
        {
            return cached.entries.clone();
        }
        let entries = (self.fetch)();
        *guard = Some(CachedFetch {
            at: Instant::now(),
            entries: entries.clone(),
        });
        entries
    }

    /// 打开动作：**用记录它的编辑器**打开。
    ///
    /// - 编辑器可用且位置类型受支持 → `Launch { exe, pre_args + open_args }`
    ///   （dispatcher 走 `Command::spawn`）
    /// - 否则 → `Launch { path, [] }`，dispatcher 把目录转交 explorer.exe
    ///   （"退回资源管理器"）。远程路径在 Windows 上找不到，这一步只会记一条
    ///   错误日志，不会 panic。
    ///
    /// **控制台窗口**：VSCode 的 CLI 是批处理 `bin\code.cmd`，`Command::spawn`
    /// （CreateProcess）跑不了，只能套 `cmd.exe /c <code.cmd>`（见
    /// [`EditorCommand`] 与 `voke-sys::workspace::vscode_command`）。那个 `cmd.exe`
    /// 是通路的实现细节、不是用户要的终端，启动瞬间会闪一下黑框。
    ///
    /// Zed 也要藏：`zed_command()` 解析到的 `bin\Zed.exe`（官方包装脚本
    /// `bin\zed` 实际调用的那个）是**控制台子系统**的 CLI（本机实测 PE
    /// Subsystem = 3，3.4MB；根目录 360MB 的 `Zed.exe` 才是 GUI 子系统），
    /// 直接 spawn 会闪控制台窗口。所以两个编辑器都走
    /// [`Action::launch_with_hidden`]（dispatcher 侧加 `CREATE_NO_WINDOW`）。
    fn open_action(&self, entry: &WorkspaceEntry) -> Action {
        let command = match entry.editor {
            EditorKind::Zed => &self.zed,
            EditorKind::VsCode => &self.code,
        };
        match command {
            Some(cmd) if !entry.open_args.is_empty() => {
                let mut args = cmd.pre_args.clone();
                args.extend(entry.open_args.iter().cloned());
                Action::launch_with_hidden(cmd.exe.clone(), args)
            }
            _ => Action::launch_with(entry.path.clone(), Vec::new()),
        }
    }

    /// 副标题：`编辑器标记 · 位置标记 · 路径`。
    ///
    /// 例：`Zed · WSL openSUSE-Tumbleweed · /home/testuser/Work/projects/personal/voke`
    /// / `VSCode · 本地 · C:\Users\testuser\code\beta`
    fn subtitle(entry: &WorkspaceEntry) -> String {
        format!(
            "{} · {} · {}",
            entry.editor.label(),
            entry.location.label(),
            entry.path
        )
    }

    fn to_match(&self, ws: &IndexedWorkspace, score: f32) -> Match {
        Match::execute(
            Self::ID,
            ws.entry.name.clone(),
            Self::subtitle(&ws.entry),
            self.open_action(&ws.entry),
            score,
        )
    }

    fn index(&self, entry: WorkspaceEntry) -> IndexedWorkspace {
        let name_keys = self.keys.get(&entry.name);
        let path_keys = self.keys.get(&entry.path);
        IndexedWorkspace {
            name_keys,
            path_keys,
            entry,
        }
    }
}

impl Provider for WorkspaceProvider {
    fn id(&self) -> &'static str {
        Self::ID
    }

    fn name(&self) -> &'static str {
        "工作区"
    }

    /// 别名：`ws` = workspaces 的常用缩写，`gzq` = 工作区拼音首字母。
    ///
    /// 冲突检查（别名是**子串**匹配，见 `QueryEngine::plugin_picker_matches`：
    /// `alias.contains(filter)`）：
    /// 现有别名 app/yy、win/ck、a/awake、wt/terminal、t/sj/ts/time、b/b64、
    /// u、r/sj/rand、c/calc/jsq 里没有任何一个包含 `ws` 或 `gzq`；
    /// 反向 `ws` / `gzq` 也不含它们中的任何一个（单字母过滤本来就多命中，
    /// 不构成"永远抢不到"的抢占）。
    fn aliases(&self) -> &'static [&'static str] {
        &["ws", "gzq"]
    }

    fn description(&self) -> &'static str {
        "Zed / VSCode 最近打开的工作区，用记录它的编辑器打开"
    }

    fn config(&self) -> &ProviderConfig {
        &self.config
    }

    fn set_config(&mut self, config: ProviderConfig) {
        self.config = config;
    }

    fn placeholder(&self, sub_mode: Option<&str>) -> &'static str {
        let _ = sub_mode;
        "输入工作区名过滤，Enter 用对应编辑器打开"
    }

    /// 查询实现。
    ///
    /// 条目列表带 TTL 缓存（默认 5 秒），避免每次按键都打 SQLite；
    /// 重读跑在 background executor 上（app 层的 `spawn_query`）。
    ///
    /// 空 query 语义与 apps/windows/terminal 一致：返回全部、score = 1.0，
    /// 由 `QueryEngine::rank` 的频率加成排序；顺序保留 [`sort_workspaces`] 的结果。
    fn query(&self, ctx: &QueryCtx) -> BoxFuture<'static, Vec<Match>> {
        let workspaces: Vec<IndexedWorkspace> =
            self.entries().into_iter().map(|e| self.index(e)).collect();

        let query = ctx.query.trim();
        if query.is_empty() {
            let matches = workspaces.iter().map(|w| self.to_match(w, 1.0)).collect();
            return ready(matches);
        }

        let mut matcher = Matcher::new(Config::DEFAULT);
        let mut matches: Vec<Match> = workspaces
            .iter()
            .filter_map(|w| {
                let name = score_entry(&mut matcher, query, &w.name_keys).map(|s| s.score);
                let path = score_entry(&mut matcher, query, &w.path_keys).map(|s| s.score);
                let best = match (name, path) {
                    (Some(a), Some(b)) => Some(a.max(b)),
                    (Some(a), None) => Some(a),
                    (None, Some(b)) => Some(b),
                    (None, None) => None,
                };
                best.map(|s| self.to_match(w, s))
            })
            .collect();
        matches.sort_by(|a, b| {
            b.score
                .partial_cmp(&a.score)
                .unwrap_or(std::cmp::Ordering::Equal)
        });
        matches.truncate(Self::MAX_RESULTS);
        ready(matches)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::future::Future;
    use std::sync::atomic::{AtomicUsize, Ordering};

    fn block_on<F: Future>(f: F) -> F::Output {
        let mut f = Box::pin(f);
        let mut cx = std::task::Context::from_waker(std::task::Waker::noop());
        match f.as_mut().poll(&mut cx) {
            std::task::Poll::Ready(v) => v,
            std::task::Poll::Pending => panic!("workspaces provider 的 future 应当立即就绪"),
        }
    }

    fn zed_remote(kind: &str, user: Option<&str>, distro: Option<&str>) -> ZedRemoteRow {
        ZedRemoteRow {
            kind: kind.to_string(),
            host: None,
            port: None,
            user: user.map(str::to_string),
            distro: distro.map(str::to_string),
            container_id: None,
        }
    }

    // ── percent_decode ──

    #[test]
    fn decodes_percent_escapes() {
        assert_eq!(
            percent_decode("wsl%2Bopensuse-tumbleweed"),
            "wsl+opensuse-tumbleweed"
        );
        assert_eq!(percent_decode("file:///c%3A/Users/x"), "file:///c:/Users/x");
    }

    #[test]
    fn keeps_plus_and_broken_escapes_verbatim() {
        // `+` 是字面量，不能当空格；残缺的 `%2` 原样保留（不 panic）。
        assert_eq!(percent_decode("a+b"), "a+b");
        assert_eq!(
            percent_decode("100%20ok%2"),
            "100%20ok%2".replace("%20", " ")
        );
        assert_eq!(percent_decode("100%2"), "100%2");
    }

    // ── display_name ──

    #[test]
    fn display_name_takes_last_segment() {
        assert_eq!(
            display_name("/home/testuser/Work/projects/personal/voke"),
            "voke"
        );
        assert_eq!(display_name(r"C:\Users\testuser\code\beta"), "beta");
        assert_eq!(display_name("/home/testuser/proj/"), "proj");
    }

    #[test]
    fn display_name_strips_code_workspace_extension() {
        assert_eq!(
            display_name(r"C:\x\code\my.code-workspace"),
            "my",
            ".code-workspace 要去掉扩展名"
        );
        // 路径本身就是 ".code-workspace" 的极端情况：保底不返回空串。
        assert_eq!(display_name("/x/.code-workspace"), ".code-workspace");
    }

    // ── 脏数据 ──

    #[test]
    fn empty_and_system_paths_are_dirty() {
        assert!(is_dirty_path(""));
        assert!(is_dirty_path("   "));
        assert!(is_dirty_path(r"C:\Windows\Logs\CBS\CBS.log"));
        assert!(is_dirty_path(r"c:\windows\system32"));
        assert!(is_dirty_path("/proc/self"));
        assert!(is_dirty_path(r"C:\"));
    }

    #[test]
    fn files_are_dirty_directories_are_not() {
        assert!(is_dirty_path("/home/testuser/e2e-report.txt"));
        assert!(is_dirty_path(
            "/home/testuser/.config/opencode/opencode.jsonc"
        ));
        assert!(!is_dirty_path("/home/testuser/Work/projects/personal/voke"));
        assert!(!is_dirty_path("/home/testuser/qt-comfyui-minimax-h3"));
        // 隐藏目录（以 . 开头）不算文件扩展名。
        assert!(!is_dirty_path("/home/testuser/proj/.git"));
    }

    // ── VSCode URI ──

    #[test]
    fn vscode_uri_local_windows() {
        let (location, path) = vscode_uri("file:///c%3A/Users/testuser/AppData/Roaming/Rime");
        assert_eq!(location, LocationKind::Local);
        assert_eq!(path, "C:/Users/testuser/AppData/Roaming/Rime");
    }

    #[test]
    fn vscode_uri_local_posix() {
        let (location, path) = vscode_uri("file:///home/testuser/proj");
        assert_eq!(location, LocationKind::Local);
        assert_eq!(path, "/home/testuser/proj");
    }

    #[test]
    fn vscode_uri_wsl_via_unc_authority() {
        // 本机实测形态：`file://wsl.localhost/openSUSE-Tumbleweed/home/testuser/...`
        let (location, path) =
            vscode_uri("file://wsl.localhost/openSUSE-Tumbleweed/home/testuser/x");
        assert_eq!(
            location,
            LocationKind::Wsl {
                user: None,
                distro: Some("openSUSE-Tumbleweed".into())
            }
        );
        assert_eq!(path, "/home/testuser/x");
    }

    #[test]
    fn vscode_uri_wsl_via_remote_scheme() {
        // `wsl%2B` 解码后是 `wsl+`
        let (location, path) =
            vscode_uri("vscode-remote://wsl%2Bopensuse-tumbleweed/home/testuser/x");
        assert_eq!(
            location,
            LocationKind::Wsl {
                user: None,
                distro: Some("opensuse-tumbleweed".into())
            }
        );
        assert_eq!(path, "/home/testuser/x");
    }

    #[test]
    fn vscode_uri_ssh_and_container() {
        let (location, path) = vscode_uri("vscode-remote://ssh-remote%2Bme%40box/home/me/p");
        assert_eq!(
            location,
            LocationKind::Ssh {
                user: Some("me".into()),
                host: "box".into(),
                port: None
            }
        );
        assert_eq!(path, "/home/me/p");

        let (location, _) = vscode_uri("vscode-remote://dev-container%2Bdeadbeef/work/src");
        assert_eq!(
            location,
            LocationKind::Container {
                id: Some("deadbeef".into())
            }
        );
    }

    #[test]
    fn vscode_uri_unknown_scheme_falls_back_to_other() {
        let (location, path) = vscode_uri("codespaces://foo/bar");
        assert_eq!(
            location,
            LocationKind::Other {
                kind: "codespaces".into(),
                label: None
            }
        );
        assert_eq!(path, "/bar");
        // 完全不是 URI 的形态：原样保留，不 panic。
        let (location, path) = vscode_uri(r"C:\x\y");
        assert_eq!(location, LocationKind::Local);
        assert_eq!(path, r"C:\x\y");
    }

    // ── VSCode entries JSON ──

    const VSCODE_JSON: &str = r#"{"entries":[
        {"folderUri":"vscode-remote://wsl%2Bopensuse-tumbleweed/home/testuser/Work/projects/personal/voke","remoteAuthority":"wsl+openSUSE-Tumbleweed"},
        {"folderUri":"file:///c%3A/Users/testuser/code/beta","remoteAuthority":""},
        {"workspace":{"configPath":"file:///c%3A/Users/testuser/code/my.code-workspace"}},
        {"fileUri":"file:///c%3A/Users/testuser/AppData/Roaming/Rime/user.yaml","remoteAuthority":""},
        {"folderUri":"vscode-remote://ssh-remote%2Bme%40box/home/me/remote-proj","remoteAuthority":"ssh-remote+me@box"},
        {"folderUri":"vscode-remote://dev-container%2Bdeadbeef/workspace/src","remoteAuthority":"dev-container+deadbeef"}
    ]}"#;

    fn vscode_payloads() -> Vec<VsCodeRecentPayload> {
        vec![VsCodeRecentPayload {
            json: VSCODE_JSON.to_string(),
            file_timestamp: "2026-06-05 14:09:00".to_string(),
        }]
    }

    #[test]
    fn vscode_entries_cover_all_shapes() {
        let entries = vscode_entries(&vscode_payloads());
        let names: Vec<&str> = entries.iter().map(|e| e.name.as_str()).collect();
        // fileUri（user.yaml）被忽略 → 6 条里剩 5 条。
        assert_eq!(names, vec!["voke", "beta", "my", "remote-proj", "src"]);

        // WSL：编辑器 = VSCode，位置 = WSL
        assert_eq!(
            entries[0].location,
            LocationKind::Wsl {
                user: None,
                distro: Some("openSUSE-Tumbleweed".into())
            }
        );
        assert_eq!(
            entries[0].open_args,
            vec![
                "--folder-uri".to_string(),
                "vscode-remote://wsl%2Bopensuse-tumbleweed/home/testuser/Work/projects/personal/voke"
                    .to_string()
            ]
        );

        // 本地：盘符大写
        assert_eq!(entries[1].path, "C:/Users/testuser/code/beta");
        assert!(entries[1].location.is_local());

        // .code-workspace 走 --file-uri
        assert_eq!(
            entries[2].open_args,
            vec![
                "--file-uri".to_string(),
                "file:///c%3A/Users/testuser/code/my.code-workspace".to_string()
            ]
        );

        // SSH：位置 = SSH（URI 原样传给 code，scheme 分类只用于副标题）
        assert!(matches!(
            &entries[3].location,
            LocationKind::Ssh { user, host, port } if user.as_deref() == Some("me") && host == "box" && port.is_none()
        ));

        // 容器
        assert_eq!(
            entries[4].location,
            LocationKind::Container {
                id: Some("deadbeef".into())
            }
        );
    }

    #[test]
    fn vscode_bad_json_never_panics() {
        assert!(
            vscode_entries(&[VsCodeRecentPayload {
                json: "{ not json".to_string(),
                file_timestamp: String::new(),
            }])
            .is_empty()
        );
        assert!(
            vscode_entries(&[VsCodeRecentPayload {
                json: "{}".to_string(),
                file_timestamp: String::new(),
            }])
            .is_empty()
        );
        assert!(vscode_entries(&[VsCodeRecentPayload::default()]).is_empty());
        assert!(vscode_entries(&[]).is_empty());
    }

    // ── Zed 行 ──

    #[test]
    fn zed_rows_become_entries_by_location() {
        let rows = vec![
            ZedWorkspaceRow {
                paths: "/home/testuser/Work/projects/personal/voke".into(),
                timestamp: "2026-10-08 09:41:31".into(),
                remote: Some(zed_remote(
                    "wsl",
                    Some("testuser"),
                    Some("openSUSE-Tumbleweed"),
                )),
            },
            ZedWorkspaceRow {
                paths: r"C:\Users\testuser\code\beta".into(),
                timestamp: "2026-10-07 08:00:00".into(),
                remote: None,
            },
            // 没有 user 的 wsl 记录：`--wsl <distro>`（username 可省，见 zed --help）
            ZedWorkspaceRow {
                paths: "/home/testuser/misc".into(),
                timestamp: "2026-08-03 13:12:06".into(),
                remote: Some(zed_remote("wsl", None, Some("openSUSE-Tumbleweed"))),
            },
        ];
        let entries = zed_entries(&rows);
        assert_eq!(entries.len(), 3);
        // 按时间降序
        assert_eq!(entries[0].name, "voke");
        assert_eq!(
            entries[0].open_args,
            vec![
                "--wsl".to_string(),
                "testuser@openSUSE-Tumbleweed".to_string(),
                "/home/testuser/Work/projects/personal/voke".to_string(),
            ]
        );
        assert_eq!(entries[1].name, "beta");
        assert_eq!(
            entries[1].open_args,
            vec![r"C:\Users\testuser\code\beta".to_string()]
        );
        assert_eq!(
            entries[2].open_args,
            vec![
                "--wsl".to_string(),
                "openSUSE-Tumbleweed".to_string(),
                "/home/testuser/misc".to_string(),
            ]
        );
    }

    #[test]
    fn zed_dirty_rows_are_dropped() {
        let rows = vec![
            // 本机真实脏数据
            ZedWorkspaceRow {
                paths: r"C:\Windows\Logs\CBS\CBS.log".into(),
                timestamp: "2026-10-08 03:49:18".into(),
                remote: None,
            },
            // 空串 / NULL 形态（sys 层会把 NULL 过滤掉，这里再兜一层）
            ZedWorkspaceRow::default(),
        ];
        assert!(zed_entries(&rows).is_empty());
    }

    #[test]
    fn zed_multi_root_paths_are_supported() {
        let json_array = ZedWorkspaceRow {
            paths: r#"["/home/x/alpha", "/home/x/beta"]"#.into(),
            timestamp: "2026-10-01 00:00:00".into(),
            remote: None,
        };
        let names: Vec<String> = zed_entries(&[json_array])
            .into_iter()
            .map(|e| e.name)
            .collect();
        assert_eq!(names, vec!["alpha".to_string(), "beta".to_string()]);

        let newline = ZedWorkspaceRow {
            paths: "/home/x/gamma\n/home/x/delta".into(),
            timestamp: "2026-10-01 00:00:00".into(),
            remote: None,
        };
        let names: Vec<String> = zed_entries(&[newline])
            .into_iter()
            .map(|e| e.name)
            .collect();
        assert_eq!(names, vec!["gamma".to_string(), "delta".to_string()]);
    }

    #[test]
    fn zed_ssh_rows_build_ssh_url() {
        let row = ZedWorkspaceRow {
            paths: "/home/me/proj".into(),
            timestamp: "2026-10-02 00:00:00".into(),
            remote: Some(ZedRemoteRow {
                kind: "ssh".into(),
                host: Some("box".into()),
                port: Some(2222),
                user: Some("me".into()),
                distro: None,
                container_id: None,
            }),
        };
        let entries = zed_entries(&[row]);
        assert_eq!(
            entries[0].open_args,
            vec!["ssh://me@box:2222/home/me/proj".to_string()]
        );
    }

    /// 本机 `remote_connections` 里 `id=2` 的真实脏记录：`kind='ssh'`，而 `host`
    /// 是 **TEXT 的 `'null'`**（不是 SQL NULL，`typeof(host)` = `text`）。
    /// 当前没有被任何 workspaces 行引用，但引用了就会拼出 `ssh://null/…`。
    #[test]
    fn zed_ssh_row_with_literal_null_host_falls_back_to_other() {
        let row = ZedWorkspaceRow {
            paths: "/home/me/proj".into(),
            timestamp: "2026-10-02 00:00:00".into(),
            remote: Some(ZedRemoteRow {
                kind: "ssh".into(),
                host: Some("null".into()),
                ..Default::default()
            }),
        };
        let entries = zed_entries(&[row]);
        assert_eq!(entries.len(), 1, "条目仍然要列出来（用户能看到）");
        assert_eq!(
            entries[0].location,
            LocationKind::Other {
                kind: "ssh".into(),
                label: None
            },
            "拼不出 ssh:// URL → 落「其它」"
        );
        assert!(
            entries[0].open_args.is_empty(),
            "绝不能生成 ssh://null/… 这种必然失败的命令"
        );
    }

    #[test]
    fn zed_unsupported_location_has_no_open_args() {
        let row = ZedWorkspaceRow {
            paths: "/work/src".into(),
            timestamp: "2026-10-02 00:00:00".into(),
            remote: Some(ZedRemoteRow {
                kind: "docker".into(),
                host: None,
                port: None,
                user: None,
                distro: None,
                container_id: Some("abc123".into()),
            }),
        };
        let entries = zed_entries(&[row]);
        assert_eq!(
            entries[0].location,
            LocationKind::Container {
                id: Some("abc123".into())
            },
            "容器条目仍然要列出来（用户能看到）"
        );
        assert!(
            entries[0].open_args.is_empty(),
            "但不支持打开 → 退回资源管理器"
        );
    }

    // ── 排序 / 去重 ──

    #[test]
    fn sorts_by_timestamp_descending_with_empty_last() {
        let mut entries = vec![
            entry("old", "2026-01-01 00:00:00"),
            entry("vscode", ""),
            entry("new", "2026-10-08 09:41:31"),
            entry("mid", "2026-06-01 00:00:00"),
        ];
        sort_workspaces(&mut entries);
        let names: Vec<&str> = entries.iter().map(|e| e.name.as_str()).collect();
        assert_eq!(names, vec!["new", "mid", "old", "vscode"]);
    }

    #[test]
    fn dedupes_same_editor_and_path() {
        let entries = vec![
            entry("voke", "2026-10-08 00:00:00"),
            entry("voke", "2026-09-08 00:00:00"),
            // 同路径但另一个编辑器 → 保留（要用各自的编辑器打开）
            WorkspaceEntry {
                editor: EditorKind::VsCode,
                ..entry("voke", "2026-10-07 00:00:00")
            },
        ];
        let out = dedupe_workspaces(entries);
        assert_eq!(out.len(), 2, "同编辑器同路径去重，跨编辑器保留: {out:?}");
    }

    fn entry(name: &str, timestamp: &str) -> WorkspaceEntry {
        WorkspaceEntry {
            name: name.to_string(),
            path: format!("/home/testuser/{name}"),
            editor: EditorKind::Zed,
            location: LocationKind::Local,
            timestamp: timestamp.to_string(),
            open_args: Vec::new(),
        }
    }

    // ── provider ──

    fn query(provider: &WorkspaceProvider, q: &str) -> Vec<Match> {
        let ctx = QueryCtx::new(q.to_string(), q.to_string());
        block_on(provider.query(&ctx))
    }

    fn provider_with(entries: Vec<WorkspaceEntry>) -> WorkspaceProvider {
        WorkspaceProvider::with_ttl(
            Arc::new(move || entries.clone()),
            Some(EditorCommand {
                exe: "zed".into(),
                pre_args: Vec::new(),
            }),
            Some(EditorCommand {
                exe: "cmd.exe".into(),
                pre_args: vec!["/c".into(), r"C:\vscode\bin\code.cmd".into()],
            }),
            Duration::ZERO,
        )
    }

    #[test]
    fn id_and_config() {
        let p = provider_with(Vec::new());
        assert_eq!(p.id(), "workspaces");
        assert_eq!(p.name(), "工作区");
        assert_eq!(p.aliases(), &["ws", "gzq"]);
        assert!(p.config().enabled && p.config().is_global);
        assert_eq!(p.config().weight, 1.0);
        assert_eq!(p.config().prefix, None, "无前缀，全局直接搜");
    }

    #[test]
    fn empty_query_lists_all_at_full_score() {
        let p = provider_with(vec![entry("alpha", ""), entry("beta", "")]);
        let hits = query(&p, "");
        let titles: Vec<&str> = hits.iter().map(|m| m.title.as_str()).collect();
        assert_eq!(titles, vec!["alpha", "beta"]);
        assert!(hits.iter().all(|m| m.score == 1.0));
        assert!(hits.iter().all(|m| m.interact == crate::Interact::Execute));
    }

    #[test]
    fn subtitle_carries_editor_and_location_markers() {
        let p = provider_with(vec![
            WorkspaceEntry {
                name: "voke".into(),
                path: "/home/testuser/voke".into(),
                editor: EditorKind::Zed,
                location: LocationKind::Wsl {
                    user: Some("testuser".into()),
                    distro: Some("openSUSE-Tumbleweed".into()),
                },
                timestamp: "2026-10-08 09:41:31".into(),
                open_args: Vec::new(),
            },
            WorkspaceEntry {
                name: "beta".into(),
                path: r"C:\Users\testuser\code\beta".into(),
                editor: EditorKind::VsCode,
                location: LocationKind::Local,
                timestamp: String::new(),
                open_args: Vec::new(),
            },
        ]);
        let hits = query(&p, "");
        let zed = hits.iter().find(|m| m.title == "voke").unwrap();
        assert_eq!(
            zed.subtitle,
            "Zed · WSL openSUSE-Tumbleweed · /home/testuser/voke"
        );
        let code = hits.iter().find(|m| m.title == "beta").unwrap();
        assert_eq!(
            code.subtitle,
            r"VSCode · 本地 · C:\Users\testuser\code\beta"
        );
    }

    #[test]
    fn action_uses_the_editor_that_recorded_it() {
        let p = provider_with(vec![
            WorkspaceEntry {
                name: "beta".into(),
                path: r"C:\Users\testuser\code\beta".into(),
                editor: EditorKind::VsCode,
                location: LocationKind::Local,
                timestamp: String::new(),
                open_args: vec![
                    "--folder-uri".into(),
                    "file:///C:/Users/testuser/code/beta".into(),
                ],
            },
            WorkspaceEntry {
                name: "remote".into(),
                path: "/home/me/remote".into(),
                editor: EditorKind::Zed,
                location: LocationKind::Ssh {
                    user: Some("me".into()),
                    host: "box".into(),
                    port: None,
                },
                timestamp: "2026-10-08 00:00:00".into(),
                open_args: vec!["ssh://me@box/home/me/remote".into()],
            },
        ]);
        let hits = query(&p, "");
        let code = hits.iter().find(|m| m.title == "beta").unwrap();
        assert_eq!(
            code.action,
            Action::Launch {
                path: "cmd.exe".into(),
                args: vec![
                    "/c".into(),
                    r"C:\vscode\bin\code.cmd".into(),
                    "--folder-uri".into(),
                    "file:///C:/Users/testuser/code/beta".into(),
                ],
                working_dir: String::new(),
                elevated: false,
                // VSCode 经 `cmd.exe /c code.cmd` 启动 → 隐藏控制台
                no_console: true,
                lnk_path: String::new(),
            }
        );
        let zed = hits.iter().find(|m| m.title == "remote").unwrap();
        assert_eq!(
            zed.action,
            Action::Launch {
                path: "zed".into(),
                args: vec!["ssh://me@box/home/me/remote".into()],
                working_dir: String::new(),
                elevated: false,
                // Zed 起的是控制台子系统的 `bin\Zed.exe` CLI → 一样要隐藏
                no_console: true,
                lnk_path: String::new(),
            }
        );
    }

    #[test]
    fn without_editor_falls_back_to_opening_the_path() {
        let p = WorkspaceProvider::with_ttl(
            Arc::new(|| vec![entry("alpha", "")]),
            None,
            None,
            Duration::ZERO,
        );
        let hits = query(&p, "alpha");
        assert_eq!(
            hits[0].action,
            Action::Launch {
                path: "/home/testuser/alpha".into(),
                args: Vec::new(),
                working_dir: String::new(),
                elevated: false,
                no_console: false,
                lnk_path: String::new(),
            },
            "编辑器不可用时 Launch 路径本身（dispatcher 转交 explorer）"
        );
    }

    #[test]
    fn unsupported_location_falls_back_to_opening_the_path() {
        let p = provider_with(vec![WorkspaceEntry {
            name: "src".into(),
            path: "/work/src".into(),
            editor: EditorKind::Zed,
            location: LocationKind::Container { id: None },
            timestamp: "2026-10-08 00:00:00".into(),
            open_args: Vec::new(),
        }]);
        let hits = query(&p, "src");
        assert_eq!(
            hits[0].action,
            Action::Launch {
                path: "/work/src".into(),
                args: Vec::new(),
                working_dir: String::new(),
                elevated: false,
                no_console: false,
                lnk_path: String::new(),
            }
        );
    }

    #[test]
    fn matches_by_name_and_path_fragment() {
        let p = provider_with(vec![
            entry("voke", "2026-10-08 00:00:00"),
            entry("other", "2026-10-07 00:00:00"),
        ]);
        assert_eq!(query(&p, "vok").len(), 1);
        // 路径片段也能命中（/home/testuser/other）
        assert!(query(&p, "testuser").iter().any(|m| m.title == "other"));
        assert!(query(&p, "zzzqqq").is_empty());
    }

    #[test]
    fn results_are_sorted_descending_and_capped() {
        let entries: Vec<WorkspaceEntry> = (0..80)
            .map(|i| entry(&format!("ws{i:02}"), "2026-10-08 00:00:00"))
            .collect();
        let p = provider_with(entries);
        let hits = query(&p, "ws");
        assert_eq!(hits.len(), WorkspaceProvider::MAX_RESULTS);
        let scores: Vec<f32> = hits.iter().map(|m| m.score).collect();
        for pair in scores.windows(2) {
            assert!(pair[0] >= pair[1], "必须降序: {scores:?}");
        }
    }

    /// TTL 内重查**不重读**；过期（TTL=0）才重读。
    #[test]
    fn ttl_cache_avoids_refetch_within_ttl_and_refetches_after() {
        let calls = Arc::new(AtomicUsize::new(0));
        let calls_in = calls.clone();
        let fetch: WorkspaceFetcher = Arc::new(move || {
            calls_in.fetch_add(1, Ordering::SeqCst);
            vec![entry("alpha", "")]
        });
        // 长 TTL：两次查询只读一次。
        let p = WorkspaceProvider::with_ttl(fetch, None, None, Duration::from_secs(3600));
        query(&p, "a");
        query(&p, "b");
        assert_eq!(calls.load(Ordering::SeqCst), 1, "TTL 内不得重读");

        // TTL=0：每次查询都过期 → 每次重读。
        let calls2 = Arc::new(AtomicUsize::new(0));
        let calls2_in = calls2.clone();
        let fetch2: WorkspaceFetcher = Arc::new(move || {
            calls2_in.fetch_add(1, Ordering::SeqCst);
            Vec::new()
        });
        let p2 = WorkspaceProvider::with_ttl(fetch2, None, None, Duration::ZERO);
        query(&p2, "a");
        query(&p2, "b");
        assert_eq!(calls2.load(Ordering::SeqCst), 2, "TTL 过期必须重读");
    }

    /// 缓存内容过期后要用**新**数据（不是只验证调用次数）。
    #[test]
    fn expired_cache_returns_fresh_entries() {
        let calls = Arc::new(AtomicUsize::new(0));
        let calls_in = calls.clone();
        let fetch: WorkspaceFetcher = Arc::new(move || {
            let n = calls_in.fetch_add(1, Ordering::SeqCst);
            vec![entry(if n == 0 { "first" } else { "second" }, "")]
        });
        let p = WorkspaceProvider::with_ttl(fetch, None, None, Duration::ZERO);
        assert_eq!(query(&p, "")[0].title, "first");
        assert_eq!(query(&p, "")[0].title, "second", "过期后必须拿到新数据");
    }
}
