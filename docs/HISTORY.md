# voke 历史与决策档案（踩坑 / 决策记录）

> 本文件是**跨文化交流有价值**的技术事实与决策记录，给后续贡献者（含 AI agent）
> 避坑用。流水账与逐日进度不在此（已清理）。正文简体中文。
>
> 分层铁律与仓库纪律见 [`AGENTS.md`](AGENTS.md)。

---

## 1. 构建与环境

- **交叉编译目标**：主目标 `x86_64-pc-windows-msvc`（cargo-xwin），`x86_64-pc-windows-gnu`
  仅用于 `cargo check` 验收。`voke-app` 只面向 Windows，`cargo check -p voke-app`
  在 Linux native 因缺 `fontconfig.pc` 环境性失败——**属预期，不要修**。
- **rustup 镜像（TUNA）覆盖问题**：`rustup` 硬编码 TUNA 且无视 `RUSTUP_DIST_SERVER`，
  `rustup target list` 可能漏报已装 target。曾出现 `rust-std-x86_64-pc-windows-gnu`
  在镜像上 **404**，解决办法是绕过镜像、从 `static.rust-lang.org` 拉 tarball 手动装 target。
  **结论：直接试编译，别信 `rustup target list` 的输出。**
- **项目级 sparse 覆盖不能改回官方 index**：全局 `~/.cargo/config.toml` 用 TUNA **git**
  索引时新 crate 可能缺；项目级 `.cargo/config.toml` 覆盖为 TUNA **sparse** 端点。
  **不能**替换为官方 `index.crates.io`——等于自指同名 registry，cargo 报 `source is
  already defined`。
- **cargo-xwin 需要真实 clang / llvm-lib 前置进 PATH**：`cc-rs` 调用的 `clang-cl` /
  `llvm-lib` 若命中工具管理器的 shim 会报 `No version is set for shim`。需把真实的
  LLVM/clang/llvm-lib 工具链 bin 目录前置到 PATH（不要用 shim）。
- **发行版不是 Ubuntu**：构建环境是 **WSL2 + 某 Linux 发行版**。mingw 交叉工具链要装
  **`mingw64-cross-gcc` + `mingw64-cross-binutils`**（不是 Wine 版的 `mingw64-gcc`，
  那是 Windows 二进制，跑不起来）。
- **release 必须走 cargo-xwin，debug 产物 Windows 无法运行**：gpui 的 `gpui-pre-windows`
  在 release 用 `fxc.exe` 预编译 shader（随 Windows SDK 自动下载），debug 则在运行时用
  `D3DCompileFromFile` 编译——而 shader 路径用了编译期展开的 `CARGO_MANIFEST_DIR`
  （WSL Linux 路径），Windows 上 `canonicalize()` 必然失败 → panic。**唯一可实测产物 =
  xwin msvc release**。gnu 的 release 也不行（同样找不到 `fxc.exe`）。
- **shell 差异**：构建环境的默认 shell 不是 bash，命令片段注意适配（`export`/`$?`
  在 fish 下会失败）。

---

## 2. 主题系统设计

- **三大 crate 分层**：`voke-core`（纯逻辑，零 gpui / 零 windows 依赖，Linux 秒级测试）、
  `voke-sys`（Win32/COM 封装，OS 调用一律 `#[cfg(windows)]` 门控）、`voke-app`（gpui UI）。
  纯逻辑要下沉成不挂 `cfg(windows)` 的模块，才能在 WSL 里跑测试——这是 `sys` 测试能覆盖
  枚举/去重/UTF-16/SQLite 解析的前提（曾因整模块 `cfg(windows)` 导致 Linux 测不到、上线漏掉 bug）。
- **Provider 契约**：`Provider` trait 的 `query` 返回 `BoxFuture` 而非 gpui 的 `Task`
  （避免 core 依赖 gpui）；副作用集中在 app 的 dispatcher，行为可审计、可单测。
- **中文 / 拼音匹配策略**：索引时给每个应用预生成「原名 + 拼音全拼 + 拼音首字母」搜索键，
  查询时零汉字转换开销。query 为 ASCII 时对全部键跑 fuzzy；含汉字只对原名跑。
- **方案 A 交互（取代早期子命令面板）**：打 `#` 拉出**插件选择列表**（按拼音/英文别名
  过滤），选中才激活插件；激活后输入框只含插件查询本身，前缀以**不可编辑令牌**渲染在
  输入框旁。这是经用户实测拍板的交互形态。
- **配置内核**：落盘 `%APPDATA%\voke\config.toml`（绝对路径；启动早期 `fix_own_cwd`
  已把 System32 类异常 cwd 修正到 `%USERPROFILE%`，绝对路径为防御性兜底），
  `ConfigPatch` 部分合并 + `normalize` 校验；默认配置锁死，坏值宽松回落。
- **设置窗「关闭 = 程序化销毁」的由来**（2026-10-12 起，见第 6 节档案）：早期每次关闭
  设置窗都真实销毁窗口，撞上上游平台层拖放目标泄漏（见 [`KNOWN-ISSUES.md`](KNOWN-ISSUES.md)），
  关窗会滞留整套 GPU 资源；一度改用「关闭即隐藏、复用窗口」止血。用户拍板回收常驻的
  ~26.5MB，改为程序化销毁（Zed 主窗同款姿势），泄漏由「绕开销毁路径」变为「在 HWND
  存活期内完成吊销」，上游 bug 本身仍在。
- **托盘即时刷新**：托盘菜单在 `WM_INITMENUPOPUP`（菜单弹出前）才按当前状态刷新 awake
  条目，而非定时器轮询。

---

## 3. 验证与测试

- **Windows UI 无法用合成输入验证**：`SendInput`/`keybd_event` 注入的虚拟按键不被
  `RegisterHotKey` 识别（会话隔离），也进不了 gpui 输入框。**路由 / 剪贴板 / 唤醒等核心
  链路改用进程内自检**（`VOKE_SELFTEST=1`，跑的是真 ENGINE / 真 `App` / 真函数），
  默认不跑、不污染正常启动。
- **核心逻辑可 Linux 秒级测试**：`cargo test -p voke-core`（纯逻辑）、`cargo test -p voke-sys`
  （不挂 cfg 的纯逻辑模块）；`cargo check --target x86_64-pc-windows-gnu -p voke-app
  --all-targets` 把测试代码一起编过，保证 app 侧测试可编译。
- **部署时 exe 被运行中实例锁住**：旧 exe 不能删/覆写，可先 `mv` 改名再部署新产物
  （Windows 允许改名运行中的 exe）。
- **剪贴板自检偶发 FAIL 是环境性问题**：自检写后立即读回，会与常驻的剪贴板管理器抢独占
  资源，读回偶尔 `None`。这是竞争资源本身的问题，不是代码回归——靠自检自带的多次重试
  扛，整脚本重跑即可，不要据此判定失败。

---

## 4. 踩坑档案（按现象 → 根因 → 对策记）

- **`is_excluded_class` 大小写比对错位**：过滤系统覆盖层窗口的常量混用大小写，只把输入
  `to_ascii_lowercase()` 而常量没小写化，导致所有覆盖层类名漏过。修法：常量与输入两边都
  小写化，并把过滤逻辑从整模块 `cfg(windows)` 下沉为跨平台可测。
- **前缀路由「接了等于没接」**：app 层只调 `route()` 又把原文塞进 `ctx.query`，剥前缀的
  逻辑在 `dispatch` 里没走。修法：把「路由 + 剥前缀」绑成唯一出口 `QueryEngine::prepare`。
- **dispatcher 决策逻辑测不了**：`dispatcher::execute` 收 `&mut App`（gpui 上下文），
  Linux native 构造不了。修法：抽 `Host` trait，生产实现是 `gpui_kit::App`，测试用
  `FakeHost` 记录调用序列。
- **汉字 query 排序偏低**：`微信` 匹配得分（~0.31）远低于拼音 `weixin`（~0.83），nucleo
  子串匹配量级天生低于 fuzzy。不影响正确性（都能命中），影响中文直接输入的排序手感，
  是否对汉字路径单独加成待定（见 [`BACKLOG.md`](BACKLOG.md)）。
- **系统应用英文名搜索不到**：MSIX 显示名是中文（如「终端」），只建了拼音/首字母键，
  没建英文名键（如 `terminal` 搜不到「终端」）。与上游同类工具行为一致，用户已接受现状；
  若以后要支持需维护英文名→显示名别名表（见 [`BACKLOG.md`](BACKLOG.md)）。
- **Zed 控制台窗口闪烁**：`voke` 启动的 Zed CLI（`bin\Zed.exe`）是**控制台子系统**
  （PE Subsystem=3），不像 GUI 主程序（Subsystem=2）那样静默；VSCode 的 CLI 是批处理
  `code.cmd`，`CreateProcessW` 跑不了 `.cmd` 需包一层 `cmd.exe /c`。两者启动都需
  `CREATE_NO_WINDOW` 避免闪控制台窗口——按动作加 flag（不全局隐藏，否则 cmd/PowerShell
  等本就该出窗口的程序会被一起藏掉）。
- **`Win+Shift+S` 截图时窗口偶发消失**（低优先级）：应用在前台按截图键有时触发失焦回调
  走到 `SW_HIDE`。前台/激活语义在截图覆盖层下不稳定，待排查（见 [`KNOWN-ISSUES.md`](KNOWN-ISSUES.md)）。
- **`.msc` 条目（任务计划程序/计算机管理等）图标是白纸**（2026-10-12 修复）：四因叠加。
  ① `resolve_lnk` 把 `GetIconLocation` 的 `路径,索引`（如 `miguiresource.dll,-202`，
  正是开始菜单同款图标的坐标）按逗号切分，索引被丢；② `pick_icon_spec` 优先级 1 只认
  exe/ico，带索引的 DLL 规格落选，spec 退成 `.msc` 文件路径；③ 注册表
  `HKCR\MSCFile\DefaultIcon = %1`，而 `.msc` 是 XML 文档自身无图标资源，
  `GetImage` 返回的是**"成功但错"的通用文档占位图**（`Some` 而非 `None`）——
  所以"主路径失败才兜底"的门控永远短路，兜底从未触发；④ 缓存键不含 spec，
  旧错图持续命中。修法：保留逗号形态 + `SHDefExtractIconW` 按索引精确提取
  （负索引=资源 ID）+ 缓存键混入 spec 哈希 + 无 IconLocation 的 `.msc` 目标退
  `mmc.exe`（`%SystemRoot%\System32\mmc.exe` 环境变量展开）。教训：**主路径返回
  `Some` 不等于图标正确**，"成功但错"比失败更难排查；缓存键必须包含影响产物的
  全部输入，否则任何提取逻辑修复都会被旧缓存掩埋。
- **管理工具三个条目图标变红工具箱**（2026-10-12 修复，上一条的续）：上一轮修好后
  用户实测「服务/组件服务/事件查看器」错（右下红块儿）、「计划程序/计算机管理」对。
  真机证据链（WSL interop 跑 PowerShell + `Add-Type` 编 C# 直调真机 API，勿凭记忆）：
  ① 五个 `.lnk` 的 StringData `ICON_LOCATION` 全是**裸路径无索引**
  （`%windir%\system32\filemgmt.dll`）；`strings` 里先看到的 `,-202/-112/-602/-3411/-102`
  属于 **NAME_STRING**（`@dll,-id` 间接显示名，那些数字是**字符串资源 ID 不是图标 ID**，
  直接拿去 `SHDefExtractIconW` 必然失败）——上一轮档案里"GetIconLocation 返回
  `miguiresource.dll,-202`"的记载不准确，以此条为准；② 真机 `GetIconLocation` 出参索引
  三形态：正序数（任务计划=1、计算机管理=2）、**0**（服务/组件服务/事件查看器）、
  负资源 ID（iSCSI=-1、ODBC=-1439、性能=-108、防火墙=-101…，这些 DLL 里确有该 ID 的
  组图标，负索引提取今天就是对的）；③ `SHGetFileInfoW(.lnk)` 与 `SHDefExtractIconW(dll,索引)`
  双重提取逐像素一致（屏蔽左下角快捷方式箭头后 avg≈6/255，与已知正确条目同噪声带）。
  根因：`lnk_icon_location` 的「**0 不补**」规则把 index=0 吞掉 → 裸 DLL 路径过不了
  `is_precise_icon_spec` → 跌到 `.msc` 目标 → `mmc.exe` 红色工具箱。修法：dll/ocx
  资源容器的 index=0 也补 `,0`（Explorer 正是按 ordinal 0 提取；.exe/.ico 不补，
  裸路径已精确）。附注：`filemgmt.dll`/`comres.dll`/`mycomput.dll` 主文件 .rsrc 里
  **没有**图标资源（仅 MUI+VERSION），但 shell 对这几个 MMC snap-in DLL 有逐文件的
  图标解析（对照组 ntdll.dll 拿到的是 `imageres.dll,-67` 通用图标），所以
  `SHDefExtractIconW(dll, 0)` 仍取到正确图标——**实测有效，机制未深究**。

---

## 5. 日志容量控制（2026-10-12）

- **语义**：`%APPDATA%\voke\voke.log` 只追加、不轮转文件。文件以追加模式打开
  （`OpenOptions::create+append+read`，取代旧的每次启动 `File::create` 截断
  清空；`.read(true)` 不可省——删头批要读回全文，纯 append 是 O_WRONLY 读会
  EBADF，O_APPEND 写语义不变），历史日志跨重启保留；一次写入使行数超过上限 N
  时，先从**头部删一批**最旧的行（批量 `max(1, N/10)`，9999 → 100 行），落一行
  中文滚动标记（`---- 日志超过 N 行上限，已删除最早 K 行 ----`，计入行数），
  再照常追加。删批而非逐行删，避免每写一行就整文件重写。
- **上限来源**：`VOKE_LOG_MAX_LINES` 环境变量，构建期经 `voke-app/build.rs`
  的 `cargo:rustc-env` 注入，运行期 `option_env!` 读取；未设/非法/为 0 默认
  **9999**（Info 级一天通常几百行，一行约 40–120 字节，9999 行 ≈ 0.6–1 MB，
  留足一次 debug 排查量又不至于 MB 级）。打包时覆盖：
  `VOKE_LOG_MAX_LINES=20000 bash packaging/build-installer.sh`。
- **实现位置**：纯逻辑在 `voke-sys/src/logcap.rs`（`RollingLogWriter`，不挂
  cfg）。放 voke-sys 而不是 voke-app：app 层测试在 Linux native 因缺
  fontconfig 跑不了（AGENTS.md 明属预期），下沉后 `cargo test -p voke-sys`
  能真跑删批/计数用例。
- **行处理口径（用户明确要求，勿硬编码换行）**：写统一 `writeln!`/`write!`
  宏；读/数行/按行分割统一 `std::io::BufRead::lines()`。仅写入缓冲区计 `\n`
  与打开时判末尾部分行两处字节级判断，用 `b'\n'` u8 常量并注释理由
  （`&[u8]` 不保证 UTF-8，过不了 `lines()`）。
- **踩坑**：曾实现过「超限就地截断重写」（seek(0)+set_len(0)+只留最新），
  被用户否决——一截断所有历史日志都没了，违背「一直追加」的预期。现行方案
  小上限（N<20，一批仅 1 行）时标记行会让行数略超上限（≤N+1），属有界可接受
  行为，默认 9999 不受影响。

---

## 6. 设置窗程序化销毁（2026-10-12）

- **决策**：设置窗「关闭」从「隐藏、复用」改为**程序化销毁**（ Zed 主窗同款
  姿势：`crates/zed/src/zed.rs:493-500` 拦 WM_CLOSE 返 false +
  `crates/workspace/src/multi_workspace.rs:555-581` 里 `window.remove_window()`）。
  用户拍板回收常驻的 ~26.5MB（880×600 逻辑像素 @125% 缩放 = 1100×750 设备像素，
  8 纹理 ≈ 26.4MB + atlas，与实测吻合；D3D device 本就共享不回收，正常）。
- **gpui 契约（gpui-pre-0.3.8 本机 registry 源码逐行核实，勿凭记忆）**：
  - `on_window_should_close` 的闭包**嵌套在 `update_window_erased` 内**运行
    （`window.rs:6732-6741` → `app/async_context.rs:315-318` → `app.rs:3020-3078`：
    窗口被 `take` 出 map → 跑闭包 → `trail` 检查 `removed`）。推论：回调内不能
    再调 `cx.update_window(同一 handle)`（slot 已 take，静默失败），只能用闭包
    参数的 `window.remove_window()`（`window.rs:2323-2326`，只置 `removed = true`）。
  - **必须返回 false**：平台侧 `handle_close_msg`（gpui-pre-windows
    `events.rs:336-341`）——true 放行 DefWindowProc 同步 DestroyWindow，Drop 任务
    后跑拿到死 HWND，重演泄漏；false 吞掉 WM_CLOSE，HWND 留给 Drop 任务
    （gpui-pre-windows `window.rs:610-631`）在存活期 RevokeDragDrop + DestroyWindow。
  - 回调是 `Fn` 不是 `FnMut`，重入守卫（第二个 WM_CLOSE 在 Drop 任务排队间隙内
    到达）必须走内部可变性：`Cell<bool>` 一次性闸门，抽到
    `voke-sys/src/close_guard.rs`（`CloseGuard`，与 logcap 同理由下沉——app 层
    测试在 Linux 因缺 fontconfig 跑不了）。
- **实现位置**：`voke-app/src/settings.rs` 的 `open()`（`on_window_should_close`
  注册处有含源码位置的长注释）；捕获改弱引用（`Entity::downgrade`），abort_recording
  保留且在 `remove_window` 之前；关闭即清 `ShellState::settings_window`，`open()`
  复用分支自愈（`handle.update` 失败 → 创建分支）作双保险。
- **收益与代价**：关闭全回收 ~26.5MB + HWND + vsync 循环项；重开 ≈ 首次打开成本
  （建窗 + 冷图集 glyph 光栅 + 页面重建，几十 ms 级），偶尔使用的页面可接受。
- **真机验证清单（20 轮开关）**：日志每轮成对出现「程序化销毁」+「设置窗口已打开」；
  关闭后 ~2s 的 `on_window_closed` 快照（main.rs）PrivateCommit 回落到面板基线
  ±2MB 且 20 轮不爬升；热键录制中点 X 旧热键恢复、无 panic。
- **上游 bug 仍在**：`gpui-pre-windows`（及 Zed 上游 `gpui_windows`、gpui-fast-windows
  逐字同源）的 RevokeDragDrop-in-Drop 结构没变，issue 文稿待提交（用户拒绝
  vendor/patch 上游）。所有「放行 WM_CLOSE 真关窗」的路径都会漏，gpui 自测也走
  程序化关闭（`window.rs:8738`）。
