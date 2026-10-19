> **历史存档（Rust/gpui 时代）**：本文描述 Go + mygo 重写之前的 Rust 实现，已不再是当前主线。
> 当前文档：[README](../../README.md) · [架构](../architecture.md) · [贡献指南](../../CONTRIBUTING.md)。
> 文内相对链接按移动前路径书写，可能失效；仅作历史参考。

# Headless 方案评估记录（gpui VSync 线程空转）

> 评估日期：2026-10-12 ｜ 结论：**挂起**，记入 `docs/BACKLOG.md`，条件成熟再重评（见 §1）。
> 依据：性能审计报告（2026-10 存档）+ 对 `gpui-pre-0.3.8` / `gpui-pre-windows-0.3.8` /
> `gpui-kit-0.7.1` 源码的逐行核实（关键位置见 §8 附录）。
> 标记：**FACT**=源码/实测可证；**ESTIMATE**=推理估算；**INFERRED**=基于机制推断未实测。

---

## 1. 结论（2026-10-12 拍板）

**Headless 方案现在不做，挂起并记入 `docs/BACKLOG.md`。** 不是收益存疑，而是入口被上游两处
硬限制堵死，且与 voke「拒绝 vendor/patch 上游 + 拖放泄漏只藏不毁」的既有策略正面冲突。
性质上本方案不改上游、不用 patch（`request_windowing` 是公开 API），但它依赖「销毁窗口」
这条被 voke 主动禁用的代码路径，等于要推翻 2026-10 的拖放泄漏止血方案。

现阶段不做的三个理由：

1. **Windows 上 headless 是单向门**：平台以 headless 创建后终身不可切换
   （`gpui-pre-windows-0.3.8/src/platform.rs:628-632`）。原审计的「方案 A：启动即 Headless、
   热键时切 Windowed」在 Windows 上直接失败，本文予以勘误。
2. **任何模式切换要求零窗口**：`App::request_windowing` 在还有窗口时直接 Err
   （`gpui-pre-0.3.8/src/app.rs:1178-1182`），且 `App.platform` 是 `pub(crate)`
   （`app.rs:760`），无公开路径绕过。而 voke 面板/设置窗从不销毁——这正是 gpui-pre-windows
   拖放目标泄漏的止血策略（每关一次窗滞留一整套 `DirectXRenderer`，实测 ~50 MB）。
3. **拖放泄漏 50 MB/轮使净账可能为负**：Headless 循环 = 空闲即销毁面板窗，按现有实测口径
   每轮泄漏 ~50 MB；用户使用越频繁泄漏越多，省下的内存可能反被吃掉（**ESTIMATE**）。而用户
   立场明确：**拒绝 vendor/patch 上游、拖放泄漏只藏不毁**——推进 Headless 就要破例，二者
   只能取一。

**重评条件（两项同时满足）**：

- 上游修复拖放目标泄漏（关窗不再滞留 `DirectXRenderer`）；
- 真机测量证实收益：PresentMon 确认隐藏态确实仍在出帧，`powercfg /energy` / 电池报告
  确认 VSync 唤醒与 dGPU 钉活有可测代价。

**背景变化**：稳态常驻空转线程此前有三条（主题注册表 1.5s 轮询、awake 1s 轮询、三个
provider 的搜索键重复计算），均已在 2026-10-12 前落地修复（`RegNotifyChangeKeyValue`
事件驱动 / 按到期剩余时长一次等待 / `SearchKeysCache` 记忆化，1412µs→6µs），
**现只剩 gpui VSync 这一条**。

---

## 2. 问题现状（为什么值得关心）

- **VSync 线程是什么**：gpui-pre-windows 的平台专属线程，线程名 `VSyncProvider`
  （`platform.rs:449-450`），线程体 `loop { wait_for_vsync(); ... }`（`platform.rs:453-492`）。
  **FACT**
- **它现在做什么**：`wait_for_vsync` 内部调 `DwmFlush()`（`vsync.rs:37`）——阻塞到下一个
  垂直同步；显示器灭屏/早退时退化为 `sleep(16.666ms)`（`vsync.rs:23-24,52-55`）。节奏 =
  `DwmGetCompositionTimingInfo` 的 `qpcRefreshPeriod`（`vsync.rs:62-71`）→ **60Hz 屏 60
  次/秒，120Hz 屏 120 次/秒**。**FACT**
  每轮还对每个窗口 `IsWindowVisible/IsIconic` 判定，并**无条件** `RedrawWindow(RDW_INVALIDATE)`
  （`platform.rs:487`，隐藏窗口也调；但隐藏窗口不收 `WM_PAINT`，大概率不真画——**ESTIMATE**，
  需 PresentMon 验证）。
- **什么时候启动**：`Application::run` → `WindowsPlatform::run`（`platform.rs:593-597`）→
  `connect_initially`（`:392-407`）→ `attach_gpu`（`:410-418`），**早于 `run` 闭包**。即 voke
  的窗口还没建、用户可能永远不唤面板，循环已在跑。voke.log 佐证：D3D 设备日志早于「窗口创建
  完成（show: false）」（`main.rs:434`）。**FACT**
- **对笔记本的影响**：
  - 空闲唤醒 **60–120 次/秒**（vs 其它线程全部 0–2 次/秒）。**FACT**（唤醒即事实；「费电」
    为 ESTIMATE）
  - 钉活独显：GPU 为 Intel Arc B370（日志 `Using GPU:`，**FACT**）；`get_adapter` 按
    `EnumAdapters` 顺序取第一个可用适配器，**无 env/API 指定 iGPU**
    （`directx_devices.rs:106-116`，**FACT**）。`DwmFlush` 把 DWM 合成路径钉住 → dGPU 难进
    低功耗，**ESTIMATE 0.5–3 W**。
  - 内存：VSync 线程本身几乎不占；它「守护」的图形栈（D3D 设备 + DirectWrite 字体集合 +
    shader，启动期同步阻塞项，**FACT** 见审计 A4）是隐藏态 108–143 MB 私有提交的主要构成
    （实测详见 [`memory-footprint.md`](memory-footprint.md)）。
  - **诚实提醒**：`DwmFlush` 是阻塞等待、**不烧 CPU**（Process Explorer 的 Cycles Delta 会很
    低）。它的代价是「唤醒次数 + 阻止深度 C-state + 钉 GPU」，不是算力。别被「空转」二字误导。
    **INFERRED**

## 3. Headless 方案具体是什么

### 3.1 公开 API（已核实存在，非编造）

| API | 位置 | 说明 |
|---|---|---|
| `enum WindowingRequest { Headless, Windowed(GraphicalEnvironment) }` | `gpui-pre-0.3.8/src/platform.rs:256-263` | 经 `gpui.rs:195 pub use platform::*` 与 `gpui-kit/src/lib.rs:97 pub use ::gpui::*`（gpui-kit 的 `gpui` 即 `package="gpui-pre"`，见其 Cargo.toml `[dependencies.gpci]`）→ voke 侧写 `gpui_kit::WindowingRequest`。**FACT** |
| `Application::with_windowing(request)` | `gpui-pre-0.3.8/src/app.rs:232-241` | 设置**启动**模式。**FACT** |
| `App::request_windowing(request) -> Task<Result<()>>` | `app.rs:1164-1184` | 运行中切换。**FACT** |

Windows 侧行为（`gpui-pre-windows-0.3.8/src/platform.rs:627-645`）：切 `Headless` →
`detach_gpu()`（停 VSync 线程、`release_gpu()` 文本系统、释放 D3D 设备）；切 `Windowed` →
`check_can_show_windows`（`:1629-1641`，只校验 session/交互窗口站）+ `attach_gpu()`（重建
设备 + 重启 VSync 线程）。**FACT**

### 3.2 启用后渲染路径变成什么样、什么会退化

- 无窗口可开：`open_window` 在 `!is_windowed()` 时直接 `Err("cannot open windows while
  headless")`（`platform.rs:752-759`；`is_windowed` = `directx_devices.is_some()`，
  `:382-384`）。**FACT**
  → headless 期间**所有 UI 不可见、不可交互**；托盘「打开设置」会失败（**INFERRED**：走同一
  `open_window`）。
- 文本系统 GPU 资源被释放：`direct_write.rs:230`「Releases the GPU state, when the platform
  switches to headless mode」；彩色字形光栅显式报错（`direct_write.rs:901`）。**FACT**
  （普通 shaping 保留，但无窗口时无意义）
- 托盘 / 热键 / 单实例线程不依赖 gpui GPU（独立 Win32 循环，
  `voke-sys/src/{tray,hotkey,single_instance}.rs`），照常工作。**INFERRED**（未在 headless 下实测）
- DPI / 透明 / 圆角 / 阴影：headless 期间无从谈起；切回 `Windowed` 后由 `attach_gpu` 全量
  重建，理论上恢复（**ESTIMATE**：无公开开关只停 VSync 而保留窗口——「运行时降级」在 gpui
  0.3.8 **不存在**）。

### 3.3 两个硬限制（查证新发现，原审计未覆盖）

1. **Windows 上「headless 创建」不可逆**：`WindowsPlatform::new(headless: bool)`
   （`platform.rs:180`），标志注释明言 "never change throughout the entire lifecycle"
   （`:60-62`）；`request_windowing` 在 `self.headless == true` 时永久
   `Err("a platform created headless can't switch windowing modes")`（`:628-632`）。**FACT**
   → **原审计「方案 A：启动走 Headless，热键按下时才 `Window(ed)`」在 Windows 上直接失败**，
   本文予以勘误。
2. **任何切换要求零窗口**：`App::request_windowing` 在 `!self.windows.is_empty()` 时直接
   `Err`（`app.rs:1178-1182`）；`App.platform` 是 `pub(crate)`（`app.rs:760`），**无公开路径
   绕过**。窗口经 `Window::remove_window()`（`window.rs:2323-2326`）标记删除、帧末从
   `cx.windows` 移除（`app.rs:3038-3052`），即**下一帧**才可切换。**FACT**
   → voke 必须做到「隐藏 → 空闲 N 分钟 → **销毁面板窗口** → 切 Headless；热键 → 切 Windowed
   → 重建窗口」。而 voke 面板/设置窗**从不销毁**（`main.rs:417-433` 建一次；
   `settings.rs:351-375`「关闭=隐藏复用」），正是因为 gpui-pre-windows 的拖放目标泄漏：
   每关一次窗滞留一整套 `DirectXRenderer`（swapchain+MSAA+图集，实测 50MB+；机制见
   `settings.rs:351-362` 注释与 `gpui-pre-windows/.../window.rs:571,610-631`）。**FACT**

**结论**：方案不 patch 上游、不改上游，纯 voke 代码；但它要走「销毁窗口」——而 voke 禁用
它。二者只能取一。

## 4. 收益量化表（对比「不改」基线）

| 项 | 基线（不改） | Headless 生效时（隐藏态） | 依据 |
|---|---|---|---|
| 空闲唤醒 | 60–120/s（仅剩 VSync；主题轮询等已修） | ~0/s | FACT（唤醒存在）/ ESTIMATE（净收益） |
| dGPU 活跃态 | 被 DWM 合成路径钉住 | 解除 | ESTIMATE 0.5–3 W；无 API 验证只能看 WPA |
| 隐藏态私有提交 | 108–143 MB（内存方案实测） | ~35 MB（= 4.4 MB 启动快照 + ~30 MB 引擎索引） | 基线 MEASURED；目标 ESTIMATE |
| CPU 时间 | ≈0（DwmFlush 阻塞等待） | 0 | FACT：无 CPU 收益，别为「降 CPU」做此方案 |
| 续航 | — | 待 `powercfg /batteryreport` A/B | ESTIMATE，**未实测** |
| 唤起延迟 | 0 ms（窗口已在） | +150–400 ms | ESTIMATE，构成见 §5 |

## 5. 代价与风险

- **唤起延迟 +150–400 ms（ESTIMATE）**：构成 = D3D11 设备创建 + DirectWrite 字体集合 +
  shader 编译（三项均为审计 A4 实测的**启动期同步阻塞项**，FACT）+ 建窗 + 首帧。体感：按
  热键后「愣了一下才出现」。
- **动画/观感**：headless 期间无窗口；切回后首帧前是「无响应窗口」，不是「慢速动画」。
  **ESTIMATE**
- **拖放泄漏（一票否决项）**：每轮 空闲→销毁→Headless 都销毁一次面板窗；按
  `settings.rs:351-362` 的实测口径**每轮泄漏 ~50 MB**。用户使用越频繁、泄漏越多——省下的
  内存可能反被泄漏吃掉。**FACT**（泄漏存在）/ **ESTIMATE**（净账为负）
- **Device lost panic**：VSync 线程遇 GPU 设备丢失走 `panic!("Device lost: {err}")`
  （`platform.rs:459-470`）；attach/detach 循环放大暴露面。**FACT**
- **detach_gpu 不 join 线程**：置停止标志即返回，旧线程「下次 vsync 后退出」
  （`platform.rs:422-425`）——切换后有极短双线程窗口期。**FACT**（低危）
- **行为变化**：headless 期间托盘「打开设置」失败（需置灰/排队）；「启动后未唤出过面板」
  时二次启动激活路径当前直接 `log::error!`（`main.rs:490-494`），要重写。**INFERRED**
- **维护成本**：升级 gpui 时以上全部行号会漂；且 voke 的窗口生命周期从「一次性装配」变成
  「可重建」，`main.rs:446-521`（hwnd/handle 捕获、命令消费循环、订阅）全部要动。**FACT**

## 6. 替代方案对比

| 方案 | 做法 | 收益 | 成本/风险 | 立场冲突 |
|---|---|---|---|---|
| (a) 什么都不做 | 基线 | — | 60–120 次/秒唤醒 + dGPU 钉活常驻 | 无 |
| (b) Headless 循环 | §3.2 的销毁/重建 | 最大（唤醒归零 + 内存 −70~100 MB） | 唤起 +300 ms；**拖放泄漏 50 MB/轮**；main.rs 重构 | **有**：推翻「只藏不毁」止血；修泄漏=patch 上游 |
| (c) 提上游 issue/PR | VSync 线程在「无可见窗口」时暂停（`platform.rs:475-491` 本就每帧遍历窗口判可见性，全隐藏时 `sleep`/`break` 即可）；或最小版：`RedrawWindow`（`:487`）加 `IsWindowVisible` 门控 | 完整版 ≈ (b) 的唤醒收益且**不丢窗口、无重建延迟** | 等上游排期 + 升级回归；收益时点不可控 | 无（这是推动上游修复，非 vendor/patch 自用） |
| (d) 修 voke 自己的轮询（原审计 P0-2 等） | `theme.rs` 1.5s 轮询 → `RegNotifyChangeKeyValue` 事件驱动；awake 1s `recv_timeout` → 按到期剩余时长一次等待；三个 provider 搜索键记忆化 | **已全部落地**（2026-10-12 前），稳态空转线程只剩 gpui VSync | 纯 sys/core 层、行为不变、零拍板 | 无 |
| (e) Windows 图形首选项 | 给 `voke.exe` 指定「节能 GPU」 | 绕开 dGPU 钉活（ESTIMATE） | 不改代码；可能拖慢每帧 | 无 |
| (f) patch `platform.rs:487` | RedrawWindow 门控 | 最小（省 user32 调用，**不省 DwmFlush**） | 需维护 patch | **有**，且收益不配 |

## 7. 如果要做，实施步骤（供未来重评后参考）

前置（**强烈建议先做**，否则可能白干）：真机测量证明收益真实存在——用 PresentMon 看隐藏态
是否仍在出帧（`MsBetweenPresents` ≈ 一个刷新周期即仍在画），用 `powercfg /energy` 看
`VSyncProvider` 的唤醒占比与平台计时器分辨率被谁抬频，必要时 A/B 拔电源对比电池报告。
具体命令清单从略，重评时按当时工具版本现查。

代码改动（全部在 `voke-app`，估计 **+120~200 行 / −30 行**）：

1. `main.rs`：把 `main.rs:417-433` 的建窗 + `Shell::new` + 消费循环装配抽成可重入的
   `ensure_panel_window(cx) -> (Option<isize> /*hwnd*/, AnyWindowHandle)`；`hwnd`/`handle`
   移入 `ShellState`。
2. 加空闲状态机：`hide_window` 后记时间戳；N 分钟（默认 5）无激活 →
   `handle.update(.., Window::remove_window)` → **下一帧**
   `cx.request_windowing(WindowingRequest::Headless).detach_and_log_err(cx)`。
3. 热键路径：若当前 headless → 先
   `cx.request_windowing(WindowingRequest::Windowed(GraphicalEnvironment::detect()))` →
   完成后再 `ensure_panel_window` + `show_panel`。
4. headless 期间 `settings::open` 直接拒绝并日志（避免 `open_window` Err 路径）。
5. 回归：`cargo check --target x86_64-pc-windows-gnu -p voke-app --all-targets`、
   `cargo test -p voke-core -p voke-sys`；真机连续 20 轮 空闲→唤回，**每轮盯 Private
   Bytes——若 +50MB 坐实泄漏，一票否决**；另测 20 次无 `Device lost` panic。内存/GPU 用
   Process Explorer（Private/Working Bytes）+ WPA（`wpr -start GPU`）对比 §4 表格。

## 8. 附录：引用的关键源码位置

| 主题 | 位置 |
|---|---|
| VSync 线程创建与线程体 | `gpui-pre-windows-0.3.8/src/platform.rs:433-495`（线程名 `:449-450`，`RedrawWindow` 无条件 `:487`） |
| VSync 节奏来源 `DwmFlush` | `gpui-pre-windows-0.3.8/src/vsync.rs:26-59`（`DwmFlush` `:37`，16.666ms 兜底 `:23-24,52-55`，`qpcRefreshPeriod` `:62-71`） |
| attach/detach GPU | `gpui-pre-windows-0.3.8/src/platform.rs:410-418` / `:421-431`（不 join 线程注释 `:422-425`） |
| 启动时序（早于 run 闭包） | `gpui-pre-windows-0.3.8/src/platform.rs:392-407,593-597` |
| Windows 切换限制（headless 单向门） | `gpui-pre-windows-0.3.8/src/platform.rs:627-645`（`self.headless` 判定 `:628-632`） |
| headless 不可开窗 | `gpui-pre-windows-0.3.8/src/platform.rs:752-759`；`is_windowed` `:382-384` |
| `WindowingRequest` / `with_windowing` | `gpui-pre-0.3.8/src/platform.rs:256-263` / `app.rs:232-241` |
| `request_windowing` 零窗口限制 | `gpui-pre-0.3.8/src/app.rs:1177-1184`；`App.platform pub(crate)` `:760` |
| `remove_window` 与帧末移除 | `gpui-pre-0.3.8/src/window.rs:2323-2326` / `app.rs:3038-3052` |
| 拖放注册/延迟 Revoke（泄漏） | `gpui-pre-windows-0.3.8/src/window.rs:571,610-631,1511` |
| GPU 适配器选择（无 iGPU 开关） | `gpui-pre-windows-0.3.8/src/directx_devices.rs:106-116` |
| gpui-kit 再导出链 | `gpui-kit-0.7.1/src/lib.rs:97` + 其 Cargo.toml `[dependencies.gpui] package="gpui-pre"` |
| voke 面板窗口创建/显隐 | `voke-app/src/main.rs:405-434`（建窗 `:417-433`，显隐 `:218-251`，失焦隐藏 `:138-145`） |
| voke 设置窗隐藏复用 | `voke-app/src/settings.rs:309-388`（止血注释 `:351-362`） |
| voke 主题轮询（原 P0-2，已修复） | `voke-sys/src/theme.rs`（原 1.5s 轮询 `:40,131-148`，现为 `RegNotifyChangeKeyValue` 事件驱动） |
| 审计报告原文 | 性能审计报告（2026-10 存档），含 P0-1、A4/A5、B2、D 章 |
