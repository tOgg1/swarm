use std::io::Write;
use std::path::PathBuf;

use serde::Serialize;

use forge_db::team_repository::{TeamRepository, TeamRole, TeamService};
use forge_db::team_task_repository::{TeamTaskFilter, TeamTaskRepository};

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct CommandOutput {
    pub stdout: String,
    pub stderr: String,
    pub exit_code: i32,
}

#[derive(Debug, Clone)]
pub struct SqliteTeamBackend {
    db_path: PathBuf,
}

impl SqliteTeamBackend {
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
    List,
    New {
        name: String,
        delegation_rules_json: String,
        default_assignee: String,
        heartbeat_interval_seconds: i64,
    },
    Remove {
        team_reference: String,
    },
    Show {
        team_reference: String,
    },
    MemberAdd {
        team_reference: String,
        agent_id: String,
        role: String,
    },
    MemberRemove {
        team_reference: String,
        agent_id: String,
    },
    MemberList {
        team_reference: String,
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
struct QueueCounts {
    queued: usize,
    assigned: usize,
    running: usize,
    blocked: usize,
    open: usize,
}

#[derive(Debug, Clone, Serialize, PartialEq, Eq)]
struct TeamListItem {
    id: String,
    name: String,
    default_assignee: String,
    heartbeat_interval_seconds: i64,
    members: usize,
    queue: QueueCounts,
}

#[derive(Debug, Clone, Serialize, PartialEq, Eq)]
struct TeamMemberItem {
    id: String,
    team_id: String,
    agent_id: String,
    role: String,
    created_at: String,
}

#[derive(Debug, Clone, Serialize, PartialEq, Eq)]
struct TeamShowOutput {
    id: String,
    name: String,
    delegation_rules_json: String,
    default_assignee: String,
    heartbeat_interval_seconds: i64,
    created_at: String,
    updated_at: String,
    members: Vec<TeamMemberItem>,
    queue: QueueCounts,
}

#[derive(Debug, Clone, Serialize, PartialEq, Eq)]
struct TeamMutationOutput {
    id: String,
    name: String,
}

pub fn run_with_backend(
    args: &[String],
    backend: &SqliteTeamBackend,
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

pub fn run_for_test(args: &[&str], backend: &SqliteTeamBackend) -> CommandOutput {
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
    backend: &SqliteTeamBackend,
    stdout: &mut dyn Write,
) -> Result<(), String> {
    let parsed = parse_args(args)?;
    match parsed.command {
        Command::Help => write_help(stdout).map_err(|err| err.to_string()),
        Command::List => execute_list(backend, &parsed, stdout),
        Command::New {
            ref name,
            ref delegation_rules_json,
            ref default_assignee,
            heartbeat_interval_seconds,
        } => execute_new(
            backend,
            &parsed,
            stdout,
            name,
            delegation_rules_json,
            default_assignee,
            heartbeat_interval_seconds,
        ),
        Command::Remove { ref team_reference } => {
            execute_remove(backend, &parsed, stdout, team_reference)
        }
        Command::Show { ref team_reference } => {
            execute_show(backend, &parsed, stdout, team_reference)
        }
        Command::MemberAdd {
            ref team_reference,
            ref agent_id,
            ref role,
        } => execute_member_add(backend, &parsed, stdout, team_reference, agent_id, role),
        Command::MemberRemove {
            ref team_reference,
            ref agent_id,
        } => execute_member_remove(backend, &parsed, stdout, team_reference, agent_id),
        Command::MemberList { ref team_reference } => {
            execute_member_list(backend, &parsed, stdout, team_reference)
        }
    }
}

fn execute_list(
    backend: &SqliteTeamBackend,
    parsed: &ParsedArgs,
    stdout: &mut dyn Write,
) -> Result<(), String> {
    let db = backend.open_db()?;
    let service = TeamService::new(&db);
    let team_repo = TeamRepository::new(&db);
    let task_repo = TeamTaskRepository::new(&db);

    let teams = service
        .list_teams()
        .map_err(|err| format!("list teams: {err}"))?;

    let mut rows = Vec::with_capacity(teams.len());
    for team in teams {
        let members = team_repo
            .list_members(&team.id)
            .map_err(|err| format!("list members for team {}: {err}", team.id))?;
        let queue = queue_counts(&task_repo, &team.id)?;
        rows.push(TeamListItem {
            id: team.id,
            name: team.name,
            default_assignee: team.default_assignee,
            heartbeat_interval_seconds: team.heartbeat_interval_seconds,
            members: members.len(),
            queue,
        });
    }

    if parsed.json || parsed.jsonl {
        return write_json_or_jsonl(stdout, &rows, parsed.jsonl);
    }

    if rows.is_empty() {
        writeln!(stdout, "No teams found").map_err(|err| err.to_string())?;
        return Ok(());
    }

    for row in rows {
        writeln!(
            stdout,
            "{}\t{}\tmembers={}\topen={}\tqueued={}\tassigned={}\trunning={}\tblocked={}",
            row.name,
            row.id,
            row.members,
            row.queue.open,
            row.queue.queued,
            row.queue.assigned,
            row.queue.running,
            row.queue.blocked,
        )
        .map_err(|err| err.to_string())?;
    }
    Ok(())
}

fn execute_new(
    backend: &SqliteTeamBackend,
    parsed: &ParsedArgs,
    stdout: &mut dyn Write,
    name: &str,
    delegation_rules_json: &str,
    default_assignee: &str,
    heartbeat_interval_seconds: i64,
) -> Result<(), String> {
    let db = backend.open_db()?;
    let service = TeamService::new(&db);
    let team = service
        .create_team(
            name,
            delegation_rules_json,
            default_assignee,
            heartbeat_interval_seconds,
        )
        .map_err(|err| format!("create team: {err}"))?;

    let output = TeamMutationOutput {
        id: team.id,
        name: team.name,
    };

    if parsed.json || parsed.jsonl {
        return write_json_or_jsonl(stdout, &output, parsed.jsonl);
    }
    if parsed.quiet {
        return Ok(());
    }

    writeln!(stdout, "Created team {} ({})", output.name, output.id).map_err(|err| err.to_string())
}

fn execute_remove(
    backend: &SqliteTeamBackend,
    parsed: &ParsedArgs,
    stdout: &mut dyn Write,
    team_reference: &str,
) -> Result<(), String> {
    let db = backend.open_db()?;
    let service = TeamService::new(&db);
    let team = service
        .show_team(team_reference)
        .map_err(|err| format!("show team {team_reference:?}: {err}"))?;
    service
        .delete_team(team_reference)
        .map_err(|err| format!("remove team {team_reference:?}: {err}"))?;

    let output = TeamMutationOutput {
        id: team.id,
        name: team.name,
    };

    if parsed.json || parsed.jsonl {
        return write_json_or_jsonl(stdout, &output, parsed.jsonl);
    }
    if parsed.quiet {
        return Ok(());
    }

    writeln!(stdout, "Removed team {} ({})", output.name, output.id).map_err(|err| err.to_string())
}

fn execute_show(
    backend: &SqliteTeamBackend,
    parsed: &ParsedArgs,
    stdout: &mut dyn Write,
    team_reference: &str,
) -> Result<(), String> {
    let db = backend.open_db()?;
    let service = TeamService::new(&db);
    let team_repo = TeamRepository::new(&db);
    let task_repo = TeamTaskRepository::new(&db);

    let team = service
        .show_team(team_reference)
        .map_err(|err| format!("show team {team_reference:?}: {err}"))?;
    let members = team_repo
        .list_members(&team.id)
        .map_err(|err| format!("list members for team {}: {err}", team.id))?
        .into_iter()
        .map(|member| TeamMemberItem {
            id: member.id,
            team_id: member.team_id,
            agent_id: member.agent_id,
            role: member.role,
            created_at: member.created_at,
        })
        .collect::<Vec<_>>();
    let queue = queue_counts(&task_repo, &team.id)?;

    let output = TeamShowOutput {
        id: team.id,
        name: team.name,
        delegation_rules_json: team.delegation_rules_json,
        default_assignee: team.default_assignee,
        heartbeat_interval_seconds: team.heartbeat_interval_seconds,
        created_at: team.created_at,
        updated_at: team.updated_at,
        members,
        queue,
    };

    if parsed.json || parsed.jsonl {
        return write_json_or_jsonl(stdout, &output, parsed.jsonl);
    }

    writeln!(stdout, "id: {}", output.id).map_err(|err| err.to_string())?;
    writeln!(stdout, "name: {}", output.name).map_err(|err| err.to_string())?;
    writeln!(
        stdout,
        "default_assignee: {}",
        if output.default_assignee.trim().is_empty() {
            "-"
        } else {
            output.default_assignee.as_str()
        }
    )
    .map_err(|err| err.to_string())?;
    writeln!(
        stdout,
        "heartbeat_interval_seconds: {}",
        output.heartbeat_interval_seconds
    )
    .map_err(|err| err.to_string())?;
    writeln!(
        stdout,
        "queue: open={} queued={} assigned={} running={} blocked={}",
        output.queue.open,
        output.queue.queued,
        output.queue.assigned,
        output.queue.running,
        output.queue.blocked,
    )
    .map_err(|err| err.to_string())?;
    writeln!(stdout, "members:").map_err(|err| err.to_string())?;
    if output.members.is_empty() {
        writeln!(stdout, "  (none)").map_err(|err| err.to_string())?;
    } else {
        for member in output.members {
            writeln!(
                stdout,
                "  {}\t{}\t{}",
                member.agent_id, member.role, member.id
            )
            .map_err(|err| err.to_string())?;
        }
    }
    Ok(())
}

fn execute_member_add(
    backend: &SqliteTeamBackend,
    parsed: &ParsedArgs,
    stdout: &mut dyn Write,
    team_reference: &str,
    agent_id: &str,
    role: &str,
) -> Result<(), String> {
    let db = backend.open_db()?;
    let service = TeamService::new(&db);
    let role = TeamRole::parse(role.trim()).map_err(|err| format!("parse role: {err}"))?;
    let member = service
        .add_member(team_reference, agent_id, role)
        .map_err(|err| format!("add member: {err}"))?;

    let output = TeamMemberItem {
        id: member.id,
        team_id: member.team_id,
        agent_id: member.agent_id,
        role: member.role,
        created_at: member.created_at,
    };

    if parsed.json || parsed.jsonl {
        return write_json_or_jsonl(stdout, &output, parsed.jsonl);
    }
    if parsed.quiet {
        return Ok(());
    }

    writeln!(
        stdout,
        "Added member {} to {} as {}",
        output.agent_id, team_reference, output.role
    )
    .map_err(|err| err.to_string())
}

fn execute_member_remove(
    backend: &SqliteTeamBackend,
    parsed: &ParsedArgs,
    stdout: &mut dyn Write,
    team_reference: &str,
    agent_id: &str,
) -> Result<(), String> {
    let db = backend.open_db()?;
    let service = TeamService::new(&db);
    let team_repo = TeamRepository::new(&db);
    let team = service
        .show_team(team_reference)
        .map_err(|err| format!("show team {team_reference:?}: {err}"))?;
    team_repo
        .remove_member(&team.id, agent_id)
        .map_err(|err| format!("remove member {agent_id} from {team_reference}: {err}"))?;

    if parsed.json || parsed.jsonl {
        let output = serde_json::json!({
            "team_id": team.id,
            "team_reference": team_reference,
            "agent_id": agent_id,
            "removed": true,
        });
        return write_json_or_jsonl(stdout, &output, parsed.jsonl);
    }
    if parsed.quiet {
        return Ok(());
    }

    writeln!(
        stdout,
        "Removed member {} from {}",
        agent_id, team_reference
    )
    .map_err(|err| err.to_string())
}

fn execute_member_list(
    backend: &SqliteTeamBackend,
    parsed: &ParsedArgs,
    stdout: &mut dyn Write,
    team_reference: &str,
) -> Result<(), String> {
    let db = backend.open_db()?;
    let service = TeamService::new(&db);
    let members = service
        .list_members(team_reference)
        .map_err(|err| format!("list members for {team_reference}: {err}"))?
        .into_iter()
        .map(|member| TeamMemberItem {
            id: member.id,
            team_id: member.team_id,
            agent_id: member.agent_id,
            role: member.role,
            created_at: member.created_at,
        })
        .collect::<Vec<_>>();

    if parsed.json || parsed.jsonl {
        return write_json_or_jsonl(stdout, &members, parsed.jsonl);
    }

    if members.is_empty() {
        writeln!(stdout, "No members found").map_err(|err| err.to_string())?;
        return Ok(());
    }

    for member in members {
        writeln!(
            stdout,
            "{}\t{}\t{}",
            member.agent_id, member.role, member.id
        )
        .map_err(|err| err.to_string())?;
    }
    Ok(())
}

fn queue_counts(task_repo: &TeamTaskRepository<'_>, team_id: &str) -> Result<QueueCounts, String> {
    let tasks = task_repo
        .list(&TeamTaskFilter {
            team_id: team_id.to_owned(),
            statuses: Vec::new(),
            assigned_agent_id: String::new(),
            limit: 10_000,
        })
        .map_err(|err| format!("list tasks for team {team_id}: {err}"))?;

    let mut counts = QueueCounts {
        queued: 0,
        assigned: 0,
        running: 0,
        blocked: 0,
        open: 0,
    };
    for task in tasks {
        match task.status.as_str() {
            "queued" => counts.queued += 1,
            "assigned" => counts.assigned += 1,
            "running" => counts.running += 1,
            "blocked" => counts.blocked += 1,
            _ => {}
        }
    }
    counts.open = counts.queued + counts.assigned + counts.running + counts.blocked;
    Ok(counts)
}

fn parse_args(args: &[String]) -> Result<ParsedArgs, String> {
    let start = if args.first().is_some_and(|arg| arg == "team") {
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
        "ls" | "list" => Command::List,
        "new" | "create" => parse_new_args(args, index)?,
        "rm" | "delete" => parse_remove_args(args, index)?,
        "show" | "get" => parse_show_args(args, index)?,
        "member" => parse_member_args(args, index)?,
        other => return Err(format!("unknown team subcommand: {other}")),
    };

    Ok(ParsedArgs {
        command,
        json,
        jsonl,
        quiet,
    })
}

fn parse_new_args(args: &[String], mut index: usize) -> Result<Command, String> {
    let mut name = String::new();
    let mut delegation_rules_json = String::new();
    let mut default_assignee = String::new();
    let mut heartbeat_interval_seconds: i64 = 300;

    while index < args.len() {
        match args[index].as_str() {
            "--name" => {
                index += 1;
                name = take_value(args, index, "--name")?;
                index += 1;
            }
            "--rules" | "--delegation-rules" => {
                index += 1;
                delegation_rules_json = take_value(args, index, "--delegation-rules")?;
                index += 1;
            }
            "--default-assignee" => {
                index += 1;
                default_assignee = take_value(args, index, "--default-assignee")?;
                index += 1;
            }
            "--heartbeat" | "--heartbeat-interval" => {
                index += 1;
                let raw = take_value(args, index, "--heartbeat")?;
                heartbeat_interval_seconds = raw
                    .parse::<i64>()
                    .map_err(|err| format!("invalid --heartbeat value {raw:?}: {err}"))?;
                index += 1;
            }
            value if value.starts_with('-') => {
                return Err(format!("unknown flag for team new: {value}"));
            }
            value => {
                if !name.trim().is_empty() {
                    return Err("team name provided multiple times".to_string());
                }
                name = value.to_string();
                index += 1;
            }
        }
    }

    if name.trim().is_empty() {
        return Err(
            "usage: forge team new <name> [--default-assignee <agent>] [--heartbeat <seconds>] [--delegation-rules <json>]".to_string(),
        );
    }

    Ok(Command::New {
        name,
        delegation_rules_json,
        default_assignee,
        heartbeat_interval_seconds,
    })
}

fn parse_remove_args(args: &[String], mut index: usize) -> Result<Command, String> {
    let mut team_reference = String::new();
    while index < args.len() {
        match args[index].as_str() {
            "--team" => {
                index += 1;
                team_reference = take_value(args, index, "--team")?;
                index += 1;
            }
            value if value.starts_with('-') => {
                return Err(format!("unknown flag for team rm: {value}"));
            }
            value => {
                if !team_reference.is_empty() {
                    return Err("team reference provided multiple times".to_string());
                }
                team_reference = value.to_string();
                index += 1;
            }
        }
    }
    if team_reference.trim().is_empty() {
        return Err("usage: forge team rm <team-id|team-name>".to_string());
    }
    Ok(Command::Remove { team_reference })
}

fn parse_show_args(args: &[String], mut index: usize) -> Result<Command, String> {
    let mut team_reference = String::new();
    while index < args.len() {
        match args[index].as_str() {
            "--team" => {
                index += 1;
                team_reference = take_value(args, index, "--team")?;
                index += 1;
            }
            value if value.starts_with('-') => {
                return Err(format!("unknown flag for team show: {value}"));
            }
            value => {
                if !team_reference.is_empty() {
                    return Err("team reference provided multiple times".to_string());
                }
                team_reference = value.to_string();
                index += 1;
            }
        }
    }
    if team_reference.trim().is_empty() {
        return Err("usage: forge team show <team-id|team-name>".to_string());
    }
    Ok(Command::Show { team_reference })
}

fn parse_member_args(args: &[String], index: usize) -> Result<Command, String> {
    let Some(subcommand) = args.get(index) else {
        return Err("usage: forge team member <add|rm|ls> ...".to_string());
    };

    match subcommand.as_str() {
        "add" => parse_member_add_args(args, index + 1),
        "rm" | "remove" => parse_member_remove_args(args, index + 1),
        "ls" | "list" => parse_member_list_args(args, index + 1),
        other => Err(format!("unknown team member subcommand: {other}")),
    }
}

fn parse_member_add_args(args: &[String], mut index: usize) -> Result<Command, String> {
    let mut team_reference = String::new();
    let mut agent_id = String::new();
    let mut role = "member".to_string();

    while index < args.len() {
        match args[index].as_str() {
            "--team" => {
                index += 1;
                team_reference = take_value(args, index, "--team")?;
                index += 1;
            }
            "--agent" => {
                index += 1;
                agent_id = take_value(args, index, "--agent")?;
                index += 1;
            }
            "--role" => {
                index += 1;
                role = take_value(args, index, "--role")?;
                index += 1;
            }
            value if value.starts_with('-') => {
                return Err(format!("unknown flag for team member add: {value}"));
            }
            value => {
                if team_reference.trim().is_empty() {
                    team_reference = value.to_string();
                } else if agent_id.trim().is_empty() {
                    agent_id = value.to_string();
                } else {
                    return Err("too many positional arguments for team member add".to_string());
                }
                index += 1;
            }
        }
    }

    if team_reference.trim().is_empty() || agent_id.trim().is_empty() {
        return Err(
            "usage: forge team member add <team-id|team-name> <agent-id> [--role leader|member]"
                .to_string(),
        );
    }

    Ok(Command::MemberAdd {
        team_reference,
        agent_id,
        role,
    })
}

fn parse_member_remove_args(args: &[String], mut index: usize) -> Result<Command, String> {
    let mut team_reference = String::new();
    let mut agent_id = String::new();

    while index < args.len() {
        match args[index].as_str() {
            "--team" => {
                index += 1;
                team_reference = take_value(args, index, "--team")?;
                index += 1;
            }
            "--agent" => {
                index += 1;
                agent_id = take_value(args, index, "--agent")?;
                index += 1;
            }
            value if value.starts_with('-') => {
                return Err(format!("unknown flag for team member rm: {value}"));
            }
            value => {
                if team_reference.trim().is_empty() {
                    team_reference = value.to_string();
                } else if agent_id.trim().is_empty() {
                    agent_id = value.to_string();
                } else {
                    return Err("too many positional arguments for team member rm".to_string());
                }
                index += 1;
            }
        }
    }

    if team_reference.trim().is_empty() || agent_id.trim().is_empty() {
        return Err("usage: forge team member rm <team-id|team-name> <agent-id>".to_string());
    }

    Ok(Command::MemberRemove {
        team_reference,
        agent_id,
    })
}

fn parse_member_list_args(args: &[String], mut index: usize) -> Result<Command, String> {
    let mut team_reference = String::new();

    while index < args.len() {
        match args[index].as_str() {
            "--team" => {
                index += 1;
                team_reference = take_value(args, index, "--team")?;
                index += 1;
            }
            value if value.starts_with('-') => {
                return Err(format!("unknown flag for team member ls: {value}"));
            }
            value => {
                if !team_reference.trim().is_empty() {
                    return Err("team reference provided multiple times".to_string());
                }
                team_reference = value.to_string();
                index += 1;
            }
        }
    }

    if team_reference.trim().is_empty() {
        return Err("usage: forge team member ls <team-id|team-name>".to_string());
    }

    Ok(Command::MemberList { team_reference })
}

fn take_value(args: &[String], index: usize, flag: &str) -> Result<String, String> {
    args.get(index)
        .cloned()
        .ok_or_else(|| format!("missing value for {flag}"))
}

fn write_help(stdout: &mut dyn Write) -> std::io::Result<()> {
    writeln!(stdout, "Manage teams and team members")?;
    writeln!(stdout)?;
    writeln!(stdout, "Usage:")?;
    writeln!(stdout, "  forge team <subcommand> [flags]")?;
    writeln!(stdout)?;
    writeln!(stdout, "Subcommands:")?;
    writeln!(stdout, "  ls                              List teams")?;
    writeln!(stdout, "  new <name>                      Create team")?;
    writeln!(stdout, "  rm <team>                       Remove team")?;
    writeln!(
        stdout,
        "  show <team>                     Show team details"
    )?;
    writeln!(stdout, "  member add <team> <agent>       Add team member")?;
    writeln!(
        stdout,
        "  member rm <team> <agent>        Remove team member"
    )?;
    writeln!(
        stdout,
        "  member ls <team>                List team members"
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

#[cfg(test)]
#[allow(clippy::unwrap_used)]
mod tests {
    use std::path::Path;
    use std::sync::atomic::{AtomicU64, Ordering};
    use std::time::{SystemTime, UNIX_EPOCH};

    use super::{run_for_test, SqliteTeamBackend};

    #[test]
    fn create_member_show_and_remove_flow() {
        let db_path = temp_db_path("team-flow");
        let backend = SqliteTeamBackend::new(db_path.clone());

        let created = run_for_test(
            &["team", "new", "ops", "--default-assignee", "agent-a"],
            &backend,
        );
        assert_eq!(created.exit_code, 0, "stderr={}", created.stderr);

        let add = run_for_test(
            &[
                "team",
                "member",
                "add",
                "ops",
                "agent-lead",
                "--role",
                "leader",
            ],
            &backend,
        );
        assert_eq!(add.exit_code, 0, "stderr={}", add.stderr);

        let listed = run_for_test(&["team", "ls"], &backend);
        assert_eq!(listed.exit_code, 0, "stderr={}", listed.stderr);
        assert!(listed.stdout.contains("ops"));
        assert!(listed.stdout.contains("members=1"));

        let shown = run_for_test(&["team", "show", "ops"], &backend);
        assert_eq!(shown.exit_code, 0, "stderr={}", shown.stderr);
        assert!(shown.stdout.contains("name: ops"));
        assert!(shown.stdout.contains("agent-lead"));
        assert!(shown.stdout.contains("queue: open=0"));

        let members = run_for_test(&["team", "member", "ls", "ops"], &backend);
        assert_eq!(members.exit_code, 0, "stderr={}", members.stderr);
        assert!(members.stdout.contains("agent-lead"));

        let removed_member = run_for_test(&["team", "member", "rm", "ops", "agent-lead"], &backend);
        assert_eq!(
            removed_member.exit_code, 0,
            "stderr={}",
            removed_member.stderr
        );

        let removed_team = run_for_test(&["team", "rm", "ops"], &backend);
        assert_eq!(removed_team.exit_code, 0, "stderr={}", removed_team.stderr);

        let listed_again = run_for_test(&["team", "ls"], &backend);
        assert_eq!(listed_again.exit_code, 0, "stderr={}", listed_again.stderr);
        assert!(listed_again.stdout.contains("No teams found"));

        cleanup_db(&db_path);
    }

    #[test]
    fn member_add_rejects_invalid_role() {
        let db_path = temp_db_path("invalid-role");
        let backend = SqliteTeamBackend::new(db_path.clone());

        let created = run_for_test(&["team", "new", "ops"], &backend);
        assert_eq!(created.exit_code, 0, "stderr={}", created.stderr);

        let out = run_for_test(
            &["team", "member", "add", "ops", "agent-a", "--role", "owner"],
            &backend,
        );
        assert_eq!(out.exit_code, 1);
        assert!(out.stderr.contains("invalid team role"));

        cleanup_db(&db_path);
    }

    #[test]
    fn team_list_json_shape() {
        let db_path = temp_db_path("json-shape");
        let backend = SqliteTeamBackend::new(db_path.clone());

        let created = run_for_test(&["team", "new", "ops"], &backend);
        assert_eq!(created.exit_code, 0, "stderr={}", created.stderr);

        let listed = run_for_test(&["team", "--json", "ls"], &backend);
        assert_eq!(listed.exit_code, 0, "stderr={}", listed.stderr);
        let value: serde_json::Value = serde_json::from_str(&listed.stdout).unwrap();
        let first = &value[0];
        assert_eq!(first["name"], "ops");
        assert_eq!(first["queue"]["open"], 0);

        cleanup_db(&db_path);
    }

    #[test]
    fn new_keeps_literal_default_assignee_named_json_flag() {
        let db_path = temp_db_path("default-assignee-json-token");
        let backend = SqliteTeamBackend::new(db_path.clone());

        let created = run_for_test(
            &[
                "team",
                "--json",
                "new",
                "ops",
                "--default-assignee",
                "--json",
            ],
            &backend,
        );
        assert_eq!(created.exit_code, 0, "stderr={}", created.stderr);

        let shown = run_for_test(&["team", "show", "ops"], &backend);
        assert_eq!(shown.exit_code, 0, "stderr={}", shown.stderr);
        assert!(shown.stdout.contains("default_assignee: --json"));

        cleanup_db(&db_path);
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
            "forge-team-command-{tag}-{pid}-{nanos}-{seq}.sqlite"
        ))
    }

    fn cleanup_db(path: &Path) {
        let _ = std::fs::remove_file(path);
    }
}
