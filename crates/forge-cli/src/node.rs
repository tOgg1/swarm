use std::io::Write;
use std::process::Command;

use serde::Serialize;

const ROUTE_STAGE_ENV: &str = "FORGE_NODE_ROUTE_STAGE";
const ROUTE_STAGE_MASTER: &str = "master";

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct CommandOutput {
    pub stdout: String,
    pub stderr: String,
    pub exit_code: i32,
}

#[derive(Debug, Clone, PartialEq, Eq)]
enum RouteStage {
    Default,
    Master,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct NodeEntry {
    pub id: String,
    pub endpoint: String,
    pub is_master: bool,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct NodeExecResult {
    pub node_id: String,
    pub command: String,
    pub stdout: String,
    pub stderr: String,
    pub exit_code: i32,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub routed_via: Option<String>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ShellCommandResult {
    pub stdout: String,
    pub stderr: String,
    pub exit_code: i32,
}

pub trait NodeBackend {
    fn mesh_status(&self) -> Result<crate::mesh::MeshStatus, String>;
    fn run_local(&mut self, command: &str) -> Result<ShellCommandResult, String>;
    fn run_ssh(&mut self, endpoint: &str, command: &str) -> Result<ShellCommandResult, String>;
}

#[derive(Debug, Clone)]
pub struct ShellNodeBackend {
    store: crate::mesh::MeshStore,
}

impl ShellNodeBackend {
    #[must_use]
    pub fn open_from_env() -> Self {
        Self {
            store: crate::mesh::MeshStore::open_from_env(),
        }
    }
}

impl NodeBackend for ShellNodeBackend {
    fn mesh_status(&self) -> Result<crate::mesh::MeshStatus, String> {
        self.store.status()
    }

    fn run_local(&mut self, command: &str) -> Result<ShellCommandResult, String> {
        let mut process = Command::new("sh");
        process.args(["-lc", command]);
        run_process(process, "local command")
    }

    fn run_ssh(&mut self, endpoint: &str, command: &str) -> Result<ShellCommandResult, String> {
        let mut process = Command::new("ssh");
        process.args([
            "-o",
            "BatchMode=yes",
            "-o",
            "ConnectTimeout=10",
            endpoint,
            "sh",
            "-lc",
            command,
        ]);
        run_process(process, "ssh command")
    }
}

fn run_process(mut command: Command, label: &str) -> Result<ShellCommandResult, String> {
    let output = command
        .output()
        .map_err(|err| format!("failed to start {label}: {err}"))?;

    Ok(ShellCommandResult {
        stdout: String::from_utf8_lossy(&output.stdout).into_owned(),
        stderr: String::from_utf8_lossy(&output.stderr).into_owned(),
        exit_code: output.status.code().unwrap_or(1),
    })
}

#[derive(Debug, Clone, PartialEq, Eq)]
enum NodeCommand {
    Help,
    List,
    Exec { node_id: String, command: String },
    Registry { node_id: String, args: Vec<String> },
}

#[derive(Debug)]
struct ParsedArgs {
    command: NodeCommand,
    json: bool,
    jsonl: bool,
}

pub fn run_for_test(args: &[&str], backend: &mut dyn NodeBackend) -> CommandOutput {
    let owned_args: Vec<String> = args.iter().map(|arg| (*arg).to_string()).collect();
    let mut stdout = Vec::new();
    let mut stderr = Vec::new();
    let exit_code = run_with_backend(&owned_args, backend, &mut stdout, &mut stderr);
    CommandOutput {
        stdout: String::from_utf8_lossy(&stdout).into_owned(),
        stderr: String::from_utf8_lossy(&stderr).into_owned(),
        exit_code,
    }
}

pub fn run_with_backend(
    args: &[String],
    backend: &mut dyn NodeBackend,
    stdout: &mut dyn Write,
    stderr: &mut dyn Write,
) -> i32 {
    let parsed = match parse_args(args) {
        Ok(parsed) => parsed,
        Err(err) => {
            let _ = writeln!(stderr, "{err}");
            return 1;
        }
    };

    match parsed.command {
        NodeCommand::Help => {
            if let Err(err) = write_help(stdout) {
                let _ = writeln!(stderr, "{err}");
                return 1;
            }
            0
        }
        NodeCommand::List => {
            let status = match backend.mesh_status() {
                Ok(status) => status,
                Err(err) => {
                    let _ = writeln!(stderr, "{err}");
                    return 1;
                }
            };
            let rows: Vec<NodeEntry> = status
                .nodes
                .into_iter()
                .map(|node| NodeEntry {
                    id: node.id,
                    endpoint: node.endpoint,
                    is_master: node.is_master,
                })
                .collect();

            if parsed.json || parsed.jsonl {
                if let Err(err) = write_json_output(stdout, &rows, parsed.jsonl) {
                    let _ = writeln!(stderr, "{err}");
                    return 1;
                }
                return 0;
            }

            if rows.is_empty() {
                let _ = writeln!(stdout, "no nodes in mesh registry");
                return 0;
            }

            for row in rows {
                let endpoint = if row.endpoint.trim().is_empty() {
                    "-"
                } else {
                    row.endpoint.as_str()
                };
                let _ = writeln!(
                    stdout,
                    "id={} endpoint={} master={}",
                    row.id, endpoint, row.is_master
                );
            }
            0
        }
        NodeCommand::Exec { node_id, command } => {
            let result = match route_exec_with_backend(&node_id, &command, backend) {
                Ok(result) => result,
                Err(err) => {
                    let _ = writeln!(stderr, "{err}");
                    return 1;
                }
            };

            if parsed.json || parsed.jsonl {
                if let Err(err) = write_json_output(stdout, &result, parsed.jsonl) {
                    let _ = writeln!(stderr, "{err}");
                    return 1;
                }
                return if result.exit_code == 0 { 0 } else { 1 };
            }

            if !result.stdout.is_empty() {
                let _ = write!(stdout, "{}", result.stdout);
                if !result.stdout.ends_with('\n') {
                    let _ = writeln!(stdout);
                }
            }
            if !result.stderr.is_empty() {
                let _ = write!(stderr, "{}", result.stderr);
                if !result.stderr.ends_with('\n') {
                    let _ = writeln!(stderr);
                }
            }
            if result.exit_code == 0 {
                0
            } else {
                let _ = writeln!(stderr, "command exited with code {}", result.exit_code);
                1
            }
        }
        NodeCommand::Registry { node_id, args } => {
            let command = build_remote_command("forge registry", &args, parsed.json, parsed.jsonl);
            match run_remote_passthrough_with_backend(&node_id, &command, backend, stdout, stderr) {
                Ok(()) => 0,
                Err(err) => {
                    let _ = writeln!(stderr, "{err}");
                    1
                }
            }
        }
    }
}

pub fn route_exec(node_id: &str, command: &str) -> Result<NodeExecResult, String> {
    let mut backend = ShellNodeBackend::open_from_env();
    route_exec_with_backend(node_id, command, &mut backend)
}

pub fn route_exec_with_backend(
    node_id: &str,
    command: &str,
    backend: &mut dyn NodeBackend,
) -> Result<NodeExecResult, String> {
    let stage = match std::env::var(ROUTE_STAGE_ENV) {
        Ok(value) if value.trim().eq_ignore_ascii_case(ROUTE_STAGE_MASTER) => RouteStage::Master,
        _ => RouteStage::Default,
    };
    route_exec_with_stage(node_id, command, backend, stage)
}

fn route_exec_with_stage(
    node_id: &str,
    command: &str,
    backend: &mut dyn NodeBackend,
    stage: RouteStage,
) -> Result<NodeExecResult, String> {
    let node_id = node_id.trim();
    if node_id.is_empty() {
        return Err("node id is required".to_string());
    }
    let command = command.trim();
    if command.is_empty() {
        return Err("command is required".to_string());
    }

    let status = backend.mesh_status()?;
    let Some(target) = status.nodes.iter().find(|node| node.id == node_id).cloned() else {
        return Err(format!("node {node_id} not found in mesh registry"));
    };

    if stage == RouteStage::Default && !target.is_master {
        let master_id = status
            .master_node_id
            .clone()
            .ok_or_else(|| "mesh master not set; run 'forge mesh promote <node-id>'".to_string())?;
        let Some(master) = status
            .nodes
            .iter()
            .find(|node| node.id == master_id)
            .cloned()
        else {
            return Err("mesh master missing from registry entries".to_string());
        };

        let master_endpoint = normalize_ssh_endpoint(master.endpoint.as_str()).ok_or_else(|| {
            format!(
                "master node {} offline: endpoint missing (set with 'forge mesh promote --endpoint')",
                master.id
            )
        })?;

        let forwarded_cmd = format!(
            "{}={} forge node exec {} -- {}",
            ROUTE_STAGE_ENV,
            ROUTE_STAGE_MASTER,
            shell_quote(node_id),
            shell_quote(command)
        );
        let forwarded_result = backend.run_ssh(&master_endpoint, &forwarded_cmd)?;
        if forwarded_result.exit_code == 255 {
            let detail = trim_or_default(&forwarded_result.stderr, "ssh connection failed");
            return Err(format!("master node {} offline: {detail}", master.id));
        }

        return Ok(NodeExecResult {
            node_id: node_id.to_string(),
            command: command.to_string(),
            stdout: forwarded_result.stdout,
            stderr: forwarded_result.stderr,
            exit_code: forwarded_result.exit_code,
            routed_via: Some(master.id),
        });
    }

    execute_on_target(node_id, command, &target, backend, None)
}

fn execute_on_target(
    node_id: &str,
    command: &str,
    target: &crate::mesh::MeshStatusNode,
    backend: &mut dyn NodeBackend,
    routed_via: Option<String>,
) -> Result<NodeExecResult, String> {
    let endpoint = target.endpoint.trim();
    let shell_result = if is_local_endpoint(endpoint) || (endpoint.is_empty() && target.is_master) {
        backend.run_local(command)?
    } else {
        let normalized = normalize_ssh_endpoint(endpoint)
            .ok_or_else(|| format!("node {node_id} offline: endpoint missing"))?;
        let result = backend.run_ssh(&normalized, command)?;
        if result.exit_code == 255 {
            let detail = trim_or_default(&result.stderr, "ssh connection failed");
            return Err(format!("node {node_id} offline: {detail}"));
        }
        result
    };

    Ok(NodeExecResult {
        node_id: node_id.to_string(),
        command: command.to_string(),
        stdout: shell_result.stdout,
        stderr: shell_result.stderr,
        exit_code: shell_result.exit_code,
        routed_via,
    })
}

fn parse_args(args: &[String]) -> Result<ParsedArgs, String> {
    if args.is_empty() {
        return Ok(ParsedArgs {
            command: NodeCommand::Help,
            json: false,
            jsonl: false,
        });
    }

    let start = if args.first().is_some_and(|token| token == "node") {
        1
    } else {
        0
    };

    let mut json = false;
    let mut jsonl = false;
    let mut index = start;
    while index < args.len() {
        match args[index].as_str() {
            "--json" => {
                json = true;
                index += 1;
            }
            "--jsonl" => {
                jsonl = true;
                index += 1;
            }
            "--quiet" => {
                index += 1;
            }
            _ => break,
        }
    }

    if json && jsonl {
        return Err("error: --json and --jsonl cannot be used together".to_string());
    }

    if index >= args.len() {
        return Ok(ParsedArgs {
            command: NodeCommand::Help,
            json,
            jsonl,
        });
    }

    let subcommand = args[index].as_str();
    index += 1;
    let command = match subcommand {
        "help" | "-h" | "--help" => NodeCommand::Help,
        "ls" | "list" => {
            if index < args.len() {
                return Err("usage: forge node ls".to_string());
            }
            NodeCommand::List
        }
        "exec" => {
            let node_id = args
                .get(index)
                .ok_or_else(|| "usage: forge node exec <node-id> -- <command>".to_string())?
                .clone();
            index += 1;
            let Some(separator) = args.get(index) else {
                return Err("usage: forge node exec <node-id> -- <command>".to_string());
            };
            if separator != "--" {
                return Err("usage: forge node exec <node-id> -- <command>".to_string());
            }
            index += 1;
            if index >= args.len() {
                return Err("usage: forge node exec <node-id> -- <command>".to_string());
            }
            let command = args[index..].join(" ");
            NodeCommand::Exec { node_id, command }
        }
        "registry" => parse_registry_command(&args[index..])?,
        other => return Err(format!("unknown node subcommand: {other}")),
    };

    Ok(ParsedArgs {
        command,
        json,
        jsonl,
    })
}

fn parse_registry_command(args: &[String]) -> Result<NodeCommand, String> {
    let Some(subcommand) = args.first().map(String::as_str) else {
        return Err("usage: forge node registry <ls|show|update> <node-id> ...".to_string());
    };

    match subcommand {
        "ls" | "list" => {
            let node_id = args
                .get(1)
                .ok_or_else(|| {
                    "usage: forge node registry ls <node-id> [agents|prompts]".to_string()
                })?
                .clone();
            let mut remote_args = vec!["ls".to_string()];
            if let Some(scope) = args.get(2) {
                remote_args.push(scope.clone());
            }
            if args.len() > 3 {
                return Err("usage: forge node registry ls <node-id> [agents|prompts]".to_string());
            }
            Ok(NodeCommand::Registry {
                node_id,
                args: remote_args,
            })
        }
        "show" => {
            let node_id = args
                .get(1)
                .ok_or_else(|| {
                    "usage: forge node registry show <node-id> <agent|prompt> <name>".to_string()
                })?
                .clone();
            let kind = args
                .get(2)
                .ok_or_else(|| {
                    "usage: forge node registry show <node-id> <agent|prompt> <name>".to_string()
                })?
                .clone();
            let name = args
                .get(3)
                .ok_or_else(|| {
                    "usage: forge node registry show <node-id> <agent|prompt> <name>".to_string()
                })?
                .clone();
            if args.len() > 4 {
                return Err(
                    "usage: forge node registry show <node-id> <agent|prompt> <name>".to_string(),
                );
            }
            Ok(NodeCommand::Registry {
                node_id,
                args: vec!["show".to_string(), kind, name],
            })
        }
        "update" => {
            let node_id = args
                .get(1)
                .ok_or_else(|| {
                    "usage: forge node registry update <node-id> <agent|prompt> <name> [flags]"
                        .to_string()
                })?
                .clone();
            if args.len() < 4 {
                return Err(
                    "usage: forge node registry update <node-id> <agent|prompt> <name> [flags]"
                        .to_string(),
                );
            }
            let mut remote_args = vec!["update".to_string()];
            remote_args.extend(args[2..].iter().cloned());
            Ok(NodeCommand::Registry {
                node_id,
                args: remote_args,
            })
        }
        "help" | "-h" | "--help" => Ok(NodeCommand::Help),
        other => Err(format!("unknown node registry subcommand: {other}")),
    }
}

fn write_help(stdout: &mut dyn Write) -> std::io::Result<()> {
    writeln!(stdout, "Manage mesh nodes and route commands.")?;
    writeln!(stdout)?;
    writeln!(stdout, "Usage:")?;
    writeln!(stdout, "  forge node ls")?;
    writeln!(stdout, "  forge node exec <node-id> -- <command>")?;
    writeln!(
        stdout,
        "  forge node registry ls <node-id> [agents|prompts]"
    )?;
    writeln!(
        stdout,
        "  forge node registry show <node-id> <agent|prompt> <name>"
    )?;
    writeln!(
        stdout,
        "  forge node registry update <node-id> <agent|prompt> <name> [flags]"
    )?;
    Ok(())
}

fn write_json_output(
    output: &mut dyn Write,
    value: &impl Serialize,
    jsonl: bool,
) -> Result<(), String> {
    if jsonl {
        let line = serde_json::to_string(value).map_err(|err| err.to_string())?;
        writeln!(output, "{line}").map_err(|err| err.to_string())
    } else {
        let text = serde_json::to_string_pretty(value).map_err(|err| err.to_string())?;
        writeln!(output, "{text}").map_err(|err| err.to_string())
    }
}

fn trim_or_default(value: &str, fallback: &str) -> String {
    let trimmed = value.trim();
    if trimmed.is_empty() {
        fallback.to_string()
    } else {
        trimmed.to_string()
    }
}

fn is_local_endpoint(endpoint: &str) -> bool {
    let normalized = endpoint.trim().to_ascii_lowercase();
    normalized == "local" || normalized == "local://"
}

fn normalize_ssh_endpoint(endpoint: &str) -> Option<String> {
    let trimmed = endpoint.trim();
    if trimmed.is_empty() || is_local_endpoint(trimmed) {
        return None;
    }
    if let Some(rest) = trimmed.strip_prefix("ssh://") {
        let rest = rest.trim();
        if rest.is_empty() {
            None
        } else {
            Some(rest.to_string())
        }
    } else {
        Some(trimmed.to_string())
    }
}

fn shell_quote(value: &str) -> String {
    format!("'{}'", value.replace('\'', "'\\''"))
}

pub fn build_remote_command(base: &str, args: &[String], json: bool, jsonl: bool) -> String {
    let mut parts = base
        .split_whitespace()
        .filter(|token| !token.is_empty())
        .map(str::to_string)
        .collect::<Vec<String>>();
    if json {
        parts.push("--json".to_string());
    }
    if jsonl {
        parts.push("--jsonl".to_string());
    }
    parts.extend(args.iter().cloned());
    parts
        .into_iter()
        .map(|value| shell_quote(&value))
        .collect::<Vec<String>>()
        .join(" ")
}

pub fn run_remote_passthrough(
    node_id: &str,
    command: &str,
    stdout: &mut dyn Write,
    stderr: &mut dyn Write,
) -> Result<(), String> {
    let mut backend = ShellNodeBackend::open_from_env();
    run_remote_passthrough_with_backend(node_id, command, &mut backend, stdout, stderr)
}

fn run_remote_passthrough_with_backend(
    node_id: &str,
    command: &str,
    backend: &mut dyn NodeBackend,
    stdout: &mut dyn Write,
    stderr: &mut dyn Write,
) -> Result<(), String> {
    let result = route_exec_with_backend(node_id, command, backend)?;

    if !result.stdout.is_empty() {
        write!(stdout, "{}", result.stdout).map_err(|err| err.to_string())?;
        if !result.stdout.ends_with('\n') {
            writeln!(stdout).map_err(|err| err.to_string())?;
        }
    }
    if !result.stderr.is_empty() {
        write!(stderr, "{}", result.stderr).map_err(|err| err.to_string())?;
        if !result.stderr.ends_with('\n') {
            writeln!(stderr).map_err(|err| err.to_string())?;
        }
    }

    if result.exit_code != 0 {
        return Err(format!(
            "remote command failed on node {node_id} (exit code {})",
            result.exit_code
        ));
    }

    Ok(())
}

#[cfg(test)]
mod tests {
    use std::collections::BTreeMap;

    use super::*;

    #[derive(Default)]
    struct InMemoryNodeBackend {
        status: crate::mesh::MeshStatus,
        mesh_error: Option<String>,
        local_result: Option<ShellCommandResult>,
        ssh_results: BTreeMap<String, ShellCommandResult>,
        local_calls: Vec<String>,
        ssh_calls: Vec<(String, String)>,
    }

    impl InMemoryNodeBackend {
        fn with_status(status: crate::mesh::MeshStatus) -> Self {
            Self {
                status,
                ..Self::default()
            }
        }

        fn with_ssh_result(mut self, endpoint: &str, result: ShellCommandResult) -> Self {
            self.ssh_results.insert(endpoint.to_string(), result);
            self
        }

        fn with_local_result(mut self, result: ShellCommandResult) -> Self {
            self.local_result = Some(result);
            self
        }
    }

    impl NodeBackend for InMemoryNodeBackend {
        fn mesh_status(&self) -> Result<crate::mesh::MeshStatus, String> {
            if let Some(err) = &self.mesh_error {
                return Err(err.clone());
            }
            Ok(self.status.clone())
        }

        fn run_local(&mut self, command: &str) -> Result<ShellCommandResult, String> {
            self.local_calls.push(command.to_string());
            Ok(self.local_result.clone().unwrap_or(ShellCommandResult {
                stdout: String::new(),
                stderr: String::new(),
                exit_code: 0,
            }))
        }

        fn run_ssh(&mut self, endpoint: &str, command: &str) -> Result<ShellCommandResult, String> {
            self.ssh_calls
                .push((endpoint.to_string(), command.to_string()));
            Ok(self
                .ssh_results
                .get(endpoint)
                .cloned()
                .unwrap_or(ShellCommandResult {
                    stdout: String::new(),
                    stderr: String::new(),
                    exit_code: 0,
                }))
        }
    }

    fn mesh_status() -> crate::mesh::MeshStatus {
        crate::mesh::MeshStatus {
            config_path: String::new(),
            mesh_id: "local-mesh".to_string(),
            master_node_id: Some("node-master".to_string()),
            node_count: 2,
            nodes: vec![
                crate::mesh::MeshStatusNode {
                    id: "node-master".to_string(),
                    endpoint: "ssh://master.example".to_string(),
                    is_master: true,
                    profiles_total: 0,
                    auth_ok: 0,
                    auth_expired: 0,
                    auth_missing: 0,
                    profile_auth: BTreeMap::new(),
                },
                crate::mesh::MeshStatusNode {
                    id: "node-worker".to_string(),
                    endpoint: "ssh://worker.example".to_string(),
                    is_master: false,
                    profiles_total: 0,
                    auth_ok: 0,
                    auth_expired: 0,
                    auth_missing: 0,
                    profile_auth: BTreeMap::new(),
                },
            ],
            profile_catalog: crate::mesh::ProfileCatalogSummary::default(),
            auth_totals: crate::mesh::MeshAuthTotals::default(),
        }
    }

    fn ok_or_panic<T>(result: Result<T, String>, context: &str) -> T {
        match result {
            Ok(value) => value,
            Err(err) => panic!("{context}: {err}"),
        }
    }

    fn err_or_panic<T>(result: Result<T, String>, context: &str) -> String {
        match result {
            Ok(_) => panic!("{context}"),
            Err(err) => err,
        }
    }

    #[test]
    fn parse_exec_requires_separator() {
        let args = vec!["node".to_string(), "exec".to_string(), "node-a".to_string()];
        let err = err_or_panic(parse_args(&args), "parse should fail without separator");
        assert_eq!(err, "usage: forge node exec <node-id> -- <command>");
    }

    #[test]
    fn parse_exec_preserves_tokens_after_separator() {
        let args = vec![
            "node".to_string(),
            "exec".to_string(),
            "node-a".to_string(),
            "--".to_string(),
            "echo".to_string(),
            "--json".to_string(),
            "--quiet".to_string(),
        ];
        let parsed = ok_or_panic(parse_args(&args), "parse exec preserving flags");
        match parsed.command {
            NodeCommand::Exec { node_id, command } => {
                assert_eq!(node_id, "node-a");
                assert_eq!(command, "echo --json --quiet");
            }
            other => panic!("unexpected command parsed: {other:?}"),
        }
    }

    #[test]
    fn parse_registry_requires_node_id() {
        let args = vec!["node".to_string(), "registry".to_string(), "ls".to_string()];
        let err = err_or_panic(parse_args(&args), "parse should fail without node id");
        assert_eq!(
            err,
            "usage: forge node registry ls <node-id> [agents|prompts]"
        );
    }

    #[test]
    fn help_includes_registry_subcommands() {
        let mut backend = InMemoryNodeBackend::with_status(mesh_status());
        let out = run_for_test(&["node", "help"], &mut backend);
        assert_eq!(out.exit_code, 0, "stderr={}", out.stderr);
        assert!(out
            .stdout
            .contains("forge node registry ls <node-id> [agents|prompts]"));
        assert!(out
            .stdout
            .contains("forge node registry show <node-id> <agent|prompt> <name>"));
        assert!(out
            .stdout
            .contains("forge node registry update <node-id> <agent|prompt> <name> [flags]"));
    }

    #[test]
    fn route_exec_forwards_to_master_for_non_master_target() {
        let mut backend = InMemoryNodeBackend::with_status(mesh_status());

        let result = ok_or_panic(
            route_exec_with_stage("node-worker", "echo hi", &mut backend, RouteStage::Default),
            "route via master",
        );

        assert_eq!(result.exit_code, 0);
        assert_eq!(result.routed_via.as_deref(), Some("node-master"));
        assert_eq!(backend.local_calls.len(), 0);
        assert_eq!(backend.ssh_calls.len(), 1);
        assert_eq!(backend.ssh_calls[0].0, "master.example");
        assert!(backend.ssh_calls[0]
            .1
            .contains("FORGE_NODE_ROUTE_STAGE=master"));
        assert!(backend.ssh_calls[0].1.contains("forge node exec"));
    }

    #[test]
    fn route_exec_stage_master_targets_worker_directly() {
        let mut backend = InMemoryNodeBackend::with_status(mesh_status());

        let result = ok_or_panic(
            route_exec_with_stage("node-worker", "echo hi", &mut backend, RouteStage::Master),
            "execute on worker",
        );

        assert_eq!(result.exit_code, 0);
        assert!(result.routed_via.is_none());
        assert_eq!(backend.ssh_calls.len(), 1);
        assert_eq!(backend.ssh_calls[0].0, "worker.example");
    }

    #[test]
    fn route_exec_unknown_node_errors() {
        let mut backend = InMemoryNodeBackend::with_status(mesh_status());

        let err = err_or_panic(
            route_exec_with_stage("missing", "echo hi", &mut backend, RouteStage::Default),
            "missing node should error",
        );

        assert_eq!(err, "node missing not found in mesh registry");
    }

    #[test]
    fn route_exec_requires_master_for_forwarding() {
        let mut status = mesh_status();
        status.master_node_id = None;
        let mut backend = InMemoryNodeBackend::with_status(status);

        let err = err_or_panic(
            route_exec_with_stage("node-worker", "echo hi", &mut backend, RouteStage::Default),
            "forwarding without master should error",
        );

        assert_eq!(
            err,
            "mesh master not set; run 'forge mesh promote <node-id>'"
        );
    }

    #[test]
    fn route_exec_offline_worker_missing_endpoint_errors() {
        let mut status = mesh_status();
        status.nodes[1].endpoint = "".to_string();
        let mut backend = InMemoryNodeBackend::with_status(status);

        let err = err_or_panic(
            route_exec_with_stage("node-worker", "echo hi", &mut backend, RouteStage::Master),
            "offline worker should error",
        );

        assert_eq!(err, "node node-worker offline: endpoint missing");
    }

    #[test]
    fn route_exec_treats_ssh_255_as_offline() {
        let mut backend = InMemoryNodeBackend::with_status(mesh_status()).with_ssh_result(
            "worker.example",
            ShellCommandResult {
                stdout: String::new(),
                stderr: "Connection refused".to_string(),
                exit_code: 255,
            },
        );

        let err = err_or_panic(
            route_exec_with_stage("node-worker", "echo hi", &mut backend, RouteStage::Master),
            "ssh 255 should map to offline error",
        );

        assert_eq!(err, "node node-worker offline: Connection refused");
    }

    #[test]
    fn run_with_backend_exec_reports_non_zero_exit() {
        let mut status = mesh_status();
        status.nodes[0].endpoint = "local".to_string();
        let mut backend =
            InMemoryNodeBackend::with_status(status).with_local_result(ShellCommandResult {
                stdout: String::new(),
                stderr: "boom".to_string(),
                exit_code: 23,
            });

        let out = run_for_test(&["node", "exec", "node-master", "--", "boom"], &mut backend);

        assert_eq!(out.exit_code, 1);
        assert!(out.stderr.contains("boom"));
        assert!(out.stderr.contains("command exited with code 23"));
    }

    #[test]
    fn node_registry_ls_routes_via_master() {
        let mut backend = InMemoryNodeBackend::with_status(mesh_status());

        let out = run_for_test(
            &["node", "--json", "registry", "ls", "node-worker", "agents"],
            &mut backend,
        );

        assert_eq!(out.exit_code, 0, "stderr={}", out.stderr);
        assert_eq!(backend.local_calls.len(), 0);
        assert_eq!(backend.ssh_calls.len(), 1);
        assert_eq!(backend.ssh_calls[0].0, "master.example");
        assert!(backend.ssh_calls[0].1.contains("forge node exec"));
        assert!(backend.ssh_calls[0].1.contains("registry"));
        assert!(backend.ssh_calls[0].1.contains("--json"));
    }

    #[test]
    fn node_registry_show_executes_remote_registry_command() {
        let mut status = mesh_status();
        status.nodes[0].endpoint = "local".to_string();
        let mut backend =
            InMemoryNodeBackend::with_status(status).with_local_result(ShellCommandResult {
                stdout: "ok".to_string(),
                stderr: String::new(),
                exit_code: 0,
            });

        let out = run_for_test(
            &["node", "registry", "show", "node-master", "agent", "alpha"],
            &mut backend,
        );

        assert_eq!(out.exit_code, 0, "stderr={}", out.stderr);
        assert_eq!(out.stdout.trim(), "ok");
        assert_eq!(backend.local_calls.len(), 1);
        assert_eq!(
            backend.local_calls[0],
            "'forge' 'registry' 'show' 'agent' 'alpha'"
        );
    }

    #[test]
    fn node_registry_update_surfaces_remote_failure() {
        let mut status = mesh_status();
        status.nodes[0].endpoint = "local".to_string();
        let mut backend =
            InMemoryNodeBackend::with_status(status).with_local_result(ShellCommandResult {
                stdout: String::new(),
                stderr: "bad update".to_string(),
                exit_code: 9,
            });

        let out = run_for_test(
            &[
                "node",
                "registry",
                "update",
                "node-master",
                "prompt",
                "triage",
                "--path",
                ".forge/prompts/triage.md",
            ],
            &mut backend,
        );

        assert_eq!(out.exit_code, 1);
        assert!(out.stderr.contains("bad update"));
        assert!(out
            .stderr
            .contains("remote command failed on node node-master"));
    }

    #[test]
    fn build_remote_command_quotes_values() {
        let cmd = build_remote_command(
            "forge workflow run",
            &["wf one".to_string(), "--extra".to_string()],
            true,
            false,
        );
        assert_eq!(cmd, "'forge' 'workflow' 'run' '--json' 'wf one' '--extra'");
    }
}
