// Package appcfg 是配置**解码层**（§4 / §7.3.4）：把落盘的配置文件字节解码成
// `config.UserOverride` + 顶层设置（hotkey / autostart / theme），交给 logic/config 的
// `Normalize` 合并 + 校验。对照生产 `voke-core/src/config.rs` 的 `ConfigPatch` /
// `AppConfig::from_toml`——**本侧落盘同样用 TOML**（顶层三键 + `[providers.<id>]`，与
// 生产 config.toml 同格式，可互相读取；旧版 `config.json` 仅保留只读迁移路径）。
//
// 分层：本包**不做文件 IO**（读盘 / %APPDATA% 路径解析在接线层 platform），只吃字节切片、
// 吐已解码的 Go 值——与 logic/config「只吃已解码值」同源，把「解码」从「归一」里分离。
package appcfg

import (
	"encoding/json"

	"github.com/BurntSushi/toml"
	"sort"
	"strings"

	"voke/internal/logic/config"
	"voke/internal/logic/engine"
	"voke/internal/logic/frequency"
)

// Theme 是界面主题模式的解码值（"system" / "dark" / "light"）。
type Theme string

const (
	ThemeSystem Theme = "system"
	ThemeDark   Theme = "dark"
	ThemeLight  Theme = "light"
)

// Loaded 是解码后的配置：顶层设置（带「有没有写」标记，未写交给上层默认）+ 按 provider
// id 稳定序的覆盖表。
type Loaded struct {
	Hotkey       string // 用户写的热键串（未写则 HasHotkey=false）
	HasHotkey    bool
	Autostart    bool
	HasAutostart bool
	Theme        Theme // 未写 / 非法 → 空串（上层回落 System）
	// ClipboardMaxEntries 是剪贴板历史的普通条目上限；HasClipboardMaxEntries=false
	// 表示没写，由调用方回落 cliphist.DefaultMaxEntries。
	ClipboardMaxEntries    int
	HasClipboardMaxEntries bool
	Overrides              []config.UserOverride
}

// providerPatch 是单个 provider 的部分覆盖（指针字段：nil = 没写；omitempty 让没写的不落盘）。
// 同时带 json 与 toml 标签：toml 为当前落盘格式（与生产 config.toml 同形），json 仅供
// 旧版 config.json 的迁移读取。
type providerPatch struct {
	Enabled    *bool    `json:"enabled,omitempty" toml:"enabled,omitempty"`
	IsGlobal   *bool    `json:"is_global,omitempty" toml:"is_global,omitempty"`
	Weight     *float64 `json:"weight,omitempty" toml:"weight,omitempty"`
	Prefix     *string  `json:"prefix,omitempty" toml:"prefix,omitempty"`
	Activation *string  `json:"activation,omitempty" toml:"activation,omitempty"`
	Count      *int     `json:"count,omitempty" toml:"count,omitempty"`
	Hotkey     *string  `json:"hotkey,omitempty" toml:"hotkey,omitempty"`
}

// configFile 是顶层 schema（与生产 config.toml 同形：顶层三键 + [providers.<id>] 表）。
type configFile struct {
	Hotkey    *string                  `json:"hotkey" toml:"hotkey"`
	Autostart *bool                    `json:"autostart" toml:"autostart"`
	Theme     *string                  `json:"theme" toml:"theme"`
	Clipboard *clipboardPatch          `json:"clipboard,omitempty" toml:"clipboard"`
	Providers map[string]providerPatch `json:"providers" toml:"providers"`
}

// clipboardPatch 是 `[clipboard]` 表（目前只有历史条数上限）。
type clipboardPatch struct {
	MaxEntries *int `json:"max_entries,omitempty" toml:"max_entries,omitempty"`
}

// FromJSON 解码配置字节。坏 JSON → 返回 error（调用方据此回落默认，对照 Rust
// `from_toml` 解析失败回 AppConfig::default）。
//
// 宽松规则（对齐 Rust 的 lenient deserializer，不让一个笔误废掉整份配置）：
//   - activation 非 direct/indirect（大小写 / 首尾空白宽松）→ 丢弃（等同没写，交 Normalize 回填）；
//   - theme 非 system/dark/light → 丢弃（交上层默认 System）；
//   - prefix 空串是合法「无前缀」值，原样保留交给 Normalize。
//
// FromTOML 解码配置 TOML（与生产 config.toml 同格式）。坏 TOML → error（调用方回落默认）。
func FromTOML(data []byte) (Loaded, error) {
	var f configFile
	if err := toml.Unmarshal(data, &f); err != nil {
		return Loaded{}, err
	}
	return decodeFile(f), nil
}

// FromJSON 解码旧版 config.json（仅供一次性迁移；新落盘一律 TOML）。
func FromJSON(data []byte) (Loaded, error) {
	var f configFile
	if err := json.Unmarshal(data, &f); err != nil {
		return Loaded{}, err
	}
	return decodeFile(f), nil
}

// decodeFile 把已解析的结构折算成 Loaded（宽松规则见 FromJSON 历史注释）。
func decodeFile(f configFile) Loaded {
	out := Loaded{}
	if f.Hotkey != nil {
		out.Hotkey, out.HasHotkey = *f.Hotkey, true
	}
	if f.Autostart != nil {
		out.Autostart, out.HasAutostart = *f.Autostart, true
	}
	if f.Theme != nil {
		if t, ok := normalizeTheme(*f.Theme); ok {
			out.Theme = t
		}
	}
	if f.Clipboard != nil && f.Clipboard.MaxEntries != nil {
		out.ClipboardMaxEntries, out.HasClipboardMaxEntries = *f.Clipboard.MaxEntries, true
	}

	// provider 覆盖按 id 稳定序输出（map 无序 → 排序保证 Normalize 的警告 / 错误确定）。
	ids := make([]string, 0, len(f.Providers))
	for id := range f.Providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		p := f.Providers[id]
		ov := config.UserOverride{
			ID:       id,
			Enabled:  p.Enabled,
			IsGlobal: p.IsGlobal,
			Weight:   p.Weight,
			Prefix:   p.Prefix,
			Count:    p.Count,
			Hotkey:   p.Hotkey,
		}
		if p.Activation != nil {
			if a, ok := normalizeActivation(*p.Activation); ok {
				ov.Activation = &a
			}
			// 非法 activation → 留 nil（等同没写）。
		}
		out.Overrides = append(out.Overrides, ov)
	}
	return out
}

// normalizeActivation：宽松匹配 direct/indirect，返回规范小写值。
func normalizeActivation(raw string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "direct":
		return "direct", true
	case "indirect":
		return "indirect", true
	default:
		return "", false
	}
}

// normalizeTheme：宽松匹配 system/dark/light。
func normalizeTheme(raw string) (Theme, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "system":
		return ThemeSystem, true
	case "dark":
		return ThemeDark, true
	case "light":
		return ThemeLight, true
	default:
		return "", false
	}
}

// Top 是顶层设置（Encode 用）。Theme 空串 → 落 "system"。
type Top struct {
	Hotkey    string
	Autostart bool
	Theme     Theme
	// ClipboardMaxEntries > 0 时落 `[clipboard] max_entries`；0 = 不写该节
	// （旧配置迁移 / 非设置页路径保持文件原样）。
	ClipboardMaxEntries int
}

// Encode 把顶层设置 + provider 覆盖序列化回 **TOML**（与生产 config.toml 同形：
// 顶层 hotkey/autostart/theme + [providers.<id>] 表）。provider 字段为指针：nil（没写）
// 不落盘；前缀为空串时与生产一致**省略**该键。
func Encode(top Top, overrides []config.UserOverride) ([]byte, error) {
	theme := top.Theme
	if theme == "" {
		theme = ThemeSystem
	}
	hotkey, autostart, themeStr := top.Hotkey, top.Autostart, string(theme)
	file := configFile{
		Hotkey:    &hotkey,
		Autostart: &autostart,
		Theme:     &themeStr,
		Providers: map[string]providerPatch{},
	}
	if top.ClipboardMaxEntries > 0 {
		maxEntries := top.ClipboardMaxEntries
		file.Clipboard = &clipboardPatch{MaxEntries: &maxEntries}
	}
	for _, ov := range overrides {
		patch := providerPatch{
			Enabled: ov.Enabled, IsGlobal: ov.IsGlobal, Weight: ov.Weight,
			Activation: ov.Activation, Count: ov.Count, Hotkey: ov.Hotkey,
		}
		if ov.Prefix != nil && *ov.Prefix != "" {
			patch.Prefix = ov.Prefix // 空前缀=无，生产 config.toml 也不写该键
		}
		file.Providers[ov.ID] = patch
	}
	return toml.Marshal(file)
}

// Merge 把「配置字节 → 解码 → config.Normalize(descriptors, overrides) → BuildTable」串成
// 一步（对照 Rust `AppConfig::from_toml`：解析 + 合并 + 归一）。这是接线层读盘后的唯一入口。
//
// 坏 TOML / 读不到 → data 为空或解析失败时**回落全部默认**（overrides=nil），与 Rust 一致，
// 绝不让一份坏配置挡住面板启动；解析错误本身作为 warning 返回给调用方落日志（绝不静默）。
// descriptors 的默认值是唯一真相源（铁律①），本函数不持有第二份默认表。
func Merge(descriptors []config.Descriptor, data []byte) (loaded Loaded, cfg config.Configs, table engine.Table, decodeErr error) {
	loaded, decodeErr = FromTOML(data)
	overrides := loaded.Overrides
	if decodeErr != nil {
		// 解析失败：按「没有任何用户覆盖」处理，走 Descriptor 默认（Rust 回 AppConfig::default）。
		overrides = nil
	}
	cfg, err := config.Normalize(descriptors, overrides)
	if err != nil {
		return loaded, config.Configs{}, nil, err
	}
	return loaded, cfg, cfg.BuildTable(), decodeErr
}

// FrequencyToTOML 序列化频率表为 TOML——与生产 voke 的 `frequency.toml` **同格式**
// （平铺 `"<key>" = <count>`），故本侧文件与 Rust 版可互相读取、升级无需迁移格式。
func FrequencyToTOML(store frequency.Store) ([]byte, error) {
	if store == nil {
		store = frequency.Store{}
	}
	return toml.Marshal(store)
}

// FrequencyFromTOML 反序列化频率表（坏 / 空数据 → 空表，不 panic；只少一层排序加成）。
func FrequencyFromTOML(data []byte) frequency.Store {
	store := frequency.New()
	if len(data) == 0 {
		return store
	}
	if err := toml.Unmarshal(data, &store); err != nil {
		return frequency.New()
	}
	return store
}

// FrequencyToJSON 序列化频率表（对照 Rust `frequency_to_toml`，此处 JSON）。频率表是
// `map[key]count`，直接 marshal。
func FrequencyToJSON(store frequency.Store) ([]byte, error) {
	if store == nil {
		store = frequency.Store{}
	}
	return json.MarshalIndent(store, "", "  ")
}

// FrequencyFromJSON 反序列化频率表（对照 Rust `frequency_from_toml`）。坏 / 空数据 → 空表
// （不 panic、不返回 nil-map 之外的怪态）：读不到频率不影响功能，只少一层排序加成。
func FrequencyFromJSON(data []byte) frequency.Store {
	store := frequency.New()
	if len(data) == 0 {
		return store
	}
	if err := json.Unmarshal(data, &store); err != nil {
		return frequency.New()
	}
	return store
}
