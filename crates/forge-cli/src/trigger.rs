#[cfg(test)]
use std::env;
use std::io::Write;
#[cfg(test)]
use std::time::{SystemTime, UNIX_EPOCH};

use chrono::Utc;
use serde::Serialize;

use crate::job::{CronTriggerRecord, JobStore};

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct CommandOutput {
    pub stdout: String,
    pub stderr: String,
    pub exit_code: i32,
}

#[derive(Debug, Clone, PartialEq, Eq)]
enum Command {
    Help,
    List,
    Add { spec: String, job: String },
    Remove { trigger_id: String },
}

#[derive(Debug, Clone, PartialEq, Eq)]
struct ParsedArgs {
    command: Command,
    json: bool,
    jsonl: bool,
    quiet: bool,
}

#[derive(Debug, Clone, Serialize)]
struct TriggerListItem {
    trigger_id: String,
    trigger_type: String,
    job_name: String,
    spec: String,
    next_fire_at: Option<String>,
    enabled: bool,
}

pub fn run_with_store(
    args: &[String],
    store: &JobStore,
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

pub fn run_for_test(args: &[&str], store: &JobStore) -> CommandOutput {
    let owned_args = args
        .iter()
        .map(|arg| (*arg).to_string())
        .collect::<Vec<_>>();
    let mut stdout = Vec::new();
    let mut stderr = Vec::new();
    let exit_code = run_with_store(&owned_args, store, &mut stdout, &mut stderr);
    CommandOutput {
        stdout: String::from_utf8_lossy(&stdout).into_owned(),
        stderr: String::from_utf8_lossy(&stderr).into_owned(),
        exit_code,
    }
}

fn execute(args: &[String], store: &JobStore, stdout: &mut dyn Write) -> Result<(), String> {
    let parsed = parse_args(args)?;
    match parsed.command {
        Command::Help => write_help(stdout).map_err(|err| err.to_string()),
        Command::List => {
            let items = store.list_triggers()?;
            let rendered = items_to_list(&items);
            if parsed.json || parsed.jsonl {
                write_json_or_jsonl(stdout, &rendered, parsed.jsonl)
            } else if rendered.is_empty() {
                writeln!(stdout, "No triggers found").map_err(|err| err.to_string())
            } else {
                for item in rendered {
                    writeln!(
                        stdout,
                        "{}\t{}\t{}\t{}",
                        item.trigger_id, item.trigger_type, item.job_name, item.spec
                    )
                    .map_err(|err| err.to_string())?;
                }
                Ok(())
            }
        }
        Command::Add { spec, job } => {
            let now = Utc::now();
            let created = if let Some(cron) = spec.strip_prefix("cron:") {
                store.create_cron_trigger(&job, cron, now)?
            } else if let Some(path) = spec.strip_prefix("webhook:") {
                store.create_webhook_trigger(&job, path, now)?
            } else {
                return Err(format!(
                    "unsupported trigger spec {:?}; use cron:<expr> or webhook:</path>",
                    spec
                ));
            };
            let rendered = to_list_item(&created);
            if parsed.json || parsed.jsonl {
                write_json_or_jsonl(stdout, &rendered, parsed.jsonl)
            } else if parsed.quiet {
                Ok(())
            } else {
                writeln!(
                    stdout,
                    "Created trigger {} ({}) for job \"{}\"",
                    created.trigger_id, created.trigger_type, created.job_name
                )
                .map_err(|err| err.to_string())
            }
        }
        Command::Remove { trigger_id } => {
            let Some(removed) = store.remove_trigger(&trigger_id)? else {
                return Err(format!("trigger not found: {}", trigger_id.trim()));
            };
            let rendered = to_list_item(&removed);
            if parsed.json || parsed.jsonl {
                write_json_or_jsonl(stdout, &rendered, parsed.jsonl)
            } else if parsed.quiet {
                Ok(())
            } else {
                writeln!(stdout, "Removed trigger {}", removed.trigger_id)
                    .map_err(|err| err.to_string())
            }
        }
    }
}

fn parse_args(args: &[String]) -> Result<ParsedArgs, String> {
    let mut index = if args.first().is_some_and(|arg| arg == "trigger") {
        1
    } else {
        0
    };
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

    let sub = args[index].as_str();
    index += 1;
    let command = match sub {
        "help" | "-h" | "--help" => Command::Help,
        "ls" | "list" => {
            ensure_no_args("trigger ls", &args[index..])?;
            Command::List
        }
        "add" => parse_add_args(args, &mut index)?,
        "rm" | "remove" => parse_remove_args(args, &mut index)?,
        other => return Err(format!("unknown trigger subcommand: {other}")),
    };

    Ok(ParsedArgs {
        command,
        json,
        jsonl,
        quiet,
    })
}

fn parse_add_args(args: &[String], index: &mut usize) -> Result<Command, String> {
    let spec = args
        .get(*index)
        .ok_or_else(|| "usage: forge trigger add <spec> --job <name>".to_string())?
        .to_string();
    *index += 1;

    let mut job = String::new();
    while *index < args.len() {
        match args[*index].as_str() {
            "--job" => {
                *index += 1;
                job = args
                    .get(*index)
                    .ok_or_else(|| "missing value for --job".to_string())?
                    .to_string();
                *index += 1;
            }
            other => return Err(format!("unknown flag for trigger add: {other}")),
        }
    }
    if job.trim().is_empty() {
        return Err("usage: forge trigger add <spec> --job <name>".to_string());
    }
    Ok(Command::Add { spec, job })
}

fn parse_remove_args(args: &[String], index: &mut usize) -> Result<Command, String> {
    let trigger_id = args
        .get(*index)
        .ok_or_else(|| "usage: forge trigger rm <trigger-id>".to_string())?
        .to_string();
    *index += 1;
    ensure_no_args("trigger rm", &args[*index..])?;
    Ok(Command::Remove { trigger_id })
}

fn ensure_no_args(command: &str, args: &[String]) -> Result<(), String> {
    if args.is_empty() {
        Ok(())
    } else {
        Err(format!(
            "unexpected arguments for {command}: {}",
            args.join(" ")
        ))
    }
}

fn items_to_list(items: &[CronTriggerRecord]) -> Vec<TriggerListItem> {
    items.iter().map(to_list_item).collect()
}

fn to_list_item(item: &CronTriggerRecord) -> TriggerListItem {
    let spec = match item.trigger_type.as_str() {
        "webhook" => format!("webhook:{}", item.cron),
        _ => format!("cron:{}", item.cron),
    };
    let next_fire_at = if item.next_fire_at.trim().is_empty() {
        None
    } else {
        Some(item.next_fire_at.clone())
    };
    TriggerListItem {
        trigger_id: item.trigger_id.clone(),
        trigger_type: item.trigger_type.clone(),
        job_name: item.job_name.clone(),
        spec,
        next_fire_at,
        enabled: item.enabled,
    }
}

fn write_help(stdout: &mut dyn Write) -> std::io::Result<()> {
    writeln!(stdout, "Manage Triggers")?;
    writeln!(stdout)?;
    writeln!(stdout, "Usage:")?;
    writeln!(stdout, "  forge trigger <command> [options]")?;
    writeln!(stdout)?;
    writeln!(stdout, "Commands:")?;
    writeln!(stdout, "  ls                         List triggers")?;
    writeln!(
        stdout,
        "  add <spec> --job <name>    Add trigger (cron:<expr> or webhook:</path>)"
    )?;
    writeln!(stdout, "  rm <trigger-id>            Remove trigger")?;
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
fn temp_store(tag: &str) -> JobStore {
    let nanos = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|value| value.as_nanos())
        .unwrap_or(0);
    let path = env::temp_dir().join(format!("forge-trigger-test-{tag}-{nanos}"));
    JobStore::new(path)
}

#[cfg(test)]
mod tests {
    use super::{run_for_test, temp_store, JobStore};

    fn cleanup(store: &JobStore) {
        let _ = std::fs::remove_dir_all(store.root());
    }

    #[test]
    fn add_list_remove_cron_trigger() {
        let store = temp_store("cron");
        let now = "2026-02-13T00:00:00Z";
        if let Err(err) = store.create_job("nightly", "wf-nightly", now) {
            panic!("create job: {err}");
        }

        let add = run_for_test(
            &["trigger", "add", "cron:0 2 * * *", "--job", "nightly"],
            &store,
        );
        assert_eq!(add.exit_code, 0, "stderr={}", add.stderr);
        assert!(add.stdout.contains("Created trigger"));

        let list = run_for_test(&["trigger", "ls"], &store);
        assert_eq!(list.exit_code, 0, "stderr={}", list.stderr);
        assert!(list.stdout.contains("cron"));
        assert!(list.stdout.contains("nightly"));

        let listed = match store.list_triggers() {
            Ok(listed) => listed,
            Err(err) => panic!("list triggers: {err}"),
        };
        let id = listed[0].trigger_id.clone();
        let rm = run_for_test(&["trigger", "rm", &id], &store);
        assert_eq!(rm.exit_code, 0, "stderr={}", rm.stderr);
        assert!(rm.stdout.contains("Removed trigger"));
        cleanup(&store);
    }

    #[test]
    fn add_webhook_trigger() {
        let store = temp_store("webhook");
        let now = "2026-02-13T00:00:00Z";
        if let Err(err) = store.create_job("ship", "wf-ship", now) {
            panic!("create job: {err}");
        }

        let out = run_for_test(
            &["trigger", "add", "webhook:/hooks/ship", "--job", "ship"],
            &store,
        );
        assert_eq!(out.exit_code, 0, "stderr={}", out.stderr);
        assert!(out.stdout.contains("webhook"));
        cleanup(&store);
    }

    #[test]
    fn rejects_invalid_trigger_spec() {
        let store = temp_store("invalid-spec");
        let now = "2026-02-13T00:00:00Z";
        if let Err(err) = store.create_job("qa", "wf-qa", now) {
            panic!("create job: {err}");
        }

        let out = run_for_test(&["trigger", "add", "timer:5m", "--job", "qa"], &store);
        assert_eq!(out.exit_code, 1);
        assert!(out.stderr.contains("unsupported trigger spec"));
        cleanup(&store);
    }

    #[test]
    fn rejects_json_and_jsonl_together() {
        let store = temp_store("json-conflict");
        let out = run_for_test(&["trigger", "--json", "--jsonl", "ls"], &store);
        assert_eq!(out.exit_code, 1);
        assert!(out
            .stderr
            .contains("--json and --jsonl are mutually exclusive"));
        cleanup(&store);
    }
}
