//go:build !windows

// mem_stub.go：非 Windows 平台的内存快照占位。
//
// 对齐 mem.rs 的 cfg(not(windows)) 分支：K32GetProcessMemoryInfo 不存在，
// 记一条「当前平台不提供」，这样调用方（panel.go / apps.go 的打点）不需要
// 写平台分支，Linux 原生 go build ./... / go test 也不回归。
package platform

import "log"

// SnapshotMem 非 Windows 占位实现。
func SnapshotMem(tag string) {
	log.Printf("内存快照 [%s]：当前平台不提供", tag)
}
