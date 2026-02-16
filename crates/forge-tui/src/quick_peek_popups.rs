//! Quick peek popups: inline entity summaries without leaving the active view.

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum PeekEntityKind {
    Loop,
    Task,
    FmailThread,
    FilePath,
    Commit,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct PeekEntityRef {
    pub kind: PeekEntityKind,
    pub value: String,
}

impl PeekEntityRef {
    #[must_use]
    pub fn loop_id(value: &str) -> Self {
        Self {
            kind: PeekEntityKind::Loop,
            value: normalize_key(PeekEntityKind::Loop, value),
        }
    }

    #[must_use]
    pub fn task_id(value: &str) -> Self {
        Self {
            kind: PeekEntityKind::Task,
            value: normalize_key(PeekEntityKind::Task, value),
        }
    }

    #[must_use]
    pub fn fmail_thread(value: &str) -> Self {
        Self {
            kind: PeekEntityKind::FmailThread,
            value: normalize_key(PeekEntityKind::FmailThread, value),
        }
    }

    #[must_use]
    pub fn file_path(value: &str) -> Self {
        Self {
            kind: PeekEntityKind::FilePath,
            value: normalize_key(PeekEntityKind::FilePath, value),
        }
    }

    #[must_use]
    pub fn commit(value: &str) -> Self {
        Self {
            kind: PeekEntityKind::Commit,
            value: normalize_key(PeekEntityKind::Commit, value),
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct LoopPeekRecord {
    pub loop_id: String,
    pub health: String,
    pub current_task: Option<String>,
    pub recent_output: Vec<String>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct TaskPeekRecord {
    pub task_id: String,
    pub status: String,
    pub assignee: Option<String>,
    pub description: String,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct FmailPeekRecord {
    pub thread_id: String,
    pub latest_from: String,
    pub latest_message: String,
    pub latest_timestamp: Option<String>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct FilePeekRecord {
    pub path: String,
    pub contents: String,
    pub recent_changes: Vec<String>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct CommitPeekRecord {
    pub commit_hash: String,
    pub summary: String,
    pub author: String,
    pub files_changed: Vec<String>,
}

#[derive(Debug, Clone, PartialEq, Eq, Default)]
pub struct QuickPeekCatalog {
    pub loops: Vec<LoopPeekRecord>,
    pub tasks: Vec<TaskPeekRecord>,
    pub fmail_threads: Vec<FmailPeekRecord>,
    pub files: Vec<FilePeekRecord>,
    pub commits: Vec<CommitPeekRecord>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct QuickPeekPopup {
    pub target: PeekEntityRef,
    pub title: String,
    pub lines: Vec<String>,
    pub dismiss_on_any_key: bool,
}

#[derive(Debug, Clone, PartialEq, Eq, Default)]
pub struct QuickPeekState {
    pub popup: Option<QuickPeekPopup>,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum QuickPeekEvent {
    Opened,
    Dismissed,
    NotFound,
    Noop,
}

#[must_use]
pub fn build_quick_peek_popup(
    target: &PeekEntityRef,
    catalog: &QuickPeekCatalog,
) -> Option<QuickPeekPopup> {
    let target = normalize_entity_ref(target);
    match target.kind {
        PeekEntityKind::Loop => build_loop_popup(&target, catalog),
        PeekEntityKind::Task => build_task_popup(&target, catalog),
        PeekEntityKind::FmailThread => build_fmail_popup(&target, catalog),
        PeekEntityKind::FilePath => build_file_popup(&target, catalog),
        PeekEntityKind::Commit => build_commit_popup(&target, catalog),
    }
}

pub fn handle_quick_peek_key(
    state: &mut QuickPeekState,
    key: char,
    focused_target: Option<&PeekEntityRef>,
    catalog: &QuickPeekCatalog,
) -> QuickPeekEvent {
    if state.popup.is_some() {
        state.popup = None;
        return QuickPeekEvent::Dismissed;
    }
    if key != ' ' {
        return QuickPeekEvent::Noop;
    }
    let Some(target) = focused_target else {
        return QuickPeekEvent::NotFound;
    };
    let Some(popup) = build_quick_peek_popup(target, catalog) else {
        return QuickPeekEvent::NotFound;
    };
    state.popup = Some(popup);
    QuickPeekEvent::Opened
}

fn build_loop_popup(target: &PeekEntityRef, catalog: &QuickPeekCatalog) -> Option<QuickPeekPopup> {
    let loop_record = catalog
        .loops
        .iter()
        .find(|item| normalize_key(PeekEntityKind::Loop, &item.loop_id) == target.value)?;

    let mut lines = vec![
        format!("health: {}", sanitize_inline(&loop_record.health)),
        format!(
            "current task: {}",
            loop_record
                .current_task
                .as_deref()
                .map(sanitize_inline)
                .unwrap_or_else(|| "-".to_owned())
        ),
    ];
    if loop_record.recent_output.is_empty() {
        lines.push("recent output: none".to_owned());
    } else {
        lines.push("recent output:".to_owned());
        for output in loop_record.recent_output.iter().take(3) {
            lines.push(format!("  {}", sanitize_inline(output)));
        }
    }

    Some(QuickPeekPopup {
        target: target.clone(),
        title: format!("Loop {}", loop_record.loop_id),
        lines,
        dismiss_on_any_key: true,
    })
}

fn build_task_popup(target: &PeekEntityRef, catalog: &QuickPeekCatalog) -> Option<QuickPeekPopup> {
    let task = catalog
        .tasks
        .iter()
        .find(|item| normalize_key(PeekEntityKind::Task, &item.task_id) == target.value)?;
    Some(QuickPeekPopup {
        target: target.clone(),
        title: format!("Task {}", task.task_id),
        lines: vec![
            format!("status: {}", sanitize_inline(&task.status)),
            format!(
                "assignee: {}",
                task.assignee
                    .as_deref()
                    .map(sanitize_inline)
                    .unwrap_or_else(|| "unassigned".to_owned())
            ),
            format!("description: {}", sanitize_inline(&task.description)),
        ],
        dismiss_on_any_key: true,
    })
}

fn build_fmail_popup(target: &PeekEntityRef, catalog: &QuickPeekCatalog) -> Option<QuickPeekPopup> {
    let thread = catalog
        .fmail_threads
        .iter()
        .find(|item| normalize_key(PeekEntityKind::FmailThread, &item.thread_id) == target.value)?;
    let mut lines = vec![
        format!("from: {}", sanitize_inline(&thread.latest_from)),
        format!("message: {}", sanitize_inline(&thread.latest_message)),
    ];
    if let Some(timestamp) = &thread.latest_timestamp {
        lines.push(format!("at: {}", sanitize_inline(timestamp)));
    }

    Some(QuickPeekPopup {
        target: target.clone(),
        title: format!("Thread {}", thread.thread_id),
        lines,
        dismiss_on_any_key: true,
    })
}

fn build_file_popup(target: &PeekEntityRef, catalog: &QuickPeekCatalog) -> Option<QuickPeekPopup> {
    let file = catalog
        .files
        .iter()
        .find(|item| normalize_key(PeekEntityKind::FilePath, &item.path) == target.value)?;
    let (preview_lines, hidden_line_count) = head_lines(&file.contents, 20);
    let mut lines = vec!["head (20 lines max):".to_owned()];
    if preview_lines.is_empty() {
        lines.push("  <empty file>".to_owned());
    } else {
        for line in preview_lines {
            lines.push(format!("  {line}"));
        }
        if hidden_line_count > 0 {
            lines.push(format!("  ... +{hidden_line_count} more lines"));
        }
    }
    if file.recent_changes.is_empty() {
        lines.push("recent changes: none".to_owned());
    } else {
        lines.push("recent changes:".to_owned());
        for change in file.recent_changes.iter().take(3) {
            lines.push(format!("  {}", sanitize_inline(change)));
        }
    }

    Some(QuickPeekPopup {
        target: target.clone(),
        title: format!("File {}", file.path),
        lines,
        dismiss_on_any_key: true,
    })
}

fn build_commit_popup(
    target: &PeekEntityRef,
    catalog: &QuickPeekCatalog,
) -> Option<QuickPeekPopup> {
    let commit = catalog
        .commits
        .iter()
        .find(|item| normalize_key(PeekEntityKind::Commit, &item.commit_hash) == target.value)?;
    let mut lines = vec![
        format!("summary: {}", sanitize_inline(&commit.summary)),
        format!("author: {}", sanitize_inline(&commit.author)),
    ];
    if commit.files_changed.is_empty() {
        lines.push("files: none".to_owned());
    } else {
        lines.push("files:".to_owned());
        for file in commit.files_changed.iter().take(4) {
            lines.push(format!("  {}", sanitize_inline(file)));
        }
    }

    Some(QuickPeekPopup {
        target: target.clone(),
        title: format!("Commit {}", short_hash(&commit.commit_hash)),
        lines,
        dismiss_on_any_key: true,
    })
}

fn normalize_entity_ref(target: &PeekEntityRef) -> PeekEntityRef {
    PeekEntityRef {
        kind: target.kind,
        value: normalize_key(target.kind, &target.value),
    }
}

fn normalize_key(kind: PeekEntityKind, raw: &str) -> String {
    let trimmed = raw.trim();
    match kind {
        PeekEntityKind::FilePath => trimmed.to_owned(),
        _ => trimmed.to_ascii_lowercase(),
    }
}

fn sanitize_inline(raw: &str) -> String {
    raw.split_whitespace().collect::<Vec<_>>().join(" ")
}

fn head_lines(contents: &str, max_lines: usize) -> (Vec<String>, usize) {
    let all = contents.lines().map(sanitize_inline).collect::<Vec<_>>();
    let preview = all.iter().take(max_lines).cloned().collect::<Vec<_>>();
    let hidden = all.len().saturating_sub(preview.len());
    (preview, hidden)
}

fn short_hash(hash: &str) -> String {
    let normalized = normalize_key(PeekEntityKind::Commit, hash);
    normalized.chars().take(8).collect()
}

#[cfg(test)]
mod tests {
    use super::{
        build_quick_peek_popup, handle_quick_peek_key, CommitPeekRecord, FilePeekRecord,
        FmailPeekRecord, LoopPeekRecord, PeekEntityRef, QuickPeekCatalog, QuickPeekEvent,
        QuickPeekState, TaskPeekRecord,
    };

    fn sample_catalog() -> QuickPeekCatalog {
        QuickPeekCatalog {
            loops: vec![LoopPeekRecord {
                loop_id: "loop-a".to_owned(),
                health: "healthy".to_owned(),
                current_task: Some("forge-6ad".to_owned()),
                recent_output: vec![
                    "render pass ok".to_owned(),
                    "task sync done".to_owned(),
                    "idle".to_owned(),
                ],
            }],
            tasks: vec![TaskPeekRecord {
                task_id: "forge-6ad".to_owned(),
                status: "in_progress".to_owned(),
                assignee: Some("@canny-glenn".to_owned()),
                description: "Quick Peek Popups - preview any entity".to_owned(),
            }],
            fmail_threads: vec![FmailPeekRecord {
                thread_id: "20260213-203154-0000".to_owned(),
                latest_from: "@tormod".to_owned(),
                latest_message: "proceed".to_owned(),
                latest_timestamp: Some("2026-02-13T20:31:54Z".to_owned()),
            }],
            files: vec![FilePeekRecord {
                path: "src/main.rs".to_owned(),
                contents: (1..=25)
                    .map(|index| format!("line-{index}"))
                    .collect::<Vec<_>>()
                    .join("\n"),
                recent_changes: vec!["added parser".to_owned(), "tightened error path".to_owned()],
            }],
            commits: vec![CommitPeekRecord {
                commit_hash: "ABCDEF1234567890".to_owned(),
                summary: "add quick peek popup model".to_owned(),
                author: "canny-glenn".to_owned(),
                files_changed: vec![
                    "crates/forge-tui/src/quick_peek_popups.rs".to_owned(),
                    "crates/forge-tui/src/lib.rs".to_owned(),
                ],
            }],
        }
    }

    #[test]
    fn loop_peek_contains_health_task_and_output() {
        let popup =
            match build_quick_peek_popup(&PeekEntityRef::loop_id("LOOP-A"), &sample_catalog()) {
                Some(popup) => popup,
                None => panic!("loop popup should exist"),
            };
        assert_eq!(popup.title, "Loop loop-a");
        assert!(popup
            .lines
            .iter()
            .any(|line| line.contains("health: healthy")));
        assert!(popup
            .lines
            .iter()
            .any(|line| line.contains("current task: forge-6ad")));
        assert!(popup
            .lines
            .iter()
            .any(|line| line.contains("render pass ok")));
    }

    #[test]
    fn task_peek_contains_status_assignee_description() {
        let popup =
            match build_quick_peek_popup(&PeekEntityRef::task_id("forge-6ad"), &sample_catalog()) {
                Some(popup) => popup,
                None => panic!("task popup should exist"),
            };
        assert_eq!(popup.title, "Task forge-6ad");
        assert!(popup
            .lines
            .iter()
            .any(|line| line.contains("status: in_progress")));
        assert!(popup
            .lines
            .iter()
            .any(|line| line.contains("assignee: @canny-glenn")));
        assert!(popup
            .lines
            .iter()
            .any(|line| line.contains("description: Quick Peek Popups")));
    }

    #[test]
    fn file_peek_shows_first_twenty_lines_and_recent_changes() {
        let popup = match build_quick_peek_popup(
            &PeekEntityRef::file_path("src/main.rs"),
            &sample_catalog(),
        ) {
            Some(popup) => popup,
            None => panic!("file popup should exist"),
        };
        assert_eq!(popup.title, "File src/main.rs");
        assert!(popup.lines.iter().any(|line| line.contains("line-20")));
        assert!(!popup.lines.iter().any(|line| line.contains("line-21")));
        assert!(popup
            .lines
            .iter()
            .any(|line| line.contains("... +5 more lines")));
        assert!(popup.lines.iter().any(|line| line.contains("added parser")));
    }

    #[test]
    fn fmail_peek_shows_latest_message() {
        let popup = match build_quick_peek_popup(
            &PeekEntityRef::fmail_thread("20260213-203154-0000"),
            &sample_catalog(),
        ) {
            Some(popup) => popup,
            None => panic!("fmail popup should exist"),
        };
        assert_eq!(popup.title, "Thread 20260213-203154-0000");
        assert!(popup
            .lines
            .iter()
            .any(|line| line.contains("from: @tormod")));
        assert!(popup
            .lines
            .iter()
            .any(|line| line.contains("message: proceed")));
    }

    #[test]
    fn commit_peek_uses_short_hash() {
        let popup = match build_quick_peek_popup(
            &PeekEntityRef::commit("abcdef1234567890"),
            &sample_catalog(),
        ) {
            Some(popup) => popup,
            None => panic!("commit popup should exist"),
        };
        assert_eq!(popup.title, "Commit abcdef12");
        assert!(popup
            .lines
            .iter()
            .any(|line| line.contains("summary: add quick peek popup model")));
    }

    #[test]
    fn space_opens_popup_any_key_dismisses() {
        let catalog = sample_catalog();
        let focus = PeekEntityRef::task_id("forge-6ad");
        let mut state = QuickPeekState::default();
        let event = handle_quick_peek_key(&mut state, ' ', Some(&focus), &catalog);
        assert_eq!(event, QuickPeekEvent::Opened);
        assert!(state.popup.is_some());

        let dismiss = handle_quick_peek_key(&mut state, 'x', Some(&focus), &catalog);
        assert_eq!(dismiss, QuickPeekEvent::Dismissed);
        assert!(state.popup.is_none());
    }

    #[test]
    fn missing_target_returns_not_found() {
        let mut state = QuickPeekState::default();
        let catalog = QuickPeekCatalog::default();
        let event = handle_quick_peek_key(
            &mut state,
            ' ',
            Some(&PeekEntityRef::loop_id("missing")),
            &catalog,
        );
        assert_eq!(event, QuickPeekEvent::NotFound);
    }
}
