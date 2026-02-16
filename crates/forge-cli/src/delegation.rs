use std::collections::BTreeMap;
use std::io::Write;
use std::path::PathBuf;

use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct CommandOutput {
    pub stdout: String,
    pub stderr: String,
    pub exit_code: i32,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq, Default)]
pub struct DelegationRuleSet {
    #[serde(default)]
    pub default_agent: String,
    #[serde(default)]
    pub default_prompt: String,
    #[serde(default)]
    pub rules: Vec<DelegationRule>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq, Default)]
pub struct DelegationRule {
    #[serde(default)]
    pub id: String,
    #[serde(default = "default_enabled")]
    pub enabled: bool,
    #[serde(default)]
    pub priority: i64,
    #[serde(default)]
    pub payload_type: String,
    #[serde(default)]
    pub repo: String,
    #[serde(default)]
    pub min_priority: i64,
    #[serde(default)]
    pub tags_any: Vec<String>,
    #[serde(default)]
    pub paths_any: Vec<String>,
    #[serde(default)]
    pub target_agent: String,
    #[serde(default)]
    pub target_prompt: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq, Default)]
pub struct DelegationPayload {
    #[serde(default, rename = "type", alias = "payload_type")]
    pub payload_type: String,
    #[serde(default)]
    pub repo: String,
    #[serde(default)]
    pub priority: i64,
    #[serde(default)]
    pub tags: Vec<String>,
    #[serde(default)]
    pub paths: Vec<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct RuleExplain {
    pub rule_id: String,
    pub matched: bool,
    pub reasons: Vec<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct DelegationDecision {
    pub source: String,
    pub target_agent: String,
    pub target_prompt: Option<String>,
    pub matched_rule_id: Option<String>,
    pub checks: Vec<RuleExplain>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct TeamDelegationConfig {
    pub team_reference: String,
    pub delegation_rules_json: String,
    pub default_assignee: String,
    pub leader_agent_id: String,
}

pub trait DelegationBackend {
    fn load_team_config(&self, team_reference: &str) -> Result<TeamDelegationConfig, String>;
}

#[derive(Debug, Clone)]
pub struct SqliteDelegationBackend {
    db_path: PathBuf,
}

impl SqliteDelegationBackend {
    #[must_use]
    pub fn open_from_env() -> Self {
        Self {
            db_path: crate::runtime_paths::resolve_database_path(),
        }
    }
}

impl DelegationBackend for SqliteDelegationBackend {
    fn load_team_config(&self, team_reference: &str) -> Result<TeamDelegationConfig, String> {
        let db = forge_db::Db::open(forge_db::Config::new(&self.db_path))
            .map_err(|err| format!("open db {}: {err}", self.db_path.display()))?;
        let service = forge_db::team_repository::TeamService::new(&db);
        let team = service
            .show_team(team_reference.trim())
            .map_err(|err| format!("load team {team_reference:?}: {err}"))?;
        let leader_agent_id = service
            .list_members(&team.id)
            .map_err(|err| format!("load team members for {}: {err}", team.id))?
            .into_iter()
            .find(|member| member.role == "leader")
            .map(|member| member.agent_id)
            .unwrap_or_default();

        Ok(TeamDelegationConfig {
            team_reference: if team.name.trim().is_empty() {
                team.id
            } else {
                team.name
            },
            delegation_rules_json: team.delegation_rules_json,
            default_assignee: team.default_assignee,
            leader_agent_id,
        })
    }
}

#[derive(Debug, Clone, Default)]
pub struct InMemoryDelegationBackend {
    team_configs: BTreeMap<String, TeamDelegationConfig>,
}

impl InMemoryDelegationBackend {
    #[must_use]
    pub fn with_team_config(mut self, config: TeamDelegationConfig) -> Self {
        self.team_configs
            .insert(config.team_reference.to_ascii_lowercase(), config);
        self
    }
}

impl DelegationBackend for InMemoryDelegationBackend {
    fn load_team_config(&self, team_reference: &str) -> Result<TeamDelegationConfig, String> {
        self.team_configs
            .get(&team_reference.trim().to_ascii_lowercase())
            .cloned()
            .ok_or_else(|| format!("team not found: {team_reference}"))
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
enum Command {
    Help,
    Route {
        explain: bool,
        payload_json: String,
        rules_json: Option<String>,
        team_reference: Option<String>,
    },
}

#[derive(Debug, Clone, PartialEq, Eq)]
struct ParsedArgs {
    command: Command,
    json: bool,
    jsonl: bool,
}

pub fn run_with_backend(
    args: &[String],
    backend: &dyn DelegationBackend,
    stdout: &mut dyn Write,
    stderr: &mut dyn Write,
) -> i32 {
    match execute(args, backend, stdout) {
        Ok(()) => 0,
        Err(err) => {
            let _ = writeln!(stderr, "{err}");
            1
        }
    }
}

pub fn run_for_test(args: &[&str], backend: &dyn DelegationBackend) -> CommandOutput {
    let owned = args
        .iter()
        .map(|item| (*item).to_string())
        .collect::<Vec<_>>();
    let mut stdout = Vec::new();
    let mut stderr = Vec::new();
    let exit_code = run_with_backend(&owned, backend, &mut stdout, &mut stderr);
    CommandOutput {
        stdout: String::from_utf8_lossy(&stdout).into_owned(),
        stderr: String::from_utf8_lossy(&stderr).into_owned(),
        exit_code,
    }
}

fn execute(
    args: &[String],
    backend: &dyn DelegationBackend,
    stdout: &mut dyn Write,
) -> Result<(), String> {
    let parsed = parse_args(args)?;
    match parsed.command {
        Command::Help => write_help(stdout).map_err(|err| err.to_string()),
        Command::Route {
            explain,
            payload_json,
            rules_json,
            team_reference,
        } => {
            let payload = parse_payload(&payload_json)?;
            let (ruleset, source) = load_ruleset(backend, rules_json, team_reference)?;
            let decision = evaluate_ruleset(&ruleset, &payload, &source);
            if parsed.json || parsed.jsonl {
                write_json_or_jsonl(stdout, &decision, parsed.jsonl)
            } else {
                for line in render_decision_lines(&decision, explain) {
                    writeln!(stdout, "{line}").map_err(|err| err.to_string())?;
                }
                Ok(())
            }
        }
    }
}

fn parse_args(args: &[String]) -> Result<ParsedArgs, String> {
    let mut index = if args.first().is_some_and(|arg| arg == "delegation") {
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
        "route" | "explain" => parse_route_args(args, index, subcommand == "explain")?,
        other => return Err(format!("unknown delegation subcommand: {other}")),
    };
    Ok(ParsedArgs {
        command,
        json,
        jsonl,
    })
}

fn parse_route_args(args: &[String], mut index: usize, explain: bool) -> Result<Command, String> {
    let mut payload_json = String::new();
    let mut rules_json = None;
    let mut team_reference = None;

    while index < args.len() {
        match args[index].as_str() {
            "--payload" => {
                index += 1;
                payload_json = args
                    .get(index)
                    .ok_or_else(|| "missing value for --payload".to_string())?
                    .to_string();
                index += 1;
            }
            "--rules" => {
                index += 1;
                rules_json = Some(
                    args.get(index)
                        .ok_or_else(|| "missing value for --rules".to_string())?
                        .to_string(),
                );
                index += 1;
            }
            "--team" => {
                index += 1;
                team_reference = Some(
                    args.get(index)
                        .ok_or_else(|| "missing value for --team".to_string())?
                        .to_string(),
                );
                index += 1;
            }
            other => return Err(format!("unknown flag for delegation route: {other}")),
        }
    }

    if payload_json.trim().is_empty() {
        return Err(
            "usage: forge delegation route --payload <json> [--rules <json> | --team <id>]"
                .to_string(),
        );
    }
    if rules_json.is_none() && team_reference.is_none() {
        return Err("either --rules or --team is required".to_string());
    }
    if rules_json.is_some() && team_reference.is_some() {
        return Err("use either --rules or --team, not both".to_string());
    }

    Ok(Command::Route {
        explain,
        payload_json,
        rules_json,
        team_reference,
    })
}

fn parse_payload(raw: &str) -> Result<DelegationPayload, String> {
    let payload: DelegationPayload =
        serde_json::from_str(raw).map_err(|err| format!("decode payload json: {err}"))?;
    if payload.payload_type.trim().is_empty() {
        return Err("payload.type is required".to_string());
    }
    Ok(payload)
}

fn load_ruleset(
    backend: &dyn DelegationBackend,
    rules_json: Option<String>,
    team_reference: Option<String>,
) -> Result<(DelegationRuleSet, String), String> {
    if let Some(rules_json) = rules_json {
        let ruleset = parse_ruleset(&rules_json)?;
        return Ok((ruleset, "inline-rules".to_string()));
    }

    let team_reference = team_reference
        .as_deref()
        .map(str::trim)
        .filter(|value| !value.is_empty())
        .ok_or_else(|| "team reference is required".to_string())?;
    let config = backend.load_team_config(team_reference)?;
    let mut ruleset = parse_ruleset_object_or_empty(&config.delegation_rules_json)?;
    if ruleset.default_agent.trim().is_empty() {
        ruleset.default_agent = config.default_assignee.trim().to_string();
    }
    if ruleset.default_agent.trim().is_empty() {
        ruleset.default_agent = config.leader_agent_id.trim().to_string();
    }
    Ok((ruleset, format!("team:{}", config.team_reference)))
}

fn parse_ruleset(raw: &str) -> Result<DelegationRuleSet, String> {
    let parsed: DelegationRuleSet =
        serde_json::from_str(raw).map_err(|err| format!("decode rules json: {err}"))?;
    Ok(normalize_ruleset(parsed))
}

fn parse_ruleset_object_or_empty(raw: &str) -> Result<DelegationRuleSet, String> {
    if raw.trim().is_empty() {
        return Ok(DelegationRuleSet::default());
    }
    parse_ruleset(raw)
}

fn normalize_ruleset(mut ruleset: DelegationRuleSet) -> DelegationRuleSet {
    ruleset.default_agent = ruleset.default_agent.trim().to_string();
    ruleset.default_prompt = ruleset.default_prompt.trim().to_string();
    for (index, rule) in ruleset.rules.iter_mut().enumerate() {
        if rule.id.trim().is_empty() {
            rule.id = format!("rule-{}", index + 1);
        } else {
            rule.id = rule.id.trim().to_string();
        }
        rule.payload_type = normalize_token(&rule.payload_type);
        rule.repo = normalize_token(&rule.repo);
        rule.target_agent = rule.target_agent.trim().to_string();
        rule.target_prompt = rule.target_prompt.trim().to_string();
        rule.tags_any = normalize_many(&rule.tags_any);
        rule.paths_any = rule
            .paths_any
            .iter()
            .map(|value| value.trim().to_string())
            .filter(|value| !value.is_empty())
            .collect();
    }
    ruleset
}

#[must_use]
pub fn evaluate_ruleset(
    ruleset: &DelegationRuleSet,
    payload: &DelegationPayload,
    source: &str,
) -> DelegationDecision {
    let normalized_payload = normalize_payload(payload);
    let mut ordered = ruleset.rules.clone();
    ordered.sort_by(|left, right| {
        right
            .priority
            .cmp(&left.priority)
            .then_with(|| left.id.cmp(&right.id))
    });

    let mut checks = Vec::new();
    for rule in &ordered {
        let (matched, reasons) = evaluate_rule(rule, &normalized_payload);
        checks.push(RuleExplain {
            rule_id: rule.id.clone(),
            matched,
            reasons,
        });
        if matched {
            return DelegationDecision {
                source: source.to_string(),
                target_agent: fallback_target_agent(rule, ruleset),
                target_prompt: fallback_target_prompt(rule, ruleset),
                matched_rule_id: Some(rule.id.clone()),
                checks,
            };
        }
    }

    DelegationDecision {
        source: source.to_string(),
        target_agent: if ruleset.default_agent.trim().is_empty() {
            "unassigned".to_string()
        } else {
            ruleset.default_agent.trim().to_string()
        },
        target_prompt: if ruleset.default_prompt.trim().is_empty() {
            None
        } else {
            Some(ruleset.default_prompt.trim().to_string())
        },
        matched_rule_id: None,
        checks,
    }
}

fn evaluate_rule(rule: &DelegationRule, payload: &DelegationPayload) -> (bool, Vec<String>) {
    if !rule.enabled {
        return (false, vec!["disabled".to_string()]);
    }

    let mut reasons = Vec::new();
    let mut matched = true;

    if !rule.payload_type.is_empty() && rule.payload_type != payload.payload_type {
        matched = false;
        reasons.push(format!(
            "payload_type mismatch expected={} got={}",
            rule.payload_type, payload.payload_type
        ));
    } else if !rule.payload_type.is_empty() {
        reasons.push(format!("payload_type={}", payload.payload_type));
    }

    if !rule.repo.is_empty() && rule.repo != payload.repo {
        matched = false;
        reasons.push(format!(
            "repo mismatch expected={} got={}",
            rule.repo, payload.repo
        ));
    } else if !rule.repo.is_empty() {
        reasons.push(format!("repo={}", payload.repo));
    }

    if payload.priority < rule.min_priority {
        matched = false;
        reasons.push(format!(
            "priority too low min={} got={}",
            rule.min_priority, payload.priority
        ));
    } else if rule.min_priority > 0 {
        reasons.push(format!("priority>={}", rule.min_priority));
    }

    if !rule.tags_any.is_empty() {
        let intersection = payload
            .tags
            .iter()
            .filter(|tag| rule.tags_any.contains(*tag))
            .cloned()
            .collect::<Vec<_>>();
        if intersection.is_empty() {
            matched = false;
            reasons.push(format!(
                "tags no match required_any={}",
                rule.tags_any.join(",")
            ));
        } else {
            reasons.push(format!("tags matched={}", intersection.join(",")));
        }
    }

    if !rule.paths_any.is_empty() {
        let has_path_match = payload.paths.iter().any(|path| {
            rule.paths_any
                .iter()
                .any(|prefix| path == prefix || path.starts_with(prefix))
        });
        if has_path_match {
            reasons.push("paths matched".to_string());
        } else {
            matched = false;
            reasons.push("paths no match".to_string());
        }
    }

    if matched {
        reasons.push("matched".to_string());
    }
    (matched, reasons)
}

fn fallback_target_agent(rule: &DelegationRule, ruleset: &DelegationRuleSet) -> String {
    if !rule.target_agent.trim().is_empty() {
        rule.target_agent.trim().to_string()
    } else if !ruleset.default_agent.trim().is_empty() {
        ruleset.default_agent.trim().to_string()
    } else {
        "unassigned".to_string()
    }
}

fn fallback_target_prompt(rule: &DelegationRule, ruleset: &DelegationRuleSet) -> Option<String> {
    if !rule.target_prompt.trim().is_empty() {
        return Some(rule.target_prompt.trim().to_string());
    }
    if !ruleset.default_prompt.trim().is_empty() {
        return Some(ruleset.default_prompt.trim().to_string());
    }
    None
}

fn normalize_payload(payload: &DelegationPayload) -> DelegationPayload {
    DelegationPayload {
        payload_type: normalize_token(&payload.payload_type),
        repo: normalize_token(&payload.repo),
        priority: payload.priority.max(0),
        tags: normalize_many(&payload.tags),
        paths: payload
            .paths
            .iter()
            .map(|path| path.trim().to_string())
            .filter(|path| !path.is_empty())
            .collect(),
    }
}

fn normalize_many(input: &[String]) -> Vec<String> {
    input
        .iter()
        .map(|value| normalize_token(value))
        .filter(|value| !value.is_empty())
        .collect()
}

fn normalize_token(input: &str) -> String {
    input.trim().to_ascii_lowercase()
}

#[must_use]
pub fn render_decision_lines(decision: &DelegationDecision, explain: bool) -> Vec<String> {
    let mut lines = vec![format!(
        "delegation source={} target_agent={} prompt={} matched_rule={}",
        decision.source,
        decision.target_agent,
        decision.target_prompt.as_deref().unwrap_or("-"),
        decision.matched_rule_id.as_deref().unwrap_or("default")
    )];
    if explain {
        for check in &decision.checks {
            lines.push(format!(
                "rule={} match={} {}",
                check.rule_id,
                check.matched,
                check.reasons.join(" | ")
            ));
        }
    }
    lines
}

fn write_help(stdout: &mut dyn Write) -> std::io::Result<()> {
    writeln!(stdout, "Delegation Rules Engine")?;
    writeln!(stdout)?;
    writeln!(stdout, "Usage:")?;
    writeln!(
        stdout,
        "  forge delegation route --payload <json> [--rules <json> | --team <id>]"
    )?;
    writeln!(
        stdout,
        "  forge delegation explain --payload <json> [--rules <json> | --team <id>]"
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

fn default_enabled() -> bool {
    true
}

#[cfg(test)]
mod tests {
    use super::{
        evaluate_ruleset, render_decision_lines, run_for_test, DelegationPayload, DelegationRule,
        DelegationRuleSet, InMemoryDelegationBackend, TeamDelegationConfig,
    };

    fn sample_ruleset() -> DelegationRuleSet {
        DelegationRuleSet {
            default_agent: "leader-a".to_string(),
            default_prompt: "default-prompt".to_string(),
            rules: vec![
                DelegationRule {
                    id: "a-priority".to_string(),
                    enabled: true,
                    priority: 10,
                    payload_type: "incident".to_string(),
                    repo: "forge".to_string(),
                    min_priority: 3,
                    tags_any: vec!["backend".to_string()],
                    paths_any: vec!["crates/forge-cli".to_string()],
                    target_agent: "agent-a".to_string(),
                    target_prompt: "prompt-a".to_string(),
                },
                DelegationRule {
                    id: "b-priority".to_string(),
                    enabled: true,
                    priority: 10,
                    payload_type: "incident".to_string(),
                    repo: "forge".to_string(),
                    min_priority: 3,
                    tags_any: vec!["backend".to_string()],
                    paths_any: vec!["crates".to_string()],
                    target_agent: "agent-b".to_string(),
                    target_prompt: String::new(),
                },
            ],
        }
    }

    fn sample_payload() -> DelegationPayload {
        DelegationPayload {
            payload_type: "incident".to_string(),
            repo: "forge".to_string(),
            priority: 4,
            tags: vec!["backend".to_string(), "urgent".to_string()],
            paths: vec!["crates/forge-cli/src/workflow.rs".to_string()],
        }
    }

    #[test]
    fn deterministic_conflict_resolution_prefers_rule_id_after_priority() {
        let decision = evaluate_ruleset(&sample_ruleset(), &sample_payload(), "inline-rules");
        assert_eq!(decision.target_agent, "agent-a");
        assert_eq!(decision.matched_rule_id.as_deref(), Some("a-priority"));
    }

    #[test]
    fn explain_lines_are_deterministic() {
        let decision = evaluate_ruleset(&sample_ruleset(), &sample_payload(), "inline-rules");
        let lines = render_decision_lines(&decision, true);
        assert_eq!(
            lines,
            vec![
                "delegation source=inline-rules target_agent=agent-a prompt=prompt-a matched_rule=a-priority".to_string(),
                "rule=a-priority match=true payload_type=incident | repo=forge | priority>=3 | tags matched=backend | paths matched | matched".to_string(),
            ]
        );
    }

    #[test]
    fn route_command_supports_inline_rules_json() {
        let backend = InMemoryDelegationBackend::default();
        let out = run_for_test(
            &[
                "delegation",
                "--json",
                "route",
                "--payload",
                r#"{"type":"incident","repo":"forge","priority":4,"tags":["backend"],"paths":["crates/forge-cli/src/lib.rs"]}"#,
                "--rules",
                r#"{"default_agent":"fallback","rules":[{"id":"r1","priority":5,"payload_type":"incident","repo":"forge","target_agent":"agent-x","tags_any":["backend"]}]}"#,
            ],
            &backend,
        );
        assert_eq!(out.exit_code, 0, "stderr={}", out.stderr);
        assert!(out.stdout.contains("\"target_agent\": \"agent-x\""));
        assert!(out.stdout.contains("\"matched_rule_id\": \"r1\""));
    }

    #[test]
    fn route_command_loads_team_rules_when_team_flag_used() {
        let backend = InMemoryDelegationBackend::default().with_team_config(TeamDelegationConfig {
            team_reference: "ops".to_string(),
            delegation_rules_json: r#"{"rules":[{"id":"ops-rule","priority":7,"payload_type":"incident","repo":"forge","target_agent":"ops-agent"}]}"#.to_string(),
            default_assignee: "ops-default".to_string(),
            leader_agent_id: "ops-leader".to_string(),
        });
        let out = run_for_test(
            &[
                "delegation",
                "explain",
                "--team",
                "ops",
                "--payload",
                r#"{"type":"incident","repo":"forge","priority":1,"tags":[],"paths":[]}"#,
            ],
            &backend,
        );
        assert_eq!(out.exit_code, 0, "stderr={}", out.stderr);
        assert!(out.stdout.contains("source=team:ops"));
        assert!(out.stdout.contains("target_agent=ops-agent"));
        assert!(out.stdout.contains("rule=ops-rule"));
    }

    #[test]
    fn rejects_json_and_jsonl_together() {
        let backend = InMemoryDelegationBackend::default();
        let out = run_for_test(
            &[
                "delegation",
                "--json",
                "--jsonl",
                "route",
                "--payload",
                r#"{"type":"incident","repo":"forge","priority":1,"tags":[],"paths":[]}"#,
                "--rules",
                r#"{"default_agent":"fallback","rules":[]}"#,
            ],
            &backend,
        );
        assert_eq!(out.exit_code, 1);
        assert!(out
            .stderr
            .contains("--json and --jsonl are mutually exclusive"));
    }
}
