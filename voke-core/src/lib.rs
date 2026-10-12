//! voke 的逻辑层：查询引擎、Provider 契约、拼音/模糊匹配、配置模型。
//!
//! 这一层**不依赖 gpui**，因此可以在 WSL 里 `cargo test -p voke-core` 秒级验证，
//! 不需要 Windows、不需要 GPU。
//!
//! 分层约定（§1/§3）：
//! - 异步用 `BoxFuture`，不引入 gpui 的 `Task`——app 层负责桥接到 gpui 的 executor
//! - 副作用用 [`Action`] 数据表达，不引入 `ActionCtx` trait——app 层统一执行，
//!   这样 core 保持零 UI 依赖，测试也不需要 mock
//!
//! ## 插件交互（方案 A）
//!
//! 每条 [`Match`] 带一个 [`Interact`]（`Execute` / `EnterSubquery` / `Hint`），
//! UI 只按这个类型分发 Enter / 点击，不再有"面板行特判"。
//! 插件用[专属前缀](engine::PREFIX_CALCULATOR)触发；需要分叉的插件（base64）
//! 把自己的两个方向列成 [`Interact::EnterSubquery`] 条目，Enter / 点击都进。

pub mod action;
pub mod apps;
pub mod awake;
pub mod base64;
pub mod calculator;
pub mod clock;
pub mod config;
pub mod engine;
pub mod entropy;
pub mod matcher;
pub mod provider;
pub mod random;
pub mod search_key;
pub mod terminal;
pub mod timestamp;
pub mod uuid;
pub mod windows;
pub mod workspace;

pub use action::Action;
pub use apps::{AppEntry, AppsProvider};
pub use awake::{AwakeProvider, AwakeStatus, AwakeStatusFetcher};
pub use base64::Base64Provider;
pub use calculator::{CalculatorProvider, evaluate, format_number};
pub use clock::{Clock, FixedClock, SystemClock};
pub use config::{AppConfig, ConfigPatch, HotkeySpec, ProviderSettings};
pub use engine::{
    Aggregated, FrequencyStore, PREFIX_BASE64, PREFIX_CALCULATOR, PREFIX_RANDOM, PREFIX_TIMESTAMP,
    PREFIX_UUID, ProviderBatch, ProviderGroup, QueryEngine, RankedMatch, RouteKind, Routing,
    frequency_from_toml, frequency_to_toml, placeholder_for,
};
pub use matcher::{MatchScore, score_entries, score_entry};
pub use provider::{Interact, Match, Provider, ProviderConfig, QueryCtx};
pub use random::RandomProvider;
pub use search_key::SearchKeys;
pub use terminal::{ProfileFetcher, TerminalProfile, TerminalProvider, profiles_from_settings};
pub use timestamp::TimestampProvider;
pub use uuid::UuidProvider;
pub use windows::{WindowEntry, WindowFetcher, WindowsProvider};
pub use workspace::{
    EditorCommand, EditorKind, LocationKind, VsCodeRecentPayload, WorkspaceEntry, WorkspaceFetcher,
    WorkspaceProvider, ZedRemoteRow, ZedWorkspaceRow, workspaces as workspaces_from_history,
};
