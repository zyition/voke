//! 应用枚举：开始菜单 `.lnk` 解析（`IShellLinkW` COM）+ MSIX/Store 应用（`Shell:AppsFolder`）。
//!
//! 这一层只做"把系统里的应用变成数据"：枚举 → 解析 → 返回 [`AppEntry`]。
//! 不依赖 voke-core，字段名与 `voke_core::AppEntry` 逐字对齐，所以 app 层
//! 可以直接做 `voke_sys::AppEntry -> voke_core::AppEntry` 的结构体字面量转换
//! （两者都是 `pub` 字段 + `String`，转换零成本、无 no-op）。
//!
//! **平台分层**：文件系统枚举（[`enum_shortcuts`]）、目标可执行性判定
//! （[`is_launchable_target`]）与字符串工具是纯 Rust，在任何平台编译并可测
//! （Linux 上 `cargo test -p voke-sys` 能真跑）；COM 解析部分整体 `#[cfg(windows)]`，
//! 因为在别处既编译不了也测不了。
//!
//! ## 设计要点
//!
//! - **COM 只初始化一次**。`CoInitializeEx` 是 **per-thread** 语义，几百个 `.lnk`
//!   每个都 CoInitialize/CoUninitialize 是纯浪费。所以对外主入口是
//!   [`resolve_all`]（批量）与 [`with_com`]（通用 COM 作用域）；单条目解析
//!   [`resolve_lnk`] 是 `unsafe`，由调用方承担"COM 已初始化"的前置条件。
//! - **用 `COINIT_MULTITHREADED`（MTA）**。本模块不跑消息循环，`.lnk` 解析是纯
//!   COM 调用，无窗口/回调需求；MTA 免去 STA 需要的 `GetMessageW` 泵。
//! - **线程模型**：调用线程自己起 COM 作用域，不新建线程；app 层想放后台线程
//!   就把整个 [`resolve_all`] 丢进 `std::thread::spawn`。
//!
//! ## 三类数据来源（都产出同一种 [`AppEntry`]）
//!
//! 1. **开始菜单 `.lnk`**（[`load_start_menu_apps`]）——传统 Win32 应用。
//! 2. **`Shell:AppsFolder`**（[`enum_store_apps`]）——MSIX/Store 应用的**唯一可靠来源**。
//!    实测（Win11 2026-10）：Windows Terminal 在开始菜单里**根本没有 `.lnk`**，
//!    它只出现在 AppsFolder 里（AUMID `Microsoft.WindowsTerminal_8wekyb3d8bbwe!App`）。
//! 3. **特殊外壳链接**——File Explorer / 控制面板 / 运行 这类 `.lnk` 的目标不是文件
//!    路径，`GetPath` 永远返回空（见下），只能靠 `GetIDList` 拿到 PIDL 再转解析名。

use std::collections::HashSet;
use std::path::{Path, PathBuf};

#[cfg(windows)]
use windows::Win32::Foundation::RPC_E_CHANGED_MODE;
#[cfg(windows)]
use windows::Win32::System::Com::{
    CLSCTX_INPROC_SERVER, COINIT_MULTITHREADED, CoCreateInstance, CoInitializeEx, CoTaskMemFree,
    CoUninitialize, IPersistFile, STGM_READ,
};
#[cfg(windows)]
use windows::Win32::UI::Shell::{
    BHID_EnumItems, FOLDERID_AppsFolder, IEnumShellItems, IShellItem, IShellLinkW, KF_FLAG_DEFAULT,
    SHCreateItemFromParsingName, SHCreateItemInKnownFolder, SHGetNameFromIDList,
    SIGDN_DESKTOPABSOLUTEPARSING, SIGDN_NORMALDISPLAY, SLGP_RAWPATH, SLR_NO_UI, ShellLink,
};
#[cfg(windows)]
use windows::core::{HSTRING, Interface};

/// 解析一个快捷方式 / Store 应用得到的应用条目。
///
/// 字段与 `voke_core::AppEntry` 完全一致（名字/类型逐字对齐），app 层直接构造
/// `voke_core::AppEntry { name, path, icon, args, app_id }`，无需转换函数。
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct AppEntry {
    /// 显示名。
    ///
    /// - `.lnk`：优先 `IShellLinkW::GetDescription`，不可信时退化为文件名去 `.lnk`
    ///   （判定见 [`is_plausible_name`]）。开始菜单里的快捷方式通常不写 description，
    ///   所以大多数条目走文件名路径——那正是用户看到的显示名。
    /// - MSIX：`IShellItem::GetDisplayName(SIGDN_NORMALDISPLAY)`，即系统本地化的
    ///   应用名（实测终端 → "终端"、计算器 → "计算器"）。
    pub name: String,
    /// 启动目标，**三种形态**（[`AppEntry::target_kind`] 给出判别，dispatcher 据此分派）：
    ///
    /// - `msix://<AUMID>`：MSIX/Store 应用。例如
    ///   `msix://Microsoft.WindowsTerminal_8wekyb3d8bbwe!App`。
    ///   **为什么带前缀**：AUMID 本身是个裸字符串（`Microsoft.X_8wekyb3d8bbwe!App`），
    ///   与"裸命令"无法区分；加前缀让 dispatcher 能无歧义地识别，且 `msix://`
    ///   不可能与真实 Windows 路径冲突。启动方式见
    ///   [`crate::icon`] 之外的 dispatcher（`explorer.exe shell:AppsFolder\<AUMID>`）。
    /// - `shell:::{GUID}`：特殊外壳项（File Explorer、控制面板、运行）。
    ///   由 `.lnk` 的 PIDL 经 `SIGDN_DESKTOPABSOLUTEPARSING` 得到，例如
    ///   `shell:::{52205FD8-5DFB-447D-801A-D0B52F2E83E1}`（文件资源管理器）。
    ///   启动方式：`explorer.exe shell:::{GUID}`（实测能打开"主文件夹"窗口）。
    /// - 普通文件/目录路径：直接 `Command::new(path)`（目录则交给 explorer.exe）。
    ///
    /// 用 `SLGP_RAWPATH` 而不是 `GetPath` 默认值：RAWPATH 只读文件里存的原始路径。
    /// 注意它**可能带未展开的环境变量**（实测 `%windir%\explorer.exe`），
    /// 展开交给 [`expand_environment`] 在解析期做掉。
    pub path: String,
    /// 图标来源路径，可为空。
    ///
    /// **解析期的语义是"图标来源规格"**，取值优先级：
    /// 1. `.lnk` 自带图标（`GetIconLocation`，如 `%windir%\explorer.exe`）——最准；
    /// 2. 目标文件本身（`C:\...\vlc.exe`）；
    /// 3. 无扩展名的外壳项（`shell:::{GUID}`）——此时为空，由 app 层用
    ///    [`crate::icon::extract_icon_png`] 走 `IShellItemImageFactory` 兜底。
    ///
    /// app 层把它变成真正的图片文件路径后写回此字段（见 `voke-app/src/main.rs`）。
    pub icon: String,
    /// 命令行参数，已按 Windows 规则拆好（`CommandLineToArgvW`）。
    pub args: Vec<String>,
    /// MSIX 应用的 Application User Model ID（AUMID），非 MSIX 时为空。
    ///
    /// 与 `path` 里 `msix://` 前缀后的内容是同一个字符串；单独留一个字段是为了
    /// **可观测性**（lnk_probe 直接打印、日志里能看出这是 Store 应用），
    /// 而不是让调用方去切字符串。
    pub app_id: String,
}

/// 启动目标的形态。dispatcher 按这个分派启动方式。
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum TargetKind {
    /// MSIX/Store 应用（`path` 形如 `msix://<AUMID>`）。
    Msix,
    /// 特殊外壳项（`path` 形如 `shell:::{GUID}`）。
    ShellItem,
    /// 普通文件或目录路径。
    Path,
}

/// `path` 的 `msix://` 前缀（[`AppEntry::path`] 的编码方案，见那里的文档）。
pub const MSIX_PREFIX: &str = "msix://";

/// `path` 的外壳项前缀（`shell:::{GUID}`）。
pub const SHELL_ITEM_PREFIX: &str = "shell:";

impl AppEntry {
    /// 由 `path` 判别启动目标形态。
    pub fn target_kind(&self) -> TargetKind {
        if self.path.starts_with(MSIX_PREFIX) {
            TargetKind::Msix
        } else if self.path.starts_with(SHELL_ITEM_PREFIX) {
            TargetKind::ShellItem
        } else {
            TargetKind::Path
        }
    }

    /// MSIX 应用的 AUMID（非 MSIX 条目返回 `None`）。
    pub fn msix_aumid(&self) -> Option<&str> {
        if self.app_id.is_empty() {
            None
        } else {
            Some(self.app_id.as_str())
        }
    }
}

// ---------------------------------------------------------------------------
// 纯逻辑：目录枚举 + 目标可执行性判定（任意平台可编译、可测）
// ---------------------------------------------------------------------------

/// 开始菜单的两个根目录（全用户 + 当前用户）。
///
/// 路径全部从环境变量取，**不硬编码用户名**：
/// - 全用户：`%ProgramData%\Microsoft\Windows\Start Menu\Programs`
/// - 当前用户：`%APPDATA%\Microsoft\Windows\Start Menu\Programs`
///
/// 环境变量名大小写在 Windows 上不敏感（`ProgramData` / `PROGRAMDATA` 都行）。
pub fn start_menu_dirs() -> Vec<PathBuf> {
    const TAIL: &str = r"Microsoft\Windows\Start Menu\Programs";
    let mut dirs = Vec::new();
    // 顺序有意义：全用户在前，去重时保留全用户那份（路径更稳定、不随用户变化）。
    if let Ok(program_data) = std::env::var("ProgramData") {
        dirs.push(Path::new(&program_data).join(TAIL));
    }
    if let Ok(app_data) = std::env::var("APPDATA") {
        dirs.push(Path::new(&app_data).join(TAIL));
    }
    dirs
}

/// 枚举两个开始菜单目录下所有 `.lnk`（递归），返回去重后的路径。
///
/// 去重原因：安装器经常把同一个快捷方式同时放进全用户和当前用户目录，
/// 两个根下会成对出现**相同的相对路径**（`Vendor\Tool\App.lnk`）。所以去重键
/// 是**相对扫描根的路径**（小写归一），而不是完整路径——用完整路径去不掉重。
/// 全用户目录优先（扫描顺序决定，它的路径更稳定）。
///
/// 不存在的目录直接跳过（`%ProgramData%` 缺失、目录被裁剪都会让 `read_dir` 失败），
/// 不报错——枚举开始菜单是"尽力而为"的操作。
pub fn enum_shortcuts() -> Vec<PathBuf> {
    enum_shortcuts_in(&start_menu_dirs())
}

/// [`enum_shortcuts`] 的可注入目录版本（测试用：Windows/Linux 都能造临时目录树）。
pub fn enum_shortcuts_in(roots: &[PathBuf]) -> Vec<PathBuf> {
    let mut out: Vec<PathBuf> = Vec::new();
    let mut seen: HashSet<String> = HashSet::new();
    for root in roots {
        collect_lnk(root, root, &mut out, &mut seen);
    }
    out
}

/// 递归收集 `.lnk`。普通文件先收、子目录后递（顺序只影响展示顺序，不影响正确性）。
///
/// `root` 是本次扫描的根（用于算去重键），`dir` 是当前目录。
fn collect_lnk(root: &Path, dir: &Path, out: &mut Vec<PathBuf>, seen: &mut HashSet<String>) {
    let Ok(read_dir) = std::fs::read_dir(dir) else {
        // 目录不存在 / 无权限：开始菜单枚举不因此失败。
        log::debug!("跳过无法读取的目录 {}", dir.display());
        return;
    };
    let mut subdirs: Vec<PathBuf> = Vec::new();
    for entry in read_dir.flatten() {
        let path = entry.path();
        match entry.file_type() {
            Ok(ft) if ft.is_dir() => subdirs.push(path),
            Ok(ft) if ft.is_file() => {
                // 扩展名大小写不敏感：`.LNK` 也要认。
                let is_lnk = path
                    .extension()
                    .is_some_and(|ext| ext.eq_ignore_ascii_case("lnk"));
                if is_lnk && seen.insert(dedup_key(root, &path)) {
                    out.push(path);
                }
            }
            // 符号链接等其它类型：不跟随，跳过。
            _ => {}
        }
    }
    for sub in subdirs {
        collect_lnk(root, &sub, out, seen);
    }
}

/// 去重键：相对扫描根的路径（小写）。取不到相对路径时退化为完整路径。
fn dedup_key(root: &Path, path: &Path) -> String {
    let rel = path.strip_prefix(root).unwrap_or(path);
    rel.to_string_lossy().to_lowercase()
}

/// 把路径切成 `(父目录, 文件名)`，供 UI 展示副标题用（sys 层不依赖 core）。
///
/// 只在 Windows 路径上有意义（Windows 的 `Path` 同时认 `\` 与 `/` 两种分隔符）。
pub fn split_dir_and_file(path: &Path) -> (String, String) {
    let file = path
        .file_name()
        .map(|s| s.to_string_lossy().into_owned())
        .unwrap_or_default();
    let dir = path
        .parent()
        .map(|p| p.to_string_lossy().into_owned())
        .unwrap_or_default();
    (dir, file)
}

/// 把 `NUL` 结尾的 UTF-16 缓冲区转成 `String`（取第一个 `NUL` 之前的内容）。
///
/// 任何平台可测：这是 COM getter 返回值的唯一解释入口，边界容易写错。
pub fn utf16_slice_to_string(buf: &[u16]) -> String {
    let len = buf.iter().position(|&c| c == 0).unwrap_or(buf.len());
    String::from_utf16_lossy(&buf[..len])
}

/// 取路径的文件名，**同时认 `\` 与 `/` 两种分隔符**。
///
/// 为什么不用 `std::path::Path::file_name`：在 Linux 上（跑单测时）`\` 不是分隔符，
/// `Path::new(r"C:\a\b.txt").file_name()` 会返回整个字符串，测试与实现语义就会分叉。
/// Windows 目标上两者一致，但判定逻辑必须能在 Linux 上被真测到。
pub fn target_file_name(target: &str) -> &str {
    let cut = target.rfind(['\\', '/']).map(|i| i + 1).unwrap_or(0);
    &target[cut..]
}

/// 取路径的扩展名（小写，不含点）。同样同时认 `\` 与 `/`，理由见 [`target_file_name`]。
///
/// 只把**最后一个分隔符之后**的点当作扩展名分隔符：`C:\a.b\c` 没有扩展名，
/// 而 `C:\a\b.txt` 是 `txt`。
pub fn target_extension(target: &str) -> Option<String> {
    let name = target_file_name(target);
    let dot = name.rfind('.')?;
    // 前导点（`.gitignore`）不算扩展名；点后为空（`trailing.`）也不算。
    if dot == 0 || dot + 1 >= name.len() {
        return None;
    }
    Some(name[dot + 1..].to_lowercase())
}

/// 可执行/可启动的扩展名白名单。
///
/// 对齐 PowerToys Run 的 `Win32Program.ExecutableApplicationExtensions`
/// （已核对其源码，2026-10 实查）：
/// `exe, bat, bin, com, cpl, msc, msi, cmd, ps1, job, msp, mst, sct, ws, wsh, wsf`。
///
/// 额外加了 `scr`/`pif`（同为可执行外壳）与 `appref-ms`（ClickOnce 应用引用，
/// PowerToys 用独立的 `ApplicationReferenceExtension` 处理，这里并进白名单即可）。
///
/// **注意没有 `lnk`**：`.lnk` 是"来源"不是"目标"，一个指向 `.lnk` 的目标是嵌套快捷方式，
/// 应当被剔除（否则会出现不可启动的条目）。
pub const EXECUTABLE_EXTS: &[&str] = &[
    "exe",
    "com",
    "bat",
    "cmd",
    "ps1",
    "msc",
    "cpl",
    "msi",
    "msp",
    "mst",
    "scr",
    "pif",
    "bin",
    "job",
    "sct",
    "ws",
    "wsh",
    "wsf",
    "vb",
    "vbe",
    "vbs",
    "js",
    "jse",
    "appref-ms",
];

/// 明确的"文档类"扩展名——命中即剔除，并给出**专门的日志**便于核对。
///
/// 这不是白名单的补集（补集一律剔除），单列出来只是为了让日志说清"为什么没收录"。
/// 实测混进来的就是这些（Win11 2026-10）：VLC 的 `NEWS.txt`（Release Notes）、
/// 联想的 `Lenovo AI Policy vF_zh_CN.pdf`、7-Zip 的 `7-zip.chm`、
/// VLC 的 `Documentation.url` / `VideoLAN Website.url`。
pub const DOCUMENT_EXTS: &[&str] = &[
    "txt", "pdf", "html", "htm", "url", "chm", "hlp", "md", "ini", "inf", "rtf", "doc", "docx",
    "xls", "xlsx", "ppt", "pptx", "csv", "log", "json", "xml", "yml", "yaml", "toml", "jpg",
    "jpeg", "png", "gif", "bmp", "svg", "ico", "mp3", "mp4", "mkv", "avi", "zip", "7z", "rar",
];

/// 目标是否"看起来可启动"，决定这条 `.lnk` 要不要收录。
///
/// 规则与理由（**判定的输入是"原始目标字符串"，不是已展开的路径**，
/// 这样纯字符串部分能在 Linux 上测；文件系统相关的分支由 `exists_dir` 外部注入）：
///
/// | 情形 | 结论 | 理由 |
/// |---|---|---|
/// | 目标与外壳项都为空 | **剔除** | 死链。空路径进 `Action::Launch` 会启动失败甚至崩，必须在入口拦掉 |
/// | 解析出了外壳项（`shell:::{GUID}`） | 收录 | File Explorer / 控制面板 / 运行——目标不是文件路径，`GetPath` 永远为空，只能靠 PIDL |
/// | 扩展名在白名单 | 收录 | `.exe` / `.msc` / `.bat` … |
/// | 扩展名在文档名单 | 剔除 | `.txt` / `.pdf` / `.url` / `.chm` … |
/// | 目标是个存在的目录 | 收录 | 目录可启动（交给 explorer.exe） |
/// | 目标无扩展名 | 收录 | 裸命令（`ping`）或目录 |
/// | 其它扩展名 | **剔除** | 保守：宁缺毋滥，未知类型多半是数据文件 |
///
/// `shell_target` 是 `.lnk` 的 PIDL 解析出的外壳项——**这是问题 1 的修复点**：
/// `File Explorer.lnk` 的 `GetPath` 在 Resolve 前后**都返回空**，唯一线索就是它。
/// `exists_dir` 表示目标在文件系统上确实是个目录。
pub fn is_launchable_target(target: &str, shell_target: Option<&str>, exists_dir: bool) -> bool {
    if target.trim().is_empty() && shell_target.is_none() {
        // 死链：GetPath 拿不到目标、PIDL 也给不出外壳项 —— 没有任何东西可启动。
        return false;
    }
    if shell_target.is_some() {
        // 外壳项本身就是有效的启动目标（控制面板 / 运行 / 文件资源管理器）。
        return true;
    }
    match target_extension(target) {
        Some(ext) => {
            if EXECUTABLE_EXTS.contains(&ext.as_str()) {
                return true;
            }
            if DOCUMENT_EXTS.contains(&ext.as_str()) {
                return false;
            }
            // 既不在白名单也不在文档名单：目录（罕见，目录一般没扩展名）或未知类型。
            // 目录优先放行——它是明确可启动的。
            exists_dir
        }
        // 无扩展名：外壳项（PIDL）或裸命令/目录，都可启动。
        None => true,
    }
}

// ---------------------------------------------------------------------------
// 图标来源规格（纯逻辑，任意平台可测）
// ---------------------------------------------------------------------------

/// 从条目里挑出"最可能给出正确图标"的来源规格，喂给
/// [`crate::icon::extract_icon_png`]（或等价 API）。
///
/// 返回值是 `IShellItemImageFactory` 能解析的字符串；`None` 表示没有任何可用来源
/// （调用方应把 `icon` 清空，让 UI 回退占位图标）。
///
/// 优先级（每条都有实机实测依据，见 docs/HISTORY.md）：
///
/// 1. **`.lnk` 自带的图标且指向 `.exe`/`.ico`** —— 最准。实测
///    `File Explorer.lnk` 虽然 `GetPath` 永远为空，但 `GetIconLocation` 写着
///    `%windir%\explorer.exe`，靠它才能画出资源管理器图标。
/// 2. **启动目标**。MSIX 必须转成 `shell:AppsFolder\<AUMID>` 形态：
///    实测**裸 AUMID 会被拒绝**（`SHCreateItemFromParsingName` 报
///    `0x80070002` ERROR_FILE_NOT_FOUND），加 `shell:AppsFolder\` 前缀就能拿到图标。
///    巧合但省事：这与 dispatcher 的启动命令是同一个字符串。
///    `shell:::{GUID}` 则原样可用。
/// 3. **兜底：图标规格原样**。有些 `.lnk` 把图标写成 `shell32.dll` 这类 DLL
///    （实测 "Windows 工具" 是 `shell32.dll,-22579`），直接喂进去只能拿到
///    **该 DLL 的通用图标**，所以它排在第 2 条之后；但比什么都没有强。
pub fn pick_icon_spec(entry: &AppEntry) -> Option<String> {
    let icon = entry.icon.trim();
    // 1. `.lnk` 自带且指向具体可执行文件/图标文件。
    let lower = icon.to_lowercase();
    if !icon.is_empty() && (lower.ends_with(".exe") || lower.ends_with(".ico")) {
        return Some(icon.to_string());
    }
    // 2. 启动目标。
    if !entry.path.is_empty() {
        if let Some(aumid) = entry.path.strip_prefix(MSIX_PREFIX) {
            if !aumid.is_empty() {
                return Some(format!("shell:AppsFolder\\{aumid}"));
            }
        } else {
            return Some(entry.path.clone());
        }
    }
    // 3. 兜底。
    if !icon.is_empty() {
        return Some(icon.to_string());
    }
    None
}

/// 图标缓存文件名里应用名部分的最大长度（避免超出 Windows MAX_PATH）。
const MAX_ICON_NAME_CHARS: usize = 40;

/// 生成图标缓存文件名（不含扩展名）：`<安全名字>-<路径哈希>`。
///
/// - 名字部分让人一眼能看出这是哪个应用（排查时直接看 `icons/` 目录）；
/// - 路径哈希保证不同条目不会撞名（同名不同路径的应用确实存在，
///   例如两个版本的 "Visual Studio Code"）。
pub fn icon_cache_key(name: &str, path: &str) -> String {
    let mut safe = String::with_capacity(MAX_ICON_NAME_CHARS);
    for c in name.chars() {
        if safe.chars().count() >= MAX_ICON_NAME_CHARS {
            break;
        }
        if c.is_alphanumeric() {
            safe.push(c);
        } else if !safe.ends_with('_') {
            safe.push('_');
        }
    }
    let safe = safe.trim_matches('_');
    let safe = if safe.is_empty() { "app" } else { safe };
    format!("{safe}-{:08x}", fnv1a(path))
}

/// FNV-1a 32 位哈希（不引入依赖，足够做文件名去重）。
fn fnv1a(s: &str) -> u32 {
    let mut hash: u32 = 0x811c_9dc5;
    for b in s.as_bytes() {
        hash ^= *b as u32;
        hash = hash.wrapping_mul(0x0100_0193);
    }
    hash
}

// ---------------------------------------------------------------------------
// COM 解析（仅 Windows）
// ---------------------------------------------------------------------------

/// GetPath/GetArguments/GetDescription/GetIconLocation 的缓冲区长度。
///
/// 官方文档对 GetPath 写"最大 MAX_PATH"，但长路径策略下实际可超；
/// 4096 足够且每个 `.lnk` 只分配一次栈缓冲（4 次调用共 32KB 栈，安全）。
#[cfg(windows)]
const PATH_BUFFER_LEN: usize = 4096;

/// 枚举 + 解析的一步到位入口：开始菜单 `.lnk` + `Shell:AppsFolder`（MSIX）。
///
/// app 层最常用。放到后台线程调用：几百个 `.lnk` 的 COM 解析通常在几十毫秒量级，
/// 但开始菜单目录可能在慢盘上，别放在 UI 线程。
///
/// ## 跨来源去重（两条规则，都有实测依据）
///
/// 1. **(名字, 启动目标) 完全相同** 的去掉（AppsFolder 内部可能有重复项）。
/// 2. **AUMID 注册的 Win32 应用**（`app_id` 不含 `!`）若与某个 `.lnk` 条目**同名**，
///    跳过——因为 `.lnk` 那条指向真正的 exe、启动更直接。
///    实测这类重复很多：`Microsoft.VisualStudioCode` ↔ `Visual Studio Code.lnk`、
///    `Microsoft.PowerToysWin32` ↔ `PowerToys (Preview).lnk`、
///    `Microsoft.SkyDrive.Desktop` ↔ `OneDrive.lnk`、`party.mihomo.app` ↔ `Clash Party.lnk`。
///    不去重的话列表里每个应用都会出现两遍。
///
/// 真 MSIX 应用（`app_id` 形如 `<PackageFamilyName>!<AppId>`，**含 `!`**）
/// 一律保留：它们**没有** `.lnk` 可取代（Windows Terminal 就是典型），
/// 即便偶尔同名也宁可留两条——AUMID 那条才是能用的。
#[cfg(windows)]
pub fn load_all_apps() -> Vec<AppEntry> {
    let shortcuts = enum_shortcuts();
    log::info!("开始菜单发现 {} 个快捷方式", shortcuts.len());
    let mut apps = with_com(|| resolve_all_inner(&shortcuts));
    log::info!("开始菜单解析出 {} 个可启动条目", apps.len());

    // `.lnk` 侧已有的名字（小写），用于规则 2。
    let lnk_names: HashSet<String> = apps.iter().map(|e| e.name.to_lowercase()).collect();

    let store = with_com(enum_store_apps);
    log::info!("Store/AppsFolder 枚举出 {} 个条目", store.len());
    let mut skipped = 0usize;
    for entry in store {
        let is_msix_package = entry.app_id.contains('!');
        if !is_msix_package && lnk_names.contains(&entry.name.to_lowercase()) {
            log::debug!(
                "跳过与 .lnk 同名的非 MSIX 条目: {} ({})",
                entry.name,
                entry.app_id
            );
            skipped += 1;
            continue;
        }
        apps.push(entry);
    }
    if skipped > 0 {
        log::info!("按同名规则跳过 {skipped} 条 AUMID 注册的 Win32 应用");
    }

    // 规则 1：完全重复项。
    let mut seen: HashSet<(String, String)> = HashSet::new();
    apps.retain(|e| seen.insert((e.name.to_lowercase(), e.path.to_lowercase())));
    log::info!("合并去重后共 {} 个应用条目", apps.len());
    apps
}

/// 只加载开始菜单 `.lnk`（保留给 lnk_probe 与"只要传统应用"的场景）。
#[cfg(windows)]
pub fn load_start_menu_apps() -> Vec<AppEntry> {
    let shortcuts = enum_shortcuts();
    log::info!("开始菜单发现 {} 个快捷方式", shortcuts.len());
    let apps = resolve_all(&shortcuts);
    log::info!("成功解析 {} 个应用条目", apps.len());
    apps
}

/// 批量解析：一个 COM 作用域内解析全部 `.lnk`。
///
/// 这是推荐入口。单个文件解析失败只记日志并跳过（开始菜单里存在指向已卸载
/// 程序的死链是常态），不影响其余条目。
#[cfg(windows)]
pub fn resolve_all(paths: &[PathBuf]) -> Vec<AppEntry> {
    with_com(|| resolve_all_inner(paths))
}

/// [`resolve_all`] 的裸实现：**假定 COM 已初始化**（由调用方包 `with_com`）。
#[cfg(windows)]
fn resolve_all_inner(paths: &[PathBuf]) -> Vec<AppEntry> {
    paths
        .iter()
        .filter_map(|path| {
            // SAFETY: 调用方已建立 COM 作用域（resolve_all / load_all_apps）。
            let entry = match unsafe { resolve_lnk(path) } {
                Ok(entry) => entry,
                Err(err) => {
                    log::warn!("解析 {} 失败: {err}", path.display());
                    return None;
                }
            };
            if entry.path.is_empty() {
                // 理论上 resolve_lnk 已过滤过；这里再兜一层，保证"空 path 不进列表"
                // 这个不变量在函数边界上成立。
                log::debug!("丢弃无目标的快捷方式 {}", path.display());
                return None;
            }
            Some(entry)
        })
        .collect()
}

/// 在 COM 作用域里执行 `f`：进入时 `CoInitializeEx(MTA)`，退出时 `CoUninitialize`。
///
/// `RPC_E_CHANGED_MODE`（线程已用另一种 apartment 初始化过）视为可用——COM 已经
/// 就绪，只是模式不是我们要的；此时**不能**配平 `CoUninitialize`
/// （计数不是我们加的，多减一次会撤掉别人的初始化）。
#[cfg(windows)]
pub fn with_com<T>(f: impl FnOnce() -> T) -> T {
    // SAFETY: pvReserved 必须为 None（保留参数）+ 合法的 COINIT 常量。
    let hr = unsafe { CoInitializeEx(None, COINIT_MULTITHREADED) };
    let owns_init = if hr.is_ok() {
        true
    } else if hr == RPC_E_CHANGED_MODE {
        log::debug!("COM 已被本线程以其它 apartment 模式初始化，复用现有 COM");
        false
    } else {
        // 初始化失败：仍执行 f（里面的 COM 调用会各自报错），但不配对 CoUninitialize。
        log::warn!("CoInitializeEx 失败: {hr:?}");
        false
    };
    let result = f();
    if owns_init {
        // SAFETY: 与上面的 CoInitializeEx 严格配对。
        unsafe { CoUninitialize() };
    }
    result
}

/// 解析单个 `.lnk`。
///
/// 流程（每一步都有实测依据，见模块文档与 docs/HISTORY.md）：
///
/// 1. `IPersistFile::Load` 读入快捷方式。
/// 2. **`Resolve(hwnd=None, SLR_NO_UI)`** 先解析一次。`SLR_NO_UI` 保证不弹
///    "找不到目标"对话框、不因网络/光驱盘阻塞批量枚举（这是原实现不敢调
///    `Resolve` 的原因）。解析失败只是 `Result::Err`，忽略即可，后面照样能拿到
///    文件里记录的原始路径。
/// 3. `GetPath(SLGP_RAWPATH)` 取目标 → `ExpandEnvironmentStringsW` 展开环境变量
///    （实测 `Administrative Tools.lnk` 存的是 `%windir%\system32\control.exe`）。
/// 4. `GetIDList` 拿 PIDL。**这是特殊外壳链接的唯一出路**：实测
///    `File Explorer.lnk` 的 `GetPath` 在 Resolve 前后**都返回空**，
///    Resolve 也救不回来（所有 SLR_* 组合都试过，见 docs/HISTORY.md）。
///    此时用 `SHGetNameFromIDList(SIGDN_DESKTOPABSOLUTEPARSING)` 把 PIDL 转成
///    解析名——对文件资源管理器得到 `shell:::{52205FD8-...}`
///    （`Open()` 能直接打开"主文件夹"窗口）。
/// 5. 目标可启动性判定（[`is_launchable_target`]），不可启动的整条剔除。
///
/// # Safety
///
/// 调用线程必须已经初始化 COM（用 [`with_com`]，或自己配对调用
/// `CoInitializeEx`/`CoUninitialize`）。本函数不做初始化也不做 `CoUninitialize`
/// ——这正是"批量解析只初始化一次"的代价。
#[cfg(windows)]
pub unsafe fn resolve_lnk(path: &Path) -> windows::core::Result<AppEntry> {
    // SAFETY: 调用方保证 COM 已初始化（见函数文档）；以下所有 COM 调用都在此前提下。
    unsafe {
        let link: IShellLinkW = CoCreateInstance(&ShellLink, None, CLSCTX_INPROC_SERVER)?;
        let persist: IPersistFile = link.cast()?;
        persist.Load(&HSTRING::from(path), STGM_READ)?;

        // ② Resolve：hwnd 传 None（无父窗口），SLR_NO_UI 不弹框、不阻塞。
        // 失败无所谓（死链、特殊外壳项都会 Err），后续 GetPath/GetIDList 仍然可用。
        if let Err(err) = link.Resolve(
            windows::Win32::Foundation::HWND::default(),
            SLR_NO_UI.0 as u32,
        ) {
            log::debug!("Resolve 失败（不影响后续取值）{}: {err}", path.display());
        }

        // ③ 原始目标路径 + 环境变量展开。
        let raw_target = get_path(&link).unwrap_or_default();
        let target = expand_environment(&raw_target);

        // ④ PIDL：外壳项的判据与唯一目标来源。
        let shell_target = shell_item_from_idlist(&link);

        // 特殊外壳项（GetPath 拿不到）：用 PIDL 的解析名当启动目标。
        let launch_target = match (&shell_target, target.as_str()) {
            (Some(shell), _) if target.is_empty() => shell.clone(),
            _ => target.clone(),
        };

        // ⑤ 可启动性判定。目录判定只在真有文件系统目标时做（`shell:::{GUID}` 不是文件）。
        let exists_dir = !launch_target.is_empty()
            && !launch_target.starts_with("shell:")
            && Path::new(&launch_target).is_dir();
        if !is_launchable_target(&launch_target, shell_target.as_deref(), exists_dir) {
            log::debug!(
                "剔除不可启动的快捷方式 {}: target={launch_target:?} (raw={raw_target:?}, shell={shell_target:?})",
                path.display()
            );
            return Ok(AppEntry::default()); // 空 path => 上层丢弃
        }

        // 显示名：Description 非营销文案且非空时优先；否则用文件名去 `.lnk` 后缀。
        // 实测（Win11 2026-10）：Office 套件的 description 是营销文案
        // （“轻松发现、直观显示和共享数据中的见解。”），直接当 name 会让搜索
        // “Excel” 匹配不到。判定标准见 is_plausible_name。
        let description = read_into_buffer(|buf| link.GetDescription(buf)).unwrap_or_default();
        let file_stem = path
            .file_stem()
            .map(|s| s.to_string_lossy().into_owned())
            .unwrap_or_default();
        let name = if !description.trim().is_empty() && is_plausible_name(&description, &file_stem)
        {
            description
        } else {
            file_stem
        };

        // 图标来源：`.lnk` 自带图标最准（`File Explorer.lnk` 虽无目标，但图标写着
        // `%windir%\explorer.exe`，足以画出资源管理器图标）；没有则回退到目标。
        let mut icon_index = 0i32;
        let icon_spec =
            read_into_buffer(|buf| link.GetIconLocation(buf, &mut icon_index)).unwrap_or_default();
        let icon = if icon_spec.trim().is_empty() {
            // 目标本身（`C:\...\vlc.exe`）。`shell:::{GUID}` 不是图片，留空交给 app 层。
            if launch_target.starts_with("shell:") {
                String::new()
            } else {
                launch_target.clone()
            }
        } else if icon_spec.contains(',') {
            // `GetIconLocation` 返回 `路径,索引` 的合并形态（实测 `io.github...` 没有，
            // 但 shell32.dll,-22579 这类是 `dll,负数资源号`）。取逗号前的路径。
            icon_spec.split(',').next().unwrap_or("").trim().to_string()
        } else {
            icon_spec
        };
        let icon = expand_environment(&icon);

        let raw_args = read_into_buffer(|buf| link.GetArguments(buf)).unwrap_or_default();

        Ok(AppEntry {
            name,
            path: launch_target,
            icon,
            args: split_arguments(&raw_args),
            // 开始菜单快捷方式不是 MSIX：AUMID 由 AppsFolder 那条路径负责。
            app_id: String::new(),
        })
    }
}

/// `GetPath(SLGP_RAWPATH)`：读原始目标路径，不做 `Resolve()` 之外的解析。
#[cfg(windows)]
unsafe fn get_path(link: &IShellLinkW) -> windows::core::Result<String> {
    let mut buf = [0u16; PATH_BUFFER_LEN];
    // SAFETY: buf 是有效可写切片；pfd 传 null（不需要 WIN32_FIND_DATA 输出）。
    unsafe {
        link.GetPath(
            &mut buf,
            std::ptr::null_mut(),
            // SLGP_RAWPATH = 4。
            SLGP_RAWPATH.0 as u32,
        )?;
    }
    Ok(utf16_slice_to_string(&buf))
}

/// 从 `.lnk` 的 PIDL 取外壳项解析名（`::{GUID}`），失败或没有 PIDL 时返回空串。
///
/// 用 `SIGDN_DESKTOPABSOLUTEPARSING`：实测这是唯一能同时覆盖
/// "文件系统目标"（`C:\Program Files\VideoLAN\VLC\vlc.exe`）与
/// "纯外壳项"（`::{52205FD8-5DFB-447D-801A-D0B52F2E83E1}`）的格式；
/// `SIGDN_FILESYSPATH` 对外壳项返回 `0x80070057`（E_INVALIDARG）。
#[cfg(windows)]
unsafe fn shell_item_from_idlist(link: &IShellLinkW) -> Option<String> {
    // SAFETY: 调用方保证 COM 已初始化。
    unsafe {
        let Ok(pidl) = link.GetIDList() else {
            return None;
        };
        if pidl.is_null() {
            return None;
        }
        // 实测：纯外壳项的 PIDL 其 "size" 字段是 0x8000|id（如 0x8000 对应
        // 表示"整个命名空间下的项"），名称要靠 SHGetNameFromIDList 才拿得到。
        let name = match SHGetNameFromIDList(pidl, SIGDN_DESKTOPABSOLUTEPARSING) {
            Ok(pwstr) if !pwstr.is_null() => {
                // SAFETY: SHGetNameFromIDList 契约：成功时返回以 NUL 结尾的
                // CoTaskMemAlloc 串，必须用 CoTaskMemFree 释放。
                let s = pwstr.to_string().unwrap_or_default();
                CoTaskMemFree(Some(pwstr.0 as *const core::ffi::c_void));
                s
            }
            _ => String::new(),
        };
        CoTaskMemFree(Some(pidl as *const core::ffi::c_void));
        // 只把"外壳项"（`::{...}`）当作 PIDL 目标；文件路径由 GetPath 负责，
        // 避免两条来源互相覆盖。
        if name.starts_with("::{") {
            // 转成 shell 可打开的 `shell:::{GUID}`。
            Some(format!("shell:{name}"))
        } else {
            None
        }
    }
}

/// `expand_environment`：把 `%VAR%` 展开成实际值。
///
/// 实测必要性：`Administrative Tools.lnk` 的目标存的是
/// `%windir%\system32\control.exe`，不展开的话 `Command::new` 找不到文件。
/// 展开失败（缓冲不足等）时原样返回，不抛错。
pub fn expand_environment(raw: &str) -> String {
    expand_environment_impl(raw)
}

/// 非 Windows：环境变量展开用最朴素的 `%VAR%` 替换（仅供 Linux 单测跑逻辑，
/// 真实调用只在 Windows 上）。
#[cfg(not(windows))]
fn expand_environment_impl(raw: &str) -> String {
    let mut out = String::with_capacity(raw.len());
    let mut rest = raw;
    while let Some(start) = rest.find('%') {
        out.push_str(&rest[..start]);
        let after = &rest[start + 1..];
        match after.find('%') {
            Some(end) => {
                let key = &after[..end];
                match std::env::var(key) {
                    Ok(val) => out.push_str(&val),
                    // 未知变量：原样保留，便于排查。
                    Err(_) => {
                        out.push('%');
                        out.push_str(key);
                        out.push('%');
                    }
                }
                rest = &after[end + 1..];
            }
            None => {
                out.push_str(&rest[start..]);
                return out;
            }
        }
    }
    out.push_str(rest);
    out
}

#[cfg(windows)]
fn expand_environment_impl(raw: &str) -> String {
    use windows::Win32::System::Environment::ExpandEnvironmentStringsW;
    if raw.is_empty() || !raw.contains('%') {
        return raw.to_string();
    }
    let src = HSTRING::from(raw);
    let mut buf = [0u16; PATH_BUFFER_LEN];
    // SAFETY: src 在调用期间存活；buf 是有效可写切片。
    let len = unsafe { ExpandEnvironmentStringsW(&src, Some(&mut buf)) };
    if len == 0 || len as usize > buf.len() {
        // 展开失败或缓冲不够：原样返回（后续可启动性判定会把它当无扩展名处理）。
        return raw.to_string();
    }
    utf16_slice_to_string(&buf)
}

/// 统一"传缓冲区 → 拿 `NUL` 结尾字符串"的样板代码。
///
/// 四个 getter 的签名都是 `(&mut [u16], ...) -> Result<()>`，用闭包收掉重复。
/// 闭包体里各自带 `unsafe`（edition 2024 下 `unsafe fn` 的函数体不隐式 unsafe，
/// 闭包体更不继承外层 unsafe 上下文），所以这里调用 `f` 本身不需要 unsafe。
#[cfg(windows)]
fn read_into_buffer(f: impl FnOnce(&mut [u16]) -> windows::core::Result<()>) -> Option<String> {
    let mut buf = [0u16; PATH_BUFFER_LEN];
    match f(&mut buf) {
        Ok(()) => Some(utf16_slice_to_string(&buf)),
        Err(_) => None,
    }
}

/// 判断 `.lnk` 的 description 是否适合做显示名。
///
/// 实测（Win11）description 有两类不可信来源：
/// - 营销文案（“轻松发现、直观显示和共享数据中的见解。”）
/// - 本地化描述短语（Chrome 的 “访问互联网”、Edge 的 “浏览 Web”）
/// 它们的共同特征：**和文件名/目标路径毫无字符重叠**。正常写 description 的
/// 快捷方式（“Windows Terminal” ↔ WindowsTerminal.lnk、“Clash Party”），
/// description 必然与文件名有词重叠。
///
/// 因此规则只有一条：description 与文件名的词（字母数字段，小写）有交集才可信，
/// 否则一律用文件名。不设“短语可信”特例——中文无空格短语（“访问互联网”）
/// 会被该特例误判，且显示名失真比搜索键缺失更伤。
///
/// **补充规则（词重叠不够用）**：有些 description 是**整句话**，
/// 却恰好含一个与文件名重叠的词，于是被上面那条规则误放行。实测两例：
///
/// - `Windows PowerShell ISE (x86).lnk` 的 description 是
///   “Windows PowerShell 集成脚本环境。执行基于对象的(命令行)函数”
///   ——含 “PowerShell” 与文件名重叠，但这是描述句不是名字；
/// - `ODBC Data Sources (32-bit).lnk` 是“维护 odbc 数据源和驱动程序。”
///   ——含 “odbc”。
///
/// 判据（两条，都只针对"句末标点"，不做长度启发式——长度阈值无法区分
/// “长名字”与“短句子”）：
/// 1. 含 CJK 句末标点 `。；！？` **在任何位置** —— 中文正常名字里绝不会出现，
///    而上面第一例的 `。` 在句中（“...集成脚本环境。执行...”），只判结尾会漏；
/// 2. 以 ASCII 句末标点 `. ! ? ;` 结尾 —— 第二例“...驱动程序。”是中文句号，
///    但英文描述句会以 `.` 结尾。
///
/// 用 `Node.js` 这类名字验证过第 2 条不会误伤：点在**中间**不算。
///
/// 另注：description 里可能混入**字面量反斜杠转义**（实测 `ODBC` 那条会变成
/// `维护 odbc 数据源和驱动程序。` 之外的形态也可能出现 `\\`），
/// 所以判定前先归一化连续空白。
#[cfg(windows)]
fn is_plausible_name(description: &str, file_stem: &str) -> bool {
    let description = description.trim();
    if description.is_empty() || looks_like_sentence(description) {
        return false;
    }
    let words = |s: &str| -> Vec<String> {
        s.split(|c: char| !c.is_alphanumeric())
            .filter(|w| !w.is_empty())
            .map(|w| w.to_lowercase())
            .collect()
    };
    let desc_words = words(description);
    let stem_words = words(file_stem);
    if stem_words.is_empty() {
        return false;
    }
    desc_words
        .iter()
        .any(|dw| stem_words.iter().any(|sw| sw == dw))
}

/// 这个字符串看起来是不是"一整句话"（而不是一个名字）。
///
/// 判据见 [`is_plausible_name`] 的文档。纯逻辑，Linux 上可测。
pub fn looks_like_sentence(s: &str) -> bool {
    let t = s.trim();
    if t.is_empty() {
        return false;
    }
    // 1. CJK 句末标点在任何位置都说明这是句子
    //    （实测 “...集成脚本环境。执行基于对象的(命令行)函数” 的点在句中）。
    if t.contains(['。', '；', '！', '？']) {
        return true;
    }
    // 2. ASCII 句末标点只在结尾才算（`Node.js` 的点在中间，不算）。
    matches!(t.chars().last(), Some('.' | '!' | '?' | ';'))
}

/// 用 `CommandLineToArgvW` 按 Windows 规则拆参数。
///
/// 失败时返回空列表而不是"整串当一个参数"：参数丢失比参数拼错更安全
/// （拼错会以意外参数启动程序）。
#[cfg(windows)]
fn split_arguments(raw: &str) -> Vec<String> {
    let trimmed = raw.trim();
    if trimmed.is_empty() {
        return Vec::new();
    }
    use windows::Win32::UI::Shell::CommandLineToArgvW;
    let wide = HSTRING::from(trimmed);
    let mut argc = 0i32;
    // SAFETY: wide 在本函数内存活；argc 是有效可写指针。
    let argv = unsafe { CommandLineToArgvW(&wide, &mut argc) };
    if argv.is_null() {
        log::warn!("CommandLineToArgvW 失败，丢弃参数: {trimmed:?}");
        return Vec::new();
    }
    let count = argc.max(0) as usize;
    let mut out = Vec::with_capacity(count);
    for i in 0..count {
        // SAFETY: argv 是 count 个有效 PWSTR 的数组（CommandLineToArgvW 契约）。
        let p = unsafe { *argv.add(i) };
        if p.is_null() {
            continue;
        }
        // SAFETY: p 非空且以 NUL 结尾。
        if let Ok(s) = unsafe { p.to_string() } {
            out.push(s);
        }
    }
    // SAFETY: argv 由 CommandLineToArgvW 用 LocalAlloc 分配，必须用 LocalFree 释放。
    unsafe {
        let _ = windows::Win32::Foundation::LocalFree(Some(windows::Win32::Foundation::HLOCAL(
            argv as *mut core::ffi::c_void,
        )));
    }
    out
}

// ---------------------------------------------------------------------------
// MSIX / Store 应用枚举（Shell:AppsFolder）
// ---------------------------------------------------------------------------

/// `Shell:AppsFolder` 的已知文件夹枚举（MSIX/UWP + 一堆 Win32 注册项）。
///
/// ## 为什么必须单独枚举
///
/// Windows Terminal 这类 **MSIX（Store）应用在开始菜单里根本没有 `.lnk`**：
/// 实测 `C:\Users\testuser\AppData\Roaming\...\Start Menu\Programs\` 下找不到任何
/// 与 Terminal 相关的文件；它只存在于 shell 命名空间 `AppsFolder` 里，
/// AUMID 为 `Microsoft.WindowsTerminal_8wekyb3d8bbwe!App`。
///
/// ## API 选型（已查 windows 0.62.2 真实签名 + 实机验证，非臆造）
///
/// 调用序列（**踩过的坑写在下面**）：
///
/// 1. `SHCreateItemInKnownFolder::<IShellItem>(&FOLDERID_AppsFolder, KF_FLAG_DEFAULT, None)`
///    拿到 AppsFolder **本身**。
/// 2. `item.BindToHandler::<IEnumShellItems>(None, &BHID_EnumItems)` 得到枚举器
///    （shell 的标准枚举入口）。
/// 3. `IEnumShellItems::Next(&mut [Option<IShellItem>], None)` 批量取。
///
/// **坑 1（实测 0x80004002 E_NOINTERFACE）**：第 1 步**不能**请求
/// `IID_IShellItemArray`——`SHCreateItemInKnownFolder` 无论请求什么 IID 都只创建
/// `IShellItem`（AppsFolder 的 `IShellItemArray` 形态要靠
/// `SHCreateItemFromIDList` 走另一条路）。实测请求 array IID 直接
/// `E_NOINTERFACE`，整个 Store 段返回 0 条（Windows Terminal 因此缺席）。
///
/// **坑 2**：`Next` 的 HRESULT 在枚举结束时是 `S_FALSE`，windows crate 把 S_FALSE
/// 映射成 `Err`，所以**不能**「只要 Err 就 break」之前的条目会丢——
/// 判据必须是「本批拿到的条目数为 0」。
///
/// **坑 3（实测 0x80070057 E_INVALIDARG）**：`Next` 的 `pceltfetched` **不能传 NULL**
/// 除非 `celt == 1`（COM 的 `IEnumXxx::Next` 契约）。批量取却传 NULL 会直接
/// `E_INVALIDARG` 且一条都拿不到。必须传一个真实的 `u32` 指针。
///
/// 每项：`GetDisplayName(SIGDN_NORMALDISPLAY)` 取本地化显示名；
/// `GetDisplayName(SIGDN_DESKTOPABSOLUTEPARSING)` 取启动标识
/// （实测：AppUserModelID 条目返回的就是 AUMID 本身，`{GUID}\cmd.exe` 形态的
/// 返回 `{GUID}\cmd.exe`）。
///
/// ## 过滤
///
/// AppsFolder 混着三类东西，只保留第一类：
/// 1. **真应用**：AUMID 形态（`Microsoft.X_8wekyb3d8bbwe!App`）、裸 AUMID
///    （`Microsoft.VisualStudioCode`）—— 收录。
/// 2. **文件系统路径**（`C:\ProgramData\...\YunShuLauncher.exe`）—— 这些已在
///    开始菜单 `.lnk` 那一路覆盖，重复收录只会产生同名两条，**跳过**。
/// 3. **非应用**：`http://` / `https://` / `steam://` 之类的 URL 与
///    `Microsoft.AutoGenerated.{...}`（系统自动生成的"打开方式"占位项）—— 跳过。
#[cfg(windows)]
pub fn enum_store_apps() -> Vec<AppEntry> {
    let mut out: Vec<AppEntry> = Vec::new();
    // SAFETY: 调用方（load_all_apps）已建立 COM 作用域。
    unsafe {
        // 注意：这里**必须**要 `IShellItem`，不能要 `IShellItemArray`
        // （实测请求 array IID 会 E_NOINTERFACE，见函数文档的"坑 1"）。
        let folder: IShellItem =
            match SHCreateItemInKnownFolder(&FOLDERID_AppsFolder, KF_FLAG_DEFAULT, None) {
                Ok(a) => a,
                Err(err) => {
                    log::warn!("打开 Shell:AppsFolder 失败: {err}");
                    return out;
                }
            };
        let enumerator: IEnumShellItems = match folder.BindToHandler(None, &BHID_EnumItems) {
            Ok(e) => e,
            Err(err) => {
                log::warn!("AppsFolder 枚举器创建失败: {err}");
                return out;
            }
        };

        // 一次取一批。两个判据都得用，理由见函数文档的"坑 2/坑 3"：
        // - `pceltfetched` 必须传真实指针（传 NULL + celt>1 => E_INVALIDARG）；
        // - 终止判据是"本批拿到 0 条"，而不是"Next 返回 Err"（S_FALSE 也是 Err）。
        loop {
            let mut batch: [Option<IShellItem>; 16] = Default::default();
            let mut fetched = 0u32;
            let hr = enumerator.Next(&mut batch, Some(&mut fetched));
            let got = fetched as usize;
            // 只遍历**实际取到**的那些槽位（未取到的槽位是 None，但用 fetched 更严谨）。
            for item in batch.iter().take(got).flatten() {
                if let Some(entry) = store_entry_from_item(item) {
                    out.push(entry);
                }
            }
            if got == 0 {
                // 本批一个都没拿到：S_FALSE 结束，或真错误。两者都该停。
                if let Err(err) = hr {
                    log::debug!("AppsFolder 枚举结束/中断: {err}");
                }
                break;
            }
        }
    }
    out
}

/// 把一个 AppsFolder 项转成 [`AppEntry`]；非应用项返回 `None`。
#[cfg(windows)]
unsafe fn store_entry_from_item(item: &IShellItem) -> Option<AppEntry> {
    // SAFETY: 调用方保证 COM 已初始化。
    unsafe {
        let display = shell_item_display_name(item, SIGDN_NORMALDISPLAY).unwrap_or_default();
        let parsing =
            shell_item_display_name(item, SIGDN_DESKTOPABSOLUTEPARSING).unwrap_or_default();
        if display.trim().is_empty() || parsing.trim().is_empty() {
            log::debug!("AppsFolder 项跳过：显示名/解析名缺失");
            return None;
        }

        // 跳过已在 `.lnk` 那一路覆盖的文件系统目标。
        if looks_like_filesystem_path(&parsing) {
            log::debug!("AppsFolder 跳过文件系统目标（.lnk 已覆盖）: {display} -> {parsing}");
            return None;
        }
        // 跳过 URL 与系统自动生成的占位项。
        if parsing.contains("://") {
            log::debug!("AppsFolder 跳过 URL 项: {display} -> {parsing}");
            return None;
        }
        if parsing.starts_with("Microsoft.AutoGenerated.") {
            log::debug!("AppsFolder 跳过自动生成占位项: {display} -> {parsing}");
            return None;
        }
        // `{GUID}\cmd.exe` 这类是 Win32 系统工具的 shell 视图，不是 MSIX；
        // 它们同样已由开始菜单 `.lnk` 覆盖（实测 "命令提示符"。若要收录可去掉这段）。
        if parsing.starts_with('{') {
            log::debug!("AppsFolder 跳过系统工具视图项: {display} -> {parsing}");
            return None;
        }

        Some(AppEntry {
            name: display,
            // `msix://<AUMID>`，启动方式见 AppEntry::path 的文档。
            path: format!("{MSIX_PREFIX}{parsing}"),
            // 图标留空：AppsFolder 项没有文件系统图标路径，
            // 由 app 层用 IShellItemImageFactory 按 AUMID 取（见 voke-sys::icon）。
            icon: String::new(),
            args: Vec::new(),
            app_id: parsing,
        })
    }
}

/// 解析名看起来是不是一个文件系统路径（`C:\...` / `\\server\share`）。
///
/// 纯逻辑，Linux 上可测。
pub fn looks_like_filesystem_path(s: &str) -> bool {
    let bytes = s.as_bytes();
    // 盘符：`X:\` 或 `X:/`
    if bytes.len() >= 3
        && bytes[0].is_ascii_alphabetic()
        && bytes[1] == b':'
        && (bytes[2] == b'\\' || bytes[2] == b'/')
    {
        return true;
    }
    // UNC：`\\server`
    s.starts_with(r"\\")
}

/// `IShellItem::GetDisplayName` 的包装：转成 `String` 并释放 COM 分配的内存。
#[cfg(windows)]
unsafe fn shell_item_display_name(
    item: &IShellItem,
    sigdn: windows::Win32::UI::Shell::SIGDN,
) -> Option<String> {
    // SAFETY: 调用方保证 COM 已初始化。成功时返回 CoTaskMemAlloc 串，必须释放。
    unsafe {
        let pwstr = item.GetDisplayName(sigdn).ok()?;
        if pwstr.is_null() {
            return None;
        }
        let s = pwstr.to_string().ok();
        CoTaskMemFree(Some(pwstr.0 as *const core::ffi::c_void));
        s
    }
}

/// 按字符串解析名（AUMID 或 shell 解析名）创建 `IShellItem`。
///
/// 给图标提取用：`IShellItemImageFactory` 挂在 `IShellItem` 上，
/// 而 `SHCreateItemFromParsingName` 是构造它的标准入口。
///
/// # Safety
///
/// 调用线程必须已经初始化 COM（用 [`with_com`]），理由与 [`resolve_lnk`] 相同。
#[cfg(windows)]
pub unsafe fn shell_item_from_parsing_name(
    parsing_name: &str,
) -> windows::core::Result<IShellItem> {
    // SAFETY: 调用方保证 COM 已初始化（见函数文档）。
    unsafe { SHCreateItemFromParsingName(&HSTRING::from(parsing_name), None) }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn utf16_conversion_stops_at_nul() {
        let buf = [0x4f60u16, 0x597du16, 0, 0x41u16];
        assert_eq!(utf16_slice_to_string(&buf), "你好");
    }

    #[test]
    fn utf16_conversion_without_nul_uses_whole_buffer() {
        let buf = [0x41u16, 0x42u16];
        assert_eq!(utf16_slice_to_string(&buf), "AB");
    }

    #[test]
    fn empty_utf16_is_empty_string() {
        assert_eq!(utf16_slice_to_string(&[0u16, 0x41]), "");
    }

    /// 递归 + `.lnk` 过滤 + 跨根去重（不碰 COM，任何平台都能跑）。
    ///
    /// 关键是**嵌套同名**也能去掉重：两个根下都存在 `Vendor/Tool/App.lnk` 时，
    /// 只应保留一份。
    #[test]
    fn enumerates_lnk_recursively_and_dedupes() {
        let tmp = tempfile::tempdir().unwrap();
        let root_a = tmp.path().join("all-users");
        let root_b = tmp.path().join("current-user");
        let nested = Path::new("Vendor").join("Tool");
        std::fs::create_dir_all(root_a.join(&nested)).unwrap();
        std::fs::create_dir_all(root_b.join(&nested)).unwrap();

        std::fs::write(root_a.join("App.lnk"), b"").unwrap();
        std::fs::write(root_a.join(&nested).join("Nested.LNK"), b"").unwrap(); // 大写扩展名
        std::fs::write(root_a.join(&nested).join("notes.txt"), b"").unwrap(); // 非 lnk
        // 与 root_a 完全相同的相对路径 => 必须去重
        std::fs::write(root_b.join("App.lnk"), b"").unwrap();
        std::fs::write(root_b.join(&nested).join("Nested.LNK"), b"").unwrap();
        // 只有当前用户独有的条目 => 必须保留
        std::fs::write(root_b.join("OnlyUser.lnk"), b"").unwrap();

        let found = enum_shortcuts_in(&[root_a.clone(), root_b.clone()]);
        let mut names: Vec<String> = found
            .iter()
            .map(|p| p.file_name().unwrap().to_string_lossy().into_owned())
            .collect();
        names.sort();
        assert_eq!(
            names,
            vec!["App.lnk", "Nested.LNK", "OnlyUser.lnk"],
            "重复项去掉、独有条目保留"
        );
        // 去重时保留全用户目录那一份
        let app = found
            .iter()
            .find(|p| p.file_name().unwrap() == "App.lnk")
            .unwrap();
        assert!(app.starts_with(&root_a), "全用户优先: {}", app.display());
    }

    /// 大小写不同的同名路径视为同一个（Windows 路径语义）。
    #[test]
    fn dedup_ignores_relative_path_case() {
        let tmp = tempfile::tempdir().unwrap();
        let root_a = tmp.path().join("a");
        let root_b = tmp.path().join("b");
        std::fs::create_dir_all(root_a.join("Sub")).unwrap();
        std::fs::create_dir_all(root_b.join("sub")).unwrap();
        std::fs::write(root_a.join("Sub").join("X.lnk"), b"").unwrap();
        std::fs::write(root_b.join("sub").join("X.lnk"), b"").unwrap();
        let found = enum_shortcuts_in(&[root_a, root_b]);
        assert_eq!(found.len(), 1, "{found:?}");
    }

    #[test]
    fn missing_root_is_ignored() {
        let tmp = tempfile::tempdir().unwrap();
        let missing = tmp.path().join("does-not-exist");
        assert!(enum_shortcuts_in(&[missing]).is_empty());
    }

    /// 两个根目录都为空/缺失时不 panic，返回空列表。
    #[test]
    fn empty_roots_yield_nothing() {
        assert!(enum_shortcuts_in(&[]).is_empty());
    }

    #[test]
    fn split_dir_and_file_works() {
        // 用 `/` 分隔符：Windows 的 Path 也认 `/`，两边平台语义一致。
        let (_dir, file) = split_dir_and_file(Path::new("C:/ProgramData/a/b.lnk"));
        assert_eq!(file, "b.lnk");
    }

    /// `start_menu_dirs` 只依赖环境变量：Linux 上两个变量都不存在时返回空，
    /// 不会硬编码任何用户名路径。
    #[test]
    fn start_menu_dirs_uses_env_vars() {
        let dirs = start_menu_dirs();
        for d in &dirs {
            let s = d.to_string_lossy();
            assert!(
                s.contains("Start Menu"),
                "路径必须来自环境变量拼出的开始菜单目录: {s}"
            );
        }
        assert!(dirs.len() <= 2);
    }

    // -----------------------------------------------------------------------
    // 目标可执行性判定（B 块的核心规则，全部可在 Linux 上跑）
    // -----------------------------------------------------------------------

    /// 反斜杠路径的扩展名解析：Linux 的 `Path` 不认 `\`，所以必须有自研实现。
    #[test]
    fn extension_parsing_handles_backslashes_on_any_platform() {
        assert_eq!(
            target_extension(r"C:\Program Files\VideoLAN\VLC\NEWS.txt").as_deref(),
            Some("txt")
        );
        assert_eq!(
            target_extension(r"C:\Windows\System32\compmgmt.msc").as_deref(),
            Some("msc")
        );
        assert_eq!(target_extension(r"C:\a\b\noext"), None);
        // 目录里的点不算扩展名
        assert_eq!(target_extension(r"C:\Program Files\foo.bar\baz"), None);
        // 前导点不算扩展名（隐藏文件）
        assert_eq!(target_extension(r"C:\.gitignore"), None);
        // 尾随点不算
        assert_eq!(target_extension(r"C:\a\trailing."), None);
        // 大小写归一
        assert_eq!(target_extension(r"C:\a\B.EXE").as_deref(), Some("exe"));
        assert_eq!(target_file_name(r"C:\a\b\c.exe"), "c.exe");
    }

    /// 已定位的真实回归（问题 3）：VLC 的 `NEWS.txt` 与联想 PDF 必须被剔除。
    #[test]
    fn real_world_documents_are_rejected() {
        assert!(!is_launchable_target(
            r"C:\Program Files\VideoLAN\VLC\NEWS.txt",
            None,
            false
        ));
        assert!(!is_launchable_target(
            r"C:\Program Files\Lenovo\Lenovo AI Policy vF_zh_CN.pdf",
            None,
            false
        ));
        // 实测里另外混进来的两个文档/网页类
        assert!(!is_launchable_target(
            r"C:\Users\testuser\scoop\apps\7zip\current\7-zip.chm",
            None,
            false
        ));
        assert!(!is_launchable_target(
            r"C:\Program Files\VideoLAN\VLC\Documentation.url",
            None,
            false
        ));
    }

    /// 真实存在的应用目标必须被收录（含 `.msc` 系统工具）。
    #[test]
    fn real_world_apps_are_accepted() {
        for target in [
            r"C:\Program Files\VideoLAN\VLC\vlc.exe",
            r"C:\Program Files\Tencent\Weixin\Weixin.exe",
            r"C:\Users\testuser\scoop\apps\alacritty\current\alacritty.exe",
            r"C:\Windows\System32\compmgmt.msc",
            r"C:\Users\testuser\scoop\apps\git\current\git-bash.exe",
        ] {
            assert!(
                is_launchable_target(target, None, false),
                "应收录: {target}"
            );
        }
    }

    /// 死链（目标为空）必须剔除——空路径进 `Action::Launch` 会启动失败。
    #[test]
    fn empty_target_is_a_dead_link() {
        assert!(!is_launchable_target("", None, false));
        assert!(!is_launchable_target("   ", None, false));
        // 目标为空但解析出了外壳项 => 仍然收录（File Explorer 正是这种）
        assert!(is_launchable_target(
            "",
            Some("shell:::{52205FD8-5DFB-447D-801A-D0B52F2E83E1}"),
            false
        ));
    }

    /// 问题 1 的关键：File Explorer / 控制面板 / 运行 的目标是外壳项，
    /// `GetPath` 为空，只能靠 PIDL 解析出的 `shell:::{GUID}` 收录。
    #[test]
    fn shell_items_are_accepted_while_dead_links_are_not() {
        for guid in [
            "shell:::{52205FD8-5DFB-447D-801A-D0B52F2E83E1}", // 文件资源管理器
            "shell:::{5399E694-6CE5-4D6C-8FCE-1D8870FDCBA0}", // 控制面板
            "shell:::{2559A1F3-21D7-11D4-BDAF-00C04F60B9F0}", // 运行
        ] {
            assert!(is_launchable_target(guid, None, false), "应收录: {guid}");
            // 目标为空但有外壳项时同样收录
            assert!(is_launchable_target("", Some(guid), false));
        }
    }

    /// 无扩展名的目录（实测 `Tools.lnk` → `C:\Users\testuser\Tools`）要收录。
    #[test]
    fn extensionless_directory_and_bare_command_are_accepted() {
        assert!(is_launchable_target(r"C:\Users\testuser\Tools", None, true));
        // 无扩展名的裸命令（PATH 里的东西）
        assert!(is_launchable_target("ping", None, false));
    }

    /// 未知扩展名保守剔除（宁缺毋滥）。
    #[test]
    fn unknown_extension_is_rejected_conservatively() {
        assert!(!is_launchable_target(r"C:\a\data.dat", None, false));
        assert!(!is_launchable_target(r"C:\a\thing.xyz", None, false));
        // 但未知扩展名 + 确实是目录 => 放行（目录明确可启动）
        assert!(is_launchable_target(r"C:\a\dir.weird", None, true));
    }

    #[test]
    fn msix_and_shell_prefixes_are_distinguished() {
        let msix = AppEntry {
            name: "终端".into(),
            path: "msix://Microsoft.WindowsTerminal_8wekyb3d8bbwe!App".into(),
            app_id: "Microsoft.WindowsTerminal_8wekyb3d8bbwe!App".into(),
            ..Default::default()
        };
        assert_eq!(msix.target_kind(), TargetKind::Msix);
        assert_eq!(
            msix.msix_aumid(),
            Some("Microsoft.WindowsTerminal_8wekyb3d8bbwe!App")
        );

        let shell = AppEntry {
            name: "文件资源管理器".into(),
            path: "shell:::{52205FD8-5DFB-447D-801A-D0B52F2E83E1}".into(),
            ..Default::default()
        };
        assert_eq!(shell.target_kind(), TargetKind::ShellItem);
        assert_eq!(shell.msix_aumid(), None);

        let plain = AppEntry {
            name: "VLC".into(),
            path: r"C:\Program Files\VideoLAN\VLC\vlc.exe".into(),
            ..Default::default()
        };
        assert_eq!(plain.target_kind(), TargetKind::Path);
        assert_eq!(plain.msix_aumid(), None);
    }

    /// AppsFolder 里混着的文件系统目标要被认出来并跳过（避免与 `.lnk` 重复收录）。
    #[test]
    fn filesystem_paths_are_recognized() {
        assert!(looks_like_filesystem_path(
            r"C:\ProgramData\YunShuLauncher\YunShuLauncher.exe"
        ));
        assert!(looks_like_filesystem_path(r"\\server\share\app.exe"));
        assert!(looks_like_filesystem_path("C:/forward/slash.exe"));
        // 这些是 AUMID / shell 解析名，不是文件系统路径
        assert!(!looks_like_filesystem_path(
            "Microsoft.WindowsTerminal_8wekyb3d8bbwe!App"
        ));
        assert!(!looks_like_filesystem_path("Microsoft.VisualStudioCode"));
        assert!(!looks_like_filesystem_path(
            r"{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\cmd.exe"
        ));
        assert!(!looks_like_filesystem_path("steam://rungameid/2631960"));
    }

    /// 描述句不该被当成显示名（实测 Windows PowerShell ISE / ODBC 数据源两例）。
    #[test]
    fn sentence_like_descriptions_are_not_names() {
        // 两条真实 description：一个点在句中，一个点在结尾
        assert!(looks_like_sentence(
            "Windows PowerShell 集成脚本环境。执行基于对象的(命令行)函数"
        ));
        assert!(looks_like_sentence("维护 odbc 数据源和驱动程序。"));
        assert!(looks_like_sentence(
            "轻松发现、直观显示和共享数据中的见解。"
        ));
        // 正常显示名（含带点的英文名）都不该被判为句子
        for good in [
            "Windows Terminal",
            "Clash Party",
            "Google Chrome",
            "File Explorer",
            "文件资源管理器",
            "7-Zip File Manager",
            "Node.js",
            "Visual Studio Code",
            "Sticky Notes (new)",
        ] {
            assert!(!looks_like_sentence(good), "正常显示名不该判为句子: {good}");
        }
    }

    /// 环境变量展开（Linux 上跑同一套逻辑，用 PATH 当样本）。
    #[test]
    fn environment_expansion_substitutes_known_vars() {
        let expanded = expand_environment("%NOPE_NOT_SET%/x");
        assert_eq!(expanded, "%NOPE_NOT_SET%/x", "未知变量原样保留");
        // 没有 % 的字符串零成本返回
        assert_eq!(expand_environment(r"C:\a\b.exe"), r"C:\a\b.exe");
        // 不完整的 % 不 panic
        assert_eq!(expand_environment("abc%"), "abc%");
    }

    // -----------------------------------------------------------------------
    // 图标来源规格
    // -----------------------------------------------------------------------

    fn icon_entry(name: &str, path: &str, icon: &str) -> AppEntry {
        AppEntry {
            name: name.to_string(),
            path: path.to_string(),
            icon: icon.to_string(),
            args: Vec::new(),
            app_id: String::new(),
        }
    }

    /// `.lnk` 自带且指向 exe 的图标优先（File Explorer 的修复路径）。
    #[test]
    fn exe_icon_spec_wins() {
        let e = icon_entry(
            "File Explorer",
            "shell:::{52205FD8-5DFB-447D-801A-D0B52F2E83E1}",
            r"C:\Windows\explorer.exe",
        );
        assert_eq!(
            pick_icon_spec(&e).as_deref(),
            Some(r"C:\Windows\explorer.exe")
        );
    }

    /// 图标是 `.dll` 资源号形态时，改用启动目标（避免拿到 DLL 的通用图标）。
    #[test]
    fn dll_icon_spec_falls_back_to_target() {
        let e = icon_entry(
            "Windows 工具",
            r"C:\Windows\system32\control.exe",
            r"C:\Windows\system32\shell32.dll",
        );
        assert_eq!(
            pick_icon_spec(&e).as_deref(),
            Some(r"C:\Windows\system32\control.exe")
        );
    }

    /// 没有图标规格时用目标；MSIX 必须转成 `shell:AppsFolder\<AUMID>`
    /// （实测裸 AUMID 取不到图标）。
    #[test]
    fn target_is_used_and_msix_becomes_appsfolder_spec() {
        let plain = icon_entry("VLC", r"C:\Program Files\VideoLAN\VLC\vlc.exe", "");
        assert_eq!(
            pick_icon_spec(&plain).as_deref(),
            Some(r"C:\Program Files\VideoLAN\VLC\vlc.exe")
        );

        let msix = icon_entry(
            "终端",
            "msix://Microsoft.WindowsTerminal_8wekyb3d8bbwe!App",
            "",
        );
        assert_eq!(
            pick_icon_spec(&msix).as_deref(),
            Some(r"shell:AppsFolder\Microsoft.WindowsTerminal_8wekyb3d8bbwe!App")
        );
    }

    /// 什么都没有 => `None`（调用方清空 icon，UI 用占位图标）。
    #[test]
    fn no_icon_source_yields_none() {
        assert_eq!(pick_icon_spec(&icon_entry("x", "", "")), None);
    }

    /// 缓存文件名：可读 + 唯一 + 文件系统安全（无路径分隔符与非法字符）。
    #[test]
    fn icon_cache_key_is_safe_and_unique() {
        let a = icon_cache_key("Visual Studio Code", r"C:\a\Code.exe");
        let b = icon_cache_key("Visual Studio Code", r"C:\b\Code.exe");
        assert_ne!(a, b, "同名不同路径不能撞名");
        assert!(!a.contains(['\\', '/', ':', '*', '?', '"', '<', '>', '|']));
        assert!(a.starts_with("Visual_Studio_Code-"), "{a}");

        // 中文名字也要安全
        let c = icon_cache_key("微信", r"C:\WeChat.exe");
        assert!(!c.contains(['\\', '/', ':']));
        assert!(c.starts_with("微信-"), "{c}");

        // 名字过长要截断（避免 MAX_PATH）
        let long = icon_cache_key(&"A".repeat(300), r"C:\a.exe");
        assert!(long.len() < 80, "{} 太长", long.len());

        // 名字全是非法字符时仍有可用前缀
        let d = icon_cache_key("///", r"C:\a.exe");
        assert!(d.starts_with("app-"), "{d}");

        // 稳定性：同输入同输出（缓存才能命中）
        assert_eq!(icon_cache_key("A", "B"), icon_cache_key("A", "B"));
    }
}
