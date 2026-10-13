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
        /// 子进程工作目录（`.lnk` 的"起始位置"）。**空串 = 不设置**（子进程
        /// 继承启动器的默认 cwd）。
        ///
        /// 这里是**解析期已展开但未校验**的原始值（core 是纯数据层，不做
        /// 文件系统判定）：`%VAR%` / `~` 应当已在 provider 数据源展开；
        /// "展开后存不存在"由 app 层 dispatcher 在启动那一刻校验
        /// （不存在就不设 cwd，绝不把字面量/死路径传给 `CreateProcessW`）。
        working_dir: String,
        /// 以管理员权限启动（对应 Ctrl+Shift+Enter，§5 的 runas verb）。
        elevated: bool,
        /// 该条目是否来自开始菜单的 `.lnk`（快捷方式**本体路径**，非 lnk 条目
        /// 为空串）。
        ///
        /// 主流启动器（PowerToys Run / Flow）把 `.lnk` 本体交 shell 执行，借用
        /// shell 链接处理器的目标解析 / 参数 / 起始位置 / 环境变量展开——与资源
        /// 管理器双击完全同义。voke 早期走"解析期展开 target+args → CreateProcessW
        /// 直启"的快路径（`voke-sys/src/env.rs` 的展开语义就是为它补的），本字段
        /// 让 dispatcher 能改走 shell 路线：非空时 `lpFile` 用这里的 `.lnk`，
        /// **忽略** `path`/`args`/`working_dir`（lnk 里存着什么，shell 自己知道）。
        /// `path` 仍保留解析后的目标，供副标题与图标使用。
        lnk_path: String,
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

    /// 启动 Windows Terminal 的某个 profile。
    ///
    /// **为什么不复用 [`Action::Launch`] 直启 wt.exe**：对齐 PowerToys CmdPal 的
    /// Windows Terminal 扩展（microsoft/PowerToys@`b76d14e3` 的
    /// `Microsoft.CmdPal.Ext.WindowsTerminal`，`Commands/LaunchProfileCommand.cs`）——
    /// 它用 `IApplicationActivationManager::ActivateApplication(aumid, args, …)`
    /// 激活 WT 包，而不是 `CreateProcess` 直启 wt.exe 的 App Execution Alias。
    /// 真机实测（2026-10-13 `voke.log`）：经 voke 直启别名时，WT 内 pwsh 的
    /// `$PROFILE` 调 scoop shim 起 mise.exe 失败；同一 profile 经 shell 激活
    /// （`explorer.exe shell:AppsFolder\…`）则正常。App Activation 是 WT 的
    /// 官方启动路径（store 版 WT 的 exe 在 `WindowsApps` 缓存目录，直启本就该
    /// 绕开），也顺带把 WT 的父进程从启动器换成激活服务。
    LaunchTerminalProfile {
        /// WT 包的 AUMID（App Activation 的 `appUserModelId`），例如
        /// `Microsoft.WindowsTerminal_8wekyb3d8bbwe!App`。
        aumid: String,
        /// profile 标识：**优先 guid**（`{574e775e-4f2a-5b96-ac1e-a2962a402336}`），
        /// guid 缺失的老条目退化为显示名。显示名会本地化、会重复、会由 `source`
        /// 动态生成（实测 pwsh profile 就是 `Windows.Terminal.PowershellCore`
        /// 生成的），guid 才是 WT 的稳定标识；`--profile` 两者都收。
        profile: String,
        /// App Activation 失败时的兜底直启路径（wt.exe 别名）。WT 不是以 MSIX
        /// 包安装（解包版）时激活会失败，此时退回 `wt.exe -p <profile>`。
        fallback_path: String,
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
            working_dir: String::new(),
            elevated: false,
            no_console: false,
            lnk_path: String::new(),
        }
    }

    /// 启动并带参数（WT profiles、zed <path> 用这个）。
    pub fn launch_with(path: impl Into<String>, args: Vec<String>) -> Self {
        Action::Launch {
            path: path.into(),
            args,
            working_dir: String::new(),
            elevated: false,
            no_console: false,
            lnk_path: String::new(),
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
            working_dir: String::new(),
            elevated: false,
            no_console: true,
            lnk_path: String::new(),
        }
    }

    /// 启动一个来自 `.lnk` 的条目：**把快捷方式本体交 shell 执行**（双击同款）。
    ///
    /// `path`/`args`/`working_dir` 是 sys 层解析期从 lnk 里展开出来的值（副标题、
    /// 图标、日志用）；dispatcher 见到 [`lnk_path`](Action::Launch::lnk_path)
    /// 非空时以它作为 `ShellExecuteExW` 的 `lpFile`，忽略其余字段——lnk 里存的
    /// 目标/参数/起始位置由 shell 链接处理器负责，避免我们复刻的展开语义与
    /// shell 不一致。
    pub fn launch_lnk(
        path: impl Into<String>,
        args: Vec<String>,
        working_dir: impl Into<String>,
        lnk_path: impl Into<String>,
    ) -> Self {
        Action::Launch {
            path: path.into(),
            args,
            working_dir: working_dir.into(),
            elevated: false,
            no_console: false,
            lnk_path: lnk_path.into(),
        }
    }

    /// 该动作执行后，搜索面板是否应**复位**（清空输入框、退出插件/子模式、
    /// 结果列表回到顶层空查询）。
    ///
    /// 规则来自用户实测 UX：**按"用户意图是不是离开面板"分类**——
    /// - **离开面板去做别的事**（启动应用 / 切换窗口 / 打开 URL / 在资源管理器
    ///   里显示 / 保持唤醒）→ `true`：launcher 一次只干一件事，干完就收；
    ///   下次唤回不该还留着上次的查询、插件页和旧结果。
    /// - **产出可复制内容**（[`Action::Copy`]：随机值 / UUID / 时间戳 /
    ///   base64 编解码 / 计算器结果）→ `false`：面板隐藏再唤回时，输入与
    ///   结果原样保留，用户能看到并再次复制。
    /// - **信号 / 展示类**（[`Action::Refresh`] 重掷、[`Action::None`] 纯展示、
    ///   [`Action::ActivatePlugin`] 由视图状态机消费）→ `false`：语义上就
    ///   不该动输入。
    ///
    /// 未来新增动作变体时**默认保守归 `false`**（不重置）：宁可多留一会儿
    /// 输入，也不要把用户刚打的字弄丢——新动作想要"复位"语义时应显式加进来。
    /// 本 match 是穷尽的：新增变体不改这里会直接编译报错，逼你显式表态。
    pub fn should_reset_input(&self) -> bool {
        match self {
            Action::Launch { .. }
            | Action::LaunchTerminalProfile { .. }
            | Action::ActivateWindow { .. }
            | Action::OpenUrl { .. }
            | Action::Reveal { .. }
            | Action::SetAwake { .. }
            | Action::CancelAwake => true,
            Action::Copy { .. }
            | Action::Refresh
            | Action::None
            | Action::ActivatePlugin { .. } => false,
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
                working_dir: String::new(),
                elevated: false,
                no_console: false,
                lnk_path: String::new(),
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
                working_dir: String::new(),
                elevated: false,
                no_console: true,
                lnk_path: String::new(),
            }
        );
    }

    /// `.lnk` 条目：`launch_lnk` 只多带一个 lnk 本体路径，其余字段语义不变。
    #[test]
    fn launch_lnk_carries_the_shortcut_itself() {
        assert_eq!(
            Action::launch_lnk(
                r"C:\exe\alacritty.exe",
                vec!["--working-directory".into(), r"C:\Users\u".into()],
                r"C:\Users\u",
                r"C:\Users\u\Desktop\Alacritty.lnk",
            ),
            Action::Launch {
                path: r"C:\exe\alacritty.exe".to_string(),
                args: vec!["--working-directory".into(), r"C:\Users\u".into()],
                working_dir: r"C:\Users\u".to_string(),
                elevated: false,
                no_console: false,
                lnk_path: r"C:\Users\u\Desktop\Alacritty.lnk".to_string(),
            }
        );
    }

    #[test]
    fn action_is_serializable() {
        let action = Action::launch_with("wt.exe".to_string(), vec!["-p".into(), "Ubuntu".into()]);
        let json = serde_json::to_string(&action).unwrap();
        assert!(json.contains("wt.exe"));
    }

    /// **输入框重置规则**（用户实测 UX）：按"用户意图是不是离开面板"分类。
    /// 覆盖 `Action` 的每个变体，规则漂移立刻在这里红。
    #[test]
    fn should_reset_input_classifies_all_variants() {
        // 离开面板去做别的事 → 复位（清空输入、退出插件、回到顶层列表）。
        for action in [
            Action::launch(r"C:\Users\test\a.exe"),
            Action::launch_with("wt.exe", vec!["-p".into(), "Ubuntu".into()]),
            Action::launch_with_hidden("cmd.exe", vec!["/c".into(), "code.cmd".into()]),
            Action::launch_lnk(r"C:\a\alacritty.exe", vec![], "", r"C:\a\Alacritty.lnk"),
            Action::LaunchTerminalProfile {
                aumid: "Microsoft.WindowsTerminal_8wekyb3d8bbwe!App".into(),
                profile: "{574e775e-4f2a-5b96-ac1e-a2962a402336}".into(),
                fallback_path: r"C:\a\wt.exe".into(),
            },
            Action::ActivateWindow { hwnd: 1234 },
            Action::OpenUrl {
                url: "https://example.com".into(),
            },
            Action::Reveal {
                path: r"C:\Users\test".into(),
            },
            Action::SetAwake {
                minutes: Some(30),
                keep_screen: false,
            },
            Action::CancelAwake,
        ] {
            assert!(
                action.should_reset_input(),
                "{action:?} 是「离开面板」类，应重置输入框"
            );
        }
        // 产出可复制内容 / 信号 / 展示 → 保留输入与结果。
        for action in [
            Action::Copy {
                text: "a1b2c3".into(),
            },
            Action::Refresh,
            Action::None,
            Action::ActivatePlugin {
                plugin_id: "base64".into(),
                prefix: "#b ".into(),
            },
        ] {
            assert!(
                !action.should_reset_input(),
                "{action:?} 是「保留」类，不该重置输入框"
            );
        }
    }

    /// 把规则落到**每个内置插件选中后真实产出的动作**上（含子模式）：
    /// 这条用例是"用户视角"的验收表——哪个插件 Enter 之后输入框该空、
    /// 哪个该留着，全在这里列一遍。
    #[test]
    fn should_reset_input_covers_every_builtin_plugin() {
        // (插件 / 场景说明, 该场景 Enter 后 core 产出的动作, 期望是否重置)
        let cases: &[(&str, Action, bool)] = &[
            (
                "apps：打开应用（Launch）",
                Action::launch(r"C:\Program Files\VideoLAN\VLC\vlc.exe"),
                true,
            ),
            (
                "apps：MSIX 应用（Launch msix://）",
                Action::launch("msix://Microsoft.WindowsTerminal_8wekyb3d8bbwe!App"),
                true,
            ),
            (
                "windows：切换窗口（ActivateWindow）",
                Action::ActivateWindow { hwnd: 6557 },
                true,
            ),
            (
                "terminal：WT profile（App Activation + --profile <guid>）",
                Action::LaunchTerminalProfile {
                    aumid: "Microsoft.WindowsTerminal_8wekyb3d8bbwe!App".into(),
                    profile: "{574e775e-4f2a-5b96-ac1e-a2962a402336}".into(),
                    fallback_path: r"C:\a\wt.exe".into(),
                },
                true,
            ),
            (
                "workspaces：打开工作区（Launch 隐藏控制台）",
                Action::launch_with_hidden(
                    "cmd.exe",
                    vec!["/c".into(), "code.cmd".into(), r"C:\repo".into()],
                ),
                true,
            ),
            (
                "random：复制随机串 / 整数 / hex（Copy）",
                Action::Copy {
                    text: "Kx9$mQ2v".into(),
                },
                false,
            ),
            ("random：重新生成（Refresh）", Action::Refresh, false),
            (
                "uuid：复制 v4（Copy）",
                Action::Copy {
                    text: "3f2504e0-4f89-11d3-9a0c-0305e82c3301".into(),
                },
                false,
            ),
            (
                "timestamp：复制时间戳 / 日期（Copy）",
                Action::Copy {
                    text: "1700000000".into(),
                },
                false,
            ),
            (
                "base64：复制加密 / 解密结果（Copy）",
                Action::Copy {
                    text: "aGVsbG8=".into(),
                },
                false,
            ),
            (
                "calculator：复制计算结果（Copy）",
                Action::Copy { text: "42".into() },
                false,
            ),
            (
                "awake：设置保持唤醒（SetAwake）",
                Action::SetAwake {
                    minutes: Some(30),
                    keep_screen: true,
                },
                true,
            ),
            (
                "awake：取消保持唤醒（CancelAwake）",
                Action::CancelAwake,
                true,
            ),
            ("awake：状态展示行（Hint，不可选）", Action::None, false),
        ];
        for (label, action, expected) in cases {
            assert_eq!(
                action.should_reset_input(),
                *expected,
                "规则漂移：{label}（{action:?}）"
            );
        }
    }
}
