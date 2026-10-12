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
- **配置内核**：落盘 `%APPDATA%\voke\config.toml`（绝对路径，自启动 CWD 不确定），
  `ConfigPatch` 部分合并 + `normalize` 校验；默认配置锁死，坏值宽松回落。
- **设置窗「关闭 = 隐藏、复用」的由来**：早期每次关闭设置窗都真实销毁窗口，但上游平台层
  有拖放目标泄漏（见 [`KNOWN-ISSUES.md`](KNOWN-ISSUES.md)），关窗会滞留整套 GPU 资源。
  改用「关闭即隐藏、复用窗口」止血，避免改动上游框架。
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
