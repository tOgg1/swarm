#![allow(clippy::unwrap_used)]

use forge_cli::{run_for_test, RootCommandOutput};
use rusqlite::params;
use std::path::PathBuf;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Mutex, MutexGuard, OnceLock};
use std::time::{SystemTime, UNIX_EPOCH};

// -- Help ----------------------------------------------------------------

#[test]
fn root_no_args_dispatches_to_tui() {
    let out = run(&[]);
    assert_eq!(out.exit_code, 1);
    assert!(out.stdout.is_empty());
    assert!(out.stderr.contains("TUI requires an interactive terminal"));
}

#[test]
fn root_help_flag() {
    let out = run(&["--help"]);
    assert_eq!(out.exit_code, 0);
    assert!(out.stderr.is_empty());
    assert!(out.stdout.contains("Control plane for AI coding agents"));
    assert!(out.stdout.contains("Commands:"));
}

#[test]
fn root_dash_h_flag() {
    let out = run(&["-h"]);
    assert_eq!(out.exit_code, 0);
    assert!(out.stdout.contains("Commands:"));
}

#[test]
fn root_help_subcommand() {
    let out = run(&["help"]);
    assert_eq!(out.exit_code, 0);
    assert!(out.stdout.contains("Commands:"));
}

#[test]
fn root_help_omits_dropped_legacy_command_groups() {
    let out = run(&["--help"]);
    assert_eq!(out.exit_code, 0);

    for command in ["accounts", "attach", "recipe", "vault", "workspace", "ws"] {
        let listed = format!("\n  {command} ");
        assert!(
            !out.stdout.contains(&listed),
            "legacy command unexpectedly listed in help: {command}"
        );
    }
}

// -- Version -------------------------------------------------------------

#[test]
fn root_version_flag() {
    let out = run(&["--version"]);
    assert_eq!(out.exit_code, 0);
    assert!(out.stdout.starts_with("forge version "));
    assert!(out.stderr.is_empty());
}

#[test]
fn root_version_contains_commit_info() {
    let out = run(&["--version"]);
    // Default version string includes (commit: ..., built: ...)
    assert!(out.stdout.contains("commit:"));
    assert!(out.stdout.contains("built:"));
}

// -- Unknown command: text mode ------------------------------------------

#[test]
fn unknown_command_text_error() {
    let out = run(&["nonexistent"]);
    assert_eq!(out.exit_code, 1);
    assert!(out.stdout.is_empty());
    assert!(out.stderr.contains("unknown forge command: nonexistent"));
    // Help text is also printed to stderr
    assert!(out.stderr.contains("Commands:"));
}

#[test]
fn dropped_legacy_commands_are_unknown() {
    for command in ["accounts", "attach", "recipe", "vault", "workspace", "ws"] {
        let out = run(&[command]);
        assert_eq!(
            out.exit_code, 1,
            "expected unknown command exit for {command}"
        );
        assert!(out.stdout.is_empty(), "unexpected stdout for {command}");
        assert!(
            out.stderr
                .contains(&format!("unknown forge command: {command}")),
            "expected unknown command error for {command}, got: {}",
            out.stderr
        );
    }
}

// -- Unknown command: JSON mode ------------------------------------------

#[test]
fn unknown_command_json_error_matches_golden() {
    let out = run(&["--json", "nonexistent"]);
    assert_eq!(out.exit_code, 1);
    assert!(out.stderr.is_empty());
    assert_eq!(
        out.stdout,
        include_str!("golden/root/unknown_command_error.json")
    );
}

#[test]
fn unknown_command_jsonl_error_matches_golden() {
    let out = run(&["--jsonl", "nonexistent"]);
    assert_eq!(out.exit_code, 1);
    assert!(out.stderr.is_empty());
    assert_eq!(
        out.stdout,
        include_str!("golden/root/unknown_command_error.jsonl")
    );
}

#[test]
fn unknown_command_json_no_help_on_stdout() {
    let out = run(&["--json", "nonexistent"]);
    // In JSON mode, help text should NOT be printed (neither stdout nor stderr)
    assert!(!out.stdout.contains("Commands:"));
    assert!(out.stderr.is_empty());
}

#[test]
fn root_runtime_dispatch_has_no_inmemory_backends() {
    let source = include_str!("../src/lib.rs");
    let runtime_source = source.split("#[cfg(test)]").next().unwrap_or(source);
    assert!(
        !runtime_source.contains("InMemory"),
        "root runtime dispatch contains InMemory backend wiring"
    );
}

// -- Global flag forwarding ----------------------------------------------

#[test]
fn global_verbose_quiet_before_help() {
    let out = run(&["--verbose", "--quiet", "--help"]);
    assert_eq!(out.exit_code, 0);
    assert!(out.stdout.contains("Commands:"));
}

#[test]
fn global_json_before_version() {
    // --version should take precedence even when --json is also present
    let out = run(&["--json", "--version"]);
    assert_eq!(out.exit_code, 0);
    assert!(out.stdout.starts_with("forge version "));
}

// -- Error envelope classification (integration) -------------------------

#[test]
fn json_error_envelope_ambiguous() {
    // Trigger an ambiguous-like error message through unknown command containing "ambiguous"
    // (The error classification works on message content, not on the actual cause.)
    let out = run(&["--json", "ambiguous-prefix"]);
    assert_eq!(out.exit_code, 1);
    let parsed: serde_json::Value = match serde_json::from_str(&out.stdout) {
        Ok(value) => value,
        Err(err) => panic!("expected valid json envelope: {err}"),
    };
    // "unknown forge command: ambiguous-prefix" contains "ambiguous"
    assert_eq!(parsed["error"]["code"], "ERR_AMBIGUOUS");
}

// -- Regression: up dispatch backend -------------------------------------

#[test]
fn up_command_dispatches_to_sqlite_backend() {
    let _lock = env_lock();
    let db_path = temp_db_path("root-up-dispatch");
    let _guard = EnvGuard::set("FORGE_DATABASE_PATH", &db_path);

    let migrate = run(&["migrate", "up"]);
    assert_eq!(migrate.exit_code, 0, "migrate failed: {}", migrate.stderr);

    let name = format!("dispatch-up-{}", unique_suffix());
    let up = run(&["up", "--name", &name, "--prompt-msg", "hello", "--json"]);
    assert_eq!(up.exit_code, 0, "up failed: {}", up.stderr);
    assert!(
        up.stdout.contains(&name),
        "unexpected up output: {}",
        up.stdout
    );

    let ps = run(&["ps", "--json"]);
    assert_eq!(ps.exit_code, 0, "ps failed: {}", ps.stderr);
    assert!(
        ps.stdout.contains(&name),
        "expected loop {name} in ps output, got: {}",
        ps.stdout
    );
}

#[test]
fn up_command_dispatches_with_global_data_dir_alias() {
    let _lock = env_lock();
    let data_dir = std::env::temp_dir().join(format!(
        "forge-cli-root-global-data-{}-{}",
        unique_suffix(),
        std::process::id()
    ));
    std::fs::create_dir_all(&data_dir)
        .unwrap_or_else(|err| panic!("create global data dir {}: {err}", data_dir.display()));

    let _g_db = EnvGuard::unset("FORGE_DATABASE_PATH");
    let _g_legacy_db = EnvGuard::unset("FORGE_DB_PATH");
    let _g_data = EnvGuard::unset("FORGE_DATA_DIR");
    let _g_global_data = EnvGuard::set("FORGE_GLOBAL_DATA_DIR", &data_dir);

    let migrate = run(&["migrate", "up"]);
    assert_eq!(migrate.exit_code, 0, "migrate failed: {}", migrate.stderr);

    let name = format!("dispatch-global-data-{}", unique_suffix());
    let up = run(&["up", "--name", &name, "--prompt-msg", "hello", "--json"]);
    assert_eq!(up.exit_code, 0, "up failed: {}", up.stderr);

    let ps = run(&["ps", "--json"]);
    assert_eq!(ps.exit_code, 0, "ps failed: {}", ps.stderr);
    assert!(
        ps.stdout.contains(&name),
        "expected loop {name} in ps output, got: {}",
        ps.stdout
    );

    let db_path = data_dir.join("forge.db");
    assert!(
        db_path.exists(),
        "expected sqlite db at {}, but file missing",
        db_path.display()
    );

    let _ = std::fs::remove_dir_all(&data_dir);
}

#[test]
fn kill_command_dispatches_to_sqlite_backend() {
    let _lock = env_lock();
    let db_path = temp_db_path("root-kill-dispatch");
    let _guard = EnvGuard::set("FORGE_DATABASE_PATH", &db_path);

    let migrate = run(&["migrate", "up"]);
    assert_eq!(migrate.exit_code, 0, "migrate failed: {}", migrate.stderr);

    let name = format!("dispatch-kill-{}", unique_suffix());
    let up = run(&["up", "--name", &name, "--prompt-msg", "hello"]);
    assert_eq!(up.exit_code, 0, "up failed: {}", up.stderr);

    let loop_id = loop_id_for_name(&name);
    let kill = run(&["kill", &loop_id]);
    assert_eq!(kill.exit_code, 0, "kill failed: {}", kill.stderr);
    assert!(
        kill.stdout.contains("Killed 1 loop"),
        "unexpected kill output: {}",
        kill.stdout
    );

    let state = loop_state_for_name(&name);
    assert_eq!(state, "stopped");
}

#[test]
fn loop_internal_command_dispatches_to_sqlite_backend() {
    let _lock = env_lock();
    let db_path = temp_db_path("root-loop-dispatch");
    let _guard = EnvGuard::set("FORGE_DATABASE_PATH", &db_path);

    let migrate = run(&["migrate", "up"]);
    assert_eq!(migrate.exit_code, 0, "migrate failed: {}", migrate.stderr);

    let profile_name = format!("dispatch-profile-{}", unique_suffix());
    let add_profile = run(&[
        "profile",
        "add",
        "codex",
        "--name",
        &profile_name,
        "--command",
        "bash -lc 'true'",
    ]);
    assert_eq!(
        add_profile.exit_code, 0,
        "profile add failed: {}",
        add_profile.stderr
    );

    let name = format!("dispatch-loop-{}", unique_suffix());
    let up = run(&[
        "up",
        "--name",
        &name,
        "--prompt-msg",
        "hello",
        "--max-iterations",
        "1",
        "--profile",
        &profile_name,
    ]);
    assert_eq!(up.exit_code, 0, "up failed: {}", up.stderr);

    let loop_id = loop_id_for_name(&name);
    let before_runs = loop_runs_for_name(&name);
    let loop_run = run(&["loop", "run", &loop_id]);
    assert_eq!(
        loop_run.exit_code, 0,
        "loop run failed: {}",
        loop_run.stderr
    );
    let after_runs = loop_runs_for_name(&name);
    assert_eq!(after_runs, before_runs + 1);
}

#[test]
fn export_command_dispatches_to_sqlite_backend() {
    let _lock = env_lock();
    let db_path = temp_db_path("root-export-dispatch");
    let _guard = EnvGuard::set("FORGE_DATABASE_PATH", &db_path);

    let migrate = run(&["migrate", "up"]);
    assert_eq!(migrate.exit_code, 0, "migrate failed: {}", migrate.stderr);

    seed_export_event(&db_path, "dispatch-export-event");

    let export = run(&["export", "events", "--json"]);
    assert_eq!(export.exit_code, 0, "export failed: {}", export.stderr);
    assert!(
        export.stdout.contains("dispatch-export-event"),
        "expected seeded event in export output, got: {}",
        export.stdout
    );
}

// -- Helper --------------------------------------------------------------

fn run(args: &[&str]) -> RootCommandOutput {
    run_for_test(args)
}

fn temp_db_path(tag: &str) -> PathBuf {
    static UNIQUE_SUFFIX: AtomicU64 = AtomicU64::new(0);
    let nanos = match SystemTime::now().duration_since(UNIX_EPOCH) {
        Ok(d) => d.as_nanos(),
        Err(_) => 0,
    };
    let suffix = UNIQUE_SUFFIX.fetch_add(1, Ordering::Relaxed);
    std::env::temp_dir().join(format!(
        "forge-cli-root-{tag}-{nanos}-{}-{suffix}.sqlite",
        std::process::id(),
    ))
}

fn unique_suffix() -> String {
    static UNIQUE_SUFFIX: AtomicU64 = AtomicU64::new(0);
    let value = UNIQUE_SUFFIX.fetch_add(1, Ordering::Relaxed);
    format!("{:06x}", value)
}

fn env_lock() -> MutexGuard<'static, ()> {
    static LOCK: OnceLock<Mutex<()>> = OnceLock::new();
    let lock = LOCK.get_or_init(|| Mutex::new(()));
    match lock.lock() {
        Ok(guard) => guard,
        Err(poisoned) => poisoned.into_inner(),
    }
}

fn loop_id_for_name(name: &str) -> String {
    let ps = run(&["ps", "--json"]);
    assert_eq!(ps.exit_code, 0, "ps failed: {}", ps.stderr);
    let loops: serde_json::Value =
        serde_json::from_str(&ps.stdout).unwrap_or_else(|err| panic!("parse ps json: {err}"));
    let items = loops
        .as_array()
        .unwrap_or_else(|| panic!("ps should return array: {}", ps.stdout));
    items
        .iter()
        .find_map(|item| {
            if item["name"].as_str() == Some(name) {
                item["id"].as_str().map(ToString::to_string)
            } else {
                None
            }
        })
        .unwrap_or_else(|| panic!("loop {name} not found in ps output: {}", ps.stdout))
}

fn loop_state_for_name(name: &str) -> String {
    let ps = run(&["ps", "--json"]);
    assert_eq!(ps.exit_code, 0, "ps failed: {}", ps.stderr);
    let loops: serde_json::Value =
        serde_json::from_str(&ps.stdout).unwrap_or_else(|err| panic!("parse ps json: {err}"));
    let items = loops
        .as_array()
        .unwrap_or_else(|| panic!("ps should return array: {}", ps.stdout));
    items
        .iter()
        .find_map(|item| {
            if item["name"].as_str() == Some(name) {
                item["state"].as_str().map(ToString::to_string)
            } else {
                None
            }
        })
        .unwrap_or_else(|| panic!("loop {name} not found in ps output: {}", ps.stdout))
}

fn loop_runs_for_name(name: &str) -> u64 {
    let ps = run(&["ps", "--json"]);
    assert_eq!(ps.exit_code, 0, "ps failed: {}", ps.stderr);
    let loops: serde_json::Value =
        serde_json::from_str(&ps.stdout).unwrap_or_else(|err| panic!("parse ps json: {err}"));
    let items = loops
        .as_array()
        .unwrap_or_else(|| panic!("ps should return array: {}", ps.stdout));
    items
        .iter()
        .find_map(|item| {
            if item["name"].as_str() == Some(name) {
                item["runs"].as_u64()
            } else {
                None
            }
        })
        .unwrap_or_else(|| panic!("loop {name} not found in ps output: {}", ps.stdout))
}

fn seed_export_event(db_path: &std::path::Path, event_id: &str) {
    let conn = rusqlite::Connection::open(db_path)
        .unwrap_or_else(|err| panic!("open sqlite db {}: {err}", db_path.display()));
    conn.execute(
        "INSERT INTO events (id, timestamp, type, entity_type, entity_id, payload_json, metadata_json)
         VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7)",
        params![
            event_id,
            "2026-02-10T00:00:00Z",
            "agent.state_changed",
            "agent",
            "agent-dispatch-test",
            "{\"state\":\"running\"}",
            "{\"source\":\"root-test\"}"
        ],
    )
    .unwrap_or_else(|err| panic!("seed event: {err}"));
}

struct EnvGuard {
    key: String,
    previous: Option<std::ffi::OsString>,
}

impl EnvGuard {
    fn set<K: Into<String>, P: AsRef<std::path::Path>>(key: K, value: P) -> Self {
        let key = key.into();
        let previous = std::env::var_os(&key);
        std::env::set_var(&key, value.as_ref());
        Self { key, previous }
    }

    fn unset<K: Into<String>>(key: K) -> Self {
        let key = key.into();
        let previous = std::env::var_os(&key);
        std::env::remove_var(&key);
        Self { key, previous }
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
