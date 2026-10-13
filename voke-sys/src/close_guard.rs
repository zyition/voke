//! 窗口「程序化关闭」的一次性闸门（重入守卫）。
//!
//! 背景：gpui 的 `on_window_should_close` 回调嵌套在 `update_window_erased` 里运行
//! （窗口被 `take` 出 map → 跑闭包 → `trail` 检查 `removed` 并同步摘除 drop）。
//! 从「闭包返回 false」到「Drop 任务真正 `DestroyWindow`」之间有一个毫秒级间隙，
//! 期间理论上可能到达第二个 WM_CLOSE。第二个一律要吞掉：放行 `true` 会让
//! DefWindowProc 同步 `DestroyWindow`，抢在 Drop 任务的 `RevokeDragDrop` 之前把
//! HWND 销毁，重演拖放目标泄漏（gpui-pre-windows events.rs:336-341、
//! window.rs:610-631）。
//!
//! 为什么放 voke-sys 而不是 voke-app：与 [`crate::logcap`] 同款理由——消费方
//! voke-app 的测试在 Linux native 因缺 fontconfig 跑不了，下沉后
//! `cargo test -p voke-sys` 能真跑这个状态机。

use std::cell::Cell;

/// 一次调用放行、后续调用一律吞掉的一次性闸门。
///
/// `enter()` 首次返回 `true`（放行），之后永远返回 `false`（吞掉）。
/// 内部用 `Cell<bool>` 而非 `&mut self`：`on_window_should_close` 收的是 `Fn`
/// 闭包，重入守卫必须走内部可变性。
#[derive(Debug, Default)]
pub struct CloseGuard(Cell<bool>);

impl CloseGuard {
    pub fn new() -> Self {
        Self(Cell::new(false))
    }

    /// 尝试进入关闭流程：首次 `true`，后续一律 `false`。
    pub fn enter(&self) -> bool {
        !self.0.replace(true)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn 首次放行后续吞掉() {
        let guard = CloseGuard::new();
        assert!(guard.enter(), "第一次必须放行");
        assert!(!guard.enter(), "第二次必须吞掉");
        assert!(!guard.enter(), "第三次及以后一律吞掉");
    }

    #[test]
    fn default_与_new_等价() {
        assert!(CloseGuard::default().enter());
    }

    #[test]
    fn 每次调用都翻转内部状态() {
        // 直接验证状态位本身（而非只验证 enter 的返回值）：
        // 放行后状态必须已置位，否则第二次 enter 会再次放行。
        let guard = CloseGuard::new();
        assert!(!guard.0.get(), "初始为未进入");
        assert!(guard.enter());
        assert!(guard.0.get(), "放行后必须置位");
        assert!(!guard.enter());
        assert!(guard.0.get(), "吞掉不改变已置位状态");
    }
}
