//! 应用图标提取：`IShellItemImageFactory` → 32×32 PNG 文件。
//!
//! ## 为什么单独做一个模块
//!
//! gpui 的 `img()` 元素能直接吃**文件路径**（`Resource::Path`，见
//! gpui-pre `elements/img.rs` 的 `ImageAssetLoader`），但**不能吃 HICON**。
//! 所以 sys 层负责把各种来源的图标统一落成 32×32 PNG，app 层只负责把路径交给
//! `img()`。这样"图标提取"的全部 unsafe 都留在 sys，app 层零 Win32。
//!
//! ## 为什么用 `IShellItemImageFactory` 而不是 `SHGetFileInfoW` + `DrawIconEx`
//!
//! 实测对比（Win11 2026-10，见 docs/HISTORY.md）：
//!
//! | 来源 | `SHGetFileInfoW`+`DrawIconEx` | `IShellItemImageFactory::GetImage` |
//! |---|---|---|
//! | `.exe` / `.lnk` / `.msc` | ✅ | ✅ |
//! | MSIX/Store 应用（AUMID） | ❌ 返回 0（无图标） | ✅ |
//! | `shell:::{GUID}`（文件资源管理器） | ❌ | ✅ |
//!
//! **关键实证**：Windows Terminal（MSIX）与控制面板（shell GUID）用
//! `SHGetFileInfoW` 完全拿不到图标，而 `GetImage` 全部成功。
//! 一个 API 覆盖全部来源，所以只用它。
//!
//! ## 像素格式（已用不变式实证，不是猜的）
//!
//! `GetImage` 返回的 HBITMAP 是 32 位 **预乘 BGRA**，正好等于 gpui atlas 期望的格式：
//! - 实证：对所有 32bpp 图标统计"是否有通道值 > alpha" ⇒ **0 次违反**
//!   （预乘的数学性质必须是 `R,G,B <= A`；straight alpha 会有大量违反）。
//! - 交叉验证：gpui 自己的 SVG 渲染走 tiny-skia（`Pixmap` 是预乘 RGBA），
//!   只做 R/B 交换（`swap_rgba_pa_to_bgra`，gpui-pre `color.rs:26`）就交给 atlas；
//!   atlas 用 `UpdateSubresource` 原样上传、blend state 是
//!   `SRC_ALPHA`/`INV_SRC_ALPHA`（`directx_renderer.rs:1500`）——这套只对预乘数据正确。
//!
//! 所以本模块**只做 R↔B 交换，不做预乘/反预乘变换**。
//! （注意 `SHGetFileInfoW`+`DrawIconEx` 那条路也是预乘，两者逐像素差异 < 2.4/255。）

#[cfg(windows)]
use std::path::Path;

#[cfg(windows)]
use windows::Win32::Foundation::SIZE;
#[cfg(windows)]
use windows::Win32::Graphics::Gdi::{
    BI_RGB, BITMAP, BITMAPINFO, BITMAPINFOHEADER, DIB_RGB_COLORS, DeleteObject, GetDC, GetDIBits,
    GetObjectW, HBITMAP, HDC, ReleaseDC,
};
#[cfg(windows)]
use windows::Win32::UI::Shell::{
    IShellItemImageFactory, SIIGBF_BIGGERSIZEOK, SIIGBF_ICONONLY, SIIGBF_RESIZETOFIT,
};
// `Interface` 提供 `cast()`（IShellItem -> IShellItemImageFactory）。
use windows::core::Interface as _;

/// 提取图标的边长（像素）。
///
/// 32 与结果列表行高 44px 匹配（行内图标按 16–20px 画，32 源图有 DPI 余量）。
/// 需要更高分辨率时改这一处即可（`GetImage` 会重新缩放）。
pub const ICON_SIZE: i32 = 32;

/// 图标的缓存文件路径（不管它存不存在）。
///
/// 给调用方一个"不碰 COM 就能算出来"的纯路径函数：窗口 provider 每次查询都要
/// 准备图标，而那时绝大多数图标已命中缓存——用本函数先查文件系统就能整个跳过
/// COM 作用域，不用为一批已缓存的图标每次进 `CoInitializeEx`。
pub fn icon_cache_path(cache_dir: &Path, cache_key: &str) -> std::path::PathBuf {
    cache_dir.join(format!("{cache_key}.png"))
}

/// 把"图标来源规格"提取成一张 32×32 PNG，返回 PNG 文件路径（失败返回 `None`）。
///
/// `parsing_name` 可以是（实测全部有效）：
/// - 真实文件路径：`C:\Program Files\VideoLAN\VLC\vlc.exe`
/// - MSIX 的 AUMID：`Microsoft.WindowsTerminal_8wekyb3d8bbwe!App`
/// - shell 解析名：`shell:::{52205FD8-5DFB-447D-801A-D0B52F2E83E1}`
/// - 图标来源规格：`%windir%\explorer.exe`（**调用方需先展开环境变量**）
///
/// `cache_key` 是缓存文件名（由调用方从应用名/路径派生，须已是安全文件名），
/// `cache_dir` 是落盘目录（通常是进程工作目录下的 `icons/`）。
///
/// # Safety
///
/// 调用线程必须已经初始化 COM（用 `crate::apps::with_com`）。
#[cfg(windows)]
pub unsafe fn extract_icon_png(
    parsing_name: &str,
    cache_dir: &Path,
    cache_key: &str,
) -> Option<std::path::PathBuf> {
    let out = icon_cache_path(cache_dir, cache_key);
    if out.is_file() {
        // 已提取过：缓存直接复用（图标极少变，不值得每次启动重算 100+ 张位图）。
        return Some(out);
    }

    // SAFETY: 调用方保证 COM 已初始化（见函数文档）。
    let bgra = unsafe { icon_bgra(parsing_name)? };
    if let Err(err) = std::fs::create_dir_all(cache_dir) {
        log::warn!("创建图标缓存目录失败 {}: {err}", cache_dir.display());
        return None;
    }
    match write_png(&out, &bgra, ICON_SIZE as u32, ICON_SIZE as u32) {
        Ok(()) => Some(out),
        Err(err) => {
            log::warn!("写图标 PNG 失败 {}: {err}", out.display());
            None
        }
    }
}

/// 用 `IShellItemImageFactory::GetImage` 拿预乘 BGRA 像素。
///
/// `SIIGBF_ICONONLY | SIIGBF_RESIZETOFIT | SIIGBF_BIGGERSIZEOK` 的组合
/// 是 `GetImage` 的推荐用法（对齐 PowerToys 与微软文档）：只取图标、
/// 缩放到请求尺寸、允许返回更大尺寸。
#[cfg(windows)]
unsafe fn icon_bgra(parsing_name: &str) -> Option<Vec<u8>> {
    // SAFETY: extract_icon_png 的文档要求调用方已初始化 COM。
    let shell_item = unsafe { crate::apps::shell_item_from_parsing_name(parsing_name) }.ok()?;
    let item: IShellItemImageFactory = shell_item.cast().ok()?;

    let flags = SIIGBF_ICONONLY | SIIGBF_RESIZETOFIT | SIIGBF_BIGGERSIZEOK;
    // SAFETY: item 有效；size/flags 是合法值。
    let hbitmap: HBITMAP = unsafe {
        item.GetImage(
            SIZE {
                cx: ICON_SIZE,
                cy: ICON_SIZE,
            },
            flags,
        )
    }
    .ok()?;
    if hbitmap.is_invalid() {
        return None;
    }
    // SAFETY: hbitmap 由 GetImage 返回，所有权归我们，函数结束前必须 DeleteObject。
    let pixels = unsafe { hbitmap_to_bgra(hbitmap) };
    // SAFETY: 与上面的 GetImage 配对释放。
    unsafe {
        let _ = DeleteObject(hbitmap.into());
    }
    pixels
}

/// 读 HBITMAP 的像素（32bpp，top-down）。
///
/// HBITMAP 可能是设备相关的 DIB（可能是 16/24/32 位），所以统一让
/// `GetDIBits` 做 32bpp 转换：`biBitCount = 32` + `BI_RGB`，
/// `biHeight` 取负数表示 top-down（与 gpui 期望的行序一致）。
#[cfg(windows)]
unsafe fn hbitmap_to_bgra(hbitmap: HBITMAP) -> Option<Vec<u8>> {
    // SAFETY: hbitmap 有效（调用方已校验）。
    unsafe {
        let mut bm = BITMAP::default();
        let got = GetObjectW(
            hbitmap.into(),
            core::mem::size_of::<BITMAP>() as i32,
            Some(&mut bm as *mut _ as *mut core::ffi::c_void),
        );
        if got == 0 || bm.bmWidth <= 0 || bm.bmHeight <= 0 {
            log::debug!(
                "GetObjectW 失败或尺寸非法: got={got} {}x{}",
                bm.bmWidth,
                bm.bmHeight
            );
            return None;
        }

        let width = bm.bmWidth as usize;
        let height = bm.bmHeight as usize;
        let mut info = BITMAPINFO::default();
        info.bmiHeader = BITMAPINFOHEADER {
            biSize: core::mem::size_of::<BITMAPINFOHEADER>() as u32,
            biWidth: bm.bmWidth,
            // 负数 => top-down：行序与 gpui 期望一致，省掉一次翻转。
            biHeight: -bm.bmHeight,
            biPlanes: 1,
            biBitCount: 32,
            biCompression: BI_RGB.0,
            ..Default::default()
        };

        let mut buf = vec![0u8; width * height * 4];
        let hdc: HDC = GetDC(None);
        if hdc.is_invalid() {
            return None;
        }
        let rows = GetDIBits(
            hdc,
            hbitmap,
            0,
            height as u32,
            Some(buf.as_mut_ptr() as *mut core::ffi::c_void),
            &mut info,
            DIB_RGB_COLORS,
        );
        ReleaseDC(None, hdc);
        if rows == 0 {
            log::debug!("GetDIBits 失败");
            return None;
        }

        // 只做 R↔B 交换（BGRA -> RGBA 字节序），**不做**预乘变换：理由见模块文档。
        for px in buf.chunks_exact_mut(4) {
            px.swap(0, 2);
        }
        Some(buf)
    }
}

/// 把 RGBA 像素写成 PNG。
///
/// 用 `image` crate 的 PNG 编码器（`image` 已经是 gpui-pre 的传递依赖且默认开 png，
/// 这里复用同一个版本，避免引入第二份图像栈）。
#[cfg(windows)]
fn write_png(path: &Path, rgba: &[u8], width: u32, height: u32) -> Result<(), String> {
    let file = std::fs::File::create(path).map_err(|e| e.to_string())?;
    let writer = std::io::BufWriter::new(file);
    let encoder = image::codecs::png::PngEncoder::new(writer);
    use image::ImageEncoder as _;
    encoder
        .write_image(rgba, width, height, image::ExtendedColorType::Rgba8)
        .map_err(|e| e.to_string())
}

/// 非 Windows 平台的占位：Linux 上跑 `cargo test --all-targets` 时也要能编译。
#[cfg(not(windows))]
pub const ICON_SIZE: i32 = 32;
