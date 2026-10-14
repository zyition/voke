# 设置页 / 插件配置 / 面板快捷键 / 托盘 / 开机自启 —— 实施文档

> 给后续实现 agent（含 multitask 子代理）的**唯一施工依据**。
> 所有 API 已对源码核实过（`gpui-component-0.7.1` / `gpui-pre-0.3.8` /
> `gpui-base-0.7.1` / `windows-0.62.2` 的 registry 源码）。
>
> **硬约束（AGENTS.md）**：严禁编造 API；保持分层（core 零 gpui/windows，
> OS 调用进 sys）；每完成一块 `git commit` + 回写 `docs/BACKLOG.md`；简体中文注释与文档。

---

## 0. 已确认的决策（用户拍板，不要擅自改）

| 项 | 决定 |
|---|---|
| 设置页载体 | **独立设置窗口**（`gpui_kit::open_window` 新开 880×600 普通窗口，带标题栏、可缩放、不自动隐藏；搜索面板完全不动） |
| 面板键位 | **最小集**：`↑↓` 主导航 + `Ctrl+N/P` + `Tab/Shift+Tab` 等价别名 + `Esc`/`Enter`/`Backspace` 现状不变。**不做** Alt+数字直达、**不做** Ctrl+U 等编辑键 |
| 托盘 | **引入 `tray-icon` crate**（target 门控，仅 Windows）；事件靠自建消息线程泵 |
| 自启动 | **手写注册表** `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`（不加新 crate） |

---

## 0.1 进度快照（2026-10-11）

| 阶段 | 内容 | 状态 |
|---|---|---|
| 阶段 0 | 路径绝对化（`voke-sys/src/paths.rs` + `main.rs` 日志 + `icons.rs` 缓存目录） | ✅ 已提交 `对应提交` |
| 阶段 1 | `voke-core` 配置内核 | ✅ 已提交 `对应提交` |
| 阶段 2 | `voke-sys` 热键可控化 + 自启动 + 托盘 | ✅ 已提交 `对应提交` |
| 阶段 3 | `voke-app` 配置状态层 + 面板键位/文案 | ✅ 已提交 `对应提交` |
| 阶段 4 | 设置窗口 UI | ✅ 已提交 `对应提交` |
| 阶段 5 | 文档同步与提交 | ✅ 本轮（本 commit） |
| 后续 | 前缀激活可配——每插件 Direct/Indirect（设置页下拉） | ✅ `对应提交` |
| 后续 | 深色模式（设置页「外观」主题下拉，跟随系统/深色/浅色） | ✅ `对应提交` |
| 后续 | 应用列表改「唤起触发扫描 + 60s 防抖」，去掉 30s 后台轮询 | ✅ `对应提交` |
| 后续 | 托盘子类化即时刷新 + awake 状态条目（`SetWindowSubclass`/`WM_INITMENUPOPUP`） | ✅ `对应提交`→`对应提交` |
| 后续 | 单实例保护 + 默认热键改 `alt+space` + exe 元数据（Voke） | ✅ `对应提交` |
| 后续 | Windows 安装器（Inno Setup 7.1.0）+ WSL 打包脚本 | ✅ `对应提交` / `对应提交` |
| 后续 | 应用显示名统一「Voke」 | ✅ `对应提交` |
| 后续 | 设置页热键录制隔离 + 注册失败回退 | ✅ `对应提交` |

> 上表 `阶段 0–5` 是本文档原计划的 5 阶段；「后续」各行是**不在原计划内**、同日追加落地的
> 工作（逐条结论见 `docs/BACKLOG.md`「已完成」节，状态/测试数快照见 `docs/HISTORY.md`
> 尾部「2026-10-11 后半段」）。

阶段 0 已做的具体改动（`git show <commit>`）：
- `voke-sys/src/paths.rs`：`app_dir()`（`%APPDATA%\voke`）/ `config_path()` / `log_path()` /
  `icon_dir()`；非 Windows 全返回 `None`（保持 `cargo test -p voke-sys` 在 WSL 可跑）
- `voke-app/src/main.rs` `init_logging()`：专用目录 → 工作目录 → 无文件日志，**逐级降级不 panic**
- `voke-app/src/icons.rs` `icon_cache_dir()`：优先 `%APPDATA%\voke\icons`，取不到才回工作目录
- `voke-sys` 58 项测试绿（+2）；`-p voke-app --all-targets` gnu 交叉 check 通过

---

## 0.2 子代理任务切分建议（multitask 用）

依赖链：**阶段 1 完成后，阶段 2 与阶段 3 可并行**（3 依赖 1 的 `AppConfig`；2 只依赖
`HotkeySpec` 与系统 API）。阶段 4 依赖 2+3。阶段 5 每阶段收尾各做一次。

| 子代理 | 任务 | 依赖 | 可并行 |
|---|---|---|---|
| A | 阶段 1（core 配置内核） | 无 | 与 B 并行 |
| B | 阶段 2（sys 热键/自启/托盘） | 需 `HotkeySpec` 定义，可与 A 约定好签名先写 | 与 A 并行 |
| C | 阶段 3（app 配置层 + 面板键位/文案） | A | A 完成后 |
| D | 阶段 4（设置窗口 UI） | A+B+C | 最后 |

> **派工模板**：先读 `docs/HISTORY.md`（环境事实）+ `docs/BACKLOG.md`（进度）+
> **本文档对应阶段章节**，完成后 `git commit` 并更新 `docs/BACKLOG.md`。
> API 不确定就查 `~/.cargo/registry/src/` 源码，**不要靠猜**。

---

## 1. 现状速览（改动落点）

- `voke-app/src/search.rs`：UI 主体（gpui 声明式，非自绘）。常量
  `ROW_HEIGHT/HINT_ROW_HEIGHT/ROW_ICON_SIZE/BADGE_SIZE/MAX_VISIBLE_ROWS/PICKER_PLACEHOLDER/CHROME_HEIGHT`
  在 `search.rs:126-147`；`window_size()` 在 `search.rs:1501`；底部提示条文案在
  `search.rs:1251-1259`；键盘处理 `on_key` 在 `search.rs:1158`（`:1160` 直接放行所有带
  修饰键的按键）；`active_prefix: Option<&'static str>` 在 `search.rs:366`。
- `voke-core/src/provider.rs:180-206`：`ProviderConfig { enabled, is_global, weight: f32, prefix: Option<&'static str> }`。
- `voke-core/src/engine.rs:34-38`：前缀常量；`prefix_owner` `:176-185`（最长匹配优先）；
  `rank` `:367-390`（`final_score = weight × score × boost`）；`aggregate` `:401-426`
  （**组间顺序 = 注册顺序，权重只影响组内**）。
- `voke-app/src/search.rs:199-270`：10 个 provider 的静态注册（顺序即组序）。
- 配置持久化：**目前完全没有**。`voke-sys/src/hotkey.rs` 是单热键、不可改键。
- 托盘：**零代码**。

---

## 2. 键位设计（最终）

| 键 | 行为 | 备注 |
|---|---|---|
| `↑` / `↓` | 上/下一项 | 主导航，保留 |
| `Ctrl+N` / `Ctrl+P` | 等价 `↓` / `↑` | gpui InputState 未占用这两个键（`gpui-base-0.7.1/src/input/base/state.rs` 只绑了 a/c/e/f/h/v/x/z/left/right/home/end/backspace/delete/… ） |
| `Tab` / `Shift+Tab` | 等价 `↓` / `↑` | 已核实：gpui-pre / gpui-base / gpui-component **均未**给 Tab 注册 KeyBinding（`KeyBinding::new("tab", …)` 只出现在测试代码里），`on_key_down` 能可靠收到 `keystroke.key == "tab"` |
| `Enter` | 执行/进入子模式 | 现状 |
| `Esc` | 退一级 → 到顶隐藏窗口 | 现状 |
| `Backspace`（输入为空） | 退一级 | 现状 |
| `Ctrl+,` | **打开设置窗口** | 新增，VS Code 约定 |

实现位置：`search.rs:1158 on_key`。把 `:1160` 的「带修饰键一律 return」改成显式分派：

```rust
let m = &event.keystroke.modifiers;
if m.control && !m.alt && !m.platform && event.keystroke.key == "n" { self.move_selection(1, cx); return; }
if m.control && !m.alt && !m.platform && event.keystroke.key == "p" { self.move_selection(-1, cx); return; }
if m.control && event.keystroke.key == "," { crate::settings::open(cx); return; }
if m.control || m.alt || m.platform || m.function { return; }   // 其余组合键一律放行
match event.keystroke.key.as_str() { "tab" => …, "up" => …, … }
```

> ⚠️ `Ctrl+,` 的 `keystroke.key` 字符串实现时先打一行 debug 日志确认（gpui 对标点键的
> `key` 取值）；**不要猜**。

---

## 3. UI 文案精简（用户明确要求）

| 位置 | 现在 | 改后 |
|---|---|---|
| 全局占位符 `voke-core/src/provider.rs:212` | `搜索应用/窗口，=计算 <计算式>` | `搜索应用、窗口` |
| 底部提示条（全局分支，`search.rs:1258`） | `↑↓ 选择 · Enter 执行 · Esc 隐藏 · # 插件列表（时间戳/base64/计算器…） · Ctrl+Alt+Space 唤起` | `↑↓ 选择 · Enter 执行 · Esc 隐藏 · # 插件列表` |
| 底部提示条（插件/子模式/picker 三个分支，`search.rs:1252-1256`） | 各自一条长文案（都含硬编码 `Ctrl+Alt+Space`） | 统一成 `↑↓ 选择 · Enter 执行 · Esc 返回`（唤醒键移到托盘 tooltip 与空状态，不再硬编码在 UI 里） |
| 空状态「开始输入以搜索」的 detail（`search.rs:948-953`） | `支持应用名、拼音全拼与首字母；可切换 N 个已打开窗口` | `已索引 {apps} 个应用 · {windows} 个窗口` |
| 空状态「没有匹配」的 detail（`search.rs:957`） | `换个关键词，或改用拼音首字母；也可用前缀进入插件` | `换个关键词，或用 # 打开插件列表` |

底部提示条高度与 `CHROME_HEIGHT`（`search.rs:147`，现 88px）**保持不变**（只是文案变短）。

---

## 4. 配置模型

落盘：`%APPDATA%\voke\config.toml`（**必须绝对路径**；启动早期 `fix_own_cwd` 已把
System32 类异常 cwd 修正到 `%USERPROFILE%`，绝对路径为防御性兜底，见 §5.0）。

```toml
hotkey = "ctrl+alt+space"
autostart = false

[providers.apps]
enabled = true
is_global = true
weight = 1.2
prefix = ""          # 空串 = 无别名

[providers.terminal]
enabled = true
is_global = true
weight = 1.0
prefix = "_"         # 下划线激活 WT profiles

[providers.windows]
prefix = "<"         # 窗口切换器

[providers.calculator]
enabled = true
is_global = false
weight = 1.0
prefix = "="
```

默认值（必须与各 provider `new()` 里的硬编码一致，用测试锁死）：

| id | enabled | is_global | weight | prefix |
|---|---|---|---|---|
| apps | ✓ | ✓ | 1.2 | — |
| windows | ✓ | ✓ | 1.0 | — |
| awake | ✓ | ✓ | 1.0 | — |
| terminal | ✓ | ✓ | 1.0 | — |
| workspaces | ✓ | ✓ | 1.0 | — |
| calculator | ✓ | ✗ | 1.0 | `=` |
| timestamp | ✓ | ✗ | 1.0 | `#` |
| base64 | ✓ | ✗ | 1.0 | `#b` |
| uuid | ✓ | ✗ | 1.0 | `#u` |
| random | ✓ | ✗ | 1.0 | `#r` |

校验规则（`AppConfig::normalize()`）：
- `weight` clamp 到 `0.0..=3.0`；
- `prefix` 不能含空白、不能为空串以外的 0 长度（空串 = 无）、**不能以 `#` 开头**
  （`#` 是插件列表专用入口，会被 `picker_filter` 抢先拦截，`search.rs:304`）；
- `prefix` 全局唯一（重复时保留先出现者，后者置空并记日志）；
- `hotkey` 必须能被 `HotkeySpec::parse` 解析；
- 缺失的 provider 用默认值补齐（部分配置必须能工作）。

---

## 5. 实施阶段

### 阶段 0：路径绝对化 ✅ 已完成（commit `对应提交`）

**原问题**：`main.rs:484` `File::create("voke.log").expect(...)` 用 CWD；自启动后 CWD 可能是
`C:\Windows\system32`，创建失败直接 panic。`icons.rs:41` 的 `icons/` 同理。

已落地：`voke-sys/src/paths.rs`（`app_dir` / `config_path` / `log_path` / `icon_dir`）、
`main.rs init_logging()` 三级降级、`icons.rs icon_cache_dir()` 改绝对路径。
（注：启动早期 `fix_own_cwd` 现已把 System32 类异常 cwd 在进程层面修正到
`%USERPROFILE%`，绝对路径是防御性兜底。）

**验收（已过）**：`cargo check --target x86_64-pc-windows-gnu -p voke-app --all-targets` 通过；
`cargo test -p voke-sys` 58 绿。

---

### 阶段 1：voke-core 配置内核（纯 Rust，Linux 可测）

**新建 `voke-core/src/config.rs`**
```rust
pub struct AppConfig { pub hotkey: String, pub autostart: bool, pub providers: BTreeMap<String, ProviderSettings> }
pub struct ProviderSettings { pub enabled: bool, pub is_global: bool, pub weight: f32, pub prefix: Option<String> }
pub struct HotkeySpec { pub ctrl: bool, pub alt: bool, pub shift: bool, pub win: bool, pub key: String }
```
- 全部 `#[derive(Serialize, Deserialize, Clone, Debug, PartialEq)]` + `#[serde(default)]`
  （voke-core 已有 serde/toml 依赖，`voke-core/Cargo.toml:13,16`）。
- `AppConfig::default()` 给出上表默认值；`fn from_toml(&str) -> AppConfig`、
  `fn to_toml(&self) -> Result<String, String>`；`fn normalize(&mut self)` 做校验 + 回填缺失 provider。
- 读盘走 `ConfigPatch`（每字段 `Option`）与默认值逐字段合并，保证「只写一项也能工作」。
- `HotkeySpec::parse("ctrl+alt+space") -> Option<Self>` / `Display`（输出规范串，用于回填 config）。
- 纯函数单测放文件末尾 `#[cfg(test)]`：解析、默认值、校验、缺字段兼容、坏 TOML 回落。

**改 `voke-core/src/provider.rs`**
- `ProviderConfig.prefix: Option<&'static str>` → `Option<String>`（配置化必须的改动）。
- trait 增加 `fn set_config(&mut self, config: ProviderConfig) { let _ = config; }`
  （默认空实现，避免打散 core 测试里的 fake provider）。

**改 10 个 provider**：各自加 `set_config` 覆盖（`self.config = config;`），并把
`prefix: Some(PREFIX_BASE64)` 改成 `.to_string()`。
涉及：`apps.rs:109`、`windows.rs:96-103`、`awake.rs:115-121`、`terminal.rs:241-248`、
`workspace.rs:891-897`、`calculator.rs:61-67`、`timestamp.rs:165-171`、`base64.rs:77-82`、
`uuid.rs:48-53`、`random.rs:124-129`。

**改 `voke-core/src/engine.rs`**
- `prefix_owner:176-185` / `:239` / `:301` / `:334-335` 的 `p.config().prefix` 改 `.as_deref()`。
- 新增 `pub fn apply_config(&mut self, cfg: &AppConfig)`：遍历 `providers.iter_mut()`，
  按 id 取 `ProviderSettings`，构造 `ProviderConfig` 调 `set_config`。
- 新增测试 `all_providers_honor_set_config`：把 10 个真实 provider 建出来，
  `set_config(enabled=false)`，逐个断言生效（防漏改）。

**验收**：`cargo test -p voke-core` 全绿（现 241 项，新增若干）。

---

### 阶段 2：voke-sys（Windows 系统能力）

1. **改 `voke-sys/src/hotkey.rs`：可控热键**
   ```rust
   pub struct HotkeyHandle { thread_id: u32, pending: Arc<Mutex<Hotkey>> }
   impl HotkeyHandle { pub fn set(&self, hotkey: Hotkey) }   // 写 pending + PostThreadMessageW(tid, WM_APP_RECONFIG)
   pub fn spawn_hotkey(initial: Hotkey, on_press: OnPress) -> Option<HotkeyHandle>
   ```
   线程循环里新增 `WM_APP_RECONFIG`（`WM_USER + 1`）分支：`UnregisterHotKey` → 读 `pending`
   → `RegisterHotKey`；注册失败记 error 日志但**线程不退出**（允许改成一个仍失败的热键）。
   `spawn_default_hotkey` 保留但改为转发到新函数；`lib.rs:57` 的 re-export 同步更新。
   `PostThreadMessageW` 已确认存在于
   `windows-0.62.2/src/Windows/Win32/UI/WindowsAndMessaging/mod.rs:1872`。

2. **新建 `voke-sys/src/autostart.rs`（`#[cfg(windows)]`）**
   - `pub fn is_enabled() -> bool`、`pub fn set_enabled(on: bool) -> bool`
   - `HKCU\Software\Microsoft\Windows\CurrentVersion\Run` + value `"voke"` = `"\"<current_exe()>\""`；`REG_SZ`。
   - 只用 `RegOpenKeyExW`(:376) / `RegSetValueExW`(:626) / `RegDeleteValueW`(:211) /
     `RegQueryValueExW`(:489) / `RegCloseKey`(:12)——**不用 `RegCreateKeyExW`**，避免引入
     `Win32_Security` 依赖。
   - `Cargo.toml` 给 windows crate 加 feature `"Win32_System_Registry"`
     （`windows-0.62.2/Cargo.toml:629` 已确认存在）。

3. **新建 `voke-sys/src/tray.rs`（`#[cfg(windows)]`）**
   ```rust
   pub enum TrayCommand { ShowPanel, OpenSettings, Quit }
   pub fn spawn_tray(icon: tray_icon::Icon, on_cmd: Arc<dyn Fn(TrayCommand) + Send + Sync>)
   ```
   - `voke-sys/Cargo.toml` 加：
     ```toml
     [target.'cfg(windows)'.dependencies]
     tray-icon = { version = "0.26", default-features = false }
     ```
     （`default-features = false` 关掉 `libappindicator`/GTK，否则 WSL 原生
     `cargo test -p voke-sys` 会去编 GTK；再叠加 target 门控，Linux 上根本不解析这个依赖。）
   - **必须先读源码再写**：`tray-icon` 官方文档明确 *"On Windows and the Linux/BSD
     AppIndicator backend, an event loop must be running on the thread"*。所以：在
     **专用 std::thread** 里创建 `TrayIcon`，然后跑
     `GetMessageW`/`TranslateMessage`/`DispatchMessageW` 循环（与现有 hotkey 线程同款），
     每轮循环后 `TrayIconEvent::receiver().try_recv()` + `MenuEvent::receiver().try_recv()`
     转成 `TrayCommand` 调 `on_cmd`。
   - 拉取依赖后**立刻**查 `~/.cargo/registry/src/*/tray-icon-0.26*/src/` 确认
     `TrayIconBuilder` 的方法名（`with_icon` / `with_tooltip` / `with_menu` /
     `with_menu_on_left_click`）与 muda 菜单构造（`MenuItem::with_id`、
     `PredefinedMenuItem::separator`、`MenuEvent.id`），**不要凭记忆写**。
   - 菜单项：`显示面板` / `设置` / `---` / `退出 voke`。
   - 图标来源：`assets/voke-icon-final.png`（41KB，现存但未被引用）→ 用 `image` crate
     （sys 已有该依赖）解码成 RGBA → `tray_icon::Icon::from_rgba`。注意 `from_rgba` 要求
     **RGBA 顺序**（不是 icon.rs 里那种），实现时核对。

4. **`voke-sys/src/lib.rs`**：按现有风格加 `#[cfg(windows)] pub mod tray;` /
   `pub mod autostart;` / `pub mod paths;` 与 re-export。

**验收**：`cargo check --target x86_64-pc-windows-gnu -p voke-sys --all-targets` 通过；
`cargo test -p voke-sys` 在 WSL 仍全绿（现 58 项，新模块被 cfg 挡掉不参与）。

---

### 阶段 3：voke-app 配置状态层

**新建 `voke-app/src/config.rs`**
```rust
pub struct VokeSettings { pub config: AppConfig, pub path: Option<PathBuf>, pub dirty: bool }
impl gpui_kit::Global for VokeSettings {}     // gpui::Global 已由 gpui_kit::* 导出
```
- `pub fn init(cx: &mut App)`：读盘（失败 → 默认 + 日志）→ `cx.set_global(...)` → 调
  `search::apply_config`。
- `pub fn commit(cx: &mut App)`：写盘（`toml::to_string` + `fs::write`）→
  `search::apply_config(&cfg)`。
- `pub fn hotkey_spec(&self) -> Option<HotkeySpec>`、`pub fn set_hotkey(...)` 等便捷读写。
- 写盘失败只记 error，**不 panic、不回滚内存态**（设置页不能因为磁盘只读就卡死）。

**新建 `voke-app/src/settings.rs`**：见阶段 4。

**改 `voke-app/src/search.rs`**
- `active_prefix: Option<&'static str>` → `Option<String>`（`search.rs:366`；
  `:757-771` 取 `provider.config().prefix.clone()`；`:1196` 改 `.as_deref().unwrap_or("")`）。
- 新增 `pub fn apply_config(cfg: &AppConfig)`：锁 `ENGINE` 调 `QueryEngine::apply_config`。
- `on_key` 按 §2 改。
- 文案按 §3 改。
- `Ctrl+,` → `crate::settings::open(cx)`。

**改 `voke-app/src/main.rs`**
- 起托盘：`voke_sys::spawn_tray(...)`（Windows）+ 事件 channel → `cx.spawn` 消费：
  - `ShowPanel` → 与热键相同的 toggle 逻辑（把现有热键回调体抽成
    `fn toggle_panel(cx: &mut App)` 复用）；
  - `OpenSettings` → `settings::open(cx)`；
  - `Quit` → `cx.quit()`（已确认 `gpui-pre-0.3.8/src/app.rs:1160 pub fn quit(&self)`）。
- 热键：`spawn_hotkey(初始 spec, ...)` 拿 `HotkeyHandle`，存进 `Shell`；设置页改键时
  `handle.set(...)`。
- `Shell` 增加字段 `hotkey: Option<HotkeyHandle>`、`settings_window: Option<AnyWindowHandle>`。
- 启动时按配置 `autostart` 无需做事（注册表已由系统拉起）；但要在日志里打印自启动状态。

---

### 阶段 4：设置窗口 UI

**`voke-app/src/settings.rs`**
```rust
pub fn open(cx: &mut App)            // 幂等：已开则 activate，未开则 open_window
pub struct SettingsView { hotkey_focus: FocusHandle, _hotkey_handle: Option<HotkeyHandle>, … }
```

窗口选项（已核实 `gpui-pre-0.3.8/src/platform.rs`）：
```rust
WindowOptions {
  window_bounds: Some(WindowBounds::Windowed(Bounds::centered(None, size(px(880.), px(600.)), cx))),
  titlebar: Some(TitlebarOptions { title: Some("voke 设置".into()), appears_transparent: false, traffic_light_position: None }),
  kind: WindowKind::Normal,
  show: true, focus: true, is_resizable: true,
  ..Default::default()
}
```

页面用 **gpui-component 自带的 setting 框架**（`gpui_kit::component::setting::*`，已逐条核实）：

- `Settings::new(id: impl Into<ElementId>)` → `.page(SettingPage)` / `.pages(iter)` /
  `.sidebar_width(px)` / `.default_selected_index(SelectIndex{page_ix,group_ix})`，
  `RenderOnce`（不是 Entity，直接 `.child(...)` 或 `.render(window, cx)`）。自带左侧
  搜索 + 导航 Sidebar（`h_resizable` + `SidebarMenu`），**group 数 >1 时每个有 title 的
  group 会成为侧栏二级项**。
- `SettingPage::new(title)` → `.icon()` / `.description()` / `.group()` / `.groups()`。
- `SettingGroup::new()`（**无参**）→ `.title()` / `.description()` / `.item()` / `.items()`。
- `SettingItem::new(title, field)` / `SettingItem::render(|options, window, cx| …)`
  （自定义 UI 逃生口）/ `.description(impl Into<Text>)` / `.keywords([])` / `.disabled(bool)`。
- `SettingField`：**`new` 是私有的**，只能走构造器：
  - `SettingField::switch(|cx: &App| bool, |v: bool, cx: &mut App| )`
  - `SettingField::checkbox(...)` 同签名
  - `SettingField::number_input(NumberFieldOptions{min,max,step}, |cx| f64, |v: f64, cx|)` — **权重用它**（f64，落库转 f32）
  - `SettingField::input(|cx| SharedString, |v: SharedString, cx|)` — **激活别名用它**（自由文本）
  - `SettingField::dropdown(Vec<(SharedString/*value*/, SharedString/*label*/)>, get, set)`
  - `.default_value(...)` / `.on_reset(is_dirty, reset)`
- 类型约束：`T: Clone + PartialEq + Send + Sync + 'static`（bool / f64 / SharedString 都满足）。

页面结构：

```
Page「通用」
  Group「启动」       item: 开机自启（switch）
  Group「热键」       item: 唤起面板（SettingItem::render → Kbd + 「点击后按键」录制区）
  Group「关于」       item: 版本 / 配置文件路径（render 只读文本）
Page「插件」（10 个 Group，title=插件名，description=插件描述）
  每个 Group: 启用(switch) / 参与全局搜索(switch) / 权重(number_input 0.0–3.0 step 0.1) / 激活别名(input)
```

所有 `set_value` 闭包统一：`cx.global_mut::<VokeSettings>()… = v;` → `config::commit(cx)`。
- 别名非法 → **不写入**（受控值，界面自然回弹）+ `log::warn`。
- 热键改动 → 额外 `handle.set(spec.to_hotkey())`。

**热键录制器**（`SettingItem::render`）：
```rust
let fh = self.hotkey_focus.clone();
SettingItem::render(move |_o, window, cx| {
    div().id("voke-hotkey-recorder").track_focus(&fh)
        .on_click(move |_, window, cx| window.focus(&fh, cx))
        .capture_key_down(move |ev: &KeyDownEvent, _w, cx: &mut App| { /* 写全局 + commit + handle.set */ })
        .child(Kbd::new(Keystroke { modifiers: Modifiers{control,alt,shift,platform,function:false}, key, key_char: None }))
        .child("点击后按下新快捷键")
})
```
- `capture_key_down` 已确认在 `gpui-pre-0.3.8/src/elements/div.rs:1145`
  （签名 `Fn(&KeyDownEvent, &mut Window, &mut App)`）。
- `Kbd::new(Keystroke)` / `Kbd::format` 在 `gpui-component-0.7.1/src/kbd.rs:31/105`。
- **忽略纯修饰键按下**（`key` 为 `shift`/`control`/`alt`/`platform` 时不落库）；
  `Escape` = 取消录制。
- `Keystroke` 在 `gpui-pre-0.3.8/src/platform/keystroke.rs:17`
  （`{modifiers, key, key_char}`，`Modifiers{control,alt,shift,platform,function}`）。

> 若 `Settings` 框架的 `h_resizable` 在 Windows 上有渲染问题，回退方案：
> `Sidebar::new()` + `SidebarMenu` + 右侧 `div().overflow_y_scrollbar()` 里堆 `SettingGroup`
> （`scroll/scrollable.rs:16-63`，trait 名是 **`ScrollableElement`**，方法
> `vertical_scrollbar(&handle)` / `overflow_y_scrollbar()`；**没有** `vertical_scroll()` 这种写法）。

---

## 6. 验收与验证命令

| 环节 | 命令 |
|---|---|
| core 逻辑 | `cargo test -p voke-core` |
| sys（Linux 侧，新模块被 cfg 挡掉） | `cargo test -p voke-sys` |
| app 交叉编译 | `cargo check --target x86_64-pc-windows-gnu -p voke-app --all-targets` |
| 出可实测产物 | `cargo xwin build --release --target x86_64-pc-windows-msvc -p voke-app`（**前置**：把 mise 的 clang / conda-lld / conda-llvm-tools bin 前置进 PATH，见 AGENTS.md） |
| 真机冒烟 | 部署到 `<userprofile>/voke-poc/voke.exe`（旧 exe 被锁用 `mv` 改名绕过），跑 `scheme-a-verify.ps1`（期望失败项 = 0） |

真机人工验收清单：
1. 面板：`↑↓` / `Ctrl+N` / `Ctrl+P` / `Tab` / `Shift+Tab` 都能移焦；`Ctrl+,` 开设置；Esc 隐藏。
2. 设置 → 插件 → terminal 别名改成 `_`：回面板打 `_` 只出 WT profiles；`<` 只出窗口；
   关掉某插件后它不再出现在 `#` 列表和全局结果里。
3. 权重调到 0.1 / 3.0 观察组内顺序变化；**组间顺序不变是预期行为**（见 §7）。
4. 热键改成 `alt+space` 后立刻生效（不用重启）。
5. 勾选开机自启 → 注销重登后 voke 自动在托盘出现。
6. 托盘右键菜单：显示面板 / 设置 / 退出（退出后进程真的结束）。

---

## 7. 已知取舍（写进文档，不要偷偷改）

- **权重只影响组内排序**。`QueryEngine::aggregate`（`engine.rs:401-426`）按注册顺序建组，
  权重不改变组间次序。这是刻意的（同类结果聚合在一起比全局混排更好用），
  本轮不做「按权重排组」。
- **别名不能以 `#` 开头**：`#` 是插件列表入口（`search.rs:304 picker_filter` 抢在路由之前），
  以 `#` 开头的别名永远触发不到。配置校验直接拒绝。
- **`Ctrl+,` 只在面板（搜索窗口）内生效**，不是全局热键（全局只有唤起键一个）。
- **托盘图标不做状态变色**（awake 激活时变色仍是 future，architecture.md:177）。

---

## 8. 文档同步（每阶段 commit 时一起改）

- `docs/BACKLOG.md`：本节清单逐条勾选（沿用现有 `- [x]` 风格 + 日期 + 一句结论）。
- `docs/architecture.md`：§3 的 TOML 示例（L89-121）更新成真实落盘格式；§4 技术选型表补
  「托盘 = tray-icon」「自启 = 注册表 Run」；§5 补托盘/自启的 Win32 行。
- `docs/HISTORY.md` + `docs/KNOWN-ISSUES.md`：更新当前状态与遗留问题。
- 本文档 §0.1 进度快照。
- commit 用 `git -c user.name="zyition" -c user.email="2321694+zyition@users.noreply.github.com" commit`
  （AGENTS.md）。

---

## 9. 风险与回退

| 风险 | 回退方案 |
|---|---|
| `tray-icon` 在 msvc/gnu 交叉编译下编不过，或 Windows 事件循环要求与 gpui 主线程冲突 | 阶段 2.3 改为手写 `Shell_NotifyIconW`（`windows-0.62.2/src/Windows/Win32/UI/Shell/mod.rs:4450`，`NOTIFYICONDATAW:52111`，`NIM_ADD/NIM_DELETE:52001/52002`，`NIN_SELECT:52013`）+ 消息专用窗口（`HWND_MESSAGE:4060`）+ `CreatePopupMenu/AppendMenuW/TrackPopupMenuEx`。**零新依赖**，与现有 hotkey 线程模式完全一致 |
| `Settings` 框架的 `h_resizable` 在 Windows 渲染异常 | 回退到 `Sidebar` + `SettingGroup` 手搭两栏（阶段 4 末） |
| 注册表自启被安全软件拦截 | 保留 UI 开关，失败时 `log::error` 并在设置项 description 里提示 |
| `SettingField::input` 的提交时机（逐字符 / 失焦）与预期不符 | 拉取后先读 `gpui-component-0.7.1/src/setting/fields/input.rs` 源码确认，再决定别名校验放在 `set_value` 还是加「确定」按钮 |
