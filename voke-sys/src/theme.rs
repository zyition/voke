//! 系统主题（深色 / 浅色）读取与变化监听（深色模式支持，阶段 4）。
//!
//! ## 读什么
//!
//! Windows 把「应用使用浅色主题」记在
//! `HKCU\Software\Microsoft\Windows\CurrentVersion\Themes\Personalize` 下的
//! `AppsUseLightTheme`（`REG_DWORD`）：`0` = 深色，`1` = 浅色。注册表键在部分精简
//! 系统 / 旧版本上可能不存在，此时按浅色处理（返回 `false`）。
//!
//! ## 变化监听为什么选「注册表变更通知」而不是 `WM_SETTINGCHANGE`
//!
//! 系统改主题时会对所有顶层窗口广播 `WM_SETTINGCHANGE`（`lParam` 指向
//! `"ImmersiveColorSet"`），看似可以直接收这条消息。但**消息窗口（message-only
//! window）收不到广播消息**——这是 Win32 的明确规定：`HWND_MESSAGE` 子窗口不接收
//! `SendMessage(HWND_BROADCAST, ...)`。要收到广播就得建一个**真正的顶层窗口**
//! （即便不可见），需要 `RegisterClassW` + `CreateWindowExW` + 一个静态 `WNDPROC`，
//! 在这条已经有两条消息循环线程（热键 / 托盘）的进程里再引入一套窗口类与回调注册，
//! 复杂度和出错面都更高。
//!
//! ## 为什么是 `RegNotifyChangeKeyValue` 而不是轮询
//!
//! 主题值就写在注册表里，所以可以直接让系统来通知：
//! `RegNotifyChangeKeyValue` + `WaitForSingleObject` **全程阻塞在内核等待上**，
//! 系统不改主题时这条线程一个定时器都不跑——常驻 launcher 的空闲唤醒是续航
//! 敏感项，1.5s 轮询意味着 40 次/分、2400 次/时的无谓唤醒，换成事件驱动后为 0。
//!
//! 通知是**一次性**的：每轮等完都要重新注册（这正是"不会漏掉第二次切换"的保证）。
//! 只有键打不开（精简系统没有 `Themes\Personalize`）或注册失败时，才退化为
//! [`RETRY_INTERVAL`]（30s）的低频重试。
//!
//! 只在**值真的变了**时才回调，避免刷屏。
//!
//! ## 平台策略
//!
//! `AppsUseLightTheme` 的 DWORD → 「是否深色」的映射是纯函数，任何平台都能编译和
//! 测试；真正读注册表与跑监听线程的部分 `#[cfg(windows)]`（另有非 Windows 的空实现，
//! 让上层无需 cfg 分支）。`cargo test -p voke-sys`（Linux）只覆盖纯函数那部分。

use std::sync::Arc;

/// 系统主题变化时的回调。
///
/// 与 `hotkey::OnPress` / `tray` 的回调同风格：sys 层不绑定具体通道实现，
/// app 层自己决定怎么把事件接到 gpui 主线程（在回调里**不要**直接做 UI 操作）。
pub type OnThemeChange = Arc<dyn Fn() + Send + Sync>;

/// 退化路径的重试间隔：键打不开 / 变更通知注册不上时用。
///
/// 见模块文档「为什么是 `RegNotifyChangeKeyValue` 而不是轮询」：正常路径零轮询，
/// 这里只在退化状态下才睡，而且比原来的 1.5s 慢 20 倍（30s）。
#[cfg(windows)]
const RETRY_INTERVAL: std::time::Duration = std::time::Duration::from_secs(30);

/// 注册表键（相对 `HKEY_CURRENT_USER`）。
#[cfg(windows)]
const PERSONALIZE_KEY: &str = "Software\\Microsoft\\Windows\\CurrentVersion\\Themes\\Personalize";

/// 值名：`0` = 深色，`1` = 浅色。
#[cfg(windows)]
const VALUE_NAME: &str = "AppsUseLightTheme";

/// `AppsUseLightTheme` 的 DWORD → 系统是否处于深色模式。
///
/// Windows 约定：`0` = 深色，`1` = 浅色。理论上不会出现的其它值按浅色处理。
/// 纯函数，任何平台可测。
pub fn light_value_is_dark(apps_use_light_theme: u32) -> bool {
    apps_use_light_theme == 0
}

/// 系统当前是否为深色模式。
///
/// 注册表键 / 值缺失或读取失败都按浅色处理（返回 `false`），**不 panic**——
/// 主题读取失败不该拖垮启动。
#[cfg(windows)]
pub fn is_system_dark() -> bool {
    match open_personalize_key() {
        Some(key) => read_light_theme_value(key.0).is_some_and(light_value_is_dark),
        None => false,
    }
}

/// 非 Windows 构建没有系统深色主题概念，恒为浅色（`false`）。
#[cfg(not(windows))]
pub fn is_system_dark() -> bool {
    false
}

/// RAII 的注册表键句柄：离开作用域 `RegCloseKey`。
///
/// 有了它就不用在每个 early return 分支上手动调 `RegCloseKey`。
#[cfg(windows)]
struct RegistryKey(windows::Win32::System::Registry::HKEY);

#[cfg(windows)]
impl Drop for RegistryKey {
    fn drop(&mut self) {
        use windows::Win32::System::Registry::RegCloseKey;
        unsafe {
            let _ = RegCloseKey(self.0);
        }
    }
}

/// 打开 `Personalize` 键；键不存在 / 打不开返回 `None`。
#[cfg(windows)]
fn open_personalize_key() -> Option<RegistryKey> {
    use windows::Win32::Foundation::ERROR_SUCCESS;
    use windows::Win32::System::Registry::{HKEY, HKEY_CURRENT_USER, KEY_READ, RegOpenKeyExW};
    use windows::core::HSTRING;

    let subkey = HSTRING::from(PERSONALIZE_KEY);
    let mut key = HKEY(std::ptr::null_mut());
    let ret = unsafe { RegOpenKeyExW(HKEY_CURRENT_USER, &subkey, None, KEY_READ, &mut key) };
    if ret != ERROR_SUCCESS {
        // 键缺失是正常情况（部分精简系统 / 旧版本没有 Themes\Personalize）：
        // 记 debug 而不是 error，免得退化重试时每次都刷一条错误。
        log::debug!("打开注册表 {PERSONALIZE_KEY} 失败：{ret:?}");
        return None;
    }
    // KEY_READ 已含 KEY_NOTIFY（0x20019 = READ | QUERY_VALUE | ENUM_SUB_KEYS |
    // NOTIFY），同一个句柄既能读值也能挂变更通知。
    Some(RegistryKey(key))
}

/// 读 `AppsUseLightTheme` 的 DWORD 值；值缺失或类型异常返回 `None`。
///
/// 键由 [`open_personalize_key`] 打开并负责关闭，这里只查询。
#[cfg(windows)]
fn read_light_theme_value(key: windows::Win32::System::Registry::HKEY) -> Option<u32> {
    use windows::Win32::Foundation::ERROR_SUCCESS;
    use windows::Win32::System::Registry::{REG_DWORD, RegQueryValueExW};
    use windows::core::HSTRING;

    let name = HSTRING::from(VALUE_NAME);
    let mut value_type = REG_DWORD;
    let mut data = [0u8; 4];
    let mut size: u32 = data.len() as u32;
    let ret = unsafe {
        RegQueryValueExW(
            key,
            &name,
            None,
            Some(&mut value_type),
            Some(data.as_mut_ptr()),
            Some(&mut size),
        )
    };
    if ret != ERROR_SUCCESS {
        log::debug!("读取注册表值 {VALUE_NAME} 失败：{ret:?}");
        return None;
    }
    if value_type != REG_DWORD || size < 4 {
        log::warn!(
            "注册表值 {VALUE_NAME} 类型/长度异常（type={:#x}, size={size}），按浅色处理",
            value_type.0
        );
        return None;
    }
    Some(u32::from_ne_bytes(data))
}

/// 阻塞等 `Personalize` 键的下一次变化。
///
/// 返回 `true` = 等到（或通知已发生）；`false` = 键打不开 / 建事件或注册通知
/// 失败，调用方退化为 [`RETRY_INTERVAL`] 低频重试。
///
/// **零空闲唤醒**：成功路径全程挂在 `WaitForSingleObject(INFINITE)` 上，
/// 系统不改主题，这条线程就一个定时器都不跑。
#[cfg(windows)]
fn wait_for_change() -> bool {
    use windows::Win32::Foundation::{CloseHandle, ERROR_SUCCESS, WAIT_OBJECT_0};
    use windows::Win32::System::Registry::{REG_NOTIFY_CHANGE_LAST_SET, RegNotifyChangeKeyValue};
    use windows::Win32::System::Threading::{CreateEventW, INFINITE, WaitForSingleObject};
    use windows::core::PCWSTR;

    let Some(key) = open_personalize_key() else {
        return false;
    };
    // 匿名、自动复位事件。`RegNotifyChangeKeyValue` 是**一次性**的（等完就失效，
    // 必须重新注册），所以每轮建一个、用完即关最省心，也省掉手动 ResetEvent。
    let event = match unsafe { CreateEventW(None, false, false, PCWSTR::null()) } {
        Ok(handle) => handle,
        Err(err) => {
            log::warn!("创建主题监听事件失败：{err}");
            return false;
        }
    };
    // SAFETY：key 是本函数持有的有效 HKEY（KEY_READ 含 KEY_NOTIFY），
    // event 是刚创建的合法句柄；两个句柄都在本函数内关闭。
    let registered = unsafe {
        RegNotifyChangeKeyValue(key.0, true, REG_NOTIFY_CHANGE_LAST_SET, Some(event), true)
    };
    if registered != ERROR_SUCCESS {
        log::debug!("注册注册表变更通知失败：{registered:?}");
        unsafe {
            let _ = CloseHandle(event);
        }
        return false;
    }
    let waited = unsafe { WaitForSingleObject(event, INFINITE) };
    unsafe {
        let _ = CloseHandle(event);
    }
    waited == WAIT_OBJECT_0
}

/// 起一条专用线程监听系统主题，**值变化时**调用 `on_change`。
///
/// `on_change` 在监听线程上被调用，里面只应投递事件（见 [`OnThemeChange`]）。
/// 线程随进程退出而结束，不需要显式停止（与热键 / 托盘线程同策略）。
#[cfg(windows)]
pub fn spawn_theme_watcher(on_change: OnThemeChange) {
    std::thread::Builder::new()
        .name("voke-theme-watch".into())
        .spawn(move || {
            // 先记下当前值，之后只在真的翻转时回调。
            let mut last = is_system_dark();
            loop {
                // 事件驱动（零轮询）；注册不上才退化成 30s 一次的低频重试。
                if !wait_for_change() {
                    std::thread::sleep(RETRY_INTERVAL);
                }
                let now = is_system_dark();
                if now != last {
                    last = now;
                    log::info!("系统主题变化：{}", if now { "深色" } else { "浅色" });
                    on_change();
                }
            }
        })
        .expect("创建系统主题监听线程失败");
}

/// 非 Windows 构建：没有系统主题变化可监听，空实现。
#[cfg(not(windows))]
pub fn spawn_theme_watcher(_on_change: OnThemeChange) {}

#[cfg(test)]
mod tests {
    use super::*;

    /// `0` = 深色，`1` = 浅色；其它值按浅色。
    #[test]
    fn light_value_maps_to_dark_only_for_zero() {
        assert!(light_value_is_dark(0), "0 应为深色");
        assert!(!light_value_is_dark(1), "1 应为浅色");
        assert!(!light_value_is_dark(2), "未知值按浅色");
        assert!(!light_value_is_dark(u32::MAX), "未知值按浅色");
    }

    /// 非 Windows 上恒为浅色（`is_system_dark` 的空实现）。
    #[cfg(not(windows))]
    #[test]
    fn non_windows_is_never_dark() {
        assert!(!is_system_dark());
    }
}
