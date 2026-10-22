# AGENTS.md —— 进这个仓库干活必须知道的事

给人看的架构/上手看 [`README.md`](README.md)；当前架构看 [`docs/architecture.md`](docs/architecture.md)。
本文件只写**非通用**的纪律与踩坑。正文一律简体中文。

当前生产实现是仓库根目录的 **Go + mygo** 模块（`module voke`）。main 历史从首个 Go 提交起算；
Rust/gpui 参考实现已归档到 **`legacy-rust` 分支与 `v0.1.x` tag**，不在当前工作树，
也不参与 CI 与发版。Rust 时代文档在 [`docs/legacy/`](docs/legacy/README.md)；Go 注释里
引用的 `voke-core/` / `voke-sys/` 路径与行号是迁移时的对照 oracle，对应 `legacy-rust` 分支。

## 仓库纪律

- 注释 / 文档 / commit message 一律**简体中文**。
- commit message 走 **Conventional Commits**：`feat:` / `fix:` / `perf:` / `refactor:` /
  `test:` / `docs:` 等英文前缀 + 中文描述。CHANGELOG 由 git-cliff 按前缀生成，
  `style|build|ci|chore|revert` 不会进 CHANGELOG。
- **格式统一 `gofmt`**：提交前在仓库根目录跑 `gofmt -l .`（应为空）或
  `gofmt -w`；CI 强制检查。不要顺手格式化与本次改动无关的文件。
- **严禁编造 API**：mygo 签名查模块缓存
  `~/go/pkg/mod/github.com/egoist/mygo@<version>/` 或官网文档；Win32/标准库用
  `go doc`、`golang.org/x/sys/windows` 真实定义。不要凭记忆写。
- `git add` 只加自己改的文件（**禁 `git add -A`**）；commit 用
  `git -c user.name="zyition" -c user.email="2321694+zyition@users.noreply.github.com" commit`，
  简体中文 3–5 行；**不 force push**。

## 分层铁律（Go）

- `internal/logic/**`、`internal/providers/**`、`internal/match/**` **零 mygo、零 syscall**，
  保持 Linux 原生可测；插件副作用以 `providers.Action` 值返回。
- mygo 平台模块（Shell / Screen / Tray / Clipboard / GlobalShortcut / 单实例）与
  Win32 / COM 调用只进 `internal/platform/**`；`internal/ui` 可用 `mygo/ui` 组件，
  `internal/app` 可用 App/Window 生命周期 API，但不得绕过 `platform` 调平台模块。
  Windows 专属文件配 `_stub.go`，非 Windows 侧 `go build ./...` / `go test ./...`
  必须保持绿。
- mygo 窗口 / 托盘 / Shell / Screen / 剪贴板 / 热键都在**主线程**；平台函数注释必须
  写明是否可后台。后台结果回主线程只走 `Window.Update`。
- 默认值只写在 `internal/app/providers.go` 的 Descriptor；引擎 / UI / 平台层不复制
  第二份默认前缀、权重、激活方式、条数。
- 配置归一（C1–C7）与引擎判定都只有一处：`internal/logic/config` + `internal/logic/engine`；
  别在 UI / provider 里另写一套前缀或排序判定。

## WSL / Windows 环境

- 系统是 **WSL2 + Linux 发行版**，默认 shell 不一定是 bash（fish/zsh 都要能跑）。
- Go 交叉构建：`GOOS=windows GOARCH=amd64 CGO_ENABLED=0`，不需要 clang/MSVC/cargo。
- 调 PowerShell 时注意交互输入：命令末尾加 `< /dev/null`，否则可能卡住等待 stdin。
- 部署 exe 时旧实例会锁文件：Windows 允许把运行中的 exe **改名**（不能删/覆写），
  可以先 `mv voke.exe voke.exe.runbak-...` 再放新产物。
- 安装器需要 Inno Setup 7；winget id 必须是 `JRSoftware.InnoSetup.7`
  （默认 `JRSoftware.InnoSetup` 只到 6.x，scoop 也没有 7）。

## 验证命令组合

Go（当前主线，与 CI 同款）：

```bash
gofmt -l . && go vet ./... && go test -race -count=1 -shuffle=on ./...
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build \
  -tags mygo_noinspector -ldflags="-s -w -H windowsgui" -o /tmp/voke.exe .
```

Rust/gpui 参考实现已归档到 `legacy-rust` 分支与 `v0.1.x` tag；main 上没有 Cargo
配置，日常验证不需要、也无法在 main 上运行 `cargo` 命令。

## 部署与安装包

- exe 部署到 Windows 侧目标目录（如 `%LOCALAPPDATA%\Programs\Voke\voke.exe`）；
  运行中的旧 exe 先改名绕过占用。
- 安装器：`bash packaging/build-installer.sh`（Go 构建 + Windows 侧 ISCC）。
  按最近 tag 取版本，可用 `VOKE_VERSION=0.2.3` 覆盖；`ISCC` 可指定编译器路径。
- 安装器在 `CurStepChanged(ssInstall)` 直接 `taskkill /F` 结束托盘实例（无未保存
  数据）；不用 Restart Manager，避免托盘进程 30 秒超时。

## 发版流程

推 `vX.Y.Z` tag 即触发 **Go 版**发版：`git tag v0.2.3 && git push origin v0.2.3` →
`.github/workflows/release.yml` 在 windows-latest 用 Go 构建根目录模块，出安装包
（`voke-setup-<ver>.exe`）与 portable zip（`voke-vX.Y.Z-windows-x86_64.zip`）；ubuntu job
用 git-cliff 把本版本前置到 `CHANGELOG.md`（保留已归档版本的旧条目）并回推默认分支，
建 GitHub Release 挂上两个产物。
Rust 时代的 CI / 发版流水线已随代码归档到 `legacy-rust` 分支；当前 CI 为
`.github/workflows/ci.yml`，发版为 `release.yml`。

版本号以 **tag 为唯一事实来源**：release 构建用
`-ldflags -X voke/internal/app.buildVersion=<ver>` 把版本注入
设置页「关于」（本地未注入显示「开发版」）；Go exe 的 VERSIONINFO 仍未注入（已知差距）。
ISCC 用 `-dVokeVersion` 覆盖安装器 AppVersion 与产物文件名。手动 `workflow_dispatch`
只出 artifacts、不建 Release。

## 项目特有机制（一句话 + 指向文件）

- 单实例：mygo `RequestSingleInstanceLock`（per-user socket，按 app 名；非命名互斥体）
  ——`internal/platform/singleinstance.go`、`internal/app/main.go`。
- 热键：mygo `GlobalShortcut` 主线程直接回调，改键「撤旧→注新→失败回滚」——
  `internal/platform/hotkey.go`、`internal/app/settings.go`。
- 插件全局快捷键：无默认值，用户逐插件录制；只注册已启用插件，停用即注销。录制时
  主热键与所有插件热键统一挂起（否则旧组合被系统先吃掉），结束后重建。插件热键是
  “直达/切换”而非显隐 toggle（隐藏仍归主热键 / Esc），同时是“立即执行”：每次全新
  进入插件，不读取也不覆盖主会话快照；主热键 / 托盘双击 / 二次启动才恢复主会话
  （方案 C）——`internal/app/hotkeys.go`、`internal/app/session.go`、`internal/app/settings.go`。
- 托盘：mygo `Tray`；左键双击显示面板，右键菜单；菜单是 `SetMenu` 快照，awake
  状态变化与面板显隐时重建——`internal/app/panel.go`。
- 设置窗「关闭 = 程序化销毁」：`Window.Update` 里 `Destroy`，回收渲染资源——
  `internal/app/settings.go`。
- 应用列表：`shell:AppsFolder`（mygo `Shell.InstalledApps`）+ 唤起时 60s 防抖重扫；
  窗口列表每次查询实时枚举——`internal/app/apps.go`、`internal/platform/shell.go`。
- 图标缓存：解码位图 256 条 LRU（`internal/logic/iconcache`）、PNG 字节 256 条 FIFO
  （`internal/platform` 的 iconcache），nil 负缓存保留；面板隐藏 10s 后解码缓存裁到 64
  条（Wox 同款，设置窗打开时跳过）。
- 日志容量：`%APPDATA%\voke\voke.log`，跨启动追加，超过上限从头部删一批
  （`max(1, N/10)`，默认 N=9999）。构建期注入：
  `-ldflags -X voke/internal/platform.logMaxLinesRaw=20000`；
  本地安装器脚本会把 `VOKE_LOG_MAX_LINES` 映射到该参数。逻辑在 `internal/logic/logcap`。
- 剪贴板历史（`@c`）：内存态、不持久化；后台每 500ms 轮询 `GetClipboardSequenceNumber`，
  变化后回主线程经 mygo `Clipboard.Read` 采文本/HTML/RTF/PNG，跳过敏感标记与文件列表。
  粘贴写回后先恢复唤起前的窗口，再发 `Ctrl+V`（终端用 `Shift+Insert`）；失败时保留面板并
  提示手动粘贴。历史存储 `internal/logic/cliphist`，采集/按键 `internal/platform/clipboard*`，
  监控与自我写入抑制 `internal/app/clipboard.go`。
- 管理员窗口粘贴：UIPI 会静默拦截 SendInput（可能仍返回成功），不能靠返回值判断。
  `platform.TargetNeedsElevation` 先检测目标进程权限；更高时经 `ShellExecuteEx(runas)`
  拉起 `voke.exe --elevated-paste` 辅助进程代发粘贴键（UAC）。辅助进程在单实例锁之前
  分流，只粘贴、不发回车，并复核目标为管理员进程。同一 `platform.RunElevated` 供终端
  “以管理员身份打开”（Ctrl+Enter）使用——实现见 `internal/platform/elevate*.go`、
  `internal/app/elevated.go`、`internal/app/clipboard.go`。
- 设置页热键录制必须用 `HandleInput`，不能用 `OnTextKey`：mygo 会把 Alt+字母/符号当作
  “会输入文本”的按键跳过 `OnTextKey`，导致这些组合录不到。
- 窗口动作：窗口 provider 行尾有「关闭窗口」（`WM_CLOSE`，等同点关闭按钮，应用可弹
  保存确认，`Ctrl+Delete`）与「结束任务」（`Ctrl+Shift+Delete`；输入框会消费 Delete，
  必须在 `OnTextKey` 截住）。结束任务直接 `TerminateProcess` 窗口所属进程（不先关窗口，
  托盘 / 后台应用也会退出）；自身 / 核心系统进程在 `platform/endtask.go` 拒绝。
  结束的是窗口所属进程，子进程不递归。实现见 `providers/windows`、
  `platform/window_windows.go`、`app/closewindow.go`、`app/endtask.go`。
- 前缀冲突（C2）：触发串（Direct=前缀，Indirect=前缀+空格）双向互为前缀即冲突；
  默认二级前缀 `@b/@u/@t/@r`，`#` 归 picker。旧配置加载时只丢弃冲突项的 Prefix
  覆盖，设置页保存前同一套校验——`internal/logic/config/prefix.go`、
  `internal/app/providers.go`、`internal/app/settings.go`。

## AI skill 约定（面向 AI agent）

- 本仓库**不自带 skill**；公共 skill 走全局安装，不入库。
- 开发界面时以 **mygo 源码**（模块缓存）与官网文档为准；`gpui-kit` skill 只适用于
  `legacy-rust` 分支中的 Rust 参考实现，不是当前主线。
- 不要在仓库内新增 `.agents/` / `.claude/` / `skills/` 之类的目录。

## 当前遗留 / 已知问题（详见 `docs/KNOWN-ISSUES.md` 与 `docs/BACKLOG.md`）

- 默认热键 `Alt+Space` 仅对**全新配置**生效，不迁移老配置（预期行为，非 bug）。
- 面板总在**主显示器**唤起；跟随指针屏未做。
- 托盘 awake 文案在菜单**打开期间**不会变化（mygo 无弹出钩子；显隐/状态变化时会重建）。
- 键盘输入后不抑制 hover 高亮（mygo 无 `OnHover` 回调）。
- Go exe 的 VERSIONINFO 未注入（安装器 AppVersion 正常，文件属性没有版本）。
- Rust/gpui 的拖放泄漏、fontconfig 等历史问题**不适用于 Go 主线**，见 `docs/legacy/`。
