# 主线程与渲染帧预算（Go + mygo）

> 一句话：渲染帧是主线程上一段**同步、不可中断**的工作。凡是「耗时不可预测 + 只能
> 主线程」的调用都不能进帧；进帧的代价不是「慢一点」，而是**每一帧都慢、输入跟着
> 一起等**。本文记录已实测的黑洞、现有的绕法、以及怎么量。
>
> Rust/gpui 时代的对应分析在 [`docs/legacy/rust-memory-footprint.md`](../legacy/rust-memory-footprint.md)，
> 数字与结论不适用于 mygo。

## 1. 帧什么时候跑

mygo 只在有变化时画帧（input、`Invalidate`、`After`、动画），空闲窗口不画
（`mygo/docs/ui/rendering.md:41-43`）。所以慢帧不是「一直卡」，而是**恰好在某一帧**：
用户敲键 → 内容变 → 重建 → 这一帧里所有同步工作都要跑完，之后才能处理下一个键。

一次按键的链路（都在同一帧内、同一线程）：

```text
TextInput.Changed()            internal/ui/view.go
  → onQueryChanged              internal/app/view.go
    → engine.Scan               纯字符串比较，µs
    → queryJob 投递            internal/app/query_async.go（唯一查询线程，latest-wins）
本帧继续构建整棵视图（build pass）
  → renderResults → 可见行 → 图标 / 文本 / 布局
```

查询本身（`FilterApps` / `RunOne` / `Aggregate`）已经在查询线程，输入回显不等它。
**剩下的风险全在 build pass 里**：那是我们自己的代码，每帧同步跑。

## 2. 已实测的黑洞

2026-11-10 真机（`MYGO_FRAME_STATS=4` + 临时探针，25 轮「输入 → 逐字删」）：

| 调用 | 实测 | 线程约束 | 现状 |
| --- | --- | --- | --- |
| `Shell.FileIcon`（`IShellItemImageFactory::GetImage`） | **平均 15.2ms，最大 72ms** | **只能主线程**（`onMainValue`） | 已出帧，见 §4 |
| `Window.Hide()` | ~21ms | 主线程 | mygo 内部，未处理（每次隐藏一帧慢，用户基本无感） |
| `goui.DecodeBitmap`（48px PNG） | <1ms（144 次无一超 1ms） | 纯 CPU，可后台 | 放帧内可接受 |
| `EnumWindows` + 逐窗取标题/进程 | 几十~几百 ms | 可后台 | 已挪后台快照（`internal/app/winsnapshot.go`） |
| TTL 数据源首键读 SQLite / settings.json | 几十 ms | 可后台 | 已在查询线程（`internal/app/query_async.go`） |
| `engine.Scan` | µs | 主线程 | 帧内，无所谓 |

改前改后的对照（同一套脚本、同样 25 轮）：

| 指标 | 改前 | 改后 |
| --- | --- | --- |
| 慢帧（>4ms） | 78 个，30~150ms | 输入/删字帧全部 <4ms；只剩隐藏帧 ~20ms |
| `renderResults` | 2221ms / 52 次 | ~2ms |
| 单帧 8 个新图标 | 100~150ms | 不再出现 |

## 3. 怎么量

mygo 自带帧统计，**不用改代码**：

```bash
MYGO_FRAME_STATS=4      # 只记录 >4ms 的帧；=1 表示 >8ms；=all 每帧都记
```

输出走 `log.Printf`，因此落在 `%APPDATA%\voke\voke.log`（`internal/platform/appdirs.go` 的 `InitLog`）。

```text
mygo: frame 217 took 124.4 ms: build 122.8, layout 0.5, paint 0.5, present 0.6 (...);
  meanwhile the process made about 6213 allocations, 6745.3 KB, 18 text layouts; GC: 1 cycles ended, 0.5 ms of assists
```

判读规则：

- **build 段独占** → 我们的 `view` 回调（取图、解码、所有同步 IO）。
- **present 段大** → GPU / swapchain，与本仓无关。
- **GC assists 大** → 分配压力（每帧全量 `make` 的中间结构）。
- `(2 passes)` 表示这一帧 build 了两遍：通常是构建期改了状态（焦点、选中）触发的二次构建。

定位到 build 之后，用临时探针把 `view()` 切成几段（样本抽取用 `time.Since` + `log.Printf`，
量完就删，别留在主干）。

## 4. 图标：唯一「必须主线程 + 耗时不可预测」的常见路径

`Shell.FileIcon` 在 Windows 上走 `SHCreateItemFromParsingName` +
`IShellItemImageFactory::GetImage`（`mygo/internal/windows/icon.go`），本质是问 Shell 要图，
耗时取决于 Shell 缓存与目标类型（`shell:AppsFolder\<AUMID>` 的打包应用最慢）。
mygo 官方口径也是「每张几 ms、在主线程画、只画显示出来的行并缓存」
（`mygo/docs/launchers.md:138`），**没有任何异步图源**：`ImageSource` 只有 `*Bitmap` / `*SVG`，
没有「给个路径自己加载」的句柄。

所以并发不了，只能调度。现在的做法（`internal/app/iconpump.go`）：

1. **三层缓存**：`internal/ui` 的解码位图 LRU(256) → `internal/platform` 的 PNG 字节 FIFO(256)
   → `internal/platform/iconstore.go` 的磁盘 PNG（`%APPDATA%\voke\iconcache\`，48px 约 1~6KB）。
2. **渲染帧只查缓存**：内存 → 磁盘（命中即解码，约 0.3ms），全 miss 才登记需求并返回 nil，
   行内用既有占位（apps 行首字母 `Avatar`、provider 行插件角标 SVG）。
3. **停手后补**：最后一次登记需求静默 90ms 才开补，一次补一张，补一张 `Window.Update` 重建一帧。
   连续打字期间一次都不取图，输入回显永远不等 FileIcon。
4. **隐藏时预热**：面板隐藏后把应用列表与窗口快照里的路径排进同一个队列，一拍一张地补完
   （只写字节/磁盘，不重建视图）；隐藏 10s 后解码缓存裁到 64 条，下次唤起只解可见的 8~9 个。

不要做的事：

- **批量预取**：FileIcon 串行且无超时，预取等于把不可控耗时排队塞进主线程。
- **把 FileIcon 丢 goroutine 假装异步**：`onMainValue` 会 post 回主线程并等待，代价不变，只多一次往返。
- **用 `Painter.After` 补图**：它只重绘不重建（`mygo/ui/paint.go:160`），换不了 bitmap。

解耦是对的、但**解码**可以离开主线程：`DecodeBitmap` 只做 `image.Decode` + `scene.NewImage`
（后者只取一个原子 id，`mygo/internal/scene/scene.go:487`），mygo 自己的 `Video` 就是这么解码的
（`mygo/docs/ui/image.md:63`）。目前解码 <1ms，留在帧内；真要挪，走 `Window.Update` 收回。

## 5. 平台 IO 坑：日志文件不能带 `O_APPEND`

2026-11-10 的事故：日志从某天起**静默停写**，文件永远停在恰好 9999 行。

原因是 `os.OpenFile(..., os.O_APPEND|os.O_RDWR, ...)`：Go 在 Windows 上遇到 `O_APPEND` 会
摘掉 `GENERIC_WRITE` 只给 `FILE_APPEND_DATA`，而裁剪历史行用的 `Truncate`（底层
`SetEndOfFile`）要求 `FILE_WRITE_DATA` —— 恒 `Access denied`。更要命的是 **Go 的 `log` 包
会丢弃 writer 返回的错误**，于是「裁剪失败」变成「之后每一行都消失」，且没有任何痕迹。

现在的契约（`internal/logic/logcap/logcap.go` 的 `File` 文档）：

- 用 `O_RDWR` 打开，**不要 `O_APPEND`**；`logcap.New` 自己 Seek 到末尾，删头批后 Seek 回 0 再重写。
- 裁剪失败**绝不吞日志**：降级为不封顶的纯追加，错误挂 `TrimError()`，接线层补一行中文警告。
- 测试的打开方式必须与生产一致——Linux 上 `O_APPEND` + `ftruncate` 完全合法，旧测试一直绿，
  这个平台差异永远测不出来。

## 6. 往渲染帧里加东西之前的检查清单

1. 这段代码会不会碰 IO / COM / Shell / 文件系统 / 注册表？会 → 不能进帧。
2. 它是每帧都跑，还是只在「某行第一次可见」时跑？后者可以贵一点，但要能摊到多帧。
3. 能不能改成「先占位 → 后台/下一帧补齐 → 再 `Window.Update` 重建」？
4. 改完用 `MYGO_FRAME_STATS=4` 跑一轮「输入 + 逐字删」，确认没有 build 段超 8ms 的帧。
5. 报错路径也要看：吞掉错误的 `log`/`Write` 会把问题藏起来（见 §5）。
