// panel.go：面板窗口的创建、显隐、定位、失焦隐藏、Esc 两级语义与托盘。
package app

import (
	"log"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/zyition/voke/poc/voke-go/internal/logic/appcfg"
	"github.com/zyition/voke/poc/voke-go/internal/logic/appsmodel"
	"github.com/zyition/voke/poc/voke-go/internal/logic/cliphist"
	"github.com/zyition/voke/poc/voke-go/internal/logic/config"
	"github.com/zyition/voke/poc/voke-go/internal/logic/engine"
	"github.com/zyition/voke/poc/voke-go/internal/logic/frequency"
	"github.com/zyition/voke/poc/voke-go/internal/logic/geometry"
	"github.com/zyition/voke/poc/voke-go/internal/logic/panelstate"
	"github.com/zyition/voke/poc/voke-go/internal/platform"
	"github.com/zyition/voke/poc/voke-go/internal/providers"
	"github.com/zyition/voke/poc/voke-go/internal/providers/awake"
	appui "github.com/zyition/voke/poc/voke-go/internal/ui"
)

// 面板尺寸（680×494 DIP）与垂直锚点系数（面板中心 48%）已随 §7.2 步骤4
// 下沉到 internal/logic/geometry（PanelWidth / PanelHeight / CenterPercent），
// 定案依据与 chrome 实测表在那边的注释里（PoC 文档 §7.6.2 / §7.6.3）。
// 本文件只留平台/窗口侧的事：取主显示器 WorkArea、建窗、SetPosition、显隐。

// appState 是 PoC 的**平台/UI 句柄集合**。所有字段只在主线程读写：mygo 的面板/
// 托盘/热键/单实例回调都在主线程；后台数据（应用枚举）经 apps.go 的 bindApps
// 接缝 + Window.Update 回到主线程后再改。
//
// §7.2 步骤5：面板的**纯决策状态**（查询 / 全量应用 / 过滤结果 / 选中行 / 懒加载
// 标志）连同过滤、选中、启动、空态文案等判断逻辑，已下沉到 internal/logic/panelstate
// 的 Model。appState 只保留 mygo 句柄，并把 Model 返回的「意图」翻译成本地的
// UI/平台副作用（见 view.go 的 execute/apply 与 execLaunch / runProviderIntent
// 的动作收尾）。
//
// §7.3.3 接线（本轮）：appState 另持三份**模式路由**状态——engine.Table（前缀表，
// 只由 config.BuildTable 产出）、engine.State（显式模式状态，零值即 Global）、
// config.Configs（归一后的注册表，picker 列表的数据源）。**分工**：
//   - engine 只管「这一帧输入落在哪个模式 / 该把查询词交给谁」；
//   - panelstate.Model 只管「apps 列表的选中 / 启动时序 / 懒加载」，它不知道有模式；
//   - picker 列表自成一套（pickerRows / pickerChosen / pickerList），刻意不复用
//     model.Results：那样会让「不打前缀」那条链路（=今天全部真实使用路径）
//     多踩一个被改写过的字段。命中前缀之前，本结构里新增的字段一个都不动。
type appState struct {
	panel *mygo.Window
	tray  *mygo.Tray
	// quitting = 正在退出流程。mygo 的 App.Quit→prepareQuit 会逐个 w.close()，
	// 而面板/设置窗的 OnClose 默认 PreventDefault（关=隐藏）。若不区分，退出时那
	// 一 veto 会让 w.close() 返回 false → prepareQuit 提前 return false → 永不
	// os.Exit，进程杀不掉（真机「托盘退出没反应」的根因）。OnBeforeQuit 在关窗
	// 循环**之前**触发，据此置真；两个 OnClose 见到它便放行真关闭。只主线程读写。
	quitting bool
	// model 是面板的纯决策状态机（零 mygo）。它的字段（Query/Results/Chosen/
	// Apps/Loading/Loaded/LoadAt）被视图直接读写/绑定（如 TextInputBase 绑
	// &st.model.Query、ListState.Selected 指向 &st.model.Chosen），判断逻辑一律
	// 走 model 的方法并回收意图，见 view.go。
	model panelstate.Model
	// engineState 是 §7.3.3 的显式模式状态（Mode / PluginID / SubMode / Query）。
	// 零值 = Global，故不需要构造初始化；main 侧只经 engine.Scan /
	// BackspaceBack / SelectPlugin 改写它，不手拼模式（Esc 不再改写 engineState，直接隐藏）。
	engineState engine.State
	// table 是前缀表（engine.Scan 的输入），装配于 ready()。
	table engine.Table
	// providers 是归一后的插件配置（picker 列表的数据源）。
	providers config.Configs
	// cfg 是最近一次装配读到的顶层设置（hotkey / autostart / theme），供设置窗 seed
	// 当前值。ready() 与 st.reloadConfigs() 各自回存；无配置文件时是零值 Loaded。
	cfg appcfg.Loaded
	// showHotkey 是**当前实际注册**的唤隐热键串（ready() 定、设置窗改键更新）。改键要
	// 「先撤这个旧串、再注新串」，故必须记住注册时用的确切串（而非 st.cfg 里存的那份）。
	showHotkey string
	// mainHotkeyOn 表示 showHotkey 当前是否真的注册着：启动失败 / 录制临时挂起时
	// 为 false，resumeHotkeys 据此决定要不要补注册。
	mainHotkeyOn bool
	// pluginHotkeys 是**当前实际注册**的插件热键：provider id → 注册时用的串；
	// pluginHotkeyErrs 是最近一次同步中失败 / 冲突的中文原因（设置页展示）。
	// hotkeysSuspended 表示设置页正在录制，所有全局热键已临时注销（见 hotkeys.go）。
	pluginHotkeys    map[string]string
	pluginHotkeyErrs map[string]string
	hotkeysSuspended bool
	// settings 是独立设置窗（§7.2 / 设计文档「设置页」880×600）：nil = 未开过；
	// 打开时懒建、绑定态存其中（见 settings.go）。只在主线程读写（托盘 Click）。
	settings *settingsWindow
	// pickerRows / pickerChosen / pickerList **只在 PluginPicker 模式**读写。
	pickerRows   []pluginRow
	pickerChosen int
	pickerList   ui.ListState
	// pluginReg 是**已落地实现**的 provider 注册表（apps+picker 不在此，走各自老路径）。
	// engineState 落在 Plugin/SubMode 且 providerID 命中本表时，结果区走 providers.RunOne
	// + appui.ProviderResults，而非 apps、也非「未接入」占位。装配于 ready()。
	pluginReg *providers.Registry
	// freq 是使用频率表（越用越准的排序加成来源）：启动经 loadFrequency 从 frequency.json
	// 读回、每次 record（启动应用 / provider 执行）后 saveFrequency 落盘（env VOKE_FREQ
	// 覆盖路径，未设则 no-op）。nil 安全：RunOne / search.FilterApps 都退回 boost=1。
	// 装配时注入给 st.model.Freq，让 apps-only 全局查询也吃到 rank 的频率加成。
	freq frequency.Store
	// provRows / provChosen / provList / provHint **只在「已注册 provider 的插件态」**读写。
	provRows   []providers.RankedMatch
	provChosen int
	provList   ui.ListState
	provHint   string
	// provLastKey 是上一次真正跑过 provider 的 (pluginID, subMode, query) 指纹，供
	// refreshProviderResults 对「会重掷」的 provider 做同输入跳过（对照 Rust
	// regenerates_on_query 的调用侧护栏）。
	provLastKey string
	// 全局合并搜索态（仅 Global && globalActive 时读写）：合并行（apps+provider 组摊平）、
	// provider 行扁平（apps 行直接现取 st.model.Results）、选中与列表状态。见 global.go。
	globalRows     []globalRow
	globalProvRows []providers.RankedMatch
	globalChosen   int
	globalList     ui.ListState
	list           ui.ListState     // 列表状态（选中/Key/滚动）
	input          ui.Handle        // 搜索框句柄，唤出时抢焦点
	icons          *appui.IconCache // 解码图标位图缓存（256 条 LRU，按 app.Path；主线程用）
	// iconTrimTimer / iconTrimGen 是「隐藏 10s 后裁剪图标缓存」的一次性定时器与
	// 世代号：showPanel 或新的 hidePanel 都会让旧定时器失效（Wox 同款延迟裁剪）。
	iconTrimTimer *time.Timer
	iconTrimGen   int
	// trayLastClick 是托盘左键上一次点击的时刻：mygo 原生只上报单击（wmLButtonUp），
	// 双击表现为两次 Clicked；据此在系统双击时间窗内识别双击并唤出面板。只主线程读写。
	trayLastClick time.Time
	// shownOnce 保证「面板首次唤起」只打一次（诊断脚手架，2026-10-16
	// 内存增长定因加）：首帧会真正构建 view 树、解码可见行图标，是内存
	// 从地板往上跳的第一个可归因点，之后的唤起不再重复打点。
	shownOnce bool
	// gpuDiagOnce 保证「设备类型诊断」只打一次（诊断脚手架，2026-10-16
	// GPU 设备类型归因加）：由 view.go 根元素的 Draw 回调在首帧绘制时
	// 落位，见该处注释为何不能复用 shownOnce。
	gpuDiagOnce bool
	// hist 是剪贴板内存历史（主线程专用）。ready() 建，退出即清，不持久化。
	hist *cliphist.Store
	// clipSeq 是最近一次已处理的剪贴板序列号；程序化写入后同步置位，用于抑制
	// 自己写剪贴板又被监控采集（例如「纯文本粘贴」降级出的新条目）。
	clipSeq uint32
	// clipTarget 是唤起面板前的前台窗口快照，粘贴时恢复它并发送按键。
	clipTarget platform.ClipTarget
	// notice 是一次性操作提示（粘贴失败 / 置顶已满 / 结束任务失败），显示在底部
	// 提示行；noticeAt 用于下次唤起时判断是否过期。
	notice   string
	noticeAt time.Time
	// clipSuppress / clipSuppressUntil 是程序化写入的内容指纹与有效期。仅靠序列号
	// 不够：Windows 的延迟渲染会让序列号在 OleSetClipboard 之后再次递增，若只对齐
	// 一次，粘贴「纯文本」写回的降级内容会被当成一次新复制。有效期内读到相同内容
	// 就跳过；内容不同则立即解除抑制（用户真复制了新东西）。
	clipSuppress      platform.ClipPayload
	clipSuppressUntil time.Time
}

// ready 在应用就绪后创建面板、注册热键、搭托盘。
func (st *appState) ready() {
	// 前缀表装配（§7.3.3 / §7.3.4）：**先建表再建窗**，让第一帧就带着表渲染。
	// 归一失败（C1 未知 id / C2 前缀撞车 / C4 保留键 / 注册表自检）是装配期 bug：
	// 不静默——落日志说清是什么坏了；同时退回**空前缀表**，空前缀表下 engine.Scan
	// 永远只返回 Global（engine_test.go 的「空表」用例守着），面板行为即退回
	// 接线前的「纯 Global」，不至于因为注册表写坏而开不了面板。
	loaded, providers, table, err := loadConfigs()
	if err != nil {
		log.Printf("voke 前缀表装配失败，退回纯全局模式（不打前缀）：%v", err)
	}
	st.providers, st.table = providers, table
	st.cfg = loaded // 供设置窗 seed 当前热键 / 自启 / 主题
	// 顶层设置：热键（配置里有且可解析才覆盖默认）+ 开机自启（注册表 Run 键）+ 主题。
	showHotkey := hotkeyShow
	if loaded.HasHotkey {
		if _, ok := config.ParseHotkey(loaded.Hotkey); ok {
			showHotkey = loaded.Hotkey
		} else {
			log.Printf("voke 配置热键 %q 不可解析，沿用默认 %s", loaded.Hotkey, hotkeyShow)
		}
	}
	st.showHotkey = showHotkey // 记下来供设置窗改键「撤旧注新」比对
	if loaded.HasAutostart {
		if err := platform.SetAutostart(loaded.Autostart); err != nil {
			log.Printf("voke 设置开机自启（=%v）失败：%v", loaded.Autostart, err)
		}
	}
	// 主题：appcfg.Theme 的码值（"system"/"light"/"dark"）与 mygo ThemeSource 逐字相同，
	// 直接透传。空（无配置）→ mygo 归一为跟随系统。
	platform.SetThemeSource(string(loaded.Theme))
	// 解码图标位图缓存：256 条 LRU（真机 155 应用约 1.4MB，上限约 2.3MB@48px），
	// 防止长会话里不同 exe 路径把堆无限推高；隐藏 10s 后另裁到 64 条。
	// 只主线程读写，见 internal/ui/iconcache.go。
	st.icons = appui.NewIconCache()
	// 剪贴板内存历史：条数上限来自顶层 `[clipboard] max_entries`，未写则默认 100。
	st.hist = cliphist.New(clipboardMaxEntries(loaded))
	// 已落地 provider 注册表 + 进程内频率表（装配一次）。剪贴板 provider 接
	// 真实历史快照闭包；局部变量 `providers` 遮蔽了包名，故经助手函数拿注册表。
	st.pluginReg = builtinProvidersWithClipboard(st.clipboardFetch)
	applyProviderConfigs(providers, st.pluginReg) // 归一后的 weight/count/enabled 下发到实例
	st.freq = loadFrequency()
	st.model.Freq = st.freq // apps-only 全局排序吃频率加成（同一张 map，Record 就地自增）
	// 警告类（C3 / C7 / 重复覆盖 / 非有限 weight）由 config 返回、在这里落日志：
	// config 是纯逻辑层拿不到日志设施（见其包注释）。
	for _, w := range providers.Warnings {
		log.Printf("voke 插件配置警告：%s", w)
	}
	st.panel = mygo.NewWindow(mygo.WindowOptions{
		Title:     "Voke",
		Panel:     true, // 浮动面板：置顶、不进任务bar、不进应用切换器
		Hidden:    true, // 启动先隐藏，靠热键/托盘唤出
		Frameless: true,
		// PoC 阶段刻意**关闭** Vibrancy（不设 Vibrancy，等价
		// mygo.VibrancyNone）并删掉 Transparent。理由：mygo v0.5.5 只要
		// Vibrancy != ""，internal/windows/window.go:131 就置
		// noRedirect=true 并加 WS_EX_NOREDIRECTIONBITMAP，渲染路径切
		// Composed，compositor.go:107-129 由此**无条件多建一套 D3D11
		// 设备 + DComp 设备**——这是提交态内存 +47MB 的头号主因
		// （真机：Vibrancy 开时 PrivateCommit 131.8MB / WS 157.5MB，
		// 生产 voke 仅 93.7MB），本轮要的就是关掉它做对照数据。
		// Transparent 对 Content 窗口本就是 no-op（唯一用途在 webview
		// 路径 window.go:915），一并去掉。
		// 关掉后 view.go:98-102 的 else 实色分支自动接管（与官方
		// examples/launcher/main.go:62-66 同款），不会出现黑块；
		// 毛玻璃材质后续做成用户开关再接回此处即可（数据定论后）。
		Width:         geometry.PanelWidth,
		Height:        geometry.PanelHeight,
		DisableResize: true,
		Content:       ui.View(st.view),
	})
	// 关闭（Alt-F4 等）= 隐藏而非销毁，下次唤出即时可见；退出才真正销毁。
	st.panel.OnClose(func(e *mygo.CloseEvent) {
		if st.quitting {
			return // 退出流程：放行真关闭，让 prepareQuit 的关窗循环通过
		}
		e.PreventDefault()
		st.hidePanel()
	})
	// 失焦即隐藏（生产 voke 同款行为）。
	st.panel.OnBlur(st.hidePanel)
	// 「面板隐藏」打点挂在 **OnHide**（mygo window.go:1490），而不是
	// hidePanel 函数体内。理由是 2026-10-16 真机日志的定因结论：一次隐藏
	// 打出**两行**「面板快照 [面板隐藏]」（18:16:22 / 18:16:26 / 18:16:28 /
	// 18:16:52 各一对，两行 Objects 不同：6060→6094、7280→7299）。
	//
	// 根因**不是**打点被注册了两次（全仓 grep 只有 panel.go 这一处
	// platform.SnapshotMem("面板隐藏")），而是 **OnBlur 回调把 hidePanel 重入了一次**：
	//
	//	hidePanel() → Window.Hide()（mygo window.go:685-693）
	//	  → native.Hide() 即 ShowWindow(hwnd, SW_HIDE)
	//	    → Windows 给**当前前台窗口**同步发 WM_ACTIVATE(WA_INACTIVE)
	//	      → internal/windows/window.go:369-377 判 waInactive → w.h.Blurred()
	//	        → fire(&w.onBlur) → 回到上面 OnBlur 注册的 hidePanel（嵌套重入）
	//
	// 内层那次 Hide() 会被 mygo 自己的 `if !w.native.IsVisible() { return }`
	// 守卫挡掉（此刻窗口已不可见），所以**窗口不会重复隐藏、也不会无限递归**
	// ——这正是只多出一行、而不是指数爆炸的原因。但只要打点写在 hidePanel
	// 函数体内且位于 Hide() 之后，内层调用就会**无条件**再打一行，因为那条
	// 打点不在任何守卫里。两行 GC/WorkingSet/HeapSys 完全一致而 Objects 递增，
	// 正是因为外层 Hide() 返回到外层打点之间又分配了一批对象（内层那次
	// ReadMemStats 看到的对象数更少，故 6060 在前、6094 在后）。
	//
	// 修法即本行：把打点收敛到 mygo 自带的 OnHide，它与 Hide() 共用同一个
	// IsVisible 守卫、且**只在真正发生「可见→隐藏」跃迁时 fire 一次**
	// （window.go:687-691）。语义正是「状态真正从可见变隐藏的那一次」，
	// 重入的内层 Hide() 早退、根本不 fire，于是天然一次隐藏一行日志。
	// 不是加防重标志位糊过去的——标志位只能掩盖症状，而这里是把打点挪到
	// 框架本就提供的「真实状态跃迁」事件上。
	//
	// 打点意图（诊断脚手架，2026-10-16 内存增长定因加）：与下一次唤起打点
	// 配对，差值即「一次唤出→隐藏」整个来回的净增量。真机数据要回答的核心
	// 问题之一是「隐藏后是否仍在合成」（mygo 侧 Windows 没有
	// OccludableSurface 实现，runtime.go:585 的 held 分支在 Windows 上恒不成立，
	// 隐藏不等于停帧）——这对快照的斜率就是判据。HeapIdle/HeapReleased
	// 也在同一条日志里（见 internal/platform/mem_windows.go），用来读「归还」
	// 而非只看 HeapSys 永不回落。
	st.panel.OnHide(func() {
		// 托盘 awake 文案含「还剩 …」，而 mygo 的 Tray.SetMenu 在调用时快照、无弹窗钩子，
		// 不重建就会一直显示建菜单那一刻的剩余时间（真机「剩 47 分还显示 1 小时」）。
		// 借「真实隐藏跃迁」这一自然事件重建一次（不加定时器）：下次唤起/收起即刷新。
		st.refreshTrayMenu()
	})

	// 全局热键唤/隐面板。注册失败（被占用等）只记日志，不阻断启动。
	// §7.2.2 步骤8a：mygo.GlobalShortcut.Register 那一句已收进
	// platform.RegisterHotkey（串、回调、错误处置全部原样，只包一层）；
	// 回调本体 st.togglePanel 读写 appState 与面板句柄，留在本文件。
	if err := platform.RegisterHotkey(showHotkey, st.togglePanel); err != nil {
		log.Printf("注册热键 %s 失败：%v", showHotkey, err)
	} else {
		st.mainHotkeyOn = true
	}
	// 插件热键：只注册“已启用且配置了 hotkey”的插件（无默认值）。单个失败只记日志
	// 并在设置页展示，不影响主热键与其它插件。
	st.syncPluginHotkeys()
	st.setupTray()
	// 应用枚举接缝（apps.go 契约）：这里**只注册回调、不触发枚举**。
	// 唤起懒加载（PoC 文档 §11.12）——启动与唤起都只有一个空输入框，
	// 不枚举、不建索引、不渲染列表；等用户首次打出非空查询时，
	// view.go 才调 appsmodel.EnsureLoaded() 起后台枚举（platform.InstalledApps
	// 读磁盘，几十到几百毫秒），完成经 Window.Update 回主线程走这里的回调：
	// 灌数据 + 过滤 + 落 loading。
	// 「引擎索引完成」的内存快照由 loadAppsAsync 自己打（apps.go），
	// 这里不重复。
	bindApps(st.panel, func(apps []appsmodel.AppEntry) {
		st.model.Apps, st.model.Loading, st.model.Loaded = apps, false, true
		st.filter()
		st.refreshGlobalAgg() // apps 首次加载完成后，用最新 Results 重算全局合并行
	})
	// 剪贴板监控：后台只轮询序列号，变化后回主线程读内容并写入内存历史。
	st.startClipboardWatcher()
	platform.SnapshotMem("启动")
	log.Printf("voke 就绪（版本 %s，热键 %s）", aboutVersion(), showHotkey)
	// 启动自检（对照生产 WhenReady 里的 VOKe_SELFTEST 分支）：env=1 时跑一轮进程内关键
	// 链路自检、失败项计数落日志。放最末——此时 provider / 热键 / 托盘 / apps 接缝均已就位。
	if selftestRequested() {
		runSelftest()
	}
}

// showPanel 唤出面板：**按主显示器** WorkArea 水平居中、垂直居中略偏上
// （系数 geometry.CenterPercent=48，见 geometry.PanelTopLeft 与本文下方算术段），
// 搜索框抢焦点。
//
// **显隐保留会话**（用户拍板的 Rust parity）：重新显示 ≠ 用户修改输入。本函数
// 不清查询、不退出插件——热键 / 托盘 / 失焦 / Esc 收起后再唤起，上次的查询与
// 插件页原样还在。复位只发生在「离开面板」类动作成功且窗口已隐藏之后（见
// resetAfterAction），或用户显式按 Ctrl+Backspace（onResetInput，面板保持可见）。
// 显示后另调 refreshOnShow **重跑当前查询**（不换会话）：
// 结果跟着隐藏期间变化的世界走，重掷类插件由该方法自行挡掉。
//
// 显示器来源（PoC 文档 §11.9 决策④，2026-10-16 落实）：用
// mygo.Screen.PrimaryDisplay()，**不用** DisplayNearestPoint(CursorScreenPoint())。
//
//   - 签名核实：`func (s *ScreenModule) PrimaryDisplay() Display`
//     （modules.go:243），实现就是遍历 s.Displays() 找 Primary == true，
//     找不到退回第一个（:246-252）；`Display` 结构在 modules.go:201-212。
//     原来的 DisplayNearestPoint 在同一模块 modules.go:265，不是新 API。
//   - 为什么改：唤醒时指针可能在副屏 → 面板落到副屏，而面板尺寸是固定 DIP
//     常量，副屏分辨率/缩放与主屏不同就会出现「尺寸不对 / 定位越界」。
//   - 与生产对齐：Rust 侧 voke-app/src/main.rs:426 用
//     `Bounds::centered(None, search::window_size(), cx)`，display_id = None
//     时 gpui 的 Bounds::centered（gpui-pre-0.3.8/src/geometry.rs:740-754）
//     取 cx.primary_display()——**Rust 版确实总在主显示器唤起**，本改动是对齐
//     生产行为，不是行为退化。代价是失去「在哪屏唤起就在哪屏」的便利。
//   - 主线程约束：PrimaryDisplay 内部走 Displays()，而 Displays() 走
//     onMainValue（modules.go:224），只许主线程调。showPanel 本来就在主线程
//     （热键回调 modules.go:627-634 直调），无问题。
//
// 定位算术已随 §7.2 步骤4 下沉 internal/logic/geometry.PanelTopLeft（纯数值、
// 零 mygo）。口径（PoC 文档 §7.6.2 有四个启动器的对照数据与本轮定因）：
//
//	x  = WorkArea.X + (WorkArea.Width - panelW) / 2           // 水平居中
//	cy = WorkArea.Y + WorkArea.Height * pct / 100              // 面板中心
//	y  = cy - panelH / 2                                        // 面板顶
//
// 垂直锚的是**面板中心**而不是输入框中心：实测本面板输入行高 56 DIP，
// 输入框中心距面板顶仅 28 DIP，而面板高 494 DIP——两者相距 247 DIP。旧
// 口径把输入框中心锚在 25%（与 CmdPal 同值），等于把 494 高的整块面板
// 的视觉重心也拽到 ≈46%，用户仍判「偏高」。改锚面板中心后，pct 直接
// 表达用户那句「居中略偏上」：48 = 正中偏上 2%。详见 geometry.CenterPercent。
//
// 分工：本函数只负责**取 WorkArea**（PrimaryDisplay，platform 侧才碰得到的
// 事）与 **SetPosition**（mygo Window），然后把纯数值喂给 geometry；尺寸用
// st.panel.Size() 的实际值而非直接引常量，保证「窗口实际多大就按多大摆」。
//
// 换显示器来源后算术无需重算，依据（源码核实 2026-10-16）：
//   - WorkArea 已是 DIP：Displays() 里 `conv` 用**该显示器自己的** dpi 做
//     toDIP（internal/windows/system.go:358-366，toDIP 见
//     internal/windows/win32.go:691 `(v*96 + dpi/2)/dpi`），
//     ScaleFactor 字段（system.go:367 `float64(dpi)/96`）只是给需要
//     像素尺寸的人用的（图标取像素，internal/platform/shell.go iconPixelSize）。
//   - SetPosition 收 DIP：window.go:879 → native.SetBounds
//     （internal/windows/window.go:622-625）用 dpiOf(hwnd) 反向 toPx，
//     同一个 DIP 体系。故 WorkArea / Size() / SetPosition 三者同系，
//     直接相减即可，**不要再乘 ScaleFactor**（乘了就双重缩放）。
//   - 跨屏不是问题：d 是哪块屏就用那块屏自己的 WorkArea 与自己的 dpi
//     换算，主屏副屏各自自洽。
//   - 进程是 PerMonitorAwareV2（internal/windows/app.go:113-114
//     SetProcessDpiAwarenessContext(-4)），dpiOf(hwnd) 走
//     GetDpiForWindow（win32.go:675-680），随窗口所在屏变化，故先
//     SetPosition 再 Show() 的顺序下换算基准正确。
//
// 尺寸不夹紧 WorkArea：面板 680×494 是与生产 voke 对齐的固定值
// （生产 680×472，search.rs:1830-1835），主屏 WorkArea 恒大于它；
// Rust 侧也只做 `size.min(&visible_bounds.size)` 的下限保护
// （geometry.rs:749），语义相同。
//
// 防首帧闪烁：窗口 Hidden 创建，本函数在 Show() 之前完成定位/聚焦请求，
// 首帧即是终态位置与内容；mygo v0.5.5 未见额外的
// 「无闪烁显示」API（如 SWP_NOACTIVATE 暴露），能做到的即此，
// 实测留 V5。
func (st *appState) showPanel() {
	st.stopIconTrimTimer()
	// 唤起前记住前台窗口（剪贴板历史「粘贴」要回到这里）。只在面板原本不可见时
	// 捕获：重复唤起 / 第二实例通知不能把面板自己当成目标。托盘点击时前台可能
	// 仍是上一次的应用（任务栏通常不抢前台），捕获不到有效目标时粘贴会回退为
	// 「已复制，请手动粘贴」。
	if !st.panel.IsVisible() {
		t := platform.ForegroundTarget()
		own := t.Hwnd == st.panel.NativeHandle() ||
			(st.settings != nil && !st.settings.closed && t.Hwnd == st.settings.win.NativeHandle())
		switch t.Class {
		case "Shell_TrayWnd", "Shell_SecondaryTrayWnd", "NotifyIconOverflowWindow", "Progman", "WorkerW":
			own = true // 托盘 / 桌面：不是可粘贴的应用窗口
		}
		if own {
			st.clipTarget = platform.ClipTarget{}
		} else if t.Valid() {
			st.clipTarget = t
		}
	}
	if st.notice != "" && !st.noticeAt.IsZero() && time.Since(st.noticeAt) > noticeTTL {
		st.notice = ""
	}
	d := mygo.Screen.PrimaryDisplay()
	w, h := st.panel.Size()
	x, y := geometry.PanelTopLeft(d.WorkArea.X, d.WorkArea.Y, d.WorkArea.Width, d.WorkArea.Height, w, h)
	st.panel.SetPosition(x, y)
	st.input.Focus()
	st.panel.Show()
	// 唤回刷新：保留会话不等于保留过期结果。隐藏期间窗口开/关、awake 状态、
	// 插件启用集合都可能变；重跑当前查询让用户敲第一个键前看到的就是当下数据
	// （对照 Rust 窗口激活观察者里的 refresh）。重掷类插件由 refreshOnShow 挡掉。
	st.refreshOnShow()
	// 唤起即刷新托盘（awake 剩余时间是随时间变的；此处重建让用户每次看托盘都拿到
	// 当下倒计时——mygo 无菜单弹出钩子，只能靠这类自然事件，不设定时器）。
	st.refreshTrayMenu()
	// 「面板首次唤起」是 docs/notes/memory.md 的快照标签之一，也是
	// 首帧真正构建 view 树（解码可见行图标、排版全部文字）的时刻——
	// 诊断脚手架（2026-10-16 内存增长定因加）用它在真机日志里把
	// 「首帧一次性地板」与「后续反复唤起的累积」分开。
	if !st.shownOnce {
		st.shownOnce = true
		platform.SnapshotMem("面板首次唤起")
	}
	// 生产「唤起重扫 + 60s 防抖」：距上次枚举 ≥ 防抖则后台再扫一轮，装/卸应用无需重启
	// 启动器即可在下次唤起看到（异步完成经 bindApps 回调换 Apps，本轮 UI 不阻塞）。
	appsmodel.MaybeRefreshOnWake(time.Now(), appsScanDebounce)
}

// openAwake 是托盘「保持唤醒」状态条目被点的动作：唤出面板 + 直达 awake 插件第一级
// （对照生产 main.rs `open_awake`：`show_panel` 之后 `activate_plugin("awake")`）。
// showPanel 只显隐、保留会话；换会话由 enterPlugin 负责——对照 Rust
// activate_plugin_from_picker 的「关 picker / 替换 active_plugin / 清空输入」，
// 所以从任意旧插件（含子模式）点进来都落在干净的 awake 第一级，而不是嵌套进去。
// showPanel 已在主线程（托盘 Click 回调），enterPlugin 只改 appState 并经
// applyEngineState→refreshProviderResults 刷出 awake 候选，无跨线程调用。
func (st *appState) openAwake() {
	st.showPanel()
	st.enterPlugin(awake.ID)
}

// resetSession 把**模式路由**状态清回「全新的 Global 会话」：engine.State 归零
// （零值即 Global）、picker 列表清空、picker 选中复位、provider / 全局合并行清空、
// 四个结果列表回顶。
//
// 调用点只有两处，都是「真的换会话」：
//   - resetAfterAction：离开面板的动作成功、窗口已隐藏之后（对照 Rust
//     search.rs reset_after_action）；
//   - enterPlugin：程序化直达插件前先换一个新会话（托盘 awake），避免插件嵌套。
//
// 这里**不碰 model.Query**：输入框清空与顶层结果预热由 resetAfterAction 负责；
// enterPlugin 随后经 applyEngineState 把框同步成新层的空查询。
func (st *appState) resetSession() {
	st.engineState = engine.State{}
	st.pickerRows, st.pickerChosen = nil, 0
	st.provRows, st.provChosen, st.provHint = nil, 0, ""
	st.provLastKey = ""
	st.globalRows, st.globalProvRows, st.globalChosen = nil, nil, 0
	st.resetListScroll()
}

// resetAfterAction 是会话复位的唯一实现：退出插件、清空输入、顶层列表重新预热。
// 两个调用时机：
//   - 「离开面板」类动作成功且窗口**已被隐藏之后**（对照 Rust search.rs:1128
//     reset_after_action）——启动应用 / 切换窗口 / 保持唤醒这些动作的意图都是
//     「离开面板去做别的事」，一次只干一件事，干完就收，下次唤回不该还留着上次
//     的查询、插件页和旧结果；
//   - 用户按 Ctrl+Backspace（onResetInput）——手动要求「清成全新会话」，此时面板
//     **保持可见**，复位后继续接受输入。
//
// Copy / Refresh / 进插件等动作**不调用**本函数：输入与结果原样保留，用户
// 回来还能看到并再次复制（对照 core Action::should_reset_input 的分类）。
//
// 末尾的 filter 用空查询重算一次，把顶层结果预置好（对照 Rust reset 后
// spawn_query("") 的预热）：下次唤起首帧就是干净的全局列表，不会闪一下旧插件
// 结果。最后再钉一次列表顶部——filter 的 ScrollIntoView(0) 只保证首行可见。
func (st *appState) resetAfterAction() {
	st.resetSession()
	st.model.Query = ""
	st.filter()
	st.resetListScroll()
}

// resetListScroll 把四个结果列表的滚动位置请求钉到顶部。
//
// ui.ListState 的 anchor/inset 是跨帧状态：只清数据/选中（resetSession、Filter、
// refreshGlobalAgg）不会把它复位，下一次出现结果时会保留上一次的滚动位置，
// 表现为「输入框已重置、列表还停在旧位置」。输入/作用域变化、唤起/隐藏、
// 首轮数据到达时都显式回顶，确保新的一次查询从第一行开始。
func (st *appState) resetListScroll() {
	st.list.ScrollTo(0, ui.Start)
	st.pickerList.ScrollTo(0, ui.Start)
	st.provList.ScrollTo(0, ui.Start)
	st.globalList.ScrollTo(0, ui.Start)
}

// hidePanel 隐藏面板（失焦 / 热键 / 托盘菜单 / Esc / 离开动作收尾都会走到）。
//
// 这里**只管隐藏，不复位会话**：显隐保留查询与插件页（Rust parity——重新显示
// ≠ 用户修改输入）。离开面板的动作（启动 / 切窗 / 保持唤醒）在隐藏**之后**
// 另调 resetAfterAction；Copy 只隐藏、保留输入供下次再复制；Esc / 失焦 / 热键
// 只藏不收。
//
// 这里也**不打点**：「面板隐藏」的打点已收敛到 ready() 里注册的
// st.panel.OnHide（那里有完整的定因注释）。原因是本函数会被 OnBlur 重入
// ——ShowWindow(SW_HIDE) 让前台窗口同步收到 WM_ACTIVATE(WA_INACTIVE)，
// mygo 据此 fire onBlur 又调回本函数（链路见 ready() 的注释）。打点写在
// 函数体内就会在重入时无条件多打一行；OnHide 则只在真实「可见→隐藏」跃迁
// 时 fire 一次，且内层重入的 Hide() 因窗口已不可见而早退、不会 fire。
//
// 另外本函数对「已隐藏」是幂等的（mygo Hide 自带 IsVisible 守卫），
// 因此多条隐藏路径（热键 toggle / Esc / OnClose / 动作收尾）重复触发也安全。
func (st *appState) hidePanel() {
	wasVisible := st.panel.IsVisible()
	st.panel.Hide()
	if wasVisible {
		st.armIconTrimTimer()
	}
}

// hiddenIconTrimDelay / hiddenIconCacheMax 对照 Wox 的隐藏裁剪：面板连续隐藏
// 10 秒后，把解码图标缓存从常规 256 条压到 64 条；快速反复唤起不裁。
const (
	hiddenIconTrimDelay = 10 * time.Second
	hiddenIconCacheMax  = 64
)

// armIconTrimTimer 在面板真正隐藏后启动一次性裁剪定时器。定时器在后台触发，
// 裁剪动作经 Window.Update 回主线程（缓存只主线程读写）。世代号确保 showPanel
// 或下一次 hidePanel 之后旧定时器即使晚到也不会误裁新会话。
func (st *appState) armIconTrimTimer() {
	st.stopIconTrimTimer()
	panel := st.panel
	if st.icons == nil || panel == nil {
		return
	}
	st.iconTrimGen++
	gen := st.iconTrimGen
	st.iconTrimTimer = time.AfterFunc(hiddenIconTrimDelay, func() {
		// 闭包只捕获 arm 时的 panel，不在后台 goroutine 里读 st.panel；真正裁剪
		// 经 Window.Update 回主线程，世代号 / 面板同一性 / 已销毁三重校验后才执行。
		panel.Update(func() {
			if st.iconTrimGen != gen || st.panel != panel || panel.IsDestroyed() {
				return
			}
			st.iconTrimTimer = nil
			if panel.IsVisible() {
				return
			}
			if st.settings != nil && !st.settings.closed {
				return // Wox 同款：设置窗打开时不裁
			}
			before := st.icons.Len()
			removed := st.icons.Trim(hiddenIconCacheMax)
			if removed > 0 {
				log.Printf("面板隐藏 %s，裁剪图标缓存 %d→%d 条", hiddenIconTrimDelay, before, st.icons.Len())
			}
		})
	})
}

// stopIconTrimTimer 停掉待触发的隐藏裁剪，并让已在途的回调失效。
func (st *appState) stopIconTrimTimer() {
	st.iconTrimGen++
	if st.iconTrimTimer != nil {
		st.iconTrimTimer.Stop()
		st.iconTrimTimer = nil
	}
}

// togglePanel 是全局热键回调：可见则隐，不可见则唤。
func (st *appState) togglePanel() {
	if st.panel.IsVisible() {
		st.hidePanel()
	} else {
		st.showPanel()
	}
}

// setupTray 创建托盘图标并挂上首版菜单（awake 状态 / 显示面板 / 设置… / --- / 退出）。
//
// awake 状态条目对照生产 voke 托盘菜单第一项：文案由 awake.MenuLabel 依当下
// platform.AwakeState() 生成（未激活 / 「还剩 … · 屏幕打开|关闭」），**任何状态都可点**，
// 点了 st.openAwake() 唤出面板并直达 awake 插件第一级（对齐生产 `open_awake →
// activate_plugin("awake")`；直达所需的生命周期装配已随步骤8b 落地，故不再只开空面板）。
// 「设置…」项经 st.openSettings 唤出设置窗（与生产菜单一致，PoC 设置窗已迁移）。
//
// 「动态刷新」的更正（核实 mygo v0.5.5 源码 2026-10-17）：Tray.SetMenu 在**调用时**就
// m.snapshot() 取快照并下发 native（modules.go:725-729），弹出用的是这份既定快照，并非
// 每次 popup 现读——mygo 没给原生 SetMenu 路径挂 WM_INITMENUPOPUP 钩子。所以不能「设一次
// 自动刷新」，必须在 awake 每次变化后重建（见 refreshTrayMenu 与 view.go execPlatformAction）。
// 只有 PopUpMenu() 才每次重取快照，但托盘菜单是用户右键弹的，走不到那条路径。
func (st *appState) setupTray() {
	tray, err := platform.NewTray(appui.TrayIcon(), "Voke")
	if err != nil {
		log.Printf("创建托盘图标失败：%v", err)
		return
	}
	st.tray = tray
	tray.OnClick(st.onTrayClick)
	st.refreshTrayMenu()
}

// trayDoubleClickWindow 是托盘双击识别窗口：mygo 原生没有 OnDoubleClick，双击在
// Windows 回调里就是两次 wmLButtonUp → 两次 Clicked。取系统默认双击时间 500ms
// （用户可在系统设置里调整；这里不为此单独起 syscall，500ms 覆盖默认与常见档位）。
const trayDoubleClickWindow = 500 * time.Millisecond

// onTrayClick 处理托盘左键单击：第一次只记时刻，500ms 内的第二次判定为双击并
// 唤出面板（不是 toggle——用户要的是「双击显示」；面板已显示时 showPanel 会重新
// 定位并聚焦，查询与插件页保留）。识别到双击后清空时刻，避免第三次点击续判。
func (st *appState) onTrayClick() {
	now := time.Now()
	if isTrayDoubleClick(st.trayLastClick, now) {
		st.trayLastClick = time.Time{}
		st.showPanel()
		return
	}
	st.trayLastClick = now
}

// isTrayDoubleClick 是双击判定的纯函数（时钟注入，便于单测）。
func isTrayDoubleClick(prev, now time.Time) bool {
	if prev.IsZero() {
		return false
	}
	d := now.Sub(prev)
	return d >= 0 && d <= trayDoubleClickWindow
}

// trayMenuItems 依当下 awake 状态产出托盘菜单项。
//
// 「退出」用显式 Click 调 mygo.App.Quit()，**不用** {Role: RoleQuit}：RoleQuit 依赖
// mygo 把托盘菜单点击路由到 performRole→App.Quit（menu.go:481-484），但托盘是非窗口
// 原生菜单，这条 role 触发链在真机上没生效（表现为「退出无响应」）；且 menu.go:356
// 在非 darwin 上还可能把纯 role 项判成 macOnly 而 Hidden。显式 Click 与同级「显示面板」
// 同款，稳。App.Quit() 会走 OnWillQuit（本文件 ready 里注册的 tray.Destroy）。
func (st *appState) trayMenuItems() []*mygo.MenuItem {
	active, keepScreen, expiresAt := platform.AwakeState()
	var status *awake.Status
	if active {
		status = &awake.Status{ExpiresAt: expiresAt, KeepScreen: keepScreen}
	}
	return []*mygo.MenuItem{
		{Label: awake.MenuLabel(status, time.Now().Unix()), Click: func(*mygo.MenuItem, *mygo.Window) { st.openAwake() }},
		// 「显示面板」走 togglePanel，对齐生产：托盘该项映射到 ShellCommand::TogglePanel
		// （main.rs:301 → panel_toggle_win32：前台可见则隐藏、否则唤起），不是无条件 show；
		// 也与 Go 自身热键的 togglePanel 一致。二次启动 OnSecondInstance 仍走无条件 showPanel
		// （对应生产 ShellCommand::ShowPanel 固定显示），二者不同、各自对齐生产。
		{Label: "显示面板", Click: func(*mygo.MenuItem, *mygo.Window) { st.togglePanel() }},
		{Label: "设置…", Click: func(*mygo.MenuItem, *mygo.Window) { st.openSettings() }},
		mygo.Separator(),
		{Label: "退出", Click: func(*mygo.MenuItem, *mygo.Window) { mygo.App.Quit() }},
	}
}

// refreshTrayMenu 用当下 awake 状态重建并挂上托盘菜单（幂等；托盘未建则跳过——headless
// 测试里 st.tray 恒 nil，正好借此不触碰 mygo）。调用点：setupTray（首挂）与 awake 状态
// 变化的落点（view.go execPlatformAction 的 SetAwake / CancelAwake，均在主线程）。定时器
// 自然到期那条在后台 goroutine、不经这里，其托盘文案会滞到下一次状态变化——生产靠弹窗
// 即时刷新消掉这段，mygo 无该钩子，留真机 V7 观察。
func (st *appState) refreshTrayMenu() {
	if st.tray == nil {
		return
	}
	platform.SetTrayMenu(st.tray, st.trayMenuItems())
}
