use std::collections::HashMap;
use std::io::IsTerminal;
use std::path::{Path, PathBuf};

use forge_tui::app::{App, LoopView};
use forge_tui::theme::detect_terminal_color_capability;

#[derive(Debug, Clone, Default)]
struct LiveLoopSnapshot {
    loops: Vec<LoopView>,
    total_queue_depth: usize,
    profile_count: usize,
    running: usize,
    sleeping: usize,
    waiting: usize,
    stopped: usize,
    errored: usize,
    teams: Vec<TeamSummaryView>,
    team_tasks: Vec<TeamTaskInboxView>,
    log_paths: HashMap<String, String>,
}

#[derive(Debug, Clone, Default)]
struct TeamSummaryView {
    id: String,
    name: String,
    members: usize,
    queued: usize,
    assigned: usize,
    running: usize,
    blocked: usize,
    open: usize,
}

#[derive(Debug, Clone, Default)]
struct TeamTaskInboxView {
    id: String,
    team_name: String,
    status: String,
    priority: i64,
    assigned_agent_id: String,
    title: String,
}

fn main() {
    let interactive = std::io::stdin().is_terminal() && std::io::stdout().is_terminal();
    if interactive {
        run_interactive();
    } else if ci_non_tty_snapshot_mode_enabled() {
        print!("{}", render_snapshot_text());
    } else {
        eprintln!(
            "error: non-interactive snapshot mode is CI-only; set CI=1 or run in a TTY for FrankenTUI runtime"
        );
        std::process::exit(2);
    }
}

fn run_interactive() {
    if runtime_legacy_requested() {
        eprintln!(
            "error: FORGE_TUI_RUNTIME=legacy is no longer supported; interactive mode always uses FrankenTUI bootstrap"
        );
        std::process::exit(2);
    }

    if let Err(err) = run_frankentui_bootstrap() {
        eprintln!("error: failed to run interactive runtime ({err})");
        std::process::exit(1);
    }
}

fn run_frankentui_bootstrap() -> Result<(), String> {
    #[cfg(feature = "frankentui-bootstrap")]
    {
        forge_tui::frankentui_bootstrap::run(resolve_database_path())
    }

    #[cfg(not(feature = "frankentui-bootstrap"))]
    {
        Err(
            "frankentui bootstrap requested via FORGE_TUI_RUNTIME but build is missing feature `frankentui-bootstrap`"
                .to_owned(),
        )
    }
}

fn render_snapshot_text() -> String {
    let lines = render_snapshot_lines();
    if lines.is_empty() {
        return String::new();
    }
    let mut output = lines.join("\n");
    output.push('\n');
    output
}

fn render_snapshot_lines() -> Vec<String> {
    let db_path = resolve_database_path();
    render_snapshot_lines_for_path(&db_path)
}

fn render_snapshot_lines_for_path(db_path: &Path) -> Vec<String> {
    let mut lines = Vec::new();
    let snapshot = match load_live_loop_snapshot(db_path) {
        Ok(snapshot) => snapshot,
        Err(err) => {
            lines.push(format!("error: load live loop snapshot: {err}"));
            return lines;
        }
    };

    let capability = detect_terminal_color_capability();
    let mut app = App::new_with_capability("default", capability, 200);
    app.set_loops(snapshot.loops.clone());
    lines.extend(app.render().snapshot().lines().map(str::to_owned));
    lines.push(String::new());
    lines.push("forge loop snapshot (rust)".to_string());
    lines.push(format!("db: {}", db_path.display()));
    lines.push(format!(
        "loops: {}  queue(pending): {}  profiles: {}",
        snapshot.loops.len(),
        snapshot.total_queue_depth,
        snapshot.profile_count
    ));
    lines.push(format!(
        "states: running={} sleeping={} waiting={} stopped={} error={}",
        snapshot.running, snapshot.sleeping, snapshot.waiting, snapshot.stopped, snapshot.errored
    ));
    lines.push(String::new());

    if snapshot.loops.is_empty() {
        lines.push("No loops found".to_string());
    } else {
        lines.push(format!(
            "{:<10} {:<9} {:>5} {:>6} {:<18} NAME",
            "ID", "STATE", "RUNS", "QUEUE", "PROFILE"
        ));
        for loop_view in snapshot.loops.iter().take(40) {
            let display_id = if loop_view.short_id.trim().is_empty() {
                trim(&loop_view.id, 10)
            } else {
                trim(&loop_view.short_id, 10)
            };
            let profile = if loop_view.profile_name.trim().is_empty() {
                "-".to_string()
            } else {
                trim(&loop_view.profile_name, 18)
            };
            lines.push(format!(
                "{:<10} {:<9} {:>5} {:>6} {:<18} {}",
                display_id,
                trim(&loop_view.state, 9),
                loop_view.runs,
                loop_view.queue_depth,
                profile,
                trim(&loop_view.name, 60)
            ));
        }
    }

    lines.push(String::new());
    lines.push("teams snapshot (read-only):".to_string());
    if snapshot.teams.is_empty() {
        lines.push("No teams found".to_string());
    } else {
        lines.push(format!(
            "{:<18} {:>7} {:>7} {:>8} {:>7} {:>7} {:>6}",
            "TEAM", "MEMBER", "OPEN", "QUEUED", "ASSIGN", "RUN", "BLOCK"
        ));
        for team in snapshot.teams.iter().take(40) {
            lines.push(format!(
                "{:<18} {:>7} {:>7} {:>8} {:>7} {:>7} {:>6}",
                trim(&team.name, 18),
                team.members,
                team.open,
                team.queued,
                team.assigned,
                team.running,
                team.blocked,
            ));
        }
    }

    lines.push(String::new());
    lines.push("team task inbox (read-only):".to_string());
    if snapshot.team_tasks.is_empty() {
        lines.push("No open team tasks".to_string());
    } else {
        lines.push(format!(
            "{:<10} {:<14} {:<9} {:>8} {:<14} TITLE",
            "TASK", "TEAM", "STATUS", "PRIORITY", "ASSIGNEE"
        ));
        for task in snapshot.team_tasks.iter().take(40) {
            let assignee = if task.assigned_agent_id.trim().is_empty() {
                "-".to_string()
            } else {
                trim(&task.assigned_agent_id, 14)
            };
            lines.push(format!(
                "{:<10} {:<14} {:<9} {:>8} {:<14} {}",
                trim(&task.id, 10),
                trim(&task.team_name, 14),
                trim(&task.status, 9),
                task.priority,
                assignee,
                trim(&task.title, 48)
            ));
        }
    }
    lines
}

fn load_live_loop_snapshot(db_path: &Path) -> Result<LiveLoopSnapshot, String> {
    if !db_path.exists() {
        return Ok(LiveLoopSnapshot::default());
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
        Err(err) if is_missing_table(&err, "loops") => return Ok(LiveLoopSnapshot::default()),
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

    let mut snapshot = LiveLoopSnapshot {
        profile_count: profile_map.len(),
        ..LiveLoopSnapshot::default()
    };

    for loop_row in loop_rows {
        let queue_depth = match queue_repo.list(&loop_row.id) {
            Ok(items) => items.iter().filter(|item| item.status == "pending").count(),
            Err(err) if is_missing_table(&err, "loop_queue_items") => 0,
            Err(err) => return Err(err.to_string()),
        };
        let runs = match run_repo.count_by_loop(&loop_row.id) {
            Ok(count) => usize::try_from(count).unwrap_or(usize::MAX),
            Err(err) if is_missing_table(&err, "loop_runs") => 0,
            Err(err) => return Err(err.to_string()),
        };

        snapshot.total_queue_depth = snapshot.total_queue_depth.saturating_add(queue_depth);
        match loop_row.state.as_str() {
            "running" => snapshot.running += 1,
            "sleeping" => snapshot.sleeping += 1,
            "waiting" => snapshot.waiting += 1,
            "stopped" => snapshot.stopped += 1,
            "error" => snapshot.errored += 1,
            _ => {}
        }

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

        if !loop_row.log_path.is_empty() {
            snapshot
                .log_paths
                .insert(loop_row.id.clone(), loop_row.log_path.clone());
        }

        snapshot.loops.push(LoopView {
            id: loop_row.id.clone(),
            short_id: loop_row.short_id,
            name: loop_row.name,
            state: loop_row.state.as_str().to_string(),
            repo_path: loop_row.repo_path,
            runs,
            queue_depth,
            last_run_at: loop_row.last_run_at,
            interval_seconds: loop_row.interval_seconds,
            max_runtime_seconds: loop_row.max_runtime_seconds,
            max_iterations: loop_row.max_iterations,
            last_error: loop_row.last_error,
            profile_name,
            profile_harness,
            profile_auth,
            profile_id: loop_row.profile_id,
            pool_name,
            pool_id: loop_row.pool_id,
        });
    }

    snapshot.loops.sort_by(|left, right| {
        left.name
            .to_ascii_lowercase()
            .cmp(&right.name.to_ascii_lowercase())
            .then_with(|| left.id.cmp(&right.id))
    });

    let team_repo = forge_db::team_repository::TeamRepository::new(&db);
    let team_task_repo = forge_db::team_task_repository::TeamTaskRepository::new(&db);
    let team_rows = match team_repo.list_teams() {
        Ok(rows) => rows,
        Err(err) if is_missing_table(&err, "teams") => Vec::new(),
        Err(err) => return Err(err.to_string()),
    };
    for team in team_rows {
        let members = match team_repo.list_members(&team.id) {
            Ok(items) => items,
            Err(err) if is_missing_table(&err, "team_members") => Vec::new(),
            Err(err) => return Err(err.to_string()),
        };
        let tasks = match team_task_repo.list(&forge_db::team_task_repository::TeamTaskFilter {
            team_id: team.id.clone(),
            statuses: vec![
                "queued".to_string(),
                "assigned".to_string(),
                "running".to_string(),
                "blocked".to_string(),
            ],
            assigned_agent_id: String::new(),
            limit: 10_000,
        }) {
            Ok(items) => items,
            Err(err) if is_missing_table(&err, "team_tasks") => Vec::new(),
            Err(err) => return Err(err.to_string()),
        };

        let mut summary = TeamSummaryView {
            id: team.id,
            name: team.name,
            members: members.len(),
            ..TeamSummaryView::default()
        };
        for task in tasks {
            match task.status.as_str() {
                "queued" => summary.queued += 1,
                "assigned" => summary.assigned += 1,
                "running" => summary.running += 1,
                "blocked" => summary.blocked += 1,
                _ => {}
            }
            snapshot.team_tasks.push(TeamTaskInboxView {
                id: task.id,
                team_name: summary.name.clone(),
                status: task.status,
                priority: task.priority,
                assigned_agent_id: task.assigned_agent_id,
                title: payload_title(&task.payload_json),
            });
        }
        summary.open = summary.queued + summary.assigned + summary.running + summary.blocked;
        snapshot.teams.push(summary);
    }
    snapshot.teams.sort_by(|left, right| {
        left.name
            .to_ascii_lowercase()
            .cmp(&right.name.to_ascii_lowercase())
            .then_with(|| left.id.cmp(&right.id))
    });
    snapshot.team_tasks.sort_by(|left, right| {
        left.priority
            .cmp(&right.priority)
            .then_with(|| left.team_name.cmp(&right.team_name))
            .then_with(|| left.id.cmp(&right.id))
    });

    Ok(snapshot)
}

fn payload_title(payload_json: &str) -> String {
    serde_json::from_str::<serde_json::Value>(payload_json)
        .ok()
        .and_then(|value| value.get("title").cloned())
        .and_then(|value| value.as_str().map(ToOwned::to_owned))
        .unwrap_or_default()
}

fn is_missing_table(err: &forge_db::DbError, table: &str) -> bool {
    err.to_string().contains(&format!("no such table: {table}"))
}

fn resolve_database_path() -> PathBuf {
    if let Some(path) = non_empty_env_path("FORGE_DATABASE_PATH") {
        return path;
    }
    if let Some(path) = non_empty_env_path("FORGE_DB_PATH") {
        return path;
    }
    for key in [
        "FORGE_DATA_DIR",
        "FORGE_GLOBAL_DATA_DIR",
        "SWARM_GLOBAL_DATA_DIR",
    ] {
        if let Some(path) = non_empty_env_path(key) {
            return path.join("forge.db");
        }
    }
    if let Some(home) = non_empty_env_path("HOME") {
        let mut path = home;
        path.push(".local");
        path.push("share");
        path.push("forge");
        path.push("forge.db");
        return path;
    }
    PathBuf::from(".forge-data/forge.db")
}

fn non_empty_env_path(key: &str) -> Option<PathBuf> {
    std::env::var_os(key).and_then(|raw| {
        if raw.is_empty() {
            None
        } else {
            Some(PathBuf::from(raw))
        }
    })
}

fn runtime_legacy_requested() -> bool {
    std::env::var("FORGE_TUI_RUNTIME")
        .map(|raw| {
            let normalized = raw.trim().to_ascii_lowercase();
            matches!(normalized.as_str(), "legacy" | "old")
        })
        .unwrap_or(false)
}

fn ci_non_tty_snapshot_mode_enabled() -> bool {
    env_truthy("CI")
}

fn env_truthy(key: &str) -> bool {
    std::env::var(key)
        .map(|raw| {
            let normalized = raw.trim().to_ascii_lowercase();
            matches!(normalized.as_str(), "1" | "true" | "yes" | "on")
        })
        .unwrap_or(false)
}

fn trim(value: &str, max: usize) -> String {
    if value.chars().count() <= max {
        return value.to_string();
    }
    if max <= 1 {
        return value.chars().take(max).collect();
    }
    let mut out: String = value.chars().take(max - 1).collect();
    out.push('~');
    out
}

#[cfg(test)]
mod tests {
    use std::ffi::OsString;
    use std::fmt::Display;
    use std::fs;
    use std::path::{Path, PathBuf};
    use std::sync::atomic::{AtomicU64, Ordering};
    use std::sync::{Mutex, MutexGuard, OnceLock};
    use std::time::{SystemTime, UNIX_EPOCH};

    use forge_db::loop_queue_repository::LoopQueueItem;
    use forge_db::loop_repository::{Loop, LoopRepository, LoopState};
    use forge_db::loop_run_repository::{LoopRun, LoopRunRepository};
    use forge_db::pool_repository::{Pool, PoolRepository};
    use forge_db::profile_repository::{Profile, ProfileRepository};
    use forge_db::team_repository::{TeamRole, TeamService};
    use forge_db::team_task_repository::TeamTaskService;

    use super::{
        ci_non_tty_snapshot_mode_enabled, load_live_loop_snapshot, render_snapshot_lines_for_path,
        resolve_database_path, runtime_legacy_requested,
    };

    fn ok_or_panic<T, E>(result: Result<T, E>, context: &str) -> T
    where
        E: Display,
    {
        match result {
            Ok(value) => value,
            Err(err) => panic!("{context}: {err}"),
        }
    }

    #[test]
    fn live_snapshot_includes_loop_queue_and_profile_fields() {
        let path = temp_db_path("snapshot-shape");
        let mut db = ok_or_panic(forge_db::Db::open(forge_db::Config::new(&path)), "open db");
        ok_or_panic(db.migrate_up(), "migrate db");

        let profile_repo = ProfileRepository::new(&db);
        let mut profile = Profile {
            name: "dev".to_string(),
            harness: "codex".to_string(),
            auth_kind: "oauth".to_string(),
            command_template: "codex run".to_string(),
            ..Default::default()
        };
        ok_or_panic(profile_repo.create(&mut profile), "create profile");

        let pool_repo = PoolRepository::new(&db);
        let mut pool = Pool {
            name: "default".to_string(),
            ..Default::default()
        };
        ok_or_panic(pool_repo.create(&mut pool), "create pool");

        let loop_repo = LoopRepository::new(&db);
        let mut loop_entry = Loop {
            name: "alpha-loop".to_string(),
            repo_path: "/tmp/alpha".to_string(),
            profile_id: profile.id.clone(),
            pool_id: pool.id.clone(),
            state: LoopState::Running,
            ..Default::default()
        };
        ok_or_panic(loop_repo.create(&mut loop_entry), "create loop");

        let queue_repo = forge_db::loop_queue_repository::LoopQueueRepository::new(&db);
        let mut queue_items = vec![LoopQueueItem {
            item_type: "message_append".to_string(),
            payload: "{\"text\":\"ship\"}".to_string(),
            ..Default::default()
        }];
        ok_or_panic(
            queue_repo.enqueue(&loop_entry.id, &mut queue_items),
            "enqueue queue item",
        );

        let run_repo = LoopRunRepository::new(&db);
        let mut run = LoopRun {
            loop_id: loop_entry.id.clone(),
            profile_id: profile.id.clone(),
            ..Default::default()
        };
        ok_or_panic(run_repo.create(&mut run), "create loop run");

        let snapshot = ok_or_panic(load_live_loop_snapshot(&path), "load live snapshot");
        assert_eq!(snapshot.loops.len(), 1);
        assert_eq!(snapshot.total_queue_depth, 1);
        assert_eq!(snapshot.profile_count, 1);
        assert_eq!(snapshot.running, 1);

        let view = &snapshot.loops[0];
        assert_eq!(view.name, "alpha-loop");
        assert_eq!(view.queue_depth, 1);
        assert_eq!(view.runs, 1);
        assert_eq!(view.profile_name, "dev");
        assert_eq!(view.profile_harness, "codex");
        assert_eq!(view.profile_auth, "oauth");
        assert_eq!(view.pool_name, "default");

        cleanup_temp_dir(&path);
    }

    #[test]
    fn live_snapshot_refreshes_queue_depth_after_enqueue() {
        let path = temp_db_path("refresh");
        let mut db = ok_or_panic(forge_db::Db::open(forge_db::Config::new(&path)), "open db");
        ok_or_panic(db.migrate_up(), "migrate db");

        let loop_repo = LoopRepository::new(&db);
        let mut loop_entry = Loop {
            name: "beta-loop".to_string(),
            repo_path: "/tmp/beta".to_string(),
            state: LoopState::Stopped,
            ..Default::default()
        };
        ok_or_panic(loop_repo.create(&mut loop_entry), "create loop");

        let before = ok_or_panic(load_live_loop_snapshot(&path), "load before enqueue");
        assert_eq!(before.loops.len(), 1);
        assert_eq!(before.loops[0].queue_depth, 0);

        let queue_repo = forge_db::loop_queue_repository::LoopQueueRepository::new(&db);
        let mut queue_items = vec![LoopQueueItem {
            item_type: "message_append".to_string(),
            payload: "{\"text\":\"hello\"}".to_string(),
            ..Default::default()
        }];
        ok_or_panic(
            queue_repo.enqueue(&loop_entry.id, &mut queue_items),
            "enqueue queue item",
        );

        let after = ok_or_panic(load_live_loop_snapshot(&path), "load after enqueue");
        assert_eq!(after.loops[0].queue_depth, 1);

        cleanup_temp_dir(&path);
    }

    #[test]
    fn snapshot_renders_team_summary_and_task_inbox_sections() {
        let path = temp_db_path("teams-inbox");
        let mut db = ok_or_panic(forge_db::Db::open(forge_db::Config::new(&path)), "open db");
        ok_or_panic(db.migrate_up(), "migrate db");

        let team_service = TeamService::new(&db);
        let team = ok_or_panic(
            team_service.create_team("ops", "{}", "", 300),
            "create team",
        );
        ok_or_panic(
            team_service.add_member(&team.id, "agent-lead", TeamRole::Leader),
            "add team leader",
        );

        let task_service = TeamTaskService::new(&db);
        ok_or_panic(
            task_service.submit(
                &team.id,
                r#"{"type":"incident","title":"database outage"}"#,
                5,
            ),
            "submit queued task",
        );
        let assigned = ok_or_panic(
            task_service.submit(&team.id, r#"{"type":"incident","title":"auth timeout"}"#, 8),
            "submit assigned task",
        );
        ok_or_panic(
            task_service.assign(&assigned.id, "agent-a", Some("agent-a")),
            "assign task",
        );

        let snapshot = ok_or_panic(load_live_loop_snapshot(&path), "load snapshot");
        assert_eq!(snapshot.teams.len(), 1);
        assert_eq!(snapshot.teams[0].name, "ops");
        assert_eq!(snapshot.teams[0].members, 1);
        assert_eq!(snapshot.teams[0].open, 2);
        assert_eq!(snapshot.team_tasks.len(), 2);

        let lines = render_snapshot_lines_for_path(&path);
        let rendered = lines.join("\n");
        assert!(rendered.contains("teams snapshot (read-only):"));
        assert!(rendered.contains("ops"));
        assert!(rendered.contains("team task inbox (read-only):"));
        assert!(rendered.contains("database outage"));
        assert!(rendered.contains("auth timeout"));

        cleanup_temp_dir(&path);
    }

    #[test]
    fn ci_non_tty_snapshot_mode_is_disabled_without_ci() {
        let _guard = env_lock();
        let _reset = EnvGuard::unset("CI");
        assert!(!ci_non_tty_snapshot_mode_enabled());
    }

    #[test]
    fn ci_non_tty_snapshot_mode_accepts_truthy_ci_values() {
        let _guard = env_lock();
        let _set = EnvGuard::set("CI", "1");
        assert!(ci_non_tty_snapshot_mode_enabled());

        std::env::set_var("CI", "true");
        assert!(ci_non_tty_snapshot_mode_enabled());

        std::env::set_var("CI", "YES");
        assert!(ci_non_tty_snapshot_mode_enabled());
    }

    #[test]
    fn ci_non_tty_snapshot_mode_rejects_falsey_ci_values() {
        let _guard = env_lock();
        let _set = EnvGuard::set("CI", "0");
        assert!(!ci_non_tty_snapshot_mode_enabled());

        std::env::set_var("CI", "no");
        assert!(!ci_non_tty_snapshot_mode_enabled());
    }

    #[test]
    fn runtime_legacy_requested_detects_legacy_values() {
        let _guard = env_lock();
        let _set_runtime = EnvGuard::set("FORGE_TUI_RUNTIME", "legacy");
        assert!(runtime_legacy_requested());

        std::env::set_var("FORGE_TUI_RUNTIME", "old");
        assert!(runtime_legacy_requested());
    }

    #[test]
    fn runtime_legacy_requested_rejects_non_legacy_values() {
        let _guard = env_lock();
        let _unset_runtime = EnvGuard::unset("FORGE_TUI_RUNTIME");
        assert!(!runtime_legacy_requested());

        std::env::set_var("FORGE_TUI_RUNTIME", "frankentui");
        assert!(!runtime_legacy_requested());

        std::env::set_var("FORGE_TUI_RUNTIME", "bootstrap");
        assert!(!runtime_legacy_requested());
    }

    #[test]
    fn resolve_database_path_uses_global_data_dir_alias_when_db_env_is_unset() {
        let _lock = env_lock();
        let _g_db = EnvGuard::unset("FORGE_DATABASE_PATH");
        let _g_legacy_db = EnvGuard::unset("FORGE_DB_PATH");
        let _g_data = EnvGuard::set("FORGE_GLOBAL_DATA_DIR", "/tmp/forge-tui-global");

        assert_eq!(
            resolve_database_path(),
            PathBuf::from("/tmp/forge-tui-global/forge.db")
        );
    }

    fn temp_db_path(tag: &str) -> PathBuf {
        static COUNTER: AtomicU64 = AtomicU64::new(0);
        let nanos = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .map(|duration| duration.as_nanos())
            .unwrap_or_default();
        let seq = COUNTER.fetch_add(1, Ordering::Relaxed);
        let pid = std::process::id();
        std::env::temp_dir().join(format!(
            "forge-tui-live-snapshot-{tag}-{pid}-{nanos}-{seq}.sqlite"
        ))
    }

    fn cleanup_temp_dir(path: &Path) {
        let _ = fs::remove_file(path);
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
        previous: Option<OsString>,
    }

    impl EnvGuard {
        fn set(key: &str, value: &str) -> Self {
            let previous = std::env::var_os(key);
            std::env::set_var(key, value);
            Self {
                key: key.to_string(),
                previous,
            }
        }

        fn unset(key: &str) -> Self {
            let previous = std::env::var_os(key);
            std::env::remove_var(key);
            Self {
                key: key.to_string(),
                previous,
            }
        }
    }

    impl Drop for EnvGuard {
        fn drop(&mut self) {
            if let Some(value) = self.previous.take() {
                std::env::set_var(&self.key, value);
            } else {
                std::env::remove_var(&self.key);
            }
        }
    }
}
