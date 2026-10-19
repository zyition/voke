// settings.go：独立设置窗口（§7.2 / 设计文档「设置页」：880×600 普通窗，带标题栏、
// 可缩放、不自动隐藏；面板完全不动）。
//
// 装顶层设置（**开机自启 / 界面主题 / 唤隐热键**）+ 每插件配置（设计文档 §385 的 Group：
// **启用 / 参与全局搜索 / 权重 0–3 / 激活别名(prefix)**，加 §33 的 Direct/Indirect 下拉）。
// 数据全走 appcfg 落盘 + platform 副作用，保存时尽量当场生效：
//   - 开机自启：`platform.SetAutostart`；
//   - 插件配置：写 config.json 的 enabled/is_global/weight/prefix/activation 覆盖 +
//     `reloadConfigs` 下发到 live 注册表（改坏的值由 Normalize C2/C3/C5/C6/C7 挡下并兜底）；
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
	"log"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/zyition/voke/poc/voke-go/internal/logic/appcfg"
	"github.com/zyition/voke/poc/voke-go/internal/logic/config"
	"github.com/zyition/voke/poc/voke-go/internal/logic/engine"
	"github.com/zyition/voke/poc/voke-go/internal/platform"
	"github.com/zyition/voke/poc/voke-go/internal/ui/theme"
)

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
	return "开发版"
}

// aboutConfigPath 是「关于」区显示的配置文件位置，对照生产 config_path_label：拿不到有效
// 落盘路径（非绝对 = DataDir 取不到且未设 env）时显示「未落盘…」而非半截相对路径。
func aboutConfigPath() string {
	p := configFilePath()
	if !filepath.IsAbs(p) {
		return "未落盘（取不到数据目录，且未设 VOKE_CONFIG）"
	}
	return p
}

// providerEdit 是「插件配置」区的一行（对照设计文档 §385 的每插件 Group：启用 /
// 参与全局搜索 / 权重 0–3 / 激活别名(prefix)，外加 §33 的 Direct/Indirect 下拉）。
// 各字段直接是绑定态（widget 就地改它），保存时逐个包成 *指针* 落 override。
// 值从 st.providers（归一后的生效态）seed，故「不改直接存」是幂等写回；改坏的值交给
// Normalize 的 C2/C3/C5/C6/C7 校验 + reloadConfigs 兜底。
type providerEdit struct {
	id         string
	name       string
	enabled    bool
	isGlobal   bool
	weight     float64
	prefix     string
	activation string // 显示值 "Direct" / "Indirect"，save 时小写落码
}

// settingsWindow 是设置窗的绑定态（只在主线程、只在窗口存活期读写）。theme 存的是
// Select 显示的中文标签，save 时经 themeCode 映射回 appcfg 的码值。
type settingsWindow struct {
	win       *mygo.Window
	hotkey    string
	autostart bool
	theme     string
	edits     []providerEdit
	// closed 防重复进入销毁流程：OnClose 与「完成」按钮可能先后触发；销毁本身
	// 经 Window.Update 延到当前帧之后执行。
	closed bool
	// 热键录制器状态：recording 时用一个只读输入框接管按键（每帧回焦点），捕获到组合
	// 即注册生效；Esc 取消并恢复旧键。recBuf 是只读框的绑定串（不承载实际内容）。
	recording bool
	hotkeyErr string
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

// activationLabels 是激活方式下拉的显示值——中文，与设置页其余文案统一（早先直出
// engine 的 "Direct"/"Indirect" 英文，风格突兀）。顺序与 direct/indirect 码一一对应。
var activationLabels = []string{"直接", "间接"}

// activationLabel 把 engine.Activation 映成下拉显示值（seed 用）。
func activationLabel(a engine.Activation) string {
	if a == engine.Indirect {
		return activationLabels[1]
	}
	return activationLabels[0]
}

// activationCode 把下拉显示值映回落码（"direct"/"indirect"，交 config 归一）。
func activationCode(label string) string {
	if label == activationLabels[1] {
		return "indirect"
	}
	return "direct"
}

// themeLabels 是 Select 选项（顺序与 themeCodes 一一对应）。
var themeLabels = []string{"跟随系统", "深色", "浅色"}

// themeCodes 是 themeLabels 对应的 appcfg 码值。
var themeCodes = []string{"system", "dark", "light"}

// themeLabel 把码值映成显示标签（未知 / 空 → 跟随系统）。
func themeLabel(code appcfg.Theme) string {
	for i, c := range themeCodes {
		if string(code) == c {
			return themeLabels[i]
		}
	}
	return themeLabels[0]
}

// themeCode 把显示标签映回码值（找不到 → system）。
func themeCode(label string) appcfg.Theme {
	for i, l := range themeLabels {
		if label == l {
			return appcfg.Theme(themeCodes[i])
		}
	}
	return appcfg.ThemeSystem
}

// seedSettings 用最近一次装配的 st.cfg（顶层）+ st.providers（插件启用态）把绑定态刷成
// 当前真值。每次 openSettings 都重刷，避免隐藏期残留上一次未保存的编辑。
func (st *appState) seedSettings(s *settingsWindow) {
	s.hotkey = st.cfg.Hotkey
	if s.hotkey == "" {
		s.hotkey = hotkeyShow
	}
	s.autostart = st.cfg.Autostart
	s.theme = themeLabel(st.cfg.Theme)
	s.edits = s.edits[:0]
	for _, p := range st.providers.Providers {
		s.edits = append(s.edits, providerEdit{
			id: p.ID, name: p.Name, enabled: p.Enabled, isGlobal: p.IsGlobal,
			weight: p.Weight, prefix: p.Prefix, activation: activationLabel(p.Activation),
		})
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
	s := &settingsWindow{}
	w := mygo.NewWindow(mygo.WindowOptions{
		Title:   "Voke 设置",
		Width:   880,
		Height:  600,
		Hidden:  true, // 先建后 Show：避开空白首帧
		Content: ui.View(st.settingsView),
	})
	w.OnClose(func(e *mygo.CloseEvent) {
		if st.quitting {
			return // 退出流程：放行真关闭（否则 veto 会让 App.Quit 的关窗循环中止）
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
	s.win.Hide()
	s.win.Update(func() {
		if st.settings == s {
			st.settings = nil
		}
		s.win.Destroy()
		log.Printf("设置窗口 -> 程序化销毁（释放渲染资源）")
	})
}

// startHotkeyRecording 进入录制态：**先注销当前全局热键**——否则按下旧组合会被系统
// 当成既有热键触发面板、轮不到我们捕获（这就是录制时的“锁定”问题）。
func (st *appState) startHotkeyRecording(s *settingsWindow) {
	if s.recording {
		return
	}
	s.recording = true
	s.hotkeyErr = ""
	platform.UnregisterHotkey(st.showHotkey)
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

// cancelHotkeyRecording 退出录制并恢复旧热键（Esc 或关窗/完成时调）。
func (st *appState) cancelHotkeyRecording(s *settingsWindow) {
	if !s.recording {
		return
	}
	s.recording = false
	stopRecTimer(s)
	if st.showHotkey != "" {
		if err := platform.RegisterHotkey(st.showHotkey, st.togglePanel); err != nil {
			log.Printf("voke 恢复旧热键 %q 失败：%v", st.showHotkey, err)
		}
	}
}

// recordHotkey 在录制态处理一次按键（输入框 OnTextKey 回调）：Esc 取消；合法组合则
// 落盘注册并退出录制；无修饰键/不支持的键提示重按。
func (st *appState) recordHotkey(s *settingsWindow, k ui.TextKey) bool {
	if !s.recording {
		return false
	}
	if k.Key == ui.KeyEscape && k.Mods == 0 {
		st.cancelHotkeyRecording(s)
		return true
	}
	acc, ok := platform.AccelFromKey(k.Key, k.Mods)
	if !ok {
		s.hotkeyErr = "请按住 Ctrl / Alt / Shift 再按一个键"
		st.armHotkeyRecTimeout(s) // 无效键也续期，给用户重试机会
		return true
	}
	s.recording = false
	s.hotkeyErr = ""
	stopRecTimer(s)
	if acc == st.showHotkey {
		// 录的就是旧键：start 时已注销，这里补回（applySettings 会因“未变化”跳过注册）。
		if err := platform.RegisterHotkey(acc, st.togglePanel); err != nil {
			log.Printf("voke 重新注册热键 %q 失败：%v", acc, err)
		}
		return true
	}
	s.hotkey = acc
	st.applySettings() // 落盘 + 撤旧注新（旧键已注销，撤是幂等）
	return true
}

// settingsView 是设置窗整帧视图。所有控件**即时生效**（无保存按钮）：开关/选择器改动
// 当帧落盘应用，文本框改动即应用；底部只留「完成」关窗。
func (st *appState) settingsView(c *ui.Context) {
	s := st.settings
	if s == nil {
		return
	}
	t := c.Theme()
	c.Root().Background(t.Background)

	ui.Column(c).Fill().Children(func() {
		// 标题栏：左标题，右「修改即时生效」提示，弱化但可读。
		ui.Row(c).Padding(24, 18).Gap(12).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "设置").FontSize(theme.InputSize + 1).TextColor(t.Text).Grow(1)
			ui.Text(c, "修改即时生效").FontSize(theme.HintSize).TextColor(t.TextMuted)
		})
		ui.Divider(c)

		ui.Scroll(c).Grow(1).Padding(24, 18).Children(func() {
			ui.Column(c).Gap(20).Children(func() {
				// ── 通用 ──
				ui.Fieldset(c, "通用", func() {
					ui.Column(c).Gap(16).Children(func() {
						ui.Field(c, "开机自启", func() {
							ui.Text(c, "登录系统后自动启动 voke").FontSize(theme.HintSize).TextColor(t.TextMuted).Grow(1)
							if ui.Switch(c, &s.autostart).Changed() {
								st.applySettings()
							}
						})
						ui.Field(c, "界面主题", func() {
							if ui.Select(c, &s.theme, themeLabels).Grow(1).Changed() {
								st.applySettings()
							}
						})
					})
				})

				// ── 快捷键 ──
				ui.Fieldset(c, "快捷键", func() {
					ui.Column(c).Gap(6).Children(func() {
						if s.recording {
							ui.Text(c, "请按下新的快捷键（Esc 取消）").FontSize(theme.HintSize).TextColor(t.TextMuted)
							ui.TextInputBase(c, &s.recBuf).Bind(&s.recHandle).ReadOnly(true).Grow(1).
								FontSize(theme.InputSize).Placeholder("等待按键…").Label("录制热键").
								OnTextKey(func(k ui.TextKey) bool { return st.recordHotkey(s, k) })
							s.recHandle.Focus() // 每帧回焦点（点击会先落到按钮，下一帧拉回）
						} else {
							ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
								ui.Text(c, "全局唤起 / 隐藏面板").FontSize(theme.HintSize).TextColor(t.TextMuted).Grow(1)
								if ui.Button(c, s.hotkey).Clicked() {
									st.startHotkeyRecording(s)
								}
							})
							ui.Text(c, "点击按键块后按下新组合，Esc 取消").FontSize(theme.HintSize).TextColor(t.TextMuted)
						}
						if s.hotkeyErr != "" {
							ui.Text(c, s.hotkeyErr).FontSize(theme.HintSize).TextColor(t.Danger)
						}
					})
				})

				// ── 插件 ──
				ui.Text(c, "插件").FontSize(theme.TitleSize).TextColor(t.TextMuted).
					Padding(2, 2)
				for i := range s.edits {
					p := &s.edits[i]
					ui.Fieldset(c, p.name, func() {
						ui.Column(c).Gap(12).Children(func() {
							ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
								if ui.Checkbox(c, &p.enabled, "启用").Changed() {
									st.applySettings()
								}
								ui.Spacer(c)
								ui.Text(c, "参与全局搜索").FontSize(theme.HintSize).TextColor(t.TextMuted)
								if ui.Switch(c, &p.isGlobal).Changed() {
									st.applySettings()
								}
							})
							ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
								ui.Text(c, "前缀").FontSize(theme.HintSize).TextColor(t.TextMuted)
								if ui.TextInput(c, &p.prefix).Grow(1).FontSize(theme.InputSize).
									Placeholder("如 #b；空=无").Label(p.name + " 前缀").Changed() {
									st.applySettings()
								}
								ui.Text(c, "权重").FontSize(theme.HintSize).TextColor(t.TextMuted)
								if ui.NumberInput(c, &p.weight, 0, 3, 0.1).Changed() {
									st.applySettings()
								}
								if ui.Select(c, &p.activation, activationLabels).Changed() {
									st.applySettings()
								}
							})
						})
					})
				}

				// ── 关于 ──
				ui.Fieldset(c, "关于", func() {
					ui.Column(c).Gap(16).Children(func() {
						ui.Field(c, "版本", func() {
							ui.Text(c, aboutVersion()).FontSize(theme.HintSize).TextColor(t.TextMuted).Grow(1)
						})
						ui.Field(c, "配置文件", func() {
							ui.Text(c, aboutConfigPath()).FontSize(theme.HintSize).TextColor(t.TextMuted).Grow(1)
						})
					})
				})
			})
		})

		ui.Divider(c)
		ui.Row(c).Padding(18).Gap(12).Children(func() {
			ui.Spacer(c)
			if ui.PrimaryButton(c, "完成").Clicked() {
				st.closeSettings(s)
			}
		})
	})
}

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

	top := appcfg.Top{Hotkey: hotkey, Autostart: autostart, Theme: themeCode(s.theme)}
	overrides := make([]config.UserOverride, 0, len(s.edits))
	for _, p := range s.edits {
		enabled, isGlobal, weight, prefix := p.enabled, p.isGlobal, p.weight, p.prefix
		activation := activationCode(p.activation)
		overrides = append(overrides, config.UserOverride{
			ID: p.id, Enabled: &enabled, IsGlobal: &isGlobal,
			Weight: &weight, Prefix: &prefix, Activation: &activation,
		})
	}

	if err := saveConfig(top, overrides); err != nil {
		log.Printf("voke 写设置失败：%v", err)
		return
	}
	// 重载：插件启用 / 前缀近即时生效，并把新 loaded 回存到 st.cfg（下次 seed 一致）。
	st.reloadConfigs()
	// 主题当场生效：appcfg 码值 = mygo ThemeSource，透传即可（见 platform.SetThemeSource）。
	platform.SetThemeSource(string(st.cfg.Theme))
	// 改键当场生效：先撤旧串、再注新串；注新失败则回滚旧串（对照生产「注册失败回退」，
	// 绝不留「新旧都没绑」的空窗）。撤/注都只在主线程（本函数由托盘 Click→saveSettings 走到）。
	if newKey := st.cfg.Hotkey; newKey != "" && newKey != st.showHotkey {
		platform.UnregisterHotkey(st.showHotkey)
		if err := platform.RegisterHotkey(newKey, st.togglePanel); err != nil {
			log.Printf("voke 改键注册 %q 失败，回滚到 %q：%v", newKey, st.showHotkey, err)
			platform.UnregisterHotkey(newKey)
			if rerr := platform.RegisterHotkey(st.showHotkey, st.togglePanel); rerr != nil {
				log.Printf("voke 回滚注册旧键 %q 也失败（下次启动按配置重建）：%v", st.showHotkey, rerr)
			}
		} else {
			st.showHotkey = newKey
		}
	}

	s.hotkey = st.showHotkey // 回显实际生效的键（改键失败时即旧键）
}
