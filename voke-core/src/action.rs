//! 条目被选中后要执行的动作。
//!
//! Provider 在 query 阶段就确定动作，以**数据**形式挂在 [`Match`](crate::Match) 上，
//! 由 app 层统一执行。这样做的好处（对比"每个 provider 各自实现 execute"）：
//! - core 不需要 `ActionCtx` trait，也就不需要 gpui 依赖，测试不用 mock
//! - 所有副作用集中在一个 dispatcher 里，行为可审计（例如统一"启动前先隐藏窗口"）
//! - `Action` 可序列化，未来接 gpui-shell 扩展时能直接跨进程传递

use serde::{Deserialize, Serialize};

/// 执行一个条目时产生的副作用。
///
/// 变体按"谁的语义"分组：应用启动、窗口激活、系统状态、纯文本结果。
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[non_exhaustive]
pub enum Action {
    /// 启动一个可执行文件。`args` 为空时直接打开。
    Launch {
        /// 目标路径（exe、lnk、目录、文档皆可）。
        path: String,
        /// 命令行参数。
        args: Vec<String>,
        /// 以管理员权限启动（对应 Ctrl+Shift+Enter，§5 的 runas verb）。
        elevated: bool,
        /// 启动时**隐藏控制台窗口**（Windows 的 `CREATE_NO_WINDOW`）。
        ///
        /// 默认 `false`：绝大多数启动（应用、文档、终端 profile……）本就该
        /// 按目标自己的性子来——GUI 程序无窗口，`cmd.exe`/`wt.exe` 该有窗口。
        ///
        /// 只有"为跑批处理而不得已套了 `cmd.exe /c`"的场景才置 `true`：
        /// 那个 `cmd.exe` 是**实现细节**而非用户想要的终端，它的控制台窗口
        /// 应当藏掉（例如工作区插件经 `bin\code.cmd` 启动 VSCode，见
        /// [`crate::workspace`]）。这是**逐动作**的标记，不是全局开关——
        /// 全局加会误伤 apps 插件启动命令提示符/PowerShell 的窗口。
        no_console: bool,
    },

    /// 激活一个已经打开的窗口。
    ///
    /// 如果窗口已消失，app 层应回退为重新启动对应进程——这个回退由 dispatcher
    /// 统一处理，不需要 provider 各自判断。
    ActivateWindow {
        /// 窗口句柄（Windows 上是 HWND 的数值）。
        hwnd: isize,
    },

    /// 在资源管理器里显示某个路径（GPUI 的 `reveal_path`）。
    Reveal { path: String },

    /// 用默认程序打开一个 URL（GPUI 的 `open_url`）。
    OpenUrl { url: String },

    /// 把文本写进剪贴板（GPUI 的 `write_clipboard`）。
    Copy { text: String },

    /// 保持系统唤醒（§5 的 `SetThreadExecutionState`）。
    ///
    /// `minutes` 为 None 表示不限时，直到用户取消。
    SetAwake {
        minutes: Option<u32>,
        keep_screen: bool,
    },

    /// 取消保持唤醒。
    CancelAwake,

    /// 重新生成 / 重新查询（"重掷"）。
    ///
    /// 语义：**不隐藏面板、不改当前输入**，只是让 provider 用同一份输入再跑一次
    /// query（random 用它重掷一批新值）。与 [`Action::None`] 的区别在于它有明确的
    /// "重新执行"意图——dispatcher 在 voke-app 收到后会对当前输入框重新发起查询，
    /// 而不是什么都不做。状态展示类行（如 awake 的"保持唤醒中"）不需要它。
    ///
    /// 它是"动作信号"而非"副作用"：本身不产生系统副作用（不写剪贴板、不启动进程），
    /// 由 app 层的视图状态机消费，不过 dispatcher 的副作用路径。
    Refresh,

    /// 激活一个插件（`#` 插件列表的选中动作）。
    ///
    /// `#` 前缀被插件列表接管（见 [`crate::engine::plugin_picker_matches`]）：
    /// 打 `#` 列出所有 enabled 插件，Enter / 点击某条后 app 层把
    /// `active_plugin` 设成这里携带的 id，并把输入框清成该插件的前缀
    /// （如 `#b ` 或 `= `）。它描述的是"进入某个插件的意图"，本身**没有**
    /// 系统副作用——由 app 层的 view 状态机消费，不经过 dispatcher 的
    /// 副作用路径（不会隐藏窗口、不写剪贴板）。
    ActivatePlugin {
        /// 插件稳定 id（`Provider::id()`），app 层用它设 `active_plugin`。
        plugin_id: String,
        /// 激活后写入输入框的前缀（`Provider::config().prefix`），如 `#b `。
        /// 无前缀的插件（awake）为空串，此时输入框清空。
        prefix: String,
    },

    /// 什么都不做（纯展示型条目）。
    ///
    /// **方案 A 下它的用途收窄为两种**：
    /// - [`Interact::Hint`](crate::Interact::Hint) 行（它们根本不可选）
    /// - 插件内部“状态展示”行（如 awake 的“保持唤醒中（还剩 25 分钟）”）
    None,
}

impl Action {
    /// 启动一个路径，无参数。
    pub fn launch(path: impl Into<String>) -> Self {
        Action::Launch {
            path: path.into(),
            args: Vec::new(),
            elevated: false,
            no_console: false,
        }
    }

    /// 启动并带参数（WT profiles、zed <path> 用这个）。
    pub fn launch_with(path: impl Into<String>, args: Vec<String>) -> Self {
        Action::Launch {
            path: path.into(),
            args,
            elevated: false,
            no_console: false,
        }
    }

    /// 启动并隐藏控制台窗口（仅用于启动器自己拼的编辑器 CLI 命令）。
    ///
    /// 与 [`Action::launch_with`] 的唯一区别是把 [`no_console`](Action::Launch) 置
    /// `true`；`elevated` 仍为 `false`。见 [`crate::workspace`] 的用法：
    /// VSCode 经 `cmd.exe /c code.cmd` 启动、Zed 起控制台子系统的
    /// `bin\Zed.exe`——两者的控制台窗口都是实现细节，不是用户要的终端。
    pub fn launch_with_hidden(path: impl Into<String>, args: Vec<String>) -> Self {
        Action::Launch {
            path: path.into(),
            args,
            elevated: false,
            no_console: true,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn launch_helper_builds_plain_launch() {
        assert_eq!(
            Action::launch("C:/a.exe"),
            Action::Launch {
                path: "C:/a.exe".to_string(),
                args: vec![],
                elevated: false,
                no_console: false,
            }
        );
    }

    /// **默认不隐藏控制台**：`launch`/`launch_with` 造出的动作 `no_console` 必须是
    /// `false`，否则 apps 插件启动 cmd/pwsh 的窗口会被一起藏掉。
    #[test]
    fn launch_helpers_default_to_no_console_hidden() {
        assert!(!matches!(
            Action::launch("C:/a.exe"),
            Action::Launch {
                no_console: true,
                ..
            }
        ));
        assert!(!matches!(
            Action::launch_with("wt.exe", vec!["-p".into()]),
            Action::Launch {
                no_console: true,
                ..
            }
        ));
    }

    /// `launch_with_hidden` 是唯一的"隐藏控制台"入口，只翻 `no_console` 这一个位。
    #[test]
    fn launch_with_hidden_sets_only_no_console() {
        assert_eq!(
            Action::launch_with_hidden("cmd.exe", vec!["/c".into()]),
            Action::Launch {
                path: "cmd.exe".to_string(),
                args: vec!["/c".to_string()],
                elevated: false,
                no_console: true,
            }
        );
    }

    #[test]
    fn action_is_serializable() {
        let action = Action::launch_with("wt.exe".to_string(), vec!["-p".into(), "Ubuntu".into()]);
        let json = serde_json::to_string(&action).unwrap();
        assert!(json.contains("wt.exe"));
    }
}
