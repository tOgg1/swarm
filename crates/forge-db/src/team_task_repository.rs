//! Team task inbox repository — queue + assignment lifecycle + audit trail.

use rusqlite::{params, OptionalExtension};
use uuid::Uuid;

use crate::{Db, DbError};

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum TeamTaskStatus {
    Queued,
    Assigned,
    Running,
    Blocked,
    Done,
    Failed,
    Canceled,
}

impl TeamTaskStatus {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Queued => "queued",
            Self::Assigned => "assigned",
            Self::Running => "running",
            Self::Blocked => "blocked",
            Self::Done => "done",
            Self::Failed => "failed",
            Self::Canceled => "canceled",
        }
    }

    pub fn parse(value: &str) -> Result<Self, DbError> {
        match value {
            "queued" => Ok(Self::Queued),
            "assigned" => Ok(Self::Assigned),
            "running" => Ok(Self::Running),
            "blocked" => Ok(Self::Blocked),
            "done" => Ok(Self::Done),
            "failed" => Ok(Self::Failed),
            "canceled" => Ok(Self::Canceled),
            other => Err(DbError::Validation(format!(
                "invalid team task status: {other}"
            ))),
        }
    }

    fn is_terminal(self) -> bool {
        matches!(self, Self::Done | Self::Failed | Self::Canceled)
    }
}

#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct TeamTask {
    pub id: String,
    pub team_id: String,
    pub payload_json: String,
    pub status: String,
    pub priority: i64,
    pub assigned_agent_id: String,
    pub submitted_at: String,
    pub assigned_at: Option<String>,
    pub started_at: Option<String>,
    pub finished_at: Option<String>,
    pub updated_at: String,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct TeamTaskEvent {
    pub id: i64,
    pub task_id: String,
    pub team_id: String,
    pub event_type: String,
    pub from_status: Option<String>,
    pub to_status: Option<String>,
    pub actor_agent_id: Option<String>,
    pub detail: Option<String>,
    pub created_at: String,
}

#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct TeamTaskFilter {
    pub team_id: String,
    pub statuses: Vec<String>,
    pub assigned_agent_id: String,
    pub limit: usize,
}

fn validate_payload(payload_json: &str) -> Result<String, DbError> {
    let trimmed = payload_json.trim();
    if trimmed.is_empty() {
        return Err(DbError::Validation("task payload is required".to_owned()));
    }
    let value = serde_json::from_str::<serde_json::Value>(trimmed)
        .map_err(|err| DbError::Validation(format!("invalid task payload json: {err}")))?;
    let obj = value
        .as_object()
        .ok_or_else(|| DbError::Validation("task payload must be a JSON object".to_owned()))?;
    if obj.get("type").and_then(|v| v.as_str()).is_none() {
        return Err(DbError::Validation(
            "task payload requires string field \"type\"".to_owned(),
        ));
    }
    if obj.get("title").and_then(|v| v.as_str()).is_none() {
        return Err(DbError::Validation(
            "task payload requires string field \"title\"".to_owned(),
        ));
    }
    serde_json::to_string(&value)
        .map_err(|err| DbError::Validation(format!("serialize task payload: {err}")))
}

fn validate_task(task: &TeamTask) -> Result<(), DbError> {
    if task.team_id.trim().is_empty() {
        return Err(DbError::Validation("team_id is required".to_owned()));
    }
    TeamTaskStatus::parse(task.status.trim())?;
    if task.priority < 0 {
        return Err(DbError::Validation("task priority must be >= 0".to_owned()));
    }
    Ok(())
}

fn now_rfc3339() -> String {
    let duration = match std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH) {
        Ok(d) => d,
        Err(_) => std::time::Duration::from_secs(0),
    };
    let secs = duration.as_secs();
    let days = secs / 86400;
    let time_of_day = secs % 86400;
    let hours = time_of_day / 3600;
    let minutes = (time_of_day % 3600) / 60;
    let seconds = time_of_day % 60;

    let (year, month, day) = days_to_civil(days as i64);
    format!("{year:04}-{month:02}-{day:02}T{hours:02}:{minutes:02}:{seconds:02}Z")
}

fn days_to_civil(days: i64) -> (i32, u32, u32) {
    let z = days + 719468;
    let era = if z >= 0 { z } else { z - 146096 } / 146097;
    let doe = (z - era * 146097) as u32;
    let yoe = (doe - doe / 1460 + doe / 36524 - doe / 146096) / 365;
    let y = (yoe as i64 + era * 400) as i32;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let d = doy - (153 * mp + 2) / 5 + 1;
    let m = if mp < 10 { mp + 3 } else { mp - 9 };
    let y = if m <= 2 { y + 1 } else { y };
    (y, m, d)
}

fn is_unique_constraint_error(err: &rusqlite::Error) -> bool {
    err.to_string().contains("UNIQUE constraint failed")
}

fn is_fk_constraint_error(err: &rusqlite::Error) -> bool {
    err.to_string().contains("FOREIGN KEY constraint failed")
}

pub struct TeamTaskRepository<'a> {
    db: &'a Db,
}

impl<'a> TeamTaskRepository<'a> {
    pub fn new(db: &'a Db) -> Self {
        Self { db }
    }

    pub fn submit(&self, task: &mut TeamTask) -> Result<(), DbError> {
        if task.id.trim().is_empty() {
            task.id = Uuid::new_v4().to_string();
        }
        task.team_id = task.team_id.trim().to_owned();
        task.status = if task.status.trim().is_empty() {
            TeamTaskStatus::Queued.as_str().to_owned()
        } else {
            task.status.trim().to_owned()
        };
        task.payload_json = validate_payload(&task.payload_json)?;
        if task.priority == 0 {
            task.priority = 100;
        }
        task.assigned_agent_id = task.assigned_agent_id.trim().to_owned();
        validate_task(task)?;

        let now = now_rfc3339();
        task.submitted_at = now.clone();
        task.updated_at = now;
        task.assigned_at = None;
        task.started_at = None;
        task.finished_at = None;

        let result = self.db.conn().execute(
            "INSERT INTO team_tasks (
                id, team_id, payload_json, status, priority, assigned_agent_id,
                submitted_at, assigned_at, started_at, finished_at, updated_at
             ) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, NULL, NULL, NULL, ?8)",
            params![
                task.id,
                task.team_id,
                task.payload_json,
                task.status,
                task.priority,
                nullable_string(&task.assigned_agent_id),
                task.submitted_at,
                task.updated_at,
            ],
        );

        match result {
            Ok(_) => {
                self.insert_event(
                    &task.id,
                    &task.team_id,
                    "submitted",
                    None,
                    Some(TeamTaskStatus::Queued.as_str()),
                    None,
                    None,
                )?;
                Ok(())
            }
            Err(err) => {
                if is_unique_constraint_error(&err) {
                    Err(DbError::TeamTaskAlreadyExists)
                } else if is_fk_constraint_error(&err) {
                    Err(DbError::TeamNotFound)
                } else {
                    Err(DbError::Open(err))
                }
            }
        }
    }

    pub fn get(&self, task_id: &str) -> Result<TeamTask, DbError> {
        let result = self
            .db
            .conn()
            .query_row(
                "SELECT id, team_id, payload_json, status, priority, assigned_agent_id, submitted_at, assigned_at, started_at, finished_at, updated_at
                 FROM team_tasks
                 WHERE id = ?1",
                params![task_id],
                scan_team_task,
            )
            .optional()?;
        result.ok_or(DbError::TeamTaskNotFound)
    }

    pub fn list(&self, filter: &TeamTaskFilter) -> Result<Vec<TeamTask>, DbError> {
        if filter.team_id.trim().is_empty() {
            return Err(DbError::Validation("team_id is required".to_owned()));
        }
        let mut stmt = self.db.conn().prepare(
            "SELECT id, team_id, payload_json, status, priority, assigned_agent_id, submitted_at, assigned_at, started_at, finished_at, updated_at
             FROM team_tasks
             WHERE team_id = ?1
             ORDER BY priority ASC, submitted_at ASC, id ASC",
        )?;
        let rows = stmt.query_map(params![filter.team_id.trim()], scan_team_task)?;
        let mut tasks = Vec::new();
        for row in rows {
            tasks.push(row?);
        }

        if !filter.statuses.is_empty() {
            let allowed = filter
                .statuses
                .iter()
                .filter_map(|status| TeamTaskStatus::parse(status.trim()).ok())
                .map(TeamTaskStatus::as_str)
                .collect::<std::collections::HashSet<_>>();
            tasks.retain(|task| allowed.contains(task.status.as_str()));
        }
        if !filter.assigned_agent_id.trim().is_empty() {
            let assigned = filter.assigned_agent_id.trim();
            tasks.retain(|task| task.assigned_agent_id == assigned);
        }
        let limit = if filter.limit == 0 { 100 } else { filter.limit };
        tasks.truncate(limit);
        Ok(tasks)
    }

    pub fn assign(
        &self,
        task_id: &str,
        agent_id: &str,
        actor: Option<&str>,
    ) -> Result<TeamTask, DbError> {
        self.transition_with_assignment(
            task_id,
            TeamTaskStatus::Assigned,
            Some(agent_id),
            "assigned",
            actor,
            None,
            &[TeamTaskStatus::Queued, TeamTaskStatus::Blocked],
        )
    }

    pub fn reassign(
        &self,
        task_id: &str,
        agent_id: &str,
        actor: Option<&str>,
    ) -> Result<TeamTask, DbError> {
        self.transition_with_assignment(
            task_id,
            TeamTaskStatus::Assigned,
            Some(agent_id),
            "reassigned",
            actor,
            None,
            &[TeamTaskStatus::Assigned],
        )
    }

    pub fn start(&self, task_id: &str, actor: Option<&str>) -> Result<TeamTask, DbError> {
        self.transition_with_assignment(
            task_id,
            TeamTaskStatus::Running,
            None,
            "started",
            actor,
            None,
            &[TeamTaskStatus::Assigned, TeamTaskStatus::Blocked],
        )
    }

    pub fn block(
        &self,
        task_id: &str,
        actor: Option<&str>,
        reason: Option<&str>,
    ) -> Result<TeamTask, DbError> {
        self.transition_with_assignment(
            task_id,
            TeamTaskStatus::Blocked,
            None,
            "blocked",
            actor,
            reason,
            &[TeamTaskStatus::Assigned, TeamTaskStatus::Running],
        )
    }

    pub fn complete(
        &self,
        task_id: &str,
        actor: Option<&str>,
        detail: Option<&str>,
    ) -> Result<TeamTask, DbError> {
        self.transition_with_assignment(
            task_id,
            TeamTaskStatus::Done,
            None,
            "completed",
            actor,
            detail,
            &[
                TeamTaskStatus::Running,
                TeamTaskStatus::Blocked,
                TeamTaskStatus::Assigned,
            ],
        )
    }

    pub fn fail(
        &self,
        task_id: &str,
        actor: Option<&str>,
        detail: Option<&str>,
    ) -> Result<TeamTask, DbError> {
        self.transition_with_assignment(
            task_id,
            TeamTaskStatus::Failed,
            None,
            "failed",
            actor,
            detail,
            &[
                TeamTaskStatus::Queued,
                TeamTaskStatus::Assigned,
                TeamTaskStatus::Running,
                TeamTaskStatus::Blocked,
            ],
        )
    }

    pub fn cancel(
        &self,
        task_id: &str,
        actor: Option<&str>,
        detail: Option<&str>,
    ) -> Result<TeamTask, DbError> {
        self.transition_with_assignment(
            task_id,
            TeamTaskStatus::Canceled,
            None,
            "canceled",
            actor,
            detail,
            &[
                TeamTaskStatus::Queued,
                TeamTaskStatus::Assigned,
                TeamTaskStatus::Running,
                TeamTaskStatus::Blocked,
            ],
        )
    }

    pub fn retry(&self, task_id: &str, actor: Option<&str>) -> Result<(TeamTask, TeamTask), DbError> {
        let source = self.get(task_id)?;
        let source_status = TeamTaskStatus::parse(source.status.trim())?;
        if !source_status.is_terminal() {
            return Err(DbError::Validation(format!(
                "retry requires terminal task status (done|failed|canceled), got {}",
                source.status
            )));
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
        self.submit(&mut retry)?;

        let detail = format!("source_task_id={}", source.id);
        self.db.conn().execute(
            "UPDATE team_task_events
             SET actor_agent_id = ?1,
                 detail = ?2
             WHERE id = (
                SELECT id
                FROM team_task_events
                WHERE task_id = ?3
                  AND event_type = 'submitted'
                ORDER BY id DESC
                LIMIT 1
             )",
            params![actor, detail, retry.id],
        )?;

        Ok((source, retry))
    }

    pub fn list_events(&self, task_id: &str) -> Result<Vec<TeamTaskEvent>, DbError> {
        let mut stmt = self.db.conn().prepare(
            "SELECT id, task_id, team_id, event_type, from_status, to_status, actor_agent_id, detail, created_at
             FROM team_task_events
             WHERE task_id = ?1
             ORDER BY id ASC",
        )?;
        let rows = stmt.query_map(params![task_id], scan_team_task_event)?;
        let mut events = Vec::new();
        for row in rows {
            events.push(row?);
        }
        Ok(events)
    }

    fn transition_with_assignment(
        &self,
        task_id: &str,
        to_status: TeamTaskStatus,
        assigned_agent_id: Option<&str>,
        event_type: &str,
        actor: Option<&str>,
        detail: Option<&str>,
        allowed_from: &[TeamTaskStatus],
    ) -> Result<TeamTask, DbError> {
        let mut task = self.get(task_id)?;
        let from_status = TeamTaskStatus::parse(task.status.trim())?;
        if from_status.is_terminal() {
            return Err(DbError::Validation(format!(
                "cannot transition terminal task from {} to {}",
                from_status.as_str(),
                to_status.as_str()
            )));
        }
        if !allowed_from.contains(&from_status) {
            return Err(DbError::Validation(format!(
                "invalid transition {} -> {}",
                from_status.as_str(),
                to_status.as_str()
            )));
        }

        let now = now_rfc3339();
        task.status = to_status.as_str().to_owned();
        if let Some(assignee) = assigned_agent_id {
            task.assigned_agent_id = assignee.trim().to_owned();
            task.assigned_at = Some(now.clone());
        }
        if to_status == TeamTaskStatus::Running && task.started_at.is_none() {
            task.started_at = Some(now.clone());
        }
        if to_status.is_terminal() {
            task.finished_at = Some(now.clone());
        }
        task.updated_at = now.clone();

        let rows = self.db.conn().execute(
            "UPDATE team_tasks
             SET status = ?1,
                 assigned_agent_id = ?2,
                 assigned_at = ?3,
                 started_at = ?4,
                 finished_at = ?5,
                 updated_at = ?6
             WHERE id = ?7",
            params![
                task.status,
                nullable_string(&task.assigned_agent_id),
                task.assigned_at,
                task.started_at,
                task.finished_at,
                task.updated_at,
                task.id,
            ],
        )?;

        if rows == 0 {
            return Err(DbError::TeamTaskNotFound);
        }

        self.insert_event(
            &task.id,
            &task.team_id,
            event_type,
            Some(from_status.as_str()),
            Some(to_status.as_str()),
            actor,
            detail,
        )?;
        Ok(task)
    }

    fn insert_event(
        &self,
        task_id: &str,
        team_id: &str,
        event_type: &str,
        from_status: Option<&str>,
        to_status: Option<&str>,
        actor: Option<&str>,
        detail: Option<&str>,
    ) -> Result<(), DbError> {
        self.db.conn().execute(
            "INSERT INTO team_task_events (
                task_id, team_id, event_type, from_status, to_status, actor_agent_id, detail
             ) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7)",
            params![
                task_id,
                team_id,
                event_type,
                from_status,
                to_status,
                actor,
                detail,
            ],
        )?;
        Ok(())
    }
}

pub struct TeamTaskService<'a> {
    repo: TeamTaskRepository<'a>,
}

impl<'a> TeamTaskService<'a> {
    pub fn new(db: &'a Db) -> Self {
        Self {
            repo: TeamTaskRepository::new(db),
        }
    }

    pub fn submit(
        &self,
        team_id: &str,
        payload_json: &str,
        priority: i64,
    ) -> Result<TeamTask, DbError> {
        let mut task = TeamTask {
            id: String::new(),
            team_id: team_id.to_owned(),
            payload_json: payload_json.to_owned(),
            status: TeamTaskStatus::Queued.as_str().to_owned(),
            priority,
            assigned_agent_id: String::new(),
            submitted_at: String::new(),
            assigned_at: None,
            started_at: None,
            finished_at: None,
            updated_at: String::new(),
        };
        self.repo.submit(&mut task)?;
        Ok(task)
    }

    pub fn list_queue(&self, team_id: &str, limit: usize) -> Result<Vec<TeamTask>, DbError> {
        self.repo.list(&TeamTaskFilter {
            team_id: team_id.to_owned(),
            statuses: vec![
                TeamTaskStatus::Queued.as_str().to_owned(),
                TeamTaskStatus::Assigned.as_str().to_owned(),
                TeamTaskStatus::Running.as_str().to_owned(),
                TeamTaskStatus::Blocked.as_str().to_owned(),
            ],
            assigned_agent_id: String::new(),
            limit,
        })
    }

    pub fn assign(
        &self,
        task_id: &str,
        agent_id: &str,
        actor: Option<&str>,
    ) -> Result<TeamTask, DbError> {
        self.repo.assign(task_id, agent_id, actor)
    }

    pub fn reassign(
        &self,
        task_id: &str,
        agent_id: &str,
        actor: Option<&str>,
    ) -> Result<TeamTask, DbError> {
        self.repo.reassign(task_id, agent_id, actor)
    }

    pub fn complete(
        &self,
        task_id: &str,
        actor: Option<&str>,
        detail: Option<&str>,
    ) -> Result<TeamTask, DbError> {
        self.repo.complete(task_id, actor, detail)
    }

    pub fn fail(
        &self,
        task_id: &str,
        actor: Option<&str>,
        detail: Option<&str>,
    ) -> Result<TeamTask, DbError> {
        self.repo.fail(task_id, actor, detail)
    }

    pub fn retry(&self, task_id: &str, actor: Option<&str>) -> Result<(TeamTask, TeamTask), DbError> {
        self.repo.retry(task_id, actor)
    }
}

fn nullable_string(value: &str) -> Option<&str> {
    if value.trim().is_empty() {
        None
    } else {
        Some(value)
    }
}

fn scan_team_task(row: &rusqlite::Row<'_>) -> rusqlite::Result<TeamTask> {
    Ok(TeamTask {
        id: row.get(0)?,
        team_id: row.get(1)?,
        payload_json: row.get(2)?,
        status: row.get(3)?,
        priority: row.get(4)?,
        assigned_agent_id: row.get::<_, Option<String>>(5)?.unwrap_or_default(),
        submitted_at: row.get(6)?,
        assigned_at: row.get(7)?,
        started_at: row.get(8)?,
        finished_at: row.get(9)?,
        updated_at: row.get(10)?,
    })
}

fn scan_team_task_event(row: &rusqlite::Row<'_>) -> rusqlite::Result<TeamTaskEvent> {
    Ok(TeamTaskEvent {
        id: row.get(0)?,
        task_id: row.get(1)?,
        team_id: row.get(2)?,
        event_type: row.get(3)?,
        from_status: row.get(4)?,
        to_status: row.get(5)?,
        actor_agent_id: row.get(6)?,
        detail: row.get(7)?,
        created_at: row.get(8)?,
    })
}
