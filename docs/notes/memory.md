# 内存测量与优化现状（Go + mygo）

> 本文是 Go 主线的内存口径、快照标签与已落地优化。Rust/gpui 时代的完整杠杆分析
> 已归档到 [`docs/legacy/rust-memory-footprint.md`](../legacy/rust-memory-footprint.md)，
> 其中的数字与 patch 建议不再适用于 mygo。

## 1. 快照标签与字段

`platform.SnapshotMem(tag)` 只在 Windows 生效（非 Windows 为占位），日志写入
`%APPDATA%\voke\voke.log`。当前标签：

| 标签 | 时机 |
|---|---|
| `启动` | 面板窗口与托盘就绪后 |
| `面板首次唤起` | 第一次显示面板 |
| `引擎索引完成` | 应用列表枚举并入索引后 |

一行示例：

```text
内存快照 [启动]：WorkingSet=29.2MB PrivateCommit=55.3MB GoHeap=3.5MB HeapSys=11.8MB HeapIdle=7.1MB HeapReleased=6.9MB Objects=11169 GC=1
```

- `WorkingSet` / `PrivateCommit` 来自 `K32GetProcessMemoryInfo`。
- `GoHeap` / `HeapSys` / `HeapIdle` / `HeapReleased` / `Objects` / `GC` 来自
  `runtime.ReadMemStats`。
- 归因方法：`PrivateCommit` 涨而 `GoHeap/HeapSys` 不涨 → 增长在原生侧
  （D3D11 / DirectWrite / 系统图标与枚举）；Go 侧同步涨 → 先查 Go 堆。
  `HeapIdle - HeapReleased` 才是进程攥着没还的闲置量。

## 2. 2026-10-19 实测（本机，200% 缩放）

| 阶段 | WorkingSet | PrivateCommit | GoHeap |
|---|---|---|---|
| 启动 | ~29–31MB | ~55MB | ~3.6MB |
| 面板首次唤起 | ~30–32MB | ~55MB | ~3.6MB |
| 引擎索引完成（首轮） | ~107MB | ~110MB | ~6.8MB |
| 后续索引 | ~116MB | ~110MB | ~6MB |

结论：

- **~55MB 是启动地板**，主要是 mygo 的 D3D11 设备、DirectWrite 文本系统、
  窗口与托盘的原生资源；Go 堆只有几 MB。
- **索引后增加 ~55MB**，主要来自应用枚举的 Shell/COM 与图标相关原生分配，
  不是应用条目本身。应用数据 / 搜索键 / 图标缓存合计约 2MB 量级。
- Go 堆长期在 6–20MB（HeapSys）内，`HeapReleased` 会随 GC 上升，暂未见
  持续爬升的泄漏形状。

## 3. 已落地的优化

- **设置窗关闭即程序化销毁**：每次打开重新建窗，关闭后渲染资源归还；
  不再「隐藏复用」常驻约 20MB+。
- **图标缓存双层有界**：
  - 解码位图 256 条 LRU（`internal/logic/iconcache`）；
  - PNG 字节 256 条 FIFO（`internal/platform` 的 `iconcache`），nil 负缓存保留；
  - 面板隐藏 10 秒后把解码缓存裁到 64 条（设置窗打开时跳过）。
- **面板窗口关闭 resize / vibrancy / transparent**：`DisableResize: true`，
  `Vibrancy` 留空、`Transparent` 关闭，避免额外的合成缓冲和 resize 重建。
- **构建期 `mygo_noinspector`**：不打包 inspector，减小二进制与运行时开销。
- **日志行数上限**：默认 9999，跨启动追加、超限删头批，避免日志无限增长。

## 4. 已知边界

- mygo 在 Windows 上没有遮挡检测，隐藏窗口不等于停帧；因此**不要给隐藏态引入
  持续动画/定时重绘**，否则会持续占 CPU 与显存。
- `EmptyWorkingSet` / `SetProcessWorkingSetSize` 只改任务管理器读数、不改
  PrivateCommit，属仪表盘 trick，当前不做。
- 图形栈地板由 mygo/D3D11/DirectWrite 决定；继续压低的收益与风险需单独评估，
  不要通过破坏窗口生命周期来换读数。
- 应用列表枚举的原生占用与系统规模相关（应用数量、图标数量、缩放比例）；
  换机对比时必须记录这三项。
