//! calculator provider：内联算术计算器（§7 的 M4 第一个纯函数插件，
//! 对齐 PowerToys Run 的 Calculator 插件）。
//!
//! ## 触发方式
//!
//! 只在 `=` 前缀下命中（`is_global = false`）：
//!
//! ```text
//! 输入 "=1+1"  →  QueryEngine::prepare 路由到本 provider，ctx.query = "1+1"
//!              →  Match { title: "2", action: Copy { text: "2" } }
//! ```
//!
//! **为什么不进全局结果**：数字串（"1+1"、"2026"）一旦参与全局模糊匹配，
//! 会极容易误命中应用名/窗口标题（年份、版本号、文件名里的数字），把真正的
//! 应用挤下去。PTR 同样要求前缀。
//!
//! ## 解析器
//!
//! 手写的递归下降（tokenize + 递归下降解析），**不引第三方 crate**
//! （保住 §1 的"core 零新依赖、秒级测试循环"）。文法：
//!
//! ```text
//! expr    := term (('+' | '-') term)*        // 左结合
//! term    := unary (('*' | '/' | '%') unary)* // 左结合
//! unary   := ('+' | '-') unary | power
//! power   := primary ('^' unary)?            // 右结合，指数允许一元负号
//! primary := number | '(' expr ')'
//! ```
//!
//! 于是 `-2^2 = -4`（一元负号低于 `^`，与计算器惯例一致），
//! `2^3^2 = 512`（右结合），`2^-1 = 0.5`。
//!
//! ## 安全约束（搜索路径上绝不能 panic）
//!
//! - 输入长度上限 [`MAX_INPUT_LEN`]、括号嵌套深度上限 [`MAX_DEPTH`]：
//!   防止畸形/超长输入把递归下降打到栈溢出（栈溢出是 abort，panic hook 都拦不住）
//! - 结果必须是有限值：`1/0`、`0/0`、`1e400` 都不返回 match（而不是显示 `inf`/`NaN`）

use crate::action::Action;
use crate::provider::{BoxFuture, Match, Provider, ProviderConfig, QueryCtx, ready};

/// 单次输入的最大字符数。正常表达式不会超过几十字符；超限直接判非法。
pub const MAX_INPUT_LEN: usize = 512;

/// 括号/一元运算的最大嵌套深度（递归下降的栈深保护）。
pub const MAX_DEPTH: usize = 64;

/// 计算器 provider。
///
/// 无状态（配置也不可变），天然 `Send + Sync`。
pub struct CalculatorProvider {
    config: ProviderConfig,
}

impl CalculatorProvider {
    /// provider 的稳定 id（`FrequencyStore` 的键前缀、配置节的键）。
    pub const ID: &'static str = "calculator";

    pub fn new() -> Self {
        Self {
            config: ProviderConfig {
                enabled: true,
                // **不进全局结果**：数字串会污染应用/窗口搜索（见模块文档）。
                is_global: false,
                weight: 1.0,
                prefix: Some(crate::engine::PREFIX_CALCULATOR.to_string()),
                activation: crate::config::Activation::Direct,
                count: 5,
            },
        }
    }

    /// 与 `=` 前缀一起使用的空实例（`Default` 同义，便于注册表里一行写清）。
    pub fn boxed() -> Box<dyn Provider> {
        Box::new(Self::new())
    }
}

impl Default for CalculatorProvider {
    fn default() -> Self {
        Self::new()
    }
}

impl Provider for CalculatorProvider {
    fn id(&self) -> &'static str {
        Self::ID
    }

    fn name(&self) -> &'static str {
        "计算器"
    }

    /// 别名：`calc` = 英文缩写；`jsq` = 拼音首字母（计算器）；
    /// `c` = 单字母快捷（`cal`/`js` 由子串语义覆盖）。
    fn aliases(&self) -> &'static [&'static str] {
        &["calc", "jsq", "c"]
    }

    fn description(&self) -> &'static str {
        "算术表达式求值，如 1+1"
    }

    fn config(&self) -> &ProviderConfig {
        &self.config
    }

    fn set_config(&mut self, config: ProviderConfig) {
        self.config = config;
    }

    fn placeholder(&self, _sub_mode: Option<&str>) -> &'static str {
        // calculator 无子模式：激活后就是直接输表达式。
        "输入计算式，如 1+1"
    }

    /// 表达式非法（或结果非有限值）时返回**空列表**，不返回错误项。
    ///
    /// 理由：用户在打 `=1+` 的过程中每一步都会经过"非法"状态，列表里不该出现
    /// 任何东西；返回错误条目会让结果区闪个不停。
    fn query(&self, ctx: &QueryCtx) -> BoxFuture<'static, Vec<Match>> {
        let Some(value) = evaluate(&ctx.query) else {
            return ready(Vec::new());
        };
        let text = format_number(value);
        ready(vec![Match {
            provider_id: Self::ID.to_string(),
            // 主标题就是结果：用户按 Enter 拿到的就是这个字符串。
            title: text.clone(),
            // 副标题回显原表达式，便于确认算的是哪个式子。
            subtitle: ctx.query.trim().to_string(),
            icon: None,
            action: Action::Copy { text },
            // provider 内部只有一条结果，分数固定 1.0（权重参与聚合）。
            score: 1.0,
            // Enter / 点击都是复制结果。
            interact: crate::provider::Interact::Execute,
        }])
    }
}

// ---------------------------------------------------------------------------
// 求值入口
// ---------------------------------------------------------------------------

/// 求一个算术表达式的值。非法输入、非有限结果都返回 `None`。
pub fn evaluate(input: &str) -> Option<f64> {
    if input.chars().count() > MAX_INPUT_LEN {
        return None;
    }
    let tokens = tokenize(input).ok()?;
    if tokens.is_empty() {
        return None;
    }
    let mut parser = Parser {
        tokens,
        pos: 0,
        depth: 0,
    };
    let value = parser.expr().ok()?;
    // 必须吃完全部 token：`1 2`、`(1)3` 这类尾随内容一律判非法。
    if parser.pos != parser.tokens.len() {
        return None;
    }
    // inf / NaN（1/0、0/0、1e400）不是可复制的结果，判非法。
    value.is_finite().then_some(value)
}

/// 把结果格式化成"人类会复制"的文本。
///
/// 规则：
/// - 整数值省略小数点（`4.0` → `"4"`，`-0.0` → `"0"`）
/// - 有小数时先按 1e-10 量级取整，消掉 IEEE754 噪声
///   （`0.1 + 0.2` 直接打印是 `0.30000000000000004`，这里给 `0.3`）
/// - 极大/极小的非整数保持 Rust 的最短往返表示
pub fn format_number(value: f64) -> String {
    if value == 0.0 {
        // 同时兜住 -0.0：`-0.0 as i64 == 0`，但显式处理更清楚。
        return "0".to_string();
    }
    if value.fract() == 0.0 && value.abs() < 1e15 {
        return (value as i64).to_string();
    }
    if value.abs() < 1e15 {
        let rounded = (value * 1e10).round() / 1e10;
        if rounded == 0.0 {
            return "0".to_string();
        }
        // rounded 已经消掉噪声，`{}` 的最短往返表示就是干净的十进制。
        return format!("{rounded}");
    }
    format!("{value}")
}

// ---------------------------------------------------------------------------
// 词法
// ---------------------------------------------------------------------------

#[derive(Clone, Copy, Debug, PartialEq)]
enum Token {
    Num(f64),
    Plus,
    Minus,
    Star,
    Slash,
    Percent,
    Caret,
    LParen,
    RParen,
}

/// 非法输入。只在内部使用——对外统一是 `Option::None`（见 [`evaluate`]）。
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum CalcError {
    /// 不认识的字符（包括全角符号）。
    UnexpectedChar,
    /// 数字字面量畸形（`1.2.3`、`1e`）。
    MalformedNumber,
    /// 表达式在需要操作数的地方结束（`1+`、`(`）。
    UnexpectedEnd,
    /// 多余右括号 / 缺少右括号 / 尾随内容。
    Unbalanced,
    /// 嵌套太深（[`MAX_DEPTH`]）。
    TooDeep,
}

type CalcResult<T> = Result<T, CalcError>;

fn tokenize(input: &str) -> CalcResult<Vec<Token>> {
    let chars: Vec<char> = input.chars().collect();
    let mut tokens = Vec::new();
    let mut i = 0;
    while i < chars.len() {
        let c = chars[i];
        match c {
            ' ' | '\t' | '\n' | '\r' => i += 1,
            '+' => {
                tokens.push(Token::Plus);
                i += 1;
            }
            '-' => {
                tokens.push(Token::Minus);
                i += 1;
            }
            // `-`/`+` 一律作为运算符 token，正负号交给解析器的 `unary` 处理，
            // 这样 `1-2` 不会被误读成 `1` 和 `-2`。
            '*' => {
                tokens.push(Token::Star);
                i += 1;
            }
            '/' => {
                tokens.push(Token::Slash);
                i += 1;
            }
            '%' => {
                tokens.push(Token::Percent);
                i += 1;
            }
            '^' => {
                tokens.push(Token::Caret);
                i += 1;
            }
            '(' => {
                tokens.push(Token::LParen);
                i += 1;
            }
            ')' => {
                tokens.push(Token::RParen);
                i += 1;
            }
            _ if c.is_ascii_digit() || c == '.' => {
                let (token, next) = read_number(&chars, i)?;
                tokens.push(token);
                i = next;
            }
            _ => return Err(CalcError::UnexpectedChar),
        }
    }
    Ok(tokens)
}

/// 读一个无符号数字字面量（含小数与科学计数后缀），返回 `(token, 新下标)`。
///
/// 指数部分必须真的有数字才吞掉 `e`/`E`：`1e` 应报错而不是当成 `1` + 未知字符。
fn read_number(chars: &[char], start: usize) -> CalcResult<(Token, usize)> {
    let mut i = start;
    let mut seen_dot = false;
    let mut seen_digit = false;
    while i < chars.len() {
        let c = chars[i];
        if c.is_ascii_digit() {
            seen_digit = true;
            i += 1;
        } else if c == '.' && !seen_dot {
            seen_dot = true;
            i += 1;
        } else {
            break;
        }
    }
    if !seen_digit {
        // 只有一个孤立的小数点
        return Err(CalcError::MalformedNumber);
    }

    // 科学计数：消化 `e`/`E`、可选正负号、至少一位数字。
    if i < chars.len() && (chars[i] == 'e' || chars[i] == 'E') {
        let mut j = i + 1;
        if j < chars.len() && (chars[j] == '+' || chars[j] == '-') {
            j += 1;
        }
        let digits_start = j;
        while j < chars.len() && chars[j].is_ascii_digit() {
            j += 1;
        }
        if j == digits_start {
            return Err(CalcError::MalformedNumber);
        }
        i = j;
    }

    let text: String = chars[start..i].iter().collect();
    // Rust 的 f64 解析接受 "1."、".5"、".5e3" 这些形态。
    let value = text
        .parse::<f64>()
        .map_err(|_| CalcError::MalformedNumber)?;
    Ok((Token::Num(value), i))
}

// ---------------------------------------------------------------------------
// 语法（递归下降）
// ---------------------------------------------------------------------------

struct Parser {
    tokens: Vec<Token>,
    pos: usize,
    depth: usize,
}

impl Parser {
    fn peek(&self) -> Option<Token> {
        self.tokens.get(self.pos).copied()
    }

    fn advance(&mut self) -> Option<Token> {
        let token = self.peek();
        if token.is_some() {
            self.pos += 1;
        }
        token
    }

    fn expr(&mut self) -> CalcResult<f64> {
        let mut left = self.term()?;
        while let Some(token) = self.peek() {
            match token {
                Token::Plus => {
                    self.advance();
                    left += self.term()?;
                }
                Token::Minus => {
                    self.advance();
                    left -= self.term()?;
                }
                _ => break,
            }
        }
        Ok(left)
    }

    fn term(&mut self) -> CalcResult<f64> {
        let mut left = self.unary()?;
        while let Some(token) = self.peek() {
            match token {
                Token::Star => {
                    self.advance();
                    left *= self.unary()?;
                }
                Token::Slash => {
                    self.advance();
                    // 除零不做特判：结果变成 inf，由 `evaluate` 的 is_finite 拦掉。
                    left /= self.unary()?;
                }
                Token::Percent => {
                    self.advance();
                    left %= self.unary()?;
                }
                _ => break,
            }
        }
        Ok(left)
    }

    fn unary(&mut self) -> CalcResult<f64> {
        self.enter()?;
        let result = match self.peek() {
            Some(Token::Minus) => {
                self.advance();
                Ok(-self.unary()?)
            }
            Some(Token::Plus) => {
                self.advance();
                self.unary()
            }
            _ => self.power(),
        };
        self.depth -= 1;
        result
    }

    /// `^` 右结合，且指数可以带一元负号（`2^-1`）。
    fn power(&mut self) -> CalcResult<f64> {
        let base = self.primary()?;
        if self.peek() == Some(Token::Caret) {
            self.advance();
            self.enter()?;
            let exponent = self.unary();
            self.depth -= 1;
            return Ok(base.powf(exponent?));
        }
        Ok(base)
    }

    fn primary(&mut self) -> CalcResult<f64> {
        match self.advance() {
            Some(Token::Num(value)) => Ok(value),
            Some(Token::LParen) => {
                self.enter()?;
                let value = self.expr();
                self.depth -= 1;
                let value = value?;
                match self.advance() {
                    Some(Token::RParen) => Ok(value),
                    // `)` 缺失或里面已经用完 token
                    _ => Err(CalcError::Unbalanced),
                }
            }
            // `1+`、`(` 之类：需要操作数却没了（或遇到运算符）
            _ => Err(CalcError::UnexpectedEnd),
        }
    }

    /// 递归下降的栈深保护：任何进入子表达式的路径都要先过这一关。
    fn enter(&mut self) -> CalcResult<()> {
        if self.depth >= MAX_DEPTH {
            return Err(CalcError::TooDeep);
        }
        self.depth += 1;
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::apps::{AppEntry, AppsProvider};
    use crate::engine::QueryEngine;
    use std::future::Future;

    fn block_on<F: Future>(f: F) -> F::Output {
        let mut f = Box::pin(f);
        let mut cx = std::task::Context::from_waker(std::task::Waker::noop());
        match f.as_mut().poll(&mut cx) {
            std::task::Poll::Ready(v) => v,
            std::task::Poll::Pending => panic!("calculator 的 future 应当立即就绪"),
        }
    }

    /// 模拟引擎剥掉 `=` 前缀后的调用方式。
    fn calc(expr: &str) -> Vec<Match> {
        let p = CalculatorProvider::new();
        let ctx = QueryCtx::new(format!("={expr}"), expr.to_string());
        block_on(p.query(&ctx))
    }

    /// 求值结果（非法输入 → None）。
    fn eval(expr: &str) -> Option<f64> {
        evaluate(expr)
    }

    // ---- 配置 ----

    #[test]
    fn is_prefix_only_and_not_global() {
        let p = CalculatorProvider::new();
        assert_eq!(p.id(), "calculator");
        assert_eq!(p.config().prefix, Some("=".to_string()));
        assert!(p.config().enabled, "默认启用");
        // 关键：不进全局，否则数字串会污染应用/窗口搜索。
        assert!(!p.config().is_global);
        assert_eq!(p.config().weight, 1.0);
    }

    // ---- 四则与优先级 ----

    #[test]
    fn addition_and_subtraction() {
        assert_eq!(eval("1+1"), Some(2.0));
        assert_eq!(eval("10-3-2"), Some(5.0), "减法左结合");
        assert_eq!(eval(" 1 + 1 "), Some(2.0), "空白可忽略");
    }

    #[test]
    fn multiplication_and_division() {
        assert_eq!(eval("2*3"), Some(6.0));
        assert_eq!(eval("10/4"), Some(2.5));
        assert_eq!(eval("100/5/2"), Some(10.0), "除法左结合");
    }

    #[test]
    fn operator_precedence() {
        assert_eq!(eval("2+3*4"), Some(14.0));
        assert_eq!(eval("2*3+4"), Some(10.0));
        assert_eq!(eval("1+2*3-4/2"), Some(5.0));
    }

    #[test]
    fn parentheses_override_precedence() {
        assert_eq!(eval("(2+3)*4"), Some(20.0));
        assert_eq!(eval("2*(3+(4-1))"), Some(12.0));
        assert_eq!(eval("((((1))))"), Some(1.0));
    }

    #[test]
    fn modulo() {
        assert_eq!(eval("7%3"), Some(1.0));
        assert_eq!(eval("10%5"), Some(0.0));
        assert_eq!(eval("2+7%3"), Some(3.0), "% 与 * / 同级");
    }

    // ---- 幂 ----

    #[test]
    fn power_is_right_associative() {
        assert_eq!(eval("2^10"), Some(1024.0));
        // 右结合：2^(3^2) = 512，而不是 (2^3)^2 = 64
        assert_eq!(eval("2^3^2"), Some(512.0));
    }

    #[test]
    fn power_binds_tighter_than_unary_minus() {
        // 与计算器惯例一致：-2^2 = -(2^2) = -4
        assert_eq!(eval("-2^2"), Some(-4.0));
        assert_eq!(eval("(-2)^2"), Some(4.0));
    }

    #[test]
    fn exponent_may_be_negative() {
        assert_eq!(eval("2^-1"), Some(0.5));
        assert_eq!(eval("2^-2"), Some(0.25));
    }

    // ---- 一元运算 ----

    #[test]
    fn unary_minus() {
        assert_eq!(eval("-5+3"), Some(-2.0));
        assert_eq!(eval("3*-2"), Some(-6.0));
        assert_eq!(eval("-(1+2)"), Some(-3.0));
        assert_eq!(eval("--3"), Some(3.0), "双重负号");
        assert_eq!(eval("+7"), Some(7.0), "一元正号");
    }

    // ---- 小数与科学计数 ----

    #[test]
    fn decimals() {
        assert_eq!(eval("1.5*2"), Some(3.0));
        assert_eq!(eval("0.5"), Some(0.5));
        assert_eq!(eval(".5+.25"), Some(0.75), "省略前导 0");
        assert_eq!(eval("5."), Some(5.0), "省略小数部分");
    }

    #[test]
    fn scientific_notation() {
        assert_eq!(eval("1e3"), Some(1000.0));
        assert_eq!(eval("1E3"), Some(1000.0));
        assert_eq!(eval("1e3+1"), Some(1001.0));
        assert_eq!(eval("3.5e-2"), Some(0.035));
        assert_eq!(eval("1e+2*2"), Some(200.0));
    }

    // ---- 非法输入一律不返回 match ----

    #[test]
    fn invalid_expressions_produce_no_match() {
        for bad in [
            "",      // 空表达式
            "   ",   // 纯空白
            "1+",    // 缺操作数
            "+",     // 只有一元运算符
            "(",     // 括号未闭合
            ")",     // 多余右括号
            "()",    // 空括号
            "(1+2",  // 缺右括号
            "1++2",  // 连写运算符（第二个 + 后面没有操作数… 实际合法，见下）
            "abc",   // 非数字
            "1a",    // 尾随字母
            "1 2",   // 尾随数字
            "1.2.3", // 畸形小数
            "1e",    // 指数没有数字
            "1e+",   // 指数只有符号
            ".",     // 孤立小数点
            "1/0",   // 除零 → inf
            "0/0",   // → NaN
            "1%0",   // 取模 0 → NaN
            "1e400", // 溢出 → inf
            "１+１", // 全角数字
            "2**3",  // 连续乘号
            "^2",    // 缺底数
        ] {
            // "1++2" 属于合法（1 + (+2)），单独排除
            if bad == "1++2" {
                continue;
            }
            assert_eq!(calc(bad).len(), 0, "{bad:?} 不该产生结果：{:?}", eval(bad));
        }
    }

    #[test]
    fn unary_plus_chain_is_legal() {
        // Python 同样接受：1 + (+2) = 3
        assert_eq!(eval("1++2"), Some(3.0));
    }

    #[test]
    fn oversized_input_is_rejected_without_panicking() {
        let long = "1".repeat(MAX_INPUT_LEN + 1);
        assert_eq!(eval(&long), None);
        // 刚好在上限内仍可用（用一个不会溢出 f64 的短表达式补齐长度）。
        let mut ok = String::from("1");
        while ok.chars().count() < MAX_INPUT_LEN - 2 {
            ok.push_str("+0");
        }
        assert!(ok.chars().count() <= MAX_INPUT_LEN);
        assert_eq!(eval(&ok), Some(1.0));
    }

    #[test]
    fn deep_nesting_is_rejected_instead_of_overflowing_the_stack() {
        // 远超 MAX_DEPTH 的括号，必须返回 None 而不是栈溢出（abort）。
        let depth = MAX_DEPTH * 4;
        let expr = format!("{}1{}", "(".repeat(depth), ")".repeat(depth));
        assert_eq!(eval(&expr), None);
    }

    // ---- 格式化 ----

    #[test]
    fn formats_integers_without_decimal_point() {
        assert_eq!(format_number(4.0), "4");
        assert_eq!(format_number(-2.0), "-2");
        assert_eq!(format_number(0.0), "0");
        assert_eq!(format_number(-0.0), "0");
        assert_eq!(format_number(1024.0), "1024");
    }

    #[test]
    fn formats_decimals_without_float_noise() {
        // 0.1 + 0.2 的 IEEE754 结果是 0.30000000000000004
        assert!(eval("0.1+0.2").unwrap() > 0.3);
        assert_eq!(format_number(eval("0.1+0.2").unwrap()), "0.3");
        assert_eq!(format_number(eval("10/4").unwrap()), "2.5");
        assert_eq!(format_number(eval("1/3").unwrap()), "0.3333333333");
        // 极小值不该被打成 "0"（round 到 1e-10 以下确实归零，明确接受）
        assert_eq!(format_number(1e-12), "0");
    }

    #[test]
    fn formats_large_values() {
        assert_eq!(format_number(eval("2^40").unwrap()), "1099511627776");
        assert_eq!(format_number(1e20), "100000000000000000000");
    }

    // ---- Match 契约 ----

    #[test]
    fn match_carries_result_and_copy_action() {
        let hits = calc("1+1");
        assert_eq!(hits.len(), 1);
        let m = &hits[0];
        assert_eq!(m.provider_id, "calculator");
        assert_eq!(m.title, "2");
        assert_eq!(m.subtitle, "1+1");
        assert_eq!(m.icon, None);
        assert_eq!(m.score, 1.0);
        assert_eq!(
            m.action,
            Action::Copy {
                text: "2".to_string()
            },
            "计算器结果的动作就是把结果写进剪贴板"
        );
    }

    #[test]
    fn match_title_equals_action_text() {
        // Enter 后用户拿到的东西必须和列表里看到的一模一样。
        for expr in ["1+1", "0.1+0.2", "2^10", "1/3"] {
            let hits = calc(expr);
            let Action::Copy { text } = &hits[0].action else {
                panic!("calculator 的动作必须是 Copy");
            };
            assert_eq!(&hits[0].title, text, "{expr}");
        }
    }

    #[test]
    fn default_and_boxed_constructors_agree() {
        let a = CalculatorProvider::default();
        assert_eq!(a.id(), CalculatorProvider::new().id());
        let boxed: Box<dyn Provider> = CalculatorProvider::boxed();
        assert_eq!(boxed.id(), "calculator");
    }

    // ---- 端到端：前缀路由真的把剥好的查询词交给本 provider ----

    #[test]
    fn prefix_routing_feeds_stripped_expression() {
        let apps = AppsProvider::new(vec![AppEntry {
            name: "微信".into(),
            path: r"C:\WeChat.exe".into(),
            icon: String::new(),
            args: Vec::new(),
            app_id: String::new(),
        }]);
        let engine = QueryEngine::new(vec![Box::new(apps), CalculatorProvider::boxed()]);

        // "=1+1" 路由到 calculator，ctx.query 已剥掉 `=`。
        let routing = engine.prepare("=1+1");
        assert_eq!(routing.providers.len(), 1);
        assert_eq!(routing.providers[0].id(), "calculator");
        let matches = block_on(routing.providers[0].query(&routing.ctx));
        assert_eq!(matches[0].title, "2");

        // 普通输入仍然走 apps，calculator 不参与（is_global = false）。
        let routing = engine.prepare("wx");
        let ids: Vec<_> = routing.providers.iter().map(|p| p.id()).collect();
        assert_eq!(ids, vec!["apps"], "calculator 不得出现在全局结果里");
        assert_eq!(routing.ctx.query, "wx");

        // "=weixin" 只查 calculator（表达式非法 → 空），不会漏到应用搜索。
        let routing = engine.prepare("=weixin");
        assert_eq!(routing.providers.len(), 1);
        assert_eq!(block_on(routing.providers[0].query(&routing.ctx)).len(), 0);
    }
}
