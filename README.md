# Voke

[![license](https://img.shields.io/badge/license-MIT-blue)](LICENSE)
[![CI](https://github.com/zyition/voke/actions/workflows/ci.yml/badge.svg)](https://github.com/zyition/voke/actions/workflows/ci.yml)
[![language](https://img.shields.io/badge/language-Go-00ADD8)](https://go.dev)
[![platform](https://img.shields.io/badge/platform-Windows-0078D6)](https://www.microsoft.com/windows)

**Voke**（读作 /voʊk/，invoke 之魂）是一个 Windows 原生、键盘优先的启动器，类 PowerToys Run。
Go + [mygo](https://github.com/egoist/mygo) 实现，单 exe、不依赖 .NET / WebView / 外部运行时，托盘常驻。

为什么要做它：同类工具多为 Electron / WebView 方案，内存与启动开销偏高。Voke 用 Go 直接调
Win32 / COM，界面走 mygo 的 D3D11 后端；启动后 PrivateCommit 约 55MB，应用索引完成后约 110MB，
其中大部分是图形栈与系统枚举的原生占用（Go 堆只有几 MB）。设置窗关闭即销毁，图标缓存有界，
不会因为反复使用持续增长。

## 亮点

- 全局热键唤起（默认 `Alt+Space`，可在设置页录制改键）。
- 每个插件可绑定独立的全局快捷键（**默认不绑定**，设置页录制），按下直达该插件。
- 管理员窗口粘贴、终端 profile 提权打开按需弹一次 UAC：由临时辅助进程完成，不常驻
  高权限进程。
- 11 个内置插件：应用、窗口、保持唤醒、终端配置、工作区、计算器、时间戳、Base64、UUID、随机值、剪贴板历史。
- `#` 拉出插件列表，拼音 / 英文别名过滤；插件支持二级交互（如 Base64 编 / 解、随机值重新生成）。
- 托盘常驻：右键打开菜单（首项为 awake 状态快照），左键双击唤起面板。
- 开机自启（写 `HKCU\...\Run`）、深色模式跟随系统、单实例（二次启动唤起已有实例）。
- 拼音 / 模糊匹配：直接打拼音首字母也能命中应用，全程不经过输入法。

## 快速开始

### 下载与安装

两种获取方式（发版产物）：

- **安装包**（`voke-setup-<版本>.exe`）：双击安装，默认 per-user 安装到
  `%LOCALAPPDATA%\Programs\Voke`；覆盖安装会自动结束正在托盘的旧实例。
- **便携版**（`voke-vX.Y.Z-windows-x86_64.zip`）：解压即得 `voke.exe`，无需安装，可放任意目录。

开机自启由设置页开关控制（写入 / 删除 `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`），
安装器本身不写自启项。

### 配置

配置文件位于 `%APPDATA%\voke\config.toml`：

- 文件缺失时全部用默认值；只写其中任意一项也算数，未写的字段沿用默认。
- 也可打开设置页（`Ctrl+,` 或托盘菜单）可视化编辑热键、主题、各插件的启用 / 全局 /
  权重 / 前缀 / 激活方式 / 全局快捷键。设置修改即时生效，没有“保存”按钮；插件未启用时
  其余配置项整体禁用。
- 插件快捷键写在 `[providers.<id>]` 的 `hotkey` 字段（如 ``hotkey = "Alt+`"``）；
  没有默认值，留空即不绑定。
  只有**已启用**的插件才会注册，停用会立即注销；与主热键或其它启用插件重复时设置页
  会就地提示且不落盘。组合也可能被系统或其它程序占用，提示“占用”时换一个即可
  （例如 `Alt+\`` 在部分机器上已被其它工具注册）。
- 前缀冲突、含空白或占用字母/数字保留键时，设置页会就地提示且不落盘。
- 剪贴板历史条数在设置页的「剪贴板」插件区修改，也可写 `[clipboard] max_entries`
  （默认 100，范围 1–1000）；置顶最多 10 条，不占普通名额。历史只保存在内存里，
  退出即清空。

## 键盘用法

| 按键 | 作用 |
|---|---|
| `Alt+Space` | 唤起 / 隐藏面板（toggle，可在设置页改键） |
| 插件快捷键 | 用户为插件单独录制；隐藏时唤起并直达该插件，已在该插件时只聚焦，不做隐藏（默认无绑定） |
| `↑` / `↓` | 在结果列表中上下移动 |
| `Ctrl+N` / `Ctrl+P` | 向下 / 向上切换选项（等价于 `↓` / `↑`） |
| `Enter` | 执行当前选中项 |
| `Esc` | 隐藏面板（失焦也会自动隐藏） |
| `Backspace` | 先清空当前层查询词，再逐级退出插件 / 子模式 |
| `#` | 拉出插件选择列表（拼音 / 英文别名过滤） |
| `Ctrl+,` | 打开设置页 |
| `Ctrl+Shift+Delete` | 选中窗口结果时直接结束其所属进程（托盘 / 后台应用也会退出，未保存数据会丢失） |
| 托盘左键双击 | 唤起面板（右键打开菜单） |

剪贴板历史插件（`@c`）内另有：

| 快捷键 | 动作 |
|---|---|
| `Enter` | 按原格式粘贴到唤起前的窗口（终端自动改用 `Shift+Insert`） |
| `Ctrl+Enter` | 仅粘贴纯文本（图片条目无此动作） |
| `Ctrl+Shift+P` | 置顶 / 取消置顶（最多 10 条，不占普通条数） |
| `Ctrl+Shift+D` | 删除当前条目 |

粘贴目标是管理员窗口时，UIPI 会静默拦截普通按键模拟；此时会弹一次 UAC，由临时的
提权辅助进程代为发送粘贴键。取消 UAC 或辅助进程失败时，内容已在剪贴板里，面板会
重新显示并提示手动粘贴。

终端配置插件（`terminal`）内另有：

| 快捷键 | 动作 |
|---|---|
| `Enter` | 按 profile 打开 Windows Terminal |
| `Ctrl+Enter` | 以管理员身份打开该 profile（UAC） |

## 插件

内置 11 个插件。**无前缀**的 5 个插件直接出现在全局结果里；其余需输入对应前缀
（Indirect 需「前缀 + 一个空格」才路由）。

| id | 作用 | 默认前缀 |
|---|---|---|
| `apps` | 启动应用（开始菜单 Applications：桌面程序快捷方式 + packaged 应用） | 无（全局结果） |
| `windows` | 窗口切换（枚举并激活已开窗口；行内可结束任务） | 无（全局结果） |
| `awake` | 保持唤醒（系统 / 连屏幕，到期自动失效） | 无（全局结果） |
| `terminal` | Windows Terminal 配置（按 profile 启动；Ctrl+Enter 管理员打开） | 无（全局结果） |
| `workspaces` | 工作区（读 Zed / VS Code 最近工作区历史） | 无（全局结果） |
| `calculator` | 计算器 | `=`（输入即路由） |
| `timestamp` | 时间戳转换 | `@t` + 空格 |
| `base64` | Base64 编 / 解 | `@b` + 空格 |
| `uuid` | 生成 UUID | `@u` + 空格 |
| `random` | 生成随机值（整数 / 字符串 / 特殊字符 / 十六进制，支持重新生成） | `@r` + 空格 |
| `clipboard` | 剪贴板历史（文本 / HTML / RTF / 图片；粘贴、纯文本粘贴、置顶、删除） | `@c` + 空格 |

> `#` 是插件列表专用入口；插件前缀统一使用 `@` 命名空间，避免与 `#` 构成「短前缀
> 遮蔽长前缀」。设置页会拦截互为前缀的配置，旧版 `#b/#u/#t/#r` 会在加载时自动
> 迁移到新默认值。
>
> 插件别名（如 `b`/`b64`）是内置元数据，用于 picker 过滤，不在配置文件里编辑。
> 完整的 Provider 契约与默认配置见 [`docs/architecture.md`](docs/architecture.md)。

## 构建与开发

完整的环境准备、分层纪律与验证命令见 [`CONTRIBUTING.md`](CONTRIBUTING.md)。常用命令：

```bash
cd poc/voke-go
gofmt -l . && go vet ./... && go test -race -count=1 -shuffle=on ./...

GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build \
  -tags mygo_noinspector -ldflags="-s -w -H windowsgui" -o /tmp/voke.exe .
```

- 产物：`/tmp/voke.exe`（交叉构建）；安装包走 `bash packaging/build-installer.sh`，
  输出 `packaging/dist/voke-setup-*.exe`（per-user 安装到 `%LOCALAPPDATA%\Programs\Voke`）。
- 发版：推 `vX.Y.Z` tag 即触发 `.github/workflows/release.yml`，在 windows-latest 上
  构建 Go exe、注入版本、出安装包与便携 zip，并建立 GitHub Release；版本号以 tag 为
  唯一事实来源。

技术栈：Go（版本见 `poc/voke-go/go.mod`）、mygo v0.6.2（D3D11 / DirectWrite）、
`go-pinyin`、nucleo-matcher 0.3.1 评分的 Go 移植、SQLite（Zed / VS Code 历史只读）、
Inno Setup 7.1（打包）。Rust/gpui 旧实现保留在仓库根目录作参考，不参与 CI 与发版。

## 架构

一句话：纯逻辑 / Provider / 平台 / UI / 装配分层，逻辑与 Provider 可 Linux 原生测试，
mygo 平台能力与 Win32 调用集中在 `internal/platform`。

| 目录 | 职责 | 约束 |
|---|---|---|
| `internal/logic` | 引擎、配置、面板状态、搜索/频率、图标缓存、日志 | 纯 Go，零 mygo / syscall |
| `internal/match` | 拼音搜索键 + nucleo 模糊评分 | 纯 Go |
| `internal/providers` | Provider 契约与 11 个插件实现 | 纯查询，副作用以 Action 返回 |
| `internal/platform` | mygo 平台模块 / Win32 / COM 边界 | 唯一封装平台能力的层 |
| `internal/ui` | mygo 组件、主题、面板/设置视图 | immediate-mode |
| `internal/app` | 装配、生命周期、主线程状态与执行 | 主线程约束 |

详见 [`docs/architecture.md`](docs/architecture.md)。

## 参与贡献

- 提交信息采用 **Conventional Commits + 中文描述**（如 `feat: 支持按拼音首字母模糊匹配`），
  类型常用 `feat` / `fix` / `perf` / `refactor` / `docs` / `test` / `chore`。
- **CHANGELOG 由 [git-cliff](https://github.com/orhun/git-cliff) 自动生成，不要手改 `CHANGELOG.md`**。
- CI 会跑 `gofmt`、`go mod tidy` 自检、`go vet`、`go test -race` 与 Windows 交叉构建；
  提交前请本地跑通。
- 更多分层铁律、严禁事项见 [`CONTRIBUTING.md`](CONTRIBUTING.md)。

## 安全

Voke 不联网、不上报任何数据（无遥测、无服务端）。涉及自启、外部程序执行、配置读写等安全面的说明
见 [`SECURITY.md`](SECURITY.md)；安全漏洞请按该文档的披露流程反馈，勿公开提 issue。

## License

[MIT](LICENSE)。

## 文档索引

- [`docs/architecture.md`](docs/architecture.md) —— 当前 Go + mygo 架构、查询链路、Provider 契约、配置
- [`docs/KNOWN-ISSUES.md`](docs/KNOWN-ISSUES.md) —— 当前已知限制与预期行为
- [`docs/BACKLOG.md`](docs/BACKLOG.md) —— 待办、产品决策与未来工作
- [`docs/notes/memory.md`](docs/notes/memory.md) —— 内存口径、实测与已落地优化
- [`docs/notes/matching.md`](docs/notes/matching.md) —— 匹配 / 拼音一致性
- [`docs/legacy/README.md`](docs/legacy/README.md) —— Rust/gpui 时代与 Go PoC 阶段的历史存档
- [`AGENTS.md`](AGENTS.md) —— 给 AI agent 的仓库须知
- [`CONTRIBUTING.md`](CONTRIBUTING.md) —— 贡献指南
- [`SECURITY.md`](SECURITY.md) —— 安全与漏洞披露
