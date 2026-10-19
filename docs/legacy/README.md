# 历史存档（Rust / gpui 与 Go PoC 阶段）

> 这里保存 **Go + mygo 成为主线之前**的架构、环境、内存与迁移记录，仅作历史参考。
> 当前实现与开发方式请看：
>
> - [README](../../README.md) —— 产品说明、安装、构建、插件
> - [架构](../architecture.md) —— Go + mygo 现行架构
> - [贡献指南](../../CONTRIBUTING.md) —— 开发环境、分层、验证命令
> - [已知问题](../KNOWN-ISSUES.md) / [待办](../BACKLOG.md) —— 当前限制与未来工作
> - [内存现状](../notes/memory.md) / [匹配一致性](../notes/matching.md) —— 从旧文档沉淀的当前经验
>
> 仓库根目录的 `voke-core/`、`voke-sys/`、`voke-app/` 与 `Cargo.toml` 等 Rust 代码
> **保留作参考实现**，不再参与 CI 与发版。

## 文件

| 文件 | 内容 |
|---|---|
| [`rust-architecture.md`](rust-architecture.md) | Rust/gpui 时代的架构、Provider 契约、技术选型与里程碑 |
| [`rust-history.md`](rust-history.md) | Rust 时代的构建环境、主题系统、验证踩坑与设置窗销毁档案 |
| [`rust-headless-evaluation.md`](rust-headless-evaluation.md) | gpui VSync/Headless 方案评估（未实施） |
| [`rust-memory-footprint.md`](rust-memory-footprint.md) | Rust/gpui 版内存测量与优化杠杆分析 |
| [`rust-settings-plugins.md`](rust-settings-plugins.md) | Rust 版设置页 / 插件配置 / 托盘 / 自启施工文档 |
| [`voke-go-poc.md`](voke-go-poc.md) | Go 迁移期的 PoC 决策、真机验证、pinyin/nucleo parity 与遗留汇总 |

## 阅读提示

- 这些文档中的内部相对链接按**移动前路径**书写，可能失效。
- 其中的命令行、crate 名、文件路径、内存数字均对应旧实现，不要照做。
- 仍有价值的技术结论（pinyin/nucleo 语料一致性、内存口径、Windows 环境坑）已在
  当前文档中重写保留；需要细节时再回到本目录。
