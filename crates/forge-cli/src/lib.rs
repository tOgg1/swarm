use std::env;
use std::io::Write;
use std::sync::OnceLock;

pub mod agent;
pub mod audit;
pub mod clean;
mod command_renderer;
pub mod completion;
pub mod config;
pub mod context;
pub mod delegation;
mod diff_renderer;
pub mod doctor;
pub mod error_envelope;
mod error_renderer;
pub mod explain;
pub mod export;
pub mod external_adapter;
pub mod highlight_spec;
pub mod hook;
pub mod init;
pub mod inject;
pub mod job;
pub mod kill;
pub mod lock;
pub mod logs;
pub mod loop_internal;
pub mod mail;
pub mod markdown_lexer;
pub mod mem;
pub mod mesh;
pub mod migrate;
pub mod msg;
pub mod node;
pub mod pool;
pub mod profile;
mod profile_catalog;
pub mod prompt;
mod prompt_resolution;
pub mod ps;
pub mod queue;
pub mod registry;
pub mod resume;
pub mod rm;
pub mod run;
mod run_exec;
mod runtime_paths;
pub mod scale;
pub mod section_parser;
pub mod send;
pub mod seq;
pub mod skills;
mod spawn_loop;
pub mod status;
pub mod stop;
mod structured_data_renderer;
pub mod task;
pub mod team;
pub mod team_heartbeat_watchdog;
pub mod template;
pub mod trigger;
pub mod tui;
pub mod up;
pub mod wait;
pub mod webhook_auth;
pub mod webhook_server;
pub mod work;
pub mod workflow;

use error_envelope::{handle_cli_error, parse_global_flags, GlobalFlags};

/// Version information set at build time.
static VERSION_STRING: OnceLock<String> = OnceLock::new();

pub fn crate_label() -> &'static str {
    "forge-cli"
}

/// Set the version string for `--version` output.
/// Must be called before `run_from_env`. Format: `"<version> (commit: <hash>, built: <date>)"`.
pub fn set_version(version: &str, commit: &str, date: &str) {
    let formatted = format!("{version} (commit: {commit}, built: {date})");
    let _ = VERSION_STRING.set(formatted);
}

fn get_version() -> &'static str {
    VERSION_STRING
        .get()
        .map(|value| value.as_str())
        .unwrap_or("dev (commit: none, built: unknown)")
}

pub fn run_from_env() -> i32 {
    let args: Vec<String> = env::args().skip(1).collect();
    let mut stdout = std::io::stdout();
    let mut stderr = std::io::stderr();
    run_with_args(&args, &mut stdout, &mut stderr)
}

pub fn run_with_args(args: &[String], stdout: &mut dyn Write, stderr: &mut dyn Write) -> i32 {
    let (flags, index) = parse_global_flags(args);

    if flags.version {
        let _ = writeln!(stdout, "forge version {}", get_version());
        return 0;
    }

    if let Err(err) = apply_chdir_if_requested(&flags) {
        return handle_cli_error(&err, &flags, stdout, stderr);
    }

    let remaining = &args[index..];
    let command = remaining.first().map(|arg| arg.as_str());
    match command {
        None => {
            if flags.robot_help {
                if let Err(err) = write_root_help(stdout) {
                    let _ = writeln!(stderr, "{err}");
                    return 1;
                }
                return 0;
            }
            let forwarded = forward_tui_args(&flags);
            tui::run_from_env(&forwarded, stdout, stderr)
        }
        Some("help") | Some("-h") | Some("--help") => {
            if let Err(err) = write_root_help(stdout) {
                let _ = writeln!(stderr, "{err}");
                return 1;
            }
            0
        }
        Some("agent") => {
            let backend = agent::ForgedAgentBackend::open_from_env();
            let forwarded = forward_args(remaining, &flags);
            agent::run_with_backend(&forwarded, &backend, stdout, stderr)
        }
        Some("hook") => {
            let backend = hook::FilesystemHookBackend;
            let forwarded = forward_args(remaining, &flags);
            hook::run_with_backend(&forwarded, &backend, stdout, stderr)
        }
        Some("inject") => {
            let mut backend = inject::SqliteInjectBackend::open_from_env();
            let forwarded = forward_args(remaining, &flags);
            inject::run_with_backend(&forwarded, &mut backend, stdout, stderr)
        }
        Some("job") => {
            let store = job::JobStore::open_from_env();
            let forwarded = forward_args(remaining, &flags);
            job::run_with_store(&forwarded, &store, stdout, stderr)
        }
        Some("trigger") => {
            let store = job::JobStore::open_from_env();
            let forwarded = forward_args(remaining, &flags);
            trigger::run_with_store(&forwarded, &store, stdout, stderr)
        }
        Some("init") => {
            let backend = init::FilesystemInitBackend;
            let forwarded = forward_args(remaining, &flags);
            init::run_with_backend(&forwarded, &backend, stdout, stderr)
        }
        Some("audit") => {
            let backend = audit::SqliteAuditBackend::open_from_env();
            let forwarded = forward_args(remaining, &flags);
            audit::run_with_backend(&forwarded, &backend, stdout, stderr)
        }
        Some("kill") => {
            let mut backend = kill::SqliteKillBackend::open_from_env();
            let forwarded = forward_args(remaining, &flags);
            kill::run_with_backend(&forwarded, &mut backend, stdout, stderr)
        }
        Some("lock") => {
            let backend = lock::FilesystemLockBackend::default();
            let forwarded = forward_args(remaining, &flags);
            lock::run_with_backend(&forwarded, &backend, stdout, stderr)
        }
        Some("loop") => {
            let mut backend = run::SqliteRunBackend::open_from_env();
            let forwarded = remaining.to_vec();
            loop_internal::run_with_backend(&forwarded, &mut backend, stdout, stderr)
        }
        Some("logs") | Some("log") => {
            let mut backend = logs::SqliteLogsBackend::open_from_env();
            let forwarded = remaining.to_vec();
            logs::run_with_backend(&forwarded, &mut backend, stdout, stderr)
        }
        Some("clean") => {
            let mut backend = clean::SqliteCleanBackend::open_from_env();
            let forwarded = forward_args(remaining, &flags);
            clean::run_with_backend(&forwarded, &mut backend, stdout, stderr)
        }
        Some("completion") => completion::run(remaining, stdout, stderr),
        Some("context") => {
            let backend = context::FilesystemContextBackend::default();
            let forwarded = forward_args(remaining, &flags);
            context::run_context(&forwarded, &backend, stdout, stderr)
        }
        Some("use") => {
            let backend = context::FilesystemContextBackend::default();
            let forwarded = forward_args(remaining, &flags);
            context::run_use(&forwarded, &backend, stdout, stderr)
        }
        Some("config") => {
            let backend = config::FilesystemConfigBackend;
            let forwarded = forward_args(remaining, &flags);
            config::run_with_backend(&forwarded, &backend, stdout, stderr)
        }
        Some("delegation") => {
            let backend = delegation::SqliteDelegationBackend::open_from_env();
            let forwarded = forward_args(remaining, &flags);
            delegation::run_with_backend(&forwarded, &backend, stdout, stderr)
        }
        Some("doctor") => {
            let backend = doctor::FilesystemDoctorBackend::default();
            let forwarded = forward_args(remaining, &flags);
            doctor::run_with_backend(&forwarded, &backend, stdout, stderr)
        }
        Some("explain") => {
            let backend = explain::SqliteExplainBackend::open_from_env();
            let forwarded = forward_args(remaining, &flags);
            explain::run_with_backend(&forwarded, &backend, stdout, stderr)
        }
        Some("export") => {
            let backend = export::SqliteExportBackend::open_from_env();
            let forwarded = forward_args(remaining, &flags);
            export::run_with_backend(&forwarded, &backend, stdout, stderr)
        }
        Some("migrate") => {
            let forwarded = forward_args(remaining, &flags);
            let mut backend = match migrate::SqliteMigrationBackend::open_from_env() {
                Ok(backend) => backend,
                Err(message) => {
                    return handle_cli_error(&message, &flags, stdout, stderr);
                }
            };
            migrate::run_with_backend(&forwarded, &mut backend, stdout, stderr)
        }
        Some("work") => {
            let mut backend = work::SqliteWorkBackend::open_from_env();
            let forwarded = forward_args(remaining, &flags);
            work::run_with_backend(&forwarded, &mut backend, stdout, stderr)
        }
        Some("prompt") => {
            let mut backend = prompt::FilesystemPromptBackend;
            let forwarded = forward_args(remaining, &flags);
            prompt::run_with_backend(&forwarded, &mut backend, stdout, stderr)
        }
        Some("queue") => {
            let mut backend = queue::SqliteQueueBackend::open_from_env();
            let forwarded = forward_args(remaining, &flags);
            queue::run_with_backend(&forwarded, &mut backend, stdout, stderr)
        }
        Some("registry") => {
            let forwarded = forward_args(remaining, &flags);
            registry::run_with_store(&forwarded, stdout, stderr)
        }
        Some("mail") => {
            let backend = mail::SqliteMailBackend::open_from_env();
            let forwarded = forward_args(remaining, &flags);
            mail::run_with_backend(&forwarded, &backend, stdout, stderr)
        }
        Some("mem") => {
            let mut backend = mem::SqliteMemBackend::open_from_env();
            let forwarded = forward_args(remaining, &flags);
            mem::run_with_backend(&forwarded, &mut backend, stdout, stderr)
        }
        Some("mesh") => {
            let store = mesh::MeshStore::open_from_env();
            let forwarded = forward_args(remaining, &flags);
            mesh::run_with_store(&forwarded, &store, stdout, stderr)
        }
        Some("node") => {
            let mut backend = node::ShellNodeBackend::open_from_env();
            let forwarded = forward_args(remaining, &flags);
            node::run_with_backend(&forwarded, &mut backend, stdout, stderr)
        }
        Some("msg") => {
            let mut backend = msg::SqliteMsgBackend::open_from_env();
            let forwarded = forward_args(remaining, &flags);
            msg::run_with_backend(&forwarded, &mut backend, stdout, stderr)
        }
        Some("pool") => {
            let mut backend = pool::SqlitePoolBackend::open_from_env();
            let forwarded = forward_args(remaining, &flags);
            pool::run_with_backend(&forwarded, &mut backend, stdout, stderr)
        }
        Some("profile") => {
            let mut backend = profile::SqliteProfileBackend::open_from_env();
            let forwarded = forward_args(remaining, &flags);
            profile::run_with_backend(&forwarded, &mut backend, stdout, stderr)
        }
        Some("resume") => {
            let mut backend = resume::SqliteResumeBackend::open_from_env();
            let forwarded = forward_loop_spawn_args(remaining, &flags);
            resume::run_with_backend(&forwarded, &mut backend, stdout, stderr)
        }
        Some("rm") => {
            let mut backend = rm::SqliteLoopBackend::open_from_env();
            let forwarded = forward_args(remaining, &flags);
            rm::run_with_backend(&forwarded, &mut backend, stdout, stderr)
        }
        Some("run") => {
            let mut backend = run::SqliteRunBackend::open_from_env();
            let forwarded = remaining.to_vec();
            run::run_with_backend(&forwarded, &mut backend, stdout, stderr)
        }
        Some("scale") => {
            let mut backend = scale::SqliteScaleBackend::open_from_env();
            let forwarded = forward_loop_spawn_args(remaining, &flags);
            scale::run_with_backend(&forwarded, &mut backend, stdout, stderr)
        }
        Some("ps") | Some("ls") => {
            let backend = ps::SqlitePsBackend::open_from_env();
            let forwarded = forward_args(remaining, &flags);
            ps::run_with_backend(&forwarded, &backend, stdout, stderr)
        }
        Some("send") => {
            let mut backend = send::SqliteSendBackend::open_from_env();
            let forwarded = forward_args(remaining, &flags);
            send::run_with_backend(&forwarded, &mut backend, stdout, stderr)
        }
        Some("seq") | Some("sequence") => {
            let mut backend = seq::FilesystemSeqBackend::open_from_env();
            let forwarded = forward_args(remaining, &flags);
            seq::run_with_backend(&forwarded, &mut backend, stdout, stderr)
        }
        Some("skills") => {
            let backend = skills::FilesystemSkillsBackend;
            let forwarded = forward_args(remaining, &flags);
            skills::run_with_backend(&forwarded, &backend, stdout, stderr)
        }
        Some("status") => {
            let backend = status::SqliteStatusBackend::open_from_env();
            let forwarded = forward_args(remaining, &flags);
            status::run_with_backend(&forwarded, &backend, stdout, stderr)
        }
        Some("task") => {
            let backend = task::SqliteTaskBackend::open_from_env();
            let forwarded = forward_args(remaining, &flags);
            task::run_with_backend(&forwarded, &backend, stdout, stderr)
        }
        Some("team") => {
            let backend = team::SqliteTeamBackend::open_from_env();
            let forwarded = forward_args(remaining, &flags);
            team::run_with_backend(&forwarded, &backend, stdout, stderr)
        }
        Some("template") | Some("tmpl") => {
            let backend = template::FilesystemTemplateBackend::open_from_env();
            let forwarded = forward_args(remaining, &flags);
            template::run_with_backend(&forwarded, &backend, stdout, stderr)
        }
        Some("tui") | Some("ui") => {
            let forwarded = forward_args(remaining, &flags);
            tui::run_from_env(&forwarded, stdout, stderr)
        }
        Some("stop") => {
            let mut backend = stop::SqliteStopBackend::open_from_env();
            let forwarded = forward_args(remaining, &flags);
            stop::run_with_backend(&forwarded, &mut backend, stdout, stderr)
        }
        Some("up") => {
            let mut backend = up::SqliteUpBackend::open_from_env();
            let forwarded = forward_loop_spawn_args(remaining, &flags);
            up::run_with_backend(&forwarded, &mut backend, stdout, stderr)
        }
        Some("wait") => {
            let backend = wait::SqliteWaitBackend::open_from_env();
            let forwarded = forward_args(remaining, &flags);
            wait::run_with_backend(&forwarded, &backend, stdout, stderr)
        }
        Some("workflow") | Some("wf") => {
            let backend = workflow::FilesystemWorkflowBackend::open_from_env();
            let forwarded = forward_args(remaining, &flags);
            workflow::run_with_backend(&forwarded, &backend, stdout, stderr)
        }
        Some(other) => {
            let message = format!("unknown forge command: {other}");
            let code = handle_cli_error(&message, &flags, stdout, stderr);
            if !flags.json && !flags.jsonl {
                let _ = write_root_help(stderr);
            }
            code
        }
    }
}

fn apply_chdir_if_requested(flags: &GlobalFlags) -> Result<(), String> {
    let target = flags.chdir.trim();
    if target.is_empty() {
        return Ok(());
    }

    std::env::set_current_dir(target)
        .map_err(|err| format!("failed to change directory to {target}: {err}"))
}

fn forward_args(remaining: &[String], flags: &GlobalFlags) -> Vec<String> {
    let mut out = remaining.to_vec();
    if out.is_empty() {
        return out;
    }

    // Most command parsers accept these flags anywhere; keep deterministic ordering.
    if flags.json {
        out.insert(1, "--json".to_string());
    }
    if flags.jsonl {
        out.insert(1, "--jsonl".to_string());
    }
    if flags.quiet {
        out.insert(1, "--quiet".to_string());
    }
    out
}

fn forward_tui_args(flags: &GlobalFlags) -> Vec<String> {
    let mut out = Vec::new();
    if flags.json {
        out.push("--json".to_string());
    }
    if flags.jsonl {
        out.push("--jsonl".to_string());
    }
    out
}

fn forward_loop_spawn_args(remaining: &[String], flags: &GlobalFlags) -> Vec<String> {
    let mut out = forward_args(remaining, flags);
    if !flags.config.trim().is_empty() && !out.is_empty() {
        out.insert(1, "--config".to_string());
        out.insert(2, flags.config.clone());
    }
    out
}

fn write_root_help(out: &mut dyn Write) -> std::io::Result<()> {
    writeln!(out, "Control plane for AI coding agents")?;
    writeln!(out)?;
    writeln!(
        out,
        "Forge is a control plane for running and supervising AI coding agents"
    )?;
    writeln!(out, "across multiple repositories and servers.")?;
    writeln!(out)?;
    writeln!(out, "It provides:")?;
    writeln!(
        out,
        "  - A fast TUI dashboard for monitoring agent progress"
    )?;
    writeln!(out, "  - A CLI for automation and scripting")?;
    writeln!(out, "  - Deep integration with tmux and SSH")?;
    writeln!(
        out,
        "  - Multi-account orchestration with cooldown management"
    )?;
    writeln!(out)?;
    writeln!(
        out,
        "Run 'forge' without arguments to launch the TUI dashboard."
    )?;
    writeln!(out)?;
    writeln!(out, "Usage:")?;
    writeln!(out, "  forge <command> [options]")?;
    writeln!(out)?;
    writeln!(out, "Commands:")?;
    writeln!(out, "  agent     Manage persistent agents")?;
    writeln!(out, "  audit     View the Forge audit log")?;
    writeln!(out, "  clean     Remove inactive loops")?;
    writeln!(out, "  completion  Generate shell completion scripts")?;
    writeln!(out, "  context   Show current context")?;
    writeln!(out, "  config    Manage global configuration")?;
    writeln!(out, "  delegation  Evaluate delegation rules")?;
    writeln!(out, "  doctor    Run environment diagnostics")?;
    writeln!(out, "  explain   Explain agent or queue item status")?;
    writeln!(out, "  export    Export Forge data")?;
    writeln!(out, "  hook      Manage event hooks")?;
    writeln!(out, "  inject    Inject message directly into agent")?;
    writeln!(out, "  init      Initialize a repo for Forge loops")?;
    writeln!(out, "  job       Manage jobs")?;
    writeln!(out, "  kill      Kill loops immediately")?;
    writeln!(out, "  lock      Manage advisory file locks")?;
    writeln!(out, "  logs      Tail loop logs")?;
    writeln!(out, "  mail      Forge Mail messaging")?;
    writeln!(out, "  migrate   Database migration command family")?;
    writeln!(out, "  mem       Loop memory command family")?;
    writeln!(out, "  mesh      Manage mesh registry and master")?;
    writeln!(out, "  node      Execute commands against mesh nodes")?;
    writeln!(out, "  msg       Queue a message for loop(s)")?;
    writeln!(out, "  pool      Profile pool command family")?;
    writeln!(out, "  profile   Harness profile command family")?;
    writeln!(out, "  prompt    Loop prompt command family")?;
    writeln!(out, "  ps        List loops")?;
    writeln!(out, "  queue     Manage loop queues")?;
    writeln!(out, "  registry  Manage central registry")?;
    writeln!(out, "  resume    Resume loop execution")?;
    writeln!(out, "  rm        Remove loop records")?;
    writeln!(out, "  run       Run a single loop iteration")?;
    writeln!(out, "  scale     Scale loops to target count")?;
    writeln!(out, "  send      Queue a message for an agent")?;
    writeln!(out, "  skills    Manage workspace skills")?;
    writeln!(out, "  status    Show fleet status summary")?;
    writeln!(out, "  stop      Stop loops after current iteration")?;
    writeln!(out, "  task      Manage team task inbox")?;
    writeln!(out, "  team      Manage teams and team members")?;
    writeln!(out, "  template  Manage message templates")?;
    writeln!(out, "  trigger   Manage job triggers")?;
    writeln!(out, "  tui       Launch the Forge TUI")?;
    writeln!(out, "  up        Start loop(s) for a repo")?;
    writeln!(out, "  use       Set current workspace or agent context")?;
    writeln!(out, "  work      Loop work-context command family")?;
    writeln!(out, "  workflow  Manage workflows")?;
    writeln!(out)?;
    writeln!(out, "Global Flags:")?;
    writeln!(
        out,
        "      --config string   config file (default is $HOME/.config/forge/config.yaml)"
    )?;
    writeln!(out, "      --json            output in JSON format")?;
    writeln!(
        out,
        "      --jsonl           output in JSON Lines format (for streaming)"
    )?;
    writeln!(
        out,
        "      --watch           watch for changes and stream updates"
    )?;
    writeln!(
        out,
        "      --since string    replay events since duration (e.g., 1h, 30m, 24h) or timestamp"
    )?;
    writeln!(out, "  -v, --verbose         enable verbose output")?;
    writeln!(out, "      --quiet           suppress non-essential output")?;
    writeln!(out, "      --no-color        disable colored output")?;
    writeln!(out, "      --no-progress     disable progress output")?;
    writeln!(
        out,
        "      --non-interactive run without prompts, use defaults"
    )?;
    writeln!(out, "  -y, --yes             skip confirmation prompts")?;
    writeln!(
        out,
        "      --log-level string  override logging level (debug, info, warn, error)"
    )?;
    writeln!(
        out,
        "      --log-format string override logging format (json, console)"
    )?;
    writeln!(
        out,
        "  -C, --chdir string    change working directory for this command"
    )?;
    writeln!(
        out,
        "      --robot-help      show agent-oriented help and exit"
    )?;
    writeln!(out, "      --version         show version information")?;
    Ok(())
}

/// Test-only helper: run CLI with string slices and capture output.
pub fn run_for_test(args: &[&str]) -> RootCommandOutput {
    let owned_args: Vec<String> = args.iter().map(|a| (*a).to_string()).collect();
    let mut stdout = Vec::new();
    let mut stderr = Vec::new();
    let exit_code = run_with_args(&owned_args, &mut stdout, &mut stderr);
    RootCommandOutput {
        stdout: String::from_utf8_lossy(&stdout).into_owned(),
        stderr: String::from_utf8_lossy(&stderr).into_owned(),
        exit_code,
    }
}

pub struct RootCommandOutput {
    pub stdout: String,
    pub stderr: String,
    pub exit_code: i32,
}

#[cfg(test)]
#[allow(clippy::unwrap_used)]
mod tests {
    use std::collections::BTreeSet;

    use super::{
        agent, audit, clean, completion, config, context, crate_label, delegation, doctor, explain,
        export, external_adapter, hook, init, inject, job, kill, lock, logs, loop_internal, mail,
        mem, mesh, migrate, msg, node, pool, profile, prompt, ps, queue, registry, resume, rm, run,
        run_for_test, scale, send, seq, skills, status, stop, task, team, team_heartbeat_watchdog,
        template, trigger, tui, up, wait, work, workflow,
    };

    #[test]
    fn crate_label_is_stable() {
        assert_eq!(crate_label(), "forge-cli");
    }

    #[test]
    fn agent_module_is_accessible() {
        let _ = agent::InMemoryAgentBackend::new();
    }

    #[test]
    fn context_module_is_accessible() {
        let _ = context::InMemoryContextBackend::default();
    }

    #[test]
    fn doctor_module_is_accessible() {
        let _ = doctor::InMemoryDoctorBackend::default();
    }

    #[test]
    fn delegation_module_is_accessible() {
        let _ = delegation::InMemoryDelegationBackend::default();
    }

    #[test]
    fn explain_module_is_accessible() {
        let _ = explain::SqliteExplainBackend::open_from_env();
    }

    #[test]
    fn external_adapter_module_is_accessible() {
        let _ = external_adapter::AdapterRuntimeConfig::default();
    }

    #[test]
    fn export_module_is_accessible() {
        let _ = export::InMemoryExportBackend::default();
    }

    #[test]
    fn hook_module_is_accessible() {
        let _ = hook::InMemoryHookBackend::default();
    }

    #[test]
    fn inject_module_is_accessible() {
        let _ = inject::InMemoryInjectBackend::default();
    }

    #[test]
    fn job_module_is_accessible() {
        let _ = job::JobStore::open_from_env();
    }

    #[test]
    fn trigger_module_is_accessible() {
        let store = job::JobStore::open_from_env();
        let out = trigger::run_for_test(&["trigger", "help"], &store);
        assert_eq!(out.exit_code, 0);
    }

    #[test]
    fn init_module_is_accessible() {
        let _ = init::FilesystemInitBackend;
    }

    #[test]
    fn audit_module_is_accessible() {
        let _ = audit::InMemoryAuditBackend::default();
    }

    #[test]
    fn kill_module_is_accessible() {
        let _ = kill::InMemoryKillBackend::default();
    }

    #[test]
    fn lock_module_is_accessible() {
        let _ = lock::InMemoryLockBackend::default();
    }

    #[test]
    fn logs_module_is_accessible() {
        let _ = logs::InMemoryLogsBackend::default();
    }

    #[test]
    fn mail_module_is_accessible() {
        let _ = mail::InMemoryMailBackend::default();
    }

    #[test]
    fn loop_internal_module_is_accessible() {
        let _ = loop_internal::InMemoryLoopInternalBackend::default();
    }

    #[test]
    fn clean_module_is_accessible() {
        let _ = clean::InMemoryLoopBackend::default();
    }

    #[test]
    fn completion_module_is_accessible() {
        let out = completion::run_for_test(&["completion", "bash"]);
        assert_eq!(out.exit_code, 0);
    }

    #[test]
    fn config_module_is_accessible() {
        let _ = config::InMemoryConfigBackend::default();
    }

    #[test]
    fn migrate_module_is_accessible() {
        let _ = migrate::InMemoryMigrationBackend::default();
    }

    #[test]
    fn work_module_is_accessible() {
        let _ = work::InMemoryWorkBackend::default();
    }

    #[test]
    fn prompt_module_is_accessible() {
        let _ = prompt::FilesystemPromptBackend;
    }

    #[test]
    fn mem_module_is_accessible() {
        let _ = mem::InMemoryMemBackend::default();
    }

    #[test]
    fn mesh_module_is_accessible() {
        let _ = mesh::MeshStore::with_path(std::path::PathBuf::from("/tmp/mesh-registry.json"));
    }

    #[test]
    fn msg_module_is_accessible() {
        let _ = msg::InMemoryMsgBackend::default();
    }

    #[test]
    fn node_module_is_accessible() {
        let _ = node::ShellNodeBackend::open_from_env();
    }

    #[test]
    fn pool_module_is_accessible() {
        let _ = pool::InMemoryPoolBackend::default();
    }

    #[test]
    fn profile_module_is_accessible() {
        let _ = profile::InMemoryProfileBackend::default();
    }

    #[test]
    fn queue_module_is_accessible() {
        let _ = queue::InMemoryQueueBackend::default();
    }

    #[test]
    fn registry_module_is_accessible() {
        let _ = registry::RegistryStore::with_paths(
            std::path::PathBuf::from("/tmp/local"),
            std::path::PathBuf::from("/tmp/repo"),
        );
    }

    #[test]
    fn rm_module_is_accessible() {
        let _ = rm::InMemoryLoopBackend::default();
    }

    #[test]
    fn run_module_is_accessible() {
        let _ = run::InMemoryRunBackend::default();
    }

    #[test]
    fn scale_module_is_accessible() {
        let _ = scale::InMemoryScaleBackend::default();
    }

    #[test]
    fn resume_module_is_accessible() {
        let _ = resume::InMemoryResumeBackend::default();
    }

    #[test]
    fn ps_module_is_accessible() {
        let _ = ps::InMemoryPsBackend::default();
    }

    #[test]
    fn send_module_is_accessible() {
        let _ = send::InMemorySendBackend::default();
    }

    #[test]
    fn skills_module_is_accessible() {
        let _ = skills::InMemorySkillsBackend::default();
    }

    #[test]
    fn status_module_is_accessible() {
        let _ = status::InMemoryStatusBackend::default();
    }

    #[test]
    fn team_heartbeat_watchdog_module_is_accessible() {
        let _ = team_heartbeat_watchdog::TeamHeartbeatState::default();
    }

    #[test]
    fn template_module_is_accessible() {
        let _ = template::Template {
            name: "demo".to_string(),
            description: String::new(),
            message: "hello".to_string(),
            variables: Vec::new(),
            tags: Vec::new(),
            source: String::new(),
        };
    }

    #[test]
    fn stop_module_is_accessible() {
        let _ = stop::InMemoryStopBackend::default();
    }

    #[test]
    fn task_module_is_accessible() {
        let _ = task::SqliteTaskBackend::open_from_env();
    }

    #[test]
    fn team_module_is_accessible() {
        let _ = team::SqliteTeamBackend::open_from_env();
    }

    #[test]
    fn up_module_is_accessible() {
        let _ = up::InMemoryUpBackend::default();
    }

    #[test]
    fn seq_module_is_accessible() {
        let _ = seq::InMemorySeqBackend::default();
    }

    #[test]
    fn tui_module_is_accessible() {
        let _ = tui::InMemoryTuiBackend::default();
    }

    #[test]
    fn wait_module_is_accessible() {
        let _ = wait::InMemoryWaitBackend::default();
    }

    #[test]
    fn workflow_module_is_accessible() {
        let _ = workflow::InMemoryWorkflowBackend::default();
    }

    #[test]
    fn no_command_dispatches_to_tui() {
        let out = run_for_test(&[]);
        assert_eq!(out.exit_code, 1);
        assert!(out.stdout.is_empty());
        assert!(out.stderr.contains("TUI requires an interactive terminal"));
    }

    #[test]
    fn robot_help_renders_root_help() {
        let out = run_for_test(&["--robot-help"]);
        assert_eq!(out.exit_code, 0);
        assert!(out.stdout.contains("Control plane for AI coding agents"));
        assert!(out.stdout.contains("Commands:"));
        assert!(out.stderr.is_empty());
    }

    #[test]
    fn root_help_includes_extended_command_families() {
        let out = run_for_test(&["--help"]);
        assert_eq!(out.exit_code, 0);
        for command in [
            "  delegation",
            "  job",
            "  trigger",
            "  mesh",
            "  node",
            "  registry",
            "  team",
            "  task",
            "  workflow",
        ] {
            assert!(
                out.stdout.contains(command),
                "missing command in root help: {command}"
            );
        }
    }

    #[test]
    fn docs_cli_covers_root_help_command_families() {
        let help = run_for_test(&["--help"]);
        assert_eq!(help.exit_code, 0);

        let docs_path = std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join("../../docs/cli.md");
        let docs_text = std::fs::read_to_string(&docs_path)
            .unwrap_or_else(|err| panic!("read {}: {err}", docs_path.display()));

        let command_ids = parse_root_help_command_ids(&help.stdout);
        let documented_ids = parse_documented_command_ids(&docs_text);

        const DOC_EXCLUSIONS: [&str; 0] = [];

        let missing = command_ids
            .into_iter()
            .filter(|cmd| !DOC_EXCLUSIONS.contains(&cmd.as_str()) && !documented_ids.contains(cmd))
            .collect::<Vec<_>>();

        assert!(
            missing.is_empty(),
            "docs/cli.md missing command sections for: {}",
            missing.join(", ")
        );
    }

    fn parse_root_help_command_ids(help_text: &str) -> BTreeSet<String> {
        let mut in_commands = false;
        let mut ids = BTreeSet::new();

        for line in help_text.lines() {
            if line.trim() == "Commands:" {
                in_commands = true;
                continue;
            }
            if in_commands && line.trim() == "Global Flags:" {
                break;
            }
            if !in_commands {
                continue;
            }
            let trimmed = line.trim_start();
            if trimmed.is_empty() {
                continue;
            }
            if let Some(command_id) = trimmed.split_whitespace().next() {
                ids.insert(command_id.to_string());
            }
        }

        ids
    }

    fn parse_documented_command_ids(docs_text: &str) -> BTreeSet<String> {
        let mut ids = BTreeSet::new();

        for line in docs_text.lines() {
            if !line.starts_with("### ") {
                continue;
            }

            let mut rest = line;
            while let Some(start_tick) = rest.find('`') {
                rest = &rest[start_tick + 1..];
                let Some(end_tick) = rest.find('`') else {
                    break;
                };
                let code_span = &rest[..end_tick];
                rest = &rest[end_tick + 1..];

                let Some(command_span) = code_span.strip_prefix("forge ") else {
                    continue;
                };
                let mut parts = command_span.split_whitespace();
                let Some(first) = parts.next() else {
                    continue;
                };
                if first == "loop" {
                    if let Some(subcommand) = parts.next() {
                        ids.insert(subcommand.to_string());
                    }
                } else {
                    ids.insert(first.to_string());
                }
            }
        }

        ids
    }

    #[test]
    fn version_flag_prints_version() {
        let out = run_for_test(&["--version"]);
        assert_eq!(out.exit_code, 0);
        assert!(out.stdout.starts_with("forge version "));
        assert!(out.stderr.is_empty());
    }

    #[test]
    fn unknown_command_returns_error() {
        let out = run_for_test(&["nonexistent"]);
        assert_eq!(out.exit_code, 1);
        assert!(out.stderr.contains("unknown forge command: nonexistent"));
        assert!(out.stderr.contains("Commands:"));
    }

    #[test]
    fn unknown_command_json_returns_envelope() {
        let out = run_for_test(&["--json", "nonexistent"]);
        assert_eq!(out.exit_code, 1);
        assert!(out.stderr.is_empty());
        let parsed: serde_json::Value = serde_json::from_str(&out.stdout).unwrap();
        assert_eq!(parsed["error"]["code"], "ERR_UNKNOWN");
        assert!(parsed["error"]["message"]
            .as_str()
            .unwrap()
            .contains("nonexistent"));
    }

    #[test]
    fn help_flag_returns_help() {
        let out = run_for_test(&["--help"]);
        assert_eq!(out.exit_code, 0);
        assert!(out.stdout.contains("Control plane for AI coding agents"));
        assert!(out.stderr.is_empty());
    }

    #[test]
    fn global_flags_parsed_before_command() {
        let out = run_for_test(&["--verbose", "--quiet", "--help"]);
        assert_eq!(out.exit_code, 0);
        assert!(out.stdout.contains("Commands:"));
    }

    #[test]
    fn invalid_chdir_global_flag_returns_error() {
        let out = run_for_test(&["-C", "/definitely/not/a/forge-dir", "up", "--name", "demo"]);
        assert_eq!(out.exit_code, 2);
        assert!(out.stdout.is_empty());
        assert!(out.stderr.contains("failed to change directory"));
        assert!(
            out.stderr.contains("/definitely/not/a/forge-dir"),
            "stderr should include the failing path"
        );
    }
}
