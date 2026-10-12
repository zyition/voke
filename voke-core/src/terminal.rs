//! terminal provider：Windows Terminal profiles（对齐 PowerToys Run 的 Terminal 插件）。
//!
//! ## 数据流
//!
//! ```text
//! voke-sys 读两个 settings.json（stable + Preview 的 LocalState）
//!         │  Vec<String>（文件原文，stable 在前；不存在的文件不进列表）
//!         ▼
//! profiles_from_settings（纯函数）：JSONC 容错解析 → hidden 过滤 →
//!         │  同名去重（stable 优先）→ defaultProfile 置顶
//!         ▼
//! app 层注入闭包：Fn() -> Vec<TerminalProfile>   ← 每次查询重读文件
//!         ▼
//! TerminalProvider::query(ctx)
//!         │  name / commandline 各建一份 SearchKeys（与 windows provider 同款）
//!         ▼
//! nucleo 打分 → Vec<Match>（Action::Launch { path: wt.exe, args: ["-p", name] }）
//! ```
//!
//! ## 分层
//!
//! - **本文件全是纯逻辑**（JSONC 预处理、解析、过滤、合并、provider），
//!   单测用 fixture 字符串覆盖，不碰文件系统、不碰 Windows API。
//! - Windows 侧的路径解析（`LOCALAPPDATA`、Packages 目录）与读文件在
//!   `voke-sys/src/terminal.rs`；sys 只负责"把文件内容读出来"，
//!   合并/去重的语义全部在这里（可测）。
//!
//! ## 为什么每次 query 都重读 settings.json
//!
//! 与 windows provider 同一条理由：profile 会被用户在 WT 设置里增删改，
//! 缓存即过期。文件很小（几十 KB），读盘在 background executor 上做，不阻塞 UI。
//!
//! ## 启动动作
//!
//! `wt.exe -p "<name>"`：wt.exe 是 App Execution Alias，路径由 app 层注入
//! （生产是 `%LOCALAPPDATA%\Microsoft\WindowsApps\wt.exe`）。
//! core 不假设路径怎么来，只把它拼进 [`Action::Launch`]。

use std::sync::Arc;

use nucleo_matcher::{Config, Matcher};

use crate::action::Action;
use crate::matcher::score_entry;
use crate::provider::{BoxFuture, Match, Provider, ProviderConfig, QueryCtx, ready};
use crate::search_key::{SearchKeys, SearchKeysCache};

/// 一条 Windows Terminal profile（合并去重后的形态）。
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct TerminalProfile {
    /// 显示名（`settings.json` 的 `name`，必需；缺失的条目在解析期就被丢弃）。
    pub name: String,
    /// 启动命令行（副标题 + 搜索键的一部分；WT 允许缺省，缺省时为空串）。
    pub commandline: String,
    /// profile 的 guid（`defaultProfile` 匹配用；动态生成的 profile 也有）。
    pub guid: String,
    /// 是否命中所在文件的顶层 `defaultProfile`。
    pub is_default: bool,
}

/// profile 数据源：每次调用都应当返回**当前**的合并后列表。
///
/// 与 `WindowsProvider` 的 `WindowFetcher` 同款：`Arc<dyn Fn>` 让 provider 能塞进
/// `Vec<Box<dyn Provider>>`，闭包必须 `Send + Sync`。
pub type ProfileFetcher = Arc<dyn Fn() -> Vec<TerminalProfile> + Send + Sync>;

/// 剥掉 JSONC 注释（`//` 行注释与 `/* */` 块注释），字符串字面量内的内容原样保留。
///
/// WT 的 settings.json 允许注释（JSONC）。 serde_json 不认注释，所以解析失败时
/// 用本函数预处理后再试一次。
///
/// 注释被替换成**等长空白**（换行保留）：这样即使解析仍然失败，serde_json 报的
/// 行列号仍然对应原文，日志可定位。
pub fn strip_jsonc_comments(input: &str) -> String {
    let mut out = String::with_capacity(input.len());
    let mut chars = input.chars().peekable();
    let mut in_string = false;
    while let Some(c) = chars.next() {
        if in_string {
            out.push(c);
            match c {
                '\\' => {
                    // 转义字符的下一个字符原样输出（\" 不会结束字符串）。
                    if let Some(next) = chars.next() {
                        out.push(next);
                    }
                }
                '"' => in_string = false,
                _ => {}
            }
            continue;
        }
        match c {
            '"' => {
                in_string = true;
                out.push(c);
            }
            '/' if chars.peek() == Some(&'/') => {
                // 行注释：吃到换行为止（换行本身保留）。
                out.push(' ');
                while let Some(&n) = chars.peek() {
                    if n == '\n' {
                        break;
                    }
                    chars.next();
                    out.push(' ');
                }
            }
            '/' if chars.peek() == Some(&'*') => {
                // 块注释：吃到 */ 为止，换行保留以维持行号。
                out.push(' ');
                chars.next(); // 吃掉 '*'
                out.push(' ');
                let mut prev = '\0';
                for n in chars.by_ref() {
                    if prev == '*' && n == '/' {
                        out.push(' ');
                        break;
                    }
                    out.push(if n == '\n' { '\n' } else { ' ' });
                    prev = n;
                }
            }
            _ => out.push(c),
        }
    }
    out
}

/// 从一份 settings.json 原文解析 profile 列表（未合并、未置顶）。
///
/// 容错策略（搜索路径上**绝不 panic**）：
/// 1. 直接 `serde_json` 解析；失败则 [`strip_jsonc_comments`] 后重试；
/// 2. 仍失败（文件截断/编码损坏等）→ 返回空列表并记 warn；
/// 3. 缺 `profiles` / `profiles.list` 字段 → 空列表（新装 WT 还没写过设置的形态）。
fn parse_settings(raw: &str) -> Vec<TerminalProfile> {
    let value: serde_json::Value = match serde_json::from_str(raw) {
        Ok(v) => v,
        Err(_) => match serde_json::from_str(&strip_jsonc_comments(raw)) {
            Ok(v) => v,
            Err(err) => {
                log::warn!("WT settings.json 解析失败（去注释后仍失败），跳过该文件: {err}");
                return Vec::new();
            }
        },
    };

    let default_guid = value.get("defaultProfile").and_then(|v| v.as_str());

    // 现行格式 profiles 是对象（{ defaults, list }）；远古版本曾直接是数组，顺手兼容。
    let profiles = value.get("profiles");
    let list = profiles
        .and_then(|p| p.get("list"))
        .and_then(|l| l.as_array())
        .or_else(|| profiles.and_then(|p| p.as_array()));

    let Some(list) = list else {
        return Vec::new();
    };

    list.iter()
        .filter_map(|entry| {
            // name 是必需的（显示与 `wt -p` 都靠它）；hidden=true 的不展示。
            let name = entry.get("name").and_then(|v| v.as_str())?.trim();
            if name.is_empty() {
                return None;
            }
            if entry.get("hidden").and_then(|v| v.as_bool()) == Some(true) {
                return None;
            }
            let guid = entry
                .get("guid")
                .and_then(|v| v.as_str())
                .unwrap_or_default()
                .to_string();
            let commandline = entry
                .get("commandline")
                .and_then(|v| v.as_str())
                .unwrap_or_default()
                .to_string();
            let is_default = !guid.is_empty() && Some(guid.as_str()) == default_guid;
            Some(TerminalProfile {
                name: name.to_string(),
                commandline,
                guid,
                is_default,
            })
        })
        .collect()
}

/// 把若干份 settings.json 原文（**stable 在前、Preview 在后**）合并成一份列表。
///
/// - 同名（`name` 完全相同）去重，**先出现的文件优先**（stable > Preview）；
/// - 命中各自文件 `defaultProfile` 的条目提到最前（多份文件各有 default 时，
///   stable 的 default 更靠前——它先被解析）。
///
/// 这是"给定 settings.json 内容列表 → 合并去重"的纯函数出口，
/// 路径解析与读文件在 voke-sys。
pub fn profiles_from_settings(contents: &[String]) -> Vec<TerminalProfile> {
    let mut merged: Vec<TerminalProfile> = Vec::new();
    for raw in contents {
        for profile in parse_settings(raw) {
            if merged.iter().any(|p| p.name == profile.name) {
                continue;
            }
            merged.push(profile);
        }
    }
    // default 置顶（stable 排序，非 default 的相对顺序不变）。
    merged.sort_by_key(|p| !p.is_default);
    merged
}

/// 带搜索键的 profile（条目每次查询重建，搜索键走 [`SearchKeysCache`]
/// ——与 windows provider 同款）。
struct IndexedProfile {
    entry: TerminalProfile,
    name_keys: Arc<SearchKeys>,
    command_keys: Arc<SearchKeys>,
}

/// terminal provider。
pub struct TerminalProvider {
    config: ProviderConfig,
    /// 数据源。provider 不假设它在哪个线程跑（调用方保证后台线程）。
    fetch: ProfileFetcher,
    /// wt.exe 的路径（App Execution Alias；app 层注入，见模块文档）。
    wt_path: String,
    /// 搜索键记忆化（容量有上限，见类型文档）。
    keys: SearchKeysCache,
}

impl TerminalProvider {
    /// provider 的稳定 id。
    pub const ID: &'static str = "terminal";

    /// 单次非空查询返回的最大条数（理由同 apps/windows：列表只渲染前几十条）。
    pub const MAX_RESULTS: usize = 50;

    /// 用数据源 + wt.exe 路径构造。
    pub fn new(fetch: ProfileFetcher, wt_path: impl Into<String>) -> Self {
        Self {
            config: ProviderConfig {
                enabled: true,
                // 直接全局搜（对齐 PTR 的 Terminal 插件：无前缀，直接打 profile 名）。
                is_global: true,
                // 与 windows 同级，低于 apps 的 1.2（应用优先）。
                weight: 1.0,
                prefix: None,
                activation: crate::config::Activation::Direct,
                count: 5,
            },
            fetch,
            wt_path: wt_path.into(),
            keys: SearchKeysCache::new(),
        }
    }

    /// 把一条 profile 变成 [`Match`]：动作在 query 阶段确定为
    /// `wt.exe -p "<name>"`（§3 的 Launch 变体，复用 apps 的启动路径）。
    fn to_match(&self, profile: &IndexedProfile, score: f32) -> Match {
        let mut m = Match::execute(
            Self::ID,
            profile.entry.name.clone(),
            profile.entry.commandline.clone(),
            Action::launch_with(
                self.wt_path.clone(),
                vec!["-p".to_string(), profile.entry.name.clone()],
            ),
            score,
        );
        if profile.entry.commandline.is_empty() {
            m.subtitle = "Windows Terminal 配置".to_string();
        }
        if profile.entry.is_default {
            m.subtitle = format!("默认配置 · {}", m.subtitle);
        }
        m
    }

    fn index(&self, entry: TerminalProfile) -> IndexedProfile {
        let name_keys = self.keys.get(&entry.name);
        let command_keys = self.keys.get(&entry.commandline);
        IndexedProfile {
            name_keys,
            command_keys,
            entry,
        }
    }

    /// 对一条 profile 打分：name 与 commandline 取最高分（与 windows provider 同款理由：
    /// 分开建键避免拼音跨界污染）。
    fn score(&self, matcher: &mut Matcher, query: &str, profile: &IndexedProfile) -> Option<f32> {
        let name = score_entry(matcher, query, &profile.name_keys).map(|s| s.score);
        let command = score_entry(matcher, query, &profile.command_keys).map(|s| s.score);
        match (name, command) {
            (Some(a), Some(b)) => Some(a.max(b)),
            (Some(a), None) => Some(a),
            (None, Some(b)) => Some(b),
            (None, None) => None,
        }
    }
}

impl Provider for TerminalProvider {
    fn id(&self) -> &'static str {
        Self::ID
    }

    fn name(&self) -> &'static str {
        // 与 apps 里的「终端」应用条目（MSIX 显示名）区分开。
        "终端配置"
    }

    /// 别名：`wt` = wt.exe 的命令名；`terminal` = 英文全名。
    ///
    /// 冲突检查（别名是**子串**匹配，见 `QueryEngine::plugin_picker_matches`）：
    /// `wt`/`terminal` 都不含其它插件的过滤词片段（`b`/`sj`/`time`/`rand`/`calc`），
    /// 反向也只有单字母 `t`/`r`/`w` 这类本来就多命中的一字母过滤会顺带命中，
    /// 不造成"永远抢不到"的抢占。
    fn aliases(&self) -> &'static [&'static str] {
        &["wt", "terminal"]
    }

    fn description(&self) -> &'static str {
        "用 Windows Terminal 打开某个配置（profile）"
    }

    fn config(&self) -> &ProviderConfig {
        &self.config
    }

    fn set_config(&mut self, config: ProviderConfig) {
        self.config = config;
    }

    fn placeholder(&self, sub_mode: Option<&str>) -> &'static str {
        let _ = sub_mode;
        "输入配置名过滤，Enter 用 wt 启动"
    }

    /// 查询实现。
    ///
    /// **每次调用都重新读数据源**（`(self.fetch)()`，生产是重读 settings.json）：
    /// profile 会被用户在 WT 设置里增删改，缓存即过期。读盘 + 解析在后台线程做
    /// （app 层的 `background_spawn`）。
    ///
    /// 空 query 语义与 apps/windows 一致：返回全部、score = 1.0，
    /// 由 `QueryEngine::rank` 的频率加成排序；顺序保留数据源的
    /// default 置顶 + stable 优先。
    fn query(&self, ctx: &QueryCtx) -> BoxFuture<'static, Vec<Match>> {
        let profiles: Vec<IndexedProfile> =
            (self.fetch)().into_iter().map(|e| self.index(e)).collect();

        let query = ctx.query.trim();
        if query.is_empty() {
            let matches = profiles.iter().map(|p| self.to_match(p, 1.0)).collect();
            return ready(matches);
        }

        let mut matcher = Matcher::new(Config::DEFAULT);
        let mut matches: Vec<Match> = profiles
            .iter()
            .filter_map(|p| {
                self.score(&mut matcher, query, p)
                    .map(|s| self.to_match(p, s))
            })
            .collect();
        matches.sort_by(|a, b| {
            b.score
                .partial_cmp(&a.score)
                .unwrap_or(std::cmp::Ordering::Equal)
        });
        matches.truncate(Self::MAX_RESULTS);
        ready(matches)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::future::Future;

    // ── fixtures ──

    const STABLE: &str = r#"{
        "defaultProfile": "{61c54bbd-c2c6-5271-96e7-009a87ff44bf}",
        "profiles": {
            "defaults": { "font": { "face": "Cascadia Mono" } },
            "list": [
                {
                    "guid": "{61c54bbd-c2c6-5271-96e7-009a87ff44bf}",
                    "name": "Windows PowerShell",
                    "commandline": "powershell.exe"
                },
                {
                    "guid": "{0caa0dad-35be-5f56-a8ff-afceeeaa6101}",
                    "name": "命令提示符",
                    "commandline": "cmd.exe"
                },
                {
                    "guid": "{b453ae62-4e3d-5e58-b989-0a998ec441b8}",
                    "name": "Azure Cloud Shell",
                    "hidden": true,
                    "source": "Windows.Terminal.Azure"
                },
                {
                    "guid": "{2c4de342-38b7-51cf-b940-2309a097f518}",
                    "name": "Ubuntu",
                    "source": "Windows.Terminal.Wsl"
                },
                {
                    "guid": "{novice}",
                    "commandline": "noname.exe"
                }
            ]
        }
    }"#;

    const PREVIEW: &str = r#"{
        "profiles": {
            "list": [
                {
                    "guid": "{aaaa}",
                    "name": "Ubuntu",
                    "commandline": "wsl.exe -d Ubuntu"
                },
                {
                    "guid": "{bbbb}",
                    "name": "PowerShell Preview",
                    "commandline": "pwsh.exe"
                }
            ]
        }
    }"#;

    fn block_on<F: Future>(f: F) -> F::Output {
        let mut f = Box::pin(f);
        let mut cx = std::task::Context::from_waker(std::task::Waker::noop());
        match f.as_mut().poll(&mut cx) {
            std::task::Poll::Ready(v) => v,
            std::task::Poll::Pending => panic!("terminal provider 的 future 应当立即就绪"),
        }
    }

    fn provider_with(entries: Vec<TerminalProfile>) -> TerminalProvider {
        TerminalProvider::new(Arc::new(move || entries.clone()), r"C:\wt\wt.exe")
    }

    fn merged_provider() -> TerminalProvider {
        let stable = STABLE.to_string();
        let preview = PREVIEW.to_string();
        provider_with(profiles_from_settings(&[stable, preview]))
    }

    fn query(provider: &TerminalProvider, q: &str) -> Vec<Match> {
        let ctx = QueryCtx::new(q.to_string(), q.to_string());
        block_on(provider.query(&ctx))
    }

    fn titles(matches: &[Match]) -> Vec<String> {
        matches.iter().map(|m| m.title.clone()).collect()
    }

    // ── JSONC 预处理 ──

    #[test]
    fn strip_line_and_block_comments() {
        let input = "{\n // comment\n \"a\": 1, /* block\n multi */ \"b\": 2 }";
        let stripped = strip_jsonc_comments(input);
        assert!(!stripped.contains("comment"));
        assert!(!stripped.contains("block"));
        // 换行保留（行号不漂）。
        assert_eq!(stripped.matches('\n').count(), input.matches('\n').count());
        let v: serde_json::Value = serde_json::from_str(&stripped).unwrap();
        assert_eq!(v["a"], 1);
        assert_eq!(v["b"], 2);
    }

    #[test]
    fn strip_preserves_urls_and_escapes_in_strings() {
        let input = r#"{ "url": "https://example.com//a", "q": "a\"//b" }"#;
        let stripped = strip_jsonc_comments(input);
        let v: serde_json::Value = serde_json::from_str(&stripped).unwrap();
        assert_eq!(v["url"], "https://example.com//a");
        assert_eq!(v["q"], "a\"//b");
    }

    // ── 解析 / 过滤 / 合并 ──

    #[test]
    fn parses_normal_profiles() {
        let profiles = profiles_from_settings(&[STABLE.to_string()]);
        let names: Vec<&str> = profiles.iter().map(|p| p.name.as_str()).collect();
        assert!(names.contains(&"Windows PowerShell"));
        assert!(names.contains(&"命令提示符"));
        assert!(names.contains(&"Ubuntu"));
    }

    #[test]
    fn hidden_and_nameless_entries_are_filtered() {
        let profiles = profiles_from_settings(&[STABLE.to_string()]);
        let names: Vec<&str> = profiles.iter().map(|p| p.name.as_str()).collect();
        assert!(!names.contains(&"Azure Cloud Shell"), "hidden 的必须过滤");
        // 无 name 的条目被跳过，不会变成空名行。
        assert!(profiles.iter().all(|p| !p.name.is_empty()));
        assert_eq!(profiles.len(), 3);
    }

    #[test]
    fn missing_profiles_field_gives_empty_list() {
        assert!(profiles_from_settings(&["{}".to_string()]).is_empty());
        assert!(profiles_from_settings(&[r#"{"defaultProfile": "{x}"}"#.to_string()]).is_empty());
    }

    #[test]
    fn broken_json_does_not_panic() {
        assert!(profiles_from_settings(&["{ not json".to_string()]).is_empty());
        assert!(profiles_from_settings(&[String::new()]).is_empty());
        assert!(profiles_from_settings(&[]).is_empty());
    }

    #[test]
    fn jsonc_with_comments_parses_via_fallback() {
        let jsonc = r#"{
            // 默认用 PowerShell
            "profiles": {
                "list": [
                    /* 块注释 */
                    { "name": "pwsh", "commandline": "pwsh.exe" }
                ]
            }
        }"#;
        let profiles = profiles_from_settings(&[jsonc.to_string()]);
        assert_eq!(profiles.len(), 1);
        assert_eq!(profiles[0].name, "pwsh");
    }

    #[test]
    fn merge_dedupes_by_name_stable_first() {
        let merged = profiles_from_settings(&[STABLE.to_string(), PREVIEW.to_string()]);
        let names: Vec<&str> = merged.iter().map(|p| p.name.as_str()).collect();
        // stable 3 条 + preview 独有的 1 条（Ubuntu 同名去重）。
        assert_eq!(names.len(), 4, "{names:?}");
        assert_eq!(
            names.iter().filter(|n| **n == "Ubuntu").count(),
            1,
            "同名必须去重"
        );
        // stable 的 Ubuntu 没有 commandline（fixture 里 stable 那条没写），
        // preview 那条有——去重保留 stable 的。
        let ubuntu = merged.iter().find(|p| p.name == "Ubuntu").unwrap();
        assert_eq!(ubuntu.commandline, "", "同名去重必须 stable 优先");
        assert!(names.contains(&"PowerShell Preview"));
    }

    #[test]
    fn default_profile_is_pinned_first() {
        let merged = profiles_from_settings(&[STABLE.to_string(), PREVIEW.to_string()]);
        assert_eq!(
            merged[0].name, "Windows PowerShell",
            "defaultProfile 必须置顶"
        );
        assert!(merged[0].is_default);
        assert_eq!(merged.iter().filter(|p| p.is_default).count(), 1);
        // 非 default 的相对顺序不变（stable 排序）。
        let rest: Vec<&str> = merged[1..].iter().map(|p| p.name.as_str()).collect();
        assert_eq!(rest, vec!["命令提示符", "Ubuntu", "PowerShell Preview"]);
    }

    #[test]
    fn preview_only_also_works() {
        let merged = profiles_from_settings(&[PREVIEW.to_string()]);
        assert_eq!(merged.len(), 2);
        assert!(merged.iter().all(|p| !p.is_default));
    }

    // ── provider ──

    #[test]
    fn id_and_config() {
        let p = merged_provider();
        assert_eq!(p.id(), "terminal");
        assert_eq!(p.name(), "终端配置");
        assert_eq!(p.aliases(), &["wt", "terminal"]);
        assert!(p.config().enabled && p.config().is_global);
        assert_eq!(p.config().weight, 1.0);
        assert_eq!(p.config().prefix, None, "无前缀，全局直接搜");
    }

    #[test]
    fn empty_query_lists_all_in_merge_order() {
        let p = merged_provider();
        let hits = query(&p, "");
        assert_eq!(
            titles(&hits),
            vec![
                "Windows PowerShell",
                "命令提示符",
                "Ubuntu",
                "PowerShell Preview"
            ]
        );
        assert!(hits.iter().all(|m| m.score == 1.0));
    }

    #[test]
    fn action_is_wt_launch_with_profile_name() {
        let p = merged_provider();
        let hits = query(&p, "ubuntu");
        assert_eq!(hits.len(), 1);
        assert_eq!(
            hits[0].action,
            Action::Launch {
                path: r"C:\wt\wt.exe".to_string(),
                args: vec!["-p".to_string(), "Ubuntu".to_string()],
                elevated: false,
                // 终端 profile 启动的是用户要的终端窗口，**不能**隐藏。
                no_console: false,
            }
        );
        assert_eq!(hits[0].interact, crate::Interact::Execute);
    }

    #[test]
    fn matches_by_name_and_pinyin() {
        let p = merged_provider();
        for q in ["powershell", "PowerShell"] {
            let hits = query(&p, q);
            assert!(
                titles(&hits).contains(&"Windows PowerShell".to_string()),
                "{q}: {:?}",
                titles(&hits)
            );
        }
        // 中文名 + 拼音首字母（SearchKeys 的既有能力）。
        let hits = query(&p, "mlts");
        assert!(
            titles(&hits).contains(&"命令提示符".to_string()),
            "{:?}",
            titles(&hits)
        );
    }

    #[test]
    fn matches_by_commandline() {
        let p = merged_provider();
        let hits = query(&p, "cmd.exe");
        assert_eq!(titles(&hits), vec!["命令提示符"]);
    }

    #[test]
    fn subtitle_shows_commandline_and_default_marker() {
        let p = merged_provider();
        let hits = query(&p, "");
        let ps = hits
            .iter()
            .find(|m| m.title == "Windows PowerShell")
            .unwrap();
        assert!(ps.subtitle.contains("powershell.exe"));
        assert!(ps.subtitle.contains("默认配置"), "{}", ps.subtitle);
        let ubuntu = hits.iter().find(|m| m.title == "Ubuntu").unwrap();
        assert!(
            !ubuntu.subtitle.is_empty(),
            "无 commandline 时副标题要有兜底"
        );
    }

    #[test]
    fn unrelated_query_returns_nothing() {
        let p = merged_provider();
        assert!(query(&p, "zzzqqqxxx").is_empty());
    }

    #[test]
    fn empty_provider_is_usable() {
        let p = provider_with(Vec::new());
        assert!(query(&p, "x").is_empty());
        assert!(query(&p, "").is_empty());
    }

    /// 每次 query 都必须重新调闭包（settings.json 会被用户改，不能缓存）。
    #[test]
    fn fetcher_is_called_on_every_query() {
        use std::sync::atomic::{AtomicUsize, Ordering};
        let calls = Arc::new(AtomicUsize::new(0));
        let calls_in = calls.clone();
        let p = TerminalProvider::new(
            Arc::new(move || {
                calls_in.fetch_add(1, Ordering::SeqCst);
                Vec::new()
            }),
            "wt.exe",
        );
        query(&p, "a");
        query(&p, "b");
        assert_eq!(calls.load(Ordering::SeqCst), 2);
    }

    #[test]
    fn results_are_sorted_descending_and_capped() {
        let entries: Vec<TerminalProfile> = (0..80)
            .map(|i| TerminalProfile {
                name: format!("profile{i:02}"),
                commandline: "cmd.exe".into(),
                guid: String::new(),
                is_default: false,
            })
            .collect();
        let p = provider_with(entries);
        let hits = query(&p, "profile");
        assert_eq!(hits.len(), TerminalProvider::MAX_RESULTS);
        let scores: Vec<f32> = hits.iter().map(|m| m.score).collect();
        for pair in scores.windows(2) {
            assert!(pair[0] >= pair[1], "必须降序: {scores:?}");
        }
    }
}
