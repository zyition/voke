# 匹配与拼音一致性

> Go 版匹配层是 Rust 版行为的 1:1 移植。迁移期的完整对照记录在
> [`docs/legacy/voke-go-poc.md`](../legacy/voke-go-poc.md) §5.3 / §10-8 / §10-9，
> 本文只保留维护时需要知道的结论。

## 1. 搜索键

`internal/match/search_key.go` 在索引期生成，查询时零汉字转换：

```text
"微信"               → name="微信", full=["weixin","weishen"], initials=["wx","ws"]
"网易云音乐"         → name="网易云音乐", full=[...], initials=["wyyyl","wyyyy"]
"Visual Studio Code" → name="Visual Studio Code", acronym=["vsc"]
```

- 纯 ASCII query：对 name / full / initials 全部搜索键跑模糊匹配。
- 拉丁文名字的**词首缩写**不建键，由 `quality.go` 的 `acronymMatch` 在查询期判定
  （Rust 参考实现只给汉字名建首字母键，英文名只能靠 fuzzy）。规则：把名字切成词
  （非字母数字 + 驼峰边界），查询的连续片段依次各取一个词的非空前缀。
  于是 `vsc` / `vscode` / `vs code` / `code` 都能命中 `Visual Studio Code`，
  中文段落不参与（没有「词首」概念，走拼音键）。
- 含汉字 query：只对原名匹配（避免拼音键与汉字查询互相干扰）。
- 数据型 provider（窗口 / 工作区 / 终端）的条目每次查询会重读，
  用 `SearchKeysCache` 复用搜索键，避免每次按键重算。

## 2. 排序分：档位加分 + 有界使用加成

nucleo 的分只奖励连续 / 边界，档位之间的分差很小（`gma` 对 `gmail` 仅 0.44，
对只是散点命中的长名字也有 0.35–0.40）。直接拿它排序，再叠上使用频率加成，
就会出现「不相干条目排在精确项前面」。所以先把命中形态分档
（`internal/match/matcher.go` 的 `MatchQuality`，主流启动器 Wox / Flow Launcher /
Raycast 同一取舍）：

| 档位 | 含义 | 例 |
| --- | --- | --- |
| `QualityExact` | 与某个搜索键全等 | `gmail` → `Gmail`；`wx` → 微信的首字母键 `wx` |
| `QualityPrefix` | 某个搜索键以查询开头 | `gma` → `gmail` |
| `QualityAcronym` | 查询由名字的若干词首按序拼成 | `vsc` / `vscode` / `vs code` → `Visual Studio Code` |
| `QualitySubstring` | 查询是某个搜索键的连续子串 | `mail` → `Gmail` |
| `QualityFuzzy` | 只有子序列命中 | `gma` → `Gamma` |

- 一个条目有多个搜索键（原名 / 全拼 / 首字母）→ 取「档位最高、其次分最高」的那个键。
- 多词查询：整串与逐词各判一次，取**较强**的那个（`quality = max(q(整串), min(q(各词)))`）。
  - 整串负责 `visual studio` 这种「本来就是前缀」的输入（逐词会把它压成子串）；
  - 逐词取最低档负责「每个词都得沾边」，防止空格放水。
- **排序分只有一处实现**（`internal/logic/rank`）：

  ```
  Final = Weight × (nucleo 分 + TierBonus[档位] + UsageBonus[次数])
  TierBonus: 全等 0.40 · 前缀 0.30 · 词首 0.20 · 子串 0.10 · 模糊 0
  UsageBonus = min(0.12, 0.025·ln(1+n))
  ```

  相邻档差 0.10、使用加成封顶 0.12 → 长期高频（约 55 次启动）+ 分数接近时可以顶掉
  **相邻一档**；两档差 0.20 > 0.12 → 永远顶不了两档（`gma` → `Gmail` 不会被反超）。
- 分档判断是纯字符串关系（`quality.go`），与 nucleo 的 DP 无关。

## 3. 模糊评分

- `internal/match/nucleo_*.go` 是 nucleo-matcher 0.3.1 评分路径的 Go 移植，
  不是替换成另一个 fuzzy 库。保留了 char-class 边界加分、最优 DP 与
  Smart 归一化语义。
- 为 voke 的调用方式裁掉了不可达路径（ASCII haystack 快路径、indices 高亮、
  grapheme 精确分割），不影响命中与分数。
- 一致性由 Rust 真值语料锁住：74 个名称 × 185 条查询 = 13,690 条
  `(name, query)` 分值断言（`testdata/rust_corpus.tsv` 共 13,764 行，另含
  74 条搜索键行），逐条比对命中与原始 u32 分；另有搜索键逐名比对与
  退化分歧单列断言。

## 4. 拼音数据

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

## 5. 已知手感问题

- 汉字 query 只看原名键，nucleo 分通常低于同义拼音（「微信」≈0.31 vs
  「weixin」≈0.83）；但汉字 query 走 substring atom，命中即落在
  Substring / Prefix / Exact 档，排序上已不再吃亏（分档之前的「中文输入偏后」
  已消解）。
- 系统应用的 MSIX 显示名是中文时，只建了拼音/首字母键，英文名（如
  `terminal` → 「终端」）搜不到，需别名表才能支持。

## 6. 相关测试

- `TestQualityTiers` / `TestMultiWordQueryKeepsQuality` / `TestPrefixBeatsAcronym`：
  档位判定（含词首缩写与多词取 max）。
- `rank` 包的 `TestUsageCrossesOneTierOnly` / `TestUsageIsBounded`：
  使用加成有界，只能顶掉相邻一档。
- `TestFilterAppsPrefixBeatsFuzzy` / `TestFilterAppsUsageCrossesOneTier` /
  `TestFilterAppsNoUsageKeepsRankDescending`：应用过滤层的端到端排序。
- `TestScoresMatchRustCorpus`：逐条分值比对 Rust 真值语料（nucleo 端口未改）。
- `TestKeysMatchRustCorpus`：搜索键（full / initials）逐名比对。
- `TestDegradationDivergence`：多音字退化分歧的显式记录。
- `internal/match/pinyin_test.go`：归一化与抽取规则的对齐抽查。
