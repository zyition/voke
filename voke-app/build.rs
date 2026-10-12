//! 把 `assets/voke.ico` 编成 Windows 资源并链进 exe。
//!
//! ## 为什么在这里做
//!
//! exe 的图标（资源管理器/任务栏/Alt-Tab 显示的那个）只能来自**资源段**，
//! 运行期 `WM_SETICON` 补不上资源管理器这一路，所以必须在链接期塞进去。
//!
//! ## 为什么自己调 windres 而不是引 embed-resource/winresource
//!
//! 那两个 crate 在 `windows-msvc` 目标下要找 Windows SDK 的 `rc.exe`，
//! 而我们是 WSL 交叉编译（没有 `rc.exe`）。mingw 的
//! `x86_64-w64-mingw32-windres` 产出的是标准 COFF `.res`，直接调它最省事，
//! 也不引入构建依赖。
//!
//! ## msvc 为什么必须用 llvm-rc（不能用 windres）
//!
//! gpui-pre 会往链接里塞一个 `gpui.lib`——名字叫 .lib，**实质是装 manifest
//! 的 .res**（RT_MANIFEST，dpi/公共控件 v6 靠它）。lld-link 按**文件内容**
//! （不看扩展名）把它归到 `.res` 一类，然后有一条硬限制：
//!
//! ```text
//! lld-link: error: internal .obj file created from .res files:
//! more than one resource obj file not allowed
//! ```
//!
//! 只要链接里已经有一个"含资源段的对象文件"，再有 .res 就报这个错。而
//! `windres -O coff` 产的是**COFF 对象**不是真 .res（`-O res` 才是），
//! 正好撞上这条限制。所以 msvc 分支改用 `llvm-rc` 产 MSVC 格式的真 .res
//! ——lld 会把多个 .res 合并成一个资源树（manifest + 图标都在）。
//! gnu 分支走 ld，没有这条限制，继续用 windres 的 COFF 对象。
//!
//! ## 非 Windows 目标
//!
//! `cargo check --target x86_64-pc-windows-gnu` 之外的场景（Linux 上跑
//! `voke-core` 测试等）不走这条路径：CARGO_CFG_TARGET_OS 不是 windows 就直接返回。

use std::env;
use std::path::PathBuf;
use std::process::Command;

fn main() {
    // 只在 Windows 目标上做（Linux 上跑 core 测试时不该要求 windres 存在）。
    if env::var("CARGO_CFG_TARGET_OS").as_deref() != Ok("windows") {
        return;
    }

    let manifest_dir = PathBuf::from(env::var("CARGO_MANIFEST_DIR").expect("CARGO_MANIFEST_DIR"));
    // 图标放在 workspace 根的 assets/（与设计稿同源，三个 crate 共用）。
    let ico = manifest_dir.join("..").join("assets").join("voke.ico");
    println!("cargo::rerun-if-changed={}", ico.display());

    let out_dir = PathBuf::from(env::var("OUT_DIR").expect("OUT_DIR"));
    let rc_path = out_dir.join("voke-icon.rc");
    let res_path = out_dir.join("voke-icon.res");

    // 资源 id 用 1：Windows 取资源段里的第一个图标组，id 最小最稳。
    // 路径里的反斜杠要转义（.rc 是 C 风格字符串）。
    let ico_literal = ico
        .to_string_lossy()
        .replace('\\', "\\\\")
        .replace('\"', "\\\"");
    std::fs::write(&rc_path, rc_source(&ico_literal)).expect("写 .rc 失败");

    let is_msvc = env::var("CARGO_CFG_TARGET_ENV").as_deref() == Ok("msvc");

    // 两条链路用不同的资源编译器：
    // - msvc：`llvm-rc`（产 MSVC 格式 .res，**llvm-cvtres 只认这一种**；
    //   它要调 clang 做预处理，所以 PATH 里得有 clang）。
    // - gnu：`x86_64-w64-mingw32-windres`（产 GNU 格式 .res，ld 直接吃）。
    if is_msvc {
        run(
            "llvm-rc",
            &[
                "/fo",
                &res_path.to_string_lossy(),
                &rc_path.to_string_lossy(),
            ],
        );
    } else {
        run(
            "x86_64-w64-mingw32-windres",
            &[
                "-O",
                "coff",
                "-i",
                &rc_path.to_string_lossy(),
                "-o",
                &res_path.to_string_lossy(),
            ],
        );
    }

    // 只对 voke 这个 bin 生效（voke-app 里没有别的 bin，写全更明确）。
    println!("cargo::rustc-link-arg-bin=voke={}", res_path.display());
}

/// 生成 `.rc` 源：文件图标 + Windows 版本信息资源。
///
/// ## 为什么要 VERSIONINFO
///
/// 任务管理器「进程」列表的**名称列显示的就是 `FileDescription`**（不是文件名、
/// 也不是 exe 内嵌图标）。不写这一块时 Windows 会给出 `voke.exe` 之类的回退名，
/// 用户拍板要显示成 **Voke**。这里把任务管理器 / 属性对话框要用到的几项一次补齐。
///
/// 版本号统一取自 `CARGO_PKG_VERSION`（workspace 的 `version = "0.1.0"`）：
/// 字符串值用 `"0.1.0"`，`FILEVERSION` / `PRODUCTVERSION` 那两行要求 4 段数字、
/// 由 [`numeric_version`] 补零成 `0,1,0,0`。
///
/// 语言 / 代码页用 en-US(0x0409) + Unicode(1200)，故 `StringFileInfo` 的块键是
/// `"040904b0"`，`Translation` 也随之写 `0x409, 1200`（windres 与 llvm-rc 都认
/// 这套标准写法）。VERSIONINFO 的资源 id 也用 1：它与 `1 ICON` **类型不同**
/// （RT_VERSION vs RT_GROUP_ICON），同 id 不冲突。
fn rc_source(ico_literal: &str) -> String {
    let version = env::var("CARGO_PKG_VERSION").unwrap_or_else(|_| "0.0.0".to_string());
    let numeric = numeric_version(&version);
    format!(
        "1 ICON \"{ico_literal}\"\n\
         \n\
         1 VERSIONINFO\n\
         FILEVERSION {numeric}\n\
         PRODUCTVERSION {numeric}\n\
         FILEFLAGSMASK 0x3fL\n\
         FILEFLAGS 0x0L\n\
         FILEOS 0x40004L\n\
         FILETYPE 0x1L\n\
         FILESUBTYPE 0x0L\n\
         BEGIN\n\
             BLOCK \"StringFileInfo\"\n\
             BEGIN\n\
                 BLOCK \"040904b0\"\n\
                 BEGIN\n\
                     VALUE \"FileDescription\", \"Voke\"\n\
                     VALUE \"ProductName\", \"Voke\"\n\
                     VALUE \"FileVersion\", \"{version}\"\n\
                     VALUE \"ProductVersion\", \"{version}\"\n\
                     VALUE \"OriginalFilename\", \"voke.exe\"\n\
                 END\n\
             END\n\
             BLOCK \"VarFileInfo\"\n\
             BEGIN\n\
                 VALUE \"Translation\", 0x409, 1200\n\
             END\n\
         END\n"
    )
}

/// `"0.1.0"` → `"0,1,0,0"`：`FILEVERSION` 固定 4 段数字，不足补 0、多余的截断。
fn numeric_version(version: &str) -> String {
    let mut parts: Vec<u16> = version
        .split(|c: char| !c.is_ascii_digit())
        .filter(|s| !s.is_empty())
        .map(|s| s.parse().unwrap_or(0))
        .collect();
    parts.truncate(4);
    while parts.len() < 4 {
        parts.push(0);
    }
    parts
        .iter()
        .map(u16::to_string)
        .collect::<Vec<_>>()
        .join(",")
}

/// 跑一个外部工具，失败就把工具名 + 参数打成 panic（构建期缺工具要一眼看出来）。
fn run(program: &str, args: &[&str]) {
    match Command::new(program).args(args).status() {
        Ok(status) if status.success() => {}
        other => panic!("{program} {args:?} 失败：{other:?}（见本文件顶部文档的 PATH 前置要求）"),
    }
}
