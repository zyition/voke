//go:build !windows

package platform

import "github.com/zyition/voke/poc/voke-go/internal/logic/appsmodel"

// ShortcutApps 非 Windows 占位：开始菜单 .lnk 是 Windows 概念，返回空。
func ShortcutApps() []appsmodel.AppEntry { return nil }
