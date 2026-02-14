use std::collections::BTreeMap;
use std::fs;
use std::io::Write;
use std::path::{Path, PathBuf};
#[cfg(test)]
use std::time::{SystemTime, UNIX_EPOCH};

use crate::profile::ProfileBackend;
use chrono::Utc;
use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct CommandOutput {
    pub stdout: String,
    pub stderr: String,
    pub exit_code: i32,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq, Default)]
pub struct MeshNode {
    #[serde(default)]
    pub endpoint: String,
    #[serde(default)]
    pub profile_auth: BTreeMap<String, String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct MeshConfig {
    pub schema_version: u32,
    pub updated_at: String,
    pub mesh_id: String,
    pub master_node_id: Option<String>,
    pub nodes: BTreeMap<String, MeshNode>,
}

impl Default for MeshConfig {
    fn default() -> Self {
        Self {
            schema_version: 1,
            updated_at: now_rfc3339(),
            mesh_id: "local-mesh".to_string(),
            master_node_id: None,
            nodes: BTreeMap::new(),
        }
    }
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq, Default)]
pub struct MeshStatusNode {
    pub id: String,
    pub endpoint: String,
    pub is_master: bool,
    pub profiles_total: usize,
    pub auth_ok: usize,
    pub auth_expired: usize,
    pub auth_missing: usize,
    pub profile_auth: BTreeMap<String, String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq, Default)]
pub struct MeshAuthTotals {
    pub ok: usize,
    pub expired: usize,
    pub missing: usize,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq, Default)]
pub struct MeshCatalogProfile {
    pub id: String,
    pub name: String,
    pub harness: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq, Default)]
pub struct ProfileCatalogSummary {
    pub total_profiles: usize,
    pub harness_counts: BTreeMap<String, usize>,
    pub profiles: Vec<MeshCatalogProfile>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq, Default)]
pub struct MeshStatus {
    pub config_path: String,
    pub mesh_id: String,
    pub master_node_id: Option<String>,
    pub node_count: usize,
    pub nodes: Vec<MeshStatusNode>,
    pub profile_catalog: ProfileCatalogSummary,
    pub auth_totals: MeshAuthTotals,
}

#[derive(Debug, Clone, PartialEq, Eq)]
enum Command {
    Help,
    Status,
    Catalog,
    Promote {
        node_id: String,
        endpoint: Option<String>,
    },
    Demote {
        node_id: String,
    },
    Provision {
        node_id: String,
    },
    ReportAuth {
        node_id: String,
        profile_id: String,
        status: String,
    },
}

#[derive(Debug, Clone, PartialEq, Eq)]
struct ParsedArgs {
    command: Command,
    json: bool,
    jsonl: bool,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct MeshStore {
    config_path: PathBuf,
}

impl MeshStore {
    #[must_use]
    pub fn open_from_env() -> Self {
        Self {
            config_path: crate::runtime_paths::resolve_data_dir()
                .join("mesh")
                .join("registry.json"),
        }
    }

    #[must_use]
    pub fn with_path(config_path: PathBuf) -> Self {
        Self { config_path }
    }

    pub fn status(&self) -> Result<MeshStatus, String> {
        self.status_with_catalog(&ProfileCatalogSummary::default())
    }

    pub fn status_with_catalog(
        &self,
        profile_catalog: &ProfileCatalogSummary,
    ) -> Result<MeshStatus, String> {
        let config = self.load_or_default()?;
        Ok(self.build_status(&config, profile_catalog))
    }

    pub fn catalog(&self, profile_catalog: &ProfileCatalogSummary) -> ProfileCatalogSummary {
        profile_catalog.clone()
    }

    pub fn promote(&self, node_id: &str, endpoint: Option<&str>) -> Result<MeshStatus, String> {
        validate_node_id(node_id)?;
        let mut config = self.load_or_default()?;
        let node = config.nodes.entry(node_id.to_string()).or_default();
        if let Some(raw_endpoint) = endpoint {
            let trimmed = raw_endpoint.trim();
            if trimmed.is_empty() {
                return Err("mesh promote --endpoint cannot be empty".to_string());
            }
            node.endpoint = trimmed.to_string();
        }
        config.master_node_id = Some(node_id.to_string());
        config.updated_at = now_rfc3339();
        self.write_config(&config)?;
        Ok(self.build_status(&config, &ProfileCatalogSummary::default()))
    }

    pub fn demote(&self, node_id: &str) -> Result<MeshStatus, String> {
        validate_node_id(node_id)?;
        let mut config = self.load_or_default()?;
        if !config.nodes.contains_key(node_id) {
            return Err(format!("node {node_id} not found in mesh registry"));
        }
        if config.master_node_id.as_deref() == Some(node_id) {
            config.master_node_id = None;
            config.updated_at = now_rfc3339();
            self.write_config(&config)?;
        }
        Ok(self.build_status(&config, &ProfileCatalogSummary::default()))
    }

    pub fn provision_node_profiles(
        &self,
        node_id: &str,
        profile_catalog: &ProfileCatalogSummary,
    ) -> Result<MeshStatus, String> {
        validate_node_id(node_id)?;
        let mut config = self.load_or_default()?;
        let node = config.nodes.entry(node_id.to_string()).or_default();
        let valid_ids = profile_catalog
            .profiles
            .iter()
            .map(|profile| profile.id.clone())
            .collect::<std::collections::BTreeSet<_>>();
        node.profile_auth
            .retain(|profile_id, _| valid_ids.contains(profile_id));
        for profile in &profile_catalog.profiles {
            node.profile_auth
                .entry(profile.id.clone())
                .or_insert_with(|| "missing".to_string());
        }
        config.updated_at = now_rfc3339();
        self.write_config(&config)?;
        Ok(self.build_status(&config, profile_catalog))
    }

    pub fn report_node_profile_auth(
        &self,
        node_id: &str,
        profile_id: &str,
        auth_state: &str,
        profile_catalog: &ProfileCatalogSummary,
    ) -> Result<MeshStatus, String> {
        validate_node_id(node_id)?;
        let profile_id = profile_id.trim();
        if profile_id.is_empty() {
            return Err("profile id cannot be empty".to_string());
        }
        let normalized_state = normalize_auth_state(auth_state)?;
        let mut config = self.load_or_default()?;
        let node = config.nodes.entry(node_id.to_string()).or_default();
        node.profile_auth
            .insert(profile_id.to_string(), normalized_state.to_string());
        config.updated_at = now_rfc3339();
        self.write_config(&config)?;
        Ok(self.build_status(&config, profile_catalog))
    }

    fn build_status(
        &self,
        config: &MeshConfig,
        profile_catalog: &ProfileCatalogSummary,
    ) -> MeshStatus {
        let mut totals = MeshAuthTotals::default();
        let mut nodes = Vec::with_capacity(config.nodes.len());
        for (id, node) in &config.nodes {
            let (auth_ok, auth_expired, auth_missing) = summarize_auth_states(&node.profile_auth);
            totals.ok = totals.ok.saturating_add(auth_ok);
            totals.expired = totals.expired.saturating_add(auth_expired);
            totals.missing = totals.missing.saturating_add(auth_missing);
            nodes.push(MeshStatusNode {
                id: id.clone(),
                endpoint: node.endpoint.clone(),
                is_master: config.master_node_id.as_deref() == Some(id.as_str()),
                profiles_total: node.profile_auth.len(),
                auth_ok,
                auth_expired,
                auth_missing,
                profile_auth: node.profile_auth.clone(),
            });
        }
        MeshStatus {
            config_path: self.config_path.display().to_string(),
            mesh_id: config.mesh_id.clone(),
            master_node_id: config.master_node_id.clone(),
            node_count: nodes.len(),
            nodes,
            profile_catalog: profile_catalog.clone(),
            auth_totals: totals,
        }
    }

    fn load_or_default(&self) -> Result<MeshConfig, String> {
        match fs::read_to_string(&self.config_path) {
            Ok(raw) => parse_config(&raw, &self.config_path),
            Err(err) if err.kind() == std::io::ErrorKind::NotFound => Ok(MeshConfig::default()),
            Err(err) => Err(format!("read {}: {err}", self.config_path.display())),
        }
    }

    fn write_config(&self, config: &MeshConfig) -> Result<(), String> {
        if let Some(parent) = self.config_path.parent() {
            fs::create_dir_all(parent)
                .map_err(|err| format!("create directory {}: {err}", parent.display()))?;
        }
        let encoded = serde_json::to_string_pretty(config)
            .map_err(|err| format!("encode mesh config: {err}"))?;
        fs::write(&self.config_path, encoded)
            .map_err(|err| format!("write {}: {err}", self.config_path.display()))
    }
}

pub fn run_with_store(
    args: &[String],
    store: &MeshStore,
    stdout: &mut dyn Write,
    stderr: &mut dyn Write,
) -> i32 {
    match execute(args, store, stdout) {
        Ok(()) => 0,
        Err(err) => {
            let _ = writeln!(stderr, "{err}");
            1
        }
    }
}

pub fn run_for_test(args: &[&str], store: &MeshStore) -> CommandOutput {
    let owned = args
        .iter()
        .map(|item| (*item).to_string())
        .collect::<Vec<_>>();
    let mut stdout = Vec::new();
    let mut stderr = Vec::new();
    let exit_code = run_with_store(&owned, store, &mut stdout, &mut stderr);
    CommandOutput {
        stdout: String::from_utf8_lossy(&stdout).into_owned(),
        stderr: String::from_utf8_lossy(&stderr).into_owned(),
        exit_code,
    }
}

fn execute(args: &[String], store: &MeshStore, stdout: &mut dyn Write) -> Result<(), String> {
    let parsed = parse_args(args)?;
    match parsed.command {
        Command::Help => write_help(stdout).map_err(|err| err.to_string()),
        Command::Status => {
            let catalog = load_profile_catalog_from_env()?;
            let status = store.status_with_catalog(&catalog)?;
            if parsed.json || parsed.jsonl {
                write_json_or_jsonl(stdout, &status, parsed.jsonl)
            } else {
                let master = status.master_node_id.as_deref().unwrap_or("none");
                writeln!(stdout, "mesh={}", status.mesh_id).map_err(|err| err.to_string())?;
                writeln!(stdout, "master={master}").map_err(|err| err.to_string())?;
                writeln!(stdout, "nodes={}", status.node_count).map_err(|err| err.to_string())?;
                writeln!(
                    stdout,
                    "profiles={} auth(ok={}, expired={}, missing={})",
                    status.profile_catalog.total_profiles,
                    status.auth_totals.ok,
                    status.auth_totals.expired,
                    status.auth_totals.missing
                )
                .map_err(|err| err.to_string())?;
                for node in &status.nodes {
                    writeln!(
                        stdout,
                        "{}\t{}\tmaster={} profiles={} ok={} expired={} missing={}",
                        node.id,
                        node.endpoint,
                        node.is_master,
                        node.profiles_total,
                        node.auth_ok,
                        node.auth_expired,
                        node.auth_missing
                    )
                    .map_err(|err| err.to_string())?;
                }
                Ok(())
            }
        }
        Command::Catalog => {
            let catalog = store.catalog(&load_profile_catalog_from_env()?);
            if parsed.json || parsed.jsonl {
                write_json_or_jsonl(stdout, &catalog, parsed.jsonl)
            } else if catalog.profiles.is_empty() {
                writeln!(stdout, "No profiles found").map_err(|err| err.to_string())
            } else {
                writeln!(stdout, "profiles={}", catalog.total_profiles)
                    .map_err(|err| err.to_string())?;
                for profile in &catalog.profiles {
                    writeln!(
                        stdout,
                        "{}\t{}\t{}",
                        profile.id, profile.harness, profile.name
                    )
                    .map_err(|err| err.to_string())?;
                }
                Ok(())
            }
        }
        Command::Promote { node_id, endpoint } => {
            let status = store.promote(&node_id, endpoint.as_deref())?;
            if parsed.json || parsed.jsonl {
                write_json_or_jsonl(stdout, &status, parsed.jsonl)
            } else {
                writeln!(stdout, "master={node_id}").map_err(|err| err.to_string())
            }
        }
        Command::Demote { node_id } => {
            let status = store.demote(&node_id)?;
            if parsed.json || parsed.jsonl {
                write_json_or_jsonl(stdout, &status, parsed.jsonl)
            } else if status.master_node_id.is_some() {
                writeln!(
                    stdout,
                    "node={node_id} demoted; active master={}",
                    status.master_node_id.unwrap_or_default()
                )
                .map_err(|err| err.to_string())
            } else {
                writeln!(stdout, "node={node_id} demoted; active master=none")
                    .map_err(|err| err.to_string())
            }
        }
        Command::Provision { node_id } => {
            let catalog = load_profile_catalog_from_env()?;
            let status = store.provision_node_profiles(&node_id, &catalog)?;
            if parsed.json || parsed.jsonl {
                write_json_or_jsonl(stdout, &status, parsed.jsonl)
            } else {
                let node = status
                    .nodes
                    .iter()
                    .find(|item| item.id == node_id)
                    .ok_or_else(|| format!("node {node_id} missing from mesh status"))?;
                writeln!(
                    stdout,
                    "node={node_id} provisioned profiles={}",
                    node.profiles_total
                )
                .map_err(|err| err.to_string())
            }
        }
        Command::ReportAuth {
            node_id,
            profile_id,
            status: auth_state,
        } => {
            let catalog = load_profile_catalog_from_env()?;
            let status =
                store.report_node_profile_auth(&node_id, &profile_id, &auth_state, &catalog)?;
            if parsed.json || parsed.jsonl {
                write_json_or_jsonl(stdout, &status, parsed.jsonl)
            } else {
                writeln!(
                    stdout,
                    "node={} profile={} auth={}",
                    node_id,
                    profile_id,
                    normalize_auth_state(&auth_state)?
                )
                .map_err(|err| err.to_string())
            }
        }
    }
}

fn parse_args(args: &[String]) -> Result<ParsedArgs, String> {
    let mut index = if args.first().is_some_and(|arg| arg == "mesh") {
        1
    } else {
        0
    };
    let mut json = false;
    let mut jsonl = false;

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
            _ => break,
        }
    }

    if json && jsonl {
        return Err("--json and --jsonl are mutually exclusive".to_string());
    }

    if index >= args.len() {
        return Ok(ParsedArgs {
            command: Command::Help,
            json,
            jsonl,
        });
    }

    let subcommand = args[index].as_str();
    index += 1;
    let command = match subcommand {
        "help" | "-h" | "--help" => Command::Help,
        "status" => {
            if index < args.len() {
                return Err(format!("unknown flag for mesh status: {}", args[index]));
            }
            Command::Status
        }
        "catalog" => {
            if index < args.len() {
                return Err(format!("unknown flag for mesh catalog: {}", args[index]));
            }
            Command::Catalog
        }
        "promote" => {
            let node_id = args
                .get(index)
                .ok_or_else(|| "missing node id for mesh promote".to_string())?
                .trim()
                .to_string();
            index += 1;
            let mut endpoint = None;
            while index < args.len() {
                match args[index].as_str() {
                    "--endpoint" => {
                        index += 1;
                        endpoint = Some(
                            args.get(index)
                                .ok_or_else(|| "missing value for --endpoint".to_string())?
                                .to_string(),
                        );
                        index += 1;
                    }
                    other => return Err(format!("unknown flag for mesh promote: {other}")),
                }
            }
            Command::Promote { node_id, endpoint }
        }
        "demote" => {
            let node_id = args
                .get(index)
                .ok_or_else(|| "missing node id for mesh demote".to_string())?
                .trim()
                .to_string();
            index += 1;
            if index < args.len() {
                return Err(format!("unknown flag for mesh demote: {}", args[index]));
            }
            Command::Demote { node_id }
        }
        "provision" => {
            let node_id = args
                .get(index)
                .ok_or_else(|| "missing node id for mesh provision".to_string())?
                .trim()
                .to_string();
            index += 1;
            if index < args.len() {
                return Err(format!("unknown flag for mesh provision: {}", args[index]));
            }
            Command::Provision { node_id }
        }
        "report-auth" => {
            let node_id = args
                .get(index)
                .ok_or_else(|| "missing node id for mesh report-auth".to_string())?
                .trim()
                .to_string();
            index += 1;
            let profile_id = args
                .get(index)
                .ok_or_else(|| "missing profile id for mesh report-auth".to_string())?
                .trim()
                .to_string();
            index += 1;
            let status = args
                .get(index)
                .ok_or_else(|| "missing status for mesh report-auth".to_string())?
                .trim()
                .to_string();
            index += 1;
            if index < args.len() {
                return Err(format!(
                    "unknown flag for mesh report-auth: {}",
                    args[index]
                ));
            }
            Command::ReportAuth {
                node_id,
                profile_id,
                status,
            }
        }
        other => return Err(format!("unknown mesh subcommand: {other}")),
    };

    Ok(ParsedArgs {
        command,
        json,
        jsonl,
    })
}

fn parse_config(raw: &str, path: &Path) -> Result<MeshConfig, String> {
    serde_json::from_str(raw).map_err(|err| format!("decode {}: {err}", path.display()))
}

fn write_help(stdout: &mut dyn Write) -> std::io::Result<()> {
    writeln!(stdout, "Mesh command family")?;
    writeln!(stdout)?;
    writeln!(stdout, "Usage:")?;
    writeln!(stdout, "  forge mesh status")?;
    writeln!(stdout, "  forge mesh catalog")?;
    writeln!(stdout, "  forge mesh promote <node-id> [--endpoint <addr>]")?;
    writeln!(stdout, "  forge mesh demote <node-id>")?;
    writeln!(stdout, "  forge mesh provision <node-id>")?;
    writeln!(
        stdout,
        "  forge mesh report-auth <node-id> <profile-id> <ok|expired|missing>"
    )?;
    Ok(())
}

fn write_json_or_jsonl<T: Serialize>(
    stdout: &mut dyn Write,
    value: &T,
    jsonl: bool,
) -> Result<(), String> {
    if jsonl {
        let encoded = serde_json::to_string(value).map_err(|err| err.to_string())?;
        writeln!(stdout, "{encoded}").map_err(|err| err.to_string())
    } else {
        let encoded = serde_json::to_string_pretty(value).map_err(|err| err.to_string())?;
        writeln!(stdout, "{encoded}").map_err(|err| err.to_string())
    }
}

fn validate_node_id(node_id: &str) -> Result<(), String> {
    if node_id.trim().is_empty() {
        return Err("node id cannot be empty".to_string());
    }
    Ok(())
}

fn normalize_auth_state(raw: &str) -> Result<&'static str, String> {
    match raw.trim().to_ascii_lowercase().as_str() {
        "ok" => Ok("ok"),
        "expired" => Ok("expired"),
        "missing" => Ok("missing"),
        other => Err(format!(
            "invalid auth state: {other} (expected ok|expired|missing)"
        )),
    }
}

fn summarize_auth_states(profile_auth: &BTreeMap<String, String>) -> (usize, usize, usize) {
    let mut ok = 0usize;
    let mut expired = 0usize;
    let mut missing = 0usize;
    for state in profile_auth.values() {
        match state.as_str() {
            "ok" => ok = ok.saturating_add(1),
            "expired" => expired = expired.saturating_add(1),
            _ => missing = missing.saturating_add(1),
        }
    }
    (ok, expired, missing)
}

fn load_profile_catalog_from_env() -> Result<ProfileCatalogSummary, String> {
    let backend = crate::profile::SqliteProfileBackend::open_from_env();
    let profiles = backend.list_profiles()?;
    let pairs = profiles
        .into_iter()
        .map(|profile| (profile.name, profile.harness))
        .collect::<Vec<_>>();
    Ok(build_profile_catalog(pairs))
}

fn build_profile_catalog<I>(profiles: I) -> ProfileCatalogSummary
where
    I: IntoIterator<Item = (String, String)>,
{
    let mut normalized = profiles
        .into_iter()
        .map(|(name, harness)| (name.trim().to_string(), harness.trim().to_ascii_lowercase()))
        .filter(|(name, harness)| !name.is_empty() && !harness.is_empty())
        .collect::<Vec<_>>();
    normalized.sort_by(|a, b| a.1.cmp(&b.1).then(a.0.cmp(&b.0)));

    let mut harness_counts = BTreeMap::new();
    let mut harness_index = BTreeMap::new();
    let mut catalog_profiles = Vec::new();
    for (name, harness) in normalized {
        let prefix = canonical_profile_prefix(&harness);
        let sequence = harness_index.entry(prefix.clone()).or_insert(0usize);
        *sequence = sequence.saturating_add(1);
        let id = format!("{prefix}{sequence}");
        let count = harness_counts.entry(harness.clone()).or_insert(0usize);
        *count = count.saturating_add(1);
        catalog_profiles.push(MeshCatalogProfile { id, name, harness });
    }

    ProfileCatalogSummary {
        total_profiles: catalog_profiles.len(),
        harness_counts,
        profiles: catalog_profiles,
    }
}

fn canonical_profile_prefix(harness: &str) -> String {
    match harness {
        "claude" => "CC".to_string(),
        "codex" => "Codex".to_string(),
        "opencode" => "OC".to_string(),
        other => {
            let mut chars = other.chars().filter(|value| value.is_ascii_alphanumeric());
            let first = chars.next().unwrap_or('P').to_ascii_uppercase();
            let second = chars.next().unwrap_or('X').to_ascii_uppercase();
            format!("{first}{second}")
        }
    }
}

fn now_rfc3339() -> String {
    Utc::now().to_rfc3339()
}

#[cfg(test)]
fn temp_path(tag: &str) -> PathBuf {
    let nanos = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|value| value.as_nanos())
        .unwrap_or(0);
    std::env::temp_dir()
        .join(format!("forge-mesh-test-{tag}-{nanos}"))
        .join("mesh")
        .join("registry.json")
}

#[cfg(test)]
#[allow(clippy::unwrap_used)]
mod tests {
    use super::{build_profile_catalog, run_for_test, MeshStatus, MeshStore};

    #[test]
    fn status_defaults_to_empty_mesh_registry() {
        let path = super::temp_path("status-default");
        let store = MeshStore::with_path(path);
        let out = run_for_test(&["mesh", "--json", "status"], &store);
        assert_eq!(out.exit_code, 0);
        let status: MeshStatus = serde_json::from_str(out.stdout.trim()).unwrap();
        assert_eq!(status.mesh_id, "local-mesh");
        assert!(status.master_node_id.is_none());
        assert_eq!(status.node_count, 0);
    }

    #[test]
    fn promote_sets_active_master_and_registers_node() {
        let path = super::temp_path("promote-master");
        let store = MeshStore::with_path(path);
        let out = run_for_test(
            &[
                "mesh",
                "--json",
                "promote",
                "node-a",
                "--endpoint",
                "ssh://node-a",
            ],
            &store,
        );
        assert_eq!(out.exit_code, 0);

        let status: MeshStatus = serde_json::from_str(out.stdout.trim()).unwrap();
        assert_eq!(status.master_node_id.as_deref(), Some("node-a"));
        assert_eq!(status.node_count, 1);
        assert_eq!(status.nodes[0].endpoint, "ssh://node-a");
        assert!(status.nodes[0].is_master);
    }

    #[test]
    fn demote_clears_master_when_target_is_active_master() {
        let path = super::temp_path("demote-master");
        let store = MeshStore::with_path(path);
        let _ = run_for_test(&["mesh", "promote", "node-a"], &store);

        let out = run_for_test(&["mesh", "--json", "demote", "node-a"], &store);
        assert_eq!(out.exit_code, 0);
        let status: MeshStatus = serde_json::from_str(out.stdout.trim()).unwrap();
        assert!(status.master_node_id.is_none());
        assert_eq!(status.node_count, 1);
    }

    #[test]
    fn demote_unknown_node_fails() {
        let path = super::temp_path("demote-missing");
        let store = MeshStore::with_path(path);
        let out = run_for_test(&["mesh", "demote", "node-missing"], &store);
        assert_eq!(out.exit_code, 1);
        assert!(out.stderr.contains("not found in mesh registry"));
    }

    #[test]
    fn promote_updates_endpoint_for_existing_node() {
        let path = super::temp_path("promote-update-endpoint");
        let store = MeshStore::with_path(path);
        let _ = run_for_test(&["mesh", "promote", "node-a"], &store);
        let out = run_for_test(
            &[
                "mesh",
                "--json",
                "promote",
                "node-a",
                "--endpoint",
                "ssh://node-a:2222",
            ],
            &store,
        );
        assert_eq!(out.exit_code, 0);
        let status: MeshStatus = serde_json::from_str(out.stdout.trim()).unwrap();
        assert_eq!(status.node_count, 1);
        assert_eq!(status.nodes[0].endpoint, "ssh://node-a:2222");
    }

    #[test]
    fn help_renders_usage() {
        let path = super::temp_path("help");
        let store = MeshStore::with_path(path);
        let out = run_for_test(&["mesh", "help"], &store);
        assert_eq!(out.exit_code, 0);
        assert!(out.stdout.contains("forge mesh status"));
    }

    #[test]
    fn catalog_counts_harnesses_and_assigns_canonical_profile_ids() {
        let catalog = build_profile_catalog(vec![
            ("alpha".to_string(), "claude".to_string()),
            ("delta".to_string(), "claude".to_string()),
            ("beta".to_string(), "codex".to_string()),
            ("gamma".to_string(), "opencode".to_string()),
        ]);
        assert_eq!(catalog.total_profiles, 4);
        assert_eq!(catalog.harness_counts.get("claude"), Some(&2usize));
        assert_eq!(catalog.harness_counts.get("codex"), Some(&1usize));
        assert_eq!(catalog.harness_counts.get("opencode"), Some(&1usize));
        let ids = catalog
            .profiles
            .iter()
            .map(|profile| profile.id.clone())
            .collect::<Vec<_>>();
        assert_eq!(ids, vec!["CC1", "CC2", "Codex1", "OC1"]);
    }

    #[test]
    fn provision_and_report_auth_aggregates_node_and_mesh_auth_status() {
        let path = super::temp_path("provision-auth");
        let store = MeshStore::with_path(path);
        let catalog = build_profile_catalog(vec![
            ("alpha".to_string(), "claude".to_string()),
            ("beta".to_string(), "codex".to_string()),
        ]);

        if let Err(err) = store.provision_node_profiles("node-a", &catalog) {
            panic!("provision node-a: {err}");
        }
        if let Err(err) = store.provision_node_profiles("node-b", &catalog) {
            panic!("provision node-b: {err}");
        }
        if let Err(err) = store.report_node_profile_auth("node-a", "CC1", "ok", &catalog) {
            panic!("report auth node-a CC1: {err}");
        }
        if let Err(err) = store.report_node_profile_auth("node-a", "Codex1", "expired", &catalog) {
            panic!("report auth node-a Codex1: {err}");
        }
        if let Err(err) = store.report_node_profile_auth("node-b", "CC1", "ok", &catalog) {
            panic!("report auth node-b CC1: {err}");
        }

        let status = match store.status_with_catalog(&catalog) {
            Ok(status) => status,
            Err(err) => panic!("status with catalog: {err}"),
        };
        let node_a = status
            .nodes
            .iter()
            .find(|node| node.id == "node-a")
            .unwrap_or_else(|| panic!("node-a status"));
        assert_eq!(node_a.profiles_total, 2);
        assert_eq!(node_a.auth_ok, 1);
        assert_eq!(node_a.auth_expired, 1);
        assert_eq!(node_a.auth_missing, 0);

        let node_b = status
            .nodes
            .iter()
            .find(|node| node.id == "node-b")
            .unwrap_or_else(|| panic!("node-b status"));
        assert_eq!(node_b.profiles_total, 2);
        assert_eq!(node_b.auth_ok, 1);
        assert_eq!(node_b.auth_expired, 0);
        assert_eq!(node_b.auth_missing, 1);

        assert_eq!(status.auth_totals.ok, 2);
        assert_eq!(status.auth_totals.expired, 1);
        assert_eq!(status.auth_totals.missing, 1);
    }

    #[test]
    fn report_auth_rejects_invalid_state() {
        let path = super::temp_path("report-auth-invalid");
        let store = MeshStore::with_path(path);
        let catalog = build_profile_catalog(vec![("alpha".to_string(), "claude".to_string())]);
        if let Err(err) = store.provision_node_profiles("node-a", &catalog) {
            panic!("provision node-a: {err}");
        }
        let err = match store.report_node_profile_auth("node-a", "CC1", "bad", &catalog) {
            Ok(_) => panic!("invalid auth state should fail"),
            Err(err) => err,
        };
        assert!(err.contains("invalid auth state"));
    }

    #[test]
    fn rejects_json_and_jsonl_together() {
        let path = super::temp_path("json-conflict");
        let store = MeshStore::with_path(path);
        let out = run_for_test(&["mesh", "--json", "--jsonl", "status"], &store);
        assert_eq!(out.exit_code, 1);
        assert!(out.stderr.contains("--json and --jsonl are mutually exclusive"));
    }
}
