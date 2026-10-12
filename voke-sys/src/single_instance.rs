//! 单实例保护：二次启动只激活已有实例，不再起第二个进程。
//!
//! ## 为什么用命名内核对象
//!
//! 拉取器（launcher）的常见体验是「重复双击图标 = 把已经开着的面板叫出来」。这
//! 里用一对内核对象实现：
//!
//! - **命名互斥体** `Local\voke.instance`：进程启动时 `CreateMutexW` 创建/打开。
//!   若 `GetLastError == ERROR_ALREADY_EXISTS` 说明**已有实例在跑**（见下面是
//!   「判定方式」一节的细节）。
//! - **命名自动重置事件** `Local\voke.show`：首实例创建并**循环等待**它；二次
//!   实例打开它 `SetEvent` 一下即退出。
//!
//! ## `Local\` 前缀的语义
//!
//! 名字带 `Local\` 前缀时，内核对象位于**当前登录会话**（logon session）的命名
//! 空间里：同一台机器上不同会话（例如控制台登录 + 一个 RDP 会话）互相看不见对方
//! 的 `Local\` 对象，因此**每个会话各自跑一份 voke**——这正是一个桌面 launcher
//! 想要的（不同用户/会话的窗口本就互不可见，`Global\` 反而会跨会话误判「已有实例」
//! 并让第二个会话的 voke 起不来）。不带前缀时默认也落在 `Local\`，但显式写出更
//! 清楚（MSDN：`Local\` 与无前缀等价）。
//!
//! ## `ERROR_ALREADY_EXISTS` 在 windows crate 0.62 里怎么判
//!
//! 0.62.2 的 `CreateMutexW` 返回 `windows_core::Result<HANDLE>`，实现是
//! `(!result.is_invalid()).then_some(result).ok_or_else(Error::from_thread)`
//! （见 registry 源码 `Win32/System/Threading/mod.rs:301`）——句柄合法即 `Ok`，
//! **成功路径不会清 last error**。而 `CreateMutexW` 在「对象已存在」时仍然返回
//! 合法句柄、只是把线程 last error 置成 `ERROR_ALREADY_EXISTS`(183)，所以：
//! `Ok(handle)` 之后**紧接着**（中间不夹任何 Win32 调用）读一次 `GetLastError()`，
//! 等于 `ERROR_ALREADY_EXISTS` 就是「已有实例」。
//!
//! ## 平台策略
//!
//! 整个 Win32 实现 `#[cfg(windows)]`；非 Windows 上 [`single_instance_guard`]
//! 直接返回「首实例」、[`ShowSignal::spawn_waiter`] 是空实现，这样 app 层的接线
//! 代码不需要额外的 cfg 分支，`cargo test -p voke-sys`（Linux native）也不受影响。

use std::sync::Arc;

/// 二次启动触发时的回调（与 [`crate::hotkey::OnPress`] 同风格：sys 层不绑定具体通道）。
pub type OnShow = Arc<dyn Fn() + Send + Sync>;

/// 首实例名下的互斥体名（`Local\` = 当前登录会话，见模块文档）。
#[cfg(windows)]
const INSTANCE_MUTEX_NAME: &str = "Local\\voke.instance";

/// 二次启动唤醒已有实例的事件名（自动重置事件）。
#[cfg(windows)]
const SHOW_EVENT_NAME: &str = "Local\\voke.show";

/// 单实例保护的结果。
pub enum SingleInstance {
    /// 本进程是唯一实例，内含「被二次启动时会触发」的信号句柄。
    Primary(ShowSignal),
    /// 已有实例在运行：本进程已向它发出「显示面板」信号，应尽快退出。
    AlreadyRunning,
}

/// 首实例持有的「二次启动」信号句柄（`Local\voke.show` 自动重置事件）。
///
/// 句柄**故意不关**（进程生命周期持有，见模块文档）：它是首实例「可被唤醒」的
/// 凭据，提前关闭会让后续二次启动扑空。
pub struct ShowSignal {
    /// 事件 `HANDLE` 的裸值。
    ///
    /// 存 `isize` 而不是 `HANDLE`：windows crate 的 `HANDLE` 是 `*mut c_void`
    /// 新类型、**没有** `Send`，直接捕获进等待线程会编译不过（`hotkey.rs` 里
    /// 也是让线程从 `None` 起 HWND、不跨线程搬句柄）。裸值数字是 `Send` 的，
    /// 线程里再重建 `HANDLE`。`0` 表示「没有可用句柄」（创建失败时的降级）。
    #[cfg(windows)]
    handle: isize,
}

impl ShowSignal {
    /// 起一条线程**循环**等待信号，每次收到调一次 `on_show`（支持多次二次启动）。
    ///
    /// `on_show` 在等待线程上被调用，里面不要做 UI 操作，只投递事件（与托盘 /
    /// 热键回调同一约束）。
    pub fn spawn_waiter(&self, on_show: OnShow) {
        #[cfg(windows)]
        {
            use windows::Win32::Foundation::{HANDLE, WAIT_OBJECT_0};
            use windows::Win32::System::Threading::{INFINITE, WaitForSingleObject};

            let raw = self.handle;
            if raw == 0 {
                log::warn!("没有单实例信号句柄，跳过二次启动监听");
                return;
            }
            std::thread::Builder::new()
                .name("voke-single-instance".into())
                .spawn(move || {
                    // SAFETY：raw 来自 CreateEventW 返回的合法句柄，且首实例全程
                    // 不关闭它（见 ShowSignal 文档），故此处重建的 HANDLE 始终有效。
                    let event = HANDLE(raw as *mut core::ffi::c_void);
                    loop {
                        let waited = unsafe { WaitForSingleObject(event, INFINITE) };
                        if waited != WAIT_OBJECT_0 {
                            log::error!("单实例等待异常中止：status={}", waited.0);
                            break;
                        }
                        on_show();
                    }
                })
                .expect("创建单实例监听线程失败");
        }
        #[cfg(not(windows))]
        {
            let _ = on_show;
        }
    }
}

/// 取单实例保护（进程启动时调用一次）。
///
/// 返回 [`SingleInstance::Primary`] 表示本进程是首实例（后续可
/// [`ShowSignal::spawn_waiter`]）；返回 [`SingleInstance::AlreadyRunning`] 表示
/// 已有实例在跑、信号已发出，调用方应尽快退出。
///
/// 互斥体 / 事件的创建失败都**不**阻断启动：降级为「当作首实例、但没有二次启动
/// 监听」，宁可少一个便利功能，也不能让 launcher 起不来。
pub fn single_instance_guard() -> SingleInstance {
    #[cfg(windows)]
    {
        use windows::Win32::Foundation::{CloseHandle, ERROR_ALREADY_EXISTS, GetLastError};
        use windows::Win32::System::Threading::{CreateEventW, CreateMutexW};
        use windows::core::HSTRING;

        // SAFETY：均为普通内核对象创建调用；名字是进程内局部、调用期间存活。
        unsafe {
            let name = HSTRING::from(INSTANCE_MUTEX_NAME);
            let mutex = match CreateMutexW(None, false, &name) {
                Ok(handle) => handle,
                Err(err) => {
                    log::error!("创建单实例互斥体失败：{err}（跳过单实例保护）");
                    return SingleInstance::Primary(ShowSignal { handle: 0 });
                }
            };

            // 判定放在 CreateMutexW 之后**紧接着**做，中间不夹别的 Win32 调用
            // （否则 last error 会被覆盖，见模块文档）。
            if GetLastError() == ERROR_ALREADY_EXISTS {
                // 已有实例：唤醒它后退出。本进程的互斥体句柄用不到了，顺手关掉
                // （对象仍由首实例的句柄保活，关它不影响首实例）。
                signal_existing_instance();
                let _ = CloseHandle(mutex);
                return SingleInstance::AlreadyRunning;
            }

            // 首个实例：创建自动重置事件（bmanualreset=false, binitialstate=false）。
            let event_name = HSTRING::from(SHOW_EVENT_NAME);
            let handle = match CreateEventW(None, false, false, &event_name) {
                Ok(event) => event.0 as isize,
                Err(err) => {
                    log::error!("创建单实例信号事件失败：{err}（二次启动将无法唤醒）");
                    0
                }
            };
            // 互斥体句柄必须活到进程结束，否则对象被销毁、后来的进程会误判成首实例。
            // `HANDLE` 是无 `Drop` 的裸 newtype：绑定后就地保留即可，作用域结束不会
            // 调 `CloseHandle`，句柄一直有效到进程退出。
            let _mutex_keep_alive = mutex;
            SingleInstance::Primary(ShowSignal { handle })
        }
    }
    #[cfg(not(windows))]
    {
        SingleInstance::Primary(ShowSignal {})
    }
}

/// 打开 `Local\voke.show` 并 `SetEvent`，唤醒已有实例。
///
/// 事件理论上由首实例在启动早期创建，但存在极窄的竞态（二次启动恰好插在首实例
/// `CreateMutexW` 与 `CreateEventW` 之间）：此时 `OpenEventW` 会失败，故带一个
/// 有上限的重试（50 × 20ms ≈ 1s），超时才放弃。
#[cfg(windows)]
fn signal_existing_instance() {
    use std::time::Duration;
    use windows::Win32::Foundation::CloseHandle;
    use windows::Win32::System::Threading::{EVENT_MODIFY_STATE, OpenEventW, SetEvent};
    use windows::core::HSTRING;

    let name = HSTRING::from(SHOW_EVENT_NAME);
    for attempt in 0..50 {
        // SAFETY：均为普通句柄操作；name 是进程内局部、调用期间存活。
        match unsafe { OpenEventW(EVENT_MODIFY_STATE, false, &name) } {
            Ok(event) => {
                // SetEvent 需要 EVENT_MODIFY_STATE 权限，OpenEventW 已按它打开。
                let _ = unsafe { SetEvent(event) };
                let _ = unsafe { CloseHandle(event) };
                return;
            }
            Err(err) => {
                if attempt == 49 {
                    log::error!("打开二次启动信号事件失败，无法唤醒已有实例：{err}");
                    return;
                }
                std::thread::sleep(Duration::from_millis(20));
            }
        }
    }
}
