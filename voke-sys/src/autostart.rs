//! 开机自启：写 `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`（阶段 2.2）。
//!
//! ## 为什么手写注册表而不加 crate
//!
//! 自启动只需要「在 `Run` 键下写一个 REG_SZ」，windows crate 已经把
//! `Reg*` 系列函数都导出来了，为这一件事再引一个依赖不划算
//! （`Win32_System_Registry` 这个 feature 就够了）。
//!
//! ## 为什么是 HKCU 而不是 HKLM
//!
//! `HKLM` 需要管理员权限（UAC 提权），而 voke 是**常驻 launcher**，装它不该
//! 要一次 UAC 弹窗。`HKCU` 只对当前用户生效且无需提权，正是这类工具该用的位置。
//!
//! ## 为什么不用 `RegCreateKeyExW`
//!
//! 它会把 `Win32_Security` 拉进依赖（`SECURITY_ATTRIBUTES` / `REG_CREATE_*`），
//! 而 `Run` 这个键在任何正常 Windows 上**本来就存在**，打开即可写值。真不存在时
//! 我们记 error 日志并返回 `false`（设置项会回弹），不去碰创建语义。
//!
//! ## 平台策略
//!
//! 整个模块 `#[cfg(windows)]`（在 `lib.rs` 里门控）：Linux 上根本不编译，
//! `cargo test -p voke-sys` 不受影响。

use windows::Win32::Foundation::{ERROR_FILE_NOT_FOUND, ERROR_SUCCESS};
use windows::Win32::System::Registry::{
    HKEY, HKEY_CURRENT_USER, KEY_READ, KEY_SET_VALUE, REG_SAM_FLAGS, REG_SZ, RegCloseKey,
    RegDeleteValueW, RegOpenKeyExW, RegQueryValueExW, RegSetValueExW,
};
use windows::core::HSTRING;

/// 自启动注册表键（相对 `HKEY_CURRENT_USER`）。
const RUN_KEY: &str = "Software\\Microsoft\\Windows\\CurrentVersion\\Run";

/// 值的名字。用 `voke` 而不是完整路径——`Run` 键的值名只要求在本键内唯一。
const VALUE_NAME: &str = "voke";

/// 自启动是否已开启（`Run` 键下存在 `voke` 这个值时即为真）。
///
/// 任何一步失败（打不开键 / 读不到值）都按「未开启」处理并记日志，
/// **不 panic**——设置页不能因为注册表只读就卡死。
pub fn is_enabled() -> bool {
    let Some(key) = open(KEY_READ) else {
        return false;
    };
    let enabled = read_value(key).is_some();
    unsafe {
        let _ = RegCloseKey(key);
    }
    enabled
}

/// 打开/关闭开机自启，返回是否成功。
///
/// - `on = true`：把 `voke` 的值写成 `"<当前 exe 全路径>"`（REG_SZ，带引号）。
/// - `on = false`：删除该值；值本来就不存在也算成功（幂等）。
///
/// 失败只记 error 日志（`ERROR_ACCESS_DENIED` 常见于安全软件拦截自启动写入）。
pub fn set_enabled(on: bool) -> bool {
    let Some(key) = open(KEY_SET_VALUE) else {
        return false;
    };
    let ok = if on {
        write_value(key)
    } else {
        delete_value(key)
    };
    unsafe {
        let _ = RegCloseKey(key);
    }
    ok
}

/// 打开 `Run` 键；失败记 error 并返回 `None`。
fn open(access: REG_SAM_FLAGS) -> Option<HKEY> {
    let subkey = HSTRING::from(RUN_KEY);
    let mut key = HKEY(std::ptr::null_mut());
    let ret = unsafe { RegOpenKeyExW(HKEY_CURRENT_USER, &subkey, None, access, &mut key) };
    if ret != ERROR_SUCCESS {
        log::error!("打开注册表 {RUN_KEY} 失败：{ret:?}");
        return None;
    }
    Some(key)
}

/// 读 `voke` 值的内容（UTF-16 → String）。读不到（不存在 / 类型异常）返回 `None`。
fn read_value(key: HKEY) -> Option<String> {
    let name = HSTRING::from(VALUE_NAME);

    // 第一遍只问长度：`lpdata` 传 None，`lpcbdata` 会被写成需要的字节数
    // （REG_SZ 的长度含结尾的 NUL）。
    let mut size: u32 = 0;
    let ret = unsafe { RegQueryValueExW(key, &name, None, None, None, Some(&mut size)) };
    if ret != ERROR_SUCCESS {
        if ret != ERROR_FILE_NOT_FOUND {
            log::error!("查询注册表值 {VALUE_NAME} 长度失败：{ret:?}");
        }
        return None;
    }
    if size == 0 {
        return None;
    }

    let mut buf = vec![0u8; size as usize];
    let ret = unsafe {
        RegQueryValueExW(
            key,
            &name,
            None,
            None,
            Some(buf.as_mut_ptr()),
            Some(&mut size),
        )
    };
    if ret != ERROR_SUCCESS {
        log::error!("读取注册表值 {VALUE_NAME} 失败：{ret:?}");
        return None;
    }

    Some(decode_utf16(&buf))
}

/// 把 UTF-16LE 字节（可能带结尾 NUL）解成 `String`。
fn decode_utf16(bytes: &[u8]) -> String {
    // `chunks_exact(2)` 切出成对的字节（尾部不成对的字节被丢弃，注册表长度
    // 算错时才会有，丢弃好过 panic）。`as_chunks` 要 1.88，这里 MSRV 是 1.87，
    // 故用更老的 `chunks_exact`。
    let units: Vec<u16> = bytes
        .chunks_exact(2)
        .map(|chunk| u16::from_ne_bytes([chunk[0], chunk[1]]))
        .collect();
    let end = units
        .iter()
        .position(|&unit| unit == 0)
        .unwrap_or(units.len());
    String::from_utf16_lossy(&units[..end])
}

/// 写 `voke` = `"<current_exe()>"`。
///
/// 必须加引号：路径里有空格时（绝大多数 `C:\Users\...` 没有，但
/// `C:\Program Files\...` 有）不加引号 `Run` 会把它拆成多个参数。
fn write_value(key: HKEY) -> bool {
    let Some(wide) = current_exe_command() else {
        log::error!("取当前 exe 路径失败，无法写入自启动");
        return false;
    };
    let name = HSTRING::from(VALUE_NAME);
    let ret = unsafe { RegSetValueExW(key, &name, None, REG_SZ, Some(&wide)) };
    if ret != ERROR_SUCCESS {
        log::error!("写入注册表值 {VALUE_NAME} 失败：{ret:?}");
        return false;
    }
    log::info!("开机自启已开启：{VALUE_NAME} = {}", decode_utf16(&wide));
    true
}

/// 删除 `voke` 值。值不存在（`ERROR_FILE_NOT_FOUND`）也算成功。
fn delete_value(key: HKEY) -> bool {
    let name = HSTRING::from(VALUE_NAME);
    let ret = unsafe { RegDeleteValueW(key, &name) };
    if ret != ERROR_SUCCESS {
        if ret == ERROR_FILE_NOT_FOUND {
            // 本来就没开：幂等地认为成功。
            return true;
        }
        log::error!("删除注册表值 {VALUE_NAME} 失败：{ret:?}");
        return false;
    }
    log::info!("开机自启已关闭");
    true
}

/// `"<current_exe()>"` 的 UTF-16LE 字节（含结尾 NUL）。
fn current_exe_command() -> Option<Vec<u8>> {
    let exe = std::env::current_exe().ok()?;
    Some(to_wide_bytes(&format!("\"{}\"", exe.display())))
}

/// UTF-16LE 编码并补结尾 NUL（REG_SZ 要求以 NUL 结束）。
fn to_wide_bytes(text: &str) -> Vec<u8> {
    let mut units: Vec<u16> = text.encode_utf16().collect();
    units.push(0);
    units.iter().flat_map(|unit| unit.to_ne_bytes()).collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    /// 自启动命令必须带引号：路径含空格时 `Run` 会按空格拆参数。
    #[test]
    fn command_is_quoted() {
        let bytes = to_wide_bytes("\"C:\\Program Files\\voke.exe\"");
        assert_eq!(decode_utf16(&bytes), "\"C:\\Program Files\\voke.exe\"");
    }

    /// REG_SZ 必须以 NUL 结尾，否则 Windows 读出来会带上垃圾字符。
    #[test]
    fn wide_bytes_are_null_terminated() {
        let bytes = to_wide_bytes("voke");
        assert_eq!(bytes.len(), 5 * 2);
        assert_eq!(&bytes[bytes.len() - 2..], &[0, 0]);
    }

    /// 解码要正确去掉结尾 NUL，且一个 UTF-16 单元 = 2 字节。
    #[test]
    fn decode_strips_trailing_nul() {
        let bytes = to_wide_bytes("C:\\Users\\voke.exe");
        assert_eq!(decode_utf16(&bytes), "C:\\Users\\voke.exe");
        // 只喂一个 NUL 也要能解成空串而不是 panic。
        assert_eq!(decode_utf16(&[0, 0]), "");
        // 奇数长度（不成对）的尾巴必须被丢弃，不能 panic。
        assert_eq!(decode_utf16(b"a\0b"), "a");
    }

    /// 中文路径走 UTF-16 也要原样往返。
    #[test]
    fn decode_roundtrips_non_ascii() {
        let path = "\"C:\\用户\\voke.exe\"";
        assert_eq!(decode_utf16(&to_wide_bytes(path)), path);
    }
}
