# 匹配与拼音一致性

> Go 版匹配层是 Rust 版行为的 1:1 移植。迁移期的完整对照记录在
> [`docs/legacy/voke-go-poc.md`](../legacy/voke-go-poc.md) §5.3 / §10-8 / §10-9，
> 本文只保留维护时需要知道的结论。

## 1. 搜索键

`internal/match/search_key.go` 在索引期生成，查询时零汉字转换：

```text
"微信"       → name="微信", full=["weixin","weishen"], initials=["wx","ws"]
"网易云音乐" → name="网易云音乐", full=[...], initials=["wyyyl","wyyyy"]
```

- 纯 ASCII query：对 name / full / initials 全部搜索键跑模糊匹配。
- 含汉字 query：只对原名匹配（避免拼音键与汉字查询互相干扰）。
- 数据型 provider（窗口 / 工作区 / 终端）的条目每次查询会重读，
  用 `SearchKeysCache` 复用搜索键，避免每次按键重算。

## 2. 模糊评分

- `internal/match/nucleo_*.go` 是 nucleo-matcher 0.3.1 评分路径的 Go 移植，
  不是替换成另一个 fuzzy 库。保留了 char-class 边界加分、最优 DP 与
  Smart 归一化语义。
- 为 voke 的调用方式裁掉了不可达路径（ASCII haystack 快路径、indices 高亮、
  grapheme 精确分割），不影响命中与分数。
- 一致性由 Rust 真值语料锁住：74 个名称 × 185 条查询 = 13,690 条
  `(name, query)` 分值断言（`testdata/rust_corpus.tsv` 共 13,764 行，另含
  74 条搜索键行），逐条比对命中与原始 u32 分；另有搜索键逐名比对与
  退化分歧单列断言。

## 3. 拼音数据

- 使用 `github.com/mozillazg/go-pinyin`，输出对齐 Rust `pinyin 0.11` 的
  plain/Normal 形态。
- 封装层处理三处差异：
  - `ü`：go-pinyin 写作 `v`，统一还原为 `ü`（因此 ASCII `lv` 与 Rust 一样
    打不中“绿色”的键）；
  - `ê` 系列预组合声调字符：go-pinyin 漏剥，显式回落为 `ê`；
  - 组合声调符号直接剔除。
- 28,096 个码点逐字比对：覆盖完全一致（26,704 个有拼音数据的字读音集合相同），
  去重后读音顺序也一致。
- 唯一实质差异是多音字重复读音导致的 `VARIANT_LIMIT=8` 退化点分歧
  （Rust 按含重复总数、go-pinyin 按去重后判断），会产生双向的少量命中差异；
  语料测试对这部分单独标记。需要字节级一致时，可绕过 go-pinyin 的去重封装，
  直接读导出的 `PinyinDict`（成本与风险见 legacy 文档 §10-9）。

## 4. 已知手感问题

- 汉字 query 的得分通常低于同义拼音（如「微信」≈0.31 vs「weixin」≈0.83），
  命中性不受影响，但中文直接输入的排序偏后；是否对汉字路径单独加成尚未定。
- 系统应用的 MSIX 显示名是中文时，只建了拼音/首字母键，英文名（如
  `terminal` → 「终端」）搜不到，需别名表才能支持。

## 5. 相关测试

- `TestScoresMatchRustCorpus`：逐条分值比对 Rust 真值语料。
- `TestKeysMatchRustCorpus`：搜索键（full / initials）逐名比对。
- `TestDegradationDivergence`：多音字退化分歧的显式记录。
- `internal/match/pinyin_test.go`：归一化与抽取规则的对齐抽查。
