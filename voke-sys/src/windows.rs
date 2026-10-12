//! 窗口枚举与激活（§5 的 Window Walker，对齐 PowerToys Run 的 Window Walker 插件）。
//!
//! ## 职责
//!
//! - [`enum_windows`]：把当前桌面上"用户能看见并切过去"的顶层窗口变成 [`WindowEntry`]。
//! - [`activate_window`]：把一个 HWND 切到前台。
//!
//! 只做"把系统状态变成数据"与"执行一个动作"，匹配/排序/UI 全在下游（core + app）。
//! 字段名与 `voke_core::WindowEntry` 逐字对齐，app 层直接结构体字面量转换。
//!
//! ## 过滤链（每一步都有理由，顺序按"便宜的先做"）
//!
//! | # | 条件 | 依据 |
//! |---|---|---|
//! | 1 | `IsWindowVisible` | 隐藏窗口用户看不见，切过去也没意义 |
//! | 2 | `GetWindowThreadProcessId != 本进程` | **否则 voke 自己的窗口会出现在结果里**（gpui 的窗口类名是 `Zed::Window`） |
//! | 3 | `GetWindowTextW` 非空 | 无标题窗口（工具窗、消息窗）不可辨识 |
//! | 4 | 类名不在 [`EXCLUDED_CLASSES`] | 任务栏 / 桌面 / 输入法等系统覆盖层 |
//! | 5 | `DwmGetWindowAttribute(DWMWA_CLOAKED) == 0` | **UWP 幽灵窗口**：UWP 应用会为每个已激活过的窗口留一个"cloaked"的隐藏顶层窗口，不滤掉会看到一堆同名重复项 |
//! | 6 | `WS_EX_TOOLWINDOW` 且**不带** `WS_EX_APPWINDOW` | **托盘/工具进程**：通知区域（Zephyr 托盘）与大量后台工具会创建可见、有标题、但绝不该出现在 Alt-Tab 里的工具窗口（`WS_EX_TOOLWINDOW` 的字面含义就是"不显示在任务栏/Alt-Tab"）。允许 `WS_EX_APPWINDOW` 同时置位的窗口是有意上任务栏的（部分应用两者都设） |
//!
//! ## 为什么不用 COM / WinRT
//!
//! `EnumWindows` 是同线程**同步回调**（回调在调用线程内被逐个直接调用，不是
//! `SendMessage`、也不排队），不需要 COM apartment，
//! 也不像 `UIAutomation` 那样要跑消息循环。它在 UI 线程上会阻塞几十到几百毫秒
//! （窗口多时），所以 **app 层必须放到后台线程调用**（见 `voke-app/src/search.rs`）。
//!
//! ## 平台分层
//!
//! [`WindowEntry`] 与过滤/进程名等纯逻辑在**任何平台**编译并测试
//! （Linux 上 `cargo test -p voke-sys` 会真跑）；只有真正碰 Win32 的
//! [`enum_windows`] / [`activate_window`] 是 `#[cfg(windows)]`。
//! **这一点踩过坑**：最初整个 `pub mod windows` 都挂在 `#[cfg(windows)]` 下，
//! 结果 Linux 上 `cargo test -p voke-sys` 根本碰不到 `is_excluded_class` 的
//! 单测，一个真实的大小写 bug（大小写常量 vs 小写化输入）得以漏网——
//! 是 Windows 实测探针把它拽出来的。

use crate::apps::target_file_name;

/// 一个可切换的顶层窗口。
///
/// 字段与 `voke_core::WindowEntry` 逐字对齐（`hwnd` 用裸 `isize`，
/// 不让 core 依赖 windows crate 的 `HWND`）。
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct WindowEntry {
    /// 窗口句柄的数值（`HWND` 的位模式）。
    ///
    /// 用 `isize` 而不是 `HWND`：core 不能依赖 windows crate，
    /// 且 `isize` 可直接进 [`crate::windows::activate_window`] 与 serde。
    pub hwnd: isize,
    /// 窗口标题（`GetWindowTextW`）。
    pub title: String,
    /// 进程可执行文件名，例如 `Code.exe`（无扩展名的兜底见 [`process_name_from_path`]）。
    ///
    /// 用作结果列表的副标题，也是搜索键的一部分（输入 `code` 能命中）。
    pub process_name: String,
    /// 进程可执行文件完整路径，例如 `C:\...\Code.exe`。
    ///
    /// 图标提取（[`crate::icon::extract_icon_png`]）与调试用；
    /// 取不到时是空串（受保护进程 `OpenProcess` 会失败，见 [`process_path_for_pid`]）。
    pub process_path: String,
    /// 图标 PNG 的落盘路径，枚举期为空，由 app 层用 `process_path` 填充。
    pub icon: String,
}

/// 系统覆盖层类名（精确匹配 + `.后缀` 变体，大小写不敏感）。
///
/// 这些窗口可见、有类名，但**不是用户想切换的目标**：
///
/// | 类名 | 是什么 |
/// |---|---|
/// | `Shell_TrayWnd` | 主任务栏 |
/// | `Shell_SecondaryTrayWnd` | 副显示器任务栏 |
/// | `WorkerW` | 壁纸/桌面图标宿主（多显示器下每个屏一个，可能是可见的） |
/// | `Progman` | 桌面（`Program Manager` 窗口） |
/// | `Windows.UI.Core.CoreWindow` | UWP 框架窗口（真正的可见窗口是外层 `ApplicationFrameWindow`）。**实测**：IME 候选窗口「Windows 输入体验」就是这个类且有非空标题，靠标题规则挡不住 |
/// | `IslandWindow` | Win11 的「单击以执行 / Click to Do」覆盖层（**实测**观察到，非文档列出） |
/// | `WinUIDesktopWin32WindowClass` | Win11 的「命令面板」等 WinUI 桌面覆盖层（**实测**观察到） |
///
/// 后两个是 Windows 实机探针（`examples/window_probe.rs`）跑出来才发现的：
/// 它们有非空中文标题、可见、进程是 `explorer.exe`，不滤掉会以「单击以执行」
/// 「命令面板」这种名字污染结果列表。
pub const EXCLUDED_CLASSES: &[&str] = &[
    "Shell_TrayWnd",
    "Shell_SecondaryTrayWnd",
    "WorkerW",
    "Progman",
    "Windows.UI.Core.CoreWindow",
    "IslandWindow",
    "WinUIDesktopWin32WindowClass",
];

/// 类名是否属于要剔除的系统覆盖层（大小写不敏感）。
///
/// 用前缀匹配以覆盖 `WorkerW` 之类的带后缀变体（实测任务栏/壁纸宿主类名可能
/// 带附加后缀，例如 `Progman` 之后的 `WorkerW`），同时不引入子串误伤。
///
/// **大小写两边都要归一**：`EXCLUDED_CLASSES` 里的常量是 `Shell_TrayWnd`
/// 这种大小写混合形式，只把输入小写化再 `strip_prefix` 会永远匹配不上
/// （实测踩过：`Progman` / `Shell_TrayWnd` 全部漏过，靠空标题规则碰巧挡掉一部分）。
pub fn is_excluded_class(class: &str) -> bool {
    let lower = class.to_ascii_lowercase();
    EXCLUDED_CLASSES.iter().any(|excluded| {
        let excluded = excluded.to_ascii_lowercase();
        lower
            .strip_prefix(&excluded)
            .is_some_and(|rest| rest.is_empty() || rest.starts_with('.'))
    })
}

/// 综合过滤判定：这个窗口要不要进结果列表。
///
/// 纯逻辑，任意平台可测。参数全部由 [`enum_windows`] 从 Win32 取好传入
/// （这也是"过滤规则可被 Linux 单测覆盖"的关键——Win32 调用本身没法在 WSL 跑）。
///
/// 每一项的理由见模块文档的过滤链表格。`tool_window` / `app_window` 分别是
/// `WS_EX_TOOLWINDOW` / `WS_EX_APPWINDOW` 是否置位。
pub fn should_include_window(
    visible: bool,
    cloaked: bool,
    title: &str,
    class: &str,
    pid: u32,
    own_pid: u32,
    tool_window: bool,
    app_window: bool,
) -> bool {
    if !visible {
        return false;
    }
    // 本进程自己的窗口（voke 的 `Zed::Window`）：不排除的话搜自己会很困惑。
    if pid == own_pid {
        return false;
    }
    if title.trim().is_empty() {
        return false;
    }
    if is_excluded_class(class) {
        return false;
    }
    // UWP 幽灵窗口。
    if cloaked {
        return false;
    }
    // 托盘 / 工具窗口：`WS_EX_TOOLWINDOW` 的系统语义就是"不进任务栏与 Alt-Tab"，
    // 而 voke 的窗口结果本质上就是 Alt-Tab 列表，理应遵循同一规则。
    //
    // **例外**：同时带 `WS_EX_APPWINDOW` 的窗口是应用有意让它上任务栏的
    // （有些应用两者都设，靠 `WS_EX_APPWINDOW` 压过 `TOOLWINDOW`），这种要留。
    // 所以拿到 `WS_EX_TOOLWINDOW` 的窗口才需要这个标记，拿不到就按"不是工具窗"处理。
    if tool_window && !app_window {
        return false;
    }
    true
}

/// 窗口扩展样式里是否置了某一位（纯逻辑，方便单测）。
pub fn has_ext_style(ex_style: isize, style_bit: u32) -> bool {
    (ex_style as usize) & (style_bit as usize) != 0
}

/// 从完整进程路径取进程名（`C:\a\Code.exe` → `Code.exe`）。
///
/// 同时认 `\` 与 `/`（Linux 单测），实现复用 [`crate::apps::target_file_name`]。
/// 空路径返回空串（受保护进程取不到路径，调用方把副标题留空即可）。
pub fn process_name_from_path(path: &str) -> String {
    if path.is_empty() {
        return String::new();
    }
    target_file_name(path).to_string()
}

// ---------------------------------------------------------------------------
// Win32：窗口枚举（仅 Windows）
// ---------------------------------------------------------------------------

/// 枚举所有"用户可切换"的顶层窗口。
///
/// **只应在后台线程调用**：`EnumWindows` 同步遍历全部顶层窗口，窗口多时
/// 在 UI 线程上会掉帧（§2 的"慢 provider 不阻塞快 provider"）。
///
/// 过滤规则见模块文档；返回值顺序是系统给的 Z 序（大致前台在前），
/// **不要**在这里排序——排序是 core 的职责。
#[cfg(windows)]
pub fn enum_windows() -> Vec<WindowEntry> {
    use ::windows::Win32::Foundation::LPARAM;
    use ::windows::Win32::UI::WindowsAndMessaging::EnumWindows;

    let mut out: Vec<WindowEntry> = Vec::new();
    // SAFETY: lparam 指向本函数栈上的 `out`，生命周期覆盖整次 EnumWindows 调用；
    // 回调只在调用期间同步执行，不会逃逸。
    unsafe {
        let _ = EnumWindows(
            Some(enum_proc),
            LPARAM(&mut out as *mut Vec<WindowEntry> as isize),
        );
    }
    out
}

/// `EnumWindows` 的回调。返回 `BOOL(1)` 继续枚举。
///
/// **不 panic**：panic 跨 FFI 边界是 UB，所以内部全部走"取不到就跳过"。
#[cfg(windows)]
unsafe extern "system" fn enum_proc(
    hwnd: ::windows::Win32::Foundation::HWND,
    lparam: ::windows::Win32::Foundation::LPARAM,
) -> ::windows::core::BOOL {
    // SAFETY: 由 enum_windows 保证 lparam 指向一个存活的 Vec。
    let out = unsafe { &mut *(lparam.0 as *mut Vec<WindowEntry>) };
    // SAFETY: hwnd 由 EnumWindows 给出，调用期间有效。
    if let Some(entry) = unsafe { window_entry(hwnd) } {
        out.push(entry);
    }
    ::windows::core::BOOL(1)
}

/// 把单个 HWND 转成 [`WindowEntry`]；不该收录时返回 `None`。
///
/// # Safety
///
/// `hwnd` 必须是一个（可能已失效的）窗口句柄。所有 Win32 调用都对无效句柄
/// 返回失败，本函数把它当"跳过"，不会 UB。
#[cfg(windows)]
unsafe fn window_entry(hwnd: ::windows::Win32::Foundation::HWND) -> Option<WindowEntry> {
    use ::windows::Win32::UI::WindowsAndMessaging::{GetWindowThreadProcessId, IsWindowVisible};

    // 1. 可见性（最便宜，先做）。
    // SAFETY: hwnd 有效（调用方契约）。
    let visible = unsafe { IsWindowVisible(hwnd) }.as_bool();

    // 2. 进程 id（本进程窗口直接排除，顺便后面取进程信息复用）。
    let mut pid: u32 = 0;
    // SAFETY: &mut pid 是有效可写指针。
    unsafe { GetWindowThreadProcessId(hwnd, Some(&mut pid)) };
    let own_pid = unsafe { ::windows::Win32::System::Threading::GetCurrentProcessId() };

    // 3. 标题。
    let title = unsafe { window_text(hwnd) };

    // 4. 类名。
    let class = unsafe { window_class(hwnd) };

    // 5. UWP cloak。
    let cloaked = unsafe { is_cloaked(hwnd) };

    // 6. 扩展样式：`WS_EX_TOOLWINDOW` / `WS_EX_APPWINDOW`（托盘 / 工具窗口过滤）。
    let ex_style = unsafe { window_ex_style(hwnd) };
    use ::windows::Win32::UI::WindowsAndMessaging::{WS_EX_APPWINDOW, WS_EX_TOOLWINDOW};
    let tool_window = has_ext_style(ex_style, WS_EX_TOOLWINDOW.0);
    let app_window = has_ext_style(ex_style, WS_EX_APPWINDOW.0);

    if !should_include_window(
        visible,
        cloaked,
        &title,
        &class,
        pid,
        own_pid,
        tool_window,
        app_window,
    ) {
        return None;
    }

    // 7. 进程信息（最贵，放在通过全部过滤之后）。
    let process_path = unsafe { process_path_for_pid(pid) };
    let process_name = process_name_from_path(&process_path);
    if process_name.is_empty() && process_path.is_empty() {
        log::debug!("窗口 {title:?} 取不到进程路径（PID {pid}，可能是受保护进程）");
    }

    Some(WindowEntry {
        hwnd: hwnd.0 as isize,
        title,
        process_name,
        process_path,
        icon: String::new(),
    })
}

/// `GetWindowLongPtrW(hwnd, GWL_EXSTYLE)`：取窗口扩展样式。
///
/// 失败（或非 Windows 语义下拿不到）时返回 0，调用方当作"没有特殊样式位"处理
/// ——宁可不剔除也不要误杀。
#[cfg(windows)]
unsafe fn window_ex_style(hwnd: ::windows::Win32::Foundation::HWND) -> isize {
    use ::windows::Win32::UI::WindowsAndMessaging::{GWL_EXSTYLE, GetWindowLongPtrW};
    // SAFETY: hwnd 由调用方保证是（可能已失效的）窗口句柄；Win32 对失效句柄
    // 返回 0（`GetLastError` 会置位），不会 UB。
    unsafe { GetWindowLongPtrW(hwnd, GWL_EXSTYLE) }
}

/// `GetWindowTextW`：先问长度再取，避免开大缓冲。
///
/// 注意 `GetWindowTextLengthW` 对**别的进程**的窗口返回 0（这是 Win32 的文档行为，
/// 因为它内部走 `WM_GETTEXT` 消息，跨进程被 `WM_GETTEXT` 的隔离限制）。
/// 所以实际取文本时用一个足够大的定长缓冲 + `GetWindowTextW`，
/// 它的跨进程行为是"能拿到就拿到"（我们实测能拿到普通窗口标题）。
#[cfg(windows)]
unsafe fn window_text(hwnd: ::windows::Win32::Foundation::HWND) -> String {
    use ::windows::Win32::UI::WindowsAndMessaging::GetWindowTextW;
    // 窗口标题的实际上限远小于此（Windows 对窗口标题有长度约束）。
    let mut buf = [0u16; 512];
    // SAFETY: buf 是有效可写切片，len 作为 nmaxcount 传入。
    let len = unsafe { GetWindowTextW(hwnd, &mut buf) };
    if len <= 0 {
        return String::new();
    }
    crate::apps::utf16_slice_to_string(&buf[..len as usize])
}

/// `GetClassNameW`。
#[cfg(windows)]
unsafe fn window_class(hwnd: ::windows::Win32::Foundation::HWND) -> String {
    use ::windows::Win32::UI::WindowsAndMessaging::GetClassNameW;
    let mut buf = [0u16; 256];
    // SAFETY: buf 是有效可写切片。
    let len = unsafe { GetClassNameW(hwnd, &mut buf) };
    if len <= 0 {
        return String::new();
    }
    crate::apps::utf16_slice_to_string(&buf[..len as usize])
}

/// `DwmGetWindowAttribute(hwnd, DWMWA_CLOAKED)`：UWP 幽灵窗口判定。
///
/// 返回 `true` 表示 cloaked（要剔除）。**调用失败时返回 `false`**：
/// 老系统 / 非 DWM 合成的窗口可能不支持这个属性，宁可不剔除也不要误杀。
///
/// windows 0.62.2 的真实常量名（已查 `~/.cargo/registry/src/.../windows-0.62.2/src/
/// Windows/Win32/Graphics/Dwm/mod.rs:222`，**不是**臆造的）：
/// `DWMWA_CLOAKED: DWMWINDOWATTRIBUTE = DWMWINDOWATTRIBUTE(14i32)`。
#[cfg(windows)]
unsafe fn is_cloaked(hwnd: ::windows::Win32::Foundation::HWND) -> bool {
    use ::windows::Win32::Graphics::Dwm::{DWMWA_CLOAKED, DwmGetWindowAttribute};
    let mut cloaked: u32 = 0;
    // SAFETY: &mut cloaked 是有效可写指针，长度与 cbattribute 一致。
    let result = unsafe {
        DwmGetWindowAttribute(
            hwnd,
            DWMWA_CLOAKED,
            &mut cloaked as *mut u32 as *mut core::ffi::c_void,
            core::mem::size_of::<u32>() as u32,
        )
    };
    match result {
        Ok(()) => cloaked != 0,
        Err(err) => {
            log::trace!("DwmGetWindowAttribute(DWMWA_CLOAKED) 失败，按未 cloaked 处理: {err}");
            false
        }
    }
}

/// `OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION)` + `QueryFullProcessImageNameW`
/// 取进程完整路径。
///
/// 失败返回空串，理由：
/// - 受保护进程（`System`、`csrss.exe`、部分反作弊）不允许查询，这是常态，不是错误；
/// - 用 `PROCESS_QUERY_LIMITED_INFORMATION` 而不是 `PROCESS_QUERY_INFORMATION`：
///   前者对权限要求低得多（Vista+ 的推荐做法），普通用户也能查大多数进程。
///
/// 缓冲从 512 起、不够时按返回的所需长度扩容（长路径策略下的路径可能超 `MAX_PATH`）。
#[cfg(windows)]
unsafe fn process_path_for_pid(pid: u32) -> String {
    use ::windows::Win32::Foundation::CloseHandle;
    use ::windows::Win32::System::Threading::{
        OpenProcess, PROCESS_NAME_WIN32, PROCESS_QUERY_LIMITED_INFORMATION,
        QueryFullProcessImageNameW,
    };
    use ::windows::core::PWSTR;

    if pid == 0 {
        return String::new();
    }
    // SAFETY: 参数是合法常量；失败返回 Err（不产生泄漏）。
    let process = match unsafe { OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, false, pid) } {
        Ok(handle) => handle,
        Err(_) => return String::new(),
    };

    // 先试 512，不够再按系统给出的所需长度扩一次；仍失败就放弃。
    // `QueryFullProcessImageNameW` 失败时会把 `size` 写成"含 NUL 的所需长度"。
    let mut cap = 512u32;
    let mut path = String::new();
    for _ in 0..3 {
        let mut buf = vec![0u16; cap as usize];
        let mut size = cap;
        // SAFETY: buf 是有效可写切片，size 是有效可写指针。
        let result = unsafe {
            QueryFullProcessImageNameW(
                process,
                PROCESS_NAME_WIN32,
                PWSTR(buf.as_mut_ptr()),
                &mut size,
            )
        };
        match result {
            Ok(()) => {
                path = crate::apps::utf16_slice_to_string(&buf[..size as usize]);
                break;
            }
            Err(_) => {
                // size 此时是所需长度（含 NUL）。加一个余量再试。
                let needed = size.saturating_add(1);
                if needed <= cap {
                    // 没有给出更大的长度：重试也不会成功。
                    break;
                }
                cap = needed;
            }
        }
    }

    // SAFETY: process 由 OpenProcess 得到，与这里配对释放。
    unsafe {
        let _ = CloseHandle(process);
    }
    path
}

// ---------------------------------------------------------------------------
// Win32：窗口激活（仅 Windows）
// ---------------------------------------------------------------------------

/// 把一个窗口切到前台。返回是否确认拿到了前台。
///
/// 步骤（对齐 PowerToys 与 gpui-pre-windows 的做法）：
///
/// 1. 最小化的窗口先 `ShowWindowAsync(SW_RESTORE)`（`SW_RESTORE` 会自动激活）。
/// 2. `SetForegroundWindow`。
/// 3. 如果没抢到前台（Windows 的前台锁定：只有"收到过输入事件"的进程才能抢），
///    模拟一次 Alt 键按下/抬起再重试——`SetForegroundWindow` 的前置条件之一是
///    调用进程最近收到过输入，注入一次按键正好满足。
///    这段 ragebait 的出处：`gpui-pre-windows/src/window.rs:833-855`。
/// 4. 返回 `GetForegroundWindow() == hwnd`。
///
/// 失败（窗口已销毁）返回 `false`，调用方记日志即可，不要 panic。
#[cfg(windows)]
pub fn activate_window(hwnd: isize) -> bool {
    use ::windows::Win32::Foundation::HWND;
    use ::windows::Win32::UI::WindowsAndMessaging::{
        IsIconic, SW_RESTORE, SetForegroundWindow, ShowWindowAsync,
    };

    let handle = HWND(hwnd as *mut core::ffi::c_void);
    if handle.0.is_null() {
        return false;
    }
    // SAFETY: handle 可能是失效句柄，Win32 对失效句柄返回失败，不会 UB。
    unsafe {
        if IsIconic(handle).as_bool() {
            let _ = ShowWindowAsync(handle, SW_RESTORE);
        }
        let _ = SetForegroundWindow(handle);
    }
    if crate::is_foreground_window(hwnd) {
        return true;
    }

    log::debug!("SetForegroundWindow 未拿到前台，注入 Alt 键后重试");
    spoof_alt_key();
    // SAFETY: 同上。
    unsafe {
        let _ = SetForegroundWindow(handle);
    }
    crate::is_foreground_window(hwnd)
}

/// 模拟一次 Alt 键按下/抬起，绕过 Windows 的前台锁定。
///
/// 用 `SendInput`（与 gpui-pre-windows 同款）而不是 `keybd_event`：
/// `SendInput` 是 `keybd_event` 的替代品，且在 UIPI 下行为更可预期。
///
/// **注意**：本函数只为了满足"调用进程最近收到过输入"这一前置条件；
/// 它注入的是**本进程**的输入队列，不会真的把 Alt 传给用户正在用的窗口。
#[cfg(windows)]
fn spoof_alt_key() {
    use ::windows::Win32::UI::Input::KeyboardAndMouse::{
        INPUT, INPUT_0, INPUT_KEYBOARD, KEYBD_EVENT_FLAGS, KEYBDINPUT, KEYEVENTF_KEYUP, SendInput,
        VK_MENU,
    };

    let inputs = [
        INPUT {
            r#type: INPUT_KEYBOARD,
            Anonymous: INPUT_0 {
                ki: KEYBDINPUT {
                    wVk: VK_MENU,
                    dwFlags: KEYBD_EVENT_FLAGS(0),
                    ..Default::default()
                },
            },
        },
        INPUT {
            r#type: INPUT_KEYBOARD,
            Anonymous: INPUT_0 {
                ki: KEYBDINPUT {
                    wVk: VK_MENU,
                    dwFlags: KEYEVENTF_KEYUP,
                    ..Default::default()
                },
            },
        },
    ];
    // SAFETY: inputs 是有效切片；cbsize 与 INPUT 大小一致。
    let sent = unsafe { SendInput(&inputs, core::mem::size_of::<INPUT>() as i32) };
    if sent == 0 {
        log::debug!("SendInput 注入 Alt 键失败（注入被阻止，SetForegroundWindow 可能仍然失败）");
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// 覆盖层类名必须被剔除（任务里点名的五个 + 实测补的 Win11 两个）。
    #[test]
    fn system_overlay_classes_are_excluded() {
        for class in [
            "Shell_TrayWnd",
            "Shell_SecondaryTrayWnd",
            "WorkerW",
            "Progman",
            "Windows.UI.Core.CoreWindow",
            "IslandWindow",
            "WinUIDesktopWin32WindowClass",
        ] {
            assert!(is_excluded_class(class), "{class} 应被剔除");
            assert!(is_excluded_class(&class.to_lowercase()), "大小写不敏感");
        }
    }

    /// 回归：`EXCLUDED_CLASSES` 是大写混合常量，输入大小写必须两边都归一。
    ///
    /// 最初实现只把输入 `to_ascii_lowercase()` 再 `strip_prefix(常量)`，
    /// 于是 `Progman` / `Shell_TrayWnd` **全部漏过**（Windows 实测才发现，
    /// 因为这条单测当时挂在 `#[cfg(windows)]` 下没跑）。
    #[test]
    fn excluded_class_match_is_case_insensitive_both_sides() {
        // 代表三个大小写形态各异的常量。
        for class in ["Shell_TrayWnd", "Progman", "Windows.UI.Core.CoreWindow"] {
            assert!(is_excluded_class(class), "原样应命中: {class}");
            assert!(
                is_excluded_class(&class.to_lowercase()),
                "小写应命中: {class}"
            );
            assert!(
                is_excluded_class(&class.to_uppercase()),
                "大写应命中: {class}"
            );
        }
    }

    /// 普通窗口类名不能被误伤。
    #[test]
    fn normal_window_classes_are_kept() {
        for class in [
            "Zed::Window",
            "Chrome_WidgetWin_1",
            "ApplicationFrameWindow",
            "CASCADIA_HOSTING_WINDOW_CLASS",
            "Notepad",
            "MozillaWindowClass",
            "HwndWrapper[Code.exe;;guid]",
        ] {
            assert!(!is_excluded_class(class), "{class} 不该被剔除");
        }
    }

    /// 带后缀的 `WorkerW` 变体也要剔除（实测多显示器下存在），
    /// 但 `ProgmanXxx` 这类不同前缀不能误伤。
    #[test]
    fn excluded_class_suffix_handling() {
        assert!(is_excluded_class("WorkerW.Something"));
        assert!(!is_excluded_class("WorkerWindow"));
    }

    /// 过滤链整体行为（任务验收的核心规则）。
    ///
    /// `tool_window` / `app_window` = `WS_EX_TOOLWINDOW` / `WS_EX_APPWINDOW`。
    fn include(
        visible: bool,
        cloaked: bool,
        title: &str,
        class: &str,
        pid: u32,
        tool_window: bool,
        app_window: bool,
    ) -> bool {
        should_include_window(
            visible,
            cloaked,
            title,
            class,
            pid,
            1,
            tool_window,
            app_window,
        )
    }

    #[test]
    fn should_include_window_covers_all_rules() {
        // 正常窗口
        assert!(include(
            true,
            false,
            "main.rs - voke",
            "Zed::Window",
            42,
            false,
            false
        ));
        // 不可见
        assert!(!include(
            false,
            false,
            "hidden",
            "Zed::Window",
            42,
            false,
            false
        ));
        // cloaked（UWP 幽灵）
        assert!(!include(
            true,
            true,
            "Settings",
            "ApplicationFrameWindow",
            42,
            false,
            false
        ));
        // 无标题
        assert!(!include(
            true,
            false,
            "",
            "Chrome_WidgetWin_1",
            42,
            false,
            false
        ));
        assert!(!include(
            true,
            false,
            "   ",
            "Chrome_WidgetWin_1",
            42,
            false,
            false
        ));
        // 本进程自己的窗口（否则 voke 搜到自己）
        assert!(!include(
            true,
            false,
            "voke",
            "Zed::Window",
            1,
            false,
            false
        ));
        // 系统覆盖层
        assert!(!include(
            true,
            false,
            "Program Manager",
            "Progman",
            42,
            false,
            false
        ));
        // IME 候选窗口有这个类、且有非空标题（实测）
        assert!(!include(
            true,
            false,
            "Windows 输入体验",
            "Windows.UI.Core.CoreWindow",
            42,
            false,
            false
        ));
    }

    /// **新增规则**：托盘 / 工具窗口（`WS_EX_TOOLWINDOW`）要剔除，
    /// 但带 `WS_EX_APPWINDOW` 的窗口要留住（应用有意让它上任务栏）。
    ///
    /// 这条盯的是 Zephyr 通知栏托盘被误收进窗口列表的真实问题：托盘窗口
    /// 可见、有标题、类名也不在覆盖层黑名单里，光靠前 5 条过滤挡不住。
    #[test]
    fn tool_windows_are_excluded_unless_app_window_is_set() {
        // 典型托盘窗口：可见、有标题、有效 pid → 但仍要剔除。
        assert!(
            !include(
                true,
                false,
                "Zephyr 通知栏托盘",
                "WindowsForms10.Window.8",
                42,
                true,
                false
            ),
            "WS_EX_TOOLWINDOW 窗口不该进结果"
        );
        // 同时置了 WS_EX_APPWINDOW：应用有意上任务栏，保留。
        assert!(
            include(
                true,
                false,
                "Real App",
                "Chrome_WidgetWin_1",
                42,
                true,
                true
            ),
            "WS_EX_APPWINDOW 压过 TOOLWINDOW"
        );
        // 普通窗口（两个标记都没置）不受影响。
        assert!(include(true, false, "Notepad", "Notepad", 42, false, false));
    }

    /// `WS_EX_TOOLWINDOW` 的位判定本身（32 位 vs 64 位 ex_style 都要对）。
    #[test]
    fn ext_style_bit_test_is_width_agnostic() {
        const TOOL: u32 = 0x0000_0080; // WS_EX_TOOLWINDOW
        assert!(has_ext_style(TOOL as isize, TOOL));
        assert!(has_ext_style(TOOL as isize | 0x0800_0000, TOOL));
        assert!(!has_ext_style(0, TOOL));
        assert!(!has_ext_style(0x0000_0004, TOOL));
        // 高位（64 位扩展样式）不影响低位判定。
        assert!(has_ext_style((TOOL as isize) | (1isize << 40), TOOL));
    }

    /// 进程名从路径取文件名，同时认 `\` 与 `/`（Linux 单测也能跑）。
    #[test]
    fn process_name_is_the_file_name() {
        assert_eq!(process_name_from_path(r"C:\a\Code.exe"), "Code.exe");
        assert_eq!(
            process_name_from_path("C:/a/wezterm-gui.exe"),
            "wezterm-gui.exe"
        );
        assert_eq!(process_name_from_path("Code.exe"), "Code.exe");
        assert_eq!(process_name_from_path(""), "");
    }

    /// `WindowEntry` 默认值：图标为空（app 层填），其余字段中性。
    #[test]
    fn window_entry_default_is_empty() {
        let e = WindowEntry::default();
        assert_eq!(e.hwnd, 0);
        assert!(e.title.is_empty() && e.process_name.is_empty());
        assert!(e.process_path.is_empty() && e.icon.is_empty());
    }
}
