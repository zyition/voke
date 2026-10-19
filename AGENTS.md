# AGENTS.md —— 进这个仓库干活必须知道的事

给人看架构/上手看 [`README.md`](README.md)；本文件只写**非通用**的纪律与踩坑。
正文一律简体中文；[`docs/HISTORY.md`](docs/HISTORY.md) 是环境事实与踩坑的**权威来源**，动手前先读。

## 仓库纪律

- 注释 / 文档 / commit message 一律**简体中文**。
- commit message 走 **Conventional Commits**：`feat:` / `fix:` / `perf:` / `refactor:` /
  `test:` / `docs:` 等英文前缀 + 中文描述（例：`feat: 支持 XXX`）。CHANGELOG 由
  git-cliff 按前缀生成，`style|build|ci|chore|revert` 不会进 CHANGELOG。
- **格式统一 `cargo fmt --all`**：仓库已用根目录 `rustfmt.toml` 统一格式化并保持 rustfmt-clean；改动提交前跑 `cargo fmt --all -- --check`（或 `cargo fmt --all`），CI 会强制检查，不通过无法合并。不要只局部格式化自己改的文件——整体一致性由根配置保证。
- **严禁编造 API**：gpui / gpui-pre / windows / tray-icon 的签名一律查
  `~/.cargo/registry/src/*/` 的源码，别按记忆写。
- `git add` 只加自己改的文件（**禁 `git add -A`**）；commit 用
  `git -c user.name="zyition" -c user.email="2321694+zyition@users.noreply.github.com" commit`，
  简体中文 3–5 行；**不 force push**。

## 分层铁律

- `voke-core` **不得**依赖 gpui / windows crate（保持可 Linux 原生秒级测试）。
- OS 调用一律进 `voke-sys` 且 `#[cfg(windows)]` 门控；非 Windows 侧
  `cargo test -p voke-sys` 必须保持绿（纯逻辑不挂 cfg，才能在 WSL 里测到）。

## WSL 交叉编译

- 系统是 **WSL2 + Linux 发行版**（不是 Ubuntu）。主目标 `x86_64-pc-windows-msvc`
  （cargo-xwin），gnu 目标用来 check。
- **cargo-xwin 需要真实 clang / llvm-lib 前置进 PATH**（工具管理器的 shim 会挡，报
  "No version is set for shim"）。安装 LLVM/clang/llvm-lib 工具链并把其 bin 目录
  前置到 PATH（bash 用 `export PATH=...`，fish 用 `set -gx PATH ... $PATH`）。
- shell 不一定是 bash：命令片段注意适配（如 bash 的 `export` / `$?` 在 fish 下会失败）。
- rustup 有镜像覆盖问题：硬编码 tuna 且无视 `RUSTUP_DIST_SERVER`，`rustup target list`
  可能漏报已装 target —— **直接试编译**，别信列表。
- Linux 原生 `cargo check -p voke-app` 会因缺 fontconfig（`fontconfig.pc`）环境性失败，
  **属预期，不要修**；`voke-app` 只面向 Windows。

## 验证命令组合

Go（当前主线，与 CI 同款）：

```bash
cd poc/voke-go
gofmt -l . && go vet ./... && go test -race -count=1 -shuffle=on ./...
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -tags mygo_noinspector -ldflags="-s -w -H windowsgui" -o /tmp/voke.exe .
```

Rust（旧版，代码暂留作参考，CI 已退役）：

```bash
cargo check --target x86_64-pc-windows-gnu -p voke-app --all-targets   # UI 改动验收（含测试代码）
cargo test -p voke-core     # 285
cargo test -p voke-sys      # 61
cargo clippy -p voke-core -p voke-sys -p voke-app --all-targets        # 新代码零警告
```

## 部署与安装包

- exe 部署到 Windows 侧的部署目录（如 `<WIN_DEPLOY_DIR>\voke.exe`）；旧 exe 被运行中实例锁住时先
  `mv` 改名绕过（Windows 允许改名运行中的 exe，只是不能删/覆写）。
- 安装器：`bash packaging/build-installer.sh`（调 Windows 侧
  `%LOCALAPPDATA%\Programs\Inno Setup 7\ISCC.exe`）。winget id 必须
  `JRSoftware.InnoSetup.7`（默认 id `JRSoftware.InnoSetup` 只到 6.x，scoop 也无 7）。

## 发版流程

推 `vX.Y.Z` tag 即触发 **Go 版**发版：`git tag v0.2.0 && git push origin v0.2.0` →
`.github/workflows/release.yml` 在 windows-latest 用 Go 构建 `poc/voke-go`，出安装包
（`voke-setup-<ver>.exe`）与 portable zip（`voke-vX.Y.Z-windows-x86_64.zip`）；ubuntu job
用 git-cliff 生成 `CHANGELOG.md` 并回推默认分支，建 GitHub Release 挂上两个产物。
Rust 的 `ci.yml` 与旧 Rust 发版流水线已退役（Rust 代码暂留作参考）。

版本号以 **tag 为唯一事实来源**：release 构建用
`-ldflags -X github.com/zyition/voke/poc/voke-go/internal/app.buildVersion=<ver>` 把版本注入
设置页「关于」（本地未注入显示「开发版」）；Go exe 的 VERSIONINFO 仍未注入（已知差距）。
ISCC 用 `-dVokeVersion` 覆盖安装器 AppVersion 与产物文件名（不 sed 改 `voke.iss`），本地
不用手动改版本文件。手动 `workflow_dispatch` 只出 artifacts、不建 Release，用于验证打包链路。

## 项目特有机制（一句话 + 指向文件）

- 单实例：命名互斥体 `Local\voke.instance` + `Local\voke.show` 事件
  （`voke-sys/src/single_instance.rs`）。
- 热键线程：Request/Reply 协议，改键不重启线程（`voke-sys/src/hotkey.rs`）。
- 托盘：子类化 `WM_INITMENUPOPUP`，在菜单弹出前刷新 awake 条目（`voke-sys/src/tray.rs`）。
- 设置窗「关闭 = 程序化销毁」（Zed 主窗同款姿势），回收 ~26.5MB；gpui 契约与源码位置见 `voke-app/src/settings.rs` 注册处长注释与 `docs/HISTORY.md` 第 6 节。
- 应用列表：唤起触发扫描 + 60s 防抖；窗口列表每次查询实时枚举（`voke-app/src/main.rs`）。
  图标两层缓存均有界：解码位图 256 条 LRU（`internal/ui/IconCache`）、PNG 字节 256 条
  FIFO（`internal/platform` 的 iconcache），nil 负缓存保留；面板隐藏 10s 后解码缓存
  裁到 64 条（Wox 同款，设置窗打开时跳过）。
- 日志容量：跨启动**追加**不清空；行数达上限时从**头部删一批**最旧的行
  （`max(1, N/10)`，默认 N=9999 → 100 行）并落一行中文标记行（计入行数）。
  上限由 `VOKE_LOG_MAX_LINES` 构建期注入（`VOKE_LOG_MAX_LINES=20000 bash
  packaging/build-installer.sh` 覆盖），逻辑与单测在 `voke-sys/src/logcap.rs`
  （放 voke-sys 是因为 voke-app 测试在 Linux 跑不了）。

## AI skill 约定（面向 AI agent）

- 本仓库**不再自带 skill**：gpui-kit 这类公共 skill 走**全局**安装，不入库。原因：skill 属于个人工具链，不应与项目版本绑定——入库只会增加维护负担，对贡献者也无必要。
- 开发本仓库（尤其涉及 gpui / gpui-component 界面、Provider 契约时），建议在全局安装 gpui-kit 相关 skill 作参考，本机已放在 `~/.agents/skills/` 供查阅。
- 不要在仓库内新增 `.agents/` / `.claude/` / `skills/` 之类的 skill 目录；相关能力靠全局 skill 提供即可。

## 当前遗留 / 已知问题（详见 `docs/KNOWN-ISSUES.md` 与 `docs/BACKLOG.md`）

- gpui-pre-windows 拖放目标泄漏：上游 issue 文稿待写；本仓用「设置窗程序化销毁」绕开（HWND 存活期内完成 RevokeDragDrop），上游 bug 仍在。
  用户**拒绝** vendor/patch 上游。
- 设置页 Group / Page 标题层级受 gpui-component 限制，无法自定义（未决策）。
- 默认热键 `alt+space` 仅对**全新配置**生效，不迁移老配置（预期行为，非 bug）。
- Linux native check 失败属环境（缺 fontconfig），非本仓代码问题。
