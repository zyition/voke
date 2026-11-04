// Package i18n 是界面文案的中英切换点。
//
// 设计取舍：本项目只有中英两种语言，且文案分散在 app / ui / providers / logic 各层，
// 不引入资源文件与 key 表，直接用 `L(中文, English)` 成对书写——调用点附近就能看到
// 两种语言的完整句子，漏译或占位符写错时容易在评审与测试中发现。纯 Go、零依赖，
// 逻辑层可以直接引用。
//
// 当前语言是进程级状态：启动时由 app 层按配置（跟随系统 / 中文 / 英文）解析后写入，
// 设置页切换语言时也在这里更新。provider 查询与界面构建可能跨线程读，故用原子布尔，
// 读路径无锁。
package i18n

import "sync/atomic"

// Language 是**已解析**的界面语言（System 在 app 层解析成 Chinese / English 后再传进来）。
type Language string

const (
	Chinese Language = "zh"
	English Language = "en"
)

// english 为 true 时 L 返回英文。默认中文：测试与未初始化的零值路径都保持既有中文断言。
var english atomic.Bool

// SetLanguage 设置当前界面语言，返回是否发生变化（调用方可据此决定是否重建界面文案）。
func SetLanguage(l Language) bool {
	want := l == English
	return english.Swap(want) != want
}

// Current 返回当前界面语言。
func Current() Language {
	if english.Load() {
		return English
	}
	return Chinese
}

// IsEnglish 报告当前是否为英文界面。
func IsEnglish() bool { return english.Load() }

// L 返回当前语言对应的文案。中文为默认值；英文为空串时回落到中文，避免漏译时出现空白。
func L(zh, en string) string {
	if english.Load() {
		if en != "" {
			return en
		}
	}
	return zh
}
