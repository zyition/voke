//! 编辑器最近工作区历史的**只读 SQLite 读取**（workspaces provider 的 sys 半边）。
//!
//! ## 为什么读编辑器自己的库
//!
//! 上一版（`f5c71b3`）是目录扫描：在 `~/source ~/projects ~/code ~/dev ~/repos`
//! 下找含 `.git` / `.vscode` / `.zed` 的目录。实机搜不到东西，根因查明——
//! 这些默认根在用户 C: 盘**一个都不存在**，真实项目几乎全在 WSL 里。
//!
//! 换成读编辑器自己的历史库，好处是**路径和位置都是编辑器自己记的**
//! （Zed 的 `remote_connections`、VSCode 的 `remoteAuthority` / URI scheme），
//! 不需要猜路径、也不需要硬编码"全在 WSL"。
//!
//! ## 分层约定（与 `terminal` 模块同款）
//!
//! - **路径拼接是纯函数**，任何平台都能编译、都能在 Linux 上单测。
//! - **读 SQLite 也不挂 `cfg(windows)`**：rusqlite 在任何平台都能编，这样
//!   "用临时 SQLite fixture 建真表" 的测试在 WSL 里就有真实覆盖（M2 教训：
//!   纯逻辑挂 `cfg(windows)` 下就测不到）。
//! - 只有读 `%LOCALAPPDATA%` / `%USERPROFILE%` / `%APPDATA%` 的包装函数是
//!   `#[cfg(windows)]`（这三个环境变量的语义只在 Windows 上成立）。
//!
//! 解析 / 过滤 / 排序去重**不在这一层**——那些是纯逻辑，在
//! `voke_core::workspace`（`workspaces`），这里只负责"把行/原文读出来"。
//!
//! ## 绝不写原库
//!
//! 只以 `SQLITE_OPEN_READ_ONLY` + URI 参数 `mode=ro` 打开。常驻 launcher
//! 在编辑器运行时读它的库，任何写操作（哪怕只是建 `-shm` / `-wal`）都可能
//! 干扰编辑器。所以**没有** `immutable=1` 之外的退化路径以外的写可能。

use std::path::{Path, PathBuf};

use rusqlite::{Connection, OpenFlags};

use voke_core::{VsCodeRecentPayload, ZedRemoteRow, ZedWorkspaceRow};
// `EditorCommand` 只在 Windows 上构造（它的形状由 Windows 的执行语义决定，
// 见 [`vscode_command`]），所以 import 也跟着 cfg——否则 Linux 构建会有
// "unused import" 警告。
#[cfg(windows)]
use voke_core::EditorCommand;

// ════════════════════════════════════════════════════════════════
// 路径定位（纯函数，任何平台可测）
// ════════════════════════════════════════════════════════════════

/// Zed 的数据库目录（`%LOCALAPPDATA%\Zed\db`）。
pub const ZED_DB_DIR_SUFFIX: &[&str] = &["Zed", "db"];

/// Zed 的 stable 通道目录名（`db\0-stable`）。本机还有 `0-global`。
pub const ZED_STABLE_CHANNEL: &str = "0-stable";

/// Zed 的数据库文件名。
pub const ZED_DB_FILE: &str = "db.sqlite";

/// VSCode ≥1.118 的共享库（`%USERPROFILE%\.vscode-shared\sharedStorage\state.vscdb`）。
///
/// **本机实测**：`history.recentlyOpenedPathsList` 只在这个库里有（16 条 entries）。
pub const VSCODE_SHARED_SUFFIX: &[&str] = &[".vscode-shared", "sharedStorage", "state.vscdb"];

/// VSCode 1.64–1.117 的兜底库（`%APPDATA%\Code\User\globalStorage\state.vscdb`）。
///
/// 本机实测：这个库里**没有** `history.recentlyOpenedPathsList`（键列表里不存在），
/// 所以对用户来说它当前读出来是空的；留着是因为它在新老版本切换时是唯一的来源。
pub const VSCODE_GLOBAL_SUFFIX: &[&str] = &["Code", "User", "globalStorage", "state.vscdb"];

/// Zed 的数据库目录（`%LOCALAPPDATA%\Zed\db`）。
pub fn zed_db_dir(localappdata: &Path) -> PathBuf {
    ZED_DB_DIR_SUFFIX
        .iter()
        .fold(localappdata.to_path_buf(), |acc, part| acc.join(part))
}

/// Zed 各通道的数据库文件（`db\*\db.sqlite`）。
///
/// **glob 全部通道**：本机有 `0-stable`（30 条真实记录）与 `0-global`（4KB 空库）。
/// 只认 stable 会在用户换通道后整片消失，所以全收、stable 排前面
/// （同路径去重时保留靠前的那条，见 `voke_core::workspace::dedupe_workspaces`）。
///
/// 目录不存在 / 读不出来时返回空列表（不 panic）。
pub fn zed_db_paths(zed_db: &Path) -> Vec<PathBuf> {
    let Ok(entries) = std::fs::read_dir(zed_db) else {
        log::debug!("Zed 数据库目录不存在，跳过: {}", zed_db.display());
        return Vec::new();
    };
    let mut dirs: Vec<PathBuf> = entries.filter_map(Result::ok).map(|e| e.path()).collect();
    // 先按名字排序保证顺序稳定，再把 stable 通道提到最前（`false` 排在 `true` 前）。
    dirs.sort();
    dirs.sort_by_key(|p| p.file_name().and_then(|n| n.to_str()) != Some(ZED_STABLE_CHANNEL));
    dirs.into_iter()
        .map(|dir| dir.join(ZED_DB_FILE))
        // 只收真实存在的文件：不存在的文件由 SQLite 报 "unable to open"，
        // 那是每次查询都会刷的噪音，不如在这里挡掉。
        .filter(|path| path.is_file())
        .collect()
}

/// VSCode 的 `state.vscdb` 候选路径，**共享库在前**（≥1.118 的真实来源）。
///
/// 纯函数：只拼路径，不检查存在性（不存在的文件由读取方跳过，
/// 与 `terminal::settings_paths` 同款约定）。
pub fn vscode_db_paths(userprofile: &Path, appdata: &Path) -> Vec<PathBuf> {
    [
        (&VSCODE_SHARED_SUFFIX, userprofile),
        (&VSCODE_GLOBAL_SUFFIX, appdata),
    ]
    .iter()
    .map(|(suffix, root)| {
        suffix
            .iter()
            .fold(root.to_path_buf(), |acc, part| acc.join(part))
    })
    .collect()
}

// ════════════════════════════════════════════════════════════════
// 只读打开
// ════════════════════════════════════════════════════════════════

/// 把路径转成 SQLite URI（`file:` 前缀，盘符与分隔符都转成 `/`）。
///
/// `%` 必须最先转义，否则后面补的 `%3f` / `%23` 会被二次解码；
/// `?` 与 `#` 在 URI 里分别是查询串与片段的起点，原样出现会截断路径。
fn read_only_uri(path: &Path) -> String {
    let normalized = path.to_string_lossy().replace('\\', "/");
    let escaped = normalized
        .replace('%', "%25")
        .replace('?', "%3f")
        .replace('#', "%23");
    format!("file:{escaped}")
}

/// 只读打开一个 SQLite 库；**任何失败都返回 `None`（绝不 panic）**。
///
/// 两级尝试，因为 Zed 的库是 **WAL 模式**且带真数据（本机实测：
/// `db.sqlite` 29MB + `db.sqlite-wal` 17.5MB，WAL 比主库新 30 分钟，
/// 里面带着更新的 `timestamp`）：
///
/// 1. `mode=ro`——能读到 WAL 里的最新数据，前提是 `-shm` 存在且可写。
///    拿不到（缺 `-shm` / 被独占锁）就退化到
/// 2. `immutable=1&mode=ro`——完全不动文件、连 `-shm` 都不需要，
///    代价是**看不到 WAL**（时间戳可能略旧）。
///
/// 退化成"数据略旧"好过退化成"一条都没有"。
fn open_read_only(path: &Path) -> Option<Connection> {
    if !path.is_file() {
        // 编辑器没装 / 通道目录是空的，是常态，不该刷日志。
        log::debug!("编辑器历史库不存在，跳过: {}", path.display());
        return None;
    }
    let base = read_only_uri(path);
    let flags = OpenFlags::SQLITE_OPEN_READ_ONLY | OpenFlags::SQLITE_OPEN_URI;
    for uri in [
        format!("{base}?mode=ro"),
        format!("{base}?immutable=1&mode=ro"),
    ] {
        match Connection::open_with_flags(&uri, flags) {
            Ok(conn) => return Some(conn),
            Err(err) => log::debug!("只读打开 {} 失败（mode=ro 退化中）: {err}", path.display()),
        }
    }
    // 被独占锁 / 不是 SQLite 文件 / 权限不足：常驻 launcher 不该因此挂，
    // 下一次按键会重试。
    log::warn!("编辑器历史库无法只读打开，跳过: {}", path.display());
    None
}

// ════════════════════════════════════════════════════════════════
// 读取：Zed
// ════════════════════════════════════════════════════════════════

/// 查 Zed 的 `workspaces` 行（`remote_connections` 已 LEFT JOIN 好）。
///
/// `paths IS NOT NULL` 只挡 NULL——本机另有 2 行是**空串**（`''`），
/// 那一层留给 core 的脏数据过滤（纯字符串判据，好测）。
const ZED_SQL: &str = "
    SELECT w.paths, w.timestamp, r.kind, r.host, r.port, r.user, r.distro, r.container_id
    FROM workspaces w
    LEFT JOIN remote_connections r ON r.id = w.remote_connection_id
    WHERE w.paths IS NOT NULL
    ORDER BY w.timestamp DESC";

/// 从一个 Zed 库读出行；缺表 / 空库 / 非 SQLite 都返回空（不 panic）。
fn zed_rows_in(path: &Path) -> Vec<ZedWorkspaceRow> {
    let Some(conn) = open_read_only(path) else {
        return Vec::new();
    };
    // 库能打开但表不对（版本变了 / 不是 Zed 的库）：prepare 会失败（SQLITE_NOTADB
    // 或 no such table），同样静默跳过。
    let Ok(mut stmt) = conn.prepare(ZED_SQL) else {
        log::debug!("Zed 历史库缺少 workspaces 表，跳过: {}", path.display());
        return Vec::new();
    };
    let Ok(mapped) = stmt.query_map([], |row| {
        let kind = row.get::<_, Option<String>>(2)?;
        let host = row.get::<_, Option<String>>(3)?;
        let port = row.get::<_, Option<i64>>(4)?;
        let user = row.get::<_, Option<String>>(5)?;
        let distro = row.get::<_, Option<String>>(6)?;
        let container_id = row.get::<_, Option<String>>(7)?;
        // `r.kind` 为 NULL 说明这条是本地工作区（LEFT JOIN 没接上）→ `remote: None`。
        let remote = kind.map(|kind| ZedRemoteRow {
            kind,
            host,
            port,
            user,
            distro,
            container_id,
        });
        Ok(ZedWorkspaceRow {
            paths: row.get::<_, Option<String>>(0)?.unwrap_or_default(),
            timestamp: row.get::<_, Option<String>>(1)?.unwrap_or_default(),
            remote,
        })
    }) else {
        log::debug!("Zed 历史库读取失败，跳过: {}", path.display());
        return Vec::new();
    };
    mapped.filter_map(Result::ok).collect()
}

/// 从若干个 Zed 库读出全部行（按传入顺序，stable 在前）。
pub fn zed_rows_from(paths: &[PathBuf]) -> Vec<ZedWorkspaceRow> {
    paths.iter().flat_map(|path| zed_rows_in(path)).collect()
}

// ════════════════════════════════════════════════════════════════
// 读取：VSCode
// ════════════════════════════════════════════════════════════════

/// `history.recentlyOpenedPathsList` 的值：entries 数组的 JSON 原文。
const VSCODE_SQL: &str =
    "SELECT value FROM ItemTable WHERE key = 'history.recentlyOpenedPathsList'";

/// 从一个 VSCode 库读出 entries JSON 原文。
///
/// 只取原文、**不解析**：JSON 容错与 entries → 条目的转换是
/// `voke_core::workspace` 的纯逻辑（Linux 上单测覆盖）。
fn vscode_payload_in(path: &Path) -> Option<VsCodeRecentPayload> {
    let conn = open_read_only(path)?;
    // `query_row` 在"没有这一行"时返回 `QueryReturnedNoRows`——那是
    // "库里没记这个键"（例如老的 globalStorage 库），静默跳过即可。
    let json = conn
        .query_row(VSCODE_SQL, [], |row| row.get::<_, String>(0))
        .ok()?;
    Some(VsCodeRecentPayload {
        json,
        file_timestamp: file_stamp(path),
    })
}

/// 从若干个 VSCode 库读出全部 payload（按传入顺序，共享库在前）。
pub fn vscode_payloads_from(paths: &[PathBuf]) -> Vec<VsCodeRecentPayload> {
    paths
        .iter()
        .filter_map(|path| vscode_payload_in(path))
        .collect()
}

// ════════════════════════════════════════════════════════════════
// 时间戳（文件 mtime → `YYYY-MM-DD HH:MM:SS`）
// ════════════════════════════════════════════════════════════════

/// 库文件的修改时间，只当排序锚点用。
///
/// VSCode 的 entries JSON **不带**每条的时间戳（本机 16 条全是裸 URI），
/// 所以拿文件 mtime 当这一批的共同锚点：组内顺序仍是 entries 数组顺序
/// （= 最近使用顺序，core 用稳定排序保住了），跨组则与 Zed 的真实时间戳可比。
fn file_stamp(path: &Path) -> String {
    let Some(secs) = std::fs::metadata(path)
        .ok()
        .and_then(|meta| meta.modified().ok())
        .and_then(|modified| modified.duration_since(std::time::UNIX_EPOCH).ok())
        .map(|dur| dur.as_secs() as i64)
    else {
        return String::new();
    };
    epoch_to_stamp(secs)
}

/// epoch 秒（UTC）→ `YYYY-MM-DD HH:MM:SS`。
///
/// 不引 chrono（`voke-sys` 不该为一个格式化多一份时间依赖），用 days-from-civil
/// 的逆变换（Howard Hinnant 的经典整数算法，纯整数、无查表、无溢出风险）。
pub fn epoch_to_stamp(secs: i64) -> String {
    // 1970-01-01 是 day 0；先平移到"以 0000-03-01 为 era 起点"的坐标系。
    let z = secs.div_euclid(86_400) + 719_468;
    let era = z.div_euclid(146_097);
    let doe = z.rem_euclid(146_097); // [0, 146096]
    let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365; // [0, 399]
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100); // [0, 365]
    let mp = (5 * doy + 2) / 153; // [0, 11]
    let day = doy - (153 * mp + 2) / 5 + 1; // [1, 31]
    let month = if mp < 10 { mp + 3 } else { mp - 9 }; // [1, 12]
    let year = yoe + era * 400 + i64::from(month <= 2);

    let rem = secs.rem_euclid(86_400);
    format!(
        "{year:04}-{:02}-{:02} {:02}:{:02}:{:02}",
        month,
        day,
        rem / 3600,
        rem % 3600 / 60,
        rem % 60
    )
}

// ════════════════════════════════════════════════════════════════
// Windows 包装（唯一依赖环境变量语义的一层）
// ════════════════════════════════════════════════════════════════

/// 本机所有 Zed 通道的最近工作区行。
#[cfg(windows)]
pub fn zed_workspaces() -> Vec<ZedWorkspaceRow> {
    let Ok(localappdata) = std::env::var("LOCALAPPDATA") else {
        log::warn!("LOCALAPPDATA 未设置，Zed 工作区历史不可用");
        return Vec::new();
    };
    zed_rows_from(&zed_db_paths(&zed_db_dir(Path::new(&localappdata))))
}

/// 本机的 VSCode 最近工作区（共享库 + 兜底库）。
#[cfg(windows)]
pub fn vscode_workspaces() -> Vec<VsCodeRecentPayload> {
    let (Ok(userprofile), Ok(appdata)) = (std::env::var("USERPROFILE"), std::env::var("APPDATA"))
    else {
        log::warn!("USERPROFILE / APPDATA 未设置，VSCode 工作区历史不可用");
        return Vec::new();
    };
    vscode_payloads_from(&vscode_db_paths(
        Path::new(&userprofile),
        Path::new(&appdata),
    ))
}

// ════════════════════════════════════════════════════════════════
// 编辑器的打开命令（app 层注入 `WorkspaceProvider` 用）
// ════════════════════════════════════════════════════════════════

/// Zed CLI 的候选路径（相对 `%LOCALAPPDATA%`），官方包装脚本跑的那个在前。
///
/// 本机 `%LOCALAPPDATA%\Programs\Zed\bin\zed` 里就是
/// `"$ZED_PATH/zed.exe" "$@"`（ZED_PATH = bin 目录），所以 `bin\Zed.exe`
/// 才是官方 CLI 的等价入口；根目录下的 `Zed.exe` 是 360MB 的主程序，
/// 也能接受 `--wsl` 之类的参数，留作第二个候选。
///
/// **子系统实测（PE OptionalHeader.Subsystem）**：`bin\Zed.exe` = 3
/// （控制台，3.4MB CLI），根目录 `Zed.exe` = 2（GUI 主程序）。所以走第一个
/// 候选启动时会闪控制台窗口——调用方（core 的 `open_action`）要配
/// `CREATE_NO_WINDOW`。
pub const ZED_EXE_CANDIDATES: &[&[&str]] = &[
    &["Programs", "Zed", "bin", "Zed.exe"],
    &["Programs", "Zed", "Zed.exe"],
];

/// VSCode CLI 的候选路径（相对 `%LOCALAPPDATA%`）。
///
/// **CLI 是批处理脚本 `bin\code.cmd`**（本机实测内容：
/// `set ELECTRON_RUN_AS_NODE=1` + `"%~dp0..\Code.exe" "...\resources\app\out\cli.js" %*`），
/// 不是 exe。
pub const VSCODE_CLI_CANDIDATES: &[&[&str]] = &[
    &["Programs", "Microsoft VS Code", "bin", "code.cmd"],
    &[
        "Programs",
        "Microsoft VS Code - Insiders",
        "bin",
        "code.cmd",
    ],
];

/// 在候选里挑**第一个真实存在**的文件（都不存在返回 `None`）。
///
/// 纯函数：只看文件系统，不读环境变量，所以 Linux 上也能测（建临时目录即可）。
pub fn first_existing(root: &Path, candidates: &[&[&str]]) -> Option<PathBuf> {
    candidates
        .iter()
        .map(|suffix| {
            suffix
                .iter()
                .fold(root.to_path_buf(), |acc, part| acc.join(part))
        })
        .find(|path| path.is_file())
}

/// Zed CLI 的绝对路径；没装 Zed 返回 `None`。
#[cfg(windows)]
pub fn zed_command() -> Option<EditorCommand> {
    let Ok(localappdata) = std::env::var("LOCALAPPDATA") else {
        return None;
    };
    first_existing(Path::new(&localappdata), ZED_EXE_CANDIDATES).map(|exe| EditorCommand {
        exe: exe.to_string_lossy().into_owned(),
        pre_args: Vec::new(),
    })
}

/// VSCode CLI 的调用形态；没装 VSCode 返回 `None`。
///
/// **为什么要包一层 `cmd.exe /c`**：dispatcher 的 `spawn` 是
/// `std::process::Command::new(..).spawn()`（CreateProcessW），而 MSDN 明确写着
/// 跑批处理文件必须启动命令解释器（`cmd.exe /c foo.cmd`）——直接拿 `.cmd`
/// 当可执行文件会失败。本机实测 `cmd.exe /c "...\bin\code.cmd" --version`
/// 输出 `1.140.0`，这条路是通的。
///
/// 代价：`cmd.exe` 是控制台程序，启动瞬间会闪一下控制台窗口（launcher 已经
/// 先隐藏了自己）。要消掉它得给 dispatcher 的 spawn 加 CREATE_NO_WINDOW，
/// 但那会连带把"用户从 apps 插件启动 cmd/pwsh"的窗口一起藏掉，所以没做。
#[cfg(windows)]
pub fn vscode_command() -> Option<EditorCommand> {
    let Ok(localappdata) = std::env::var("LOCALAPPDATA") else {
        return None;
    };
    let cli = first_existing(Path::new(&localappdata), VSCODE_CLI_CANDIDATES)?;
    Some(EditorCommand {
        exe: std::env::var("COMSPEC").unwrap_or_else(|_| "cmd.exe".to_string()),
        pre_args: vec!["/c".to_string(), cli.to_string_lossy().into_owned()],
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    /// 临时目录里的一个库文件（跑完随目录一起被回收）。
    fn fixture(name: &str) -> PathBuf {
        let dir = std::env::temp_dir().join(format!("voke-sys-ws-{}-{name}", std::process::id()));
        let _ = std::fs::create_dir_all(&dir);
        dir.join("db.sqlite")
    }

    /// 照 Zed 的真表结构建一个库（STRICT 表 + 唯一索引都在）。
    fn zed_fixture(name: &str) -> PathBuf {
        let path = fixture(name);
        let _ = std::fs::remove_file(&path);
        let conn = Connection::open(&path).expect("建测试库");
        conn.execute_batch(
            "CREATE TABLE remote_connections (
                id INTEGER PRIMARY KEY, kind TEXT, host TEXT, port INTEGER,
                user TEXT, distro TEXT, name TEXT, container_id TEXT,
                use_podman INTEGER, remote_env TEXT);
             CREATE TABLE workspaces (
                workspace_id INTEGER PRIMARY KEY, paths TEXT, paths_order TEXT,
                remote_connection_id INTEGER REFERENCES remote_connections(id),
                timestamp TEXT DEFAULT CURRENT_TIMESTAMP NOT NULL,
                session_id TEXT) STRICT;
             CREATE UNIQUE INDEX ix_workspaces_location
                ON workspaces (remote_connection_id, paths);",
        )
        .expect("建表");
        path
    }

    #[test]
    fn epoch_to_stamp_matches_known_moments() {
        assert_eq!(epoch_to_stamp(0), "1970-01-01 00:00:00");
        // `date -u -d "2026-10-08 09:41:31" +%s` = 1791452491（本机 Zed 最新一条）
        assert_eq!(epoch_to_stamp(1_791_452_491), "2026-10-08 09:41:31");
        assert_eq!(epoch_to_stamp(1_780_668_540), "2026-06-05 14:09:00");
        // 闰日（era 边界最容易算错的地方）
        assert_eq!(epoch_to_stamp(1_709_210_096), "2024-02-29 12:34:56");
        // 跨 400 年 era 的世纪边界：1999 最后 1 秒 / 2000 第 1 秒
        assert_eq!(epoch_to_stamp(946_684_799), "1999-12-31 23:59:59");
        assert_eq!(epoch_to_stamp(946_684_800), "2000-01-01 00:00:00");
    }

    #[test]
    fn zed_db_paths_glob_all_channels_with_stable_first() {
        let root = std::env::temp_dir().join(format!("voke-sys-ws-glob-{}", std::process::id()));
        let db = zed_db_dir(&root);
        for channel in ["0-global", "0-stable", "0-preview"] {
            let dir = db.join(channel);
            std::fs::create_dir_all(&dir).expect("建通道目录");
            std::fs::write(dir.join(ZED_DB_FILE), b"").expect("建空库占位");
        }
        // 一个没有 db.sqlite 的目录（不该进列表）
        std::fs::create_dir_all(db.join("0-empty")).expect("建空目录");

        let names: Vec<String> = zed_db_paths(&db)
            .iter()
            .filter_map(|p| p.parent()?.file_name()?.to_str().map(str::to_string))
            .collect();
        assert_eq!(
            names,
            vec!["0-stable", "0-global", "0-preview"],
            "stable 在前，全通道都收"
        );
        let _ = std::fs::remove_dir_all(&root);
    }

    #[test]
    fn zed_db_paths_on_missing_dir_is_empty() {
        assert!(zed_db_paths(Path::new("/nonexistent/voke/zed/db")).is_empty());
    }

    #[test]
    fn first_existing_picks_the_first_real_file_and_skips_the_rest() {
        let root = std::env::temp_dir().join(format!("voke-sys-exe-{}", std::process::id()));
        // 只造第二个候选（Insiders / 根目录主程序），第一个不存在 → 应落到第二个。
        let second = root.join("Programs").join("Zed").join("Zed.exe");
        std::fs::create_dir_all(second.parent().unwrap()).expect("建目录");
        std::fs::write(&second, b"").expect("写占位");
        assert_eq!(first_existing(&root, ZED_EXE_CANDIDATES), Some(second));

        // 造了第一个之后，就该选第一个（bin\Zed.exe = 官方包装脚本跑的那个）。
        let first = root
            .join("Programs")
            .join("Zed")
            .join("bin")
            .join("Zed.exe");
        std::fs::create_dir_all(first.parent().unwrap()).expect("建目录");
        std::fs::write(&first, b"").expect("写占位");
        assert_eq!(first_existing(&root, ZED_EXE_CANDIDATES), Some(first));

        // 一个都没有 → None（provider 据此退化成"资源管理器打开"）。
        assert_eq!(
            first_existing(Path::new("/nonexistent"), ZED_EXE_CANDIDATES),
            None
        );
        let _ = std::fs::remove_dir_all(&root);
    }

    #[test]
    fn vscode_db_paths_are_shared_first() {
        let profile = Path::new("/home/u");
        let appdata = profile.join("AppData/Roaming");
        let paths = vscode_db_paths(profile, &appdata);
        assert_eq!(paths.len(), 2);
        assert!(paths[0].ends_with("state.vscdb"));
        assert!(
            paths[0]
                .to_string_lossy()
                .contains(".vscode-shared/sharedStorage/state.vscdb"),
            "共享库必须在前: {paths:?}"
        );
        assert!(paths[1].starts_with(&appdata));
    }

    #[test]
    fn read_only_uri_escapes_the_characters_that_would_truncate_it() {
        // Windows 路径：盘符 + 反斜杠都要归一；`?`/`#`/`%` 必须转义。
        let uri = read_only_uri(Path::new(r"C:\Users\a b#c\100%?\db.sqlite"));
        assert_eq!(uri, "file:C:/Users/a b%23c/100%25%3f/db.sqlite");
    }

    #[test]
    fn zed_rows_carry_the_joined_remote_connection() {
        let path = zed_fixture("zed-rows");
        let conn = Connection::open(&path).expect("开库");
        conn.execute_batch(
            "INSERT INTO remote_connections (id, kind, host, port, user, distro, container_id)
             VALUES (1, 'wsl', NULL, NULL, NULL, 'openSUSE-Tumbleweed', NULL),
                    (2, 'ssh', 'box', 2222, 'me', NULL, NULL);
             INSERT INTO workspaces (workspace_id, paths, remote_connection_id, timestamp)
             VALUES (1, '/home/testuser/voke', 1, '2026-10-08 09:41:31'),
                    (2, '/home/me/remote', 2, '2026-10-07 00:00:00'),
                    (3, 'C:\\Users\\testuser\\code\\beta', NULL, '2026-10-06 00:00:00'),
                    (4, '', NULL, '2026-10-05 00:00:00');",
        )
        .expect("插数据");
        drop(conn);

        let rows = zed_rows_from(std::slice::from_ref(&path));
        // `paths` 是 NULL 的没插；空串那条会读出来（core 负责过滤）。
        assert_eq!(rows.len(), 4);
        assert_eq!(rows[0].paths, "/home/testuser/voke");
        assert_eq!(rows[0].timestamp, "2026-10-08 09:41:31");
        assert_eq!(
            rows[0]
                .remote
                .as_ref()
                .map(|r| (r.kind.as_str(), r.distro.as_deref())),
            Some(("wsl", Some("openSUSE-Tumbleweed"))),
            "LEFT JOIN 要带上 remote_connections"
        );
        assert_eq!(
            rows[1]
                .remote
                .as_ref()
                .map(|r| (r.kind.as_str(), r.host.as_deref(), r.port)),
            Some(("ssh", Some("box"), Some(2222)))
        );
        assert!(
            rows[2].remote.is_none(),
            "remote_connection_id 为 NULL → 本地"
        );
        assert_eq!(rows[3].paths, "", "空串行照样读出（core 过滤）");
        let _ = std::fs::remove_file(&path);
    }

    /// 缺表 / 空库 / 非 SQLite 文件：三种都不能 panic，都返回空。
    #[test]
    fn zed_reader_never_panics_on_broken_databases() {
        // 空库（有连接、没表）
        let empty = fixture("zed-empty");
        let _ = std::fs::remove_file(&empty);
        Connection::open(&empty).expect("建空库");
        assert!(zed_rows_from(std::slice::from_ref(&empty)).is_empty());
        let _ = std::fs::remove_file(&empty);

        // 不是 SQLite 文件（能打开、读不出表）
        let garbage = fixture("zed-garbage");
        std::fs::write(&garbage, b"this is definitely not a database").expect("写垃圾");
        assert!(zed_rows_from(std::slice::from_ref(&garbage)).is_empty());
        let _ = std::fs::remove_file(&garbage);

        // 不存在的文件
        assert!(zed_rows_from(&[PathBuf::from("/nonexistent/voke/db.sqlite")]).is_empty());
        assert!(zed_rows_from(&[]).is_empty());
    }

    #[test]
    fn vscode_reader_reads_the_recently_opened_payload() {
        let path = fixture("vscode-ok");
        let _ = std::fs::remove_file(&path);
        let conn = Connection::open(&path).expect("建库");
        conn.execute_batch(
            "CREATE TABLE ItemTable (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB);
             INSERT INTO ItemTable (key, value) VALUES
               ('history.recentlyOpenedPathsList',
                '{\"entries\":[{\"folderUri\":\"file:///c%3A/Users/testuser/code/beta\"}]}'),
               ('unrelated.key', 'noise');",
        )
        .expect("插数据");
        drop(conn);

        let payloads = vscode_payloads_from(std::slice::from_ref(&path));
        assert_eq!(payloads.len(), 1);
        assert!(
            payloads[0].json.contains("folderUri"),
            "要取到 entries 原文"
        );
        // mtime 锚点：格式固定 19 位，且不该为空（文件刚写）。
        assert_eq!(
            payloads[0].file_timestamp.len(),
            19,
            "{:?}",
            payloads[0].file_timestamp
        );
        let _ = std::fs::remove_file(&path);
    }

    /// 库里没有那个 key / 没有 ItemTable / 不是 SQLite：都返回空，不 panic。
    #[test]
    fn vscode_reader_never_panics_on_broken_databases() {
        // 有 ItemTable 但没有这个 key（本机 globalStorage 库就是这个形态）
        let no_key = fixture("vscode-nokey");
        let _ = std::fs::remove_file(&no_key);
        let conn = Connection::open(&no_key).expect("建库");
        conn.execute_batch(
            "CREATE TABLE ItemTable (key TEXT, value BLOB);
             INSERT INTO ItemTable (key, value) VALUES ('other', 'x');",
        )
        .expect("建表");
        drop(conn);
        assert!(vscode_payloads_from(std::slice::from_ref(&no_key)).is_empty());
        let _ = std::fs::remove_file(&no_key);

        // 空库 / 垃圾文件 / 不存在
        let empty = fixture("vscode-empty");
        let _ = std::fs::remove_file(&empty);
        Connection::open(&empty).expect("建空库");
        assert!(vscode_payloads_from(std::slice::from_ref(&empty)).is_empty());
        let _ = std::fs::remove_file(&empty);

        let garbage = fixture("vscode-garbage");
        std::fs::write(&garbage, b"not a db at all").expect("写垃圾");
        assert!(vscode_payloads_from(std::slice::from_ref(&garbage)).is_empty());
        let _ = std::fs::remove_file(&garbage);

        assert!(vscode_payloads_from(&[PathBuf::from("/nonexistent/state.vscdb")]).is_empty());
        assert!(vscode_payloads_from(&[]).is_empty());
    }

    /// 读到的行要能直接喂给 core 的纯函数（`workspaces` 端到端跑一遍）。
    #[test]
    fn rows_feed_straight_into_core() {
        let path = zed_fixture("zed-endtoend");
        let conn = Connection::open(&path).expect("开库");
        conn.execute_batch(
            "INSERT INTO remote_connections (id, kind, user, distro)
             VALUES (1, 'wsl', 'testuser', 'openSUSE-Tumbleweed');
             INSERT INTO workspaces (workspace_id, paths, remote_connection_id, timestamp)
             VALUES (1, '/home/testuser/Work/projects/personal/voke', 1, '2026-10-08 09:41:31'),
                    (2, 'C:\\Windows\\Logs\\CBS\\CBS.log', NULL, '2026-10-08 03:49:18');",
        )
        .expect("插数据");
        drop(conn);

        let entries =
            voke_core::workspaces_from_history(&zed_rows_from(std::slice::from_ref(&path)), &[]);
        // CBS.log 是文件（脏数据）→ 只剩 voke 一条。
        assert_eq!(entries.len(), 1);
        assert_eq!(entries[0].name, "voke");
        assert_eq!(
            entries[0].open_args,
            vec![
                "--wsl".to_string(),
                "testuser@openSUSE-Tumbleweed".to_string(),
                "/home/testuser/Work/projects/personal/voke".to_string(),
            ]
        );
        let _ = std::fs::remove_file(&path);
    }
}
