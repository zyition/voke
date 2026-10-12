//! 全局热键：RegisterHotKey 在一个专用线程里注册并跑消息循环（§5）。
//!
//! 设计要点：
//! - `RegisterHotKey` 要求注册线程有一个消息循环才能收到 `WM_HOTKEY`，
//!   所以这里起一条 OS 线程常驻 `GetMessageW`。
//! - 热键触发后通过回调把事件送回 gpui 主线程（主线程不阻塞）。
//! - `SetThreadExecutionState` 是 per-thread 语义，同理也需要常驻线程（awake，M4）。
//!
//! ## 可控热键（阶段 2）
//!
//! 原来只能注册一次、注册完就不能改。设置页要能改键，所以现在
//! [`spawn_hotkey`] 返回一个 [`HotkeyHandle`]：调用方拿到它之后随时可以
//! [`HotkeyHandle::set`] 换键。
//!
//! 为什么改键要走「写请求 + `PostThreadMessageW`」两步，而不是让调用方
//! 直接 `RegisterHotKey`：`RegisterHotKey` 是**线程绑定**的——`WM_HOTKEY` 只会
//! 投递到注册它的那个线程的消息队列里，别的线程注册的同 id 热键拿不到消息。
//! 所以必须由热键线程自己执行注销/注册，其它线程只能通过消息通知它。
//!
//! ## 抑制与注册回执（阶段 6：修复录制热键体验）
//!
//! 设置页录制热键时，若旧热键还注册着，用户按到旧组合键会当场触发旧动作。
//! 为此 [`HotkeyHandle`] 多了两件事：
//! - [`HotkeyHandle::suppress`]：注销当前热键并保持注销态（**阻塞**到热键线程
//!   确认已 `UnregisterHotKey`），录制期间按任何键都只进录制器；
//! - [`HotkeyHandle::try_register`]：注册新热键并**等待回执**（成功/失败），
//!   失败好让 app 层回退到原热键并给出提示。
//!
//! M0 阶段的热键刻意避开 Alt+Space：PowerToys Run 很可能还占着它，热键注册
//! 会失败并掩盖真正要验证的东西（窗口显隐链路）。等正式接管 PTR 位置时再换回。

use std::sync::{Arc, Mutex, mpsc};
use std::time::Duration;

use windows::Win32::Foundation::{HWND, LPARAM, WPARAM};
use windows::Win32::System::Threading::GetCurrentThreadId;
use windows::Win32::UI::Input::KeyboardAndMouse::{
    HOT_KEY_MODIFIERS, MOD_ALT, MOD_CONTROL, MOD_SHIFT, MOD_WIN, RegisterHotKey, UnregisterHotKey,
    VIRTUAL_KEY, VK_0, VK_A, VK_BACK, VK_DELETE, VK_DOWN, VK_END, VK_ESCAPE, VK_F1, VK_HOME,
    VK_INSERT, VK_LEFT, VK_NEXT, VK_OEM_1, VK_OEM_2, VK_OEM_3, VK_OEM_4, VK_OEM_5, VK_OEM_6,
    VK_OEM_7, VK_OEM_COMMA, VK_OEM_MINUS, VK_OEM_PERIOD, VK_OEM_PLUS, VK_PRIOR, VK_RETURN,
    VK_RIGHT, VK_SPACE, VK_TAB, VK_UP,
};
use windows::Win32::UI::WindowsAndMessaging::{
    DispatchMessageW, GetMessageW, MSG, PostThreadMessageW, TranslateMessage, WM_HOTKEY, WM_QUIT,
    WM_USER,
};

/// 热键修饰键组合。
#[derive(Clone, Copy, Debug)]
pub struct Hotkey {
    /// `HOT_KEY_MODIFIERS`，例如 `MOD_ALT | MOD_CONTROL`。
    pub modifiers: HOT_KEY_MODIFIERS,
    /// 虚拟键码，例如 `VK_SPACE`。
    pub vk: VIRTUAL_KEY,
}

/// 热键被按下时的回调。
///
/// 用回调而不是 `Sender`，是为了让 sys 层不绑定具体的通道实现：
/// app 层可以用 async-channel、mpsc 或任何方式把事件接到 gpui 主线程。
pub type OnPress = Arc<dyn Fn() + Send + Sync>;

/// 注册用的热键 id，一个线程只注册一个，固定即可。
pub const HOTKEY_ID: i32 = 1;

/// 自定义线程消息：热键线程收到它就按 `shared` 里的最新请求执行（注册 / 抑制）。
///
/// 用 `WM_USER + 1`（1025）这个区间是安全的：我们只往**自己**的线程消息队列
/// 投递，`WM_USER..WM_APP` 这一段归线程私有窗口类使用，不会撞系统消息。
const WM_APP_RECONFIG: u32 = WM_USER + 1;

/// 等待热键线程回执的超时。
///
/// 热键线程只做 `RegisterHotKey` / `UnregisterHotKey` 这类毫秒级 user32 调用，
/// 正常远快于这个上限；设超时只是兜底：万一热键线程异常（例如被卡住），
/// 也不能把调用它的 gpui 主线程永久挂死。
const REPLY_TIMEOUT: Duration = Duration::from_secs(2);

/// 交给热键线程执行的一次请求。
///
/// `Copy`：热键线程取请求时直接按值拷贝，不必把「取走」与「执行」两步拆开。
#[derive(Clone, Copy)]
enum Request {
    /// 注册这个热键（会先注销旧的，再注册新的）。
    Register(Hotkey),
    /// 注销当前热键并**不**再注册（抑制态）。录制热键期间用它，保证按键不触发旧动作。
    Suppress,
}

/// 跨线程共享的「当前请求 + 回执通道」。
///
/// 调用线程写、热键线程读；`reply` 是可选的一次性回执（`Sender<bool>`，成功 true /
/// 失败 false），只有需要知道结果的调用（`suppress` / `try_register`）才带上。
struct Shared {
    request: Request,
    reply: Option<mpsc::Sender<bool>>,
}

/// 已启动的热键线程的句柄。
///
/// `thread_id` 是 `PostThreadMessageW` 的目标；`shared` 是「下一个要执行的请求」，
/// 由调用线程写、热键线程读。
pub struct HotkeyHandle {
    thread_id: u32,
    shared: Arc<Mutex<Shared>>,
}

impl HotkeyHandle {
    /// 换键（非阻塞，不等待结果）。
    ///
    /// 写完请求就 `PostThreadMessageW` 通知热键线程，真正的注销/注册在热键线程上做，
    /// 主线程不阻塞；注册失败只会在热键线程记 error 日志。需要知道成败时用
    /// [`HotkeyHandle::try_register`]。
    pub fn set(&self, hotkey: Hotkey) {
        if let Err(err) = self.post(Request::Register(hotkey), None) {
            log::error!(
                "投递 WM_APP_RECONFIG 到热键线程({}) 失败：{err}",
                self.thread_id
            );
        }
    }

    /// 注销当前热键并进入抑制态（不再注册），阻塞直到热键线程确认完成。
    ///
    /// 设置页进入录制态时必须先调它，否则录制期间按到旧组合键会当场触发旧动作。
    /// 返回是否达成目标态（热键本就未注册时也返回 true——目标态已满足）。
    pub fn suppress(&self) -> bool {
        self.request_with_reply(Request::Suppress).unwrap_or(false)
    }

    /// 注册新热键并**等待结果**，返回是否注册成功。
    ///
    /// 注册失败（组合键已被其它程序占用等）会返回 false，调用方据此回退。
    pub fn try_register(&self, hotkey: Hotkey) -> bool {
        self.request_with_reply(Request::Register(hotkey))
            .unwrap_or(false)
    }

    /// 投递请求并阻塞等待热键线程回执；超时 / 投递失败返回 `None`。
    fn request_with_reply(&self, request: Request) -> Option<bool> {
        let (tx, rx) = mpsc::channel();
        if let Err(err) = self.post(request, Some(tx)) {
            log::error!(
                "投递 WM_APP_RECONFIG 到热键线程({}) 失败：{err}",
                self.thread_id
            );
            return None;
        }
        match rx.recv_timeout(REPLY_TIMEOUT) {
            Ok(ok) => Some(ok),
            Err(_) => {
                log::error!("等待热键线程({}) 回执超时", self.thread_id);
                None
            }
        }
    }

    /// 写共享请求（+ 可选回执通道）后投递消息；先写再投，热键线程读到的一定是新值。
    fn post(
        &self,
        request: Request,
        reply: Option<mpsc::Sender<bool>>,
    ) -> windows::core::Result<()> {
        {
            // 锁中毒不应当让改键这件事把调用方搞崩：热键线程不会因为持锁 panic，
            // 中毒只可能是历史 panic 的残留，取回数据继续即可。
            let mut guard = self
                .shared
                .lock()
                .unwrap_or_else(|poisoned| poisoned.into_inner());
            guard.request = request;
            guard.reply = reply;
        }

        unsafe { PostThreadMessageW(self.thread_id, WM_APP_RECONFIG, WPARAM(0), LPARAM(0)) }
    }
}

/// 起一条专用线程注册热键并跑消息循环，返回可换键的句柄。
///
/// `on_press` 在热键线程上被调用，所以里面**不要做 UI 操作**，只投递事件。
///
/// 返回 `None` 表示线程没能起来（拿不到线程 id）；热键本身注册失败（被占用）
/// **不**会让这里返回 `None`——线程仍然会跑消息循环，之后
/// [`HotkeyHandle::set`] / [`HotkeyHandle::try_register`] 还能改成别的键重试。
pub fn spawn_hotkey(initial: Hotkey, on_press: OnPress) -> Option<HotkeyHandle> {
    let (tx, rx) = mpsc::channel::<u32>();
    let shared = Arc::new(Mutex::new(Shared {
        request: Request::Register(initial),
        reply: None,
    }));
    let thread_shared = Arc::clone(&shared);

    std::thread::spawn(move || unsafe {
        let hwnd = None; // 线程级热键，不绑定窗口

        // 先注册再上报线程 id：`RegisterHotKey` 是 user32 调用，会顺带把线程的
        // 消息队列建好。之后 `PostThreadMessageW` 才不会因为「目标线程无队列」
        // 失败（ERROR_INVALID_THREAD_ID）。
        apply_request(hwnd, Request::Register(initial));
        let thread_id = GetCurrentThreadId();
        let _ = tx.send(thread_id);

        let mut msg = MSG::default();
        loop {
            // GetMessageW 返回 <= 0 表示退出或出错。
            if GetMessageW(&mut msg, hwnd, 0, 0).0 <= 0 {
                break;
            }
            match msg.message {
                WM_HOTKEY => on_press(),
                WM_APP_RECONFIG => {
                    // 取出请求与回执通道：先取再执行，避免和下一轮请求串台。
                    let (request, reply) = {
                        let mut guard = thread_shared
                            .lock()
                            .unwrap_or_else(|poisoned| poisoned.into_inner());
                        (guard.request, guard.reply.take())
                    };
                    let ok = apply_request(hwnd, request);
                    // 回执发给发起方（`suppress` / `try_register`）；发起方已超时离开时
                    // 发送失败也无所谓，忽略即可。
                    if let Some(tx) = reply {
                        let _ = tx.send(ok);
                    }
                }
                WM_QUIT => break,
                _ => {
                    let _ = TranslateMessage(&msg);
                    DispatchMessageW(&msg);
                }
            }
        }
        let _ = UnregisterHotKey(hwnd, HOTKEY_ID);
    });

    // 线程起来前就返回会让 `set()` 扑空（消息队列还没建），所以这里等它上报 id。
    // 线程 panic 时 `tx` 被 drop，`recv` 立刻返回 Err → 返回 None，不会卡死。
    let thread_id = rx.recv().ok()?;
    Some(HotkeyHandle { thread_id, shared })
}

/// 在热键线程上执行一次请求，返回是否达成目标态（注册成功 / 已注销）。
///
/// 无论注册还是抑制，都先 `UnregisterHotKey` 注销旧的（没注册过时这一步会失败，
/// 忽略即可）。注册失败**不退出线程**：改成一个仍被占用的键是可以发生的，线程活着
/// 用户才能继续改回别的键。
unsafe fn apply_request(hwnd: Option<HWND>, request: Request) -> bool {
    let _ = unsafe { UnregisterHotKey(hwnd, HOTKEY_ID) };
    match request {
        Request::Suppress => {
            log::info!("热键已注销（抑制态：录制热键期间不触发旧动作）");
            true
        }
        Request::Register(hotkey) => match unsafe { register(hwnd, &hotkey) } {
            Ok(()) => {
                log::info!(
                    "热键已注册 modifiers={:#x} vk={:#x}",
                    hotkey.modifiers.0,
                    hotkey.vk.0
                );
                true
            }
            Err(err) => {
                log::error!(
                    "RegisterHotKey 失败（热键可能已被占用）modifiers={:#x} vk={:#x}: {err}",
                    hotkey.modifiers.0,
                    hotkey.vk.0
                );
                false
            }
        },
    }
}

/// 注册热键（`RegisterHotKey` 的薄封装，只为少写一遍 `u32::from(vk.0)`）。
unsafe fn register(hwnd: Option<HWND>, hotkey: &Hotkey) -> windows::core::Result<()> {
    unsafe { RegisterHotKey(hwnd, HOTKEY_ID, hotkey.modifiers, u32::from(hotkey.vk.0)) }
}

/// 由配置里的热键描述（[`voke_core::HotkeySpec`]）构造 Win32 热键。
///
/// `key` 用的是 **gpui 的键名**（`"space"` / `"a"` / `"f1"` / `"tab"` …），也就是
/// `KeyDownEvent.keystroke.key` 那一套命名——因为设置页的录制器直接把它落进
/// 配置，而 Win32 侧 `RegisterHotKey` 要的是虚拟键码，两者的翻译只能在这里做
/// （app 层不该为此依赖 windows crate）。
///
/// 认不出来的键返回 `None`，调用方回落到默认热键并记日志。
pub fn hotkey_from_spec(spec: &voke_core::HotkeySpec) -> Option<Hotkey> {
    let mut bits = 0u32;
    if spec.ctrl {
        bits |= MOD_CONTROL.0;
    }
    if spec.alt {
        bits |= MOD_ALT.0;
    }
    if spec.shift {
        bits |= MOD_SHIFT.0;
    }
    if spec.win {
        bits |= MOD_WIN.0;
    }
    Some(Hotkey {
        modifiers: HOT_KEY_MODIFIERS(bits),
        vk: virtual_key(&spec.key)?,
    })
}

/// 把 gpui 的键名翻译成虚拟键码。见 [`hotkey_from_spec`] 的命名约定说明。
fn virtual_key(key: &str) -> Option<VIRTUAL_KEY> {
    let vk = match key {
        "space" => VK_SPACE,
        "tab" => VK_TAB,
        "enter" => VK_RETURN,
        "escape" => VK_ESCAPE,
        "backspace" => VK_BACK,
        "delete" => VK_DELETE,
        "insert" => VK_INSERT,
        "home" => VK_HOME,
        "end" => VK_END,
        "pageup" => VK_PRIOR,
        "pagedown" => VK_NEXT,
        "up" => VK_UP,
        "down" => VK_DOWN,
        "left" => VK_LEFT,
        "right" => VK_RIGHT,
        // 标点键：这些没有自己的 VK_* 名字，挂在 OEM 系列上。取值与
        // gpui-pre-windows `keyboard.rs::get_vkey_from_key_with_us_layout`
        // 的 US 布局表一致（未按 Shift 换挡时的那个字符）。
        "," => VK_OEM_COMMA,
        "." => VK_OEM_PERIOD,
        "/" => VK_OEM_2,
        ";" => VK_OEM_1,
        "'" => VK_OEM_7,
        "[" => VK_OEM_4,
        "\\" => VK_OEM_5,
        "]" => VK_OEM_6,
        "`" => VK_OEM_3,
        "-" => VK_OEM_MINUS,
        "=" => VK_OEM_PLUS,
        other => return virtual_key_other(other),
    };
    Some(vk)
}

/// 字母 / 数字 / F1–F24：这几类的键码是连续的，按偏移算比列全表更短。
fn virtual_key_other(key: &str) -> Option<VIRTUAL_KEY> {
    let mut chars = key.chars();
    if let (Some(c), None) = (chars.next(), chars.next()) {
        if c.is_ascii_alphabetic() {
            let offset = c.to_ascii_uppercase() as u16 - 'A' as u16;
            return Some(VIRTUAL_KEY(VK_A.0 + offset));
        }
        if c.is_ascii_digit() {
            return Some(VIRTUAL_KEY(VK_0.0 + c as u16 - '0' as u16));
        }
        return None;
    }
    // F 区：VK_F1..VK_F24 连续。注意要先排除单字符情况（键名 `f` 是字母 F，
    // 不是 F1）。
    let index: u16 = key.strip_prefix('f')?.parse().ok()?;
    if (1..=24).contains(&index) {
        Some(VIRTUAL_KEY(VK_F1.0 + index - 1))
    } else {
        None
    }
}

/// PoC 阶段用：Ctrl+Alt+Space，避开 PowerToys Run 占用的 Alt+Space。
pub const CTRL_ALT_SPACE: Hotkey = Hotkey {
    modifiers: HOT_KEY_MODIFIERS(MOD_ALT.0 | MOD_CONTROL.0),
    vk: VK_SPACE,
};

/// 正式目标热键（§1）：Alt+Space，接管 PowerToys Run 的位置。
/// 等 PTR 停用或改键后再切到这个。
pub const ALT_SPACE: Hotkey = Hotkey {
    modifiers: MOD_ALT,
    vk: VK_SPACE,
};

/// 便捷入口：PoC 默认注册 Ctrl+Alt+Space。
///
/// 要拿句柄改键请用 [`spawn_hotkey`]。
pub fn spawn_default_hotkey(on_press: OnPress) {
    let _ = spawn_hotkey(CTRL_ALT_SPACE, on_press);
}
