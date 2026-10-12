//! 进程内存快照（排查「启动后一直活着」的常驻内存用）。
//!
//! 只做一件事：读本进程的**工作集**与**私有提交**，供启动/关键节点打日志。
//! 跨平台可编译：非 Windows 上 [`process_memory`] 返回 `None`，[`log_snapshot`]
//! 记一条「当前平台不提供」，这样 app 层的打点代码不需要额外 cfg 分支。

/// 读本进程内存：(WorkingSetSize, PrivateUsage)，单位**字节**。
///
/// - `WorkingSetSize`：进程当前工作集（物理内存里驻留的页，含共享页）。
/// - `PrivateUsage`：私有提交（commit charge，任务管理器的「提交大小」）。
///
/// 非 Windows（或 API 调用失败）返回 `None`。
#[cfg(windows)]
pub fn process_memory() -> Option<(u64, u64)> {
    use ::windows::Win32::System::ProcessStatus::{
        K32GetProcessMemoryInfo, PROCESS_MEMORY_COUNTERS_EX,
    };
    use ::windows::Win32::System::Threading::GetCurrentProcess;

    // `cb` 必须等于传入结构体的实际大小：只有用 _EX 版本才拿得到 `PrivateUsage`
    // （基础版没有这个字段）。K32GetProcessMemoryInfo 的声明参数是
    // `*mut PROCESS_MEMORY_COUNTERS`，而 _EX 是它的扩展版（前 10 个字段布局一致），
    // 按 _EX 大小调用是 Windows 文档支持的标准用法。
    // 就地初始化 `cb`（不用 `default()` 后再赋值，避免 clippy 的 field_reassign）。
    let mut counters = PROCESS_MEMORY_COUNTERS_EX {
        cb: core::mem::size_of::<PROCESS_MEMORY_COUNTERS_EX>() as u32,
        ..Default::default()
    };
    // SAFETY: `GetCurrentProcess()` 是本进程的伪句柄（调用后始终有效，无需关闭）；
    // 出参结构体按 _EX 的实际大小分配，`cb` 与之严格一致。
    let ok = unsafe {
        K32GetProcessMemoryInfo(
            GetCurrentProcess(),
            (&mut counters as *mut PROCESS_MEMORY_COUNTERS_EX).cast(),
            counters.cb,
        )
    };
    if !ok.as_bool() {
        return None;
    }
    Some((counters.WorkingSetSize as u64, counters.PrivateUsage as u64))
}

/// 非 Windows 平台的占位：Linux 上跑 `cargo test` 时也要能编译。
#[cfg(not(windows))]
pub fn process_memory() -> Option<(u64, u64)> {
    None
}

/// 打一条标准格式的内存快照日志（单位 MB，保留一位小数）。
///
/// 格式固定，方便事后 `grep "内存快照"` 汇总：
/// `内存快照 [<tag>]：WorkingSet=x.xMB PrivateCommit=y.yMB`
pub fn log_snapshot(tag: &str) {
    match process_memory() {
        Some((working_set, private_commit)) => log::info!(
            "内存快照 [{tag}]：WorkingSet={:.1}MB PrivateCommit={:.1}MB",
            working_set as f64 / (1024.0 * 1024.0),
            private_commit as f64 / (1024.0 * 1024.0),
        ),
        None => log::info!("内存快照 [{tag}]：当前平台不提供"),
    }
}
