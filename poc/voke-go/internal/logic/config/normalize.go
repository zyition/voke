// 本文件实现 §7.3.4 的 normalize：把 Descriptor 默认值与用户覆盖合并成唯一真相
// ProviderConfig，并逐条跑校验 C1–C7。失败语义见包注释（报错 vs 警告），全部不静默。

package config

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"

	"github.com/zyition/voke/poc/voke-go/internal/logic/engine"
)

// 校验失败的哨兵错误：接线层用 errors.Is 区分「该退出」还是「该降级」。
var (
	// ErrUnknownProviderID 是 C1：用户写了注册表里不存在的 provider id。
	ErrUnknownProviderID = errors.New("未知的 provider id")
	// ErrPrefixConflict 是 C2：两条前缀展开后的触发串互为前缀（含完全相同），
	// 逐键扫描时先命中的一条会遮蔽另一条。判定见 prefix.go。
	ErrPrefixConflict = errors.New("前缀冲突")
	// ErrReservedPrefix 是 C4：前缀撞上保留键。
	ErrReservedPrefix = errors.New("保留键冲突")
	// ErrDuplicateDescriptor 是注册表自身出错（同一 id 出现多次，属于装配期 bug）。
	ErrDuplicateDescriptor = errors.New("注册表 id 重复")
)

// 归一化的数值边界（§7.3.4 的 C5 / C6，沿用生产 config.rs:406-415 的取值）。
const (
	// MinWeight / MaxWeight 是 weight 的夹紧区间（C5）。
	MinWeight = 0.0
	MaxWeight = 3.0
	// MinCount / MaxCount 是 count 的夹紧区间（C6）。
	MinCount = 1
	MaxCount = 50
	// neutralWeight 是**乘法单位元**，不是某个插件的默认权重：用户写了 NaN / ±Inf
	// 且 Descriptor 的默认值本身也不可用时的兜底。放这里是为了让「默认值只在
	// Descriptor」这条铁律有唯一一处例外可以说清（它是恒等元，不是产品取值）。
	neutralWeight = 1.0
)

// Normalize 合并默认值与用户覆盖，产出归一后的 Configs，并跑 C1–C7。
//
// 执行顺序（越靠前的失败越致命，后面的校验要用前面归一出的最终值）：
//
//  0. 注册表自检（id 非空且不重复）——C1 的前提；
//  1. C1 未知 id → 报错，列出**全部**未知 id（生产 D4 的静默失效就是缺这条）；
//  2. 逐插件合并 + C7 activation、C5 weight、C6 count、C3 prefix；
//  3. C4 保留键 → 报错（要拦的是「picker 那条还没进表就被字母键挤掉」）；
//  4. C2 触发串互不为前缀 → 报错，列出每一对撞车插件（判定见 prefix.go）。
//
// 返回 error 时 Configs 是零值：调用方不该「带着坏配置继续跑」，
// 要么退出要么显式降级（接线层决定，本包只保证消息里说清了是什么坏了）。
func Normalize(descriptors []Descriptor, overrides []UserOverride) (Configs, error) {
	// 0. 注册表自检。
	regIDs := make([]string, 0, len(descriptors))
	seen := make(map[string]bool, len(descriptors))
	for _, d := range descriptors {
		if d.ID == "" {
			return Configs{}, fmt.Errorf("注册表里有插件未声明 id（Descriptor.ID 为空串）")
		}
		if seen[d.ID] {
			return Configs{}, fmt.Errorf("%w：%s（注册表 id 必须唯一）", ErrDuplicateDescriptor, d.ID)
		}
		seen[d.ID] = true
		regIDs = append(regIDs, d.ID)
	}

	// 1. C1：用户写了注册表里没有的 id。
	ovByID := make(map[string]UserOverride, len(overrides))
	ovOrder := make([]string, 0, len(overrides))     // 覆盖项的首次出现顺序，警告才有确定性
	dupCount := make(map[string]int, len(overrides)) // 同 id 写了几次
	var unknown []string
	for _, ov := range overrides {
		if dupCount[ov.ID] == 0 {
			ovOrder = append(ovOrder, ov.ID)
		}
		dupCount[ov.ID]++
		if !seen[ov.ID] {
			unknown = append(unknown, ov.ID)
			continue
		}
		ovByID[ov.ID] = ov // 同 id 出现多次：后写覆盖先写（另记警告，见下）
	}
	if len(unknown) > 0 {
		return Configs{}, fmt.Errorf("%w：%s（注册表里的 id：%s）",
			ErrUnknownProviderID, joinIDs(sortedCopy(unknown)), joinIDs(regIDs))
	}

	// 2. 逐插件合并 + 单项校验。
	out := Configs{Providers: make([]ProviderConfig, 0, len(descriptors))}
	for _, id := range ovOrder {
		if n := dupCount[id]; n > 1 {
			out.warnf("插件 %s 在用户配置里出现 %d 次，以最后一条为准", id, n)
		}
	}
	for _, d := range descriptors {
		ov := ovByID[d.ID]
		p := ProviderConfig{
			ID:          d.ID,
			Name:        d.Name,
			Description: d.Description,
			Aliases:     append([]string(nil), d.Aliases...),
			Enabled:     derefBool(ov.Enabled, d.DefaultEnabled),
			IsGlobal:    derefBool(ov.IsGlobal, d.DefaultIsGlobal),
		}

		// C7：activation 未知取值 → 记日志、按「没写」处理（用 Descriptor 默认）。
		p.Activation = d.DefaultActivation
		if ov.Activation != nil {
			a, ok := parseActivation(*ov.Activation)
			if !ok {
				out.warnf("插件 %s 的 activation %q 不是 direct / indirect，按「没写」处理（C7）",
					d.ID, *ov.Activation)
			} else {
				p.Activation = a
			}
		}

		// C5：weight 必须有限，然后 clamp 到 0..3（夹紧是静默的，非有限要吭声）。
		weight := derefFloat(ov.Weight, d.DefaultWeight)
		if !isFinite(weight) {
			fallback := d.DefaultWeight
			if !isFinite(fallback) {
				fallback = neutralWeight
			}
			out.warnf("插件 %s 的 weight %s 不是有限数，改用 %g（C5）", d.ID, formatFloat(weight), fallback)
			weight = fallback
		}
		p.Weight = clampFloat(weight, MinWeight, MaxWeight)

		// C6：count clamp 到 1..50（静默夹紧）。
		p.Count = clampInt(derefInt(ov.Count, d.DefaultCount), MinCount, MaxCount)

		// C3：前缀含空白 → 该项置空 + 中文警告；空串是合法值（"" = 无前缀）。
		p.Prefix = derefString(ov.Prefix, d.DefaultPrefix)
		if p.Prefix != "" && containsSpace(p.Prefix) {
			out.warnf("插件 %s 的前缀 %q 含空白，已置空（C3：前缀不能含空白）", d.ID, p.Prefix)
			p.Prefix = ""
		}

		out.Providers = append(out.Providers, p)
	}

	// C4：保留键冲突 → 报错退出（先把所有撞车项收集齐，一次说全）。
	var reserved []string
	for _, p := range out.Providers {
		if why, bad := reservedViolation(p.Prefix); bad {
			reserved = append(reserved, fmt.Sprintf("%s 的前缀 %q 是%s", p.ID, p.Prefix, why))
		}
	}
	if len(reserved) > 0 {
		return Configs{}, fmt.Errorf("%w：%s（§7.3.6b：前缀按符号键分配，不占用字母与数字）",
			ErrReservedPrefix, strings.Join(reserved, "；"))
	}

	// C2：两条前缀展开后的触发串互为前缀 → 报错。判定的是**触发串**而不是字面量：
	// Direct 展开为前缀本身、Indirect 展开为「前缀 + 一个空格」（engine.Scan 的全等
	// 比较目标），所以 `#` 与 `#b`（间接）也会冲突；而 `@b` 与 `@bc`（都间接）不冲突。
	// 空前缀不参与：多个「无前缀」插件共存是合法配置。
	claims := make([]prefixClaim, 0, len(out.Providers))
	for _, p := range out.Providers {
		if p.Prefix == "" {
			continue
		}
		claims = append(claims, prefixClaim{
			id: p.ID, name: p.Name, prefix: p.Prefix, activation: p.Activation,
		})
	}
	var clashes []string
	for i := 0; i < len(claims); i++ {
		for j := i + 1; j < len(claims); j++ {
			a, b := claims[i], claims[j]
			if !prefixesConflict(a, b) {
				continue
			}
			clashes = append(clashes, fmt.Sprintf(
				"%s（%s，%s）的前缀 %q 与 %s（%s，%s）的前缀 %q 冲突：触发串 %q 与 %q 互为前缀",
				a.id, a.name, activationChinese(a.activation), a.prefix,
				b.id, b.name, activationChinese(b.activation), b.prefix,
				ActivationTarget(a.prefix, a.activation), ActivationTarget(b.prefix, b.activation)))
		}
	}
	if len(clashes) > 0 {
		return Configs{}, fmt.Errorf("%w：%s（C2：各前缀展开后的触发串不能互为前缀）",
			ErrPrefixConflict, strings.Join(clashes, "；"))
	}

	return out, nil
}

// parseActivation 宽松解析 activation（同生产 deserialize_activation_lenient：
// 大小写与首尾空白不敏感，未知取值返回 ok=false 交回 C7 处理）。
func parseActivation(raw string) (engine.Activation, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "direct":
		return engine.Direct, true
	case "indirect":
		return engine.Indirect, true
	default:
		return engine.Direct, false
	}
}

// reservedViolation 判定 C4 的保留键：返回撞上的类别名（用于中文消息）。
//
// 判定的是**归一后的最终前缀**（C3 已把含空白的前缀置空，空前缀不是保留键）。
// 保留集合按 §7.3.6b 的分配原则「按符号键、不占用字母」写死两档：
//   - 首字符是字母（含中文等任何 Unicode 字母）→ 保留给模糊搜索。
//     单个字母（`c`）会抢 Camera / Chrome，双字母（`ca`）文档已明确**弃用**
//     「与 Calendar / Word 开头的应用名撞车」，故整档一起拒。
//   - 首字符是 ASCII 数字 → 保留。数字打头是全局搜索的常态输入（时间戳
//     `1737…`、算式 `1+1`），且 CmdPal 的符号键分配表里没有数字。
//
// 除此之外不拒任何前缀：`#` 族不再有特殊含义（§7.3.4 尾注），picker 的默认前缀
// 由 Descriptor 带进来、靠 C2 保证不撞车。
func reservedViolation(prefix string) (string, bool) {
	if prefix == "" {
		return "", false
	}
	first := []rune(prefix)[0]
	switch {
	case unicode.IsLetter(first):
		return "字母键", true
	case first >= '0' && first <= '9':
		return "数字键", true
	default:
		return "", false
	}
}

// containsSpace 判定前缀里是否有任何空白（同生产 `p.chars().any(|c| c.is_whitespace())`）。
func containsSpace(s string) bool {
	return strings.IndexFunc(s, unicode.IsSpace) >= 0
}

func isFinite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// clampInt 是 clampFloat 的整数版（C6 用；不用 float 绕一圈免得出现小数中间态）。
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// formatFloat 给警告消息里的非有限数一个看得懂的写法（NaN / +Inf / -Inf）。
func formatFloat(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "+Inf"
	case math.IsInf(f, -1):
		return "-Inf"
	default:
		return strconv.FormatFloat(f, 'g', -1, 64)
	}
}

func derefBool(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

func derefString(p *string, def string) string {
	if p == nil {
		return def
	}
	return *p
}

func derefFloat(p *float64, def float64) float64 {
	if p == nil {
		return def
	}
	return *p
}

func derefInt(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
}

// warnf 追加一条中文警告（接线层负责落日志）。
func (c *Configs) warnf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	c.Warnings = append(c.Warnings, msg)
}
