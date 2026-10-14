# 更新日志

> 本文件由 [git-cliff](https://git-cliff.org/) 依据 Conventional Commits 自动生成
> （分组规则见 `cliff.toml`，发版流程见 `AGENTS.md`），**请勿手工编辑**。

## [0.1.3] - 2026-10-14

### 缺陷修复

- 安装器「立即运行」改经 explorer.exe 代启动
## [0.1.2] - 2026-10-13

### 缺陷修复

- 修正 voke.iss 的 WorkingDir 常量（{userprofile} 非 Inno Setup 合法常量）
- Voke.iss 的 WorkingDir 改用 Inno Setup 内建环境变量常量 {%USERPROFILE}

### 文档

- 优化 README——移除内部表述与截图占位、更新常驻内存为 60-70MB、补充 Ctrl+N/P 快捷键
## [0.1.1] - 2026-10-13

### 缺陷修复

- 修复安装包启动后终端 profile 工作目录异常
## [0.1.0] - 2026-10-13

### 新功能

- Voke 首个开源发布

### 缺陷修复

- 收尾修复首个开源发布(initial)遗留问题
- 在 windows build job 内启用 clippy 覆盖全 workspace 并修复告警
