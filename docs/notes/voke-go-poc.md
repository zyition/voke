# voke-go PoC：用 MyGo 重写 voke 的决策与施工依据

> **本文档定位**：给下一个接手的 agent 的**唯一权威施工依据**。结论优先、
> 过程史已删、口径唯一。MyGo 的 API 一律以官网文档（mygo.egoist.dev）与
> 仓库源码（github.com/egoist/mygo）为准；本文引用过的均已核对原文，
> 没引用到的**先查后写，不许拍脑袋**（AGENTS.md 铁律）。
>
> **硬约束**：简体中文；PoC 与生产 voke **并存运行不得互相干扰**；产物名
> `voke-go`；默认热键 `Ctrl+Alt+Space`；**不改 Rust 侧任何文件**。
>
> **章节地图**：
> §1 决策记录（用 MyGo 重写，2026-10-16） · §2 项目位置与工作方式 ·
> §3 环境坑位 · §4 范围与隔离 · §5 实测数据 · §6 真机验证清单 ·
> §7 重写期设计约束 · §8 风险与已知不确定项 · §9 附录：MyGo v0.5.5 API 面 ·
> §10 当前进度、待决策与遗留汇总
>
> **两条唯一性纪律（读之前先记住）**：
> 1. **§5 是所有数字的唯一出处**。§1 / §6 / §7 / §8 引用内存、语料、
>    二进制大小的具体数值时一律写「见 §5.x」，不复制数字。
> 2. **§10 是待决策与遗留的唯一出处**。§7 正文只陈述已定案的设计，
>    不复述「已拍板」的进度汇报。

---

## 1. 决策记录：用 MyGo 重写

**决策日期**：2026-10-16 · **判定**：**Go**（进入重写分期规划）

### 1.1 依据（数据全部见 §5，此处不复制数值）

| # | 依据 | 结论 | 出处 |
|---|---|---|---|
| 1 | 冷启动地板 56.5MB vs 主线冷启动中位 108.7MB | **低 52MB**，且 MyGo 侧地板更低 | §5.1 / §5.2 |
| 2 | 私有工作集 43.5–48.1MB vs 主线 73.2MB | **省约 40%** | §5.2 |
| 3 | 交付二进制 11.1MiB（剥符号） | 与生产 voke 同量级，工程链无退化 | §5.3 |
| 4 | 105s 空闲 PrivateCommit 净 −0.3MB、句柄线程零增长 | **无泄漏** | §5.1 |
| 5 | 140 条应用枚举 +2.2MB / 205ms | 「应用信息缓存占大头」的假设被否 | §5.1 |
| 6 | 匹配引擎语料逐分断言一致 | 命脉（V2）有把握 | §5.3 |

**必须同时记住的限定条件**（否则决策报告会误导）：

- **三个口径结论不一致**：PrivateCommit 口径下 **voke-go 反而高 12MB**
  （95.6 vs 107.6），只有私有工作集口径明显省。见 §5.2 的口径对照表。
- **功能不对等**：PoC 只有 apps 一个 provider，**无托盘、无设置窗、无
  awake、无 frequency**；主线是完整产品。拿 PoC 的数字代表「重写后的
  voke-go」会系统性偏乐观。**任何对外引用必须带上这句**。

### 1.2 决策门槛的修正（重要：原稿的「稳态」未定义，已重写为双门槛）

原稿只写「稳态 ≤70MB」一个数，「稳态」既可指冷启动未上屏、也可指唤起后
静置，两者差 50MB 以上，导致结论可被任意解读。**现按双门槛判定**：

| 门槛 | 口径 | 阈值 | 实测 | 判定 |
|---|---|---|---|---|
| **地板** | 冷启动、**面板未上屏** | ≤70MB | 56.5MB | ✅ 过 |
| **唤起后静置** | 唤起一次后静置 | ≤ 主线同状态中位 135.0MB | ~107.6MB | ✅ 过 |

**「稳态」在本文一律指「唤起面板后静置」**，不是冷启动地板。地板是另一
个门槛，用来说明「启动即付出的框架代价」。

### 1.3 最终判定与保留条件

- **判定 Go**：双门槛均通过，且 §5.1 的三条否证结论（Vibrancy / MSAA /
  交换链）证明剩下的开销是 **D3D11 + DirectWrite 的框架地板**，与 MyGo
  实现质量无关——这既是 Go 侧的天花板，也说明「继续在 Rust 侧调优地板」
  是同一场仗。
- **Rust 侧暂不删**：`voke-core` / `voke-sys` / `voke-app` 原地保留到重写
  真正落地为止。**退路不需要 patch 上游**——`docs/notes/memory-footprint.md`
  档 1「隐藏即缩窗（1×1）」即可回收 26.5MB，与 MyGo 无关。若重写中途中止，
  直接走这条退路。
- **匹配引擎是唯一「值得一比一复制」的地方**：其余按 §7.1 的等价判据允许
  替换实现；匹配引擎保留 1:1，因为 V2 命脉就是「命中与否与分值一致」。

### 1.4 未决前提（唯一阻塞重写开工的项）

**MyGo 生态 bus factor = 1**（egoist 399 commits vs 次高 12）。若上游停维护
则触发重估，见 §8.1。此项是**产品决策不是技术决策**，见 §10 待决策表。

---

## 2. 项目位置与工作方式

| 项 | 值 |
|---|---|
| **worktree 绝对路径** | `/home/loiac/Work/projects/personal/voke/.codebuddy/worktrees/voke-go` |
| **分支** | `poc/voke-go`（**本地已提交，未 push**） |
| **提交** | `7120214` feat 初始实现 + `cab3a01` docs；本轮第三波改动**未提交** |
| **代码落点** | `poc/voke-go/`（独立 Go module） |
| **Rust 侧** | **零改动**（`cargo fmt` 不碰 `.go`，CI 只对 main/master 触发） |

> ⚠️ **Go module 在子目录，所有 Go 命令必须 `cd poc/voke-go` 之后跑**，
> 从仓库根跑**不生效**。

### 2.1 构建 / 测试 / 静态检查

```bash
cd poc/voke-go

# 交付构建（唯一口径：GUI 子系统 + 剥符号 + 剔除 inspector）
GOOS=windows GOARCH=amd64 go build -tags mygo_noinspector \
  -ldflags="-s -w -H windowsgui" -o voke-go.exe .

# 测试 / 静态检查（全部 Linux 原生可跑）
go build ./...
go test -count=1 ./...
go vet ./...
gofmt -l .                                       # 必须为空
GOOS=windows GOARCH=amd64 go vet ./...
```

`internal/match` 的测试数据是 `testdata/rust_corpus.tsv`（由 path 依赖
voke-core 的 Rust 程序生成，生成物不入库）。

### 2.2 部署

- 目标目录 `%USERPROFILE%\voke-go-poc\`，**严禁覆盖生产 `voke.exe`**。
- 生产 exe 被运行中实例锁住时先 `mv` 改名绕过——Windows 允许改名运行中
  的 exe，只是不能删/覆写（AGENTS.md 纪律）。
- `.gitignore` 已含 `*.exe`，构建产物天然不入库。

### 2.3 日志与内存口径

- 日志：`%APPDATA%\voke-go\voke-go.log`（跨启动**追加**）。
- `mem.go` 打点格式与 `voke-sys/src/mem.rs` 逐字一致：
  `内存快照 [tag]：WorkingSet=x.xMB PrivateCommit=y.yMB`。
  现有 tag：`启动`、`引擎索引完成`。
- **三个内存口径的结论不一致，引用必须带口径**：

  | 口径 | 含义 | 谁更省 |
  |---|---|---|
  | PrivateCommit | `mem.rs` / `mem.go` 快照口径（提交量） | **主线**（见 §5.2） |
  | WS | 工作集（含共享页） | 差异小 |
  | **私有工作集** | 任务管理器「内存」列 | **voke-go 省 40%** |

  **本仓谈「地板」历来用 PrivateCommit**，但「用户实际感觉到占多少」是私有
  工作集。两者指向不同结论，**对外引用时必须写明是哪一个**。

### 2.4 单实例与热键

- 热键 `Ctrl+Alt+Space`（生产是 `alt+space`，两者并存互不触发）。
- MyGo 单实例按 **app id 字符串**锁定（底层是 unix socket 文件，不是命名
  互斥体），与生产的 `Local\voke.instance` **机制不同 → 可以并存 A/B**。
  PoC 另显式 `SetName("voke-go")` 双保险。源码核实见 §8.2。

---

## 3. 环境坑位

1. **WSL 调 Windows PowerShell 必须 `< /dev/null`**：`pwsh` 与
   PowerShell 5.1 均复现无限挂起。输出分隔符**别用反引号** `` `t ``
   （bash 当命令替换），改用 `||`。
2. **构建三件套缺一不可**：`-H windowsgui`（缺了弹控制台窗口）、
   `-s -w`（剥符号，交付口径）、`-tags mygo_noinspector`（inspector
   常驻内存）。
3. **勿用裸 `go build -o voke-go.exe`**——历史上出过丢 GUI 子系统的事故。
   构建命令以 §2.1 为唯一口径。
4. **fish 是默认 shell**，命令片段用 `bash -c '...'` 包裹；fish 不支持
   heredoc，脚本压成单行；引号用外层单引号 + 内层 `\"` / `\$`。

---

## 4. 范围与隔离

### 4.1 做什么（最小可验证闭环）

面板（搜索框 + 结果列表 + 图标行）→ `Ctrl+Alt+Space` 唤/隐 → 失焦隐藏
→ Esc 两级语义（先清空查询，再隐藏）→ Enter 启动选中项 → 托盘菜单
（显示面板 / 退出）→ 第二实例激活首实例 → **匹配引擎 1:1 移植**
（`search_key.rs` 多音字展开 + `SearchKeysCache` + 拼音数据包 + 自研移植
的 nucleo 评分）→ 内存快照打点（口径对齐 `mem.rs`）。

### 4.2 明确不做（防范围蔓延）

- 8 个 provider 只做 apps；timestamp / base64 / uuid / random /
  calculator / awake / windows / terminal / workspace 一律不做。
- 不做设置窗、配置系统、频率学习、日志容量管理。
- 不做安装器、开机自启动、自动更新。
- 匹配引擎**全量移植、不砍子集**（早前「不做完整拼音引擎」的方案作废）。
- 不改 Rust 侧一行代码，不碰 `%APPDATA%\voke\` 任何文件。

### 4.3 与生产 voke 的隔离（并存 A/B 的前提）

| 维度 | 生产 voke | voke-go（PoC） |
|---|---|---|
| 二进制 | `voke.exe` | `voke-go.exe` |
| 默认热键 | `alt+space` | `ctrl+alt+space` |
| 单实例 | 命名互斥体 `Local\voke.instance` | MyGo socket 锁，按 app id 字符串（见 §2.4） |
| 数据目录 | `%APPDATA%\voke\` | `%APPDATA%\voke-go\` |
| 日志 | `voke.log`（用户在日常看，**严禁污染**） | `voke-go.log` |
| 图标缓存 | `%APPDATA%\voke\icons\`（322 个 PNG） | 不落盘，进程内 FIFO 有界缓存（上限 512） |
| 安装方式 | 安装器 + 可选自启动 | 绿色便携，手动启动，不进自启动 |

**四个隔离点 = 二进制 / 热键 / 数据目录 / 单实例 id**。生产 voke 与
voke-go **可以同时运行**（单实例机制不同，见 §8.2）。

---

## 5. 实测数据

> **本章是全文所有数字的唯一出处。** 其它章节一律写「见 §5.x」。

### 5.1 voke-go 内存三版演进（同机实测，2026-10-16）

三个版本是同一份代码的逐轮收敛，每轮只动一个变量：

| 版本 | 启动 PrivateCommit | 引擎索引增量 | 面板上屏阶跃 | 峰值 | 私有工作集 |
|---|---|---|---|---|---|
| v1（Vibrancy 开） | 84.7MB | +1.5MB | — | 131.8MB（28.6min） | — |
| 中间版（关 Vibrancy） | 57.0MB | +1.8MB | +53.9MB | 119.7MB | 48.0MB |
| **最终版**（+`noinspector` + GPU 诊断） | **56.5MB** | **+2.2MB** | **+53.2MB** | **112.4MB（无超调）** | **43.5–48.1MB** |

**四条可直接写进决策报告的结论**：

1. **140 条应用枚举不是内存大头**：`+2.2MB` / 耗时 **205ms**，与地板差
   30 倍。「应用信息缓存占大头」的假设**已排除**。
2. **无泄漏**：105 秒空闲窗口 PrivateCommit 净 **−0.3MB**，句柄数与线程数
   **零增长**。
3. **面板上屏 +53.2MB 中 Go 侧只占 +8.0MB**（`HeapSys` 7.7→15.7MB），
   `HeapReleased` 冻结在 3.7MB → **约 85% 在 Go 堆外**：D3D11 设备 +
   DirectWrite 字体集合 + 着色器编译 + 驱动侧分页池。
4. **设备是硬件 GPU**：`Accelerated = true`（非 WARP），排除软件光栅。

**上屏阶跃是 D3D11 + DirectWrite 的框架地板，与 MyGo 实现质量无关**——
这正是 Rust 侧 `memory-footprint.md` §4 说的「要低于 104MB 必须动框架地板」，
在 Go 侧同样成立。

**三条否证结论（别再在这三条上花时间）**：

- **Vibrancy 是隐形杀手（已定因，已关）**。链路：`WindowOptions.Vibrancy != ""`
  → `internal/windows/window.go:131` `noRedirect = true` → 加
  `WS_EX_NOREDIRECTIONBITMAP` → `ui/gpu_windows.go:14` `n.Composed` 渲染切
  Composed → `internal/windows/compositor.go:107-129` **无条件多建一整套
  D3D11 + DComp 设备**。实测地板 84.7 → 56.5MB，省 **27.7MB**。
  两条性质：**16 个档位成本完全相同**（`window.go:131` 只判 `!= ""`，不看
  是哪个材质，**没有「换便宜档位」这条退路**）；**不可逆**（源码注释明写
  "The style is the window's for life"，只能启动时读配置）。
- **无 MSAA**。mygo v0.5.5 全模块 `SampleCount` 四处硬编码 1
  （`internal/gpu/d3d11/d3d11.go:483`/`:654`/`:1148`、
  `offscreen.go:55`），抗锯齿在着色器里用 SDF 解析做
  （`internal/raster/raster.go:3-5`）。Rust 侧
  `memory-footprint.md` 杠杆 2 想省的那笔，**mygo 天生就没有**。
- **交换链无可调空间**。稳态 2 缓冲 × 精确尺寸（`resizeBuffers = 3` 只在
  resize 中用，面板 `DisableResize: true` 永不触发）；离屏 backdrop /
  blur target 在关掉 Vibrancy 后一张未建；图集四类合计仅 5.4MB，
  **解释不了 46MB**。
  ⚠️ `device.go:36-49` 的 hardware → warp 回退是**静默的、无任何日志**，
  换机器 / 远程桌面 / 虚拟机时**必须重测**，别拿本文数字当定论。

### 5.2 主线 voke 基线与同状态 A/B

**全量日志（2026-10-12~16，412 条快照 / 38 次启动）**：

| 口径 | 数值 |
|---|---|
| PrivateCommit 中位 | **108.7MB** |
| PrivateCommit 均值 | 110.6MB |
| PrivateCommit 峰值 | **181.0MB** |
| 窗口创建即锁在 ≥105.6MB 的启动次数 | **38 / 38** |

**静置态是双峰分布**：

- 低峰 **91~94MB**，仅出现 **4 / 16** 次，且**全是「本次会话从未唤起过
  面板」**的特例；
- **唤起后静置中位 135.0MB**（n=16 的高峰段 140.3~144.1）。

按标签拆：面板首次唤起 123.5MB / 设置窗打开 **154.9MB** / 设置窗关闭
+10s **135.0MB（中位）**。历史峰值 181.0MB 出现在**设置窗程序化销毁的那
一瞬间**——renderer 延迟数十秒才归还。

> ⚠️ **引用主线数字必须带口径**：此前出现过的 93.7 / 94.1 / 94.8MB 是
> **双峰的谷底**（只在「本次会话从未唤起过面板」时出现，16 次里仅 4 次），
> **不能代表主线典型值**。主线典型值只有两个数：**冷启动地板中位
> 108.7MB / 唤起后静置中位 135.0MB**。

**同状态实时 A/B**（两进程 `MainWindowHandle` 均为 0，窗口都关）：

| 口径 | voke（主线） | voke-go（PoC） | 谁更省 |
|---|---|---|---|
| **PrivateCommit** | 95.6MB | **107.6MB** | **主线反高 12MB** |
| WS | 145.5MB | 119.9MB | voke-go |
| **私有工作集** | 73.2MB | **43.5MB** | **voke-go 省 40%** |

> ⚠️ **两条必须同时说出口的限定**：
> 1. **PrivateCommit 口径下 voke-go 并不省，反而高 12MB**；只有私有工作集
>    口径明显省。三个口径结论不一致（见 §2.3）。
> 2. **功能不对等**：PoC 只有 apps 一个 provider，**无托盘、无设置窗、无
>    awake、无 frequency**；主线是完整产品。用一个「功能少一半的实现」去
>    比它赢不了的指标，结论会误导——所以门槛改用双门槛（见 §1.2）。

**对比原始动机的还原**（2026-10-16 排查，主线日志 `%APPDATA%\voke\voke.log`）：
一次 25h 未重启的会话里，`引擎索引完成` 后 PrivateCommit 108.9 → 设置窗
打开 164.6 → 关闭 +10s **144.1（约 50MB 没回落）** → 再开关一轮 179.8
（其间 5.5h 纯开关面板爬 +35.7MB）。判定**不是经典无界泄漏**（数字低于
峰值、冷启动地板恒定、那 50MB 最终确实释放），但两个真实可疑点造成了
观感上的 110MB：① 设置窗关闭后的释放时机不可控（gpui 上游异步 Drop 迟到，
本仓「程序化销毁」只保证最终成功、不保证时机，`settings.rs:398-415`）；
② 每次唤起触发全量重扫（110 个 `.lnk` + 62 个 AppsFolder COM + 135 次
图标 + 拼音索引，堆只提交不归还）。
**用户当时观测到的「稳态 110MB」是单次会话的瞬时峰值，不是稳态**——
`110` 这个数字不要再作为主线典型值引用。

### 5.3 二进制与匹配引擎语料

| 项 | 值 |
|---|---|
| 交付口径（剥符号） | **11,648,000 字节（≈11.1MiB）** |
| 未剥符号 | 16,180,224 字节（≈15.4MiB） |
| 构建命令 | 见 §2.1（**唯一口径**） |

对标 MyGo 宣称的 ~7MB 仍超一截，超量 = mygo 框架本体 + go-pinyin 字典 +
匹配引擎 Unicode 表（拆解留重写决策轮）。构建过程中出现过的
14,440,960 / 14,459,904 / 14,557,696 是各波**未剥符号**的中间值，已作废。

**匹配引擎语料断言**：

| 项 | 值 |
|---|---|
| 语料规模 | 74 名 × 185 查询 = **13,690 条** Rust 真值逐分断言 |
| 完全一致 | **12,580 条** |
| 跳过 | 1,110 条（6 个含退化分歧字的名字） |
| 覆盖路径 | Fuzzy / Substring 双路径、DP 与 greedy 双分支（超长输入触发 `MatrixSlab` 分配失败）、单/多 atom、ASCII/汉字/混合 query、空与纯空白 query |
| `SCORE_SATURATION` | **维持 200.0**（Rust 侧实测依据直接沿用：完整匹配 166 / 218，首字母串 62，弱匹配 36–62） |

`SCORE_SATURATION` 的标定逻辑：移植保住了 nucleo 原始分公式（u16 分量级
不变），故 Rust 侧的分值可直接沿用，取 200 让满匹配接近 1.0 且保留区分度。

**★ MyGo 宣称值仅供对照，不可当承诺**：官方 counter demo 宣称 native UI
常驻 ~44MB。该值来自空 counter 场景，**不是 voke 负载**（140 应用 + 图标 +
拼音索引 + 日志）的实测；voke-go 的实测地板见 §5.1。**任何地方引用
「44MB」时必须带上这句限定。**

---

## 6. 真机验证清单

> 本章是**可执行版**：部署 → A/B → 逐项操作步骤 → 结果记入 §6.8 表。
> V8（构建链）已在 Linux 侧完成，见 §5.3。
> **V1 是核心门槛**（判定口径见 §1.2）。

### 6.0 部署与 A/B 前提（一次性）

1. **构建**（交付口径）：

   ```bash
   cd poc/voke-go && GOOS=windows GOARCH=amd64 go build \
     -tags mygo_noinspector -ldflags="-s -w -H windowsgui" -o voke-go.exe .
   ```

2. **部署**到 `%USERPROFILE%\voke-go-poc\voke-go.exe`——**不要覆盖生产
   `voke.exe`**；被运行中实例锁住时先 `mv` 改名再拷。
3. **隔离自检**（启动后逐条确认，见 §4.3）：进程名 `voke-go`；热键
   `Ctrl+Alt+Space`（生产 `alt+space` 应互不触发）；日志落
   `%APPDATA%\voke-go\voke-go.log`（**严禁污染** `%APPDATA%\voke\`）；
   托盘 tooltip 为 `voke-go`。
4. **A/B 同跑**：生产 `voke.exe` 保持运行，两个二进制同时跑（单实例机制
   不同，见 §8.2）。
5. **内存采样**（PowerShell，**必须 `< /dev/null`**，见 §3）：

   ```powershell
   Get-Process voke, voke-go | ForEach-Object {
     "{0,-8} WS={1,6}MB  PM(commit)={2,6}MB  WS-P={3,6}MB  Handles={4}  Threads={5}" -f `
       $_.ProcessName, [math]::Round($_.WorkingSet64/1MB,1), `
       [math]::Round($_.PrivateMemorySize64/1MB,1), `
       [math]::Round($_.WorkingSet64/1MB,1), $_.HandleCount, $_.Threads.Count
   }
   # 私有工作集用性能计数器：
   # Get-Counter '\Process(voke*)\Working Set - Private'
   ```

   **三个口径必须同时记**（见 §2.3），只报一个会误导。

6. **日志交叉验证**：`voke-go.log` 的两条快照行与 PowerShell 采样对读。

### 6.1 V1 内存（核心门槛）

- **方法**：同机同时运行两个二进制；都冷启动、都加载完全部应用 + 图标 +
  匹配引擎（含拼音索引）；唤起面板一次后**静置 5min**；按 §6.0 第 5 步采样。
- **通过标准**：见 §1.2 的**双门槛**（地板 ≤70MB 且唤起后静置 ≤135.0MB）。
- **步骤**：
  1. 两边都退出（托盘→退出），等 10s 让内存回落；
  2. 启生产 voke，`alt+space` 唤一次面板（确保索引完成）后隐藏；
  3. 启 voke-go，`Ctrl+Alt+Space` 唤一次（等日志出现「引擎索引完成」
     且图标行渲染过）后隐藏；
  4. 静置 5min，跑采样命令，记录**三个口径**的 WS / PM / WS-P / Handles /
     Threads；
  5. 读 `voke-go.log` 两条快照行交叉验证；
  6. 记录：地板=___ / 唤起后静置=___ / 差值=___。
- **对应风险**：MyGo 的宣称值来自空 counter 场景，非 voke 负载（见 §5.3
  末的限定）；Immediate-mode 每帧重建 view 的常驻成本未知。

### 6.2 V2 中文 IME + 匹配行为（命脉，硬失败即 No-Go）

- **方法**：
  1. **IME**：微软拼音 / 清风输入法（用户日常在用的）下输入拼音 → 选字
     上屏 → 查询命中；composing 期间按 Enter / Escape 应归输入法。
  2. **匹配四件套**：`wx` / `weixin` / `微信` 三路命中；多音字「音乐」
     同时命中 yinyue / yinle（可用「网易云音乐」验）；ASCII 原名 `vsc`
     找 VS Code；极端输入「重重重」不炸（上限 8 组）。
- **通过标准**：IME 行为与生产 voke 一致（上字正常、composing 期间快捷键
  不穿透、Esc 先清空不关面板）；匹配语义四项全部一致（排序细节差异可接受）。
- **步骤**：切微软拼音 → 唤面板 → 逐条输入四件套 → 记录命中项与排序 →
  composing 期按 Enter/Esc 记录行为 → 换清风重复 → 生产 voke 同输入对照。
- **对应风险**：IME 有文档承诺且 Windows 侧 IMM32 实现已核实（见 §8.2），
  但**本仓零实测**——候选框位置、跨屏等边界情况要记录。

### 6.3 V3 应用枚举覆盖

- **方法**：PoC 枚举结果 vs 生产 135 条（日志口径：开始菜单 110 个 `.lnk`
  + AppsFolder 62 条 − 同名去重 25）。重点 MSIX/Store 应用（无 `.lnk`，
  如 Windows Terminal）——源码层已核实枚举底层就是 `FOLDERID_AppsFolder`
  COM（见 §8.2）。
- **通过标准**：差异 ≤5 条且可解释；超出则记录缺口清单，评估重写阶段补
  枚举的成本。
- **步骤**：
  1. 读 `voke-go.log`「应用枚举完成：N 条（原始 M，耗时 T）」，与生产比；
  2. 面板翻看前 50 条（非空查询上限），验证常见应用在位；
  3. 人工核对 MSIX/Store 应用在不在；
  4. 重点核对「同名不同 ID」条目（去重规则 2 未搬）；
  5. 需要逐条 diff 时临时在 `view.go` 加全量导出日志（验完还原）。
- **顺带**：记录启动成功率（PoC 恒走 `shell:AppsFolder\<ID>` 路径形态，
  生产对 `.lnk` 解析出真实 exe 路径）。

### 6.4 V4 图标

- **方法**：`Shell.FileIcon(path, 32×scale)` 现取（不落盘）vs 生产 32×32
  落盘缓存；列表滚动时图标获取不掉帧。
- **通过标准**：观感不退化；单次获取毫秒级；内存增量可忽略（并入 V1 观测）。
- **步骤**：唤面板 → 快速上下滚动 → 记录掉帧 / 闪白 / 占位头像出现情况；
  100% 缩放下与生产 voke 同应用图标逐一对照清晰度（应同规格）。

### 6.5 V5 面板行为

- **方法**：显隐无首帧闪烁；失焦隐藏可靠；Esc 两级语义；125% DPI 不错位；
  主显示器 WorkArea 内水平居中 + 垂直锚**面板中心 48%**（§7.6）
  定位；**Win11 Acrylic 降级表现必须记录**（`Transparent`/`Vibrancy` 文档
  标注仅 macOS + Win11 22H2+；**Win10 上的表现若无法验证就写「未验证」**）；
  可见行数复核（面板 680×494、期望 8 行，chrome 为估值）。
  **定位推导见 §7.6。**
- **通过标准**：以上全部达到生产 voke 同等体验。
- **步骤**：逐条勾验；**多显示器专项**（务必验）：
  1. 指针在**主屏** → 热键唤起 → 面板落在主屏、尺寸正确；
  2. 指针拖到**副屏** → 热键唤起 → 面板**仍落主屏**、尺寸正确、不越出
     主屏 WorkArea；
  3. 主副屏缩放不同（如主 125% / 副 100%）时重复 1、2；
  4. 与生产 voke 在同一双屏环境下并排唤起，核对落屏与尺寸一致。
  125% DPI 下另重复显隐 / 定位 / 滚动四项。
- **对应风险**：`Vibrancy` 的 16 个档位成本相同且不可逆（见 §5.1），所以
  **Win10 上不能靠换材质档位补救**，只能降级为纯色 / 不透底。

### 6.6 V6 热键

- **方法**：`GlobalShortcut.Register("Ctrl+Alt+Space", …)`（底层
  `RegisterHotKey`）注册 / 唤起 / 隐藏；与生产 `alt+space` 并存互不触发；
  改键路径只验证 API 可用（`Unregister` 旧 + `Register` 新）。
- **通过标准**：注册成功、响应及时、并存不冲突；注册失败（被占用）有
  可感知反馈。
- **步骤**：`Ctrl+Alt+Space` 唤 / 隐 ×3；同步验生产 `alt+space`；用别的
  程序占用 `Ctrl+Alt+Space` 后重启 voke-go，确认日志有「注册热键失败」且
  启动不阻断（**当前只记日志**，「可感知反馈」是否要补待真机确认）。

### 6.7 V7 托盘 + 单实例

- **方法**：托盘图标 + 菜单（显示面板 / 退出）；第二实例启动 →
  `OnSecondInstance` 激活首实例；动态菜单刷新时机（PoC 无 awake 条目，只
  验证「菜单每次打开用 Go 现构建」是否成立——成立则 awake 刷新需求天然
  满足，对应生产的 `WM_INITMENUPOPUP` 子类化）。
- **通过标准**：三项全部可用。
- **步骤**：托盘菜单点「显示面板」×2；保持首实例运行再启一个 `voke-go.exe`，
  确认首实例面板唤出且第二实例退出；反复开关菜单确认现构建响应。

### 6.8 结果记录表（**唯一一份**）

| 项 | 结果 | 数据 / 备注 | 日期 |
|---|---|---|---|
| V1 内存 | 部分数据 | 索引后 WS=88.0/PvtCmt=97.4MB GoHeap=4.6；反复唤起 WS ~110→峰 142→GC 稳 ~133–141，Objects 峰 70k→GC 回 8.6k。**Go 稳态高于生产 ~108MB**，§1.2 双门槛待复核。**同跑瞬时 A/B 快照（2026-10-18 11:06，agent `Get-Process` 只读、用户 V-pass 期间两进程并跑）**：voke WS=145.2/PvtCmt=95.4 vs voke-go WS=175.8/PvtCmt=158.9 ⇒ **Δ Go−生产 = +30.6MB WS / +63.5MB PvtCmt**（Go 两项都更重，与上方稳态偏高一致）。**单次瞬时、前后景/加载态未对齐**——但 **~7min 后二次同跑采样印证为稳态**：voke 143.3/95.2、voke-go **175.8/158.9（与首样逐字一致）**，两次 Δ 稳定在 **+32MB WS / +64MB PvtCmt**，非瞬时尖峰（voke-go 亦全程无崩溃）。严谨多轮均值仍可选做。 | 2026-10-17/18(日志) |
| V2 匹配 1:1 + IME 手感 | 部分(命脉已验) | **匹配行为 = §6.2 命脉，已 headless 1:1 验**：`rust_corpus.tsv`（13764 行，Rust `pinyin 0.11`+`nucleo 0.3.1` 真值程序生成）逐 `(name,query)` 比对**命中与否 + 原始 u32 分**（`TestScoresMatchRustCorpus` 绿）+ SearchKeys full/initials 逐名比对（`TestKeysMatchRustCorpus` 绿），含退化分歧字由 `TestDegradationDivergence` 单列断言。这比真人敲 IME 观测更硬。**唯「IME 输入手感」**（候选框位置/组句时序）仍需真人——运行期输入路径、日志无证据。（早前本行笼统标「待测」低估了命脉已达的 1:1 覆盖，此处纠正，与 §10 第1599行「唯 V2 IME 手感待真人」一致） | 2026-10-18(语料) |
| V3 枚举 | 通过(核心) | `应用枚举完成 141 条（原始 141，206.5ms）`；PoC N=141 vs 生产 135，Δ+6 待差异清单；MSIX 条目级待人工核 | 2026-10-17(日志) |
| V4 图标 | 部分(通路) | **V-pass 全量日志 0 条「取图标失败」**——Shell.FileIcon→iconcache→ui.Bitmap 数据通路对每次请求均成功；像素级观感仍待人工 | 2026-10-18(日志) |
| V5 面板 | 部分(核心) | `设备诊断 Accelerated=true`（硬件 GPU）；多次 `面板隐藏` 快照 = show/hide/渲染通；Win10 降级 / 多屏待人工 | 2026-10-17(日志) |
| V6 热键 | 部分(核心) | `就绪（热键 Ctrl+Alt+Space）`×7 = 注册成功 + 加载自定义键（默认 alt+space 被 config 覆盖）；即时生效待人工 | 2026-10-17(日志) |
| V7 托盘/单实例 | 部分(核心) | 双启动 A→B 实测：`Get-Process voke-go` 仅剩 1 个（B 检出锁即退）+ A 日志紧随 `[面板首次唤起]`/`Accelerated=true` ⇒ **V7 单实例「二次启动显示已有实例」真机通**（详见下方脚注；本轮 `c1c747c6` 再复现 Objects 2857→2998）；唯托盘菜单点击待人工。**托盘 awake 文案刷新（2026-10-19 已按事件驱动修）**：mygo 无弹窗钩子、`Tray.SetMenu` 调用时快照，故文案改为在**面板 show / 真实 hide** 时 `refreshTrayMenu` 重建（不加定时器）——用户每次唤起/收起后看托盘即为当下倒计时（修「剩 47 分仍显 1 小时」）；菜单**打开期间**不变化（无定时器下与生产「弹窗即刷新」的最近似残差）。 | 2026-10-17→18(日志) |

> ⚠️ **数据来源与新鲜度**：上表日志证据出自 **2026-10-17 08:2x 的一次运行**（本轮会话 6 处修复
> 之前的构建）。**现部署版 `f44b9361` 已由 agent 经 WSL interop 代跑一次**（`VOKe_GO_SELFTEST=1`，
> （**新鲜度更新 2026-10-18**：`f44b9361` 之后的 base64/uuid 修正版 `c1c747c6` 已再次代跑，
> 日志复现同样的 `[启动]`→`awake 自检 ok`→`clipboard 自检 ok`→`失败项 = 0`，且二启走
> `OnSecondInstance→showPanel` 复现 `[面板首次唤起]`/`Accelerated=true`/Objects 2857→2998——
> 下方各行证据**在当前部署版逐条复算成立**，非仅旧构建。）
> Hidden 窗、瞬时托盘、跑完 `Stop-Process` 收干净），新增日志行给出**本会话改动的真机证据**：
> - `工作目录正常，未调整: \\wsl.localhost\…` ⇒ **cwd 修正在 Windows 生效**（合法非系统目录不误改）；
> - `awake 自检 ok：已写状态（剩余≈1800s, keepScreen=false）` ⇒ **SetThreadExecutionState 线程真机通**；
> - `clipboard 自检 ok：写入并读回 "voke-go-selftest-42"（第 1 次）` ⇒ **真实系统剪贴板写→读回通**（Copy 路径设备级证据）；
> - `启动自检结束，失败项 = 0` + `[启动] WS=29.4MB` + `就绪` ⇒ 新构建启动/自检全过。
> **二次启动/单实例（再代跑一次，双启动 A→B）实测通过**：双启动后 `Get-Process voke-go` 只剩 1 个
> 进程（B 检出锁即退），且 A 日志 `00:05:31 [面板首次唤起]` + `设备诊断 Accelerated=true` 紧随 B 启动出现
> ⇒ **V7 单实例「二次启动激活并显示已有实例」+ V5 面板首帧 GPU 渲染** 真机通；B 亦打了 `工作目录正常`
> （FixOwnCwd 每启动都跑）。B 的日志行与 A 同写一份 `voke-go.log`（append），符合预期。
> **持久化 / 自启 的设备侧核查（读文件+注册表，只读）**：`%APPDATA%\voke-go\` 目前只有 `voke-go.log`、
> 无 `config.json`/`frequency.json` —— 符合「仅在设置保存 / 启动应用 record 时才写」的懒写设计（用户尚未
> 经 voke-go 存过设置或启动过应用），非缺陷。**配置读→解析→应用链路已设备正向验证**：经
> `VOKe_GO_CONFIG` 指向临时 config.json（`{"hotkey":"ctrl+shift+j",…}`）启动，日志 `就绪（热键 ctrl+shift+j）`
> ⇒ 读盘 + `ParseHotkey` + 应用/注册全通（临时文件跑后即删、进程已回收、无残留）。`Run` 键存在一个 **空数据** 的 `voke-go` 值：写路径经核正确（enabled→写 `"<exe>"` 永不空、startup 仅在 `HasAutostart` 下调用）→ **该空值是更早某次测试/旧构建的残留、非现行代码 bug**（登录时空命令静默 no-op，无害；如需清可 `Remove-ItemProperty 'HKCU:\…\Run' -Name voke-go`，属用户注册表、未擅动）。
> **对照 Rust 复核纠正（2026-10-18，取代早前「autostart_windows.go 正确」的乐观结论）**：深读
> voke-sys/src/autostart.rs 后另发现两处不一致、已修——① **关自启吞错**：旧码 `RegDeleteKeyValueW`
> 把任何非 0 结果都吞成 nil（成功），安全软件拦 Run 写入（`autostart.rs:58` 明确预料的
> ERROR_ACCESS_DENIED）也被误判成功；现按 Rust `delete_value` 仅 0/ERROR_FILE_NOT_FOUND 幂等成功、
> 其余上抛。② **配置谎报**：设置保存旧序先 `saveConfig` 落 `autostart=true`、后 `SetAutostart`、失败仅记日志
> → 注册表没写成而 config 谎报已开（下次启动 ready 据 config 再试、UI 常亮）；现改成 Rust
> `set_autostart` 次序：注册表改成功才把新值落进 config，失败则配置与界面回弹（`settings.go saveSettings`）。
> 非 Windows stub 恒 nil，行为不变。
> **仍待人工**：V2 IME 手感、V4 图标视觉、多屏 DPI、以及**在设置里开一次自启 / 启动一次应用**以正向确认
> 写盘链路（config.json / frequency.json / Run 值由真值写出）。
>
> **本轮改动/审计引入的两项，附具体验收步骤**：
> ① **开机自启修复验证**（涉注册表写，须用户本人跑，agent 不代做）：先**重启 voke-go** 让运行实例 == 部署版
> `98ae5b8e`（现跑的是修复前的旧实例）。设置开自启→保存→PowerShell 只读核
> `Get-ItemProperty 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run' -Name voke-go`，应见值 = **带引号**的
> exe 全路径 `"C:\Users\loiac\voke-go-poc\voke-go.exe"`；关自启→保存→该值应**已消失**。日志「设置开机自启…失败，
> 配置与界面回弹」仅在该值写不进（安全软件拦）时出现；正常机器看不到此行即符合预期（旧版会静默谎报成功）。
> ② **V3 Δ+6 条目级归因**：Go 141 / 生产 135。三源核实指向——Go 单源 AppsFolder，**多出的 ~6 条大概率是 Rust
> rule-2 会折叠的 AUMID-Win32 重复项**（VSCode/PowerToys/OneDrive/Clash 那类，Go 无 `.lnk` 对照故保留）。要坐实需**逐条
> 名单比对**，但当前 Go 只打**计数**不打条目名——若你要精确 diff，我可加一个 **env 门控的一次性条目名 dump**（仿
> `VOKe_GO_SELFTEST`，默认休眠不改常态、需重构建部署）；**是否要做由你定**，不擅加诊断码。

> 回填口径：结果出来后在此表留数据 + 更新 §10 的验证状态列。**V1 数据
> 出来后回 §1.2 复核双门槛是否仍成立。**

---

## 7. 重写期设计约束

> **本章是重写期的施工约束**，源自 2026-10-16 一轮真机实测 + 源码核实 +
> 用户反馈的 5 条要求与 4 条决策。PoC 期的历史做法若与本章冲突，**以本章
> 为准**。
>
> **核实声明**：本章 §7.1–§7.5、§7.9 的全部结论均于 2026-10-16 经源码或
> 真机核实。mygo 行号取自模块缓存
> `~/go/pkg/mod/github.com/egoist/mygo@v0.5.5/`，Rust 行号取自本仓工作区。
> §7.3 的 CmdPal 参照结论由用户经 GitHub API 取 PowerToys 源码核实，本章
> 转述。

### 7.1 等价判据与可优化点

#### 7.1.1 等价判据（三条，缺一不可）

「功能一致」在本文档的口径是：

| # | 判据 | 内容 | 怎么验 |
|---|---|---|---|
| **E-1** | **行为等价** | 同一输入序列下**用户可观测的输出与状态迁移**一致（命中项集合、排序、选中项、Esc/Enter/Bksp 语义、模式进出）。内部怎么实现不算 | 逐条对拍，见 §7.3.5 |
| **E-2** | **数据等价** | 中间表示一致（拼音键向量、模糊分值、图标字节流规格） | 匹配引擎有**现成机制**：真值语料逐分断言（见 §5.3） |
| **E-3** | **契约等价** | 边界行为一致（IME composing 期间按键归属、退出后跨线程调用的返回值、失焦隐藏时机、托盘菜单刷新时机） | 真机逐条，§6 清单 |

**不要求等价、且鼓励替换的**：内部数据结构、线程模型、Win32 调用方式、
错误处理形态、文件组织。**这就是「不一比一复制」的授权范围。**

#### 7.1.2 可优化点清单（用 Go/mygo 特性换掉 Rust 实现）

| # | 生产 voke 的做法（Rust） | 换成 | 收益与依据 |
|---|---|---|---|
| **O1** | awake 用 `SetThreadExecutionState`（**per-thread**），因此必须专用 OS 线程 + 世代号 + mpsc 通道（`awake.rs` 生产 ≈416 行） | `mygo.Power.KeepAwake(reason, display)` | **专用线程 + 世代号 + 通道整套消失**。mygo 底层是 `PowerCreateRequest` + `PowerSetRequest`（**进程级 HANDLE**，`internal/windows/power.go`），引用计数、无需续期、不绑线程；全仓 grep `SetThreadExecutionState` **零命中**。减 ~400 行 + 一个线程 |
| **O1-b** | 同上 | ⚠️ **Go 侧必须自补的坑** | 连续调两次 `KeepAwake` 而不 release 第一个 → **HANDLE 泄漏 → 系统永久不休眠**。必须保存 release 闭包、起新的先释放旧的（约 10 行）。另见 §8.3 的 Modern Standby 差异 |
| **O2** | 热键走**专用线程 + Request/Reply 协议**（`hotkey.rs` 388 行） | `GlobalShortcut.Register` 直接调 fn | **整文件删除**。`WM_HOTKEY` → `modules.go:627-634` `pressed()` **直接 `fn()`、无 channel**；fn 里调 `Window.Show/Hide` 是主线程直调、零往返。减 388 行 + 一个线程 |
| **O3** | 托盘靠**子类化 `WM_INITMENUPOPUP`** 在菜单弹出前刷新 awake 条目（`tray.rs` ~100 行） | **回调里 `SetMenu(新菜单)`** | **子类化整块删除**。mygo 的右键回调**先于**弹出（`tray.go:200` 的 `RightClicked()` vs `:203` 的 `PopUpMenu`），回调里同步赋值 `t.menu` 已在 `:203` 读之前生效。且 mygo **无等价钩子、不暴露子类化**（全仓 grep `WM_INITMENUPOPUP` 零命中，`setWindowLong` **从不碰 `GWLP_WNDPROC`**） |
| **O3-b** | 同上 | 同上 | `Tray.SetMenu` 在 Windows 是**纯指针赋值**（`tray.go:125`），**零 Win32 调用** → 刷新条目几乎免费 |
| **O4** | 图标**落盘缓存** 322 个 PNG（`icon.rs`） | **进程内有界缓存 + 懒加载** | 省掉落盘 / 读盘 / 失效三件事。⚠️ **硬约束**：`Shell.FileIcon` 走 `onMainValue` → **只许主线程调**，批量取会**串行阻塞主线程且无超时**（`loop.go:94-107`）→ 必须「列表项首次可见才取」+ 缓存，**禁止扫描时批量预取** |
| **O4-b** | 同上 | 同上 | ⚠️ 退出后跨线程调用**静默丢弃、返回零值**（`loop.go:19-27`、`:101-105`，**不设默认值**）→ `FileIcon` 可能返回 `nil, nil` **假成功**，**nil 判断不可省** |
| **O5** | `InstalledApps` 之外还要 `.lnk` 解析 + AppsFolder 双来源合并（`main.rs:603-611`） | **只用 `InstalledApps`** | mygo 后端枚举的就是 `FOLDERID_AppsFolder` COM，含桌面快捷方式与 packaged 应用；且它是**唯一后台安全**的平台调用（自建 COM STA，刻意绕开 `onMain`）→ 删掉一整块枚举代码 |
| **O6** | 中文硬编码在源码里 | **mygo 内置 i18n**（带 **zh-Hans** 与 CLDR） | 净收益：日期 / 数字 / 复数 / 排序全白拿。⚠️ voke 现在**全硬编码中文**，迁 i18n 属「顺手做」，但**别为 i18n 引入 key 爆炸**——建议只把设置窗与插件描述走 key，面板固定文案仍直出中文 |
| **O7** | 匹配引擎「Rust 真解 → Go 复刻」 | **保留现状**（已 1:1 移植） | **唯一「值得一比一」的地方**：命中与否与分值是 §6.2 的命脉，换包会破坏可对比性 |
| **O8** | Rust 侧 `CHANGELOG.md` 由 git-cliff 生成 | **无需另建** | git-cliff 只看 commit message 前缀，对 `.go` 提交同样生效；但 Go 侧提交**必须**守 Conventional Commits 才能进 CHANGELOG |
| **O9** | `voke-sys/src/logcap.rs`（跨启动追加、超限从头删一批最旧行） | **同款实现，放 logic 层** | 同逻辑同单测。放 logic 层的理由同 `internal/match`：**不 import mygo/windows，Linux 原生可测** |

> **O1/O2/O3 合计**：Rust 侧 `awake.rs` ≈416 行 + `hotkey.rs` 388 行 +
> 托盘子类化 ~100 行 ≈ **900 行** Go 侧**不需要存在**，且**少 2~3 个线程**。
> 这是「不一比一复制」最直观的一笔账。

#### 7.1.3 线程模型总纲（决定并发风险等级）

**单线程 UI，重写期所有并发设计都从这一条推出来**：

| 事实 | 位置 |
|---|---|
| UI 必须活在启动进程的那个线程：`runtime.LockOSThread()` 在 `init()` 无条件执行 | `mygo.go:72-76` |
| `App.Run` 强制校验不在主线程就报错 | `app.go:90-92` |
| 主线程跑 `GetMessageW` / `TranslateMessage` / `DispatchMessageW` 消息循环 | `internal/windows/app.go:193-207` |
| **所有 platform 调用与 handler 回调都在主线程** | `docs/architecture.md:183-184` |

**跨线程两条路，风险完全不同**：

| 路 | 语义 | 用途 |
|---|---|---|
| `postMain` / `Window.Update`（`content.go:172`） | **投递、不等待** | 后台 goroutine → UI 状态通道（官方推荐姿势） |
| `onMain`（`loop.go:94-107`）/ `onMainValue`（`modules.go:110`） | **投递 + `<-done` 阻塞等待、且无超时** | 只在「必须拿到返回值」时用（如 `FileIcon`） |

**唯一后台安全**的平台调用是 `Shell.InstalledApps`。

> **施工含义**：`platform` 层每个 Win32 调用都要标一句「主线程 / 可后台」。
> 标错的后果分两种——在后台调主线程函数会**串行阻塞主线程且无超时**
> （UI 卡死）；在主线程调后台安全函数只是白白丢一次并行。建议在
> `platform` 层的包注释里放一张**逐函数标注表**。

#### 7.1.4 一条要警惕的坑

`Window.Update(fn)` 是**异步投递不等待**，而 `Window.Show()/Hide()` 是
**同步语义**（主线程直调）。两者混用会出现「状态已改、窗口还没刷」的帧。
**规范**：跨 goroutine 改状态**一律**走 `Update`；`Show/Hide` **只从主线程
调**（热键回调、托盘点击、IME/键盘回调本来就在主线程）。真机验证见 §8.3。

### 7.2 分层与目录结构

#### 7.2.1 三层铁律（对应仓库既有的「core 不依赖 gpui/windows」）

```text
app/       进程装配：单实例 / 面板 / 热键 / 托盘 / 退出   只做接线，不含业务
ui/        视图：只读 logic 状态 + 只发意图               不含业务判断
logic/     路由 / 匹配 / 配置 / 模式状态机 / 日志          **零 mygo、零 x/sys/windows**
platform/  一切 Win32 与 mygo 平台调用                   唯一允许 import mygo 的层
```

**硬约束**（照抄 AGENTS.md 的 voke-core 铁律）：

1. `internal/logic/**` **不得** import `mygo` 或任何 `x/sys/windows`——
   保证 `go test ./internal/logic/...` 在 Linux 原生跑（PoC 已验证：
   `internal/match` 直接依赖只有 go-pinyin + 标准库，`GOOS=windows` 交叉
   编译亦通过）。
2. `internal/ui/**` **不得**直接调 Win32；要拿显示器、图标、电源必须经
   `platform` 的窄接口。
3. `platform` 层里每个平台专属文件配一个 `_stub.go` 占位（PoC 已立规矩：
   `mem.go` / `mem_stub.go`），使 Linux 原生 `go build ./...` 保持绿。

#### 7.2.2 目录结构提案

```text
poc/voke-go/
├── main.go                    # 只做装配：建 App、跑 Run
├── go.mod / go.sum            # MyGo v0.5.5 精确 pin
│
└── internal/
    ├── platform/              # ── 唯一 import mygo / syscall 的层
    │   ├── win32_windows.go   #   //go:build windows —— 原生句柄、HWND、EnumWindows
    │   ├── win32_stub.go      #   //go:build !windows —— 占位
    │   ├── screen.go          #   显示器枚举 / 主显示器
    │   ├── apps.go            #   InstalledApps / LaunchApp / FileIcon（懒加载 + 缓存）
    │   ├── power.go           #   KeepAwake + release 串行化（O1-b 的坑在这层补）
    │   ├── tray.go            #   托盘 + 菜单构造（O3：回调里 SetMenu）
    │   ├── hotkey.go          #   GlobalShortcut 封装（O2：只包一层）
    │   ├── singleinstance.go  #   RequestSingleInstanceLock + SetName
    │   ├── mem_windows.go / mem_stub.go   # 内存快照（windows-only + 占位）
    │   └── log_windows.go     #   日志路径 %APPDATA%\voke-go\
    │
    ├── logic/                 # ── 纯逻辑，零 mygo / 零 x/sys/windows
    │   ├── engine/            #   前缀表路由 + 全局搜索 + 模式状态（§7.3 新写）
    │   ├── match/             #   **已存在**：拼音 + fuzzy（O7，保持 1:1）
    │   ├── appsmodel/         #   条目模型 + 去重 + 排序（不碰 Shell）
    │   ├── config/            #   配置 schema + 默认值 + 校验（§7.3.4）
    │   ├── logcap/            #   日志容量（O9）
    │   └── frequency/         #   频率学习（已落地：Store/Boost/Key + JSON 落盘 + apps/聚合排序加成）
    │
    ├── ui/                    # ── 视图 + 意图回调
    │   ├── panel.go           #   面板窗口生命周期（Hidden 建 / Show 前定位）
    │   ├── view.go            #   整帧视图（输入行 + 虚拟化列表）
    │   ├── row.go             #   单行渲染（图标 + 标题 + 副行）
    │   ├── keys.go            #   键盘 / Esc 两级 / ↑↓ / Enter 三路
    │   ├── icons.go           #   ui.Bitmap 解码 + 有界缓存
    │   └── theme.go           #   **字号单一事实来源**（§7.4）
    │
    ├── providers/             # ── 插件层：每个插件一个包
    │   ├── registry.go        #   注册表 + 启停 + 前缀绑定
    │   ├── provider.go        #   Provider 接口定义（见 §7.2.3）
    │   └── apps/  windows/  awake/  terminal/  workspace/
    │       timestamp/  base64/  uuid/  random/  calculator/
    │
    ├── app/                   # ── 装配层：接线
    │   ├── app.go             #   生命周期、单实例、热键注册、托盘创建
    │   ├── dispatch.go        #   UI 意图 → logic 调用（对应 dispatcher.rs）
    │   └── state.go           #   主线程唯一状态容器
    │
    └── match/testdata/        # **已存在**：rust_corpus.tsv（见 §5.3）
```

#### 7.2.3 插件注册接口（对应生产 `Provider` trait）

生产侧接口在 `voke-core/src/provider.rs:235-260`（`fn id` / `fn name` /
`fn aliases` / `fn description` / `fn config`）。Go 侧提案（**`Descriptor`
里只有默认值，运行时真相只有配置一份**——见 §7.3.4）：

```go
// internal/providers/provider.go
type Descriptor struct {
    ID          string   // "apps" / "calculator" / …（稳定标识，改名要迁移）
    Name        string   // 面板里显示的名字
    Description string   // 副行
    Aliases     []string // 匹配用的额外键（多别名设计比 CmdPal 的一对一好，保留）
    // ── 以下全是「默认值」，用户配置可覆盖；不在此处硬编码任何行为 ──
    DefaultEnabled    bool
    DefaultIsGlobal   bool
    DefaultPrefix     string   // "" = 无前缀
    DefaultActivation Activation // Direct / Indirect
    DefaultWeight     float64
    DefaultCount      int      // random 用
}

type Provider interface {
    Descriptor() Descriptor
    // Search 返回该插件在给定查询下的匹配；ctx 里带 QueryCtx（模式 + 子模式）
    Search(ctx *QueryCtx) ([]Match, error)
    // Action 处理 Enter（启动 / 生成 / 复制…）
    Action(ctx *QueryCtx, m Match) error
    // SubModes 声明该插件的子模式（base64 的 enc/dec…）
    SubModes() []SubMode
}
```

**借鉴来源标注**（用户要求区分「抄 CmdPal」与「mygo 生态既有做法」）：

| 设计点 | 来源 |
|---|---|
| `Descriptor` 元信息 / 运行时配置分离 | **CmdPal**（`Settings.Aliases` 与 `CommandAlias` 分离，`AliasManager.cs:48`） |
| `ID` 稳定标识 + 改名需迁移 | **CmdPal**（`id = com.microsoft.cmdpal.<camelCase>`，源码注释明写改名必须迁移） |
| 命令名用**动宾短语**、无缩写 | **CmdPal**（`CalculatorCopyCommand`、`ReplaceQueryCommand`） |
| `Provider` **多别名**（`Aliases []string`） | **voke 自有设计，比 CmdPal 好**——CmdPal 的 `CommandAlias` 是一对一，没有多 keywords 字段。**保留 voke 的多别名** |
| 子包分层 + `_stub.go` 平台占位 | **mygo 生态既有做法**（mygo 自己就是 `internal/{windows,linux,darwin}` 分包 + 条件编译） |
| 每帧重建视图（immediate-mode）+ `Update` 回主线程 | **mygo 生态既有做法**（`content.go:162-171` 官方注释给的就是这个姿势） |
| `//go:build windows` + `_stub.go` | **本仓既有纪律**（Rust 侧 `voke-sys` 门控 → Go 侧同款） |

#### 7.2.4 能力面核对：UI 缺口为零（2026-10-16 核实）

| 类别 | 结论 |
|---|---|
| 13 类表单控件 | **全中**：Checkbox / RadioButton / Select / Combobox / Slider / Switch / TextArea / Tabs / Accordion / Scroll / Tooltip / ContextMenu / 原生文件对话框 |
| 多窗口 | 支持，`Parent` + `Modal` 都有 |
| 列表虚拟化 | **支持可变行高**（`ui/list.go:151`，实测测量后缓存） |
| 数值输入 | `ui.NumberInput(c, *float64, lo, hi, step)` **正好对上** voke 权重 0–3 / step 0.1 |
| 国际化 | 内置 i18n + zh-Hans + CLDR（O6） |
| Win32 兜底 | `Window.NativeHandle()` → HWND + `RunOnMain` **通用逃生口** |
| **真缺口** | **窗口枚举**——`windows` 插件需要，必须自研 `EnumWindows` + UI Automation（走 `platform/win32_windows.go`） |

**结论：UI 层没有需要换框架的缺口。** 设置窗在 mygo 侧是纯收益——13 类
控件 + i18n + 原生文件对话框全都现成，而生产 voke 的设置窗
（`settings.rs` 905 行）大部分工作量正是在手搓这些。

### 7.3 前缀键机制重写（要求⑤ + 决策①②③）

#### 7.3.1 产品语义（先说清，避免又做成两套）

> 搜索框内输入**前缀字符**路由到对应插件，如 `=` 计算器、`{` 工作区。
> **这是搜索框内的输入语义，不是全局快捷键。**

现有实现的根本问题就是**没把这句话贯彻到底**：它同时存在「查询串前缀
派生」（engine 层）与「picker 列表选中」（app 层）两套互相不知情的状态机。

#### 7.3.2 现状缺陷清单（逐条带 `file:line` 与「为什么是问题」）

**根因一句话：`search.rs` 的 `active_plugin` 只有一个赋值点，engine 层却
走另一套「查询串前缀派生」，两者从不互通。**

| # | 缺陷 | 位置 | 为什么是问题 |
|---|---|---|---|
| **D0** | 两套状态机并存 | `active_plugin` 唯一赋值点 `search.rs:991`（picker 列表选中）；engine 层走 `prefix_owner`（`engine.rs:260`） | 前缀常量定义在 `engine.rs:35-39`，但**只被 engine 的 `prefix_owner` 消费**；`search.rs` 的 picker 路径完全绕开它。两套逻辑各测各的都"绿"，合起来是坏的 |
| **D1** | **致命**：base64/uuid/random 的子模式在真实路径上基本不可达 | `enter_sub_mode`（`search.rs:1009-1010`）**只设 `sub_mode`、不设 `active_plugin`**；`prepare_in_mode`（`engine.rs:441`）见 `active_plugin` 为空就落回 `prepare()` → 全局搜索 | 打 `#b ` → 显示子模式 → Enter 进 `enc` → **base64 加密结果永远出不来，用户拿到的是全局搜索结果** |
| **D1-b** | 自检测试**掩盖了** D1 | `search.rs:1912` / `:1915-1928` | 测试**显式传 `Some("base64")`**，恰好绕开了「真实 app 从不设该字段」这一事实。测试是绿的、用户是坏的 |
| **D2** | `=` 直通 calculator 但没有模式语义 | `search.rs:630` 注释明写「`=` 也不再直进 calculator」 | 注释与实际**直接矛盾**：`=1+1` **确实**直通（`engine.rs:260` + `prefix_matches` 的 Direct 分支 `:543`）。而 `active_plugin` 为空 → 无模式令牌、占位符错、**Esc 直接关面板**（用户期望「退回全局搜索」） |
| **D3** | 两套 trim 规则 | `picker_filter`（`search.rs:399-402`）与 `run_query` 的 picker 分支（`:1669-1673`）**又抄了一遍** | 前导空格下的 prefix 与 engine 的 `prefix_owner` 不一致。同一份规则写两遍 = 迟早漂移 |
| **D4** | 配置里未知 plugin id **静默失效** | `config.rs:395` `normalize()` 只回填缺失项、校验 weight/prefix 空白，**不校验 id 是否存在** | 用户写 `[providers.myplugin]` → 静默无效、无任何提示。且 `normalize` 里有**自相矛盾的注释**（`:423-431`）：计划文档说「prefix 不能以 `#` 开头」，但默认表与 241 项既有测试全依赖 `#` 族 → 校验规则**已经摇摆过一次** |
| **D5** | `#` 硬编码、不可配置 | `search.rs:400` / `:1671` / `:1960` | 想换前缀字符必须改源码 |
| **D6** | `#` 的语义与用户原始描述不一致 | `search.rs:393-398` 注释详述「`#` 先命中 timestamp、后续字符被吞」这个致命 bug | **用户原始描述是「`{` 激活工作区」，不是「`#` 列出所有插件」**——两个语义至今并存（本轮已决策：并入统一前缀表） |
| **T1** | 测试缺口 | — | 缺：真实 app 路径 + Enter 子模式的集成用例；IME 组合输入；前缀与快捷字母冲突；Esc / Bksp 回退；状态残留；配置冲突校验 |

#### 7.3.3 新机制（决策① + ②：全等扫表 + 模式状态显式化）

**核心改动一句话**：把「模式」从**查询串派生**改成**显式字段**；前缀只在
**输入框内容恰好等于某个前缀**的那一刻触发一次切换，此后模式由字段持有。

```text
        前缀表（唯一真相，配置生成，见 §7.3.4）
          "=" → calculator (Direct)   "{" → workspaces (Direct)
          "…" → 其余插件各一条   专属条目：列出所有插件（默认 "#"，同表同路径）
                         ▲
        输入框内容 == 某个前缀（全等）→ 一次线性扫描，命中即切
                         │
        appState（显式模式状态，不再从查询串派生）：
          mode: Global | Plugin | PluginPicker | SubMode
          pluginID / subMode / query    ← **query 永远不含前缀**
```

> ⚠️ **这里修掉了 D6**：现状里 `#` **既是** timestamp 的前缀、**又是**插件
> 列表的入口。新机制下两者是**两条独立可配置条目**（默认各给一个字符），
> 配置冲突由 C2/C4 校验拦下——用户想要「`#` 两者兼具」也可以，但那是**显式
> 写的一条配置**，不是代码里的隐式约定。

**规则（按执行顺序，逐步可测）**：

1. **全等扫描**：`q = trim(query)`；对前缀表做**一次线性扫描**，找
   `activation == Direct && q == prefix` 的项。**命中即切换**：`mode = Plugin`，
   `pluginID` = 命中项，`subMode = ""`，`query = ""`。
2. **`Indirect` 项**：`activation == Indirect` 时，`q == prefix + " "` 才命中
   （对应 `engine.rs:544-546` `prefix_matches` 的 Indirect 分支）。裸前缀与
   Direct 依然被隔开，但**判定条件是全等、不是 `StartsWith`**，语义单一。
3. **插件列表并入同一张表**（决策②）：给「列出所有插件」分配一个专属条目
   （可配置，默认 `#`），与其它条目**走同一条扫描路径**，只是命中后的
   `mode` 不同。**代码里不再有第二处 `strip_prefix('#')`。**
4. **状态剥离**：`mode` 一旦是 `Plugin`/`SubMode`，后续输入**只当查询词**，
   **不再做前缀判定**——从结构上消灭了 D1。
5. **前缀不进输入框**：命中后 `query` 置空，前缀以**模式令牌**形式渲染
   （沿用生产 `active_prefix`，`search.rs:993`）。
6. **回退语义**（三条，各有唯一出口）：
   - `Esc`：`SubMode` → `Plugin` → `Global`（**不是**关面板）→ 才关面板。
   - `Backspace` 且 `query` 为空：同上逐级回退。
   - 空输入 + 已激活插件：保持插件第一级（不自动退回全局）。
7. **未知 id 立即报错**（修 D4）：配置加载时校验 `[providers.X]` 的 `X` 是否
   在注册表里，不在则**启动即列出全部未知 id 并退出/降级**，不静默。

**子模式（`SubMode`）的归属明确化**：`SubModes()` 由插件自己声明
（`enc`/`dec` 属于 base64），`Enter` 在插件第一级命中子模式条目时**同时**设
`mode = SubMode` + `pluginID` + `subMode`——一次赋值，原子完成。D1 的「只设
一半」在类型层面不可能。

#### 7.3.4 配置 schema（决策③：不 hardcode 值和逻辑，元信息只保留默认值）

| 层 | 内容 | 谁写 |
|---|---|---|
| **Descriptor（默认）** | `DefaultPrefix` / `DefaultActivation` / `DefaultEnabled` / `DefaultIsGlobal` / `DefaultWeight` / `DefaultCount` | **插件作者**，写在 `internal/providers/<name>/` 里 |
| **用户配置（真相）** | `enabled` / `prefix` / `activation` / `is_global` / `weight` / `count` | **用户**，TOML |
| **运行时** | 只有**一份**：合并后的 `ProviderConfig` | `config` 层归一化产出 |

> **铁律三条**：
> ① **默认值只允许出现在 Descriptor 里**，不许出现在 `engine` / `ui` /
> `platform` 任何一处；
> ② **判定逻辑只有一处**（前缀扫描在 `logic/engine`），不许在 `ui` 或
> `platform` 里出现第二个 `strings.HasPrefix`；
> ③ 引擎层**只吃归一化后的配置**，不知道任何插件的存在。

```toml
[providers.calculator]
enabled    = true      # 可配置：是否启用
is_global  = false     # 可配置：是否参与全局搜索
prefix     = "="       # 可配置：前缀键（"" = 无前缀）
activation = "direct"  # 可配置：direct / indirect
weight     = 1.0
count      = 5

# 插件列表入口也是表里的一条，不特殊处理（决策②）
[providers.__picker]
prefix     = "#"
activation = "direct"
```

**校验规则**（`normalize()` 阶段，全部要有明确失败语义，**不许静默**）：

| # | 规则 | 失败处理 |
|---|---|---|
| C1 | provider id 必须存在于注册表 | **报错退出**，列出全部未知 id |
| C2 | 前缀全局唯一（撞车即错） | **报错退出**，列出撞车的两个插件 |
| C3 | 前缀非空、不含空白（沿用 `config.rs:421`） | 该项置 `None` + 中文警告 |
| C4 | 前缀不得与保留键冲突（见 §7.3.6 b 的符号键分配原则） | **报错退出** |
| C5 | `weight` 必须有限且 clamp 到 0..3（沿用 `config.rs:406-409`） | 静默夹紧 |
| C6 | `count` clamp 到 1..=50（沿用 `config.rs:412-415`） | 静默夹紧 |
| C7 | `activation` 未知取值（沿用宽松反序列化 `config.rs:50`） | 记日志按「没写」处理 |

> **D4 的注释矛盾在新机制下自然消解**：前缀就是普通字符串，没有「`#`
> 特殊」这回事，`#` 只是插件列表这一条的默认值。那段基于旧语义的注释可退休。

#### 7.3.5 待验证项（重构期开工后逐条验）

| # | 验证项 | 方法 | 通过标准 |
|---|---|---|---|
| V-1 | 全等扫描唯一触发 | 逐前缀敲入，录屏核对 | 只有恰好等于前缀时才切；多打一个字符不切 |
| V-2 | 插件列表并表 | 敲列表前缀 → 出全部插件；再打一字 → 过滤 | 与现状 picker 行为一致，但**只有一条扫描路径** |
| V-3 | **D1 修复验证**（最关键） | base64 走完整链路：列表前缀 → 选中 → Enter 进 `enc` → 输入内容 → Enter | **必须拿到加密结果**；不能出现全局搜索结果 |
| V-4 | Esc / Bksp 三级回退 | 逐级敲 | `SubMode → Plugin → Global → 关闭`，每级都单独验 |
| V-5 | IME 组合输入 | 前缀字符用输入法打（如拼音打 `denghao` 上屏得 `=`） | 组字期间不触发扫描；上屏后正常触发 |
| V-6 | 前缀与快捷字母冲突 | 面板里输入以快捷字母开头的内容（如 `c` 开头的 app 名） | 不误触发 |
| V-7 | 配置冲突校验 | 故意写撞车前缀 / 未知 id | **启动即报中文错误**，列出冲突项，不静默 |
| V-8 | 状态残留 | 切到插件后点失焦隐藏、再唤起 | 模式回到 Global、query 空（沿用生产「隐藏即清空」） |
| V-9 | 状态机穷举 | 对 `mode × subMode × query 是否为空` 的组合跑单测 | 无 panic、无不可达状态；每条边都有测试覆盖 |

#### 7.3.6 两个前缀键遗留项（**已拍板**）

**✅ 已决策 a：`#b` 之后继续打字（如 `#ba`）的兜底 → 照抄 CmdPal。**

- **决策内容**：**全等触发**（`#b` 命中才激活），**不做前缀回落触发**；
  但输入 `#ba` 这类**部分匹配时不激活**，改为**在排序层给 alias bonus**，
  把它托底到 Fuzzy tier，**保证不被过滤掉**。
- **CmdPal 依据**：`MainListRanker.cs:98-109` 的 alias 前缀 bonus 只是
  「防止被 fuzzy 层过滤掉」，真正的触发仍是 `AliasManager.cs:48` 的
  **全等查表**。即「加 bonus 但不加触发」是 CmdPal 的原生做法，不是折中。
- **为什么不选备选 2（加前缀回落触发）**：会引入第三种触发语义，与决策①
  「精确匹配」的口径有张力；且用户看不到「已进入插件模式」的提示，状态更糊。
- **实现要点**：bonus 必须在**过滤之前**生效，否则条目已被 fuzzy 判掉再
  加分也救不回来——这正是 CmdPal 把它放在 ranker 里的原因。
- 已入 [`docs/BACKLOG.md`](../../BACKLOG.md)（实现随前缀键重写一起做）。

**✅ 已决策 b：前缀字符集 → 参考 CmdPal 的符号键分配。**

- **分配原则（写死，重写期照此办）**：**按符号键、不占用字母**。
  CmdPal 的符号键分配供参考：`` ` `` `$` `=` `>` `<` `)` `??`——规律正是
  **符号键、与字母模糊搜索天然不冲突**。voke 现状的 `=`（计算器）、
  `{`（工作区）就是符号键；生产默认表 `config.rs:178-189` 里的 `#` 族
  **不符合**（`#` / `#b` / `#u` / `#r` 四条同族互为前缀，才有了 D6 的混乱）。
- **备选方向的处置**：**弃用「双字母」**——会与 `ca` / `wo` 开头的应用名
  （Calendar、Camera、Word…）撞车，误触发率高。「单层统一命名空间」的
  「不搞二级」精神与本决策一致，但具体单层还是二级由重写期按插件数量决定。
- **具体字符集分配留给开发阶段按需增补**（本文档不定死表格）。

#### 7.3.7 CmdPal 参照结论（2026-10-16 用户经 GitHub API 核实，本节转述）

**触发机制（可直接照抄的部分）**：

- **别名触发 = 全等查表，无任何前缀匹配**：`AliasManager.cs:48`
  `Settings.Aliases.TryGetValue(searchText, out var alias)`；唯一调用点
  `MainListPage.cs:607`；零 `StartsWith` / `Contains` / Trie。
- **`IsDirect` 不是匹配策略开关**，只决定 key 是否带尾随空格：
  `CommandAlias.cs:18` `SearchPrefix => Alias + (IsDirect ? "" : " ")`。
- **别名判定绝对优先于 fuzzy 排序**，命中即 `return`（`MainListPage.cs:598-622`）。
- **`SearchPrefix` 的用途**：别名表 dict key + UI 展示标签
  （`TopLevelViewModel.cs:386`），**不是点击回填输入框**。

**不要照抄的三处（CmdPal 自身的不一致 / 死代码）**：

| # | 问题 | voke 的做法 |
|---|---|---|
| 1 | 触发用 Ordinal **大小写敏感**（`AliasManager.cs:48` 默认 comparer）vs 排序用 `OrdinalIgnoreCase`（`MainListRanker.cs:149-157`）→ `FILE ` 不命中 `file ` | **在触发层显式规范化**（统一 trim + casing）后再查表（§7.3.4 的 normalize 步骤） |
| 2 | `CommandAlias.FromSearchText`（`CommandAlias.cs:32-37`）**零调用** | 不设对应概念 |
| 3 | 多别名「能写进文件但只认第一个」——`AliasManager.cs:147-180` 改一个命令别名时会清掉它所有旧别名 | **保留 voke 的 `Provider::aliases()` 多别名设计**（`provider.rs:253`），它比 CmdPal 的一对一 `CommandAlias` 更好 |

> CmdPal 的 WinUI 分层（`...UI.ViewModels` / `ext/*` / `extensionsdk` 的
> `ListPage` / `IPage`、`TopLevelCommandManager.LookupCommand` 线性按 id 比较、
> `HasBackButton = isNested && !message.TransientPage`）**对 Go 无移植价值**，
> 此处不转述——留档只为对照「触发机制的纯粹性」。

### 7.4 字号体系（要求①）

**问题定性**：不是「输入框太大」这一个问题，是 **PoC 的字号体系没有主线**
——输入框、行标题、副行、提示行四个值各自独立硬编码，彼此无比例约束。

**根因**：`view.go` 的 `FontSize(22)` 是逐字抄官方示例
`examples/launcher/main.go:87`。但 launcher 的行标题用主题默认 14、副行
更小，比例失衡在官方示例里就不明显；PoC 把 22 搬过来、同时把行标题也定
成 14，**输入框比行标题大 57%**，失衡就暴露了。

**定案值**：**输入框 16 / 副行 12**（备选 15 未采纳：更保守，行标题显得可
再放大）。**执行口径（重写期落 `ui/theme.go`，单一事实来源）**：

1. 行标题 = **14**（= mygo 主题默认 `defaultFontSize()`，Windows 返回 14），
   副行 = **12**，提示行 = **11**——**不要用 11 给副行**：12 与 14 的层级更
   清楚，11 在 125% DPI 下会糊。
2. 输入框 = **16**，并写成 `titleFontSize + 2` 的表达式而非裸数字。
3. 主题切浅 / 深色时字号不变（字号不是主题项）。
4. 提示行**刻意留在 11**：它是比副行更弱一档的信息（快捷键提示），一旦与
   副行同号，两者层级就消失了。

**PoC 已落实**：`view.go` 四个字号收敛成一个常量块，输入框写成
`inputSize = titleSize + 2`。

### 7.5 特效候选表（要求②）

**准入门槛**（用户原话：「不影响性能和资源占用前提下」）——一条特性要进，
必须同时满足：

- **G1 无常驻资源**：不新增常驻 GPU 纹理 / 位图 / 设备，且**不新增第二套
  渲染设备**（Vibrancy 就是踩这条被否，见 §5.1）。
- **G2 隐藏时不烧 CPU**：面板隐藏后不请求帧。注意 §8.3 的硬约束——
  Windows 上 `occluded()` 恒 false，**持续动画必须自带 idle 停止**。
- **G3 不每帧分配 + 不画两遍子树**：`Element.Blur` / `Element.Effect` 是唯二
  违反的（每帧把子树渲进 GPU 位图再叠一道，`ui/effect.go:54-70`），
  **本表一律排除**。

| # | 候选 | 成本 | 常驻 | 定案 |
|---|---|---|---|---|
| E1 | **圆角（窗口）** | 零 | 否 | ☐ 保持——`Panel: true` 自动触发 `roundFlyout()` → `DwmSetWindowAttribute(DWMWCP_ROUND)`。**已在用** |
| E2 | **圆角（控件）** | 零 | 否 | ☐ 保持 6——`Theme.Radius` 默认 6。**已在用** |
| E3 | **窗口阴影** | 零 | 否 | ☐ 保持——`HasShadow: !o.DisableShadow`，走 DWM 帧扩展、由系统画。**已在用** |
| E4 | **元素阴影**（`Element.Shadow`） | 低 | 否 | ☑ **开：仅 tooltip + 设置窗分组头**，**不给列表行**（140 行 × 每行一次 GPU 降采样 + 模糊 pass 不划算）。只在元素**画出来的那一帧**跑 |
| E5 | **hover 效果** | 零 | 否 | ☑ **保持自带**——列表行 hover 是 `ui.List` **自带**的（`ui/list.go:599-602` 命中且非选中时铺 `t.SurfaceHover`，驱动重绘由 `setHover` 做，`ui/input.go:154-179`），**不需要自实现**；只「键盘输入后抑制 hover」那层状态不做 → 已入 BACKLOG |
| E6 | **焦点环** | 零 | 否 | ☐ 保持——主题 `Focus` + `Element.FocusRing`。**已在用** |
| E7 | **选中高亮** | 零 | 否 | ☑ **已改用 `Selection`**——mygo Theme **无 `list_active` 语义色**。实现坑见下方 |
| E8 | **渐变**（`LinearGradient` / `ConicGradient`） | 零 | 否 | ☐ 不用——纯 scene op 无额外资源，但启动器风格是纯色，渐变是噪声 |
| E9 | **装饰图标** | 零 | 否 | ☐ 保持——走图集。**已在用**（搜索框左侧放大镜） |
| E10 | **滚动动画**（`FollowAnimation` / spring） | 零（**但持续帧**） | 否 | ⛔ **砍掉**——违反 G2（硬阻塞，见 §8.3） |
| E11 | **行入场/出场动画** | 零（**但持续帧**） | 否 | ⛔ **砍掉**（同上） |
| E12 | **颜色过渡** | 零（**但持续帧**） | 否 | ⛔ **砍掉**（同上） |
| E13 | **`Element.Blur`（背景模糊）** | **高** | **是** | ⛔ 排除——每帧分配 GPU 位图 + 子树画两遍，与 G3 冲突 |
| E14 | **`Element.Effect`（着色器特效）** | **高** | **是** | ⛔ 排除（同上） |
| E15 | **Vibrancy（毛玻璃材质）** | **+28.2MB 常驻** | **是** | ⛔ 排除——违反 G1。16 档成本相同、不可逆（见 §5.1） |
| E16 | 剔除 inspector | 省常驻 | — | ☐ 保持——`mygo_noinspector` build tag。**已在用**（构建命令见 §2.1） |

**T2（E10–E12）是硬阻塞不是取舍**：mygo 侧**无配置项可关**持续动画，
除非 vendor 改帧循环 → **等上游**，解除条件已入
[`docs/BACKLOG.md`](../../BACKLOG.md)。

**T3（E4 阴影范围）**：PoC 当前**没有** tooltip 与设置窗，故本轮只把这条
约束写进代码注释（`view.go` `row()` 上方），并确认列表行渲染路径上没有
`ui.Shadow` 调用——重写期新增这两类元素时照此办理。

**E7 的一个实现坑（PoC 踩到并已解决）**：改成 `Selection` 后**不能直接拿
半透明的 `t.Selection` 当行底色**。`ui.List` 构建行节点时会自己对选中行调
`w.Selected(true)`（`ui/list.go:582`），而 `node.Selected` 的实现是
`e.Background(t.Accent).TextColor(t.AccentText)`（`ui/table.go:561-570`）
——**外层行节点自带一个不透明的 Accent 底色**。故 PoC 里写成
`t.Selection.Over(t.Background)` 压成不透明色（`ui/color.go:120-125`），并把
文字色从 `AccentText`（为实心蓝底设计的白字）**改回** `t.Text` / `t.TextMuted`
——白字在 Selection 的浅色底上读不出来。

#### 7.5.1 列表行视觉重做（2026-10-16 真机反馈第二轮）

**用户原话**：「还是高，而且没注意每个选项行的边框和圆角是什么时候加的，
好丑，去了吧，hover 上去高亮就行，或者加一条浅色不拉满的分割线也行，全框
起来然后框又连着太难看了，要有审美，或者 hover 的行框起来+高亮，总之都比
现在的样式强」。

拆成两件：**① 每行都有边框+圆角，且相邻行的框首尾相连**；**② 面板仍偏高**
（定位部分见 §7.6）。

**定因：边框是我们自己调 `ui.Button` 引进来的，不是 `ui.List` 的默认行为。**

| 环节 | 位置 | 做了什么 |
|---|---|---|
| `ui.Button` | `api_gen.go:744-753` → `widgets.go:135` → `:140 button` | = `ButtonBase` + **主题外观** |
| `styleButton` | `widgets.go:154` | `.Radius(t.Radius)` → **每行圆角 6** |
| `styleButton` | `widgets.go:167` | `.Background(t.Surface)` |
| `styleButton` | `widgets.go:168-170` | `if border.A > 0 { b.Border(1, border) }` → **每行 1px 描边** |

描边色 `t.Border`（浅色 `#d9d9de` / 深色 `#3f3f46`）**恒不透明**，所以每一行
都被画一圈灰框；圆角 6 让每行成了圆角矩形，行高 48 紧挨 48 → 上下两行的框边
贴在一起，正是用户说的「框又连着」。

> **`ui.List` 本身不画描边**：行节点只在 hover 且非选中时铺 `t.SurfaceHover`
> （`ui/list.go:599-602`），选中时才由 `w.Selected(on)` 上一个不透明 `t.Accent`
> 底（`ui/list.go:582`）。框的唯一来源就是上面那个 `ui.Button`。
>
> 这条结论是**扫像素**拿到的，不是看 API 签名推的——`ui.List` 从头到尾没出现
> 过 `Border`。已用 `ui.NewTester` 渲染对比确认：`ui.Button` 在行槽左右缘画出
> 竖排的 `#d9d9de` 像素（相邻两行在 y=110-111 / 158-159 处相接，**直接复现
> 了用户说的「框连着」**），`ui.ButtonBase` 同位置全白。

**决策：换 `ui.ButtonBase`，行高亮自绘为「内缩圆角药丸 + 垂直留白」。**

| 状态 | 底色 | 说明 |
|---|---|---|
| 静止 | `Transparent` | **什么都不画**，与面板底色融为一体 |
| hover | `t.SurfaceHover` | 圆角药丸（`t.Radius`） |
| 选中 | `t.Selection.Over(t.Background)` | 圆角药丸，沿用 E7 定案 |

几何（常量在 `view.go`）：

- **行距 `rowSlotH = 48`** 不变，与生产 `ROW_HEIGHT`（`search.rs:144`）对齐；
- **药丸高 `rowSlotH - rowGap = 40`**，`rowGap = 4`，即上下各留 2 DIP；
- **药丸左右各缩进 `rowInsetX = 6`**，不贴着列表边缘。

**为什么不选另外两个候选**：

- **「hover 行加框+高亮」**——用户说它也算过关，但一圈描边在 8 行密排的列表里
  会重新变成「一排小盒子」，正是他嫌弃的东西。**圆角药丸本身就是 Win11 设置
  项、开始菜单、CmdPal 条目的通用语言**，比描边克制，且三态里只有 hover/选中
  两态有形，静止态彻底干净。
- **「浅色不拉满的分割线」**——查过 mygo 的能力，两条路都不通：
  `ui.Divider` 是 `coreBox` 铺 `t.Border` 的 1 DIP 实心条（`widgets.go:39-46`），
  **没有任何 padding / 宽度 / 颜色参数**，想要「内缩的浅线」只能自己拿
  `ui.Box(c).Height(1).MarginX(...)` 拼一条，等于绕开控件自绘；而
  `Element.Dividers(width, color)`（`element.go:1034-1041`）虽能在 List 行间
  画线，却是**横贯整条列表**的（`paint.go:532-552` 的 `lo/hi` 取列表左右内
  边界）——正是用户说的「拉满」，且 7 条横线会把列表切成格子、更吵。**本轮
  不加分割线**，只靠药丸 + 留白建立层次；若真机觉得行间太空再补。

**一个隐蔽的第二个框源（必须一起处理）**：外层行节点在选中时铺**满行槽**的
不透明 `t.Accent`、hover 时铺满行槽的 `t.SurfaceHover`（`list.go:582/:599-602`）。
而我们的药丸是内缩的、盖不满行槽——不处理的话，选中行会在药丸四周露出一圈
**蓝边**、hover 行露出一圈灰边。故外层 `ui.ButtonBase` 铺一层不透明
`t.Background` 当**遮罩**（root 本来就铺这个色，视觉上多不出东西）。单测
`TestSelectedRowNoAccentBleed` 守着这条。

**仍然不动的（§7.5 预算）**：`ui.Shadow` 不给列表行（E4）；`Element.Blur` /
`Element.Effect` 禁用（G3）；无渐变（E8）；无滚动/入场/颜色动画（E10–E12）。
本轮的遮罩与药丸都是**普通 scene fill**，不新增任何 GPU 资源、不画两遍子树，
不违反 G1/G3。

**单击命中链与键盘焦点未被破坏**：`coreButtonBase` 与 `coreButton` 共用同一
函数体（`base.go:25-29`），`flagClickable` / `flagHover` / `Focusable` 全部保留，
只是去掉了主题外观；行内容仍是按钮的 `Children`（祖先关系，见 §7.8 的第 3 条）。
因此 §7.8 的两条配套约束依然必要且依然成立：按钮仍可获焦点（点击会抢焦点），
故 `view()` 末尾**每帧** `st.input.Focus()` 的回抢照旧。单测
`TestRowSingleClickExecutesOnce`（单击序列）与
`TestRowClickKeepsKeyboardNavigation`（点后 ↑↓ 仍归应用层）均已改为用
`ButtonBase` 结构并通过。

### 7.6 面板定位（决策④ + 面板中心 48% 定案）

**根因（已核实）**：PoC 的 `showPanel` 原用**指针所在显示器**——
`panel.go:163` `mygo.Screen.DisplayNearestPoint(mygo.Screen.CursorScreenPoint())`。
唤起时指针可能在副屏 → 面板落在副屏；副屏分辨率 / 缩放 / DPI 与主屏不同，
而面板尺寸是**固定 DIP 常量**，副屏若更小或缩放不同，就会「尺寸不对 /
定位越界」。

**已定案（已实施）**：**改按主显示器定位**，对齐生产 voke。`showPanel` 已
改成 `d := mygo.Screen.PrimaryDisplay()`。签名核实：`func (s *ScreenModule)
PrimaryDisplay() Display`（`modules.go:243`），实现即遍历 `Displays()` 找
`Primary == true`、找不到退回第一个；`Display` 的 `WorkArea` /
`ScaleFactor` / `Primary` 字段齐备（`modules.go:201-212`）。
**不需要任何新 API**——`PrimaryDisplay` 与原来的 `DisplayNearestPoint` 在
同一个模块里。

**Rust 侧的对应行为**（已核实，确认「Rust 版总在主显示器」的说法成立）：
`voke-app/src/main.rs:426` `Bounds::centered(None, search::window_size(), cx)`，
`display_id = None` 时 gpui 取 `cx.primary_display()`
（`gpui-pre-0.3.8/src/geometry.rs:740-754`）。

**风险**：低。多显示器用户失去「在哪个屏唤起就在哪个屏」的便利，但这
**与生产 voke 一致**，不算行为退化。若该改动真出问题，降级方案是：保持
指针显示器定位，但按目标显示器的 `WorkArea` 与 `ScaleFactor` 夹紧尺寸与
坐标——属「修了一半」，**不列入遗留**。

**定位算术不需要按主屏重算（容易想错，记档）**：换的只是「哪块屏的
WorkArea」，**DIP 体系**一个字都不用改，也**不要再乘 `ScaleFactor`**
（乘了就双重缩放）。

- `Displays()` 返回的 `WorkArea` **已经是 DIP**
  （`internal/windows/system.go:358-366` 的 `conv` 用该显示器自己的 dpi 做
  `toDIP`）；`ScaleFactor` 字段只是给需要像素尺寸的人用的。
- `SetPosition` 收 DIP（`window.go:879` → native `SetBounds` 用
  `dpiOf(hwnd)` 反向 `toPx`，`internal/windows/window.go:622-625`），与上面
  同一体系。故 `WorkArea` / `Size()` / `SetPosition` 三者同系，直接相减即可。
- **跨屏不是问题**：`d` 是哪块屏就用那块屏自己的 `WorkArea` 与自己的 dpi
  换算，各屏自洽。进程是 PerMonitorAwareV2
  （`internal/windows/app.go:113-114`），`dpiOf(hwnd)` 走 `GetDpiForWindow`
  随窗口所在屏变化，所以「先 `SetPosition` 后 `Show()`」的顺序下换算基准正确。
- **尺寸不夹紧 WorkArea**：面板 680×494 是与生产对齐的固定值（生产 680×472，
  `search.rs:1830-1835`），主屏 WorkArea 恒大于它。Rust 侧也只做
  `size.min(&visible_bounds.size)` 的下限保护，语义相同。
- `Screen.Displays()` 走 `onMainValue`，**只许主线程调**——`showPanel` 本来
  就在主线程（热键回调直调），`PrimaryDisplay()` 内部就是遍历 `Displays()`，
  同理无问题。

> ⚠️ 原先只核实了「用哪块屏」、**没核实「放在多高」**，1/3 是估值。

#### 7.6.1 四家参照的源码依据

⚠️ **关键口径坑：CmdPal 与 Run 锚的都是「搜索框中心」而不是「面板顶」**——
这一条在下一小节被证明是本轮真正的坑。

- **生产 voke（Rust）——面板正中**：`Bounds::centered_at(
  visible_bounds.center(), …)` 即 `y = (WorkArea.Height - h)/2`，
  **纯正中无偏移**（`gpui-pre-0.3.8/src/geometry.rs:882-888`）。
  尺寸 680 × (48×8 + 88 + 24)。
- **CmdPal——搜索框中心 = 0.25·H**：`MainWindow.xaml.cs:413-414`
  `desiredCardCenterY = workArea.Y + Round(workArea.Height *
  fractionFromTop)`，`:396-399` 注释明确这个 Y 是**搜索框的中心**
  （"not the much larger transparent HWND"）。`fractionFromTop = 1 - pct/100`
  （`:474-480`），`pct` 默认 **75**（`SettingsModel.cs:20`）→ 0.25。
  有下限保护（`CompactPositionConstraints` 以
  `MinimumExpandedSpaceBelowSearchDip = 420` 反推最小百分比）。
  非 compact 模式走 `CenterOnDisplay` → 也是**正中**。
- **PowerToys Run——`(H - SearchBox 高度) / 4`**：
  `src/modules/launcher/PowerLauncher/MainLauncher/MainWindow.xaml.cs:490-493`
  的 `MoveToScreenTopCenter`。除数是 4 且减的是 **SearchBox 高度**——同样锚在
  搜索框。
- **Wox——按最大窗口高度居中**：`wox.core/ui/position.go`
  `getCenterLocation`：`y = size.Y + (size.Height - maxWindowHeight)/2`，
  注释说明「窗口顶固定、向下生长，展开到最大高度时正好居中」。

#### 7.6.2 「42% 仍偏高」的根因：锚点错了，不是系数不够大

**这一节是本轮最重要的排查记录，写下来是为了别再拍脑袋定系数。**

第二轮真机反馈原话是「**还是高**」——上一轮刚从 1/3 调到 42 之后。于是先做
一件上一轮没做的事：**实测 chrome 几何，而不是估值**。

用 `ui.NewTester` 渲染与 `view.go` 同款的面板 chrome，给输入行/分隔线铺底
色后从渲染结果逐像素反读（探针已删，结论如下）：

| 区段 | y 范围 | 高度 | 构成 |
|---|---|---|---|
| 输入行 | 0..55 | **56** | `Row.Padding(16,18)` + TextInput 高 24 |
| 分隔线 | 56 | 1 | `ui.Divider` |
| 列表区 | 57..459 | 403 | `Padding(6)` + 8 行 × 48 + 余量 |
| 分隔线 | 460 | 1 | `ui.Divider` |
| 提示行 | 461..493 | 33 | `Row.Padding(8,16)` + 11px 文字 |

**输入框中心 = 面板顶 + 28 DIP**（不是上一轮注释估的 64 DIP 的一半 32）。

拿实测值重算 42%：

| 实现 | 算术 | 面板顶 y | 面板中心 | **输入框中心占 work area** |
|---|---|---|---|---|
| CmdPal | 搜索框中心 = 0.25·H | ≈228 | ≈25%（卡片仅 ~48 高） | **25.0%** |
| PowerToys Run | (H − 48)/4 + 24 | ≈248 | ≈27% | **26.2%** |
| 生产 voke（Rust） | (H − h)/2 | 273 | **50.0%** | **28.8%** |
| PoC 原值 1/3 | (H − h)/3 | 180 | 41.1% | **20.0%** ← 比谁都高 |
| **PoC 42%（顶口径）** | (H − h)×0.42 | 229 | **45.8%** | **24.7%** |

**结论：42% 确实把输入框中心放在 24.7%，与 CmdPal 的 25.0% 几乎重合——
可用户仍然说「偏高」。所以问题不在系数，在锚点。**

根因是**拿两家的数字套一个并不共通的口径**：

- CmdPal 的 25% 锚在搜索框中心，而它那块卡片 compact 模式下**总高只有约
  48 DIP**（只显示搜索行）。对它而言「搜索框中心」≈「整块卡片的中心」，
  25% 同时也是卡片中心，两个口径**恰好重合**。
- 本面板**总高 494 DIP、可见 8 行**，输入框中心距面板顶只有 28 DIP，与面板
  几何中心相距 **247 DIP**。把输入框锚在 25%，等于把 494 高的整块面板的
  **视觉重心**（1080p 下 ≈46%）一起拽到上半屏。

眼睛读的是**整块面板的体量**，不是顶部那一条输入框。于是「输入框位置算对了」
但「整块面板仍然偏高」——**上两轮一直在调一个调不动的量**。

#### 7.6.3 定案：改锚「面板中心」，系数 48

算术改为（`panel.go` 的 `panelTopLeft`，**全整数运算**）：

```
cy = WorkArea.Y + WorkArea.Height * pct / 100   ← 面板中心
y  = cy - 面板高 / 2                             ← 面板顶
x  = WorkArea.X + (WorkArea.Width - 面板宽) / 2   ← 水平居中（不变）
```

**`panelCenterPercent = 48`** —— 面板中心落在 work area 的 48%，即正中偏上
2%，第一次**直接表达**用户那句「居中略偏上」。

| 分辨率 | 旧 42%（顶口径）面板中心 | 新 48%（中心口径）面板中心 | 新值输入框中心 |
|---|---|---|---|
| 1080p（work area 1040） | 45.8% | **48.0%** | 27.9% |
| 1440p（work area 1400） | 44.8% | **48.0%** | 33.0% |

参照：生产 voke 是纯正中（面板中心 50%、输入框中心 28.8%）、CmdPal 输入框
中心 25.0%。新值落在两者之间、偏 Rust 一侧，与用户「CmdPal 与 Rust 版高度
差不多」的观感一致。**真机若仍想再低，把 48 改成 50 即完全对齐 Rust 的正中，
语义不变、只挪一个系数。**

**为什么换成「work area 的百分比」而不是「顶占剩余空间的百分比」**——后者
有两个实测毛病：

1. **与面板高度耦合**：改 `panelHeight` 会悄悄挪位置，且看不出因果。
2. **跨分辨率漂移**：同一个 42% 在 1080p 是面板中心 45.8%、在 1440p 却是
   44.8%（面板占屏比越小，「剩余空间的百分比」越难线性映射到中心）——
   也就是**屏越高越偏上**。新口径在两档分辨率上都稳定落在 48.0%。

**未改动的部分**：水平仍正中；**面板尺寸不变**（680×494、可见 8 行，与生产
8 行对齐，本轮另有单测 `TestPanelStillShowsEightRows` 守着）；DIP 体系口径
不变、**绝不乘 `ScaleFactor`**；显示器来源仍是 `PrimaryDisplay()`
（用户明确要求**不要**为此改逻辑）。

**方法论教训（写给下次）**：定任何「位置/尺寸」系数前，**先用探针把宿主真实
布局量出来**，别用组件文档里的估值（上一轮「输入行 ~64 DIP」就是估值，实测
56）；并且确认**参照物的口径和自己要设的口径是不是同一个**——本轮两轮不达标的
根因就是拿「小卡片的高度口径」去套「大面板的观感」。

### 7.7 唤起懒加载（真机反馈第二轮）

**用户反馈**：唤起即全量加载应用列表；目标是**唤起时只有一个空输入框**，
不渲染列表、不触发应用枚举 / 索引，等用户打字产生过滤需求时再加载
（参照 CmdPal：唤起先出输入框，结果随输入异步出现）。

**触发点设计**（`apps.go` 的 `ensureApps()` 为唯一入口）：

| 时机 | 是否触发枚举 | 落点 |
|---|---|---|
| 进程启动 | ❌ | `ready()` 只注册 `bindApps` 回调，不调 `ensureApps` |
| 唤起面板 | ❌ | `showPanel` 清查询 + `filter()`，不碰 `ensureApps` |
| 搜索框内容变为**非空** | ✅ | `view.go` 搜索行 `input.Changed()` → `onQueryChanged()` → `ensureApps()` |
| Esc 清空查询 | ❌ | `onQueryChanged` 判 `TrimSpace(query) != ""` 才触发 |
| Enter 启动后复位 | ❌ | 走 `filter()`，不经 `ensureApps` |
| **（将来）apps 前缀键激活** | ✅ | 前缀机制（§7.3）**已实现**（engine.Scan 全等路由 + Direct/Indirect + 各 provider 前缀接线）；apps 专用前缀键作为额外触发入口仍未接（可选，非生产必需），接入点即前缀激活处调 `appsmodel.EnsureLoaded()` |

**并发纪律落实点**：

1. `Shell.InstalledApps` 是**唯一后台安全**的 Shell API → 整段枚举留在
   后台 goroutine。
2. `Shell.FileIcon` 走 `onMainValue` **阻塞等待主线程** → 图标**只由
   `row()` 在可见行上取**（`ui.List` 虚拟化，未进屏的行根本不构建），
   **没有任何批量预取**；进程内 FIFO 缓存上限 512 不变。懒加载后这一点
   更强：唤起时 `results` 为空，连一行图标都不会取。
3. **singleflight 去重**：`ensureApps` 用 `appsLoadMu` + `appsLoaded` 做
   「已触发就返回」。触发点是每次按键，`"wxl"` 会连打 3 次，不加闸门会
   并发跑 3 个 goroutine 各自枚举一遍。
4. 结果回主线程只经 `Window.Update`（异步投递）。

**空态 UI**（`view.go` 的 `emptyHint()`，四态分明，不留空白）：

| 状态 | 文案 |
|---|---|
| 从没加载过（唤起后的默认态） | **输入以搜索应用** |
| 加载中（≤10s） | 正在枚举应用… |
| 加载完成但无命中 | 没有匹配的应用 |
| 枚举失败 | 暂无应用（枚举失败见日志） |

> `st.loaded` 专门用来区分前两种「空」——否则唤起时「列表为空」既可能是
> 没加载、也可能是加载完没命中，文案会误导。10s 超时兜底沿用；失败也会
> 回调（`apply(nil)`），故正常路径不会卡在 loading。

**已核实的前提**：`ui.List` 在 `n=0` 时**安全**——`plan()` 直接
`if n == 0 { return 0, 0, 0 }`（`ui/list.go:513-516`），不构建任何行、
不 panic、空态文案正常渲染；且 n 从 0 变 3 时列表能正常出现（不闪不塌）。
两条都有单测覆盖。

### 7.8 列表项单击即执行（真机反馈第二轮）

**用户反馈**：列表项要双击或 Enter 才执行，改成单击即执行。

**结论先行**：行内**自造双击检测是错路**，正确做法是整行包一层
`ui.Button` 用其 `Clicked()`。下面是三条被实测排除的替代方案，
**全部结论来自 mygo 自带 Tester 脚手架跑真实指针 / 按键事件**，不是读码
推测（探针结论记录在此，避免重写期再踩一遍）：

- **排除方案 ①：读行节点自己的点击态**——不可行。mygo 内部确实为每行算好
  了 `rowBuild{clicked, double}`（`ui/list.go:573`），但 `rowBuild` 与
  `Context.row` **都是未导出字段**（`context.go:47`），应用层读不到；导出的
  `ui.ListRow`（`list_builder.go:4-12`）也只暴露 `Selected()` /
  `ListFocused()`。
- **排除方案 ②：拿 `ListState.Changed()` 当「单击」信号**——不可行，
  **会被键盘导航污染**。点击行时 mygo 会把焦点抢给列表
  （`value_input.go:325` `f.owner.Focus()`），此后 ↑↓ 由列表自己的
  `navigate()`（`list.go:926-970`）消费并**同样打 `Changed`**。实测：点一次
  行再按一次 ↓，`Changed` 立刻又响一次——若拿它当单击信号，**键盘移动
  选中就会误启动应用**。（缓解手段是每帧回抢焦点，但那只是把概率压低，
  不该把「选中变了」等同于「用户点了」。）
- **排除方案 ③：在行内容里放可点击元素**——必须注意**祖先 vs 兄弟**。
  mygo 的命中链从**最内层命中元素**起、**只沿 parent 向上**走
  （`ui/input.go:141-146`），**兄弟节点根本不在链上**。实测：`ui.Button`
  与行内容平级摆放时 `Clicked()` 恒为 false；把内容用 `.Children(...)` 塞进
  Button 里，点击立刻正常。**这条是本轮最容易复发的坑**，已写进 `view.go`
  `row()` 的注释。

**采用方案**：`row()` 用 `ui.Button` 包住整行内容，`btn.Clicked()` 里直接
启动指定行（`clickRow(i)`）。注意 `ui.ButtonBase` 虽是可点击的无外观基类，
但**没有 local state**，`Clicked()` 不生效，别用。

**防误触处理**（针对「点 A 再点 B 会不会执行两次」）：

- mygo 在 `pointerUp` 里 `s.clicks++`（`input.go:412-414`），**一帧一清**，
  一次按下-抬起对应一次 `Clicked()`，不存在一帧内累积多次。
- 实测连点不同行 `[BB DD AA CC BB]` 得到**一一对应**的执行序列，无重复、
  无漏触发。
- 双击 = 两次单击 = 执行两次，但**第一次就隐藏面板**了，第二次点击落在
  别处，天然不会重复启动同一个应用。
- `clickRow(i)` 直接用**被点的那一行 i**，不复用 `st.chosen`，避免与键盘
  选中状态打架；启动前先 `st.chosen = i`，保证「点哪行亮哪行」在任何路径下
  都成立。

**配套约束（不做就会坏）**：mygo 的按钮**可获焦点**（`base.go:25`
`.Focusable()`）。点击它会把焦点从搜索框抢走，此后 ↑↓ 不再抵达视图的
`c.Shortcut`。实测去掉回抢焦点后，点击行再按 ↑↓，**应用层收到 0 次**。
故 `view()` 末尾**每帧**调 `st.input.Focus()`；该操作幂等（`focusOn` 里
`if rt.focused != s.id` 才重置 `focusVisible/blinkStart`，`access.go:566-568`），
焦点没变即空操作。注意必须放在 `Column` **之后**：焦点变更要在本帧控件构建
完后生效。

**hover 的归属变了（2026-10-16 第三轮，行视觉重做的连带影响）**：原先 hover
由 `ui.List` 自带的行 `styleFn` 提供（`ui/list.go:599-602`，满行槽铺
`t.SurfaceHover`）。因为药丸改成**内缩**，那层满行槽的 hover 底会在药丸四周
露出一圈灰边（与选中时的 Accent 边同源，见 §7.5.1「第二个框源」），所以改为
在 `row()` 里**自绘** hover 药丸，并在外层 `ui.ButtonBase` 铺不透明
`t.Background` 把 mygo 那层盖掉。触发机制不变：`coreButtonBase` 带
`flagHover`（`base.go:27`），`setHover` 对带 `flagHover` 的元素要帧重绘
（`ui/input.go:154-179`），指针一动就亮，不需要轮询启发式。
**§7.5 E5 与 BACKLOG 里「键盘抑制 hover」那条仍然不动**（那说的是「键盘输入
后抑制 hover」这层状态，本轮依然没做）。

**单击命中链在本轮未被破坏**：换成 `ui.ButtonBase` 后 `coreButtonBase` 与
`coreButton` 共用同一函数体（`base.go:25-29`），`flagClickable` /
`Focusable` 全保留，行内容仍是按钮的 `Children`——上面第 3 条「可点击元素
必须是祖先」依然成立。单测 `TestRowSingleClickExecutesOnce` 与
`TestRowClickKeepsKeyboardNavigation` 已改用 `ButtonBase` 结构并通过。

### 7.9 mygo 线程模型与 UI 适配层的存在理由

见 §7.1.3（线程模型总纲）与 §8.1（bus factor）。**结论只有一条**：UI 层
必须有一层**薄适配**，把「输入框怎么收键、列表怎么滚、hover 怎么抑制」
这类**行为约定**收在自己手里，而不是散落到各处的 `ui.TextInputBase(...)`
调用里。升级 mygo 时改一个适配文件 + 跑 §6.2 的四件套即可。

---

## 8. 风险与已知不确定项

### 8.1 未解除的风险（**只列当前仍需按风险处理的**）

| # | 风险 | 影响 | 缓解 |
|---|---|---|---|
| 1 | 枚举**来源基数**差异（非「漏 MSIX」，旧述反了）：mygo `InstalledApps` 是 `FOLDERID_AppsFolder` **单源**（已核 mygo internal/windows/apps.go 只 `SHGetKnownFolderItem(AppsFolder)`），Rust 是「开始菜单 `.lnk` ∪ AppsFolder」**双源 + rule-2**（非 MSIX 且与某 `.lnk` 同名则跳过该 AUMID 条）。⇒ Go 可能**漏**无 AUMID 注册的纯 `.lnk` 桌面快捷方式（Rust 经 `enum_shortcuts` 兜到），又**留**了 Rust rule-2 会折叠的 AUMID-Win32 重复项；**MSIX/Store 不属此风险**（AppsFolder 正是其所在，Go 覆盖） | V3 覆盖条目级差异（实测 Go 141 > Rust 135，Δ+6）净方向未逐条归属 → 重写隐性成本 | 三源结构机制 2026-10-18 已核实（Go apps.go / mygo apps.go / Rust voke-sys/apps.rs）；**Δ+6 归因待 §6.3 真机逐条 diff**——应查「有无 Start-Menu-only 快捷方式被 Go 漏」与「Go 多出的 6 条是否即 rule-2 折叠项/不同名重复」，非评分缺陷 |
| 2 | IME 边界情况（候选框位置 / 跨屏 / composing 时序） | V2 不过 → 硬失败 | 源码层已核实（见 §8.2），**真机表现待 §6.2** |
| 3 | Win10 无 Acrylic（`Vibrancy` 仅 Win11 22H2+） | 视觉退化 | 降级为纯色 / 不透底；**Win10 表现必须在 §6.5 记录**。⚠️ 16 档成本相同且不可逆（见 §5.1），**不能靠换档位补救** |
| 4 | MyGo pre-1.0 API 漂移 | 重写阶段跟进成本 | pin v0.5.5；用到的 API 面见 §9，升级前 diff |
| 5 | 44MB 宣称不实（→ 决策反转） | No-Go | 已用 §5.1 的实测取代宣称值；Rust 退路见 §1.3（不需 patch） |
| 7 | 国内网络拉不到 MyGo 模块（默认 proxy.golang.org） | 无法 `go mod tidy` | `go env -w GOPROXY=https://goproxy.cn,direct` |
| 8 | **MyGo 生态 bus factor = 1**（2026-10-16 核实，项目年龄仅 20 天、74 个 tag、无 CHANGELOG / 无 LTS） | 上游停维护 → voke-go 无人接手 | pin 版本；**把 UI 适配层做薄**（§7.9）；关注上游 roadmap issue **#105**（窗口枚举——正是 voke `windows` 插件的唯一真缺口，可提 PR 换话语权）。**决策状态见 §10** |
| 9 | **上游出现「签名不变但语义反转」的变更** | 静默行为漂移，查不出来 | **已发生过一次**（v0.5.0 的 input timing）。作者用 apidiff 门禁，但 **apidiff 查不出语义变更**——这正是 UI 适配层必须存在的唯一理由；升级时**必须跑 §6.2 的 IME + 匹配四件套**，不能只看编译过 |

**风险 6（原「单实例锁不区分二进制」）已解除**——源码核实结论见 §8.2，
不要按风险处理。

### 8.2 源码级已核实结论（**不要再花时间重新调研**）

| # | 结论 | 依据 |
|---|---|---|
| 1 | **单实例按 app id 字符串锁定**，与可执行路径无关 → 与生产 voke **可并存 A/B** | `single_instance.go:46` `RequestSingleInstanceLock()`，锁 key 由 `singleInstanceKey(a.Name())` 生成（`:150-155`）；底层是 **unix socket 文件**（`:159-168`，落在 `XDG_RUNTIME_DIR` / `TempDir` / `/tmp`），第二实例 `net.Dial` 交给首实例后返回 false。`Name()` 默认取可执行文件名去 `.exe`（`app.go:275-290`），`SetName` 可覆盖（`:293`）。`OnSecondInstance`（`:104`）在首实例主线程触发。生产用命名互斥体 `Local\voke.instance`，机制不同 |
| 2 | **热键字符串是 Electron 风格加速器，大小写不敏感**，`"Ctrl+Alt+Space"` 写法正确 | `modules.go:540` `Register(acc, fn)` → `internal/accelerator.Parse`；`accelerator.go:100-126` 按 `+` 分隔、末段为键，修饰符查表前先 `strings.ToLower`（`:141`），`namedKeys`（`:59-96`）里命名键同样大小写不敏感。配套 `Unregister` / `UnregisterAll` / `IsRegistered`（`modules.go:581-625`）；重复注册同一加速器返回 error |
| 3 | **`InstalledApps` 覆盖 MSIX / Store 应用** | Windows 后端枚举的就是 `FOLDERID_AppsFolder` COM（`internal/windows/apps.go:30-68`，含桌面快捷方式与 packaged 应用），与生产的 `enum_store_apps` **同源**；文档亦明言 "desktop apps by their shortcuts and packaged apps alike"（`modules.go:97-98`）。生产保留 AppsFolder 枚举的唯一理由就是覆盖 MSIX / Store（`main.rs:603-611`）。**条目级差异清单仍待 §6.3** |
| 4 | **IME composing 期间按键归输入法**（不改输入法行为） | 文档承诺 `docs/ui/text-input.md:136-140`「the keys typed while an input method composes are its own…they submit nothing, press no shortcut and close no dialog」+ `api_gen.go:4641-4644` 同款；查询面 `Element.Composing()`（`api_gen.go:3177`，实现 `textinput.go:244`）。Windows 实现走 IMM32（`internal/windows/surface.go:629-666`：`WM_IME_STARTCOMPOSITION` / `WM_IME_COMPOSITION` 含 GCS_RESULTSTR 上屏与 GCS_COMPSTR 组字 / `WM_IME_ENDCOMPOSITION`，`imm32.dll` 见 `:33-36`）。composing 期间 Enter/Esc 由 IME 自行消费，不会成为 WM_KEYDOWN 抵达引擎 → shortcut 不触发、输入框不 Submitted。**视图层无需 composing 过滤代码**（自行过滤反而会双切）。**真机表现仍待 §6.2** |
| 5 | **`FileIcon` 的 size 是「DIP × 显示器缩放」，输出 size×size PNG** | `modules.go:55-61` 文档注释 + launcher 示例同款算法（`examples/launcher/main.go:179-184`）。PoC 取 32 DIP × 最大缩放（`sync.OnceValue` 解析一次），渲染侧同为 32 DIP。**清晰度观感仍属 §6.4 验收项** |
| 6 | **fuzzy 匹配器选型：自研移植 nucleo-matcher 0.3.1 的评分算法**（不是换包） | fuzzysearch / sahilm:fuzzy 都是「子序列 + 简单加权」，与 nucleo 的 char-class 边界加分 / 最优 DP / Smart 归一化语义不同，「命中与否」无法保证一致——而 §6.2 的匹配一致性是命脉。移植约 700 行；Unicode 表（case fold 1454 条 + normalize 453 条）用脚本从 nucleo Rust 源码机械提取生成（**勿手改**）。标定结论与语料断言见 §5.3 |
| 7 | **排序差异面**：评分公式 1:1 移植后**同分排序无差异**；残余不一致全部来自拼音数据层。移植按 voke 实际调用裁剪了 nucleo 死路径：Ascii-haystack 快路径（`score_one` 恒传 Unicode haystack，不可达）、indices 高亮（`score_one` 不用）、grapheme 分割（Go 用「基础字符 + 组合符」近似，仅影响含组合符号的查询）。**这些裁剪不影响 voke 的命中与分值** | 语料断言见 §5.3 |
| 8 | **go-pinyin 与 Rust `pinyin` 0.11 的数据覆盖**（28096 码点逐字比对）：**覆盖完全一致**（26704 个有拼音数据的字给出相同集合，无单边缺口）；去重后读音顺序完全一致（0 个反序）。封装层已对齐两处写法差异：ü（Rust `ü` vs go-pinyin `v`）与 ê 声调（go-pinyin 漏剥）。**唯一实质差异**是多音字表重复读音导致的 `VARIANT_LIMIT=8` 退化点分歧（Rust 按含重复的总数判断、go-pinyin 按去重后判断），这是**双向的真实命中差异**（"那种" Go 多命中、"敦促" Go 少命中），语料中因此跳过一部分断言。如需字节级一致：go-pinyin 的 `PinyinDict` 是导出变量，可读原始逗号串绕过去重（放弃 `SinglePinyin` 封装、自付声调剥离）——**重写阶段若要字节级一致再走这条路** | `internal/match/pinyin_test.go` 有对齐抽查 + 归一化 + 退化分歧的可执行记录 |

### 8.3 待真机验证的不确定项

1. **`Power.KeepAwake` 在 Modern Standby + DC 供电下的 5 分钟终止**：
   mygo 走 `PowerCreateRequest` + `PowerSetRequest`（**进程级 HANDLE**），
   生产 voke 走 `SetThreadExecutionState(ES_CONTINUOUS | ES_SYSTEM_REQUIRED)`
   （`wake-sys/src/awake.rs:300`、`:308`）。两者语义不同：Modern Standby 下
   系统会在进入 sleep timeout **5 分钟后**终止 power request，
   `ES_CONTINUOUS` **没有**这条条款。
   **验证**：笔记本接电源、开 Modern Standby，唤起 awake 后静置 >5 分钟，
   观察是否提前允许休眠。**判定**：若提前休眠，在 Go 侧叠加 `ES_CONTINUOUS`
   兜底（`syscall` 直调，属平台层，允许）。
   注意「用户主动睡眠」两者都挡不住，那是现状特性、**不是**迁移风险。
2. **托盘菜单的 awake 倒计时是否需要 tooltip 承载**：mygo 的
   `Tray.SetToolTip`（`modules.go:721`）写的是 `NIF_TIP`，托盘图标只有
   一个 tooltip 槽位；生产 voke 是「菜单里每条 awake 项带剩余分钟」。
   **验证**：真机跑「菜单打开 → 停留 2 分钟 → 看倒计时是否还在走」。
   **判定**：若菜单打开期间倒计时冻结（Win32 `TrackPopupMenu` 进模态循环、
   主线程消息泵被菜单占用，`internal/windows/menu.go:384`），需在菜单弹出前
   预计算截止时间戳、关闭后重算。
3. **`Window.Update` 与 `Show/Hide` 的同步语义差异是否踩坑**（规范见
   §7.1.4）。**验证**：后台枚举完成 → `Update` 改数据 → 同帧 `Show()`，看
   是否有一帧空列表。**判定**：若出现，改为 `Update` 内改数据 +
   `Invalidate`，`Show` 排在其后。
4. **⚠️ Windows 上「隐藏 ≠ 停帧」（硬约束，不是可调项）**：
   `ui/runtime.go:581-593` 的 `next()` 只在 `rt.host.occluded()` 为真时 hold
   住动画，而 `occluded()` 的实现是类型断言 `platform.OccludableSurface`
   （`ui/window.go:229-232`），**只有 darwin 与 web 实现了 `Occluded()`**
   （`internal/darwin/surface.go:274`、`internal/web/surface.go:582`）——
   Windows 后端没有该方法，断言恒失败 → `occluded()` **恒 false**。
   结论：**任何持续动画在 Windows 上即使面板隐藏也会继续请求帧**。
   这是 §7.5 中 E10–E12 被砍的**硬阻塞**原因，mygo 侧**无配置项可关**。

---

## 9. 附录：MyGo v0.5.5 API 面清单（供升级 diff）

> 四个 worker（骨架 / apps+mem / view+panel / match）核实过的 API 签名
> 合并去重版（2026-10-16）。同一声明被多个 worker 引用的只留一处，全部
> 从模块缓存源码核实（`~/go/pkg/mod/github.com/egoist/mygo@v0.5.5/`，含
> `examples/launcher` 对照），未凭记忆书写。

**应用生命周期**（`app.go`）

- `func (a *Application) SetName(name string)`（:293，Run 前调用）
- `func (a *Application) WhenReady(fn func())`（:137）
- `func (a *Application) Run() error`（:86）
- `func (a *Application) Quit()`（:158）
- `func (a *Application) OnWillQuit(fn func(e *QuitEvent)) (off func())`（:436）
- `func (a *Application) OnWindowAllClosed(fn func()) (off func())`（:425）

**单实例**（`single_instance.go`）

- `func (a *Application) RequestSingleInstanceLock() bool`（:46）
- `func (a *Application) OnSecondInstance(fn func(args []string, workingDir string)) (off func())`（:104）

**窗口**（`window.go`）

- `func NewWindow(opts WindowOptions) *Window`（:359）
- `WindowOptions` 关键字段：`Title`；`Width, Height int`（DIP）；`X, Y int`；
  `Hidden`；`Frameless`；`Transparent`；`Vibrancy Vibrancy`；`Panel`；
  `DisableResize`；`Content Content`（`ui.View` 返回值）
- `func (w *Window) Show()`（:655）/ `Hide()`（:685）/ `IsVisible() bool`（:700）
- `func (w *Window) SetPosition(x, y int)`（:879）/ `Size() (width, height int)`（:855）
- `func (w *Window) OnClose(fn func(e *CloseEvent)) (off func())`（:1475）
- `func (w *Window) OnBlur(fn func()) (off func())`（:1484）
- `func (w *Window) Update(fn func())`（`content.go:172`，后台线程回主线程）
- `CloseEvent` 内嵌 `Preventable`，`PreventDefault()` 可取消关闭
  （`events.go:21`、:14）

**热键**（`modules.go`）

- `func (g *GlobalShortcutModule) Register(acc string, fn func()) error`（:540）
- `Unregister(acc string)`（:593）/ `UnregisterAll()`（:612）/
  `IsRegistered(acc string) bool`（:581）

**托盘 / 菜单**（`modules.go`、`menu.go`）

- `func NewTray(opts TrayOptions) (*Tray, error)`（`modules.go:665`，
  必须在 `WhenReady` 里调）
- `TrayOptions{Icon []byte, IconIsTemplate bool, Title, ToolTip string, Menu *Menu}`
- `func (t *Tray) SetMenu(m *Menu)`（:725）/ `Destroy()`（:751）
- `func NewMenu(items []*MenuItem) *Menu`（`menu.go:148`）
- `MenuItem{ID, Label string; Type MenuItemType; Role MenuRole;
  Accelerator string; Checked bool; Click func(item *MenuItem, win *Window)}`
  （`menu.go:89`）
- `func Separator() *MenuItem`（:127）；`RoleQuit MenuRole = "quit"`（:58）

**屏幕**（`modules.go`）

- `var Screen = &ScreenModule{}`（:220）
- `func (s *ScreenModule) Displays() []Display`（:223）
- `func (s *ScreenModule) PrimaryDisplay() Display`（:243）
- `func (s *ScreenModule) CursorScreenPoint() Point`（:259）
- `func (s *ScreenModule) DisplayNearestPoint(p Point) Display`（:265）
- `Display{Bounds, WorkArea Rectangle; ScaleFactor float64; Primary bool; …}`（:201）；
  `Rectangle{X, Y, Width, Height int}`、`Point{X, Y int}`（`window.go:49,56`）

**Shell**（`modules.go`）

- `func (ShellModule) InstalledApps() ([]InstalledApp, error)`（:103）
- `func (ShellModule) FileIcon(path string, size int) ([]byte, error)`（:62）
- `func (ShellModule) LaunchApp(app InstalledApp) error`（:124）
- `InstalledApp{Name, ID, Path string}`（:77）

**Vibrancy**（`vibrancy.go`）

- `VibrancyPopover Vibrancy = "popover"`（:29）

**ui 包**（`ui/`）

- `func View(view func(c *Context)) *Content`（`api_gen.go:2518`）
- `func Column(c *Context) Element`（:907）/ `Row`（:1649）/
  `Text(c *Context, s string)`（:2138）/ `Divider`（:1020）
- `func TextInputBase(c *Context, value *string) Element`（:2214）
- `func List(c *Context, s *ListState, n int, row ...func(int)) Element`
  （`list_builder.go:22`）
- `ListState{Handle; Key func(row int) any; Selected *int; …}`
  （`list.go:38`）；`func (s *ListState) ScrollIntoView(row int)`（:212）
- `type Handle struct`（`ref.go:7`）；`func (r *Handle) Focus(window ...*Context)`
- `func (e Element) Bind(r *Handle) Element`（`ref.go`）
- Element 构建器（`api_gen.go`）：`Fill()`（:3699）`Grow(f float32)`（:3973）
  `Padding(v ...float32)`（:4703）`Gap(v float32)`（:3901）
  `AlignItems(a Align)`（:2613）`Children(fn func())`（`frame.go:122`）
  `FontSize(v float32)`（:3878）`TextColor(c Color)`（:5441）
  `SingleLine()`（:5268）`Placeholder(s string)`（:4796）
  `Label(s string)`（:4243）`Changed() bool`（:3019）
  `Submitted() bool`（:5380）`Size(w, h float32)`（:5279）
  `Background(c Color)`（:2812）
- `Context`：`Theme() *Theme`（`context.go:308`）`Vibrancy() bool`（:355）
  `Root()`（:362）`Shortcut(mods Modifiers, key Key) bool`（:439）
  `OnShortcut(mods Modifiers, key Key, fn func())`（`actions.go:51`）
- 按键常量（`keys.go`）：`KeyEscape`/`KeyEnter`/`KeyDown`/`KeyUp`；
  `var Cmd Modifiers`（Windows 上 = Ctrl）
- `var Transparent = Color{}`（`color.go:23`）；
  `func (c Color) Alpha(a float32) Color`（`color.go:93`，返回 alpha
  相乘后的新色）；`func (c Color) Over(bg Color) Color`（`color.go:120-125`）
- `Center Align`（`element.go:21`）：`AlignItems` 的居中取值
- 主题字段（`theme.go:7-55`）：`Background`(:10)/`Surface`(:13)/
  `Text`(:20)/`TextMuted`(:21)/`Accent`(:24)/`AccentText`(:27)/
  `Selection`(:34)/`Radius`(:48)/`FontSize`(:55，Windows 默认 14，:98-103)/
  `SurfaceHover`(:110)/`AccentHover`(:116)/`Focus`(:123)

**图片 / 图标**

- `func Image(c *Context, src ImageSource) Element`（`api_gen.go:1269`）：
  显示位图或 SVG，默认按图片像素尺寸以 DIP 显示，可 `Size(w,h)` 缩放
- `type ImageSource interface{ imageSize() (w, h float32) }`
  （`widgets.go:520-522`），实现者 `*Bitmap` / `*SVG`
  （`coreImage` `widgets.go:527-541`）
- `func DecodeBitmap(data []byte) (*Bitmap, error)`（`bitmap.go:56-69`）：
  纯 Go 解 PNG/JPEG/GIF(首帧)/WebP/BMP（`bitmap.go:7-13` 注册解码器），
  **无平台门控**，Linux 原生可跑；返回 nil 安全（`bitmap.go:98-103`）
- `func NewBitmap(img image.Image) *Bitmap`（`bitmap.go:48`）；
  `func (b *Bitmap) Size() (w, h int)`（`bitmap.go:96`，像素）
- `func Avatar(c *Context, name string, image *Bitmap) Element`
  （`api_gen.go:671`）：image 传 nil 时按 name 首字母 + 主题色占位，
  图标缺失降级用它（launcher 示例同款）
- 元素尺寸链（`api_gen.go`）：`Height(v float32)`(:4021)、
  `PaddingX(v float32)`(:4737)、`Radius(r ...float32)`(:4851)、
  `Size(w,h float32)`(:5279，DIP)
- `func (c *Context) Invalidate()`（`frame.go:41`，任何 goroutine 可请求帧）

**输入法 / 键盘路由**

- `func (_handle Element) Composing() bool`（`api_gen.go:3177`，
  实现 `textinput.go:244`：`ed != nil && ed.compose != ""`）：
  输入法是否正在组字
- `func (_handle Element) Hovered() bool`（`api_gen.go:4060`，实现
  `input.go:1046`）：指针是否悬停（**查询，非回调**）
- `func (_handle Element) PointerPosition() (x, y float32, over bool)`
  （`api_gen.go:4809`，实现 `input.go:1168`）
- `func (e *node) Changed() bool`（`input.go:1313`）/
  `Submitted() bool`（`input.go:1326`，仅单行输入、Enter 触发）：
  帧内幂等查询，可同帧连调
- `func (b Button) Clicked() bool`：`ui.ButtonBase` 是可点击的无外观基类
  （`base.go:23-29`）但**无 local state**、`Clicked()` 不生效；**必须用
  `ui.Button`**（`base.go:25` 带 `.Focusable()`）
- 键盘路由（`input.go:597-688` `keyDownPhysical`）：聚焦编辑器 `wants` 的键
  被编辑器排队消费（`editor_navigation.go:27-61`：单行无修饰时只认
  Backspace/Left/Right/Home/End/Delete/Enter；↑↓ 与 Esc **不**消费），
  其余键进 `rt.keys`(:685) → 帧构建时 `Context.Shortcut` 查询命中
  （`routeKeys` `input.go:776-828`）

---

## 10. 当前进度、待决策与遗留汇总

> **本章是待决策与遗留的唯一出处。** 其它章节只陈述已定案的设计。

### 10.1 当前进度

**已完成（2026-10-16）**：骨架 + apps/mem + view/panel + 匹配引擎 1:1
（用例绿）+ 语料断言 + 面板中心 48% 定位 + 唤起懒加载 + 单击执行 +
字号体系 + 特效取舍。**六项验证全过**（命令见 §2.1）。

**新增单测**（`panel_behavior_test.go`，全部用 mygo 自带 Tester 跑真实
事件）：定位系数换算关系、四态空态文案、懒加载 singleflight 只触发一次、
`n=0` 空列表渲染与加载过渡、单击执行序列、单击后键盘导航仍可用。其中三条
做了**变异测试**（故意改坏实现，确认测试会红：系数 42→33、Button 改回
平级、去掉焦点回抢），确保不是空转断言。

**真机验证进度**：

| 项 | 状态 |
|---|---|
| V1 内存 | **已取三版数据**（见 §5.1），但 **A/B 同跑差值未做**（见 §5.2 的历史 A/B） |
| V2–V7 | **§6.8 表已非空**（此前「全部未做/表仍空」表述已过时）：V3 枚举通过(核心,141 条)、V5 面板/V6 热键/V7 单实例 部分(核心) 经 agent WSL interop 代跑取证、awake+clipboard 自检失败项=0；唯 **V2 IME 手感 / V4 图标观感** 仍需真人（无输入路径 / 无渲染像素证据） |

**接手中（重写期开工，2026-10-16 后续会话，非新结论、仅进度）**：

- **HEAD 重部署（最新，2026-10-17 会话续）**：自 `bc9714a`（上一条「现部署版 md5 `1ef800e9`」）后 HEAD 又并入 workspaces 纯 Go SQLite 数据源（引 `glebarez/go-sqlite`，体积 11.5MB→15.7MB 即此因）、Global 聚合数据/渲染/交互接线、step8b 生命周期装配迁 `internal/app`、以及本轮**应用列表唤起重扫 + 60s 防抖**（`a9bbbd7`，直接改 V5 面板唤起行为）。为让 V5 打在含此行为的现码上，在 HEAD `a9bbbd7` 重跑 §2.1 交付交叉构建：PE32+ GUI 子系统、md5 `71cfd777`，`mv` 旧 `voke-go.exe → voke-go.exe.runbak11`（保留、可回退）后 `cp` 新码到 `%USERPROFILE%\voke-go-poc\voke-go.exe`，部署后 md5 复核一致。本轮 WSL interop 报不到 `tasklist`，无法确认运行态，故走「改名再覆盖」而非「直接覆盖」——改名对运行中的 exe 也成立（Windows 允许），两种状态都安全。**V2–V7 仍待用户真机回填 §6.8**（mygo GUI 运行期 headless 到不了，非码功能缺口）。至此**按文档的全功能迁移，其 headless 可证部分已全部落地并重新部署**；余下仅真机验收，与少数上游阻塞/超生产的已砍项（BACKLOG §7 记录：动画 E10–E12 被上游遮挡检测阻塞、键盘后抑制 hover、跟随指针屏唤起等，均非生产既有功能）。
- **续部署（同会话，行为变更）**：`8fab436`「托盘 awake 状态条目直达 awake 插件」（对齐生产 `open_awake`，改 V7 托盘交互 + V5 面板初始态，**属行为变更**）重跑交叉构建 md5 `14a2506f`，`mv` 上一条部署版为 `runbak12` 后覆盖部署、md5 复核一致。**其后的 `32e8b8f` 仅订正过时注释/BACKLOG 措辞、不改行为，故无需再部署**（现部署版仍是 `14a2506f`，行为 == 当前 HEAD）。故用户 V 跑应同时验：托盘点 awake 项→面板直达 awake 第一级（两条亮屏方向候选）；装/卸应用后距上次扫描 ≥60s 的下次唤起自动重扫。
- **续部署（同会话，行为变更 2）**：`f0ec3a7`「random『重新生成』接上重掷执行」（原 no-op，改 V-random 交互，行为变更）交叉构建 md5 `f32593f4`→`runbak13`；`073e5a2`「apps-only 全局按频率排序 + 启动记频率」（对齐生产 `engine.rank` 的 frequency_boost，改 V2/V3 排序手感，行为变更）交叉构建 md5 `fe22e2dd`→`runbak14`。**现部署版＝`fe22e2dd`，行为 == 当前 HEAD**（其后仅注释/BACKLOG 措辞改动不改码，无需再部署）。用户 V 跑另需验：反复启动某应用后，空查询与查询命中里它越排越前；random 二级点「重新生成」值行确变、面板不关。
- **续部署（同会话，行为变更 3）**：`0809e05`「设置窗补『关于』区」（版本 + 配置文件位置，只读，补生产 general_page 第四 group）交叉构建 md5 `8f054265`→`runbak15`，为现部署版（行为 == HEAD；其后的 BACKLOG 措辞改动不改码）。V 跑时设置窗应见「关于」段显示版本（PoC 本地构建多显 `(devel)`，属如实）与 config.json 实际落点。
- **续部署（同会话，能力新增）**：`selftest.go` 启动自检 `VOKe_GO_SELFTEST=1`（对照生产 awake_selftest；env 门控，不设即休眠、不改常态行为）交叉构建 md5 `343e9e69`→`runbak16`，为现部署版。用户可用它一次性自动验进程内 awake 状态链（免半小时人肉观察），命令：在 PowerShell 里 `$env:VOKE_GO_SELFTEST=1; & "$env:USERPROFILE\voke-go-poc\voke-go.exe"`（GUI 无控制台，看 `%APPDATA%\voke-go\voke-go.log` 的「awake 自检 / 启动自检结束，失败项 =」行）。
- **续部署（同会话，行为变更）**：`66a279a`「启动工作目录纠正 `FixOwnCwd`」（模块级覆盖审计发现：开机自启继承 System32 cwd 会传给 os/exec 直启的子进程）交叉构建 md5 `ca022f61`→`runbak17`，为现部署版（行为 == HEAD）。V 跑时若走自启，日志应见「启动时将工作目录从 … 修正为 …」；手动从终端启动则见「工作目录正常，未调整」。
- **续部署（同会话，行为变更 2）**：托盘「显示面板」项由无条件 `showPanel` 改为 `togglePanel`（对齐生产 main.rs:301→`panel_toggle_win32`：该项映射 TogglePanel，前台可见则隐藏、否则唤起；也与 Go 自身热键一致）。交叉构建 md5 `54aba095`→`runbak18`，为现部署版（行为 == HEAD）。OnSecondInstance 仍无条件 showPanel（对应生产 ShowPanel 固定显示），二者各自对齐生产。V7 跑时：面板在前台时点托盘「显示面板」应隐藏它。
- **provider 逐插件 parity 审计（本轮，对照 Rust 源码 + oracle 测试）**：核查 5 个纯逻辑插件，修两处**真缺陷**——① `base64` dec 分支直接用 `StdEncoding`，不剥内部 ASCII 空白，粘贴折行（76 列）/ 含空格的 base64 会误报「解码失败」；Rust `decode` 先 `filter(is_ascii_whitespace)`（其单测 `whitespace_is_ignored_when_decoding` 钉死），Go 补 `stripASCIISpace` 对齐。② `uuid` 的 `requestedCount` 用 `strconv.Atoi` 吃下负号、原只挡 `n==0`，输入 `-1` 会流到 `make([]Match, 0, count)` 触发**负容量运行时 panic**；Rust `parse::<usize>()` 本就拒负，Go 改 `n<=0` 一律拒绝并补 `-1/1.5/x:3` 用例。`calculator`/`timestamp`/`random`/`terminal`/`workspaces` 逐函数核对判据一致，未发现行为偏差（**订正**：曾记 `random.validate` 天文级区间 i64 溢出「不可达」，实为可达——`"-9223372036854775808..9223372036854775807"` 令 `high-low` 溢出成负、被旧 `-span` 折成小值误判合法；已按 Rust i128 改为 `span<0 或 >maxRangeSpan` 一律拒（commit `2eb3244`，现部署 `a0966bdf`），并由此连带令 `randomInt` 的 `high-low+1` 真不可达）。**另补齐 `apps`/`windows` 两插件核对**（此前只跑了 5 个纯逻辑插件）：`windows` 逐函数 faithful（空查询不截断、title/process 取 max、sort+truncate 50、`RegeneratesOnQuery=false`、空标题/空句柄入口拦），唯一差异 `Match.Icon`——Rust 带 app 层写回的 PNG 路径、Go 带 `ProcessPath` 由渲染层经 `platform.AppIcon→Shell.FileIcon` 懒取（O4 设计，用户可见结果同为「窗口图标」）；`apps` 采 **mygo `Shell.InstalledApps`（AppsFolder 单一来源）+ `platform.LaunchApp(InstalledApp{Path,ID})`**，故 `AppEntry` 为 3 字段（Name/Path/ID）而非 Rust 的 7 字段（.lnk 解析的 args/working_dir/lnk_path/icon）——属「迁移到 mygo 即应复用框架枚举/启动后端」的既定架构分歧，去重键 lowercased(name,path) 与生产逐字一致；Rust 的空 name/path 构造期过滤在 Go 未加，经核实 mygo 枚举不产空名/空路径条目、依「信任框架保证、不为不可能场景加校验」纪律**有意不加**。至此 **9 provider + apps + picker 全覆盖审计完毕**。**另核一处易误判为 Go 缺口的特性**：Ctrl+Shift+Enter 管理员启动——生产 `dispatcher.rs:474-476` 亦 `if elevated { warn 尚未实现，降级为普通启动 }`，即**生产本身未实现 elevation**；Go 仅在 `Action` 上保留 `Elevated bool` 字段对齐 Rust 结构、启动走普通路径，与生产可观测行为一致（非缺口）。V 跑无须追这项。
- **重新部署（同轮，行为变更）**：HEAD `71e0d45` 重跑 §2.1 交付交叉构建，PE32+ GUI 子系统、md5 `c1c747c6`，旧部署版 `mv`→`voke-go.exe.runbak20` 保留可回退后覆盖。**真机启动自检**：新 exe 落地即见「工作目录正常」→ 内存快照 → 「就绪（热键 Ctrl+Alt+Space）」三行、无 panic；临时 config 已清除故回落默认热键（顺带正向验证配置缺省路径）；跑完 `Stop-Process` 归零，未留驻留进程、未碰生产 `voke.exe` 与 `%APPDATA%\voke\`。**现部署版＝`c1c747c6`，行为 == 当前 HEAD**。
- **§6 V6/V7 部分真机证据（同轮，只读、免合成输入）**：经 WSL interop 二启 `voke-go.exe`（无合成按键、无注册表写）。日志实证：① 首实例「就绪（热键 Ctrl+Alt+Space）」——**V6 热键注册**通路真机跑通（仍缺真人按键验 toggle 手感）；② 第二实例退出、首实例随即打出「内存快照 [面板首次唤起]」+「设备诊断：Accelerated=true（硬件 GPU）」、Objects 2857→2998（面板窗体已建）——**V7 单实例** `OnSecondInstance→showPanel` 与 mygo 面板首绘真机跑通（仍缺真人点托盘菜单验交互全貌）；③ 未现应用枚举计数行——apps 枚举**首次查询（打字）才懒触发**、面板 show 本身不枚举，故 **V3 枚举仍需真人打字**观测。跑完已 `Stop-Process` 归零。
- **§6 awake + clipboard 链自检真机通过（同轮，`VOKe_GO_SELFTEST=1`，现部署版 `c1c747c6`）**：日志三行——「awake 自检 ok：已写状态（剩余≈1800s，keepScreen=false）」「clipboard 自检 ok：写入并读回 "voke-go-selftest-42"（第 1 次尝试）」「启动自检结束，失败项 = 0」。实证：① 平台 power 链 `SetThreadExecutionState`（awake provider 状态机）在当前构建的**真实硬件**跑通；② 我移植的 mygo `Clipboard.ReadText` 在真机读回成功（此前仅 Linux 单测覆盖）。selftest 自身已 `CancelAwake`、跑完 `Stop-Process` 归零，未留驻留进程与残留亮屏态。
- **重新部署（同轮，行为变更·随机区间溢出修正）**：`2eb3244`（`random.validate` 跨度 high-low 溢出改判非法）重跑 §2.1 交叉构建，PE32+ GUI、md5 `a0966bdf`；旧部署版 `c1c747c6` `mv`→`runbak21` 保留可回退。**现部署版＝`a0966bdf`，行为 == 当前 HEAD**（其后仅文档提交）。新 exe 落地即见「就绪（热键 Ctrl+Alt+Space）」无 panic，跑完 `Stop-Process` 归零、未碰生产与 `%APPDATA%\voke\`。
- **V-pass 反馈驱动修正 + 再部署（2026-10-18）**：用户真机跑 V-pass 期间，agent 只读 `voke-go.log` 取证，实证 **V3 枚举 141 条 + >60s 防抖重扫、终端 WT App Activation（`profile={574e…} pid=10504`）、面板 show/hide + 内存/GC 有界（Objects 117k→GC 后 8k，无泄漏）** 全部工作正常。同时发现一处**日志级别 parity 缺陷**：`platform/workspaces.go` 把「Zed 库缺 workspaces 表」的良性跳过用 `log.Printf` 每次唤起面板都刷一行，而生产 `voke-sys` 记为 `log::debug!`（正常级别过滤掉）——已改「每路径只记一次」(`f1f40f8`)。交叉构建 md5 `330146f5`，旧版 `a0966bdf`→`runbak22`，**改名部署不打断用户正在跑的实例**（运行中进程仍用旧 inode、下次启动才吃到修复）。**现部署版＝`330146f5`，行为 == HEAD**。
- **再部署（2026-10-18，行为变更·开机自启两处对齐 Rust）**：`841925d` 修①「关自启把 `RegDeleteKeyValueW` 任何非 0（含安全软件拦 Run 写入的 ERROR_ACCESS_DENIED）吞成成功」②「设置保存先落 config 再试注册表、失败仍谎报已开」——改采 Rust `set_autostart` 次序（注册表成功才落 config，失败配置与界面回弹）。属行为变更，重跑 §2.1 交叉构建 PE32+ GUI、md5 `98ae5b8e`；旧部署版 `330146f5` `mv`→`runbak23` 保留可回退后覆盖，**改名部署不打断正在跑的实例**（下次启动才吃到）。**现部署版＝`98ae5b8e`，行为 == HEAD `841925d`**。
- **V-pass 反馈批量修复 + 再部署（2026-10-18，用户真机跑了 V-pass 回报 4 项）**：交叉构建 md5 `702e0b7e`，旧版 `98ae5b8e` `mv`→`runbak24`。**须重启才吃到**（旧实例仍跑修复前版）。四项（各自独立提交、六项门禁全绿）：
  ① **托盘退出无反应**（fix `a830350`）：根因 mygo `prepareQuit` 逐个 `w.close()`，面板/设置窗 `OnClose` 无条件 `PreventDefault`（关=隐藏）使 `close()` 返回 false → `prepareQuit` 提前中止、永不 `PostQuitMessage`。加 `appState.quitting`（`OnBeforeQuit` 于关窗循环前置真），两处 `OnClose` 见它放行真关闭。Ctrl+Q 同修。
  ② **Backspace 逻辑**（fix `dfbd1b1`）：删掉「非空→空」状态猜测 hack（会让空查询进的插件按退格退不动、且删空即弹出）；改为非空 Backspace 交编辑引擎正常删字符、空 Backspace 经输入框 `OnTextKey`（单行默认 `leaveEmptyBackspace=false`⇒wants 恒真）触发 `engine.BackspaceBack` 退一级、可逐级上溯、到顶停、绝不隐藏。
  ③ **插件图标**（feat `df8e0cf`）：picker 每行 / provider 结果行 / 全局合并列表的无 PNG 行都显示 Lucide 矢量角标（逐字取自生产 gpui-kit-assets 的 Lucide SVG，键对齐 Rust `badge::plugin_icon`：awake=coffee…fallback=blocks），替掉文本首字母色块；apps/windows 结果仍优先真 PNG。加 `pluginicons_test` 强制解析全部 SVG。
  ④ **设置页**（fix `11ab5c7`）：输入框 `TextInputBase`→`TextInput`（带边框/背景，一眼看出是输入框）；激活方式 "Direct/Indirect"→中文「直接/间接」（`activationLabel`/`activationCode` 做显示↔落码映射）；常规/每插件/关于各用 `Fieldset` 卡片分组。
- **作用域栈重构 + 再部署（2026-10-18，V-pass 反馈：删除/插件激活仍不对）**：`0cb6217` 把 `engine.State` 从扁平四元组改为**显式 LIFO 作用域栈**（每层带自己的过滤词）。前缀直达插件栈 `[Global,Plugin]`、经 `#` 列表选中 `[Global,Picker,Plugin]`、子命令再压一层 ⇒ 退格**严格按进入路径出栈**（前缀直达回 Global、picker 进入回列表），非空删字、空则弹栈、到顶不动、绝不隐藏。输入行改为**面包屑**（插件图标+名称+`›`，当前层高亮）呈现完整栈路径，无前缀插件（apps）也显示「插件列表 › 应用」，替掉单一前缀令牌。交叉构建 md5 `b9dfb0b2`，旧版 `702e0b7e`→`runbak25`，**须重启**才生效。
- **面包屑/占位/托盘微调 + 再部署（2026-10-18，用户再反馈）**：`870de7d`——① 面包屑改**单框紧凑**：最左一枚图标（当前插件），其后各层更小字号、`›` 分隔，当前层正文色其余弱化（对齐 Rust 观感，不再每层大图标大字号抢空间）；② 占位符在 Plugin/SubMode 走 `provider.Placeholder(subMode)`（base64 等不再回落「搜索应用」）；③ 托盘「显示面板」去掉 Accelerator。交叉构建 md5 `9e72705e`，旧版 `b9dfb0b2`→`runbak26`，**须重启**。
- **面包屑图标层级微调（`9fc3ead`，runbak27）**：每层各带图标，祖先层只显图标、当前层图标+文本（图标紧贴自身文本左侧），两形态：`[列表图标]›[插件图标]名` / `[插件图标]名`。
- **设置页即时生效 + 交互热键录制 + 启动器收窄/字号图标下调（`e0e8d4d`，runbak28）**：去保存/取消，开关/选择/数字即时落盘应用；热键改为点按录制（录制先注销旧全局键避免旧组合触发＝锁定问题，Esc/关窗取消并恢复；`platform.AccelFromKey` 转加速器串）；启动器宽 680→640、字号 14/16/12/11→13/15/11/10、图标 32→24、面板中心 48%→52%（下移）。
- **托盘倒计时按事件刷新 + 性能清理（`3dc6993`，runbak29）**：面板 show / 真实 hide 时 `refreshTrayMenu` 重建（修「剩 47 分仍显 1 小时」，**不加定时器**，菜单打开期间不变为残差）；移除 30s 内存打点 ticker 与每次隐藏的 `ReadMemStats`（STW），保留启动/首帧/索引完成的单次快照；`IconSize` 随字号降 24。**须重启**。
- **快捷键区文案/大小写统一（runbak30）**：删掉「唤 / 隐」标签，功能改由说明「全局唤起 / 隐藏面板」表达；录制出的组合统一为规范大小写（字母大写、命名键 Title-case，如 `Ctrl+Alt+Space`），与默认值一致不再混杂。
- **热键录制加超时/失焦取消（runbak31）**：录制态 10s 短超时（焦点回来 / 按无效键则续期，令牌式 TTL），窗口失焦或关窗即取消并恢复旧键；定时器经 `Window.Update` 回主线程执行取消，避免跨线程读写竞态。防「开了录制去干别的 / 组合被别的应用截走」一直卡在等待态。
- **定时器 / 唤醒循环回收加固（runbak32）**：录制超时定时器在续期、取消、成功时一律 `Stop` 并置 nil（不堆积、不在录制结束后再向主线程投递取消）；awake 的重申/到期/取消回调按「当前会话所有权（stop 通道相等）」判定，被新 `SetAwake` 替换的旧循环不再清位/置位，消除与新一轮置位的竞态。
- **英文程序名检索恢复（runbak33）**：V-pass 反馈 `service` / `regedit` 搜不到（以前可以）。定因：mygo 的 `InstalledApps` 只按 `SIGDN_NORMALDISPLAY` 取**本地化显示名**（「服务」「注册表编辑器」），而 Rust 另枚举开始菜单 `.lnk`（英文文件名词干）故能英文命中。Go 侧在 `search.FilterApps` 里以**条目 ID 的英文文件名词干作补充键**（`…\services.msc`→services、`…\regedit.exe`→regedit），取分高者；中文/拼音路径不变（`fw` 仍中）。**残留**：`Microsoft.AutoGenerated.*` 条目（如任务管理器，ID 无英文名）→ `task` 仍搜不到；彻底对齐需在 Go 补开始菜单 `.lnk` 枚举（COM IShellLink），另行评估。
- **开始菜单 `.lnk` 枚举（runbak34，对齐 Rust 双来源）**：`platform.ShortcutApps()` 用 COM（`IShellLinkW`/`IPersistFile`，vtable 序号按 windows-0.57 绑定核实：GetPath=3/GetDescription=6/Load=5）递归枚举 `%ProgramData%`、`%APPDATA%` 的 Start Menu Programs，取显示名（可信 description 否则文件名）与目标。`appsmodel.MergeShortcutApps`：**按目标/ID 文件名词干识别同一程序**（`services.lnk` ↔ 服务(`…\services.msc`) → 只留 AppsFolder 条，中英文皆可搜）、rule-2 同名非 MSIX 跳过；与 Rust 的有意差异：**不丢** `Microsoft.AutoGenerated.*`（任务管理器/任务计划程序）——Rust 用英文 `.lnk` 代表会丢失本地化名（中文/拼音搜不到），这里保留本地化条 + 英文 `.lnk`（名字不同，过滤态不同屏；空查询全量列表可能各出现一次，属「宁全勿丢」取舍）。启动走 `.lnk` 本体 ShellExecute、图标走 FileIcon(.lnk)。真机探针实测：**101 条 .lnk；service→2、task→2**（regedit 由 AppsFolder ID 别名覆盖）。
- **微信消失修复（runbak36）**：V 反馈「微信过滤不出来」。定因：微信的 `.lnk`（名「微信」、目标 `…\Weixin.exe`）与 AppsFolder 条（名「微信」、ID 也是 `…\Weixin.exe`）并存时，旧合并逻辑 **rule-2 删 AppsFolder、词干关联又删 `.lnk`** → 两边互删。改为：**rule-2 只针对实际保留的 `.lnk`**；词干关联命中则由 AppsFolder 条代表（含 ID，可中文/拼音搜）。加回归测试（微信、微信开发者工具）。
- **品牌/图标/默认热键对齐生产（2026-10-19）**：打包名 **`voke.exe`**（部署在 `%USERPROFILE%\voke-go-poc\`，旧 `voke-go.exe` 保留可回退）；窗口标题与托盘提示显示 **Voke**（设置窗「Voke 设置」）；托盘图标改用生产资产 `assets/voke-tray.png`（`//go:embed`，128×128）；exe 图标用 `assets/voke.ico`（`windres` → `rsrc_windows_amd64.syso`，真机核验 **7 帧全部逐字节嵌入**）；默认热键改为 **Alt+Space**。（注：此条中的隔离约定随后已按用户要求解除，见「全面对齐生产标识」。）**注意**当时新旧 exe 共用单实例 key：先退出旧的 `voke-go.exe` 再启动新的 `voke.exe`。
- **频率历史导入（体感对齐 Rust）**：V 反馈「wx/dd 等常用应用不如 Rust 版靠前」。定因：排序公式与 Rust **逐式一致**（`record_use` + `1+ln(n+1)*0.5` rank），但 Go 的 `frequency.json` 是新表（仅 9 键、次数 1–2），而生产 `%APPDATA%\voke\frequency.toml` 有长期历史（终端 25、Alacritty 23、Task Manager 15、钉钉 5…）。`loadFrequency` 现于启动时**只读**解析生产 `frequency.toml`（极简 TOML 子集：`"key" = n`，支持注释/转义），逐键 **max 合并**（只增不减、幂等）后写回**本侧** `%APPDATA%\voke-go\frequency.json`——绝不写生产目录。加解析/合并单测。
- **频率数据改 TOML（与生产同格式）**：按用户建议引入 `github.com/BurntSushi/toml`，本侧频率落盘从 `frequency.json` 改为 **`frequency.toml`**（与 Rust `frequency.toml` 同为平铺 `"<key>" = <count>`）——互相可读、跨版本不迁移格式。首次启动自动把旧 `frequency.json` 迁移为 TOML（旧文件改名 `.bak` 留底）；生产历史导入改用同一 TOML 库解析。加 TOML 往返/生产片段解析/坏数据回落单测。
- **配置改 TOML（与生产同格式）**：`config.json` → **`config.toml`**（顶层 `hotkey/autostart/theme` + `[providers.<id>]` 的 `enabled/is_global/weight/prefix/activation`；空前缀与生产一致**省略该键**），BurntSushi/toml 编解码（带 json 旧标签的 `FromJSON` 仅保留给迁移读取）。首启自动把旧 `config.json` 迁移为 TOML（改名 `.bak` 留底）；`VOKE_CONFIG` 覆盖路径现指向 TOML。
- **Go CI/CD（已并入主线，Rust CI/发版退役）**：`.github/workflows/go-ci.yml` 仅在 `poc/voke-go/**` 变更时跑——`go mod tidy` 自检、`gofmt`、`go vet`、`go test -race -shuffle`、Windows 目标 vet + 交付交叉构建（GUI/syso 图标），上传 `voke.exe` 工件。`.github/workflows/release.yml` 已由 **Go 版统一发版**覆盖（替代原 Rust release.yml），配合 `.github/actions/install-inno7`：推 `vX.Y.Z` tag 或手动 `workflow_dispatch`，windows runner 构建后**复用生产 `packaging/voke.iss`**（`-dVokeExe` 指向 Go 产物、`-dVokeVersion` 取 tag）产出同名形态安装包 + portable zip；tag 时 ubuntu job 用 git-cliff 生成 `CHANGELOG.md` / release 正文、回推默认分支并建 Release（与原 Rust 发版行为一致）；手动触发只出 artifacts、不建 Release。Rust `ci.yml` 已删除，过渡性的 `go-v*` tag 方案取消。2026-10-19 实测：CI 全绿；`workflow_dispatch` 版本 `0.1.4` dry-run 全链路成功（Go 构建 → ISCC 安装包 → portable zip → artifacts），本地同命令 ISCC 出包亦通过。
- **设置窗内存 / 版本注入 / 托盘双击（2026-10-19）**：① 设置窗从「关闭=隐藏、复用窗口」改为**程序化销毁**——OnClose 仍 `PreventDefault`，经 `Window.Update` 投递到主循环下一轮再 `Destroy`（避免在渲染回调栈内释放原生窗），`st.settings` 置 nil、下次打开重建；真机隔离副本实测：设置窗打开后 Private 103.3MB → 关闭+4s 87.8MB → +8s 80.3MB（释放约 23MB），Go 堆不变（5.4MB），降的全是窗口渲染资源。② `aboutVersion` 优先取发布流水线 `-ldflags -X .../internal/app.buildVersion=<tag 版本>`（与安装器 `-dVokeVersion` 同源），本地未注入退回 `debug.ReadBuildInfo` 并显示「开发版」而不是 `(devel)`；启动日志也带版本。**exe 文件属性的 VERSIONINFO 仍未注入**（已知差距，与设置页显示是两条通道）。③ 托盘左键双击唤出面板：mygo 原生只上报单击（wmLButtonUp），双击表现为两次 Clicked，用 500ms 窗口识别（第二次点击 → `showPanel`，不是 toggle），单测钉住边界。
- **全面对齐生产标识（用户拍板：本版将替换原版）**：数据目录 `%APPDATA%\voke`、**日志 `voke.log`**、**自启 `Run` 值名 `voke`**、单实例 key `voke`、启动自检 env `VOKE_SELFTEST`（与生产同名）；Go 专属开发钩子改名 `VOKE_CONFIG` / `VOKE_FREQ`；日志文案前缀统一为 `voke`。`config.toml` / `frequency.toml` 与 Rust 同格式共用。**仍存的非一致点**：① 单实例机制——mygo 用 per-user socket、生产用命名互斥体+事件（不可改，两版仍可并行，共用文件会 last-writer-wins，建议只跑一个）；② 图标缓存——Go 进程内、生产落 `icons/` 盘；③ 安装路径/安装器未做（现部署在 `%USERPROFILE%\voke-go-poc`，替换原版时安装到生产路径）；④ 少数历史频率键因两版条目显示名不同（如 `services` vs 服务）不迁移。
- **日志轮转现状（问答）**：已有 **logcap 容量封顶**——跨启动追加，总行数达上限时从**头部删一批**最旧行（`max(1, N/10)`，默认 9999；构建期 `-X …logMaxLinesRaw=20000` 或生产 `VOKE_LOG_MAX_LINES` 可注入），并落一行中文滚动标记；**没有**按大小/日期切割成多个历史文件（与 Rust `logcap.rs` 行为一致，非缺失）。
- **现状收口（2026-10-17 HEAD `c597dbd`，权威小结，取代上文 1606 行「真实多 provider / 设置窗 / 配置 UI 属 §10.2 第5条未决、仅剩 step8b、接线只用 apps+picker」的过时表述）**：§10.2 第5条拍板的「完整全功能迁移」其**代码部分已全部落地并经当前源码核实**——① 全部 10 provider 经 `builtinProviders()` 注册进实时表（`providers_test` 有「带前缀却没注册→落未接入占位」护栏）；② step8b 生命周期装配完整（`main.go` 注册 `RequestSingleInstance`/`OnSecondInstance`/`OnWillQuit`/`WhenReady`/`App.Run`）；③ 托盘菜单（awake 状态 / 显示面板 toggle / 设置 / 退出）；④ 热键注册 + 设置窗改键（`UnregisterHotkey` 旧→`RegisterHotkey` 新、失败回滚）；⑤ 设置窗 + 配置 UI 编辑全 5 字段（enabled/is_global/weight/prefix/activation）+ 配置 schema 读写；⑥ frequency 持久化（`loadFrequency`/`saveFrequency`）+ apps 全局排序吃加成；⑦ logcap 接入 `InitLog`；⑧ `selftest.go`、`FixOwnCwd`、托盘 toggle 均在。**唯一仍未落代码的两项**：Task#6 撤 30s 内存打点脚手架（文档明列「排在 §6.1 采样之后」，现仍被读，非缺口）、以及 §10.2 的产品决策项（#1 bus-factor、#3 枚举失败重试——Rust 侧本无重试，属新增能力非迁移 parity）。**§6 V1–V7 真机验收是 mygo GUI 运行期 headless 到不了的外部输入，不是代码功能缺口**。387 单测 / 六项门禁 / `-race` / Windows 交叉 build 全绿。
> **错误路径 parity 审计（2026-10-18，接 `841925d` autostart 的教训：早前「正确」多按 happy-path 判，本轮专查「外部写失败」路径）**：① **开机自启**两处吞错/谎报已修（见上）。② **config.json / frequency.json 落盘**核实**等价、无缺陷**——Rust `commit`/`save_frequency` 用 `std::fs::write`（config.rs:150/197，非原子 truncate-write）、Go 用 `os.WriteFile`（providers.go:400/383，同非原子）⇒ **无原子性分歧**；二者「序列化失败/写盘失败 → 记日志、**不把新值下发引擎**（内存态不变）」一致（Go saveConfig 返 err→saveSettings 提前 return、不 reloadConfigs；Rust commit 写失败 `return` 于 apply_config 之前）。③ **全局热键**核实**等价**：`platform.RegisterHotkey`（hotkey.go:31）**原样带回 error、不吞**（与自启旧吞错正相反），调用方 saveSettings（settings.go:282-287）撤旧→注新→失败回滚旧键、`UnregisterHotkey` 幂等无返回——错误路径齐备；Rust 侧 388 行专用线程 hotkey.rs 的删除是 O2 记录的**架构简化**（mygo WM_HOTKEY→pressed() 主线程直调），非行为缺口。两处**已接受的次要不一致**（非缺陷、不追）：Rust `load_frequency` 首启 NotFound 记 `log::info`、Go 静默（少一行首启噪声，合理）；Rust 取不到数据目录路径时仍把配置下发引擎（仅本会话生效）、Go 判保存失败下次重试——后者只在 APPDATA 不可得的极边角触发（GUI 运行期近乎不可能），按「不为不可能场景加分支」纪律不补。
> **命令面 / dispatcher 完整性审计（2026-10-18，专查「Rust 有、Go 像没有」的两个候选缺口，结论：均非功能缺口）**：① **`Action::OpenUrl`**——Go 有枚举 `ActionOpenUrl`（provider.go:90）但 `execPlatformAction` 无对应臂（落 `default→未接入`）。核实 Rust 全仓仅 `action.rs` 定义 + serde/谓词（96/234/345），**无任何 provider 构造 OpenUrl**——Go 亦无。⇒ Rust 侧这条 dispatcher 臂是**前瞻声明、当前休眠**，两侧都无生产者，Go 无臂的可观测行为与 Rust 完全一致（永远走不到）；补一个跨平台 open-url 原语给不可达路径 = 投机代码，按纪律不做。② **`ShellCommand::SystemThemeChanged`**——Rust 显式监听 OS 主题广播再重贴（gpui 不自动跟随）；Go 走 `mygo.Theme.SetSource(ThemeSystem)`（theme.go:18），**mygo 内建 `appHandler.ThemeChanged()→contentThemeChanged()`（app.go:595）已自动跟随系统主题** ⇒ Go「跟随系统」live-follow 由框架免费提供，与 Rust 同观测行为、异实现路径（同 O2 热键：mygo 主线程直调 vs Rust 专用线程）。已在 providers.go/BACKLOG 标为「真机确认项」，非未实现。**Rust ShellCommand 全集**（TogglePanel/ShowPanel/OpenAwake/OpenSettings/SystemThemeChanged/Quit）逐个核对，Go 均有落地（togglePanel / OnSecondInstance→showPanel / openAwake / openSettings / mygo 自动 / App.Quit）——命令面无缺口。
> **§7.2.2 目录提案 → 实际文件映射（2026-10-18 据实核对 HEAD `e047be1`；提案是结构骨架非逐字清单，实现有合理漂移与增强——防后继照提案文件名误判「缺模块」）**：`platform/apps.go`→`shell.go`(+`launch.go`)；`win32_windows.go`→`windows_enum_windows.go`/`window_windows.go`；**`screen.go` 无独立文件**——主屏 `mygo.Screen.PrimaryDisplay()`（app/panel.go:317，签名源码核实 :258）、缩放 `Screen.Displays()`（platform/shell.go:138），**纯几何算术上提 `logic/geometry`（超提案，Linux 可测）**；**`log_windows.go`→`platform.InitLog()`**（main.go:29）接 `logic/logcap`；`ui/theme.go`→`ui/theme/` 子包；`ui/row.go`/`keys.go` 并入 `ui/view.go`/`apps.go`；**`providers/apps/` 无独立包**（apps 经 `logic/{appsmodel,search,panelstate}`+`app/{apps,global}.go`：展示在 global 折进 Match、启动留 panelstate 保 AppsFolder/UWP 语义）；`app/app.go`→`main.go`、`dispatch.go`→`view.go`+`providers/dispatch.go`、`state.go`→`panel.go` 的 appState。**能力面全覆盖、零缺失模块**；六项门禁于本 HEAD 复核全绿（27 包测试 / Windows 交叉构建 PE32+ GUI / `-race` 无 DATA RACE）。
- §6 部署已重核（2026-10-16 续会话，HEAD `cca78e6`）：因 **step8a 重构的正是 V6 热键 / V7 托盘+单实例 的构造路径、7b 搬的正是 V5 面板渲染路径**，而部署版旧 exe 早于这两步——**故为让 V5/V6/V7 打在现 HEAD 代码上而重新构建部署**（这属「搬迁动了被测子系统」的例外，非纯搬迁）。且补上一处门禁缺口：§2.1 六项只跑 Linux 原生 `go build ./...` + `GOOS=windows go vet`，**从没跑过真·Windows 交叉 build**；本轮实跑 §2.1 交付构建命令，产出 PE32+ **GUI 子系统**（`file` 核实）、md5 `388e26a0`，已 `cp` 到 `%USERPROFILE%\voke-go-poc\voke-go.exe`（voke-go **未在运行**→文件未锁→直接覆盖，无需 mv 改名）。**A/B 前提**：生产 `voke.exe` 正在运行（Id 22956，未触碰），voke-go 未运行——**用户须先启动部署版 `voke-go.exe` 再逐项跑 V1–V7**。V2–V7 结果仍待用户真机回填 §6.8。
- HEAD 重部署（本轮）：因自 `ef04676` 后 HEAD 又并入 trayIcon 抽取（`459792e`，输出 PNG 逐字节不变）与两处纯测试/纯文档提交，重新在 **HEAD `bc9714a`** 跑 §2.1 交付交叉构建，产出 PE32+ **GUI** 子系统、md5 `1ef800e9`，已 `cp` 到 `%USERPROFILE%\voke-go-poc\voke-go.exe`（voke-go 未运行→未锁→直接覆盖）。**与 `388e26a0` 行为逐字节等价**（prod 代码仅 trayIcon 且输出未变；md5 差异纯来自 Go 构建内嵌路径）——此举只为让「部署版 == HEAD 构建」字面成立，消除用户 V 跑时的任何「测的是不是最新码」疑虑。**隔离核实**：生产 `voke.exe` 仍在 `C:\Users\loiac\AppData\Local\Programs\Voke\voke.exe`（Id 22956）运行、未触碰；本轮只写 `voke-go-poc\`，未碰 `%APPDATA%\voke\`（红线②③成立）。
- §6.0 隔离自检部分实测通过（只读）：voke-go 仅写 `%APPDATA%\voke-go\voke-go.log`，未触碰 `%APPDATA%\voke\`——**红线②成立**；内存快照 6 次 30s 打点 PrivateCommit 稳定 ~103MB、Objects 有界、GC 平缓，窗口内无泄漏旁证（与 §5.1 一致，非 §5 数据源）。
- 地基 + **接线已落地并提交**（每块独立跑过 §2.1 六项门禁全绿、测试只增不减，模块测试 54→**142**）：`logic/engine`（§7.3.3 前缀全等扫描 + 模式状态机）、`logic/config`（§7.3.4 归一 C1–C7 + `BuildTable` + §7.3.6a alias bonus，加分在过滤前）、`logic/panelstate`（步骤5 决策状态机 + Action 意图外化）、`logic/{iconcache,appsmodel,search,geometry}`（步骤1/3/4）、`logic/logcap`（§7.1 O9，cargo test 为 oracle）。**接线（task5）**：`providers.go` 用 config 产 engine.Table，view/panel 接 `engine.Scan/EscBack/BackspaceBack/PrefixToken`，`#` 唤起插件列表——**Global 零回归有硬证据**（`TestGlobalPathUnchangedWithoutPrefix` + 真机帧 + 变异 M8 摘 Scan 后 Global 用例仍全绿）。接线那次构建曾部署 `%USERPROFILE%\voke-go-poc\voke-go.exe`（当时 md5 `6da4e095`）；**现部署版＝HEAD `bc9714a` 交叉构建 md5 `1ef800e9`**（见「HEAD 重部署」条）。**剩余**：§7.2 步骤 **1/3/4/5/6a/6b/7a/7b 已全部落地并提交**（每步独立六项门禁全绿、`-race`+`-shuffle`+`-count=2` 全绿、测试只增不减、行为等价；7a 抽 ui/theme 常量、7b 把视图/渲染搬 `internal/ui` 且 Tester/像素用例随迁自证）。**仅剩 step8b**：step8a 已把 `singleinstance/hotkey/tray` 的 mygo 构造原语收进 platform 薄封装（`212de6d`）、trayIcon 已抽入 `internal/ui`（`459792e`）；剩下的是与 App 生命周期回调缠绕的装配——`ready/WhenReady/OnSecondInstance/OnWillQuit/Run` 注册、`setupTray` 菜单 Click 闭包 + `App.Quit` 改走 dispatch 意图、`bindApps/Window.Update` 回主线程接缝、面板窗口 show/hide、`view()/execute/apply/execLaunch` 执行器（精确清单与施工序列见 BACKLOG `27f66ca`）。**全工程唯一「行为保持搬迁但回归跑不进六项门禁」的区**（无主循环即 `onMainValue` 永久阻塞、无 headless 覆盖），安全网是待跑的 V5/V6/V7，**故等 V 门槛过后再做**。真实多 provider / 设置窗 / 配置 UI 属 §10.2 第5条未决。部署版 exe：7a/6b/7b 属纯搬迁不改可观测行为，**唯 step8a 重构了 V6/V7 的构造子系统**，已按上方「§6 部署已重核」与「HEAD 重部署」重新构建部署（**现部署版 md5 `1ef800e9`**，与 `388e26a0` 行为逐字节等价），不存在「部署版滞后 HEAD」的隐患。**§7.1 O9 已彻底完成**：logcap 不仅实现、且已接入 `platform.InitLog`（`voke-go.log` 真正封顶，默认 9999、`-X` 注 string 变量覆盖；封顶失败降级不阻断）。真实多 provider 属 §10.2 第5条未决，接线只用 apps+picker。
- **覆盖率审计（`go test -cover ./...`，非新结论、是安全网现状取证）**：纯逻辑地基已**饱和**——`appsmodel`/`geometry`/`iconcache`/`panelstate`/`search` **100%**，`match` 94%、`config` 92%、`logcap` 84.8%、`engine` 83.9%。逐条核实**残余缺口全属良性类别、无一处是可补的未测正确性洞**：① `engine` 未覆盖仅两个 **diagnostic-only `String()`**（源码注明「不参与判定」）；② `config` 的 `Enabled`/`IDs` 显示 0% 是**归属假象**——生产路径**内联**判 `p.Enabled`（`BuildTable` table.go:24 把禁用插件挡在表外、`rankPlugins` providers.go:98 把它挡在 picker 外，均已核实正确，无「禁用项漏进列表」的潜在 bug），这俩方法只被 main 侧集成测试调用并断言（`providers_test`/`engine_wiring_test`/`fixtures_test`）；`String()`/`formatFloat`/`Rank`/`AliasBonus` 的零头是诊断与近满分支；③ `logcap` 的 `pruneHead`/`printlnFull`/`Write` 零头是文件错误等**难触发的边角**，其语义另有 `cargo test` 作 oracle。`ui` 26.6% / `main` 47% / `platform` 74.5% 的低值是**结构性天花板**：三者绑 mygo 事件循环（无主循环即 `onMainValue` 阻塞），headless 到不了——**能抬高它们覆盖的只有 §6 真机 V 跑，不是更多单测**。结论：地基侧再补测属空转（违红线⑧精神），剩余安全网增益必须等 V 数据，佐证当前确为「卡在外部输入」。
- step8b 去风险切片（非新结论、仅进度）：`panel.go` 的 `trayIcon()`（纯 stdlib 画 PNG、零 mygo、无生命周期耦合）已**逐字**抽入 `internal/ui/icons.go` 导出为 `TrayIcon()`，setupTray 经 `appui` 别名调用（commit `459792e`）。**改前/改后输出 PNG 字节 md5 一致**（`a682ddd5…`，260B）⇒ 行为逐字节不变，**不需重部署**、上方 V 测试仍有效。补 `internal/ui/icons_test.go`：只断言解码后可观测不变量（32×32 / 镜环+手柄固定像素墨色 79·124·255·255 / 四角与镜心全透明 / alpha 仅 0·255 两档 / 墨迹面积 215），不抄绘制循环；对 ink·内外半径·画布·手柄长短 7 项变异全部变红（红线⑧）。效果：`package main` 少一处非装配职责，step8b 待搬面积再缩小一点。六项门禁 + Windows 交叉 build + `-race -count=2 -shuffle=on` 全绿。
- 到期清理（待办）：main.go 的 30s 周期内存打点是 §5.1 定因的临时脚手架，文档要求定因后撤除；因 §6.1 采样仍需读它，**排在 §6 验证之后**随重构步骤6（mem→platform）一并撤。

**待用户真机确认 3 条**：

1. 面板新位置的观感（面板中心 48% 是否合意；若仍偏高，改成 50 即对齐 Rust 正中）。
2. 唤起时是否还有可感知的加载延迟——**若上屏阶跃明显下降，建议补一轮
   内存复测**（懒加载把应用枚举与首屏图标解码都推迟到首次打字，正是那笔
   内存的主要来源之一）。
3. 单击执行的手感（尤其：图标行右侧空白处是否也可点——已用 `Grow(1)`
   让按钮铺满整行，但需真机确认视觉上无异常）。
4. **列表行视觉**（§7.5.1）：① 静止态确认**没有**边框、圆角矩形，框已彻底
   消失；② 指针划过某行是否只亮**一个**圆角药丸；③ 相邻两行药丸之间确有
   留白、没有首尾相连；④ 行左右各留 6 DIP、不贴边。已用 Tester 扫像素验证
   （`TestRowHasNoBorderPixels` / `TestRowPillGapBetweenRows` 等），但**观感
   仍须真机确认**——像素对不等于好看。
5. 若第 4 条里「行间留白」实测偏空/偏挤，只调 `view.go` 的 `rowGap`
   （当前 4）与 `rowInsetX`（当前 6）两个常量即可，**不要**回去改 `rowSlotH`
   ——它与生产 `ROW_HEIGHT=48` 对齐，且 8 行可见性由
   `TestPanelStillShowsEightRows` 守着。

### 10.1.1 第三轮（行视觉 + 定位锚点）验证记录

**五项验证**（`cd poc/voke-go`，全过）：

| # | 命令 | 结果 |
|---|---|---|
| 1 | `go build ./...` | ✅ |
| 2 | `go test -count=1 ./...` | ✅（含 `internal/match`） |
| 3 | `go vet ./...` | ✅ 零警告 |
| 4 | `gofmt -l .` | ✅ 空 |
| 5 | `GOOS=windows GOARCH=amd64 go vet ./...` | ✅ |

**新增单测**（`row_visual_test.go`，全部**渲染真实帧并扫像素**，
且**直接调生产代码** `st.row()` / `panelTopLeft()`）：

- `TestRowHasNoBorderPixels`——静止 / hover / hover 紧邻选中三态下，
  扫三列断言**零描边像素**（本轮最关键的一条）
- `TestRowHoverPillAppears`——真实 `tt.Move` 触发 hover 高亮亮起/移开消失
- `TestRowPillGapBetweenRows`——相邻高亮块之间的空隙 ≥ 2 DIP
- `TestRowPillHeight`——药丸明显矮于行槽（上下留白 ≥ 2 DIP）
- `TestRowPillInsetHorizontally`——药丸左右不贴边（各 ≥ 4 DIP）
- `TestSelectedRowNoAccentBleed`——选中行不露 Accent 实底（遮罩有效性）
- `TestPanelStillShowsEightRows`——494 高度下**第 8 行仍完整可见**
- `TestPanelCenterAnchorMatchesLaunchers`——1080p / 1440p 面板中心都落 48%

**变异测试 10 条，全部被捕获**（这是本轮的重点，见下）：

| # | 故意改坏什么 | 捕获它的测试 |
|---|---|---|
| 1 | `row()` 改回 `ui.Button` | `TestRowHasNoBorderPixels` |
| 2 | 去掉 hover 高亮 | `TestRowHoverPillAppears` + `TestRowPillGapBetweenRows` |
| 3 | `rowGap` 4→0 | `TestRowPillGapBetweenRows` + `TestRowPillHeight` |
| 4 | `rowInsetX` 6→0 | `TestRowPillInsetHorizontally` |
| 5 | 去掉外层遮罩 | 4 条同时红 |
| 6 | `rowSlotH` 48→56 | `TestPanelStillShowsEightRows` |
| 7 | 定位系数 48→42 | `TestPanelCenterAnchorMatchesLaunchers` |
| 8 | 算术改锚输入框中心（旧根因） | 同上 |
| 9 | 算术改回「顶 × 剩余空间」（旧口径） | 同上 |
| 10 | 水平居中失效 | 同上 |

**本轮踩到的两个「测试是假的」陷阱（比 bug 本身更值得记档）**：

1. **测试里抄了一份生产代码**。第一版行视觉测试照抄了 `row()` 的结构，
   于是「把生产 `row()` 改回 `ui.Button`」这个变异**照样全绿**——测的是
   测试自己那份副本。定位算术同理（测试里自己复刻了 `panelTopFor`）。
   **修法**：一律调生产函数。为此把定位算术抽成了 `panelTopLeft()`
   （`panel.go`），`showPanel` 与单测共用同一份实现。
2. **用被测常量当阈值 = 同义反复**。`gap < rowGap`、`got != rowSlotH-rowGap`、
   `left < rowInsetX` 这类断言，在把 `rowGap`/`rowInsetX` 变异成 0 时
   **阈值跟着一起变**，测试照样绿（实测漏过 2 条）。**修法**：阈值一律写
   **绝对值**（间隙 ≥2 DIP、左右缩进 ≥4 DIP），锁的是「必须有可见留白 /
   不能贴边」这条要求本身，而不是某个常数的算术关系。

> 这两条与 §7.8 的教训同源：**空转断言比没测试更糟**——它会让人误以为改动
> 被覆盖了。凡是新增断言，先问「改坏实现时它会不会红」。

### 10.2 待决策

| # | 待决策 | 位置 | 状态 |
|---|---|---|---|
| 1 | **bus factor = 1 是否可接受**（上游停维护的兜底策略） | §8.1 风险 8 | ⬜ **唯一未决**。材料不足，且是**产品决策不是技术决策**：接受「上游挂了自行 fork」／还是现在就保留 Rust 侧退路。不挡施工 |
| 2 | **唤起重扫 + 60s 防抖是否搬** | §7.7 | ✅ **已落地（本会话 `a9bbbd7`）**：`showPanel` 每次唤起调 `appsmodel.MaybeRefreshOnWake(now, 60s)`，距上次扫描 ≥60s 且空闲则后台重扫（`loaded`/`inFlight`/`lastLoadAt` 三态闸门，结果经 bindApps 异步换 Apps、UI 不阻塞）；`TestMaybeRefreshOnWake` 守三道人挡。装/卸应用下次唤起即见，无需重启进程 |
| 3 | **枚举失败后是否重试** | §7.7 | ✅ **主要已由决策 2 消解**：失败同样 `defer MarkLoaded` 清 inFlight、`lastLoadAt` 记在失败那一刻，故下次唤起距 ≥60s 时 `MaybeRefreshOnWake` 自动再枚举一轮——失败不再是「必须重启进程」。尚缺「改查询时给一次立即重试」与指数退避，属锦上添花（记 BACKLOG `[低-中成本，可择机]`） |
| 4 | **跟随指针屏**（在指针所在屏唤起） | §7.6 | ✅ **已决策不做**，已入 BACKLOG。这是**与生产 voke 也没有的能力**，且必须先解决副屏尺寸 / 定位 bug 才谈得上 |
| 5 | **重写范围推进到哪一步**：§4.2 把 PoC 界定为「只做 apps、无配置系统、无多 provider、无设置窗」，而 §7 全章（尤其 §7.2.2 目录、§7.3.4 配置 schema、O1 awake / O9 logcap / frequency）描述的是**全产品对等的重写**。两者对「现在要交付多少」口径不一致。 | §4.2 vs §7 | ✅ **已拍板（用户 2026-10-17）：做完整全功能迁移到 mygo**。按 §7.2.2 全树实现——step8b 装配 + 全部 providers（windows/awake/terminal/workspace/timestamp/base64/uuid/random/calculator）+ frequency + 配置系统 + 设置窗，**一律对照 Rust 版实现**；不合理/含糊处**记录决策点跳过、迁移完成后统一处理**。§6 真机 V 不 block 施工（用户已大体验过，仅报两处小问题）。 |
| 6 | **BACKLOG 里 `§11.x` 交叉引用已过时**：文档在收敛为「唯一权威施工依据」时把原 §11 并入 §7（`bf4ac21`），`docs/BACKLOG.md` 的 voke-go 节曾写 `§11.5/§11.8.6/§11.9/§11.12`。 | §7 ↔ `docs/BACKLOG.md` | ✅ **已清理**：BACKLOG.md 8 处 `§11.x` 已逐条核对替换为对应 §7 子号（§11→§7、§11.5→§7.5、§11.8.6→§7.3.6、§11.9→§7.6、§11.12→§7.7），并顺带去掉同句里同样失效的 `§10.1 第 14 条`、`§8.5` 两个坏指针 |
| 7 | **Esc 回退链口径冲突**：§7.3.3 规则 6 原文是 `SubMode→Plugin→Global`（未提 PluginPicker），但 engine 增量1 实现的是 `SubMode→Plugin→PluginPicker→Global` 全序链。`State` 无来源字段，区分不了「picker 选进来的插件」与「键入前缀直达的插件」，两者不能同时成立。 | §7.3.3 规则 6 ↔ engine.go `EscBack` | ✅ **已决（CmdPal 对齐，用户 2026-10-17，`0539fb0`）**：Esc **不再走回退链**——任意模式下 Esc 一律直接隐藏面板；整条逐级回退链（SubMode→Plugin→PluginPicker→Global）**交给 Backspace**（`BackspaceBack` 承接），`engine.EscBack` 已删除。冲突随语义重划消解（无「来源字段」的歧义不复存在，因为链只由删空触发一次一级）。 |
| 8 | **Indirect 触发用 `TrimLeftFunc` 而非 `TrimSpace`**：§7.3.3 规则 1 伪码写 `trim(query)`，与规则 2「`q == prefix + " "` 才命中」矛盾——`TrimSpace` 会削掉那个必需空格使 Indirect **永不可达**。engine 改用只裁前导空白。副作用：`= `（等号后带空格）不触发 calculator、回落全局（与决策⑥「全等不兜底」一致）。 | §7.3.3 规则 1/2 | ✅ 已按最小正确解实现（engine 增量1），文档伪码此处需订正口径；非阻塞 |
| 9 | **Backspace 退不出「前缀直达的插件」**：按规则 6 第三条「空输入+已激活插件→保持插件第一级」，键入前缀进来的插件只能靠 Esc 退出。若产品期望 Backspace 也能退到全局，需改 `BackspaceBack` 的 `case Plugin`。另 engine 增量1 多加了一个 `SelectPlugin`（超出简报，为守铁律②避免模式构造散到 app 层）。 | `engine.go` `BackspaceBack` | ✅ **已决（`0539fb0`）**：Backspace 现在从插件第一级也逐级回退（空+Plugin→PluginPicker→Global），不再「原地保持」。`SelectPlugin` 保留（守铁律②）。退出面板只由 Esc 负责。 |
| 10 | **接线（task5）上报的产品手感项**（均已在 `providers.go/view.go` 实现、有测试，仅口径可再定）：① apps 取**空前缀**→无前缀插件在插件态**无可见模式令牌**（要不要改成显示插件名？）；② picker 带过滤词时 Esc **一下直接回 Global**（engine 全序链结果，嫌跳改 `EscBack` 而非接线层）；③「退格删空」靠**状态差**认定，全选删除/粘贴清空也会被当作逐级回退一次（mygo 不单独暴露退格事件）；④ 只在 `#` 里逛不打字→本次会话**一次枚举都不做**（合 §7.7 决策④，真机可确认手感）。 | `providers.go` / `view.go` | ✅ **部分已决（`0539fb0`）**：②③ 随 CmdPal 对齐处理——Esc 改为直接隐藏（不再「一下回 Global」）；③ mygo 空输入无退格事件的限制已记入 `engine.go` 注释作**已知限制**（一次删空只退一级）。① apps 无前缀令牌、④ 只逛 `#` 不枚举 仍留手感/文案统一决策。Global 零回归不变。 |
| 11 | **view.go 里 5 个「按模式现取现算」纯函数的归属层**：`placeholder` / `hintLine` / `pluginPending` / `pendingHint` / `pickerHint` 只读 `engine.State`（纯数据、零 mygo、不碰窗口）。step8b 后它们已从 `package main` 随装配迁入 `internal/app`（`package app`，view.go），不再违反「main 只装配」——但按 §7.2.1 严格看仍属「app 层里的纯文案派生」，可选下沉。落到哪层是三选一：① `engine`（模式状态机，`PrefixToken` 已是它产出的展示串、有先例，但塞中文文案稀释其单一职责）；② `panelstate`（面板决策模型，可它被刻意设计成**不知道有模式**，塞进来破坏该不变量）；③ 新逻辑子包（如 `logic/paneltext`，最干净但新增结构）。 | `view.go`（internal/app） | ⬜ **待拍板（纯归属，非功能缺口）**。文案分支的测试覆盖已补（`TestModeCopyBranchesAreExact` 钉死每分支精确串、变异证明改坏仅本用例红）。剩下的纯是搬迁归属、行为不变；step8b 已解掉「坐在 package main」的原始顾虑，是否再下沉到独立 logic 子包属可选优化，不挡「全功能迁移」。 |
| 12 | **`internal/providers` 契约的 Go 形态**（照 `voke-core/provider.rs`/`action.rs` 移植时的设计岔口）：① `Provider::query` 在 Rust 是 `async fn→BoxFuture<Vec<Match>>`（为 object-safe）；纯 provider（base64/calculator/uuid/random/timestamp）同步且廉价，数据型（apps/windows/workspace）需后台——Go 接口取 `Query(QueryCtx) []Match`（同步）还是 `chan`/future？倾向：同步接口 + 数据型 provider 的后台加载沿用现有 `appsmodel` singleflight + `Window.Update` 接缝，不进 provider 接口。② `Action` 变体需读全 `action.rs` 再 1:1 建（已知 None/Launch{path,args}/Copy{text}/SetAwake{minutes,keep_screen}/CancelAwake/Refresh，可能还有）。③ 新 providers/ 是**增量**：填充现在 `pluginPending()`「未接入」分支，**apps 现有 `panelstate.AppEntry` 路径先不并入 Match 模型**（避免动到 V 已验的 Global）；apps 最终是否统一进 Match 留迁移后定。 | `internal/providers`（待建）↔ `voke-core/provider.rs`/`action.rs` | ⬜ **待实现时定夺**。倾向方案已记上；下一步先建纯模型（Interact/Match/QueryCtx/Provider/ProviderConfig）+ 第一个 provider，apps 统一留后。不挡施工 |

### 10.3 已定案（不再讨论）

| # | 事项 | 定案 | 位置 |
|---|---|---|---|
| 1 | 特效候选表勾选（E1–E16） | E1/E2/E3/E6/E8/E9/E16 保持；E4 开（仅 tooltip + 设置窗分组头）；E5 保持自带（见 §7.5）；E7 改用 `Selection`；E10/E11/E12 砍（硬阻塞）；E13/E14/E15 排除 | §7.5 |
| 2 | 输入框字号 | **16 / 副行 12**，写成 `titleSize + 2` | §7.4 |
| 3 | 面板垂直位置 | ☑ **面板中心锚点 + 48%**（锚点从「输入框中心」改为「面板中心」——这是 42% 反复仍偏高的根因；详见 §7.6.2） | §7.6 |
| 4 | 唤起是否懒加载 | **懒加载**（启动 / 唤起均不枚举，首个非空查询才触发） | §7.7 |
| 5 | 列表项单击 vs 双击 | **单击即执行**（用 `ui.ButtonBase` 包整行，不自造双击检测；行视觉见 §7.5.1） | §7.8 |
| 6 | 前缀兜底：`#ba` 要不要回落命中 | **照抄 CmdPal**——全等触发不兜底，改在排序层给 alias bonus 托底到 Fuzzy tier | §7.3.6 a |
| 7 | 前缀字符集与分配表 | **参考 CmdPal 的符号键分配**——原则「按符号键、不占用字母」；具体字符集开发阶段按需增补 | §7.3.6 b |
| 8 | 多显示器定位 | **总在主显示器唤起**，对齐生产 voke | §7.6 |
| 9 | 重写总路线 | **Go**，双门槛均过（见 §1.2）；Rust 侧暂不删，退路不需 patch | §1.3 |
