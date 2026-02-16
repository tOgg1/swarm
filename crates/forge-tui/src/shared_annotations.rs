//! Shared annotations for runs and log lines.

#[derive(Debug, Clone, PartialEq, Eq, Hash)]
pub enum AnnotationTarget {
    Run { run_id: String },
    LogLine { loop_id: String, line_index: usize },
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct SharedAnnotation {
    pub id: String,
    pub target: AnnotationTarget,
    pub author: String,
    pub body: String,
    pub tags: Vec<String>,
    pub created_at_epoch_s: i64,
    pub updated_at_epoch_s: i64,
}

#[derive(Debug, Clone, PartialEq, Eq, Default)]
pub struct SharedAnnotationStore {
    entries: Vec<SharedAnnotation>,
}

impl SharedAnnotationStore {
    #[must_use]
    pub fn entries(&self) -> &[SharedAnnotation] {
        &self.entries
    }

    pub fn add_annotation(
        &mut self,
        target: AnnotationTarget,
        author: &str,
        body: &str,
        tags: &[String],
        now_epoch_s: i64,
    ) -> Result<String, String> {
        let author = normalize_required(author);
        if author.is_empty() {
            return Err("annotation author is required".to_owned());
        }
        let body = body.trim();
        if body.is_empty() {
            return Err("annotation body is required".to_owned());
        }
        let id = format!(
            "ann-{}-{}",
            now_epoch_s.max(0),
            self.entries.len().saturating_add(1)
        );
        self.entries.push(SharedAnnotation {
            id: id.clone(),
            target,
            author: author.to_owned(),
            body: body.to_owned(),
            tags: normalize_tags(tags),
            created_at_epoch_s: now_epoch_s.max(0),
            updated_at_epoch_s: now_epoch_s.max(0),
        });
        Ok(id)
    }

    pub fn update_annotation(
        &mut self,
        id: &str,
        body: &str,
        tags: &[String],
        now_epoch_s: i64,
    ) -> Result<(), String> {
        let Some(entry) = self.entries.iter_mut().find(|entry| entry.id == id.trim()) else {
            return Err(format!("annotation not found: {id}"));
        };
        let body = body.trim();
        if body.is_empty() {
            return Err("annotation body is required".to_owned());
        }
        entry.body = body.to_owned();
        entry.tags = normalize_tags(tags);
        entry.updated_at_epoch_s = now_epoch_s.max(entry.created_at_epoch_s);
        Ok(())
    }

    pub fn remove_annotation(&mut self, id: &str) -> bool {
        let before = self.entries.len();
        self.entries.retain(|entry| entry.id != id.trim());
        before != self.entries.len()
    }

    #[must_use]
    pub fn list_for_target(&self, target: &AnnotationTarget) -> Vec<SharedAnnotation> {
        let mut list = self
            .entries
            .iter()
            .filter(|entry| &entry.target == target)
            .cloned()
            .collect::<Vec<_>>();
        list.sort_by(|a, b| {
            b.updated_at_epoch_s
                .cmp(&a.updated_at_epoch_s)
                .then_with(|| a.id.cmp(&b.id))
        });
        list
    }

    #[must_use]
    pub fn search_text(&self, query: &str) -> Vec<SharedAnnotation> {
        let query = normalize_required(query);
        if query.is_empty() {
            return Vec::new();
        }
        let mut list = self
            .entries
            .iter()
            .filter(|entry| {
                entry.body.to_ascii_lowercase().contains(&query)
                    || entry.author.to_ascii_lowercase().contains(&query)
                    || entry.tags.iter().any(|tag| tag.contains(&query))
            })
            .cloned()
            .collect::<Vec<_>>();
        list.sort_by(|a, b| {
            b.updated_at_epoch_s
                .cmp(&a.updated_at_epoch_s)
                .then_with(|| a.id.cmp(&b.id))
        });
        list
    }
}

fn normalize_required(value: &str) -> String {
    value.trim().to_ascii_lowercase()
}

fn normalize_tags(tags: &[String]) -> Vec<String> {
    let mut out = tags
        .iter()
        .map(|tag| normalize_required(tag))
        .filter(|tag| !tag.is_empty())
        .collect::<Vec<_>>();
    out.sort();
    out.dedup();
    out
}

#[cfg(test)]
mod tests {
    use super::{AnnotationTarget, SharedAnnotationStore};

    #[test]
    fn add_and_list_annotations_for_target() {
        let mut store = SharedAnnotationStore::default();
        let target = AnnotationTarget::Run {
            run_id: "run-1".to_owned(),
        };
        let id = match store.add_annotation(
            target.clone(),
            "agent-a",
            "Investigate timeout in step deploy",
            &["incident".to_owned()],
            100,
        ) {
            Ok(id) => id,
            Err(err) => panic!("add should succeed: {err}"),
        };
        assert!(id.starts_with("ann-"));

        let list = store.list_for_target(&target);
        assert_eq!(list.len(), 1);
        assert_eq!(list[0].body, "Investigate timeout in step deploy");
    }

    #[test]
    fn update_annotation_refreshes_body_tags_and_timestamp() {
        let mut store = SharedAnnotationStore::default();
        let target = AnnotationTarget::LogLine {
            loop_id: "loop-2".to_owned(),
            line_index: 44,
        };
        let id = match store.add_annotation(target, "agent-a", "old body", &[], 100) {
            Ok(id) => id,
            Err(err) => panic!("add should succeed: {err}"),
        };
        if let Err(err) = store.update_annotation(&id, "new body", &["follow-up".to_owned()], 180) {
            panic!("update should succeed: {err}");
        }

        let entry = match store.entries().iter().find(|entry| entry.id == id) {
            Some(entry) => entry,
            None => panic!("entry should exist"),
        };
        assert_eq!(entry.body, "new body");
        assert_eq!(entry.tags, vec!["follow-up".to_owned()]);
        assert_eq!(entry.updated_at_epoch_s, 180);
    }

    #[test]
    fn search_text_matches_body_author_and_tags() {
        let mut store = SharedAnnotationStore::default();
        if let Err(err) = store.add_annotation(
            AnnotationTarget::Run {
                run_id: "run-9".to_owned(),
            },
            "agent-river",
            "deadlock reproduced in replay",
            &["critical".to_owned()],
            50,
        ) {
            panic!("add should succeed: {err}");
        }
        if let Err(err) = store.add_annotation(
            AnnotationTarget::Run {
                run_id: "run-10".to_owned(),
            },
            "agent-sky",
            "safe cleanup complete",
            &["note".to_owned()],
            70,
        ) {
            panic!("add should succeed: {err}");
        }

        let deadlock = store.search_text("deadlock");
        assert_eq!(deadlock.len(), 1);
        assert_eq!(deadlock[0].author, "agent-river");

        let critical = store.search_text("critical");
        assert_eq!(critical.len(), 1);
        assert_eq!(critical[0].body, "deadlock reproduced in replay");
    }

    #[test]
    fn remove_annotation_deletes_entry() {
        let mut store = SharedAnnotationStore::default();
        let id = match store.add_annotation(
            AnnotationTarget::Run {
                run_id: "run-3".to_owned(),
            },
            "agent-a",
            "temporary note",
            &[],
            100,
        ) {
            Ok(id) => id,
            Err(err) => panic!("add should succeed: {err}"),
        };
        assert!(store.remove_annotation(&id));
        assert!(!store.remove_annotation(&id));
        assert!(store.entries().is_empty());
    }
}
