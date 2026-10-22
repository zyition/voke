// activation.go：WT profile 的**激活参数字符串**（对照 voke-core/src/terminal.rs 的
// `profile_activation_arguments`）。放本包（provider 的领域），执行层（main 的 dispatcher）
// 调用它拼 COM `ActivateApplication` 的参数串——与 Rust「core 出参数串、sys/app 执行」同构。
//
// 纯字符串、零 IO，Linux 原生可测（见 activation_test.go）。
package terminal

import "fmt"

// ActivationArguments 把 profile 标识（guid 优先、缺失退化显示名）拼成 WT 的激活参数串：
// `--profile "<profile>"`。逐字对照 Rust `format!("--profile \"{}\"", profile)`——用
// `%s` 手工加双引号而非 `%q`（后者按 Go 规则转义，会与 Rust 的裸插值不一致）。
func ActivationArguments(profile string) string {
	return fmt.Sprintf("--profile \"%s\"", profile)
}
