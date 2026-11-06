// Package charset 定义随机值插件的字符集规则：一个名称 + 一个字符类正则 + 启用开关，
// 编译成「可随机抽取的单字符集合」。
//
// 正则口径（有意收窄，避免用户写出无法枚举的表达式）：
//   - 写法是**字符类内容**，如 `a-zA-Z0-9`、`a-f0-9`、`!@#$%`；也可以自己带方括号
//     （`[a-zA-Z0-9]`）。没有方括号时自动包一层 `[...]`，因此不需要写 `^...$`。
//   - 候选字符来自可打印 ASCII（U+0020–U+007E），外加**正则里直接写出的**可打印
//     非 ASCII 字符（如 `￥`、`…`）。`\p{Han}` 这类依赖完整 Unicode 字符表的类
//     不在候选范围内。
//   - 区分大小写：`a-z` 与 `A-Z` 是两个集合，不提供忽略大小写开关。
//   - 空白会先被裁掉，需要空格请写 `\x20`。
//
// 本包是纯逻辑：零 mygo、零平台调用，Linux 原生可测。名称重复判定也按**区分大小写**
// 的精确串比较——`hex` 与 `Hex` 视为两个不同的字符集。
package charset

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"voke/internal/i18n"
)

const (
	// MaxSpecs 是单个插件最多可配置的字符集数量（随机值一级列表不宜过长）。
	MaxSpecs = 16
	// MaxNameRunes 是字符集名称上限。
	MaxNameRunes = 32
	// MaxPatternRunes 是字符集正则上限。
	MaxPatternRunes = 256
	// MinLength / MaxLength 是默认长度（插件级或字符集级）的取值范围。
	MinLength = 1
	MaxLength = 256
)

// Spec 是一套字符集配置。Name 同时作为展示名与子模式标识（random 会编码成
// `cs:<name>`），因此要求非空、无控制字符且在列表内唯一。
type Spec struct {
	Name    string
	Pattern string
	Enabled bool
	// Length 是该字符集的默认生成长度；0 = 跟随插件级默认长度。
	Length int
}

// Compile 把字符类正则编译成实际可抽取的字符列表。返回顺序为 ASCII 升序，
// 随后按正则中首次出现的顺序补充非 ASCII 字面字符；重复字符只保留一次。
func Compile(pattern string) ([]rune, error) {
	expr, err := expression(pattern)
	if err != nil {
		return nil, err
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return nil, fmt.Errorf(i18n.L("正则无效：%v", "invalid regular expression: %v"), err)
	}

	seen := make(map[rune]bool, 128)
	out := make([]rune, 0, 128)
	add := func(r rune) {
		if seen[r] {
			return
		}
		seen[r] = true
		out = append(out, r)
	}
	// 可打印 ASCII 是基础候选集；字符类正则在这里判定每个字符是否属于集合。
	for r := rune(0x20); r <= 0x7e; r++ {
		if re.MatchString(string(r)) {
			add(r)
		}
	}
	// 非 ASCII 没有可枚举的通用字符表，只认正则里直接写出来的字面字符（如 `￥`）。
	for _, r := range pattern {
		if r > 0x7e && unicode.IsPrint(r) && re.MatchString(string(r)) {
			add(r)
		}
	}
	if len(out) == 0 {
		return nil, errors.New(i18n.L("字符集为空：正则没有匹配任何可打印字符",
			"empty character set: the pattern matches no printable characters"))
	}
	return out, nil
}

// expression 把用户写的字符类内容规范成可直接编译的正则。
func expression(pattern string) (string, error) {
	p := strings.TrimSpace(pattern)
	if p == "" {
		return "", errors.New(i18n.L("字符集正则不能为空", "character-set pattern cannot be empty"))
	}
	if utf8.RuneCountInString(p) > MaxPatternRunes {
		return "", fmt.Errorf(i18n.L("字符集正则过长（最多 %d 个字符）",
			"character-set pattern is too long (max %d characters)"), MaxPatternRunes)
	}
	// 已经写了完整方括号就原样用；否则按字符类内容自动包一层。
	if strings.HasPrefix(p, "[") && strings.HasSuffix(p, "]") {
		return p, nil
	}
	return "[" + p + "]", nil
}

// Validate 校验单个字符集配置。名称与正则都会检查，正则必须是可枚举且非空的字符类。
func Validate(spec Spec) error {
	name := strings.TrimSpace(spec.Name)
	if name == "" {
		return errors.New(i18n.L("字符集名称不能为空", "character-set name cannot be empty"))
	}
	if utf8.RuneCountInString(name) > MaxNameRunes {
		return fmt.Errorf(i18n.L("字符集名称过长（最多 %d 个字符）",
			"character-set name is too long (max %d characters)"), MaxNameRunes)
	}
	if strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return errors.New(i18n.L("字符集名称不能包含控制字符",
			"character-set name cannot contain control characters"))
	}
	if spec.Length != 0 && (spec.Length < MinLength || spec.Length > MaxLength) {
		return fmt.Errorf(i18n.L("字符集长度必须在 %d–%d 之间，或留空跟随默认长度",
			"character-set length must be %d–%d, or empty to follow the default"), MinLength, MaxLength)
	}
	_, err := Compile(spec.Pattern)
	return err
}

// Sanitize 清洗配置列表：裁剪首尾空白、丢弃名称空/重复/正则无效的条目，并限制总数。
// 返回清洗后的列表与中文警告（供配置归一落日志）。重复判定区分大小写；同名条目保留
// 先出现的一条。所有条目（包括已停用的）都会校验，避免把坏配置带进设置页。
func Sanitize(specs []Spec) ([]Spec, []string) {
	out := make([]Spec, 0, len(specs))
	warnings := make([]string, 0)
	seen := make(map[string]bool, len(specs))
	for i, spec := range specs {
		spec.Name = strings.TrimSpace(spec.Name)
		spec.Pattern = strings.TrimSpace(spec.Pattern)
		if len(out) >= MaxSpecs {
			warnings = append(warnings, fmt.Sprintf(
				i18n.L("字符集数量超过上限 %d，已忽略后续条目", "too many character sets (max %d); extra entries were ignored"),
				MaxSpecs))
			break
		}
		if err := Validate(spec); err != nil {
			warnings = append(warnings, fmt.Sprintf(
				i18n.L("第 %d 个字符集 %q 无效，已忽略：%v", "character set #%d %q is invalid and was ignored: %v"),
				i+1, spec.Name, err))
			continue
		}
		if seen[spec.Name] {
			warnings = append(warnings, fmt.Sprintf(
				i18n.L("字符集名称 %q 重复，已忽略后一个", "duplicate character-set name %q; the later entry was ignored"),
				spec.Name))
			continue
		}
		seen[spec.Name] = true
		out = append(out, spec)
	}
	return out, warnings
}
