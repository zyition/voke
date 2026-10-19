# 贡献指南（CONTRIBUTING）

感谢你考虑为 **voke** 做出贡献。本文档说明如何在本仓库里高效、安全地开发，避免踩到我们已经踩过的坑。

## 一、项目是什么

**voke** 是一个键盘优先的 Windows 桌面启动器，用 Rust 编写。

- **Windows-only**：核心能力（全局热键、托盘、自启、窗口枚举、`.lnk` 解析等）都依赖 Win32 / COM API，只在 Windows 上能完整运行。
- **单 exe 零运行时**：最终产物是一个独立的 `voke.exe`，不依赖 .NET、不依赖浏览器、不后台常驻任何服务。
- **无遥测、无服务端**：程序不联网、不上报任何数据（详见 `SECURITY.md`）。

代码按三层组织，构建关系自上而下：

| 层 | crate | 职责 | 关键约束 |
| --- | --- | --- | --- |
| 逻辑层 | `voke-core` | 查询引擎、Provider 契约、拼音模糊匹配 | 零 UI 依赖、零 `windows` 依赖 |
| 系统层 | `voke-sys` | Win32 / COM 封装、unsafe 隔离区 | OS 调用集中于此，且 `#[cfg(windows)]` 门控 |
| UI 层 | `voke-app` | gpui-kit 界面与程序入口 | 仅面向 Windows |

## 二、开发前置

- **Rust stable**（edition 2024）。请用最新 stable 工具链。
  - **建议（已知良好）版本：`1.98.1`**。这是本仓库当前开发与审计时使用的版本；低于 `1.85` 不行（edition 2024 硬下限，见 `clippy.toml` 的 `msrv`）。
  - **本仓库刻意不提供 `rust-toolchain.toml`**：本机 `rustup` 存在镜像覆盖怪癖（硬编码 TUNA 且无视 `RUSTUP_DIST_SERVER`），一旦 pin channel 会让本机 `cargo` 直接跑不起来。故版本约束只用 `clippy.toml` 的 `msrv` + 此处的文字说明来表达，统一工具链版本交给 CI（GitHub `dtolnay/rust-toolchain@1.98.1`）保证。
- **Windows 目标编译**：
  - 在 Windows 本机：安装 **MSVC** 构建环境（Visual Studio Build Tools 的 "C++ 桌面开发"  workload）即可。
  - 在 Linux / WSL 交叉编译：需要 **LLVM/clang + llvm-lib**，走 **cargo-xwin**（`x86_64-pc-windows-msvc`）。注意 cargo-xwin 需要真实 clang/llvm 前置进 `PATH`，mise 的 shim 会挡住它。
  - 仅做类型检查（不产出 exe）时，也可以临时用 gnu 目标：`x86_64-pc-windows-gnu`。
- **Inno Setup 7**：仅当你要**打包安装程序**时才需要（参考 `packaging/` 目录）。日常开发与测试不需要。

> 更完整的环境事实（镜像源、rustup 怪癖、字体库缺失等）见 `docs/HISTORY.md`。

## 三、常用命令

```bash
# 逻辑层测试（纯 Rust，零 OS 依赖，可在 Linux/WSL 秒级跑通）
cargo test -p voke-core

# 系统层测试（非 Windows 侧只测纯逻辑部分，COM/Win32 部分被 cfg 门控掉）
cargo test -p voke-sys

# UI 改动验收：类型检查 app 层（含测试代码），用 gnu 目标即可，
# 不需要真去编 MSVC / 链接 Windows 资源
cargo check --target x86_64-pc-windows-gnu -p voke-app --all-targets

# 新代码零 Clippy 警告（core + sys；app 层按需要加 --all-targets）
cargo clippy -p voke-core -p voke-sys --all-targets
```

> **重要环境事实**：在 Linux 原生直接跑 `cargo check -p voke-app` 会因为缺少 `fontconfig`（`fontconfig.pc`）而**环境性失败**，这是预期行为，**不要去修**。`voke-app` 只面向 Windows，UI 改动请用上面的 gnu 目标 check 来验收。

## 四、分层铁律（务必遵守）

- **`voke-core` 不得依赖 `gpui` / `windows` crate**。保持它纯净，才能在 Linux 上原生、秒级跑测试。
- **所有 OS 调用一律进 `voke-sys`**，并用 `#[cfg(windows)]` 门控；非 Windows 侧要保证 `cargo test -p voke-sys` 仍然是绿的（纯逻辑不挂 cfg，才能在 WSL 里测到）。
- 不要把 Win32 细节漏到 `voke-core` 或 `voke-app`，系统层是唯一的 unsafe 隔离区。

## 五、严禁事项

1. **格式必须 `cargo fmt --all` 一致**：仓库已用根目录 `rustfmt.toml` 统一格式化并保持 rustfmt-clean。提交前请运行 `cargo fmt --all -- --check`（或 `cargo fmt --all`）确保新增改动符合统一风格；CI 会强制检查，不通过则无法合并。不要只局部格式化自己改的文件。
2. **严禁编造 API**：涉及 `gpui` / `gpui-pre` / `windows` / `tray-icon` 的签名，一律去 `~/.cargo/registry/src/*/` 里查对应 crate 的源码，按真实签名写，**不要凭记忆**。

## 六、Commit 规范

提交信息采用 **Conventional Commits + 中文描述**，例如：

- `feat: 支持按拼音首字母模糊匹配`
- `fix: 修复托盘菜单在空列表时崩溃`
- `perf: 优化查询引擎的候选排序`
- `docs: 补充贡献指南`
- `refactor: 抽离热键线程的请求/应答协议`

类型常用：`feat` / `fix` / `perf` / `refactor` / `docs` / `test` / `chore`。

- **CHANGELOG 由 [git-cliff](https://github.com/orhun/git-cliff) 自动生成，不要手改 `CHANGELOG.md`**。
- 注释、文档、commit message 一律使用**简体中文**。

## 七、PR 要求

- **小步提交**：一个 PR 只做一件事，方便 review 与 bisect。
- **跑通验收命令**：至少 `cargo test -p voke-core` 与 `cargo test -p voke-sys` 要绿；若动了 UI，补 `cargo check --target x86_64-pc-windows-gnu -p voke-app --all-targets`；新代码 `cargo clippy` 零警告。
- **不改无关代码**：不要顺手重构、不要格式化无关文件、不要调整你不负责的依赖版本。
- 涉及安全相关变更（自启、外部程序执行、配置读写等）请同步更新 `SECURITY.md` 的威胁面描述，并在 PR 里说明。

## 八、本地目录结构速览（节选）

```
voke-core/   逻辑层：查询引擎、Provider、拼音匹配
voke-sys/    系统层：Win32/COM、单实例、热键、托盘、自启
voke-app/    UI 层：gpui-kit 界面、程序入口、设置窗
packaging/   安装包脚本（Inno Setup 7）
docs/        环境事实与设计文档（见 docs/HISTORY.md）
```

更多背景请阅读根目录 `README.md` 与 `docs/` 下的文档。
