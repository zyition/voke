//! base64 provider：base64 编码 / 解码（§7 的 M4 插件，对齐 PTR 的
//! "Value Generator / Base64" 类插件）。
//!
//! ## 触发方式（方案 A：前缀 + 子模式）
//!
//! 方案 A 去掉了 M5 的子命令面板——**没有"先输 `#` 再选插件"这一步**。
//! base64 用专属前缀 [`PREFIX_BASE64`]（`#b`）触发：打 `#b` 就进来了。
//!
//! 进来之后它**自己列出两个方向**（`EnterSubquery` 条目，
//! Enter / 鼠标点击都能进）：
//!
//! ```text
//! 输入 `#b` → 第一级：列表里两条
//!              · 加密（base64 编码）      sub_mode = "enc"
//!              · 解密（base64 解码）      sub_mode = "dec"
//!           → Enter / 点击任一条 → 进入该子模式
//! 进入后 → 输入框占位符变成 "输入要加密的内容" / "输入要解密的 base64"
//!           → 打字实时出结果（结果行 = Execute，Enter / 点击都复制）
//! ```
//!
//! **为什么用子模式而不是 M5 的 `enc:` / `dec:` 文本前缀**：`enc:` 要求用户
//! 记住并正确拼写一个命令词，拼错就只能得到"编码"——用户想解码却得到一串更长的
//! base64，正是任务里说的"反人类"。子模式把这两个方向变成**可见的两条候选**，
//! 用户点一下就进，不需要记任何命令。
//!
//! `is_global = false`：base64 不做任何启发式自动判断（决策：猜错比多打几个字
//! 更烦人）。想用就必须打前缀或从插件列表里选。
//!
//! ## 子模式下的输入语义
//!
//! | 子模式 | 输入 | 行为 |
//! |---|---|---|
//! | `enc` | 任意非空文本 | 编码；结果行可复制 |
//! | `enc` | 空 | 无结果行（用法见占位符） |
//! | `dec` | 合法 base64 且解出合法 UTF-8 | 解码；结果行可复制 |
//! | `dec` | 解码失败 | 一条提示（Hint，告诉用户哪里不对） |
//! | `dec` | 空 | 无结果行（用法见占位符） |
//!
//! **未选子模式时**（刚打 `#b`）只列两条 `EnterSubquery`，不猜方向、不出结果。
//!
//! ## 为什么手写编解码
//!
//! §1 的"core 零新依赖"。标准 base64 是 30 行的表驱动算法，不值得为它拉
//! `base64` crate（该 crate 还带 no_std/engine 抽象，编译开销远超收益）。
//!
//! ## 历史说明
//!
//! 早期版本有一条"严格合法 base64 + 解出合法 UTF-8 就自动解码"的启发式。
//! 它在 M5 被删除（决策：不做启发式自动判断），现在 [`decode_strict`] 只作为
//! **纯函数**保留（单测与将来的显式场景用），不参与查询路径。

use crate::action::Action;
use crate::engine::PREFIX_BASE64;
use crate::provider::{BoxFuture, Interact, Match, Provider, ProviderConfig, QueryCtx, ready};

/// 标准 base64 字母表（RFC 4648 §4，`+/` 变体）。
const ALPHABET: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";

/// 子模式标识：编码。
pub const SUB_MODE_ENC: &str = "enc";

/// 子模式标识：解码。
pub const SUB_MODE_DEC: &str = "dec";

/// base64 provider。
pub struct Base64Provider {
    config: ProviderConfig,
}

impl Base64Provider {
    /// provider 的稳定 id。
    pub const ID: &'static str = "base64";

    pub fn new() -> Self {
        Self {
            config: ProviderConfig {
                enabled: true,
                is_global: false,
                weight: 1.0,
                prefix: Some(PREFIX_BASE64.to_string()),
                // `#` 系：默认间接激活（前缀 + 空格），见 config::default_activation。
                activation: crate::config::Activation::Indirect,
                count: 5,
            },
        }
    }
}

impl Default for Base64Provider {
    fn default() -> Self {
        Self::new()
    }
}

impl Provider for Base64Provider {
    fn id(&self) -> &'static str {
        Self::ID
    }

    fn name(&self) -> &'static str {
        "Base64"
    }

    /// 别名：`b` = 单字母快捷；`b64` = 常用缩写（`base64`/`base` 由 name
    /// 子串语义覆盖，不用单列）。
    fn aliases(&self) -> &'static [&'static str] {
        &["b", "b64"]
    }

    fn description(&self) -> &'static str {
        "base64 编解码（加密 / 解密）"
    }

    fn config(&self) -> &ProviderConfig {
        &self.config
    }

    fn set_config(&mut self, config: ProviderConfig) {
        self.config = config;
    }

    fn placeholder(&self, sub_mode: Option<&str>) -> &'static str {
        match sub_mode {
            Some(SUB_MODE_ENC) => "输入要加密的内容（例：hello → aGVsbG8=）",
            Some(SUB_MODE_DEC) => "输入要解密的 base64（例：aGVsbG8= → hello）",
            // 第一级：先选方向。
            _ => "选择 加密 或 解密",
        }
    }

    fn query(&self, ctx: &QueryCtx) -> BoxFuture<'static, Vec<Match>> {
        let matches = match ctx.sub_mode.as_deref() {
            // 第一级：只列两个方向，不猜。
            None => vec![
                Match {
                    provider_id: Self::ID.to_string(),
                    title: "加密".to_string(),
                    subtitle: "把文本编码成 base64".to_string(),
                    icon: None,
                    action: Action::None,
                    score: 1.0,
                    interact: Interact::EnterSubquery(SUB_MODE_ENC.to_string()),
                },
                Match {
                    provider_id: Self::ID.to_string(),
                    title: "解密".to_string(),
                    subtitle: "把 base64 解码成文本".to_string(),
                    icon: None,
                    action: Action::None,
                    score: 0.9,
                    interact: Interact::EnterSubquery(SUB_MODE_DEC.to_string()),
                },
            ],
            // enc / dec 子模式：真出结果。
            Some(mode) => convert(mode, &ctx.query)
                .into_iter()
                .enumerate()
                .map(|(i, c)| {
                    let score = 1.0 - i as f32 * 0.02;
                    match c.copy {
                        Some(text) => Match {
                            provider_id: Self::ID.to_string(),
                            title: c.title,
                            subtitle: c.subtitle,
                            icon: None,
                            action: Action::Copy { text },
                            score,
                            interact: Interact::Execute,
                        },
                        None => {
                            // 提示行：不渲染成可选行，只在结果区下方展示。
                            let mut m = Match::hint(Self::ID, c.title, c.subtitle);
                            m.score = score;
                            m
                        }
                    }
                })
                .collect(),
        };
        ready(matches)
    }
}

/// 一次转换的结果。
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Conversion {
    /// 主标题：转换结果（可复制行）或提示文案（提示行）。
    pub title: String,
    /// 副标题：说明这次是编码还是解码。
    pub subtitle: String,
    /// 选中后复制的文本；纯提示行为 `None`（动作 → [`Action::None`]）。
    pub copy: Option<String>,
}

impl Conversion {
    fn result(title: String, subtitle: String) -> Self {
        Self {
            copy: Some(title.clone()),
            title,
            subtitle,
        }
    }

    fn hint(title: impl Into<String>, subtitle: impl Into<String>) -> Self {
        Self {
            title: title.into(),
            subtitle: subtitle.into(),
            copy: None,
        }
    }
}

/// 在**某个子模式**下做一次转换（可返回多行：结果行 + 提示行）。
///
/// `mode` 是 [`SUB_MODE_ENC`] / [`SUB_MODE_DEC`]；未知模式（理论上不会出现，
/// 因为子模式标识由本 provider 自己发出）按编码处理，不 panic。
pub fn convert(mode: &str, query: &str) -> Vec<Conversion> {
    let query = query.trim();

    if mode == SUB_MODE_DEC {
        if query.is_empty() {
            // 用法已挪进占位符；空输入不再给提示行。
            return vec![];
        }
        return match decode(query).and_then(|bytes| String::from_utf8(bytes).ok()) {
            Some(text) => vec![Conversion::result(
                text.clone(),
                format!("base64 解码（{} 字节）", text.len()),
            )],
            // 解码失败给提示而不是空列表：用户明确说了"解密"，静默无结果会像卡住。
            None => vec![Conversion::hint(
                "解码失败",
                "不是合法的 base64，或解出的字节不是 UTF-8 文本",
            )],
        };
    }

    // 加密分支（含未知 mode 的兜底）。
    if query.is_empty() {
        // 用法已挪进占位符；空输入不再给提示行。
        return vec![];
    }
    vec![Conversion::result(
        encode(query.as_bytes()),
        format!("base64 编码（{} 字节）", query.len()),
    )]
}

/// 标准 base64 编码（带 `=` 填充）。
pub fn encode(input: &[u8]) -> String {
    let mut out = String::with_capacity(input.len().div_ceil(3) * 4);
    for chunk in input.chunks(3) {
        let b0 = chunk[0] as u32;
        let b1 = chunk.get(1).copied().unwrap_or(0) as u32;
        let b2 = chunk.get(2).copied().unwrap_or(0) as u32;
        let triple = (b0 << 16) | (b1 << 8) | b2;

        out.push(ALPHABET[((triple >> 18) & 0x3F) as usize] as char);
        out.push(ALPHABET[((triple >> 12) & 0x3F) as usize] as char);
        if chunk.len() > 1 {
            out.push(ALPHABET[((triple >> 6) & 0x3F) as usize] as char);
        } else {
            out.push('=');
        }
        if chunk.len() > 2 {
            out.push(ALPHABET[(triple & 0x3F) as usize] as char);
        } else {
            out.push('=');
        }
    }
    out
}

/// 标准 base64 解码。字符集/填充非法时返回 `None`。
pub fn decode(input: &str) -> Option<Vec<u8>> {
    let bytes: Vec<u8> = input.bytes().filter(|b| !b.is_ascii_whitespace()).collect();
    if !bytes.len().is_multiple_of(4) {
        return None;
    }
    let mut out = Vec::with_capacity(bytes.len() / 4 * 3);
    for chunk in bytes.chunks(4) {
        let mut values = [0u32; 4];
        let mut padding = 0usize;
        for (i, &byte) in chunk.iter().enumerate() {
            if byte == b'=' {
                // 填充只允许出现在最后，且最多两个。
                if i < 2 {
                    return None;
                }
                padding += 1;
                values[i] = 0;
            } else {
                // 填充开始后不允许再出现数据字符。
                if padding > 0 {
                    return None;
                }
                values[i] = index_of(byte)? as u32;
            }
        }
        if padding > 2 {
            return None;
        }
        let triple = (values[0] << 18) | (values[1] << 12) | (values[2] << 6) | values[3];
        out.push((triple >> 16) as u8);
        if padding < 2 {
            out.push((triple >> 8) as u8);
        }
        if padding < 1 {
            out.push(triple as u8);
        }
    }
    Some(out)
}

/// 严格版本：长度是 4 的倍数、`=` 只出现在末尾的 base64 解码。
///
/// **M5 起它不参与查询路径**（决策：base64 不做启发式自动判断）。
/// 保留为纯函数，供显式场景与单测使用；查询只用 [`decode`]。
///
/// 用途举例：想知道"这段输入是不是一段合法 base64"时（将来做预览提示、
/// 或其它插件需要这个判据时直接用，不要在别处重写一份）。
///
/// 与 [`decode`] 的差别：额外要求长度至少一个完整块、`=`（若出现）只能集中在
/// 末尾。目的是尽量不把普通文本误判成 base64——`hello`（长度 5）会被挡下。
pub fn decode_strict(input: &str) -> Option<Vec<u8>> {
    let trimmed = input.trim();
    // 长度不足一个完整块，或不是 4 的倍数，直接否掉（走编码分支）。
    if trimmed.len() < 4 || !trimmed.len().is_multiple_of(4) {
        return None;
    }
    // `=` 只能出现在末尾（`decode` 也会查，这里提前挡掉"中间夹 = 的文本"）。
    if let Some(first_pad) = trimmed.find('=')
        && !trimmed[first_pad..].bytes().all(|b| b == b'=')
    {
        return None;
    }
    decode(trimmed)
}

/// 单字符 → 6 位值。
fn index_of(byte: u8) -> Option<u8> {
    match byte {
        b'A'..=b'Z' => Some(byte - b'A'),
        b'a'..=b'z' => Some(byte - b'a' + 26),
        b'0'..=b'9' => Some(byte - b'0' + 52),
        b'+' => Some(62),
        b'/' => Some(63),
        _ => None,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::future::Future;

    fn block_on<F: Future>(f: F) -> F::Output {
        let mut f = Box::pin(f);
        let mut cx = std::task::Context::from_waker(std::task::Waker::noop());
        match f.as_mut().poll(&mut cx) {
            std::task::Poll::Ready(v) => v,
            std::task::Poll::Pending => panic!("base64 的 future 应当立即就绪"),
        }
    }

    /// 模拟引擎剥掉 `#b` 前缀、并带上子模式后的调用。
    fn b64_mode(mode: Option<&str>, q: &str) -> Vec<Match> {
        let p = Base64Provider::new();
        let ctx = QueryCtx::with_sub_mode(format!("#b{q}"), q.to_string(), mode);
        block_on(p.query(&ctx))
    }

    fn titles(matches: &[Match]) -> Vec<String> {
        matches.iter().map(|m| m.title.clone()).collect()
    }

    #[test]
    fn is_prefix_only_and_not_global() {
        let p = Base64Provider::new();
        assert_eq!(p.id(), "base64");
        assert_eq!(p.name(), "Base64");
        assert!(!p.description().is_empty());
        assert_eq!(p.config().prefix, Some("#b".to_string()));
        // 决策：base64 强制不参与全局搜索（必须显式触发）。
        assert!(!p.config().is_global, "base64 不得进全局结果");
    }

    /// 第一级只列两个方向（`EnterSubquery`），不猜、不出结果。
    #[test]
    fn first_level_lists_the_two_directions() {
        let hits = b64_mode(None, "");
        assert_eq!(titles(&hits), vec!["加密", "解密"]);
        assert_eq!(
            hits[0].interact,
            Interact::EnterSubquery(SUB_MODE_ENC.to_string())
        );
        assert_eq!(
            hits[1].interact,
            Interact::EnterSubquery(SUB_MODE_DEC.to_string())
        );
        // 子模式条目自身不产生副作用（进入动作由 app 层处理）。
        assert!(hits.iter().all(|m| m.action == Action::None));
        assert!(hits.iter().all(|m| !m.interact.is_hint()));
        // 第一级**绝不**因为输入内容而直接出结果（方向必须由用户显式选择）。
        let hits = b64_mode(None, "hello");
        assert_eq!(titles(&hits), vec!["加密", "解密"]);
    }

    /// 占位符随子模式变（方案 A 的可见性要求）。
    #[test]
    fn placeholder_reflects_sub_mode() {
        let p = Base64Provider::new();
        assert_eq!(p.placeholder(None), "选择 加密 或 解密");
        assert_eq!(
            p.placeholder(Some(SUB_MODE_ENC)),
            "输入要加密的内容（例：hello → aGVsbG8=）"
        );
        assert_eq!(
            p.placeholder(Some(SUB_MODE_DEC)),
            "输入要解密的 base64（例：aGVsbG8= → hello）"
        );
    }

    /// RFC 4648 §10 的标准测试向量。
    #[test]
    fn rfc4648_vectors_encode() {
        assert_eq!(encode(b""), "");
        assert_eq!(encode(b"f"), "Zg==");
        assert_eq!(encode(b"fo"), "Zm8=");
        assert_eq!(encode(b"foo"), "Zm9v");
        assert_eq!(encode(b"foob"), "Zm9vYg==");
        assert_eq!(encode(b"fooba"), "Zm9vYmE=");
        assert_eq!(encode(b"foobar"), "Zm9vYmFy");
    }

    #[test]
    fn rfc4648_vectors_decode() {
        assert_eq!(decode("").unwrap(), b"");
        assert_eq!(decode("Zg==").unwrap(), b"f");
        assert_eq!(decode("Zm8=").unwrap(), b"fo");
        assert_eq!(decode("Zm9v").unwrap(), b"foo");
        assert_eq!(decode("Zm9vYg==").unwrap(), b"foob");
        assert_eq!(decode("Zm9vYmE=").unwrap(), b"fooba");
        assert_eq!(decode("Zm9vYmFy").unwrap(), b"foobar");
    }

    #[test]
    fn roundtrips_all_single_bytes() {
        for byte in 0u8..=255 {
            let encoded = encode(&[byte]);
            let decoded = decode(&encoded).expect("编码产物必须可解码");
            assert_eq!(decoded, vec![byte], "byte {byte} roundtrip 失败");
        }
    }

    #[test]
    fn roundtrips_utf8_text() {
        for text in ["hello", "你好", "voke — 搜索", "a\nb\tc", "emoji 🎉 ok"] {
            assert_eq!(
                String::from_utf8(decode(&encode(text.as_bytes())).unwrap()).unwrap(),
                text
            );
        }
    }

    #[test]
    fn invalid_base64_is_rejected() {
        for bad in [
            "Zg=",
            "Zg",
            "Zg===",
            "Zm9v!",
            "=Zg=",
            "aGVs=bG8=",
            "中文",
            "Zm9v YmFy!",
        ] {
            assert!(decode(bad).is_none(), "{bad:?} 应当解码失败");
        }
        // 严格版（启发式用）额外把长度不是 4 倍数的挡掉
        assert!(decode_strict("hello").is_none());
        assert!(decode_strict("Zm9v=").is_none());
        assert!(decode_strict("aGVs=bG8=").is_none());
        assert_eq!(decode_strict("Zm9v").unwrap(), b"foo");
    }

    #[test]
    fn whitespace_is_ignored_when_decoding() {
        assert_eq!(decode("Zm9v\nYmFy").unwrap(), b"foobar");
        assert_eq!(decode(" Zm9v ").unwrap(), b"foo");
    }

    /// enc 子模式：任意非空文本都编码。
    #[test]
    fn enc_sub_mode_encodes() {
        let hits = b64_mode(Some(SUB_MODE_ENC), "hello");
        assert_eq!(hits.len(), 1);
        assert_eq!(hits[0].title, "aGVsbG8=");
        assert!(hits[0].subtitle.contains("编码"));
        assert_eq!(hits[0].interact, Interact::Execute);
        assert_eq!(
            hits[0].action,
            Action::Copy {
                text: "aGVsbG8=".to_string()
            }
        );
    }

    /// dec 子模式：合法 base64 → 解出文本。
    #[test]
    fn dec_sub_mode_decodes() {
        let hits = b64_mode(Some(SUB_MODE_DEC), "aGVsbG8=");
        assert_eq!(hits.len(), 1);
        assert_eq!(hits[0].title, "hello");
        assert!(hits[0].subtitle.contains("解码"));
        assert_eq!(hits[0].interact, Interact::Execute);
    }

    /// dec 失败时给一条**提示行**（`Hint`，不进可选列表），而不是静默空列表。
    #[test]
    fn dec_failure_yields_a_hint_row() {
        for q in ["not base64!", "////x"] {
            let hits = b64_mode(Some(SUB_MODE_DEC), q);
            assert_eq!(hits.len(), 1, "{q}");
            assert!(
                hits[0].interact.is_hint(),
                "{q} 的失败提示必须是 Hint（不可选）"
            );
            assert_eq!(hits[0].action, Action::None, "{q}");
        }
    }

    /// 空输入在两种子模式下都不再给用法提示行（用法已挪进占位符），返回空结果。
    #[test]
    fn empty_query_gives_no_hint_per_sub_mode() {
        for mode in [SUB_MODE_ENC, SUB_MODE_DEC] {
            let hits = b64_mode(Some(mode), "");
            assert_eq!(hits.len(), 0, "{mode} 空输入应无结果行");
        }
    }

    /// **决策：不做启发式**。enc 子模式下一切按编码处理，
    /// 即便输入本身是合法 base64（`Zm9v` → `Wm05dg==`，而不是 `foo`）。
    #[test]
    fn no_heuristic_enc_always_encodes() {
        let hits = b64_mode(Some(SUB_MODE_ENC), "Zm9v");
        assert_eq!(hits.len(), 1);
        assert_eq!(hits[0].title, encode(b"Zm9v"));
        assert!(hits[0].subtitle.contains("编码"));

        let hits = b64_mode(Some(SUB_MODE_ENC), "////");
        assert_eq!(hits[0].title, encode(b"////"));
    }

    /// `decode_strict` 作为纯函数保留（不再参与查询路径），行为不变。
    #[test]
    fn decode_strict_is_kept_as_a_pure_helper() {
        assert!(decode_strict("hello").is_none());
        assert!(decode_strict("Zm9v=").is_none());
        assert!(decode_strict("aGVs=bG8=").is_none());
        assert_eq!(decode_strict("Zm9v").unwrap(), b"foo");
    }

    #[test]
    fn match_title_equals_copied_text() {
        for (mode, q) in [
            (SUB_MODE_ENC, "hello"),
            (SUB_MODE_ENC, "x"),
            (SUB_MODE_DEC, "aGVsbG8="),
        ] {
            let hits = b64_mode(Some(mode), q);
            let Action::Copy { text } = &hits[0].action else {
                panic!("base64 的可复制行动作必须是 Copy：{mode}/{q}");
            };
            assert_eq!(&hits[0].title, text, "{mode}/{q}");
        }
    }

    /// 未知子模式（理论上不会出现，因为标识由本 provider 自己发出）按编码兜底，
    /// 不能 panic。
    #[test]
    fn unknown_sub_mode_falls_back_to_encoding() {
        let hits = b64_mode(Some("bogus"), "hello");
        assert_eq!(hits[0].title, "aGVsbG8=");
        assert_eq!(hits[0].interact, Interact::Execute);
    }

    #[test]
    fn convert_never_panics_on_arbitrary_input() {
        for q in [
            "=",
            "==",
            "===",
            "====",
            "=a==",
            "a===",
            "ab=c",
            "\u{0}",
            "🎉",
            "aGVsbG8",
            "aGVsbG8==",
            "AAAA",
            "////",
            "++++",
            "   ",
        ] {
            for mode in [SUB_MODE_ENC, SUB_MODE_DEC, "bogus"] {
                // 只要不 panic 即可（合法性由上面的具体断言覆盖）
                let _ = convert(mode, q);
            }
        }
    }
}
