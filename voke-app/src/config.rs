//! 配置状态层（阶段 3：voke-app 配置状态层，§5 阶段 3）。
//!
//! 把磁盘上的 `config.toml`（路径由 `voke_sys::paths::config_path` 给出）与
//! 进程内 [`AppConfig`] 之间架起桥梁，并以 gpui `Global` 形式挂在 `App` 上，
//! 供设置窗口（阶段 4）与面板随时读写。
//!
//! 读写纪律（见 docs/HISTORY.md「写盘失败只记日志」）：
//! - 读盘失败 / 文件不存在 → 回落默认配置 + 日志，绝不 panic。
//! - 写盘失败 → 只 `log::error`，不回滚内存态、不 panic（设置页不能因磁盘
//!   只读就卡死）。
//! - 改完配置后统一 `commit` 落盘，并把新值 `apply_config` 下发到查询引擎。
//!
//! 注意：本模块在 `main.rs` 声明为顶层 `mod config`（见那里的说明），
//! 挂载点是 gpui 的全局状态，设置窗口与托盘命令都从这里读写。

#![allow(dead_code)]

use std::path::PathBuf;

use gpui_kit::Global;
use gpui_kit::component::theme::{Theme, ThemeMode as GpuiThemeMode};
use voke_core::config::{AppConfig, HotkeySpec, ThemeMode};
use voke_core::engine::{FrequencyStore, frequency_from_toml, frequency_to_toml};

/// 进程内的配置状态：内存中的 [`AppConfig`] + 落盘路径 + 脏标记。
///
/// 挂在 gpui `App` 的全局状态上（`impl Global`），通过
/// `cx.global::<VokeSettings>()` / `cx.global_mut::<VokeSettings>()` 访问。
pub struct VokeSettings {
    /// 当前生效的配置（设置窗口改的就是它）。
    pub config: AppConfig,
    /// 落盘路径（`%APPDATA%\voke\config.toml`），拿不到时 `None`。
    pub path: Option<PathBuf>,
    /// 内存态与磁盘是否一致（`commit` 成功后清掉，改配置后置位）。
    pub dirty: bool,
}

impl Global for VokeSettings {}

/// 读盘初始化：失败 / 不存在 → 默认配置 + 日志；`normalize` 后再挂全局。
///
/// 由 `main.rs` 在建搜索面板之前调用一次（`config::init(cx)`）。
///
/// **这里不下发引擎**：此刻 `ENGINE` 还没被 `search::set_providers` 注入，
/// `search::apply_config` 会静默跳过。真正生效的那一次是 provider 就绪后
/// `main.rs` 调的 [`apply_current`]。
pub fn init(cx: &mut gpui_kit::App) {
    let path = voke_sys::paths::config_path();
    let config = match &path {
        Some(p) => match std::fs::read_to_string(p) {
            Ok(text) => {
                // 坏 TOML / 缺字段都由 `AppConfig::from_toml` 内部回落默认。
                AppConfig::from_toml(&text)
            }
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => {
                log::info!("配置文件不存在，使用默认配置：{}", p.display());
                AppConfig::default()
            }
            Err(e) => {
                log::error!("读取配置失败，使用默认配置：{e}");
                AppConfig::default()
            }
        },
        None => {
            log::warn!("拿不到配置路径（非 Windows 或 APPDATA 缺失），使用默认配置");
            AppConfig::default()
        }
    };
    // `from_toml` 内部已 merge 默认并校验；这里再 normalize 一次兜底。
    let mut config = config;
    config.normalize();

    // 主题要在挂全局后立刻应用一次：`gpui_kit::init` 已经把主题初始化成浅色，
    // 用户配置的（默认「跟随系统」）主题必须在这一帧就生效。
    let theme = config.theme;

    cx.set_global(VokeSettings {
        config,
        path,
        dirty: false,
    });

    apply_theme(theme, cx);
}

/// 按主题模式切换全局配色。
///
/// `System` 现查一次系统深色状态（`voke_sys::is_system_dark`，即注册表的
/// `AppsUseLightTheme`）。切换走 gpui-component 的 [`Theme::change`]：它加载该模式
/// 注册的配色、重建 Base 投影（滚动条等），并 `refresh_windows()`——所以面板与设置
/// 两个窗口都会立即重绘，**不需要**调用方各自 notify。
pub fn apply_theme(mode: ThemeMode, cx: &mut gpui_kit::App) {
    let target = match mode {
        ThemeMode::Dark => GpuiThemeMode::Dark,
        ThemeMode::Light => GpuiThemeMode::Light,
        ThemeMode::System => {
            if voke_sys::is_system_dark() {
                GpuiThemeMode::Dark
            } else {
                GpuiThemeMode::Light
            }
        }
    };
    Theme::change(target, None, cx);
}

/// 读全局配置里的主题并应用（启动时兜底 / 需要按配置重切时调用）。
pub fn apply_current_theme(cx: &mut gpui_kit::App) {
    let mode = cx.global::<VokeSettings>().config.theme;
    apply_theme(mode, cx);
}

/// 系统主题变化回调（watcher 线程 → 主线程）：仅当模式为「跟随系统」时重查并热切换。
///
/// 用户显式选了深色 / 浅色时，系统主题变化与 voke 无关，直接忽略。
pub fn on_system_theme_changed(cx: &mut gpui_kit::App) {
    if cx.global::<VokeSettings>().config.theme == ThemeMode::System {
        apply_theme(ThemeMode::System, cx);
    }
}

/// 把当前内存配置重新下发到查询引擎（provider 的 enabled / is_global /
/// weight / prefix）。
///
/// 启动时必须在 `search::set_providers` **之后**调一次：provider 实例那时才建好，
/// 之前 `apply_config` 无处可落。设置页改完配置则由 [`commit`] 顺带下发，不必
/// 再调这里。
pub fn apply_current(cx: &gpui_kit::App) {
    let config = cx.global::<VokeSettings>().config.clone();
    crate::search::apply_config(&config);
}

/// 写盘 + 下发引擎：写盘失败只记 error，**不回滚内存态、不 panic**。
///
/// 设置页（阶段 4）的每个 setter 最后都调它：UI 改的是内存里的
/// [`VokeSettings::config`]，这里统一落盘并让引擎立刻看到新值。
pub fn commit(cx: &mut gpui_kit::App) {
    // 先拿出要写的内容（克隆，避免与下面的可变借用打架）。
    let (toml, path) = {
        let s = cx.global::<VokeSettings>();
        match s.config.to_toml() {
            Ok(t) => (t, s.path.clone()),
            Err(e) => {
                log::error!("配置序列化失败，未写入磁盘：{e}");
                return;
            }
        }
    };
    if let Some(p) = &path {
        if let Err(e) = std::fs::write(p, toml) {
            log::error!("配置写盘失败，内存态保持不变：{e}");
            return;
        }
    } else {
        log::warn!("配置路径为空，跳过写盘（内存态已更新）");
    }
    cx.global_mut::<VokeSettings>().dirty = false;
    // 写盘成功后把新配置下发到引擎。
    let cfg = cx.global::<VokeSettings>().config.clone();
    crate::search::apply_config(&cfg);
}

/// 读盘：使用频率数据（`frequency.toml`）→ 频率表。
///
/// **`set_frequency` 以前没有任何调用点**，等于"越用越准"只在单次会话里有效、
/// 一重启就归零。这里补上加载侧：坏文件 / 不存在一律回落空表 + 日志
/// （与配置同一套"读失败不 panic"纪律），由下次执行重写。
pub fn load_frequency() -> FrequencyStore {
    let Some(path) = voke_sys::paths::frequency_path() else {
        log::warn!("拿不到频率数据路径（非 Windows 或 APPDATA 缺失），本次不加载");
        return FrequencyStore::new();
    };
    match std::fs::read_to_string(&path) {
        Ok(text) => frequency_from_toml(&text),
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => {
            log::info!("频率数据不存在，从零开始：{}", path.display());
            FrequencyStore::new()
        }
        Err(e) => {
            log::error!("读取频率数据失败，本次按空表处理：{e}");
            FrequencyStore::new()
        }
    }
}

/// 写盘：把当前频率表落回 `frequency.toml`。
///
/// 每次记录使用后就写（一次执行动作才写一次，频率很低，文件也小），不做节流：
/// 退出时不保证还有机会 flush，丢了比多写一次更糟。写失败只记日志。
pub fn save_frequency(store: &FrequencyStore) {
    let Some(path) = voke_sys::paths::frequency_path() else {
        log::warn!("拿不到频率数据路径，跳过写盘（内存态保留）");
        return;
    };
    match frequency_to_toml(store) {
        Ok(text) => {
            if let Err(e) = std::fs::write(&path, text) {
                log::error!("频率数据写盘失败，内存态保持不变：{e}");
            }
        }
        Err(e) => log::error!("频率数据序列化失败，未写入磁盘：{e}"),
    }
}

impl VokeSettings {
    /// 当前热键的语义拆解（解析 `config.hotkey`）。
    pub fn hotkey_spec(&self) -> Option<HotkeySpec> {
        HotkeySpec::parse(&self.config.hotkey)
    }

    /// 设定新热键（写热键串 + 标脏）。
    pub fn set_hotkey(&mut self, spec: &HotkeySpec) {
        self.config.hotkey = spec.to_string();
        self.dirty = true;
    }

    /// 是否开机自启。
    pub fn autostart(&self) -> bool {
        self.config.autostart
    }

    /// 设定是否开机自启（标脏，由 `commit` 落盘）。
    pub fn set_autostart(&mut self, on: bool) {
        self.config.autostart = on;
        self.dirty = true;
    }

    /// 当前主题模式。
    pub fn theme(&self) -> ThemeMode {
        self.config.theme
    }

    /// 设定主题模式（标脏，由 `commit` 落盘；配色切换另走 [`apply_theme`]）。
    pub fn set_theme(&mut self, mode: ThemeMode) {
        self.config.theme = mode;
        self.dirty = true;
    }
}
