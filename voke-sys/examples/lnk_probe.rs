//! Windows 实测探针：枚举并解析开始菜单 `.lnk` + `Shell:AppsFolder`，把结果打到 stdout。
//!
//! 用途：把 xwin/gnu 产物拷到 Windows 上跑一次，就能确认 COM 解析链路真的通
//! （Linux 上跑不了这一层）。这是 A/B/C 三块的**验收工具**：
//!
//! 1. 修复前 98 个 `.lnk` → 95 条目；修复后应**上升**（File Explorer / 控制面板 /
//!    运行 这些外壳项被收录）同时**剔除**文档类（`NEWS.txt` / `.pdf` / `.chm` / `.url`）。
//! 2. `微软终端`（MSIX）应出现在 Store 段——它在开始菜单里根本没有 `.lnk`。
//! 3. 每条都打印 `target_kind`（path / msix / shell），便于核对启动分派。
//!
//! 交叉编译（Linux 侧）：
//! ```text
//! cargo build --target x86_64-pc-windows-gnu -p voke-sys --example lnk_probe
//! cargo xwin build --release --target x86_64-pc-windows-msvc -p voke-sys --example lnk_probe
//! ```

#[cfg(windows)]
fn main() {
    // 探针要求能看到 debug 级日志（剔除原因全靠它核对）。
    let _ = env_logger::Builder::from_default_env()
        .filter_level(log::LevelFilter::Debug)
        .try_init();

    let dirs = voke_sys::apps::start_menu_dirs();
    for d in &dirs {
        println!("扫描目录: {} (存在={})", d.display(), d.exists());
    }

    // ---- 段 1：开始菜单 .lnk ----
    println!("\n========== 开始菜单 .lnk ==========");
    let shortcuts = voke_sys::apps::enum_shortcuts();
    println!("发现 {} 个 .lnk", shortcuts.len());
    let lnk_apps = voke_sys::apps::load_start_menu_apps();
    println!("解析成功 {} 个可启动条目\n", lnk_apps.len());
    for app in &lnk_apps {
        print_entry(app);
    }

    // ---- 段 2：AppsFolder（MSIX/Store） ----
    println!("\n========== Shell:AppsFolder（MSIX/Store） ==========");
    let store = voke_sys::apps::with_com(voke_sys::apps::enum_store_apps);
    println!("枚举出 {} 个 Store/MSIX 条目\n", store.len());
    for app in &store {
        print_entry(app);
    }

    // ---- 段 3：合并结果（app 层实际拿到的形态） ----
    println!("\n========== 合并（load_all_apps） ==========");
    let all = voke_sys::apps::load_all_apps();
    println!("合并去重后 {} 个条目", all.len());
    println!(
        "  其中 path 形态: {}",
        count_kind(&all, voke_sys::TargetKind::Path)
    );
    println!(
        "  其中 msix 形态: {}",
        count_kind(&all, voke_sys::TargetKind::Msix)
    );
    println!(
        "  其中 shell 形态: {}",
        count_kind(&all, voke_sys::TargetKind::ShellItem)
    );

    // ---- 段 4：目标检查（这三个是本次修复的验收点） ----
    println!("\n========== 验收点 ==========");
    report(&all, "File Explorer", "文件资源管理器");
    report(&all, "Windows Terminal", "终端");
    report(&all, "Control Panel", "控制面板");
    println!("\n--- 应被剔除的文档类（若出现在上面列表里就是 bug）---");
    for bad in [
        "NEWS",
        "Release Notes",
        "Documentation",
        "VideoLAN Website",
        "7-Zip Help",
        "政策",
    ] {
        let hit = all.iter().any(|a| a.name.contains(bad));
        println!("  {bad:<20} 出现在列表里 = {hit}");
    }

    // ---- 段 5：图标提取（E 块的验收点） ----
    //
    // 对**全部**条目提取（而不是采样前几个）：成功率本身就是验收指标，
    // 而且 MSIX/shell 这两类在列表里排在后面，采样会漏掉——它们正是
    // `SHGetFileInfoW` 拿不到图标、必须走 `IShellItemImageFactory` 的那批。
    println!("\n========== 图标提取（IShellItemImageFactory -> PNG） ==========");
    let icon_dir = std::env::current_dir()
        .unwrap_or_default()
        .join("icon-probe-out");
    let mut stats: [(usize, usize); 3] = [(0, 0); 3]; // (成功, 总数)
    let mut shown: [usize; 3] = [0; 3];
    for app in &all {
        let slot = match app.target_kind() {
            voke_sys::TargetKind::Path => 0,
            voke_sys::TargetKind::Msix => 1,
            voke_sys::TargetKind::ShellItem => 2,
        };
        let spec = match voke_sys::apps::pick_icon_spec(app) {
            Some(s) => s,
            None => continue,
        };
        stats[slot].1 += 1;
        let key = voke_sys::apps::icon_cache_key(&app.name, &app.path, &spec);
        // SAFETY: 探针是单线程程序，with_com 提供 COM 作用域。
        let got = voke_sys::with_com(|| unsafe {
            voke_sys::icon::extract_icon_png(&spec, &icon_dir, &key)
        });
        match got {
            Some(p) => {
                stats[slot].0 += 1;
                // 每类只打印前几条，避免刷屏（完整数量看下面的统计行）。
                if shown[slot] < 3 {
                    shown[slot] += 1;
                    let size = std::fs::metadata(&p).map(|m| m.len()).unwrap_or(0);
                    println!(
                        "  OK   [{}] {:<26} spec={:<52} -> {} ({size} B)",
                        kind_label(slot),
                        app.name,
                        truncate(&spec, 52),
                        p.file_name().unwrap_or_default().to_string_lossy()
                    );
                }
            }
            None => println!(
                "  FAIL [{}] {:<26} spec={}",
                kind_label(slot),
                app.name,
                spec
            ),
        }
    }
    println!("\n图标提取统计：");
    for (i, label) in ["path ", "msix ", "shell"].iter().enumerate() {
        let (ok, total) = stats[i];
        println!("  {label}: 成功 {ok} / {total}");
    }
}

/// 打印一条条目（含启动形态，便于核对 dispatcher 分派）。
#[cfg(windows)]
fn print_entry(app: &voke_sys::AppEntry) {
    let kind = match app.target_kind() {
        voke_sys::TargetKind::Msix => "msix ",
        voke_sys::TargetKind::ShellItem => "shell",
        voke_sys::TargetKind::Path => "path ",
    };
    println!("name = {}", app.name);
    println!("  kind = {kind}  path = {}", app.path);
    if !app.app_id.is_empty() {
        println!("  aumid = {}", app.app_id);
    }
    if !app.icon.is_empty() {
        println!("  icon = {}", app.icon);
    }
    if !app.args.is_empty() {
        println!("  args = {:?}", app.args);
    }
    if !app.working_dir.is_empty() {
        println!(
            "  cwd = {}（解析期已展开，未做存在性校验）",
            app.working_dir
        );
    }
}

/// 启动形态的短标签（图标统计用）。
#[cfg(windows)]
fn kind_label(slot: usize) -> &'static str {
    match slot {
        0 => "path ",
        1 => "msix ",
        _ => "shell",
    }
}

/// 按字符数截断（避免把长 AUMID 撑爆终端宽度）。
#[cfg(windows)]
fn truncate(s: &str, max: usize) -> String {
    if s.chars().count() <= max {
        return s.to_string();
    }
    let head: String = s.chars().take(max.saturating_sub(1)).collect();
    format!("{head}…")
}

/// 统计某种启动形态的条目数。
#[cfg(windows)]
fn count_kind(apps: &[voke_sys::AppEntry], kind: voke_sys::TargetKind) -> usize {
    apps.iter().filter(|a| a.target_kind() == kind).count()
}

/// 在结果里找一个应用并打印它的关键字段（验收用）。
#[cfg(windows)]
fn report(apps: &[voke_sys::AppEntry], label: &str, needle: &str) {
    let found = apps
        .iter()
        .find(|a| a.name.contains(needle) || a.name.eq_ignore_ascii_case(label));
    match found {
        Some(a) => println!("  [OK]   {label:<18} name={:<16} path={}", a.name, a.path),
        None => println!("  [MISS] {label:<18} 未出现在结果里"),
    }
}

/// 非 Windows 平台（Linux 上 `cargo test --all-targets` 会构建 example）：
/// 空 main，让这个 target 在哪儿都能编译。
#[cfg(not(windows))]
fn main() {
    eprintln!("lnk_probe 只能在 Windows 上运行（.lnk / AppsFolder 解析依赖 COM）");
}
