use std::io::Write;
use std::path::PathBuf;

use serde::Serialize;
use serde_json::{json, Value};

use forge_db::team_repository::TeamService;
use forge_db::team_task_repository::{
    TeamTask, TeamTaskEvent, TeamTaskFilter, TeamTaskRepository, TeamTaskService, TeamTaskStatus,
};

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct CommandOutput {
    pub stdout: String,
    pub stderr: String,
    pub exit_code: i32,
}

#[derive(Debug, Clone)]
pub struct SqliteTaskBackend {
    db_path: PathBuf,
}

impl SqliteTaskBackend {
    #[must_use]
    pub fn open_from_env() -> Self {
        Self {
            db_path: crate::runtime_paths::resolve_database_path(),
        }
    }

    #[must_use]
    pub fn new(db_path: PathBuf) -> Self {
        Self { db_path }
    }

    fn open_db(&self) -> Result<forge_db::Db, String> {
        let mut db = forge_db::Db::open(forge_db::Config::new(&self.db_path))
            .map_err(|err| format!("open database {}: {err}", self.db_path.display()))?;
        db.migrate_up()
            .map_err(|err| format!("migrate database {}: {err}", self.db_path.display()))?;
        Ok(db)
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
enum Command {
    Help,
    Send {
        team_reference: String,
        payload_type: String,
        title: String,
        body: String,
        repo: String,
        tags: Vec<String>,
        external_id: String,
        priority: i64,
    },
    List {
        team_reference: String,
        statuses: Vec<String>,
        assignee: String,
        limit: usize,
    },
    Show {
        task_id: String,
    },
    Assign {
        task_id: String,
        agent_id: String,
        actor: Option<String>,
    },
    Retry {
        task_id: String,
        actor: Option<String>,
    },
}

#[derive(Debug, Clone, PartialEq, Eq)]
struct ParsedArgs {
    command: Command,
    json: bool,
    jsonl: bool,
    quiet: bool,
}

#[derive(Debug, Clone, Serialize, PartialEq, Eq)]
struct TaskItem {
    id: String,
    team_id: String,
    status: String,
    priority: i64,
    assigned_agent_id: String,
    submitted_at: String,
    updated_at: String,
    payload_type: String,
    title: String,
}

#[derive(Debug, Clone, Serialize, PartialEq, Eq)]
struct TaskMutationOutput {
    id: String,
    team_id: String,
    status: String,
    assigned_agent_id: String,
}

#[derive(Debug, Clone, Serialize, PartialEq, Eq)]
struct TaskRetryOutput {
    source_task_id: String,
    retry_task_id: String,
    team_id: String,
    status: String,
}

#[derive(Debug, Clone, Serialize, PartialEq, Eq)]
struct TaskEventItem {
    id: i64,
    task_id: String,
    team_id: String,
    event_type: String,
    from_status: Option<String>,
    to_status: Option<String>,
    actor_agent_id: Option<String>,
    detail: Option<String>,
    created_at: String,
}

#[derive(Debug, Clone, Serialize, PartialEq)]
struct TaskShowOutput {
    task: TaskItem,
    payload: Value,
    events: Vec<TaskEventItem>,
}

pub fn run_with_backend(
    args: &[String],
    backend: &SqliteTaskBackend,
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

pub fn run_for_test(args: &[&str], backend: &SqliteTaskBackend) -> CommandOutput {
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
    backend: &SqliteTaskBackend,
    stdout: &mut dyn Write,
) -> Result<(), String> {
    let parsed = parse_args(args)?;
    match parsed.command {
        Command::Help => write_help(stdout).map_err(|err| err.to_string()),
        Command::Send {
            ref team_reference,
            ref payload_type,
            ref title,
            ref body,
            ref repo,
            ref tags,
            ref external_id,
            priority,
        } => execute_send(
            backend,
            &parsed,
            stdout,
            team_reference,
            payload_type,
            title,
            body,
            repo,
            tags,
            external_id,
            priority,
        ),
        Command::List {
            ref team_reference,
            ref statuses,
            ref assignee,
            limit,
        } => execute_list(
            backend,
            &parsed,
            stdout,
            team_reference,
            statuses,
            assignee,
            limit,
        ),
        Command::Show { ref task_id } => execute_show(backend, &parsed, stdout, task_id),
        Command::Assign {
            ref task_id,
            ref agent_id,
            ref actor,
        } => execute_assign(
            backend,
            &parsed,
            stdout,
            task_id,
            agent_id,
            actor.as_deref(),
        ),
        Command::Retry {
            ref task_id,
            ref actor,
        } => execute_retry(backend, &parsed, stdout, task_id, actor.as_deref()),
    }
}

#[allow(clippy::too_many_arguments)]
fn execute_send(
    backend: &SqliteTaskBackend,
    parsed: &ParsedArgs,
    stdout: &mut dyn Write,
    team_reference: &str,
    payload_type: &str,
    title: &str,
    body: &str,
    repo: &str,
    tags: &[String],
    external_id: &str,
    priority: i64,
) -> Result<(), String> {
    let db = backend.open_db()?;
    let team_service = TeamService::new(&db);
    let task_service = TeamTaskService::new(&db);

    let team = team_service
        .show_team(team_reference)
        .map_err(|err| format!("show team {team_reference:?}: {err}"))?;

    let payload = json!({
        "type": payload_type,
        "title": title,
        "body": body,
        "repo": repo,
        "tags": tags,
        "external_id": external_id,
    });
    let payload_json =
        serde_json::to_string(&payload).map_err(|err| format!("encode payload: {err}"))?;

    let task = task_service
        .submit(&team.id, &payload_json, priority)
        .map_err(|err| format!("submit task: {err}"))?;

    let output = TaskMutationOutput {
        id: task.id,
        team_id: task.team_id,
        status: task.status,
        assigned_agent_id: task.assigned_agent_id,
    };

    if parsed.json || parsed.jsonl {
        return write_json_or_jsonl(stdout, &output, parsed.jsonl);
    }
    if parsed.quiet {
        return Ok(());
    }

    writeln!(
        stdout,
        "Submitted task {} to team {} (status={})",
        output.id, team_reference, output.status
    )
    .map_err(|err| err.to_string())
}

fn execute_list(
    backend: &SqliteTaskBackend,
    parsed: &ParsedArgs,
    stdout: &mut dyn Write,
    team_reference: &str,
    statuses: &[String],
    assignee: &str,
    limit: usize,
) -> Result<(), String> {
    let db = backend.open_db()?;
    let team_service = TeamService::new(&db);
    let task_repo = TeamTaskRepository::new(&db);

    let team = team_service
        .show_team(team_reference)
        .map_err(|err| format!("show team {team_reference:?}: {err}"))?;

    let tasks = task_repo
        .list(&TeamTaskFilter {
            team_id: team.id,
            statuses: statuses.to_vec(),
            assigned_agent_id: assignee.to_owned(),
            limit,
        })
        .map_err(|err| format!("list tasks: {err}"))?;

    let rows = tasks.into_iter().map(task_to_item).collect::<Vec<_>>();

    if parsed.json || parsed.jsonl {
        return write_json_or_jsonl(stdout, &rows, parsed.jsonl);
    }

    if rows.is_empty() {
        writeln!(stdout, "No tasks found").map_err(|err| err.to_string())?;
        return Ok(());
    }

    for row in rows {
        let assignee = if row.assigned_agent_id.trim().is_empty() {
            "-"
        } else {
            row.assigned_agent_id.as_str()
        };
        writeln!(
            stdout,
            "{}\t{}\tpriority={}\tassignee={}\ttitle={}",
            row.id, row.status, row.priority, assignee, row.title
        )
        .map_err(|err| err.to_string())?;
    }
    Ok(())
}

fn execute_show(
    backend: &SqliteTaskBackend,
    parsed: &ParsedArgs,
    stdout: &mut dyn Write,
    task_id: &str,
) -> Result<(), String> {
    let db = backend.open_db()?;
    let repo = TeamTaskRepository::new(&db);

    let task = repo
        .get(task_id)
        .map_err(|err| format!("show task: {err}"))?;
    let events = repo
        .list_events(task_id)
        .map_err(|err| format!("list task events: {err}"))?
        .into_iter()
        .map(event_to_item)
        .collect::<Vec<_>>();
    let payload = decode_payload(&task.payload_json)?;
    let item = task_to_item(task);

    let output = TaskShowOutput {
        task: item.clone(),
        payload,
        events: events.clone(),
    };

    if parsed.json || parsed.jsonl {
        return write_json_or_jsonl(stdout, &output, parsed.jsonl);
    }

    let assignee = if item.assigned_agent_id.trim().is_empty() {
        "-"
    } else {
        item.assigned_agent_id.as_str()
    };
    writeln!(stdout, "id: {}", item.id).map_err(|err| err.to_string())?;
    writeln!(stdout, "team_id: {}", item.team_id).map_err(|err| err.to_string())?;
    writeln!(stdout, "status: {}", item.status).map_err(|err| err.to_string())?;
    writeln!(stdout, "priority: {}", item.priority).map_err(|err| err.to_string())?;
    writeln!(stdout, "assignee: {assignee}").map_err(|err| err.to_string())?;
    writeln!(stdout, "type: {}", item.payload_type).map_err(|err| err.to_string())?;
    writeln!(stdout, "title: {}", item.title).map_err(|err| err.to_string())?;
    writeln!(stdout, "events: {}", events.len()).map_err(|err| err.to_string())?;
    for event in events {
        writeln!(
            stdout,
            "  {}\t{}\t{}->{}\t{}",
            event.id,
            event.event_type,
            event.from_status.as_deref().unwrap_or("-"),
            event.to_status.as_deref().unwrap_or("-"),
            event.actor_agent_id.as_deref().unwrap_or("-"),
        )
        .map_err(|err| err.to_string())?;
    }
    Ok(())
}

fn execute_assign(
    backend: &SqliteTaskBackend,
    parsed: &ParsedArgs,
    stdout: &mut dyn Write,
    task_id: &str,
    agent_id: &str,
    actor: Option<&str>,
) -> Result<(), String> {
    let db = backend.open_db()?;
    let repo = TeamTaskRepository::new(&db);

    let existing = repo
        .get(task_id)
        .map_err(|err| format!("read task before assign: {err}"))?;
    let updated = if existing.status == TeamTaskStatus::Assigned.as_str() {
        repo.reassign(task_id, agent_id, actor)
    } else {
        repo.assign(task_id, agent_id, actor)
    }
    .map_err(|err| format!("assign task: {err}"))?;

    let output = TaskMutationOutput {
        id: updated.id,
        team_id: updated.team_id,
        status: updated.status,
        assigned_agent_id: updated.assigned_agent_id,
    };

    if parsed.json || parsed.jsonl {
        return write_json_or_jsonl(stdout, &output, parsed.jsonl);
    }
    if parsed.quiet {
        return Ok(());
    }

    writeln!(
        stdout,
        "Assigned task {} to {} (status={})",
        output.id, output.assigned_agent_id, output.status
    )
    .map_err(|err| err.to_string())
}

fn execute_retry(
    backend: &SqliteTaskBackend,
    parsed: &ParsedArgs,
    stdout: &mut dyn Write,
    task_id: &str,
    _actor: Option<&str>,
) -> Result<(), String> {
    let db = backend.open_db()?;
    let repo = TeamTaskRepository::new(&db);

    let source = repo
        .get(task_id)
        .map_err(|err| format!("show source task: {err}"))?;
    if !matches!(source.status.as_str(), "done" | "failed" | "canceled") {
        return Err(format!(
            "retry requires terminal task status (done|failed|canceled), got {}",
            source.status
        ));
    }

    let mut retry = TeamTask {
        id: String::new(),
        team_id: source.team_id.clone(),
        payload_json: source.payload_json.clone(),
        status: TeamTaskStatus::Queued.as_str().to_owned(),
        priority: source.priority,
        assigned_agent_id: String::new(),
        submitted_at: String::new(),
        assigned_at: None,
        started_at: None,
        finished_at: None,
        updated_at: String::new(),
    };
    repo.submit(&mut retry)
        .map_err(|err| format!("retry submit: {err}"))?;

    let output = TaskRetryOutput {
        source_task_id: source.id,
        retry_task_id: retry.id,
        team_id: retry.team_id,
        status: retry.status,
    };

    if parsed.json || parsed.jsonl {
        return write_json_or_jsonl(stdout, &output, parsed.jsonl);
    }
    if parsed.quiet {
        return Ok(());
    }

    writeln!(
        stdout,
        "Retried task {} as {}",
        output.source_task_id, output.retry_task_id
    )
    .map_err(|err| err.to_string())
}

fn task_to_item(task: TeamTask) -> TaskItem {
    TaskItem {
        id: task.id,
        team_id: task.team_id,
        status: task.status,
        priority: task.priority,
        assigned_agent_id: task.assigned_agent_id,
        submitted_at: task.submitted_at,
        updated_at: task.updated_at,
        payload_type: payload_string_field(&task.payload_json, "type"),
        title: payload_string_field(&task.payload_json, "title"),
    }
}

fn event_to_item(event: TeamTaskEvent) -> TaskEventItem {
    TaskEventItem {
        id: event.id,
        task_id: event.task_id,
        team_id: event.team_id,
        event_type: event.event_type,
        from_status: event.from_status,
        to_status: event.to_status,
        actor_agent_id: event.actor_agent_id,
        detail: event.detail,
        created_at: event.created_at,
    }
}

fn payload_string_field(payload_json: &str, field: &str) -> String {
    decode_payload(payload_json)
        .ok()
        .and_then(|value| value.get(field).cloned())
        .and_then(|value| value.as_str().map(ToOwned::to_owned))
        .unwrap_or_default()
}

fn decode_payload(payload_json: &str) -> Result<Value, String> {
    serde_json::from_str(payload_json).map_err(|err| format!("decode task payload json: {err}"))
}

fn parse_args(args: &[String]) -> Result<ParsedArgs, String> {
    let start = if args.first().is_some_and(|arg| arg == "task") {
        1
    } else {
        0
    };
    let mut index = start;
    let mut json = false;
    let mut jsonl = false;
    let mut quiet = false;

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
                quiet = true;
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
            quiet,
        });
    }

    let subcommand = args[index].as_str();
    index += 1;
    let command = match subcommand {
        "help" | "-h" | "--help" => Command::Help,
        "send" => parse_send_args(args, index)?,
        "ls" | "list" => parse_list_args(args, index)?,
        "show" => parse_show_args(args, index)?,
        "assign" => parse_assign_args(args, index)?,
        "retry" => parse_retry_args(args, index)?,
        other => return Err(format!("unknown task subcommand: {other}")),
    };

    Ok(ParsedArgs {
        command,
        json,
        jsonl,
        quiet,
    })
}

fn parse_send_args(args: &[String], mut index: usize) -> Result<Command, String> {
    let mut team_reference = String::new();
    let mut payload_type = String::new();
    let mut title = String::new();
    let mut body = String::new();
    let mut repo = String::new();
    let mut tags = Vec::new();
    let mut external_id = String::new();
    let mut priority: i64 = 100;

    while index < args.len() {
        match args[index].as_str() {
            "--team" => {
                index += 1;
                team_reference = take_value(args, index, "--team")?;
                index += 1;
            }
            "--type" => {
                index += 1;
                payload_type = take_value(args, index, "--type")?;
                index += 1;
            }
            "--title" => {
                index += 1;
                title = take_value(args, index, "--title")?;
                index += 1;
            }
            "--body" => {
                index += 1;
                body = take_value(args, index, "--body")?;
                index += 1;
            }
            "--repo" => {
                index += 1;
                repo = take_value(args, index, "--repo")?;
                index += 1;
            }
            "--tag" => {
                index += 1;
                tags.push(take_value(args, index, "--tag")?);
                index += 1;
            }
            "--tags" => {
                index += 1;
                let raw = take_value(args, index, "--tags")?;
                tags.extend(
                    raw.split(',')
                        .map(str::trim)
                        .filter(|item| !item.is_empty())
                        .map(ToOwned::to_owned),
                );
                index += 1;
            }
            "--external-id" => {
                index += 1;
                external_id = take_value(args, index, "--external-id")?;
                index += 1;
            }
            "--priority" => {
                index += 1;
                let raw = take_value(args, index, "--priority")?;
                priority = raw
                    .parse::<i64>()
                    .map_err(|err| format!("invalid --priority value {raw:?}: {err}"))?;
                index += 1;
            }
            value if value.starts_with('-') => {
                return Err(format!("unknown flag for task send: {value}"));
            }
            value => {
                if title.trim().is_empty() {
                    title = value.to_string();
                    index += 1;
                } else {
                    return Err("too many positional arguments for task send".to_string());
                }
            }
        }
    }

    if team_reference.trim().is_empty() || payload_type.trim().is_empty() || title.trim().is_empty()
    {
        return Err(
            "usage: forge task send --team <team-id|team-name> --type <type> --title <title> [--body <text>] [--repo <repo>] [--tag <tag>] [--priority <n>] [--external-id <id>]".to_string(),
        );
    }

    Ok(Command::Send {
        team_reference,
        payload_type,
        title,
        body,
        repo,
        tags,
        external_id,
        priority,
    })
}

fn parse_list_args(args: &[String], mut index: usize) -> Result<Command, String> {
    let mut team_reference = String::new();
    let mut statuses = Vec::new();
    let mut assignee = String::new();
    let mut limit: usize = 100;

    while index < args.len() {
        match args[index].as_str() {
            "--team" => {
                index += 1;
                team_reference = take_value(args, index, "--team")?;
                index += 1;
            }
            "--status" => {
                index += 1;
                let raw = take_value(args, index, "--status")?;
                statuses.extend(split_statuses(&raw));
                index += 1;
            }
            "--assignee" => {
                index += 1;
                assignee = take_value(args, index, "--assignee")?;
                index += 1;
            }
            "--limit" => {
                index += 1;
                let raw = take_value(args, index, "--limit")?;
                limit = raw
                    .parse::<usize>()
                    .map_err(|err| format!("invalid --limit value {raw:?}: {err}"))?;
                index += 1;
            }
            value if value.starts_with('-') => {
                return Err(format!("unknown flag for task ls: {value}"));
            }
            value => {
                if team_reference.trim().is_empty() {
                    team_reference = value.to_string();
                    index += 1;
                } else {
                    return Err("too many positional arguments for task ls".to_string());
                }
            }
        }
    }

    if team_reference.trim().is_empty() {
        return Err(
            "usage: forge task ls --team <team-id|team-name> [--status queued,assigned,...] [--assignee <agent>] [--limit <n>]"
                .to_string(),
        );
    }

    validate_statuses(&statuses)?;

    Ok(Command::List {
        team_reference,
        statuses,
        assignee,
        limit,
    })
}

fn parse_show_args(args: &[String], mut index: usize) -> Result<Command, String> {
    let mut task_id = String::new();
    while index < args.len() {
        match args[index].as_str() {
            "--task" | "--id" => {
                index += 1;
                task_id = take_value(args, index, "--task")?;
                index += 1;
            }
            value if value.starts_with('-') => {
                return Err(format!("unknown flag for task show: {value}"));
            }
            value => {
                if !task_id.trim().is_empty() {
                    return Err("task id provided multiple times".to_string());
                }
                task_id = value.to_string();
                index += 1;
            }
        }
    }

    if task_id.trim().is_empty() {
        return Err("usage: forge task show <task-id>".to_string());
    }

    Ok(Command::Show { task_id })
}

fn parse_assign_args(args: &[String], mut index: usize) -> Result<Command, String> {
    let mut task_id = String::new();
    let mut agent_id = String::new();
    let mut actor = None;

    while index < args.len() {
        match args[index].as_str() {
            "--task" | "--id" => {
                index += 1;
                task_id = take_value(args, index, "--task")?;
                index += 1;
            }
            "--agent" => {
                index += 1;
                agent_id = take_value(args, index, "--agent")?;
                index += 1;
            }
            "--actor" => {
                index += 1;
                actor = Some(take_value(args, index, "--actor")?);
                index += 1;
            }
            value if value.starts_with('-') => {
                return Err(format!("unknown flag for task assign: {value}"));
            }
            value => {
                if task_id.trim().is_empty() {
                    task_id = value.to_string();
                } else if agent_id.trim().is_empty() {
                    agent_id = value.to_string();
                } else {
                    return Err("too many positional arguments for task assign".to_string());
                }
                index += 1;
            }
        }
    }

    if task_id.trim().is_empty() || agent_id.trim().is_empty() {
        return Err(
            "usage: forge task assign <task-id> --agent <agent-id> [--actor <agent-id>]"
                .to_string(),
        );
    }

    Ok(Command::Assign {
        task_id,
        agent_id,
        actor,
    })
}

fn parse_retry_args(args: &[String], mut index: usize) -> Result<Command, String> {
    let mut task_id = String::new();
    let mut actor = None;

    while index < args.len() {
        match args[index].as_str() {
            "--task" | "--id" => {
                index += 1;
                task_id = take_value(args, index, "--task")?;
                index += 1;
            }
            "--actor" => {
                index += 1;
                actor = Some(take_value(args, index, "--actor")?);
                index += 1;
            }
            value if value.starts_with('-') => {
                return Err(format!("unknown flag for task retry: {value}"));
            }
            value => {
                if !task_id.trim().is_empty() {
                    return Err("task id provided multiple times".to_string());
                }
                task_id = value.to_string();
                index += 1;
            }
        }
    }

    if task_id.trim().is_empty() {
        return Err("usage: forge task retry <task-id> [--actor <agent-id>]".to_string());
    }

    Ok(Command::Retry { task_id, actor })
}

fn split_statuses(raw: &str) -> Vec<String> {
    raw.split(',')
        .map(str::trim)
        .filter(|value| !value.is_empty())
        .map(ToOwned::to_owned)
        .collect()
}

fn validate_statuses(statuses: &[String]) -> Result<(), String> {
    for status in statuses {
        TeamTaskStatus::parse(status.trim())
            .map_err(|err| format!("invalid --status {status:?}: {err}"))?;
    }
    Ok(())
}

fn take_value(args: &[String], index: usize, flag: &str) -> Result<String, String> {
    args.get(index)
        .cloned()
        .ok_or_else(|| format!("missing value for {flag}"))
}

fn write_help(stdout: &mut dyn Write) -> std::io::Result<()> {
    writeln!(stdout, "Manage team task inbox")?;
    writeln!(stdout)?;
    writeln!(stdout, "Usage:")?;
    writeln!(stdout, "  forge task <subcommand> [flags]")?;
    writeln!(stdout)?;
    writeln!(stdout, "Subcommands:")?;
    writeln!(stdout, "  send      Submit task to team inbox")?;
    writeln!(stdout, "  ls        List team tasks")?;
    writeln!(stdout, "  show      Show task details and events")?;
    writeln!(stdout, "  assign    Assign/reassign task to agent")?;
    writeln!(stdout, "  retry     Clone terminal task into queued retry")?;
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

#[cfg(test)]
#[allow(clippy::unwrap_used)]
mod tests {
    use std::path::Path;
    use std::sync::atomic::{AtomicU64, Ordering};
    use std::time::{SystemTime, UNIX_EPOCH};

    use forge_db::team_repository::TeamService;
    use forge_db::team_task_repository::TeamTaskRepository;

    use super::{run_for_test, SqliteTaskBackend};

    #[test]
    fn send_list_show_assign_retry_flow() {
        let db_path = temp_db_path("task-flow");
        seed_team(&db_path, "ops");
        let backend = SqliteTaskBackend::new(db_path.clone());

        let sent = run_for_test(
            &[
                "task",
                "--json",
                "send",
                "--team",
                "ops",
                "--type",
                "incident",
                "--title",
                "pipeline down",
                "--priority",
                "5",
            ],
            &backend,
        );
        assert_eq!(sent.exit_code, 0, "stderr={}", sent.stderr);
        let sent_value: serde_json::Value = serde_json::from_str(&sent.stdout).unwrap();
        let task_id = sent_value["id"].as_str().unwrap().to_string();

        let listed = run_for_test(&["task", "ls", "--team", "ops"], &backend);
        assert_eq!(listed.exit_code, 0, "stderr={}", listed.stderr);
        assert!(listed.stdout.contains("pipeline down"));
        assert!(listed.stdout.contains("queued"));

        let assigned = run_for_test(
            &["task", "--json", "assign", &task_id, "--agent", "agent-a"],
            &backend,
        );
        assert_eq!(assigned.exit_code, 0, "stderr={}", assigned.stderr);
        let assigned_value: serde_json::Value = serde_json::from_str(&assigned.stdout).unwrap();
        assert_eq!(assigned_value["status"], "assigned");

        let shown = run_for_test(&["task", "show", &task_id], &backend);
        assert_eq!(shown.exit_code, 0, "stderr={}", shown.stderr);
        assert!(shown.stdout.contains("status: assigned"));
        assert!(shown.stdout.contains("assignee: agent-a"));

        mark_failed(&db_path, &task_id);

        let retried = run_for_test(&["task", "--json", "retry", &task_id], &backend);
        assert_eq!(retried.exit_code, 0, "stderr={}", retried.stderr);
        let retry_value: serde_json::Value = serde_json::from_str(&retried.stdout).unwrap();
        assert_eq!(retry_value["source_task_id"], task_id);
        assert_eq!(retry_value["status"], "queued");
        assert_ne!(
            retry_value["source_task_id"].as_str(),
            retry_value["retry_task_id"].as_str()
        );

        cleanup_db(&db_path);
    }

    #[test]
    fn retry_rejects_non_terminal_task() {
        let db_path = temp_db_path("retry-invalid");
        seed_team(&db_path, "ops");
        let backend = SqliteTaskBackend::new(db_path.clone());

        let sent = run_for_test(
            &[
                "task",
                "--json",
                "send",
                "--team",
                "ops",
                "--type",
                "incident",
                "--title",
                "pipeline down",
            ],
            &backend,
        );
        assert_eq!(sent.exit_code, 0, "stderr={}", sent.stderr);
        let task_id = serde_json::from_str::<serde_json::Value>(&sent.stdout).unwrap()["id"]
            .as_str()
            .unwrap()
            .to_string();

        let retry = run_for_test(&["task", "retry", &task_id], &backend);
        assert_eq!(retry.exit_code, 1);
        assert!(retry.stderr.contains("retry requires terminal task status"));

        cleanup_db(&db_path);
    }

    #[test]
    fn send_keeps_literal_title_named_json_flag() {
        let db_path = temp_db_path("send-literal-json-title");
        seed_team(&db_path, "ops");
        let backend = SqliteTaskBackend::new(db_path.clone());

        let sent = run_for_test(
            &[
                "task",
                "--json",
                "send",
                "--team",
                "ops",
                "--type",
                "incident",
                "--title",
                "--json",
            ],
            &backend,
        );
        assert_eq!(sent.exit_code, 0, "stderr={}", sent.stderr);
        let task_id = serde_json::from_str::<serde_json::Value>(&sent.stdout).unwrap()["id"]
            .as_str()
            .unwrap()
            .to_string();

        let shown = run_for_test(&["task", "show", &task_id], &backend);
        assert_eq!(shown.exit_code, 0, "stderr={}", shown.stderr);
        assert!(shown.stdout.contains("title: --json"));

        cleanup_db(&db_path);
    }

    #[test]
    fn send_requires_team_and_payload_fields() {
        let db_path = temp_db_path("send-usage");
        let backend = SqliteTaskBackend::new(db_path.clone());

        let out = run_for_test(
            &["task", "send", "--type", "incident", "--title", "oops"],
            &backend,
        );
        assert_eq!(out.exit_code, 1);
        assert!(out.stderr.contains("usage: forge task send"));

        cleanup_db(&db_path);
    }

    fn seed_team(db_path: &Path, name: &str) {
        let mut db = forge_db::Db::open(forge_db::Config::new(db_path)).unwrap();
        db.migrate_up().unwrap();
        let service = TeamService::new(&db);
        service.create_team(name, "{}", "", 300).unwrap();
    }

    fn mark_failed(db_path: &Path, task_id: &str) {
        let db = forge_db::Db::open(forge_db::Config::new(db_path)).unwrap();
        let repo = TeamTaskRepository::new(&db);
        repo.fail(task_id, Some("agent-a"), Some("needs retry"))
            .unwrap();
    }

    fn temp_db_path(tag: &str) -> std::path::PathBuf {
        static COUNTER: AtomicU64 = AtomicU64::new(0);
        let nanos = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .map(|value| value.as_nanos())
            .unwrap_or(0);
        let seq = COUNTER.fetch_add(1, Ordering::Relaxed);
        let pid = std::process::id();
        std::env::temp_dir().join(format!(
            "forge-task-command-{tag}-{pid}-{nanos}-{seq}.sqlite"
        ))
    }

    fn cleanup_db(path: &Path) {
        let _ = std::fs::remove_file(path);
    }
}
