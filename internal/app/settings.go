// settings.go：独立设置窗口（§7.2 / 设计文档「设置页」：880×600 普通窗，带标题栏、
// 可缩放、不自动隐藏；面板完全不动）。
//
// 装顶层设置（**开机自启 / 界面主题 / 唤隐热键**）+ 每插件配置（设计文档 §385 的 Group：
// **启用 / 参与全局搜索 / 权重 0–10 / 激活别名(prefix)**，加 §33 的 Direct/Indirect 下拉）。
// 数据全走 appcfg 落盘 + platform 副作用，保存时尽量当场生效：
//   - 开机自启：`platform.SetAutostart`；
//   - 插件配置：写 config.toml 的 enabled/is_global/weight/prefix/activation 覆盖 +
//     `reloadConfigs` 下发到 live 注册表（前缀先过 prefixSettingsError 的 C2/C3/C4，无效不落盘）；
//   - 界面主题：`platform.SetThemeSource`（appcfg 码值 = mygo ThemeSource，即时换肤）；
//   - 唤隐热键：`UnregisterHotkey(旧)` → `RegisterHotkey(新)`，失败回滚旧串（不留空窗）。
//
// 这些换肤 / 改键的**真机效果**仍需 V 跑确认（mygo 主线程 API，headless 到不了），但
// 接线已落，不再推迟。
//
// mygo 是 immediate-mode：本窗 view 每帧整体重建，只读写 st.settings（绑定态），副作用
// 集中在「保存」按钮点击那一次（saveSettings）。widget 只发意图（Clicked / 绑定指针被
// 就地改写），判定与落盘在本文件末尾的执行器。
package app

import (
	"fmt"
	"log"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"voke/internal/i18n"
	"voke/internal/logic/appcfg"
	"voke/internal/logic/charset"
	"voke/internal/logic/cliphist"
	"voke/internal/logic/config"
	"voke/internal/logic/engine"
	"voke/internal/platform"
	"voke/internal/providers/random"
	appui "voke/internal/ui"
	"voke/internal/ui/theme"
)

// 设置页左侧导航的稳定 id（只在本窗口生命周期内使用，不落盘）。
const (
	settingsSectionGeneral   = "general"
	settingsSectionShortcuts = "shortcuts"
	settingsSectionAbout     = "about"
	// pluginNavPrefix 是侧栏「单个插件」条目的 id 前缀，如 plugin:base64。
	// 用独立前缀而不是裸 id，避免与页面 id 撞车。
	pluginNavPrefix = "plugin:"
)

// pluginNavID 是某个插件在侧栏里的导航 id。
func pluginNavID(id string) string { return pluginNavPrefix + id }

// pluginIDFromNav 从侧栏 id 解出插件 id；不是插件条目时 ok=false。
// 刻意不用 strings.HasPrefix/CutPrefix：本包有一条守卫测试禁止在装配层出现
// 第二套前缀判定写法（见 TestNoSecondPrefixJudgementInMain），这里只是导航 id
// 的机械拆分，但仍复用同样的约束，避免扫描误报。
func pluginIDFromNav(nav string) (string, bool) {
	if len(nav) <= len(pluginNavPrefix) || nav[:len(pluginNavPrefix)] != pluginNavPrefix {
		return "", false
	}
	return nav[len(pluginNavPrefix):], true
}

// projectURL 是「关于」里展示的项目地址（GitHub 仓库）。
const projectURL = "https://github.com/zyition/voke/"

// buildVersion 是发布流水线通过 `-ldflags -X .../internal/app.buildVersion=<tag 版本>`
// 注入的版本号（与安装器 -dVokeVersion 同源）。本地 / 未注入构建为空串。
var buildVersion string

// aboutVersion 是「关于」区显示的版本串，对照生产 read_only_item("版本", CARGO_PKG_VERSION)。
// 优先取构建期注入的 buildVersion；未注入时退回 debug.ReadBuildInfo（本地 `go build`
// 给 "(devel)"，显示为「开发版」而不是把内部占位串直接甩给用户）。
func aboutVersion() string {
	if v := strings.TrimSpace(buildVersion); v != "" {
		return v
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		if v := strings.TrimSpace(bi.Main.Version); v != "" && v != "(devel)" {
			return v
		}
	}
	return i18n.L("开发版", "Development build")
}

// aboutConfigPath 是「关于」区显示的配置文件位置，对照生产 config_path_label：拿不到有效
// 落盘路径（非绝对 = DataDir 取不到且未设 env）时显示「未落盘…」而非半截相对路径。
func aboutConfigPath() string {
	p := configFilePath()
	if !filepath.IsAbs(p) {
		return i18n.L("未落盘（取不到数据目录，且未设 VOKE_CONFIG）",
			"Not saved yet (data directory unavailable and VOKE_CONFIG is unset)")
	}
	return p
}

// providerEdit 是「插件配置」区的一行（对照设计文档 §385 的每插件 Group：启用 /
// 参与全局搜索 / 权重 0–10 / 激活别名(prefix)，外加 §33 的 Direct/Indirect 下拉）。
// 各字段直接是绑定态（widget 就地改它），保存时逐个包成 *指针* 落 override。
// 值从 st.providers（归一后的生效态）seed，故「不改直接存」是幂等写回；前缀改坏了
// 由 prefixSettingsError 在落盘前拦下（冲突 / 空白 / 保留键），其余数值由 Normalize 兜底。
type providerEdit struct {
	id          string
	name        string
	description string
	enabled     bool
	isGlobal    bool
	weight      float64
	prefix      string
	activation  string // 显示值（本地化后的 Direct / Indirect 标签），save 时映射回 "direct"/"indirect"
	hotkey      string // 插件全局快捷键；空 = 未绑定
	// defaultLength 仅随机值插件使用：字符集未单独设置长度时的默认值。
	defaultLength float64
	// charsets 仅随机值插件使用：名称 + 字符类正则 + 默认长度 + 启用开关。
	charsets []charsetEdit
}

// charsetEdit 是设置页里一套字符集的绑定态。
type charsetEdit struct {
	name    string
	pattern string
	length  float64 // 0 = 跟随插件级默认长度
	enabled bool
}

// settingsWindow 是设置窗的绑定态（只在主线程、只在窗口存活期读写）。theme / language
// 存的是 Select 显示的当前语言标签，save 时经 themeCode / languageCode 映射回配置码值。
type settingsWindow struct {
	win          *mygo.Window
	section      string // 左侧导航当前项（general / shortcuts / about / plugin:<id>）
	pluginsOpen  bool   // 左侧「插件」分组的展开态（默认展开）
	scrollPlugin string // 待滚到顶部的插件 id；插件总览页消费一次
	pluginList   ui.ListState
	hotkey       string
	autostart    bool
	theme        string
	language     string
	clipboardMax float64 // 剪贴板上限（1–1000），落盘前转 int
	edits        []providerEdit
	// closed 防重复进入销毁流程：OnClose 与「完成」按钮可能先后触发；销毁本身
	// 经 Window.Update 延到当前帧之后执行。
	closed bool
	// 热键录制器状态：recording 时用一个只读输入框接管按键（每帧回焦点），捕获到组合
	// 即注册生效；Esc 取消并恢复旧键。recBuf 是只读框的绑定串（不承载实际内容）。
	// recPluginID 是本次录制的目标插件 id；空串 = 主唤隐热键。
	recording   bool
	recPluginID string
	hotkeyErr   string
	// configErr 是插件前缀校验失败的中文提示（冲突 / 含空白 / 保留键）。
	// 非空时 applySettings 不落盘、不重载，界面停留在上一次有效配置。
	configErr string
	recBuf    string
	recHandle ui.Handle
	// recGen 是录制超时的世代号：每次续期 +1；超时回调只在世代号未变且仍在录制时
	// 才取消（旧定时器即使晚到也失效）。只主线程读写。
	recGen int
	// recTimer 是当前录制超时定时器；续期/取消/成功时 Stop 并置 nil，确保不残留
	// 定时器、也不会在录制结束后再往主线程投递取消。
	recTimer *time.Timer
}

// hotkeyRecTimeout 是录制态的短超时：用户忘了 / 组合被别的应用截走时，不至于一直
// 停在「等待按键」。焦点回来或按了无效键会续期（类似短 TTL + 聚焦续期）。
const hotkeyRecTimeout = 10 * time.Second

// activationLabels 是激活方式下拉的显示值（随界面语言变化）。顺序与
// direct/indirect 码一一对应。
func activationLabels() []string {
	return []string{i18n.L("直接", "Direct"), i18n.L("间接", "Indirect")}
}

// activationLabel 把 engine.Activation 映成下拉显示值（seed 用）。
func activationLabel(a engine.Activation) string {
	if a == engine.Indirect {
		return activationLabels()[1]
	}
	return activationLabels()[0]
}

// activationCode 把下拉显示值映回落码（"direct"/"indirect"，交 config 归一）。
func activationCode(label string) string {
	if label == activationLabels()[1] {
		return "indirect"
	}
	return "direct"
}

// themeLabels 是主题 Select 的选项（顺序与 themeCodes 一一对应）。
func themeLabels() []string {
	return []string{i18n.L("跟随系统", "Follow system"), i18n.L("深色", "Dark"), i18n.L("浅色", "Light")}
}

// themeCodes 是 themeLabels 对应的 appcfg 码值。
var themeCodes = []string{"system", "dark", "light"}

// themeLabel 把码值映成显示标签（未知 / 空 → 跟随系统）。
func themeLabel(code appcfg.Theme) string {
	labels := themeLabels()
	for i, c := range themeCodes {
		if string(code) == c {
			return labels[i]
		}
	}
	return labels[0]
}

// themeCode 把显示标签映回码值（找不到 → system）。
func themeCode(label string) appcfg.Theme {
	for i, l := range themeLabels() {
		if label == l {
			return appcfg.Theme(themeCodes[i])
		}
	}
	return appcfg.ThemeSystem
}

// languageCodes 是语言 Select 的选项对应的配置码值。
var languageCodes = []appcfg.Language{appcfg.LanguageSystem, appcfg.LanguageChinese, appcfg.LanguageEnglish}

// languageLabels 是语言 Select 的显示值（随界面语言变化）。
func languageLabels() []string {
	return []string{i18n.L("跟随系统", "Follow system"), i18n.L("中文", "Chinese"), "English"}
}

// languageLabel 把配置码值映成显示标签（未知 / 空 → 跟随系统）。
func languageLabel(code appcfg.Language) string {
	for i, c := range languageCodes {
		if code == c {
			return languageLabels()[i]
		}
	}
	return languageLabels()[0]
}

// languageCode 把显示标签映回配置码值（找不到 → system）。
func languageCode(label string) appcfg.Language {
	for i, l := range languageLabels() {
		if label == l {
			return languageCodes[i]
		}
	}
	return appcfg.LanguageSystem
}

// seedSettings 用最近一次装配的 st.cfg（顶层）+ st.providers（插件启用态）把绑定态刷成
// 当前真值。每次 openSettings 都重刷，避免隐藏期残留上一次未保存的编辑。
func (st *appState) seedSettings(s *settingsWindow) {
	s.configErr = ""
	s.hotkey = st.cfg.Hotkey
	if s.hotkey == "" {
		s.hotkey = hotkeyShow
	}
	s.autostart = st.cfg.Autostart
	s.theme = themeLabel(st.cfg.Theme)
	s.language = languageLabel(st.cfg.Language)
	s.clipboardMax = float64(clipboardMaxEntries(st.cfg))
	s.edits = s.edits[:0]
	for _, p := range st.providers.Providers {
		ed := providerEdit{
			id: p.ID, name: p.Name, description: p.Description,
			enabled: p.Enabled, isGlobal: p.IsGlobal,
			weight: p.Weight, prefix: p.Prefix, activation: activationLabel(p.Activation),
			hotkey: p.Hotkey,
		}
		if p.ID == random.ID {
			ed.defaultLength = float64(p.DefaultLength)
			if ed.defaultLength < 1 {
				ed.defaultLength = 16
			}
			ed.charsets = make([]charsetEdit, 0, len(p.Charsets))
			for _, cs := range p.Charsets {
				ed.charsets = append(ed.charsets, charsetEdit{
					name: cs.Name, pattern: cs.Pattern, length: float64(cs.Length), enabled: cs.Enabled,
				})
			}
		}
		s.edits = append(s.edits, ed)
	}
}

// openSettings 懒建并唤出设置窗（托盘「设置…」点来，主线程）。已存在则重刷当前值 +
// 唤回焦点；创建走 mygo.NewWindow（普通窗，非 Panel/Frameless）。
//
// 生命周期：**关闭 = 程序化销毁**（对照 Rust settings.rs 的 remove_window 处置）。
// 设置窗 880×600 会新建一套 swapchain / 字体纹理，常驻约 20MB；若像面板一样隐藏复用，
// 这部分资源在窗口对象存活期间不会归还。故 OnClose /「完成」都走 closeSettings：
// 先拦截，再把 Destroy 投递到主循环下一次迭代（避免在渲染回调栈内释放原生窗口），
// 下次打开重新建窗。与 Rust 的「HWND 存活期内销毁」同一目的。
func (st *appState) openSettings() {
	if st.settings != nil {
		if st.settings.closed {
			return // 正在销毁；等主循环落地后下一次点击再建新窗
		}
		st.seedSettings(st.settings)
		st.settings.win.Show()
		st.settings.win.Focus()
		return
	}
	s := &settingsWindow{section: settingsSectionGeneral, pluginsOpen: true}
	w := mygo.NewWindow(mygo.WindowOptions{
		Title:   i18n.L("Voke 设置", "Voke Settings"),
		Width:   880,
		Height:  600,
		Hidden:  true, // 先建后 Show：避开空白首帧
		Content: ui.View(st.settingsView),
	})
	w.OnClose(func(e *mygo.CloseEvent) {
		if st.quitting {
			// 退出流程：放行真关闭（否则 veto 会让 App.Quit 的关窗循环中止）。
			// 录制定时器仍要停掉并置 nil，且先清 recording，避免已在途的回调
			// 在退出过程中再执行一次取消/注册热键。
			s.recording = false
			stopRecTimer(s)
			return
		}
		e.PreventDefault()
		st.closeSettings(s)
	})
	// 录制态的两道保险：聚焦续期（短 TTL + 聚焦续期，防「开了录制去干别的」），
	// 失焦即取消（切到别的应用 / 组合被系统或他人截走时不再干等）。
	w.OnFocus(func() {
		if s.recording {
			st.armHotkeyRecTimeout(s)
		}
	})
	w.OnBlur(func() {
		if s.recording {
			st.cancelHotkeyRecording(s)
		}
	})
	s.win = w
	st.settings = s
	st.seedSettings(s)
	w.Show()
	w.Focus()
	log.Printf("设置窗口已打开")
}

// closeSettings 结束设置窗：放弃录制、停掉录制定时器，然后**销毁窗口**而不是隐藏。
// Destroy 经 Window.Update 投递到主循环下一轮执行——「完成」按钮是在设置窗 view 的
// 渲染回调里调进来的，立即销毁会在回调栈内释放原生窗口；投递后当前帧先正常收尾。
func (st *appState) closeSettings(s *settingsWindow) {
	if s == nil || s.closed {
		return
	}
	s.closed = true
	if s.recording {
		st.cancelHotkeyRecording(s) // 关窗=放弃录制，恢复旧键（必须赶在销毁前）
	}
	stopRecTimer(s)
	if st.settings == s {
		st.settings = nil // 立即放掉 appState 对旧窗的引用，销毁回调不再持有它
	}
	s.win.Hide()
	s.win.Update(func() {
		s.win.Destroy()
		log.Printf("设置窗口 -> 程序化销毁（释放渲染资源）")
	})
}

// startHotkeyRecording 进入录制态：**先挂起所有全局热键**（主热键 + 插件热键）——
// 否则按下旧组合会被系统当成既有热键触发，轮不到我们捕获。pluginID 为空 = 录制
// 主唤隐热键；否则是目标插件 id。
func (st *appState) startHotkeyRecording(s *settingsWindow, pluginID string) {
	if s.recording {
		return
	}
	s.recording = true
	s.recPluginID = pluginID
	s.hotkeyErr = ""
	st.suspendHotkeys()
	st.armHotkeyRecTimeout(s)
}

// armHotkeyRecTimeout 为录制设/续一个短超时。定时器在后台触发，但取消动作经
// `Window.Update` 回主线程执行——录制态字段只在主线程读写，避免数据竞争；世代号
// 让旧定时器晚到时自动失效。
func (st *appState) armHotkeyRecTimeout(s *settingsWindow) {
	if s.recTimer != nil {
		s.recTimer.Stop() // 续期：先停旧的，避免定时器堆积
	}
	s.recGen++
	gen := s.recGen
	s.recTimer = time.AfterFunc(hotkeyRecTimeout, func() {
		s.win.Update(func() {
			if s.recording && s.recGen == gen {
				s.recTimer = nil
				st.cancelHotkeyRecording(s)
			}
		})
	})
}

// stopRecTimer 停掉当前录制超时（取消 / 成功 / 关窗时调），保证定时器被回收、
// 不会在录制结束后再投递一次取消。
func stopRecTimer(s *settingsWindow) {
	if s.recTimer != nil {
		s.recTimer.Stop()
		s.recTimer = nil
	}
}

// cancelHotkeyRecording 退出录制并恢复全部热键（Esc 或关窗/完成时调）。
func (st *appState) cancelHotkeyRecording(s *settingsWindow) {
	if !s.recording {
		return
	}
	s.recording = false
	s.recPluginID = ""
	s.hotkeyErr = ""
	stopRecTimer(s)
	st.resumeHotkeys()
}

// recordHotkeyInput 是录制框的 HandleInput 回调。不能用 OnTextKey：mygo 的
// editor 把 Alt+字母 / 符号当作“会输入文本”的键跳过 OnTextKey，导致这些组合
// 根本录不到（Alt+Space、Alt+` 等都会漏）。HandleInput 在文本处理之前拿到所有
// KeyDown，只消费按下、忽略自动重复与抬起。
//
// mygo v0.8 起单独按下的修饰键（Ctrl/Shift/Alt/Win）、CapsLock 等也会进
// HandleInput，且 Key 为 KeyUnknown（只有物理位置 Code 有名字）。这类键录不成
// 热键，直接放行：否则用户「先按 Ctrl 再按字母」的常规手势会被当成无效组合，
// 每按一次修饰键就弹一次「请按住 Ctrl / Alt / Shift」的错误。
func (st *appState) recordHotkeyInput(s *settingsWindow, ev ui.InputEvent) bool {
	if ev.Kind != ui.InputKeyDown || ev.Repeat {
		return false
	}
	if ev.Key == ui.KeyUnknown {
		return false
	}
	return st.recordHotkey(s, ev.Key, ev.Mods)
}

// recordHotkey 在录制态处理一次按键：Esc 取消；合法组合则先查冲突与系统占用，
// 通过后落盘并退出录制；无修饰键/不支持的键提示重按。
func (st *appState) recordHotkey(s *settingsWindow, key ui.Key, mods ui.Modifiers) bool {
	if !s.recording {
		return false
	}
	if key == ui.KeyEscape && mods == 0 {
		st.cancelHotkeyRecording(s)
		return true
	}
	acc, ok := platform.AccelFromKey(key, mods)
	if !ok {
		s.hotkeyErr = i18n.L("请按住 Ctrl / Alt / Shift 再按一个键", "Hold Ctrl / Alt / Shift and press another key")
		st.armHotkeyRecTimeout(s) // 无效键也续期，给用户重试机会
		return true
	}

	// 先写进编辑态，用统一校验查“与主热键 / 其它启用插件”的冲突；失败回滚。
	target := s.recPluginID
	oldMain := s.hotkey
	var edit *providerEdit
	var oldPlugin string
	if target == "" {
		s.hotkey = acc
	} else {
		edit = findProviderEdit(s.edits, target)
		if edit == nil {
			st.cancelHotkeyRecording(s)
			return true
		}
		oldPlugin = edit.hotkey
		edit.hotkey = acc
	}
	revert := func() {
		if target == "" {
			s.hotkey = oldMain
		} else if edit != nil {
			edit.hotkey = oldPlugin
		}
	}
	if msg := hotkeySettingsError(s.edits, s.hotkey); msg != "" {
		revert()
		s.hotkeyErr = msg
		st.armHotkeyRecTimeout(s)
		return true
	}
	// 探测系统级占用：此刻所有热键都已挂起，注册成功即说明组合可用。
	if err := platform.RegisterHotkey(acc, func() {}); err != nil {
		log.Printf("voke 录制热键 %q 探测注册失败：%v", acc, err)
		revert()
		s.hotkeyErr = i18n.L("该快捷键已被系统或其他程序占用，请换一个", "This shortcut is already used by the system or another app; choose another")
		st.armHotkeyRecTimeout(s)
		return true
	}
	platform.UnregisterHotkey(acc)

	s.recording = false
	s.recPluginID = ""
	s.hotkeyErr = ""
	stopRecTimer(s)
	st.applySettings() // 落盘 + 重载（录制中插件热键同步会跳过）
	st.resumeHotkeys() // 结束后按新配置统一重建
	return true
}

// findProviderEdit 按插件 id 取设置页里的可变编辑项（找不到返回 nil）。
func findProviderEdit(edits []providerEdit, id string) *providerEdit {
	for i := range edits {
		if edits[i].id == id {
			return &edits[i]
		}
	}
	return nil
}

// settingsView 是设置窗整帧视图。所有控件**即时生效**（无保存按钮）：开关/选择器改动
// 当帧落盘应用，文本框改动即应用；底部只留「完成」关窗。
//
// 布局：顶部标题栏 + 「左侧导航 / 右侧内容」两栏 + 底部操作栏。右侧按导航切换组别，
// 插件页继续用卡片列出每个插件，避免原先一长条 Fieldset 堆叠的模板感。
func (st *appState) settingsView(c *ui.Context) {
	s := st.settings
	if s == nil {
		return
	}
	t := c.Theme()
	c.Root().Background(t.Background)

	ui.Column(c).Fill().Children(func() {
		// 标题栏：左标题 + 副标题，右侧留白，让内容成为视觉主体。
		ui.Row(c).Padding(28, 22).AlignItems(ui.Center).Children(func() {
			ui.Column(c).Gap(3).Grow(1).Children(func() {
				ui.Text(c, i18n.L("设置", "Settings")).FontSize(20).Bold().TextColor(t.Text)
				ui.Text(c, i18n.L("修改即时生效，无需手动保存", "Changes apply immediately—no save button")).
					FontSize(theme.HintSize).TextColor(t.TextMuted)
			})
		})
		ui.Divider(c)

		ui.Row(c).Grow(1).Children(func() {
			if st.settingsSidebar(c, s) {
				// 点插件条目时记下目标：插件总览页用 ListState.ScrollTo 把它对到顶部。
				if id, ok := pluginIDFromNav(s.section); ok {
					s.scrollPlugin = id
				} else {
					s.scrollPlugin = ""
				}
			}
			ui.Box(c).Width(1).FillHeight().Background(t.Border)
			ui.Column(c).Grow(1).FillHeight().Children(func() {
				if _, ok := pluginIDFromNav(s.section); ok {
					st.settingsPlugins(c, s)
					return
				}
				ui.Scroll(c).Key(s.section).Grow(1).FillHeight().Padding(28, 20).Children(func() {
					ui.Column(c).Gap(18).Children(func() {
						switch {
						case s.section == settingsSectionShortcuts:
							st.settingsShortcuts(c, s)
						case s.section == settingsSectionAbout:
							st.settingsAbout(c)
						default:
							st.settingsGeneral(c, s)
						}
					})
				})
			})
		})

		ui.Divider(c)
		ui.Row(c).Padding(20, 14).Gap(12).AlignItems(ui.Center).Children(func() {
			ui.Spacer(c)
			if ui.PrimaryButton(c, i18n.L("完成", "Done")).Clicked() {
				st.closeSettings(s)
			}
		})
	})
}

// settingsSidebar 画左侧导航：通用 / 快捷键 / 可折叠的插件列表 / 关于，
// 返回本次是否切换了选中项。插件分组只有逐插件条目；点哪个插件，右侧总览就
// 滚到哪个插件卡片（不另设「全部插件」入口）。
func (st *appState) settingsSidebar(c *ui.Context, s *settingsWindow) bool {
	t := c.Theme()
	changed := false
	ui.Column(c).Width(188).FillHeight().Shrink(0).Padding(10, 10).
		Background(t.Surface.Alpha(0.55)).Children(func() {
		sidebar := ui.Sidebar(c, &s.section, func() {
			ui.SidebarSection(c, i18n.L("偏好设置", "Preferences"), nil, func() {
				ui.SidebarItem(c, settingsSectionGeneral, settingsIcon("general"), i18n.L("通用", "General"))
				ui.SidebarItem(c, settingsSectionShortcuts, settingsIcon("shortcuts"), i18n.L("快捷键", "Shortcuts"))
			})
			// 插件作为可折叠一级：默认展开，逐插件直达。
			ui.SidebarSection(c, i18n.L("插件", "Plugins"), &s.pluginsOpen, func() {
				for i := range s.edits {
					p := &s.edits[i]
					ui.SidebarItem(c, pluginNavID(p.id), appui.PluginIconSVG(p.id), p.name).
						PaddingStart(18)
				}
			})
			ui.SidebarSection(c, i18n.L("应用", "Application"), nil, func() {
				ui.SidebarItem(c, settingsSectionAbout, settingsIcon("about"), i18n.L("关于", "About"))
			})
		}).Grow(1)
		changed = sidebar.Changed()
	})
	return changed
}

// settingsGeneral 画「通用」页：启动、语言、外观。
func (st *appState) settingsGeneral(c *ui.Context, s *settingsWindow) {
	settingsPageHeader(c,
		i18n.L("通用", "General"),
		i18n.L("启动、语言与外观。", "Startup, language, and appearance."))

	settingsCard(c, i18n.L("启动", "Startup"), "", func() {
		settingsRow(c, i18n.L("开机自启", "Launch at login"),
			i18n.L("登录系统后自动启动 Voke", "Start Voke when you sign in"), func() {
				ui.Spacer(c)
				if ui.Switch(c, &s.autostart).Changed() {
					st.applySettings()
				}
			})
	})

	settingsCard(c, i18n.L("语言", "Language"),
		i18n.L("默认跟随系统；切换后界面立即更新。", "Follows the system by default; the UI updates immediately."), func() {
			settingsRow(c, i18n.L("界面语言", "Language"), "", func() {
				if ui.Select(c, &s.language, languageLabels()).Grow(1).Changed() {
					st.applySettings()
				}
			})
		})

	settingsCard(c, i18n.L("外观", "Appearance"), "", func() {
		settingsRow(c, i18n.L("界面主题", "Theme"),
			i18n.L("深色 / 浅色或跟随系统。", "Dark, light, or follow the system."), func() {
				if ui.Select(c, &s.theme, themeLabels()).Grow(1).Changed() {
					st.applySettings()
				}
			})
	})
}

// settingsShortcuts 画「快捷键」页：全局唤隐热键。
func (st *appState) settingsShortcuts(c *ui.Context, s *settingsWindow) {
	t := c.Theme()
	settingsPageHeader(c,
		i18n.L("快捷键", "Shortcuts"),
		i18n.L("全局唤隐与插件快捷键。", "Global show/hide and per-plugin shortcuts."))

	settingsCard(c, i18n.L("全局热键", "Global hotkey"),
		i18n.L("随时显示或隐藏 Voke 面板。", "Show or hide the Voke panel at any time."), func() {
			if s.recording && s.recPluginID == "" {
				ui.Text(c, i18n.L("请按下新的快捷键（Esc 取消）", "Press the new shortcut (Esc to cancel)")).
					FontSize(theme.HintSize).TextColor(t.TextMuted)
				ui.TextInputBase(c, &s.recBuf).Bind(&s.recHandle).ReadOnly(true).Grow(1).
					FontSize(theme.InputSize).
					Placeholder(i18n.L("等待按键…", "Waiting for keys…")).
					Label(i18n.L("录制热键", "Record hotkey")).
					HandleInput(func(ev ui.InputEvent) bool { return st.recordHotkeyInput(s, ev) })
				s.recHandle.Focus() // 每帧回焦点（点击会先落到按钮，下一帧拉回）
			} else {
				settingsRow(c, i18n.L("显示 / 隐藏面板", "Show / hide panel"), "", func() {
					// 设置页保持规范英文键名（Alt+Space / Ctrl+Backspace），
					// 中文缩写只用于启动器底栏键帽。
					btn := ui.Button(c, s.hotkey)
					btn.Disabled(s.recording)
					if btn.Clicked() {
						st.startHotkeyRecording(s, "")
					}
					ui.Text(c, i18n.L("点击按键块后按下新组合，Esc 取消", "Click the key block, then press a new combination; Esc cancels")).
						FontSize(theme.HintSize).TextColor(t.TextMuted).Grow(1)
				})
			}
			if s.hotkeyErr != "" && s.recPluginID == "" {
				ui.Text(c, s.hotkeyErr).FontSize(theme.HintSize).TextColor(t.Danger)
			}
		})

	settingsCard(c, i18n.L("插件快捷键", "Plugin shortcuts"),
		i18n.L("在「插件」页为单个插件录制；只有已启用的插件才会注册。",
			"Record shortcuts per plugin on the Plugins page; only enabled plugins register them."), func() {
			ui.Text(c, i18n.L("插件快捷键用于直达该插件，不会改变主热键的显隐行为。",
				"Plugin shortcuts jump straight to that plugin and do not change the main show/hide hotkey.")).
				FontSize(theme.HintSize).TextColor(t.TextMuted)
		})
}

// settingsPlugins 画插件总览页：页标题 + 可滚动、可虚拟化的插件卡片列表。
// 点侧栏插件条目时用 ListState.ScrollTo(…, Start) 把目标卡片对到顶部；列表本身
// 仍可继续滚动，查看其它插件。
func (st *appState) settingsPlugins(c *ui.Context, s *settingsWindow) {
	ui.Column(c).Fill().Padding(28, 20).Children(func() {
		settingsPageHeader(c,
			i18n.L("插件", "Plugins"),
			i18n.L("启用插件并调整搜索行为。", "Enable plugins and tune how they appear in search."))
		if s.configErr != "" {
			ui.Text(c, s.configErr).FontSize(theme.HintSize).TextColor(c.Theme().Danger)
		}
		s.pluginList.Key = func(i int) any { return s.edits[i].id }
		s.pluginList.Label = func(i int) string { return s.edits[i].name }
		if s.scrollPlugin != "" {
			if idx := providerEditIndex(s.edits, s.scrollPlugin); idx >= 0 {
				s.pluginList.ScrollTo(idx, ui.Start)
			}
			s.scrollPlugin = ""
		}
		ui.List(c, &s.pluginList, len(s.edits), func(i int) {
			ui.Column(c).Padding(0, 0, 18, 0).Children(func() {
				st.settingsProviderCard(c, s, &s.edits[i])
			})
		}).Grow(1)
	})
}

// providerEditIndex 按插件 id 找它在设置列表里的下标（找不到返回 -1）。
func providerEditIndex(edits []providerEdit, id string) int {
	for i := range edits {
		if edits[i].id == id {
			return i
		}
	}
	return -1
}

// settingsProviderCard 画单个插件卡片。
func (st *appState) settingsProviderCard(c *ui.Context, s *settingsWindow, p *providerEdit) {
	t := c.Theme()
	ui.Column(c).Gap(14).Padding(18).Radius(10).
		Background(t.Surface).Border(1, t.Border.Alpha(0.65)).Children(func() {
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, appui.PluginIconSVG(p.id)).Size(16, 16).TextColor(t.TextMuted)
			ui.Column(c).Gap(2).Grow(1).Children(func() {
				ui.Text(c, p.name).FontSize(theme.TitleSize + 1).Bold().TextColor(t.Text)
				ui.Text(c, p.description).FontSize(theme.HintSize).TextColor(t.TextMuted).SingleLine()
			})
			if ui.Switch(c, &p.enabled).Changed() {
				st.applySettings()
			}
		})
		if !p.enabled {
			ui.Text(c, i18n.L("插件已停用，以下配置不生效", "This plugin is disabled; the settings below do not apply")).
				FontSize(theme.HintSize).TextColor(t.TextMuted)
		}
		ui.Column(c).Gap(12).Disabled(!p.enabled).Children(func() {
			settingsRow(c, i18n.L("参与全局搜索", "Include in global search"), "", func() {
				ui.Spacer(c)
				if ui.Switch(c, &p.isGlobal).Changed() {
					st.applySettings()
				}
			})
			settingsRow(c, i18n.L("前缀", "Prefix"),
				i18n.L("如 @b；留空表示无前缀", "e.g. @b; empty means no prefix"), func() {
					if ui.TextInput(c, &p.prefix).Grow(1).FontSize(theme.InputSize).
						Placeholder(i18n.L("如 @b；空=无", "e.g. @b; empty = none")).
						Label(p.name + i18n.L(" 前缀", " prefix")).Changed() {
						st.applySettings()
					}
				})
			settingsRow(c, i18n.L("分组顺序 / 激活", "Group order / activation"),
				i18n.L("0–10，越大越靠前", "0–10, higher comes first"), func() {
					if ui.NumberInput(c, &p.weight, 0, 10, 0.1).Changed() {
						st.applySettings()
					}
					if ui.Select(c, &p.activation, activationLabels()).Changed() {
						st.applySettings()
					}
				})
			if p.id != engine.PickerPluginID {
				settingsRow(c, i18n.L("全局快捷键", "Global shortcut"), "", func() {
					if s.recording && s.recPluginID == p.id {
						ui.TextInputBase(c, &s.recBuf).Bind(&s.recHandle).ReadOnly(true).Grow(1).
							FontSize(theme.InputSize).
							Placeholder(i18n.L("等待按键…", "Waiting for keys…")).
							Label(p.name + i18n.L(" 全局快捷键", " global shortcut")).
							HandleInput(func(ev ui.InputEvent) bool { return st.recordHotkeyInput(s, ev) })
						s.recHandle.Focus()
					} else {
						label := p.hotkey
						if label == "" {
							label = i18n.L("未设置", "Not set")
						}
						btn := ui.Button(c, label)
						btn.Disabled(s.recording)
						if btn.Clicked() {
							st.startHotkeyRecording(s, p.id)
						}
						if p.hotkey != "" {
							if ui.Button(c, i18n.L("清除", "Clear")).Clicked() {
								p.hotkey = ""
								s.hotkeyErr = ""
								st.applySettings()
							}
						}
						ui.Text(c, i18n.L("点击后按组合键；仅启用时注册", "Click and press a combination; registered only while enabled")).
							FontSize(theme.HintSize).TextColor(t.TextMuted).Grow(1)
					}
				})
				if s.recording && s.recPluginID == p.id && s.hotkeyErr != "" {
					ui.Text(c, s.hotkeyErr).FontSize(theme.HintSize).TextColor(t.Danger)
				}
				if msg := st.pluginHotkeyError(p.id); msg != "" &&
					!(s.recording && s.recPluginID == p.id) {
					ui.Text(c, msg).FontSize(theme.HintSize).TextColor(t.Danger)
				}
			}
			if p.id == providerClipboardID {
				settingsRow(c, i18n.L("历史条数", "History size"),
					i18n.L("普通条目上限；置顶 10 条不计入", "Limit for regular entries; up to 10 pinned items are excluded"), func() {
						if ui.NumberInput(c, &s.clipboardMax, 1, 1000, 1).Changed() {
							st.applySettings()
						}
					})
			}
			if p.id == random.ID {
				settingsRow(c, i18n.L("默认长度", "Default length"),
					i18n.L("字符集未单独设置长度时使用（1–256）", "Used when a character set does not set its own length (1–256)"), func() {
						if ui.NumberInput(c, &p.defaultLength, charset.MinLength, charset.MaxLength, 1).Changed() {
							st.applySettings()
						}
					})
				st.settingsCharsets(c, p)
			}
		})
	})
}

// settingsCharsets 画随机值插件的字符集编辑器：每行「启用 / 名称 / 字符类正则 /
// 删除」，底部可新增。名称区分大小写且必须唯一；正则语义见 internal/logic/charset。
func (st *appState) settingsCharsets(c *ui.Context, p *providerEdit) {
	t := c.Theme()
	ui.Column(c).Gap(8).Children(func() {
		ui.Text(c, i18n.L("字符集", "Character sets")).FontSize(theme.TitleSize).Bold().TextColor(t.Text)
		ui.Text(c, i18n.L("左侧开关控制是否出现在一级列表；长度 0 = 跟随默认长度；名称唯一（区分大小写）；正则为字符类内容，如 a-zA-Z0-9、a-f0-9、!@#$%",
			"The switch controls whether an entry appears in the first level; length 0 follows the default length; names are unique (case-sensitive); patterns are character classes such as a-zA-Z0-9, a-f0-9, !@#$%")).
			FontSize(theme.HintSize).TextColor(t.TextMuted)

		deleteAt := -1
		for i := range p.charsets {
			cs := &p.charsets[i]
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				if ui.Switch(c, &cs.enabled).Changed() {
					st.applySettings()
				}
				if ui.TextInput(c, &cs.name).Grow(1).FontSize(theme.InputSize).
					Placeholder(i18n.L("名称", "Name")).
					Label(i18n.L("字符集名称", "Character-set name")).Changed() {
					st.applySettings()
				}
				if ui.TextInput(c, &cs.pattern).Grow(2).FontSize(theme.InputSize).
					Placeholder(i18n.L("正则，如 a-zA-Z0-9", "Pattern, e.g. a-zA-Z0-9")).
					Label(i18n.L("字符集正则", "Character-set pattern")).Changed() {
					st.applySettings()
				}
				if ui.NumberInput(c, &cs.length, 0, charset.MaxLength, 1).Width(78).
					Label(i18n.L("字符集长度", "Character-set length")).Changed() {
					st.applySettings()
				}
				if len(p.charsets) > 1 {
					if ui.Button(c, i18n.L("删除", "Delete")).Clicked() {
						deleteAt = i
					}
				}
			})
		}
		if deleteAt >= 0 {
			p.charsets = append(p.charsets[:deleteAt], p.charsets[deleteAt+1:]...)
			st.applySettings()
		}
		if ui.Button(c, i18n.L("添加字符集", "Add character set")).Clicked() {
			p.charsets = append(p.charsets, charsetEdit{
				name: nextCharsetName(p.charsets), pattern: "a-zA-Z0-9", enabled: true,
			})
			st.applySettings()
		}
	})
}

// nextCharsetName 生成一个不与现有条目重复的新字符集名称。
func nextCharsetName(list []charsetEdit) string {
	used := make(map[string]bool, len(list))
	for _, cs := range list {
		used[strings.TrimSpace(cs.name)] = true
	}
	for i := 1; ; i++ {
		name := fmt.Sprintf(i18n.L("自定义 %d", "Custom %d"), i)
		if !used[name] {
			return name
		}
	}
}

// settingsAbout 画「关于」页。
func (st *appState) settingsAbout(c *ui.Context) {
	settingsPageHeader(c, i18n.L("关于", "About"),
		i18n.L("版本与配置位置。", "Version and where the configuration lives."))
	settingsCard(c, "Voke", "", func() {
		ui.Column(c).Gap(12).Children(func() {
			settingsInfoRow(c, i18n.L("版本", "Version"), aboutVersion())
			settingsPathRow(c, i18n.L("配置文件", "Config file"), aboutConfigPath())
			settingsLinkRow(c, i18n.L("项目地址", "Project"), projectURL)
		})
	})
}

// settingsPageHeader 是右侧内容区的页标题。
func settingsPageHeader(c *ui.Context, title, desc string) {
	t := c.Theme()
	ui.Column(c).Gap(4).Padding(2, 0).Children(func() {
		ui.Text(c, title).FontSize(18).Bold().TextColor(t.Text)
		if desc != "" {
			ui.Text(c, desc).FontSize(theme.DetailSize).TextColor(t.TextMuted)
		}
	})
}

// settingsCard 是内容区的一张分组卡片（标题 + 可选说明 + 表单）。
func settingsCard(c *ui.Context, title, desc string, fn func()) {
	t := c.Theme()
	ui.Column(c).Gap(14).Padding(18).Radius(10).
		Background(t.Surface).Border(1, t.Border.Alpha(0.65)).Children(func() {
		if title != "" || desc != "" {
			ui.Column(c).Gap(3).Children(func() {
				if title != "" {
					ui.Text(c, title).FontSize(theme.TitleSize + 1).Bold().TextColor(t.Text)
				}
				if desc != "" {
					ui.Text(c, desc).FontSize(theme.HintSize).TextColor(t.TextMuted)
				}
			})
		}
		ui.Column(c).Gap(14).Children(fn)
	})
}

// settingsRow 是「左侧标签 + 右侧控件」的一行；标签下可带一行弱提示。
func settingsRow(c *ui.Context, label, hint string, control func()) {
	t := c.Theme()
	ui.Row(c).Gap(16).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Width(150).Gap(2).Shrink(0).Children(func() {
			ui.Text(c, label).FontSize(theme.DetailSize + 1).TextColor(t.Text)
			if hint != "" {
				ui.Text(c, hint).FontSize(theme.HintSize).TextColor(t.TextMuted)
			}
		})
		ui.Row(c).Grow(1).Gap(10).AlignItems(ui.Center).Children(control)
	})
}

// settingsInfoRow 是只读信息行（版本）。字号比早期口径大一档，保证「关于」面板
// 里的值在默认 DPI 下也清楚可读。
func settingsInfoRow(c *ui.Context, label, value string) {
	t := c.Theme()
	ui.Row(c).Gap(16).AlignItems(ui.Start).Children(func() {
		ui.Text(c, label).Width(100).Shrink(0).FontSize(theme.DetailSize + 1).TextColor(t.TextMuted)
		ui.Text(c, value).Grow(1).FontSize(theme.TitleSize).TextColor(t.Text)
	})
}

// settingsPathRow 是配置文件路径行：路径本体更大，右侧带文件夹按钮，点击用系统
// 默认文件管理器打开所在目录。路径无效（未落盘）时不显示按钮。
func settingsPathRow(c *ui.Context, label, path string) {
	t := c.Theme()
	ui.Row(c).Gap(16).AlignItems(ui.Center).Children(func() {
		ui.Text(c, label).Width(100).Shrink(0).FontSize(theme.DetailSize + 1).TextColor(t.TextMuted)
		ui.Text(c, path).Shrink(1).SingleLine().FontSize(theme.TitleSize).TextColor(t.Text)
		if filepath.IsAbs(path) {
			btn := ui.ButtonBase(c).Padding(4).Radius(6).
				Label(i18n.L("打开配置目录", "Open config folder"))
			btn.Children(func() {
				ui.Icon(c, settingsIcon("folder")).Size(18, 18).TextColor(t.TextMuted)
			})
			if btn.Clicked() {
				if err := platform.OpenDirectory(filepath.Dir(path)); err != nil {
					log.Printf("voke 打开配置目录 %s 失败：%v", filepath.Dir(path), err)
				}
			}
		}
		ui.Spacer(c)
	})
}

// settingsLinkRow 是带可点击链接的信息行（项目地址用；点击或回车打开浏览器）。
func settingsLinkRow(c *ui.Context, label, url string) {
	t := c.Theme()
	ui.Row(c).Gap(16).AlignItems(ui.Start).Children(func() {
		ui.Text(c, label).Width(100).Shrink(0).FontSize(theme.DetailSize + 1).TextColor(t.TextMuted)
		ui.Link(c, url, url).Grow(1).FontSize(theme.TitleSize)
	})
}

var (
	settingsSVGOnce sync.Once
	settingsSVGs    map[string]*ui.SVG
)

// settingsIcon 返回左侧导航用的 Lucide 图标（首次用到时解析并缓存）。
func settingsIcon(key string) *ui.SVG {
	settingsSVGOnce.Do(func() {
		settingsSVGs = map[string]*ui.SVG{
			"general":   ui.MustParseSVG(lucideSettings),
			"shortcuts": ui.MustParseSVG(lucideKeyboard),
			"about":     ui.MustParseSVG(lucideInfo),
			"folder":    ui.MustParseSVG(lucideFolder),
		}
	})
	if s, ok := settingsSVGs[key]; ok {
		return s
	}
	return settingsSVGs["general"]
}

// Lucide v0.544.0（ISC）：settings / keyboard / info / folder。
var (
	lucideSettings = []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2z"/><circle cx="12" cy="12" r="3"/></svg>`)
	lucideKeyboard = []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M10 8h.01"/><path d="M12 12h.01"/><path d="M14 8h.01"/><path d="M16 12h.01"/><path d="M18 8h.01"/><path d="M6 8h.01"/><path d="M7 16h10"/><path d="M8 12h.01"/><rect width="20" height="16" x="2" y="4" rx="2"/></svg>`)
	lucideInfo     = []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><path d="M12 16v-4"/><path d="M12 8h.01"/></svg>`)
	lucideFolder   = []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M20 20a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7.9a2 2 0 0 1-1.69-.9L9.6 3.9A2 2 0 0 0 7.93 3H4a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2Z"/></svg>`)
)

// applySettings 即时落盘 + 应用（实时生效，无“保存”按钮）：
//   - 热键不可解析则沿用当前生效值、不写垃圾；开机自启成功才落配置、失败回弹；
//   - 插件启用 / 前缀 / 权重 / 激活方式经 saveConfig + reloadConfigs 近即时生效；
//   - 主题当场生效；热键变更则撤旧注新、失败回滚。
//
// 幂等：任一项变化后调用一次即可；不关窗（窗的显隐归按钮 / OnClose）。
func (st *appState) applySettings() {
	s := st.settings
	if s == nil {
		return
	}
	overrides := providerOverrides(s.edits)
	if msg := charsetSettingsError(s.edits); msg != "" {
		s.configErr = msg
		return
	}
	if msg := prefixSettingsError(s.edits, overrides); msg != "" {
		// 前缀无效时不落盘、不重载：界面与生效配置都停在上一次有效状态，
		// 错误提示就地显示（见 settingsView 的「插件」区）。
		s.configErr = msg
		return
	}
	if msg := hotkeySettingsError(s.edits, s.hotkey); msg != "" {
		// 与主热键 / 其它启用插件撞键：同样不落盘，避免“保存成功但没注册”。
		s.configErr = msg
		return
	}
	s.configErr = ""
	hotkey := s.hotkey
	if _, ok := config.ParseHotkey(hotkey); !ok {
		fallback := st.cfg.Hotkey
		if fallback == "" {
			fallback = hotkeyShow
		}
		log.Printf("voke 设置热键 %q 不可解析，沿用 %s", hotkey, fallback)
		hotkey = fallback
	}

	// 开机自启：注册表改成功才把新值落进配置，失败则配置与界面都回弹（对照 voke-app
	// set_autostart「注册表改成功才落配置，失败就让它回弹」：先 set_enabled、成功后才
	// 写 config）。安全软件拦 HKCU\Run 写入时，不能谎报 autostart 已按用户所 toggling——
	// 否则下次启动 panel.go ready 会据 config 再试一次、UI 一直显示开着却实际没注册。
	// 非 Windows 走 stub 恒返回 nil，autostart 保持 s.autostart，行为与改动前一致。
	autostart := s.autostart
	if err := platform.SetAutostart(autostart); err != nil {
		log.Printf("voke 设置开机自启（=%v）失败，配置与界面回弹：%v", autostart, err)
		autostart = st.cfg.Autostart
		s.autostart = autostart
	}

	clipMax := int(s.clipboardMax)
	if clipMax < cliphist.MinMaxEntries {
		clipMax = cliphist.MinMaxEntries
	}
	if clipMax > cliphist.MaxMaxEntries {
		clipMax = cliphist.MaxMaxEntries
	}
	langPref := languageCode(s.language)
	top := appcfg.Top{
		Hotkey: hotkey, Autostart: autostart, Theme: themeCode(s.theme),
		Language: langPref, ClipboardMaxEntries: clipMax,
	}

	if err := saveConfig(top, overrides); err != nil {
		log.Printf("voke 写设置失败：%v", err)
		// 落盘失败：把语言选择回弹到当前配置，避免界面显示与实际不一致。
		s.language = languageLabel(st.cfg.Language)
		return
	}
	// 语言必须先于 reloadConfigs 生效：Descriptor 的名称 / 描述在装配时取当前语言。
	langChanged := st.applyLanguage(langPref)
	// 历史上限即时生效：超出新上限的普通条目当场淘汰（置顶不计入）。
	if st.hist != nil {
		st.hist.SetMaxEntries(clipMax)
		if st.isClipboardPlugin() {
			st.refreshClipboardRows()
		}
	}
	// 重载：插件启用 / 前缀 / 语言近即时生效，并把新 loaded 回存到 st.cfg（下次 seed 一致）。
	st.reloadConfigs()
	// 主题当场生效：appcfg 码值 = mygo ThemeSource，透传即可（见 platform.SetThemeSource）。
	platform.SetThemeSource(string(st.cfg.Theme))
	// 改键当场生效：先撤旧串、再注新串；注新失败则回滚旧串（对照生产「注册失败回退」，
	// 绝不留「新旧都没绑」的空窗）。撤/注都只在主线程（本函数由托盘 Click→saveSettings 走到）。
	st.rebindMainHotkey(st.cfg.Hotkey)
	// 插件启用 / 停用 / 改键后，同步实际的插件热键注册表。录制期间会跳过（热键已挂起），
	// 录制结束后由 resumeHotkeys 统一重建。
	st.syncPluginHotkeys()

	if langChanged {
		// 已生成的文案需要跟着换：窗口标题、托盘菜单、设置页标签与插件名/描述。
		if s.win != nil {
			s.win.SetTitle(i18n.L("Voke 设置", "Voke Settings"))
		}
		st.refreshTrayMenu()
		// 重刷绑定态：theme / language 存的是本地化标签，插件名 / 描述也随语言变化。
		st.seedSettings(s)
	}
	s.hotkey = st.showHotkey // 回显实际生效的键（改键失败时即旧键）
}

// providerOverrides 把设置页的绑定态包成 UserOverride。除 hotkey 外所有字段都写成
// 指针，保持「打开后不改、直接触发一次保存」也是幂等写回。
//
// hotkey 的省略规则：没有默认热键的插件，空 = 未绑定，省略该键与写空串等价，保持文件
// 干净；**有**默认热键的插件（如剪贴板的 Alt+`）必须显式写空串，否则「清除」会回落
// 到 Descriptor 默认值、清不掉。
func providerOverrides(edits []providerEdit) []config.UserOverride {
	overrides := make([]config.UserOverride, 0, len(edits))
	for _, p := range edits {
		enabled, isGlobal, weight, prefix := p.enabled, p.isGlobal, p.weight, p.prefix
		activation := activationCode(p.activation)
		ov := config.UserOverride{
			ID: p.id, Enabled: &enabled, IsGlobal: &isGlobal,
			Weight: &weight, Prefix: &prefix, Activation: &activation,
		}
		if p.hotkey != "" || defaultHotkeyFor(p.id) != "" {
			hk := p.hotkey
			ov.Hotkey = &hk
		}
		if p.id == random.ID {
			length := int(p.defaultLength)
			ov.DefaultLength = &length
			list := make([]charset.Spec, 0, len(p.charsets))
			for _, cs := range p.charsets {
				list = append(list, charset.Spec{
					Name: strings.TrimSpace(cs.name), Pattern: strings.TrimSpace(cs.pattern),
					Length: int(cs.length), Enabled: cs.enabled,
				})
			}
			ov.Charsets = list
		}
		overrides = append(overrides, ov)
	}
	return overrides
}

// defaultHotkeyFor 返回某插件在 Descriptor 里的默认热键（没有则空串）。
func defaultHotkeyFor(id string) string {
	for _, d := range builtinDescriptors() {
		if d.ID == id {
			return d.DefaultHotkey
		}
	}
	return ""
}

// mainHotkeyOwner 是 owners 表里主唤隐热键的内部键（不参与展示，避免用本地化文案
// 做比较——切到英文后 "显示/隐藏热键" 会对不上）。
const mainHotkeyOwner = "\x00main-hotkey"

// hotkeySettingsError 校验插件快捷键：只检查**启用**的插件（停用时不注册），
// 并同时与主唤隐热键比较。返回本地化错误（空串 = 通过）。
func hotkeySettingsError(edits []providerEdit, mainHotkey string) string {
	owners := make(map[string]string) // 规范化热键 → 占用者描述
	if mainHotkey != "" {
		owners[config.CanonicalHotkey(mainHotkey)] = mainHotkeyOwner
	}
	for _, p := range edits {
		if !p.enabled || p.hotkey == "" || p.id == engine.PickerPluginID {
			continue
		}
		if !config.HasModifier(p.hotkey) {
			return fmt.Sprintf(i18n.L("%s 的快捷键 %s 无效：需至少一个修饰键",
				"%s shortcut %s is invalid: at least one modifier is required"), p.name, p.hotkey)
		}
		key := config.CanonicalHotkey(p.hotkey)
		if owner, ok := owners[key]; ok {
			if owner == mainHotkeyOwner {
				return fmt.Sprintf(i18n.L("%s 的快捷键 %s 与显示/隐藏热键冲突",
					"%s shortcut %s conflicts with the show/hide hotkey"), p.name, p.hotkey)
			}
			return fmt.Sprintf(i18n.L("%s 的快捷键 %s 与%s冲突", "%s shortcut %s conflicts with %s"),
				p.name, p.hotkey, owner)
		}
		owners[key] = fmt.Sprintf(i18n.L("插件「%s」的快捷键", "plugin “%s” shortcut"), p.name)
	}
	return ""
}

// charsetSettingsError 校验随机值插件的字符集：至少保留一套；名称非空、不重复
// （区分大小写）；正则可编译且至少匹配一个可打印字符。空串 = 通过。
func charsetSettingsError(edits []providerEdit) string {
	for _, p := range edits {
		if p.id != random.ID {
			continue
		}
		if len(p.charsets) == 0 {
			return i18n.L("随机值至少需要保留一个字符集", "Random needs at least one character set")
		}
		if n := int(p.defaultLength); n < charset.MinLength || n > charset.MaxLength {
			return fmt.Sprintf(i18n.L("随机值的默认长度必须在 %d–%d 之间",
				"Random default length must be between %d and %d"), charset.MinLength, charset.MaxLength)
		}
		seen := make(map[string]bool, len(p.charsets))
		for i, cs := range p.charsets {
			spec := charset.Spec{
				Name: strings.TrimSpace(cs.name), Pattern: strings.TrimSpace(cs.pattern),
				Length: int(cs.length),
			}
			if err := charset.Validate(spec); err != nil {
				label := spec.Name
				if label == "" {
					label = fmt.Sprintf(i18n.L("第 %d 个", "#%d"), i+1)
				}
				return fmt.Sprintf(i18n.L("字符集「%s」：%v", "character set “%s”: %v"), label, err)
			}
			if seen[spec.Name] {
				return fmt.Sprintf(i18n.L("字符集名称「%s」重复（区分大小写）",
					"duplicate character-set name “%s” (case-sensitive)"), spec.Name)
			}
			seen[spec.Name] = true
		}
	}
	return ""
}

// prefixSettingsError 在落盘前校验插件前缀，返回中文错误（空串 = 通过）：
//   - 含空白：Normalize 的 C3 会静默置空，设置页必须当场拦住并说明原因；
//   - Normalize 的 C2 / C4：触发串互为前缀（含相同）或撞上保留键（字母 / 数字），
//     直接复用归一层的判定与消息，避免设置页与启动路径各有一套规则。
func prefixSettingsError(edits []providerEdit, overrides []config.UserOverride) string {
	for _, p := range edits {
		if p.prefix != "" && strings.IndexFunc(p.prefix, unicode.IsSpace) >= 0 {
			return fmt.Sprintf(i18n.L("%s 的前缀不能包含空白（间接模式由程序自动追加一个空格）",
				"%s prefix cannot contain whitespace (indirect mode appends a space automatically)"), p.name)
		}
	}
	if _, err := config.Normalize(builtinDescriptors(), overrides); err != nil {
		return err.Error()
	}
	return ""
}
