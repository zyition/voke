//! 应用图标准备：把 sys 层给的"图标来源规格"落成 PNG 文件路径。
//!
//! ## 数据流
//!
//! ```text
//! voke_sys::AppEntry.icon   解析期的"图标来源规格"
//!   （.lnk 自带图标 或 目标文件路径，可能为空、可能是 .dll）
//!         │  prepare_icons()  ── 后台线程 + COM 作用域
//!         ▼
//! voke_sys::icon::extract_icon_png()  ── IShellItemImageFactory::GetImage
//!         │  -> 32×32 PNG 落盘（icons/<名字>-<路径哈希>.png）
//!         │     （来源规格的挑选规则见 voke_sys::apps::pick_icon_spec）
//!         ▼
//! AppEntry.icon = "C:\\...\\icons\\VLC_3f2a91c0.png"   ← 真实文件路径
//!         │  to_core_entries() -> Match.icon
//!         ▼
//! voke-app/src/search.rs  render_row_icon() -> gpui img(Resource::Path)
//! ```
//!
//! 窗口条目（M2）走同一条路，只是"来源规格"直接就是进程 exe 路径
//! （[`prepare_window_icons`]）：窗口没有 `.lnk`、没有 AUMID，图标只能从
//! 进程可执行文件取。
//!
//! ## 为什么在这里做（而不是 sys 层）
//!
//! sys 层只负责"把图标变成像素"，**落盘位置与命名策略属于应用层决策**
//! （缓存目录在哪、文件名怎么起、失败怎么办），而且要写 core 的 `AppEntry`。
//! 这样 sys 保持"无策略、纯能力"。
//!
//! ## 失败策略
//!
//! 提取失败**不是错误**：清空 `icon` 让 UI 回退到占位图标即可。
//! 系统里存在大量取不到图标的东西（损坏的资源、被 ACL 保护的文件），
//! 任何一个都不该让整批应用加载失败。

use std::path::PathBuf;

use voke_sys::AppEntry;

/// 图标缓存目录名。
const ICON_DIR: &str = "icons";

/// 图标缓存目录的绝对路径。
///
/// 优先 `%APPDATA%\voke\icons`（与配置文件、日志同一处）；拿不到专用目录
/// 才退回进程工作目录。
///
/// **为什么不再固定用工作目录**：开机自启动时工作目录由系统决定（常见
/// `C:\Windows\system32`），按工作目录算出来的缓存路径每次都不一样——
/// 图标每轮都重新提取一遍，而且往系统目录里写还会失败。
pub fn icon_cache_dir() -> PathBuf {
    voke_sys::paths::icon_dir().unwrap_or_else(|| {
        let base = std::env::current_dir().unwrap_or_else(|_| PathBuf::from("."));
        base.join(ICON_DIR)
    })
}

/// 就地给一批条目补齐图标路径（把"来源规格"换成落盘 PNG 的真实路径）。
///
/// 在后台线程调用（COM + 上百次位图提取，几十到几百毫秒）。
pub fn prepare_icons(entries: &mut [AppEntry]) {
    let dir = icon_cache_dir();
    let mut ok = 0usize;
    let mut failed = 0usize;

    // COM 作用域包住整批：`CoInitializeEx` 是 per-thread 的，逐个初始化纯浪费。
    voke_sys::with_com(|| {
        for entry in entries.iter_mut() {
            let Some(spec) = voke_sys::apps::pick_icon_spec(entry) else {
                entry.icon.clear();
                failed += 1;
                continue;
            };
            let key = voke_sys::apps::icon_cache_key(&entry.name, &entry.path);
            // SAFETY: 外层 with_com 已建立 COM 作用域。
            match unsafe { voke_sys::icon::extract_icon_png(&spec, &dir, &key) } {
                Some(path) => {
                    entry.icon = path.to_string_lossy().into_owned();
                    ok += 1;
                }
                None => {
                    // 提取不到：清空，UI 回退占位图标。
                    log::debug!("图标提取失败: {} (spec={spec})", entry.name);
                    entry.icon.clear();
                    failed += 1;
                }
            }
        }
    });

    log::info!("图标准备完成：成功 {ok}，失败/无图标 {failed}");
}

/// 就地给一批窗口条目补齐图标路径（来源就是进程 exe 路径）。
///
/// 与 [`prepare_icons`] 同一套路，差别只在来源规格直接取 `process_path`：
/// 窗口没有 `.lnk`、没有 AUMID，图标只能从进程可执行文件取。
///
/// **在后台线程调用**（COM 作用域；缓存未命中时是真实的位图提取）。
/// 同一 exe 的图标会命中 [`voke_sys::icon::extract_icon_png`] 的落盘缓存
/// （缓存键 = 进程名 + 路径哈希），所以重复查询不会反复解码。
///
/// **已有缓存的图标不走 COM**：本函数先查
/// [`voke_sys::icon::icon_cache_path`]，只有真需要提取时才进 `with_com`
/// ——窗口 provider 每次按键都会调到这里，全缓存命中时不该反复
/// `CoInitializeEx`。
///
/// 受保护进程取不到 `process_path`（枚举期就是空串）→ 直接留空，UI 回退占位图标。
pub fn prepare_window_icons(entries: &mut [voke_sys::WindowEntry]) {
    let dir = icon_cache_dir();
    let mut cached = 0usize;
    let mut extracted = 0usize;
    let mut skipped = 0usize;

    // 同一 exe 的多个窗口共享一张图标：用路径去重，避免重复走同一条流程。
    let mut per_path: std::collections::HashMap<String, String> = std::collections::HashMap::new();
    // 需要在本次新建 COM 作用域提取的（(索引, 路径, 缓存键)）。
    let mut pending: Vec<(usize, String, String)> = Vec::new();

    for (i, entry) in entries.iter_mut().enumerate() {
        if entry.process_path.is_empty() {
            entry.icon.clear();
            skipped += 1;
            continue;
        }
        if let Some(existing) = per_path.get(&entry.process_path) {
            entry.icon = existing.clone();
            if existing.is_empty() {
                skipped += 1;
            } else {
                cached += 1;
            }
            continue;
        }
        // 缓存键用进程名（而非窗口标题）：同一程序的所有窗口共享一张图标。
        let key = voke_sys::apps::icon_cache_key(&entry.process_name, &entry.process_path);
        let path = voke_sys::icon::icon_cache_path(&dir, &key);
        if path.is_file() {
            // 纯文件系统检查，不碰 COM。
            let path = path.to_string_lossy().into_owned();
            entry.icon = path.clone();
            per_path.insert(entry.process_path.clone(), path);
            cached += 1;
        } else {
            pending.push((i, entry.process_path.clone(), key));
        }
    }

    if pending.is_empty() {
        log::debug!("窗口图标全部命中缓存：{cached}，无需 COM");
        return;
    }

    // 只有真有未缓存的才进 COM 作用域（一次，包住整批）。
    let mut results: std::collections::HashMap<String, String> = std::collections::HashMap::new();
    voke_sys::with_com(|| {
        for (_i, process_path, key) in &pending {
            // SAFETY: 外层 with_com 已建立 COM 作用域。
            let got = unsafe { voke_sys::icon::extract_icon_png(process_path, &dir, key) };
            let value = got
                .map(|p| p.to_string_lossy().into_owned())
                .unwrap_or_default();
            results.insert(process_path.clone(), value);
        }
    });

    for (i, process_path, _key) in pending {
        let value = results.get(&process_path).cloned().unwrap_or_default();
        if value.is_empty() {
            log::debug!("窗口图标提取失败: {} ({process_path})", entries[i].title);
            skipped += 1;
        } else {
            extracted += 1;
        }
        entries[i].icon = value.clone();
        per_path.insert(process_path, value);
    }

    log::info!("窗口图标准备完成：缓存 {cached}，新提取 {extracted}，无图标 {skipped}");
}

#[cfg(test)]
mod tests {
    use super::*;

    /// 缓存目录必须以 `icons` 结尾（在专用目录下或退回工作目录，都成立）。
    #[test]
    fn cache_dir_ends_with_icons() {
        let dir = icon_cache_dir();
        assert!(dir.ends_with(ICON_DIR), "{}", dir.display());
    }
}
