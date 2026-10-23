# 排序模型与重构方案

> **状态：已按本文落地（2026-10-23），落地时的取舍见 §10。**
>
> 目标：把「字符串关系 + 使用习惯」合成排序的过程收敛成**一层的单一内核**，
> 让「匹配越好越靠前」与「常用条目靠前」是**同一个标量里两段有界的贡献**，
> 并让调参只改一个文件。本文是实施方案；落地后核心结论并回 [`matching.md`](matching.md)。

## 1. 结论先行

三层职责，各自只有一个家：

| 层 | 包 | 职责 | 不做什么 |
| --- | --- | --- | --- |
| 字符串关系 | `internal/match` | 命中形态（档位）+ nucleo 连续分 | 不认识权重、频率、provider |
| 使用习惯 | `internal/logic/frequency` | 次数 → **有界**加分 `UsageBonus` | 不认识 provider、不做排序 |
| 合成排序 | `internal/logic/rank`（新） | 档位折算 + 权重 + 使用加成 → 单一排序分 | 不碰 IO / provider / UI |
| 记录与落盘 | `internal/app` | 执行成功后 `Record`、启动读 / 变更写 | 不参与权重计算 |

**排序分（唯一形式，单标量）**：

```
Final = Weight × (Score + TierBonus[Quality] + UsageBonus(n))

TierBonus:  Exact 0.40 | Prefix 0.30 | Acronym 0.20 | Substring 0.10 | Fuzzy 0
UsageBonus(n) = min(0.12, 0.025·ln(1+n))
               1 次 0.017 · 5 次 0.045 · 31 次 0.086 · 100 次 0.115 · ≥147 次封顶 0.12
```

由此得到三条可解释的性质：

- **相邻档差 0.10，usage 封顶 0.12** → 长期高频（约 55 次启动以上）+ 分数接近时，
  可以顶掉**相邻一档**。
- **两档差 0.20 > 0.12** → 永远顶不了两档。`gma` 命中的是一堆散点模糊条目（Fuzzy），
  比 `Gmail` 的 Prefix 差 0.30+，不会因为用得多久被反过来。
- **同档内**：分数差通常 0–0.2，几次启动就能重排——「越用越准」主要发生在这一层。

## 2. 为什么不用「档位当主键」

硬档位（`(Quality, score×usage)` 字典序）能做到「精确永远第一」，但代价是：
**使用习惯对跨档完全无效**。而使用习惯恰恰是启动器区别于纯模糊搜索的核心信号——
一个每天开 30 次的程序，匹配上只输一档，却永远排在一个从不使用的条目后面，
这与「尽可能命中用户想要的」这个第一性目标冲突。

所以档位不做主键，只做**有刻度的加分**：

| 方案 | 相邻档 | 跨两档 | 评价 |
| --- | --- | --- | --- |
| 现状（乘性无界 boost） | 1 次使用即跨 | 可跨 | 匹配质量被完全压过 |
| 硬档位（字典序） | 永不跨 | 永不跨 | 使用习惯失效 |
| **本方案（有界加法）** | **~55 次使用可跨** | 永不跨 | 两个信号都保留 |

## 3. 现状问题清单（证据）

1. **两套排序内核**：apps 走 `search.FilterApps`（`score × boost`），
   窗口 / 终端 / 工作区 / 插件走 `providers.Registry.Rank`（`weight × score × boost`），
   插件 picker 走 `config.Rank`（**完全不吃 freq**）。改一次权重得改三处。
2. **量纲不匹配**：档位是量化值（每档 0.2），boost 是无界乘数。实测当前曲线：
   跨 Exact→Prefix 只需 **0.7 次**使用、Prefix→Acronym 只需 **1.3 次**，
   而 Fuzzy 顶掉 Substring 要 **251 次**；档内一次启动就 +35%，分差通常 <5%。
   即「相邻档之间形同虚设，档内完全按使用次数排」。
3. **档位有实据但量化过度**：真实语料 96 个三字符查询，纯 nucleo 的 top1 有 92%
   是「以该查询开头」的条目，分档后 100%——档位只修 8% 的边界误判，
   却引出「档宽」「要不要再细分一档」的连锁复杂度。
4. **多词查询必然掉档**：质量取各词最低档，`visual studio`（本是前缀）掉到包含档，
   `vs code` / `vscode`（都是 VS Code）掉到模糊档。
5. **缩写键是补丁**：`SearchKeys.Acronym` 只能表达「正好等于词首串」，`vscode` 仍不行，
   还要额外维护 keyKind 与封顶逻辑。
6. **截断在 boost 之前**：命中超过 50 条时（实测 `a` 有 63 条），第 51 名以后的
   常用条目永远吃不到使用加成。
7. **频率表有脏键**（见 §4）：临时结果、带时间戳的标题也在累计，等于给噪声发权重。

## 4. 频率表格式（现状）

- **路径**：`%APPDATA%\voke\frequency.toml`（`platform.DataDir()`；`VOKE_FREQ` 可覆盖）。
- **结构**：扁平 `"provider:标题" = 次数`，值是 `uint32` 累计次数；
  由 `toml.Marshal(map[string]uint32)` 生成，所以带冒号 / 中文的键会加引号。
  与 Rust 版**同格式共用**（`appcfg.FrequencyToTOML`）。
- **没有**时间戳、权重、衰减——纯计数；记录点是执行成功时（`internal/app/view.go`），
  整表重写落盘（`saveFrequency`）。

实测样本（本机，66 行）：

```toml
"apps:Alacritty" = 31
"apps:钉钉" = 17
"apps:微信" = 5
"terminal:PowerShell" = 74
"windows:(1) 主页 / X - Google Chrome" = 14
"awake:保持唤醒中 · 屏幕打开（还剩 1 小时 12 分钟）" = 1
"random:8USAgNEtFt" = 1
"uuid:5cf3be2c-bec6-43bf-a4ad-98852ff5034d" = 1
```

暴露三个问题（都进本方案的清理项）：

1. **临时结果在记分**：`uuid:*` / `random:*` 是生成出来的值，永不复用；
   `awake:*` 的倒计时把「剩余时间」写进键，每次都是新键。
2. **易变标题学不到**：窗口条目的键是标题（`… - Google Chrome`、终端里的分支名），
   每天都在变 → 窗口 / 终端这两类几乎不可能累计。
3. **显示名带描述**：`apps:PowerToys - Windows system utilities to maximize productivity`
   ——AppsFolder 的描述被拼进了名字，既污染排序也污染匹配。

## 5. 目标结构

```
internal/match
  matcher.go     ScoreEntry → MatchScore{Score, Quality}      （删 Rank()）
  quality.go     档位判定：Exact / Prefix / Acronym / Substring / Fuzzy
  search_key.go  只保留拼音键：Name / Full / Initials        （删 Acronym、keyKind）
internal/logic/frequency
  frequency.go   UsageBonus(n) = min(0.12, 0.025·ln(1+n))     （Boost → UsageBonus）
  recordable.go  记录白名单：哪些 provider / 结果才进表       （见 §5.2）
internal/logic/rank        （新包，唯一排序内核）
  rank.go        TierBonus(q) float64
                 Final(quality, score, weight, usage) float64
                 UsageOf(freq, providerID, title) float64     // nil 安全
                 Sort(items, signalOf)                        // stable
internal/logic/search      FilterApps：match → rank（删自有 boost 逻辑）
internal/providers         Registry.Rank：match → rank（删自有 boost 逻辑）
internal/logic/config      plugin picker 的别名排序：沿用原语义，不吃使用习惯（§10）
internal/app               Record / loadFrequency / saveFrequency 不变
```

依赖方向：`app → {search, providers, config} → rank → {match, frequency}`；
`match` 与 `frequency` 互不依赖，都是纯逻辑，Linux 原生可测。

### 5.1 档位判定规则（`internal/match/quality.go`）

对候选键 K 与查询串 Q（大小写不敏感；多词查询同时判整体与各词）：

| 档 | 条件 | 例 |
| --- | --- | --- |
| `Exact` | K == Q | `gmail` → `gmail`；`wx` → 微信首字母键 |
| `Prefix` | K 以 Q 开头 | `gma` → `gmail` |
| `Acronym` | Q（去空格）可拆成若干非空前缀，按序对应 K 的词首序列 | `vsc` / `vscode` / `vs code` → `Visual Studio Code` |
| `Substring` | Q 是 K 的连续子串 | `mail` → `Gmail` |
| `Fuzzy` | 仅子序列命中（nucleo 命中即可） | `gma` → `Gamma` |

- **词** = 按非字母数字字符切 + 驼峰（lower→upper）切；只有 ASCII 字母数字组成的词参与，
  中文段落不参与（中文走拼音键）。
- **多词查询**：`quality = max( q(Q 整体), min_i q(A_i) )`。
- **一个条目多个键**（原名 / 全拼 / 首字母）：取档位最高、其次分最高的键。
- 删除 `SearchKeys.Acronym` 与 `keyKind`：`Acronym` 由规则判定，顺带支持 `vscode`。

### 5.2 频率记录白名单

- 只记**由用户动作产生、且键稳定**的结果：应用启动、窗口激活（键改用进程名）、
  工作区打开、终端 profile、插件里语义稳定的行（`awake:1 小时`）。
- 不记：`uuid` / `random` 这类生成值、含倒计时 / 时间 / 页码的标题、剪贴板历史。
- 名字清洗：去掉 AppsFolder 的 ` - <描述>` 后缀（只影响展示与匹配，不改 ID）。

## 6. 行为变化（用户可见）

1. 「精确 / 前缀更靠前」仍然成立，但不再绝对：长期高频的相邻档条目**能**顶上来
   （约 55 次启动），跨两档的乱序依然不可能。
2. `vscode`、`vs code` → Visual Studio Code 从模糊档升到缩写档；
   `visual studio` → Visual Studio 2022 从包含档升到前缀档。
3. 删缩写键后 `wp` → Windows PowerShell 从「全等」降为「缩写档」（仍高于包含 / 模糊）。
4. 「越用越准」变温和：5 次 ≈ +0.045，31 次 ≈ +0.086，100 次封顶 0.12。
5. 命中 > 50 条时，常用条目也能进入结果（截断挪到排序之后）。
6. 频率表不再被临时结果污染（新装用户的表会短很多）。

## 7. 实施步骤（每阶段独立全绿）

**Phase 1 — 排序内核 + 有界使用加成**
- 新建 `internal/logic/rank`；`frequency.Boost` → `UsageBonus`（有界）。
- `FilterApps`、`Registry.Rank`、`config.Rank`（补 usage 通道）全部改走 rank。
- 删 `MatchScore.Rank()`。
- 验收：`TestUsageCrossesOneTierOnly`（~55 次可跨一档、任意次数跨不了两档）。

**Phase 2 — 档位规则升级**
- 新增 `quality.go`：词首缩写判定 + 多词 `max(整体, min 各词)`。
- 删 `SearchKeys.Acronym` / `AcronymKey` / `keyKind` / 封顶逻辑及其测试。
- 验收：`vsc` / `vscode` / `vs code` / `visual studio` 四条断言；语料 top1 前缀正确率仍 100%。

**Phase 3 — 收尾**
- 候选截断挪到排序之后（窗口放宽到 200，再截 50）。
- 频率记录白名单（`frequency.Recordable`）+ 加载时清洗历史脏键。
- 文档：`matching.md` 第 2 节重写为本文 §1 的公式。

## 8. 验收与回归

- `gofmt -l .` 空；`go vet ./...`；`go test -race -count=1 -shuffle=on ./...` 全绿；
  `GOOS=windows` 交叉构建通过。
- 语料 top-1 前缀正确率 100%（`TestScratchTopOne` 的正式版，进 `internal/match`）。
- 不变量：`UsageBonus ≤ 0.12`、两档差 0.20 恒大于它、空查询行为不变、
  `frequency.toml` 仍能被旧版读取（格式不变）。
- 性能：`FilterApps` 与 `Registry.Rank` 的 `Benchmark` 不劣于现状 10%。

## 9. 本次不做（明确边界）

- **不细分 Substring 子档**：档内用 nucleo 分排序已足够；再分档会重现当前复杂度。
- **不改 `frequency.toml` 结构**：与 Rust 版共用格式，加时间戳会破坏互通。
  「recency 衰减」需要单独决策（方案：新增可选段并保留向后兼容读取）。
- **不动 nucleo 移植**：fzf 同款评分（nucleo 官方声明 same scoring system as fzf），
  Rust 真值语料逐分锁定，任何改动都会失去 oracle。
- **不做 query→item 关联学习**：收益不确定，且需要新持久化结构。

## 10. 落地记录（2026-10-23）

落地时相对本文的三处取舍：

1. **词首缩写改成查询期规则，不再建索引键**。原计划在 `SearchKeys` 里加
   `Acronym`，实测发现它只能表达「正好等于词首串」，`vscode` / `vs code` 仍不行，
   还要额外维护 keyKind 封顶逻辑。改成 `match/quality.go` 的 `acronymMatch`
   （查询片段依次取词首前缀）后：键表回到三路、少一套索引，且 `vsc` / `vscode` /
   `vs code` / `code` 全部命中 `Visual Studio Code`。
2. **插件 picker 不接使用习惯**。白名单只收查找类 provider（apps / workspaces /
   terminal），插件（base64 / uuid / random / awake…）不再记分，所以 picker 侧
   没有可加的使用加成；其排序仍由 alias bonus 决定（`config.Rank` 不动）。
3. **窗口不记使用习惯**。窗口键是标题（含页码、分支名），每次都在变；与用户确认
   「不计也 ok」。终端 profile 名稳定，保留。

未做：

- **应用名去掉 ` - <描述>` 后缀**：AppsFolder 的部分显示名带描述
  （`PowerToys - Windows system utilities…`），既污染匹配也污染频率键。清洗需要
  判断哪些 ` - ` 是描述、哪些是名字的一部分（`Code - Insiders`），风险大于收益，
  留待单独决策。
- **recency 衰减**：`frequency.toml` 与 Rust 版共用格式，加时间戳破坏互通（§9）。

## 11. 性能（2026-10-23 落地后）

排序语义没改，但把「同一件事算上千遍」都消掉了。1200 条**互不相同**的候选，
`go test -run XXX -bench FilterApps -benchmem ./internal/logic/search`：

| 版本 | 每次按键 | 分配 | 其中 |
| --- | --- | --- | --- |
| `d2b7e90`（v0.4.0） | 6.70ms / 4.19MB / 180k allocs | 搜索键缓存装不下（上限 512 < 1200）→ 整表清空，每键重算拼音键 |
| 分档+加法的第一版 | 6.6ms（同样卡在缓存）+ 档位判定本身多 40% | 每候选重编译查询、每键切词分配 |
| 现在 | **0.80ms / 104KB / 16 allocs** | — |

四处改动：

1. **`match.Compile` → `Query`**：查询只解析一次，所有候选复用。
   此前每次 `ScoreEntry` 都重新切词 / 转 rune / 建 atom（1160 条候选实测省 164µs、5.5k 次分配）。
2. **`SearchKeys.refs`**：每个搜索键的小写形态在索引期算好（键不变，小写也不变），
   档位判定不再每键 `ToLower`。
3. **缩写判定零分配**：流式切词 + `uint64` 位掩码（查询 > 63 字符跳过），
   取代原先的 `splitWords` + 布尔切片。
4. **`rank.SortBy`**：排序分在进排序前算一次，比较器里不再拼频率键。
5. 顺带修了 **`match.CacheCapacity` 512 → 4096**：apps 动辄上千条，
   缓存装不下时是「整表清空」策略，命中率直接归零、每次按键重算全部拼音键
   （实测 5.1ms → 0.8ms）。这是上一版在真机上「打字明显延迟」的主要原因之一。

回归由 `internal/logic/search/bench_test.go` 的常驻基准守住。

### 输入路径：从「帧内同步」改成「投递 + 回主线程」

Global 态原来每一次按键都在**渲染帧内**同步跑完「Scan → FilterApps → 全部 is_global
插件」，输入回显要等这些算完。两处必须改：

| 问题 | 原行为 | 现行为 |
| --- | --- | --- |
| 窗口枚举 | 每键 `EnumWindows` + 逐窗取标题 / 进程名 / exe 路径，**窗口多时阻塞几十到几百毫秒**（`platform/windows_enum_windows.go` 自己的注释） | 后台快照（`internal/app/winsnapshot.go`），主线程只读 `[]Entry`；**查询不触发枚举** |
| 过滤 | 帧内同步 `FilterApps` | 投递到单 worker（`internal/app/filter_async.go`），结果经 `Window.Update` 回主线程；世代号丢弃过期结果 |

窗口快照的刷新只有两个触发点（都单飞，跑完即退）：

- **面板唤起**（`showPanelEntry`）刷一次，保证这次搜索看到的是当下的窗口；
- **可见期间每 2s** 刷一次（`windowRefreshInterval`，一次性 `time.AfterFunc` + 世代号自重挂），
  隐藏（`hidePanel`）与退出（`OnWillQuit`）都停表，面板关着时没有任何常驻定时器或枚举。

配套细节：

- 队列容量 1，满了丢旧留新（latest-wins）——连续打字时中间的查询根本不需要算完；
  全进程只有一个过滤 goroutine，跑完阻塞在 channel 上，进程退出即回收。
- 后台只读不可变快照：apps 列表按 slice 头传入（主线程只整体替换、不改数组），
  频率表按版本惰性克隆（`freqVersion` 变了才复制）。
- 空查询仍就地同步算（复制 + 按使用次数排，约 1ms）：否则首帧会闪一下「没有匹配」。
- 测试/装配早期 `runner == nil` 时走同步路径，断言语义与改造前一致。
- 落地页 `applyFilterResult` 与 `applyEngineState` 共用同一套「哪条结果区在用」的
  判定（picker / 已落地 provider / 未落地插件都不落 apps 结果）。

### 常驻开销清单（笔记本口径，2026-10-23 审计）

目标：空闲 / 隐藏时除了剪贴板必须的那点轮询，不应该有任何周期性工作。

| 来源 | 触发条件 | 空闲成本 | 备注 |
| --- | --- | --- | --- |
| 剪贴板 | **事件驱动**：`AddClipboardFormatListener` + `WM_CLIPBOARDUPDATE`（`platform/clipboard_watch_windows.go`） | 空闲 0 唤醒、0 系统调用 | 监听失败 / 非 Windows 自动回退 500ms 轮询；插件停用时空转（`clipWatchOn`） |
| 窗口快照 | 面板唤起 + 可见期每 2s | 隐藏时为 0 | 隐藏 / 退出即停表 |
| 图标缓存裁剪 | 隐藏后 10s 一次性 | 0 | 10s 后把解码缓存 256→64 |
| 保持唤醒重申 | 仅用户开启时，30s | 一次 `SetThreadExecutionState` | 功能本身 |
| 热键录制超时 | 仅录制时 | 0 | 一次性 |
| 粘贴 / 焦点延迟 | 动作时 60ms | 0 | 一次性 |
| mygo 帧循环 | 事件驱动（无 VSync 空转） | 0 | 空闲后释放帧内存（mygo `windowHost.idle`） |

常驻内存的大头（都有上界）：搜索键缓存 `CacheCapacity`=4096（约 1MB 上限）、
解码图标缓存 256 条（隐藏 10s 后 64）、剪贴板历史（按配置条数）、频率表（按不同应用名）。

剪贴板监听已改为事件驱动（自建 message-only 窗口，见上）。mygo 的 `listeners` 类型与
`AppHandler` 接口都在 internal 包里、不对外暴露，无法注册自定义原生事件，所以事件源
只能自己建——这也是 voke 里唯一一处「绕过 mygo 自建窗口」的地方。
