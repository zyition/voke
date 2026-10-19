# Voke

[![license](https://img.shields.io/badge/license-MIT-blue)](LICENSE)
[![CI](https://github.com/zyition/voke/actions/workflows/ci.yml/badge.svg)](https://github.com/zyition/voke/actions/workflows)
[![language](https://img.shields.io/badge/language-Rust-dea584)](https://www.rust-lang.org)
[![platform](https://img.shields.io/badge/platform-Windows-0078D6)](https://www.microsoft.com/windows)

**Voke**（读作 /voʊk/，invoke 之魂）是一个 Windows 原生、键盘优先的启动器，类 PowerToys Run。
Rust 自研，基于 [gpui-kit](https://github.com/longbridge/gpui-kit) 做界面，单 exe、零运行时依赖、托盘常驻。

为什么要做它：同类工具（PowerToys Run / Command Palette）多为 Electron / WebView 方案，内存占用偏高。
Voke 用 Rust + gpui-kit 自研，稳态常驻内存约 **60–70MB**（实测值，其中大部分是 gpui 图形栈的地板开销，
会随 gpui 版本浮动；设置页已可正常销毁回收，不再导致内存只增不减），托盘常驻、单 exe、不依赖外部运行时。

## 亮点

- 全局热键唤起（默认 `Alt+Space`，可在设置页录制改键）。
- 10 个内置插件：应用、窗口、保持唤醒、终端配置、工作区、计算器、时间戳、Base64、UUID、随机值。
- `#` 拉出插件列表，拼音 / 英文别名过滤；插件支持二级交互（如 Base64 编 / 解、随机值重新生成）。
- 托盘常驻：菜单首项为 awake 状态快照（打开菜单即时刷新），左键单击直接唤起面板。
- 开机自启（写 `HKCU\...\Run`）、深色模式跟随系统、单实例（二次启动唤起已有实例）。
- 拼音 / 模糊匹配：直接打拼音首字母也能命中应用，全程不经过输入法。

## 快速开始

### 下载与安装

两种获取方式（发版产物，详见 [发版流程](#构建与开发)）：

- **安装包**（`voke-setup-<版本>.exe`）：双击安装，默认 per-user 安装到
  `%LOCALAPPDATA%\Programs\Voke`，并可选写入开机自启。
- **便携版**（`voke-vX.Y.Z-windows-x86_64.zip`）：解压即得 `voke.exe`，无需安装，可放任意目录。

### 配置

配置文件位于 `%APPDATA%\voke\config.toml`：

- 文件缺失时全部用默认值；只写其中任意一项也算数，未写的字段沿用默认。
- 也可打开设置页（`Ctrl+,` 或托盘菜单）可视化编辑热键、主题、各插件的启用 / 全局 /
  权重 / 前缀 / 激活方式 / 别名。

## 键盘用法

| 按键 | 作用 |
|---|---|
| `Alt+Space` | 唤起 / 隐藏面板（toggle，可在设置页改键） |
| `↑` / `↓` | 在结果列表中上下移动 |
| `Ctrl+N` / `Ctrl+P` | 在结果列表中向下 / 向上切换选项（等价于 `↓` / `↑`，键盘驱动的核心导航键） |
| `Enter` | 执行当前选中项 |
| `Esc` | 隐藏面板（失焦也会自动隐藏） |
| `#` | 拉出插件选择列表（拼音 / 英文别名过滤） |
| `Ctrl+,` | 打开设置页 |
| 托盘左键单击 | 直接唤起面板 |

## 插件

内置 10 个插件。下表列出默认触发前缀；**无前缀**的 5 个插件直接出现在全局结果里，
其余需输入对应前缀（间接激活型需「前缀 + 一个空格」才路由，避免与裸 `#` 的插件列表入口冲突）。

| id | 作用 | 默认前缀 |
|---|---|---|
| `apps` | 启动应用（开始菜单 / 桌面 `.lnk`、UWP） | 无（全局结果） |
| `windows` | 窗口切换（枚举并激活已开窗口） | 无（全局结果） |
| `awake` | 保持唤醒（系统 / 连屏幕，到期自动失效） | 无（全局结果） |
| `terminal` | Windows Terminal 配置（按 profile 启动） | 无（全局结果） |
| `workspaces` | 工作区（读 Zed / VSCode 最近工作区历史） | 无（全局结果） |
| `calculator` | 计算器 | `=`（输入即路由） |
| `timestamp` | 时间戳转换 | `@t` + 空格 |
| `base64` | Base64 编 / 解 | `@b` + 空格 |
| `uuid` | 生成 UUID | `@u` + 空格 |
| `random` | 生成随机值（整数 / 字符串 / 特殊字符 / 十六进制，支持重新生成） | `@r` + 空格 |

> `#` 是插件列表专用入口；插件前缀统一使用 `@` 命名空间，避免与 `#` 构成「短前缀
> 遮蔽长前缀」。设置页会拦截互为前缀的配置，旧版 `#b/#u/#t/#r` 会在加载时自动
> 迁移到新默认值。
>
> 前缀、权重、激活方式均可在 `config.toml` 或设置页调整。完整的 Provider 契约与默认配置见
> [`docs/architecture.md`](docs/architecture.md)。

## 构建与开发

完整的环境准备与踩坑见 [`CONTRIBUTING.md`](CONTRIBUTING.md)。常用命令：

```bash
cargo test -p voke-core              # 逻辑层测试（纯 Rust，零 OS 依赖）
cargo test -p voke-sys               # 系统层测试（非 Windows 侧只测纯逻辑）
cargo check --target x86_64-pc-windows-gnu -p voke-app --all-targets  # UI 改动验收
cargo clippy -p voke-core -p voke-sys --all-targets                    # 新代码零警告
```

产物：`target/x86_64-pc-windows-msvc/release/voke.exe`；安装包
`packaging/dist/voke-setup-*.exe`（per-user 安装到 `%LOCALAPPDATA%\Programs\Voke`）。
打安装包：`bash packaging/build-installer.sh`。

发版：推 `vX.Y.Z` tag 即触发 `.github/workflows/release.yml`，在 windows-latest 上出
安装包与便携 zip，并建立 GitHub Release。版本号以 tag 为唯一事实来源，本地无需手动改版本文件。

技术栈：Rust stable（edition 2024）、gpui-kit 0.7.1（re-export `gpui-pre` / `gpui-component`）、
`tray-icon` + `muda`（托盘）、`windows` 0.62（Win32 / COM）、`pinyin` + `nucleo-matcher`（拼音 / 模糊匹配）、
`rusqlite`（只读编辑器历史库）、Inno Setup 7.1（打包）。

## 架构

一句话：纯逻辑 / Win32 调用 / UI 三层分离，逻辑层零 OS 依赖，可在 Linux 原生秒级测试。

| crate | 职责 | 约束 |
|---|---|---|
| `voke-core` | 查询引擎、Provider 契约、拼音 / 模糊匹配、配置 | 零 gpui / 零 `windows` 依赖 |
| `voke-sys` | Win32 / COM 调用（unsafe 隔离区） | OS 调用一律 `#[cfg(windows)]` 门控 |
| `voke-app` | gpui-kit UI 与程序入口 | 只面向 Windows |

详见 [`docs/architecture.md`](docs/architecture.md)。

## 参与贡献

- 提交信息采用 **Conventional Commits + 中文描述**（如 `feat: 支持按拼音首字母模糊匹配`），
  类型常用 `feat` / `fix` / `perf` / `refactor` / `docs` / `test` / `chore`。
- **CHANGELOG 由 [git-cliff](https://github.com/orhun/git-cliff) 自动生成，不要手改 `CHANGELOG.md`**。
- CI 会跑 `cargo fmt --all -- --check`、`cargo clippy` 与 `cargo test`；提交前请本地跑通。
- 更多分层铁律、严禁事项见 [`CONTRIBUTING.md`](CONTRIBUTING.md)。

## 安全

Voke 不联网、不上报任何数据（无遥测、无服务端）。涉及自启、外部程序执行、配置读写等安全面的说明
见 [`SECURITY.md`](SECURITY.md)；安全漏洞请按该文档的披露流程反馈，勿公开提 issue。

## License

[MIT](LICENSE)。

## 文档索引

- [`docs/architecture.md`](docs/architecture.md) —— 架构、数据流、Provider 契约、技术选型、里程碑
- [`docs/HISTORY.md`](docs/HISTORY.md) —— 构建环境、主题设计、验证与踩坑档案
- [`docs/KNOWN-ISSUES.md`](docs/KNOWN-ISSUES.md) —— 已知限制与预期行为（上游泄漏、标题层级、热键迁移等）
- [`docs/BACKLOG.md`](docs/BACKLOG.md) —— 待办与未来工作
- [`docs/notes/memory-footprint.md`](docs/notes/memory-footprint.md) —— 内存优化方案（探索，未实施）
- [`docs/design/settings-plugins.md`](docs/design/settings-plugins.md) —— 设置页 / 插件配置 / 托盘 / 自启施工依据
- [`AGENTS.md`](AGENTS.md) —— 给 AI agent 的仓库须知
- [`CONTRIBUTING.md`](CONTRIBUTING.md) —— 贡献指南
- [`SECURITY.md`](SECURITY.md) —— 安全与漏洞披露
