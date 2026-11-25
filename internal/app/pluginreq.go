// pluginreq.go：插件对宿主环境的前置依赖（目前是外部软件是否安装）。
//
// 语义：
//   - 初始化 / 配置重载时，依赖不满足的插件**按停用处理**（不改写 config.toml，
//     只影响本次运行的生效配置）；用户装好软件后下次启动或重新打开设置即可启用；
//   - 设置页在插件卡片上给出缺失项与官方安装 / 下载链接，用户拨开关不会被接受。
//
// 为什么不在 internal/logic/config 里做：探测要碰注册表 / 文件系统，属于
// internal/platform 的职责；logic 只保留纯归一逻辑。
package app

import (
	"log"

	"voke/internal/logic/config"
	"voke/internal/logic/engine"
	"voke/internal/platform"
	"voke/internal/providers/terminal"
	"voke/internal/providers/workspaces"
)

// pluginRequirement 是一条环境依赖：software 是用户可见的软件名，url 是官方
// 安装文档 / 下载页（设置页直接做成可点链接），probe 在调用时现测。
type pluginRequirement struct {
	software string
	url      string
	probe    func() bool
}

// pluginRequirements 返回内置插件的外部依赖表。软件名是专有名词，不随语言变化；
// 每次调用重建，避免将来把本地化文案固化在包级变量里。
func pluginRequirements() map[string]pluginRequirement {
	return map[string]pluginRequirement{
		terminal.ID: {
			software: "Windows Terminal",
			url:      "https://learn.microsoft.com/windows/terminal/install",
			probe:    platform.WindowsTerminalInstalled,
		},
		workspaces.VsCodeID: {
			software: "VS Code",
			url:      "https://code.visualstudio.com/download",
			probe:    platform.VSCodeInstalled,
		},
		workspaces.ZedID: {
			software: "Zed",
			url:      "https://zed.dev/download",
			probe:    platform.ZedInstalled,
		},
	}
}

// applyPluginAvailability 把依赖不满足的插件改成停用，并按新启用态重建前缀表。
//
// 只改传进来的 Configs 副本里的 Enabled（slice 元素），不写回 config.toml：
// 用户配置里的启用意图保留，装好软件后无需再手动打开开关。依赖满足的插件
// 与非依赖插件原样不动。
func applyPluginAvailability(cfg config.Configs, reqs map[string]pluginRequirement) (config.Configs, engine.Table) {
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		if !p.Enabled {
			continue
		}
		req, ok := reqs[p.ID]
		if !ok || req.probe() {
			continue
		}
		log.Printf("voke 未检测到 %s，插件 %s 已自动停用", req.software, p.ID)
		p.Enabled = false
	}
	return cfg, cfg.BuildTable()
}
