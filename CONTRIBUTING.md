# 贡献指南（CONTRIBUTING）

感谢你考虑为 **voke** 做出贡献。本文说明当前 **Go + mygo** 主线的开发方式；
更完整的环境与历史踩坑见 [`docs/legacy/rust-history.md`](docs/legacy/rust-history.md)（Rust 时代存档，仅作参考）。

## 一、项目是什么

**voke** 是一个键盘优先的 Windows 桌面启动器，用 Go 编写，界面基于
[mygo](https://github.com/egoist/mygo)。

- **Windows-only**：全局热键、托盘、自启、窗口枚举、终端/工作区读取等能力依赖 Win32 / COM。
- **单 exe**：最终产物是一个独立的 `voke.exe`，不依赖 .NET、浏览器或外部运行时。
- **无遥测、无服务端**：程序不联网、不上报任何数据（详见 `SECURITY.md`）。

当前源码在 **`poc/voke-go/`**（历史上是 PoC 目录，现在是唯一生产实现）。
仓库根目录的 `voke-core/`、`voke-sys/`、`voke-app/` 是 Rust/gpui 参考实现，不参与 CI 与发版。

## 二、开发前置

- **Go**：版本以下面文件为准，不要依赖系统里恰好装了什么：

  ```bash
  cat poc/voke-go/go.mod | head -3
  ```

- **CGO 不需要开**：Windows 交叉构建用 `CGO_ENABLED=0`。
- **Inno Setup 7**：仅打包安装程序时需要（`winget install --id JRSoftware.InnoSetup.7 -e`）。
- 运行环境是 **WSL2 + Linux 发行版**，默认 shell 不一定是 bash（fish/zsh 都要能跑）。

## 三、常用命令

在 `poc/voke-go` 下执行：

```bash
# 格式、静态检查、race 测试（CI 同款）
gofmt -l . && go vet ./... && go test -race -count=1 -shuffle=on ./...

# Windows 交付构建（GUI 子系统 + 图标 syso）
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build \
  -tags mygo_noinspector -ldflags="-s -w -H windowsgui" -o /tmp/voke.exe .

# 安装包（Go 构建 + Inno Setup；版本号默认取最近的 vX.Y.Z tag）
bash packaging/build-installer.sh
```

其他实用环境变量：

| 变量 | 作用 |
|---|---|
| `VOKE_CONFIG` / `VOKE_FREQ` | 覆盖 `config.toml` / `frequency.toml` 路径（测试与便携模式） |
| `VOKE_SELFTEST=1` | 启动时跑 awake / clipboard 自检并打日志 |
| `VOKE_VERSION` | 本地打安装包时覆盖版本号（三段数字） |
| `VOKE_LOG_MAX_LINES` | 本地打安装包时注入日志行数上限（默认 9999） |
| `ISCC` | 指定 `ISCC.exe` 路径 |

> **Windows UI 的真机边界**：mygo 的窗口/托盘/Shell 调用走主线程，headless 测试
> 到不了真实窗口行为；这些改动需要真机跑一遍（`/tmp` 产物复制到 Windows 再启动）。
> 单元测试负责逻辑与状态机，不负责“窗口看起来对不对”。

### 测试纪律

- **调用生产入口**，不要在测试里复制一份生产逻辑（否则改坏生产代码测试照样绿）。
- 阈值写**绝对值**，不要拿被测常量当阈值（常量被改成 0 时断言会失去意义）。
- 新增/修改断言后做一次“把实现故意改坏，测试会不会红”的检查。
- mygo 主线程 API（CreateWindow / Shell / Tray / Screen / Clipboard）在无主循环时
  会阻塞或 panic；这类调用点用接口/函数变量隔离，headless 只测纯逻辑与状态。

## 四、分层铁律

- `internal/logic/**` 与 `internal/providers/**` **不得** import mygo / syscall，
  保持 Linux 原生可测；副作用以 `providers.Action` 值返回。
- **所有 mygo 平台模块（Shell / Screen / Tray / Clipboard / GlobalShortcut / 单实例）
  与 Win32 / COM 调用只能出现在 `internal/platform/**`**；调用方不写平台分支。
  `internal/ui` 可以 import `mygo/ui` 组件，`internal/app` 可以用 mygo 的 App/Window
  生命周期 API，但不要绕过 `platform` 直接调平台模块。
- 平台专属文件必须配 `_stub.go`（非 Windows 占位），保证 Linux 侧
  `go build ./...` / `go test ./...` 保持绿。
- mygo 的窗口、托盘、Shell、Screen、剪贴板、热键都在**主线程**；平台层每个函数都要
  注释是否可后台调用。后台结果回主线程只走 `Window.Update`。
- 插件默认值只写在 `internal/app/providers.go` 的 Descriptor 里；引擎、UI、平台层
  不复制第二份默认前缀/权重。

## 五、严禁事项

1. **格式必须 `gofmt` 干净**：提交前跑 `gofmt -w` 或至少 `gofmt -l .` 为空；
   CI 会强制检查。不要只格式化自己改的文件之外的无关文件。
2. **严禁编造 API**：mygo 的签名查模块缓存
   `~/go/pkg/mod/github.com/egoist/mygo@<version>/` 或官网文档；标准库/Win32 以
   `go doc`、`syscall`/`golang.org/x/sys/windows` 的真实定义为准，不要凭记忆写。
3. **不要在 `internal/logic` 里偷偷调 OS**，也不要为了 UI 方便把状态放到非主线程。

## 六、Commit / PR

- Conventional Commits + 中文描述，例如 `feat: 支持按拼音首字母模糊匹配`。
- 类型常用 `feat` / `fix` / `perf` / `refactor` / `docs` / `test` / `chore`；
  CHANGELOG 由 git-cliff 自动生成，**不要手改**。
- 一个 PR 只做一件事；提交前跑第三节的格式/静态检查/测试。
- 涉及自启、外部程序执行、配置读写等安全面变更，请同步更新 `SECURITY.md` 的威胁面。

## 七、目录结构

```text
poc/voke-go/
  main.go             入口（只调 app.Main）
  internal/app/       装配、生命周期、面板/设置、意图执行
  internal/logic/     引擎、配置、面板状态、搜索、频率、缓存、日志
  internal/match/     拼音搜索键 + nucleo 评分移植
  internal/providers/ Provider 契约与 10 个插件
  internal/platform/  mygo / Win32 / COM 边界
  internal/ui/        mygo 组件、主题、视图
packaging/            安装器脚本与 Inno Setup 脚本
docs/                 当前文档 + docs/legacy 历史存档
voke-*/               Rust/gpui 参考实现（不参与 CI/发版）
```
