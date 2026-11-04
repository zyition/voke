//go:build !windows

// opendir_stub.go：非 Windows 占位。产品面向 Windows，测试 / 开发构建里打开目录
// 不产生副作用，返回错误让调用方按失败路径处理。
package platform

import "errors"

// OpenDirectory 非 Windows 占位。
func OpenDirectory(string) error { return errors.New("打开目录仅支持 Windows") }
