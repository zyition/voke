# voke 待办与未来工作（BACKLOG）

> 从已清理的进度/交接文档中提炼的**尚未完成且对未来有价值**的条目。纯过程、已完成项不收录。
> 正文简体中文。

---

## 发布前人工冒烟验收

- **真机端到端冒烟清单**：热键即时生效、托盘三项（含 awake 状态条目打开菜单时即时刷新）、
  自启注销重登、别名 `_`/`<` 生效、权重组内顺序、前缀激活下拉、深色模式三档、单实例二次
  启动唤起并聚焦、任务管理器进程名显示 Voke、安装器安装/卸载。
  *为什么*：这些交互/观感类无法靠进程内自检覆盖，只能人肉过一遍。
  *来源*：交接文档「待做 / 遗留」与进度文档「收尾遗留」。

## 已知待修 / 产品决策

- **中文 query 排序手感偏弱**：`微信` 得分约 0.31，拼音 `weixin` 约 0.83（都能命中，不影响
  正确性，但中文直接输入排序靠后）。*为什么*：nucleo 子串匹配量级天生低于 fuzzy，影响中文
  用户体验。*来源*：进度文档「已知遗留问题」。`[待决策]`
- **系统应用英文名搜索键**：MSIX 显示名是中文（如「终端」）只建了拼音/首字母键，`terminal`
  搜不到「终端」。*为什么*：与上游同类工具一致、用户已接受现状；若要支持需维护英文名→显示名
  别名表。*来源*：进度文档「待确认的产品决策」。`[待决策]`
- **`Win+Shift+S` 截图时窗口偶发消失**：应用在前台按截图键有时触发失焦回调走到 `SW_HIDE`。
  *为什么*：截图覆盖层下前台/激活语义不稳定，低优先级待排查。*来源*：进度文档「已知低优先级 bug」。

## voke-go（MyGo PoC / 重写期）——已砍掉但记档的项

> 依据 [`notes/voke-go-poc.md`](notes/voke-go-poc.md) §7 重写期设计约束（原 §11，收敛时并入重编号）。
> 用户 2026-10-16 拍板口径：「**不能或成本高的就砍掉，然后记录一下，作为
> backlog，可以后面抽空实现或者等依赖版本升级看能不能解决**」。
> 本节每条都写清「现状/为什么砍 → 解除条件 → 粗粒度工作量」。

- **列表行「键盘输入后抑制 hover」层**：hover 高亮本身**已经有了**，且不是我
  们写的——`ui.List` 给每行挂 `styleFn`，命中且非选中时铺 `t.SurfaceHover`
  （mygo `ui/list.go:599-602`），驱动重绘由 `setHover` 自动做
  （`ui/input.go:154-179`，指针一动就比对 hover 链、变了就 `requestFrame`）。
  *为什么*：真正缺的是「键盘输入后抑制 hover」——生产 voke 有（`search.rs:1257`
  `.on_hover(...)`），mygo 无 `OnHover` 回调、只有 `Hovered()` 查询，要自己记
  「最后一次输入来自键盘还是指针」的状态，状态不确定性高、收益不抵成本，故砍。
  *解除条件*：① mygo 提供 `OnHover` 类回调；或 ② 先在真机量一次「轮询指针位置
  + `Invalidate()`」的实际代价（是否真的引发隐藏期额外帧）再评估。
  *来源*：PoC 文档 §7.5 E5 / T1。`[低成本，可抽空做]`
- **托盘图标左键直接唤起/收起面板**（生产 `tray.rs:343-349`：`TrayIconEvent::Click{Left,Up}` →
  ShowPanel，配合 `with_menu_on_left_click(false)` 让「左键开面板、右键出菜单」并存）：Go **做不到同款**，
  属 mygo 能力限制而非漏搬。*为什么*：mygo 的 Tray 把「菜单」和「左键单击」绑死——`SetMenu` 文档
  （modules.go:723）明说菜单「shown when the icon is clicked；nil 才让 click 走 OnClick」，且 `OnClick`
  只在**无菜单**时触发（modules.go:757）。mygo 未暴露 tray-icon 的 `menu_on_left_click(false)` 开关。
  Go 若去菜单换 OnClick→toggle，就丢了 awake 状态项 / 设置 / 退出整张菜单，得不偿失。
  *现状替代*：左键开菜单、菜单里「显示面板」已改走 `togglePanel`（见 §7 托盘条），两步可达；且主用途
  是全局热键 alt+space（Go 已注册、单键直达），托盘左键只是次要便利。*解除条件*：上游 mygo 给 Tray
  加「菜单仅右键、左键触发 OnClick」的配置项。`[被上游阻塞]` *来源*：读生产 tray.rs 逐事件核实。
- **滚动动画 / 行入场出场动画 / 颜色过渡**（E10/E11/E12）：全砍。*为什么*：
  **硬阻塞，不是取舍问题**——mygo 停帧的唯一分支是
  `case moving && rt.host.occluded(): rt.held = true`（`ui/runtime.go:585-586`），
  而 `windowHost.occluded()` 要求 surface 实现 `platform.OccludableSurface`
  （`ui/window.go:229-232`），全仓**只有** `internal/darwin/surface.go:274` 与
  `internal/web/surface.go:582` 实现 `Occluded()`，**Windows 没有** →
  Windows 上 `occluded()` 恒 false，**隐藏 ≠ 停帧**。只要有持续动画，面板隐藏后
  照样每帧跑 CPU；mygo 侧无配置项可关。*解除条件*：上游 mygo 支持 Windows
  遮挡检测，**或** vendor 改帧循环（加一个「窗口不可见时不 requestFrame」的
  总开关）——后者与本仓「拒绝 vendor/patch 上游」的立场冲突，优先等上游。
  *来源*：PoC 文档 §7.5 E10–E12 / T2。`[被上游阻塞]`
- **「在指针所在屏唤起」（跟随指针屏）**：当前 voke-go **总在主显示器**唤起，
  刻意对齐生产 voke 的行为（Rust 侧 `Bounds::centered(None, …, cx)`，
  `display_id = None` → `cx.primary_display()`，gpui-pre-0.3.8
  `src/geometry.rs:740-754`；`voke-app/src/main.rs:426`）。*为什么*：这是**与
  上一条相反的方向**——不是恢复原状，而是新增一个生产 voke 也没有的能力；
  且 PoC 曾因副屏分辨率/缩放不同出现「尺寸不对 / 定位越界」，**必须先解决那个
  bug 才谈得上做这个功能**。*解除条件*：① 先按目标屏的 `WorkArea` 与
  `ScaleFactor` 把面板尺寸与坐标夹紧（降级方案，PoC 文档 §7.6）；② 在
  §9.5 V5 清单里补「副屏唤起」的真机验收项；③ 明确这属于「有意偏离生产」，
  需用户拍板要不要这个便利。*来源*：PoC 文档 §7.6 决策④。`[中成本，需先修 bug]`
- **前缀键部分匹配兜底的排序层实现**：**决策已定（本轮只记决策，实现随前缀键
  重写一起做）**——照抄 CmdPal：全等触发（`#b` 命中才激活），但输入 `#ba` 这类
  部分匹配时**不激活**、改为在**排序层给 alias bonus**，把它托底到 Fuzzy tier、
  保证不被过滤掉（CmdPal 依据 `MainListRanker.cs:98-109`，其触发仍是
  `AliasManager.cs:48` 的全等查表）。*为什么*：这是重写期本来就要做的一块，
  不是独立工作项，故只记决策不单独排期。*实现要点*：bonus 必须在**过滤之前**
  生效，否则条目已被 fuzzy 判掉、再加分也救不回来——这正是 CmdPal 把它放在
  ranker 里的原因。*来源*：PoC 文档 §7.3.6 a。`[含在前缀键重写里]`
- **唤起重扫 + 60s 防抖**（生产 voke 的应用索引刷新策略）：**已落地**。首载仍是懒加载
  一次性闸门（`EnsureLoaded`，首个非空查询触发，view.go）；在此之上加了唤醒重扫路径：
  `showPanel` 每次唤起调 `appsmodel.MaybeRefreshOnWake(now, appsScanDebounce)`，距上次
  发起扫描 ≥ 60s 且空闲则后台再枚举一轮（对照 `main.rs:590-612` 的 APPS_SCAN_DEBOUNCE）。
  闸门拆成 `loaded`（首载一次性）+ `inFlight`（本轮在跑，由 `MarkLoaded` 在
  `loadAppsAsync` 完成时清除）+ `lastLoadAt`（防抖锚）三态；重扫结果仍经 `bindApps`
  回调异步换 `Apps`，本轮 UI 不阻塞。装/卸应用后下次唤起即可见，无需重启进程。
  单测 `TestMaybeRefreshOnWake` 锁三道人挡（首载前不强扫保懒加载 / 防抖内不重扫 /
  inFlight 不并发）。*来源*：PoC 文档 §7.7。
- **枚举失败后的重试**：首载失败后 `loaded` 仍置位，`EnsureLoaded` 不再重试（避免
  Shell 未就绪时每次按键重试的抖动风暴）。但**唤醒重扫已提供恢复路径**：失败同样走
  `defer MarkLoaded` 清 inFlight，`lastLoadAt` 记在失败那一刻，故下次唤起距 ≥60s 时
  `MaybeRefreshOnWake` 会再枚举一轮——失败不再是「必须重启进程」。尚缺的是「改查询时
  给一次立即重试机会」与指数退避，属锦上添花。*来源*：PoC 文档 §7.7。`[低-中成本，可择机]`
- **provider 端到端接线：voke-core 全部 provider 查询层已移植**：路由范式经 base64 打通，
  其余复用。**已提交**（`7222115` base64、`c9dfa67` uuid、`456d04d` timestamp、`096cc09`
  random、`74bfe21` calculator、`dc6d6b5` awake、`f270625` windows、`cf93961` terminal、
  `6c6d3df` workspaces）——apps 走 panelstate 老路径不并入 Match 模型。范式三处触点固定：
  ① `providers.go` `builtinDescriptors` 加 Descriptor + `builtinProviders()` 追加；② `panel.go`
  appState 字段与 ready()/resetSession 已在 base64 轮就位，加 provider 不动它；③ `view.go`
  的 `providerActive`/`refreshProviderResults`/`openProvider`（`providers.IntentFor`）同一套。
  前缀：picker `#`、base64 `#b`、uuid `#u`、timestamp `#t`、random `#r`、calculator `=`（Direct）；
  awake/windows/terminal/workspaces 四条 is_global 无前缀（Global 聚合链路已接
  `collectGlobalBatches→Registry.Aggregate→globalActive`，裸查询直接命中成组，`TestCollectGlobalBatches` 锁；Linux 下
  windows/terminal/workspaces 数据源返回空被跳过、awake 出方向候选，真机 V 验实际命中）；fake 未接入 id 现用「不在表里的任意串」。globalCount 现钉恰好 5
  （apps + 四条），再加全局插件须复核该口径。
  **provider 数据源接线**：terminal 已接真实源——`internal/platform/terminal*.go`（纯路径
  函数 `WTSettingsPath`/`WTExePath`/`PackageAUMID` Linux 可测 + `ReadTerminalSettings` Windows
  现读 `%LOCALAPPDATA%\Packages\{stable,preview}\LocalState\settings.json`、非 Windows 返回 nil），
  `providers.go terminalProvider()` 交已移植的 `ProfilesFromSettings` 合并去重。非 Windows 恒空 =
  接线前 NewEmpty 零回归。**windows provider 已接真实源**（本次）：`platform.EnumerateWindows`
  （`windows_enum_windows.go`，Windows-only syscall：`EnumWindows`+`syscall.NewCallback` 同步回调
  → IsWindowVisible/GetWindowThreadProcessId/GetWindowTextW/GetClassNameW/GetWindowLongPtrW(
  GWLP_EXSTYLE)/DwmGetWindowAttribute(DWMWA_CLOAKED=14)/OpenProcess(PROCESS_QUERY_LIMITED)+
  QueryFullProcessImageNameW，复用 windowfilter 谓词过滤；非 Windows stub 返回 nil）→
  `providers.go windowsProvider()` 转 windows.Entry。
- **provider 行图标已接（本次；像素待真机）**：O4 模型——Match.Icon 携「图标来源路径」（不再是
  落盘 PNG 路径）。windows toMatch 把 Icon 设为 `entry.ProcessPath`（受保护进程取不到→空→不画图标）。
  `internal/ui/provider_results.go` `ProviderResults/providerRow` 新增 `icons *map[string]*ui.Bitmap`
  参数（与 apps 共用同一 `st.icons` 缓存），`iconForPath`（从 apps `Icon()` 抽出的懒取+缓存核心：
  `platform.AppIcon`→`DecodeBitmap`，失败缓存 nil）在 `Match.Icon != ""` 时画 `Image`、取不到降级
  `Avatar`（首字母）；`Icon` 空串的纯计算 provider（base64/uuid/…）行布局与改造前逐字一致。视图
  接线 `view.go` ProviderResults 传 `&st.icons`。windows_test `TestIconSourceIsProcessPath` 断言
  Match.Icon=ProcessPath 且受保护进程空串。**provider 行图标至此与生产一致**：核实
  voke-core/terminal.rs、workspace.rs 的 match 构造**不设 icon**（只有 windows.rs 带），故
  terminal/workspaces 行本就不该有图标——给它们加反而偏离生产。图标 parity 已收口。
  **workspaces 已接真实 SQLite 源（本次；用户批准引入纯 Go 依赖）**：`internal/platform/workspaces.go`
  （无 build 标签，纯 Go 驱动 `github.com/glebarez/go-sqlite`=modernc 移植，Linux/Windows 皆可编可跑）
  只读打开编辑器历史库——Zed `%LOCALAPPDATA%\Zed\db\*\db.sqlite` 的 `workspaces LEFT JOIN
  remote_connections`（`mode=ro`→`immutable=1&mode=ro` 两级退化，WAL 库友好）、VS Code
  `%USERPROFILE%\.vscode-shared\sharedStorage\state.vscdb`（兜底 `%APPDATA%\Code\User\globalStorage`）
  的 `ItemTable→history.recentlyOpenedPathsList` 原文 + 库 mtime 排序锚；纯路径函数 `ZedDBPaths`
  (stable 在前)/`VsCodeDBPaths` 与读取均可 Linux 原生测（`workspaces_test.go` 造真 SQLite fixture 验证
  LEFT JOIN / NULL remote / stable-first / 无 env→nil）。`providers.go workspacesProvider()` 交已测的
  纯逻辑 `workspaces.Workspaces` 解析/排序/去重，zed/code EditorCommand 暂传 nil（open 退化为直接开
  路径，见下）。这**取代**了此前「决策点：暂缓」（Go 无标准库 SQLite；用户已授权为此一处破零依赖例，
  取纯 Go modernc 而非 cgo——交叉编译无痛且 Linux 可测）。**awake** 已接 `platform.AwakeState()`。
  *编辑器 CLI 发现已补*：`platform.ZedCommand`/`VSCodeCommand`/`firstExisting`（候选表照 workspace.rs，
  VS Code 包 `cmd.exe /c code.cmd`）→ `workspacesProvider` 传入，open 用真编辑器；未装 / 非 Windows
  返回 nil → 退化直接开路径。`workspaces_test.go` 造临时 exe/cmd 验三态。生产 workspaces 行亦无 icon。
  **windows 过滤层已移植（本次）**：`internal/platform/windowfilter.go`（无 build 标签、Linux 原生
  可测）1:1 对照 voke-sys/windows.rs 的**纯**半边——`ExcludedClasses` / `IsExcludedClass`（两边归一
  + `.` 后缀前缀匹配）/ `ShouldIncludeWindow`（六条过滤链）/ `HasExtStyle` / `ProcessNameFromPath`；
  `windowfilter_test.go` 复刻生产测试矩阵（黑名单、大小写、后缀、全规则、TOOLWINDOW vs APPWINDOW、
  ext-style 宽度无关、进程名取段）。消费者 `platform.EnumerateWindows`（见上条）已接、复用这些谓词。
  **执行层（`view.go` `execPlatformAction`）已全部落地**：awake 的 SetAwake/CancelAwake、windows 的
  ActivateWindow、workspaces/apps 的 Launch、terminal 的 LaunchTerminalProfile 均接
  `internal/platform` 真实实现（Linux 侧 stub 可测、Windows 真实现）。terminal 走 COM
  App Activation（`appactivate_windows.go`：`CoCreateInstance(CLSID_ApplicationActivationManager)`
  → `ActivateApplication(aumid, "--profile \"<guid>\"", AO_NONE, &pid)`，槽 3、INPROC_SERVER，
  GUID 逐字取自 appactivate.rs），激活失败回退 `wt.exe -p <profile>` 直启；参数串
  `terminal.ActivationArguments`（对照 `profile_activation_arguments`，`activation_test.go` 覆盖）。
  windows `go vet` 已过（COM vtable 经类型化指针读取、无 uintptr→Pointer）。真机效果待 V。
  **托盘 awake 状态条目已落**（本次）：`awake.MenuLabel`（1:1 移植 voke-sys `awake.rs` 的
  `menu_label`，含 `TestMenuLabel` 全分支覆盖）→ `panel.go` `setupTray/trayMenuItems/refreshTrayMenu`
  按当下 `platform.AwakeState()` 现建首项（未激活 / 「还剩 … · 屏幕打开|关闭」，任何状态可点→
  `showPanel`），`execPlatformAction` 的 SetAwake/CancelAwake 后调 `refreshTrayMenu` 即时刷新。
  *核实更正*：mygo `Tray.SetMenu` 是**调用时取快照**下发 native（modules.go:725-729），不挂
  `WM_INITMENUPOPUP`、弹出不现读——旧「每次弹出自动刷新」的判断不成立，故须显式重建；唯
  `PopUpMenu()` 每次重取快照但用户右键弹不走它。定时器自然到期在后台 goroutine、不刷新，
  托盘文案滞到下次状态变化——生产靠弹窗即时刷新消掉，mygo 无钩子，留真机 V7 观察。直达 awake 插件（生产点该项即进插件）**已落地**：step8b 已并入，托盘 awake 项 Click 改走 `openAwake = showPanel + enterPlugin(awake.ID)`（`enterPlugin` 用 `engine.SelectPlugin` 原子进第一级，对照生产 `activate_plugin`；`awake` 一级空查询出两条亮屏方向候选，`TestEnterPluginRoutesToAwakeFirstLevel` headless 守住）。（headless
  里 `st.tray==nil`，`refreshTrayMenu` 幂等早退，不触碰 mygo。）**engine.rs 纯逻辑层也已补齐**：`Registry.Aggregate`
  （跨 provider 分组聚合，`7df4cf3`）与 Provider 契约的 `RegeneratesOnQuery`（`3e1fe78`，
  uuid/random/timestamp=true + view.go 同输入跳过护栏）均已移植并被接线测试覆盖。
  **ActionRefresh 执行语义补齐（本次）**：random 二级首条「重新生成」行（`Action{Kind:ActionRefresh}`）此前
  被 `IntentFor` 落到 default→`IntentPlatformAction`→`execPlatformAction` 的 `default: 未接入` 静默 no-op——点它
  什么都不发生，与生产 `random.rs:18-19`「让 app 用同一输入再掷一批」不符。新增 `IntentRefresh`：`IntentFor`
  显式分流 `ActionRefresh→IntentRefresh`，`runProviderIntent` 收到它清 `provLastKey` 绕过 RegeneratesOnQuery 的
  「同输入跳过」护栏后重跑 `refreshProviderResults`（面板保留、模式/输入不变，对齐 `should_reset_input=false`）。
  `TestRefreshRowRerollsSameInput` 用十六进制二级（32 位 hex，值域 16^32）断言点「重新生成」后值行确变、模式不改、
  providerActive 仍真——旧 no-op 会在 `before==after` 上红。`Reveal`/`OpenUrl` 无 provider 产出（workspaces 落资源管理器
  属远期想法），enum 保留仅为与 Rust 动作词汇对齐，非缺口。
  **config 读盘也已落地（JSON、零新依赖）**：`internal/logic/appcfg`（`21bc15c`）做
  decode/encode/merge，`f7726cc` 经 env `VOKE_GO_CONFIG` 把读盘接进 `ready()`（未设=纯默认、
  坏配置回落不静默、Normalize 真错退内置默认）；`19b83ee` 移植 HotkeySpec；`fa56168`+`9f1043d`
  频率表 JSON 编解码 + env `VOKE_GO_FREQ` 启动读回 / record 落盘。**apps 排序吃频率已补齐（本次）**：
  此前 apps-only 全局查询走 `search.FilterApps` 只按原始分、启动也不 record 频率（record 只在
  `runProviderIntent` 的 provider 路径），与生产「一切结果都经 `engine.rank` 的 `frequency_boost`」
  不符（apps.rs:22 明说频率在 rank 而非 provider，engine.rs:504/525 aggregate 对含 apps 的每批调 rank，
  search.rs:892 启动即 `record_use("apps:<name>")`）。补法贴合生产「provider 出分 + rank 加成」两步：
  `FilterApps` 增 `freq` 参——空查询全量后按 boost 降序稳定排、非空查询先按原始分截断 50（截断在 boost 前，
  被截掉的高频项不复活）再按 `score×boost` 稳定排（apps 内 weight 恒定约掉）；`panelstate.Model` 加 `Freq`
  字段、`ready` 里 `st.model.Freq = st.freq`；`execLaunch` 启动成功后 `freq.Record(Key(ProviderID,name))+save`。
  键前缀统一到 `appsmodel.ProviderID`（`TestAppsFreqKeyPrefixConsistent` 钉住与 app 层两个 apps id 一致，
  否则「记了加不上」）。nil 频率表退化成只按原始分（零回归，旧 `FilterApps(q,in,nil)` 测试全绿）；
  `TestFilterAppsEmptyOrdersByFrequency`/`TestFilterAppsBoostOverridesScoreGap`/`TestFilterUsesModelFreq` 覆盖。
  格式用 JSON 而非 TOML 是
  权衡（避第三方依赖；代价：无注释、文件改 `config.json`，日后要 comment-UX 只换 appcfg
  一处、Normalize 下游不动）。**路径已落**：`platform.DataDir()` 读 `%APPDATA%\voke-go\`
  （`appdirs_test.go` 双分支覆盖，PoC 隔离于生产 `voke\`），`configFilePath`/`freqFilePath`
  env 覆盖优先、否则落该目录（`9f1043d`）。**执行层已落**：SetAwake/CancelAwake/ActivateWindow/
  Launch 经 `execPlatformAction`→`internal/platform` 真实实现（Windows 真、Linux stub 可测）。
  **剩余面（全卡 Windows 真机）**：设置窗 / 托盘 / awake 换肤 / 改键 / terminal App Activation
  的**真机效果**均需 V 跑确认（mygo 主线程 + COM，headless 到不了；接线与 windows build/vet 已过）；
  step8b 生命周期装配（明确 V5/V6/V7 门槛后再做）。headless 无新依赖的纯逻辑 + 接线 +
  平台执行至此穷尽。`[纯逻辑/接线/路径/执行全收尾；余真机验证 + step8b]`
- **全局聚合·数据层已落（本次）**：`global.go` 补上生产有、PoC 缺的全局搜索跨 provider 聚合
  数据侧（`renderResults` 的 Global 分支此前只出 apps）。`foldAppMatch`（apps→Match 展示折入，
  不改 apps 自身链路）、`collectGlobalBatches`（apps 批在最前 + 按注册顺序跑 pluginReg 里
  enabled&&is_global 的插件；base64 等带前缀非全局不进、apps/picker 不在 reg 自然跳）、
  `globalAggregate`（→ 已移植的 `Registry.Aggregate`：组内 weight×score×freq 排序、组间保序、
  hints 单列）。`global_test.go` headless 验折叠字段 / 批次选取 / 分组顺序。**未动渲染路径**
  （Global 仍 apps-only，既有 `TestGlobalPathUnchangedWithoutPrefix` 等全绿 = 零回归）。
  **渲染就绪行模型已加**：`buildGlobalRows` 把 apps（带 AppEntry 下标，走既有执行器）与非 apps 组
  （带 provRows 下标 + 组首标记）摊平成单条可导航序列。关键约束：apps 启动是 `mygo.Shell.LaunchApp`
  （AUMID/`shell:AppsFolder` 语义），**不能**折成通用 `platform.Launch`（会断 UWP 启动，即文档
  「apps→Match 留迁移后定」之由），故行模型保 apps 走自身执行器。`TestBuildGlobalRows` 覆盖。
  **全局合并渲染已接（本次；交互待真机）**：`internal/ui/global_results.go` `GlobalResults`
  （apps+provider 摊平成单条可导航列表、组首渲组名、图标懒取/首字母降级、三态同 apps）；`appState`
  加 globalRows/globalProvRows/globalChosen/globalList；`refreshGlobalAgg` 在查询变化+apps 加载完成
  重算（不进渲染帧，避免每帧 windows 枚举阻塞主线程）；`renderResults`/`open`/`moveSelection` 在
  `globalActive()` 时走合并列表，`openGlobal` apps 行回 model 执行器（保 LaunchApp/频率）、provider
  行走 `runProviderIntent`。**零回归保证**：`globalActive` 仅命中非 apps 全局插件时为真，否则（Linux/
  测试/纯应用查询）落回原 `AppsResults` 逐字不变（`TestGlobalPathUnchangedWithoutPrefix` 等全绿）；
  `pluginReg`/`providers` 未就绪时 `refreshGlobalAgg` 早退。`TestGlobalView` 覆盖渲染输入映射。
  合并列表实际交互（↑↓/Enter/滚动/组视觉）无法 headless（mygo 主线程 + 数据插件 Linux 恒空）→ **真机 V**。
- **设置窗 UI 已落（本次；视觉待真机）**：`settings.go` 建独立 `mygo.NewWindow`（880×600
  普通窗，OnClose 拦截为隐藏复用），托盘加「设置…」项经 `openSettings` 唤出。四项绑定
  appcfg 落盘 + platform 副作用：**开机自启**（当场 `platform.SetAutostart`）/ **界面主题** /
  **唤隐热键**（写 config.json，下次启动生效）/ **插件配置**（对照设计文档 §385 的每插件 Group：
  启用 / 参与全局搜索 / 权重 0–3 / 别名 prefix + §33 的 Direct/Indirect 下拉，全写 override +
  `reloadConfigs` 近即时下发；值从归一态 seed 故不改即幂等写回，改坏交给 Normalize C2/C3/C5/C6/C7
  挡下兜底。`NumberInput/Switch/Select/Checkbox` 均经 windows build 核实）。**主题与热键的实时应用已接**：`platform.SetThemeSource`
  （转 `mygo.Theme.SetSource`，appcfg 码值=ThemeSource 透传）当场换肤；改键走
  `platform.UnregisterHotkey(旧)`→`RegisterHotkey(新)`、失败回滚旧串（不留空窗），`appState.showHotkey`
  记当前实绑串供比对。换肤/改键的**真机效果**仍待 V 跑（mygo 主线程 API、headless 到不了），接线已落。
  widget 全用已核实的 mygo v0.5.5 `ui` 面（Switch/Select/
  Checkbox/Button/PrimaryButton/Field/Scroll，Element.Clicked/Changed）——windows build 过即
  证明无编造 API。headless 覆盖 `themeLabel/themeCode` 往返 + `seedSettings`（`settings_test.go`），
  渲染/保存只 compile 验证、视觉需真机 V。
  **设置窗「关于」区补齐（本次）**：此前只落了 生产 general_page 的前三 group（启动 / 热键 / 插件），
  漏了第四个「关于」（只读：版本 + 配置文件位置）。补 `aboutVersion`（`debug.ReadBuildInfo` 取主模块版本，
  等价生产 `CARGO_PKG_VERSION`；本地构建如实显示 `(devel)`，不编造版本号）+ `aboutConfigPath`
  （`configFilePath`，非绝对路径回落「未落盘…」文案，对齐生产 `config_path_label`），渲染成两行只读
  `ui.Field`。windows build 过即证 `ui.Field/Text/Grow` 非编造 API；`TestAboutHelpers` 覆盖版本非空 +
  配置文件绝对/相对两分支（env 设路径决定，Linux 原生可测）。视觉仍需真机 V。
- **panel 键位对齐生产（本次）**：`internal/ui/view.go` `View` 快捷键路由补上 `Ctrl+N`/`Ctrl+P`
  （=↓/↑）、`Tab`/`Shift+Tab`（=↓/↑）、`Ctrl+,`（开设置窗，经新 `ViewInput.OnOpenSettings`→
  `st.openSettings`）。常量 `goui.Ctrl/Shift/KeyN/KeyP/KeyTab/KeyComma` 均已核实存在（mygo v0.5.5
  ui/keys.go），复用现有 `OnMove`，不改既有 ↑↓/Enter/Esc 路径。⚠️ Tab 在 mygo 默认参与焦点遍历，
  能否抵达视图 shortcut 待真机确认；抵达不了时 Ctrl+N/P 与 ↑↓ 仍覆盖同一导航（无主流程损失）。
- **模块级覆盖审计（本次；把每个 Rust 源文件映射到 Go 归属）**：逐一核对 voke-core/voke-sys/voke-app
  的全部 .rs 是否都有 Go 对应。结论——绝大多数 1:1（action→provider/dispatch、appactivate→appactivate_*、
  apps→appsmodel+apps、autostart/awake/hotkey/logcap/matcher(nucleo*)/paths→appdirs/provider/registry、
  search_key→search_key+pinyin、shell→shell、single_instance→singleinstance、terminal/timestamp/uuid/
  windows/workspace/base64/calculator/random→各 provider、theme/settings/tray/main→各 app 文件）。
  **`theme.rs` 的注册表轮询线程 Go 有意不搬（非缺口）**：生产起 `spawn_theme_watcher` 低频轮询
  `AppsUseLightTheme`，因 gpui 观测不到系统主题变更；而 **mygo 原生处理**——Windows 后端在
  `WM_SETTINGCHANGE` 上调 `appHandler.ThemeChanged()`→`contentThemeChanged()` 通知所有窗口重绘
  （mygo internal/windows/app.go:351-360 / content.go:302），`ThemeSystem`("system") 又是其默认源。
  故 Go `platform.SetThemeSource("system")` 天然跟随 OS 明暗切换，无需自建轮询线程——用更优机制
  达同一效果。*核实依据*：读 mygo 源码确认，非假设。**6 个无同名文件的模块，逐个定性**：① `clock.rs`→Go 用各 provider 注入的 `now func() int64`（等价 Clock trait，折叠）；
  ② `entropy.rs`→Go 直接用 stdlib `crypto/rand`（生产为「零依赖」自造熵源，Go 无此约束，折叠更优）；
  ③ `close_guard.rs`→生产为 gpui「程序化销毁 + 拖放泄漏重入」的一次性闸门，mygo 设置窗用「OnClose→隐藏复用」
  不同策略、无该问题，**N/A 非缺口**；④ `env.rs`（.lnk 直启 exe 时 `%VAR%` 展开）→Go apps 走 ShellExecute on
  shell:AppsFolder 项（由 shell 解析、不经手工 exe+args 快路径），当前 provider 不产生需手工展开的 `%VAR%`，
  **暂不触发**（若日后加「解析 .lnk 直启目标」再补）；⑤ `badge.rs`（分组结果行的 per-provider 哈希色+Lucide
  图标角标）→纯视觉、Go 全局列表已用**组名文字**标识分组，且 mygo 无 Lucide 图标面，颜色角标属可选视觉打磨、
  **非功能缺口**（记此备查，需要时补色块）；⑥ **`cwd.rs`→真实缺口，本次已移植**（见下）。
- **启动工作目录纠正 `platform.FixOwnCwd`（本次；对照 voke-sys/src/cwd.rs）**：开机自启经 `HKCU\…\Run`
  不指定工作目录，进程继承到系统目录（实测 `C:\Windows\System32`），任何继承 cwd 的子进程（os/exec 直启的
  编辑器 / wt.exe 兜底）会拿 System32 当起始目录。生产启动早期（log init 后、相对路径解析前）纠正之，
  只动「系统目录」与「读取失败」两类、尊重用户显式 cwd。Go 此前完全没做——已补 `internal/platform/cwd.go`
  （纯函数 `isSystemDir`/`shouldFixOwnCwd` Linux 表驱动可测 + `FixOwnCwd` 用 os.Getwd/os.Chdir，目标优先级
  %USERPROFILE%→exe 目录，绝不变 panic），在 `app.Main` 的 `InitLog` 之后调用。`TestIsSystemDir`/
  `TestShouldFixOwnCwd`/`TestPickCwdTargetPrefersUserprofile` 覆盖。**elevated 启动**生产自身也未实现
  （dispatcher.rs:474「尚未实现，降级普通启动」），故 Go 不做 elevation 与生产一致、非缺口。
- **§7.2 剩余分层单元：step8b（生命周期装配 → `internal/app`）已落地**：把 package main 的全部装配
  （`main`/`ready`/`setupTray`/`showPanel`/`view`/`settings`/`providers`/`global`/意图执行器 + 其
  `init()` 与包级常量）整体迁入 `internal/app`（`package main`→`package app`、`func main`→`func Main`），
  根目录只留薄壳 `main.go`（`package main; func main(){app.Main()}`，`-H windowsgui` 需一个 main 入口）。
  **这是行为保持的机械搬迁**：7 个测试文件随包迁入 `internal/app`，同包引用不变、全量测试对**搬迁后**
  代码重跑通过；`GOOS=windows` vet + 交叉 build 全绿。§7.2.2 提案的 `app/{app,dispatch,state}.go` 三文件
  命名按 §7.1.1「文件组织不要求等价」未强拆（现按 panel/view/settings/global/providers/apps/main 分），
  「main 降为装配薄壳、app 拥状态与回调」这一实质已达成。**仍待真机**：mygo 生命周期运行期（`App.Run`
  /WhenReady/OnSecondInstance/OnWillQuit/托盘/热键）本就无 headless 覆盖，搬迁不改其代码，但「跑起来一切
  如常」只能由 V5/V6/V7 确认——我**不声称**运行期已验，只保证编译 + 单测 + 交叉构建通过。
- **§7.2.2 目录树完成度审计（本次）**：逐格核对提案树与实际文件——**唯一结构性缺席**的是
  `internal/app/{app,dispatch,state}.go`（即 step8b，刻意 V-gate 未做）。其余全部到位，部分
  按功能而非提案文件名落位（§7.1.1 明确「文件组织不要求等价、鼓励替换」，非缺陷）：platform
  的 `win32.go` 拆成 window/awake/autostart/appactivate/launch 各 `_windows/_stub` 对；`power.go`
  的保持唤醒用 `awake*.go` 以 SetThreadExecutionState 实现（O1 想用 `Power.KeepAwake` 简化属
  「可优化点」非功能缺口，现有实现已过 AwakeState 测试，换机制需真机 30 分钟不睡人肉观察，
  盲改不划算）；ui 的 `row.go/keys.go` 折进 `view.go/apps.go`；`icons.go` 在 `internal/ui/icons.go`。
  **功能面**：除 workspaces 数据源（SQLite 决策点）、Global 聚合 + step8b（V-gate）、终端/工作区
  图标（生产本就无、非缺口）外，全功能迁移的 headless 可证部分已穷尽——apps/各 value provider/
  windows/terminal/awake/frequency/config/settings/托盘/热键/单实例/自启/日志/COM 激活/窗口枚举
  /panel 键位/图标 parity 均已实现、接线、六项门禁绿。

## 内存优化（探索完成，尚未实施）

> 详见 [`notes/memory-footprint.md`](notes/memory-footprint.md)。稳态 ~108MB 基本是 gpui 图形栈
> 地板，以下为可选优化项。

- **隐藏即缩窗到 1×1**：面板/设置窗隐藏时把窗口缩到 1×1，回收每窗 GPU 纹理，把「用过之后」
  的稳态钉回 ~108MB。*为什么*：无需 patch 依赖、性价比最高。*来源*：内存优化方案 杠杆 1。`[建议先做]`
- **关 a11y（accesskit / UIA）**：调用已有的 `Application::new_inaccessible` 关掉无障碍。
  *为什么*：启动器场景无障碍收益低，可能省 <2MB。*来源*：杠杆 5。
- **隐藏时 `EmptyWorkingSet`**：只降「私有工作集」读数、私有提交不变，属仪表盘级 trick，
  须诚实告知用户。*为什么*：可选的数字优化。*来源*：杠杆 7。`[需用户拍板]`
- **patch gpui 三常量（MSAA / swapchain 缓冲 / 图集）或恢复设置窗销毁**：可见期再省 ~18~28MB，
  但需 vendor / `[patch.crates-io]` 或接受「每次开设置重渲染」。*为什么*：收益需用户接受上游改动。
  *来源*：杠杆 2/3/4a。`[需用户同意]`

## 性能 / 电源

- **Headless 方案（gpui VSync 线程空转）——已挂起**：gpui 的 `VSyncProvider` 线程 60–120Hz
  永久空转（`DwmFlush` 阻塞等 vsync），切 `WindowingRequest::Headless` 可连 D3D11 设备 /
  DirectWrite / VSync 线程一起释放；但 Windows 上 headless 是单向门、切换要求零窗口（要销毁
  面板窗，触发拖放泄漏 ~50MB/轮），净账可能为负。*为什么*：与「拒绝 vendor/patch 上游、
  拖放泄漏只藏不毁」的立场冲突，暂不实施；其余稳态空转线程（主题轮询 / awake 等待 /
  搜索键记忆化）均已落地，现只剩这一条。*来源*：完整评估见
  [`notes/headless-evaluation.md`](notes/headless-evaluation.md)，重评条件见该文档 §1。`[已挂起]`

## 功能想法 / 远期

- **托盘图标状态变色**（awake 激活时变色）。*为什么*：视觉反馈。*来源*：设置页设计文档已知取舍。
- **容器 / 其它位置工作区打开**：当前 Zed CLI 无「打开已记录容器路径」入口，落「退回资源管理器」。
  *为什么*：完善 workspaces 覆盖。*来源*：进度文档 workspaces 实现事实。
- **排除易混淆字符的随机串**：random 暂未做「排除 `0/O`、`1/l/I`」选项。*为什么*：生成的可读性。
  *来源*：进度文档决策 5。
- **awake「机器 30 分钟不睡」人肉观察**：**进程内链路已由启动自检自动化**（见下条 VOKe_GO_SELFTEST），
  真睡仍需实地等 30 分钟确认 OS 层 SetThreadExecutionState 的实际效果。*为什么*：状态机+线程+取消
  链路已可自证，唯独「OS 真的不睡」无法在进程内断言。*来源*：进度文档「仍未验证」。
- **启动自检 `VOKe_GO_SELFTEST=1` 已移植（本次；对照生产 main.rs selftest 分支）**：`internal/app/selftest.go`
  在 `ready()` 末尾（provider/热键/托盘/apps 就位后）跑 `awakeSelftest`——复刻生产 awake_selftest 的
  状态链：CancelAwake 清场→未激活；SetAwake(30,不连屏)→回读 expiresAt≈now+1800s（偏差≤5s）+keepScreen
  正确；awake provider 查询在激活态给出 `ActionCancelAwake` 可执行项（判据取 Action 非文案，同生产）；
  再 CancelAwake→未激活。失败项计数落日志。Go 侧激活态独立于 OS 跟踪，故 **Linux 也自证**
  （`TestAwakeSelftestPasses` 断言 0 失败）；Windows 上额外收益是重申 goroutine 真调 assertExecutionState。
  **clipboard_selftest 也已移植（本次）**：更正——先前误记「mygo 未暴露剪贴板读原语」，实则有
  `Clipboard.ReadText`（modules.go:145）。补 `platform.ReadClipboardText` + `clipboardSelftest`
  （写哨兵 `voke-go-selftest-42`→读回，至多 10 次每次 150ms 重试，照生产 main.rs:787 clipboard_selftest），
  是 Copy 类 provider 在设备上「点了真能复制」的进程内唯一证据；主线程内联执行（`onMain` 见 loop.go:95
  在主线程直接跑、不自锁）。**routing_selftest**
  未另写——引擎路由已有完整单测覆盖，生产那段是为「合成输入进不了框」补的，Go 侧路由本就可 headless 测。
