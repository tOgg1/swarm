use std::io::{IsTerminal, Write};
use std::path::Path;
use std::process::Command;

use serde::Serialize;

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct CommandOutput {
    pub stdout: String,
    pub stderr: String,
    pub exit_code: i32,
}

/// Backend trait abstracting the TUI launch dependencies.
///
/// In production this opens the database, reads config, and runs the TUI.
/// In tests it records whether `launch` was called and returns a configurable result.
pub trait TuiBackend {
    /// Returns `true` when running in non-interactive mode (no TTY, `--non-interactive` flag, etc.).
    fn is_non_interactive(&self) -> bool;
    /// Launch the TUI. Returns `Ok(())` on clean exit, `Err(message)` on failure.
    fn launch(&self) -> Result<(), String>;
}

/// In-memory backend for unit and integration tests.
#[derive(Default)]
pub struct InMemoryTuiBackend {
    pub non_interactive: bool,
    pub launch_error: Option<String>,
    pub launched: std::cell::Cell<bool>,
}

impl TuiBackend for InMemoryTuiBackend {
    fn is_non_interactive(&self) -> bool {
        self.non_interactive
    }

    fn launch(&self) -> Result<(), String> {
        self.launched.set(true);
        match &self.launch_error {
            Some(err) => Err(err.clone()),
            None => Ok(()),
        }
    }
}

/// Production backend that launches the Rust `forge-tui` process.
pub struct ProcessTuiBackend {
    non_interactive: bool,
    tui_bin: String,
}

impl Default for ProcessTuiBackend {
    fn default() -> Self {
        let non_interactive = !std::io::stdin().is_terminal() || !std::io::stdout().is_terminal();
        let tui_bin = resolve_tui_binary();
        Self {
            non_interactive,
            tui_bin,
        }
    }
}

fn resolve_tui_binary() -> String {
    let override_bin = std::env::var("FORGE_TUI_BIN").ok();
    let current_exe = std::env::current_exe().ok();
    resolve_tui_binary_with(override_bin.as_deref(), current_exe.as_deref())
}

fn resolve_tui_binary_with(override_bin: Option<&str>, current_exe: Option<&Path>) -> String {
    if let Some(override_bin) = override_bin {
        let trimmed = override_bin.trim();
        if !trimmed.is_empty() {
            return trimmed.to_string();
        }
    }

    let sibling_name = format!("forge-tui{}", std::env::consts::EXE_SUFFIX);
    if let Some(current_exe) = current_exe {
        if let Some(dir) = current_exe.parent() {
            let sibling = dir.join(sibling_name);
            if sibling.is_file() {
                return sibling.to_string_lossy().to_string();
            }
        }
    }

    "forge-tui".to_string()
}

impl TuiBackend for ProcessTuiBackend {
    fn is_non_interactive(&self) -> bool {
        self.non_interactive
    }

    fn launch(&self) -> Result<(), String> {
        let status = Command::new(&self.tui_bin)
            .status()
            .map_err(|err| format!("failed to launch {}: {err}", self.tui_bin))?;
        if status.success() {
            Ok(())
        } else {
            Err(format!(
                "{} exited with status {}",
                self.tui_bin,
                status
                    .code()
                    .map(|code| code.to_string())
                    .unwrap_or_else(|| "signal".to_string())
            ))
        }
    }
}

/// Run the `tui` command from the environment (production entry point).
pub fn run_from_env(args: &[String], stdout: &mut dyn Write, stderr: &mut dyn Write) -> i32 {
    let backend = ProcessTuiBackend::default();
    run_with_backend(args, &backend, stdout, stderr)
}

/// Run the `tui` command with an injected backend.
pub fn run_with_backend(
    args: &[String],
    backend: &dyn TuiBackend,
    stdout: &mut dyn Write,
    stderr: &mut dyn Write,
) -> i32 {
    let parsed = match parse_args(args) {
        Ok(parsed) => parsed,
        Err(message) => {
            let _ = writeln!(stderr, "error: {message}");
            return 1;
        }
    };

    match parsed {
        ParsedCommand::Help => {
            write_help(stdout);
            0
        }
        ParsedCommand::Launch { json, jsonl } => {
            execute_launch(backend, json, jsonl, stdout, stderr)
        }
    }
}

/// Test-only helper: run with string slices and capture output.
pub fn run_for_test(args: &[&str], backend: &dyn TuiBackend) -> CommandOutput {
    let owned: Vec<String> = args.iter().map(|a| (*a).to_string()).collect();
    let mut stdout = Vec::new();
    let mut stderr = Vec::new();
    let exit_code = run_with_backend(&owned, backend, &mut stdout, &mut stderr);
    CommandOutput {
        stdout: String::from_utf8_lossy(&stdout).into_owned(),
        stderr: String::from_utf8_lossy(&stderr).into_owned(),
        exit_code,
    }
}

// ---------------------------------------------------------------------------
// Argument parsing
// ---------------------------------------------------------------------------

#[derive(Debug, Clone, PartialEq, Eq)]
enum ParsedCommand {
    Help,
    Launch { json: bool, jsonl: bool },
}

fn parse_args(args: &[String]) -> Result<ParsedCommand, String> {
    let mut json = false;
    let mut jsonl = false;

    // Skip the command name itself ("tui" or "ui").
    let tokens = if args.first().map(|a| a.as_str()) == Some("tui")
        || args.first().map(|a| a.as_str()) == Some("ui")
    {
        &args[1..]
    } else {
        args
    };

    for token in tokens {
        match token.as_str() {
            "-h" | "--help" | "help" => return Ok(ParsedCommand::Help),
            "--json" => json = true,
            "--jsonl" => jsonl = true,
            other => {
                return Err(format!("unknown flag: {other}"));
            }
        }
    }

    if json && jsonl {
        return Err("--json and --jsonl are mutually exclusive".to_string());
    }

    Ok(ParsedCommand::Launch { json, jsonl })
}

// ---------------------------------------------------------------------------
// Execution
// ---------------------------------------------------------------------------

/// Preflight error response matching Go's `PreflightError` shape.
#[derive(Debug, Clone, Serialize)]
struct PreflightError {
    error: PreflightPayload,
}

#[derive(Debug, Clone, Serialize)]
struct PreflightPayload {
    code: String,
    message: String,
    hint: String,
    next_step: String,
}

/// Launched response for JSON/JSONL modes (informational).
#[derive(Debug, Clone, Serialize)]
struct LaunchResponse {
    status: String,
    message: String,
}

fn execute_launch(
    backend: &dyn TuiBackend,
    json: bool,
    jsonl: bool,
    stdout: &mut dyn Write,
    stderr: &mut dyn Write,
) -> i32 {
    // Check non-interactive mode.
    if backend.is_non_interactive() {
        let err = PreflightError {
            error: PreflightPayload {
                code: "ERR_PREFLIGHT".to_string(),
                message: "TUI requires an interactive terminal".to_string(),
                hint: "Run without --non-interactive and with a TTY, or use CLI subcommands"
                    .to_string(),
                next_step: "forge --help".to_string(),
            },
        };

        if json {
            if let Ok(data) = serde_json::to_string_pretty(&err) {
                let _ = writeln!(stdout, "{data}");
            }
        } else if jsonl {
            if let Ok(data) = serde_json::to_string(&err) {
                let _ = writeln!(stdout, "{data}");
            }
        } else {
            let _ = writeln!(stderr, "error: TUI requires an interactive terminal");
            let _ = writeln!(
                stderr,
                "hint: Run without --non-interactive and with a TTY, or use CLI subcommands"
            );
            let _ = writeln!(stderr, "next: forge --help");
        }
        return 1;
    }

    // Attempt to launch the TUI.
    match backend.launch() {
        Ok(()) => {
            if json {
                let resp = LaunchResponse {
                    status: "ok".to_string(),
                    message: "TUI exited normally".to_string(),
                };
                if let Ok(data) = serde_json::to_string_pretty(&resp) {
                    let _ = writeln!(stdout, "{data}");
                }
            } else if jsonl {
                let resp = LaunchResponse {
                    status: "ok".to_string(),
                    message: "TUI exited normally".to_string(),
                };
                if let Ok(data) = serde_json::to_string(&resp) {
                    let _ = writeln!(stdout, "{data}");
                }
            }
            0
        }
        Err(message) => {
            if json {
                let err = PreflightError {
                    error: PreflightPayload {
                        code: "ERR_OPERATION_FAILED".to_string(),
                        message: message.clone(),
                        hint: "Check that the database is accessible and the terminal supports TUI"
                            .to_string(),
                        next_step: "forge --help".to_string(),
                    },
                };
                if let Ok(data) = serde_json::to_string_pretty(&err) {
                    let _ = writeln!(stdout, "{data}");
                }
            } else if jsonl {
                let err = PreflightError {
                    error: PreflightPayload {
                        code: "ERR_OPERATION_FAILED".to_string(),
                        message: message.clone(),
                        hint: "Check that the database is accessible and the terminal supports TUI"
                            .to_string(),
                        next_step: "forge --help".to_string(),
                    },
                };
                if let Ok(data) = serde_json::to_string(&err) {
                    let _ = writeln!(stdout, "{data}");
                }
            } else {
                let _ = writeln!(stderr, "error: {message}");
            }
            2
        }
    }
}

// ---------------------------------------------------------------------------
// Help text
// ---------------------------------------------------------------------------

fn write_help(out: &mut dyn Write) {
    let _ = writeln!(out, "Launch the Forge TUI");
    let _ = writeln!(out);
    let _ = writeln!(out, "Launch the Forge terminal user interface (TUI).");
    let _ = writeln!(out);
    let _ = writeln!(out, "Usage:");
    let _ = writeln!(out, "  forge tui [flags]");
    let _ = writeln!(out);
    let _ = writeln!(out, "Aliases:");
    let _ = writeln!(out, "  tui, ui");
    let _ = writeln!(out);
    let _ = writeln!(out, "Flags:");
    let _ = writeln!(out, "  -h, --help   help for tui");
}

// ---------------------------------------------------------------------------
// Unit tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;

    fn parse_json_or_panic(raw: &str, context: &str) -> serde_json::Value {
        match serde_json::from_str(raw) {
            Ok(value) => value,
            Err(err) => panic!("{context}: {err}"),
        }
    }

    fn str_or_panic<'a>(value: Option<&'a str>, context: &str) -> &'a str {
        match value {
            Some(value) => value,
            None => panic!("{context}"),
        }
    }

    fn default_backend() -> InMemoryTuiBackend {
        InMemoryTuiBackend::default()
    }

    fn non_interactive_backend() -> InMemoryTuiBackend {
        InMemoryTuiBackend {
            non_interactive: true,
            ..Default::default()
        }
    }

    fn failing_backend(message: &str) -> InMemoryTuiBackend {
        InMemoryTuiBackend {
            launch_error: Some(message.to_string()),
            ..Default::default()
        }
    }

    // -- help ----------------------------------------------------------------

    #[test]
    fn tui_help_flag() {
        let backend = default_backend();
        let out = run_for_test(&["tui", "--help"], &backend);
        assert_eq!(out.exit_code, 0);
        assert!(out.stdout.contains("Launch the Forge TUI"));
        assert!(out.stdout.contains("Usage:"));
        assert!(out.stdout.contains("forge tui [flags]"));
        assert!(out.stdout.contains("Aliases:"));
        assert!(out.stdout.contains("tui, ui"));
        assert!(out.stderr.is_empty());
    }

    #[test]
    fn tui_help_short_flag() {
        let backend = default_backend();
        let out = run_for_test(&["tui", "-h"], &backend);
        assert_eq!(out.exit_code, 0);
        assert!(out.stdout.contains("Launch the Forge TUI"));
    }

    #[test]
    fn tui_help_subcommand() {
        let backend = default_backend();
        let out = run_for_test(&["tui", "help"], &backend);
        assert_eq!(out.exit_code, 0);
        assert!(out.stdout.contains("Launch the Forge TUI"));
    }

    #[test]
    fn ui_alias_help() {
        let backend = default_backend();
        let out = run_for_test(&["ui", "--help"], &backend);
        assert_eq!(out.exit_code, 0);
        assert!(out.stdout.contains("Launch the Forge TUI"));
    }

    // -- launch (success) ---------------------------------------------------

    #[test]
    fn tui_launch_interactive() {
        let backend = default_backend();
        let out = run_for_test(&["tui"], &backend);
        assert_eq!(out.exit_code, 0);
        assert!(out.stdout.is_empty());
        assert!(out.stderr.is_empty());
        assert!(backend.launched.get());
    }

    #[test]
    fn tui_launch_interactive_json() {
        let backend = default_backend();
        let out = run_for_test(&["tui", "--json"], &backend);
        assert_eq!(out.exit_code, 0);
        assert!(backend.launched.get());
        let parsed = parse_json_or_panic(&out.stdout, "valid json");
        assert_eq!(parsed["status"], "ok");
        assert_eq!(parsed["message"], "TUI exited normally");
    }

    #[test]
    fn tui_launch_interactive_jsonl() {
        let backend = default_backend();
        let out = run_for_test(&["tui", "--jsonl"], &backend);
        assert_eq!(out.exit_code, 0);
        assert!(backend.launched.get());
        let parsed = parse_json_or_panic(out.stdout.trim(), "valid jsonl");
        assert_eq!(parsed["status"], "ok");
    }

    #[test]
    fn ui_alias_launches() {
        let backend = default_backend();
        let out = run_for_test(&["ui"], &backend);
        assert_eq!(out.exit_code, 0);
        assert!(backend.launched.get());
    }

    // -- non-interactive error -----------------------------------------------

    #[test]
    fn tui_non_interactive_text() {
        let backend = non_interactive_backend();
        let out = run_for_test(&["tui"], &backend);
        assert_eq!(out.exit_code, 1);
        assert!(!backend.launched.get());
        assert!(out.stderr.contains("TUI requires an interactive terminal"));
        assert!(out.stderr.contains("hint:"));
        assert!(out.stderr.contains("forge --help"));
    }

    #[test]
    fn tui_non_interactive_json() {
        let backend = non_interactive_backend();
        let out = run_for_test(&["tui", "--json"], &backend);
        assert_eq!(out.exit_code, 1);
        assert!(!backend.launched.get());
        let parsed = parse_json_or_panic(&out.stdout, "valid json");
        assert_eq!(parsed["error"]["code"], "ERR_PREFLIGHT");
        assert_eq!(
            parsed["error"]["message"],
            "TUI requires an interactive terminal"
        );
        assert!(
            str_or_panic(parsed["error"]["hint"].as_str(), "hint present")
                .contains("non-interactive")
        );
        assert_eq!(parsed["error"]["next_step"], "forge --help");
        assert!(out.stderr.is_empty());
    }

    #[test]
    fn tui_non_interactive_jsonl() {
        let backend = non_interactive_backend();
        let out = run_for_test(&["tui", "--jsonl"], &backend);
        assert_eq!(out.exit_code, 1);
        assert!(!backend.launched.get());
        let parsed = parse_json_or_panic(out.stdout.trim(), "valid jsonl");
        assert_eq!(parsed["error"]["code"], "ERR_PREFLIGHT");
    }

    // -- launch error --------------------------------------------------------

    #[test]
    fn tui_launch_error_text() {
        let backend = failing_backend("failed to open database");
        let out = run_for_test(&["tui"], &backend);
        assert_eq!(out.exit_code, 2);
        assert!(backend.launched.get());
        assert!(out.stderr.contains("failed to open database"));
    }

    #[test]
    fn tui_launch_error_json() {
        let backend = failing_backend("failed to open database");
        let out = run_for_test(&["tui", "--json"], &backend);
        assert_eq!(out.exit_code, 2);
        let parsed = parse_json_or_panic(&out.stdout, "valid json");
        assert_eq!(parsed["error"]["code"], "ERR_OPERATION_FAILED");
        assert_eq!(parsed["error"]["message"], "failed to open database");
        assert!(out.stderr.is_empty());
    }

    #[test]
    fn tui_launch_error_jsonl() {
        let backend = failing_backend("failed to open database");
        let out = run_for_test(&["tui", "--jsonl"], &backend);
        assert_eq!(out.exit_code, 2);
        let parsed = parse_json_or_panic(out.stdout.trim(), "valid jsonl");
        assert_eq!(parsed["error"]["code"], "ERR_OPERATION_FAILED");
    }

    // -- invalid flags -------------------------------------------------------

    #[test]
    fn tui_unknown_flag() {
        let backend = default_backend();
        let out = run_for_test(&["tui", "--bogus"], &backend);
        assert_eq!(out.exit_code, 1);
        assert!(out.stderr.contains("unknown flag: --bogus"));
    }

    #[test]
    fn tui_json_jsonl_exclusive() {
        let backend = default_backend();
        let out = run_for_test(&["tui", "--json", "--jsonl"], &backend);
        assert_eq!(out.exit_code, 1);
        assert!(out.stderr.contains("mutually exclusive"));
    }

    #[test]
    fn resolve_tui_binary_uses_override_when_set() {
        let resolved = resolve_tui_binary_with(Some("custom-tui"), None);
        assert_eq!(resolved, "custom-tui");
    }

    #[test]
    fn resolve_tui_binary_prefers_sibling_binary() {
        let base = std::env::temp_dir().join(format!(
            "forge-cli-tui-resolve-{}-{}",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .map(|duration| duration.as_nanos())
                .unwrap_or(0)
        ));
        let bin_dir = base.join("bin");
        if let Err(err) = fs::create_dir_all(&bin_dir) {
            panic!("create temp dir: {err}");
        }

        let current_exe = bin_dir.join("forge");
        if let Err(err) = fs::write(&current_exe, b"") {
            panic!("write fake current exe: {err}");
        }

        let sibling_name = format!("forge-tui{}", std::env::consts::EXE_SUFFIX);
        let sibling = bin_dir.join(sibling_name);
        if let Err(err) = fs::write(&sibling, b"") {
            panic!("write sibling tui: {err}");
        }

        let resolved = resolve_tui_binary_with(None, Some(current_exe.as_path()));
        assert_eq!(resolved, sibling.to_string_lossy().to_string());

        let _ = fs::remove_dir_all(base);
    }

    #[test]
    fn resolve_tui_binary_falls_back_to_path_lookup() {
        let resolved = resolve_tui_binary_with(None, None);
        assert_eq!(resolved, "forge-tui");
    }
}
