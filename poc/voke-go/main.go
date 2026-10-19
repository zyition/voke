// main.go 是 PoC 的可执行入口。**§7.2.2 步骤8b**：package main 只留这一个薄壳——真正的
// 进程装配（单实例 / App.Run 生命周期 / WhenReady / 托盘 / 热键 / 面板 / 设置窗 / 意图执行）
// 全在 `internal/app`。root 只剩 `func main` 的唯一理由：`-H windowsgui` 链接需要一个
// package main 入口，且让「main = 装配的接缝」这一职责边界显式化。
package main

import "github.com/zyition/voke/poc/voke-go/internal/app"

func main() { app.Main() }
