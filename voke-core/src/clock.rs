//! 可替换的时钟（时间相关 provider 共用）。
//!
//! 抽成 trait 只为测试可复现：`timestamp` / `awake` 的换算逻辑不该依赖
//! "跑测试那一刻的真实时间"。生产用 [`SystemClock`]，测试用 [`FixedClock`]。
//!
//! 为什么放在独立模块而不是 `timestamp.rs` 里：`awake` 也要算"还剩多久"，
//! 两个 provider 共用一个时钟定义比各自复制一份 trait 更干净（也不会出现
//! "两个 Clock trait 名字一样但类型不兼容"的困惑）。

/// 当前时钟。
pub trait Clock: Send + Sync {
    /// 当前 unix 秒。
    fn now_secs(&self) -> i64;
}

/// 系统时钟（生产用）。
#[derive(Clone, Copy, Debug, Default)]
pub struct SystemClock;

impl Clock for SystemClock {
    fn now_secs(&self) -> i64 {
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .map(|d| d.as_secs() as i64)
            .unwrap_or(0)
    }
}

/// 固定时钟（测试用）。
#[derive(Clone, Copy, Debug)]
pub struct FixedClock(pub i64);

impl Clock for FixedClock {
    fn now_secs(&self) -> i64 {
        self.0
    }
}
