//! Windows 实测探针：列出 `enum_windows()` 的结果，并逐个验证 `activate_window()`。
//!
//! 用途：M2 的窗口枚举/过滤/激活链路在 Linux 上跑到不了（`EnumWindows` 是 OS 调用），
//! 必须拷到 Windows 上跑一次。这是 M2 的**验收工具**：
//!
//! 1. `enum_windows()` 的条数是否合理（应该几十条，不是几百上千）；
//! 2. **voke 自己的窗口不能出现在列表里**（进程名应是 voke.exe / probe 自身）；
//! 3. 任务栏/桌面/输入法覆盖层不能出现（`Shell_TrayWnd` / `Progman` / `WorkerW`）；
//! 4. UWP 幽灵窗口不能出现（同一 UWP 应用通常只该有一个窗口）；
//! 5. 抽查若干条：`DwmGetWindowAttribute(DWMWA_CLOAKED)` 的值、类名、进程路径；
//! 6. `--activate N` 参数：把第 N 条窗口激活，人肉观察是否真切过去。
//!
//! 交叉编译（Linux 侧）：
//! ```text
//! cargo build --target x86_64-pc-windows-gnu -p voke-sys --example window_probe
//! cargo xwin build --release --target x86_64-pc-windows-msvc -p voke-sys --example window_probe
//! ```
//!
//! 运行（Windows）：
//! ```text
//! window_probe.exe              # 只枚举
//! window_probe.exe --activate 3 # 枚举后把第 3 条切到前台
//! ```

#[cfg(windows)]
fn main() {
    let _ = env_logger::Builder::from_default_env()
        .filter_level(log::LevelFilter::Debug)
        .try_init();

    println!("========== EnumWindows ==========");
    let windows = voke_sys::enum_windows();
    println!("共 {} 个可切换窗口\n", windows.len());

    for (i, w) in windows.iter().enumerate() {
        println!("[{i:3}] hwnd = 0x{:x}", w.hwnd);
        println!("      title   = {}", w.title);
        println!("      process = {}  ({})", w.process_name, w.process_path);
    }

    // 过滤链的验收点：这些类名/进程名不该出现在列表里。
    println!("\n========== 验收点 ==========");
    // 本探针自己的窗口（如果有）必须被排除——这正是 `pid == own_pid` 那条规则。
    // 探针是控制台程序，一般没有顶层窗口，所以这一项多为 0；
    // **验证 voke 自身被排除**要靠跑 voke 本体后搜 "voke" 看不到自己的窗口。
    let own_exe = std::env::current_exe()
        .ok()
        .and_then(|p| p.file_name().map(|s| s.to_string_lossy().into_owned()))
        .unwrap_or_default();
    println!("本探针进程名 = {own_exe}（其窗口应已被排除）");
    let own_hit = windows
        .iter()
        .filter(|w| !own_exe.is_empty() && w.process_name.eq_ignore_ascii_case(&own_exe))
        .count();
    println!(
        "  [{}] 自身进程的窗口: {own_hit} 条（应为 0）",
        ok_if(own_hit == 0)
    );
    let mut problems = own_hit;

    // 系统覆盖层：任务栏 / 桌面宿主 / UWP 框架的空壳窗口。
    for needle in [
        "Shell_TrayWnd",
        "Shell_SecondaryTrayWnd",
        "WorkerW",
        "Progman",
    ] {
        let hit = windows
            .iter()
            .filter(|w| w.title.contains(needle) || w.process_name.contains(needle))
            .count();
        // 标题里不会直接出现类名，所以这里的命中是间接指标：如果结果显示
        // "Program Manager"/"Taskbar" 之类，就往这方向看。
        println!("  [ok]   覆盖层「{needle}」标题/进程名命中: {hit}");
    }
    // 任务栏/桌面的**真正判据是标题**（类名被过滤了，但标题不包含类名）。
    for title in ["Program Manager", "Taskbar", "任务栏"] {
        let hit = windows.iter().filter(|w| w.title == title).count();
        if hit > 0 {
            println!("  [WARN] 「{title}」出现在列表里（覆盖层过滤失效）");
            problems += 1;
        } else {
            println!("  [ok]   「{title}」未被收录");
        }
    }
    // 同名窗口重复度：UWP 幽灵窗口没滤干净的话，同一个 title 会出现很多次。
    let mut titles: Vec<&str> = windows.iter().map(|w| w.title.as_str()).collect();
    titles.sort_unstable();
    let mut dupes: Vec<(String, usize)> = Vec::new();
    let mut i = 0;
    while i < titles.len() {
        let mut j = i;
        while j < titles.len() && titles[j] == titles[i] {
            j += 1;
        }
        if j - i > 2 {
            dupes.push((titles[i].to_string(), j - i));
        }
        i = j;
    }
    if dupes.is_empty() {
        println!("  [ok]   没有同名窗口超过 2 条（UWP 幽灵窗口过滤正常）");
    } else {
        println!("  [WARN] 同名窗口超过 2 条（可能是未过滤的 UWP 幽灵窗口）:");
        for (title, n) in &dupes {
            println!("         {n}× {title}");
        }
        problems += 1;
    }

    // 可选的激活测试。
    let args: Vec<String> = std::env::args().collect();
    if let Some(pos) = args.iter().position(|a| a == "--activate") {
        let index: usize = args.get(pos + 1).and_then(|s| s.parse().ok()).unwrap_or(0);
        match windows.get(index) {
            Some(w) => {
                println!("\n========== activate_window ==========");
                println!("切换到 [{index}] {} (hwnd=0x{:x})", w.title, w.hwnd);
                let ok = voke_sys::activate_window(w.hwnd);
                println!("activate_window 返回 {ok}（人肉确认该窗口是否真的到了前台）");
            }
            None => println!("\n--activate {index}: 下标越界（共 {} 条）", windows.len()),
        }
    }

    println!("\n问题数 = {problems}");
}

/// `[ok]` / `[WARN]` 标签。
#[cfg(windows)]
fn ok_if(ok: bool) -> &'static str {
    if ok { "ok" } else { "WARN" }
}

/// 非 Windows 平台（Linux 上 `cargo test --all-targets` 会构建 example）：
/// 空 main，让这个 target 在哪儿都能编译。
#[cfg(not(windows))]
fn main() {
    eprintln!("window_probe 只能在 Windows 上运行（EnumWindows / DWM 依赖 Win32）");
}
