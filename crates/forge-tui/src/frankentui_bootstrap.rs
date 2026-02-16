use std::collections::HashMap;
use std::fs::File;
use std::io::{Read, Seek, SeekFrom};
use std::path::{Path, PathBuf};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use crate::app::{
    ActionKind, ActionResult, ActionType, App, Command as AppCommand, LogTailView, LoopView,
    RunView,
};
use crate::theme::detect_terminal_color_capability;
use forge_cli::{kill, resume, rm, stop, up};
use forge_ftui_adapter::input::{
    translate_input, InputEvent, Key, KeyEvent, Modifiers, MouseButton, MouseEvent, MouseEventKind,
    MouseWheelDirection, ResizeEvent, UiAction,
};
use forge_ftui_adapter::render::FrameCell as AdapterFrameCell;
use forge_ftui_adapter::upstream_bridge::term_color_to_packed_rgba;
use forge_ftui_adapter::upstream_ftui as ftui;
use ftui::core::event::{
    Event, KeyCode as FtuiKeyCode, KeyEvent as FtuiKeyEvent, KeyEventKind as FtuiKeyEventKind,
    Modifiers as FtuiModifiers, MouseEvent as FtuiMouseEvent, MouseEventKind as FtuiMouseEventKind,
};
use ftui::render::cell::{Cell, CellAttrs as FtuiCellAttrs, StyleFlags as FtuiCellStyleFlags};
use ftui::runtime::{Every, Subscription};
use ftui::{App as FtuiApp, Cmd, Frame, Model, ScreenMode};

const REFRESH_INTERVAL_MS: u64 = 900;
const LOG_TAIL_MAX_LINES: usize = 240;
const LOG_TAIL_READ_BYTES: u64 = 256 * 1024;
const RUN_HISTORY_LIMIT: usize = 120;
type LiveData = (
    Vec<LoopView>,
    HashMap<String, Vec<RunView>>,
    HashMap<String, LogTailView>,
);

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum RuntimeEvent {
    Input(InputEvent),
    Tick,
    Quit,
    Ignore,
}

#[derive(Debug, Clone)]
pub enum ForgeShellMsg {
    Runtime(RuntimeEvent),
    SnapshotLoaded(BootstrapSnapshot),
}

impl From<Event> for ForgeShellMsg {
    fn from(event: Event) -> Self {
        Self::Runtime(translate_runtime_event(&event))
    }
}

#[derive(Debug, Clone)]
pub struct BootstrapSnapshot {
    pub loops: Vec<LoopView>,
    pub run_history_by_loop: HashMap<String, Vec<RunView>>,
    pub log_tails_by_loop: HashMap<String, LogTailView>,
    pub refreshed_at_epoch_secs: u64,
    pub error: Option<String>,
}

impl BootstrapSnapshot {
    fn ok(
        loops: Vec<LoopView>,
        run_history_by_loop: HashMap<String, Vec<RunView>>,
        log_tails_by_loop: HashMap<String, LogTailView>,
    ) -> Self {
        Self {
            loops,
            run_history_by_loop,
            log_tails_by_loop,
            refreshed_at_epoch_secs: unix_timestamp_secs(),
            error: None,
        }
    }

    fn err(message: String) -> Self {
        Self {
            loops: Vec::new(),
            run_history_by_loop: HashMap::new(),
            log_tails_by_loop: HashMap::new(),
            refreshed_at_epoch_secs: unix_timestamp_secs(),
            error: Some(message),
        }
    }
}

pub struct ForgeShell {
    db_path: PathBuf,
    spawn_owner: String,
    app: App,
    refresh_count: usize,
    last_action: UiAction,
    last_event: RuntimeEvent,
    last_error: Option<String>,
    last_refreshed_at_epoch_secs: u64,
}

impl ForgeShell {
    #[must_use]
    pub fn new(db_path: PathBuf) -> Self {
        let capability = detect_terminal_color_capability();
        Self {
            db_path,
            spawn_owner: resolve_spawn_owner(),
            app: App::new_with_capability("default", capability, 200),
            refresh_count: 0,
            last_action: UiAction::Noop,
            last_event: RuntimeEvent::Ignore,
            last_error: None,
            last_refreshed_at_epoch_secs: 0,
        }
    }

    fn apply_snapshot(&mut self, snapshot: BootstrapSnapshot) {
        self.last_refreshed_at_epoch_secs = snapshot.refreshed_at_epoch_secs;
        self.refresh_count = self.refresh_count.saturating_add(1);

        self.app.set_loops(snapshot.loops.clone());
        let selected_loop_id = self.selected_loop_id();
        let selected_runs = snapshot
            .run_history_by_loop
            .get(&selected_loop_id)
            .cloned()
            .unwrap_or_default();
        self.app.set_run_history(selected_runs);

        let selected_log = snapshot
            .log_tails_by_loop
            .get(&selected_loop_id)
            .cloned()
            .unwrap_or_else(|| LogTailView {
                lines: Vec::new(),
                message: "No logs captured yet".to_owned(),
            });
        self.app.set_selected_log(selected_log);
        self.app.set_multi_logs(snapshot.log_tails_by_loop);

        self.last_error = snapshot.error.clone();
        if let Some(error) = snapshot.error {
            let _ = self.app.handle_action_result(ActionResult {
                kind: ActionType::None,
                loop_id: String::new(),
                selected_loop_id: String::new(),
                message: String::new(),
                error: Some(error),
            });
        }
    }

    fn selected_loop_id(&self) -> String {
        if !self.app.selected_id().trim().is_empty() {
            return self.app.selected_id().to_owned();
        }
        self.app
            .loops()
            .first()
            .map(|loop_view| loop_view.id.clone())
            .unwrap_or_default()
    }

    fn perform_refresh(&self, task_name: &'static str) -> Cmd<ForgeShellMsg> {
        perform_refresh(task_name, self.db_path.clone())
    }

    fn action_result_for_export_not_wired() -> ActionResult {
        ActionResult {
            kind: ActionType::None,
            loop_id: String::new(),
            selected_loop_id: String::new(),
            message: String::new(),
            error: Some("view export not wired in FrankenTUI runtime yet".to_owned()),
        }
    }

    fn execute_action(&self, action: ActionKind) -> ActionResult {
        match action {
            ActionKind::Resume { loop_id } => {
                let mut backend = resume::SqliteResumeBackend::new(self.db_path.clone());
                let mut args = vec!["resume".to_owned(), loop_id.clone(), "--json".to_owned()];
                if self.spawn_owner != "auto" {
                    args.push("--spawn-owner".to_owned());
                    args.push(self.spawn_owner.clone());
                }
                let (exit_code, stdout, stderr) = run_resume(&args, &mut backend);
                if exit_code == 0 {
                    let message = parse_resume_success_message(&stdout, &loop_id)
                        .unwrap_or_else(|| format!("Loop {loop_id} resumed"));
                    ActionResult {
                        kind: ActionType::Resume,
                        loop_id,
                        selected_loop_id: String::new(),
                        message,
                        error: None,
                    }
                } else {
                    ActionResult {
                        kind: ActionType::Resume,
                        loop_id,
                        selected_loop_id: String::new(),
                        message: String::new(),
                        error: Some(error_from_stderr("resume loop", &stderr)),
                    }
                }
            }
            ActionKind::Stop { loop_id } => {
                let mut backend = stop::SqliteStopBackend::new(self.db_path.clone());
                let args = vec!["stop".to_owned(), loop_id.clone(), "--json".to_owned()];
                let (exit_code, _stdout, stderr) = run_stop(&args, &mut backend);
                if exit_code == 0 {
                    ActionResult {
                        kind: ActionType::Stop,
                        loop_id: loop_id.clone(),
                        selected_loop_id: String::new(),
                        message: format!("Stop queued for loop {loop_id}"),
                        error: None,
                    }
                } else {
                    ActionResult {
                        kind: ActionType::Stop,
                        loop_id: loop_id.clone(),
                        selected_loop_id: String::new(),
                        message: String::new(),
                        error: Some(error_from_stderr("stop loop", &stderr)),
                    }
                }
            }
            ActionKind::Kill { loop_id } => {
                let mut backend = kill::SqliteKillBackend::new(self.db_path.clone());
                let args = vec!["kill".to_owned(), loop_id.clone(), "--json".to_owned()];
                let (exit_code, _stdout, stderr) = run_kill(&args, &mut backend);
                if exit_code == 0 {
                    ActionResult {
                        kind: ActionType::Kill,
                        loop_id: loop_id.clone(),
                        selected_loop_id: String::new(),
                        message: format!("Kill queued for loop {loop_id}"),
                        error: None,
                    }
                } else {
                    ActionResult {
                        kind: ActionType::Kill,
                        loop_id: loop_id.clone(),
                        selected_loop_id: String::new(),
                        message: String::new(),
                        error: Some(error_from_stderr("kill loop", &stderr)),
                    }
                }
            }
            ActionKind::Delete { loop_id, force } => {
                let mut backend = rm::SqliteLoopBackend::new(self.db_path.clone());
                let mut args = vec!["rm".to_owned(), loop_id.clone(), "--json".to_owned()];
                if force {
                    args.push("--force".to_owned());
                }
                let (exit_code, _stdout, stderr) = run_rm(&args, &mut backend);
                if exit_code == 0 {
                    ActionResult {
                        kind: ActionType::Delete,
                        loop_id: loop_id.clone(),
                        selected_loop_id: String::new(),
                        message: format!("Removed loop {loop_id}"),
                        error: None,
                    }
                } else {
                    ActionResult {
                        kind: ActionType::Delete,
                        loop_id: loop_id.clone(),
                        selected_loop_id: String::new(),
                        message: String::new(),
                        error: Some(error_from_stderr("remove loop", &stderr)),
                    }
                }
            }
            ActionKind::Create { wizard } => {
                let mut backend = up::SqliteUpBackend::new(self.db_path.clone());
                let args = build_up_args(&wizard, &self.spawn_owner);
                let (exit_code, stdout, stderr) = run_up(&args, &mut backend);
                if exit_code == 0 {
                    let selected_loop_id =
                        parse_created_loop_id(&self.db_path, &stdout).unwrap_or_default();
                    ActionResult {
                        kind: ActionType::Create,
                        loop_id: String::new(),
                        selected_loop_id,
                        message: create_success_message(&stdout),
                        error: None,
                    }
                } else {
                    ActionResult {
                        kind: ActionType::Create,
                        loop_id: String::new(),
                        selected_loop_id: String::new(),
                        message: String::new(),
                        error: Some(error_from_stderr("create loop", &stderr)),
                    }
                }
            }
        }
    }

    fn apply_batch_commands(
        &mut self,
        commands: Vec<AppCommand>,
        should_refresh: &mut bool,
    ) -> bool {
        for command in commands {
            match command {
                AppCommand::None => {}
                AppCommand::Quit => return true,
                AppCommand::Fetch => *should_refresh = true,
                AppCommand::ExportCurrentView => {
                    let _ = self
                        .app
                        .handle_action_result(Self::action_result_for_export_not_wired());
                }
                AppCommand::RunAction(action) => {
                    let follow_up = self.app.handle_action_result(self.execute_action(action));
                    if self.apply_batch_commands(vec![follow_up], should_refresh) {
                        return true;
                    }
                }
                AppCommand::Batch(nested) => {
                    if self.apply_batch_commands(nested, should_refresh) {
                        return true;
                    }
                }
            }
        }
        false
    }

    fn handle_app_command(&mut self, command: AppCommand) -> Cmd<ForgeShellMsg> {
        match command {
            AppCommand::None => Cmd::none(),
            AppCommand::Quit => Cmd::quit(),
            AppCommand::Fetch => self.perform_refresh("forge-shell-app-fetch"),
            AppCommand::ExportCurrentView => {
                let _ = self
                    .app
                    .handle_action_result(Self::action_result_for_export_not_wired());
                Cmd::none()
            }
            AppCommand::Batch(commands) => {
                let mut should_refresh = false;
                if self.apply_batch_commands(commands, &mut should_refresh) {
                    return Cmd::quit();
                }
                if should_refresh {
                    self.perform_refresh("forge-shell-app-batch-refresh")
                } else {
                    Cmd::none()
                }
            }
            AppCommand::RunAction(action) => {
                let follow_up = self.app.handle_action_result(self.execute_action(action));
                self.handle_app_command(follow_up)
            }
        }
    }
}

impl Model for ForgeShell {
    type Message = ForgeShellMsg;

    fn init(&mut self) -> Cmd<Self::Message> {
        self.perform_refresh("forge-shell-init-refresh")
    }

    fn update(&mut self, msg: Self::Message) -> Cmd<Self::Message> {
        match msg {
            ForgeShellMsg::Runtime(runtime_event) => {
                self.last_event = runtime_event;
                match runtime_event {
                    RuntimeEvent::Input(input) => {
                        self.last_action = translate_input(&input);
                        let command = self.app.update(input);
                        self.handle_app_command(command)
                    }
                    RuntimeEvent::Tick => self.perform_refresh("forge-shell-tick-refresh"),
                    RuntimeEvent::Quit => Cmd::quit(),
                    RuntimeEvent::Ignore => Cmd::none(),
                }
            }
            ForgeShellMsg::SnapshotLoaded(snapshot) => {
                self.apply_snapshot(snapshot);
                Cmd::none()
            }
        }
    }

    fn view(&self, frame: &mut Frame) {
        // This runtime is fully application-driven; never expose a text cursor.
        frame.set_cursor(None);
        frame.set_cursor_visible(false);

        let rendered = self.app.render();
        let max_rows = usize::from(frame.height()).min(rendered.size().height);
        let max_cols = usize::from(frame.width()).min(rendered.size().width);

        for row in 0..max_rows {
            for col in 0..max_cols {
                if let Some(cell) = rendered.cell(col, row) {
                    frame
                        .buffer
                        .set(col as u16, row as u16, render_frame_cell_to_ftui_cell(cell));
                }
            }
        }
    }

    fn subscriptions(&self) -> Vec<Box<dyn Subscription<Self::Message>>> {
        vec![Box::new(Every::new(
            Duration::from_millis(REFRESH_INTERVAL_MS),
            || ForgeShellMsg::Runtime(RuntimeEvent::Tick),
        ))]
    }
}

pub fn run(db_path: PathBuf) -> Result<(), String> {
    FtuiApp::new(ForgeShell::new(db_path))
        .screen_mode(resolve_screen_mode_from_env())
        .run()
        .map_err(|err| format!("run frankentui bootstrap runtime: {err}"))
}

#[must_use]
pub const fn resolve_screen_mode_from_env() -> ScreenMode {
    ScreenMode::AltScreen
}

#[must_use]
pub fn translate_runtime_event(event: &Event) -> RuntimeEvent {
    match event {
        Event::Tick => RuntimeEvent::Tick,
        Event::Resize { width, height } => RuntimeEvent::Input(InputEvent::Resize(ResizeEvent {
            width: usize::from(*width),
            height: usize::from(*height),
        })),
        Event::Mouse(mouse_event) => map_mouse_event(*mouse_event)
            .map(|mouse| RuntimeEvent::Input(InputEvent::Mouse(mouse)))
            .unwrap_or(RuntimeEvent::Ignore),
        Event::Key(key_event) => {
            if is_quit_key(*key_event) {
                return RuntimeEvent::Quit;
            }
            map_key_event(*key_event)
                .map(|key| RuntimeEvent::Input(InputEvent::Key(key)))
                .unwrap_or(RuntimeEvent::Ignore)
        }
        _ => RuntimeEvent::Ignore,
    }
}

fn is_quit_key(key_event: FtuiKeyEvent) -> bool {
    if !matches!(
        key_event.kind,
        FtuiKeyEventKind::Press | FtuiKeyEventKind::Repeat
    ) {
        return false;
    }

    if key_event.modifiers.contains(FtuiModifiers::CTRL)
        && matches!(key_event.code, FtuiKeyCode::Char('c'))
    {
        return true;
    }

    matches!(key_event.code, FtuiKeyCode::Char('q'))
}

fn map_key_event(key_event: FtuiKeyEvent) -> Option<KeyEvent> {
    if !matches!(
        key_event.kind,
        FtuiKeyEventKind::Press | FtuiKeyEventKind::Repeat
    ) {
        return None;
    }

    let mut modifiers = Modifiers {
        shift: key_event.modifiers.contains(FtuiModifiers::SHIFT),
        ctrl: key_event.modifiers.contains(FtuiModifiers::CTRL),
        alt: key_event.modifiers.contains(FtuiModifiers::ALT),
    };

    let key = match key_event.code {
        FtuiKeyCode::Char(ch) => Key::Char(ch),
        FtuiKeyCode::Enter => Key::Enter,
        FtuiKeyCode::Escape => Key::Escape,
        FtuiKeyCode::Tab => Key::Tab,
        FtuiKeyCode::BackTab => {
            modifiers.shift = true;
            Key::Tab
        }
        FtuiKeyCode::Backspace => Key::Backspace,
        FtuiKeyCode::Up => Key::Up,
        FtuiKeyCode::Down => Key::Down,
        FtuiKeyCode::Left => Key::Left,
        FtuiKeyCode::Right => Key::Right,
        _ => return None,
    };

    Some(KeyEvent { key, modifiers })
}

fn map_mouse_event(mouse_event: FtuiMouseEvent) -> Option<MouseEvent> {
    let kind = match mouse_event.kind {
        FtuiMouseEventKind::ScrollUp => MouseEventKind::Wheel(MouseWheelDirection::Up),
        FtuiMouseEventKind::ScrollDown => MouseEventKind::Wheel(MouseWheelDirection::Down),
        FtuiMouseEventKind::Down(button) => MouseEventKind::Down(map_mouse_button(button)?),
        FtuiMouseEventKind::Up(button) => MouseEventKind::Up(map_mouse_button(button)?),
        FtuiMouseEventKind::Drag(button) => MouseEventKind::Drag(map_mouse_button(button)?),
        FtuiMouseEventKind::Moved => MouseEventKind::Move,
        _ => return None,
    };
    Some(MouseEvent {
        kind,
        column: mouse_event.x as usize,
        row: mouse_event.y as usize,
    })
}

fn map_mouse_button(button: ftui::core::event::MouseButton) -> Option<MouseButton> {
    match button {
        ftui::core::event::MouseButton::Left => Some(MouseButton::Left),
        ftui::core::event::MouseButton::Right => Some(MouseButton::Right),
        ftui::core::event::MouseButton::Middle => Some(MouseButton::Middle),
    }
}

fn render_frame_cell_to_ftui_cell(cell: AdapterFrameCell) -> Cell {
    let mut flags = FtuiCellStyleFlags::empty();
    if cell.style.bold {
        flags.insert(FtuiCellStyleFlags::BOLD);
    }
    if cell.style.dim {
        flags.insert(FtuiCellStyleFlags::DIM);
    }
    if cell.style.underline {
        flags.insert(FtuiCellStyleFlags::UNDERLINE);
    }

    Cell::from_char(cell.glyph)
        .with_fg(term_color_to_packed_rgba(cell.style.fg))
        .with_bg(term_color_to_packed_rgba(cell.style.bg))
        .with_attrs(FtuiCellAttrs::new(flags, FtuiCellAttrs::LINK_ID_NONE))
}

fn perform_refresh(task_name: &'static str, db_path: PathBuf) -> Cmd<ForgeShellMsg> {
    Cmd::task_named(task_name, move || {
        ForgeShellMsg::SnapshotLoaded(load_snapshot(&db_path))
    })
}

fn load_snapshot(db_path: &Path) -> BootstrapSnapshot {
    match load_live_data(db_path) {
        Ok((loops, runs_by_loop, log_tails_by_loop)) => {
            BootstrapSnapshot::ok(loops, runs_by_loop, log_tails_by_loop)
        }
        Err(err) => BootstrapSnapshot::err(err),
    }
}

fn load_live_data(db_path: &Path) -> Result<LiveData, String> {
    if !db_path.exists() {
        return Ok((Vec::new(), HashMap::new(), HashMap::new()));
    }

    let db = forge_db::Db::open(forge_db::Config::new(db_path))
        .map_err(|err| format!("open database {}: {err}", db_path.display()))?;
    let loop_repo = forge_db::loop_repository::LoopRepository::new(&db);
    let queue_repo = forge_db::loop_queue_repository::LoopQueueRepository::new(&db);
    let run_repo = forge_db::loop_run_repository::LoopRunRepository::new(&db);
    let profile_repo = forge_db::profile_repository::ProfileRepository::new(&db);
    let pool_repo = forge_db::pool_repository::PoolRepository::new(&db);

    let loop_rows = match loop_repo.list() {
        Ok(rows) => rows,
        Err(err) if is_missing_table(&err, "loops") => {
            return Ok((Vec::new(), HashMap::new(), HashMap::new()));
        }
        Err(err) => return Err(err.to_string()),
    };

    let profile_map: HashMap<String, (String, String, String)> = match profile_repo.list() {
        Ok(rows) => rows
            .into_iter()
            .map(|profile| {
                (
                    profile.id,
                    (profile.name, profile.harness, profile.auth_kind),
                )
            })
            .collect(),
        Err(err) if is_missing_table(&err, "profiles") => HashMap::new(),
        Err(err) => return Err(err.to_string()),
    };

    let pool_map: HashMap<String, String> = match pool_repo.list() {
        Ok(rows) => rows.into_iter().map(|pool| (pool.id, pool.name)).collect(),
        Err(err) if is_missing_table(&err, "pools") => HashMap::new(),
        Err(err) => return Err(err.to_string()),
    };

    let mut loops = Vec::new();
    let mut run_history_by_loop: HashMap<String, Vec<RunView>> = HashMap::new();
    let mut log_tails_by_loop: HashMap<String, LogTailView> = HashMap::new();

    for loop_row in loop_rows {
        let queue_depth = match queue_repo.list(&loop_row.id) {
            Ok(items) => items.iter().filter(|item| item.status == "pending").count(),
            Err(err) if is_missing_table(&err, "loop_queue_items") => 0,
            Err(err) => return Err(err.to_string()),
        };

        let run_rows = match run_repo.list_by_loop(&loop_row.id) {
            Ok(items) => items,
            Err(err) if is_missing_table(&err, "loop_runs") => Vec::new(),
            Err(err) => return Err(err.to_string()),
        };

        let (profile_name, profile_harness, profile_auth) =
            match profile_map.get(&loop_row.profile_id) {
                Some((name, harness, auth)) => (name.clone(), harness.clone(), auth.clone()),
                None => (loop_row.profile_id.clone(), String::new(), String::new()),
            };
        let pool_name = if loop_row.pool_id.is_empty() {
            String::new()
        } else {
            pool_map
                .get(&loop_row.pool_id)
                .cloned()
                .unwrap_or(loop_row.pool_id.clone())
        };

        loops.push(LoopView {
            id: loop_row.id.clone(),
            short_id: loop_row.short_id.clone(),
            name: loop_row.name.clone(),
            state: loop_row.state.as_str().to_string(),
            repo_path: loop_row.repo_path.clone(),
            runs: run_rows.len(),
            queue_depth,
            last_run_at: loop_row.last_run_at.clone(),
            interval_seconds: loop_row.interval_seconds,
            max_runtime_seconds: loop_row.max_runtime_seconds,
            max_iterations: loop_row.max_iterations,
            last_error: loop_row.last_error.clone(),
            profile_name: profile_name.clone(),
            profile_harness: profile_harness.clone(),
            profile_auth: profile_auth.clone(),
            profile_id: loop_row.profile_id.clone(),
            pool_name,
            pool_id: loop_row.pool_id.clone(),
        });

        let run_history = run_rows
            .iter()
            .take(RUN_HISTORY_LIMIT)
            .map(|run_row| map_run_view(run_row, &profile_name, &loop_row.profile_id))
            .collect::<Vec<_>>();
        if !run_history.is_empty() {
            run_history_by_loop.insert(loop_row.id.clone(), run_history);
        }

        let fallback_log = run_rows
            .first()
            .map(|run_row| run_output_lines(&run_row.output_tail, LOG_TAIL_MAX_LINES))
            .unwrap_or_default();
        let log_tail = match load_log_tail(&loop_row.log_path, &fallback_log) {
            Ok(log_tail) => log_tail,
            Err(err) => LogTailView {
                lines: fallback_log,
                message: err,
            },
        };
        log_tails_by_loop.insert(loop_row.id.clone(), log_tail);
    }

    loops.sort_by(|left, right| {
        left.name
            .to_ascii_lowercase()
            .cmp(&right.name.to_ascii_lowercase())
            .then_with(|| left.id.cmp(&right.id))
    });

    Ok((loops, run_history_by_loop, log_tails_by_loop))
}

fn map_run_view(
    run: &forge_db::loop_run_repository::LoopRun,
    profile_name: &str,
    profile_id: &str,
) -> RunView {
    RunView {
        id: run.id.clone(),
        status: run_status_label(&run.status).to_owned(),
        exit_code: run.exit_code,
        duration: run_duration_label(run),
        profile_name: profile_name.to_owned(),
        profile_id: profile_id.to_owned(),
        harness: String::new(),
        auth_kind: String::new(),
        started_at: run.started_at.clone(),
        output_lines: run_output_lines(&run.output_tail, LOG_TAIL_MAX_LINES),
    }
}

fn run_status_label(status: &forge_db::loop_run_repository::LoopRunStatus) -> &'static str {
    match status {
        forge_db::loop_run_repository::LoopRunStatus::Running => "RUNNING",
        forge_db::loop_run_repository::LoopRunStatus::Success => "SUCCESS",
        forge_db::loop_run_repository::LoopRunStatus::Error => "ERROR",
        forge_db::loop_run_repository::LoopRunStatus::Killed => "KILLED",
    }
}

fn run_duration_label(run: &forge_db::loop_run_repository::LoopRun) -> String {
    if matches!(
        run.status,
        forge_db::loop_run_repository::LoopRunStatus::Running
    ) {
        "running".to_owned()
    } else if run.finished_at.is_some() {
        "done".to_owned()
    } else {
        "-".to_owned()
    }
}

fn run_output_lines(output_tail: &str, max_lines: usize) -> Vec<String> {
    let mut lines: Vec<String> = output_tail
        .lines()
        .map(str::trim_end)
        .filter(|line| !line.is_empty())
        .map(ToOwned::to_owned)
        .collect();
    if lines.len() > max_lines {
        lines = lines[lines.len() - max_lines..].to_vec();
    }
    lines
}

fn load_log_tail(log_path: &str, fallback_lines: &[String]) -> Result<LogTailView, String> {
    if log_path.trim().is_empty() {
        return Ok(LogTailView {
            lines: fallback_lines.to_vec(),
            message: "No loop log path configured".to_owned(),
        });
    }
    let path = PathBuf::from(log_path);
    if !path.exists() {
        return Ok(LogTailView {
            lines: fallback_lines.to_vec(),
            message: format!("Log file missing: {}", path.display()),
        });
    }

    let mut file = File::open(&path).map_err(|err| format!("open {}: {err}", path.display()))?;
    let metadata = file
        .metadata()
        .map_err(|err| format!("read metadata {}: {err}", path.display()))?;
    let len = metadata.len();
    let start = len.saturating_sub(LOG_TAIL_READ_BYTES);
    if start > 0 {
        file.seek(SeekFrom::Start(start))
            .map_err(|err| format!("seek {}: {err}", path.display()))?;
    }

    let mut raw = Vec::new();
    file.read_to_end(&mut raw)
        .map_err(|err| format!("read {}: {err}", path.display()))?;

    let text = String::from_utf8_lossy(&raw);
    let mut lines: Vec<String> = text.lines().map(str::to_owned).collect();
    if start > 0 && !lines.is_empty() {
        // Discard potentially partial first line from windowed read.
        lines.remove(0);
    }
    if lines.len() > LOG_TAIL_MAX_LINES {
        lines = lines[lines.len() - LOG_TAIL_MAX_LINES..].to_vec();
    }
    Ok(LogTailView {
        lines,
        message: format!("tailing {}", path.display()),
    })
}

fn is_missing_table(err: &forge_db::DbError, table: &str) -> bool {
    err.to_string().contains(&format!("no such table: {table}"))
}

fn unix_timestamp_secs() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|duration| duration.as_secs())
        .unwrap_or_default()
}

fn run_resume(args: &[String], backend: &mut resume::SqliteResumeBackend) -> (i32, String, String) {
    let mut stdout = Vec::new();
    let mut stderr = Vec::new();
    let exit_code = resume::run_with_backend(args, backend, &mut stdout, &mut stderr);
    (
        exit_code,
        String::from_utf8_lossy(&stdout).into_owned(),
        String::from_utf8_lossy(&stderr).into_owned(),
    )
}

fn run_stop(args: &[String], backend: &mut stop::SqliteStopBackend) -> (i32, String, String) {
    let mut stdout = Vec::new();
    let mut stderr = Vec::new();
    let exit_code = stop::run_with_backend(args, backend, &mut stdout, &mut stderr);
    (
        exit_code,
        String::from_utf8_lossy(&stdout).into_owned(),
        String::from_utf8_lossy(&stderr).into_owned(),
    )
}

fn run_kill(args: &[String], backend: &mut kill::SqliteKillBackend) -> (i32, String, String) {
    let mut stdout = Vec::new();
    let mut stderr = Vec::new();
    let exit_code = kill::run_with_backend(args, backend, &mut stdout, &mut stderr);
    (
        exit_code,
        String::from_utf8_lossy(&stdout).into_owned(),
        String::from_utf8_lossy(&stderr).into_owned(),
    )
}

fn run_rm(args: &[String], backend: &mut rm::SqliteLoopBackend) -> (i32, String, String) {
    let mut stdout = Vec::new();
    let mut stderr = Vec::new();
    let exit_code = rm::run_with_backend(args, backend, &mut stdout, &mut stderr);
    (
        exit_code,
        String::from_utf8_lossy(&stdout).into_owned(),
        String::from_utf8_lossy(&stderr).into_owned(),
    )
}

fn run_up(args: &[String], backend: &mut up::SqliteUpBackend) -> (i32, String, String) {
    let mut stdout = Vec::new();
    let mut stderr = Vec::new();
    let exit_code = up::run_with_backend(args, backend, &mut stdout, &mut stderr);
    (
        exit_code,
        String::from_utf8_lossy(&stdout).into_owned(),
        String::from_utf8_lossy(&stderr).into_owned(),
    )
}

fn build_up_args(wizard: &[(String, String)], spawn_owner: &str) -> Vec<String> {
    let mut args = vec!["up".to_owned(), "--json".to_owned()];
    if spawn_owner != "auto" {
        args.push("--spawn-owner".to_owned());
        args.push(spawn_owner.to_owned());
    }

    push_non_empty_pair(&mut args, "--name", wizard_value(wizard, "name"));
    push_non_empty_pair(
        &mut args,
        "--name-prefix",
        wizard_value(wizard, "name_prefix"),
    );
    push_non_empty_pair(&mut args, "--count", wizard_value(wizard, "count"));
    push_non_empty_pair(&mut args, "--pool", wizard_value(wizard, "pool"));
    push_non_empty_pair(&mut args, "--profile", wizard_value(wizard, "profile"));
    push_non_empty_pair(&mut args, "--prompt", wizard_value(wizard, "prompt"));
    push_non_empty_pair(
        &mut args,
        "--prompt-msg",
        wizard_value(wizard, "prompt_msg"),
    );
    push_non_empty_pair(&mut args, "--interval", wizard_value(wizard, "interval"));
    push_non_empty_pair(
        &mut args,
        "--max-runtime",
        wizard_value(wizard, "max_runtime"),
    );
    push_non_empty_pair(
        &mut args,
        "--max-iterations",
        wizard_value(wizard, "max_iterations"),
    );
    push_non_empty_pair(&mut args, "--tags", wizard_value(wizard, "tags"));
    args
}

fn wizard_value<'a>(wizard: &'a [(String, String)], key: &str) -> Option<&'a String> {
    wizard
        .iter()
        .find(|(entry_key, _)| entry_key == key)
        .map(|(_, value)| value)
}

fn push_non_empty_pair(args: &mut Vec<String>, flag: &str, value: Option<&String>) {
    let Some(raw) = value else {
        return;
    };
    let trimmed = raw.trim();
    if trimmed.is_empty() {
        return;
    }
    args.push(flag.to_owned());
    args.push(trimmed.to_owned());
}

fn parse_resume_success_message(stdout: &str, loop_id: &str) -> Option<String> {
    let value = serde_json::from_str::<serde_json::Value>(stdout).ok()?;
    let resumed = value.get("resumed")?.as_bool()?;
    if !resumed {
        return None;
    }
    let name = value
        .get("name")
        .and_then(serde_json::Value::as_str)
        .unwrap_or(loop_id);
    let id = value
        .get("loop_id")
        .and_then(serde_json::Value::as_str)
        .unwrap_or(loop_id);
    Some(format!("Loop {name} resumed ({id})"))
}

fn parse_created_loop_id(db_path: &Path, stdout: &str) -> Option<String> {
    let value = serde_json::from_str::<serde_json::Value>(stdout).ok()?;
    let entries = value.as_array()?;
    let first = entries.first()?;
    let name = first.get("name")?.as_str()?.to_owned();

    if !db_path.exists() {
        return None;
    }

    let db = forge_db::Db::open(forge_db::Config::new(db_path)).ok()?;
    let repo = forge_db::loop_repository::LoopRepository::new(&db);
    let mut loops = repo.list().ok()?;
    loops.sort_by(|left, right| right.created_at.cmp(&left.created_at));
    loops
        .into_iter()
        .find(|loop_entry| loop_entry.name == name)
        .map(|loop_entry| loop_entry.id)
}

fn create_success_message(stdout: &str) -> String {
    let Ok(value) = serde_json::from_str::<serde_json::Value>(stdout) else {
        return "Loop created".to_owned();
    };
    let Some(entries) = value.as_array() else {
        return "Loop created".to_owned();
    };
    match entries.len() {
        0 => "Loop created".to_owned(),
        1 => {
            let name = entries
                .first()
                .and_then(|entry| entry.get("name"))
                .and_then(serde_json::Value::as_str)
                .unwrap_or("loop");
            format!("Loop {name} created")
        }
        count => format!("Created {count} loops"),
    }
}

fn error_from_stderr(action: &str, stderr: &str) -> String {
    let message = stderr.trim();
    if message.is_empty() {
        return format!("{action} failed");
    }
    format!("{action} failed: {message}")
}

fn resolve_spawn_owner() -> String {
    std::env::var("FORGE_TUI_SPAWN_OWNER").unwrap_or_else(|_| "auto".to_owned())
}

#[cfg(test)]
mod tests {
    #![allow(clippy::expect_used)]

    use super::{
        build_up_args, resolve_screen_mode_from_env, translate_runtime_event, BootstrapSnapshot,
        ForgeShell, ForgeShellMsg, RuntimeEvent,
    };
    use crate::app::LoopView;
    use forge_ftui_adapter::input::{
        InputEvent, Key, KeyEvent, Modifiers, MouseEvent, MouseEventKind, MouseWheelDirection,
        ResizeEvent,
    };
    use forge_ftui_adapter::upstream_ftui as ftui;
    use ftui::core::event::{
        Event, KeyCode as FtuiKeyCode, KeyEvent as FtuiKeyEvent, KeyEventKind as FtuiKeyEventKind,
        MouseEvent as FtuiMouseEvent, MouseEventKind as FtuiMouseEventKind,
    };
    use ftui::render::cell::PackedRgba;
    use ftui::{Cmd, Model, ScreenMode};
    use std::collections::HashMap;
    use std::sync::{Mutex, MutexGuard, OnceLock};

    #[test]
    fn translate_runtime_event_maps_key_resize_mouse() {
        let key_event =
            Event::Key(FtuiKeyEvent::new(FtuiKeyCode::Up).with_kind(FtuiKeyEventKind::Press));
        assert_eq!(
            translate_runtime_event(&key_event),
            RuntimeEvent::Input(InputEvent::Key(KeyEvent::plain(Key::Up)))
        );

        let resize_event = Event::Resize {
            width: 120,
            height: 44,
        };
        assert_eq!(
            translate_runtime_event(&resize_event),
            RuntimeEvent::Input(InputEvent::Resize(ResizeEvent {
                width: 120,
                height: 44,
            }))
        );

        let mouse_event = Event::Mouse(FtuiMouseEvent::new(FtuiMouseEventKind::ScrollDown, 0, 0));
        assert_eq!(
            translate_runtime_event(&mouse_event),
            RuntimeEvent::Input(InputEvent::Mouse(MouseEvent {
                kind: MouseEventKind::Wheel(MouseWheelDirection::Down),
                column: 0,
                row: 0,
            }))
        );
    }

    #[test]
    fn translate_runtime_event_maps_quit_keys() {
        let key_event = Event::Key(FtuiKeyEvent::new(FtuiKeyCode::Char('q')));
        assert_eq!(translate_runtime_event(&key_event), RuntimeEvent::Quit);

        let ctrl_c_event = Event::Key(
            FtuiKeyEvent::new(FtuiKeyCode::Char('c')).with_modifiers(ftui::Modifiers::CTRL),
        );
        assert_eq!(translate_runtime_event(&ctrl_c_event), RuntimeEvent::Quit);
    }

    #[test]
    fn shell_tick_uses_async_task_command() {
        let mut shell = ForgeShell::new(std::env::temp_dir().join("forge-shell-bootstrap.sqlite"));
        let cmd = shell.update(ForgeShellMsg::Runtime(RuntimeEvent::Tick));
        assert!(matches!(cmd, Cmd::Task(..)));
    }

    #[test]
    fn shell_snapshot_completion_updates_state() {
        let mut shell = ForgeShell::new(std::env::temp_dir().join("forge-shell-bootstrap.sqlite"));
        let snapshot = BootstrapSnapshot {
            loops: vec![LoopView {
                id: "loop-1".to_owned(),
                short_id: "l001".to_owned(),
                name: "demo".to_owned(),
                state: "running".to_owned(),
                ..LoopView::default()
            }],
            run_history_by_loop: HashMap::new(),
            log_tails_by_loop: HashMap::new(),
            refreshed_at_epoch_secs: 123,
            error: Some("boom".to_owned()),
        };
        let cmd = shell.update(ForgeShellMsg::SnapshotLoaded(snapshot));

        assert!(matches!(cmd, Cmd::None));
        assert_eq!(shell.app.loops().len(), 1);
        assert_eq!(shell.refresh_count, 1);
        assert_eq!(shell.last_refreshed_at_epoch_secs, 123);
        assert_eq!(shell.last_error.as_deref(), Some("boom"));
    }

    #[test]
    fn shell_view_projects_styles_and_hides_cursor() {
        let mut shell = ForgeShell::new(std::env::temp_dir().join("forge-shell-bootstrap.sqlite"));
        let snapshot = BootstrapSnapshot {
            loops: vec![LoopView {
                id: "loop-1".to_owned(),
                short_id: "l001".to_owned(),
                name: "demo".to_owned(),
                state: "running".to_owned(),
                ..LoopView::default()
            }],
            run_history_by_loop: HashMap::new(),
            log_tails_by_loop: HashMap::new(),
            refreshed_at_epoch_secs: 123,
            error: None,
        };
        let _ = shell.update(ForgeShellMsg::SnapshotLoaded(snapshot));

        let mut pool = ftui::GraphemePool::new();
        let mut frame = ftui::Frame::new(100, 30, &mut pool);
        shell.view(&mut frame);

        assert!(!frame.cursor_visible);
        assert_eq!(frame.cursor_position, None);

        let mut has_background_color = false;
        'outer: for row in 0..frame.height() {
            for col in 0..frame.width() {
                let Some(cell) = frame.buffer.get(col, row) else {
                    continue;
                };
                if cell.bg != PackedRgba::TRANSPARENT {
                    has_background_color = true;
                    break 'outer;
                }
            }
        }
        assert!(
            has_background_color,
            "view bridge should project non-transparent background styling"
        );
    }

    #[test]
    fn from_event_uses_translator() {
        let msg = ForgeShellMsg::from(Event::Resize {
            width: 88,
            height: 22,
        });
        match msg {
            ForgeShellMsg::Runtime(RuntimeEvent::Input(InputEvent::Resize(ResizeEvent {
                width,
                height,
            }))) => {
                assert_eq!(width, 88);
                assert_eq!(height, 22);
            }
            other => panic!("unexpected message from resize event: {other:?}"),
        }
    }

    #[test]
    fn map_key_preserves_modifiers_for_supported_keys() {
        let input = translate_runtime_event(&Event::Key(
            FtuiKeyEvent::new(FtuiKeyCode::Char('r')).with_modifiers(ftui::Modifiers::CTRL),
        ));

        assert_eq!(
            input,
            RuntimeEvent::Input(InputEvent::Key(KeyEvent {
                key: Key::Char('r'),
                modifiers: Modifiers {
                    shift: false,
                    ctrl: true,
                    alt: false,
                },
            }))
        );
    }

    #[test]
    fn build_up_args_maps_wizard_values() {
        let wizard = vec![
            ("name".to_owned(), "demo-loop".to_owned()),
            ("count".to_owned(), "2".to_owned()),
            ("interval".to_owned(), "45s".to_owned()),
            ("tags".to_owned(), "alpha,beta".to_owned()),
            ("profile".to_owned(), "default".to_owned()),
        ];

        let args = build_up_args(&wizard, "daemon");
        assert_eq!(
            args,
            vec![
                "up".to_owned(),
                "--json".to_owned(),
                "--spawn-owner".to_owned(),
                "daemon".to_owned(),
                "--name".to_owned(),
                "demo-loop".to_owned(),
                "--count".to_owned(),
                "2".to_owned(),
                "--profile".to_owned(),
                "default".to_owned(),
                "--interval".to_owned(),
                "45s".to_owned(),
                "--tags".to_owned(),
                "alpha,beta".to_owned(),
            ]
        );
    }

    #[test]
    fn screen_mode_is_always_alt_screen() {
        let _lock = env_lock();
        let _guard = EnvGuard::set("FORGE_TUI_SCREEN_MODE", "inline");
        assert_eq!(resolve_screen_mode_from_env(), ScreenMode::AltScreen);
    }

    fn env_lock() -> MutexGuard<'static, ()> {
        static LOCK: OnceLock<Mutex<()>> = OnceLock::new();
        let lock = LOCK.get_or_init(|| Mutex::new(()));
        match lock.lock() {
            Ok(guard) => guard,
            Err(poisoned) => poisoned.into_inner(),
        }
    }

    struct EnvGuard {
        key: String,
        previous: Option<String>,
    }

    impl EnvGuard {
        fn set(key: &str, value: &str) -> Self {
            let previous = std::env::var(key).ok();
            std::env::set_var(key, value);
            Self {
                key: key.to_owned(),
                previous,
            }
        }
    }

    impl Drop for EnvGuard {
        fn drop(&mut self) {
            if let Some(previous) = self.previous.take() {
                std::env::set_var(&self.key, previous);
            } else {
                std::env::remove_var(&self.key);
            }
        }
    }
}
