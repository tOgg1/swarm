//! App shell and state model for the Forge loop TUI.
//!
//! Ports the Go `internal/looptui/looptui.go` model: tab-based navigation,
//! modal UI modes (filter/confirm/wizard/help/expanded-logs), loop selection,
//! log source/layer cycling, multi-log pagination, and pinned loops.

use std::collections::{HashMap, HashSet, VecDeque};
use std::io::Write;

use forge_cli::logs::{render_lines_for_layer, LogRenderLayer};
use forge_ftui_adapter::input::{
    InputEvent, Key, KeyEvent, MouseButton, MouseEvent, MouseEventKind, MouseWheelDirection,
};
use forge_ftui_adapter::render::{CellStyle, FrameSize, Rect, RenderFrame, StyledSpan, TextRole};
use forge_ftui_adapter::style::ThemeSpec;
use forge_ftui_adapter::widgets::BorderStyle;

use crate::adaptive_hints::{AdaptiveHintRanker, HintSpec};
use crate::command_palette::{
    CommandPalette, PaletteActionId, PaletteContext, DEFAULT_SEARCH_BUDGET,
};
use crate::keymap::{KeyChord, KeyCommand, KeyScope, Keymap, ModeScope};
use crate::layouts::{
    fit_pane_layout_for_breakpoint, layout_cell_size, layout_index_for, normalize_layout_index,
    PaneLayout, PANE_LAYOUTS,
};
use crate::link_registry::{LinkRegistry, LinkTarget};
use crate::log_source_abstraction::{LogContentKind, LogSourceRoute, LogTransportKind};
use crate::search_overlay::SearchOverlay;
use crate::theme::{
    cycle_accessibility_preset, cycle_palette, resolve_palette_colors,
    resolve_palette_for_capability, Palette, ResolvedPalette, TerminalColorCapability,
};

// ---------------------------------------------------------------------------
// Constants – matching Go defaults
// ---------------------------------------------------------------------------

pub const DEFAULT_LOG_LINES: usize = 12;
pub const LOG_SCROLL_STEP: usize = 20;
pub const MAX_LOG_BACKFILL: usize = 8000;

pub const MULTI_HEADER_ROWS: i32 = 2;
pub const MULTI_CELL_GAP: i32 = 1;
pub const MULTI_MIN_CELL_WIDTH: i32 = 38;
pub const MULTI_MIN_CELL_HEIGHT: i32 = 8;
const MAX_NOTIFICATION_QUEUE: usize = 32;
const MAX_NAV_HISTORY: usize = 32;
const DESTRUCTIVE_CONFIRM_REASON_MIN_CHARS: usize = 12;
const MAX_DESTRUCTIVE_CONFIRM_REASON_CHARS: usize = 160;

pub const FILTER_STATUS_OPTIONS: &[&str] =
    &["all", "running", "sleeping", "waiting", "stopped", "error"];

// ---------------------------------------------------------------------------
// MainTab
// ---------------------------------------------------------------------------

/// Main tabs for the Forge operator shell.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub enum MainTab {
    Overview,
    Logs,
    Runs,
    MultiLogs,
    Inbox,
}

impl MainTab {
    pub const ORDER: [MainTab; 5] = [
        MainTab::Overview,
        MainTab::Logs,
        MainTab::Runs,
        MainTab::MultiLogs,
        MainTab::Inbox,
    ];

    #[must_use]
    pub fn label(self) -> &'static str {
        match self {
            Self::Overview => "Overview",
            Self::Logs => "Logs",
            Self::Runs => "Runs",
            Self::MultiLogs => "Multi Logs",
            Self::Inbox => "Inbox",
        }
    }

    #[must_use]
    pub fn short_label(self) -> &'static str {
        match self {
            Self::Overview => "ov",
            Self::Logs => "logs",
            Self::Runs => "runs",
            Self::MultiLogs => "multi",
            Self::Inbox => "inbox",
        }
    }
}

fn parse_main_tab_id(tab_id: &str) -> Option<MainTab> {
    let normalized = tab_id.trim().to_ascii_lowercase();
    match normalized.as_str() {
        "1" | "overview" | "ov" => Some(MainTab::Overview),
        "2" | "logs" | "log" => Some(MainTab::Logs),
        "3" | "runs" | "run" => Some(MainTab::Runs),
        "4" | "multi" | "multi-logs" | "multilogs" | "multi logs" => Some(MainTab::MultiLogs),
        "5" | "inbox" => Some(MainTab::Inbox),
        _ => None,
    }
}

fn parse_layout_id(layout_id: &str) -> Option<usize> {
    let normalized = layout_id.trim().to_ascii_lowercase();
    if normalized.is_empty() {
        return None;
    }
    if let Ok(index) = normalized.parse::<usize>() {
        if index < PANE_LAYOUTS.len() {
            return Some(index);
        }
        return None;
    }
    let mut parts = normalized.split('x');
    let rows: i32 = parts.next()?.parse().ok()?;
    let cols: i32 = parts.next()?.parse().ok()?;
    if parts.next().is_some() {
        return None;
    }
    if PANE_LAYOUTS
        .iter()
        .any(|layout| layout.rows == rows && layout.cols == cols)
    {
        Some(layout_index_for(rows, cols))
    } else {
        None
    }
}

// ---------------------------------------------------------------------------
// UiMode
// ---------------------------------------------------------------------------

/// UI mode – which interaction mode is active.
/// Matches Go's `uiMode` constants.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum UiMode {
    Main,
    Palette,
    Filter,
    RegexSearch,
    ExpandedLogs,
    Confirm,
    Wizard,
    Help,
    Search,
}

// ---------------------------------------------------------------------------
// StatusKind
// ---------------------------------------------------------------------------

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum StatusKind {
    Info,
    Ok,
    Err,
}

#[derive(Debug, Clone, PartialEq, Eq)]
struct NotificationEvent {
    kind: StatusKind,
    text: String,
    acknowledged: bool,
    escalated: bool,
    snoozed_until_sequence: Option<u64>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
struct NavigationReturnPoint {
    tab: MainTab,
    selected_id: String,
    selected_run: usize,
    log_source: LogSource,
    log_layer: LogLayer,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct NotificationCenterEntry {
    pub kind: StatusKind,
    pub text: String,
    pub acknowledged: bool,
    pub escalated: bool,
    pub snoozed: bool,
}

// ---------------------------------------------------------------------------
// FilterFocus
// ---------------------------------------------------------------------------

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum FilterFocus {
    Text,
    Status,
}

// ---------------------------------------------------------------------------
// DensityMode / FocusMode
// ---------------------------------------------------------------------------

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum DensityMode {
    Comfortable,
    Compact,
}

impl DensityMode {
    pub const ORDER: [DensityMode; 2] = [DensityMode::Comfortable, DensityMode::Compact];

    #[must_use]
    pub fn label(self) -> &'static str {
        match self {
            Self::Comfortable => "comfortable",
            Self::Compact => "compact",
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum FocusMode {
    Standard,
    DeepDebug,
}

impl FocusMode {
    #[must_use]
    pub fn label(self) -> &'static str {
        match self {
            Self::Standard => "standard",
            Self::DeepDebug => "deep",
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum AccessibilityQuickMode {
    Contrast,
    Typography,
    MotionReduced,
}

impl AccessibilityQuickMode {
    const ORDER: [AccessibilityQuickMode; 3] = [
        AccessibilityQuickMode::Contrast,
        AccessibilityQuickMode::Typography,
        AccessibilityQuickMode::MotionReduced,
    ];

    #[must_use]
    pub fn label(self) -> &'static str {
        match self {
            Self::Contrast => "contrast",
            Self::Typography => "typography",
            Self::MotionReduced => "motion-reduced",
        }
    }
}

// ---------------------------------------------------------------------------
// ActionType
// ---------------------------------------------------------------------------

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ActionType {
    None,
    Stop,
    Kill,
    Delete,
    Resume,
    Create,
}

// ---------------------------------------------------------------------------
// LogSource / LogLayer
// ---------------------------------------------------------------------------

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum LogSource {
    Live,
    LatestRun,
    RunSelection,
}

impl LogSource {
    pub const ORDER: [LogSource; 3] = [
        LogSource::Live,
        LogSource::LatestRun,
        LogSource::RunSelection,
    ];

    #[must_use]
    pub fn label(self) -> &'static str {
        match self {
            Self::Live => "live",
            Self::LatestRun => "latest-run",
            Self::RunSelection => "selected-run",
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum LogLayer {
    Raw,
    Events,
    Errors,
    Tools,
    Diff,
}

impl LogLayer {
    pub const ORDER: [LogLayer; 5] = [
        LogLayer::Raw,
        LogLayer::Events,
        LogLayer::Errors,
        LogLayer::Tools,
        LogLayer::Diff,
    ];

    #[must_use]
    pub fn label(self) -> &'static str {
        match self {
            Self::Raw => "raw",
            Self::Events => "events",
            Self::Errors => "errors",
            Self::Tools => "tools",
            Self::Diff => "diff",
        }
    }
}

// ---------------------------------------------------------------------------
// LoopView / RunView / LogTailView – view-model data
// ---------------------------------------------------------------------------

/// Minimal loop data shown in the loop list. Matches Go's `loopView`.
#[derive(Debug, Clone, Default)]
pub struct LoopView {
    pub id: String,
    /// Preferred short id for display; falls back to `id` truncated to 8.
    pub short_id: String,
    pub name: String,
    pub state: String,
    pub repo_path: String,
    pub runs: usize,
    pub queue_depth: usize,
    /// RFC3339 UTC timestamp (already formatted), or `None`.
    pub last_run_at: Option<String>,
    pub interval_seconds: i64,
    pub max_runtime_seconds: i64,
    pub max_iterations: i64,
    pub last_error: String,
    pub profile_name: String,
    pub profile_harness: String,
    pub profile_auth: String,
    pub profile_id: String,
    pub pool_name: String,
    pub pool_id: String,
}

/// A single run entry. Matches Go's `runView`.
#[derive(Debug, Clone, Default)]
pub struct RunView {
    pub id: String,
    pub status: String,
    pub exit_code: Option<i32>,
    /// Preformatted duration (e.g. "12s", "1m0s", "running", "-").
    pub duration: String,
    pub profile_name: String,
    pub profile_id: String,
    pub harness: String,
    pub auth_kind: String,
    /// RFC3339 UTC timestamp when run started.
    pub started_at: String,
    /// Parsed output tail lines (newest window), used by Runs sticky output pane.
    pub output_lines: Vec<String>,
}

/// Tail view of log content. Matches Go's `logTailView`.
#[derive(Debug, Clone, Default)]
pub struct LogTailView {
    pub lines: Vec<String>,
    pub message: String,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum InboxFilter {
    All,
    Unread,
    AckRequired,
}

impl InboxFilter {
    const ORDER: [InboxFilter; 3] = [
        InboxFilter::All,
        InboxFilter::Unread,
        InboxFilter::AckRequired,
    ];

    #[must_use]
    pub fn label(self) -> &'static str {
        match self {
            Self::All => "all",
            Self::Unread => "unread",
            Self::AckRequired => "ack-required",
        }
    }
}

#[derive(Debug, Clone, Default)]
pub struct InboxMessageView {
    pub id: i64,
    pub thread_id: Option<String>,
    pub from: String,
    pub subject: String,
    pub body: String,
    pub created_at: String,
    pub ack_required: bool,
    pub read_at: Option<String>,
    pub acked_at: Option<String>,
}

#[derive(Debug, Clone, Default)]
struct InboxThreadView {
    thread_key: String,
    message_indices: Vec<usize>,
    subject: String,
    latest_created_at: String,
    latest_message_id: i64,
    unread_count: usize,
    pending_ack_count: usize,
    participant_count: usize,
}

#[derive(Debug, Clone, Default)]
pub struct ClaimEventView {
    pub task_id: String,
    pub claimed_by: String,
    pub claimed_at: String,
}

#[derive(Debug, Clone, Default)]
struct ClaimConflictView {
    task_id: String,
    latest_by: String,
    previous_by: String,
    latest_at: String,
}

#[derive(Debug, Clone, Default)]
struct HandoffSnapshotView {
    thread_key: String,
    task_id: String,
    loop_id: String,
    status: String,
    context: String,
    links: String,
    pending_risks: String,
}

impl HandoffSnapshotView {
    fn lines(&self) -> [String; 5] {
        [
            format!("task={} loop={}", self.task_id, self.loop_id),
            format!("status: {}", self.status),
            format!("context: {}", self.context),
            format!("links: {}", self.links),
            format!("pending-risks: {}", self.pending_risks),
        ]
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum EvidenceKind {
    Error,
    Warning,
    Ack,
}

impl EvidenceKind {
    #[must_use]
    fn label(self) -> &'static str {
        match self {
            Self::Error => "ERROR",
            Self::Warning => "WARN",
            Self::Ack => "ACK",
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
struct EvidenceReturnPoint {
    tab: MainTab,
    selected_id: String,
    selected_idx: usize,
    selected_run: usize,
    log_scroll: usize,
    inbox_selected_thread: usize,
    focus_right: bool,
    multi_page: usize,
}

// ---------------------------------------------------------------------------
// ConfirmState / WizardValues / WizardState
// ---------------------------------------------------------------------------

#[derive(Debug, Clone)]
pub struct ConfirmState {
    pub action: ActionType,
    pub loop_id: String,
    pub prompt: String,
    pub force_delete: bool,
    pub selected: ConfirmRailSelection,
    pub reason: String,
    pub reason_required: bool,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ConfirmRailSelection {
    Cancel,
    Confirm,
}

#[derive(Debug, Clone, Default)]
pub struct WizardValues {
    pub name: String,
    pub name_prefix: String,
    pub count: String,
    pub pool: String,
    pub profile: String,
    pub prompt: String,
    pub prompt_msg: String,
    pub interval: String,
    pub max_runtime: String,
    pub max_iterations: String,
    pub tags: String,
}

#[derive(Debug, Clone)]
pub struct WizardState {
    pub step: usize,
    pub field: usize,
    pub values: WizardValues,
    pub error: String,
}

impl Default for WizardState {
    fn default() -> Self {
        Self {
            step: 1,
            field: 0,
            values: WizardValues {
                count: "1".to_owned(),
                ..WizardValues::default()
            },
            error: String::new(),
        }
    }
}

impl WizardState {
    /// Create a wizard state pre-populated with config defaults.
    /// Matches Go `newWizardState(defaultInterval, defaultPrompt, defaultPromptMsg)`.
    #[must_use]
    pub fn with_defaults(interval: &str, prompt: &str, prompt_msg: &str) -> Self {
        Self {
            step: 1,
            field: 0,
            values: WizardValues {
                count: "1".to_owned(),
                prompt: prompt.trim().to_owned(),
                prompt_msg: prompt_msg.trim().to_owned(),
                interval: interval.trim().to_owned(),
                ..WizardValues::default()
            },
            error: String::new(),
        }
    }
}

// ---------------------------------------------------------------------------
// Command
// ---------------------------------------------------------------------------

/// Commands returned from update handlers. Matches BubbleTea's `tea.Cmd` pattern.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Command {
    None,
    Quit,
    Fetch,
    ExportCurrentView,
    Batch(Vec<Command>),
    RunAction(ActionKind),
}

impl Command {
    #[must_use]
    pub fn is_none(&self) -> bool {
        matches!(self, Self::None)
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum ActionKind {
    Resume { loop_id: String },
    Stop { loop_id: String },
    Kill { loop_id: String },
    Delete { loop_id: String, force: bool },
    Create { wizard: Vec<(String, String)> },
}

/// Result of an asynchronous action execution. Matches Go's `actionResultMsg`.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ActionResult {
    pub kind: ActionType,
    pub loop_id: String,
    /// For create actions, the ID of the newly selected loop.
    pub selected_loop_id: String,
    /// Human-readable success message.
    pub message: String,
    /// Error message, if the action failed.
    pub error: Option<String>,
}

fn wizard_field_count(step: usize) -> usize {
    match step {
        1 => 3,
        2 => 2,
        3 => 6,
        _ => 0,
    }
}

fn wizard_field_key(step: usize, field: usize) -> Option<&'static str> {
    match step {
        1 => match field {
            0 => Some("name"),
            1 => Some("name_prefix"),
            2 => Some("count"),
            _ => None,
        },
        2 => match field {
            0 => Some("pool"),
            1 => Some("profile"),
            _ => None,
        },
        3 => match field {
            0 => Some("prompt"),
            1 => Some("prompt_msg"),
            2 => Some("interval"),
            3 => Some("max_runtime"),
            4 => Some("max_iterations"),
            5 => Some("tags"),
            _ => None,
        },
        _ => None,
    }
}

fn wizard_get<'a>(values: &'a WizardValues, key: &str) -> &'a str {
    match key {
        "name" => values.name.as_str(),
        "name_prefix" => values.name_prefix.as_str(),
        "count" => values.count.as_str(),
        "pool" => values.pool.as_str(),
        "profile" => values.profile.as_str(),
        "prompt" => values.prompt.as_str(),
        "prompt_msg" => values.prompt_msg.as_str(),
        "interval" => values.interval.as_str(),
        "max_runtime" => values.max_runtime.as_str(),
        "max_iterations" => values.max_iterations.as_str(),
        "tags" => values.tags.as_str(),
        _ => "",
    }
}

fn wizard_set(values: &mut WizardValues, key: &str, value: String) {
    match key {
        "name" => values.name = value,
        "name_prefix" => values.name_prefix = value,
        "count" => values.count = value,
        "pool" => values.pool = value,
        "profile" => values.profile = value,
        "prompt" => values.prompt = value,
        "prompt_msg" => values.prompt_msg = value,
        "interval" => values.interval = value,
        "max_runtime" => values.max_runtime = value,
        "max_iterations" => values.max_iterations = value,
        "tags" => values.tags = value,
        _ => {}
    }
}

fn parse_duration_value(raw: &str, field: &str) -> Result<(), String> {
    let value = raw.trim();
    if value.is_empty() {
        return Ok(());
    }
    if value.starts_with('-') {
        return Err(format!("{field} must be >= 0"));
    }

    let (number, unit) = if let Some(stripped) = value.strip_suffix("ms") {
        (stripped, "ms")
    } else if let Some(stripped) = value.strip_suffix('s') {
        (stripped, "s")
    } else if let Some(stripped) = value.strip_suffix('m') {
        (stripped, "m")
    } else if let Some(stripped) = value.strip_suffix('h') {
        (stripped, "h")
    } else {
        (value, "s")
    };

    if number.trim().is_empty() {
        return Err(format!("invalid {field} {raw:?}"));
    }
    if number.trim().parse::<f64>().is_err() {
        return Err(format!("invalid {field} {raw:?}"));
    }
    if unit.is_empty() {
        return Err(format!("invalid {field} {raw:?}"));
    }
    Ok(())
}

fn validate_wizard_step(step: usize, values: &WizardValues) -> Result<(), String> {
    match step {
        1 => {
            let count_raw = if values.count.trim().is_empty() {
                "1"
            } else {
                values.count.trim()
            };
            let count = count_raw
                .parse::<i64>()
                .map_err(|_| format!("invalid count {:?}", values.count))?;
            if count < 1 {
                return Err("count must be >= 1".to_owned());
            }
            if !values.name.trim().is_empty() && !values.count.trim().is_empty() && count > 1 {
                return Err("name requires count=1".to_owned());
            }
        }
        2 => {
            if !values.pool.trim().is_empty() && !values.profile.trim().is_empty() {
                return Err("use either pool or profile, not both".to_owned());
            }
        }
        3 => {
            parse_duration_value(&values.interval, "interval")?;
            parse_duration_value(&values.max_runtime, "max runtime")?;
            if !values.max_iterations.trim().is_empty() {
                let parsed =
                    values.max_iterations.trim().parse::<i64>().map_err(|_| {
                        format!("invalid max-iterations {:?}", values.max_iterations)
                    })?;
                if parsed < 0 {
                    return Err("max-iterations must be >= 0".to_owned());
                }
            }
        }
        _ => {}
    }
    Ok(())
}

// ---------------------------------------------------------------------------
// View trait
// ---------------------------------------------------------------------------

/// View-model interface for tab content panes.
pub trait View {
    fn init(&mut self) -> Command;
    fn update(&mut self, event: InputEvent) -> Command;
    fn view(&self, size: FrameSize, theme: ThemeSpec) -> RenderFrame;
}

// ---------------------------------------------------------------------------
// App
// ---------------------------------------------------------------------------

/// The loop TUI application state, matching Go's `model` struct.
pub struct App {
    // -- tab/mode state --
    pub tab: MainTab,
    pub mode: UiMode,
    pub help_return: UiMode,

    // -- loop selection --
    loops: Vec<LoopView>,
    filtered: Vec<LoopView>,
    selected_id: String,
    selected_idx: usize,
    selected_log: LogTailView,

    // -- run selection --
    run_history: Vec<RunView>,
    selected_run: usize,

    // -- log display --
    log_source: LogSource,
    log_layer: LogLayer,
    log_scroll: usize,
    pub log_lines: usize,
    follow_mode: bool,
    prev_log_line_count: usize,

    // -- focus/layout --
    focus_right: bool,
    density_mode: DensityMode,
    focus_mode: FocusMode,
    accessibility_quick_mode: AccessibilityQuickMode,
    reduced_motion: bool,
    layout_idx: usize,
    multi_page: usize,
    multi_compare_mode: bool,
    multi_logs: HashMap<String, LogTailView>,
    pinned: HashSet<String>,
    inbox_messages: Vec<InboxMessageView>,
    inbox_filter: InboxFilter,
    inbox_selected_thread: usize,
    claim_events: Vec<ClaimEventView>,
    selected_claim_conflict: usize,
    handoff_snapshot: Option<HandoffSnapshotView>,
    onboarding_dismissed_tabs: HashSet<MainTab>,

    // -- filter --
    filter_text: String,
    filter_state: String,
    filter_focus: FilterFocus,
    log_regex_query: String,
    log_regex_error: String,
    log_regex_selected_match: usize,
    log_regex_compiled: Option<regex::Regex>,

    // -- confirm/wizard --
    confirm: Option<ConfirmState>,
    wizard: WizardState,

    // -- wizard defaults (from config) --
    default_interval: String,
    default_prompt: String,
    default_prompt_msg: String,

    // -- status bar --
    status_text: String,
    status_kind: StatusKind,
    clipboard_mirror: Option<String>,
    notification_queue: VecDeque<NotificationEvent>,
    notification_sequence: u64,
    action_busy: bool,

    // -- display --
    width: usize,
    height: usize,
    pub(crate) color_capability: TerminalColorCapability,
    palette: Palette,
    keymap: Keymap,
    hint_ranker: AdaptiveHintRanker,
    command_palette: CommandPalette,
    search_overlay: SearchOverlay,
    nav_history: Vec<NavigationReturnPoint>,
    evidence_return: Option<EvidenceReturnPoint>,
    quitting: bool,

    // -- view registry (for tab content) --
    views: HashMap<MainTab, Box<dyn View>>,
}

impl App {
    /// Create a new loop TUI app with the given palette name.
    #[must_use]
    pub fn new(palette_name: &str, log_lines: usize) -> Self {
        Self::new_with_capability(palette_name, TerminalColorCapability::TrueColor, log_lines)
    }

    /// Create a new loop TUI app with explicit terminal color capability.
    #[must_use]
    pub fn new_with_capability(
        palette_name: &str,
        capability: TerminalColorCapability,
        log_lines: usize,
    ) -> Self {
        let palette = resolve_palette_for_capability(palette_name, capability);
        let log_lines = if log_lines == 0 {
            DEFAULT_LOG_LINES
        } else {
            log_lines
        };

        Self {
            tab: MainTab::Overview,
            mode: UiMode::Main,
            help_return: UiMode::Main,

            loops: Vec::new(),
            filtered: Vec::new(),
            selected_id: String::new(),
            selected_idx: 0,
            selected_log: LogTailView::default(),

            run_history: Vec::new(),
            selected_run: 0,

            log_source: LogSource::Live,
            log_layer: LogLayer::Raw,
            log_scroll: 0,
            log_lines,
            follow_mode: true,
            prev_log_line_count: 0,

            focus_right: false,
            density_mode: DensityMode::Comfortable,
            focus_mode: FocusMode::Standard,
            accessibility_quick_mode: AccessibilityQuickMode::Contrast,
            reduced_motion: false,
            layout_idx: layout_index_for(2, 2),
            multi_page: 0,
            multi_compare_mode: false,
            multi_logs: HashMap::new(),
            pinned: HashSet::new(),
            inbox_messages: Vec::new(),
            inbox_filter: InboxFilter::All,
            inbox_selected_thread: 0,
            claim_events: Vec::new(),
            selected_claim_conflict: 0,
            handoff_snapshot: None,
            onboarding_dismissed_tabs: HashSet::new(),

            filter_text: String::new(),
            filter_state: "all".to_owned(),
            filter_focus: FilterFocus::Text,
            log_regex_query: String::new(),
            log_regex_error: String::new(),
            log_regex_selected_match: 0,
            log_regex_compiled: None,

            confirm: None,
            wizard: WizardState::default(),

            default_interval: String::new(),
            default_prompt: String::new(),
            default_prompt_msg: String::new(),

            status_text: String::new(),
            status_kind: StatusKind::Info,
            clipboard_mirror: None,
            notification_queue: VecDeque::new(),
            notification_sequence: 0,
            action_busy: false,

            width: 120,
            height: 40,
            color_capability: capability,
            palette,
            keymap: Keymap::default_forge_tui(),
            hint_ranker: AdaptiveHintRanker::default(),
            command_palette: CommandPalette::new_default(),
            search_overlay: SearchOverlay::new(),
            nav_history: Vec::new(),
            evidence_return: None,
            quitting: false,

            views: HashMap::new(),
        }
    }

    // -- view registration ---------------------------------------------------

    pub fn register_view(&mut self, tab: MainTab, view: Box<dyn View>) {
        self.views.insert(tab, view);
    }

    // -- accessors -----------------------------------------------------------

    #[must_use]
    pub fn tab(&self) -> MainTab {
        self.tab
    }

    #[must_use]
    pub fn mode(&self) -> UiMode {
        self.mode
    }

    #[must_use]
    pub fn width(&self) -> usize {
        self.width
    }

    #[must_use]
    pub fn height(&self) -> usize {
        self.height
    }

    #[must_use]
    pub fn palette(&self) -> &Palette {
        &self.palette
    }

    #[must_use]
    pub fn selected_id(&self) -> &str {
        &self.selected_id
    }

    #[must_use]
    pub fn selected_idx(&self) -> usize {
        self.selected_idx
    }

    #[must_use]
    pub fn loops(&self) -> &[LoopView] {
        &self.loops
    }

    #[must_use]
    pub fn filtered(&self) -> &[LoopView] {
        &self.filtered
    }

    #[must_use]
    pub fn run_history(&self) -> &[RunView] {
        &self.run_history
    }

    #[must_use]
    pub fn log_source(&self) -> LogSource {
        self.log_source
    }

    #[must_use]
    pub fn current_log_route(&self) -> LogSourceRoute {
        let transport = match self.log_source {
            LogSource::Live => LogTransportKind::LiveLoop,
            LogSource::LatestRun => LogTransportKind::LatestRun,
            LogSource::RunSelection => LogTransportKind::SelectedRun,
        };
        let content = match self.log_layer {
            LogLayer::Diff => LogContentKind::Diff,
            LogLayer::Raw | LogLayer::Events | LogLayer::Errors | LogLayer::Tools => {
                LogContentKind::Parsed
            }
        };
        LogSourceRoute::new(transport, content)
    }

    #[must_use]
    pub fn log_layer(&self) -> LogLayer {
        self.log_layer
    }

    #[must_use]
    pub fn nav_history_len(&self) -> usize {
        self.nav_history.len()
    }

    #[must_use]
    pub fn log_scroll(&self) -> usize {
        self.log_scroll
    }

    #[must_use]
    pub fn follow_mode(&self) -> bool {
        self.follow_mode
    }

    #[must_use]
    pub fn focus_right(&self) -> bool {
        self.focus_right
    }

    #[must_use]
    pub fn density_mode(&self) -> DensityMode {
        self.density_mode
    }

    #[must_use]
    pub fn accessibility_quick_mode(&self) -> AccessibilityQuickMode {
        self.accessibility_quick_mode
    }

    #[must_use]
    pub fn reduced_motion(&self) -> bool {
        self.reduced_motion
    }

    #[must_use]
    pub fn focus_mode(&self) -> FocusMode {
        self.focus_mode
    }

    #[must_use]
    pub fn is_pinned(&self, loop_id: &str) -> bool {
        !loop_id.trim().is_empty() && self.pinned.contains(loop_id)
    }

    #[must_use]
    pub fn pinned_count(&self) -> usize {
        self.pinned.len()
    }

    #[must_use]
    pub fn filter_text(&self) -> &str {
        &self.filter_text
    }

    #[must_use]
    pub fn filter_state(&self) -> &str {
        &self.filter_state
    }

    #[must_use]
    pub fn filter_focus(&self) -> FilterFocus {
        self.filter_focus
    }

    #[must_use]
    pub fn log_regex_query(&self) -> &str {
        &self.log_regex_query
    }

    #[must_use]
    pub fn log_regex_error(&self) -> &str {
        &self.log_regex_error
    }

    #[must_use]
    pub fn log_regex_match_count(&self) -> usize {
        let rendered_lines = self.rendered_log_lines();
        self.collect_regex_match_indices(&rendered_lines).len()
    }

    #[must_use]
    pub fn status_text(&self) -> &str {
        &self.status_text
    }

    #[must_use]
    pub fn status_kind(&self) -> StatusKind {
        self.status_kind
    }

    #[must_use]
    pub fn notification_queue_len(&self) -> usize {
        self.notification_queue.len()
    }

    #[must_use]
    pub fn notification_center_entries(&self) -> Vec<NotificationCenterEntry> {
        self.notification_queue
            .iter()
            .rev()
            .map(|event| NotificationCenterEntry {
                kind: event.kind,
                text: event.text.clone(),
                acknowledged: event.acknowledged,
                escalated: event.escalated,
                snoozed: self.notification_event_is_snoozed(event),
            })
            .collect()
    }

    #[must_use]
    pub fn layout_perf_hud_snapshot(&self) -> crate::layout_perf_hud::LayoutInspectorSnapshot {
        let content_start_row = if self.focus_mode == FocusMode::DeepDebug {
            1
        } else {
            2
        };
        let footer_rows = if self.status_text.is_empty() { 1 } else { 2 };
        let content_height = self
            .height
            .saturating_sub(content_start_row + footer_rows)
            .max(1);
        let split_focus_supported = self.supports_split_focus_graph();
        let focus_graph_nodes = if split_focus_supported {
            vec!["left".to_owned(), "right".to_owned()]
        } else {
            vec!["main".to_owned()]
        };
        let focused_node = if split_focus_supported {
            if self.focus_right {
                "right"
            } else {
                "left"
            }
        } else {
            "main"
        };
        crate::layout_perf_hud::LayoutInspectorSnapshot {
            tab: self.tab,
            mode: self.mode,
            frame_width: self.width,
            frame_height: self.height,
            content_start_row,
            content_height,
            requested_layout: self.current_layout(),
            effective_layout: self.effective_multi_layout(),
            density_mode: self.density_mode,
            focus_mode: self.focus_mode,
            focus_right: self.focus_right,
            split_focus_supported,
            focus_graph_nodes,
            focused_node: focused_node.to_owned(),
        }
    }

    #[must_use]
    pub fn confirm(&self) -> Option<&ConfirmState> {
        self.confirm.as_ref()
    }

    #[must_use]
    pub fn wizard(&self) -> &WizardState {
        &self.wizard
    }

    #[must_use]
    pub fn quitting(&self) -> bool {
        self.quitting
    }

    #[must_use]
    pub fn action_busy(&self) -> bool {
        self.action_busy
    }

    #[must_use]
    pub fn clipboard_mirror(&self) -> Option<&str> {
        self.clipboard_mirror.as_deref()
    }

    #[must_use]
    pub fn selected_log(&self) -> &LogTailView {
        &self.selected_log
    }

    #[must_use]
    pub fn palette_query(&self) -> &str {
        self.command_palette.query()
    }

    #[must_use]
    pub fn palette_match_count(&self) -> usize {
        self.command_palette.matches().len()
    }

    fn palette_context(&self) -> PaletteContext {
        PaletteContext {
            tab: self.tab,
            has_selection: self.selected_view().is_some(),
        }
    }

    fn key_scope_chain(&self) -> [KeyScope; 3] {
        let mode_scope = match self.mode {
            UiMode::Main => ModeScope::Main,
            UiMode::Filter => ModeScope::Filter,
            UiMode::RegexSearch => ModeScope::Search,
            UiMode::ExpandedLogs => ModeScope::ExpandedLogs,
            UiMode::Confirm => ModeScope::Confirm,
            UiMode::Wizard => ModeScope::Wizard,
            UiMode::Help => ModeScope::Help,
            UiMode::Palette => ModeScope::Palette,
            UiMode::Search => ModeScope::Search,
        };
        [
            KeyScope::View(self.tab),
            KeyScope::Mode(mode_scope),
            KeyScope::Global,
        ]
    }

    fn resolve_key_command(&self, key: KeyEvent) -> Option<KeyCommand> {
        self.keymap
            .resolve(&self.key_scope_chain(), KeyChord::from_event(key))
    }

    #[must_use]
    pub fn multi_logs(&self) -> &HashMap<String, LogTailView> {
        &self.multi_logs
    }

    #[must_use]
    pub fn multi_page(&self) -> usize {
        self.multi_page
    }

    #[must_use]
    pub fn multi_compare_mode(&self) -> bool {
        self.multi_compare_mode
    }

    #[must_use]
    pub fn inbox_filter(&self) -> InboxFilter {
        self.inbox_filter
    }

    #[must_use]
    pub fn inbox_messages(&self) -> &[InboxMessageView] {
        &self.inbox_messages
    }

    pub fn set_inbox_messages(&mut self, messages: Vec<InboxMessageView>) {
        self.inbox_messages = messages;
        self.handoff_snapshot = None;
        self.clamp_inbox_selection();
    }

    #[must_use]
    pub fn claim_events(&self) -> &[ClaimEventView] {
        &self.claim_events
    }

    pub fn set_claim_events(&mut self, events: Vec<ClaimEventView>) {
        self.claim_events = events;
        self.claim_events.sort_by(|a, b| {
            b.claimed_at
                .cmp(&a.claimed_at)
                .then(a.task_id.cmp(&b.task_id))
        });
        let conflicts = self.claim_conflicts();
        if conflicts.is_empty() {
            self.selected_claim_conflict = 0;
        } else {
            self.selected_claim_conflict = self
                .selected_claim_conflict
                .min(conflicts.len().saturating_sub(1));
        }
    }

    pub fn set_layout_idx(&mut self, idx: usize) {
        self.layout_idx = idx;
    }

    #[must_use]
    pub fn active_layout(&self) -> PaneLayout {
        self.current_layout()
    }

    #[must_use]
    pub fn session_restore_context(&self) -> crate::session_restore::SessionContext {
        let mut pinned_loop_ids = self.pinned.iter().cloned().collect::<Vec<_>>();
        pinned_loop_ids.sort();
        crate::session_restore::SessionContext {
            selected_loop_id: if self.selected_id.trim().is_empty() {
                None
            } else {
                Some(self.selected_id.clone())
            },
            selected_run_id: self.selected_run_view().map(|run| run.id.clone()),
            log_scroll: self.log_scroll,
            tab_id: Some(self.tab.short_label().to_owned()),
            layout_id: Some(self.active_layout().label()),
            filter_state: if self.filter_state.trim().is_empty() {
                None
            } else {
                Some(self.filter_state.clone())
            },
            filter_query: if self.filter_text.trim().is_empty() {
                None
            } else {
                Some(self.filter_text.clone())
            },
            panes: vec![
                crate::session_restore::PaneSelection {
                    pane_id: "left".to_owned(),
                    focused: !self.focus_right,
                },
                crate::session_restore::PaneSelection {
                    pane_id: "right".to_owned(),
                    focused: self.focus_right,
                },
            ],
            pinned_loop_ids,
        }
    }

    pub fn restore_from_session_context(
        &mut self,
        context: &crate::session_restore::SessionContext,
    ) -> Vec<String> {
        let mut notices = Vec::new();

        if let Some(tab_id) = context.tab_id.as_deref() {
            if let Some(tab) = parse_main_tab_id(tab_id) {
                self.set_tab(tab);
            } else {
                notices.push(format!("stored tab unavailable: {tab_id}"));
            }
        }

        if let Some(layout_id) = context.layout_id.as_deref() {
            if let Some(layout_idx) = parse_layout_id(layout_id) {
                self.layout_idx = layout_idx;
            } else {
                notices.push(format!("stored layout unavailable: {layout_id}"));
            }
        }

        if let Some(filter_state) = context.filter_state.as_deref() {
            let normalized = filter_state.trim().to_ascii_lowercase();
            if FILTER_STATUS_OPTIONS
                .iter()
                .any(|option| *option == normalized)
            {
                self.filter_state = normalized;
            } else {
                notices.push(format!("stored filter-state unavailable: {filter_state}"));
            }
        }
        self.filter_text = context.filter_query.clone().unwrap_or_default();

        if let Some(selected_loop_id) = context.selected_loop_id.as_deref() {
            let previous = self.selected_id.clone();
            self.select_loop_by_id(selected_loop_id);
            if !self
                .selected_id
                .trim()
                .eq_ignore_ascii_case(selected_loop_id.trim())
            {
                self.selected_id = previous;
                notices.push(format!(
                    "stored loop unavailable: {}",
                    selected_loop_id.trim()
                ));
            }
        }

        if let Some(selected_run_id) = context.selected_run_id.as_deref() {
            if let Some(index) = self
                .run_history
                .iter()
                .position(|run| run.id.trim() == selected_run_id.trim())
            {
                self.selected_run = index;
            } else {
                notices.push(format!(
                    "stored run unavailable: {}",
                    selected_run_id.trim()
                ));
            }
        }

        if let Some(focused) = context.panes.iter().find(|pane| pane.focused) {
            match focused.pane_id.trim().to_ascii_lowercase().as_str() {
                "right" => self.focus_right = true,
                "left" => self.focus_right = false,
                _ => notices.push(format!(
                    "stored pane focus unavailable: {}",
                    focused.pane_id.trim()
                )),
            }
        }

        let available_ids = self
            .loops
            .iter()
            .map(|loop_view| loop_view.id.trim().to_ascii_lowercase())
            .collect::<HashSet<_>>();
        self.pinned = context
            .pinned_loop_ids
            .iter()
            .map(|id| id.trim().to_ascii_lowercase())
            .filter(|id| !id.is_empty() && available_ids.contains(id))
            .collect();

        self.log_scroll = context.log_scroll.min(MAX_LOG_BACKFILL);
        self.follow_mode = self.log_scroll == 0;
        notices
    }

    fn inbox_threads(&self) -> Vec<InboxThreadView> {
        let mut grouped: HashMap<String, InboxThreadView> = HashMap::new();
        for (idx, message) in self.inbox_messages.iter().enumerate() {
            let thread_key = inbox_thread_key(message);
            let entry = grouped
                .entry(thread_key.clone())
                .or_insert_with(|| InboxThreadView {
                    thread_key: thread_key.clone(),
                    ..InboxThreadView::default()
                });

            entry.message_indices.push(idx);
            let candidate_subject = if message.subject.trim().is_empty() {
                "(no subject)".to_owned()
            } else {
                message.subject.trim().to_owned()
            };
            if entry.subject.is_empty() {
                entry.subject = candidate_subject.clone();
            }
            if message.created_at >= entry.latest_created_at {
                entry.latest_created_at = message.created_at.clone();
                entry.latest_message_id = message.id;
                entry.subject = candidate_subject;
            }
            if message.read_at.is_none() {
                entry.unread_count += 1;
            }
            if message.ack_required && message.acked_at.is_none() {
                entry.pending_ack_count += 1;
            }
        }

        let mut threads: Vec<InboxThreadView> = grouped
            .into_values()
            .map(|mut thread| {
                thread.message_indices.sort_by(|a, b| {
                    self.inbox_messages[*a]
                        .created_at
                        .cmp(&self.inbox_messages[*b].created_at)
                });
                let mut participants = HashSet::new();
                for index in &thread.message_indices {
                    participants.insert(self.inbox_messages[*index].from.trim().to_owned());
                }
                thread.participant_count = participants.len();
                thread
            })
            .collect();

        threads.retain(|thread| match self.inbox_filter {
            InboxFilter::All => true,
            InboxFilter::Unread => thread.unread_count > 0,
            InboxFilter::AckRequired => thread.pending_ack_count > 0,
        });
        threads.sort_by(|a, b| {
            b.latest_created_at
                .cmp(&a.latest_created_at)
                .then(b.latest_message_id.cmp(&a.latest_message_id))
        });
        threads
    }

    fn clamp_inbox_selection(&mut self) {
        let total = self.inbox_threads().len();
        if total == 0 {
            self.inbox_selected_thread = 0;
            return;
        }
        self.inbox_selected_thread = self.inbox_selected_thread.min(total.saturating_sub(1));
    }

    fn move_inbox_selection(&mut self, delta: i32) {
        let total = self.inbox_threads().len();
        if total == 0 {
            self.inbox_selected_thread = 0;
            return;
        }
        let mut idx = self.inbox_selected_thread as i32 + delta;
        if idx < 0 {
            idx = 0;
        }
        if idx >= total as i32 {
            idx = total as i32 - 1;
        }
        self.inbox_selected_thread = idx as usize;
    }

    fn cycle_inbox_filter(&mut self, delta: i32) {
        let mut idx = 0i32;
        for (i, option) in InboxFilter::ORDER.iter().enumerate() {
            if *option == self.inbox_filter {
                idx = i as i32;
                break;
            }
        }
        idx += delta;
        while idx < 0 {
            idx += InboxFilter::ORDER.len() as i32;
        }
        self.inbox_filter = InboxFilter::ORDER[(idx as usize) % InboxFilter::ORDER.len()];
        self.clamp_inbox_selection();
        self.set_status(
            StatusKind::Info,
            &format!("Inbox filter: {}", self.inbox_filter.label()),
        );
    }

    fn mark_selected_inbox_thread_read(&mut self) {
        let threads = self.inbox_threads();
        let Some(thread) = threads.get(self.inbox_selected_thread) else {
            self.set_status(StatusKind::Info, "Inbox is empty");
            return;
        };

        let mut marked = 0usize;
        for index in &thread.message_indices {
            if let Some(message) = self.inbox_messages.get_mut(*index) {
                if message.read_at.is_none() {
                    message.read_at = Some("now".to_owned());
                    marked += 1;
                }
            }
        }
        if marked == 0 {
            self.set_status(StatusKind::Info, "Thread already read");
        } else {
            self.set_status(StatusKind::Ok, &format!("Marked {marked} message(s) read"));
        }
        self.clamp_inbox_selection();
    }

    fn acknowledge_selected_inbox_message(&mut self) {
        let threads = self.inbox_threads();
        let Some(thread) = threads.get(self.inbox_selected_thread) else {
            self.set_status(StatusKind::Info, "Inbox is empty");
            return;
        };

        let mut acked_id = None;
        for index in thread.message_indices.iter().rev() {
            if let Some(message) = self.inbox_messages.get_mut(*index) {
                if message.ack_required && message.acked_at.is_none() {
                    message.acked_at = Some("now".to_owned());
                    acked_id = Some(message.id);
                    break;
                }
            }
        }

        if let Some(id) = acked_id {
            self.set_status(
                StatusKind::Ok,
                &format!("Acknowledged {}", format_mail_id(id)),
            );
        } else {
            self.set_status(StatusKind::Info, "No pending ack in selected thread");
        }
        self.clamp_inbox_selection();
    }

    fn quick_reply_selected_inbox_thread(&mut self) {
        let threads = self.inbox_threads();
        let Some(thread) = threads.get(self.inbox_selected_thread) else {
            self.set_status(StatusKind::Info, "Inbox is empty");
            return;
        };
        let Some(latest_index) = thread.message_indices.last().copied() else {
            self.set_status(StatusKind::Info, "Inbox is empty");
            return;
        };
        let message = &self.inbox_messages[latest_index];
        let target = message.from.trim();
        let target = if target.is_empty() { "unknown" } else { target };
        self.set_status(
            StatusKind::Info,
            &format!(
                "Reply shortcut: to {target}, thread {}, reply-to {}",
                thread.thread_key,
                format_mail_id(message.id)
            ),
        );
    }

    fn claim_conflicts(&self) -> Vec<ClaimConflictView> {
        let mut by_task: HashMap<String, Vec<&ClaimEventView>> = HashMap::new();
        for event in &self.claim_events {
            if event.task_id.trim().is_empty() {
                continue;
            }
            by_task
                .entry(event.task_id.clone())
                .or_default()
                .push(event);
        }

        let mut conflicts = Vec::new();
        for (task_id, events) in by_task {
            if events.len() < 2 {
                continue;
            }
            let latest = events[0];
            let previous = events.iter().skip(1).find(|event| {
                !event.claimed_by.trim().is_empty() && event.claimed_by != latest.claimed_by
            });
            let Some(previous) = previous else {
                continue;
            };
            conflicts.push(ClaimConflictView {
                task_id,
                latest_by: latest.claimed_by.clone(),
                previous_by: previous.claimed_by.clone(),
                latest_at: latest.claimed_at.clone(),
            });
        }

        conflicts.sort_by(|a, b| {
            b.latest_at
                .cmp(&a.latest_at)
                .then(a.task_id.cmp(&b.task_id))
        });
        conflicts
    }

    fn cycle_claim_conflict(&mut self, delta: i32) {
        let conflicts = self.claim_conflicts();
        if conflicts.is_empty() {
            self.selected_claim_conflict = 0;
            self.set_status(StatusKind::Info, "No claim ownership conflicts");
            return;
        }
        let len = conflicts.len() as i32;
        let mut idx = self.selected_claim_conflict as i32 + delta;
        while idx < 0 {
            idx += len;
        }
        self.selected_claim_conflict = (idx as usize) % conflicts.len();
        let conflict = &conflicts[self.selected_claim_conflict];
        self.set_status(
            StatusKind::Err,
            &format!(
                "Claim conflict {}: {} vs {}",
                conflict.task_id, conflict.latest_by, conflict.previous_by
            ),
        );
    }

    fn show_claim_resolution_hint(&mut self) {
        let conflicts = self.claim_conflicts();
        let Some(conflict) = conflicts.get(self.selected_claim_conflict) else {
            self.set_status(StatusKind::Info, "No claim conflicts to resolve");
            return;
        };
        self.set_status(
            StatusKind::Info,
            &format!(
                "Resolve {}: confirm owner, then post `fmail send task \"takeover claim: {} by <agent>\"`",
                conflict.task_id, conflict.task_id
            ),
        );
    }

    fn extract_task_id_from_thread(&self, thread: &InboxThreadView) -> Option<String> {
        for index in thread.message_indices.iter().rev() {
            let Some(message) = self.inbox_messages.get(*index) else {
                continue;
            };
            if let Some(task_id) = extract_prefixed_token(&message.subject, "forge-")
                .or_else(|| extract_prefixed_token(&message.body, "forge-"))
            {
                return Some(task_id);
            }
        }
        None
    }

    fn extract_loop_id_from_thread(&self, thread: &InboxThreadView) -> Option<String> {
        for index in thread.message_indices.iter().rev() {
            let Some(message) = self.inbox_messages.get(*index) else {
                continue;
            };
            if let Some(loop_id) = extract_prefixed_token(&message.subject, "loop-")
                .or_else(|| extract_prefixed_token(&message.body, "loop-"))
            {
                return Some(loop_id);
            }
        }
        None
    }

    fn loop_state_for_handoff(&self, loop_id: &str) -> Option<String> {
        if loop_id.trim().is_empty() {
            return None;
        }
        self.loops
            .iter()
            .find(|view| view.id == loop_id)
            .map(|view| view.state.trim().to_owned())
            .filter(|state| !state.is_empty())
    }

    fn generate_handoff_snapshot(&mut self) {
        let threads = self.inbox_threads();
        let Some(thread) = threads.get(self.inbox_selected_thread) else {
            self.set_status(StatusKind::Info, "Inbox is empty");
            return;
        };
        let Some(latest_index) = thread.message_indices.last().copied() else {
            self.set_status(StatusKind::Info, "Inbox is empty");
            return;
        };
        let Some(latest_message) = self.inbox_messages.get(latest_index) else {
            self.set_status(StatusKind::Info, "Inbox is empty");
            return;
        };

        let claim_conflicts = self.claim_conflicts();
        let task_id = self
            .extract_task_id_from_thread(thread)
            .or_else(|| {
                claim_conflicts
                    .get(self.selected_claim_conflict)
                    .map(|conflict| conflict.task_id.clone())
            })
            .or_else(|| self.claim_events.first().map(|event| event.task_id.clone()))
            .unwrap_or_else(|| "unknown-task".to_owned());

        let loop_id = self
            .extract_loop_id_from_thread(thread)
            .or_else(|| self.selected_view().map(|view| view.id.clone()))
            .unwrap_or_else(|| "unknown-loop".to_owned());

        let loop_state = self
            .loop_state_for_handoff(&loop_id)
            .unwrap_or_else(|| "unknown".to_owned());
        let from = latest_message.from.trim();
        let from = if from.is_empty() { "unknown" } else { from };
        let status = format!(
            "loop={loop_state} unread={} pending-ack={}",
            thread.unread_count, thread.pending_ack_count
        );
        let context = format!(
            "thread={} latest={} from={} messages={} participants={}",
            thread.thread_key,
            format_mail_id(latest_message.id),
            from,
            thread.message_indices.len(),
            thread.participant_count
        );
        let links = format!(
            "task:sv task show {} | loop:forge logs {} | mail:fmail log task -n 200 | rg {}",
            task_id, loop_id, thread.thread_key
        );

        let mut risks = Vec::new();
        if thread.unread_count > 0 {
            risks.push(format!("unread messages={}", thread.unread_count));
        }
        if thread.pending_ack_count > 0 {
            risks.push(format!("ack pending={}", thread.pending_ack_count));
        }
        if loop_id == "unknown-loop" {
            risks.push("loop mapping missing".to_owned());
        }
        if loop_state.eq_ignore_ascii_case("error") {
            risks.push("loop state=error".to_owned());
        }
        if let Some(conflict) = claim_conflicts
            .iter()
            .find(|conflict| conflict.task_id == task_id)
        {
            risks.push(format!(
                "ownership conflict: {} vs {}",
                conflict.latest_by, conflict.previous_by
            ));
        }
        let pending_risks = if risks.is_empty() {
            "none".to_owned()
        } else {
            risks.join("; ")
        };

        self.handoff_snapshot = Some(HandoffSnapshotView {
            thread_key: thread.thread_key.clone(),
            task_id: task_id.clone(),
            loop_id: loop_id.clone(),
            status,
            context,
            links,
            pending_risks,
        });
        self.set_status(
            StatusKind::Ok,
            &format!("Handoff snapshot ready: task {task_id}, loop {loop_id}"),
        );
    }

    // -- data setters (called from refresh/tick) -----------------------------

    pub fn set_loops(&mut self, loops: Vec<LoopView>) {
        let old_id = self.selected_id.clone();
        let old_idx = self.selected_idx;
        self.loops = loops;
        self.apply_filters(&old_id, old_idx);
    }

    pub fn set_selected_log(&mut self, log: LogTailView) {
        let new_count = log.lines.len();
        // In follow mode, keep scroll pinned to bottom when new lines arrive.
        if self.follow_mode && new_count != self.prev_log_line_count {
            self.log_scroll = 0;
        }
        self.prev_log_line_count = new_count;
        self.selected_log = log;
    }

    pub fn set_run_history(&mut self, runs: Vec<RunView>) {
        self.run_history = runs;
        if self.run_history.is_empty() {
            self.selected_run = 0;
            self.log_source = LogSource::Live;
        } else if self.selected_run >= self.run_history.len() {
            self.selected_run = self.run_history.len() - 1;
        }
    }

    pub fn set_multi_logs(&mut self, logs: HashMap<String, LogTailView>) {
        self.multi_logs = logs;
    }

    pub fn set_action_busy(&mut self, busy: bool) {
        self.action_busy = busy;
    }

    fn copy_clipboard_context(&mut self) {
        let (label, value) = match self.tab {
            MainTab::Runs => {
                let Some(run) = self.run_history.get(
                    self.selected_run
                        .min(self.run_history.len().saturating_sub(1)),
                ) else {
                    self.set_status(StatusKind::Info, "Clipboard: no run selected");
                    return;
                };
                ("run id", run.id.trim().to_owned())
            }
            MainTab::Logs => {
                if self.selected_log.lines.is_empty() {
                    self.set_status(StatusKind::Info, "Clipboard: no log lines");
                    return;
                }
                let last = self.selected_log.lines.len().saturating_sub(1);
                let idx = last.saturating_sub(self.log_scroll.min(last));
                ("log line", self.selected_log.lines[idx].clone())
            }
            MainTab::Inbox => {
                let threads = self.inbox_threads();
                let Some(thread) = threads.get(self.inbox_selected_thread) else {
                    self.set_status(StatusKind::Info, "Clipboard: inbox is empty");
                    return;
                };
                let Some(latest_index) = thread.message_indices.last().copied() else {
                    self.set_status(StatusKind::Info, "Clipboard: inbox is empty");
                    return;
                };
                let Some(message) = self.inbox_messages.get(latest_index) else {
                    self.set_status(StatusKind::Info, "Clipboard: inbox is empty");
                    return;
                };
                let value = if message.body.trim().is_empty() {
                    message.subject.trim().to_owned()
                } else {
                    message.body.trim().to_owned()
                };
                ("thread content", value)
            }
            _ => {
                self.set_status(
                    StatusKind::Info,
                    "Clipboard: use Ctrl+Y in Runs, Logs, or Inbox",
                );
                return;
            }
        };

        let trimmed = value.trim();
        if trimmed.is_empty() {
            self.set_status(StatusKind::Info, "Clipboard: selected content is empty");
            return;
        }
        self.clipboard_mirror = Some(trimmed.to_owned());

        if copy_to_system_clipboard(trimmed) {
            self.set_status(StatusKind::Ok, &format!("Copied {label} to clipboard"));
        } else {
            self.set_status(
                StatusKind::Info,
                &format!("Clipboard unavailable; mirrored {label} in app state"),
            );
        }
    }

    pub fn clear_status(&mut self) {
        self.status_text.clear();
        self.notification_queue.clear();
    }

    pub fn advance_notification_clock(&mut self, ticks: u64) {
        self.notification_sequence = self.notification_sequence.saturating_add(ticks);
    }

    pub fn notification_center_ack_latest(&mut self) -> bool {
        if let Some(event) = self
            .notification_queue
            .iter_mut()
            .rev()
            .find(|event| !event.acknowledged)
        {
            event.acknowledged = true;
            return true;
        }
        false
    }

    pub fn notification_center_escalate_latest(&mut self) -> bool {
        if let Some(event) = self
            .notification_queue
            .iter_mut()
            .rev()
            .find(|event| !event.acknowledged)
        {
            event.escalated = true;
            return true;
        }
        false
    }

    pub fn notification_center_snooze_latest(&mut self, ticks: u64) -> bool {
        let ticks = ticks.max(1);
        let wake_sequence = self.notification_sequence.saturating_add(ticks);
        if let Some(event) = self
            .notification_queue
            .iter_mut()
            .rev()
            .find(|event| !event.acknowledged)
        {
            event.snoozed_until_sequence = Some(wake_sequence);
            return true;
        }
        false
    }

    fn navigation_return_point(&self) -> NavigationReturnPoint {
        NavigationReturnPoint {
            tab: self.tab,
            selected_id: self.selected_id.clone(),
            selected_run: self.selected_run,
            log_source: self.log_source,
            log_layer: self.log_layer,
        }
    }

    fn push_navigation_return_point(&mut self) {
        let point = self.navigation_return_point();
        if self.nav_history.last().is_some_and(|last| *last == point) {
            return;
        }
        if self.nav_history.len() >= MAX_NAV_HISTORY {
            self.nav_history.remove(0);
        }
        self.nav_history.push(point);
    }

    fn pop_navigation_return_point(&mut self) -> bool {
        let Some(point) = self.nav_history.pop() else {
            self.set_status(StatusKind::Info, "Backtrack: no navigation history");
            return false;
        };
        self.set_tab(point.tab);
        if !point.selected_id.trim().is_empty() {
            self.select_loop_by_id(&point.selected_id);
        }
        self.selected_run = point
            .selected_run
            .min(self.run_history.len().saturating_sub(1));
        self.log_source = point.log_source;
        self.log_layer = point.log_layer;
        self.log_scroll = 0;
        self.follow_mode = true;
        self.set_status(
            StatusKind::Info,
            &format!("Backtracked to {}", self.tab.label()),
        );
        true
    }

    // -- tab management (matching Go) ----------------------------------------

    pub fn set_tab(&mut self, tab: MainTab) {
        if self.tab == tab {
            return;
        }
        self.tab = tab;
        self.log_scroll = 0;
        self.follow_mode = true;
        if tab == MainTab::MultiLogs {
            self.focus_right = true;
            self.clamp_multi_page();
        } else if tab == MainTab::Inbox {
            self.clamp_inbox_selection();
        } else if self.focus_mode == FocusMode::DeepDebug {
            self.focus_right = true;
        } else if self.focus_right {
            self.focus_right = false;
        }
    }

    pub fn cycle_tab(&mut self, delta: i32) {
        let order = &MainTab::ORDER;
        let mut idx = 0i32;
        for (i, &t) in order.iter().enumerate() {
            if t == self.tab {
                idx = i as i32;
                break;
            }
        }
        idx += delta;
        while idx < 0 {
            idx += order.len() as i32;
        }
        self.set_tab(order[(idx as usize) % order.len()]);
    }

    // -- theme ---------------------------------------------------------------

    pub fn cycle_theme(&mut self) {
        self.palette = cycle_palette(self.palette.name, 1);
        self.set_status(StatusKind::Info, &format!("Theme: {}", self.palette.name));
    }

    pub fn cycle_accessibility_theme(&mut self) {
        self.palette = cycle_accessibility_preset(self.palette.name, 1);
        self.set_status(
            StatusKind::Info,
            &format!("Accessibility preset: {}", self.palette.name),
        );
    }

    pub fn cycle_density_mode(&mut self, delta: i32) {
        let options = &DensityMode::ORDER;
        let mut idx = 0i32;
        for (i, mode) in options.iter().enumerate() {
            if *mode == self.density_mode {
                idx = i as i32;
                break;
            }
        }
        idx += delta;
        while idx < 0 {
            idx += options.len() as i32;
        }
        self.density_mode = options[(idx as usize) % options.len()];
        if self.tab == MainTab::MultiLogs {
            self.clamp_multi_page();
        }
        self.set_status(
            StatusKind::Info,
            &format!("Density: {}", self.density_mode.label()),
        );
    }

    fn supports_split_focus_graph(&self) -> bool {
        matches!(
            self.tab,
            MainTab::Overview | MainTab::Logs | MainTab::Runs | MainTab::MultiLogs | MainTab::Inbox
        )
    }

    fn traverse_focus_graph(&mut self, delta: i32) -> bool {
        if !self.supports_split_focus_graph() {
            return false;
        }
        let nodes = [false, true];
        let current = if self.focus_right { 1i32 } else { 0i32 };
        let next = (current + delta).rem_euclid(nodes.len() as i32) as usize;
        self.focus_right = nodes[next];
        if self.tab == MainTab::MultiLogs {
            self.clamp_multi_page();
        }
        self.set_status(
            StatusKind::Info,
            if self.focus_right {
                "Focus: right pane"
            } else {
                "Focus: left pane"
            },
        );
        true
    }

    pub fn cycle_accessibility_quick_mode(&mut self) {
        let mut idx = 0usize;
        for (i, mode) in AccessibilityQuickMode::ORDER.iter().enumerate() {
            if *mode == self.accessibility_quick_mode {
                idx = i;
                break;
            }
        }
        let next = AccessibilityQuickMode::ORDER[(idx + 1) % AccessibilityQuickMode::ORDER.len()];
        self.apply_accessibility_quick_mode(next);
    }

    fn apply_accessibility_quick_mode(&mut self, mode: AccessibilityQuickMode) {
        let (palette_name, density_mode, reduced_motion) = match mode {
            AccessibilityQuickMode::Contrast => ("high-contrast", DensityMode::Comfortable, false),
            AccessibilityQuickMode::Typography => ("colorblind-safe", DensityMode::Compact, false),
            AccessibilityQuickMode::MotionReduced => ("low-light", DensityMode::Comfortable, true),
        };

        self.palette = resolve_palette_for_capability(palette_name, self.color_capability);
        self.density_mode = density_mode;
        self.reduced_motion = reduced_motion;
        self.accessibility_quick_mode = mode;
        if self.tab == MainTab::MultiLogs {
            self.clamp_multi_page();
        }
        self.set_status(
            StatusKind::Info,
            &format!(
                "Accessibility mode: {} (theme:{} density:{} motion:{})",
                mode.label(),
                self.palette.name,
                self.density_mode.label(),
                if self.reduced_motion {
                    "reduced"
                } else {
                    "full"
                }
            ),
        );
    }

    #[allow(dead_code)]
    fn toggle_zen_mode(&mut self) {
        self.focus_right = !self.focus_right;
        if self.tab == MainTab::MultiLogs {
            self.clamp_multi_page();
        }
        if self.focus_right {
            self.set_status(StatusKind::Info, "Zen mode: right pane focus");
        } else {
            self.set_status(StatusKind::Info, "Zen mode: split view");
        }
    }

    #[allow(dead_code)]
    fn toggle_deep_focus_mode(&mut self) {
        self.focus_mode = if self.focus_mode == FocusMode::Standard {
            FocusMode::DeepDebug
        } else {
            FocusMode::Standard
        };
        if self.focus_mode == FocusMode::DeepDebug {
            self.focus_right = true;
        } else if self.tab != MainTab::MultiLogs {
            self.focus_right = false;
        }
        if self.tab == MainTab::MultiLogs {
            self.clamp_multi_page();
        }
        self.set_status(
            StatusKind::Info,
            &format!("Focus mode: {}", self.focus_mode.label()),
        );
    }

    // -- selection -----------------------------------------------------------

    pub fn move_selection(&mut self, delta: i32) {
        if self.filtered.is_empty() {
            self.selected_idx = 0;
            self.selected_id.clear();
            self.log_scroll = 0;
            return;
        }
        let mut idx = self.selected_idx as i32 + delta;
        if idx < 0 {
            idx = 0;
        }
        if idx >= self.filtered.len() as i32 {
            idx = (self.filtered.len() as i32) - 1;
        }
        self.selected_idx = idx as usize;
        self.selected_id = self.filtered[self.selected_idx].id.clone();
        self.log_scroll = 0;
    }

    #[must_use]
    pub fn selected_view(&self) -> Option<&LoopView> {
        if self.filtered.is_empty() {
            return None;
        }
        let idx = self.selected_idx.min(self.filtered.len().saturating_sub(1));
        Some(&self.filtered[idx])
    }

    // -- pinning -------------------------------------------------------------

    pub fn toggle_pinned(&mut self, loop_id: &str) {
        if loop_id.trim().is_empty() {
            return;
        }
        if self.pinned.contains(loop_id) {
            self.pinned.remove(loop_id);
            self.set_status(StatusKind::Info, &format!("Unpinned {loop_id}"));
        } else {
            self.pinned.insert(loop_id.to_owned());
            self.set_status(StatusKind::Info, &format!("Pinned {loop_id}"));
        }
    }

    pub fn clear_pinned(&mut self) {
        self.pinned.clear();
        self.set_status(StatusKind::Info, "Cleared pinned loops");
    }

    // -- filters -------------------------------------------------------------

    pub fn apply_filters(&mut self, previous_id: &str, previous_idx: usize) {
        let query = self.filter_text.trim().to_ascii_lowercase();
        let state = self.filter_state.trim().to_ascii_lowercase();

        let mut filtered = Vec::with_capacity(self.loops.len());
        for lv in &self.loops {
            let loop_state = lv.state.to_ascii_lowercase();
            if !state.is_empty() && state != "all" && loop_state != state {
                continue;
            }
            if !query.is_empty() {
                let id_lower = lv.id.to_ascii_lowercase();
                let name_lower = lv.name.to_ascii_lowercase();
                let repo_lower = lv.repo_path.to_ascii_lowercase();
                if !id_lower.contains(&query)
                    && !name_lower.contains(&query)
                    && !repo_lower.contains(&query)
                {
                    continue;
                }
            }
            filtered.push(lv.clone());
        }

        self.filtered = filtered;
        if self.filtered.is_empty() {
            self.selected_idx = 0;
            self.selected_id.clear();
            self.multi_page = 0;
            return;
        }

        if !previous_id.is_empty() {
            for (i, lv) in self.filtered.iter().enumerate() {
                if lv.id == previous_id {
                    self.selected_idx = i;
                    self.selected_id = previous_id.to_owned();
                    return;
                }
            }
        }

        let clamped = previous_idx.min(self.filtered.len().saturating_sub(1));
        self.selected_idx = clamped;
        self.selected_id = self.filtered[clamped].id.clone();
        self.clamp_multi_page();
    }

    pub fn cycle_filter_status(&mut self, delta: i32) {
        let mut idx = 0i32;
        for (i, &opt) in FILTER_STATUS_OPTIONS.iter().enumerate() {
            if opt == self.filter_state {
                idx = i as i32;
                break;
            }
        }
        idx += delta;
        if idx < 0 {
            idx = FILTER_STATUS_OPTIONS.len() as i32 - 1;
        }
        if idx >= FILTER_STATUS_OPTIONS.len() as i32 {
            idx = 0;
        }
        self.filter_state = FILTER_STATUS_OPTIONS[idx as usize].to_owned();
        let old_id = self.selected_id.clone();
        let old_idx = self.selected_idx;
        self.apply_filters(&old_id, old_idx);
    }

    // -- log source/layer cycling --------------------------------------------

    pub fn cycle_log_source(&mut self, delta: i32) {
        let options = &LogSource::ORDER;
        let mut idx = 0i32;
        for (i, &opt) in options.iter().enumerate() {
            if opt == self.log_source {
                idx = i as i32;
                break;
            }
        }
        idx += delta;
        while idx < 0 {
            idx += options.len() as i32;
        }
        self.log_source = options[(idx as usize) % options.len()];
        self.log_scroll = 0;
        self.follow_mode = true;
        self.set_status(
            StatusKind::Info,
            &format!("Log source: {}", self.log_source.label()),
        );
    }

    pub fn cycle_log_layer(&mut self, delta: i32) {
        let options = &LogLayer::ORDER;
        let mut idx = 0i32;
        for (i, &opt) in options.iter().enumerate() {
            if opt == self.log_layer {
                idx = i as i32;
                break;
            }
        }
        idx += delta;
        while idx < 0 {
            idx += options.len() as i32;
        }
        self.log_layer = options[(idx as usize) % options.len()];
        self.set_status(
            StatusKind::Info,
            &format!("Log layer: {}", self.log_layer.label()),
        );
    }

    // -- log scrolling -------------------------------------------------------

    pub fn scroll_logs(&mut self, delta: i32) {
        if delta >= 0 {
            self.log_scroll = self.log_scroll.saturating_add(delta as usize);
            // Scrolling away from bottom disengages follow mode.
            if self.log_scroll > 0 {
                self.follow_mode = false;
            }
        } else {
            self.log_scroll = self.log_scroll.saturating_sub((-delta) as usize);
            // Reaching bottom re-engages follow mode.
            if self.log_scroll == 0 {
                self.follow_mode = true;
            }
        }
    }

    pub fn scroll_logs_to_top(&mut self) {
        self.log_scroll = MAX_LOG_BACKFILL;
        self.follow_mode = false;
    }

    pub fn scroll_logs_to_bottom(&mut self) {
        self.log_scroll = 0;
        self.follow_mode = true;
    }

    pub fn toggle_follow_mode(&mut self) {
        self.follow_mode = !self.follow_mode;
        if self.follow_mode {
            self.log_scroll = 0;
            self.set_status(StatusKind::Info, "Follow: on");
        } else {
            self.set_status(StatusKind::Info, "Follow: off");
        }
    }

    #[must_use]
    pub fn log_scroll_page_size(&self) -> usize {
        // Approximate page based on configured viewport, not full terminal height.
        let estimate = (self.log_lines / 2).saturating_add(LOG_SCROLL_STEP);
        estimate.max(LOG_SCROLL_STEP)
    }

    // -- run selection -------------------------------------------------------

    pub fn move_run_selection(&mut self, delta: i32) {
        if self.run_history.is_empty() {
            self.selected_run = 0;
            return;
        }
        let mut idx = self.selected_run as i32 + delta;
        if idx < 0 {
            idx = 0;
        }
        if idx >= self.run_history.len() as i32 {
            idx = (self.run_history.len() as i32) - 1;
        }
        self.selected_run = idx as usize;
        self.log_scroll = 0;
    }

    #[must_use]
    pub fn selected_run_view(&self) -> Option<&RunView> {
        if self.run_history.is_empty() {
            return None;
        }
        let idx = self
            .selected_run
            .min(self.run_history.len().saturating_sub(1));
        Some(&self.run_history[idx])
    }

    fn stash_evidence_return_point(&mut self) {
        if self.evidence_return.is_some() {
            return;
        }
        self.evidence_return = Some(EvidenceReturnPoint {
            tab: self.tab,
            selected_id: self.selected_id.clone(),
            selected_idx: self.selected_idx,
            selected_run: self.selected_run,
            log_scroll: self.log_scroll,
            inbox_selected_thread: self.inbox_selected_thread,
            focus_right: self.focus_right,
            multi_page: self.multi_page,
        });
    }

    fn restore_evidence_return_point(&mut self) -> bool {
        let Some(point) = self.evidence_return.take() else {
            self.set_status(StatusKind::Info, "No sticky evidence return point");
            return false;
        };
        self.set_tab(point.tab);
        self.focus_right = point.focus_right;
        self.multi_page = point.multi_page;
        if self.tab == MainTab::MultiLogs {
            self.clamp_multi_page();
        }

        if self.filtered.is_empty() {
            self.selected_idx = 0;
            self.selected_id.clear();
        } else if !point.selected_id.is_empty() {
            if let Some(idx) = self
                .filtered
                .iter()
                .position(|loop_view| loop_view.id == point.selected_id)
            {
                self.selected_idx = idx;
                self.selected_id = point.selected_id.clone();
            } else {
                self.selected_idx = point
                    .selected_idx
                    .min(self.filtered.len().saturating_sub(1));
                self.selected_id = self.filtered[self.selected_idx].id.clone();
            }
        } else {
            self.selected_idx = point
                .selected_idx
                .min(self.filtered.len().saturating_sub(1));
            self.selected_id = self.filtered[self.selected_idx].id.clone();
        }

        if self.run_history.is_empty() {
            self.selected_run = 0;
        } else {
            self.selected_run = point
                .selected_run
                .min(self.run_history.len().saturating_sub(1));
        }

        let thread_count = self.inbox_threads().len();
        if thread_count == 0 {
            self.inbox_selected_thread = 0;
        } else {
            self.inbox_selected_thread = point
                .inbox_selected_thread
                .min(thread_count.saturating_sub(1));
        }

        self.log_scroll = point.log_scroll;
        self.follow_mode = self.log_scroll == 0;
        self.set_status(StatusKind::Info, "Returned to sticky evidence source");
        true
    }

    fn jump_to_latest_ack_evidence(&mut self) -> bool {
        let threads = self.inbox_threads();
        let Some(thread_idx) = threads
            .iter()
            .position(|thread| thread.pending_ack_count > 0)
        else {
            self.set_status(StatusKind::Info, "No pending ACK evidence found");
            return false;
        };
        self.stash_evidence_return_point();
        self.set_tab(MainTab::Inbox);
        self.inbox_selected_thread = thread_idx;
        self.clamp_inbox_selection();
        self.set_status(
            StatusKind::Info,
            "Jumped to latest ACK evidence (Ctrl+B to return)",
        );
        true
    }

    fn jump_to_latest_error_warning_evidence(&mut self, kind: EvidenceKind) -> bool {
        if let Some((line_idx, total_lines)) = self.latest_evidence_line_index(kind) {
            self.stash_evidence_return_point();
            self.log_scroll = total_lines.saturating_sub(line_idx + 1);
            self.follow_mode = self.log_scroll == 0;
            self.set_status(
                StatusKind::Info,
                &format!(
                    "Jumped to latest {} evidence line (Ctrl+B to return)",
                    kind.label()
                ),
            );
            return true;
        }

        if let Some(run_idx) = self.latest_run_index_with_evidence(kind) {
            self.stash_evidence_return_point();
            self.set_tab(MainTab::Runs);
            self.selected_run = run_idx;
            self.log_scroll = 0;
            self.follow_mode = true;
            self.set_status(
                StatusKind::Info,
                &format!(
                    "Jumped to latest {} run evidence (Ctrl+B to return)",
                    kind.label()
                ),
            );
            return true;
        }

        if let Some(loop_idx) = self.latest_loop_index_with_evidence(kind) {
            self.stash_evidence_return_point();
            self.set_tab(MainTab::Overview);
            self.selected_idx = loop_idx;
            self.selected_id = self.filtered[loop_idx].id.clone();
            self.log_scroll = 0;
            self.follow_mode = true;
            self.set_status(
                StatusKind::Info,
                &format!(
                    "Jumped to latest {} loop evidence (Ctrl+B to return)",
                    kind.label()
                ),
            );
            return true;
        }

        self.set_status(
            StatusKind::Info,
            &format!("No {} evidence found", kind.label()),
        );
        false
    }

    fn jump_to_latest_evidence(&mut self, kind: EvidenceKind) -> bool {
        match kind {
            EvidenceKind::Ack => self.jump_to_latest_ack_evidence(),
            EvidenceKind::Error | EvidenceKind::Warning => {
                self.jump_to_latest_error_warning_evidence(kind)
            }
        }
    }

    fn latest_evidence_line_index(&self, kind: EvidenceKind) -> Option<(usize, usize)> {
        let lines = self.failure_explain_source_lines()?;
        for idx in (0..lines.len()).rev() {
            if evidence_line_matches(kind, &lines[idx]) {
                return Some((idx, lines.len()));
            }
        }
        None
    }

    fn latest_run_index_with_evidence(&self, kind: EvidenceKind) -> Option<usize> {
        self.run_history.iter().enumerate().find_map(|(idx, run)| {
            if evidence_line_matches(kind, &run.status)
                || run
                    .output_lines
                    .iter()
                    .rev()
                    .any(|line| evidence_line_matches(kind, line))
            {
                Some(idx)
            } else {
                None
            }
        })
    }

    fn latest_loop_index_with_evidence(&self, kind: EvidenceKind) -> Option<usize> {
        let mut best: Option<(usize, String)> = None;
        for (idx, loop_view) in self.filtered.iter().enumerate() {
            let matches = evidence_line_matches(kind, &loop_view.state)
                || evidence_line_matches(kind, &loop_view.last_error);
            if !matches {
                continue;
            }
            let recency = loop_view.last_run_at.clone().unwrap_or_default();
            if best
                .as_ref()
                .map_or(true, |(_, best_recency)| recency > *best_recency)
            {
                best = Some((idx, recency));
            }
        }
        best.map(|(idx, _)| idx)
    }

    // -- multi-log / layout helpers ------------------------------------------

    #[must_use]
    pub fn current_layout(&self) -> PaneLayout {
        if PANE_LAYOUTS.is_empty() {
            return PaneLayout { rows: 1, cols: 1 };
        }
        PANE_LAYOUTS[normalize_layout_index(self.layout_idx as i32)]
    }

    pub fn cycle_layout(&mut self, delta: i32) {
        self.layout_idx = normalize_layout_index(self.layout_idx as i32 + delta);
        self.clamp_multi_page();
        self.set_status(
            StatusKind::Info,
            &format!("Layout: {}", self.current_layout().label()),
        );
    }

    pub fn toggle_multi_compare_mode(&mut self) {
        self.multi_compare_mode = !self.multi_compare_mode;
        self.log_scroll = 0;
        if self.multi_compare_mode {
            self.set_status(
                StatusKind::Info,
                "Multi compare: on (shared scroll + diff hints)",
            );
        } else {
            self.set_status(StatusKind::Info, "Multi compare: off");
        }
    }

    pub fn ordered_multi_target_views(&self) -> Vec<&LoopView> {
        let mut ordered: Vec<&LoopView> = Vec::with_capacity(self.filtered.len());
        let mut added = HashSet::new();

        // Pinned first.
        for lv in &self.filtered {
            if self.pinned.contains(&lv.id) {
                ordered.push(lv);
                added.insert(&lv.id);
            }
        }
        // Then unpinned.
        for lv in &self.filtered {
            if !added.contains(&lv.id) {
                ordered.push(lv);
            }
        }
        ordered
    }

    #[must_use]
    pub fn effective_multi_layout(&self) -> PaneLayout {
        let (width, height) = self.multi_viewport_size();
        let grid_height = (height - self.multi_header_rows()).max(self.multi_min_cell_height());
        fit_pane_layout_for_breakpoint(
            self.current_layout(),
            width,
            grid_height,
            self.multi_cell_gap(),
            self.multi_min_cell_width(),
            self.multi_min_cell_height(),
        )
    }

    #[must_use]
    pub(crate) fn multi_header_rows(&self) -> i32 {
        if self.focus_mode == FocusMode::DeepDebug || self.density_mode == DensityMode::Compact {
            1
        } else {
            MULTI_HEADER_ROWS
        }
    }

    #[must_use]
    pub(crate) fn multi_cell_gap(&self) -> i32 {
        if self.density_mode == DensityMode::Compact {
            0
        } else {
            MULTI_CELL_GAP
        }
    }

    #[must_use]
    pub(crate) fn multi_min_cell_width(&self) -> i32 {
        if self.density_mode == DensityMode::Compact {
            32
        } else {
            MULTI_MIN_CELL_WIDTH
        }
    }

    #[must_use]
    pub(crate) fn multi_min_cell_height(&self) -> i32 {
        if self.density_mode == DensityMode::Compact {
            6
        } else {
            MULTI_MIN_CELL_HEIGHT
        }
    }

    #[must_use]
    pub fn multi_page_size(&self) -> usize {
        self.effective_multi_layout().capacity().max(1) as usize
    }

    pub fn clamp_multi_page(&mut self) {
        let total = self.ordered_multi_target_views().len();
        let (page, _, _, _) = multi_page_bounds(total, self.multi_page_size(), self.multi_page);
        self.multi_page = page;
    }

    pub fn move_multi_page(&mut self, delta: i32) {
        let total = self.ordered_multi_target_views().len();
        let new_page = if delta >= 0 {
            self.multi_page.saturating_add(delta as usize)
        } else {
            self.multi_page.saturating_sub((-delta) as usize)
        };
        let (page, total_pages, _, _) = multi_page_bounds(total, self.multi_page_size(), new_page);
        self.multi_page = page;
        self.set_status(
            StatusKind::Info,
            &format!("Matrix page {}/{}", page + 1, total_pages),
        );
    }

    pub fn move_multi_page_to_start(&mut self) {
        self.multi_page = 0;
        self.clamp_multi_page();
    }

    pub fn move_multi_page_to_end(&mut self) {
        let total = self.ordered_multi_target_views().len();
        let (page, _, _, _) = multi_page_bounds(total, self.multi_page_size(), usize::MAX / 2);
        self.multi_page = page;
    }

    fn multi_viewport_size(&self) -> (i32, i32) {
        let width = self.width as i32;
        let height = self.height as i32;
        let base_overhead = match self.focus_mode {
            FocusMode::Standard => 4,
            FocusMode::DeepDebug => 2,
        };
        let mode_overhead = match self.mode {
            UiMode::Palette
            | UiMode::Filter
            | UiMode::RegexSearch
            | UiMode::Confirm
            | UiMode::Wizard
            | UiMode::Help => 3,
            _ => 0,
        };
        let density_adjust = if self.density_mode == DensityMode::Compact {
            1
        } else {
            0
        };
        let overhead: i32 = (base_overhead + mode_overhead - density_adjust).max(1)
            + if self.status_text.is_empty() { 0 } else { 1 };
        let pane_height = (height - overhead).max(10);
        let right_width = if self.focus_right {
            width
        } else {
            // Approximate right pane width: 60% of total for non-overview tabs.
            (width * 6 / 10).max(1)
        };
        ((right_width - 2).max(1), (pane_height - 2).max(1))
    }

    // -- status bar ----------------------------------------------------------

    pub fn set_status(&mut self, kind: StatusKind, text: &str) {
        self.status_kind = kind;
        self.status_text = text.to_owned();
        self.notification_sequence = self.notification_sequence.saturating_add(1);
        if !text.trim().is_empty() {
            if self.notification_queue.len() >= MAX_NOTIFICATION_QUEUE {
                self.notification_queue.pop_front();
            }
            self.notification_queue.push_back(NotificationEvent {
                kind,
                text: text.to_owned(),
                acknowledged: false,
                escalated: false,
                snoozed_until_sequence: None,
            });
        }
    }

    // -- wizard helpers ------------------------------------------------------

    pub fn set_wizard_defaults(&mut self, interval: &str, prompt: &str, prompt_msg: &str) {
        self.default_interval = interval.to_owned();
        self.default_prompt = prompt.to_owned();
        self.default_prompt_msg = prompt_msg.to_owned();
    }

    pub fn set_wizard(&mut self, wizard: WizardState) {
        self.wizard = wizard;
    }

    pub fn wizard_next_field(&mut self) {
        let field_count = wizard_field_count(self.wizard.step);
        if field_count == 0 {
            return;
        }
        self.wizard.field = (self.wizard.field + 1) % field_count;
    }

    pub fn wizard_prev_field(&mut self) {
        let field_count = wizard_field_count(self.wizard.step);
        if field_count == 0 {
            return;
        }
        if self.wizard.field == 0 {
            self.wizard.field = field_count - 1;
        } else {
            self.wizard.field -= 1;
        }
    }

    fn wizard_pairs(&self) -> Vec<(String, String)> {
        vec![
            ("name".to_owned(), self.wizard.values.name.clone()),
            (
                "name_prefix".to_owned(),
                self.wizard.values.name_prefix.clone(),
            ),
            ("count".to_owned(), self.wizard.values.count.clone()),
            ("pool".to_owned(), self.wizard.values.pool.clone()),
            ("profile".to_owned(), self.wizard.values.profile.clone()),
            ("prompt".to_owned(), self.wizard.values.prompt.clone()),
            (
                "prompt_msg".to_owned(),
                self.wizard.values.prompt_msg.clone(),
            ),
            ("interval".to_owned(), self.wizard.values.interval.clone()),
            (
                "max_runtime".to_owned(),
                self.wizard.values.max_runtime.clone(),
            ),
            (
                "max_iterations".to_owned(),
                self.wizard.values.max_iterations.clone(),
            ),
            ("tags".to_owned(), self.wizard.values.tags.clone()),
        ]
    }

    // -- confirm helpers -----------------------------------------------------

    pub fn enter_confirm(&mut self, action: ActionType) -> Command {
        let view = match self.selected_view() {
            Some(v) => v.clone(),
            None => {
                self.set_status(StatusKind::Info, "No loop selected");
                return Command::None;
            }
        };

        let loop_id = &view.id;
        let prompt = match action {
            ActionType::Stop => {
                format!("Stop loop {loop_id} after current iteration? [y/N]")
            }
            ActionType::Kill => {
                format!("Kill loop {loop_id} immediately? [y/N]")
            }
            ActionType::Delete => {
                if view.state == "stopped" {
                    format!("Delete loop record {loop_id}? [y/N]")
                } else {
                    format!("Loop is still running. Force delete record {loop_id}? [y/N]")
                }
            }
            _ => {
                self.set_status(StatusKind::Err, "Unsupported destructive action");
                return Command::None;
            }
        };
        let force_delete = action == ActionType::Delete && view.state != "stopped";
        let reason_required = action == ActionType::Kill || force_delete;

        self.confirm = Some(ConfirmState {
            action,
            loop_id: loop_id.to_owned(),
            prompt,
            force_delete,
            selected: ConfirmRailSelection::Cancel,
            reason: String::new(),
            reason_required,
        });
        self.mode = UiMode::Confirm;
        Command::None
    }

    // -- main update loop ----------------------------------------------------

    /// Process an input event. Returns a command for the host to execute.
    pub fn update(&mut self, event: InputEvent) -> Command {
        if let InputEvent::Resize(r) = event {
            self.width = r.width;
            self.height = r.height;
            if self.tab == MainTab::MultiLogs {
                self.clamp_multi_page();
            }
            return Command::Fetch;
        }

        if let InputEvent::Mouse(mouse_event) = event {
            return self.update_mouse_mode(mouse_event);
        }

        if let InputEvent::Key(key_event) = event {
            let resolved = self.resolve_key_command(key_event);
            if matches!(resolved, Some(KeyCommand::Quit)) {
                self.quitting = true;
                return Command::Quit;
            }
            if let Some(command) = resolved {
                self.hint_ranker.record(command);
            }

            match self.mode {
                UiMode::Palette => self.update_palette_mode(key_event),
                UiMode::Filter => self.update_filter_mode(key_event),
                UiMode::RegexSearch => self.update_regex_search_mode(key_event),
                UiMode::ExpandedLogs => self.update_expanded_logs_mode(key_event),
                UiMode::Confirm => self.update_confirm_mode(key_event),
                UiMode::Wizard => self.update_wizard_mode(key_event),
                UiMode::Help => self.update_help_mode(key_event),
                UiMode::Search => self.update_search_mode(key_event),
                UiMode::Main => self.update_main_mode(key_event),
            }
        } else {
            Command::None
        }
    }

    fn content_area_metrics(&self) -> (usize, usize) {
        let content_start = if self.focus_mode == FocusMode::DeepDebug {
            1
        } else {
            2
        };
        let failure_explain_strip = self.failure_explain_strip_text();
        let footer_lines = if self.status_text.is_empty() && failure_explain_strip.is_none() {
            1
        } else {
            2
        };
        let content_height = self
            .height
            .max(1)
            .saturating_sub(content_start + footer_lines)
            .max(1);
        (content_start, content_height)
    }

    fn tab_at_column(&self, column: usize) -> Option<MainTab> {
        let mut x = 0usize;
        for (index, tab) in MainTab::ORDER.iter().enumerate() {
            if index > 0 {
                x = x.saturating_add(2);
            }
            let label = if self.density_mode == DensityMode::Compact {
                tab.short_label()
            } else {
                tab.label()
            };
            let badge = format!(" {}:{} ", index + 1, label);
            let width = badge.chars().count();
            if column >= x && column < x.saturating_add(width) {
                return Some(*tab);
            }
            x = x.saturating_add(width);
        }
        None
    }

    fn update_mouse_mode(&mut self, mouse: MouseEvent) -> Command {
        if self.mode != UiMode::Main {
            return Command::None;
        }

        if self.focus_mode != FocusMode::DeepDebug && mouse.row == 1 {
            if let Some(tab) = self.tab_at_column(mouse.column) {
                if tab != self.tab {
                    self.set_tab(tab);
                }
                return Command::Fetch;
            }
        }

        match mouse.kind {
            MouseEventKind::Wheel(direction) => match self.tab {
                MainTab::Logs | MainTab::Runs | MainTab::MultiLogs => {
                    match direction {
                        MouseWheelDirection::Up => self.scroll_logs(3),
                        MouseWheelDirection::Down => self.scroll_logs(-3),
                    }
                    Command::Fetch
                }
                MainTab::Inbox => {
                    match direction {
                        MouseWheelDirection::Up => self.move_inbox_selection(-1),
                        MouseWheelDirection::Down => self.move_inbox_selection(1),
                    }
                    Command::Fetch
                }
                MainTab::Overview => {
                    match direction {
                        MouseWheelDirection::Up => self.move_selection(-1),
                        MouseWheelDirection::Down => self.move_selection(1),
                    }
                    Command::Fetch
                }
            },
            MouseEventKind::Down(MouseButton::Left) | MouseEventKind::Drag(MouseButton::Left) => {
                let (content_start, content_height) = self.content_area_metrics();
                if mouse.row < content_start || mouse.row >= content_start + content_height {
                    return Command::None;
                }
                let local_y = mouse.row - content_start;
                let changed = match self.tab {
                    MainTab::Inbox => {
                        self.handle_inbox_mouse_hit(mouse.column, local_y, content_height)
                    }
                    MainTab::MultiLogs => {
                        self.handle_multi_logs_mouse_hit(mouse.column, local_y, content_height)
                    }
                    MainTab::Logs | MainTab::Runs => {
                        let old = self.focus_right;
                        self.focus_right = mouse.column >= self.width.saturating_div(2);
                        old != self.focus_right
                    }
                    MainTab::Overview => false,
                };
                if changed {
                    Command::Fetch
                } else {
                    Command::None
                }
            }
            _ => Command::None,
        }
    }

    fn handle_inbox_mouse_hit(
        &mut self,
        column: usize,
        local_y: usize,
        pane_height: usize,
    ) -> bool {
        let threads = self.inbox_threads();
        if threads.is_empty() || pane_height < 4 {
            return false;
        }

        let width = self.width.max(1);
        let timeline_panel_h = if self.claim_events.is_empty() {
            0usize
        } else {
            5usize.min(pane_height.saturating_sub(2))
        };
        let body_height = pane_height.saturating_sub(1 + timeline_panel_h);
        let body_y = 1usize;
        if local_y < body_y || local_y >= body_y.saturating_add(body_height) {
            return false;
        }

        let min_detail_width = 30usize;
        let list_panel_width = if width > min_detail_width + 24 {
            (width * 2 / 5).clamp(24, width - min_detail_width - 1)
        } else {
            width
        };
        let has_detail = list_panel_width + 2 < width;
        let detail_x = list_panel_width + 1;

        if has_detail && column >= detail_x {
            let old = self.focus_right;
            self.focus_right = true;
            return old != self.focus_right;
        }

        if column >= list_panel_width {
            return false;
        }

        let mut changed = false;
        if self.focus_right {
            self.focus_right = false;
            changed = true;
        }

        let list_inner_y = body_y + 1;
        let list_inner_height = body_height.saturating_sub(2);
        if local_y < list_inner_y || local_y >= list_inner_y.saturating_add(list_inner_height) {
            return changed;
        }
        let row = local_y - list_inner_y;
        if row < threads.len() && row != self.inbox_selected_thread {
            self.inbox_selected_thread = row;
            self.clamp_inbox_selection();
            changed = true;
        }
        changed
    }

    fn handle_multi_logs_mouse_hit(
        &mut self,
        column: usize,
        local_y: usize,
        pane_height: usize,
    ) -> bool {
        let width = self.width.max(1);
        if width < 4 || pane_height < 4 {
            return false;
        }

        let ordered = self.ordered_multi_target_views();
        if ordered.is_empty() {
            return false;
        }

        let page_size = self.multi_page_size();
        let (_, _, start, end) = multi_page_bounds(ordered.len(), page_size, self.multi_page);
        if start >= ordered.len() {
            return false;
        }
        let targets = &ordered[start..end];
        if targets.is_empty() {
            return false;
        }

        let header_rows = self.multi_header_rows().max(1) as usize;
        if local_y < header_rows {
            let old = self.focus_right;
            self.focus_right = true;
            return old != self.focus_right;
        }

        let cell_gap = self.multi_cell_gap().max(0);
        let min_cell_width = self.multi_min_cell_width();
        let min_cell_height = self.multi_min_cell_height();
        let grid_height = ((pane_height as i32) - self.multi_header_rows()).max(min_cell_height);
        let layout = fit_pane_layout_for_breakpoint(
            self.current_layout(),
            width as i32,
            grid_height,
            cell_gap,
            min_cell_width,
            min_cell_height,
        );
        let (cell_w, cell_h) = layout_cell_size(layout, width as i32, grid_height, cell_gap);
        let cell_w = cell_w.max(1) as usize;
        let cell_h = cell_h.max(1) as usize;
        let gap = cell_gap as usize;

        let grid_y = local_y.saturating_sub(header_rows);
        for row in 0..layout.rows as usize {
            let y_base = row * (cell_h + gap);
            for col in 0..layout.cols as usize {
                let index = row * layout.cols as usize + col;
                if index >= targets.len() {
                    continue;
                }
                let x_base = col * (cell_w + gap);
                let in_x = column >= x_base && column < x_base + cell_w;
                let in_y = grid_y >= y_base && grid_y < y_base + cell_h;
                if in_x && in_y {
                    let old_selected = self.selected_id.clone();
                    let old_focus = self.focus_right;
                    let target_id = targets[index].id.clone();
                    self.focus_right = true;
                    self.select_loop_by_id(&target_id);
                    return old_selected != self.selected_id || old_focus != self.focus_right;
                }
            }
        }
        false
    }

    fn update_main_mode(&mut self, key: KeyEvent) -> Command {
        if matches!(self.resolve_key_command(key), Some(KeyCommand::OpenPalette)) {
            self.command_palette
                .open(self.palette_context(), DEFAULT_SEARCH_BUDGET);
            self.mode = UiMode::Palette;
            return Command::None;
        }
        if matches!(self.resolve_key_command(key), Some(KeyCommand::OpenSearch)) {
            self.populate_search_index();
            self.search_overlay.open();
            self.mode = UiMode::Search;
            return Command::None;
        }

        match key.key {
            Key::Char('q') => {
                self.quitting = true;
                Command::Quit
            }
            Key::Char('?') => {
                self.help_return = UiMode::Main;
                self.mode = UiMode::Help;
                Command::None
            }
            Key::Char('1') => {
                self.set_tab(MainTab::Overview);
                Command::Fetch
            }
            Key::Char('2') => {
                self.set_tab(MainTab::Logs);
                Command::Fetch
            }
            Key::Char('3') => {
                self.set_tab(MainTab::Runs);
                Command::Fetch
            }
            Key::Char('4') => {
                self.set_tab(MainTab::MultiLogs);
                Command::Fetch
            }
            Key::Char('5') => {
                self.set_tab(MainTab::Inbox);
                Command::Fetch
            }
            Key::Char('b') if !key.modifiers.ctrl => {
                if self.pop_navigation_return_point() {
                    Command::Fetch
                } else {
                    Command::None
                }
            }
            Key::Char(']') => {
                self.cycle_tab(1);
                Command::Fetch
            }
            Key::Char('[') => {
                self.cycle_tab(-1);
                Command::Fetch
            }
            Key::Char('t') => {
                self.cycle_theme();
                Command::None
            }
            Key::Char('T') => {
                self.cycle_accessibility_theme();
                Command::None
            }
            Key::Char('A') => {
                self.cycle_accessibility_quick_mode();
                Command::Fetch
            }
            Key::Char('z') => {
                self.toggle_zen_mode();
                Command::Fetch
            }
            Key::Char('Z') => {
                self.toggle_deep_focus_mode();
                Command::Fetch
            }
            Key::Char('M') => {
                self.cycle_density_mode(1);
                Command::Fetch
            }
            Key::Char('i') => {
                self.dismiss_onboarding_for_tab(self.tab);
                Command::Fetch
            }
            Key::Char('I') => {
                self.recall_onboarding_for_tab(self.tab);
                Command::Fetch
            }
            Key::Char('/') => {
                self.mode = UiMode::Filter;
                self.filter_focus = FilterFocus::Text;
                Command::None
            }
            Key::Tab if key.modifiers.shift => {
                if self.traverse_focus_graph(-1) {
                    Command::Fetch
                } else {
                    Command::None
                }
            }
            Key::Tab => {
                if self.traverse_focus_graph(1) {
                    Command::Fetch
                } else {
                    Command::None
                }
            }
            Key::Left => {
                if self.traverse_focus_graph(-1) {
                    Command::Fetch
                } else {
                    Command::None
                }
            }
            Key::Right => {
                if self.traverse_focus_graph(1) {
                    Command::Fetch
                } else {
                    Command::None
                }
            }
            Key::Char('E') => Command::ExportCurrentView,
            Key::Char('j') | Key::Down => {
                if self.tab == MainTab::Inbox {
                    self.move_inbox_selection(1);
                } else {
                    self.move_selection(1);
                }
                Command::Fetch
            }
            Key::Char('k') | Key::Up => {
                if self.tab == MainTab::Inbox {
                    self.move_inbox_selection(-1);
                } else {
                    self.move_selection(-1);
                }
                Command::Fetch
            }
            Key::Enter => {
                if self.tab == MainTab::Inbox {
                    self.mark_selected_inbox_thread_read();
                    Command::Fetch
                } else {
                    Command::None
                }
            }
            Key::Char('f') => {
                if self.tab == MainTab::Inbox {
                    self.cycle_inbox_filter(1);
                    Command::Fetch
                } else {
                    Command::None
                }
            }
            Key::Char('a') if !key.modifiers.ctrl => {
                if self.tab == MainTab::Inbox {
                    self.acknowledge_selected_inbox_message();
                    Command::Fetch
                } else {
                    Command::None
                }
            }
            Key::Char('h') => {
                if self.tab == MainTab::Inbox {
                    self.generate_handoff_snapshot();
                    Command::Fetch
                } else {
                    Command::None
                }
            }
            Key::Char('R') => {
                if self.tab == MainTab::Inbox {
                    self.quick_reply_selected_inbox_thread();
                    Command::None
                } else if self.tab == MainTab::Logs || self.tab == MainTab::Runs {
                    self.mode = UiMode::RegexSearch;
                    Command::None
                } else {
                    Command::None
                }
            }
            Key::Char('o') if key.modifiers.ctrl => {
                if self.activate_primary_link() {
                    Command::Fetch
                } else {
                    Command::None
                }
            }
            Key::Char('o') if !key.modifiers.ctrl => {
                if self.tab == MainTab::Inbox {
                    self.cycle_claim_conflict(1);
                    Command::None
                } else {
                    Command::None
                }
            }
            Key::Char('O') => {
                if self.tab == MainTab::Inbox {
                    self.show_claim_resolution_hint();
                    Command::None
                } else {
                    Command::None
                }
            }
            Key::Char('y') if key.modifiers.ctrl => {
                self.copy_clipboard_context();
                Command::Fetch
            }
            Key::Char('u') if key.modifiers.ctrl => {
                if self.tab == MainTab::Logs
                    || self.tab == MainTab::Runs
                    || (self.tab == MainTab::MultiLogs && self.multi_compare_mode)
                {
                    let page = self.log_scroll_page_size() as i32;
                    self.scroll_logs(page);
                    Command::Fetch
                } else {
                    Command::None
                }
            }
            Key::Char('d') if key.modifiers.ctrl => {
                if self.tab == MainTab::Logs
                    || self.tab == MainTab::Runs
                    || (self.tab == MainTab::MultiLogs && self.multi_compare_mode)
                {
                    let page = self.log_scroll_page_size() as i32;
                    self.scroll_logs(-page);
                    Command::Fetch
                } else {
                    Command::None
                }
            }
            Key::Char('e') if key.modifiers.ctrl => {
                if self.jump_to_latest_evidence(EvidenceKind::Error) {
                    Command::Fetch
                } else {
                    Command::None
                }
            }
            Key::Char('w') if key.modifiers.ctrl => {
                if self.jump_to_latest_evidence(EvidenceKind::Warning) {
                    Command::Fetch
                } else {
                    Command::None
                }
            }
            Key::Char('a') if key.modifiers.ctrl => {
                if self.jump_to_latest_evidence(EvidenceKind::Ack) {
                    Command::Fetch
                } else {
                    Command::None
                }
            }
            Key::Char('b') if key.modifiers.ctrl => {
                if self.restore_evidence_return_point() {
                    Command::Fetch
                } else {
                    Command::None
                }
            }
            Key::Char('u') => {
                if self.tab == MainTab::Logs
                    || self.tab == MainTab::Runs
                    || (self.tab == MainTab::MultiLogs && self.multi_compare_mode)
                {
                    let page = self.log_scroll_page_size() as i32;
                    self.scroll_logs(page);
                    Command::Fetch
                } else {
                    Command::None
                }
            }
            Key::Char('d') => {
                if self.tab == MainTab::Logs
                    || self.tab == MainTab::Runs
                    || (self.tab == MainTab::MultiLogs && self.multi_compare_mode)
                {
                    let page = self.log_scroll_page_size() as i32;
                    self.scroll_logs(-page);
                    Command::Fetch
                } else {
                    Command::None
                }
            }
            Key::Char(' ') => {
                if let Some(view) = self.selected_view().cloned() {
                    self.toggle_pinned(&view.id);
                }
                Command::Fetch
            }
            Key::Char('c') => {
                self.clear_pinned();
                Command::Fetch
            }
            Key::Char('C') => {
                if self.tab == MainTab::MultiLogs {
                    self.toggle_multi_compare_mode();
                    Command::Fetch
                } else {
                    Command::None
                }
            }
            Key::Char('m') => {
                if self.tab == MainTab::MultiLogs {
                    self.cycle_layout(1);
                    Command::Fetch
                } else {
                    Command::None
                }
            }
            Key::Char('g') => {
                if self.tab == MainTab::MultiLogs {
                    self.move_multi_page_to_start();
                    Command::Fetch
                } else {
                    Command::None
                }
            }
            Key::Char('G') => {
                if self.tab == MainTab::MultiLogs {
                    self.move_multi_page_to_end();
                    Command::Fetch
                } else {
                    Command::None
                }
            }
            Key::Char('v') => {
                if self.tab == MainTab::Logs {
                    self.cycle_log_source(1);
                    Command::Fetch
                } else {
                    Command::None
                }
            }
            Key::Char('x') => {
                if self.tab == MainTab::Logs
                    || self.tab == MainTab::Runs
                    || self.tab == MainTab::MultiLogs
                {
                    self.cycle_log_layer(1);
                }
                Command::None
            }
            Key::Char('F') => {
                if self.tab == MainTab::Logs || self.tab == MainTab::Runs {
                    self.toggle_follow_mode();
                    Command::Fetch
                } else {
                    Command::None
                }
            }
            Key::Char(',') => {
                if self.tab == MainTab::Logs || self.tab == MainTab::Runs {
                    self.move_run_selection(-1);
                    Command::Fetch
                } else if self.tab == MainTab::MultiLogs {
                    self.move_multi_page(-1);
                    Command::Fetch
                } else {
                    Command::None
                }
            }
            Key::Char('.') => {
                if self.tab == MainTab::Logs || self.tab == MainTab::Runs {
                    self.move_run_selection(1);
                    Command::Fetch
                } else if self.tab == MainTab::MultiLogs {
                    self.move_multi_page(1);
                    Command::Fetch
                } else {
                    Command::None
                }
            }
            Key::Char('l') => {
                if self.selected_view().is_none() {
                    self.set_status(StatusKind::Info, "No loop selected");
                    Command::None
                } else {
                    self.mode = UiMode::ExpandedLogs;
                    Command::Fetch
                }
            }
            Key::Char('n') => {
                self.mode = UiMode::Wizard;
                self.wizard = WizardState::with_defaults(
                    &self.default_interval,
                    &self.default_prompt,
                    &self.default_prompt_msg,
                );
                Command::None
            }
            Key::Char('r') => {
                if self.tab == MainTab::Inbox {
                    self.quick_reply_selected_inbox_thread();
                    return Command::None;
                }
                let loop_id = match self.selected_view() {
                    Some(v) => v.id.clone(),
                    None => {
                        self.set_status(StatusKind::Info, "No loop selected");
                        return Command::None;
                    }
                };
                self.run_action(ActionType::Resume, &loop_id)
            }
            Key::Char('S') => self.enter_confirm(ActionType::Stop),
            Key::Char('K') => self.enter_confirm(ActionType::Kill),
            Key::Char('D') => self.enter_confirm(ActionType::Delete),
            _ => Command::None,
        }
    }

    fn update_palette_mode(&mut self, key: KeyEvent) -> Command {
        match self.resolve_key_command(key) {
            Some(KeyCommand::PaletteClose) => {
                self.mode = UiMode::Main;
                Command::None
            }
            Some(KeyCommand::ToggleHelp) => {
                self.help_return = UiMode::Palette;
                self.mode = UiMode::Help;
                Command::None
            }
            Some(KeyCommand::PaletteMoveNext) => {
                self.command_palette.move_selection(1);
                Command::None
            }
            Some(KeyCommand::PaletteMovePrev) => {
                self.command_palette.move_selection(-1);
                Command::None
            }
            Some(KeyCommand::PaletteQueryBackspace) => {
                self.command_palette
                    .pop_char(self.palette_context(), DEFAULT_SEARCH_BUDGET);
                Command::None
            }
            Some(KeyCommand::PaletteExecute) => {
                let context = self.palette_context();
                let Some(action) = self.command_palette.accept(context, DEFAULT_SEARCH_BUDGET)
                else {
                    return Command::None;
                };
                self.execute_palette_action(action)
            }
            _ => match key.key {
                Key::Char(ch) if !key.modifiers.ctrl && !key.modifiers.alt => {
                    self.command_palette.push_char(
                        ch,
                        self.palette_context(),
                        DEFAULT_SEARCH_BUDGET,
                    );
                    Command::None
                }
                _ => Command::None,
            },
        }
    }

    fn execute_palette_action(&mut self, action: PaletteActionId) -> Command {
        self.mode = UiMode::Main;
        match action {
            PaletteActionId::SwitchOverview => {
                self.set_tab(MainTab::Overview);
                Command::Fetch
            }
            PaletteActionId::SwitchLogs => {
                self.set_tab(MainTab::Logs);
                Command::Fetch
            }
            PaletteActionId::SwitchRuns => {
                self.set_tab(MainTab::Runs);
                Command::Fetch
            }
            PaletteActionId::SwitchMultiLogs => {
                self.set_tab(MainTab::MultiLogs);
                Command::Fetch
            }
            PaletteActionId::SwitchInbox => {
                self.set_tab(MainTab::Inbox);
                Command::Fetch
            }
            PaletteActionId::OpenFilter => {
                self.mode = UiMode::Filter;
                self.filter_focus = FilterFocus::Text;
                Command::None
            }
            PaletteActionId::ExportCurrentView => Command::ExportCurrentView,
            PaletteActionId::NewLoopWizard => {
                self.mode = UiMode::Wizard;
                self.wizard = WizardState::with_defaults(
                    &self.default_interval,
                    &self.default_prompt,
                    &self.default_prompt_msg,
                );
                Command::None
            }
            PaletteActionId::ResumeSelectedLoop => {
                let loop_id = match self.selected_view() {
                    Some(v) => v.id.clone(),
                    None => {
                        self.set_status(StatusKind::Info, "No loop selected");
                        return Command::None;
                    }
                };
                self.run_action(ActionType::Resume, &loop_id)
            }
            PaletteActionId::StopSelectedLoop => self.enter_confirm(ActionType::Stop),
            PaletteActionId::KillSelectedLoop => self.enter_confirm(ActionType::Kill),
            PaletteActionId::DeleteSelectedLoop => self.enter_confirm(ActionType::Delete),
            PaletteActionId::CycleTheme => {
                self.cycle_theme();
                Command::None
            }
            PaletteActionId::ToggleZenMode => {
                self.toggle_zen_mode();
                Command::Fetch
            }
            PaletteActionId::CycleDensityMode => {
                self.cycle_density_mode(1);
                Command::Fetch
            }
            PaletteActionId::ToggleFocusMode => {
                self.toggle_deep_focus_mode();
                Command::Fetch
            }
            PaletteActionId::Custom(_) => Command::None,
        }
    }

    fn update_filter_mode(&mut self, key: KeyEvent) -> Command {
        match key.key {
            Key::Char('q') | Key::Escape => {
                self.mode = UiMode::Main;
                self.filter_focus = FilterFocus::Text;
                Command::None
            }
            Key::Char('?') => {
                self.help_return = UiMode::Filter;
                self.mode = UiMode::Help;
                Command::None
            }
            Key::Tab => {
                self.filter_focus = match self.filter_focus {
                    FilterFocus::Text => FilterFocus::Status,
                    FilterFocus::Status => FilterFocus::Text,
                };
                Command::None
            }
            _ => {
                if self.filter_focus == FilterFocus::Status {
                    match key.key {
                        Key::Left | Key::Up | Key::Char('k') => {
                            self.cycle_filter_status(-1);
                            Command::None
                        }
                        Key::Right | Key::Down | Key::Char('j') | Key::Enter => {
                            self.cycle_filter_status(1);
                            Command::None
                        }
                        _ => Command::None,
                    }
                } else {
                    match key.key {
                        Key::Backspace => {
                            if !self.filter_text.is_empty() {
                                self.filter_text.pop();
                                let old_id = self.selected_id.clone();
                                let old_idx = self.selected_idx;
                                self.apply_filters(&old_id, old_idx);
                                Command::Fetch
                            } else {
                                Command::None
                            }
                        }
                        Key::Char(' ') => {
                            self.filter_text.push(' ');
                            let old_id = self.selected_id.clone();
                            let old_idx = self.selected_idx;
                            self.apply_filters(&old_id, old_idx);
                            Command::Fetch
                        }
                        Key::Char(ch) => {
                            self.filter_text.push(ch);
                            let old_id = self.selected_id.clone();
                            let old_idx = self.selected_idx;
                            self.apply_filters(&old_id, old_idx);
                            Command::Fetch
                        }
                        _ => Command::None,
                    }
                }
            }
        }
    }

    fn rendered_log_lines(&self) -> Vec<String> {
        render_lines_for_layer(
            &self.selected_log.lines,
            map_log_render_layer(self.log_layer),
            true,
        )
    }

    fn collect_regex_match_indices(&self, rendered_lines: &[String]) -> Vec<usize> {
        let Some(regex) = self.log_regex_compiled.as_ref() else {
            return Vec::new();
        };
        rendered_lines
            .iter()
            .enumerate()
            .filter_map(|(idx, line)| {
                if regex.is_match(line) {
                    Some(idx)
                } else {
                    None
                }
            })
            .collect()
    }

    fn set_log_regex_query(&mut self, query: String) {
        self.log_regex_query = query;
        self.log_regex_selected_match = 0;
        self.log_regex_error.clear();
        self.log_regex_compiled = None;
        if self.log_regex_query.trim().is_empty() {
            return;
        }
        match regex::Regex::new(&self.log_regex_query) {
            Ok(regex) => {
                self.log_regex_compiled = Some(regex);
            }
            Err(err) => {
                self.log_regex_error = format!("invalid regex: {err}");
            }
        }
    }

    fn jump_log_regex_match(&mut self, delta: i32) -> Command {
        if self.log_regex_query.trim().is_empty() {
            self.set_status(StatusKind::Info, "Regex query is empty");
            return Command::None;
        }
        if !self.log_regex_error.is_empty() {
            let err = self.log_regex_error.clone();
            self.set_status(StatusKind::Err, &err);
            return Command::None;
        }
        let rendered_lines = self.rendered_log_lines();
        let matches = self.collect_regex_match_indices(&rendered_lines);
        if matches.is_empty() {
            self.set_status(StatusKind::Info, "No regex matches");
            return Command::None;
        }
        let mut index = self.log_regex_selected_match as i32 + delta;
        while index < 0 {
            index += matches.len() as i32;
        }
        self.log_regex_selected_match = (index as usize) % matches.len();
        let line_index = matches[self.log_regex_selected_match];
        self.log_scroll = rendered_lines.len().saturating_sub(line_index + 1);
        self.set_status(
            StatusKind::Info,
            &format!(
                "Regex match {}/{}",
                self.log_regex_selected_match + 1,
                matches.len()
            ),
        );
        Command::Fetch
    }

    fn update_regex_search_mode(&mut self, key: KeyEvent) -> Command {
        match key.key {
            Key::Char('q') | Key::Escape => {
                self.mode = UiMode::Main;
                Command::None
            }
            Key::Char('?') => {
                self.help_return = UiMode::RegexSearch;
                self.mode = UiMode::Help;
                Command::None
            }
            Key::Enter => {
                self.mode = UiMode::Main;
                Command::Fetch
            }
            Key::Char('j') | Key::Down => self.jump_log_regex_match(1),
            Key::Char('k') | Key::Up => self.jump_log_regex_match(-1),
            Key::Char('n') if key.modifiers.ctrl => self.jump_log_regex_match(1),
            Key::Char('p') if key.modifiers.ctrl => self.jump_log_regex_match(-1),
            Key::Backspace => {
                if !self.log_regex_query.is_empty() {
                    self.log_regex_query.pop();
                    self.set_log_regex_query(self.log_regex_query.clone());
                    Command::Fetch
                } else {
                    Command::None
                }
            }
            Key::Char('u') if key.modifiers.ctrl => {
                self.set_log_regex_query(String::new());
                Command::Fetch
            }
            Key::Char(ch) if !key.modifiers.ctrl && !key.modifiers.alt => {
                self.log_regex_query.push(ch);
                self.set_log_regex_query(self.log_regex_query.clone());
                Command::Fetch
            }
            _ => Command::None,
        }
    }

    fn update_expanded_logs_mode(&mut self, key: KeyEvent) -> Command {
        match key.key {
            Key::Char('q') | Key::Escape => {
                self.mode = UiMode::Main;
                Command::Fetch
            }
            Key::Char('?') => {
                self.help_return = UiMode::ExpandedLogs;
                self.mode = UiMode::Help;
                Command::None
            }
            Key::Char(']') => {
                self.cycle_tab(1);
                Command::Fetch
            }
            Key::Char('[') => {
                self.cycle_tab(-1);
                Command::Fetch
            }
            Key::Char('t') => {
                self.cycle_theme();
                Command::None
            }
            Key::Char('T') => {
                self.cycle_accessibility_theme();
                Command::None
            }
            Key::Char('z') => {
                self.toggle_zen_mode();
                Command::Fetch
            }
            Key::Char('Z') => {
                self.toggle_deep_focus_mode();
                Command::Fetch
            }
            Key::Char('M') => {
                self.cycle_density_mode(1);
                Command::Fetch
            }
            Key::Char('j') | Key::Down => {
                self.move_selection(1);
                Command::Fetch
            }
            Key::Char('k') | Key::Up => {
                self.move_selection(-1);
                Command::Fetch
            }
            Key::Char('v') => {
                if self.tab == MainTab::Logs {
                    self.cycle_log_source(1);
                    Command::Fetch
                } else {
                    Command::None
                }
            }
            Key::Char('x') => {
                self.cycle_log_layer(1);
                Command::None
            }
            Key::Char('F') => {
                self.toggle_follow_mode();
                Command::Fetch
            }
            Key::Char(',') => {
                self.move_run_selection(-1);
                Command::Fetch
            }
            Key::Char('.') => {
                self.move_run_selection(1);
                Command::Fetch
            }
            Key::Char('/') => {
                self.mode = UiMode::Filter;
                self.filter_focus = FilterFocus::Text;
                Command::None
            }
            Key::Char('R') => {
                self.mode = UiMode::RegexSearch;
                Command::None
            }
            Key::Char('S') => {
                self.mode = UiMode::Main;
                self.enter_confirm(ActionType::Stop)
            }
            Key::Char('K') => {
                self.mode = UiMode::Main;
                self.enter_confirm(ActionType::Kill)
            }
            Key::Char('D') => {
                self.mode = UiMode::Main;
                self.enter_confirm(ActionType::Delete)
            }
            Key::Char('r') => {
                let loop_id = match self.selected_view() {
                    Some(v) => v.id.clone(),
                    None => {
                        self.set_status(StatusKind::Info, "No loop selected");
                        return Command::None;
                    }
                };
                self.mode = UiMode::Main;
                self.run_action(ActionType::Resume, &loop_id)
            }
            _ => Command::None,
        }
    }

    fn update_confirm_mode(&mut self, key: KeyEvent) -> Command {
        if self.confirm.is_none() {
            self.mode = UiMode::Main;
            return Command::None;
        }
        let reason_required = self
            .confirm
            .as_ref()
            .is_some_and(|confirm| confirm.reason_required);

        let set_confirm_selection = |confirm: &mut ConfirmState, delta: i32| {
            let options = [ConfirmRailSelection::Cancel, ConfirmRailSelection::Confirm];
            let current = match confirm.selected {
                ConfirmRailSelection::Cancel => 0i32,
                ConfirmRailSelection::Confirm => 1i32,
            };
            let next = (current + delta).rem_euclid(options.len() as i32) as usize;
            confirm.selected = options[next];
        };
        let confirm_reason_is_valid = |confirm: &ConfirmState| {
            if !confirm.reason_required {
                return true;
            }
            confirm.reason.trim().chars().count() >= DESTRUCTIVE_CONFIRM_REASON_MIN_CHARS
        };

        let submit_confirm = |confirm: ConfirmState| -> Command {
            let action = match confirm.action {
                ActionType::Stop => ActionKind::Stop {
                    loop_id: confirm.loop_id,
                },
                ActionType::Kill => ActionKind::Kill {
                    loop_id: confirm.loop_id,
                },
                ActionType::Delete => ActionKind::Delete {
                    loop_id: confirm.loop_id,
                    force: confirm.force_delete,
                },
                _ => return Command::None,
            };
            Command::RunAction(action)
        };

        match key.key {
            Key::Char('q') | Key::Escape => {
                self.mode = UiMode::Main;
                self.confirm = None;
                self.set_status(StatusKind::Info, "Action cancelled");
                Command::None
            }
            Key::Char('n') | Key::Char('N') if !reason_required => {
                self.mode = UiMode::Main;
                self.confirm = None;
                self.set_status(StatusKind::Info, "Action cancelled");
                Command::None
            }
            Key::Char('?') => {
                self.help_return = UiMode::Confirm;
                self.mode = UiMode::Help;
                Command::None
            }
            Key::Tab | Key::Right => {
                if let Some(confirm) = self.confirm.as_mut() {
                    if key.key == Key::Tab && key.modifiers.shift {
                        set_confirm_selection(confirm, -1);
                    } else {
                        set_confirm_selection(confirm, 1);
                    }
                }
                Command::None
            }
            Key::Left => {
                if let Some(confirm) = self.confirm.as_mut() {
                    set_confirm_selection(confirm, -1);
                }
                Command::None
            }
            Key::Backspace if reason_required => {
                if let Some(confirm) = self.confirm.as_mut() {
                    confirm.reason.pop();
                }
                Command::None
            }
            Key::Char('u') if reason_required && key.modifiers.ctrl => {
                if let Some(confirm) = self.confirm.as_mut() {
                    confirm.reason.clear();
                }
                Command::None
            }
            Key::Char(ch) if reason_required && !key.modifiers.ctrl && !key.modifiers.alt => {
                if let Some(confirm) = self.confirm.as_mut() {
                    if confirm.reason.chars().count() < MAX_DESTRUCTIVE_CONFIRM_REASON_CHARS {
                        confirm.reason.push(ch);
                    }
                }
                Command::None
            }
            Key::Enter => {
                let submit_selected = self
                    .confirm
                    .as_ref()
                    .is_some_and(|confirm| confirm.selected == ConfirmRailSelection::Confirm);
                if !submit_selected {
                    self.mode = UiMode::Main;
                    self.confirm = None;
                    self.set_status(StatusKind::Info, "Action cancelled");
                    return Command::None;
                }
                if let Some(confirm) = self.confirm.as_ref() {
                    if !confirm_reason_is_valid(confirm) {
                        let message = format!(
                            "Reason required for high-risk action (min {} chars)",
                            DESTRUCTIVE_CONFIRM_REASON_MIN_CHARS
                        );
                        self.set_status(StatusKind::Err, &message);
                        return Command::None;
                    }
                }
                let confirm = self.confirm.take();
                self.mode = UiMode::Main;
                if let Some(confirm) = confirm {
                    submit_confirm(confirm)
                } else {
                    Command::None
                }
            }
            Key::Char('y') | Key::Char('Y') if !reason_required => {
                let confirm = self.confirm.take();
                self.mode = UiMode::Main;
                if let Some(confirm) = confirm {
                    submit_confirm(confirm)
                } else {
                    Command::None
                }
            }
            _ => Command::None,
        }
    }

    fn update_wizard_mode(&mut self, key: KeyEvent) -> Command {
        match key.key {
            Key::Char('q') | Key::Escape => {
                self.mode = UiMode::Main;
                self.wizard.error.clear();
                Command::None
            }
            Key::Char('?') => {
                self.help_return = UiMode::Wizard;
                self.mode = UiMode::Help;
                Command::None
            }
            Key::Tab if key.modifiers.shift => {
                self.wizard_prev_field();
                Command::None
            }
            Key::Tab | Key::Down | Key::Char('j') => {
                self.wizard_next_field();
                Command::None
            }
            Key::Up | Key::Char('k') => {
                self.wizard_prev_field();
                Command::None
            }
            Key::Enter => {
                if self.wizard.step < 4 {
                    if let Err(err) = validate_wizard_step(self.wizard.step, &self.wizard.values) {
                        self.wizard.error = err;
                        return Command::None;
                    }
                    self.wizard.step += 1;
                    self.wizard.field = 0;
                    self.wizard.error.clear();
                    return Command::None;
                }
                self.run_action(ActionType::Create, "")
            }
            Key::Char('b') | Key::Left => {
                if self.wizard.step > 1 {
                    self.wizard.step -= 1;
                    self.wizard.field = 0;
                    self.wizard.error.clear();
                }
                Command::None
            }
            Key::Backspace => {
                if self.wizard.step > 3 {
                    return Command::None;
                }
                let Some(field_key) = wizard_field_key(self.wizard.step, self.wizard.field) else {
                    return Command::None;
                };
                let mut value = wizard_get(&self.wizard.values, field_key).to_owned();
                value.pop();
                wizard_set(&mut self.wizard.values, field_key, value);
                Command::None
            }
            Key::Char('h') if key.modifiers.ctrl => {
                if self.wizard.step > 3 {
                    return Command::None;
                }
                let Some(field_key) = wizard_field_key(self.wizard.step, self.wizard.field) else {
                    return Command::None;
                };
                let mut value = wizard_get(&self.wizard.values, field_key).to_owned();
                value.pop();
                wizard_set(&mut self.wizard.values, field_key, value);
                Command::None
            }
            Key::Char(' ') => {
                if self.wizard.step > 3 {
                    return Command::None;
                }
                let Some(field_key) = wizard_field_key(self.wizard.step, self.wizard.field) else {
                    return Command::None;
                };
                let mut value = wizard_get(&self.wizard.values, field_key).to_owned();
                value.push(' ');
                wizard_set(&mut self.wizard.values, field_key, value);
                Command::None
            }
            Key::Char(ch) => {
                if self.wizard.step > 3 {
                    return Command::None;
                }
                let Some(field_key) = wizard_field_key(self.wizard.step, self.wizard.field) else {
                    return Command::None;
                };
                let mut value = wizard_get(&self.wizard.values, field_key).to_owned();
                value.push(ch);
                wizard_set(&mut self.wizard.values, field_key, value);
                Command::None
            }
            _ => Command::None,
        }
    }

    fn update_help_mode(&mut self, key: KeyEvent) -> Command {
        match key.key {
            Key::Char('q') | Key::Escape | Key::Char('?') => {
                if self.help_return == UiMode::Help {
                    self.mode = UiMode::Main;
                } else {
                    self.mode = self.help_return;
                }
                Command::None
            }
            _ => Command::None,
        }
    }

    fn update_search_mode(&mut self, key: KeyEvent) -> Command {
        match self.resolve_key_command(key) {
            Some(KeyCommand::SearchClose) => {
                self.mode = UiMode::Main;
                Command::None
            }
            Some(KeyCommand::SearchMoveNext) => {
                self.search_overlay.move_selection(1);
                Command::None
            }
            Some(KeyCommand::SearchMovePrev) => {
                self.search_overlay.move_selection(-1);
                Command::None
            }
            Some(KeyCommand::SearchQueryBackspace) => {
                self.search_overlay.pop_char();
                Command::None
            }
            Some(KeyCommand::SearchNextMatch) => {
                self.search_overlay.next_match();
                Command::None
            }
            Some(KeyCommand::SearchPrevMatch) => {
                self.search_overlay.prev_match();
                Command::None
            }
            Some(KeyCommand::SearchExecute) => {
                if let Some(target) = self.search_overlay.accept() {
                    self.mode = UiMode::Main;
                    self.jump_to_search_target(target);
                    Command::Fetch
                } else {
                    Command::None
                }
            }
            _ => match key.key {
                Key::Char(ch) if !key.modifiers.ctrl && !key.modifiers.alt => {
                    self.search_overlay.push_char(ch);
                    Command::None
                }
                _ => Command::None,
            },
        }
    }

    fn collect_active_links(&self) -> LinkRegistry {
        let mut registry = LinkRegistry::new();
        match self.tab {
            MainTab::Logs => {
                registry.register_text(&self.selected_log.message);
                for line in self.selected_log.lines.iter().rev().take(256) {
                    registry.register_text(line);
                }
            }
            MainTab::Runs => {
                if let Some(run) = self.run_history.get(self.selected_run) {
                    registry.register_target(LinkTarget::Run(run.id.clone()));
                    registry.register_text(&run.status);
                    for line in run.output_lines.iter().rev().take(256) {
                        registry.register_text(line);
                    }
                }
            }
            MainTab::Overview => {
                if let Some(view) = self.selected_view() {
                    registry.register_target(LinkTarget::Loop(view.id.clone()));
                    registry.register_text(&view.state);
                    registry.register_text(&view.last_error);
                }
            }
            MainTab::MultiLogs => {
                for view in self.ordered_multi_target_views().into_iter().take(8) {
                    registry.register_target(LinkTarget::Loop(view.id.clone()));
                    registry.register_text(&view.state);
                    registry.register_text(&view.last_error);
                }
            }
            MainTab::Inbox => {
                if let Some(snapshot) = &self.handoff_snapshot {
                    for line in snapshot.lines() {
                        registry.register_text(&line);
                    }
                }
            }
        }
        registry
    }

    fn activate_primary_link(&mut self) -> bool {
        let links = self.collect_active_links();
        let Some(entry) = links.first() else {
            self.set_status(StatusKind::Info, "No links in current context");
            return false;
        };

        match &entry.target {
            LinkTarget::Run(run_id) => {
                self.jump_to_search_target(crate::search_overlay::SearchJumpTarget::Run {
                    run_id: run_id.clone(),
                });
            }
            LinkTarget::Loop(loop_id) => {
                self.jump_to_search_target(crate::search_overlay::SearchJumpTarget::Log {
                    loop_id: loop_id.clone(),
                });
            }
            LinkTarget::Url(url) => {
                self.set_status(
                    StatusKind::Info,
                    &format!("Open URL: {url} (fallback: copy/paste)"),
                );
            }
        }
        true
    }

    fn populate_search_index(&mut self) {
        let index = self.search_overlay.index_mut();
        crate::search_overlay::index_loops(index, &self.loops);
        crate::search_overlay::index_runs(index, &self.run_history, &self.selected_id);
        crate::search_overlay::index_logs(index, &self.selected_log, &self.selected_id);
    }

    fn jump_to_search_target(&mut self, target: crate::search_overlay::SearchJumpTarget) {
        use crate::search_overlay::SearchJumpTarget;
        self.push_navigation_return_point();
        match target {
            SearchJumpTarget::Loop { loop_id } => {
                self.select_loop_by_id(&loop_id);
                self.set_tab(MainTab::Overview);
                self.set_status(StatusKind::Info, &format!("Jumped to loop {loop_id}"));
            }
            SearchJumpTarget::Run { run_id } => {
                self.set_tab(MainTab::Runs);
                self.set_status(StatusKind::Info, &format!("Jumped to run {run_id}"));
            }
            SearchJumpTarget::Log { loop_id } => {
                let clean_id = loop_id.strip_prefix("log:").unwrap_or(&loop_id);
                self.select_loop_by_id(clean_id);
                self.set_tab(MainTab::Logs);
                self.set_status(StatusKind::Info, &format!("Jumped to logs for {clean_id}"));
            }
        }
    }

    fn select_loop_by_id(&mut self, loop_id: &str) {
        if loop_id.is_empty() {
            return;
        }
        for (idx, lv) in self.filtered.iter().enumerate() {
            if lv.id == loop_id {
                self.selected_idx = idx;
                self.selected_id = loop_id.to_owned();
                return;
            }
        }
        // Fallback: try in full loop list
        for lv in &self.loops {
            if lv.id == loop_id {
                self.selected_id = loop_id.to_owned();
                return;
            }
        }
    }

    fn run_action(&mut self, action: ActionType, loop_id: &str) -> Command {
        if self.action_busy {
            self.set_status(StatusKind::Info, "Another action is still running");
            return Command::None;
        }

        self.action_busy = true;
        let msg = match action {
            ActionType::Create => "Creating loop(s)...",
            ActionType::Resume => "Resuming loop...",
            ActionType::Stop => "Requesting graceful stop...",
            ActionType::Kill => "Killing loop...",
            ActionType::Delete => "Deleting loop record...",
            _ => "Running action...",
        };
        self.set_status(StatusKind::Info, msg);

        match action {
            ActionType::Create => Command::RunAction(ActionKind::Create {
                wizard: self.wizard_pairs(),
            }),
            ActionType::Resume => Command::RunAction(ActionKind::Resume {
                loop_id: loop_id.to_owned(),
            }),
            ActionType::Stop => Command::RunAction(ActionKind::Stop {
                loop_id: loop_id.to_owned(),
            }),
            ActionType::Kill => Command::RunAction(ActionKind::Kill {
                loop_id: loop_id.to_owned(),
            }),
            ActionType::Delete => Command::RunAction(ActionKind::Delete {
                loop_id: loop_id.to_owned(),
                force: false,
            }),
            _ => Command::None,
        }
    }

    /// Handle the result of an asynchronous action.
    ///
    /// Matches Go `actionResultMsg` handling: clears busy flag, shows
    /// success/error status, resets wizard on create success, and
    /// triggers a data refresh.
    pub fn handle_action_result(&mut self, result: ActionResult) -> Command {
        self.action_busy = false;

        if let Some(ref err) = result.error {
            self.set_status(StatusKind::Err, err);
            if result.kind == ActionType::Create {
                self.mode = UiMode::Wizard;
                self.wizard.error = err.clone();
            }
            return Command::None;
        }

        if result.kind == ActionType::Create {
            self.mode = UiMode::Main;
            self.wizard.error.clear();
            if !result.selected_loop_id.is_empty() {
                self.selected_id = result.selected_loop_id;
            }
        }

        if !result.message.is_empty() {
            self.set_status(StatusKind::Ok, &result.message);
        }

        Command::Fetch
    }

    // -- render --------------------------------------------------------------

    /// Render the full TUI frame.
    #[must_use]
    pub fn render(&self) -> RenderFrame {
        let width = self.width.max(1);
        let height = self.height.max(1);
        let theme = crate::theme_for_capability(self.color_capability);
        let pal = resolve_palette_colors(&self.palette);

        let mut frame = RenderFrame::new(FrameSize { width, height }, theme);

        if self.quitting {
            return frame;
        }

        // Fill background with palette RGB color
        frame.fill_bg(
            Rect {
                x: 0,
                y: 0,
                width,
                height,
            },
            pal.background,
        );

        // Header line with panel background stripe.
        let header = self.render_header_text(width);
        frame.draw_styled_text(0, 0, &header, pal.accent, pal.panel, true);

        // Tab rail with styled active/inactive badge spans.
        let content_start = if self.focus_mode == FocusMode::DeepDebug {
            1
        } else {
            self.render_tab_rail(&mut frame, width, &pal);
            2
        };
        let failure_explain_strip = self.failure_explain_strip_text();
        let footer_lines = if self.status_text.is_empty() && failure_explain_strip.is_none() {
            1
        } else {
            2
        };
        let content_height = height.saturating_sub(content_start + footer_lines).max(1);

        match self.mode {
            UiMode::Help => {
                self.render_help_content(&mut frame, width, content_height, content_start);
            }
            UiMode::Palette => {
                let lines = self.command_palette.render_lines(width, content_height);
                for (idx, line) in lines.iter().enumerate() {
                    if idx >= content_height {
                        break;
                    }
                    let role = if idx == 0 {
                        TextRole::Accent
                    } else if idx == 1 {
                        TextRole::Muted
                    } else if line.starts_with(">") {
                        TextRole::Primary
                    } else {
                        TextRole::Muted
                    };
                    frame.draw_text(0, content_start + idx, line, role);
                }
            }
            UiMode::Search => {
                let lines = self.search_overlay.render_lines(width, content_height);
                for (idx, search_line) in lines.iter().enumerate() {
                    if idx >= content_height {
                        break;
                    }
                    let role = if idx == 0 {
                        TextRole::Accent
                    } else if idx == 1 {
                        TextRole::Muted
                    } else if search_line.selected {
                        TextRole::Primary
                    } else if search_line.highlighted {
                        TextRole::Success
                    } else {
                        TextRole::Muted
                    };
                    frame.draw_text(0, content_start + idx, &search_line.text, role);
                }
            }
            UiMode::RegexSearch => {
                let rendered_lines = self.rendered_log_lines();
                let matches = self.collect_regex_match_indices(&rendered_lines);
                let current = if matches.is_empty() {
                    0
                } else {
                    self.log_regex_selected_match.min(matches.len() - 1) + 1
                };
                let lines = [
                    "Regex Log Search  (type query, j/k next-prev match, enter apply, esc close)"
                        .to_owned(),
                    format!("query: /{}/", self.log_regex_query),
                    if self.log_regex_error.is_empty() {
                        format!(
                            "matches: {current}/{}  lines:{}",
                            matches.len(),
                            rendered_lines.len()
                        )
                    } else {
                        format!("error: {}", self.log_regex_error)
                    },
                ];
                for (idx, line) in lines.iter().enumerate() {
                    if idx >= content_height {
                        break;
                    }
                    let role = if idx == 0 {
                        TextRole::Accent
                    } else if idx == 2 && !self.log_regex_error.is_empty() {
                        TextRole::Danger
                    } else {
                        TextRole::Primary
                    };
                    frame.draw_text(0, content_start + idx, &trim_to_width(line, width), role);
                }
            }
            UiMode::Confirm => {
                if let Some(ref confirm) = self.confirm {
                    let prompt = &confirm.prompt;
                    let truncated = trim_to_width(prompt, width);
                    frame.draw_text(0, content_start, &truncated, TextRole::Danger);
                    let cancel_selected = confirm.selected == ConfirmRailSelection::Cancel;
                    let confirm_selected = confirm.selected == ConfirmRailSelection::Confirm;
                    let cancel_label = if cancel_selected {
                        "[Cancel]"
                    } else {
                        " Cancel "
                    };
                    let confirm_label = if confirm_selected {
                        "[Confirm]"
                    } else {
                        " Confirm "
                    };
                    frame.draw_text(0, content_start + 1, "Action rail:", TextRole::Muted);
                    frame.draw_text(
                        13,
                        content_start + 1,
                        cancel_label,
                        if cancel_selected {
                            TextRole::Accent
                        } else {
                            TextRole::Muted
                        },
                    );
                    frame.draw_text(
                        23,
                        content_start + 1,
                        confirm_label,
                        if confirm_selected {
                            TextRole::Danger
                        } else {
                            TextRole::Muted
                        },
                    );
                    frame.draw_text(
                        0,
                        content_start + 2,
                        &{
                            if confirm.reason_required {
                                trim_to_width(
                                    &format!(
                                        "Reason ({}/{}+): {}",
                                        confirm.reason.trim().chars().count(),
                                        DESTRUCTIVE_CONFIRM_REASON_MIN_CHARS,
                                        if confirm.reason.is_empty() {
                                            "<type reason>"
                                        } else {
                                            &confirm.reason
                                        }
                                    ),
                                    width,
                                )
                            } else {
                                "tab/left/right switch  enter select  y confirm  n cancel"
                                    .to_owned()
                            }
                        },
                        if confirm.reason_required
                            && confirm.reason.trim().chars().count()
                                < DESTRUCTIVE_CONFIRM_REASON_MIN_CHARS
                        {
                            TextRole::Danger
                        } else if confirm.reason_required {
                            TextRole::Success
                        } else {
                            TextRole::Muted
                        },
                    );
                    frame.draw_text(
                        0,
                        content_start + 3,
                        if confirm.reason_required {
                            "type reason  backspace edit  Ctrl+U clear  tab/left/right switch  enter select  esc/q cancel"
                        } else {
                            ""
                        },
                        TextRole::Muted,
                    );
                }
            }
            UiMode::Filter => {
                let filter_line = format!(
                    "Filter: {} [status: {}]",
                    self.filter_text, self.filter_state
                );
                let truncated = trim_to_width(&filter_line, width);
                frame.draw_text(0, content_start, &truncated, TextRole::Accent);
            }
            UiMode::Wizard => {
                for (idx, line) in self.render_wizard_lines(width).iter().enumerate() {
                    if idx >= content_height {
                        break;
                    }
                    let role = if idx == 0 {
                        TextRole::Accent
                    } else if line.starts_with("Error:") {
                        TextRole::Danger
                    } else {
                        TextRole::Primary
                    };
                    frame.draw_text(0, content_start + idx, line, role);
                }
            }
            _ => {
                // Delegate to registered view if available.
                if let Some(view) = self.views.get(&self.tab) {
                    let view_frame = crate::panel_error_boundary::render_panel_with_boundary(
                        self.tab.label(),
                        FrameSize {
                            width,
                            height: content_height,
                        },
                        theme,
                        &pal,
                        || {
                            view.view(
                                FrameSize {
                                    width,
                                    height: content_height,
                                },
                                theme,
                            )
                        },
                    );
                    blit_frame(&mut frame, &view_frame, 0, content_start);
                } else if self.mode == UiMode::Main && self.tab == MainTab::Overview {
                    let mut overview_frame = RenderFrame::new(
                        FrameSize {
                            width,
                            height: content_height,
                        },
                        theme,
                    );
                    crate::overview_tab::render_overview_paneled_with_options(
                        &mut overview_frame,
                        &self.loops,
                        self.selected_view(),
                        &self.run_history,
                        self.selected_run,
                        &pal,
                        Rect {
                            x: 0,
                            y: 0,
                            width,
                            height: content_height,
                        },
                        self.focus_right,
                        crate::overview_tab::OverviewPaneOptions {
                            reserve_next_action_slot: true,
                        },
                    );
                    blit_frame(&mut frame, &overview_frame, 0, content_start);
                } else if self.mode == UiMode::Main && self.tab == MainTab::Logs {
                    let logs_frame = crate::panel_error_boundary::render_panel_with_boundary(
                        MainTab::Logs.label(),
                        FrameSize {
                            width,
                            height: content_height,
                        },
                        theme,
                        &pal,
                        || self.render_logs_pane(width, content_height, &pal, self.focus_right),
                    );
                    blit_frame(&mut frame, &logs_frame, 0, content_start);
                } else if self.mode == UiMode::Main && self.tab == MainTab::Runs {
                    let runs_frame = crate::panel_error_boundary::render_panel_with_boundary(
                        MainTab::Runs.label(),
                        FrameSize {
                            width,
                            height: content_height,
                        },
                        theme,
                        &pal,
                        || {
                            let runs_state = crate::runs_tab::RunsTabState {
                                runs: self
                                    .run_history
                                    .iter()
                                    .map(|rv| crate::runs_tab::RunEntry {
                                        id: rv.id.clone(),
                                        status: rv.status.clone(),
                                        exit_code: rv.exit_code,
                                        profile_name: rv.profile_name.clone(),
                                        profile_id: rv.profile_id.clone(),
                                        harness: rv.harness.clone(),
                                        started_at: rv.started_at.clone(),
                                        duration_display: rv.duration.clone(),
                                        output_lines: rv.output_lines.clone(),
                                    })
                                    .collect(),
                                selected_run: self.selected_run,
                                layer_label: self.log_layer.label().to_owned(),
                                loop_display_id: self
                                    .selected_view()
                                    .map(|lv| crate::filter::loop_display_id(&lv.id, &lv.short_id))
                                    .unwrap_or_default(),
                                log_scroll: self.log_scroll,
                            };
                            crate::runs_tab::render_runs_paneled(
                                &runs_state,
                                FrameSize {
                                    width,
                                    height: content_height,
                                },
                                theme,
                                &pal,
                                self.focus_right,
                            )
                        },
                    );
                    blit_frame(&mut frame, &runs_frame, 0, content_start);
                } else if self.mode == UiMode::Main && self.tab == MainTab::MultiLogs {
                    let multi_frame = crate::panel_error_boundary::render_panel_with_boundary(
                        MainTab::MultiLogs.label(),
                        FrameSize {
                            width,
                            height: content_height,
                        },
                        theme,
                        &pal,
                        || self.render_multi_logs_pane(width, content_height, &pal),
                    );
                    blit_frame(&mut frame, &multi_frame, 0, content_start);
                } else if self.mode == UiMode::Main && self.tab == MainTab::Inbox {
                    let inbox_frame = crate::panel_error_boundary::render_panel_with_boundary(
                        MainTab::Inbox.label(),
                        FrameSize {
                            width,
                            height: content_height,
                        },
                        theme,
                        &pal,
                        || self.render_inbox_pane(width, content_height, &pal, self.focus_right),
                    );
                    blit_frame(&mut frame, &inbox_frame, 0, content_start);
                } else {
                    // Placeholder: show tab label + selection info.
                    let info = format!(
                        "{} tab  |  {} loops  |  selected: {}",
                        self.tab.label(),
                        self.filtered.len(),
                        if self.selected_id.is_empty() {
                            "none"
                        } else {
                            &self.selected_id
                        }
                    );
                    frame.draw_text(0, content_start, &info, TextRole::Primary);
                }
            }
        }

        if self.should_render_onboarding_overlay() {
            self.render_onboarding_overlay(&mut frame, width, content_height, content_start);
        }

        // Status line with semantic color.
        if !self.status_text.is_empty() {
            let status_y = height.saturating_sub(2);
            let status_fg = match self.status_kind {
                StatusKind::Ok => pal.success,
                StatusKind::Err => pal.error,
                StatusKind::Info => pal.info,
            };
            let status_text = self.status_display_text();
            let truncated = trim_to_width(&status_text, width);
            frame.draw_styled_text(0, status_y, &truncated, status_fg, pal.background, false);
        } else if let Some(strip) = &failure_explain_strip {
            let status_y = height.saturating_sub(2);
            let truncated = trim_to_width(strip, width);
            frame.draw_styled_text(0, status_y, &truncated, pal.warning, pal.background, false);
        }

        // Footer hint line with panel background stripe.
        let footer_y = height.saturating_sub(1);
        let hint = self.footer_hint_line();
        let truncated = trim_to_width(&hint, width);
        // Footer bar: fill with panel bg, then draw text
        frame.fill_bg(
            Rect {
                x: 0,
                y: footer_y,
                width,
                height: 1,
            },
            pal.panel,
        );
        frame.draw_styled_text(0, footer_y, &truncated, pal.text_muted, pal.panel, false);

        frame
    }

    fn footer_hint_line(&self) -> String {
        let max_hints = if self.focus_mode == FocusMode::DeepDebug {
            6
        } else if self.density_mode == DensityMode::Compact {
            7
        } else {
            8
        };
        let ranked = self.hint_ranker.rank(&self.footer_hint_specs(), max_hints);
        ranked
            .into_iter()
            .map(HintSpec::render)
            .collect::<Vec<String>>()
            .join("  ")
    }

    fn footer_hint_specs(&self) -> Vec<HintSpec> {
        let mut hints = if self.focus_mode == FocusMode::DeepDebug {
            vec![
                HintSpec::new("?", "help", 10, Some(KeyCommand::ToggleHelp)),
                HintSpec::new("q", "quit", 9, Some(KeyCommand::Quit)),
                HintSpec::new("Z", "toggle focus", 7, None),
                HintSpec::new("M", "density", 6, None),
                HintSpec::new("z", "zen", 6, Some(KeyCommand::ToggleZen)),
                HintSpec::new("A", "a11y-mode", 6, None),
                HintSpec::new("E", "export", 6, Some(KeyCommand::ExportCurrentView)),
                HintSpec::new("ctrl+e", "evidence", 5, Some(KeyCommand::JumpEvidenceError)),
                HintSpec::new("ctrl+b", "return", 5, Some(KeyCommand::JumpEvidenceBack)),
                HintSpec::new("ctrl+y", "copy", 5, None),
                HintSpec::new("i", "dismiss-hints", 4, None),
                HintSpec::new("I", "recall-hints", 4, None),
                HintSpec::new("ctrl+p", "palette", 5, Some(KeyCommand::OpenPalette)),
            ]
        } else if self.density_mode == DensityMode::Compact {
            vec![
                HintSpec::new("?", "help", 10, Some(KeyCommand::ToggleHelp)),
                HintSpec::new("q", "quit", 9, Some(KeyCommand::Quit)),
                HintSpec::new("ctrl+p", "palette", 8, Some(KeyCommand::OpenPalette)),
                HintSpec::new("/", "filter", 8, Some(KeyCommand::OpenFilter)),
                HintSpec::new("1-5", "tabs", 7, Some(KeyCommand::SwitchTabOverview)),
                HintSpec::new("j/k", "sel", 7, Some(KeyCommand::MoveSelectionNext)),
                HintSpec::new("ctrl+f", "search", 6, Some(KeyCommand::OpenSearch)),
                HintSpec::new("z", "zen", 6, Some(KeyCommand::ToggleZen)),
                HintSpec::new("A", "a11y-mode", 6, None),
                HintSpec::new("E", "export", 6, Some(KeyCommand::ExportCurrentView)),
                HintSpec::new("ctrl+e", "evidence", 5, Some(KeyCommand::JumpEvidenceError)),
                HintSpec::new("ctrl+b", "return", 5, Some(KeyCommand::JumpEvidenceBack)),
                HintSpec::new("ctrl+y", "copy", 5, None),
                HintSpec::new("F", "follow", 5, Some(KeyCommand::ToggleFollow)),
                HintSpec::new("M", "density", 5, None),
                HintSpec::new("Z", "focus", 5, None),
            ]
        } else {
            vec![
                HintSpec::new("?", "help", 10, Some(KeyCommand::ToggleHelp)),
                HintSpec::new("q", "quit", 9, Some(KeyCommand::Quit)),
                HintSpec::new("ctrl+p", "palette", 9, Some(KeyCommand::OpenPalette)),
                HintSpec::new("/", "filter", 8, Some(KeyCommand::OpenFilter)),
                HintSpec::new("ctrl+f", "search", 8, Some(KeyCommand::OpenSearch)),
                HintSpec::new("1-5", "tabs", 8, Some(KeyCommand::SwitchTabOverview)),
                HintSpec::new("j/k", "sel", 7, Some(KeyCommand::MoveSelectionNext)),
                HintSpec::new("E", "export", 7, Some(KeyCommand::ExportCurrentView)),
                HintSpec::new("ctrl+e", "evidence", 6, Some(KeyCommand::JumpEvidenceError)),
                HintSpec::new("ctrl+b", "return", 6, Some(KeyCommand::JumpEvidenceBack)),
                HintSpec::new("ctrl+y", "copy", 6, None),
                HintSpec::new("t/T", "theme", 6, Some(KeyCommand::CycleTheme)),
                HintSpec::new("A", "a11y-mode", 6, None),
                HintSpec::new("M", "density", 6, None),
                HintSpec::new("Z", "focus", 6, None),
                HintSpec::new("F", "follow", 5, Some(KeyCommand::ToggleFollow)),
            ]
        };
        match self.tab {
            MainTab::Logs => {
                hints.push(HintSpec::new(
                    "v",
                    "source",
                    6,
                    Some(KeyCommand::LogsCycleSource),
                ));
                hints.push(HintSpec::new(
                    "x",
                    "layer",
                    6,
                    Some(KeyCommand::CycleLogLayer),
                ));
                hints.push(HintSpec::new(
                    "u/d",
                    "scroll",
                    5,
                    Some(KeyCommand::ScrollLogsDown),
                ));
                hints.push(HintSpec::new("R", "regex", 6, None));
            }
            MainTab::Runs => {
                hints.push(HintSpec::new(
                    "x",
                    "layer",
                    6,
                    Some(KeyCommand::CycleLogLayer),
                ));
                hints.push(HintSpec::new(
                    ",/.",
                    "run",
                    6,
                    Some(KeyCommand::RunSelectionNext),
                ));
                hints.push(HintSpec::new(
                    "u/d",
                    "scroll",
                    5,
                    Some(KeyCommand::ScrollLogsDown),
                ));
                hints.push(HintSpec::new("R", "regex", 6, None));
            }
            MainTab::MultiLogs => {
                hints.push(HintSpec::new(
                    "m",
                    "layout",
                    7,
                    Some(KeyCommand::MultiCycleLayout),
                ));
                hints.push(HintSpec::new(
                    "g/G",
                    "pages",
                    6,
                    Some(KeyCommand::MultiPageNext),
                ));
                hints.push(HintSpec::new(
                    ",/.",
                    "page",
                    6,
                    Some(KeyCommand::MultiPageNext),
                ));
                hints.push(HintSpec::new(
                    "x",
                    "layer",
                    6,
                    Some(KeyCommand::CycleLogLayer),
                ));
                hints.push(HintSpec::new("C", "compare", 7, None));
            }
            MainTab::Inbox => {
                hints.push(HintSpec::new("f", "inbox-filter", 7, None));
                hints.push(HintSpec::new("a", "ack", 7, None));
                hints.push(HintSpec::new("h", "handoff", 7, None));
                hints.push(HintSpec::new("o/O", "claim", 6, None));
            }
            MainTab::Overview => {}
        }
        hints
    }

    fn render_header_text(&self, width: usize) -> String {
        let count_label = if self.tab == MainTab::Inbox {
            let threads = self.inbox_threads();
            let unread = threads
                .iter()
                .map(|thread| thread.unread_count)
                .sum::<usize>();
            format!("{} threads, {} unread", threads.len(), unread)
        } else {
            format!("{}/{} loops", self.filtered.len(), self.loops.len())
        };
        let mode_label = match self.mode {
            UiMode::Wizard => "  mode:New Loop Wizard",
            UiMode::Palette => "  mode:Command Palette",
            UiMode::Filter => "  mode:Filter",
            UiMode::Help => "  mode:Help",
            UiMode::Confirm => "  mode:Confirm",
            UiMode::ExpandedLogs => "  mode:Expanded Logs",
            UiMode::RegexSearch => "  mode:Regex Search",
            UiMode::Search => "  mode:Search",
            UiMode::Main => "",
        };
        let follow_label = if matches!(self.tab, MainTab::Logs | MainTab::Runs | MainTab::MultiLogs)
            || self.mode == UiMode::ExpandedLogs
        {
            if self.follow_mode {
                "  follow:ON"
            } else {
                "  follow:off"
            }
        } else {
            ""
        };
        let header = format!(
            " Forge Loops  [{tab}]  {count}  theme:{theme}  density:{density}  focus:{focus}{mode}{follow}",
            tab = self.tab.label(),
            count = count_label,
            theme = self.palette.name,
            density = self.density_mode.label(),
            focus = self.focus_mode.label(),
            mode = mode_label,
            follow = follow_label,
        );
        trim_to_width(&header, width)
    }

    #[allow(dead_code)]
    fn render_tab_bar(&self, width: usize) -> String {
        let tabs: Vec<String> = MainTab::ORDER
            .iter()
            .enumerate()
            .map(|(i, t)| {
                let label = if self.density_mode == DensityMode::Compact {
                    t.short_label()
                } else {
                    t.label()
                };
                if *t == self.tab {
                    format!("[{}:{}]", i + 1, label)
                } else {
                    format!(" {}:{} ", i + 1, label)
                }
            })
            .collect();
        let bar = tabs.join("  ");
        trim_to_width(&bar, width)
    }

    /// Render tab rail into the frame at row 1 using styled badge spans.
    ///
    /// Each tab is a styled badge: active tabs get accent/bold styling on the
    /// panel background, inactive tabs get muted styling on panel_alt.  A
    /// two-space gap separates adjacent tabs.
    fn render_tab_rail(&self, frame: &mut RenderFrame, width: usize, pal: &ResolvedPalette) {
        // Fill row with panel_alt background.
        frame.fill_bg(
            Rect {
                x: 0,
                y: 1,
                width,
                height: 1,
            },
            pal.panel_alt,
        );

        let active_style = CellStyle {
            fg: pal.accent,
            bg: pal.panel,
            bold: true,
            dim: false,
            underline: false,
        };
        let inactive_style = CellStyle {
            fg: pal.text_muted,
            bg: pal.panel_alt,
            bold: false,
            dim: false,
            underline: false,
        };
        let gap_style = CellStyle {
            fg: pal.panel_alt,
            bg: pal.panel_alt,
            bold: false,
            dim: false,
            underline: false,
        };

        let mut spans: Vec<StyledSpan<'_>> = Vec::with_capacity(MainTab::ORDER.len() * 2);
        let mut labels: Vec<String> = Vec::with_capacity(MainTab::ORDER.len());

        for (i, t) in MainTab::ORDER.iter().enumerate() {
            let label = if self.density_mode == DensityMode::Compact {
                t.short_label()
            } else {
                t.label()
            };
            labels.push(format!(" {}:{} ", i + 1, label));
        }

        for (i, t) in MainTab::ORDER.iter().enumerate() {
            if i > 0 {
                spans.push(StyledSpan::cell("  ", gap_style));
            }
            let is_active = *t == self.tab;
            let style = if is_active {
                active_style
            } else {
                inactive_style
            };
            spans.push(StyledSpan::cell(&labels[i], style));
        }

        frame.draw_spans(0, 1, &spans);
    }

    /// Build tab rail badges for the upstream ftui render path.
    ///
    /// Returns a `Vec` of `Badge` widgets, one per tab, with active/inactive
    /// styling derived from the adapter's theme tokens.  Callers position and
    /// render each badge side-by-side into an ftui `Frame`.
    #[cfg(feature = "frankentui-bootstrap")]
    #[must_use]
    pub fn build_ftui_tab_badges(
        &self,
        theme: forge_ftui_adapter::style::ThemeSpec,
    ) -> Vec<forge_ftui_adapter::upstream_primitives::Badge<'static>> {
        use forge_ftui_adapter::style::StyleToken;
        use forge_ftui_adapter::upstream_bridge::{term_color_to_packed_rgba, token_style};
        use forge_ftui_adapter::upstream_primitives::badge;

        let active_style = {
            let s = token_style(theme, StyleToken::Accent);
            let bg = term_color_to_packed_rgba(forge_ftui_adapter::render::TermColor::Ansi256(
                theme.color(StyleToken::Surface),
            ));
            s.bg(bg)
        };
        let inactive_style = token_style(theme, StyleToken::Muted);

        MainTab::ORDER
            .iter()
            .enumerate()
            .map(|(i, t)| {
                let label = if self.density_mode == DensityMode::Compact {
                    t.short_label()
                } else {
                    t.label()
                };
                let is_active = *t == self.tab;
                let text: &'static str = Box::leak(format!("{}:{}", i + 1, label).into_boxed_str());
                badge(text).with_style(if is_active {
                    active_style
                } else {
                    inactive_style
                })
            })
            .collect()
    }

    fn render_wizard_lines(&self, width: usize) -> Vec<String> {
        let step = self.wizard.step.clamp(1, 4);
        let mut lines = vec![
            format!("New loop wizard (step {step}/4)"),
            "1) Identity+Count  2) Pool/Profile  3) Prompt+Runtime  4) Review+Submit".to_owned(),
            String::new(),
        ];

        match step {
            1 => {
                lines.push(self.render_wizard_field("name", &self.wizard.values.name, 0));
                lines.push(self.render_wizard_field(
                    "name-prefix",
                    &self.wizard.values.name_prefix,
                    1,
                ));
                lines.push(self.render_wizard_field("count", &self.wizard.values.count, 2));
            }
            2 => {
                lines.push(self.render_wizard_field("pool", &self.wizard.values.pool, 0));
                lines.push(self.render_wizard_field("profile", &self.wizard.values.profile, 1));
            }
            3 => {
                lines.push(self.render_wizard_field("prompt", &self.wizard.values.prompt, 0));
                lines.push(self.render_wizard_field(
                    "prompt-msg",
                    &self.wizard.values.prompt_msg,
                    1,
                ));
                lines.push(self.render_wizard_field("interval", &self.wizard.values.interval, 2));
                lines.push(self.render_wizard_field(
                    "max-runtime",
                    &self.wizard.values.max_runtime,
                    3,
                ));
                lines.push(self.render_wizard_field(
                    "max-iterations",
                    &self.wizard.values.max_iterations,
                    4,
                ));
                lines.push(self.render_wizard_field("tags", &self.wizard.values.tags, 5));
            }
            4 => {
                lines.push("Review:".to_owned());
                lines.push(format!("  name={:?}", self.wizard.values.name));
                lines.push(format!(
                    "  name-prefix={:?}",
                    self.wizard.values.name_prefix
                ));
                lines.push(format!("  count={:?}", self.wizard.values.count));
                lines.push(format!("  pool={:?}", self.wizard.values.pool));
                lines.push(format!("  profile={:?}", self.wizard.values.profile));
                lines.push(format!("  prompt={:?}", self.wizard.values.prompt));
                lines.push(format!("  prompt-msg={:?}", self.wizard.values.prompt_msg));
                lines.push(format!("  interval={:?}", self.wizard.values.interval));
                lines.push(format!(
                    "  max-runtime={:?}",
                    self.wizard.values.max_runtime
                ));
                lines.push(format!(
                    "  max-iterations={:?}",
                    self.wizard.values.max_iterations
                ));
                lines.push(format!("  tags={:?}", self.wizard.values.tags));
            }
            _ => {}
        }

        lines.push(String::new());
        lines.push("tab/down/up navigate fields, enter next/submit, b back, esc cancel".to_owned());
        if !self.wizard.error.is_empty() {
            lines.push(format!("Error: {}", self.wizard.error));
        }

        lines
            .into_iter()
            .map(|line| trim_to_width(&line, width))
            .collect()
    }

    fn render_wizard_field(&self, label: &str, value: &str, field: usize) -> String {
        let display = if value.trim().is_empty() {
            "<empty>"
        } else {
            value
        };
        if self.wizard.field == field {
            format!("{label}: {display}_")
        } else {
            format!("{label}: {display}")
        }
    }

    fn render_logs_pane(
        &self,
        width: usize,
        height: usize,
        pal: &ResolvedPalette,
        focus_right: bool,
    ) -> RenderFrame {
        let theme = crate::theme_for_capability(self.color_capability);
        let mut frame = RenderFrame::new(FrameSize { width, height }, theme);
        if width < 4 || height < 4 {
            return frame;
        }

        frame.fill_bg(
            Rect {
                x: 0,
                y: 0,
                width,
                height,
            },
            pal.background,
        );

        let border_role = if focus_right {
            TextRole::Accent
        } else {
            TextRole::Muted
        };
        let title = if let Some(selected) = self.selected_view() {
            format!(
                "Logs · {}:{}",
                crate::filter::loop_display_id(&selected.id, &selected.short_id),
                selected.name
            )
        } else {
            "Logs".to_owned()
        };
        let inner = frame.draw_panel(
            Rect {
                x: 0,
                y: 0,
                width,
                height,
            },
            &title,
            BorderStyle::Rounded,
            frame.color_for_role(border_role),
            pal.panel,
        );
        if inner.width == 0 || inner.height == 0 {
            return frame;
        }

        let rendered_lines = render_lines_for_layer(
            &self.selected_log.lines,
            map_log_render_layer(self.log_layer),
            true,
        );
        let regex_matches = self.collect_regex_match_indices(&rendered_lines);
        let selected_regex_line = if regex_matches.is_empty() {
            None
        } else {
            Some(regex_matches[self.log_regex_selected_match.min(regex_matches.len() - 1)])
        };
        let regex_suffix = if self.log_regex_query.trim().is_empty() {
            String::new()
        } else if !self.log_regex_error.is_empty() {
            format!("  regex:{}", self.log_regex_error)
        } else {
            format!(
                "  regex:/{}/ {}/{}",
                self.log_regex_query,
                if regex_matches.is_empty() {
                    0
                } else {
                    self.log_regex_selected_match.min(regex_matches.len() - 1) + 1
                },
                regex_matches.len()
            )
        };

        let info_line = format!(
            "source:{}  layer:{}  follow:{}  scroll:{}  lines:{}{}",
            self.log_source.label(),
            self.log_layer.label(),
            if self.follow_mode { "on" } else { "off" },
            self.log_scroll,
            rendered_lines.len(),
            regex_suffix
        );
        frame.draw_styled_text(
            inner.x,
            inner.y,
            &trim_to_width(&info_line, inner.width),
            pal.text_muted,
            pal.panel,
            false,
        );

        let available = inner.height.saturating_sub(1);
        if available == 0 {
            return frame;
        }

        if rendered_lines.is_empty() {
            let message = if self.selected_log.message.trim().is_empty() {
                "No log content yet"
            } else {
                self.selected_log.message.trim()
            };
            frame.draw_styled_text(
                inner.x,
                inner.y + 1,
                &trim_to_width(message, inner.width),
                pal.text_muted,
                pal.panel,
                false,
            );
            return frame;
        }

        let (start, end, _) =
            crate::multi_logs::log_window_bounds(rendered_lines.len(), available, self.log_scroll);
        for (offset, line) in rendered_lines[start..end].iter().enumerate() {
            let line_index = start + offset;
            let is_regex_match = regex_matches.binary_search(&line_index).is_ok();
            let role = if Some(line_index) == selected_regex_line {
                TextRole::Accent
            } else if is_regex_match {
                TextRole::Success
            } else if line.starts_with("! [ANOM:") {
                TextRole::Danger
            } else {
                TextRole::Primary
            };
            let decorated = if Some(line_index) == selected_regex_line {
                format!("> {line}")
            } else if is_regex_match {
                format!("* {line}")
            } else {
                line.clone()
            };
            frame.draw_text(
                inner.x,
                inner.y + 1 + offset,
                &trim_to_width(&decorated, inner.width),
                role,
            );
        }
        frame
    }

    fn render_inbox_pane(
        &self,
        width: usize,
        height: usize,
        pal: &ResolvedPalette,
        focus_right: bool,
    ) -> RenderFrame {
        let theme = crate::theme_for_capability(self.color_capability);
        let mut frame = RenderFrame::new(FrameSize { width, height }, theme);
        if width < 4 || height < 4 {
            return frame;
        }

        // Fill background
        frame.fill_bg(
            Rect {
                x: 0,
                y: 0,
                width,
                height,
            },
            pal.background,
        );

        let threads = self.inbox_threads();
        let claim_conflicts = self.claim_conflicts();
        let unread_total: usize = threads.iter().map(|t| t.unread_count).sum();
        let pending_ack_total: usize = threads.iter().map(|t| t.pending_ack_count).sum();

        // -- Header stats line (styled, not in a panel) --
        let header = format!(
            "Inbox filter:{}  threads:{}  unread:{}  pending-ack:{}  claims:{}  conflicts:{}",
            self.inbox_filter.label(),
            threads.len(),
            unread_total,
            pending_ack_total,
            self.claim_events.len(),
            claim_conflicts.len()
        );
        frame.draw_styled_text(
            0,
            0,
            &trim_to_width(&header, width),
            pal.accent,
            pal.background,
            false,
        );

        // Reserve rows for claim timeline panel at bottom
        let timeline_panel_h = if self.claim_events.is_empty() {
            0usize
        } else {
            5usize.min(height.saturating_sub(2)) // 2 border + up to 3 events
        };
        let body_height = height.saturating_sub(1 + timeline_panel_h); // 1 for header row
        let body_y = 1usize;

        if threads.is_empty() {
            // Empty state panel
            let empty_h = 4usize.min(body_height);
            let empty_rect = Rect {
                x: 0,
                y: body_y,
                width,
                height: empty_h,
            };
            let inner = frame.draw_panel(
                empty_rect,
                "No Messages",
                BorderStyle::Rounded,
                pal.border,
                pal.panel,
            );
            if inner.height > 0 {
                frame.draw_styled_text(
                    inner.x,
                    inner.y,
                    "No messages for selected filter",
                    pal.text_muted,
                    pal.panel,
                    false,
                );
            }
            if inner.height > 1 {
                frame.draw_styled_text(
                    inner.x,
                    inner.y + 1,
                    "keys: f filter  j/k select  enter read  a ack  h handoff  r reply",
                    pal.text_muted,
                    pal.panel,
                    false,
                );
            }
        } else {
            // Two-panel layout: thread list (left) + detail (right)
            let min_detail_width = 30usize;
            let list_panel_width = if width > min_detail_width + 24 {
                (width * 2 / 5).clamp(24, width - min_detail_width - 1)
            } else {
                width
            };
            let has_detail = list_panel_width + 2 < width;
            let detail_panel_width = if has_detail {
                width - list_panel_width - 1 // 1 gap column
            } else {
                0
            };

            // -- Thread list panel (focused when focus_right is false) --
            let list_border = if !focus_right { pal.accent } else { pal.border };
            let list_rect = Rect {
                x: 0,
                y: body_y,
                width: list_panel_width,
                height: body_height,
            };
            let list_inner = frame.draw_panel(
                list_rect,
                "Threads",
                BorderStyle::Rounded,
                list_border,
                pal.panel,
            );
            for row in 0..list_inner.height {
                let Some(thread) = threads.get(row) else {
                    break;
                };
                let selected = row == self.inbox_selected_thread;
                let prefix = if selected { "\u{25B8}" } else { " " }; // ▸ for selected
                let line = format!(
                    "{prefix} {} [u:{}][a:{}] ({})",
                    thread.subject,
                    thread.unread_count,
                    thread.pending_ack_count,
                    format_mail_id(thread.latest_message_id),
                );
                let fg = if selected { pal.accent } else { pal.text_muted };
                let bold = selected;
                frame.draw_styled_text(
                    list_inner.x,
                    list_inner.y + row,
                    &trim_to_width(&line, list_inner.width),
                    fg,
                    pal.panel,
                    bold,
                );
            }

            // -- Detail panel (right side) --
            if has_detail {
                let detail_x = list_panel_width + 1;
                let detail_rect = Rect {
                    x: detail_x,
                    y: body_y,
                    width: detail_panel_width,
                    height: body_height,
                };

                if let Some(selected_thread) = threads.get(self.inbox_selected_thread) {
                    let detail_title = format!(
                        "thread:{}  msgs:{}  participants:{}",
                        selected_thread.thread_key,
                        selected_thread.message_indices.len(),
                        selected_thread.participant_count
                    );
                    let detail_border = if focus_right { pal.accent } else { pal.border };
                    let detail_inner = frame.draw_panel(
                        detail_rect,
                        &trim_to_width(&detail_title, detail_panel_width.saturating_sub(4)),
                        BorderStyle::Rounded,
                        detail_border,
                        pal.panel,
                    );

                    let mut row = 0usize;

                    // Hint line
                    if row < detail_inner.height {
                        frame.draw_styled_text(
                            detail_inner.x, detail_inner.y + row,
                            &trim_to_width(
                                "enter=read  a=ack  h=handoff  r=reply  o=next-conflict  O=resolution",
                                detail_inner.width,
                            ),
                            pal.text_muted, pal.panel, false,
                        );
                        row += 1;
                    }

                    // Handoff snapshot
                    if let Some(snapshot) = self
                        .handoff_snapshot
                        .as_ref()
                        .filter(|s| s.thread_key == selected_thread.thread_key)
                    {
                        if row < detail_inner.height {
                            frame.draw_styled_text(
                                detail_inner.x,
                                detail_inner.y + row,
                                &trim_to_width(
                                    "handoff snapshot (h regenerate)",
                                    detail_inner.width,
                                ),
                                pal.info,
                                pal.panel,
                                true,
                            );
                            row += 1;
                        }
                        for line in snapshot.lines() {
                            if row >= detail_inner.height {
                                break;
                            }
                            frame.draw_styled_text(
                                detail_inner.x,
                                detail_inner.y + row,
                                &trim_to_width(&line, detail_inner.width),
                                pal.text,
                                pal.panel,
                                false,
                            );
                            row += 1;
                        }
                        if row < detail_inner.height {
                            frame.draw_styled_text(
                                detail_inner.x,
                                detail_inner.y + row,
                                "recent thread messages",
                                pal.text_muted,
                                pal.panel,
                                false,
                            );
                            row += 1;
                        }
                    }

                    if let Some(latest_message_idx) =
                        selected_thread.message_indices.last().copied()
                    {
                        if let Some(latest_message) = self.inbox_messages.get(latest_message_idx) {
                            let markdown_lines = markdown_to_plain_lines(
                                &latest_message.body,
                                detail_inner.width,
                                4,
                            );
                            if !markdown_lines.is_empty() && row < detail_inner.height {
                                frame.draw_styled_text(
                                    detail_inner.x,
                                    detail_inner.y + row,
                                    "markdown detail (latest body)",
                                    pal.info,
                                    pal.panel,
                                    false,
                                );
                                row += 1;
                            }
                            for line in markdown_lines {
                                if row >= detail_inner.height {
                                    break;
                                }
                                frame.draw_styled_text(
                                    detail_inner.x,
                                    detail_inner.y + row,
                                    &line,
                                    pal.text,
                                    pal.panel,
                                    false,
                                );
                                row += 1;
                            }
                        }
                    }

                    // Thread messages (newest first)
                    for idx in selected_thread.message_indices.iter().rev() {
                        if row >= detail_inner.height {
                            break;
                        }
                        let Some(message) = self.inbox_messages.get(*idx) else {
                            continue;
                        };
                        let unread_mark = if message.read_at.is_none() { "*" } else { " " };
                        let ack_mark = if message.ack_required && message.acked_at.is_none() {
                            "!"
                        } else if message.acked_at.is_some() {
                            "a"
                        } else {
                            "-"
                        };
                        let preview = if !message.subject.trim().is_empty() {
                            message.subject.trim()
                        } else if !message.body.trim().is_empty() {
                            message.body.trim()
                        } else {
                            "(empty)"
                        };
                        let line = format!(
                            "{unread_mark}{ack_mark} {} {} {}",
                            format_mail_id(message.id),
                            message.from.trim(),
                            preview
                        );
                        let fg = if message.read_at.is_none() {
                            pal.text
                        } else {
                            pal.text_muted
                        };
                        frame.draw_styled_text(
                            detail_inner.x,
                            detail_inner.y + row,
                            &trim_to_width(&line, detail_inner.width),
                            fg,
                            pal.panel,
                            false,
                        );
                        row += 1;

                        if row >= detail_inner.height {
                            break;
                        }
                        if !message.subject.trim().is_empty() {
                            let subject_line = format!("  subject: {}", message.subject.trim());
                            frame.draw_styled_text(
                                detail_inner.x,
                                detail_inner.y + row,
                                &trim_to_width(&subject_line, detail_inner.width),
                                pal.text_muted,
                                pal.panel,
                                false,
                            );
                            row += 1;
                        }

                        let body_width = detail_inner.width.saturating_sub(2);
                        for detail_line in
                            render_inbox_markdown_detail_lines(&message.body, body_width)
                        {
                            if row >= detail_inner.height {
                                break;
                            }
                            let indented = if detail_line.is_empty() {
                                String::new()
                            } else {
                                format!("  {detail_line}")
                            };
                            frame.draw_styled_text(
                                detail_inner.x,
                                detail_inner.y + row,
                                &trim_to_width(&indented, detail_inner.width),
                                fg,
                                pal.panel,
                                false,
                            );
                            row += 1;
                        }

                        if row < detail_inner.height {
                            row += 1;
                        }
                    }
                } else {
                    // No thread selected — draw empty detail panel
                    frame.draw_panel(
                        detail_rect,
                        "Detail",
                        BorderStyle::Rounded,
                        pal.border,
                        pal.panel,
                    );
                }
            }
        }

        // -- Claim timeline panel (bottom) --
        if timeline_panel_h > 0 {
            let tl_y = height - timeline_panel_h;
            let tl_rect = Rect {
                x: 0,
                y: tl_y,
                width,
                height: timeline_panel_h,
            };
            let tl_inner = frame.draw_panel(
                tl_rect,
                "Claim Timeline (latest)",
                BorderStyle::Rounded,
                pal.border,
                pal.panel,
            );
            let conflict_task_ids: HashSet<&str> =
                claim_conflicts.iter().map(|c| c.task_id.as_str()).collect();
            let highlight_task = claim_conflicts
                .get(self.selected_claim_conflict)
                .map(|c| c.task_id.as_str());
            for row in 0..tl_inner.height {
                let Some(event) = self.claim_events.get(row) else {
                    break;
                };
                let flag = if conflict_task_ids.contains(event.task_id.as_str()) {
                    "!"
                } else {
                    " "
                };
                let line = format!(
                    "{flag} {} {} <- {}",
                    event.claimed_at, event.task_id, event.claimed_by
                );
                let fg = if Some(event.task_id.as_str()) == highlight_task {
                    pal.error
                } else if flag == "!" {
                    pal.warning
                } else {
                    pal.text_muted
                };
                frame.draw_styled_text(
                    tl_inner.x,
                    tl_inner.y + row,
                    &trim_to_width(&line, tl_inner.width),
                    fg,
                    pal.panel,
                    false,
                );
            }
        }

        frame
    }

    fn status_display_text(&self) -> String {
        let mut out = if self.status_kind == StatusKind::Err {
            let trimmed = self.status_text.trim();
            if trimmed.starts_with("Error:") {
                trimmed.to_owned()
            } else {
                format!("Error: {trimmed}")
            }
        } else {
            self.status_text.clone()
        };
        if out.trim().is_empty() {
            if let Some(event) = self.latest_visible_notification() {
                out = if event.kind == StatusKind::Err {
                    let trimmed = event.text.trim();
                    if trimmed.starts_with("Error:") {
                        trimmed.to_owned()
                    } else {
                        format!("Error: {trimmed}")
                    }
                } else {
                    event.text.clone()
                };
            }
        }
        let queued = self.visible_notification_count().saturating_sub(1);
        if queued > 0 && !out.trim().is_empty() {
            out.push_str(&format!(" (+{queued} queued)"));
        }
        let timers = self.pending_scheduled_timer_count();
        if timers > 0 {
            let next_due = self.next_scheduled_timer_in_ticks().unwrap_or(0);
            let timer_summary = format!("timers:{timers} next:{next_due}t");
            if out.trim().is_empty() {
                out = timer_summary;
            } else {
                out.push_str(&format!(" [{timer_summary}]"));
            }
        }
        out
    }

    fn failure_explain_strip_text(&self) -> Option<String> {
        let lines = self.failure_explain_source_lines()?;
        let focus = crate::failure_focus::build_failure_focus(lines, None)?;
        let mut links = focus.links;
        links.sort_by(|a, b| {
            failure_explain_label_priority(&a.label)
                .cmp(&failure_explain_label_priority(&b.label))
                .then(a.line_index.cmp(&b.line_index))
        });

        let mut seen_labels: HashSet<&'static str> = HashSet::new();
        let mut top_causes = Vec::new();
        for link in links {
            let label = failure_explain_label_display(&link.label);
            if !seen_labels.insert(label) {
                continue;
            }
            let compact = trim_to_width(
                &link.text.split_whitespace().collect::<Vec<_>>().join(" "),
                36,
            );
            top_causes.push(format!("{label}={compact}"));
            if top_causes.len() >= 3 {
                break;
            }
        }

        if top_causes.is_empty() {
            None
        } else {
            Some(format!("Failure explain: {}", top_causes.join("  |  ")))
        }
    }

    fn failure_explain_source_lines(&self) -> Option<&[String]> {
        if self.tab == MainTab::Runs {
            if let Some(run) = self.run_history.get(self.selected_run) {
                if !run.output_lines.is_empty() {
                    return Some(&run.output_lines);
                }
            }
        }
        if !self.selected_log.lines.is_empty() {
            return Some(&self.selected_log.lines);
        }
        None
    }

    fn notification_event_is_snoozed(&self, event: &NotificationEvent) -> bool {
        event
            .snoozed_until_sequence
            .is_some_and(|until| self.notification_sequence < until)
    }

    fn notification_event_is_visible(&self, event: &NotificationEvent) -> bool {
        !event.acknowledged && !self.notification_event_is_snoozed(event)
    }

    fn latest_visible_notification(&self) -> Option<&NotificationEvent> {
        self.notification_queue
            .iter()
            .rev()
            .find(|event| self.notification_event_is_visible(event))
    }

    fn visible_notification_count(&self) -> usize {
        self.notification_queue
            .iter()
            .filter(|event| self.notification_event_is_visible(event))
            .count()
    }

    fn pending_scheduled_timer_count(&self) -> usize {
        self.notification_queue
            .iter()
            .filter(|event| {
                !event.acknowledged
                    && event
                        .snoozed_until_sequence
                        .is_some_and(|until| self.notification_sequence < until)
            })
            .count()
    }

    fn next_scheduled_timer_in_ticks(&self) -> Option<u64> {
        self.notification_queue
            .iter()
            .filter(|event| !event.acknowledged)
            .filter_map(|event| event.snoozed_until_sequence)
            .filter(|until| self.notification_sequence < *until)
            .map(|until| until.saturating_sub(self.notification_sequence))
            .min()
    }

    fn render_help_content(
        &self,
        frame: &mut RenderFrame,
        width: usize,
        height: usize,
        y_offset: usize,
    ) {
        let mut lines: Vec<String> = vec![
            "=== Forge Loop TUI Help ===".to_owned(),
            "".to_owned(),
            "Navigation:".to_owned(),
            "  1/2/3/4/5 switch tabs (Overview/Logs/Runs/MultiLogs/Inbox)".to_owned(),
            "  ]/[       cycle tabs".to_owned(),
            "  b         backtrack last deep-link jump".to_owned(),
            "  Ctrl+O    activate primary link (run/loop/url fallback)".to_owned(),
            "  j/k       move loop selection".to_owned(),
            "  ,/.       move run selection / multi page".to_owned(),
            "".to_owned(),
            "Actions:".to_owned(),
            "  S         stop selected loop".to_owned(),
            "  K         kill selected loop".to_owned(),
            "  D         delete selected loop".to_owned(),
            "  r         resume selected loop".to_owned(),
            "  n         new loop wizard".to_owned(),
            "  R         regex log search (logs/runs; j/k jump matches)".to_owned(),
            "  confirm rail: tab/left/right choose action, enter selects (safe default=cancel)"
                .to_owned(),
            "  high-risk confirm (kill/force-delete): type reason (12+ chars)".to_owned(),
            "  E         export current view as text/html/svg".to_owned(),
            "".to_owned(),
            "Command Palette:".to_owned(),
            "  Ctrl+P    open command palette".to_owned(),
            "  type      fuzzy search action registry".to_owned(),
            "  tab/j/k   move result selection".to_owned(),
            "  enter     run selected action".to_owned(),
            "".to_owned(),
            "Logs:".to_owned(),
            "  v         cycle log source".to_owned(),
            "  x         cycle log layer".to_owned(),
            "  u/d       scroll logs".to_owned(),
            "  l         expand logs fullscreen".to_owned(),
            "".to_owned(),
            "Multi Logs:".to_owned(),
            "  m         cycle layout".to_owned(),
            "  C         toggle side-by-side compare mode".to_owned(),
            "  u/d       shared compare scroll (when compare enabled)".to_owned(),
            "  space     toggle pin".to_owned(),
            "  c         clear pinned".to_owned(),
            "  ,/.       page left/right".to_owned(),
            "  g/G       first/last page".to_owned(),
            "".to_owned(),
            "Inbox:".to_owned(),
            "  f         cycle inbox filter (all/unread/ack-required)".to_owned(),
            "  enter     mark selected thread read".to_owned(),
            "  a         ack latest pending message in thread".to_owned(),
            "  h         generate handoff snapshot package".to_owned(),
            "  r         quick reply shortcut (thread + reply-to id)".to_owned(),
            "  o         next claim conflict".to_owned(),
            "  O         show conflict resolution hint".to_owned(),
            "".to_owned(),
            "Global:".to_owned(),
            "  ?         toggle help".to_owned(),
            "  q         quit".to_owned(),
            "  t         cycle all themes".to_owned(),
            "  T         quick cycle accessibility presets".to_owned(),
            "  tab/shift+tab focus next/prev pane (wrap)".to_owned(),
            "  left/right   directional pane focus traversal".to_owned(),
            "  A         cycle accessibility quick modes (contrast/typography/motion)".to_owned(),
            "  z         zen mode (focus right pane)".to_owned(),
            "  Z         deep focus mode (distraction-minimized)".to_owned(),
            "  M         cycle density (comfortable/compact)".to_owned(),
            "  i         dismiss first-run contextual hints for current tab".to_owned(),
            "  I         recall first-run contextual hints for current tab".to_owned(),
            "  /         filter mode".to_owned(),
            "  Ctrl+E/W/A jump latest ERROR/WARN/ACK evidence".to_owned(),
            "  Ctrl+B    return to sticky evidence source".to_owned(),
            "  Ctrl+Y    copy context (run id/log line/thread content)".to_owned(),
            "".to_owned(),
        ];
        lines.extend(
            self.keymap
                .conflict_diagnostics_lines(width, height.saturating_sub(lines.len())),
        );
        for (i, line) in lines.iter().enumerate() {
            if i >= height {
                break;
            }
            let truncated = trim_to_width(line, width);
            frame.draw_text(0, y_offset + i, &truncated, TextRole::Primary);
        }
    }
}

impl App {
    fn should_render_onboarding_overlay(&self) -> bool {
        self.mode == UiMode::Main && !self.onboarding_dismissed_tabs.contains(&self.tab)
    }

    fn dismiss_onboarding_for_tab(&mut self, tab: MainTab) {
        if self.onboarding_dismissed_tabs.insert(tab) {
            self.set_status(
                StatusKind::Info,
                &format!(
                    "Onboarding hints dismissed for {} (press I to recall)",
                    tab.label()
                ),
            );
        } else {
            self.set_status(
                StatusKind::Info,
                &format!(
                    "Onboarding hints already dismissed for {} (press I to recall)",
                    tab.label()
                ),
            );
        }
    }

    fn recall_onboarding_for_tab(&mut self, tab: MainTab) {
        if self.onboarding_dismissed_tabs.remove(&tab) {
            self.set_status(
                StatusKind::Info,
                &format!("Onboarding hints recalled for {}", tab.label()),
            );
        } else {
            self.set_status(
                StatusKind::Info,
                &format!("Onboarding hints already visible for {}", tab.label()),
            );
        }
    }

    fn render_onboarding_overlay(
        &self,
        frame: &mut RenderFrame,
        width: usize,
        content_height: usize,
        y_offset: usize,
    ) {
        if width == 0 || content_height == 0 {
            return;
        }
        let lines = self.onboarding_lines(width);
        for (idx, (role, line)) in lines.into_iter().enumerate() {
            if idx >= content_height {
                break;
            }
            frame.draw_text(0, y_offset + idx, &line, role);
        }
    }

    fn onboarding_lines(&self, width: usize) -> Vec<(TextRole, String)> {
        let (line_a, line_b) = match self.tab {
            MainTab::Overview => (
                "overview: j/k select loop, 2 jump logs, n open wizard, ctrl+p command palette",
                "overview workflow: inspect state here, then pivot to runs/logs for root-cause",
            ),
            MainTab::Logs => (
                "logs: v cycle source, x cycle layer, u/d scroll, l expand pane",
                "logs workflow: pick run with ,/. then inspect raw/events/errors/tools/diff",
            ),
            MainTab::Runs => (
                "runs: ,/. select run, x layer, u/d scroll output, l expand pane",
                "runs workflow: compare recent exits, then drill into run output window",
            ),
            MainTab::MultiLogs => (
                "multi logs: m layout, C compare mode, u/d sync scroll, g/G first-last page",
                "multi workflow: pin loops with space, compare lanes side-by-side, clear with c",
            ),
            MainTab::Inbox => (
                "inbox: f filter, enter mark read, a ack, h handoff snapshot, r quick reply",
                "inbox workflow: resolve claim conflicts with o/O, then post closure note",
            ),
        };
        let mut lines = Vec::with_capacity(4);
        lines.push((
            TextRole::Accent,
            trim_to_width(
                &format!("first-run hints: {}", self.tab.label().to_ascii_lowercase()),
                width,
            ),
        ));
        lines.push((TextRole::Primary, trim_to_width(line_a, width)));
        lines.push((TextRole::Primary, trim_to_width(line_b, width)));
        lines.push((
            TextRole::Muted,
            trim_to_width(
                "i dismiss hints for this tab  |  I recall hints  |  ? full help",
                width,
            ),
        ));
        lines
    }
}

/// Blit a source frame onto a destination frame at the given offset.
fn blit_frame(dest: &mut RenderFrame, src: &RenderFrame, x_offset: usize, y_offset: usize) {
    let src_size = src.size();
    for sy in 0..src_size.height {
        for sx in 0..src_size.width {
            if let Some(cell) = src.cell(sx, sy) {
                dest.set_cell(x_offset + sx, y_offset + sy, cell);
            }
        }
    }
}

fn inbox_thread_key(message: &InboxMessageView) -> String {
    if let Some(thread_id) = &message.thread_id {
        let trimmed = thread_id.trim();
        if !trimmed.is_empty() {
            return trimmed.to_owned();
        }
    }
    format_mail_id(message.id)
}

fn extract_prefixed_token(text: &str, prefix: &str) -> Option<String> {
    let normalized_prefix = prefix.to_ascii_lowercase();
    text.split(|ch: char| !(ch.is_ascii_alphanumeric() || ch == '-' || ch == '_'))
        .find_map(|token| {
            if token.is_empty() {
                return None;
            }
            let normalized = token.to_ascii_lowercase();
            if normalized.starts_with(&normalized_prefix) {
                Some(normalized)
            } else {
                None
            }
        })
}

fn format_mail_id(id: i64) -> String {
    format!("m-{id}")
}

fn copy_to_system_clipboard(text: &str) -> bool {
    if text.is_empty() {
        return false;
    }
    try_copy_via_command("pbcopy", &[], text)
        || try_copy_via_command("wl-copy", &[], text)
        || try_copy_via_command("xclip", &["-selection", "clipboard"], text)
        || try_copy_via_command("xsel", &["--clipboard", "--input"], text)
}

fn try_copy_via_command(program: &str, args: &[&str], text: &str) -> bool {
    let mut child = match std::process::Command::new(program)
        .args(args)
        .stdin(std::process::Stdio::piped())
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::null())
        .spawn()
    {
        Ok(child) => child,
        Err(_) => return false,
    };

    if let Some(mut stdin) = child.stdin.take() {
        if stdin.write_all(text.as_bytes()).is_err() {
            let _ = child.kill();
            let _ = child.wait();
            return false;
        }
    } else {
        let _ = child.kill();
        let _ = child.wait();
        return false;
    }

    child.wait().is_ok_and(|status| status.success())
}

fn map_log_render_layer(layer: LogLayer) -> LogRenderLayer {
    match layer {
        LogLayer::Raw => LogRenderLayer::Raw,
        LogLayer::Events => LogRenderLayer::Events,
        LogLayer::Errors => LogRenderLayer::Errors,
        LogLayer::Tools => LogRenderLayer::Tools,
        LogLayer::Diff => LogRenderLayer::Diff,
    }
}

fn contains_ascii_token_ci(haystack: &str, needle: &str) -> bool {
    haystack
        .split(|ch: char| !ch.is_ascii_alphanumeric())
        .any(|token| !token.is_empty() && token.eq_ignore_ascii_case(needle))
}

fn evidence_line_matches(kind: EvidenceKind, line: &str) -> bool {
    let lower = line.to_ascii_lowercase();
    match kind {
        EvidenceKind::Error => {
            contains_ascii_token_ci(&lower, "error")
                || contains_ascii_token_ci(&lower, "failed")
                || contains_ascii_token_ci(&lower, "panic")
                || contains_ascii_token_ci(&lower, "fatal")
                || contains_ascii_token_ci(&lower, "exception")
                || contains_ascii_token_ci(&lower, "timeout")
                || contains_ascii_token_ci(&lower, "oom")
        }
        EvidenceKind::Warning => {
            contains_ascii_token_ci(&lower, "warn")
                || contains_ascii_token_ci(&lower, "warning")
                || contains_ascii_token_ci(&lower, "retry")
                || contains_ascii_token_ci(&lower, "degraded")
                || contains_ascii_token_ci(&lower, "slow")
        }
        EvidenceKind::Ack => {
            contains_ascii_token_ci(&lower, "ack")
                || contains_ascii_token_ci(&lower, "acknowledge")
                || contains_ascii_token_ci(&lower, "acknowledged")
        }
    }
}

fn replace_markdown_links(input: &str) -> String {
    let mut out = String::with_capacity(input.len());
    let mut rest = input;
    while let Some(open) = rest.find('[') {
        out.push_str(&rest[..open]);
        let after_open = &rest[open + 1..];
        let Some(close) = after_open.find("](") else {
            out.push_str(&rest[open..]);
            return out;
        };
        let after_close = &after_open[close + 2..];
        let Some(url_end) = after_close.find(')') else {
            out.push_str(&rest[open..]);
            return out;
        };
        let text = &after_open[..close];
        let url = &after_close[..url_end];
        out.push_str(text);
        if !url.trim().is_empty() {
            out.push_str(" <");
            out.push_str(url.trim());
            out.push('>');
        }
        rest = &after_close[url_end + 1..];
    }
    out.push_str(rest);
    out
}

fn markdown_to_plain_lines(markdown: &str, width: usize, max_lines: usize) -> Vec<String> {
    if max_lines == 0 || width == 0 {
        return Vec::new();
    }
    let mut out = Vec::new();
    let mut in_code = false;
    for raw in markdown.lines() {
        if out.len() >= max_lines {
            break;
        }
        let trimmed = raw.trim();
        if trimmed.starts_with("```") {
            in_code = !in_code;
            continue;
        }
        let line = if in_code {
            format!("`{trimmed}`")
        } else if let Some(rest) = trimmed.strip_prefix("### ") {
            format!("{} {}", "\u{25B8}", rest.trim())
        } else if let Some(rest) = trimmed.strip_prefix("## ") {
            format!("{} {}", "\u{25B8}", rest.trim().to_ascii_uppercase())
        } else if let Some(rest) = trimmed.strip_prefix("# ") {
            format!("{} {}", "\u{25B8}", rest.trim().to_ascii_uppercase())
        } else if let Some(rest) = trimmed.strip_prefix("- ") {
            format!("{} {}", "\u{2022}", rest.trim())
        } else if let Some(rest) = trimmed.strip_prefix("* ") {
            format!("{} {}", "\u{2022}", rest.trim())
        } else {
            trimmed.to_owned()
        };
        let line = replace_markdown_links(&line)
            .replace("**", "")
            .replace("__", "")
            .replace('`', "");
        if line.trim().is_empty() {
            continue;
        }
        out.push(trim_to_width(&line, width));
    }
    out
}

fn trim_to_width(value: &str, width: usize) -> String {
    if width == 0 {
        return String::new();
    }
    if value.len() <= width {
        return value.to_owned();
    }
    value.chars().take(width).collect()
}

fn render_inbox_markdown_detail_lines(markdown: &str, width: usize) -> Vec<String> {
    if width == 0 {
        return Vec::new();
    }

    let markdown = markdown.trim();
    if markdown.is_empty() {
        return Vec::new();
    }

    let mut rendered = Vec::new();
    let mut in_code_block = false;

    for raw_line in markdown.lines() {
        let line = raw_line.trim_end();
        let trimmed = line.trim();

        if trimmed.starts_with("```") {
            in_code_block = !in_code_block;
            continue;
        }

        if in_code_block {
            push_wrapped_prefixed_lines(&mut rendered, line, "` ", "` ", width);
            continue;
        }

        if trimmed.is_empty() {
            if rendered.last().is_some_and(|value| !value.is_empty()) {
                rendered.push(String::new());
            }
            continue;
        }

        if let Some((level, heading)) = parse_markdown_heading(trimmed) {
            let prefix = format!("{} ", "#".repeat(level.min(3)));
            push_wrapped_prefixed_lines(&mut rendered, heading, &prefix, "  ", width);
            continue;
        }

        if let Some((prefix, list_text)) = parse_markdown_list_item(trimmed) {
            let continuation = " ".repeat(prefix.chars().count());
            push_wrapped_prefixed_lines(&mut rendered, list_text, &prefix, &continuation, width);
            continue;
        }

        if let Some(quoted) = parse_markdown_quote(trimmed) {
            push_wrapped_prefixed_lines(&mut rendered, quoted, "> ", "> ", width);
            continue;
        }

        push_wrapped_prefixed_lines(&mut rendered, trimmed, "", "", width);
    }

    while rendered.last().is_some_and(String::is_empty) {
        rendered.pop();
    }

    if rendered.is_empty() {
        rendered.push(trim_to_width(markdown, width));
    }
    rendered
}

fn parse_markdown_heading(line: &str) -> Option<(usize, &str)> {
    let level = line.chars().take_while(|ch| *ch == '#').count();
    if !(1..=6).contains(&level) {
        return None;
    }
    let heading = line[level..].trim_start();
    if heading.is_empty() {
        return None;
    }
    Some((level, heading))
}

fn parse_markdown_quote(line: &str) -> Option<&str> {
    let mut remaining = line;
    let mut saw_quote = false;
    while let Some(stripped) = remaining.strip_prefix('>') {
        saw_quote = true;
        remaining = stripped.trim_start();
    }
    if !saw_quote {
        return None;
    }
    Some(remaining)
}

fn parse_markdown_list_item(line: &str) -> Option<(String, &str)> {
    for marker in ["- ", "* ", "+ "] {
        if let Some(rest) = line.strip_prefix(marker) {
            return Some(("* ".to_owned(), rest.trim_start()));
        }
    }

    let digit_prefix_len = line
        .chars()
        .take_while(|ch| ch.is_ascii_digit())
        .map(char::len_utf8)
        .sum::<usize>();
    if digit_prefix_len == 0 {
        return None;
    }

    let suffix = &line[digit_prefix_len..];
    let rest = suffix.strip_prefix(". ")?;
    Some((
        format!("{}. ", &line[..digit_prefix_len]),
        rest.trim_start(),
    ))
}

fn push_wrapped_prefixed_lines(
    out: &mut Vec<String>,
    text: &str,
    prefix: &str,
    continuation_prefix: &str,
    width: usize,
) {
    if width == 0 {
        return;
    }

    let body_width = width.saturating_sub(prefix.chars().count()).max(1);
    let wrapped = wrap_words_to_width(text, body_width);
    if wrapped.is_empty() {
        out.push(trim_to_width(prefix, width));
        return;
    }

    for (index, line) in wrapped.into_iter().enumerate() {
        let active_prefix = if index == 0 {
            prefix
        } else {
            continuation_prefix
        };
        let combined = if line.is_empty() {
            active_prefix.to_owned()
        } else {
            format!("{active_prefix}{line}")
        };
        out.push(trim_to_width(&combined, width));
    }
}

fn wrap_words_to_width(text: &str, width: usize) -> Vec<String> {
    if width == 0 {
        return Vec::new();
    }

    let mut wrapped = Vec::new();
    let mut current = String::new();

    for word in text.split_whitespace() {
        let chunks = split_text_chunks(word, width);
        for (chunk_idx, chunk) in chunks.into_iter().enumerate() {
            if chunk_idx > 0 && !current.is_empty() {
                wrapped.push(std::mem::take(&mut current));
            }

            if current.is_empty() {
                current = chunk;
                continue;
            }

            let candidate_width = current.chars().count() + 1 + chunk.chars().count();
            if candidate_width <= width {
                current.push(' ');
                current.push_str(&chunk);
            } else {
                wrapped.push(std::mem::take(&mut current));
                current = chunk;
            }
        }
    }

    if !current.is_empty() {
        wrapped.push(current);
    }

    if wrapped.is_empty() && !text.trim().is_empty() {
        return split_text_chunks(text.trim(), width);
    }

    wrapped
}

fn split_text_chunks(text: &str, width: usize) -> Vec<String> {
    if width == 0 || text.is_empty() {
        return Vec::new();
    }

    let mut chunks = Vec::new();
    let mut current = String::new();
    for ch in text.chars() {
        current.push(ch);
        if current.chars().count() >= width {
            chunks.push(std::mem::take(&mut current));
        }
    }
    if !current.is_empty() {
        chunks.push(current);
    }
    chunks
}

fn failure_explain_label_priority(label: &str) -> usize {
    match label {
        "root-cause" => 0,
        "root-frame" => 1,
        "command" => 2,
        "failure" => 3,
        "cause-context" => 4,
        _ => 5,
    }
}

fn failure_explain_label_display(label: &str) -> &'static str {
    match label {
        "root-cause" => "root cause",
        "root-frame" => "frame",
        "command" => "command",
        "failure" => "failure",
        "cause-context" => "context",
        _ => "cause",
    }
}

/// Multi-page pagination bounds, matching Go's `multiPageBounds`.
#[must_use]
pub fn multi_page_bounds(
    total: usize,
    page_size: usize,
    page: usize,
) -> (usize, usize, usize, usize) {
    let page_size = page_size.max(1);
    let total_pages = if total == 0 {
        1
    } else {
        total.div_ceil(page_size)
    };
    let page = page.min(total_pages.saturating_sub(1));

    let start = (page * page_size).min(total);
    let end = (start + page_size).min(total);
    (page, total_pages, start, end)
}

// ---------------------------------------------------------------------------
// PlaceholderView
// ---------------------------------------------------------------------------

/// A minimal view used for tabs that haven't been ported yet.
pub struct PlaceholderView {
    tab: MainTab,
    last_action: String,
}

impl PlaceholderView {
    #[must_use]
    pub fn new(tab: MainTab) -> Self {
        Self {
            tab,
            last_action: String::new(),
        }
    }
}

impl View for PlaceholderView {
    fn init(&mut self) -> Command {
        Command::None
    }

    fn update(&mut self, event: InputEvent) -> Command {
        if let InputEvent::Key(key_event) = event {
            let action = forge_ftui_adapter::input::translate_input(&InputEvent::Key(key_event));
            self.last_action = format!("{action:?}");
        }
        Command::None
    }

    fn view(&self, size: FrameSize, theme: ThemeSpec) -> RenderFrame {
        let mut frame = RenderFrame::new(size, theme);
        let label = format!("{} tab", self.tab.label());
        frame.draw_text(0, 0, &label, TextRole::Accent);
        frame.draw_text(0, 1, "placeholder content", TextRole::Muted);
        if !self.last_action.is_empty() {
            let status = format!("last: {}", self.last_action);
            frame.draw_text(0, 2, &status, TextRole::Primary);
        }
        frame
    }
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
#[allow(clippy::unwrap_used)]
mod tests {
    use super::*;
    use forge_ftui_adapter::input::{
        InputEvent, Key, KeyEvent, Modifiers, MouseButton, MouseEvent, MouseEventKind,
        MouseWheelDirection, ResizeEvent,
    };

    fn key(k: Key) -> InputEvent {
        InputEvent::Key(KeyEvent::plain(k))
    }

    fn ctrl_key(ch: char) -> InputEvent {
        InputEvent::Key(KeyEvent {
            key: Key::Char(ch),
            modifiers: Modifiers {
                shift: false,
                ctrl: true,
                alt: false,
            },
        })
    }

    fn mouse_left_down(column: usize, row: usize) -> InputEvent {
        InputEvent::Mouse(MouseEvent {
            kind: MouseEventKind::Down(MouseButton::Left),
            column,
            row,
        })
    }

    fn mouse_left_drag(column: usize, row: usize) -> InputEvent {
        InputEvent::Mouse(MouseEvent {
            kind: MouseEventKind::Drag(MouseButton::Left),
            column,
            row,
        })
    }

    fn mouse_wheel(direction: MouseWheelDirection, column: usize, row: usize) -> InputEvent {
        InputEvent::Mouse(MouseEvent {
            kind: MouseEventKind::Wheel(direction),
            column,
            row,
        })
    }

    struct PanicView;

    impl View for PanicView {
        fn init(&mut self) -> Command {
            Command::None
        }

        fn update(&mut self, _event: InputEvent) -> Command {
            Command::None
        }

        fn view(&self, _size: FrameSize, _theme: ThemeSpec) -> RenderFrame {
            panic!("pane exploded");
        }
    }

    fn sample_loops(n: usize) -> Vec<LoopView> {
        (0..n)
            .map(|i| LoopView {
                id: format!("loop-{i}"),
                name: format!("test-loop-{i}"),
                state: if i % 2 == 0 {
                    "running".to_owned()
                } else {
                    "stopped".to_owned()
                },
                repo_path: format!("/repo/{i}"),
                ..Default::default()
            })
            .collect()
    }

    fn app_with_loops(n: usize) -> App {
        let mut app = App::new("default", 12);
        app.set_loops(sample_loops(n));
        app
    }

    fn sample_inbox_messages() -> Vec<InboxMessageView> {
        vec![
            InboxMessageView {
                id: 1,
                thread_id: Some("thread-a".to_owned()),
                from: "agent-a".to_owned(),
                subject: "handoff ready".to_owned(),
                body: "please review".to_owned(),
                created_at: "2026-02-12T08:10:00Z".to_owned(),
                ack_required: true,
                read_at: None,
                acked_at: None,
            },
            InboxMessageView {
                id: 2,
                thread_id: Some("thread-a".to_owned()),
                from: "agent-b".to_owned(),
                subject: "re: handoff ready".to_owned(),
                body: "reviewing now".to_owned(),
                created_at: "2026-02-12T08:11:00Z".to_owned(),
                ack_required: false,
                read_at: None,
                acked_at: None,
            },
            InboxMessageView {
                id: 3,
                thread_id: Some("thread-b".to_owned()),
                from: "agent-c".to_owned(),
                subject: "incident escalated".to_owned(),
                body: "needs ack".to_owned(),
                created_at: "2026-02-12T08:12:00Z".to_owned(),
                ack_required: true,
                read_at: Some("2026-02-12T08:12:30Z".to_owned()),
                acked_at: None,
            },
        ]
    }

    fn sample_handoff_messages() -> Vec<InboxMessageView> {
        vec![InboxMessageView {
            id: 11,
            thread_id: Some("thread-handoff".to_owned()),
            from: "agent-h".to_owned(),
            subject: "handoff forge-jws loop-2".to_owned(),
            body: "status update and pending risk summary".to_owned(),
            created_at: "2026-02-12T08:13:00Z".to_owned(),
            ack_required: true,
            read_at: None,
            acked_at: None,
        }]
    }

    fn sample_markdown_inbox_messages() -> Vec<InboxMessageView> {
        vec![InboxMessageView {
            id: 21,
            thread_id: Some("thread-markdown".to_owned()),
            from: "agent-md".to_owned(),
            subject: "markdown status".to_owned(),
            body: "# Incident Playbook\n- restart daemon\n- verify health\n[notes](https://example.com/notes)".to_owned(),
            created_at: "2026-02-12T08:14:00Z".to_owned(),
            ack_required: false,
            read_at: None,
            acked_at: None,
        }]
    }

    fn sample_claim_events() -> Vec<ClaimEventView> {
        vec![
            ClaimEventView {
                task_id: "forge-jws".to_owned(),
                claimed_by: "agent-a".to_owned(),
                claimed_at: "2026-02-12T08:10:00Z".to_owned(),
            },
            ClaimEventView {
                task_id: "forge-jws".to_owned(),
                claimed_by: "agent-b".to_owned(),
                claimed_at: "2026-02-12T08:12:00Z".to_owned(),
            },
            ClaimEventView {
                task_id: "forge-73b".to_owned(),
                claimed_by: "agent-c".to_owned(),
                claimed_at: "2026-02-12T08:11:00Z".to_owned(),
            },
        ]
    }

    // -- MainTab labels --

    #[test]
    fn tab_label_snapshot() {
        let labels: Vec<&str> = MainTab::ORDER.iter().map(|t| t.label()).collect();
        assert_eq!(labels.join("|"), "Overview|Logs|Runs|Multi Logs|Inbox");
    }

    #[test]
    fn tab_short_label_snapshot() {
        let labels: Vec<&str> = MainTab::ORDER.iter().map(|t| t.short_label()).collect();
        assert_eq!(labels.join("|"), "ov|logs|runs|multi|inbox");
    }

    // -- session restore bridge --

    #[test]
    fn session_restore_round_trip_restores_tab_layout_selection_and_scroll() {
        let mut app = app_with_loops(4);
        app.set_run_history(vec![
            RunView {
                id: "run-1".to_owned(),
                status: "success".to_owned(),
                ..RunView::default()
            },
            RunView {
                id: "run-2".to_owned(),
                status: "error".to_owned(),
                ..RunView::default()
            },
        ]);
        app.set_tab(MainTab::MultiLogs);
        app.layout_idx = layout_index_for(2, 3);
        app.filter_state = "running".to_owned();
        app.filter_text = "agent timeout".to_owned();
        app.select_loop_by_id("loop-2");
        app.selected_run = 1;
        app.focus_right = true;
        app.pinned.insert("loop-0".to_owned());
        app.pinned.insert("loop-3".to_owned());
        app.log_scroll = 22;
        app.follow_mode = false;

        let context = app.session_restore_context();

        let mut restored = app_with_loops(4);
        restored.set_run_history(vec![
            RunView {
                id: "run-1".to_owned(),
                status: "success".to_owned(),
                ..RunView::default()
            },
            RunView {
                id: "run-2".to_owned(),
                status: "error".to_owned(),
                ..RunView::default()
            },
        ]);

        let notices = restored.restore_from_session_context(&context);
        assert_eq!(notices, Vec::<String>::new());
        assert_eq!(restored.tab(), MainTab::MultiLogs);
        assert_eq!(restored.active_layout(), PaneLayout { rows: 2, cols: 3 });
        assert_eq!(restored.filter_state(), "running");
        assert_eq!(restored.filter_text(), "agent timeout");
        assert_eq!(restored.selected_id(), "loop-2");
        assert_eq!(
            restored.selected_run_view().map(|run| run.id.as_str()),
            Some("run-2")
        );
        assert!(restored.focus_right());
        assert!(restored.pinned.contains("loop-0"));
        assert!(restored.pinned.contains("loop-3"));
        assert_eq!(restored.log_scroll(), 22);
        assert!(!restored.follow_mode());
    }

    #[test]
    fn restore_from_session_context_reports_unavailable_values_and_clamps_scroll() {
        let mut app = app_with_loops(2);
        app.set_run_history(vec![RunView {
            id: "run-1".to_owned(),
            status: "success".to_owned(),
            ..RunView::default()
        }]);
        app.pinned.insert("loop-1".to_owned());
        let original_layout = app.active_layout();

        let context = crate::session_restore::SessionContext {
            selected_loop_id: Some("missing-loop".to_owned()),
            selected_run_id: Some("missing-run".to_owned()),
            log_scroll: MAX_LOG_BACKFILL + 5000,
            tab_id: Some("missing-tab".to_owned()),
            layout_id: Some("9x9".to_owned()),
            filter_state: Some("unknown".to_owned()),
            filter_query: Some("x".to_owned()),
            panes: vec![crate::session_restore::PaneSelection {
                pane_id: "unknown-pane".to_owned(),
                focused: true,
            }],
            pinned_loop_ids: vec!["missing-loop".to_owned()],
        };

        let notices = app.restore_from_session_context(&context);
        assert!(notices
            .iter()
            .any(|msg| msg.contains("stored tab unavailable")));
        assert!(notices
            .iter()
            .any(|msg| msg.contains("stored layout unavailable")));
        assert!(notices
            .iter()
            .any(|msg| msg.contains("stored filter-state unavailable")));
        assert!(notices
            .iter()
            .any(|msg| msg.contains("stored loop unavailable")));
        assert!(notices
            .iter()
            .any(|msg| msg.contains("stored run unavailable")));
        assert!(notices
            .iter()
            .any(|msg| msg.contains("stored pane focus unavailable")));
        assert_eq!(app.tab(), MainTab::Overview);
        assert_eq!(app.active_layout(), original_layout);
        assert_eq!(app.selected_id(), "loop-0");
        assert_eq!(
            app.selected_run_view().map(|run| run.id.as_str()),
            Some("run-1")
        );
        assert!(app.pinned.is_empty());
        assert_eq!(app.log_scroll(), MAX_LOG_BACKFILL);
        assert!(!app.follow_mode());
    }

    // -- LogSource / LogLayer labels --

    #[test]
    fn log_source_labels() {
        let labels: Vec<&str> = LogSource::ORDER.iter().map(|s| s.label()).collect();
        assert_eq!(labels.join("|"), "live|latest-run|selected-run");
    }

    #[test]
    fn log_layer_labels() {
        let labels: Vec<&str> = LogLayer::ORDER.iter().map(|l| l.label()).collect();
        assert_eq!(labels.join("|"), "raw|events|errors|tools|diff");
    }

    #[test]
    fn current_log_route_maps_live_raw_to_live_parsed() {
        let app = App::new("default", 12);
        let route = app.current_log_route();
        assert_eq!(route.transport, LogTransportKind::LiveLoop);
        assert_eq!(route.content, LogContentKind::Parsed);
    }

    #[test]
    fn current_log_route_maps_selected_diff_to_selected_diff() {
        let mut app = App::new("default", 12);
        app.log_source = LogSource::RunSelection;
        app.log_layer = LogLayer::Diff;
        let route = app.current_log_route();
        assert_eq!(route.transport, LogTransportKind::SelectedRun);
        assert_eq!(route.content, LogContentKind::Diff);
    }

    // -- App construction --

    #[test]
    fn new_app_defaults() {
        let app = App::new("default", 0);
        assert_eq!(app.tab(), MainTab::Overview);
        assert_eq!(app.mode(), UiMode::Main);
        assert_eq!(app.log_source(), LogSource::Live);
        assert_eq!(app.log_layer(), LogLayer::Raw);
        assert_eq!(app.palette().name, "default");
        assert_eq!(app.log_lines, DEFAULT_LOG_LINES);
        assert!(app.filtered().is_empty());
        assert!(app.selected_id().is_empty());
    }

    // -- tab switching --

    #[test]
    fn number_keys_switch_tabs() {
        let mut app = App::new("default", 12);
        app.update(key(Key::Char('2')));
        assert_eq!(app.tab(), MainTab::Logs);
        app.update(key(Key::Char('3')));
        assert_eq!(app.tab(), MainTab::Runs);
        app.update(key(Key::Char('4')));
        assert_eq!(app.tab(), MainTab::MultiLogs);
        app.update(key(Key::Char('5')));
        assert_eq!(app.tab(), MainTab::Inbox);
        app.update(key(Key::Char('1')));
        assert_eq!(app.tab(), MainTab::Overview);
    }

    #[test]
    fn bracket_keys_cycle_tabs() {
        let mut app = App::new("default", 12);
        app.update(key(Key::Char(']')));
        assert_eq!(app.tab(), MainTab::Logs);
        app.update(key(Key::Char(']')));
        assert_eq!(app.tab(), MainTab::Runs);
        app.update(key(Key::Char('[')));
        assert_eq!(app.tab(), MainTab::Logs);
    }

    #[test]
    fn multi_logs_tab_sets_focus_right() {
        let mut app = App::new("default", 12);
        assert!(!app.focus_right());
        app.update(key(Key::Char('4')));
        assert!(app.focus_right());
        app.update(key(Key::Char('1')));
        assert!(!app.focus_right());
    }

    #[test]
    fn tab_and_shift_tab_wrap_focus_graph_in_main_mode() {
        let mut app = app_with_loops(3);
        app.update(key(Key::Char('2')));
        assert!(!app.focus_right());

        let cmd = app.update(key(Key::Tab));
        assert_eq!(cmd, Command::Fetch);
        assert!(app.focus_right());

        let cmd = app.update(key(Key::Tab));
        assert_eq!(cmd, Command::Fetch);
        assert!(!app.focus_right());

        let cmd = app.update(InputEvent::Key(KeyEvent {
            key: Key::Tab,
            modifiers: Modifiers {
                shift: true,
                ctrl: false,
                alt: false,
            },
        }));
        assert_eq!(cmd, Command::Fetch);
        assert!(app.focus_right());
    }

    #[test]
    fn left_right_wrap_focus_graph_without_dead_end() {
        let mut app = app_with_loops(3);
        app.update(key(Key::Char('5')));
        assert!(!app.focus_right());

        let cmd = app.update(key(Key::Left));
        assert_eq!(cmd, Command::Fetch);
        assert!(app.focus_right());

        let cmd = app.update(key(Key::Right));
        assert_eq!(cmd, Command::Fetch);
        assert!(!app.focus_right());
    }

    #[test]
    fn ctrl_y_copies_selected_run_id_into_clipboard_mirror() {
        let mut app = app_with_loops(2);
        app.set_tab(MainTab::Runs);
        app.set_run_history(vec![
            RunView {
                id: "run-11".to_owned(),
                status: "success".to_owned(),
                ..RunView::default()
            },
            RunView {
                id: "run-22".to_owned(),
                status: "error".to_owned(),
                ..RunView::default()
            },
        ]);
        app.move_run_selection(1);

        let cmd = app.update(ctrl_key('y'));
        assert_eq!(cmd, Command::Fetch);
        assert_eq!(app.clipboard_mirror(), Some("run-22"));
        assert!(
            app.status_text().contains("Copied run id")
                || app.status_text().contains("Clipboard unavailable")
        );
    }

    #[test]
    fn ctrl_y_copies_log_line_into_clipboard_mirror() {
        let mut app = app_with_loops(1);
        app.set_tab(MainTab::Logs);
        app.set_selected_log(LogTailView {
            lines: vec!["older line".to_owned(), "newest line".to_owned()],
            message: String::new(),
        });

        let cmd = app.update(ctrl_key('y'));
        assert_eq!(cmd, Command::Fetch);
        assert_eq!(app.clipboard_mirror(), Some("newest line"));
    }

    #[test]
    fn ctrl_y_copies_selected_inbox_thread_content() {
        let mut app = App::new("default", 12);
        app.set_inbox_messages(sample_inbox_messages());
        app.set_tab(MainTab::Inbox);

        let cmd = app.update(ctrl_key('y'));
        assert_eq!(cmd, Command::Fetch);
        assert_eq!(app.clipboard_mirror(), Some("needs ack"));
    }

    #[test]
    fn layout_perf_hud_snapshot_reflects_focus_and_layout_state() {
        let mut app = app_with_loops(6);
        app.update(key(Key::Char('4')));
        app.update(key(Key::Char('m')));
        app.update(key(Key::Char('Z')));

        let snapshot = app.layout_perf_hud_snapshot();
        assert_eq!(snapshot.tab, MainTab::MultiLogs);
        assert!(snapshot.split_focus_supported);
        assert_eq!(
            snapshot.focus_graph_nodes,
            vec!["left".to_owned(), "right".to_owned()]
        );
        assert_eq!(snapshot.focused_node, "right");
        assert_eq!(snapshot.focus_mode, FocusMode::DeepDebug);
        assert!(snapshot.content_height > 0);
        assert!(snapshot.effective_layout.capacity() >= 1);
    }

    #[test]
    fn inbox_filter_cycles_and_clamps_selection() {
        let mut app = App::new("default", 12);
        app.set_inbox_messages(sample_inbox_messages());
        app.update(key(Key::Char('5')));
        assert_eq!(app.tab(), MainTab::Inbox);
        assert_eq!(app.inbox_filter(), InboxFilter::All);

        app.update(key(Key::Char('f')));
        assert_eq!(app.inbox_filter(), InboxFilter::Unread);
        app.update(key(Key::Char('f')));
        assert_eq!(app.inbox_filter(), InboxFilter::AckRequired);
        app.update(key(Key::Char('f')));
        assert_eq!(app.inbox_filter(), InboxFilter::All);
    }

    #[test]
    fn inbox_enter_marks_selected_thread_read() {
        let mut app = App::new("default", 12);
        app.set_inbox_messages(sample_inbox_messages());
        app.update(key(Key::Char('5')));
        app.update(key(Key::Char('j')));
        app.update(key(Key::Enter));
        let unread = app
            .inbox_messages()
            .iter()
            .filter(|message| message.read_at.is_none())
            .count();
        assert_eq!(unread, 0);
    }

    #[test]
    fn inbox_acknowledges_latest_pending_message() {
        let mut app = App::new("default", 12);
        app.set_inbox_messages(sample_inbox_messages());
        app.update(key(Key::Char('5')));
        app.update(key(Key::Char('a')));
        let acked = app
            .inbox_messages()
            .iter()
            .find(|message| message.id == 3)
            .and_then(|message| message.acked_at.clone());
        assert_eq!(acked.as_deref(), Some("now"));
    }

    #[test]
    fn inbox_render_uses_cli_mail_ids_and_threads() {
        let mut app = App::new("default", 12);
        app.set_inbox_messages(sample_inbox_messages());
        app.set_claim_events(sample_claim_events());
        app.update(key(Key::Char('5')));
        app.update(key(Key::Char('i'))); // dismiss onboarding overlay
        let frame = app.render();
        let snapshot = frame.snapshot();
        assert!(snapshot.contains("Inbox filter:all"));
        assert!(snapshot.contains("m-3"));
        assert!(snapshot.contains("thread:thread-b"));
        assert!(snapshot.contains("Claim Timeline (latest)"));
        assert!(snapshot.contains("! 2026-02-12T08:12:00Z forge-jws <- agent-b"));
    }

    #[test]
    fn inbox_thread_list_is_subject_first_with_badges() {
        let mut app = App::new("default", 12);
        app.set_inbox_messages(sample_inbox_messages());
        app.update(InputEvent::Resize(ResizeEvent {
            width: 200,
            height: 36,
        }));
        app.update(key(Key::Char('5')));
        app.update(key(Key::Char('i'))); // dismiss onboarding overlay
        let snapshot = app.render().snapshot();
        assert!(snapshot.contains("incident escalated [u:0][a:1] (m-3)"));
        assert!(snapshot.contains("re: handoff ready [u:2][a:1] (m-2)"));
    }

    #[test]
    fn inbox_detail_pane_keeps_thread_details_visible() {
        let mut app = App::new("default", 12);
        app.set_inbox_messages(sample_inbox_messages());
        app.update(InputEvent::Resize(ResizeEvent {
            width: 140,
            height: 36,
        }));
        app.update(key(Key::Char('5')));
        app.update(key(Key::Char('i'))); // dismiss onboarding overlay
        let snapshot = app.render().snapshot();
        assert!(snapshot.contains("thread:thread-b"));
        assert!(snapshot.contains("m-3 agent-c incident escalated"));
    }

    #[test]
    fn inbox_detail_pane_renders_markdown_body() {
        let mut app = App::new("default", 12);
        app.set_inbox_messages(sample_markdown_inbox_messages());
        app.update(InputEvent::Resize(ResizeEvent {
            width: 140,
            height: 36,
        }));
        app.update(key(Key::Char('5')));
        app.update(key(Key::Char('i'))); // dismiss onboarding overlay
        let snapshot = app.render().snapshot();
        assert!(snapshot.contains("markdown detail (latest body)"));
        assert!(snapshot.contains("▸ INCIDENT PLAYBOOK"));
        assert!(snapshot.contains("• restart daemon"));
        assert!(snapshot.contains("notes <https://example.com/notes>"));
    }

    #[test]
    fn inbox_markdown_detail_lines_preserve_markdown_structure() {
        let lines = render_inbox_markdown_detail_lines(
            "# Title\n- first item\n1. second item\n> quoted line\n```\nlet x = 1;\n```",
            40,
        );
        assert!(lines.iter().any(|line| line == "# Title"));
        assert!(lines.iter().any(|line| line == "* first item"));
        assert!(lines.iter().any(|line| line == "1. second item"));
        assert!(lines.iter().any(|line| line == "> quoted line"));
        assert!(lines.iter().any(|line| line == "` let x = 1;"));
    }

    #[test]
    fn inbox_markdown_detail_lines_wrap_long_words_and_continuations() {
        let lines =
            render_inbox_markdown_detail_lines("- supercalifragilisticexpialidocious token", 16);
        assert!(!lines.is_empty());
        assert!(lines[0].starts_with("* "));
        assert!(lines.len() >= 2);
        assert!(lines.iter().all(|line| line.chars().count() <= 16));
    }

    #[test]
    fn mouse_click_tab_rail_switches_tabs() {
        let mut app = app_with_loops(3);
        assert_eq!(app.tab(), MainTab::Overview);
        app.update(mouse_left_down(15, 1));
        assert_eq!(app.tab(), MainTab::Logs);
    }

    #[test]
    fn mouse_click_inbox_list_selects_thread_and_focuses_list_pane() {
        let mut app = App::new("default", 12);
        app.set_inbox_messages(sample_inbox_messages());
        app.update(InputEvent::Resize(ResizeEvent {
            width: 140,
            height: 36,
        }));
        app.update(key(Key::Char('5')));
        app.update(key(Key::Char('i')));
        assert_eq!(app.inbox_selected_thread, 0);

        // Global row = content_start(2) + list_inner_y(2) + row(1).
        app.update(mouse_left_down(2, 5));
        assert_eq!(app.inbox_selected_thread, 1);
        assert!(!app.focus_right());
    }

    #[test]
    fn mouse_drag_inbox_list_updates_thread_selection() {
        let mut app = App::new("default", 12);
        app.set_inbox_messages(sample_inbox_messages());
        app.update(InputEvent::Resize(ResizeEvent {
            width: 140,
            height: 36,
        }));
        app.update(key(Key::Char('5')));
        app.update(key(Key::Char('i')));
        assert_eq!(app.inbox_selected_thread, 0);

        app.update(mouse_left_drag(2, 5));
        assert_eq!(app.inbox_selected_thread, 1);
    }

    #[test]
    fn mouse_click_multi_logs_cell_selects_corresponding_loop() {
        let mut app = app_with_loops(4);
        app.update(InputEvent::Resize(ResizeEvent {
            width: 120,
            height: 36,
        }));
        app.update(key(Key::Char('4')));
        assert_eq!(app.selected_id(), "loop-0");

        // content_start(2), header_rows(2), second cell x starts near 60.
        app.update(mouse_left_down(65, 5));
        assert_eq!(app.selected_id(), "loop-1");
        assert!(app.focus_right());
    }

    #[test]
    fn mouse_wheel_scrolls_inbox_thread_selection() {
        let mut app = App::new("default", 12);
        app.set_inbox_messages(sample_inbox_messages());
        app.update(InputEvent::Resize(ResizeEvent {
            width: 140,
            height: 36,
        }));
        app.update(key(Key::Char('5')));
        app.update(key(Key::Char('i')));

        app.update(mouse_wheel(MouseWheelDirection::Down, 10, 6));
        assert_eq!(app.inbox_selected_thread, 1);
        app.update(mouse_wheel(MouseWheelDirection::Up, 10, 6));
        assert_eq!(app.inbox_selected_thread, 0);
    }

    #[test]
    fn inbox_claim_conflict_shortcuts_show_status() {
        let mut app = App::new("default", 12);
        app.set_inbox_messages(sample_inbox_messages());
        app.set_claim_events(sample_claim_events());
        app.update(key(Key::Char('5')));
        app.update(key(Key::Char('o')));
        assert!(app.status_text().contains("Claim conflict forge-jws"));
        app.update(key(Key::Char('O')));
        assert!(app
            .status_text()
            .contains("takeover claim: forge-jws by <agent>"));
    }

    #[test]
    fn inbox_handoff_snapshot_generates_compact_package() {
        let mut app = app_with_loops(3);
        app.set_inbox_messages(sample_handoff_messages());
        app.set_claim_events(sample_claim_events());
        app.update(key(Key::Char('5')));
        app.update(key(Key::Char('h')));
        assert!(app.status_text().contains("Handoff snapshot ready"));
        let snapshot = app.render().snapshot();
        assert!(snapshot.contains("handoff snapshot"));
        assert!(snapshot.contains("task=forge-jws loop=loop-2"));
        assert!(snapshot.contains("status: loop=running"));
        assert!(snapshot.contains("context: thread=thread-handoff"));
        assert!(snapshot.contains("links: task:sv task show forge-jws"));
        assert!(snapshot.contains("pending-risks:"));
    }

    #[test]
    fn inbox_handoff_snapshot_uses_claim_fallback_when_task_not_in_thread() {
        let mut app = app_with_loops(2);
        app.set_inbox_messages(sample_inbox_messages());
        app.set_claim_events(sample_claim_events());
        app.update(key(Key::Char('5')));
        app.update(key(Key::Char('h')));
        let snapshot = app.render().snapshot();
        assert!(snapshot.contains("task=forge-jws"));
    }

    #[test]
    fn ctrl_e_jumps_to_latest_error_line_and_ctrl_b_restores_scroll() {
        let mut app = app_with_loops(2);
        app.set_tab(MainTab::Logs);
        app.set_selected_log(LogTailView {
            lines: vec![
                "info startup".to_owned(),
                "warn: latency spike".to_owned(),
                "error: failed to connect".to_owned(),
                "info retry scheduled".to_owned(),
            ],
            message: String::new(),
        });
        assert_eq!(app.log_scroll(), 0);

        let jump_cmd = app.update(ctrl_key('e'));
        assert_eq!(jump_cmd, Command::Fetch);
        assert_eq!(app.log_scroll(), 1);
        assert!(!app.follow_mode());
        assert!(app.status_text().contains("ERROR evidence"));

        let return_cmd = app.update(ctrl_key('b'));
        assert_eq!(return_cmd, Command::Fetch);
        assert_eq!(app.log_scroll(), 0);
        assert!(app.follow_mode());
        assert!(app
            .status_text()
            .contains("Returned to sticky evidence source"));
    }

    #[test]
    fn ctrl_w_jumps_to_warning_run_and_ctrl_b_restores_previous_tab() {
        let mut app = app_with_loops(3);
        app.set_tab(MainTab::Overview);
        app.move_selection(1);
        app.set_run_history(vec![
            RunView {
                id: "run-ok".to_owned(),
                status: "success".to_owned(),
                started_at: "2026-02-13T11:00:00Z".to_owned(),
                ..Default::default()
            },
            RunView {
                id: "run-warn".to_owned(),
                status: "warning".to_owned(),
                started_at: "2026-02-13T11:01:00Z".to_owned(),
                ..Default::default()
            },
        ]);

        let jump_cmd = app.update(ctrl_key('w'));
        assert_eq!(jump_cmd, Command::Fetch);
        assert_eq!(app.tab(), MainTab::Runs);
        assert_eq!(app.selected_run, 1);
        assert!(app.status_text().contains("WARN"));

        let return_cmd = app.update(ctrl_key('b'));
        assert_eq!(return_cmd, Command::Fetch);
        assert_eq!(app.tab(), MainTab::Overview);
        assert_eq!(app.selected_idx(), 1);
        assert_eq!(app.selected_id(), "loop-1");
    }

    #[test]
    fn ctrl_a_jumps_to_latest_ack_thread_and_ctrl_b_restores_tab() {
        let mut app = app_with_loops(2);
        app.set_tab(MainTab::Logs);
        app.set_inbox_messages(sample_inbox_messages());

        let jump_cmd = app.update(ctrl_key('a'));
        assert_eq!(jump_cmd, Command::Fetch);
        assert_eq!(app.tab(), MainTab::Inbox);
        let threads = app.inbox_threads();
        assert!(!threads.is_empty());
        assert!(threads[app.inbox_selected_thread].pending_ack_count > 0);
        assert!(app.status_text().contains("ACK evidence"));

        let return_cmd = app.update(ctrl_key('b'));
        assert_eq!(return_cmd, Command::Fetch);
        assert_eq!(app.tab(), MainTab::Logs);
    }

    #[test]
    fn ctrl_b_without_prior_jump_reports_missing_return_point() {
        let mut app = App::new("default", 12);
        let cmd = app.update(ctrl_key('b'));
        assert_eq!(cmd, Command::None);
        assert!(app
            .status_text()
            .contains("No sticky evidence return point"));
    }

    // -- quit --

    #[test]
    fn q_quits() {
        let mut app = App::new("default", 12);
        let cmd = app.update(key(Key::Char('q')));
        assert_eq!(cmd, Command::Quit);
        assert!(app.quitting());
    }

    #[test]
    fn ctrl_c_quits() {
        let mut app = App::new("default", 12);
        let cmd = app.update(ctrl_key('c'));
        assert_eq!(cmd, Command::Quit);
        assert!(app.quitting());
    }

    // -- help mode --

    #[test]
    fn question_mark_enters_help() {
        let mut app = App::new("default", 12);
        app.update(key(Key::Char('?')));
        assert_eq!(app.mode(), UiMode::Help);
    }

    #[test]
    fn first_run_onboarding_overlay_renders_by_default() {
        let mut app = app_with_loops(3);
        app.update(InputEvent::Resize(ResizeEvent {
            width: 120,
            height: 30,
        }));
        let snapshot = app.render().snapshot();
        assert!(snapshot.contains("first-run hints: overview"));
        assert!(snapshot.contains("overview: j/k select loop"));
        assert!(snapshot.contains("i dismiss hints for this tab"));
    }

    #[test]
    fn dismiss_onboarding_hides_overlay_per_tab() {
        let mut app = app_with_loops(3);
        app.update(InputEvent::Resize(ResizeEvent {
            width: 120,
            height: 30,
        }));
        app.update(key(Key::Char('i')));
        assert!(app.status_text().contains("dismissed for Overview"));
        let overview = app.render().snapshot();
        assert!(!overview.contains("first-run hints: overview"));

        app.update(key(Key::Char('2')));
        let logs = app.render().snapshot();
        assert!(logs.contains("first-run hints: logs"));
    }

    #[test]
    fn recall_onboarding_restores_overlay_for_tab() {
        let mut app = app_with_loops(3);
        app.update(InputEvent::Resize(ResizeEvent {
            width: 120,
            height: 30,
        }));
        app.update(key(Key::Char('i')));
        app.update(key(Key::Char('I')));
        assert!(app.status_text().contains("recalled for Overview"));
        let snapshot = app.render().snapshot();
        assert!(snapshot.contains("first-run hints: overview"));
    }

    #[test]
    fn help_exits_on_q() {
        let mut app = App::new("default", 12);
        app.update(key(Key::Char('?')));
        assert_eq!(app.mode(), UiMode::Help);
        app.update(key(Key::Char('q')));
        assert_eq!(app.mode(), UiMode::Main);
    }

    #[test]
    fn help_returns_to_previous_mode() {
        let mut app = App::new("default", 12);
        app.update(key(Key::Char('/')));
        assert_eq!(app.mode(), UiMode::Filter);
        app.update(key(Key::Char('?')));
        assert_eq!(app.mode(), UiMode::Help);
        app.update(key(Key::Escape));
        assert_eq!(app.mode(), UiMode::Filter);
    }

    #[test]
    fn help_includes_keymap_diagnostics_panel() {
        let mut app = App::new("default", 12);
        app.height = 80;
        app.update(key(Key::Char('?')));
        let frame = app.render();
        let all_text = (0..app.height())
            .map(|row| frame.row_text(row))
            .collect::<Vec<String>>()
            .join("\n");
        assert!(all_text.contains("Keymap diagnostics"));
        assert!(all_text.contains("no conflicts detected"));
    }

    #[test]
    fn help_lists_evidence_hotkeys() {
        let mut app = App::new("default", 12);
        app.height = 80;
        app.update(key(Key::Char('?')));
        let frame = app.render();
        let all_text = (0..app.height())
            .map(|row| frame.row_text(row))
            .collect::<Vec<String>>()
            .join("\n");
        assert!(all_text.contains("Ctrl+E/W/A jump latest ERROR/WARN/ACK evidence"));
        assert!(all_text.contains("Ctrl+B    return to sticky evidence source"));
    }

    #[test]
    fn ctrl_p_enters_palette_mode() {
        let mut app = App::new("default", 12);
        let cmd = app.update(ctrl_key('p'));
        assert_eq!(cmd, Command::None);
        assert_eq!(app.mode(), UiMode::Palette);
        assert!(app.palette_match_count() > 0);
        assert!(app.palette_query().is_empty());
    }

    #[test]
    fn palette_typing_and_backspace_updates_query() {
        let mut app = App::new("default", 12);
        app.update(ctrl_key('p'));
        app.update(key(Key::Char('l')));
        app.update(key(Key::Char('o')));
        assert_eq!(app.palette_query(), "lo");
        app.update(key(Key::Backspace));
        assert_eq!(app.palette_query(), "l");
    }

    #[test]
    fn palette_enter_executes_navigation_action() {
        let mut app = App::new("default", 12);
        app.update(ctrl_key('p'));
        for ch in ['l', 'o', 'g', 's'] {
            app.update(key(Key::Char(ch)));
        }
        let cmd = app.update(key(Key::Enter));
        assert_eq!(cmd, Command::Fetch);
        assert_eq!(app.mode(), UiMode::Main);
        assert_eq!(app.tab(), MainTab::Logs);
    }

    #[test]
    fn palette_enter_executes_export_action() {
        let mut app = App::new("default", 12);
        app.update(ctrl_key('p'));
        for ch in ['e', 'x', 'p'] {
            app.update(key(Key::Char(ch)));
        }
        let cmd = app.update(key(Key::Enter));
        assert_eq!(cmd, Command::ExportCurrentView);
        assert_eq!(app.mode(), UiMode::Main);
    }

    #[test]
    fn palette_enter_executes_selected_loop_action() {
        let mut app = app_with_loops(2);
        app.update(ctrl_key('p'));
        for ch in ['s', 't', 'o', 'p'] {
            app.update(key(Key::Char(ch)));
        }
        let cmd = app.update(key(Key::Enter));
        assert_eq!(cmd, Command::None);
        assert_eq!(app.mode(), UiMode::Confirm);
        assert!(app.confirm().is_some());
    }

    #[test]
    fn palette_help_round_trips_back_to_palette() {
        let mut app = App::new("default", 12);
        app.update(ctrl_key('p'));
        assert_eq!(app.mode(), UiMode::Palette);
        app.update(key(Key::Char('?')));
        assert_eq!(app.mode(), UiMode::Help);
        app.update(key(Key::Escape));
        assert_eq!(app.mode(), UiMode::Palette);
    }

    #[test]
    fn export_key_dispatches_export_command() {
        let mut app = App::new("default", 12);
        let cmd = app.update(key(Key::Char('E')));
        assert_eq!(cmd, Command::ExportCurrentView);
    }

    // -- selection --

    #[test]
    fn selection_moves_with_jk() {
        let mut app = app_with_loops(5);
        assert_eq!(app.selected_idx(), 0);
        app.update(key(Key::Char('j')));
        assert_eq!(app.selected_idx(), 1);
        assert_eq!(app.selected_id(), "loop-1");
        app.update(key(Key::Char('k')));
        assert_eq!(app.selected_idx(), 0);
    }

    #[test]
    fn selection_clamps_at_bounds() {
        let mut app = app_with_loops(3);
        app.update(key(Key::Char('k')));
        assert_eq!(app.selected_idx(), 0);
        app.update(key(Key::Char('j')));
        app.update(key(Key::Char('j')));
        app.update(key(Key::Char('j')));
        app.update(key(Key::Char('j')));
        assert_eq!(app.selected_idx(), 2);
    }

    #[test]
    fn selected_view_returns_none_for_empty() {
        let app = App::new("default", 12);
        assert!(app.selected_view().is_none());
    }

    // -- pinning --

    #[test]
    fn toggle_pin_and_clear() {
        let mut app = app_with_loops(3);
        assert!(!app.is_pinned("loop-0"));
        app.toggle_pinned("loop-0");
        assert!(app.is_pinned("loop-0"));
        assert_eq!(app.pinned_count(), 1);
        app.toggle_pinned("loop-0");
        assert!(!app.is_pinned("loop-0"));
        app.toggle_pinned("loop-1");
        app.clear_pinned();
        assert_eq!(app.pinned_count(), 0);
    }

    #[test]
    fn pin_via_space_key() {
        let mut app = app_with_loops(3);
        app.update(key(Key::Char(' ')));
        assert!(app.is_pinned("loop-0"));
    }

    // -- filter mode --

    #[test]
    fn slash_enters_filter_mode() {
        let mut app = App::new("default", 12);
        app.update(key(Key::Char('/')));
        assert_eq!(app.mode(), UiMode::Filter);
        assert_eq!(app.filter_focus(), FilterFocus::Text);
    }

    #[test]
    fn filter_text_narrows_results() {
        let mut app = app_with_loops(5);
        assert_eq!(app.filtered().len(), 5);
        app.update(key(Key::Char('/')));

        // Type "loop-1"
        for ch in "loop-1".chars() {
            app.update(key(Key::Char(ch)));
        }
        assert_eq!(app.filtered().len(), 1);
        assert_eq!(app.filtered()[0].id, "loop-1");
    }

    #[test]
    fn filter_backspace_removes_char() {
        let mut app = app_with_loops(5);
        app.update(key(Key::Char('/')));
        app.update(key(Key::Char('x')));
        assert_eq!(app.filter_text(), "x");
        app.update(key(Key::Backspace));
        assert_eq!(app.filter_text(), "");
    }

    #[test]
    fn filter_tab_switches_focus() {
        let mut app = App::new("default", 12);
        app.update(key(Key::Char('/')));
        assert_eq!(app.filter_focus(), FilterFocus::Text);
        app.update(key(Key::Tab));
        assert_eq!(app.filter_focus(), FilterFocus::Status);
        app.update(key(Key::Tab));
        assert_eq!(app.filter_focus(), FilterFocus::Text);
    }

    #[test]
    fn filter_status_cycling() {
        let mut app = app_with_loops(5);
        app.update(key(Key::Char('/')));
        app.update(key(Key::Tab));
        // Cycle status filter.
        app.update(key(Key::Char('j')));
        assert_eq!(app.filter_state(), "running");
        // Should filter to only running loops.
        assert!(app.filtered().iter().all(|l| l.state == "running"));
    }

    #[test]
    fn filter_escape_exits() {
        let mut app = App::new("default", 12);
        app.update(key(Key::Char('/')));
        assert_eq!(app.mode(), UiMode::Filter);
        app.update(key(Key::Escape));
        assert_eq!(app.mode(), UiMode::Main);
    }

    // -- theme cycling --

    #[test]
    fn t_cycles_theme() {
        let mut app = App::new("default", 12);
        assert_eq!(app.palette().name, "default");
        app.update(key(Key::Char('t')));
        assert_eq!(app.palette().name, "high-contrast");
        app.update(key(Key::Char('t')));
        assert_eq!(app.palette().name, "low-light");
    }

    #[test]
    fn shift_t_cycles_accessibility_theme_presets() {
        let mut app = App::new("default", 12);
        app.update(key(Key::Char('T')));
        assert_eq!(app.palette().name, "high-contrast");
        app.update(key(Key::Char('T')));
        assert_eq!(app.palette().name, "low-light");
        app.update(key(Key::Char('T')));
        assert_eq!(app.palette().name, "colorblind-safe");
    }

    #[test]
    fn shift_a_cycles_accessibility_quick_modes() {
        let mut app = App::new("default", 12);
        assert_eq!(
            app.accessibility_quick_mode(),
            AccessibilityQuickMode::Contrast
        );
        assert!(!app.reduced_motion());

        app.update(key(Key::Char('A')));
        assert_eq!(
            app.accessibility_quick_mode(),
            AccessibilityQuickMode::Typography
        );
        assert_eq!(app.palette().name, "colorblind-safe");
        assert_eq!(app.density_mode(), DensityMode::Compact);
        assert!(!app.reduced_motion());

        app.update(key(Key::Char('A')));
        assert_eq!(
            app.accessibility_quick_mode(),
            AccessibilityQuickMode::MotionReduced
        );
        assert_eq!(app.palette().name, "low-light");
        assert_eq!(app.density_mode(), DensityMode::Comfortable);
        assert!(app.reduced_motion());

        app.update(key(Key::Char('A')));
        assert_eq!(
            app.accessibility_quick_mode(),
            AccessibilityQuickMode::Contrast
        );
        assert_eq!(app.palette().name, "high-contrast");
        assert!(!app.reduced_motion());
    }

    #[test]
    fn ansi16_capability_forces_high_contrast_palette() {
        let app = App::new_with_capability("ocean", TerminalColorCapability::Ansi16, 12);
        assert_eq!(app.palette().name, "high-contrast");
    }

    // -- zen mode --

    #[test]
    fn z_toggles_zen() {
        let mut app = App::new("default", 12);
        assert!(!app.focus_right());
        app.update(key(Key::Char('z')));
        assert!(app.focus_right());
        app.update(key(Key::Char('z')));
        assert!(!app.focus_right());
    }

    #[test]
    fn m_cycles_density_modes() {
        let mut app = App::new("default", 12);
        assert_eq!(app.density_mode(), DensityMode::Comfortable);
        app.update(key(Key::Char('M')));
        assert_eq!(app.density_mode(), DensityMode::Compact);
        assert!(app.status_text().contains("Density: compact"));
        app.update(key(Key::Char('M')));
        assert_eq!(app.density_mode(), DensityMode::Comfortable);
    }

    #[test]
    fn shift_z_toggles_deep_focus_and_collapses_tab_bar() {
        let mut app = app_with_loops(3);
        app.update(InputEvent::Resize(ResizeEvent {
            width: 120,
            height: 36,
        }));
        let baseline = app.render();
        assert!(baseline.row_text(1).contains("1:Overview"));

        app.update(key(Key::Char('Z')));
        assert_eq!(app.focus_mode(), FocusMode::DeepDebug);
        assert!(app.focus_right());
        let focused = app.render();
        assert!(!focused.row_text(1).contains("1:Overview"));
        assert!(focused.row_text(0).contains("focus:deep"));

        app.update(key(Key::Char('Z')));
        assert_eq!(app.focus_mode(), FocusMode::Standard);
        assert!(!app.focus_right());
    }

    #[test]
    fn compact_density_increases_multi_page_capacity() {
        let mut app = app_with_loops(24);
        app.set_tab(MainTab::MultiLogs);
        app.update(InputEvent::Resize(ResizeEvent {
            width: 120,
            height: 30,
        }));
        let comfortable = app.multi_page_size();
        app.update(key(Key::Char('M')));
        let compact = app.multi_page_size();
        assert!(compact >= comfortable);
    }

    #[test]
    fn footer_hints_promote_recent_follow_action() {
        let mut app = app_with_loops(3);
        app.update(InputEvent::Resize(ResizeEvent {
            width: 200,
            height: 30,
        }));
        app.update(key(Key::Char('2')));
        let baseline = app.footer_hint_line();
        assert!(
            !baseline.contains("F follow"),
            "follow hint should be omitted before recency boost: {baseline}"
        );

        app.update(key(Key::Char('F')));
        let ranked = app.footer_hint_line();
        let ranked_follow = match ranked.find("F follow") {
            Some(idx) => idx,
            None => panic!("missing follow hint after usage: {ranked}"),
        };
        let ranked_palette = match ranked.find("ctrl+p palette") {
            Some(idx) => idx,
            None => panic!("missing palette hint after usage: {ranked}"),
        };
        assert!(ranked_follow < ranked_palette, "ranked hints: {ranked}");
    }

    #[test]
    fn footer_hints_follow_latest_recency_signal() {
        let mut app = app_with_loops(3);
        app.update(InputEvent::Resize(ResizeEvent {
            width: 200,
            height: 30,
        }));
        app.update(key(Key::Char('2')));
        app.update(key(Key::Char('F')));
        app.update(key(Key::Char('/')));
        app.update(key(Key::Escape));
        let ranked = app.footer_hint_line();
        let filter_idx = match ranked.find("/ filter") {
            Some(idx) => idx,
            None => panic!("missing filter hint: {ranked}"),
        };
        let follow_idx = match ranked.find("F follow") {
            Some(idx) => idx,
            None => panic!("missing follow hint: {ranked}"),
        };
        assert!(filter_idx < follow_idx, "ranked hints: {ranked}");
    }

    #[test]
    fn footer_hints_never_exceed_eight_items() {
        let mut app = app_with_loops(4);
        app.update(InputEvent::Resize(ResizeEvent {
            width: 200,
            height: 30,
        }));

        let normal = app.footer_hint_line();
        assert!(normal.split("  ").count() <= 8, "normal hints: {normal}");

        app.update(key(Key::Char('M')));
        let compact = app.footer_hint_line();
        assert!(compact.split("  ").count() <= 8, "compact hints: {compact}");

        app.update(key(Key::Char('Z')));
        let deep = app.footer_hint_line();
        assert!(deep.split("  ").count() <= 8, "deep hints: {deep}");
    }

    // -- log source/layer cycling --

    #[test]
    fn v_cycles_log_source_in_logs_tab() {
        let mut app = App::new("default", 12);
        app.set_tab(MainTab::Logs);
        assert_eq!(app.log_source(), LogSource::Live);
        app.update(key(Key::Char('v')));
        assert_eq!(app.log_source(), LogSource::LatestRun);
        app.update(key(Key::Char('v')));
        assert_eq!(app.log_source(), LogSource::RunSelection);
    }

    #[test]
    fn v_noop_in_overview_tab() {
        let mut app = App::new("default", 12);
        app.update(key(Key::Char('v')));
        assert_eq!(app.log_source(), LogSource::Live);
    }

    #[test]
    fn x_cycles_log_layer() {
        let mut app = App::new("default", 12);
        app.set_tab(MainTab::Logs);
        assert_eq!(app.log_layer(), LogLayer::Raw);
        app.update(key(Key::Char('x')));
        assert_eq!(app.log_layer(), LogLayer::Events);
    }

    // -- log scrolling --

    #[test]
    fn u_d_scroll_in_logs_tab() {
        let mut app = App::new("default", 12);
        app.set_tab(MainTab::Logs);
        assert_eq!(app.log_scroll(), 0);
        app.update(key(Key::Char('u')));
        assert!(app.log_scroll() > 0);
    }

    #[test]
    fn logs_tab_renders_real_logs_pane_not_placeholder() {
        let mut app = app_with_loops(2);
        app.set_tab(MainTab::Logs);
        app.set_selected_log(LogTailView {
            lines: vec!["error: failed to connect".to_owned()],
            message: "last log line".to_owned(),
        });
        app.update(InputEvent::Resize(ResizeEvent {
            width: 120,
            height: 30,
        }));
        app.update(key(Key::Char('i')));

        let snapshot = app.render().snapshot();
        assert!(snapshot.contains("source:live  layer:raw"));
        assert!(snapshot.contains("error: failed to connect"));
        assert!(!snapshot.contains("Logs tab  |"));
        assert!(!snapshot.contains("placeholder content"));
    }

    #[test]
    fn regex_search_mode_opens_on_logs_tab_and_persists_query() {
        let mut app = app_with_loops(2);
        app.set_tab(MainTab::Logs);
        app.set_selected_log(LogTailView {
            lines: vec![
                "alpha event".to_owned(),
                "error: timeout".to_owned(),
                "error: retry".to_owned(),
            ],
            message: String::new(),
        });

        app.update(key(Key::Char('R')));
        assert_eq!(app.mode(), UiMode::RegexSearch);
        for ch in "error".chars() {
            app.update(key(Key::Char(ch)));
        }
        assert_eq!(app.log_regex_query(), "error");
        assert_eq!(app.log_regex_match_count(), 2);

        app.update(key(Key::Enter));
        assert_eq!(app.mode(), UiMode::Main);
        app.update(key(Key::Char('1')));
        app.update(key(Key::Char('2')));
        assert_eq!(app.log_regex_query(), "error");
    }

    #[test]
    fn regex_search_jumps_matches_and_highlights_selected_line() {
        let mut app = app_with_loops(2);
        app.set_tab(MainTab::Logs);
        app.set_selected_log(LogTailView {
            lines: vec![
                "first".to_owned(),
                "error: first".to_owned(),
                "between".to_owned(),
                "error: second".to_owned(),
            ],
            message: String::new(),
        });

        app.update(key(Key::Char('R')));
        for ch in "error".chars() {
            app.update(key(Key::Char(ch)));
        }
        let cmd = app.update(key(Key::Char('j')));
        assert_eq!(cmd, Command::Fetch);
        assert!(app.status_text().contains("Regex match 2/2"));
        app.update(key(Key::Enter));

        let snapshot = app.render().snapshot();
        assert!(snapshot.contains("error: second"));
        assert_eq!(app.log_regex_match_count(), 2);
        assert!(app.status_text().contains("Regex match 2/2"));
    }

    #[test]
    fn regex_search_invalid_pattern_surfaces_error() {
        let mut app = app_with_loops(2);
        app.set_tab(MainTab::Logs);
        app.set_selected_log(LogTailView {
            lines: vec!["error: first".to_owned()],
            message: String::new(),
        });

        app.update(key(Key::Char('R')));
        app.update(key(Key::Char('[')));
        assert!(app.log_regex_error().contains("invalid regex"));
        let frame = app.render();
        let snapshot = frame.snapshot();
        assert!(snapshot.contains("invalid regex"));
    }

    #[test]
    fn deep_link_jump_pushes_nav_history_and_b_backtracks() {
        let mut app = app_with_loops(3);
        app.set_tab(MainTab::Overview);
        app.move_selection(1);
        assert_eq!(app.selected_id(), "loop-1");
        app.set_run_history(vec![
            RunView {
                id: "run-1".to_owned(),
                status: "error".to_owned(),
                exit_code: Some(1),
                duration: "4s".to_owned(),
                ..RunView::default()
            },
            RunView {
                id: "run-0".to_owned(),
                status: "success".to_owned(),
                exit_code: Some(0),
                duration: "2s".to_owned(),
                ..RunView::default()
            },
        ]);

        app.jump_to_search_target(crate::search_overlay::SearchJumpTarget::Run {
            run_id: "run-1".to_owned(),
        });
        assert_eq!(app.tab(), MainTab::Runs);
        assert_eq!(app.nav_history_len(), 1);

        let cmd = app.update(key(Key::Char('b')));
        assert_eq!(cmd, Command::Fetch);
        assert_eq!(app.tab(), MainTab::Overview);
        assert_eq!(app.selected_id(), "loop-1");
        assert_eq!(app.nav_history_len(), 0);
    }

    #[test]
    fn backtrack_key_without_history_is_noop_with_status() {
        let mut app = app_with_loops(1);
        let cmd = app.update(key(Key::Char('b')));
        assert_eq!(cmd, Command::None);
        assert!(app.status_text().contains("no navigation history"));
    }

    #[test]
    fn ctrl_o_jumps_to_logs_when_loop_link_is_present() {
        let mut app = app_with_loops(3);
        app.set_tab(MainTab::Logs);
        app.set_selected_log(LogTailView {
            lines: vec!["investigate loop-2 for newest errors".to_owned()],
            message: String::new(),
        });

        let cmd = app.update(ctrl_key('o'));
        assert_eq!(cmd, Command::Fetch);
        assert_eq!(app.tab(), MainTab::Logs);
        assert_eq!(app.selected_id(), "loop-2");
        assert!(app.status_text().contains("Jumped to logs for loop-2"));
    }

    #[test]
    fn ctrl_o_shows_url_fallback_status() {
        let mut app = app_with_loops(1);
        app.set_tab(MainTab::Logs);
        app.set_selected_log(LogTailView {
            lines: vec!["docs: https://example.com/runbook".to_owned()],
            message: String::new(),
        });

        let cmd = app.update(ctrl_key('o'));
        assert_eq!(cmd, Command::Fetch);
        assert!(app
            .status_text()
            .contains("Open URL: https://example.com/runbook"));
    }

    #[test]
    fn ctrl_o_without_links_reports_no_links() {
        let mut app = app_with_loops(1);
        app.set_tab(MainTab::Logs);
        app.set_selected_log(LogTailView {
            lines: vec!["all systems nominal".to_owned()],
            message: String::new(),
        });

        let cmd = app.update(ctrl_key('o'));
        assert_eq!(cmd, Command::None);
        assert!(app.status_text().contains("No links in current context"));
    }

    // -- follow mode --

    #[test]
    fn follow_mode_defaults_on() {
        let app = App::new("default", 12);
        assert!(app.follow_mode());
    }

    #[test]
    fn scroll_up_disengages_follow() {
        let mut app = App::new("default", 12);
        app.set_tab(MainTab::Logs);
        assert!(app.follow_mode());
        app.update(key(Key::Char('u')));
        assert!(!app.follow_mode());
    }

    #[test]
    fn scroll_to_bottom_reengages_follow() {
        let mut app = App::new("default", 12);
        app.set_tab(MainTab::Logs);
        app.update(key(Key::Char('u')));
        assert!(!app.follow_mode());
        app.scroll_logs_to_bottom();
        assert!(app.follow_mode());
        assert_eq!(app.log_scroll(), 0);
    }

    #[test]
    fn toggle_follow_flips_state() {
        let mut app = App::new("default", 12);
        app.set_tab(MainTab::Logs);
        assert!(app.follow_mode());
        app.toggle_follow_mode();
        assert!(!app.follow_mode());
        app.toggle_follow_mode();
        assert!(app.follow_mode());
        assert_eq!(app.log_scroll(), 0);
    }

    #[test]
    fn f_key_toggles_follow_in_logs_tab() {
        let mut app = App::new("default", 12);
        app.set_tab(MainTab::Logs);
        assert!(app.follow_mode());
        app.update(key(Key::Char('F')));
        assert!(!app.follow_mode());
        app.update(key(Key::Char('F')));
        assert!(app.follow_mode());
    }

    #[test]
    fn follow_mode_resets_on_tab_change() {
        let mut app = App::new("default", 12);
        app.set_tab(MainTab::Logs);
        app.update(key(Key::Char('u')));
        assert!(!app.follow_mode());
        app.set_tab(MainTab::Runs);
        assert!(app.follow_mode());
    }

    #[test]
    fn follow_mode_resets_on_source_change() {
        let mut app = App::new("default", 12);
        app.set_tab(MainTab::Logs);
        app.update(key(Key::Char('u')));
        assert!(!app.follow_mode());
        app.cycle_log_source(1);
        assert!(app.follow_mode());
    }

    #[test]
    fn follow_mode_pins_scroll_on_new_data() {
        let mut app = App::new("default", 12);
        app.set_tab(MainTab::Logs);
        assert!(app.follow_mode());
        // Simulate new log data arriving with more lines.
        app.set_selected_log(LogTailView {
            lines: vec!["line1".into(), "line2".into()],
            message: String::new(),
        });
        assert_eq!(app.log_scroll(), 0);
        // Simulate even more data.
        app.set_selected_log(LogTailView {
            lines: vec!["line1".into(), "line2".into(), "line3".into()],
            message: String::new(),
        });
        assert_eq!(app.log_scroll(), 0);
        assert!(app.follow_mode());
    }

    #[test]
    fn follow_header_shows_on_in_logs_tab() {
        let mut app = App::new("default", 12);
        app.set_tab(MainTab::Logs);
        app.update(InputEvent::Resize(ResizeEvent {
            width: 160,
            height: 30,
        }));
        let frame = app.render();
        let header = frame.row_text(0);
        assert!(
            header.contains("follow:ON"),
            "header should show follow:ON, got: {header}"
        );
    }

    #[test]
    fn follow_header_shows_off_after_scroll() {
        let mut app = App::new("default", 12);
        app.set_tab(MainTab::Logs);
        app.update(InputEvent::Resize(ResizeEvent {
            width: 160,
            height: 30,
        }));
        app.update(key(Key::Char('u')));
        let frame = app.render();
        let header = frame.row_text(0);
        assert!(
            header.contains("follow:off"),
            "header should show follow:off, got: {header}"
        );
    }

    #[test]
    fn expanded_logs_f_toggles_follow() {
        let mut app = app_with_loops(3);
        app.update(key(Key::Char('l')));
        assert_eq!(app.mode(), UiMode::ExpandedLogs);
        assert!(app.follow_mode());
        app.update(key(Key::Char('F')));
        assert!(!app.follow_mode());
        app.update(key(Key::Char('F')));
        assert!(app.follow_mode());
    }

    // -- run selection --

    #[test]
    fn comma_dot_move_run_selection() {
        let mut app = App::new("default", 12);
        app.set_tab(MainTab::Logs);
        app.set_run_history(vec![
            RunView {
                id: "run-0".into(),
                ..Default::default()
            },
            RunView {
                id: "run-1".into(),
                ..Default::default()
            },
        ]);
        assert_eq!(
            app.selected_run_view().map(|r| r.id.as_str()),
            Some("run-0")
        );
        app.update(key(Key::Char('.')));
        assert_eq!(
            app.selected_run_view().map(|r| r.id.as_str()),
            Some("run-1")
        );
        app.update(key(Key::Char(',')));
        assert_eq!(
            app.selected_run_view().map(|r| r.id.as_str()),
            Some("run-0")
        );
    }

    #[test]
    fn runs_pane_selection_drives_output_context() {
        let mut app = App::new("default", 12);
        app.set_tab(MainTab::Runs);
        app.set_run_history(vec![
            RunView {
                id: "run-a".into(),
                status: "success".into(),
                exit_code: Some(0),
                duration: "10s".into(),
                profile_name: "prod-sre".into(),
                profile_id: "profile-a".into(),
                harness: "codex".into(),
                auth_kind: "ssh".into(),
                started_at: "2026-02-13T12:00:00Z".into(),
                output_lines: vec!["output-a-line-1".into(), "output-a-line-2".into()],
            },
            RunView {
                id: "run-b".into(),
                status: "error".into(),
                exit_code: Some(1),
                duration: "11s".into(),
                profile_name: "prod-sre".into(),
                profile_id: "profile-a".into(),
                harness: "codex".into(),
                auth_kind: "ssh".into(),
                started_at: "2026-02-13T12:01:00Z".into(),
                output_lines: vec!["output-b-line-1".into(), "output-b-line-2".into()],
            },
        ]);
        app.update(InputEvent::Resize(ResizeEvent {
            width: 110,
            height: 24,
        }));
        app.update(key(Key::Char('i')));

        let first = app.render().snapshot();
        assert!(first.contains("output-a-line-1"), "snapshot:\n{first}");
        assert!(!first.contains("output-b-line-1"), "snapshot:\n{first}");

        app.update(key(Key::Char('.')));
        let second = app.render().snapshot();
        assert!(second.contains("output-b-line-1"), "snapshot:\n{second}");
    }

    #[test]
    fn runs_pane_respects_output_scroll_offset() {
        let mut app = App::new("default", 12);
        app.set_tab(MainTab::Runs);
        app.set_run_history(vec![RunView {
            id: "run-scroll".into(),
            status: "success".into(),
            exit_code: Some(0),
            duration: "9s".into(),
            profile_name: "prod-sre".into(),
            profile_id: "profile-a".into(),
            harness: "codex".into(),
            auth_kind: "ssh".into(),
            started_at: "2026-02-13T12:02:00Z".into(),
            output_lines: (0..220)
                .map(|idx| format!("scroll-line-{idx:03}"))
                .collect(),
        }]);
        app.update(InputEvent::Resize(ResizeEvent {
            width: 110,
            height: 24,
        }));
        app.update(key(Key::Char('i')));

        let before = app.render().snapshot();
        assert!(before.contains("scroll-line-219"), "snapshot:\n{before}");

        app.update(key(Key::Char('u')));
        let after = app.render().snapshot();
        assert!(after.contains("scroll-line-193"), "snapshot:\n{after}");
        assert!(!after.contains("scroll-line-219"), "snapshot:\n{after}");
    }

    // -- expanded logs mode --

    #[test]
    fn l_enters_expanded_logs() {
        let mut app = app_with_loops(3);
        app.update(key(Key::Char('l')));
        assert_eq!(app.mode(), UiMode::ExpandedLogs);
    }

    #[test]
    fn l_noop_without_selection() {
        let mut app = App::new("default", 12);
        app.update(key(Key::Char('l')));
        assert_eq!(app.mode(), UiMode::Main);
    }

    #[test]
    fn expanded_logs_escape_returns_to_main() {
        let mut app = app_with_loops(3);
        app.update(key(Key::Char('l')));
        assert_eq!(app.mode(), UiMode::ExpandedLogs);
        app.update(key(Key::Escape));
        assert_eq!(app.mode(), UiMode::Main);
    }

    // -- wizard mode --

    #[test]
    fn n_enters_wizard() {
        let mut app = App::new("default", 12);
        app.update(key(Key::Char('n')));
        assert_eq!(app.mode(), UiMode::Wizard);
        assert_eq!(app.wizard().step, 1);
        assert_eq!(app.wizard().field, 0);
        assert_eq!(app.wizard().values.count, "1");
    }

    #[test]
    fn wizard_escape_exits() {
        let mut app = App::new("default", 12);
        app.update(key(Key::Char('n')));
        app.update(key(Key::Escape));
        assert_eq!(app.mode(), UiMode::Main);
    }

    #[test]
    fn wizard_tab_wraps_fields_by_step() {
        let mut app = App::new("default", 12);
        app.update(key(Key::Char('n')));
        assert_eq!(app.wizard().field, 0);
        app.update(key(Key::Tab));
        assert_eq!(app.wizard().field, 1);
        app.update(key(Key::Tab));
        assert_eq!(app.wizard().field, 2);
        app.update(key(Key::Tab));
        assert_eq!(app.wizard().field, 0);
    }

    #[test]
    fn wizard_shift_tab_and_up_move_previous_field() {
        let mut app = App::new("default", 12);
        app.update(key(Key::Char('n')));
        app.update(key(Key::Tab));
        assert_eq!(app.wizard().field, 1);

        app.update(InputEvent::Key(KeyEvent {
            key: Key::Tab,
            modifiers: Modifiers {
                shift: true,
                ctrl: false,
                alt: false,
            },
        }));
        assert_eq!(app.wizard().field, 0);

        app.update(key(Key::Up));
        assert_eq!(app.wizard().field, 2);
    }

    #[test]
    fn wizard_enter_validates_count_and_stays_on_step() {
        let mut app = App::new("default", 12);
        app.update(key(Key::Char('n')));
        app.wizard.values.count = "0".to_owned();

        let cmd = app.update(key(Key::Enter));
        assert_eq!(cmd, Command::None);
        assert_eq!(app.wizard().step, 1);
        assert!(app.wizard().error.contains("count"));
    }

    #[test]
    fn wizard_enter_advances_steps_and_back_goes_previous() {
        let mut app = App::new("default", 12);
        app.update(key(Key::Char('n')));
        assert_eq!(app.wizard().step, 1);

        app.update(key(Key::Enter));
        assert_eq!(app.wizard().step, 2);

        app.update(key(Key::Enter));
        assert_eq!(app.wizard().step, 3);

        app.update(key(Key::Char('b')));
        assert_eq!(app.wizard().step, 2);

        app.update(key(Key::Left));
        assert_eq!(app.wizard().step, 1);
    }

    #[test]
    fn wizard_text_editing_updates_focused_field() {
        let mut app = App::new("default", 12);
        app.update(key(Key::Char('n')));

        app.update(key(Key::Char('x')));
        app.update(key(Key::Char('y')));
        assert_eq!(app.wizard().values.name, "xy");

        app.update(key(Key::Backspace));
        assert_eq!(app.wizard().values.name, "x");

        app.update(key(Key::Char(' ')));
        assert_eq!(app.wizard().values.name, "x ");
    }

    #[test]
    fn wizard_enter_on_review_submits_create_action() {
        let mut app = App::new("default", 12);
        app.update(key(Key::Char('n')));

        app.wizard.values.name = "wizard-loop".to_owned();
        app.wizard.values.count = "1".to_owned();
        app.wizard.values.interval = "30s".to_owned();

        app.update(key(Key::Enter));
        app.update(key(Key::Enter));
        app.update(key(Key::Enter));
        assert_eq!(app.wizard().step, 4);

        let cmd = app.update(key(Key::Enter));
        match cmd {
            Command::RunAction(ActionKind::Create { wizard }) => {
                assert!(wizard
                    .iter()
                    .any(|(k, v)| k == "name" && v == "wizard-loop"));
                assert!(wizard.iter().any(|(k, v)| k == "count" && v == "1"));
                assert!(wizard.iter().any(|(k, v)| k == "interval" && v == "30s"));
            }
            other => panic!("Expected RunAction(Create), got {other:?}"),
        }
        assert!(app.action_busy());
    }

    #[test]
    fn wizard_with_defaults_populates_fields() {
        let mut app = App::new("default", 12);
        app.set_wizard_defaults("30s", "default.md", "run tests");
        app.update(key(Key::Char('n')));
        assert_eq!(app.wizard().values.interval, "30s");
        assert_eq!(app.wizard().values.prompt, "default.md");
        assert_eq!(app.wizard().values.prompt_msg, "run tests");
        assert_eq!(app.wizard().values.count, "1");
        assert!(app.wizard().values.name.is_empty());
    }

    #[test]
    fn wizard_q_exits() {
        let mut app = App::new("default", 12);
        app.update(key(Key::Char('n')));
        assert_eq!(app.mode(), UiMode::Wizard);
        app.update(key(Key::Char('q')));
        assert_eq!(app.mode(), UiMode::Main);
    }

    #[test]
    fn wizard_question_mark_opens_help() {
        let mut app = App::new("default", 12);
        app.update(key(Key::Char('n')));
        app.update(key(Key::Char('?')));
        assert_eq!(app.mode(), UiMode::Help);
        assert_eq!(app.help_return, UiMode::Wizard);
        app.update(key(Key::Escape));
        assert_eq!(app.mode(), UiMode::Wizard);
    }

    #[test]
    fn wizard_j_k_cycle_fields() {
        let mut app = App::new("default", 12);
        app.update(key(Key::Char('n')));
        assert_eq!(app.wizard().field, 0);
        app.update(key(Key::Char('j')));
        assert_eq!(app.wizard().field, 1);
        app.update(key(Key::Char('k')));
        assert_eq!(app.wizard().field, 0);
        app.update(key(Key::Char('k')));
        assert_eq!(app.wizard().field, 2);
    }

    #[test]
    fn wizard_down_up_cycle_fields() {
        let mut app = App::new("default", 12);
        app.update(key(Key::Char('n')));
        app.update(key(Key::Down));
        assert_eq!(app.wizard().field, 1);
        app.update(key(Key::Up));
        assert_eq!(app.wizard().field, 0);
    }

    #[test]
    fn wizard_ctrl_h_backspaces() {
        let mut app = App::new("default", 12);
        app.update(key(Key::Char('n')));
        app.update(key(Key::Char('a')));
        app.update(key(Key::Char('c')));
        assert_eq!(app.wizard().values.name, "ac");
        app.update(ctrl_key('h'));
        assert_eq!(app.wizard().values.name, "a");
    }

    #[test]
    fn wizard_back_noop_on_step_1() {
        let mut app = App::new("default", 12);
        app.update(key(Key::Char('n')));
        assert_eq!(app.wizard().step, 1);
        app.update(key(Key::Char('b')));
        assert_eq!(app.wizard().step, 1);
        app.update(key(Key::Left));
        assert_eq!(app.wizard().step, 1);
    }

    #[test]
    fn wizard_step4_ignores_text_input() {
        let mut app = App::new("default", 12);
        app.update(key(Key::Char('n')));
        app.update(key(Key::Enter));
        app.update(key(Key::Enter));
        app.update(key(Key::Enter));
        assert_eq!(app.wizard().step, 4);
        app.update(key(Key::Char('x')));
        app.update(key(Key::Char(' ')));
        app.update(key(Key::Backspace));
        app.update(ctrl_key('h'));
        assert!(app.wizard().values.name.is_empty());
    }

    #[test]
    fn wizard_enter_clears_error_on_success() {
        let mut app = App::new("default", 12);
        app.update(key(Key::Char('n')));
        app.wizard.values.count = "0".to_owned();
        app.update(key(Key::Enter));
        assert!(!app.wizard().error.is_empty());
        app.wizard.values.count = "1".to_owned();
        app.update(key(Key::Enter));
        assert_eq!(app.wizard().step, 2);
        assert!(app.wizard().error.is_empty());
    }

    #[test]
    fn wizard_escape_clears_error() {
        let mut app = App::new("default", 12);
        app.update(key(Key::Char('n')));
        app.wizard.values.count = "bad".to_owned();
        app.update(key(Key::Enter));
        assert!(!app.wizard().error.is_empty());
        app.update(key(Key::Escape));
        assert_eq!(app.mode(), UiMode::Main);
    }

    #[test]
    fn wizard_validates_name_requires_count_1() {
        let mut app = App::new("default", 12);
        app.update(key(Key::Char('n')));
        app.wizard.values.name = "my-loop".to_owned();
        app.wizard.values.count = "3".to_owned();
        app.update(key(Key::Enter));
        assert_eq!(app.wizard().step, 1);
        assert!(app.wizard().error.contains("name requires count=1"));
    }

    #[test]
    fn wizard_validates_pool_profile_conflict() {
        let mut app = App::new("default", 12);
        app.update(key(Key::Char('n')));
        app.update(key(Key::Enter));
        assert_eq!(app.wizard().step, 2);
        app.wizard.values.pool = "my-pool".to_owned();
        app.wizard.values.profile = "my-profile".to_owned();
        app.update(key(Key::Enter));
        assert_eq!(app.wizard().step, 2);
        assert!(app
            .wizard()
            .error
            .contains("use either pool or profile, not both"));
    }

    #[test]
    fn wizard_validates_interval_duration() {
        let mut app = App::new("default", 12);
        app.update(key(Key::Char('n')));
        app.update(key(Key::Enter));
        app.update(key(Key::Enter));
        assert_eq!(app.wizard().step, 3);
        app.wizard.values.interval = "not-a-duration".to_owned();
        app.update(key(Key::Enter));
        assert_eq!(app.wizard().step, 3);
        assert!(app.wizard().error.contains("interval"));
    }

    #[test]
    fn wizard_validates_max_runtime_duration() {
        let mut app = App::new("default", 12);
        app.update(key(Key::Char('n')));
        app.update(key(Key::Enter));
        app.update(key(Key::Enter));
        app.wizard.values.max_runtime = "xyz".to_owned();
        app.update(key(Key::Enter));
        assert_eq!(app.wizard().step, 3);
        assert!(app.wizard().error.contains("max runtime"));
    }

    #[test]
    fn wizard_validates_max_iterations_integer() {
        let mut app = App::new("default", 12);
        app.update(key(Key::Char('n')));
        app.update(key(Key::Enter));
        app.update(key(Key::Enter));
        app.wizard.values.max_iterations = "abc".to_owned();
        app.update(key(Key::Enter));
        assert_eq!(app.wizard().step, 3);
        assert!(app.wizard().error.contains("max-iterations"));
    }

    #[test]
    fn wizard_validates_negative_max_iterations() {
        let mut app = App::new("default", 12);
        app.update(key(Key::Char('n')));
        app.update(key(Key::Enter));
        app.update(key(Key::Enter));
        app.wizard.values.max_iterations = "-5".to_owned();
        app.update(key(Key::Enter));
        assert_eq!(app.wizard().step, 3);
        assert!(app.wizard().error.contains("max-iterations"));
    }

    #[test]
    fn wizard_empty_limits_are_valid() {
        let mut app = App::new("default", 12);
        app.update(key(Key::Char('n')));
        app.update(key(Key::Enter));
        app.update(key(Key::Enter));
        app.wizard.values.interval = String::new();
        app.wizard.values.max_runtime = String::new();
        app.wizard.values.max_iterations = String::new();
        app.update(key(Key::Enter));
        assert_eq!(app.wizard().step, 4);
        assert!(app.wizard().error.is_empty());
    }

    #[test]
    fn wizard_valid_durations_pass() {
        let mut app = App::new("default", 12);
        app.update(key(Key::Char('n')));
        app.update(key(Key::Enter));
        app.update(key(Key::Enter));
        app.wizard.values.interval = "30s".to_owned();
        app.wizard.values.max_runtime = "1h".to_owned();
        app.wizard.values.max_iterations = "100".to_owned();
        app.update(key(Key::Enter));
        assert_eq!(app.wizard().step, 4);
    }

    #[test]
    fn wizard_step2_has_2_fields() {
        let mut app = App::new("default", 12);
        app.update(key(Key::Char('n')));
        app.update(key(Key::Enter));
        assert_eq!(app.wizard().step, 2);
        assert_eq!(app.wizard().field, 0);
        app.update(key(Key::Tab));
        assert_eq!(app.wizard().field, 1);
        app.update(key(Key::Tab));
        assert_eq!(app.wizard().field, 0);
    }

    #[test]
    fn wizard_step3_has_6_fields() {
        let mut app = App::new("default", 12);
        app.update(key(Key::Char('n')));
        app.update(key(Key::Enter));
        app.update(key(Key::Enter));
        assert_eq!(app.wizard().step, 3);
        for i in 0..6 {
            assert_eq!(app.wizard().field, i);
            app.update(key(Key::Tab));
        }
        assert_eq!(app.wizard().field, 0);
    }

    #[test]
    fn wizard_typing_on_step2_edits_pool() {
        let mut app = App::new("default", 12);
        app.update(key(Key::Char('n')));
        app.update(key(Key::Enter));
        app.update(key(Key::Char('p')));
        app.update(key(Key::Char('1')));
        assert_eq!(app.wizard().values.pool, "p1");
        app.update(key(Key::Tab));
        app.update(key(Key::Char('x')));
        assert_eq!(app.wizard().values.profile, "x");
    }

    #[test]
    fn wizard_render_step1_snapshot() {
        let mut app = App::new("default", 12);
        app.update(key(Key::Char('n')));
        app.wizard.values.name = "my-loop".to_owned();
        let lines = app.render_wizard_lines(80);
        assert!(lines[0].contains("step 1/4"));
        assert!(lines[1].contains("1) Identity+Count"));
        let body = lines.join("\n");
        assert!(body.contains("name: my-loop_"));
        assert!(body.contains("name-prefix: <empty>"));
        assert!(body.contains("count: 1"));
        assert!(body.contains("tab/down/up navigate"));
    }

    #[test]
    fn wizard_render_step2_snapshot() {
        let mut app = App::new("default", 12);
        app.update(key(Key::Char('n')));
        app.update(key(Key::Enter));
        let lines = app.render_wizard_lines(80);
        assert!(lines[0].contains("step 2/4"));
        let body = lines.join("\n");
        assert!(body.contains("pool: <empty>_"));
        assert!(body.contains("profile: <empty>"));
    }

    #[test]
    fn wizard_render_step3_snapshot() {
        let mut app = App::new("default", 12);
        app.update(key(Key::Char('n')));
        app.update(key(Key::Enter));
        app.update(key(Key::Enter));
        let lines = app.render_wizard_lines(80);
        assert!(lines[0].contains("step 3/4"));
        let body = lines.join("\n");
        assert!(body.contains("prompt:"));
        assert!(body.contains("interval:"));
        assert!(body.contains("max-runtime:"));
        assert!(body.contains("max-iterations:"));
        assert!(body.contains("tags:"));
    }

    #[test]
    fn wizard_render_step4_review_snapshot() {
        let mut app = App::new("default", 12);
        app.update(key(Key::Char('n')));
        app.wizard.values.name = "test".to_owned();
        app.wizard.values.interval = "10s".to_owned();
        app.update(key(Key::Enter));
        app.update(key(Key::Enter));
        app.update(key(Key::Enter));
        assert_eq!(app.wizard().step, 4);
        let lines = app.render_wizard_lines(80);
        assert!(lines[0].contains("step 4/4"));
        let body = lines.join("\n");
        assert!(body.contains("Review:"));
        assert!(body.contains("name=\"test\""));
        assert!(body.contains("count=\"1\""));
        assert!(body.contains("interval=\"10s\""));
    }

    #[test]
    fn wizard_render_shows_error() {
        let mut app = App::new("default", 12);
        app.update(key(Key::Char('n')));
        app.wizard.values.count = "0".to_owned();
        app.update(key(Key::Enter));
        let lines = app.render_wizard_lines(80);
        let body = lines.join("\n");
        assert!(body.contains("Error:"));
        assert!(body.contains("count"));
    }

    #[test]
    fn wizard_render_truncates_long_lines() {
        let mut app = App::new("default", 12);
        app.update(key(Key::Char('n')));
        app.wizard.values.name = "a".repeat(100);
        let lines = app.render_wizard_lines(50);
        for line in &lines {
            assert!(line.len() <= 50, "line too long: {}", line.len());
        }
    }

    #[test]
    fn wizard_render_frame_smoke() {
        let mut app = App::new("default", 12);
        app.update(key(Key::Char('n')));
        app.wizard.values.count = "bad".to_owned();
        app.update(key(Key::Enter));
        let frame = app.render();
        let snap = frame.snapshot();
        assert!(snap.contains("New loop wizard"));
    }

    #[test]
    fn wizard_header_shows_mode() {
        let mut app = App::new("default", 12);
        app.update(key(Key::Char('n')));
        let frame = app.render();
        let header = frame.row_text(0);
        assert!(
            header.contains("New Loop Wizard") || header.contains("Wizard"),
            "header: {header}"
        );
    }

    // -- confirm mode --

    #[test]
    fn s_enters_confirm_for_stop() {
        let mut app = app_with_loops(3);
        app.update(key(Key::Char('S')));
        assert_eq!(app.mode(), UiMode::Confirm);
        let confirm = app.confirm();
        assert!(confirm.is_some());
        let confirm = match confirm {
            Some(v) => v,
            None => panic!("expected confirm state"),
        };
        assert_eq!(confirm.action, ActionType::Stop);
        assert!(confirm.prompt.contains("Stop loop"));
    }

    #[test]
    fn confirm_n_cancels() {
        let mut app = app_with_loops(3);
        app.update(key(Key::Char('S')));
        assert_eq!(app.mode(), UiMode::Confirm);
        app.update(key(Key::Char('n')));
        assert_eq!(app.mode(), UiMode::Main);
        assert!(app.confirm().is_none());
    }

    #[test]
    fn confirm_y_returns_action() {
        let mut app = app_with_loops(3);
        app.update(key(Key::Char('S')));
        let cmd = app.update(key(Key::Char('y')));
        assert_eq!(app.mode(), UiMode::Main);
        match cmd {
            Command::RunAction(ActionKind::Stop { loop_id }) => {
                assert_eq!(loop_id, "loop-0");
            }
            other => panic!("Expected RunAction(Stop), got {other:?}"),
        }
    }

    #[test]
    fn confirm_enter_uses_safe_cancel_by_default() {
        let mut app = app_with_loops(3);
        app.update(key(Key::Char('S')));
        let cmd = app.update(key(Key::Enter));
        assert_eq!(cmd, Command::None);
        assert_eq!(app.mode(), UiMode::Main);
        assert!(app.confirm().is_none());
        assert!(app.status_text().contains("cancelled"));
    }

    #[test]
    fn confirm_action_rail_tab_then_enter_submits() {
        let mut app = app_with_loops(3);
        app.update(key(Key::Char('S')));
        app.update(key(Key::Tab));
        let cmd = app.update(key(Key::Enter));
        match cmd {
            Command::RunAction(ActionKind::Stop { loop_id }) => {
                assert_eq!(loop_id, "loop-0");
            }
            other => panic!("Expected RunAction(Stop), got {other:?}"),
        }
    }

    #[test]
    fn confirm_kill_requires_typed_reason_before_submit() {
        let mut app = app_with_loops(3);
        app.update(key(Key::Char('K')));
        app.update(key(Key::Tab));
        let cmd = app.update(key(Key::Enter));
        assert_eq!(cmd, Command::None);
        assert_eq!(app.mode(), UiMode::Confirm);
        assert!(app.status_text().contains("Reason required"));
    }

    #[test]
    fn confirm_kill_submit_after_reason_input() {
        let mut app = app_with_loops(3);
        app.update(key(Key::Char('K')));
        app.update(key(Key::Tab));
        for ch in "incident risk".chars() {
            app.update(key(Key::Char(ch)));
        }
        let cmd = app.update(key(Key::Enter));
        match cmd {
            Command::RunAction(ActionKind::Kill { loop_id }) => {
                assert_eq!(loop_id, "loop-0");
            }
            other => panic!("Expected RunAction(Kill), got {other:?}"),
        }
    }

    // -- confirm no selection --

    #[test]
    fn confirm_noop_without_selection() {
        let mut app = App::new("default", 12);
        app.update(key(Key::Char('S')));
        assert_eq!(app.mode(), UiMode::Main);
        assert!(app.confirm().is_none());
    }

    // -- resize --

    #[test]
    fn resize_updates_dimensions() {
        let mut app = App::new("default", 12);
        app.update(InputEvent::Resize(ResizeEvent {
            width: 200,
            height: 50,
        }));
        assert_eq!(app.width(), 200);
        assert_eq!(app.height(), 50);
    }

    // -- render smoke test --

    #[test]
    fn render_produces_non_empty_frame() {
        let app = app_with_loops(3);
        let frame = app.render();
        assert_eq!(frame.size().width, 120);
        assert_eq!(frame.size().height, 40);
        assert!(frame.row_text(0).contains("Forge Loops"));
        assert!(frame.row_text(1).contains("Overview"));
    }

    #[test]
    fn render_panicking_registered_view_falls_back_locally() {
        let mut app = app_with_loops(2);
        app.register_view(MainTab::Overview, Box::new(PanicView));
        app.update(key(Key::Char('i')));

        let frame = app.render();
        let snapshot = frame.snapshot();
        assert!(snapshot.contains("Overview unavailable"), "{snapshot}");
        assert!(snapshot.contains("cause: pane exploded"), "{snapshot}");
        assert!(snapshot.contains("Forge Loops"), "{snapshot}");
    }

    #[test]
    fn overview_empty_state_guides_loop_creation() {
        let mut app = App::new("default", 12);
        app.update(key(Key::Char('i'))); // dismiss onboarding overlay
        let frame = app.render();

        let all_rows = frame.snapshot();
        assert!(
            all_rows.contains("Start one: forge up --count 1"),
            "rendered rows:\\n{all_rows}"
        );
    }

    #[test]
    fn render_error_state_shows_prefixed_error_text() {
        let mut app = App::new("default", 12);
        app.set_status(StatusKind::Err, "boom");

        let frame = app.render();
        let all_rows = frame.snapshot();
        assert!(
            all_rows.contains("Error: boom"),
            "rendered rows:\\n{all_rows}"
        );
    }

    #[test]
    fn render_help_mode_shows_help() {
        let mut app = App::new("default", 12);
        app.update(key(Key::Char('?')));
        let frame = app.render();
        assert!(frame.row_text(2).contains("Forge Loop TUI Help"));
    }

    #[test]
    fn render_quitting_returns_blank() {
        let mut app = App::new("default", 12);
        app.update(key(Key::Char('q')));
        let frame = app.render();
        // All rows should be blank.
        assert!(frame.row_text(0).trim().is_empty());
    }

    // -- multi page bounds --

    #[test]
    fn multi_page_bounds_basic() {
        let (page, total_pages, start, end) = multi_page_bounds(10, 4, 0);
        assert_eq!(page, 0);
        assert_eq!(total_pages, 3);
        assert_eq!(start, 0);
        assert_eq!(end, 4);
    }

    #[test]
    fn multi_page_bounds_clamps() {
        let (page, total_pages, start, end) = multi_page_bounds(10, 4, 999);
        assert_eq!(page, 2);
        assert_eq!(total_pages, 3);
        assert_eq!(start, 8);
        assert_eq!(end, 10);
    }

    #[test]
    fn multi_page_bounds_empty() {
        let (page, total_pages, start, end) = multi_page_bounds(0, 4, 0);
        assert_eq!(page, 0);
        assert_eq!(total_pages, 1);
        assert_eq!(start, 0);
        assert_eq!(end, 0);
    }

    #[test]
    fn trim_to_width_handles_unicode_without_panicking() {
        let value = "⚠ warning: résumé";
        let trimmed = trim_to_width(value, 5);
        assert_eq!(trimmed.chars().count(), 5);
        assert_eq!(trimmed, "⚠ war");
    }

    // -- ordered multi target views --

    #[test]
    fn ordered_multi_targets_pinned_first() {
        let mut app = app_with_loops(4);
        app.toggle_pinned("loop-2");
        let ordered = app.ordered_multi_target_views();
        assert_eq!(ordered[0].id, "loop-2");
        assert_eq!(ordered.len(), 4);
    }

    // -- placeholder view --

    #[test]
    fn placeholder_view_renders() {
        let view = PlaceholderView::new(MainTab::Overview);
        let frame = view.view(
            FrameSize {
                width: 40,
                height: 3,
            },
            crate::default_theme(),
        );
        assert!(frame.row_text(0).contains("Overview tab"));
    }

    // -- resume action --

    #[test]
    fn r_resumes_selected() {
        let mut app = app_with_loops(3);
        let cmd = app.update(key(Key::Char('r')));
        match cmd {
            Command::RunAction(ActionKind::Resume { loop_id }) => {
                assert_eq!(loop_id, "loop-0");
            }
            other => panic!("Expected RunAction(Resume), got {other:?}"),
        }
    }

    #[test]
    fn r_noop_without_selection() {
        let mut app = App::new("default", 12);
        let cmd = app.update(key(Key::Char('r')));
        assert_eq!(cmd, Command::None);
    }

    // -- action busy --

    #[test]
    fn set_status_enqueues_notification_event() {
        let mut app = App::new("default", 12);
        assert_eq!(app.notification_queue_len(), 0);
        app.set_status(StatusKind::Info, "hello");
        assert_eq!(app.notification_queue_len(), 1);
        app.set_status(StatusKind::Err, "boom");
        assert_eq!(app.notification_queue_len(), 2);
    }

    #[test]
    fn notification_queue_caps_at_max_entries() {
        let mut app = App::new("default", 12);
        for i in 0..(MAX_NOTIFICATION_QUEUE + 5) {
            app.set_status(StatusKind::Info, &format!("event-{i}"));
        }
        assert_eq!(app.notification_queue_len(), MAX_NOTIFICATION_QUEUE);
    }

    #[test]
    fn status_display_uses_latest_notification_when_current_status_empty() {
        let mut app = App::new("default", 12);
        app.set_status(StatusKind::Info, "hello");
        app.status_text.clear();

        let display = app.status_display_text();
        assert_eq!(display, "hello");
    }

    #[test]
    fn status_display_appends_queued_count_suffix() {
        let mut app = App::new("default", 12);
        app.set_status(StatusKind::Info, "first");
        app.set_status(StatusKind::Info, "second");

        let display = app.status_display_text();
        assert_eq!(display, "second (+1 queued)");
    }

    #[test]
    fn status_display_prefixes_error_from_notification_queue() {
        let mut app = App::new("default", 12);
        app.set_status(StatusKind::Err, "boom");
        app.status_text.clear();
        app.status_kind = StatusKind::Info;

        let display = app.status_display_text();
        assert_eq!(display, "Error: boom");
    }

    #[test]
    fn failure_explain_strip_prioritizes_root_cause_then_frame_then_command() {
        let mut app = App::new("default", 12);
        app.tab = MainTab::Logs;
        app.selected_log.lines = vec![
            "$ cargo test --workspace".to_owned(),
            "running 1 test".to_owned(),
            "thread 'main' panicked at src/main.rs:42:11".to_owned(),
            "caused by: database is locked".to_owned(),
            "at forge::db::persist_run (src/db.rs:120)".to_owned(),
            "fatal: run failed".to_owned(),
        ];

        let strip = match app.failure_explain_strip_text() {
            Some(text) => text,
            None => panic!("failure explain strip should be present"),
        };
        let root_cause_idx = match strip.find("root cause=") {
            Some(idx) => idx,
            None => panic!("root cause fragment should exist"),
        };
        let frame_idx = match strip.find("frame=") {
            Some(idx) => idx,
            None => panic!("frame fragment should exist"),
        };
        let command_idx = match strip.find("command=") {
            Some(idx) => idx,
            None => panic!("command fragment should exist"),
        };

        assert!(strip.starts_with("Failure explain: "));
        assert!(root_cause_idx < frame_idx);
        assert!(frame_idx < command_idx);
        assert!(!strip.contains("context="));
    }

    #[test]
    fn failure_explain_strip_hides_when_no_failure_detected() {
        let mut app = App::new("default", 12);
        app.tab = MainTab::Logs;
        app.selected_log.lines = vec![
            "$ cargo test --workspace".to_owned(),
            "running 12 tests".to_owned(),
            "all tests passed".to_owned(),
        ];

        assert!(app.failure_explain_strip_text().is_none());
    }

    #[test]
    fn notification_center_ack_hides_latest_from_status_fallback() {
        let mut app = App::new("default", 12);
        app.set_status(StatusKind::Info, "first");
        app.set_status(StatusKind::Info, "second");
        app.status_text.clear();
        app.status_kind = StatusKind::Info;
        assert!(app.notification_center_ack_latest());

        let display = app.status_display_text();
        assert_eq!(display, "first");
    }

    #[test]
    fn notification_center_snooze_hides_until_clock_advances() {
        let mut app = App::new("default", 12);
        app.set_status(StatusKind::Info, "only");
        app.status_text.clear();
        app.status_kind = StatusKind::Info;
        assert!(app.notification_center_snooze_latest(3));
        assert_eq!(app.status_display_text(), "timers:1 next:3t");

        app.advance_notification_clock(2);
        assert_eq!(app.status_display_text(), "timers:1 next:1t");

        app.advance_notification_clock(1);
        assert_eq!(app.status_display_text(), "only");
    }

    #[test]
    fn status_display_appends_timer_summary_when_status_present() {
        let mut app = App::new("default", 12);
        app.set_status(StatusKind::Info, "first");
        app.set_status(StatusKind::Info, "second");
        assert!(app.notification_center_snooze_latest(4));

        let display = app.status_display_text();
        assert_eq!(display, "second [timers:1 next:4t]");
    }

    #[test]
    fn notification_center_entries_include_escalation_and_snooze_flags() {
        let mut app = App::new("default", 12);
        app.set_status(StatusKind::Err, "critical");
        assert!(app.notification_center_escalate_latest());
        assert!(app.notification_center_snooze_latest(2));

        let entries = app.notification_center_entries();
        assert_eq!(entries.len(), 1);
        assert_eq!(entries[0].kind, StatusKind::Err);
        assert_eq!(entries[0].text, "critical");
        assert!(entries[0].escalated);
        assert!(entries[0].snoozed);
        assert!(!entries[0].acknowledged);
    }

    #[test]
    fn action_busy_blocks_new_action() {
        let mut app = app_with_loops(3);
        app.set_action_busy(true);
        let cmd = app.update(key(Key::Char('r')));
        assert_eq!(cmd, Command::None);
        assert!(app.status_text().contains("Another action"));
    }

    // -- set_loops preserves selection --

    #[test]
    fn set_loops_preserves_selected_id() {
        let mut app = app_with_loops(5);
        app.move_selection(2);
        assert_eq!(app.selected_id(), "loop-2");
        // Re-set loops with same data.
        app.set_loops(sample_loops(5));
        assert_eq!(app.selected_id(), "loop-2");
        assert_eq!(app.selected_idx(), 2);
    }

    // -- delete confirm force --

    #[test]
    fn delete_running_loop_shows_force() {
        let mut app = app_with_loops(3);
        // loop-0 is "running"
        app.update(key(Key::Char('D')));
        let confirm = match app.confirm() {
            Some(v) => v,
            None => panic!("expected confirm state"),
        };
        assert!(confirm.prompt.contains("Force delete"));
        assert!(confirm.force_delete);
        assert!(confirm.reason_required);
    }

    #[test]
    fn delete_stopped_loop_normal() {
        let mut app = app_with_loops(3);
        app.move_selection(1); // loop-1 is "stopped"
        app.update(key(Key::Char('D')));
        let confirm = match app.confirm() {
            Some(v) => v,
            None => panic!("expected confirm state"),
        };
        assert!(confirm.prompt.contains("Delete loop record"));
        assert!(!confirm.prompt.contains("Force"));
        assert!(!confirm.force_delete);
        assert!(!confirm.reason_required);
    }

    #[test]
    fn force_delete_requires_typed_reason_before_submit() {
        let mut app = app_with_loops(3);
        app.update(key(Key::Char('D')));
        app.update(key(Key::Tab));
        let cmd = app.update(key(Key::Enter));
        assert_eq!(cmd, Command::None);
        assert_eq!(app.mode(), UiMode::Confirm);
        assert!(app.status_text().contains("Reason required"));
    }

    // -- handle_action_result --

    #[test]
    fn action_result_success_clears_busy_and_sets_ok_status() {
        let mut app = app_with_loops(3);
        app.set_action_busy(true);
        let cmd = app.handle_action_result(ActionResult {
            kind: ActionType::Stop,
            loop_id: "loop-0".into(),
            selected_loop_id: String::new(),
            message: "Stop requested for loop loop-0".into(),
            error: None,
        });
        assert!(!app.action_busy());
        assert!(app.status_text().contains("Stop requested"));
        assert_eq!(cmd, Command::Fetch);
    }

    #[test]
    fn action_result_error_sets_err_status() {
        let mut app = app_with_loops(3);
        app.set_action_busy(true);
        let cmd = app.handle_action_result(ActionResult {
            kind: ActionType::Kill,
            loop_id: "loop-0".into(),
            selected_loop_id: String::new(),
            message: String::new(),
            error: Some("loop not found".into()),
        });
        assert!(!app.action_busy());
        assert!(app.status_text().contains("loop not found"));
        assert_eq!(cmd, Command::None);
    }

    #[test]
    fn action_result_create_error_returns_to_wizard() {
        let mut app = app_with_loops(3);
        app.set_action_busy(true);
        app.mode = UiMode::Main;
        app.handle_action_result(ActionResult {
            kind: ActionType::Create,
            loop_id: String::new(),
            selected_loop_id: String::new(),
            message: String::new(),
            error: Some("invalid count".into()),
        });
        assert_eq!(app.mode(), UiMode::Wizard);
        assert_eq!(app.wizard().error, "invalid count");
    }

    #[test]
    fn action_result_create_success_selects_new_loop() {
        let mut app = app_with_loops(3);
        app.set_action_busy(true);
        app.mode = UiMode::Wizard;
        let cmd = app.handle_action_result(ActionResult {
            kind: ActionType::Create,
            loop_id: String::new(),
            selected_loop_id: "new-loop-42".into(),
            message: "Created 1 loop".into(),
            error: None,
        });
        assert_eq!(app.mode(), UiMode::Main);
        assert_eq!(app.selected_id(), "new-loop-42");
        assert!(app.wizard().error.is_empty());
        assert!(app.status_text().contains("Created 1 loop"));
        assert_eq!(cmd, Command::Fetch);
    }

    #[test]
    fn action_result_resume_success() {
        let mut app = app_with_loops(3);
        app.set_action_busy(true);
        let cmd = app.handle_action_result(ActionResult {
            kind: ActionType::Resume,
            loop_id: "loop-1".into(),
            selected_loop_id: String::new(),
            message: "Loop \"my-loop\" resumed (loop-1)".into(),
            error: None,
        });
        assert!(!app.action_busy());
        assert!(app.status_text().contains("resumed"));
        assert_eq!(cmd, Command::Fetch);
    }
}
