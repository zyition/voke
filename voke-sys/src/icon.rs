//! 应用图标提取：`SHDefExtractIconW` / `IShellItemImageFactory` → 32×32 PNG 文件。
//!
//! ## 为什么单独做一个模块
//!
//! gpui 的 `img()` 元素能直接吃**文件路径**（`Resource::Path`，见
//! gpui-pre `elements/img.rs` 的 `ImageAssetLoader`），但**不能吃 HICON**。
//! 所以 sys 层负责把各种来源的图标统一落成 32×32 PNG，app 层只负责把路径交给
//! `img()`。这样"图标提取"的全部 unsafe 都留在 sys，app 层零 Win32。
//!
//! ## 提取链路：`路径,索引` 精确分支 → `IShellItemImageFactory` 为主，
//! `SHGetFileInfoW` 兜底
//!
//! 三条路各有覆盖盲区（Win11 实测 + 用户实测反馈）：
//!
//! | 来源 | `SHDefExtractIconW`（带索引 DLL） | `IShellItemImageFactory::GetImage` | `SHGetFileInfoW`+`DrawIconEx` |
//! |---|---|---|---|
//! | `.lnk` 的 `dll,-资源号`（管理工具类） | ✅ **开始菜单同款** | ❌ 只能拿到 DLL 通用图标 | ❌ 同上 |
//! | `.exe` / `.lnk` | — | ✅ 取**文件自身**图标 | ⚠️ 只能取注册表关联（配 `USEFILEATTRIBUTES` 时是通用 exe 图标） |
//! | `.msc` / `.cpl` / `.bat` 等关联目标 | — | ❌ 用户实测**白纸**（`DefaultIcon=%1` 指向无资源的文件自身，返回的是"成功但错"的通用文档占位图） | ⚠️ 同样退化到通用文档图标 |
//! | MSIX/Store 应用（AUMID） | — | ✅ | ❌ 返回 0（无图标） |
//! | `shell:::{GUID}`（文件资源管理器） | — | ✅ | ❌ |
//!
//! 所以分流规则：
//! 1. **spec 是 `路径,索引` 形态时优先走 [`icon_bgra_via_resource_dll`]**
//!    （`SHDefExtractIconW`，负索引 = 资源 ID，正索引 = 图标序号）。这是 `.lnk`
//!    的 `GetIconLocation` 给出的**精确坐标**——"管理工具"目录的条目
//!    （任务计划程序、计算机管理等 `.msc`）全靠它拿到开始菜单同款图标；
//! 2. 其余 spec 走 **`GetImage`**（exe/MSIX/shell GUID 全覆盖，且取到的就是文件
//!    自身图标）；
//! 3. `GetImage` 没产出且 spec 是文件系统路径时兜底 `SHGetFileInfoW`
//!    （`SHGFI_USEFILEATTRIBUTES` 不碰磁盘——网络/光驱路径不会阻塞批量扫描，
//!    只按扩展名查关联）。
//!
//! 两类 `.msc` 条目的兜底在 **spec 挑选期**就已完成（见
//! [`crate::apps::pick_icon_spec`]）：`.lnk` 带 `dll,-索引` 规格的走上面的 1；
//! 没有 IconLocation 的 `.msc` 目标换成 `mmc.exe`（`%SystemRoot%\System32\mmc.exe`）。
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
//! （`SHGetFileInfoW`+`DrawIconEx` 兜底路同样产出预乘数据：本模块早前的实测
//! 对比里，它与 `GetImage` 的输出逐像素差异 < 2.4/255。）

#[cfg(windows)]
use std::path::Path;

#[cfg(windows)]
use windows::Win32::Foundation::SIZE;
#[cfg(windows)]
use windows::Win32::Graphics::Gdi::{
    BI_RGB, BITMAP, BITMAPINFO, BITMAPINFOHEADER, CreateCompatibleDC, CreateDIBSection,
    DIB_RGB_COLORS, DeleteDC, DeleteObject, GetDC, GetDIBits, GetObjectW, HBITMAP, HDC, ReleaseDC,
    SelectObject,
};
#[cfg(windows)]
use windows::Win32::Storage::FileSystem::FILE_ATTRIBUTE_NORMAL;
#[cfg(windows)]
use windows::Win32::UI::Shell::{
    IShellItemImageFactory, SHDefExtractIconW, SHFILEINFOW, SHGFI_ICON, SHGFI_LARGEICON,
    SHGFI_USEFILEATTRIBUTES, SHGetFileInfoW, SIIGBF_BIGGERSIZEOK, SIIGBF_ICONONLY,
    SIIGBF_RESIZETOFIT,
};
#[cfg(windows)]
use windows::Win32::UI::WindowsAndMessaging::{DI_NORMAL, DestroyIcon, DrawIconEx, HICON};
// `Interface` 提供 `cast()`（IShellItem -> IShellItemImageFactory）；
// `HSTRING` 是宽字符路径参数（`Param<PCWSTR>`）的载体。
use windows::core::{HSTRING, Interface as _};

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
/// - 带资源索引的 DLL 规格：`C:\Windows\system32\miguiresource.dll,-202`
///   （`路径,索引` 形态，走 `SHDefExtractIconW` 精确提取，见
///   [`icon_bgra_via_resource_dll`]）
///
/// `cache_key` 是缓存文件名（由调用方从应用名/路径/spec 派生，须已是安全文件名），
/// `cache_dir` 是落盘目录（通常是进程工作目录下的 `icons/`）。
///
/// 提取链路（详见模块头部覆盖表）：`路径,索引` 形态走 `SHDefExtractIconW`；
/// 其余先 `IShellItemImageFactory`，文件系统路径 spec 没产出时兜底
/// `SHGetFileInfoW` 注册表关联。全部失败才返回 `None`（调用方清空 `icon`，
/// UI 回退占位图标）。
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
        // 诊断日志：spec 一并打出——排查"改了提取逻辑却不生效"时，
        // 缓存命中会让新逻辑整批短路，只看汇总行看不出来。
        log::debug!(
            "图标缓存命中（跳过提取）: spec={parsing_name} -> {}",
            out.display()
        );
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

/// 拿"图标来源规格"的预乘 BGRA 像素。
///
/// 分流（详见模块头部覆盖表）：
/// 1. spec 是 `路径,索引` 形态 → [`icon_bgra_via_resource_dll`]
///    （`SHDefExtractIconW` 按资源坐标精确提取）。**放在 `GetImage` 之前**：
///    带索引的 DLL 规格是 `.lnk` 给出的精确指令（开始菜单同款图标），
///    `GetImage` 对它只会"猜"出 DLL 的通用图标。只对该形态分流，
///    exe / ico / AUMID / `shell:::` 一律走原路；
/// 2. 其余 → 主路径 `IShellItemImageFactory`；
/// 3. 主路径没产出且 spec 是文件系统路径 → `SHGetFileInfoW` 注册表关联兜底。
#[cfg(windows)]
unsafe fn icon_bgra(parsing_name: &str) -> Option<Vec<u8>> {
    // 1. `路径,索引` 精确指令（`.lnk` 的 `GetIconLocation` 保留的资源坐标）。
    if crate::apps::parse_indexed_icon_spec(parsing_name).is_some() {
        return unsafe { icon_bgra_via_resource_dll(parsing_name) };
    }
    // 2. 主路径：一个 API 覆盖 exe / lnk / MSIX(AUMID) / shell:::{GUID}。
    if let Some(pixels) = unsafe { icon_bgra_via_shell_item(parsing_name) } {
        // 注意：主路径"成功"不等于图标正确——实测 .msc 这类关联目标会拿到
        // 通用文档占位图（Some）。诊断日志把 spec 打出来，便于从日志区分
        // "没走兜底"与"走了兜底但也没取到"。
        log::debug!("图标主路径产出: {parsing_name}");
        return Some(pixels);
    }
    log::debug!("图标主路径无产出: {parsing_name}");
    // 3. 兜底：只对**文件系统路径** spec 生效（`looks_like_filesystem_path` 是纯
    // 函数、任意平台可测）。AUMID / `shell:::` 不试——对它们 `SHGetFileInfoW`
    // 本来也拿不到（见模块头部覆盖表）。
    if crate::apps::looks_like_filesystem_path(parsing_name) {
        log::debug!("图标进入 SHGetFileInfoW 兜底: {parsing_name}");
        return unsafe { icon_bgra_via_file_info(parsing_name) };
    }
    log::debug!("图标无兜底来源（非文件系统路径 spec）: {parsing_name}");
    None
}

/// 精确分支：`路径,索引` 形态的 spec（`.lnk` 的 `GetIconLocation` 产出，如
/// `C:\Windows\system32\miguiresource.dll,-202`）走 `SHDefExtractIconW`
/// 按资源坐标提取（**负索引 = 资源 ID，正索引 = 图标序号**）。
///
/// 为什么优先于 `GetImage`：这正是 shell 解析注册表里 `shell32.dll,-22579`
/// 这类写法的既定 API，取到的就是开始菜单/资源管理器显示的那个图标；
/// `GetImage` 收不带索引的解析名，对 `.msc` 这类关联文档只会返回通用占位图。
///
/// **签名已核对**（windows-0.62.2 `Win32/UI/Shell/mod.rs:2904`，未凭记忆写）：
/// `SHDefExtractIconW(psziconfile: PCWSTR, iindex: i32, uflags: u32,
/// phiconlarge: Option<*mut HICON>, phiconsmall: Option<*mut HICON>,
/// niconsize: u32) -> HRESULT`。`uflags` 传 `0`：同文件 10436-10439 行的
/// `GIL_PERINSTANCE`(2) / `GIL_PERCLASS`(4) / `GIL_NOTFILENAME`(8) 均为
/// per-class/per-instance 提取语义，按资源 ID 精确提取不需要，实测调用方一律传 0。
/// 只需大图标（`hicon_to_bgra` 会画到 32×32），`phiconsmall` 传 `None`。
#[cfg(windows)]
unsafe fn icon_bgra_via_resource_dll(spec: &str) -> Option<Vec<u8>> {
    let (path, index) = crate::apps::parse_indexed_icon_spec(spec)?;
    let mut hicon = HICON::default();
    // SAFETY: path 转 HSTRING；hicon 是本函数栈上的合法出参；调用方已初始化 COM。
    let hr = unsafe {
        SHDefExtractIconW(
            &HSTRING::from(path),
            index,
            0,
            Some(&mut hicon),
            None,
            ICON_SIZE as u32,
        )
    };
    // HRESULT < 0 即失败（windows-core 的 `is_err`）；成功但句柄无效同样按失败处理。
    if hr.is_err() || hicon.is_invalid() {
        log::debug!("SHDefExtractIconW 未取到图标: {spec} (hr={hr:?})");
        return None;
    }
    log::debug!("SHDefExtractIconW 按资源索引取到图标: {spec}");
    // SAFETY: hIcon 由 SHDefExtractIconW 给出、所有权归我们，转换后必须配对释放。
    let pixels = unsafe { hicon_to_bgra(hicon) };
    // SAFETY: 与上面的取得配对（即使转换失败也要释放，绝不泄漏 HICON）。
    unsafe {
        let _ = DestroyIcon(hicon);
    }
    pixels
}

/// 用 `IShellItemImageFactory::GetImage` 拿预乘 BGRA 像素。
///
/// `SIIGBF_ICONONLY | SIIGBF_RESIZETOFIT | SIIGBF_BIGGERSIZEOK` 的组合
/// 是 `GetImage` 的推荐用法（对齐 PowerToys 与微软文档）：只取图标、
/// 缩放到请求尺寸、允许返回更大尺寸。
#[cfg(windows)]
unsafe fn icon_bgra_via_shell_item(parsing_name: &str) -> Option<Vec<u8>> {
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

/// 兜底：`SHGetFileInfoW` 按**注册表文件关联**取图标（HICON → 预乘 BGRA）。
///
/// 只由 [`icon_bgra`] 在"主路径没产出 + spec 是文件系统路径"时调用。三个
/// flag 的选择都有依据：
/// - `SHGFI_USEFILEATTRIBUTES`：**不碰磁盘**，只按扩展名查关联——网络/光驱
///   路径不会阻塞批量扫描（与 `resolve_lnk` 的 `SLR_NO_UI` 同款取舍）；
/// - `SHGFI_LARGEICON`：系统大图标（SM_CXICON，96dpi 下 32²），与
///   [`ICON_SIZE`] 匹配、免缩放；
/// - `SHGFI_ICON`：要 `SHFILEINFOW::hIcon`。
#[cfg(windows)]
unsafe fn icon_bgra_via_file_info(path: &str) -> Option<Vec<u8>> {
    let mut info = SHFILEINFOW::default();
    let flags = SHGFI_ICON | SHGFI_LARGEICON | SHGFI_USEFILEATTRIBUTES;
    // SAFETY: path 转 HSTRING；info 是本函数栈上的合法结构体，尺寸如实传入。
    let got = unsafe {
        SHGetFileInfoW(
            &HSTRING::from(path),
            FILE_ATTRIBUTE_NORMAL,
            Some(&mut info),
            core::mem::size_of::<SHFILEINFOW>() as u32,
            flags,
        )
    };
    // 返回 0 = 失败（无关联 / 路径不合法……）；hIcon 无效同样按失败处理。
    if got == 0 || info.hIcon.is_invalid() {
        log::debug!("SHGetFileInfoW 未取到图标（兜底）: {path}");
        return None;
    }
    log::debug!("SHGetFileInfoW 兜底取到图标: {path}");
    // SAFETY: hIcon 由 SHGetFileInfoW 给出、所有权归我们，转换后必须配对释放。
    let pixels = unsafe { hicon_to_bgra(info.hIcon) };
    // SAFETY: 与上面的取得配对（即使转换失败也要释放，绝不泄漏 HICON）。
    unsafe {
        let _ = DestroyIcon(info.hIcon);
    }
    pixels
}

/// 把 `HICON` 落成预乘 BGRA：画进自建的 32bpp top-down DIB，复用
/// [`hbitmap_to_bgra`] 的读取 + R/B 交换（全仓只有这一套像素转换）。
///
/// 为什么绕 `DrawIconEx` 而不是直接读 `GetIconInfo` 的 `hbmColor`：
/// `DrawIconEx` 对"带 alpha 通道的图标"和"老式 mask 图标"都能正确合成；
/// 直接读 `hbmColor` 遇到老式图标会丢掉透明度（`GetDIBits` 补不出 alpha）。
#[cfg(windows)]
unsafe fn hicon_to_bgra(hicon: HICON) -> Option<Vec<u8>> {
    // SAFETY: CreateCompatibleDC(None) 建内存 DC；失败返回无效句柄（下面校验）。
    let hdc = unsafe { CreateCompatibleDC(None) };
    if hdc.is_invalid() {
        log::debug!("CreateCompatibleDC 失败（图标兜底）");
        return None;
    }
    // 32bpp top-down DIB：CreateDIBSection 零初始化像素（全透明底），
    // DrawIconEx 画上去后交给 hbitmap_to_bgra 读（biHeight 取负 = top-down，
    // 行序与 gpui 期望一致，与 hbitmap_to_bgra 内部同款约定）。
    let info = BITMAPINFO {
        bmiHeader: BITMAPINFOHEADER {
            biSize: core::mem::size_of::<BITMAPINFOHEADER>() as u32,
            biWidth: ICON_SIZE,
            biHeight: -ICON_SIZE,
            biPlanes: 1,
            biBitCount: 32,
            biCompression: BI_RGB.0,
            ..Default::default()
        },
        ..Default::default()
    };
    let mut bits: *mut core::ffi::c_void = core::ptr::null_mut();
    // SAFETY: hdc 有效；info 已按 32bpp top-down 初始化；bits 只接收指针。
    let dib = unsafe { CreateDIBSection(Some(hdc), &info, DIB_RGB_COLORS, &mut bits, None, 0) };
    let Ok(dib) = dib else {
        log::debug!("CreateDIBSection 失败（图标兜底）");
        // SAFETY: 与 CreateCompatibleDC 配对。
        unsafe {
            let _ = DeleteDC(hdc);
        }
        return None;
    };
    // SAFETY: dib 是新建位图、尚未选入任何 DC；返回的旧对象稍后要换回去。
    let old = unsafe { SelectObject(hdc, dib.into()) };
    // SAFETY: hicon 由调用方校验有效；DI_NORMAL 正常绘制，目标尺寸即 ICON_SIZE。
    let drawn = unsafe { DrawIconEx(hdc, 0, 0, hicon, ICON_SIZE, ICON_SIZE, 0, None, DI_NORMAL) };
    // SAFETY: 先把 dib 换出 DC，下面的 DeleteObject 才合法。
    unsafe {
        SelectObject(hdc, old);
    }
    let pixels = if drawn.is_ok() {
        // SAFETY: dib 已被画出内容且不在任何 DC 里，可以读取。
        unsafe { hbitmap_to_bgra(dib) }
    } else {
        log::debug!("DrawIconEx 失败（图标兜底）");
        None
    };
    // SAFETY: 与 CreateDIBSection 配对释放。
    unsafe {
        let _ = DeleteObject(dib.into());
    }
    // SAFETY: 与 CreateCompatibleDC 配对释放。
    unsafe {
        let _ = DeleteDC(hdc);
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
        // `mut` 供下面 GetDIBits 的 `&mut info` 借用（只写头部、不重排字段，
        // 所以仍是单表达式构造，不触发 field_reassign_with_default）。
        let mut info = BITMAPINFO {
            bmiHeader: BITMAPINFOHEADER {
                biSize: core::mem::size_of::<BITMAPINFOHEADER>() as u32,
                biWidth: bm.bmWidth,
                // 负数 => top-down：行序与 gpui 期望一致，省掉一次翻转。
                biHeight: -bm.bmHeight,
                biPlanes: 1,
                biBitCount: 32,
                biCompression: BI_RGB.0,
                ..Default::default()
            },
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
