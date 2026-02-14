use std::collections::BTreeMap;
use std::env;
use std::fs;
use std::io::Write;
use std::path::{Path, PathBuf};
#[cfg(test)]
use std::time::{SystemTime, UNIX_EPOCH};

use chrono::Utc;
use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct CommandOutput {
    pub stdout: String,
    pub stderr: String,
    pub exit_code: i32,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq, Default)]
pub struct RegistryAgentEntry {
    pub harness: String,
    pub profile: String,
    pub source: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq, Default)]
pub struct RegistryPromptEntry {
    pub path: String,
    pub source: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct RegistryDocument {
    pub schema_version: u32,
    pub updated_at: String,
    pub agents: BTreeMap<String, RegistryAgentEntry>,
    pub prompts: BTreeMap<String, RegistryPromptEntry>,
}

impl Default for RegistryDocument {
    fn default() -> Self {
        Self {
            schema_version: 1,
            updated_at: now_rfc3339(),
            agents: BTreeMap::new(),
            prompts: BTreeMap::new(),
        }
    }
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct RegistryStatus {
    pub local_path: String,
    pub repo_path: String,
    pub local_agents: usize,
    pub local_prompts: usize,
    pub repo_agents: usize,
    pub repo_prompts: usize,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct RegistryExportResult {
    pub repo_path: String,
    pub exported_agents: usize,
    pub exported_prompts: usize,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct RegistryImportResult {
    pub local_path: String,
    pub imported_agents: usize,
    pub imported_prompts: usize,
    pub preference: String,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum MergePreference {
    Local,
    Repo,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum RegistryEntryKind {
    Agent,
    Prompt,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum RegistryListKind {
    All,
    Agents,
    Prompts,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
#[serde(tag = "kind", rename_all = "lowercase")]
pub enum RegistryEntry {
    Agent {
        name: String,
        harness: String,
        profile: String,
        source: String,
    },
    Prompt {
        name: String,
        path: String,
        source: String,
    },
}

#[derive(Debug, Clone, PartialEq, Eq)]
enum Command {
    Help,
    Status,
    Export,
    Import {
        preference: MergePreference,
    },
    List {
        kind: RegistryListKind,
    },
    Show {
        kind: RegistryEntryKind,
        name: String,
    },
    UpdateAgent {
        name: String,
        harness: String,
        profile: String,
        source: String,
    },
    UpdatePrompt {
        name: String,
        path: String,
        source: String,
    },
}

#[derive(Debug, Clone, PartialEq, Eq)]
struct ParsedArgs {
    command: Command,
    json: bool,
    jsonl: bool,
    repo_root: Option<PathBuf>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct RegistryStore {
    local_root: PathBuf,
    repo_root: PathBuf,
}

impl RegistryStore {
    pub fn open_from_env(repo_root_override: Option<PathBuf>) -> Result<Self, String> {
        let repo_root = match repo_root_override {
            Some(path) => path,
            None => env::current_dir().map_err(|err| format!("resolve current dir: {err}"))?,
        };
        Ok(Self {
            local_root: crate::runtime_paths::resolve_data_dir().join("registry"),
            repo_root,
        })
    }

    #[must_use]
    pub fn with_paths(local_root: PathBuf, repo_root: PathBuf) -> Self {
        Self {
            local_root,
            repo_root,
        }
    }

    pub fn status(&self) -> Result<RegistryStatus, String> {
        let local = self.load_local_document()?;
        let repo = self.load_repo_document().unwrap_or_default();
        Ok(RegistryStatus {
            local_path: self.local_registry_path().display().to_string(),
            repo_path: self.repo_registry_path().display().to_string(),
            local_agents: local.agents.len(),
            local_prompts: local.prompts.len(),
            repo_agents: repo.agents.len(),
            repo_prompts: repo.prompts.len(),
        })
    }

    pub fn export_to_repo(&self) -> Result<RegistryExportResult, String> {
        let mut local = self.load_local_document()?;
        for (name, entry) in self.scan_repo_prompts()? {
            local.prompts.entry(name).or_insert(entry);
        }
        local.updated_at = now_rfc3339();
        self.write_document(&self.repo_registry_path(), &local)?;
        Ok(RegistryExportResult {
            repo_path: self.repo_registry_path().display().to_string(),
            exported_agents: local.agents.len(),
            exported_prompts: local.prompts.len(),
        })
    }

    fn import_from_repo(
        &self,
        preference: MergePreference,
    ) -> Result<RegistryImportResult, String> {
        let repo = self.load_repo_document()?;
        let local = self.load_local_document()?;
        let merged = merge_documents(&local, &repo, preference);
        self.write_document(&self.local_registry_path(), &merged)?;
        Ok(RegistryImportResult {
            local_path: self.local_registry_path().display().to_string(),
            imported_agents: merged.agents.len(),
            imported_prompts: merged.prompts.len(),
            preference: match preference {
                MergePreference::Local => "local".to_string(),
                MergePreference::Repo => "repo".to_string(),
            },
        })
    }

    fn list_entries(&self, kind: RegistryListKind) -> Result<Vec<RegistryEntry>, String> {
        let doc = self.load_local_document()?;
        let mut entries = Vec::new();
        if matches!(kind, RegistryListKind::All | RegistryListKind::Agents) {
            for (name, entry) in doc.agents {
                entries.push(RegistryEntry::Agent {
                    name,
                    harness: entry.harness,
                    profile: entry.profile,
                    source: entry.source,
                });
            }
        }
        if matches!(kind, RegistryListKind::All | RegistryListKind::Prompts) {
            for (name, entry) in doc.prompts {
                entries.push(RegistryEntry::Prompt {
                    name,
                    path: entry.path,
                    source: entry.source,
                });
            }
        }
        Ok(entries)
    }

    fn show_entry(&self, kind: RegistryEntryKind, name: &str) -> Result<RegistryEntry, String> {
        let trimmed = name.trim();
        if trimmed.is_empty() {
            return Err("registry show requires <name>".to_string());
        }
        let doc = self.load_local_document()?;
        match kind {
            RegistryEntryKind::Agent => {
                let entry = doc
                    .agents
                    .get(trimmed)
                    .ok_or_else(|| format!("agent entry not found: {trimmed}"))?;
                Ok(RegistryEntry::Agent {
                    name: trimmed.to_string(),
                    harness: entry.harness.clone(),
                    profile: entry.profile.clone(),
                    source: entry.source.clone(),
                })
            }
            RegistryEntryKind::Prompt => {
                let entry = doc
                    .prompts
                    .get(trimmed)
                    .ok_or_else(|| format!("prompt entry not found: {trimmed}"))?;
                Ok(RegistryEntry::Prompt {
                    name: trimmed.to_string(),
                    path: entry.path.clone(),
                    source: entry.source.clone(),
                })
            }
        }
    }

    fn update_agent_entry(
        &self,
        name: &str,
        harness: &str,
        profile: &str,
        source: &str,
    ) -> Result<RegistryEntry, String> {
        let name = normalize_required_field(name, "agent name")?;
        let harness = normalize_required_field(harness, "agent harness")?;
        let profile = normalize_required_field(profile, "agent profile")?;
        let source = normalize_or_default(source, "manual");
        let mut doc = self.load_local_document()?;
        doc.agents.insert(
            name.to_string(),
            RegistryAgentEntry {
                harness: harness.to_string(),
                profile: profile.to_string(),
                source: source.to_string(),
            },
        );
        doc.updated_at = now_rfc3339();
        self.write_document(&self.local_registry_path(), &doc)?;
        Ok(RegistryEntry::Agent {
            name: name.to_string(),
            harness: harness.to_string(),
            profile: profile.to_string(),
            source: source.to_string(),
        })
    }

    fn update_prompt_entry(
        &self,
        name: &str,
        path: &str,
        source: &str,
    ) -> Result<RegistryEntry, String> {
        let name = normalize_required_field(name, "prompt name")?;
        let path = normalize_required_field(path, "prompt path")?;
        let source = normalize_or_default(source, "manual");
        let mut doc = self.load_local_document()?;
        doc.prompts.insert(
            name.to_string(),
            RegistryPromptEntry {
                path: path.to_string(),
                source: source.to_string(),
            },
        );
        doc.updated_at = now_rfc3339();
        self.write_document(&self.local_registry_path(), &doc)?;
        Ok(RegistryEntry::Prompt {
            name: name.to_string(),
            path: path.to_string(),
            source: source.to_string(),
        })
    }

    fn scan_repo_prompts(&self) -> Result<BTreeMap<String, RegistryPromptEntry>, String> {
        let prompts_dir = self.repo_root.join(".forge").join("prompts");
        let entries = match fs::read_dir(&prompts_dir) {
            Ok(entries) => entries,
            Err(err) if err.kind() == std::io::ErrorKind::NotFound => return Ok(BTreeMap::new()),
            Err(err) => {
                return Err(format!(
                    "read prompts directory {}: {err}",
                    prompts_dir.display()
                ))
            }
        };

        let mut prompts = BTreeMap::new();
        for entry in entries {
            let entry = entry.map_err(|err| format!("read prompts directory entry: {err}"))?;
            let path = entry.path();
            let is_markdown = path
                .extension()
                .and_then(|value| value.to_str())
                .is_some_and(|ext| ext.eq_ignore_ascii_case("md"));
            if !is_markdown {
                continue;
            }
            let Some(stem) = path.file_stem().and_then(|value| value.to_str()) else {
                continue;
            };
            prompts.insert(
                stem.to_string(),
                RegistryPromptEntry {
                    path: format!(".forge/prompts/{stem}.md"),
                    source: "repo-scan".to_string(),
                },
            );
        }
        Ok(prompts)
    }

    fn load_local_document(&self) -> Result<RegistryDocument, String> {
        load_document_or_default(&self.local_registry_path())
    }

    fn load_repo_document(&self) -> Result<RegistryDocument, String> {
        load_document_required(&self.repo_registry_path())
    }

    fn local_registry_path(&self) -> PathBuf {
        self.local_root.join("registry.json")
    }

    fn repo_registry_path(&self) -> PathBuf {
        self.repo_root
            .join(".forge")
            .join("registry")
            .join("registry.json")
    }

    fn write_document(&self, path: &Path, document: &RegistryDocument) -> Result<(), String> {
        if let Some(parent) = path.parent() {
            fs::create_dir_all(parent)
                .map_err(|err| format!("create directory {}: {err}", parent.display()))?;
        }
        let encoded = serde_json::to_string_pretty(document)
            .map_err(|err| format!("encode registry document: {err}"))?;
        fs::write(path, encoded).map_err(|err| format!("write {}: {err}", path.display()))
    }
}

fn load_document_or_default(path: &Path) -> Result<RegistryDocument, String> {
    match fs::read_to_string(path) {
        Ok(raw) => parse_document(&raw, path),
        Err(err) if err.kind() == std::io::ErrorKind::NotFound => Ok(RegistryDocument::default()),
        Err(err) => Err(format!("read {}: {err}", path.display())),
    }
}

fn load_document_required(path: &Path) -> Result<RegistryDocument, String> {
    let raw = fs::read_to_string(path).map_err(|err| format!("read {}: {err}", path.display()))?;
    parse_document(&raw, path)
}

fn parse_document(raw: &str, path: &Path) -> Result<RegistryDocument, String> {
    serde_json::from_str(raw).map_err(|err| format!("decode {}: {err}", path.display()))
}

fn merge_documents(
    local: &RegistryDocument,
    repo: &RegistryDocument,
    preference: MergePreference,
) -> RegistryDocument {
    let mut merged = RegistryDocument {
        schema_version: local.schema_version.max(repo.schema_version),
        updated_at: now_rfc3339(),
        ..RegistryDocument::default()
    };

    merged.agents = local.agents.clone();
    for (name, entry) in &repo.agents {
        match preference {
            MergePreference::Repo => {
                merged.agents.insert(name.clone(), entry.clone());
            }
            MergePreference::Local => {
                merged
                    .agents
                    .entry(name.clone())
                    .or_insert_with(|| entry.clone());
            }
        }
    }

    merged.prompts = local.prompts.clone();
    for (name, entry) in &repo.prompts {
        match preference {
            MergePreference::Repo => {
                merged.prompts.insert(name.clone(), entry.clone());
            }
            MergePreference::Local => {
                merged
                    .prompts
                    .entry(name.clone())
                    .or_insert_with(|| entry.clone());
            }
        }
    }

    merged
}

pub fn run_with_store(args: &[String], stdout: &mut dyn Write, stderr: &mut dyn Write) -> i32 {
    match execute(args, stdout) {
        Ok(()) => 0,
        Err(err) => {
            let _ = writeln!(stderr, "{err}");
            1
        }
    }
}

pub fn run_for_test(args: &[&str], store: &RegistryStore) -> CommandOutput {
    let owned = args
        .iter()
        .map(|arg| (*arg).to_string())
        .collect::<Vec<_>>();
    let mut stdout = Vec::new();
    let mut stderr = Vec::new();
    let exit_code = execute_with_store(&owned, store, &mut stdout, &mut stderr);
    CommandOutput {
        stdout: String::from_utf8_lossy(&stdout).into_owned(),
        stderr: String::from_utf8_lossy(&stderr).into_owned(),
        exit_code,
    }
}

fn execute(args: &[String], stdout: &mut dyn Write) -> Result<(), String> {
    let parsed = parse_args(args)?;
    let store = RegistryStore::open_from_env(parsed.repo_root.clone())?;
    execute_parsed(parsed, &store, stdout)
}

fn execute_with_store(
    args: &[String],
    store: &RegistryStore,
    stdout: &mut dyn Write,
    stderr: &mut dyn Write,
) -> i32 {
    match parse_args(args).and_then(|parsed| execute_parsed(parsed, store, stdout)) {
        Ok(()) => 0,
        Err(err) => {
            let _ = writeln!(stderr, "{err}");
            1
        }
    }
}

fn execute_parsed(
    parsed: ParsedArgs,
    store: &RegistryStore,
    stdout: &mut dyn Write,
) -> Result<(), String> {
    match parsed.command {
        Command::Help => write_help(stdout).map_err(|err| err.to_string()),
        Command::Status => {
            let status = store.status()?;
            if parsed.json || parsed.jsonl {
                write_json_or_jsonl(stdout, &status, parsed.jsonl)
            } else {
                writeln!(stdout, "Local: {}", status.local_path).map_err(|err| err.to_string())?;
                writeln!(
                    stdout,
                    "  agents={} prompts={}",
                    status.local_agents, status.local_prompts
                )
                .map_err(|err| err.to_string())?;
                writeln!(stdout, "Repo: {}", status.repo_path).map_err(|err| err.to_string())?;
                writeln!(
                    stdout,
                    "  agents={} prompts={}",
                    status.repo_agents, status.repo_prompts
                )
                .map_err(|err| err.to_string())
            }
        }
        Command::Export => {
            let result = store.export_to_repo()?;
            if parsed.json || parsed.jsonl {
                write_json_or_jsonl(stdout, &result, parsed.jsonl)
            } else {
                writeln!(
                    stdout,
                    "Exported registry to {} (agents={}, prompts={})",
                    result.repo_path, result.exported_agents, result.exported_prompts
                )
                .map_err(|err| err.to_string())
            }
        }
        Command::Import { preference } => {
            let result = store.import_from_repo(preference)?;
            if parsed.json || parsed.jsonl {
                write_json_or_jsonl(stdout, &result, parsed.jsonl)
            } else {
                writeln!(
                    stdout,
                    "Imported registry to {} (agents={}, prompts={}, preference={})",
                    result.local_path,
                    result.imported_agents,
                    result.imported_prompts,
                    result.preference
                )
                .map_err(|err| err.to_string())
            }
        }
        Command::List { kind } => {
            let entries = store.list_entries(kind)?;
            if parsed.json || parsed.jsonl {
                write_json_or_jsonl(stdout, &entries, parsed.jsonl)
            } else if entries.is_empty() {
                writeln!(stdout, "No registry entries").map_err(|err| err.to_string())
            } else {
                for entry in entries {
                    match entry {
                        RegistryEntry::Agent {
                            name,
                            harness,
                            profile,
                            source,
                        } => writeln!(
                            stdout,
                            "agent\t{name}\tharness={harness}\tprofile={profile}\tsource={source}"
                        )
                        .map_err(|err| err.to_string())?,
                        RegistryEntry::Prompt { name, path, source } => {
                            writeln!(stdout, "prompt\t{name}\tpath={path}\tsource={source}")
                                .map_err(|err| err.to_string())?
                        }
                    }
                }
                Ok(())
            }
        }
        Command::Show { kind, name } => {
            let entry = store.show_entry(kind, &name)?;
            if parsed.json || parsed.jsonl {
                write_json_or_jsonl(stdout, &entry, parsed.jsonl)
            } else {
                match entry {
                    RegistryEntry::Agent {
                        name,
                        harness,
                        profile,
                        source,
                    } => writeln!(
                        stdout,
                        "agent\t{name}\tharness={harness}\tprofile={profile}\tsource={source}"
                    )
                    .map_err(|err| err.to_string()),
                    RegistryEntry::Prompt { name, path, source } => {
                        writeln!(stdout, "prompt\t{name}\tpath={path}\tsource={source}")
                            .map_err(|err| err.to_string())
                    }
                }
            }
        }
        Command::UpdateAgent {
            name,
            harness,
            profile,
            source,
        } => {
            let entry = store.update_agent_entry(&name, &harness, &profile, &source)?;
            if parsed.json || parsed.jsonl {
                write_json_or_jsonl(stdout, &entry, parsed.jsonl)
            } else {
                writeln!(stdout, "Updated agent entry: {name}").map_err(|err| err.to_string())
            }
        }
        Command::UpdatePrompt { name, path, source } => {
            let entry = store.update_prompt_entry(&name, &path, &source)?;
            if parsed.json || parsed.jsonl {
                write_json_or_jsonl(stdout, &entry, parsed.jsonl)
            } else {
                writeln!(stdout, "Updated prompt entry: {name}").map_err(|err| err.to_string())
            }
        }
    }
}

fn parse_args(args: &[String]) -> Result<ParsedArgs, String> {
    let mut index = if args.first().is_some_and(|arg| arg == "registry") {
        1
    } else {
        0
    };
    let mut json = false;
    let mut jsonl = false;
    let mut repo_root: Option<PathBuf> = None;

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

    if index >= args.len() {
        return Ok(ParsedArgs {
            command: Command::Help,
            json,
            jsonl,
            repo_root,
        });
    }

    let sub = args[index].as_str();
    index += 1;
    let command = match sub {
        "help" | "-h" | "--help" => Command::Help,
        "status" => {
            while index < args.len() {
                match args[index].as_str() {
                    "--repo" => {
                        index += 1;
                        repo_root = Some(PathBuf::from(
                            args.get(index)
                                .ok_or_else(|| "missing value for --repo".to_string())?,
                        ));
                        index += 1;
                    }
                    other => return Err(format!("unknown flag for registry status: {other}")),
                }
            }
            Command::Status
        }
        "export" => {
            while index < args.len() {
                match args[index].as_str() {
                    "--repo" => {
                        index += 1;
                        repo_root = Some(PathBuf::from(
                            args.get(index)
                                .ok_or_else(|| "missing value for --repo".to_string())?,
                        ));
                        index += 1;
                    }
                    other => return Err(format!("unknown flag for registry export: {other}")),
                }
            }
            Command::Export
        }
        "import" => {
            let mut preference = MergePreference::Local;
            while index < args.len() {
                match args[index].as_str() {
                    "--repo" => {
                        index += 1;
                        repo_root = Some(PathBuf::from(
                            args.get(index)
                                .ok_or_else(|| "missing value for --repo".to_string())?,
                        ));
                        index += 1;
                    }
                    "--prefer" => {
                        index += 1;
                        let raw = args
                            .get(index)
                            .ok_or_else(|| "missing value for --prefer".to_string())?
                            .to_ascii_lowercase();
                        preference = match raw.as_str() {
                            "local" => MergePreference::Local,
                            "repo" => MergePreference::Repo,
                            _ => return Err(format!("invalid --prefer value: {raw}")),
                        };
                        index += 1;
                    }
                    other => return Err(format!("unknown flag for registry import: {other}")),
                }
            }
            Command::Import { preference }
        }
        "ls" | "list" => {
            let mut kind = RegistryListKind::All;
            let mut scope_seen = false;
            while index < args.len() {
                match args[index].as_str() {
                    "--repo" => {
                        index += 1;
                        repo_root = Some(PathBuf::from(
                            args.get(index)
                                .ok_or_else(|| "missing value for --repo".to_string())?,
                        ));
                        index += 1;
                    }
                    token => {
                        if scope_seen {
                            return Err(
                                "usage: forge registry ls [all|agents|prompts] [--repo <path>]"
                                    .to_string(),
                            );
                        }
                        kind = parse_registry_list_kind(token)?;
                        scope_seen = true;
                        index += 1;
                    }
                }
            }
            Command::List { kind }
        }
        "show" => {
            let kind =
                parse_registry_entry_kind(args.get(index).ok_or_else(|| {
                    "usage: forge registry show <agent|prompt> <name>".to_string()
                })?)?;
            index += 1;
            let name = args
                .get(index)
                .ok_or_else(|| "usage: forge registry show <agent|prompt> <name>".to_string())?
                .to_string();
            index += 1;
            while index < args.len() {
                match args[index].as_str() {
                    "--repo" => {
                        index += 1;
                        repo_root = Some(PathBuf::from(
                            args.get(index)
                                .ok_or_else(|| "missing value for --repo".to_string())?,
                        ));
                        index += 1;
                    }
                    other => return Err(format!("unknown flag for registry show: {other}")),
                }
            }
            Command::Show { kind, name }
        }
        "update" => {
            let kind = parse_registry_entry_kind(args.get(index).ok_or_else(|| {
                "usage: forge registry update <agent|prompt> <name> [flags]".to_string()
            })?)?;
            index += 1;
            let name = args
                .get(index)
                .ok_or_else(|| {
                    "usage: forge registry update <agent|prompt> <name> [flags]".to_string()
                })?
                .to_string();
            index += 1;

            match kind {
                RegistryEntryKind::Agent => {
                    let mut harness = String::new();
                    let mut profile = String::new();
                    let mut source = "manual".to_string();
                    while index < args.len() {
                        match args[index].as_str() {
                            "--harness" => {
                                index += 1;
                                harness = args
                                    .get(index)
                                    .ok_or_else(|| "missing value for --harness".to_string())?
                                    .to_string();
                                index += 1;
                            }
                            "--profile" => {
                                index += 1;
                                profile = args
                                    .get(index)
                                    .ok_or_else(|| "missing value for --profile".to_string())?
                                    .to_string();
                                index += 1;
                            }
                            "--source" => {
                                index += 1;
                                source = args
                                    .get(index)
                                    .ok_or_else(|| "missing value for --source".to_string())?
                                    .to_string();
                                index += 1;
                            }
                            "--repo" => {
                                index += 1;
                                repo_root = Some(PathBuf::from(
                                    args.get(index)
                                        .ok_or_else(|| "missing value for --repo".to_string())?,
                                ));
                                index += 1;
                            }
                            other => {
                                return Err(format!(
                                    "unknown flag for registry update agent: {other}"
                                ))
                            }
                        }
                    }
                    Command::UpdateAgent {
                        name,
                        harness,
                        profile,
                        source,
                    }
                }
                RegistryEntryKind::Prompt => {
                    let mut path = String::new();
                    let mut source = "manual".to_string();
                    while index < args.len() {
                        match args[index].as_str() {
                            "--path" => {
                                index += 1;
                                path = args
                                    .get(index)
                                    .ok_or_else(|| "missing value for --path".to_string())?
                                    .to_string();
                                index += 1;
                            }
                            "--source" => {
                                index += 1;
                                source = args
                                    .get(index)
                                    .ok_or_else(|| "missing value for --source".to_string())?
                                    .to_string();
                                index += 1;
                            }
                            "--repo" => {
                                index += 1;
                                repo_root = Some(PathBuf::from(
                                    args.get(index)
                                        .ok_or_else(|| "missing value for --repo".to_string())?,
                                ));
                                index += 1;
                            }
                            other => {
                                return Err(format!(
                                    "unknown flag for registry update prompt: {other}"
                                ));
                            }
                        }
                    }
                    Command::UpdatePrompt { name, path, source }
                }
            }
        }
        other => return Err(format!("unknown registry subcommand: {other}")),
    };

    Ok(ParsedArgs {
        command,
        json,
        jsonl,
        repo_root,
    })
}

fn parse_registry_entry_kind(raw: &str) -> Result<RegistryEntryKind, String> {
    match raw.trim().to_ascii_lowercase().as_str() {
        "agent" | "agents" => Ok(RegistryEntryKind::Agent),
        "prompt" | "prompts" => Ok(RegistryEntryKind::Prompt),
        other => Err(format!(
            "invalid registry entry kind: {other} (expected agent|prompt)"
        )),
    }
}

fn parse_registry_list_kind(raw: &str) -> Result<RegistryListKind, String> {
    match raw.trim().to_ascii_lowercase().as_str() {
        "all" => Ok(RegistryListKind::All),
        "agent" | "agents" => Ok(RegistryListKind::Agents),
        "prompt" | "prompts" => Ok(RegistryListKind::Prompts),
        other => Err(format!(
            "invalid registry list kind: {other} (expected all|agents|prompts)"
        )),
    }
}

fn normalize_required_field<'a>(value: &'a str, field: &str) -> Result<&'a str, String> {
    let trimmed = value.trim();
    if trimmed.is_empty() {
        return Err(format!("{field} is required"));
    }
    Ok(trimmed)
}

fn normalize_or_default<'a>(value: &'a str, default: &'a str) -> &'a str {
    let trimmed = value.trim();
    if trimmed.is_empty() {
        default
    } else {
        trimmed
    }
}

fn write_help(stdout: &mut dyn Write) -> std::io::Result<()> {
    writeln!(stdout, "Registry command family")?;
    writeln!(stdout)?;
    writeln!(stdout, "Usage:")?;
    writeln!(stdout, "  forge registry status [--repo <path>]")?;
    writeln!(stdout, "  forge registry export [--repo <path>]")?;
    writeln!(
        stdout,
        "  forge registry import [--repo <path>] [--prefer local|repo]"
    )?;
    writeln!(stdout, "  forge registry ls [all|agents|prompts]")?;
    writeln!(stdout, "  forge registry show <agent|prompt> <name>")?;
    writeln!(
        stdout,
        "  forge registry update agent <name> --harness <h> --profile <p> [--source <s>]"
    )?;
    writeln!(
        stdout,
        "  forge registry update prompt <name> --path <path> [--source <s>]"
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

fn now_rfc3339() -> String {
    Utc::now().to_rfc3339()
}

#[cfg(test)]
fn temp_paths(tag: &str) -> (PathBuf, PathBuf) {
    let nanos = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|value| value.as_nanos())
        .unwrap_or(0);
    let root = env::temp_dir().join(format!("forge-registry-test-{tag}-{nanos}"));
    (root.join("local"), root.join("repo"))
}

#[cfg(test)]
mod tests {
    use super::{
        merge_documents, run_for_test, MergePreference, RegistryAgentEntry, RegistryDocument,
        RegistryPromptEntry, RegistryStore,
    };

    fn cleanup(store: &RegistryStore) {
        let _ = std::fs::remove_dir_all(&store.local_root);
        let _ = std::fs::remove_dir_all(&store.repo_root);
    }

    fn seed_store() -> RegistryStore {
        let (local_root, repo_root) = super::temp_paths("seed");
        RegistryStore::with_paths(local_root, repo_root)
    }

    #[test]
    fn export_writes_commit_friendly_registry_file() {
        let store = seed_store();
        let repo_prompts_dir = store.repo_root.join(".forge").join("prompts");
        if let Err(err) = std::fs::create_dir_all(&repo_prompts_dir) {
            panic!("failed to create repo prompts dir: {err}");
        }
        if let Err(err) = std::fs::write(repo_prompts_dir.join("review.md"), "# review prompt\n") {
            panic!("failed to write prompt fixture: {err}");
        }

        let out = run_for_test(&["registry", "export"], &store);
        assert_eq!(out.exit_code, 0, "stderr={}", out.stderr);
        assert!(out.stdout.contains("Exported registry"));

        let status = match store.status() {
            Ok(status) => status,
            Err(err) => panic!("failed to load registry status: {err}"),
        };
        assert!(status.repo_prompts >= 1);
        cleanup(&store);
    }

    #[test]
    fn import_merges_with_local_preference_by_default() {
        let mut local = RegistryDocument::default();
        local.agents.insert(
            "alpha".to_string(),
            RegistryAgentEntry {
                harness: "codex".to_string(),
                profile: "local".to_string(),
                source: "local".to_string(),
            },
        );

        let mut repo = RegistryDocument::default();
        repo.agents.insert(
            "alpha".to_string(),
            RegistryAgentEntry {
                harness: "claude".to_string(),
                profile: "repo".to_string(),
                source: "repo".to_string(),
            },
        );

        let merged = merge_documents(&local, &repo, MergePreference::Local);
        assert_eq!(merged.agents["alpha"].harness, "codex");
    }

    #[test]
    fn import_merges_with_repo_preference_when_requested() {
        let mut local = RegistryDocument::default();
        local.prompts.insert(
            "triage".to_string(),
            RegistryPromptEntry {
                path: ".forge/prompts/triage-local.md".to_string(),
                source: "local".to_string(),
            },
        );
        let mut repo = RegistryDocument::default();
        repo.prompts.insert(
            "triage".to_string(),
            RegistryPromptEntry {
                path: ".forge/prompts/triage-repo.md".to_string(),
                source: "repo".to_string(),
            },
        );
        let merged = merge_documents(&local, &repo, MergePreference::Repo);
        assert_eq!(
            merged.prompts["triage"].path,
            ".forge/prompts/triage-repo.md"
        );
    }

    #[test]
    fn status_command_supports_json() {
        let (local_root, repo_root) = super::temp_paths("status");
        let store = RegistryStore::with_paths(local_root, repo_root);
        let out = run_for_test(&["registry", "--json", "status"], &store);
        assert_eq!(out.exit_code, 0, "stderr={}", out.stderr);
        assert!(out.stdout.contains("\"local_agents\""));
        cleanup(&store);
    }

    #[test]
    fn update_agent_then_show_round_trip() {
        let store = seed_store();
        let update = run_for_test(
            &[
                "registry",
                "update",
                "agent",
                "codex-main",
                "--harness",
                "codex",
                "--profile",
                "default",
            ],
            &store,
        );
        assert_eq!(update.exit_code, 0, "stderr={}", update.stderr);
        assert!(update.stdout.contains("Updated agent entry"));

        let show = run_for_test(
            &["registry", "--json", "show", "agent", "codex-main"],
            &store,
        );
        assert_eq!(show.exit_code, 0, "stderr={}", show.stderr);
        assert!(show.stdout.contains("\"kind\": \"agent\""));
        assert!(show.stdout.contains("\"harness\": \"codex\""));
        assert!(show.stdout.contains("\"profile\": \"default\""));
        cleanup(&store);
    }

    #[test]
    fn list_filters_agents_and_prompts() {
        let store = seed_store();
        let _ = run_for_test(
            &[
                "registry",
                "update",
                "agent",
                "codex-main",
                "--harness",
                "codex",
                "--profile",
                "default",
            ],
            &store,
        );
        let _ = run_for_test(
            &[
                "registry",
                "update",
                "prompt",
                "triage",
                "--path",
                ".forge/prompts/triage.md",
            ],
            &store,
        );

        let agents = run_for_test(&["registry", "ls", "agents"], &store);
        assert_eq!(agents.exit_code, 0, "stderr={}", agents.stderr);
        assert!(agents.stdout.contains("agent\tcodex-main"));
        assert!(!agents.stdout.contains("prompt\ttriage"));

        let prompts = run_for_test(&["registry", "ls", "prompts"], &store);
        assert_eq!(prompts.exit_code, 0, "stderr={}", prompts.stderr);
        assert!(prompts.stdout.contains("prompt\ttriage"));
        assert!(!prompts.stdout.contains("agent\tcodex-main"));
        cleanup(&store);
    }

    #[test]
    fn update_prompt_requires_path() {
        let store = seed_store();
        let out = run_for_test(&["registry", "update", "prompt", "triage"], &store);
        assert_eq!(out.exit_code, 1);
        assert!(out.stderr.contains("prompt path is required"));
        cleanup(&store);
    }

    #[test]
    fn list_rejects_multiple_scope_tokens() {
        let store = seed_store();
        let out = run_for_test(&["registry", "ls", "agents", "prompts"], &store);
        assert_eq!(out.exit_code, 1);
        assert!(out.stderr.contains("usage: forge registry ls [all|agents|prompts]"));
        cleanup(&store);
    }
}
