use std::collections::BTreeMap;
#[cfg(test)]
use std::env;
use std::fs::{self, OpenOptions};
use std::io::Write;
use std::path::{Path, PathBuf};
#[cfg(test)]
use std::time::{SystemTime, UNIX_EPOCH};

use chrono::{DateTime, Datelike, Timelike, Utc};
use serde::{Deserialize, Serialize};
use uuid::Uuid;

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct CommandOutput {
    pub stdout: String,
    pub stderr: String,
    pub exit_code: i32,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct JobDefinition {
    pub name: String,
    pub workflow: String,
    pub created_at: String,
    pub updated_at: String,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct JobRunRecord {
    pub run_id: String,
    pub job_name: String,
    pub status: String,
    pub trigger: String,
    pub inputs: BTreeMap<String, String>,
    pub outputs: BTreeMap<String, String>,
    pub started_at: String,
    pub finished_at: Option<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq, Eq)]
pub struct CronTriggerRecord {
    pub trigger_id: String,
    #[serde(default = "default_trigger_type")]
    pub trigger_type: String,
    pub job_name: String,
    pub cron: String,
    pub next_fire_at: String,
    pub enabled: bool,
    pub created_at: String,
    pub updated_at: String,
}

fn default_trigger_type() -> String {
    "cron".to_string()
}

#[derive(Debug, Clone, PartialEq, Eq)]
struct CronSchedule {
    minute: CronField,
    hour: CronField,
    day_of_month: CronField,
    month: CronField,
    day_of_week: CronField,
}

#[derive(Debug, Clone, PartialEq, Eq)]
enum CronField {
    Any,
    Exact(u32),
}

#[derive(Debug, Clone, PartialEq, Eq)]
enum Command {
    Help,
    List,
    Show {
        name: String,
    },
    Create {
        name: String,
        workflow: String,
    },
    Run {
        name: String,
        trigger: String,
        inputs: BTreeMap<String, String>,
    },
    Runs {
        name: String,
        limit: usize,
    },
    Logs {
        name: String,
        limit: usize,
    },
    Cancel {
        run_id: String,
    },
}

#[derive(Debug, Clone, PartialEq, Eq)]
struct ParsedArgs {
    command: Command,
    json: bool,
    jsonl: bool,
    quiet: bool,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct JobStore {
    root: PathBuf,
}

impl JobStore {
    #[must_use]
    pub fn open_from_env() -> Self {
        Self::new(crate::runtime_paths::resolve_data_dir().join("jobs"))
    }

    #[must_use]
    pub fn new(root: PathBuf) -> Self {
        Self { root }
    }

    #[must_use]
    pub fn root(&self) -> &Path {
        &self.root
    }

    pub fn create_job(
        &self,
        name: &str,
        workflow: &str,
        now: &str,
    ) -> Result<JobDefinition, String> {
        let normalized_name = normalize_job_name(name)?;
        let workflow = workflow.trim();
        if workflow.is_empty() {
            return Err("workflow name is required".to_string());
        }

        self.ensure_dirs()?;
        let path = self.definition_path(&normalized_name);
        if path.is_file() {
            return Err(format!("job already exists: {normalized_name}"));
        }

        let definition = JobDefinition {
            name: normalized_name,
            workflow: workflow.to_string(),
            created_at: now.to_string(),
            updated_at: now.to_string(),
        };
        let encoded = serde_json::to_string_pretty(&definition)
            .map_err(|err| format!("encode job definition: {err}"))?;
        fs::write(&path, encoded)
            .map_err(|err| format!("write job definition {}: {err}", path.display()))?;
        Ok(definition)
    }

    pub fn list_jobs(&self) -> Result<Vec<JobDefinition>, String> {
        let dir = self.definitions_dir();
        let entries = match fs::read_dir(&dir) {
            Ok(entries) => entries,
            Err(err) if err.kind() == std::io::ErrorKind::NotFound => return Ok(Vec::new()),
            Err(err) => return Err(format!("read jobs directory {}: {err}", dir.display())),
        };

        let mut out = Vec::new();
        for entry in entries {
            let entry = entry.map_err(|err| format!("read jobs directory entry: {err}"))?;
            let path = entry.path();
            if !path
                .extension()
                .and_then(|value| value.to_str())
                .is_some_and(|ext| ext.eq_ignore_ascii_case("json"))
            {
                continue;
            }
            let raw = fs::read_to_string(&path)
                .map_err(|err| format!("read job definition {}: {err}", path.display()))?;
            let parsed: JobDefinition = serde_json::from_str(&raw)
                .map_err(|err| format!("decode job definition {}: {err}", path.display()))?;
            out.push(parsed);
        }
        out.sort_by(|a, b| a.name.cmp(&b.name));
        Ok(out)
    }

    pub fn get_job(&self, name: &str) -> Result<Option<JobDefinition>, String> {
        let normalized_name = normalize_job_name(name)?;
        let path = self.definition_path(&normalized_name);
        let raw = match fs::read_to_string(&path) {
            Ok(raw) => raw,
            Err(err) if err.kind() == std::io::ErrorKind::NotFound => return Ok(None),
            Err(err) => return Err(format!("read job definition {}: {err}", path.display())),
        };
        let parsed: JobDefinition = serde_json::from_str(&raw)
            .map_err(|err| format!("decode job definition {}: {err}", path.display()))?;
        Ok(Some(parsed))
    }

    pub fn record_run(
        &self,
        name: &str,
        trigger: &str,
        inputs: BTreeMap<String, String>,
    ) -> Result<JobRunRecord, String> {
        let normalized_name = normalize_job_name(name)?;
        let Some(job) = self.get_job(&normalized_name)? else {
            return Err(format!("job not found: {}", normalized_name.trim()));
        };
        let now = now_rfc3339();
        let run = JobRunRecord {
            run_id: format!("jobrun-{}", Uuid::new_v4().simple()),
            job_name: job.name.clone(),
            status: "recorded".to_string(),
            trigger: trigger.trim().to_string(),
            inputs,
            outputs: BTreeMap::new(),
            started_at: now.clone(),
            finished_at: Some(now),
        };
        self.append_run(&run)?;
        Ok(run)
    }

    pub fn append_run(&self, run: &JobRunRecord) -> Result<(), String> {
        self.ensure_dirs()?;
        let path = self.run_log_path(&run.job_name);
        let encoded = serde_json::to_string(run).map_err(|err| format!("encode run: {err}"))?;
        let mut file = OpenOptions::new()
            .create(true)
            .append(true)
            .open(&path)
            .map_err(|err| format!("open run log {}: {err}", path.display()))?;
        file.write_all(encoded.as_bytes())
            .map_err(|err| format!("write run log {}: {err}", path.display()))?;
        file.write_all(b"\n")
            .map_err(|err| format!("write run log {}: {err}", path.display()))?;
        Ok(())
    }

    pub fn list_runs(&self, name: &str, limit: usize) -> Result<Vec<JobRunRecord>, String> {
        let normalized_name = normalize_job_name(name)?;
        let path = self.run_log_path(&normalized_name);
        let raw = match fs::read_to_string(&path) {
            Ok(raw) => raw,
            Err(err) if err.kind() == std::io::ErrorKind::NotFound => return Ok(Vec::new()),
            Err(err) => return Err(format!("read run log {}: {err}", path.display())),
        };

        let mut out = Vec::new();
        for line in raw.lines() {
            let trimmed = line.trim();
            if trimmed.is_empty() {
                continue;
            }
            let record: JobRunRecord = serde_json::from_str(trimmed)
                .map_err(|err| format!("decode run log {}: {err}", path.display()))?;
            out.push(record);
        }

        out.sort_by(|a, b| {
            b.started_at
                .cmp(&a.started_at)
                .then_with(|| a.run_id.cmp(&b.run_id))
        });
        if limit > 0 {
            out.truncate(limit);
        }
        Ok(out)
    }

    pub fn cancel_run(&self, run_id: &str, now: &str) -> Result<JobRunRecord, String> {
        let trimmed_run_id = run_id.trim();
        if trimmed_run_id.is_empty() {
            return Err("run id is required".to_string());
        }

        let dir = self.runs_dir();
        let entries = match fs::read_dir(&dir) {
            Ok(entries) => entries,
            Err(err) if err.kind() == std::io::ErrorKind::NotFound => {
                return Err(format!("run not found: {trimmed_run_id}"));
            }
            Err(err) => return Err(format!("read run log directory {}: {err}", dir.display())),
        };

        for entry in entries {
            let entry = entry.map_err(|err| format!("read run log entry: {err}"))?;
            let path = entry.path();
            if !path
                .extension()
                .and_then(|value| value.to_str())
                .is_some_and(|ext| ext.eq_ignore_ascii_case("jsonl"))
            {
                continue;
            }

            let raw = fs::read_to_string(&path)
                .map_err(|err| format!("read run log {}: {err}", path.display()))?;
            let mut records = Vec::new();
            let mut canceled: Option<JobRunRecord> = None;

            for line in raw.lines() {
                let trimmed = line.trim();
                if trimmed.is_empty() {
                    continue;
                }
                let mut record: JobRunRecord = serde_json::from_str(trimmed)
                    .map_err(|err| format!("decode run log {}: {err}", path.display()))?;
                if record.run_id == trimmed_run_id {
                    record.status = "canceled".to_string();
                    record.finished_at = Some(now.to_string());
                    canceled = Some(record.clone());
                }
                records.push(record);
            }

            let Some(canceled_record) = canceled else {
                continue;
            };

            let mut encoded = String::new();
            for record in &records {
                let line = serde_json::to_string(record).map_err(|err| {
                    format!(
                        "encode run record {} in {}: {err}",
                        record.run_id,
                        path.display()
                    )
                })?;
                encoded.push_str(&line);
                encoded.push('\n');
            }
            fs::write(&path, encoded)
                .map_err(|err| format!("write run log {}: {err}", path.display()))?;
            return Ok(canceled_record);
        }

        Err(format!("run not found: {trimmed_run_id}"))
    }

    pub fn create_cron_trigger(
        &self,
        job_name: &str,
        cron: &str,
        now: DateTime<Utc>,
    ) -> Result<CronTriggerRecord, String> {
        let normalized_name = normalize_job_name(job_name)?;
        if self.get_job(&normalized_name)?.is_none() {
            return Err(format!("job not found: {}", normalized_name));
        }

        let normalized_cron = cron.trim();
        if normalized_cron.is_empty() {
            return Err("cron expression is required".to_string());
        }
        let schedule = parse_cron_schedule(normalized_cron)?;
        let next_fire_at = schedule
            .next_fire_after(now)
            .ok_or_else(|| format!("unable to compute next fire time for cron {:?}", cron))?;

        self.ensure_dirs()?;
        let record = CronTriggerRecord {
            trigger_id: format!("trg_{}", Uuid::new_v4().simple()),
            trigger_type: "cron".to_string(),
            job_name: normalized_name,
            cron: normalized_cron.to_string(),
            next_fire_at: next_fire_at.to_rfc3339(),
            enabled: true,
            created_at: now.to_rfc3339(),
            updated_at: now.to_rfc3339(),
        };
        self.write_trigger(&record)?;
        Ok(record)
    }

    pub fn create_webhook_trigger(
        &self,
        job_name: &str,
        webhook_path: &str,
        now: DateTime<Utc>,
    ) -> Result<CronTriggerRecord, String> {
        let normalized_name = normalize_job_name(job_name)?;
        if self.get_job(&normalized_name)?.is_none() {
            return Err(format!("job not found: {}", normalized_name));
        }

        let webhook_path = webhook_path.trim();
        if webhook_path.is_empty() {
            return Err("webhook path is required".to_string());
        }
        if !webhook_path.starts_with('/') {
            return Err(format!(
                "invalid webhook path {:?}: expected leading '/'",
                webhook_path
            ));
        }

        self.ensure_dirs()?;
        let record = CronTriggerRecord {
            trigger_id: format!("trg_{}", Uuid::new_v4().simple()),
            trigger_type: "webhook".to_string(),
            job_name: normalized_name,
            cron: webhook_path.to_string(),
            next_fire_at: String::new(),
            enabled: true,
            created_at: now.to_rfc3339(),
            updated_at: now.to_rfc3339(),
        };
        self.write_trigger(&record)?;
        Ok(record)
    }

    pub fn list_triggers(&self) -> Result<Vec<CronTriggerRecord>, String> {
        let dir = self.triggers_dir();
        let entries = match fs::read_dir(&dir) {
            Ok(entries) => entries,
            Err(err) if err.kind() == std::io::ErrorKind::NotFound => return Ok(Vec::new()),
            Err(err) => return Err(format!("read triggers directory {}: {err}", dir.display())),
        };

        let mut out = Vec::new();
        for entry in entries {
            let entry = entry.map_err(|err| format!("read triggers directory entry: {err}"))?;
            let path = entry.path();
            if !path
                .extension()
                .and_then(|value| value.to_str())
                .is_some_and(|ext| ext.eq_ignore_ascii_case("json"))
            {
                continue;
            }
            let raw = fs::read_to_string(&path)
                .map_err(|err| format!("read trigger {}: {err}", path.display()))?;
            let parsed: CronTriggerRecord = serde_json::from_str(&raw)
                .map_err(|err| format!("decode trigger {}: {err}", path.display()))?;
            out.push(parsed);
        }
        out.sort_by(|a, b| a.trigger_id.cmp(&b.trigger_id));
        Ok(out)
    }

    pub fn remove_trigger(&self, trigger_id: &str) -> Result<Option<CronTriggerRecord>, String> {
        let trigger_id = trigger_id.trim();
        if trigger_id.is_empty() {
            return Err("trigger id is required".to_string());
        }
        let path = self.trigger_path(trigger_id);
        let raw = match fs::read_to_string(&path) {
            Ok(raw) => raw,
            Err(err) if err.kind() == std::io::ErrorKind::NotFound => return Ok(None),
            Err(err) => return Err(format!("read trigger {}: {err}", path.display())),
        };
        let parsed: CronTriggerRecord = serde_json::from_str(&raw)
            .map_err(|err| format!("decode trigger {}: {err}", path.display()))?;
        fs::remove_file(&path)
            .map_err(|err| format!("remove trigger {}: {err}", path.display()))?;
        Ok(Some(parsed))
    }

    pub fn tick_cron_triggers(&self, now: DateTime<Utc>) -> Result<Vec<JobRunRecord>, String> {
        let mut triggers = self.list_triggers()?;
        let mut fired_runs = Vec::new();

        for trigger in triggers
            .iter_mut()
            .filter(|trigger| trigger.enabled && trigger.trigger_type == "cron")
        {
            let next_fire = DateTime::parse_from_rfc3339(&trigger.next_fire_at)
                .map_err(|err| {
                    format!(
                        "parse trigger next_fire_at {:?}: {err}",
                        trigger.next_fire_at
                    )
                })?
                .with_timezone(&Utc);
            if next_fire > now {
                continue;
            }

            if self.get_job(&trigger.job_name)?.is_none() {
                return Err(format!(
                    "trigger {} references missing job {}",
                    trigger.trigger_id, trigger.job_name
                ));
            }

            let now_text = now.to_rfc3339();
            let run = JobRunRecord {
                run_id: format!("jobrun-{}", Uuid::new_v4().simple()),
                job_name: trigger.job_name.clone(),
                status: "recorded".to_string(),
                trigger: format!("cron:{}", trigger.cron),
                inputs: BTreeMap::new(),
                outputs: BTreeMap::new(),
                started_at: now_text.clone(),
                finished_at: Some(now_text.clone()),
            };
            self.append_run(&run)?;
            fired_runs.push(run);

            let schedule = parse_cron_schedule(&trigger.cron)?;
            let next_fire = schedule
                .next_fire_after(now)
                .ok_or_else(|| {
                    format!(
                        "unable to compute next fire time for trigger {}",
                        trigger.trigger_id
                    )
                })?
                .to_rfc3339();
            trigger.next_fire_at = next_fire;
            trigger.updated_at = now_text;
            self.write_trigger(trigger)?;
        }

        Ok(fired_runs)
    }

    fn ensure_dirs(&self) -> Result<(), String> {
        fs::create_dir_all(self.definitions_dir()).map_err(|err| {
            format!(
                "create jobs directory {}: {err}",
                self.definitions_dir().display()
            )
        })?;
        fs::create_dir_all(self.runs_dir()).map_err(|err| {
            format!(
                "create run log directory {}: {err}",
                self.runs_dir().display()
            )
        })?;
        fs::create_dir_all(self.triggers_dir()).map_err(|err| {
            format!(
                "create trigger directory {}: {err}",
                self.triggers_dir().display()
            )
        })?;
        Ok(())
    }

    fn definitions_dir(&self) -> PathBuf {
        self.root.join("definitions")
    }

    fn runs_dir(&self) -> PathBuf {
        self.root.join("runs")
    }

    fn definition_path(&self, name: &str) -> PathBuf {
        self.definitions_dir().join(format!("{name}.json"))
    }

    fn run_log_path(&self, name: &str) -> PathBuf {
        self.runs_dir().join(format!("{name}.jsonl"))
    }

    fn triggers_dir(&self) -> PathBuf {
        self.root.join("triggers")
    }

    fn trigger_path(&self, trigger_id: &str) -> PathBuf {
        self.triggers_dir().join(format!("{trigger_id}.json"))
    }

    fn write_trigger(&self, trigger: &CronTriggerRecord) -> Result<(), String> {
        let encoded = serde_json::to_string_pretty(trigger)
            .map_err(|err| format!("encode trigger {}: {err}", trigger.trigger_id))?;
        let path = self.trigger_path(&trigger.trigger_id);
        fs::write(&path, encoded).map_err(|err| format!("write trigger {}: {err}", path.display()))
    }
}

impl CronField {
    fn matches(&self, value: u32) -> bool {
        match self {
            Self::Any => true,
            Self::Exact(expected) => *expected == value,
        }
    }
}

impl CronSchedule {
    fn matches(&self, ts: DateTime<Utc>) -> bool {
        self.minute.matches(ts.minute())
            && self.hour.matches(ts.hour())
            && self.day_of_month.matches(ts.day())
            && self.month.matches(ts.month())
            && self
                .day_of_week
                .matches(ts.weekday().num_days_from_sunday())
    }

    fn next_fire_after(&self, after: DateTime<Utc>) -> Option<DateTime<Utc>> {
        let mut candidate = after + chrono::Duration::minutes(1);
        candidate = candidate
            .with_second(0)
            .and_then(|value| value.with_nanosecond(0))
            .unwrap_or(candidate);

        const LOOKAHEAD_MINUTES: i64 = 366 * 24 * 60 * 5;
        for _ in 0..LOOKAHEAD_MINUTES {
            if self.matches(candidate) {
                return Some(candidate);
            }
            candidate += chrono::Duration::minutes(1);
        }
        None
    }
}

fn parse_cron_schedule(raw: &str) -> Result<CronSchedule, String> {
    let fields = raw
        .split_whitespace()
        .map(str::trim)
        .filter(|value| !value.is_empty())
        .collect::<Vec<_>>();
    if fields.len() != 5 {
        return Err(format!(
            "invalid cron {:?}: expected 5 fields (minute hour day month weekday)",
            raw
        ));
    }

    Ok(CronSchedule {
        minute: parse_cron_field(fields[0], 0, 59, "minute")?,
        hour: parse_cron_field(fields[1], 0, 23, "hour")?,
        day_of_month: parse_cron_field(fields[2], 1, 31, "day-of-month")?,
        month: parse_cron_field(fields[3], 1, 12, "month")?,
        day_of_week: parse_cron_field(fields[4], 0, 6, "day-of-week")?,
    })
}

fn parse_cron_field(raw: &str, min: u32, max: u32, label: &str) -> Result<CronField, String> {
    if raw == "*" {
        return Ok(CronField::Any);
    }

    if raw.contains('/') || raw.contains(',') || raw.contains('-') {
        return Err(format!(
            "unsupported {label} field {:?}: only '*' or exact numeric values are supported",
            raw
        ));
    }

    let value = raw
        .parse::<u32>()
        .map_err(|_| format!("invalid {label} field {:?}", raw))?;
    if !(min..=max).contains(&value) {
        return Err(format!(
            "invalid {label} field {:?}: expected {min}..={max}",
            raw
        ));
    }
    Ok(CronField::Exact(value))
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
            let jobs = store.list_jobs()?;
            if parsed.json || parsed.jsonl {
                write_json_or_jsonl(stdout, &jobs, parsed.jsonl)
            } else if jobs.is_empty() {
                writeln!(stdout, "No jobs found").map_err(|err| err.to_string())
            } else {
                for item in jobs {
                    writeln!(stdout, "{}\t{}", item.name, item.workflow)
                        .map_err(|err| err.to_string())?;
                }
                Ok(())
            }
        }
        Command::Show { name } => {
            let Some(job) = store.get_job(&name)? else {
                return Err(format!("job not found: {}", name.trim()));
            };
            let runs = store.list_runs(&job.name, 1)?;
            if parsed.json || parsed.jsonl {
                #[derive(Serialize)]
                struct JobShowResult<'a> {
                    job: &'a JobDefinition,
                    run_count: usize,
                    latest_run: Option<JobRunRecord>,
                }
                let payload = JobShowResult {
                    job: &job,
                    run_count: store.list_runs(&job.name, 0)?.len(),
                    latest_run: runs.into_iter().next(),
                };
                write_json_or_jsonl(stdout, &payload, parsed.jsonl)
            } else {
                writeln!(stdout, "name: {}", job.name).map_err(|err| err.to_string())?;
                writeln!(stdout, "workflow: {}", job.workflow).map_err(|err| err.to_string())?;
                writeln!(stdout, "created_at: {}", job.created_at)
                    .map_err(|err| err.to_string())?;
                writeln!(stdout, "updated_at: {}", job.updated_at)
                    .map_err(|err| err.to_string())?;
                writeln!(
                    stdout,
                    "run_count: {}",
                    store.list_runs(&job.name, 0)?.len()
                )
                .map_err(|err| err.to_string())?;
                Ok(())
            }
        }
        Command::Create { name, workflow } => {
            let now = now_rfc3339();
            let created = store.create_job(&name, &workflow, &now)?;
            if parsed.json || parsed.jsonl {
                write_json_or_jsonl(stdout, &created, parsed.jsonl)
            } else if parsed.quiet {
                Ok(())
            } else {
                writeln!(
                    stdout,
                    "Created job \"{}\" (workflow: {})",
                    created.name, created.workflow
                )
                .map_err(|err| err.to_string())
            }
        }
        Command::Run {
            name,
            trigger,
            inputs,
        } => {
            let run = store.record_run(&name, &trigger, inputs)?;

            if parsed.json || parsed.jsonl {
                write_json_or_jsonl(stdout, &run, parsed.jsonl)
            } else if parsed.quiet {
                Ok(())
            } else {
                writeln!(
                    stdout,
                    "Recorded run {} for job \"{}\"",
                    run.run_id, run.job_name
                )
                .map_err(|err| err.to_string())
            }
        }
        Command::Runs { name, limit } => {
            if store.get_job(&name)?.is_none() {
                return Err(format!("job not found: {}", name.trim()));
            }
            let runs = store.list_runs(&name, limit)?;
            if parsed.json || parsed.jsonl {
                write_json_or_jsonl(stdout, &runs, parsed.jsonl)
            } else if runs.is_empty() {
                writeln!(stdout, "No runs found for \"{}\"", name.trim())
                    .map_err(|err| err.to_string())
            } else {
                for run in runs {
                    writeln!(
                        stdout,
                        "{}\t{}\t{}\t{}",
                        run.run_id, run.status, run.trigger, run.started_at
                    )
                    .map_err(|err| err.to_string())?;
                }
                Ok(())
            }
        }
        Command::Logs { name, limit } => {
            if store.get_job(&name)?.is_none() {
                return Err(format!("job not found: {}", name.trim()));
            }
            let runs = store.list_runs(&name, limit)?;
            if parsed.json || parsed.jsonl {
                write_json_or_jsonl(stdout, &runs, parsed.jsonl)
            } else if runs.is_empty() {
                writeln!(stdout, "No logs found for \"{}\"", name.trim())
                    .map_err(|err| err.to_string())
            } else {
                for run in runs {
                    writeln!(
                        stdout,
                        "[{}] run={} status={} trigger={} finished_at={}",
                        run.started_at,
                        run.run_id,
                        run.status,
                        run.trigger,
                        run.finished_at.unwrap_or_default()
                    )
                    .map_err(|err| err.to_string())?;
                }
                Ok(())
            }
        }
        Command::Cancel { run_id } => {
            let Some(updated) = cancel_run_by_id(store, &run_id)? else {
                return Err(format!("run not found: {}", run_id.trim()));
            };
            if parsed.json || parsed.jsonl {
                write_json_or_jsonl(stdout, &updated, parsed.jsonl)
            } else if parsed.quiet {
                Ok(())
            } else {
                writeln!(
                    stdout,
                    "Canceled run {} for job \"{}\"",
                    updated.run_id, updated.job_name
                )
                .map_err(|err| err.to_string())
            }
        }
    }
}

fn parse_args(args: &[String]) -> Result<ParsedArgs, String> {
    let mut index = if args.first().is_some_and(|arg| arg == "job") {
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
            ensure_no_args("job ls", &args[index..])?;
            Command::List
        }
        "show" => parse_show_args(args, &mut index)?,
        "create" => parse_create_args(args, &mut index)?,
        "run" => parse_run_args(args, &mut index)?,
        "runs" | "history" => parse_runs_args(args, &mut index)?,
        "logs" => parse_logs_args(args, &mut index)?,
        "cancel" => parse_cancel_args(args, &mut index)?,
        other => return Err(format!("unknown job subcommand: {other}")),
    };

    Ok(ParsedArgs {
        command,
        json,
        jsonl,
        quiet,
    })
}

fn parse_create_args(args: &[String], index: &mut usize) -> Result<Command, String> {
    let name = args
        .get(*index)
        .ok_or_else(|| "usage: forge job create <name> --workflow <workflow>".to_string())?
        .to_string();
    *index += 1;

    let mut workflow = String::new();
    while *index < args.len() {
        match args[*index].as_str() {
            "--workflow" => {
                *index += 1;
                workflow = args
                    .get(*index)
                    .ok_or_else(|| "missing value for --workflow".to_string())?
                    .to_string();
                *index += 1;
            }
            other => return Err(format!("unknown flag for job create: {other}")),
        }
    }

    if workflow.trim().is_empty() {
        return Err("usage: forge job create <name> --workflow <workflow>".to_string());
    }
    Ok(Command::Create { name, workflow })
}

fn parse_show_args(args: &[String], index: &mut usize) -> Result<Command, String> {
    let name = args
        .get(*index)
        .ok_or_else(|| "usage: forge job show <name>".to_string())?
        .to_string();
    *index += 1;
    ensure_no_args("job show", &args[*index..])?;
    Ok(Command::Show { name })
}

fn parse_run_args(args: &[String], index: &mut usize) -> Result<Command, String> {
    let name = args
        .get(*index)
        .ok_or_else(|| {
            "usage: forge job run <name> [--trigger <source>] [--input key=value]".to_string()
        })?
        .to_string();
    *index += 1;

    let mut trigger = "manual".to_string();
    let mut inputs = BTreeMap::new();

    while *index < args.len() {
        match args[*index].as_str() {
            "--trigger" => {
                *index += 1;
                trigger = args
                    .get(*index)
                    .ok_or_else(|| "missing value for --trigger".to_string())?
                    .to_string();
                *index += 1;
            }
            "--input" => {
                *index += 1;
                let raw = args
                    .get(*index)
                    .ok_or_else(|| "missing value for --input".to_string())?;
                let (key, value) = parse_key_value(raw)?;
                inputs.insert(key, value);
                *index += 1;
            }
            other => return Err(format!("unknown flag for job run: {other}")),
        }
    }

    Ok(Command::Run {
        name,
        trigger: trigger.trim().to_string(),
        inputs,
    })
}

fn parse_runs_args(args: &[String], index: &mut usize) -> Result<Command, String> {
    let name = args
        .get(*index)
        .ok_or_else(|| "usage: forge job runs <name> [--limit <n>]".to_string())?
        .to_string();
    *index += 1;

    let mut limit: usize = 20;
    while *index < args.len() {
        match args[*index].as_str() {
            "--limit" => {
                *index += 1;
                let raw = args
                    .get(*index)
                    .ok_or_else(|| "missing value for --limit".to_string())?;
                limit = raw
                    .parse::<usize>()
                    .map_err(|_| format!("invalid --limit value: {raw}"))?;
                *index += 1;
            }
            other => return Err(format!("unknown flag for job runs: {other}")),
        }
    }

    Ok(Command::Runs { name, limit })
}

fn parse_logs_args(args: &[String], index: &mut usize) -> Result<Command, String> {
    let name = args
        .get(*index)
        .ok_or_else(|| "usage: forge job logs <name> [--limit <n>]".to_string())?
        .to_string();
    *index += 1;

    let mut limit: usize = 20;
    while *index < args.len() {
        match args[*index].as_str() {
            "--limit" => {
                *index += 1;
                let raw = args
                    .get(*index)
                    .ok_or_else(|| "missing value for --limit".to_string())?;
                limit = raw
                    .parse::<usize>()
                    .map_err(|_| format!("invalid --limit value: {raw}"))?;
                *index += 1;
            }
            other => return Err(format!("unknown flag for job logs: {other}")),
        }
    }
    Ok(Command::Logs { name, limit })
}

fn parse_cancel_args(args: &[String], index: &mut usize) -> Result<Command, String> {
    let run_id = args
        .get(*index)
        .ok_or_else(|| "usage: forge job cancel <run-id>".to_string())?
        .to_string();
    *index += 1;
    ensure_no_args("job cancel", &args[*index..])?;
    Ok(Command::Cancel { run_id })
}

fn cancel_run_by_id(store: &JobStore, run_id: &str) -> Result<Option<JobRunRecord>, String> {
    let run_id = run_id.trim();
    if run_id.is_empty() {
        return Err("run id is required".to_string());
    }
    let dir = store.runs_dir();
    let entries = match fs::read_dir(&dir) {
        Ok(entries) => entries,
        Err(err) if err.kind() == std::io::ErrorKind::NotFound => return Ok(None),
        Err(err) => return Err(format!("read run log directory {}: {err}", dir.display())),
    };

    for entry in entries {
        let entry = entry.map_err(|err| format!("read run log directory entry: {err}"))?;
        let path = entry.path();
        if !path
            .extension()
            .and_then(|value| value.to_str())
            .is_some_and(|ext| ext.eq_ignore_ascii_case("jsonl"))
        {
            continue;
        }
        let raw = fs::read_to_string(&path)
            .map_err(|err| format!("read run log {}: {err}", path.display()))?;
        let mut records = Vec::new();
        let mut found: Option<usize> = None;
        for line in raw.lines() {
            let trimmed = line.trim();
            if trimmed.is_empty() {
                continue;
            }
            let record: JobRunRecord = serde_json::from_str(trimmed)
                .map_err(|err| format!("decode run log {}: {err}", path.display()))?;
            if record.run_id == run_id {
                found = Some(records.len());
            }
            records.push(record);
        }

        if let Some(index) = found {
            if run_status_is_terminal(&records[index].status) {
                return Err(format!(
                    "run {} already terminal (status={})",
                    run_id, records[index].status
                ));
            }
            let finished_at = now_rfc3339();
            records[index].status = "canceled".to_string();
            records[index].finished_at = Some(finished_at);
            let mut encoded = String::new();
            for record in &records {
                let line = serde_json::to_string(record)
                    .map_err(|err| format!("encode run record: {err}"))?;
                encoded.push_str(&line);
                encoded.push('\n');
            }
            fs::write(&path, encoded)
                .map_err(|err| format!("write run log {}: {err}", path.display()))?;
            return Ok(Some(records[index].clone()));
        }
    }

    Ok(None)
}

fn parse_key_value(raw: &str) -> Result<(String, String), String> {
    let Some((key, value)) = raw.split_once('=') else {
        return Err(format!("invalid key=value pair: {raw}"));
    };
    let key = key.trim();
    if key.is_empty() {
        return Err(format!("invalid key=value pair: {raw}"));
    }
    Ok((key.to_string(), value.trim().to_string()))
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

fn normalize_job_name(value: &str) -> Result<String, String> {
    let normalized = value.trim().to_ascii_lowercase();
    if normalized.is_empty() {
        return Err("job name is required".to_string());
    }
    if normalized
        .chars()
        .any(|ch| !(ch.is_ascii_lowercase() || ch.is_ascii_digit() || ch == '-' || ch == '_'))
    {
        return Err(format!("invalid job name: {value}"));
    }
    Ok(normalized)
}

fn run_status_is_terminal(status: &str) -> bool {
    matches!(status, "success" | "failed" | "canceled" | "recorded")
}

fn write_help(stdout: &mut dyn Write) -> std::io::Result<()> {
    writeln!(stdout, "Manage Jobs")?;
    writeln!(stdout)?;
    writeln!(stdout, "Usage:")?;
    writeln!(stdout, "  forge job <command> [options]")?;
    writeln!(stdout)?;
    writeln!(stdout, "Commands:")?;
    writeln!(stdout, "  ls                     List jobs")?;
    writeln!(stdout, "  show <name>            Show a job definition")?;
    writeln!(
        stdout,
        "  create <name> --workflow <workflow>   Create a job definition"
    )?;
    writeln!(
        stdout,
        "  run <name> [--trigger <source>] [--input key=value]   Record a job run"
    )?;
    writeln!(stdout, "  runs <name> [--limit <n>]      List run history")?;
    writeln!(
        stdout,
        "  logs <name> [--limit <n>]      Show recent run logs"
    )?;
    writeln!(
        stdout,
        "  cancel <run-id>                Cancel a running run"
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
fn temp_store(tag: &str) -> JobStore {
    let nanos = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|value| value.as_nanos())
        .unwrap_or(0);
    let path = env::temp_dir().join(format!("forge-job-test-{tag}-{nanos}"));
    JobStore::new(path)
}

#[cfg(test)]
mod tests {
    use std::collections::BTreeMap;

    use chrono::{DateTime, Utc};

    use super::{parse_cron_schedule, run_for_test, temp_store, JobRunRecord, JobStore};

    fn cleanup(store: &JobStore) {
        let _ = std::fs::remove_dir_all(store.root());
    }

    #[test]
    fn create_and_list_jobs() {
        let store = temp_store("create-list");
        let created = run_for_test(
            &["job", "create", "nightly", "--workflow", "wf-nightly"],
            &store,
        );
        assert_eq!(created.exit_code, 0, "stderr={}", created.stderr);

        let listed = run_for_test(&["job", "ls"], &store);
        assert_eq!(listed.exit_code, 0, "stderr={}", listed.stderr);
        assert!(listed.stdout.contains("nightly"));
        assert!(listed.stdout.contains("wf-nightly"));
        cleanup(&store);
    }

    #[test]
    fn duplicate_create_is_rejected() {
        let store = temp_store("duplicate");
        let first = run_for_test(&["job", "create", "nightly", "--workflow", "wf"], &store);
        assert_eq!(first.exit_code, 0, "stderr={}", first.stderr);
        let second = run_for_test(&["job", "create", "nightly", "--workflow", "wf"], &store);
        assert_eq!(second.exit_code, 1);
        assert!(second.stderr.contains("job already exists"));
        cleanup(&store);
    }

    #[test]
    fn run_records_history_and_runs_lists_it() {
        let store = temp_store("run-history");
        let created = run_for_test(
            &["job", "create", "release", "--workflow", "wf-release"],
            &store,
        );
        assert_eq!(created.exit_code, 0, "stderr={}", created.stderr);

        let ran = run_for_test(
            &[
                "job",
                "run",
                "release",
                "--trigger",
                "cron:0 2 * * *",
                "--input",
                "repo=.",
            ],
            &store,
        );
        assert_eq!(ran.exit_code, 0, "stderr={}", ran.stderr);
        assert!(ran.stdout.contains("Recorded run"));

        let history = run_for_test(&["job", "runs", "release"], &store);
        assert_eq!(history.exit_code, 0, "stderr={}", history.stderr);
        assert!(history.stdout.contains("recorded"));
        assert!(history.stdout.contains("cron:0 2 * * *"));
        cleanup(&store);
    }

    #[test]
    fn run_for_missing_job_returns_error() {
        let store = temp_store("missing-job");
        let out = run_for_test(&["job", "run", "unknown"], &store);
        assert_eq!(out.exit_code, 1);
        assert!(out.stderr.contains("job not found"));
        cleanup(&store);
    }

    #[test]
    fn create_requires_workflow_flag() {
        let store = temp_store("requires-workflow");
        let out = run_for_test(&["job", "create", "nightly"], &store);
        assert_eq!(out.exit_code, 1);
        assert!(out.stderr.contains("usage: forge job create"));
        cleanup(&store);
    }

    #[test]
    fn runs_supports_json_output() {
        let store = temp_store("json-runs");
        let created = run_for_test(&["job", "create", "qa", "--workflow", "wf-qa"], &store);
        assert_eq!(created.exit_code, 0, "stderr={}", created.stderr);
        let ran = run_for_test(&["job", "run", "qa"], &store);
        assert_eq!(ran.exit_code, 0, "stderr={}", ran.stderr);

        let out = run_for_test(&["job", "--json", "runs", "qa", "--limit", "1"], &store);
        assert_eq!(out.exit_code, 0, "stderr={}", out.stderr);
        assert!(out.stdout.contains("\"job_name\": \"qa\""));
        assert!(out.stdout.contains("\"status\": \"recorded\""));
        cleanup(&store);
    }

    #[test]
    fn cron_parser_rejects_misconfigured_expression() {
        let err = match parse_cron_schedule("* * * *") {
            Ok(_) => panic!("cron should have 5 fields"),
            Err(err) => err,
        };
        assert!(err.contains("expected 5 fields"));

        let err = match parse_cron_schedule("61 0 * * *") {
            Ok(_) => panic!("minute out of range"),
            Err(err) => err,
        };
        assert!(err.contains("minute"));

        let err = match parse_cron_schedule("*/5 0 * * *") {
            Ok(_) => panic!("unsupported step syntax"),
            Err(err) => err,
        };
        assert!(err.contains("unsupported"));
    }

    #[test]
    fn cron_tick_records_run_and_advances_next_fire_time() {
        let store = temp_store("cron-tick");
        let created = run_for_test(
            &["job", "create", "nightly", "--workflow", "wf-nightly"],
            &store,
        );
        assert_eq!(created.exit_code, 0, "stderr={}", created.stderr);

        let base = match DateTime::parse_from_rfc3339("2026-02-13T00:00:00Z") {
            Ok(base) => base.with_timezone(&Utc),
            Err(err) => panic!("parse base time: {err}"),
        };
        let trigger = match store.create_cron_trigger("nightly", "1 0 * * *", base) {
            Ok(trigger) => trigger,
            Err(err) => panic!("create trigger: {err}"),
        };
        assert!(trigger.next_fire_at.starts_with("2026-02-13T00:01:00"));

        let initial = match store.tick_cron_triggers(base) {
            Ok(initial) => initial,
            Err(err) => panic!("tick at non-fire time: {err}"),
        };
        assert!(initial.is_empty());

        let fire_at = match DateTime::parse_from_rfc3339("2026-02-13T00:01:00Z") {
            Ok(fire_at) => fire_at.with_timezone(&Utc),
            Err(err) => panic!("parse fire time: {err}"),
        };
        let fired = match store.tick_cron_triggers(fire_at) {
            Ok(fired) => fired,
            Err(err) => panic!("tick at fire time: {err}"),
        };
        assert_eq!(fired.len(), 1);
        assert_eq!(fired[0].job_name, "nightly");
        assert_eq!(fired[0].trigger, "cron:1 0 * * *");

        let history = match store.list_runs("nightly", 10) {
            Ok(history) => history,
            Err(err) => panic!("list runs: {err}"),
        };
        assert_eq!(history.len(), 1);
        assert_eq!(history[0].trigger, "cron:1 0 * * *");

        let triggers = match store.list_triggers() {
            Ok(triggers) => triggers,
            Err(err) => panic!("list triggers: {err}"),
        };
        assert_eq!(triggers.len(), 1);
        assert!(triggers[0].next_fire_at.starts_with("2026-02-14T00:01:00"));

        cleanup(&store);
    }

    #[test]
    fn show_subcommand_outputs_job_definition() {
        let store = temp_store("show-job");
        let created = run_for_test(
            &["job", "create", "nightly", "--workflow", "wf-nightly"],
            &store,
        );
        assert_eq!(created.exit_code, 0, "stderr={}", created.stderr);

        let show = run_for_test(&["job", "show", "nightly"], &store);
        assert_eq!(show.exit_code, 0, "stderr={}", show.stderr);
        assert!(show.stdout.contains("name: nightly"));
        assert!(show.stdout.contains("workflow: wf-nightly"));
        cleanup(&store);
    }

    #[test]
    fn logs_subcommand_returns_history_lines() {
        let store = temp_store("logs-job");
        let created = run_for_test(&["job", "create", "qa", "--workflow", "wf-qa"], &store);
        assert_eq!(created.exit_code, 0, "stderr={}", created.stderr);
        let ran = run_for_test(&["job", "run", "qa"], &store);
        assert_eq!(ran.exit_code, 0, "stderr={}", ran.stderr);

        let logs = run_for_test(&["job", "logs", "qa", "--limit", "1"], &store);
        assert_eq!(logs.exit_code, 0, "stderr={}", logs.stderr);
        assert!(logs.stdout.contains("run="));
        assert!(logs.stdout.contains("status=recorded"));
        cleanup(&store);
    }

    #[test]
    fn cancel_subcommand_marks_running_run_canceled() {
        let store = temp_store("cancel-job");
        let created = run_for_test(&["job", "create", "ops", "--workflow", "wf-ops"], &store);
        assert_eq!(created.exit_code, 0, "stderr={}", created.stderr);

        let running = JobRunRecord {
            run_id: "jobrun-running-1".to_string(),
            job_name: "ops".to_string(),
            status: "running".to_string(),
            trigger: "manual".to_string(),
            inputs: BTreeMap::new(),
            outputs: BTreeMap::new(),
            started_at: "2026-02-13T00:00:00Z".to_string(),
            finished_at: None,
        };
        if let Err(err) = store.append_run(&running) {
            panic!("append running run: {err}");
        }

        let out = run_for_test(&["job", "cancel", "jobrun-running-1"], &store);
        assert_eq!(out.exit_code, 0, "stderr={}", out.stderr);
        assert!(out.stdout.contains("Canceled run"));

        let runs = match store.list_runs("ops", 1) {
            Ok(runs) => runs,
            Err(err) => panic!("list runs: {err}"),
        };
        assert_eq!(runs[0].status, "canceled");
        cleanup(&store);
    }

    #[test]
    fn webhook_trigger_can_be_created_and_removed() {
        let store = temp_store("webhook-trigger");
        let created = run_for_test(&["job", "create", "ship", "--workflow", "wf-ship"], &store);
        assert_eq!(created.exit_code, 0, "stderr={}", created.stderr);

        let now = match DateTime::parse_from_rfc3339("2026-02-13T00:00:00Z") {
            Ok(now) => now.with_timezone(&Utc),
            Err(err) => panic!("parse now time: {err}"),
        };
        let trigger = match store.create_webhook_trigger("ship", "/hooks/ship", now) {
            Ok(trigger) => trigger,
            Err(err) => panic!("create webhook trigger: {err}"),
        };
        assert_eq!(trigger.trigger_type, "webhook");
        assert_eq!(trigger.cron, "/hooks/ship");

        let removed = match store.remove_trigger(&trigger.trigger_id) {
            Ok(Some(removed)) => removed,
            Ok(None) => panic!("trigger exists"),
            Err(err) => panic!("remove trigger: {err}"),
        };
        assert_eq!(removed.trigger_id, trigger.trigger_id);
        cleanup(&store);
    }

    #[test]
    fn rejects_json_and_jsonl_together() {
        let store = temp_store("json-conflict");
        let out = run_for_test(&["job", "--json", "--jsonl", "ls"], &store);
        assert_eq!(out.exit_code, 1);
        assert!(out
            .stderr
            .contains("--json and --jsonl are mutually exclusive"));
        cleanup(&store);
    }
}
