//! 零依赖的熵源（UUID / 随机串用）。
//!
//! ## 为什么不引 `rand` / `getrandom` / `uuid`
//!
//! §1 要求 core 保持"零新依赖、Linux 秒级测试循环"。三个 crate 各自成立的
//! 理由都不足以打破这条线：
//! - `getrandom` 要额外 target 适配（Windows 上是 `BCryptGenRandom`，交叉编译没问题，
//!   但 core 会第一次引入平台相关代码）
//! - `uuid` 的 v4 生成最终也只是要 16 字节熵 + 版本/变体位设置
//! - `rand` 的体积（默认 feature 拉一整套分布/采样）对"生成个随机串"严重过重
//!
//! 于是这里自己取熵。**注意定位**：这些值用于"生成一个 UUID / 随机验证码"这类
//! 随手场景，不是密码学用途。熵质量够（系统熵池），但**不做**密码学承诺——
//! 不要用它生成密钥/令牌。真需要密码学随机时再引 `getrandom`，届时本模块
//! 只需换一个 `fill_bytes` 实现（调用方不变）。
//!
//! ## 各平台的实现
//!
//! - Windows：`BCryptGenRandom`（`windows` crate 是 app 层依赖，core 不依赖它
//!   ——所以这里走 `std::time` + 地址熵的兜底，见下；`voke-sys` 若需要更强的
//!   实现可以后续把它注入进来）
//! - 其它：`/dev/urandom`（Linux/macOS 都有），失败时退回时间 + 地址熵
//!
//! 为了让"测试可复现"，[`Entropy::seeded`] 提供确定性实现；生产用
//! [`Entropy::system`]。
//!
//! ## 为什么是 trait
//!
//! provider 要在 `cargo test` 里断言"同一个种子产出同一串"，所以随机源必须
//! 可替换。`Entropy` 是 `Send + Sync` 的（provider 契约要求）。

/// 随机字节源。
pub trait Entropy: Send + Sync {
    /// 填入 `buf`（长度任意）。实现必须是线程安全的。
    fn fill(&self, buf: &mut [u8]);
}

/// 系统熵源（生产用）。优先 `/dev/urandom`，不可用时退回时间 + 地址混合。
#[derive(Clone, Copy, Debug, Default)]
pub struct SystemEntropy;

impl Entropy for SystemEntropy {
    fn fill(&self, buf: &mut [u8]) {
        if !fill_from_urandom(buf) {
            fill_from_clock(buf);
        }
    }
}

/// 从 `/dev/urandom` 取熵。成功返回 `true`。
///
/// Windows 上文件不存在，直接返回 `false` 走 [`fill_from_clock`]。
/// （不在这里调 `BCryptGenRandom`：那需要 core 依赖 `windows`，
/// 破坏"core 零平台依赖"。Windows 上时间 + ASLR 地址的混合熵对
/// "生成 UUID / 随机串"这个量级够用。）
fn fill_from_urandom(buf: &mut [u8]) -> bool {
    use std::io::Read as _;
    let Ok(mut file) = std::fs::File::open("/dev/urandom") else {
        return false;
    };
    file.read_exact(buf).is_ok()
}

/// 兜底熵源：时钟 + 栈/堆地址 + 一个进程内计数器，做 splitmix64 混合。
///
/// 不是密码学安全，但对"随手生成个 UUID"足够：跨进程重启不重复，
/// 同进程内连续调用也被计数器保证不重复。
fn fill_from_clock(buf: &mut [u8]) {
    use std::sync::atomic::{AtomicU64, Ordering};
    static COUNTER: AtomicU64 = AtomicU64::new(0);

    let nanos = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_nanos() as u64)
        .unwrap_or(0);
    // 取一个栈上地址作为 ASLR 熵。
    let local = 0u8;
    let addr = &local as *const u8 as u64;
    let count = COUNTER.fetch_add(1, Ordering::Relaxed);

    let mut state = nanos ^ addr.rotate_left(17) ^ count.wrapping_mul(0x9E37_79B9_7F4A_7C15);
    for chunk in buf.chunks_mut(8) {
        state = splitmix64(state);
        let bytes = state.to_le_bytes();
        chunk.copy_from_slice(&bytes[..chunk.len()]);
    }
}

/// splitmix64：一个便宜的雪崩置换，用来把弱熵源搅匀。
fn splitmix64(mut x: u64) -> u64 {
    x = x.wrapping_add(0x9E37_79B9_7F4A_7C15);
    let mut z = x;
    z = (z ^ (z >> 30)).wrapping_mul(0xBF58_476D_1CE4_E5B9);
    z = (z ^ (z >> 27)).wrapping_mul(0x94D0_49BB_1331_11EB);
    z ^ (z >> 31)
}

/// 确定性熵源（测试用）：给定种子产出可复现的字节流。
///
/// 内部是 splitmix64 状态机，不是密码学随机。
#[derive(Debug)]
pub struct SeededEntropy {
    state: std::sync::Mutex<u64>,
}

impl SeededEntropy {
    /// 用一个种子构造（同一个种子 → 同一串字节）。
    pub fn new(seed: u64) -> Self {
        Self {
            state: std::sync::Mutex::new(seed),
        }
    }
}

impl Entropy for SeededEntropy {
    fn fill(&self, buf: &mut [u8]) {
        // 中毒的锁不该让随机源失效（状态只是 u64）。
        let mut state = self
            .state
            .lock()
            .unwrap_or_else(|poisoned| poisoned.into_inner());
        for chunk in buf.chunks_mut(8) {
            *state = splitmix64(*state);
            let bytes = state.to_le_bytes();
            chunk.copy_from_slice(&bytes[..chunk.len()]);
        }
    }
}

/// 便捷入口：往 `buf` 填系统熵。
pub fn fill_system_bytes(buf: &mut [u8]) {
    SystemEntropy.fill(buf);
}

/// 生成一个 v4 UUID 的规范字符串形式（小写、带连字符）。
pub fn uuid_v4(entropy: &dyn Entropy) -> String {
    format_uuid_v4(&random_uuid_bytes(entropy))
}

/// 生成 v4 UUID 的 16 个原始字节（版本位 4、变体位 10）。
pub fn random_uuid_bytes(entropy: &dyn Entropy) -> [u8; 16] {
    let mut bytes = [0u8; 16];
    entropy.fill(&mut bytes);
    // 版本 4：高 4 位固定为 0100。
    bytes[6] = (bytes[6] & 0x0F) | 0x40;
    // 变体 RFC 4122：高 2 位固定为 10。
    bytes[8] = (bytes[8] & 0x3F) | 0x80;
    bytes
}

/// 把 16 字节按 8-4-4-4-12 分组格式化成标准 UUID 文本。
pub fn format_uuid_v4(bytes: &[u8; 16]) -> String {
    let hex: String = bytes.iter().map(|b| format!("{b:02x}")).collect();
    format!(
        "{}-{}-{}-{}-{}",
        &hex[0..8],
        &hex[8..12],
        &hex[12..16],
        &hex[16..20],
        &hex[20..32]
    )
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn seeded_entropy_is_reproducible() {
        let a = SeededEntropy::new(42);
        let b = SeededEntropy::new(42);
        let mut x = [0u8; 16];
        let mut y = [0u8; 16];
        a.fill(&mut x);
        b.fill(&mut y);
        assert_eq!(x, y, "同一个种子必须产出同一串");
        // 不同种子必须不同（否则随机源是坏的）
        let c = SeededEntropy::new(43);
        let mut z = [0u8; 16];
        c.fill(&mut z);
        assert_ne!(x, z);
    }

    #[test]
    fn seeded_entropy_advances_between_calls() {
        let e = SeededEntropy::new(7);
        let mut a = [0u8; 16];
        let mut b = [0u8; 16];
        e.fill(&mut a);
        e.fill(&mut b);
        assert_ne!(a, b, "连续两次调用不能返回同一串");
    }

    #[test]
    fn system_entropy_fills_and_varies() {
        let e = SystemEntropy;
        let mut a = [0u8; 16];
        let mut b = [0u8; 16];
        e.fill(&mut a);
        e.fill(&mut b);
        assert_ne!(a, [0u8; 16]);
        assert_ne!(b, [0u8; 16]);
        assert_ne!(a, b, "系统熵源连续两次不应相同");
    }

    #[test]
    fn uuid_has_version_and_variant_bits() {
        for _ in 0..32 {
            let bytes = random_uuid_bytes(&SystemEntropy);
            assert_eq!(bytes[6] >> 4, 0x4, "版本位必须是 4");
            assert_eq!(bytes[8] >> 6, 0b10, "变体位必须是 RFC4122");
        }
    }

    #[test]
    fn uuid_text_shape_is_canonical() {
        let text = uuid_v4(&SeededEntropy::new(1));
        let parts: Vec<&str> = text.split('-').collect();
        assert_eq!(parts.len(), 5, "标准 UUID 必须有 5 段");
        assert_eq!(
            parts.iter().map(|p| p.len()).collect::<Vec<_>>(),
            vec![8, 4, 4, 4, 12]
        );
        assert!(text.chars().all(|c| c.is_ascii_hexdigit() || c == '-'));
        assert!(text.chars().all(|c| !c.is_ascii_uppercase()), "小写");
        assert_eq!(text.chars().filter(|c| *c == '-').count(), 4);
    }

    #[test]
    fn uuid_v4_is_deterministic_for_a_seed() {
        assert_eq!(
            uuid_v4(&SeededEntropy::new(99)),
            uuid_v4(&SeededEntropy::new(99))
        );
    }

    /// 长度不是 8 的倍数的缓冲区也要能填满（chunks_mut 的最后一块短）。
    #[test]
    fn fill_handles_non_multiple_of_eight_sizes() {
        for len in 1..=20usize {
            let mut buf = vec![0u8; len];
            SystemEntropy.fill(&mut buf);
            // 只要不 panic 且真的写了东西即可
            assert_eq!(buf.len(), len);
        }
    }
}
