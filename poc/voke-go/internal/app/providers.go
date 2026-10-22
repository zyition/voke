// providers.go：装配层的**插件注册表**、前缀表与分数据源。
//
// 这里是铁律①（「默认值只允许出现在 Descriptor 里」）在装配侧的唯一落点：
// apps 无前缀、picker 前缀 "#"、各插件默认前缀/激活方式/权重/条数全都写在
// 本文件的 Descriptor 中，engine / ui / platform 一处都不再抄第二份。
//
// 当前注册 10 个内置插件 + picker；用户覆盖由 appcfg 从 config.toml 解码后
// 交给 config.Normalize 合并，再经 BuildTable 产出前缀表。前缀冲突（C2）、
// 旧版 #b/#u/#t/#r 的自动修复都在这一层接线（见 loadConfigs / repairPrefixConflicts）。
package app

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zyition/voke/poc/voke-go/internal/logic/appcfg"
	"github.com/zyition/voke/poc/voke-go/internal/logic/cliphist"
	"github.com/zyition/voke/poc/voke-go/internal/logic/config"
	"github.com/zyition/voke/poc/voke-go/internal/logic/engine"
	"github.com/zyition/voke/poc/voke-go/internal/logic/frequency"
	"github.com/zyition/voke/poc/voke-go/internal/match"
	"github.com/zyition/voke/poc/voke-go/internal/platform"
	"github.com/zyition/voke/poc/voke-go/internal/providers"
	"github.com/zyition/voke/poc/voke-go/internal/providers/awake"
	"github.com/zyition/voke/poc/voke-go/internal/providers/base64"
	"github.com/zyition/voke/poc/voke-go/internal/providers/calculator"
	clipboardprovider "github.com/zyition/voke/poc/voke-go/internal/providers/clipboard"
	"github.com/zyition/voke/poc/voke-go/internal/providers/random"
	"github.com/zyition/voke/poc/voke-go/internal/providers/terminal"
	"github.com/zyition/voke/poc/voke-go/internal/providers/timestamp"
	"github.com/zyition/voke/poc/voke-go/internal/providers/uuid"
	"github.com/zyition/voke/poc/voke-go/internal/providers/windows"
	"github.com/zyition/voke/poc/voke-go/internal/providers/workspaces"
)

// providerAppsID 是 apps 插件的 id：现阶段**唯一**能把查询词变成结果的 provider。
// Global 态（不打前缀）走的就是它，Plugin 态下 PluginID 也是它，两条路的结果集
// 因此逐字相同（这是「Global 零回归」的结构依据）。
const providerAppsID = "apps"

// providerClipboardID 是剪贴板插件 id 的 app 侧别名（设置页按它挂专属配置项）。
const providerClipboardID = clipboardprovider.ID

// providerWindowsID 是窗口插件 id 的 app 侧别名（快捷键 / 行内动作按它定位）。
const providerWindowsID = windows.ID

// builtinDescriptors 是内置注册表。**注册顺序 = 前缀表优先级顺序 = picker 列表顺序**
// （engine.Scan 按表序做首个全等命中，config.BuildTable 不排序）。
//
// apps 的 DefaultPrefix 是空串（「无前缀」）：
//   - 不打前缀的裸查询词 = Global 态 = 现有 apps 过滤链路，一个字节都不变；
//   - 若给 apps 也配一个符号前缀，那个字符在今天的链路里是合法的搜索输入
//     （用户可以在框里打任意符号），一旦命中就会切模式——那是 Global 路径的
//     行为改动，本轮明确不做。
//
// picker 那条（ID == engine.PickerPluginID）是表里的**普通一条**（决策②）：
// 字符 "#" 由 Descriptor 带进来，代码里没有第二处硬编码的 '#'。
func builtinDescriptors() []config.Descriptor {
	return []config.Descriptor{
		{
			ID: providerAppsID, Name: "应用", Description: "搜索并启动开始菜单应用",
			Aliases: []string{"app", "yy"},
			// DefaultIsGlobal：裸查询词走它（§7.3.4 的 is_global）。
			DefaultEnabled: true, DefaultIsGlobal: true,
			DefaultPrefix: "", DefaultActivation: engine.Direct,
			DefaultWeight: 1.2, DefaultCount: 5,
		},
		{
			ID: engine.PickerPluginID, Name: "列出所有插件", Description: "按名称 / 别名过滤插件",
			DefaultEnabled: true, DefaultIsGlobal: false,
			DefaultPrefix: "#", DefaultActivation: engine.Direct,
			DefaultWeight: 1.0, DefaultCount: 5,
		},
		{
			// 二级前缀统一 `@+字母`：picker 用 Direct 的 "#" 独占 `#` 命名空间，
			// 任何 "#…" 触发串都会被它的 "#" 抢先命中（C2 也会直接拒绝），
			// 故 #b/#u/#t/#r 迁到 @b/@u/@t/@r。
			ID: base64.ID, Name: "Base64", Description: "base64 编解码（加密 / 解密）",
			Aliases:        []string{"b", "b64"},
			DefaultEnabled: true, DefaultIsGlobal: false,
			DefaultPrefix: "@b", DefaultActivation: engine.Indirect,
			DefaultWeight: 1.0, DefaultCount: 5,
		},
		{
			ID: uuid.ID, Name: "UUID", Description: "生成 UUID v4（可指定个数）",
			Aliases:        []string{"u"},
			DefaultEnabled: true, DefaultIsGlobal: false,
			DefaultPrefix: "@u", DefaultActivation: engine.Indirect,
			DefaultWeight: 1.0, DefaultCount: 5,
		},
		{
			ID: timestamp.ID, Name: "时间戳", Description: "时间戳 ⇄ 可读时间",
			Aliases:        []string{"t", "sj", "ts", "time"},
			DefaultEnabled: true, DefaultIsGlobal: false,
			DefaultPrefix: "@t", DefaultActivation: engine.Indirect,
			DefaultWeight: 1.0, DefaultCount: 5,
		},
		{
			ID: random.ID, Name: "随机值", Description: "随机整数 / 随机字符串",
			Aliases:        []string{"r", "sj", "rand"},
			DefaultEnabled: true, DefaultIsGlobal: false,
			DefaultPrefix: "@r", DefaultActivation: engine.Indirect,
			DefaultWeight: 1.0, DefaultCount: 5,
		},
		{
			// clipboard：`@c` 二级前缀、Indirect。历史数据只在内存，退出即清；
			// 条数上限走顶层 `[clipboard] max_entries`，不占 provider Count（后者
			// 上限 50，且历史本身已有自己的淘汰规则）。
			ID: clipboardprovider.ID, Name: "剪贴板", Description: "剪贴板历史（文本 / 富文本 / 图片）",
			Aliases:        []string{"clip", "nbt", "jqb"},
			DefaultEnabled: true, DefaultIsGlobal: false,
			DefaultPrefix: "@c", DefaultActivation: engine.Indirect,
			DefaultWeight: 1.0, DefaultCount: 5,
		},
		{
			ID: calculator.ID, Name: "计算器", Description: "算术表达式求值，如 1+1",
			Aliases:        []string{"calc", "jsq", "c"},
			DefaultEnabled: true, DefaultIsGlobal: false,
			DefaultPrefix: "=", DefaultActivation: engine.Direct,
			DefaultWeight: 1.0, DefaultCount: 5,
		},
		{
			// awake 是继 apps 之后**第二条 is_global**：无前缀、靠名字「保持唤醒 / awake」
			// 触发（对齐 §3 `[provider.awake] is_global = true`）。空前缀不进前缀表；裸查询
			// 里由 Global 聚合链路（collectGlobalBatches→Registry.Aggregate→globalActive）
			// 直接命中成组（`TestCollectGlobalBatches` 锁 awake 在裸查询批次里），也可经 `#`
			// picker 进 Plugin(awake)，或点托盘 awake 项经 openAwake 直达第一级。
			ID: awake.ID, Name: "保持唤醒", Description: "阻止系统休眠（可选连屏幕一起）",
			Aliases:        []string{"awake", "a"},
			DefaultEnabled: true, DefaultIsGlobal: true,
			DefaultPrefix: "", DefaultActivation: engine.Direct,
			DefaultWeight: 1.0, DefaultCount: 5,
		},
		{
			// windows：第三条 is_global、同样无前缀（靠打字命中，对齐 PTR Window Walker）。
			// 权重 1.0 < apps 1.2（同名优先启动应用）。数据源已接 `platform.EnumerateWindows`
			// （Windows 出真实窗口、Linux/测试侧 NewEmpty 恒空被聚合跳过）；Global 聚合链路
			// 同 awake 已接，只是 Linux 无窗口数据时不出组（真机 V3/V5 验实际命中）。
			ID: windows.ID, Name: "窗口", Description: "切换到已打开的窗口",
			Aliases:        []string{"win", "ck"},
			DefaultEnabled: true, DefaultIsGlobal: true,
			DefaultPrefix: "", DefaultActivation: engine.Direct,
			DefaultWeight: 1.0, DefaultCount: 5,
		},
		{
			// terminal：第四条 is_global、无前缀（直接打 profile 名命中，对齐 PTR Terminal）。
			// 数据源已接 `platform.ReadTerminalSettings`（Windows 现读 %LOCALAPPDATA%\Packages
			// 下 stable/preview 的 settings.json 合并去重 profile；Linux/测试侧返回 nil 恒空）。
			ID: terminal.ID, Name: "终端配置", Description: "用 Windows Terminal 打开某个配置（profile）",
			Aliases:        []string{"wt", "terminal"},
			DefaultEnabled: true, DefaultIsGlobal: true,
			DefaultPrefix: "", DefaultActivation: engine.Direct,
			DefaultWeight: 1.0, DefaultCount: 5,
		},
		{
			// workspaces：第五条 is_global、无前缀。数据源已接 `platform` 的编辑器历史只读
			// 读取器（纯 Go SQLite：Zed db.sqlite + VS Code state.vscdb 最近工作区，Linux 亦可
			// 测；无 env/未装时返回 nil 恒空）。
			ID: workspaces.ID, Name: "工作区", Description: "Zed / VSCode 最近打开的工作区，用记录它的编辑器打开",
			Aliases:        []string{"ws", "gzq"},
			DefaultEnabled: true, DefaultIsGlobal: true,
			DefaultPrefix: "", DefaultActivation: engine.Direct,
			DefaultWeight: 1.0, DefaultCount: 5,
		},
	}
}

// builtinProviders 是**已落地实现**的 provider 注册表（有真实 Query 的那批）。与
// builtinDescriptors（前缀路由/picker 数据源，含 apps/picker 这些**不**走
// providers.Provider 契约的条目）分开：apps 走 panelstate 老路径、picker 是哨兵，
// 都不进这张表。表里有的插件命中前缀后能真出结果；没有又非 apps = 未接入占位。
func builtinProviders() *providers.Registry {
	return builtinProvidersWithClipboard(nil)
}

// builtinProvidersWithClipboard 是生产装配用的版本：剪贴板 provider 接真实历史
// 快照（每次查询现取）。fetch=nil 时与 builtinProviders 等价（测试 / 非 Windows）。
func builtinProvidersWithClipboard(fetch clipboardprovider.Fetcher) *providers.Registry {
	return providers.NewRegistry(base64.New(), uuid.New(), timestamp.New(), random.New(), calculator.New(),
		clipboardprovider.New(fetch, nil), awakeProvider(), windowsProvider(), terminalProvider(), workspacesProvider())
}

// clipboardFetch 返回剪贴板历史的只读快照闭包。st.hist 为 nil（未初始化 / 测试）
// 时恒空，provider 行为与空数据源一致。
func (st *appState) clipboardFetch() []cliphist.Item {
	if st.hist == nil {
		return nil
	}
	return st.hist.Items()
}

// awakeProvider 让 awake provider 反映 platform 的真实保持唤醒状态：active 时一级首条
// 变「保持唤醒中 · …（剩余时间）」+ 取消项，否则只有两个方向。状态源每次查询现读
// （与 voke-sys 一致：到期 / 取消都会改它）。未激活（默认 / 测试）→ fetch 返回 nil，
// provider 行为与接线前一致（零回归）。
func awakeProvider() *awake.Provider {
	return awake.NewWithSource(func() *awake.Status {
		active, keepScreen, expiresAt := platform.AwakeState()
		if !active {
			return nil
		}
		return &awake.Status{ExpiresAt: expiresAt, KeepScreen: keepScreen}
	}, func() int64 { return time.Now().Unix() })
}

// terminalProvider 把 terminal provider 接到真实数据源：每次查询现读 %LOCALAPPDATA% 下
// 两包 settings.json（platform.ReadTerminalSettings，Windows 真读、非 Windows 返回 nil），
// 交纯逻辑 ProfilesFromSettings 合并去重盖章。wt.exe 兜底路径来自 platform.WTExe()。
// 非 Windows（含测试）→ 源为空 → provider 恒空，与接线前 NewEmpty 逐字一致（零回归）。
func terminalProvider() *terminal.Provider {
	return terminal.New(func() []terminal.Profile {
		srcs := platform.ReadTerminalSettings()
		if len(srcs) == 0 {
			return nil
		}
		in := make([]terminal.SettingsSource, len(srcs))
		for i, s := range srcs {
			in[i] = terminal.SettingsSource{Contents: s.Contents, AUMID: s.AUMID}
		}
		return terminal.ProfilesFromSettings(in)
	}, platform.WTExe())
}

// workspacesProvider 把 workspaces provider 接到真实数据源：只读 Zed 各通道 db.sqlite 的
// workspaces 行 + VS Code state.vscdb 的 recentlyOpenedPathsList（platform.Read*，纯 Go
// SQLite 驱动、跨平台，非 Windows 无 %LOCALAPPDATA%/%USERPROFILE% → 返回 nil），交已移植的
// 纯逻辑 workspaces.Workspaces 解析/排序/去重。zed/code 经 platform.ZedCommand/VSCodeCommand
// 发现（装则用编辑器 CLI 打开，VS Code 走 cmd.exe /c code.cmd；未装 / 非 Windows 返回 nil →
// open 退化为直接开路径）。非 Windows（含测试）源为空 = NewEmpty 零回归。
func workspacesProvider() *workspaces.Provider {
	return workspaces.New(func() []workspaces.Entry {
		zedRows := platform.ReadZedWorkspaces()
		vsRows := platform.ReadVsCodeRecents()
		if len(zedRows) == 0 && len(vsRows) == 0 {
			return nil
		}
		zs := make([]workspaces.ZedWorkspaceRow, len(zedRows))
		for i, r := range zedRows {
			zr := workspaces.ZedWorkspaceRow{Paths: r.Paths, Timestamp: r.Timestamp}
			if r.Remote != nil {
				zr.Remote = &workspaces.ZedRemoteRow{
					Kind: r.Remote.Kind, Host: r.Remote.Host, Port: r.Remote.Port,
					User: r.Remote.User, Distro: r.Remote.Distro, ContainerID: r.Remote.ContainerID,
				}
			}
			zs[i] = zr
		}
		vs := make([]workspaces.VsCodeRecentPayload, len(vsRows))
		for i, p := range vsRows {
			vs[i] = workspaces.VsCodeRecentPayload{JSON: p.JSON, FileTimestamp: p.FileTimestamp}
		}
		return workspaces.Workspaces(zs, vs)
	}, platformEditorCmd(platform.ZedCommand()), platformEditorCmd(platform.VSCodeCommand()))
}

// platformEditorCmd 把 platform 的 neutral 编辑器命令映射成 workspaces.EditorCommand
// （nil 透传 nil：未装编辑器 / 非 Windows → open 退化为直接开路径）。
func platformEditorCmd(c *platform.EditorCmd) *workspaces.EditorCommand {
	if c == nil {
		return nil
	}
	return &workspaces.EditorCommand{Exe: c.Exe, PreArgs: c.PreArgs}
}

// windowsProvider 把 windows provider 接到真实数据源：每次查询现枚举可切换顶层窗口
// （platform.EnumerateWindows，Windows 走 Win32 EnumWindows、非 Windows 返回 nil），转成
// windows.Entry。Icon 留空（PoC 未接图标提取，provider 走首字母降级）。非 Windows 恒空 =
// 接线前 NewEmpty 逐字一致（零回归）。
func windowsProvider() *windows.Provider {
	return windows.New(func() []windows.Entry {
		infos := platform.EnumerateWindows()
		if len(infos) == 0 {
			return nil
		}
		out := make([]windows.Entry, len(infos))
		for i, w := range infos {
			out[i] = windows.Entry{Hwnd: w.Hwnd, Title: w.Title, ProcessName: w.ProcessName, ProcessPath: w.ProcessPath}
		}
		return out
	})
}

// applyProviderConfigs 把归一后的配置（weight / count / enabled / is_global / prefix /
// activation）下发到已注册 provider 实例（对照 Rust `QueryEngine::apply_config`）。缺了
// 这一步，RunOne 用的还是 provider New() 里的内置默认——配置文件里给 random 写 count、
// 给某插件调 weight/关 enabled 就不生效。apps/picker 不在 reg 里（Get 返回 false）自然跳过。
func applyProviderConfigs(cfg config.Configs, reg *providers.Registry) {
	for _, c := range cfg.Providers {
		p, ok := reg.Get(c.ID)
		if !ok {
			continue
		}
		p.SetConfig(providers.ProviderConfig{
			Enabled: c.Enabled, IsGlobal: c.IsGlobal, Weight: c.Weight,
			Prefix: c.Prefix, Activation: c.Activation.String(), Count: c.Count,
		})
	}
}

// buildPrefixTable 装配前缀表：Descriptor → config.Normalize（C1–C7）→
// Configs.BuildTable。engine.Table 的**唯一来源**是 BuildTable（铁律③），
// main 侧不手拼任何 PrefixEntry。
//
// 归一失败（C1 / C2 / C4 这类报错）返回 error 交回调用方降级——注册表是装配期
// 常量，出错属于开发期 bug，不该让面板起不来；但也不静默（错误必须落日志，
// 见 panel.go 的 ready）。
func buildPrefixTable() (config.Configs, engine.Table, error) {
	cfg, err := config.Normalize(builtinDescriptors(), nil)
	if err != nil {
		return config.Configs{}, nil, fmt.Errorf("归一化插件配置失败：%w", err)
	}
	return cfg, cfg.BuildTable(), nil
}

// configPathEnv / freqPathEnv 是覆盖默认落盘位置的环境变量钩子（主要给测试与「便携模式」
// 换目录用）。未设 → 用 `platform.DataDir()`（Windows 上是 `%APPDATA%\voke\`）下的
// config.toml / frequency.toml。
const (
	configPathEnv = "VOKE_CONFIG"
	freqPathEnv   = "VOKE_FREQ"
)

// configFilePath：env 覆盖优先，否则数据目录下的 **config.toml**（与生产 Rust 同格式）。
func configFilePath() string {
	if p := strings.TrimSpace(os.Getenv(configPathEnv)); p != "" {
		return p
	}
	return filepath.Join(platform.DataDir(), "config.toml")
}

// freqFilePath：env 覆盖优先，否则数据目录下的 **frequency.toml**（与生产 Rust 同格式）。
func freqFilePath() string {
	if p := strings.TrimSpace(os.Getenv(freqPathEnv)); p != "" {
		return p
	}
	return filepath.Join(platform.DataDir(), "frequency.toml")
}

// readConfigFile 取配置文件内容。文件不存在（首启用）→ 尝试把旧版 config.json 一次性
// 迁移成 config.toml；存在但读失败 → 落日志后按默认（不静默）。
func readConfigFile() (data []byte, path string) {
	path = configFilePath()
	b, err := os.ReadFile(path)
	if err == nil {
		return b, path
	}
	if !errors.Is(err, fs.ErrNotExist) {
		log.Printf("voke 读配置文件失败（%s），按默认配置启动：%v", path, err)
		return nil, ""
	}
	if data, ok := migrateLegacyConfigJSON(path); ok {
		return data, path
	}
	return nil, ""
}

// migrateLegacyConfigJSON 把旧版 Go PoC 的 config.json 迁移成 config.toml（生产同格式），
// 旧文件改名 .bak 留底。仅在默认路径、env 未覆盖本侧路径时执行；任何一步失败都退回默认。
func migrateLegacyConfigJSON(tomlPath string) ([]byte, bool) {
	if strings.TrimSpace(os.Getenv(configPathEnv)) != "" {
		return nil, false
	}
	legacy := filepath.Join(platform.DataDir(), "config.json")
	lb, err := os.ReadFile(legacy)
	if err != nil {
		return nil, false
	}
	ld, derr := appcfg.FromJSON(lb)
	if derr != nil {
		log.Printf("voke：迁移配置失败（旧 config.json 解析错误）：%v", derr)
		return nil, false
	}
	hk := ld.Hotkey
	if !ld.HasHotkey {
		hk = hotkeyShow
	}
	data, eerr := appcfg.Encode(appcfg.Top{Hotkey: hk, Autostart: ld.Autostart, Theme: ld.Theme}, ld.Overrides)
	if eerr != nil {
		log.Printf("voke：迁移配置失败（编码 TOML）：%v", eerr)
		return nil, false
	}
	if werr := os.MkdirAll(filepath.Dir(tomlPath), 0o755); werr != nil {
		log.Printf("voke：迁移配置失败（建目录 %s）：%v", filepath.Dir(tomlPath), werr)
		return nil, false
	}
	if werr := os.WriteFile(tomlPath, data, 0o600); werr != nil {
		log.Printf("voke：迁移配置失败（写 %s）：%v", tomlPath, werr)
		return nil, false
	}
	if rerr := os.Rename(legacy, legacy+".bak"); rerr != nil {
		log.Printf("voke：配置已迁移，但旧文件改名失败（%s）：%v", legacy, rerr)
	} else {
		log.Printf("voke：配置已从 JSON 迁移为 TOML（旧文件保留为 config.json.bak）")
	}
	return data, true
}

// loadConfigs 是 ready() 的前缀表装配入口：有配置文件则「解码 → Normalize(descriptors,
// overrides) → BuildTable」，否则退回 buildPrefixTable() 的纯默认。坏 JSON 已在 appcfg.Merge
// 内回落默认（非静默：错误落日志）；Normalize 真错（C1/C2/C4）退回内置默认，绝不让面板起不来。
// loadConfigs 是 ready() 的配置装配入口：读配置文件 → 顶层设置（hotkey/autostart/theme）
// + provider 归一（Normalize→BuildTable）。无配置文件 = 零值 Loaded + Descriptor 纯默认。
// 坏 JSON 已在 appcfg.Merge 内回落默认（非静默：错误落日志）；Normalize 真错（C1/C2/C4）
// 退回内置默认，绝不让面板起不来。返回的 Loaded 供 ready() 应用热键 / 自启 / 主题。
func loadConfigs() (appcfg.Loaded, config.Configs, engine.Table, error) {
	data, path := readConfigFile()
	if len(data) == 0 {
		cfg, table, err := buildPrefixTable()
		return appcfg.Loaded{}, cfg, table, err
	}
	loaded, cfg, table, err := appcfg.Merge(builtinDescriptors(), data)
	if err != nil {
		// 旧配置迁移：v0.2.3 之前预设是 #b/#u/#t/#r，与 picker 的 "#" 构成
		// 「短触发串遮蔽长触发串」。此时只丢掉冲突插件的 Prefix 覆盖再归一，
		// 让新默认（@b/@u/@t/@r）生效，其余设置（热键/主题/自定义前缀）保持不动。
		if errors.Is(err, config.ErrPrefixConflict) {
			if repairedCfg, repairedTable, dropped, rerr := repairPrefixConflicts(loaded.Overrides); rerr == nil {
				log.Printf("voke 配置(%s)存在前缀冲突，已忽略 %s 的自定义前缀并回落到默认值：%v",
					path, strings.Join(dropped, "、"), err)
				return loaded, repairedCfg, repairedTable, nil
			} else {
				log.Printf("voke 配置(%s)前缀冲突自动修复失败：%v", path, rerr)
			}
		}
		if table == nil {
			// Normalize 报错（覆盖里未知 id / 保留键 / 无法修复的前缀冲突等）：
			// 这份配置不可用，插件配置退回内置默认；顶层设置仍保留，避免一次坏前缀
			// 连带丢掉热键 / 主题 / 自启。
			log.Printf("voke 配置(%s)归一失败，插件配置退回内置默认：%v", path, err)
			fallbackCfg, fallbackTable, ferr := buildPrefixTable()
			return loaded, fallbackCfg, fallbackTable, ferr
		}
		// 可恢复的解析告警（坏 JSON 已按默认兜底）：落日志，正常返回兜底产物。
		log.Printf("voke 配置(%s)有解析问题，已按默认兜底：%v", path, err)
	}
	return loaded, cfg, table, nil
}

// repairPrefixConflicts 把与前缀冲突相关的 Prefix 覆盖丢掉后重跑归一，返回修复后的
// 配置、表和被丢弃的插件 id（按首次出现顺序、去重）。
//
// 循环至多 4 轮：丢掉某个前缀覆盖后，该插件会回落到 Descriptor 默认值，可能与另一条
// 自定义前缀形成新的冲突。每一轮只处理 Prefix 指针非 nil 的条目；若冲突全部来自
// Descriptor 默认值（装配期 bug）则修不了，交回调用方走整体兜底。
func repairPrefixConflicts(overrides []config.UserOverride) (config.Configs, engine.Table, []string, error) {
	ovs := append([]config.UserOverride(nil), overrides...)
	dropped := make([]string, 0, 2)
	seen := make(map[string]bool)
	for attempt := 0; attempt < 4; attempt++ {
		bad := config.PrefixConflictIDs(builtinDescriptors(), ovs)
		if len(bad) == 0 {
			break
		}
		changed := false
		for _, id := range bad {
			for i := range ovs {
				if ovs[i].ID == id && ovs[i].Prefix != nil {
					ovs[i].Prefix = nil
					changed = true
					if !seen[id] {
						seen[id] = true
						dropped = append(dropped, id)
					}
				}
			}
		}
		if !changed {
			return config.Configs{}, nil, nil, fmt.Errorf("前缀冲突涉及默认值，无法自动修复")
		}
	}
	cfg, err := config.Normalize(builtinDescriptors(), ovs)
	if err != nil {
		return config.Configs{}, nil, nil, err
	}
	if len(dropped) == 0 {
		return config.Configs{}, nil, nil, fmt.Errorf("没有可丢弃的前缀覆盖")
	}
	return cfg, cfg.BuildTable(), dropped, nil
}

// loadFrequency 启动时读频率表（数据目录下的 frequency.toml，env 可覆盖）。文件不存在
// （首启用）→ 尝试迁移旧版 frequency.json；存在但读失败 / 坏数据 → 空表（不致命）。
func loadFrequency() frequency.Store {
	path := freqFilePath()
	var store frequency.Store
	hadOwn := false
	if data, err := os.ReadFile(path); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Printf("voke 读频率表失败（%s），按空表启动：%v", path, err)
		}
		store = frequency.New()
	} else {
		hadOwn = true
		store = appcfg.FrequencyFromTOML(data)
	}
	if !hadOwn {
		store = migrateLegacyFrequencyJSON(store)
	}
	if n := importProductionFrequency(store); n > 0 {
		log.Printf("voke：已从生产 voke 只读导入 %d 条使用频率（历史排序参照）", n)
		saveFrequency(store)
	}
	return store
}

// migrateLegacyFrequencyJSON 把旧版 Go PoC 的 frequency.json 一次性迁移成 frequency.toml
// （Rust 同格式），旧文件改名 .bak 留底。env 覆盖了本侧路径 / 无旧文件时不动。
func migrateLegacyFrequencyJSON(store frequency.Store) frequency.Store {
	if strings.TrimSpace(os.Getenv(freqPathEnv)) != "" {
		return store
	}
	legacy := filepath.Join(platform.DataDir(), "frequency.json")
	data, err := os.ReadFile(legacy)
	if err != nil {
		return store
	}
	store = appcfg.FrequencyFromJSON(data)
	saveFrequency(store)
	if rerr := os.Rename(legacy, legacy+".bak"); rerr != nil {
		log.Printf("voke：迁移频率表后改名旧文件失败（%s）：%v", legacy, rerr)
	} else {
		log.Printf("voke：频率表已从 JSON 迁移为 TOML（旧文件保留为 frequency.json.bak）")
	}
	return store
}

// importProductionFrequency 只读合并 `%APPDATA%\voke\frequency.toml`（生产 voke 的历史
// 使用次数）进本侧频率表：逐键取 max（只增不减、幂等），让迁移后「越用越靠前」的体感
// 与生产一致。**绝不写生产目录**；非 Windows（APPDATA 缺失）/ 无文件 / env 覆盖了本侧
// 频率路径时不导入。返回发生变化的键数。
func importProductionFrequency(store frequency.Store) int {
	if store == nil || strings.TrimSpace(os.Getenv(freqPathEnv)) != "" {
		return 0
	}
	base := os.Getenv("APPDATA")
	if base == "" {
		return 0
	}
	data, err := os.ReadFile(filepath.Join(base, "voke", "frequency.toml"))
	if err != nil {
		return 0
	}
	changed := 0
	for k, v := range appcfg.FrequencyFromTOML(data) {
		if store[k] < v {
			store[k] = v
			changed++
		}
	}
	return changed
}

// saveFrequency 把频率表写回数据目录（尽力而为）：先 MkdirAll（数据目录一般已由 InitLog
// 建好，这里兜底），写失败只记日志、绝不影响用户已拿到的结果。
func saveFrequency(store frequency.Store) {
	if store == nil {
		return
	}
	path := freqFilePath()
	data, err := appcfg.FrequencyToTOML(store)
	if err != nil {
		log.Printf("voke 序列化频率表失败：%v", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.Printf("voke 建频率表目录失败（%s）：%v", filepath.Dir(path), err)
		return
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		log.Printf("voke 写频率表失败（%s）：%v", path, err)
	}
}

// saveConfig 把设置窗的编辑结果写回 config.json（对照 saveFrequency 的落盘纪律：
// 先 MkdirAll 兜底、写失败只记日志不 panic）。top = 热键 / 自启 / 主题；overrides =
// 各 provider 的启用覆盖（Enabled 指针）。appcfg.Encode 与 FromJSON 往返稳定。
func saveConfig(top appcfg.Top, overrides []config.UserOverride) error {
	path := configFilePath()
	data, err := appcfg.Encode(top, overrides)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// reloadConfigs 在设置窗保存后重跑装配（读回刚写的 config.json → 归一 → 建表 →
// 下发到 live provider 实例），让插件启用 / 前缀变更近即时生效（picker 下次查询即用
// 新表）。热键 / 主题的**实时**应用不在本 PoC（热键重注册要拆旧句柄、主题要接 mygo
// 换肤，二者皆真机项，见 BACKLOG）；autostart 已在设置窗里当场 platform.SetAutostart。
// 归一失败沿用 ready() 的降级：落日志、退回纯默认，绝不让面板失能。返回新 loaded 供
// 调用方回存。
func (st *appState) reloadConfigs() {
	loaded, cfg, table, err := loadConfigs()
	if err != nil {
		log.Printf("voke 重载配置退回纯全局模式：%v", err)
	}
	applyProviderConfigs(cfg, st.pluginReg) // 下发到当前 live 注册表（appState 持有）
	st.cfg, st.providers, st.table = loaded, cfg, table
}

// pluginRow 是 picker 模式列表的一行（id 用于 SelectPlugin，两行文本用于渲染）。
type pluginRow struct {
	ID          string
	Name        string
	Description string
}

// rankPlugins 产出「所有**已启用** provider」按过滤词排好的列表
// （picker 哨兵条目自己不算候选——选中它没有意义）。
//
// 两条通道各司其职，且都不做触发判定（触发只在 engine.Scan，铁律②）：
//   - fuzzy 通道：插件名走 internal/match（与应用列表同一套匹配引擎，
//     含拼音 / 首字母键），得分进 Candidate.Fuzzy；
//   - bonus 通道：config.Rank 内部按 AliasKeys（前缀目标串 + Descriptor.Aliases）
//     加分，把「fuzzy 判不掉但别名沾边」的条目托底进 Fuzzy tier（§7.3.6 a 决策⑥）。
//
// 空过滤词不过滤任何行（config.Rank 的 q == "" 分支），列表按注册顺序出。
func rankPlugins(cfg config.Configs, query string) []pluginRow {
	rows := make(map[string]pluginRow, len(cfg.Providers))
	cands := make([]config.Candidate, 0, len(cfg.Providers))
	m := match.NewMatcher()
	q := strings.TrimSpace(query)
	for _, p := range cfg.Providers {
		if !p.Enabled || p.ID == engine.PickerPluginID {
			continue
		}
		var fuzzy float32
		if q != "" {
			if s, ok := match.ScoreEntry(m, q, match.NewSearchKeys(p.Name)); ok {
				fuzzy = s.Score
			}
		}
		c, ok := cfg.CandidateFor(p.ID, p.Name, fuzzy)
		if !ok {
			continue
		}
		cands = append(cands, c)
		rows[p.ID] = pluginRow{ID: p.ID, Name: p.Name, Description: p.Description}
	}
	out := make([]pluginRow, 0, len(cands))
	for _, r := range config.Rank(cands, query) {
		if row, ok := rows[r.PluginID]; ok {
			out = append(out, row)
		}
	}
	return out
}
