//! 保持系统唤醒（§5 的 Awake 模块，对齐 PowerToys Awake）。
//!
//! ## 为什么必须是一条**专用线程**
//!
//! `SetThreadExecutionState` 是 **per-thread** 语义：它设置的是**调用线程**的
//! 执行状态，`ES_CONTINUOUS` 要求该线程一直存活且不再改回默认值。所以
//! "保持唤醒"不能在 UI 线程上做完就返回——那样状态会随调用结束而失效
//! （实际上线程还在，但我们会立刻又调一次清掉，语义反而混乱）。
//!
//! 设计的线程形态：
//!
//! ```text
//! spawn_awake(minutes, keep_screen)
//!   → 建立新线程（名字 voke-awake）
//!   → 线程内 SetThreadExecutionState(ES_CONTINUOUS | ES_SYSTEM_REQUIRED [| ES_DISPLAY_REQUIRED])
//!   → 线程阻塞在 mpsc::Receiver 上等命令
//!        · 收到 Cancel / 超时（minutes 到了）→ SetThreadExecutionState(ES_CONTINUOUS)  清除
//!        · 线程退出
//! ```
//!
//! **崩溃自愈**：进程一退出，线程消亡，执行状态自动失效，系统恢复休眠。
//! 不存在"launcher 挂了导致电脑永不睡眠"的风险——这是该 API 的天然兜底。
//! 参考 `voke-sys/src/hotkey.rs` 的线程写法（同样需要常驻线程 + 通道收命令）。
//!
//! ## 状态可见性
//!
//! [`AwakeHandle`] 携带到期时刻与是否保持屏幕，`AwakeState` 读它即可回答
//! "现在还剩多久"。UI 侧的托盘图标提示列为可选项，不在本模块。
//!
//! ## 平台分层
//!
//! 时长换算、剩余时间格式化、状态机都放在纯逻辑部分（[`AwakeState`] /
//! [`AwakeRequest`]），Linux 上 `cargo test -p voke-sys` 会真跑；只有真正调
//! `SetThreadExecutionState` 的线程是 `#[cfg(windows)]`。

use std::sync::atomic::{AtomicU64, Ordering};
use std::time::{SystemTime, UNIX_EPOCH};

/// 一个保持唤醒请求。
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct AwakeRequest {
    /// 保持多少分钟；`None` 表示不限时（直到用户取消）。
    pub minutes: Option<u32>,
    /// 是否连屏幕一起保持（`ES_DISPLAY_REQUIRED`）。
    pub keep_screen: bool,
}

impl AwakeRequest {
    /// 请求对应的到期时刻（`None` = 永不到期）。
    ///
    /// `now_secs` 由调用方给，便于单测（生产传 [`now_secs`]）。
    pub fn expires_at(self, now_secs: u64) -> Option<u64> {
        self.minutes
            .map(|m| now_secs.saturating_add(u64::from(m) * 60))
    }

    /// 人类可读的时长（"30 分钟" / "2 小时" / "永久"）。
    pub fn label(self) -> String {
        match self.minutes {
            None => "永久".to_string(),
            Some(m) if m % 60 == 0 && m >= 60 => format!("{} 小时", m / 60),
            Some(m) => format!("{m} 分钟"),
        }
    }
}

/// 当前 unix 秒（`0` 表示时钟异常，调用方自行处理）。
pub fn now_secs() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0)
}

/// 进程级的"当前保持唤醒"状态（原子，任何线程可读）。
///
/// 编码方式：`0` = 未激活；否则低 1 位是 `keep_screen`，其余位是到期时刻
/// （unix 秒）。`u64` 完全放得下（unix 秒用不满 63 位）。
static AWAKE: AtomicU64 = AtomicU64::new(0);

/// 打包/解包的两个纯函数（抽出来是为了单测覆盖位运算本身）。
///
/// 编码：`0` = 未激活；否则 `(secs << 1) | keep_screen`。
/// `secs == MAX_SECS` 是"永久"的哨兵值，所以显式到期时刻会被**钳到**
/// `MAX_SECS - 1`——真实的 unix 秒离 `2^62` 还有上百亿年，钳位在现实中永不触发。
const MAX_SECS: u64 = u64::MAX >> 1;

fn pack(expires_at: Option<u64>, keep_screen: bool) -> u64 {
    // `None`（永久）写哨兵 `MAX_SECS`；显式时刻钳到 `MAX_SECS - 1`（真实
    // unix 秒离 `2^62` 还有上百亿年，钳位在现实中永不触发）。
    let secs = match expires_at {
        None => MAX_SECS,
        Some(secs) => secs.min(MAX_SECS - 1),
    };
    // `secs << 1` 留出最低位给 keep_screen；钳位后不会溢出。
    (secs << 1) | u64::from(keep_screen)
}

fn unpack(raw: u64) -> Option<(Option<u64>, bool)> {
    if raw == 0 {
        return None;
    }
    let keep_screen = raw & 1 == 1;
    let secs = raw >> 1;
    let expires_at = if secs >= MAX_SECS { None } else { Some(secs) };
    Some((expires_at, keep_screen))
}

/// 等待到 `expires_at` 的剩余秒数（已到期返回 `0`，供 `recv_timeout` 用）。
///
/// 纯函数：`now` 由调用方给，便于单测覆盖"未来 / 已到期"两种边界。
/// 调用点只有 Windows 的保持唤醒线程，非 Windows 侧仅测试引用。
#[cfg_attr(not(windows), allow(dead_code))]
fn remaining_wait_secs(expires_at: u64, now: u64) -> u64 {
    expires_at.saturating_sub(now)
}

/// 读当前保持唤醒状态（`None` = 没在保持）。
pub fn current() -> Option<AwakeState> {
    let raw = AWAKE.load(Ordering::Relaxed);
    let (expires_at, keep_screen) = unpack(raw)?;
    // 已过期的（线程可能刚退出，或者进程被挂起过）如实回报"未激活"。
    if let Some(at) = expires_at
        && at <= now_secs()
    {
        return None;
    }
    Some(AwakeState {
        expires_at,
        keep_screen,
        since: 0,
    })
}

/// 当前保持唤醒的快照。
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct AwakeState {
    /// 到期时刻（unix 秒）；`None` = 永久。
    pub expires_at: Option<u64>,
    /// 是否连屏幕一起保持。
    pub keep_screen: bool,
    /// 激活时刻（unix 秒）；`0` 表示未知（[`current`] 不记录）。
    pub since: u64,
}

impl AwakeState {
    /// 剩余秒数；永久返回 `None`；已到期返回 `Some(0)`。
    pub fn remaining_secs(&self, now: u64) -> Option<u64> {
        self.expires_at.map(|at| at.saturating_sub(now))
    }

    /// 人类可读的剩余时间（"还剩 25 分钟" / "永久"）。
    pub fn remaining_label(&self, now: u64) -> String {
        match self.remaining_secs(now) {
            None => "永久".to_string(),
            Some(0) => "即将结束".to_string(),
            Some(secs) if secs < 60 => format!("还剩 {secs} 秒"),
            Some(secs) if secs < 3600 => format!("还剩 {} 分钟", secs / 60),
            Some(secs) => {
                let (h, m) = (secs / 3600, (secs % 3600) / 60);
                if m == 0 {
                    format!("还剩 {h} 小时")
                } else {
                    format!("还剩 {h} 小时 {m} 分钟")
                }
            }
        }
    }

    /// 屏幕是否也被保持（副标题文案用）。
    pub fn screen_label(&self) -> &'static str {
        if self.keep_screen {
            "系统 + 屏幕"
        } else {
            "仅系统"
        }
    }
}

/// 托盘右键菜单里 awake 状态条目的文案。
///
/// 纯函数：状态与"现在"都由调用方给，便于单测；托盘线程的定时器周期调用它，
/// 把结果写进菜单项（见 `tray.rs`）。
///
/// - 未激活 → `保持唤醒：未激活`
/// - 激活（限时）→ `保持唤醒：还剩 25 分钟 · 屏幕打开` / `· 屏幕关闭`
/// - 激活（永久）→ `保持唤醒：永久 · 屏幕打开`
pub fn menu_label(status: Option<AwakeState>, now: u64) -> String {
    let Some(state) = status else {
        return "保持唤醒：未激活".to_string();
    };
    // `· 屏幕打开 / · 屏幕关闭` 是**结果描述**（屏幕会不会被保持），与插件内状态行同口径；
    // 插件里「连屏幕一起保持 / 仅系统唤醒」是进入二级的**动作描述**，两者不混用。
    let scope = if state.keep_screen {
        "屏幕打开"
    } else {
        "屏幕关闭"
    };
    let remain = match state.remaining_secs(now) {
        None => "永久".to_string(),
        // 已到期（线程可能刚退出）：不显示负数/0，给一个收尾说法。
        Some(0) => "即将结束".to_string(),
        Some(secs) => format!("还剩 {}", short_duration(secs)),
    };
    format!("保持唤醒：{remain} · {scope}")
}

/// 剩余时长的短文案（精度到分钟；不足 1 分钟按秒显示）。
fn short_duration(secs: u64) -> String {
    if secs < 60 {
        format!("{secs} 秒")
    } else if secs < 3600 {
        format!("{} 分钟", secs / 60)
    } else {
        let (h, m) = (secs / 3600, (secs % 3600) / 60);
        if m == 0 {
            format!("{h} 小时")
        } else {
            format!("{h} 小时 {m} 分钟")
        }
    }
}

/// 未在 Windows 上时的占位实现：记下状态（供 app 层单测与 UI 展示），
/// 但不真的阻止休眠（没有 Win32 可调）。
#[cfg(not(windows))]
pub fn spawn_awake(request: AwakeRequest) {
    log::warn!("保持唤醒请求（{}）在非 Windows 平台被忽略", request.label());
    AWAKE.store(
        pack(request.expires_at(now_secs()), request.keep_screen),
        Ordering::Relaxed,
    );
}

/// 未在 Windows 上时的占位实现。
#[cfg(not(windows))]
pub fn cancel_awake() {
    AWAKE.store(0, Ordering::Relaxed);
}

/// 启动保持唤醒（异步，立刻返回）。
///
/// 已有一轮保持在跑时，**先取消旧的再起新的**（用户连续点两个时长，语义是
/// "改成新的那个"，不是叠加）。取消走原子标志，由旧线程自己观察到并清除。
#[cfg(windows)]
pub fn spawn_awake(request: AwakeRequest) {
    use std::sync::mpsc;

    // 让旧线程退出：换掉世代号，旧线程醒来会发现自己过期。
    let generation = GENERATION.fetch_add(1, Ordering::SeqCst) + 1;
    let (tx, rx) = mpsc::channel::<()>();
    ACTIVE_SENDER
        .lock()
        .unwrap_or_else(|poisoned| poisoned.into_inner())
        .replace(tx);

    let expires_at = request.expires_at(now_secs());
    // 先写状态再起线程：provider 立刻查到时就能看到"已激活"。
    AWAKE.store(pack(expires_at, request.keep_screen), Ordering::Relaxed);

    std::thread::Builder::new()
        .name("voke-awake".into())
        .spawn(move || awake_thread(request, expires_at, generation, rx))
        .expect("创建保持唤醒线程失败");
}

/// 取消保持唤醒（幂等：没在保持时也安全）。
#[cfg(windows)]
pub fn cancel_awake() {
    // 世代号 +1 让当前线程退出；状态清零。
    GENERATION.fetch_add(1, Ordering::SeqCst);
    AWAKE.store(0, Ordering::Relaxed);
    if let Some(tx) = ACTIVE_SENDER
        .lock()
        .unwrap_or_else(|poisoned| poisoned.into_inner())
        .take()
    {
        let _ = tx.send(());
    }
}

/// 当前活跃线程的退出通道（新请求会替换它）。
#[cfg(windows)]
static ACTIVE_SENDER: std::sync::Mutex<Option<std::sync::mpsc::Sender<()>>> =
    std::sync::Mutex::new(None);

/// 世代号：每次 `spawn_awake` / `cancel_awake` 自增，旧线程据此判断自己是否该退出。
#[cfg(windows)]
static GENERATION: AtomicU64 = AtomicU64::new(0);

/// 保持唤醒线程体。退出前一定把 `ES_CONTINUOUS` 清掉（否则线程死后状态残留）。
#[cfg(windows)]
fn awake_thread(
    request: AwakeRequest,
    expires_at: Option<u64>,
    generation: u64,
    rx: std::sync::mpsc::Receiver<()>,
) {
    use ::windows::Win32::System::Power::{
        ES_CONTINUOUS, ES_DISPLAY_REQUIRED, ES_SYSTEM_REQUIRED, SetThreadExecutionState,
    };

    let mut flags = ES_CONTINUOUS | ES_SYSTEM_REQUIRED;
    if request.keep_screen {
        flags |= ES_DISPLAY_REQUIRED;
    }
    // SAFETY: 参数是合法常量位或；无指针参数。
    let previous = unsafe { SetThreadExecutionState(flags) };
    if previous.0 == 0 {
        log::error!(
            "SetThreadExecutionState 失败，保持唤醒未生效（{}）",
            request.label()
        );
        AWAKE.store(0, Ordering::Relaxed);
        return;
    }
    log::info!(
        "保持唤醒已开启（{}，{}）",
        request.label(),
        if request.keep_screen {
            "系统 + 屏幕"
        } else {
            "仅系统"
        }
    );

    // 等取消信号或到期。循环每次被唤醒都先复查世代号（新请求或取消都会让
    // 世代号变化）与到期判断，语义不变；区别只在于不再每秒空转轮询：
    loop {
        if GENERATION.load(Ordering::SeqCst) != generation {
            break;
        }
        if let Some(at) = expires_at
            && now_secs() >= at
        {
            log::info!("保持唤醒到期（{}），自动结束", request.label());
            break;
        }
        match expires_at {
            // 限时：算准到 `expires_at` 的剩余时长，一次 `recv_timeout` 等到底。
            // 到点后回到循环顶部，由上面的到期判断收尾（时钟回跳导致"没到期
            // 又醒了"时会按新的剩余时长继续等，不会提前结束）。
            Some(at) => match rx.recv_timeout(std::time::Duration::from_secs(remaining_wait_secs(
                at,
                now_secs(),
            ))) {
                // 收到取消信号（或 sender 被新请求替换而断开）。
                Ok(()) | Err(std::sync::mpsc::RecvTimeoutError::Disconnected) => break,
                // 超时 = 到点，回顶部走到期/世代号复查。
                Err(std::sync::mpsc::RecvTimeoutError::Timeout) => continue,
            },
            // 永久模式没有自然醒时刻：阻塞 `recv` 等信号即可。取消会发信号；
            // 新请求替换 `ACTIVE_SENDER` 会丢掉旧 sender，旧 rx 随之断开，
            // 同样能唤醒——世代号复查不会因此被绕过。
            None => match rx.recv() {
                Ok(()) | Err(std::sync::mpsc::RecvError) => break,
            },
        }
    }

    // 清除：只留 ES_CONTINUOUS（这是"恢复默认行为"的官方写法）。
    // SAFETY: 同上。
    unsafe {
        SetThreadExecutionState(ES_CONTINUOUS);
    }
    // 只有自己还是"当前那一代"时才清状态，避免把新一轮的状态误清掉。
    if GENERATION.load(Ordering::SeqCst) == generation {
        AWAKE.store(0, Ordering::Relaxed);
        ACTIVE_SENDER
            .lock()
            .unwrap_or_else(|poisoned| poisoned.into_inner())
            .take();
    }
    log::info!("保持唤醒已结束（{}）", request.label());
}

// ---------------------------------------------------------------------------
// 本地时区偏移（timestamp provider 用）
// ---------------------------------------------------------------------------

/// 当前本地时区相对 UTC 的偏移（秒，东八区 = `28800`）。
///
/// 用 `GetTimeZoneInformation` 拿 `Bias`（UTC = 本地 + Bias，**符号与直觉相反**），
/// 若当前处于夏令时则再叠加 `DaylightBias`。取不到时返回 `0`（等价 UTC），
/// 这比让 timestamp 插件报错好——顶多是"本地时间"那一行显示成 UTC。
///
/// **注意**：返回值是"本地 = UTC + offset"里的 `offset`，即 `-(Bias + 附加)`。
#[cfg(windows)]
pub fn local_offset_seconds() -> i32 {
    use ::windows::Win32::System::SystemServices::TIME_ZONE_ID_DAYLIGHT;
    use ::windows::Win32::System::Time::{
        GetTimeZoneInformation, TIME_ZONE_ID_INVALID, TIME_ZONE_INFORMATION,
    };

    let mut info = TIME_ZONE_INFORMATION::default();
    // SAFETY: &mut info 是有效可写指针（Default 已清零）。
    let result = unsafe { GetTimeZoneInformation(&mut info) };
    if result == TIME_ZONE_ID_INVALID {
        log::warn!("GetTimeZoneInformation 失败，本地时间将按 UTC 处理");
        return 0;
    }
    let mut bias = info.Bias;
    if result == TIME_ZONE_ID_DAYLIGHT {
        bias += info.DaylightBias;
    }
    // Win32 规定 UTC = 本地 + Bias → 本地 = UTC - Bias。
    // 东八区 Bias = -480（分钟），于是 offset = +480 分钟 = 28800 秒。
    -bias.saturating_mul(60)
}

/// 非 Windows：没有本地时区可查，按 UTC 处理。
#[cfg(not(windows))]
pub fn local_offset_seconds() -> i32 {
    0
}

#[cfg(test)]
mod tests {
    use super::*;

    /// 位打包必须可逆（这是跨线程共享状态的核心编码）。
    #[test]
    fn pack_unpack_roundtrips() {
        for (expires_at, keep_screen) in [
            (None, false),
            (None, true),
            (Some(1_700_000_000), false),
            (Some(1_700_000_000), true),
        ] {
            let raw = pack(expires_at, keep_screen);
            assert_ne!(raw, 0, "已激活状态不能编码成 0");
            assert_eq!(unpack(raw), Some((expires_at, keep_screen)));
        }
        assert_eq!(unpack(0), None, "0 = 未激活");
        // 极端值被钳位（真实 unix 秒永远够不到），不 panic 也不与哨兵撞车。
        let raw = pack(Some(u64::MAX), true);
        assert_eq!(unpack(raw), Some((Some(MAX_SECS - 1), true)));
    }

    /// `None`（永久）与"已到期"必须能区分：前者哨兵值，后者是过去的时刻。
    #[test]
    fn forever_is_distinct_from_expiry() {
        let forever = unpack(pack(None, false)).unwrap();
        assert_eq!(forever.0, None);
        let expired = unpack(pack(Some(1), false)).unwrap();
        assert_eq!(expired.0, Some(1));
    }

    #[test]
    fn expires_at_arithmetic() {
        let now = 1_700_000_000u64;
        assert_eq!(
            AwakeRequest {
                minutes: Some(30),
                keep_screen: false
            }
            .expires_at(now),
            Some(now + 30 * 60)
        );
        assert_eq!(
            AwakeRequest {
                minutes: None,
                keep_screen: false
            }
            .expires_at(now),
            None
        );
        // 溢出也不 panic（`u64::MAX` 秒后已是天文数字，直接被钳位）
        assert_eq!(
            AwakeRequest {
                minutes: Some(u32::MAX),
                keep_screen: false
            }
            .expires_at(u64::MAX - 1),
            Some(u64::MAX)
        );
    }

    /// 线程等待时长：未来时刻给剩余秒数，已到期给 `0`（`recv_timeout(0)` 立即
    /// 醒，由循环顶部的到期判断收尾，不会多等也不会提前结束）。
    #[test]
    fn remaining_wait_secs_covers_bounds() {
        let now = 1_700_000_000u64;
        assert_eq!(remaining_wait_secs(now + 90, now), 90);
        assert_eq!(remaining_wait_secs(now, now), 0);
        // 时钟回跳 / 已过期：钳到 0，绝不下溢成天文数字。
        assert_eq!(remaining_wait_secs(now - 1, now), 0);
        assert_eq!(remaining_wait_secs(0, now), 0);
    }

    #[test]
    fn request_labels_are_human_readable() {
        let label = |minutes| {
            AwakeRequest {
                minutes,
                keep_screen: false,
            }
            .label()
        };
        assert_eq!(label(Some(30)), "30 分钟");
        assert_eq!(label(Some(60)), "1 小时");
        assert_eq!(label(Some(120)), "2 小时");
        assert_eq!(label(Some(480)), "8 小时");
        assert_eq!(label(Some(90)), "90 分钟");
        assert_eq!(label(None), "永久");
    }

    #[test]
    fn remaining_labels_cover_all_ranges() {
        let now = 1_700_000_000u64;
        let state = |expires_at| AwakeState {
            expires_at,
            keep_screen: false,
            since: 0,
        };
        assert_eq!(state(None).remaining_label(now), "永久");
        assert_eq!(state(Some(now)).remaining_label(now), "即将结束");
        assert_eq!(state(Some(now + 45)).remaining_label(now), "还剩 45 秒");
        assert_eq!(
            state(Some(now + 25 * 60)).remaining_label(now),
            "还剩 25 分钟"
        );
        assert_eq!(state(Some(now + 3600)).remaining_label(now), "还剩 1 小时");
        assert_eq!(
            state(Some(now + 5400)).remaining_label(now),
            "还剩 1 小时 30 分钟"
        );
    }

    #[test]
    fn screen_label_reflects_keep_screen() {
        let mut state = AwakeState {
            expires_at: None,
            keep_screen: false,
            since: 0,
        };
        assert_eq!(state.screen_label(), "仅系统");
        state.keep_screen = true;
        assert_eq!(state.screen_label(), "系统 + 屏幕");
    }

    /// 托盘菜单文案覆盖全部状态：未激活 / 限时 / 永久 / 连屏幕与否 / 已到期。
    #[test]
    fn menu_labels_cover_all_states() {
        let now = 1_700_000_000u64;
        let state = |expires_at, keep_screen| AwakeState {
            expires_at,
            keep_screen,
            since: 0,
        };
        assert_eq!(menu_label(None, now), "保持唤醒：未激活");
        assert_eq!(
            menu_label(Some(state(Some(now + 25 * 60), true)), now),
            "保持唤醒：还剩 25 分钟 · 屏幕打开"
        );
        assert_eq!(
            menu_label(Some(state(Some(now + 25 * 60), false)), now),
            "保持唤醒：还剩 25 分钟 · 屏幕关闭"
        );
        // 永久：不带"还剩"。
        assert_eq!(
            menu_label(Some(state(None, true)), now),
            "保持唤醒：永久 · 屏幕打开"
        );
        assert_eq!(
            menu_label(Some(state(None, false)), now),
            "保持唤醒：永久 · 屏幕关闭"
        );
        // 超过一小时的剩余时间仍按小时+分钟显示。
        assert_eq!(
            menu_label(Some(state(Some(now + 5400), false)), now),
            "保持唤醒：还剩 1 小时 30 分钟 · 屏幕关闭"
        );
        // 不足一分钟按秒；恰好到期给"即将结束"。
        assert_eq!(
            menu_label(Some(state(Some(now + 30), false)), now),
            "保持唤醒：还剩 30 秒 · 屏幕关闭"
        );
        assert_eq!(
            menu_label(Some(state(Some(now), false)), now),
            "保持唤醒：即将结束 · 屏幕关闭"
        );
    }

    /// 非 Windows 平台上的占位实现也必须维护状态（app 层逻辑才能真正被单测）。
    #[cfg(not(windows))]
    #[test]
    fn placeholder_keeps_state_bookkeeping() {
        // 先清干净
        cancel_awake();
        assert!(current().is_none());

        // `spawn_awake` 在非 Windows 上不真的阻止休眠，但状态要如实可读。
        spawn_awake(AwakeRequest {
            minutes: Some(10),
            keep_screen: true,
        });
        let state = current().expect("刚写入的状态应当可读");
        assert!(state.keep_screen);
        assert!(state.remaining_secs(now_secs()).unwrap() > 500);

        cancel_awake();
        assert!(current().is_none());
    }

    /// 永久（`minutes: None`）的状态也要能读回来。
    #[cfg(not(windows))]
    #[test]
    fn placeholder_supports_permanent() {
        spawn_awake(AwakeRequest {
            minutes: None,
            keep_screen: false,
        });
        let state = current().expect("永久状态应当可读");
        assert_eq!(state.expires_at, None);
        cancel_awake();
    }

    /// 已到期的状态读出来是"未激活"（防止线程意外死亡留下幽灵状态）。
    #[cfg(not(windows))]
    #[test]
    fn expired_state_reads_as_inactive() {
        AWAKE.store(
            pack(Some(now_secs().saturating_sub(1)), false),
            Ordering::Relaxed,
        );
        assert!(current().is_none());
        cancel_awake();
    }
}
