> **历史存档（Rust/gpui 时代）**：本文描述 Go + mygo 重写之前的 Rust 实现，已不再是当前主线。
> 当前文档：[README](../../README.md) · [架构](../architecture.md) · [贡献指南](../../CONTRIBUTING.md)。
> 文内相对链接按移动前路径书写，可能失效；仅作历史参考。

# launcher 架构与选型方案（v1.1）

> 项目：voke —— 自用类 PowerToys Run 效率工具，Windows 11，gpui-kit
> 阶段：需求与可行性已确认（2026-10-08），架构已确认（同日）
>
> v1.1 变更（Y 确认）：项目定名 voke（invoke 之魂，crates.io 未占用），crate 前缀 voke-*；
> crate 组织定为三 crate；默认热键 Alt+Space（接管 PTR 位置）；
> 构建环境改为 WSL 内 Rust + 交叉编译出 Windows exe，Windows 侧不装工具链（详见 §8）

---

## 1. 架构总览

```
┌────────────────────────────────────────────────┐
│  voke-app（UI 层，依赖 gpui-kit）           │
│  搜索窗口 / VirtualList 结果列表 / 键盘导航      │
│  主题 / 窗口显隐控制（热键唤起、失焦隐藏）        │
├────────────────────────────────────────────────┤
│  voke-core（纯逻辑，零 UI 依赖）            │
│  查询引擎：前缀路由 → 分发 → 聚合 → 排序         │
│  Provider 注册表 + 配置模型                      │
│  索引层：应用索引 / 拼音预处理 / 使用频率         │
│  拼音转换 + 模糊匹配（pinyin / nucleo-matcher）  │
├────────────────────────────────────────────────┤
│  voke-sys（系统集成层，windows-rs）          │
│  .lnk 解析（COM）/ UWP 枚举（WinRT）             │
│  EnumWindows 窗口枚举 / SetForegroundWindow      │
│  RegisterHotKey 热键 / 图标提取（HICON→RGBA）    │
├────────────────────────────────────────────────┤
│  基础设施：TOML 配置 / 日志 / 单实例锁 / 自启     │
└────────────────────────────────────────────────┘
```

**crate 组织：cargo workspace 三 crate（已确认）**

- `voke-core`：查询引擎 + Provider 契约 + 拼音/匹配 + 配置。**不依赖 gpui**，`cargo test` 秒级反馈
- `voke-sys`：Win32/COM 封装（unsafe 重灾区，隔离污染面）
- `voke-app`：gpui-kit UI + main

这样拆的核心理由：**你是 Rust 零基础 vibe coding，逻辑层和 UI 层隔离后，大部分功能迭代（P1 全部纯函数插件、拼音匹配、排序权重）只改 core，测试循环是秒级，完全不碰 GPUI 编译和调试**。GPUI 相关的坑被限制在 app 薄层。

---

## 2. 查询数据流（核心链路）

```
输入 query
  │
  ├─ 前缀路由：query[0] 是某 provider 的触发字符？
  │    "=" → 只查 calculator；"#" → 只查 value-generator
  │
  └─ 全局模式：并行分发所有 enabled 且 is_global 的 provider
       │
       ├─ 每个 provider 异步返回 Vec<Match>（带原始分）
       │
       ├─ 聚合排序：final = provider.weight × match_score × 频率加成
       │
       ├─ 防抖：输入停止 50-80ms 后触发（对齐 PTR input smoothing）
       │
       └─ Top N → VirtualList 渲染 → Enter 执行 action
```

关键设计点：

- **慢查询不阻塞快查询**：先到先渲染，慢 provider（如窗口枚举）结果到达后合并更新（对齐 PTR 的 immediate/background plugins 区分）
- **Enter 双保险**：结果未到时按 Enter，缓存 pending query，结果到了自动执行第一项

---

## 3. Provider 契约（P1 扩展系统地基）

```rust
pub trait Provider: Send + Sync {
    fn id(&self) -> &'static str;                       // "calculator"
    fn config(&self) -> &ProviderConfig;                // 读用户配置
    fn query(&self, ctx: &QueryCtx) -> Task<Vec<Match>>; // 异步查询
    fn execute(&self, item: &Match, ctx: &ActionCtx);   // 执行动作
}

pub struct ProviderConfig {
    pub enabled: bool,
    pub is_global: bool,      // 是否出现在全局结果
    pub weight: f32,          // 权重（只影响组内排序，见 §5）
    pub prefix: Option<String>, // 触发前缀，如 "="、"#"、"#b"
    pub activation: Activation,  // 前缀激活方式（Direct=输入即路由 / Indirect=前缀+空格）
}
```

配置文件（`%APPDATA%\voke\config.toml`，绝对路径；与 `AppConfig::default()` 落盘格式一致）：

```toml
hotkey = "ctrl+alt+space"   # 唤起面板的全局热键（默认接管 PTR 位置）
autostart = false           # 是否开机自启（写入注册表 HKCU\...\Run）

[providers.apps]
enabled = true
is_global = true
weight = 1.2

[providers.awake]
enabled = true
is_global = true
weight = 1.0

[providers.base64]
enabled = true
is_global = false
weight = 1.0
prefix = "#b"
activation = "indirect"    # 前缀 + 空格才路由（见下「激活方式」）

[providers.calculator]
enabled = true
is_global = false
weight = 1.0
prefix = "="
activation = "direct"

[providers.random]
enabled = true
is_global = false
weight = 1.0
prefix = "#r"
activation = "indirect"

[providers.terminal]
enabled = true
is_global = true
weight = 1.0

[providers.timestamp]
enabled = true
is_global = false
weight = 1.0
prefix = "#"
activation = "indirect"

[providers.uuid]
enabled = true
is_global = false
weight = 1.0
prefix = "#u"
activation = "indirect"

[providers.windows]
enabled = true
is_global = true
weight = 1.0

[providers.workspaces]
enabled = true
is_global = true
weight = 1.0
```

上面是 10 个 provider 的**完整默认配置**（`to_toml` 由 `BTreeMap` 按 id 排序输出，
`prefix = None` 的不输出 `prefix` 行）。读盘走 `ConfigPatch` 部分合并——只写其中
任意一项（如只写 `hotkey`）其余字段沿用默认值，**不用把整张表抄全**。

**激活方式（`activation`）**：`"direct"` = 输入前缀立即路由进插件（`=` → calculator）；
`"indirect"` = 必须「前缀 + 恰好一个空格」才路由（`#`/`#b`/`#u`/`#r` 默认如此，避免与
裸 `#` 的插件列表入口冲突）。`None`（用户没写）由 `AppConfig::normalize` 按前缀回填：
以 `#` 开头 → `indirect`，其余 → `direct`。坏值（如 `"xxx"`）记日志并按默认处理、不废整份配置。

**扩展形态：MVP 用编译期静态注册**（`Vec<Box<dyn Provider>>`，写死在 main 里），不做动态加载。理由：自用工具，改一行注册代码的成可接受，换来零安全问题、调试简单、编译期类型检查。

远期如果想要"不改主程序加功能"：gpui-kit 自带 **gpui-shell**（官方 JS 扩展运行时，能力逐个授权、热重载），是框架官方的扩展路径。当前架构下 Provider trait 不变即可平滑接入，不需要现在决策。

---

## 4. 技术选型表

| 领域 | 选型 | 依据 | 备选 |
|---|---|---|---|
| UI 框架 | gpui-kit 0.7（crates.io） | 已定，含 VirtualList 组件 | — |
| Win32/COM/WinRT | `windows` crate（微软官方） | 全 API 覆盖，PTR 的 C# 调用可直接平移 | — |
| 汉字→拼音 | `pinyin` 0.11 | mozillazg 维护，多音字（to_pinyin_multi），数据来自 pinyin-data，零运行时依赖 | — |
| 模糊匹配 | `nucleo-matcher` | Helix 编辑器同款，fzf 评分算法，SIMD，比 fuzzy-matcher 快约 6 倍，API 稳定接近 1.0 | fuzzy-matcher（劣） |
| 配置 | `serde` + `toml` | Rust 生态标准，可注释 | JSON |
| 全局热键 | `RegisterHotKey`（windows crate 直调） | 无额外依赖，行为可控 | global-hotkey crate |
| 托盘 | `tray-icon 0.26.1`（`default-features = false` + `cfg(windows)` 门控） | 专用线程跑 `GetMessageW` 消息循环，无需手写 `Shell_NotifyIconW` | 手写 `Shell_NotifyIconW` + 消息窗口（回退方案，未采用） |
| 开机自启 | 手写注册表 `HKCU\Software\Microsoft\Windows\CurrentVersion\Run` | 不加新 crate，只用 `RegOpenKeyExW`/`RegSetValueExW` 等 | — |
| 频率/历史统计 | 本地 JSON 文件 | 条目量级几百，无需数据库 | SQLite（过度设计） |
| SQLite（编辑器历史） | `rusqlite` 0.40（`bundled`） | workspaces provider 读 Zed/VSCode 的历史库（都是 SQLite，见 §5），只读查询；`bundled` 自带 C 源码编译，在 xwin msvc 交叉编译下已验证可编过 | — |

**拼音匹配策略（P0 核心）**

索引时对每个应用条目预生成搜索键，查询时零汉字转换开销：

```
"微信" → keys: ["微信", "weixin", "wx"]        （多音字条目展开为多组）
"网易云音乐" → keys: ["网易云音乐", "wangyiyunyinyyue"(多音字展开), "wyyyy"]
```

查询时 query 为纯 ASCII → 对所有 keys 跑 nucleo fuzzy（首字母串天然获得前缀高分）；query 含汉字 → 只对原名跑。`wx`、`weixin`、`微信` 三种输入都能命中，全程不经过输入法。

---

## 5. 系统层实现方案（PTR 源码映射）

| 功能 | Win32/COM API | PTR 参照 |
|---|---|---|
| 应用枚举 | 开始菜单/桌面 `.lnk`：`IShellLinkW` COM 解析（名称/目标/参数/图标）；UWP：WinRT `PackageManager.FindPackagesForUser` + AppxManifest 读 logo | Program 插件 |
| 窗口枚举 | `EnumWindows` + `GetWindowTextW` + `DwmGetWindowAttribute(DWMWA_CLOAKED)` 过滤 UWP 幽灵窗口 + 进程名/图标关联 | Window Walker 插件 |
| 激活窗口 | `SetForegroundWindow` + 前台锁处理（AttachThreadInput / alt-key spoof） | Window Walker 插件 |
| 启动应用 | `ShellExecuteExW`（预留 runas verb = Ctrl+Shift+Enter 管理员启动） | Program 插件 |
| WT profiles | 读 `%LOCALAPPDATA%\Packages\Microsoft.WindowsTerminal*\LocalState\settings.json` | Terminal 插件 |
| workspace 历史 | 数据源 = 编辑器最近工作区库，rusqlite 只读打开（绝不写原库）：**Zed** `%LOCALAPPDATA%\Zed\db\<通道>\db.sqlite` 的 `workspaces` 表 LEFT JOIN `remote_connections`（位置类型来自 `kind`：本地 / WSL / SSH / 容器，全通道 glob、stable 在前）；**VSCode** `%USERPROFILE%\.vscode-shared\sharedStorage\state.vscdb` 的 `history.recentlyOpenedPathsList`（低于 1.118 兜底 `%APPDATA%\Code\User\globalStorage\state.vscdb`）。Zed 的库是 WAL，只读主库会拿到过期时间戳，所以先 `mode=ro` 打开（能读到 WAL 里的最新数据）、失败才退化 `immutable=1`（代价是看不到 WAL、时间戳可能略旧）。**目录扫描方案已删除，不再兜底** | 无现成参照（PTR 的 VSCodeWorkspaces 插件仅思路参考） |
| 编辑器打开 | 按记录它的编辑器打开：Zed 本地 `Zed.exe <路径>`；Zed WSL `Zed.exe --wsl <user>@<distro> <linux路径>`；Zed SSH `Zed.exe ssh://[<user>@]<host>[:<port>]/<路径>`（zed.dev「Remote Development」文档）；VSCode `cmd.exe /c <bin\code.cmd> --folder-uri / --file-uri <uri>`（CreateProcess 跑不了 `.cmd`，必须包一层 cmd.exe）。容器 / 其它位置类型不被 CLI 支持 → 退回 Launch 路径本身（dispatcher 转交 explorer.exe）。两个 CLI 都是控制台子系统，启动加 `CREATE_NO_WINDOW` | — |
| 保持唤醒（awake） | `SetThreadExecutionState`（`ES_CONTINUOUS` \| `ES_SYSTEM_REQUIRED`，可选 `ES_DISPLAY_REQUIRED` 连屏幕一起保持）；专用线程持有，到期/取消时清除 | **Awake 独立模块**（非 Run 插件） |
| 图标提取 | `SHGetFileInfo` / `ExtractIconEx` → HICON → `GetDIBits` 转 RGBA → gpui ImageSource | PTR 同款路径 |
| 托盘 | `tray-icon 0.26.1`：`TrayIconBuilder` 建图标 + `GetMessageW` 循环，每轮 `TrayIconEvent`/`MenuEvent` receiver `try_recv()` 转成命令（显示面板 / 设置 / 退出）；左键单击直接唤起面板，右键菜单含「显示面板 / 设置 / 退出」 | — |
| 开机自启 | 注册表 `HKCU\Software\Microsoft\Windows\CurrentVersion\Run` 写值 `"voke"` = 当前 exe 路径（`REG_SZ`）；只用 `RegOpenKeyExW`/`RegSetValueExW`/`RegDeleteValueExW`/`RegQueryValueExW`，不用 `RegCreateKeyExW`（避免引入 `Win32_Security`）；`windows` crate 加 `Win32_System_Registry` | — |

workspaces 数据源的取舍：最初设计是目录扫描（不依赖编辑器内部数据格式、任意编辑器通用），实机验证走不通——默认扫描根在用户 C: 盘**一个都不存在**，真实项目几乎全在 WSL 里。已改为读编辑器自己的最近工作区历史库：路径和位置（本地 / WSL / SSH）都是编辑器自己记的，不需要猜。代价：只认 Zed / VSCode 两家（没装的编辑器对应条目自然缺失），且依赖它们的数据格式与版本——Zed 的库表结构 / VSCode 的 `state.vscdb` 键名与库文件路径（含 1.118 前后的迁移）变了就要跟着适配；读取全部容错（缺表 / 坏库 / 锁住都跳过，不 panic）。

**awake 的有状态设计**（区别于其他 P1 插件）：

- `SetThreadExecutionState` 是 **per-thread** 语义 → sys 层起一条专用线程持有状态；到期或用户取消时该线程清除状态退出
- **崩溃自愈**：voke 进程退出 → 状态随线程消亡自动失效，系统恢复正常睡眠，不会出现"launcher 挂了导致电脑永不睡眠"的坑，这是该 API 的天然安全兜底
- 无需管理员权限
- Provider 契约不变：query 时读全局 AwakeState 生成动态候选（未激活 → 时长选项列表；已激活 → 剩余时间 + 取消项），execute 改状态
- 状态可见性：呼出 voke 输入 awake 即可查看；托盘图标提示（激活时变色）列为 M2+ 可选项

GPUI 已原生提供、不用自己做的：剪贴板读写（`read/write_clipboard`）、打开所在文件夹（`reveal_path`）、URL 打开（`open_url`）。

---

## 6. 窗口行为方案（M0 PoC 核心）

GPUI 已确认存在的能力（源码/examples 验证）：

- `WindowOptions`：`show: false`（创建即隐藏）、`titlebar: None`（无系统标题栏）、`WindowKind::PopUp`、`window_decorations`、居中 bounds、`focus` 开关
- `cx.hide()` / `cx.activate()`：应用级显隐（官方 window.rs 示例有完整 demo：hide 后 3 秒 activate 恢复）
- 窗口激活状态回调：`on_window_activation`（失焦检测的基础）
- Windows 后端对 `show:false` 窗口的行为在 2024 年有专项修复（创建隐藏、activate 时显示），链路被处理过

**PoC 要验证的组合（Windows 11 实测，产物来自 WSL 交叉编译）**：

| # | 验证项 | 通过标准 |
|---|---|---|
| **0** | **交叉编译可行性**：WSL 内构建 gpui-kit 最小窗口程序，产出 exe 在 Windows 11 正常打开窗口 | 编译通过 + 窗口正常渲染。**这是所有后续项的前提** |
| 1 | `RegisterHotKey`（独立线程）→ 唤起：show + activate + 居中 + 抢焦点 | 热键到可输入 < 100ms，焦点稳定 |
| 2 | 失焦（activation callback）→ `cx.hide()` | 隐藏不销毁、任务栏无残留、再次唤起 < 50ms、状态保留 |
| 3 | PopUp + 无边框 + 置顶组合 | 观感正确，无闪边框 |
| 4 | 常驻内存 | RSS < 100MB（争取 < 50MB） |

**#0 的风险声明**：gpui-kit + Linux 交叉编译没有公开先例（官方只支持 Windows 原生构建）。两条路线按顺序尝试：
1. `x86_64-pc-windows-gnu`（mingw-w64）：WSL 里 `apt install mingw-w64` + `rustup target add`，步骤最简单，windows crate 生态对 gnu target 基本兼容
2. 失败则换 `x86_64-pc-windows-msvc` + `cargo-xwin`（自动下载 Windows SDK，产物与原生 MSVC 一致）

两条都走不通的回退：Windows 侧安装 rustup（仅 Rust 工具链，无需 VS Build Tools——gnu/xwin 路线本身也不需要 Windows 侧任何东西）。#0 失败不代表项目失败，只影响开发工作流选择。

全链路结论：#0 过 → 继续验证 1-4；#2 失败 → 评估 windows-rs 直接调 `ShowWindow(SW_HIDE)` 的兜底路径；#4 超标 → 回到选型讨论。

---

## 7. 里程碑

| 里程碑 | 内容 | 说明 |
|---|---|---|
| **M0 环境 + PoC** | 工具链安装 → 四项验证 | 验证窗口行为 + 内存，产出 go/no-go |
| **M1 P0 最小可用** | apps 枚举（先 .lnk 后 UWP）+ 拼音匹配 + Enter 启动 | 自用开始，无图标也可接受 |
| **M2 P0 完整** | 窗口切换 provider + 图标 + 频率加权 | |
| **M3 P1 地基** | Provider 配置系统（enabled/is_global/weight/prefix）+ 前缀路由 | 扩展系统成型 |
| **M4 P1 插件分批** | calculator → timestamp/base64/uuid（纯函数，先验证扩展系统）→ awake（有状态，验证状态型 provider）→ WT profiles → workspaces | 由易到难 |

---

## 8. 构建环境与工作流（WSL 交叉编译，已确认）

Y 的决定：**WSL 内已有的 Rust 环境做全部构建，交叉编译产出 Windows exe，Windows 侧不装任何开发工具链**。

### 环境准备（WSL 内执行）

```bash
# 1. 确认 rustup（Y 已装）+ 添加 Windows target
rustup target add x86_64-pc-windows-gnu

# 2. mingw-w64 交叉工具链（gnu 路线，首选）
sudo apt install mingw-w64

# 3. 备选路线（#0 首选失败时启用）：cargo-xwin，msvc target
# cargo install cargo-xwin
# rustup target add x86_64-pc-windows-msvc

# 4. gpui-kit 官方 AI skills（给编程 agent 的上下文，在哪侧开发就装哪侧）
# npx skills add longbridge/gpui-kit
```

### 日常工作流

```
WSL 内（项目仓库放 WSL 文件系统，如 ~/voke，
       不要放 /mnt/c 下——跨界 IO 会让 cargo 性能暴跌）：

  cargo test -p voke-core        # core 逻辑层：Linux native 秒级测试
  cargo build --target x86_64-pc-windows-gnu -p voke-app

产物：target/x86_64-pc-windows-gnu/release/voke.exe
运行：拷到 Windows 侧（或直接经 /mnt/c 路径）双击/终端运行实测
```

### 各 crate 的测试策略

| crate | 测试环境 | 方式 |
|---|---|---|
| voke-core | WSL native | `cargo test`（拼音/匹配/排序/路由全在 Linux 跑，无需 Windows） |
| voke-sys | WSL native + Windows 实测 | 纯逻辑（枚举过滤/去重/UTF-16/SQLite reader）下沉成不挂 `cfg(windows)` 的模块，在 Linux 上 `cargo test`；OS 调用部分靠交叉编译产物 Windows 实测 |
| voke-app | 交叉编译产物 | Windows 黑盒实测（窗口行为全靠观察 + 日志）；原生测试在 Linux 编不过（gpui 依赖 fontconfig.pc） |

### 已知代价（接受的工作流约束）

- 交叉编译 gpui-kit 无公开先例，#0 是 M0 第一优先级验证项（见 §6）
- 无 Windows 调试器，调试靠日志（log 输出到 stderr/文件）
- Windows 侧运行 exe 的动作（热键测试、窗口行为观察）需要手动或经 `wsl.exe` 反向触发

---

## 9. 决策记录与剩余待办

已定（2026-10-08）：

- ✅ 项目名称：**voke**（invoke 之魂；crates.io 未占用）
- ✅ crate 组织：三 crate workspace（core / sys / app）
- ✅ 默认热键：Alt+Space，接管 PTR 位置（PTR 停用或改键）
- ✅ 构建环境：WSL Rust + 交叉编译（gnu 首选，cargo-xwin 备选），Windows 侧零安装
- ✅ 技术选型全表（§4）
- ✅ P1 追加 awake 扩展（保持唤醒，参考 PowerToys Awake 独立模块；设计见 §5）

剩余待办（M1 前补齐即可，不阻塞 M0）：

- [x] ~~workspace 扫描默认目录列表~~ —— 已作废：数据源改为编辑器最近工作区历史库（见 §5），不再扫描目录，无需默认目录列表；原待办的动机（默认根需要 Y 的实际开发目录）随之消失
