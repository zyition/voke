//! 系统托盘图标（阶段 2.3）。
//!
//! ## 为什么必须在专用线程里跑消息循环
//!
//! `tray-icon` 的 Windows 后端是在**创建它的那条线程**上建一个隐藏窗口
//! （`Shell_NotifyIconW` 的消息目标），并用 `SendMessageW` 跟这个窗口通信。
//! `SendMessageW` 是同步的：目标线程不泵消息就永远不返回。官方文档也明确要求
//! 「Windows 与 Linux/BSD AppIndicator 后端下，事件循环必须跑在同一条线程上」。
//! 所以这里跟 `hotkey.rs` 一样，起一条专用 `std::thread` 跑
//! `GetMessageW` / `TranslateMessage` / `DispatchMessageW`。
//!
//! gpui 主线程有自己的一套事件循环，把托盘塞进去会跟它的窗口消息互相抢，
//! 所以**不**复用主线程。
//!
//! ## 事件如何回到 gpui
//!
//! `TrayIconEvent` / `MenuEvent` 都是 crate 内部的全局 channel，只能在**创建
//! 托盘的那条线程**上取（否则跨线程取也是可以的，但事件对象本身来自该线程的
//! 窗口过程）。因此消息循环每跑一轮就 `try_recv()` 一次，把事件翻译成
//! [`TrayCommand`] 交给调用方传进来的 `on_cmd`。
//!
//! `on_cmd` 是 `Arc<dyn Fn>` 而不是 `Sender`，跟 `hotkey.rs` 的 `OnPress` 保持
//! 一致的风格：sys 层不绑定具体的通道实现，app 层想怎么接到 gpui 主线程都行。
//!
//! ## 平台策略
//!
//! 整个模块 `#[cfg(windows)]`（在 `lib.rs` 里门控），依赖也是
//! `[target.'cfg(windows)'.dependencies]`：Linux 上既不编译也不解析依赖，
//! `cargo test -p voke-sys` 不受影响。

use std::sync::Arc;

use windows::Win32::Foundation::{FALSE, HWND, LPARAM, LRESULT, WPARAM};
use windows::Win32::UI::Shell::{DefSubclassProc, RemoveWindowSubclass, SetWindowSubclass};
use windows::Win32::UI::WindowsAndMessaging::{
    DispatchMessageW, GetMessageW, MSG, TranslateMessage, WM_INITMENUPOPUP,
};

use tray_icon::{
    Icon, MouseButton, MouseButtonState, TrayIcon, TrayIconBuilder, TrayIconEvent,
    menu::{ContextMenu, Menu, MenuEvent, MenuItem, PredefinedMenuItem},
};

/// 托盘图标要传达给 app 层的命令。
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum TrayCommand {
    /// 唤起 / 隐藏搜索面板（与热键同一套 toggle 逻辑）。
    ShowPanel,
    /// 显示面板并直达 awake 插件（右键菜单的「保持唤醒」状态条目，**任何状态都可点**）。
    ///
    /// 未激活时也可点：用户没在保持唤醒时点进来，多半就是想设置保持唤醒，直达插件
    /// 比置灰更顺。文案仍是 `保持唤醒：未激活`。
    OpenAwake,
    /// 打开设置窗口。
    OpenSettings,
    /// 退出进程。
    Quit,
}

/// 托盘 tooltip。
const TOOLTIP: &str = "Voke";

/// 菜单项 id：`MenuEvent` 只带 id，靠它分辨用户点了哪一条。
const ID_AWAKE_STATUS: &str = "voke.awake_status";
const ID_SHOW_PANEL: &str = "voke.show_panel";
const ID_OPEN_SETTINGS: &str = "voke.open_settings";
const ID_QUIT: &str = "voke.quit";

/// 我们在托盘隐藏窗口上挂的子类 id（`SetWindowSubclass` 的 `uIdSubclass`）。
///
/// 同一个 HWND 上可以有多个子类，靠 id 区分，comctl32 会把它们串成链。
/// 必须避开 muda 已占用的 id：muda 0.21 的 `platform_impl/windows/mod.rs` 用
/// `MENU_SUBCLASS_ID = 200` / `SUBMENU_SUBCLASS_ID = 202`（它靠子类化处理菜单
/// 主题与 `MenuEvent`）。3001 与这两个都不同，互不干扰。
const AWAKE_SUBCLASS_ID: usize = 3001;

/// 内嵌的托盘图标（`assets/voke-tray.png`，128²）。
///
/// 编译期内嵌而不是运行时读文件：自启动拉起时工作目录是 `C:\Windows\system32`
/// （见 `paths.rs`），读相对路径必然失败。
///
/// 为什么用专门的 128² 预缩小 PNG，而不是原设计稿或 `voke.ico`：
///
/// - 托盘只吃裸 RGBA，不认多尺寸 ico（`tray_icon::Icon::from_rgba` 要的就是 RGBA
///   缓冲），所以没法用 `voke.ico`；
/// - 原设计稿 `voke-icon-final.png` 是 2048²，每次启动解码 + 缩放都白白吃一次
///   几十 MB 的瞬时分配（见 `main.rs` 里「托盘图标解码前 / 后」两条快照）；
///   128² 留足 HiDPI 余量，解码只有约 65KB。
const DEFAULT_ICON_PNG: &[u8] = include_bytes!("../../assets/voke-tray.png");

/// 把 PNG 字节解码成托盘图标（`Icon::from_rgba` 要求 **RGBA** 顺序）。
///
/// `image` 已经是 voke-sys 的依赖（图标提取用的就是它），不必再加新依赖。
/// 内嵌的图已经是 128²，解码后直接交给 `tray_icon::Icon::from_rgba`——它在
/// Windows 后端内部会自己把 RGBA 转成 GDI 需要的 BGRA
/// （`platform_impl/windows/icon.rs` 的 `convert_to_bgra`），所以这里**只**要按
/// RGBA 给它，不需要再 resize。
pub fn icon_from_png(png: &[u8]) -> Option<Icon> {
    let image = image::load_from_memory(png).ok()?;
    let rgba = image.into_rgba8();
    let (width, height) = rgba.dimensions();
    Icon::from_rgba(rgba.into_raw(), width, height).ok()
}

/// 用内嵌的 `voke-tray.png` 造一个托盘图标；解码失败返回 `None`。
pub fn default_icon() -> Option<Icon> {
    let icon = icon_from_png(DEFAULT_ICON_PNG);
    if icon.is_none() {
        log::error!("内嵌的托盘图标解码失败");
    }
    icon
}

/// 起一条专用线程创建托盘图标并跑消息循环。
///
/// `on_cmd` 在**托盘线程**上被调用，里面不要直接做 UI 操作，只投递事件给
/// gpui 主线程（与 `hotkey::OnPress` 同样的约束）。
///
/// 托盘创建失败（极少见）只记 error 日志并结束线程，不影响主程序。
pub fn spawn_tray(icon: Icon, on_cmd: Arc<dyn Fn(TrayCommand) + Send + Sync>) {
    std::thread::Builder::new()
        .name("voke-tray".into())
        .spawn(move || {
            let Some((menu, awake_item)) = build_menu() else {
                return;
            };
            // 取 popup 的原生 HMENU：菜单随后会被 `with_menu` 移交给 tray-icon。
            // `ContextMenu::hpopupmenu` 在 Windows 下返回该 popup 的句柄（isize），
            // 子类里用它核对 `WM_INITMENUPOPUP` 的 wParam 是不是我们这个菜单。
            let popup = menu.hpopupmenu();

            let tray = TrayIconBuilder::new()
                .with_icon(icon)
                .with_tooltip(TOOLTIP)
                .with_menu(Box::new(menu))
                // 左键**不**弹菜单：左键直接唤起面板更快，菜单留给右键
                // （`menu_on_right_click` 默认为 true）。
                .with_menu_on_left_click(false)
                .build();

            let tray = match tray {
                Ok(tray) => tray,
                Err(err) => {
                    log::error!("创建托盘图标失败：{err}");
                    return;
                }
            };
            log::info!("托盘图标已创建");
            // 托盘线程真正建好托盘后打一条快照（图标解码在 app 层，这里只补
            // 「第二个 Win32 消息循环 + Shell_NotifyIcon + muda 菜单」的常驻增量）。
            crate::log_snapshot("托盘创建完成");

            // `tray` 必须活到线程结束：drop 会销毁图标与它的隐藏窗口。
            let _keep_alive = tray;

            // 在托盘隐藏窗口上挂子类，拦截菜单弹出前的 `WM_INITMENUPOPUP`，用当下的
            // awake 状态刷新条目（替代原先的 30s `SetTimer`）。
            //
            // guard **必须**在 `_keep_alive` 之后声明：线程结束按逆序 drop，先走它
            // 摘子类（此时窗口仍存活），再销毁窗口。
            let _awake_hook = install_awake_subclass(&_keep_alive, awake_item, popup);

            unsafe {
                let mut msg = MSG::default();
                loop {
                    // GetMessageW 返回 <= 0 表示退出或出错。
                    if GetMessageW(&mut msg, None, 0, 0).0 <= 0 {
                        break;
                    }
                    let _ = TranslateMessage(&msg);
                    DispatchMessageW(&msg);

                    // 每派发完一条消息就把这一轮攒下的事件转成命令。
                    pump(&on_cmd);
                }
            }
        })
        .expect("创建托盘线程失败");
}

/// 挂在托盘隐藏窗口上的子类所携带的状态（`dwrefdata` 指向它）。
///
/// 只在托盘线程上访问（`SetWindowSubclass` 的注册与回调同线程），无需 `Send`/`Sync`。
struct AwakeMenuHook {
    /// awake 状态条目（`Clone` 共享平台状态），刷新时对它调 `set_text`。
    awake_item: MenuItem,
    /// 我们那个 popup 的 HMENU（`ContextMenu::hpopupmenu()` 的返回值），
    /// 用来核对 `WM_INITMENUPOPUP` 的 `wParam` 是否为本菜单。
    popup: isize,
}

/// [`AwakeMenuHook`] 的所有者：drop 时先摘子类、再释放 hook，保证回调不会踩到
/// 已释放的内存。
///
/// hook 放堆上（`Box`）：`dwrefdata` 只需要一个稳定地址，堆地址在 `Box` 存活
/// 期间不随结构体移动而变。
struct AwakeSubclassGuard {
    hwnd: HWND,
    _data: Box<AwakeMenuHook>,
}

impl Drop for AwakeSubclassGuard {
    fn drop(&mut self) {
        unsafe {
            // 先摘子类（此刻窗口仍存活），字段随后自动 drop 释放 hook。
            // 返回值只是 BOOL 成功标记，摘不掉也无所谓（窗口即将销毁）。
            let _ = RemoveWindowSubclass(self.hwnd, Some(awake_subclass_proc), AWAKE_SUBCLASS_ID);
        }
    }
}

/// 在托盘隐藏窗口上挂子类，用菜单弹出事件驱动 awake 条目刷新；失败返回 `None`。
///
/// `hwnd` 取 `TrayIcon::window_handle()`（tray-icon 0.26 在 Windows 下的公开 API，
/// 返回 windows-sys 的裸 `HWND`，即 `*mut c_void`）。托盘右键菜单就是在这个窗口的
/// WndProc 里 `TrackPopupMenu` 弹出的（见 `tray-icon-0.26.1/src/platform_impl/
/// windows/mod.rs` 的 `show_tray_menu`），所以 `WM_INITMENUPOPUP` 会发到这个
/// hwnd，子类能截到。
fn install_awake_subclass(
    tray: &TrayIcon,
    awake_item: MenuItem,
    popup: isize,
) -> Option<AwakeSubclassGuard> {
    // windows-sys 的 `HWND` 是 `*mut c_void`，这里包成 windows crate 的 `HWND`
    // 新类型（两者底层同一个指针）。
    let hwnd = HWND(tray.window_handle());
    let data = Box::new(AwakeMenuHook { awake_item, popup });
    // `&*data` 取的是 Box 内容而非 Box 本身，其地址即子类的 dwrefdata。
    let dwrefdata = std::ptr::from_ref(&*data) as usize;

    let ok = unsafe {
        SetWindowSubclass(
            hwnd,
            Some(awake_subclass_proc),
            AWAKE_SUBCLASS_ID,
            dwrefdata,
        )
    };
    if ok == FALSE {
        log::warn!("SetWindowSubclass 失败：托盘 awake 条目不会在打开菜单时刷新");
        return None;
    }
    log::debug!("已在托盘窗口挂 awake 刷新子类（id={AWAKE_SUBCLASS_ID}）");
    Some(AwakeSubclassGuard { hwnd, _data: data })
}

/// 托盘窗口的菜单刷新子类：菜单（含 popup）显示前刷新 awake 条目文案。
///
/// `WM_INITMENUPOPUP` 是微软文档建议的「菜单显示前修改菜单项」时机
/// （KB242577）：`TrackPopupMenu` 在显示前把它发给 owner 窗口，此时改文案会立即
/// 反映到即将弹出的菜单上。`wParam` 是待显示菜单的 HMENU——我们的托盘菜单只有
/// 一个 popup，但仍核对一次，避免将来加了子菜单被误刷。
///
/// 其余消息一律原样交给 `DefSubclassProc`（同窗口上还有 muda 的子类 200/202，
/// 必须沿链转发）。
unsafe extern "system" fn awake_subclass_proc(
    hwnd: HWND,
    msg: u32,
    wparam: WPARAM,
    lparam: LPARAM,
    _uidsubclass: usize,
    dwrefdata: usize,
) -> LRESULT {
    if msg == WM_INITMENUPOPUP {
        // SAFETY：dwrefdata 是 install 时登记的 `&AwakeMenuHook`，guard 保证回调
        // 期间它始终存活（摘子类在释放 hook 之前）。
        let hook = unsafe { &*(dwrefdata as *const AwakeMenuHook) };
        if wparam.0 as isize == hook.popup {
            let label = refresh_awake_item(&hook.awake_item);
            log::info!("托盘菜单打开：awake 条目已刷新为「{label}」");
        }
    }
    // SAFETY：参数由系统按 SUBCLASSPROC 约定传入，原样转发。
    unsafe { DefSubclassProc(hwnd, msg, wparam, lparam) }
}

/// 构造右键菜单：awake 状态条目 / 显示面板 / 设置 / 分隔线 / 退出 voke。
///
/// 第一项是 awake 状态快照（文案见 [`crate::awake::menu_label`]）：**任何状态都可点**，
/// 点了经 [`TrayCommand::OpenAwake`] 直达插件（未激活时就是去设置保持唤醒）。
/// 它是构建时的静态快照，之后每次菜单弹出前由子类（[`install_awake_subclass`]）
/// 按当下状态刷新。
///
/// 返回值带上状态条目本身：它是 `Clone` 且共享平台状态，刷新时直接对这份句柄
/// 调 `set_text`（muda 的 Windows 实现在内部对该项的父菜单调 `SetMenuItemInfoW`，
/// 见 muda 0.21 `platform_impl/windows`）。
///
/// 失败（muda 建菜单出错）只记 error 并返回 `None`，调用方跳过建托盘。
fn build_menu() -> Option<(Menu, MenuItem)> {
    let menu = Menu::new();
    let text = awake_item_state();
    let awake_item = MenuItem::with_id(ID_AWAKE_STATUS, text, true, None);
    let result = menu
        .append(&awake_item)
        .and_then(|()| menu.append(&MenuItem::with_id(ID_SHOW_PANEL, "显示面板", true, None)))
        .and_then(|()| menu.append(&MenuItem::with_id(ID_OPEN_SETTINGS, "设置", true, None)))
        .and_then(|()| menu.append(&PredefinedMenuItem::separator()))
        .and_then(|()| menu.append(&MenuItem::with_id(ID_QUIT, "退出 Voke", true, None)));
    match result {
        Ok(()) => Some((menu, awake_item)),
        Err(err) => {
            log::error!("构造托盘菜单失败：{err}");
            None
        }
    }
}

/// 读一次 awake 状态并给出状态条目文案（未激活 → `保持唤醒：未激活`）。
///
/// 条目始终可点，所以只返回文案，没有"是否 enabled"这一维。
fn awake_item_state() -> String {
    let status = crate::awake::current();
    crate::awake::menu_label(status, crate::awake::now_secs())
}

/// 读当前 awake 状态并刷新状态条目的文案，返回本次刷新后的实际文案。
///
/// 只有真的变了才调 Win32（`text()` 会读回平台状态），避免每次弹菜单都无谓地
/// `SetMenuItemInfoW`。条目恒为可点，不再碰 enabled。
fn refresh_awake_item(item: &MenuItem) -> String {
    let text = awake_item_state();
    if item.text() != text {
        item.set_text(&text);
    }
    text
}

/// 把 channel 里攒下的 `TrayIconEvent` / `MenuEvent` 转成 [`TrayCommand`]。
fn pump(on_cmd: &Arc<dyn Fn(TrayCommand) + Send + Sync>) {
    while let Ok(event) = MenuEvent::receiver().try_recv() {
        let id = event.id.as_ref();
        match id {
            ID_AWAKE_STATUS => on_cmd(TrayCommand::OpenAwake),
            ID_SHOW_PANEL => on_cmd(TrayCommand::ShowPanel),
            ID_OPEN_SETTINGS => on_cmd(TrayCommand::OpenSettings),
            ID_QUIT => on_cmd(TrayCommand::Quit),
            _ => log::debug!("托盘菜单：未识别的 id {id}"),
        }
    }

    while let Ok(event) = TrayIconEvent::receiver().try_recv() {
        // 左键单击（抬起时）唤起面板；右键留给菜单，中键忽略。
        if let TrayIconEvent::Click {
            button: MouseButton::Left,
            button_state: MouseButtonState::Up,
            ..
        } = event
        {
            on_cmd(TrayCommand::ShowPanel);
        }
    }
}
