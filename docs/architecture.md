# Voke 架构（Go + mygo）

> 当前实现位于 [`poc/voke-go/`](../poc/voke-go)，产品形态是单 exe、托盘常驻的
> Windows 启动器。Rust/gpui 版代码保留在仓库根目录作参考实现，已不参与 CI 与发版；
> 其架构文档见 [`docs/legacy/rust-architecture.md`](legacy/rust-architecture.md)。

## 1. 总览

```text
main.go
  └─ app.Main()
       ├─ platform.InitLog / FixOwnCwd         日志滚动 + 纠正继承 cwd
       ├─ platform.RequestSingleInstance("voke") mygo per-user socket 锁
       ├─ mygo.App 注册生命周期回调
       └─ mygo.App.Run()
            └─ WhenReady → app.ready()
                 ├─ 配置 / 频率读取与归一
                 ├─ 前缀表、provider 注册表
                 ├─ 热键、托盘、面板窗口
                 └─ 可选启动自检（VOKE_SELFTEST=1）
```

输入链路：

```text
键盘输入
  → engine.Scan（按每个按键后的原文做前缀判定）
  → Global / PluginPicker / Plugin / SubMode
  → panelstate（应用列表）或 provider.Registry（插件结果）
  → 排序 / 分组 / 频率加成
  → mygo UI 渲染
  → Enter / 点击 → app 层 dispatcher → platform（启动、复制、切窗、唤醒…）
```

## 2. 分层

| 目录 | 职责 | 约束 |
|---|---|---|
| `internal/logic/**` | 查询引擎、配置归一、面板状态、搜索/频率、图标缓存、日志滚动、几何 | **纯 Go**，零 mygo、零 syscall；Linux 原生可测 |
| `internal/match` | 拼音键、模糊匹配（nucleo 0.3.1 评分移植） | 纯 Go，可 Linux 测 |
| `internal/providers/**` | Provider 契约 + 11 个内置插件实现 | 纯查询，无 IO / 无 mygo；副作用以 `Action` 值返回 |
| `internal/platform/**` | **唯一**封装 mygo 平台模块（Shell / Screen / Tray / Clipboard / GlobalShortcut / 单实例）与 Win32/COM 的层 | 每项标注主线程约束；Windows 专属文件配 `_stub.go` |
| `internal/ui/**` | mygo `ui` 组件、主题、面板/插件/设置视图 | 只做视图；不持有业务状态、不直接调平台模块 |
| `internal/app/**` | 进程装配、生命周期、主线程状态与意图执行 | 可用 mygo App/Window 生命周期 API；平台能力经 `platform` |
| `main.go` | 仅 `app.Main()` 薄壳 | 保持入口唯一 |

## 3. 前缀路由与冲突

- 前缀由 `config.BuildTable` 产出，**表序 = 注册顺序 = 优先级**；`engine.Scan`
  只做一次线性扫描，首个命中压栈。
- 触发是全等比较：
  - **Direct**：输入原文 == 前缀；
  - **Indirect**：输入原文 == 前缀 + 一个空格。
- 输入只在 Global 态判前缀；已进入插件/子模式后，文字只作为该层查询词。
- `#` 是「列出所有插件」入口（picker，Direct）。
- 默认二级前缀：`@b` / `@u` / `@t` / `@r`（全部 Indirect）；calculator 是 `=`（Direct）。

**冲突判定（C2）**：把每条前缀展开成触发串（Direct = 前缀本身，Indirect = 前缀 + 空格），
任意两条触发串双向互为前缀（含相等）即拒绝。原因：逐键扫描下短触发串会先命中并吞掉后续
输入，长前缀永远不可达。例如 `#` 与 `#b`（间接）冲突；`@b` 与 `@bc`（都间接）可以共存。
设置页保存前会跑同一套校验；加载旧配置时只丢弃冲突项的 `prefix` 覆盖并回落到新默认，
热键 / 主题 / 无关自定义前缀保持不动。

## 4. Provider 契约

`internal/providers/provider.go` 定义同步接口：

```go
type Provider interface {
    ID() string
    Name() string
    Aliases() []string
    Description() string
    Config() ProviderConfig
    SetConfig(ProviderConfig)
    Placeholder(subMode string) string
    RegeneratesOnQuery() bool
    Query(ctx QueryCtx) []Match
}
```

- `QueryCtx`：`Raw`（原文）、`Query`（剥前缀后的词）、`SubMode`。
- `Match`：标题 / 副标题 / 图标路径 / 分数 / `Interact`（执行、进子模式、提示）+ `Action`。
- `Action` 是纯数据（启动、复制、打开 URL、切窗口、保持唤醒、刷新等），
  真正执行在 `app` 层的 dispatcher。
- 数据型 provider（应用、窗口、终端配置、工作区）的慢查询由 `app` 层用
  singleflight / `Window.Update` 切回主线程，不阻塞输入。

内置插件：

| id | 触发 | 说明 |
|---|---|---|
| `apps` | 无前缀（全局） | 开始菜单 Applications 文件夹（含桌面程序快捷方式与 packaged 应用） |
| `windows` | 无前缀（全局） | 枚举并激活已打开窗口；行内「结束任务」直接 TerminateProcess |
| `awake` | 无前缀（全局） | 保持唤醒 / 取消，支持连屏幕 |
| `terminal` | 无前缀（全局） | Windows Terminal profile |
| `workspaces` | 无前缀（全局） | Zed / VS Code 最近工作区 |
| `calculator` | `=`（Direct） | 算术表达式 |
| `base64` | `@b `（Indirect） | 编码 / 解码 |
| `uuid` | `@u `（Indirect） | UUID v4 |
| `timestamp` | `@t `（Indirect） | 时间戳 ⇄ 可读时间 |
| `random` | `@r `（Indirect） | 随机整数 / 字符串 |
| `clipboard` | `@c `（Indirect） | 剪贴板历史：文本 / HTML / RTF / 图片；Enter 粘贴、Ctrl+Enter 纯文本、置顶、删除 |

Aliases 由 Descriptor 固定用于 picker 过滤，**不在用户配置里可编辑**。

剪贴板历史的运行时分工：
- `internal/logic/cliphist`：内存态有界存储（默认 100 条、上限 1000、置顶最多 10 条
  不占普通名额、总预算 64MiB、单图 16MiB、富文本 1MiB），SHA-256 去重、FIFO 淘汰、
  48px 缩略图；
- `internal/providers/clipboard`：纯查询与行文案 / 动作声明；
- `internal/platform/clipboard*.go`：序列号轮询、多格式读写、敏感标记与文件列表跳过、
  前台窗口捕获与 `Ctrl+V` / `Shift+Insert` 按键模拟；
- `internal/app/clipboard.go`：监控生命周期、自我写入抑制、粘贴失败回退提示。
历史**不持久化**，退出即清；`[clipboard] max_entries` 在设置页可改。

## 5. 配置

- 路径：`%APPDATA%\voke\config.toml`（与旧 Rust 版同格式，可互相读取）。
- 顶层：`hotkey` / `autostart` / `theme` / `[clipboard] max_entries`（默认 100，范围 1–1000）。
- 插件节：`[providers.<id>]`，字段 `enabled` / `is_global` / `weight` / `prefix` /
  `activation` / `count` / `hotkey`；未写字段回落 Descriptor 默认。`hotkey` 是例外：
  刻意没有默认值，未写 = 不绑定，由用户手动录制。
- 归一（`internal/logic/config`）：C1 未知 id、C2 触发串冲突、C3 前缀不含空白、
  C4 字母/数字保留键、C5 weight、C6 count、C7 activation、C8 插件热键需可解析且
  至少一个修饰键；错误落日志并降级，不静默。
- 频率表：`%APPDATA%\voke\frequency.toml`，跨版本保留；首次可从旧目录只读导入。
- 设置页修改即时落盘并热重载；前缀非法时不写盘、就地显示错误。
- 错误路径与旧实现对齐：自启注册失败则配置与界面回弹；热键注册失败撤新注旧；
  配置写失败不重载（内存态保持上一次有效值）。

## 6. 平台层（`internal/platform`）

| 能力 | 实现 |
|---|---|
| 应用枚举 / 启动 / 图标 | mygo `Shell.InstalledApps` / `LaunchApp` / `FileIcon`（`shell.go`） |
| 单实例 | mygo `App.RequestSingleInstanceLock`（per-user socket，按 app 名） |
| 全局热键 | mygo 全局快捷键模块（`hotkey.go`） |
| 托盘 | mygo `Tray`；左键双击显示面板，右键出菜单（`tray.go`） |
| 开机自启 | Win32 注册表 `HKCU\...\Run\voke`（`autostart_windows.go`） |
| 窗口枚举 / 激活 | user32 EnumWindows + SetForegroundWindow（`windows_enum_windows.go`） |
| 终端配置 / 工作区 | 读取 Windows Terminal settings.json；只读 SQLite（Zed / VS Code） |
| 剪贴板 | mygo `Clipboard` |
| 内存快照 | `K32GetProcessMemoryInfo` + `runtime.ReadMemStats`（`mem_windows.go`） |
| 日志 | `%APPDATA%\voke\voke.log`，滚动上限默认 9999 行（`logic/logcap`） |

主线程约束：所有 mygo 窗口 / 托盘 / Shell / Screen 调用都必须在主线程；
`platform` 中每个函数都有注释说明是否可后台。

## 7. 生命周期与 UI

- 启动即托盘常驻；面板窗口隐藏创建，主热键 toggle、托盘双击、二次启动唤醒。
- 插件全局快捷键：只注册“已启用 + 配置了 hotkey”的插件；停用即注销，启用后重注。
  交互对齐主流启动器的 per-plugin hotkey——隐藏时唤起并 `enterPlugin` 直达，已在
  该插件时只聚焦（保留过滤词），其它状态则切换；不做“再按一次隐藏”。录制期间主热键
  与所有插件热键统一挂起，结束后按新配置重建（`internal/app/hotkeys.go`）。
- 失焦 / Esc 隐藏面板（保留查询与插件页）；Backspace 逐级回退作用域，回到上层时输入
  恒为空（过滤词在入栈时丢弃，不跨层恢复）；Enter 执行选中项；Ctrl+Backspace 手动清空
  查询并退回 Global（面板保持可见，焦点不在输入框时也生效）。
- 重新显示时保留会话但**重跑当前查询**（对照 Rust 窗口激活观察者的 `refresh`）：
  窗口 / awake 等实时数据不冻结在隐藏前；random / uuid / timestamp 同输入跳过重跑，
  刚复制的值不会被唤回悄悄换掉。
- 启动应用 / 切窗口 / 保持唤醒等「离开面板」动作成功后：先隐藏，再复位输入与插件页
  （对照 Rust `reset_after_action`）；Copy 类动作只隐藏、保留输入与结果供再次复制。
- 设置窗独立 880×600，关闭时**程序化销毁**（`Window.Update` 里 `Destroy`），
  避免渲染资源常驻；再次打开重建。
- 面板隐藏 10 秒后把解码图标缓存裁到 64 条（设置窗打开时跳过）。
- 图标缓存两层且都有界：解码位图 256 条 LRU、PNG 字节 256 条 FIFO。
- 托盘 awake 文案在状态变化与面板显隐时重建（mygo 的 `SetMenu` 是快照，无弹出钩子）。
- `VOKE_SELFTEST=1` 启动时跑 awake / clipboard 自检。

## 8. 构建、测试与发版

```bash
cd poc/voke-go
gofmt -l . && go vet ./... && go test -race -count=1 -shuffle=on ./...
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build \
  -tags mygo_noinspector -ldflags="-s -w -H windowsgui" -o /tmp/voke.exe .
```

- CI：`.github/workflows/ci.yml`（gofmt / tidy / vet / race / Windows 交叉构建）。
- 发版：推 `vX.Y.Z` tag → `.github/workflows/release.yml` 在 windows-latest 构建
  Go exe、注入 `internal/app.buildVersion`、用 Inno Setup 7 出安装包与 portable zip，
  ubuntu job 生成 CHANGELOG 并建 GitHub Release。
- 本地安装包：`bash packaging/build-installer.sh`（Go 构建 + ISCC）。
- 版本号以 tag 为唯一事实来源；exe 的 VERSIONINFO 尚未注入（安装器 AppVersion 由
  `-dVokeVersion` 提供）。

## 9. 历史与经验

- Rust/gpui 旧架构：[`legacy/rust-architecture.md`](legacy/rust-architecture.md)。
- Go 迁移期完整记录（含 pinyin/nucleo parity、真机验证、遗留）：
  [`legacy/voke-go-poc.md`](legacy/voke-go-poc.md)。
- 当前内存测量与快照标签：[`notes/memory.md`](notes/memory.md)。
- 匹配 / 拼音一致性：[`notes/matching.md`](notes/matching.md)。
