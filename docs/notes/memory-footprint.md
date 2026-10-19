# voke 内存优化方案（探索 + 方案，不实施）

> 给后续实现 agent 的**施工依据**。所有 gpui API / 常量 / 行号均已对
> `~/.cargo/registry/src/*/` 下的 registry 源码核实过，**不许拍脑袋**。
>
> 硬约束：简体中文；不动 `voke-core` 分层；本任务**只出方案，不写任何优化代码**。
>
> 相关前情：`docs/BACKLOG.md`「内存修复：设置窗口隐藏复用 + 托盘预缩小图」（commit
> `对应提交`）；`voke-sys/src/mem.rs` 已有 `process_memory()` / `log_snapshot()`。

---

## 0. 结论速览（先看这里）

- **稳态 ~110MB 基本就是 gpui-pre-windows 的「图形栈地板」**：日志实测
  `窗口创建完成` 一步就 +103.6MB 私有提交（`voke.log:2` → `:9`）。voke 自身
  数据只有 KB~MB 级。
- 真正**能拿、且风险低**的收益有两块：
  1. **隐藏即把窗口缩到 1×1**（面板 + 设置窗各放掉一套全尺寸 GPU 纹理）——
     纯 app 层改动、**不需要 patch 依赖**。这是唯一能把「用过之后的稳态」
     拉回「启动地板」的手段。
  2. **patch gpui-pre-windows 的三个常量**（MSAA / swapchain 缓冲 / 图集起步尺寸）——
     可见窗口期再省 ~18~28MB，代价是要 vendor 或 `[patch.crates-io]`。
- **低于 ~104MB 私有提交**需要动 DirectWrite 字体集合 / D3D11 设备 / shader 这些
  框架地板，**本轮不建议做**（收益/风险比差）。
- 若接受「只让任务管理器数字好看」：`EmptyWorkingSet` 能把**私有工作集**读数压到
  几十 MB，但**私有提交不变**、下次用时 page-in——诚实标注为仪表盘级 trick。

---

## 1. 实测数据（`%APPDATA%\voke\voke.log`，2026-10-11T10:14 一次冷启动）

快照格式：`内存快照 [<tag>]：WorkingSet=x.xMB PrivateCommit=y.yMB`
（`voke-sys/src/mem.rs:54-63`）。数据取自 `<userprofile>/AppData/Roaming/voke/voke.log`：

| 序 | 节点 | WorkingSet | PrivateCommit | ΔWS | ΔPC | 日志行 |
|---|---|---|---|---|---|---|
| 1 | 进程启动 | 11.3 | 4.4 | — | — | `voke.log:2` |
| 2 | 窗口创建完成 | 131.1 | 108.0 | +119.8 | **+103.6** | `voke.log:9` |
| 3 | 托盘图标解码前 | 131.3 | 108.1 | +0.2 | +0.1 | `voke.log:12` |
| 4 | 托盘图标解码后 | 131.7 | 108.3 | +0.4 | +0.2 | `voke.log:13` |
| 5 | 启动完成 | 131.7 | 108.4 | 0.0 | +0.1 | `voke.log:14` |
| 6 | 托盘创建完成 | 133.9 | 108.5 | +2.2 | +0.1 | `voke.log:17` |
| 7 | 面板首次唤起 | 148.9 | 122.3 | +15.0 | **+13.8** | `voke.log:19` |
| 8 | 引擎索引完成 | 177.3 | 138.7 | +28.4 | **+16.4** | `voke.log:33` |
| 9 | provider 就绪 | 177.3 | 138.7 | 0.0 | 0.0 | `voke.log:34` |
| 10 | 设置窗口已打开 | 206.7 | 165.2 | +29.4 | **+26.5** | `voke.log:42` |

> 时间顺序说明：第 7 条（面板首次唤起）发生在第 8 条（引擎索引完成）之前——
> 后台「开始菜单扫描线程」与主线程并发（`main.rs:354`），所以 ΔPC 里第 8 条
> 记的是后台线程（应用枚举 + 127 次图标提取 + 拼音索引）冲上来的那一段。

### 1.1 三段增量归因

- **`进程启动 → 窗口创建完成` = +103.6MB**（图形栈地板）。
  这一段包含（`main.rs:274-304`）：`gpui_kit::application()` 起平台（DirectWrite
  文本系统 + D3D11 设备 + shader 编译），`gpui_kit::init`，`open_window` 建面板
  窗口并在 `WindowsWindow::new` 里**无条件**创建一个 `DirectXRenderer`
  （`gpui-pre-windows/src/window.rs:145`，`show:false` 也建）。
  Renderer 初建时资源是 **1×1**（`directx_renderer.rs:167` `DirectXResources::new(&devices, 1, 1, …)`），
  所以这 103MB **主要不是** swapchain/图集，而是设备 + DirectWrite 字体集合 + shader +
  gpui 运行时。**这部分是当前最大项，也是最难砍的项**（见 §4「地板归因」）。
- **`启动完成 → 引擎索引完成` = +16.4MB（相对面板唤起）/ +30.3MB（相对启动完成）**。
  含应用条目 + 拼音索引，以及 127 次 COM/GDI 图标提取后的**堆残留**（Windows 段堆
  常常 committed 不回落）。**这一块里有多少是「真常驻」、多少是「堆不还」需实测**（见 §3 杠杆 8）。
- **`面板首次唤起` +13.8MB / `设置窗口已打开` +26.5MB**。
  这是「每个全尺寸窗口一整套 GPU 纹理」的直接证据，模型见 §2。**可回收性最高**。

> ⚠️ 映射说明：任务管理器「内存」列 = **私有工作集**，而快照的 `PrivateCommit`
> 是 `PROCESS_MEMORY_COUNTERS_EX.PrivateUsage`（提交量，`mem.rs:41`），两者不等价
> （部分私有页可不驻留）。用户观测的 ~110MB 与第 5 条 `启动完成` 的 108.4MB 同量级，
> 但**第 8 条之后真实稳态是 138.7MB 提交**。日志没有 DPI 行，窗口设备像素数
> **待实测**（见 §6）。

---

## 2. 每窗口 GPU 纹理成本模型（源码核实）

单窗口 `DirectXRenderer` 持有的全尺寸纹理（`directx_renderer.rs:70-84`、
`1407-1468`）：

| 资源 | 尺寸 | 字节/像素 | 来源 |
|---|---|---|---|
| swapchain 缓冲 ×3 | W×H | 4 (BGRA) | `BUFFER_COUNT=3`（`directx_renderer.rs:1695`），`create_swap_chain*`（`:1304-1352`）|
| path 中间纹理 | W×H | 4 | `create_path_intermediate_texture`（`:1407`）|
| path MSAA 纹理 | W×H | 4×**4 采样** | `PATH_MULTISAMPLE_COUNT=4`（`:29`），`create_path_intermediate_msaa_texture_and_view`（`:1440`）|

即 **每窗口 ≈ 32·W·H 字节**（12+4+16）+ 图集。

图集（`directx_atlas.rs`，**按格式懒创建**，`push_texture` `:159-251`）：
起步尺寸固定 `DEFAULT_ATLAS_SIZE = 1024×1024`（`:164-167`），低于此值被
`:174` 的 `.max(&DEFAULT_ATLAS_SIZE)` 抬回。三种格式（`:178-194`）：
Monochrome=R8(1B)、Polychrome=BGRA(4B)、Subpixel=RGBA(4B)。用哪种建哪种——
map 在 `gpui-pre-0.3.8/src/platform.rs:1646-1668`：
`Glyph`(非 emoji 非 subpixel)→Monochrome、`Glyph`(subpixel)→Subpixel、
`Glyph`(emoji)/`Image`→Polychrome、`Svg`→Monochrome。
所以 **每窗口图集 ≤ 9.4MB（三格式齐）**，典型 5~9MB。

面板窗口尺寸：`680 × (48×8 + 88) = 680×472` 逻辑像素
（`search.rs:129/144/150`、`window_size()` `search.rs:1571`）。设置窗口
`880×600`（`settings.rs:50`）。

用测得增量反推量级一致：`设置窗口已打开 +26.5MB` 与 `32·W·H + 图集`
同量级，模型成立（DPI 未在日志中，精确值待实测）。

---

## 3. 优化杠杆逐项（原理 / 预估收益 / 实现方式 / 风险 / 是否需 patch）

> 收益单位 = 私有提交 MB。「面板」按 `680×472`、「设置」按 `880×600`
> 逻辑像素估，实际随 DPI 缩放；**结论性数字按「可见窗口期」与「隐藏期」分开给**。

### 杠杆 1：隐藏即缩窗到 1×1（**最高性价比，无需 patch**）
- **原理**：gpui 的 `Window::resize(size)`（`gpui-pre-0.3.8/src/window.rs:2773-2776`）
  → 平台 `resize`（`gpui-pre-windows/src/window.rs:654-677`，`SetWindowPos SWP_NOMOVE`）
  → `WM_SIZE` → `handle_size_change` 调 **`renderer.resize(device_size)`**
  （`gpui-pre-windows/src/events.rs:272-293`），进而 `ResizeBuffers` + `recreate_resources`
  （`directx_renderer.rs:485-527`）——**把 swapchain + path + MSAA 纹理全部按新尺寸重建**。
  缩到 1×1 时这三块几乎归零（图集**不**释放，见风险）。
  触发时机：`observe_window_visibility`（**已确认存在**：`gpui-pre/src/app/context.rs:439-454`，
  回调给 `WindowVisibility::{Hidden,Visible}`，枚举在 `platform.rs:106-116`）
  或现有 toggle 路径（`main.rs:166-186` 面板、`settings.rs:120/163-166` 设置）。
- **预估收益**：**隐藏时**面板回收 ~10~14MB、设置窗回收 ~22~26MB。
  意义在于——**用户日常两个窗口都是隐藏的**，此招把「用过之后」的稳态从
  ~165MB（日志末态）拉回 ~108MB 地板。这是**唯一能让稳态不再增长**的手段。
- **实现方式**：隐藏路径里 `handle.update(cx, |_, w, _| w.resize(size(px(1.), px(1.))))`；
  唤起路径先 `w.resize(真实尺寸)` 再 `show_window(hwnd)`。
- **风险**：
  - `resize` 是 `executor.spawn(...).detach()`（`window.rs:659-676`）**异步**的，与
    `ShowWindow` 竞态可能出现「1×1 首帧闪一下」。缓解：唤起时先 resize、下一帧再 show，
    或容忍一次重排。
  - **图集不会释放**（renderer 的 `atlas` 与窗口同生命周期，`directx_renderer.rs:165`），
    隐藏期仍占 ~5~9MB/窗口。要连图集一起放掉得走「销毁窗口」（杠杆 4a）。
  - 首次 resize 到 1×1 会重建资源，可能有一次 GPU 同步开销（可忽略）。
- **需 patch**：否。

### 杠杆 2：MSAA 4→1（`PATH_MULTISAMPLE_COUNT`）
- **原理**：`directx_renderer.rs:29` `const PATH_MULTISAMPLE_COUNT: u32 = 4;` 只用于
  path MSAA 纹理（`:1453-1455`）。降到 1 时该纹理从 W×H×4B×4 → W×H×4B（省 W×H×12）。
  这是**无条件分配**：`recreate_resources` 每次都建（`:1372-1375`），与是否真画 path 无关。
- **预估收益**：面板 ~3.7~4.9MB、设置窗 ~6.3~10.8MB，**合计 ~10~16MB**（可见期）。
- **实现方式**：改常量（`4`→`1`）并给 `SampleDesc.Quality` 置 0；**建议做成环境变量开关**
  （如 `GPUI_PATH_MSAA`，参照同文件已有的 `GPUI_DISABLE_DIRECT_COMPOSITION` 模式，
  `directx_renderer.rs:26` + `platform.rs:245-246`），便于 A/B 与回退。
- **风险**：**视觉质量下降**。gpui-component 的图标是 SVG（`gpui-component/src/icon.rs:7` 用
  `svg`），SVG 走 path 管线（`platform.rs:1665`），MSAA 降到 1× 会让**设置页图标边缘出现锯齿**。
  面板行图标是 PNG（`search.rs:1164` `img()`）走**图集**、不吃 path MSAA，面板受影响小。
  → 若要保留设置页质量，可考虑「面板窗口 1×、设置窗 4×」按窗口分档（需改 allocator 逻辑）。
- **需 patch**：是（`gpui-pre-windows`）。

### 杠杆 3：swapchain 缓冲 3→2（`BUFFER_COUNT`）
- **原理**：`directx_renderer.rs:1695` `const BUFFER_COUNT: usize = 3;` 同时喂给
  `create_swap_chain_for_composition`（`:1314`）与 `create_swap_chain`（`:1343`）。
  `DXGI_SWAP_EFFECT_FLIP_SEQUENTIAL` 要求 BufferCount ∈ [2,16]，**2 合法**。
- **预估收益**：省 1 个 W×H×4B：面板 ~1.2~1.6MB、设置 ~2.1~3.6MB，**合计 ~3~5MB**。
- **实现方式**：改常量 3→2（两处 `CreateSwapChain` 共用该常量，一处改即可）。
- **风险**：低。2 缓冲 flip 在 vsync 下偶发多一帧延迟/轻微撕裂观感差异，
  但没有 FLIP_DISCARD 之外的正确性问题。建议保留 env 开关。
- **需 patch**：是。

### 杠杆 4：隐藏窗口的资源策略（设置窗 ~26.5MB 白占）
现状：设置窗「关闭=隐藏、复用」（`settings.rs:163-166`），**整套 renderer 常驻**
（含 swapchain/path/MSAA/图集）。两条路：
- **4a 恢复销毁 + 打 RevokeDragDrop 补丁**（「正解」路径，用户此前拒绝 vendor/patch）：
  - **原理**：真正的泄漏根因在平台层——`register_drag_drop`（`gpui-pre-windows/src/window.rs:1511-1523`）
    把 `Rc<WindowsWindowInner>` 存进 OLE `IDropTarget`，Windows 持引用直到
    `RevokeDragDrop`；而 `RevokeDragDrop` 在 `Drop for WindowsWindow` 的延迟异步任务里
    （`window.rs:610-631`），执行时机不可靠 → renderer 滞留（`docs/KNOWN-ISSUES.md`）。
  - **收益**：关闭即回收**整窗 26.5MB（含图集）**；但每次打开要重建窗口 + renderer（首帧变慢）。
  - **成本**：需 vendor `gpui-pre-windows` 或 `[patch.crates-io]`，落一个 ~几行的补丁
    （把 `RevokeDragDrop` 提前到 `DestroyWindow` 之前**同步**执行，或直接放在窗口关闭
    路径而非 Drop）。用户已明确拒绝 vendor/patch（`docs/KNOWN-ISSUES.md`），**除非愿意改主意，
    否则不选**。
  - **需 patch**：是。
- **4b 隐藏时「释放/重建」渲染资源**：等价于杠杆 1（缩窗到 1×1）+ 想连图集也放掉。
  图集释放**没有公开 API**（`DirectXAtlas` 只在 `handle_device_lost` 里清，非 pub），
  所以 4b 的现实形态就是**杠杆 1**，图集那 ~5~9MB 留在隐藏期。
  - **需 patch**：否（但拿不到图集那部分）。
- 结论：**优先做杠杆 1**；只有在用户接受 patch，且愿意承担「每次开设置重渲染」的
  前提下，才考虑 4a。

### 杠杆 5：关闭 accesskit / UIA（**无需 patch，但收益存疑**）
- **原理**：每个窗口创建时 `a11y_init` 会建 `accesskit_windows::Adapter`
  （`gpui-pre-windows/src/window.rs:1075-1093`），除非
  `cx.accessibility_force_disabled`（`gpui-pre-0.3.8/src/window.rs:1615-1619`）。
  关掉它的**公开 API 已存在**：`Application::new_inaccessible(platform)`
  （`gpui-pre-0.3.8/src/app.rs:195-199`，`pub`），搭配 `gpui_kit::platform::current_platform(false)`
  （`gpui-pre-platform-0.3.8/src/gpui_platform.rs:67`，`pub`；`gpui-kit/src/lib.rs:110` re-export）。
- **预估收益**：**待实测**。Adapter 是懒激活的（真正建 UIA 树要等 `WM_GETOBJECT`），
  没有屏读器时**可能 <2MB**；有屏读器时才显著。不要预期大收益。
- **实现方式**（无需 patch，一行）：
  ```rust
  gpui_kit::Application::new_inaccessible(
      gpui_kit::platform::current_platform(false))
      .with_assets(gpui_kit::assets::Assets)
      .run(|cx: &mut App| { … })
  ```
  （替换 `main.rs:274-276` 的 `gpui_kit::application()`。）
- **风险**：丧失无障碍（启动器场景收益低，可接受）；需确认 gpui-kit 版本里
  `Application` 与 `platform` 符号可用（已核实 `pub use ::gpui::*` +
  `pub use ::gpui_platform as platform`，`gpui-kit/src/lib.rs:97/110`）。
- **需 patch**：否。

### 杠杆 6：DirectComposition 设备共享（收益低）
- **原理**：`DirectComposition { comp_device, comp_target, comp_visual }`
  （`directx_renderer.rs:118-122`）**每窗口一套**（`:174-183`，`DirectComposition::new(dxgi_device, hwnd)`）。
  `comp_device` 绑的是 D3D 设备（共享的），`comp_target`/`comp_visual` 天生 per-HWND。
- **预估收益**：可省的只有跨窗口共享一个 `IDCompositionDevice`——**量级很小**
  （单个 DComp 设备不是大头），不优先。
- **实现方式/风险**：需改 gpui 结构；且 `GPUI_DISABLE_DIRECT_COMPOSITION=1`
  （`directx_renderer.rs:26`、`platform.rs:245-246`）**已能从外部 A/B**，
  建议先用它量一次 DComp 的净成本再决定是否值得改。
- **需 patch**：是（真要共享的话）。

### 杠杆 7：`EmptyWorkingSet` / `SetProcessWorkingSetSize(-1,-1)`（**仪表盘级 trick，诚实标注**）
- **原理**：把工作集页换出到 standby，**不减提交**、下次访问 page-in。
  任务管理器「内存」（私有工作集）读数会明显下降。
- **API 已核实**：`EmptyWorkingSet(HANDLE)` 在
  `windows-0.62.2/src/Windows/Win32/System/ProcessStatus/mod.rs:2`；
  `SetProcessWorkingSetSize` 在 `Win32/System/Threading/mod.rs:1661`。
  `voke-sys/src/mem.rs` 已 import `Win32::System::ProcessStatus`（feature 已开，无需新增）。
- **预估收益**：**私有工作集读数**可能从 100+MB 掉到几十 MB；**私有提交不变**
  （§1 表里的数字不会变）。PowerToys Run 自己就这么干。
- **实现方式**：在隐藏窗口时（或 30s 定时）调 `EmptyWorkingSet(GetCurrentProcess())`；
  或 `SetProcessWorkingSetSize(GetCurrentProcess(), -1, -1)`（传 `usize::MAX`）。
- **风险**：下次唤起时首批 page-in 导致一次短暂卡顿；任务管理器「提交大小」列暴露真相。
  **必须诚实地在文档/设置里说明这是「数字优化，不是真省内存」**。
- **需 patch**：否。

### 杠杆 8：堆行为（mimalloc / HeapCompact）——针对 §1 的 +30MB 索引段
- **原理**：§1 的 `引擎索引完成 +30MB` 里可能有一部分是 Windows 段堆在 127 次
  COM/GDI 图标提取后 **committed 但不还**。换用带 purge 的分配器（mimalloc）或
  `HeapCompact` 可能把这段私提降下来。
- **现状**：`Cargo.lock` 里**没有** mimalloc/tikv-jemalloc（已 grep 确认），加它 = 新依赖。
- **预估收益**：**待实测**，可能 0 ~ 十几 MB；不可拍脑袋，先测再说。
- **实现方式**：`mimalloc` 作 `#[global_allocator]`（带 `MI_OPTION_PURGE_DELAY=0` +
  定期 `mi_collect`）；或对默认堆调 `HeapCompact(GetProcessHeap(), 0)`。
- **风险**：换分配器有全局影响（性能/碎片），需回归测试；`HeapCompact` 是同步的，
  不宜频繁调。
- **需 patch**：否（app 层）。

### 杠杆 9：ICU / 静态大对象
- **原理**：`Cargo.lock` 有 `icu_normalizer`/`icu_properties`/`icu_*_data`（来自
  `url`→`idna` 链），但这些是**只读数据段**（文件映射，属共享页），**基本不进私有工作集**。
  `MAX_INSTANCE_BUFFER_SIZE = 256MB`（`directx_renderer.rs:30`）只是**上限断言**
  （`:1140-1147`），**不是预分配**——已核实，别当成 256MB 白占。
- **预估收益**：**~0**（无可回收项）。
- **需 patch**：否。**结论：不是问题，排除。**

### 杠杆 10：窗口尺寸 / DPI
- **原理**：面板 `680×472`、设置 `880×600` 逻辑像素，GPU 纹理正比于 W×H
  （§2）。缩面板高度（如 `MAX_VISIBLE_ROWS` 8→6）能按比例减 swapchain/path/MSAA。
- **预估收益**：面板高度 472→376 逻辑像素，约省面板那 32·W·H 的 ~20%
  （**~2MB**）。小。
- **风险**：可用行数变少，UX 变化，需用户拍板。
- **需 patch**：否。

---

## 4. 地板归因（待细化打点）——决定「能不能低于 104MB」

`窗口创建完成 +103.6MB` 是最大项。当前日志粒度只能定位到「设备 + DirectWrite +
shader + gpui 运行时」这一整块。**建议下一步先加 3 个中间快照**（仅打点，不算优化）：
`application() 后` / `文本系统创建后` / `open_window 前`，把 103.6MB 拆开，
再决定是否值得碰地板。若 103MB 里 DirectWrite 字体集合占大头，理论上有两个激进方向
（**均需 patch，收益不明，本方案不推荐**）：
1. 延迟/裁剪 `GetSystemFontCollection` + `generate_font_fallbacks`
   （`direct_write.rs:196-214`、`:398-454`）；
2. 隐藏期整体释放 renderer（治标不治本，且图集/设备仍在）。

> 打点命令见 §6。**在拿到这 3 条中间快照前，不要启动任何「砍地板」的工程。**

---

## 5. 推荐路线（三档）

### 档 1 —— 不动依赖（**先做这些**）
| # | 动作 | 预期（稳态/可见期） | 风险 |
|---|---|---|---|
| 1 | **隐藏即缩窗到 1×1**（面板 + 设置窗，走 `observe_window_visibility`） | 稳态回到 ~108MB（不再随使用增长） | 中（异步 resize 首帧闪烁，需处理）|
| 5 | `new_inaccessible` 关 a11y | 待实测（可能 <2MB） | 低 |
| 7 | 隐藏时 `EmptyWorkingSet`（可选，用户拍板） | 私提不变，私有工作集读数↓ | 低（观感 trick）|
| 8 | 换 mimalloc / HeapCompact 测索引段 | 待实测 0~10+MB | 中（全局分配器）|

### 档 2 —— patch gpui-pre-windows（**要 vendor 或 `[patch.crates-io]`，需用户同意**）
| # | 动作 | 预期（可见期合计） |
|---|---|---|
| 2 | `PATH_MULTISAMPLE_COUNT` 4→1（建议 env 开关） | ~10~16MB |
| 3 | `BUFFER_COUNT` 3→2 | ~3~5MB |
| 2' | 图集 `DEFAULT_ATLAS_SIZE` 1024²→512²（建议可配） | ~5~7MB |
| 4a | 恢复设置窗销毁 + 修 `RevokeDragDrop` | 关闭即回收整窗 ~26MB（含图集）|

三件套合计 **~18~28MB**（可见期），且可做成 env 开关、零回退成本。
4a 与档 1 的缩窗是**二选一**（缩窗不需要 patch，4a 需要）。

### 档 3 —— 仪表盘级 trick（**只影响读数，明确告知用户**）
- 杠杆 7（`EmptyWorkingSet`），以及隐藏时的 `SetProcessWorkingSetSize(-1,-1)`。
- 诚实标注：「私有提交不变；这是把页换出，下次用时 page-in。」

---

## 6. 验收与自测命令

| 环节 | 命令 |
|---|---|
| 抓最新快照 | 冷启动 voke，`grep "内存快照" <userprofile>/AppData/Roaming/voke/voke.log` |
| 复现「用过之后」稳态 | 唤起面板 1 次 → 开设置 → 关设置（隐藏）→ 再抓快照，对比 `设置窗口已打开` 行 |
| 中间打点（§4 用） | 在 `main.rs` 的 `application()/init 后`、`open_window 前` 各插一条 `voke_sys::log_snapshot(...)` |
| 出可实测产物 | `cargo xwin build --release --target x86_64-pc-windows-msvc -p voke-app`（**前置**：把 mise 的 clang / conda-lld / conda-llvm-tools bin 前置进 PATH，见 AGENTS.md）|
| 部署 | `<userprofile>/voke-poc/voke.exe`（旧 exe 被锁用 `mv` 改名绕过）|
| DComp A/B（杠杆 6） | 设 `GPUI_DISABLE_DIRECT_COMPOSITION=1` 跑一轮，对比快照（注意可能影响面板圆角/透明）|
| MSAA/缓冲 A/B（档 2） | patch 后设 env 开关跑两轮，对比 `面板首次唤起` / `设置窗口已打开` 行 |

回归底线：`cargo test -p voke-core`、`cargo test -p voke-sys`、
`cargo check --target x86_64-pc-windows-gnu -p voke-app --all-targets` 全绿；
必须跑 `cargo fmt --all`（`-- --check` 验收；仓库已统一 rustfmt 且保持 rustfmt-clean，CI 强制，见 AGENTS.md）。

---

## 7. 预期总收益

| 口径 | 现在 | 档 1 后 | 档 1+2 后 |
|---|---|---|---|
| 稳态私有提交（两窗都隐藏） | ~138~165MB（随使用增长）| **~108MB（回到地板，不再增长）** | ~108MB（地板不变）|
| 可见窗口期私有提交 | 165MB | ~165MB | **~137~147MB** |
| 任务管理器「内存」（私有工作集） | ~110MB+ | 若加档 3：**读数可到 ~30~60MB** | 同左 |

- **一句话推荐路线**：先做**档 1 的「隐藏即缩窗到 1×1」**（不需要 patch，直接把稳态
  从「用过就涨」钉回 ~108MB 地板），再评估是否值得让用户接受 **档 2 的 gpui 三常量补丁**
  拿下可见期 ~18~28MB；`EmptyWorkingSet` 作为可选的读数优化诚实提供。
- **地板提醒**：**~104MB 私有提交是 gpui-pre-windows 框架地板**（DirectWrite/D3D/shader），
  低于它需要动框架或延迟建窗，**本轮不做**。
- 与「初衷」（嫌 PTR/Command Palette 占内存）的关系：voke 地板 ~108MB 已显著低于
  那些 Electron/WebView 方案；本轮目标是把**使用后的增长**消掉，而不是把地板砍到几十 MB。

---

## 8. 已知不确定项（必须实测，不许拍脑袋）

1. **DPI / 窗口设备像素数**：日志无 DPI 行，`32·W·H` 的绝对值待确认（模型本身成立）。
2. **a11y 关闭的真实收益**（杠杆 5）：需有/无屏读器两种情形各测一次。
3. **索引段 +30MB 里「堆残留」占比**（杠杆 8）：换 mimalloc 前后对比。
4. **103.6MB 地板的内部拆分**（§4）：需先加中间打点。
5. **缩窗 1×1 的首帧闪烁**是否可接受：需真机人肉验收。
