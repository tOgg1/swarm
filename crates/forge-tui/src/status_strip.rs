//! Configurable top/bottom status strips with pluggable widget slots.

use std::collections::{BTreeMap, BTreeSet};

use serde_json::{Map, Value};

pub const STATUS_STRIP_SCHEMA_VERSION: u32 = 2;

#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord)]
pub enum StripPosition {
    Top,
    Bottom,
}

impl StripPosition {
    #[must_use]
    pub fn slug(self) -> &'static str {
        match self {
            Self::Top => "top",
            Self::Bottom => "bottom",
        }
    }

    #[must_use]
    pub fn from_slug(value: &str) -> Option<Self> {
        match value.trim().to_ascii_lowercase().as_str() {
            "top" => Some(Self::Top),
            "bottom" => Some(Self::Bottom),
            _ => None,
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct StatusWidgetDefinition {
    pub id: String,
    pub title: String,
    pub default_position: StripPosition,
    pub default_order: u16,
    pub default_enabled: bool,
}

impl StatusWidgetDefinition {
    #[must_use]
    pub fn new(id: &str, title: &str, default_position: StripPosition, default_order: u16) -> Self {
        Self {
            id: normalize_widget_id(id),
            title: title.trim().to_owned(),
            default_position,
            default_order,
            default_enabled: true,
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct StatusWidgetRegistry {
    widgets: BTreeMap<String, StatusWidgetDefinition>,
}

impl Default for StatusWidgetRegistry {
    fn default() -> Self {
        Self::with_builtins()
    }
}

impl StatusWidgetRegistry {
    #[must_use]
    pub fn new() -> Self {
        Self {
            widgets: BTreeMap::new(),
        }
    }

    #[must_use]
    pub fn with_builtins() -> Self {
        let mut registry = Self::new();
        for definition in builtin_status_widgets() {
            let _ = registry.register(definition);
        }
        registry
    }

    pub fn register(&mut self, mut definition: StatusWidgetDefinition) -> Result<(), String> {
        definition.id = normalize_widget_id(&definition.id);
        definition.title = definition.title.trim().to_owned();
        if definition.id.is_empty() {
            return Err("status widget id cannot be empty".to_owned());
        }
        if definition.title.is_empty() {
            return Err(format!(
                "status widget '{}' title cannot be empty",
                definition.id
            ));
        }
        if self.widgets.contains_key(&definition.id) {
            return Err(format!(
                "status widget '{}' already registered",
                definition.id
            ));
        }
        self.widgets.insert(definition.id.clone(), definition);
        Ok(())
    }

    #[must_use]
    pub fn contains(&self, widget_id: &str) -> bool {
        self.widgets.contains_key(&normalize_widget_id(widget_id))
    }

    #[must_use]
    pub fn definition(&self, widget_id: &str) -> Option<&StatusWidgetDefinition> {
        self.widgets.get(&normalize_widget_id(widget_id))
    }

    #[must_use]
    pub fn definitions(&self) -> Vec<StatusWidgetDefinition> {
        let mut definitions = self.widgets.values().cloned().collect::<Vec<_>>();
        definitions.sort_by(|a, b| {
            (a.default_position, a.default_order, a.id.as_str()).cmp(&(
                b.default_position,
                b.default_order,
                b.id.as_str(),
            ))
        });
        definitions
    }
}

#[must_use]
pub fn builtin_status_widgets() -> Vec<StatusWidgetDefinition> {
    vec![
        StatusWidgetDefinition::new("workspace", "Workspace", StripPosition::Top, 10),
        StatusWidgetDefinition::new("view", "View", StripPosition::Top, 20),
        StatusWidgetDefinition::new("filters", "Filters", StripPosition::Top, 30),
        StatusWidgetDefinition::new("selection", "Selection", StripPosition::Bottom, 10),
        StatusWidgetDefinition::new("queue_depth", "Queue", StripPosition::Bottom, 20),
        StatusWidgetDefinition::new("alerts", "Alerts", StripPosition::Bottom, 30),
        StatusWidgetDefinition::new("clock", "Clock", StripPosition::Bottom, 40),
    ]
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct StatusStripPlacement {
    pub widget_id: String,
    pub position: StripPosition,
    pub order: u16,
    pub enabled: bool,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct StatusStripStore {
    pub schema_version: u32,
    pub placements: Vec<StatusStripPlacement>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct StatusStripLoadOutcome {
    pub store: StatusStripStore,
    pub migrated: bool,
    pub warnings: Vec<String>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct StatusStripSlot {
    pub slot: usize,
    pub widget_id: String,
    pub title: String,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct StatusStripPlan {
    pub top_slots: Vec<StatusStripSlot>,
    pub bottom_slots: Vec<StatusStripSlot>,
}

#[must_use]
pub fn default_status_strip_store(registry: &StatusWidgetRegistry) -> StatusStripStore {
    let mut placements = registry
        .definitions()
        .into_iter()
        .map(|definition| StatusStripPlacement {
            widget_id: definition.id,
            position: definition.default_position,
            order: definition.default_order,
            enabled: definition.default_enabled,
        })
        .collect::<Vec<_>>();
    normalize_orders(&mut placements);
    StatusStripStore {
        schema_version: STATUS_STRIP_SCHEMA_VERSION,
        placements,
    }
}

#[must_use]
pub fn restore_status_strip_store(
    raw: &str,
    registry: &StatusWidgetRegistry,
) -> StatusStripLoadOutcome {
    let trimmed = raw.trim();
    if trimmed.is_empty() {
        return StatusStripLoadOutcome {
            store: default_status_strip_store(registry),
            migrated: false,
            warnings: Vec::new(),
        };
    }

    let parsed = serde_json::from_str::<Value>(trimmed);
    let value = match parsed {
        Ok(value) => value,
        Err(err) => {
            return StatusStripLoadOutcome {
                store: default_status_strip_store(registry),
                migrated: false,
                warnings: vec![format!(
                    "invalid json; status strip defaults restored ({err})"
                )],
            };
        }
    };

    let mut warnings = Vec::new();
    let schema_version = value
        .as_object()
        .and_then(|obj| obj.get("schema_version"))
        .and_then(Value::as_u64)
        .unwrap_or(1) as u32;

    let (parsed_store, migrated) = if schema_version <= 1 {
        (
            parse_v1_store(&value, registry, &mut warnings),
            schema_version != STATUS_STRIP_SCHEMA_VERSION,
        )
    } else if schema_version == STATUS_STRIP_SCHEMA_VERSION {
        (parse_v2_store(&value, registry, &mut warnings), false)
    } else {
        warnings.push(format!(
            "unknown schema_version={schema_version}; parsed as v{}",
            STATUS_STRIP_SCHEMA_VERSION
        ));
        (
            parse_v2_store(&value, registry, &mut warnings),
            schema_version != STATUS_STRIP_SCHEMA_VERSION,
        )
    };

    StatusStripLoadOutcome {
        store: sanitize_store(parsed_store, registry, &mut warnings),
        migrated,
        warnings,
    }
}

#[must_use]
pub fn persist_status_strip_store(
    store: &StatusStripStore,
    registry: &StatusWidgetRegistry,
) -> String {
    let normalized = sanitize_store(store.clone(), registry, &mut Vec::new());

    let mut root = Map::new();
    root.insert(
        "schema_version".to_owned(),
        Value::from(STATUS_STRIP_SCHEMA_VERSION),
    );
    root.insert(
        "placements".to_owned(),
        Value::Array(
            normalized
                .placements
                .iter()
                .map(|placement| {
                    let mut item = Map::new();
                    item.insert(
                        "widget_id".to_owned(),
                        Value::from(placement.widget_id.clone()),
                    );
                    item.insert(
                        "position".to_owned(),
                        Value::from(placement.position.slug()),
                    );
                    item.insert("order".to_owned(), Value::from(placement.order));
                    item.insert("enabled".to_owned(), Value::from(placement.enabled));
                    Value::Object(item)
                })
                .collect(),
        ),
    );

    match serde_json::to_string_pretty(&Value::Object(root)) {
        Ok(json) => json,
        Err(_) => "{}".to_owned(),
    }
}

pub fn move_widget_slot(
    store: &mut StatusStripStore,
    widget_id: &str,
    new_position: StripPosition,
    new_index: usize,
    registry: &StatusWidgetRegistry,
) -> Result<(), String> {
    let mut warnings = Vec::new();
    let normalized = sanitize_store(store.clone(), registry, &mut warnings);
    let target_id = normalize_widget_id(widget_id);

    let mut top_ids = Vec::new();
    let mut bottom_ids = Vec::new();
    let mut enabled = BTreeMap::new();

    for placement in &normalized.placements {
        enabled.insert(placement.widget_id.clone(), placement.enabled);
        match placement.position {
            StripPosition::Top => top_ids.push(placement.widget_id.clone()),
            StripPosition::Bottom => bottom_ids.push(placement.widget_id.clone()),
        }
    }

    let removed =
        remove_widget(&mut top_ids, &target_id) || remove_widget(&mut bottom_ids, &target_id);
    if !removed {
        return Err(format!("status widget '{}' not found", widget_id.trim()));
    }

    let target = match new_position {
        StripPosition::Top => &mut top_ids,
        StripPosition::Bottom => &mut bottom_ids,
    };
    let clamped = new_index.min(target.len());
    target.insert(clamped, target_id.clone());

    store.schema_version = STATUS_STRIP_SCHEMA_VERSION;
    store.placements.clear();
    append_positions(
        &mut store.placements,
        &top_ids,
        StripPosition::Top,
        &enabled,
    );
    append_positions(
        &mut store.placements,
        &bottom_ids,
        StripPosition::Bottom,
        &enabled,
    );
    Ok(())
}

pub fn set_widget_enabled(
    store: &mut StatusStripStore,
    widget_id: &str,
    enabled: bool,
    registry: &StatusWidgetRegistry,
) -> Result<(), String> {
    let target_id = normalize_widget_id(widget_id);
    let mut warnings = Vec::new();
    let mut normalized = sanitize_store(store.clone(), registry, &mut warnings);

    let Some(placement) = normalized
        .placements
        .iter_mut()
        .find(|placement| placement.widget_id == target_id)
    else {
        return Err(format!("status widget '{}' not found", widget_id.trim()));
    };

    placement.enabled = enabled;
    *store = normalized;
    Ok(())
}

#[must_use]
pub fn build_status_strip_plan(
    store: &StatusStripStore,
    registry: &StatusWidgetRegistry,
) -> StatusStripPlan {
    let mut warnings = Vec::new();
    let normalized = sanitize_store(store.clone(), registry, &mut warnings);

    let mut top_slots = Vec::new();
    let mut bottom_slots = Vec::new();

    for placement in normalized.placements {
        if !placement.enabled {
            continue;
        }

        let title = registry
            .definition(&placement.widget_id)
            .map(|definition| definition.title.clone())
            .unwrap_or_else(|| placement.widget_id.clone());

        let slot = match placement.position {
            StripPosition::Top => {
                let slot = top_slots.len();
                top_slots.push(StatusStripSlot {
                    slot,
                    widget_id: placement.widget_id,
                    title,
                });
                continue;
            }
            StripPosition::Bottom => bottom_slots.len(),
        };

        bottom_slots.push(StatusStripSlot {
            slot,
            widget_id: placement.widget_id,
            title,
        });
    }

    StatusStripPlan {
        top_slots,
        bottom_slots,
    }
}

#[must_use]
pub fn render_status_strip_line(
    plan: &StatusStripPlan,
    position: StripPosition,
    widget_values: &BTreeMap<String, String>,
    width: usize,
) -> String {
    if width == 0 {
        return String::new();
    }

    let slots = match position {
        StripPosition::Top => &plan.top_slots,
        StripPosition::Bottom => &plan.bottom_slots,
    };

    let mut segments = Vec::new();
    for slot in slots {
        let label = widget_values
            .get(&slot.widget_id)
            .map(String::as_str)
            .map(str::trim)
            .filter(|value| !value.is_empty())
            .unwrap_or(slot.title.as_str());
        segments.push(format!("[{label}]"));
    }

    let line = segments.join(" ");
    if line.len() > width {
        truncate_with_ellipsis(&line, width)
    } else {
        pad_to_width(line, width)
    }
}

fn parse_v1_store(
    value: &Value,
    registry: &StatusWidgetRegistry,
    warnings: &mut Vec<String>,
) -> StatusStripStore {
    let Some(obj) = value.as_object() else {
        warnings.push("v1 status strip state was not an object; defaults applied".to_owned());
        return default_status_strip_store(registry);
    };

    let mut store = default_status_strip_store(registry);
    if let Some(top) = obj.get("top").and_then(Value::as_array) {
        apply_position_array(&mut store, top, StripPosition::Top, registry, warnings);
    }
    if let Some(bottom) = obj.get("bottom").and_then(Value::as_array) {
        apply_position_array(
            &mut store,
            bottom,
            StripPosition::Bottom,
            registry,
            warnings,
        );
    }
    if let Some(disabled) = obj.get("disabled").and_then(Value::as_array) {
        let disabled_set = disabled
            .iter()
            .filter_map(Value::as_str)
            .map(normalize_widget_id)
            .filter(|value| !value.is_empty())
            .collect::<BTreeSet<_>>();
        for placement in &mut store.placements {
            if disabled_set.contains(&placement.widget_id) {
                placement.enabled = false;
            }
        }
    }

    store
}

fn parse_v2_store(
    value: &Value,
    registry: &StatusWidgetRegistry,
    warnings: &mut Vec<String>,
) -> StatusStripStore {
    let Some(obj) = value.as_object() else {
        warnings.push("status strip state was not an object; defaults applied".to_owned());
        return default_status_strip_store(registry);
    };

    let placements = obj
        .get("placements")
        .and_then(Value::as_array)
        .map(|items| parse_placements(items, warnings))
        .unwrap_or_default();

    StatusStripStore {
        schema_version: STATUS_STRIP_SCHEMA_VERSION,
        placements,
    }
}

fn parse_placements(values: &[Value], warnings: &mut Vec<String>) -> Vec<StatusStripPlacement> {
    let mut placements = Vec::new();

    for value in values {
        let Some(obj) = value.as_object() else {
            warnings.push("ignored malformed placement entry (not object)".to_owned());
            continue;
        };

        let widget_id = obj
            .get("widget_id")
            .and_then(Value::as_str)
            .map(normalize_widget_id)
            .unwrap_or_default();
        if widget_id.is_empty() {
            warnings.push("ignored placement with empty widget_id".to_owned());
            continue;
        }

        let position = obj
            .get("position")
            .and_then(Value::as_str)
            .and_then(StripPosition::from_slug)
            .unwrap_or(StripPosition::Bottom);

        let order = obj.get("order").and_then(Value::as_u64).unwrap_or(0) as u16;
        let enabled = obj.get("enabled").and_then(Value::as_bool).unwrap_or(true);

        placements.push(StatusStripPlacement {
            widget_id,
            position,
            order,
            enabled,
        });
    }

    placements
}

fn apply_position_array(
    store: &mut StatusStripStore,
    values: &[Value],
    position: StripPosition,
    registry: &StatusWidgetRegistry,
    warnings: &mut Vec<String>,
) {
    let ids = values
        .iter()
        .filter_map(Value::as_str)
        .map(normalize_widget_id)
        .filter(|id| !id.is_empty())
        .collect::<Vec<_>>();

    let mut seen = BTreeSet::new();
    for (idx, widget_id) in ids.into_iter().enumerate() {
        if !registry.contains(&widget_id) {
            warnings.push(format!("ignored unknown status widget '{}'(v1)", widget_id));
            continue;
        }
        if !seen.insert(widget_id.clone()) {
            warnings.push(format!(
                "ignored duplicate status widget '{}'(v1)",
                widget_id
            ));
            continue;
        }

        if let Some(placement) = store
            .placements
            .iter_mut()
            .find(|placement| placement.widget_id == widget_id)
        {
            placement.position = position;
            placement.order = idx as u16;
        }
    }
}

fn sanitize_store(
    store: StatusStripStore,
    registry: &StatusWidgetRegistry,
    warnings: &mut Vec<String>,
) -> StatusStripStore {
    let mut by_id = default_status_strip_store(registry)
        .placements
        .into_iter()
        .map(|placement| (placement.widget_id.clone(), placement))
        .collect::<BTreeMap<_, _>>();

    let mut seen = BTreeSet::new();
    for placement in store.placements {
        let widget_id = normalize_widget_id(&placement.widget_id);
        if widget_id.is_empty() {
            warnings.push("ignored placement with empty widget_id".to_owned());
            continue;
        }
        if !registry.contains(&widget_id) {
            warnings.push(format!("ignored unknown status widget '{}'(v2)", widget_id));
            continue;
        }
        if !seen.insert(widget_id.clone()) {
            warnings.push(format!(
                "ignored duplicate status widget '{}'(v2)",
                widget_id
            ));
            continue;
        }

        if let Some(existing) = by_id.get_mut(&widget_id) {
            existing.position = placement.position;
            existing.order = placement.order;
            existing.enabled = placement.enabled;
        }
    }

    let mut placements = by_id.into_values().collect::<Vec<_>>();
    normalize_orders(&mut placements);

    StatusStripStore {
        schema_version: STATUS_STRIP_SCHEMA_VERSION,
        placements,
    }
}

fn normalize_orders(placements: &mut Vec<StatusStripPlacement>) {
    placements.sort_by(|a, b| {
        (a.position, a.order, a.widget_id.as_str()).cmp(&(
            b.position,
            b.order,
            b.widget_id.as_str(),
        ))
    });

    let mut top_order = 0u16;
    let mut bottom_order = 0u16;
    for placement in placements {
        match placement.position {
            StripPosition::Top => {
                placement.order = top_order;
                top_order = top_order.saturating_add(1);
            }
            StripPosition::Bottom => {
                placement.order = bottom_order;
                bottom_order = bottom_order.saturating_add(1);
            }
        }
    }
}

fn remove_widget(ids: &mut Vec<String>, target_id: &str) -> bool {
    if let Some(index) = ids.iter().position(|id| id == target_id) {
        ids.remove(index);
        true
    } else {
        false
    }
}

fn append_positions(
    placements: &mut Vec<StatusStripPlacement>,
    ids: &[String],
    position: StripPosition,
    enabled: &BTreeMap<String, bool>,
) {
    for (index, widget_id) in ids.iter().enumerate() {
        placements.push(StatusStripPlacement {
            widget_id: widget_id.clone(),
            position,
            order: index as u16,
            enabled: enabled.get(widget_id).copied().unwrap_or(true),
        });
    }
}

fn normalize_widget_id(value: &str) -> String {
    value
        .trim()
        .to_ascii_lowercase()
        .replace(' ', "_")
        .chars()
        .filter(|ch| ch.is_ascii_alphanumeric() || *ch == '_' || *ch == '-')
        .collect()
}

fn truncate_with_ellipsis(line: &str, width: usize) -> String {
    if width == 0 {
        return String::new();
    }
    if width <= 3 {
        return line.chars().take(width).collect();
    }
    let take = width - 3;
    let mut out = line.chars().take(take).collect::<String>();
    out.push_str("...");
    out
}

fn pad_to_width(mut line: String, width: usize) -> String {
    if line.len() >= width {
        return line;
    }
    line.push_str(&" ".repeat(width - line.len()));
    line
}

#[cfg(test)]
#[allow(clippy::expect_used)]
mod tests {
    use std::collections::BTreeMap;

    use super::{
        build_status_strip_plan, default_status_strip_store, move_widget_slot,
        persist_status_strip_store, render_status_strip_line, restore_status_strip_store,
        set_widget_enabled, StatusWidgetDefinition, StatusWidgetRegistry, StripPosition,
    };

    fn plan_ids_top(plan: &super::StatusStripPlan) -> Vec<String> {
        plan.top_slots
            .iter()
            .map(|slot| slot.widget_id.clone())
            .collect()
    }

    fn plan_ids_bottom(plan: &super::StatusStripPlan) -> Vec<String> {
        plan.bottom_slots
            .iter()
            .map(|slot| slot.widget_id.clone())
            .collect()
    }

    #[test]
    fn default_store_contains_top_and_bottom_slots() {
        let registry = StatusWidgetRegistry::with_builtins();
        let store = default_status_strip_store(&registry);
        let plan = build_status_strip_plan(&store, &registry);

        assert_eq!(plan_ids_top(&plan), vec!["workspace", "view", "filters"]);
        assert_eq!(
            plan_ids_bottom(&plan),
            vec!["selection", "queue_depth", "alerts", "clock"]
        );
    }

    #[test]
    fn register_pluggable_widget_and_persist_round_trip() {
        let mut registry = StatusWidgetRegistry::with_builtins();
        if let Err(err) = registry.register(StatusWidgetDefinition::new(
            "latency",
            "Latency",
            StripPosition::Bottom,
            15,
        )) {
            panic!("register latency widget should succeed: {err}");
        }

        let store = default_status_strip_store(&registry);
        let json = persist_status_strip_store(&store, &registry);
        let restored = restore_status_strip_store(&json, &registry);
        let plan = build_status_strip_plan(&restored.store, &registry);

        assert!(plan_ids_bottom(&plan).contains(&"latency".to_owned()));
        assert!(!restored.migrated);
    }

    #[test]
    fn register_duplicate_widget_id_is_rejected() {
        let mut registry = StatusWidgetRegistry::new();
        if let Err(err) = registry.register(StatusWidgetDefinition::new(
            "queue_depth",
            "Queue",
            StripPosition::Bottom,
            10,
        )) {
            panic!("first register should succeed: {err}");
        }

        let err = match registry.register(StatusWidgetDefinition::new(
            "queue_depth",
            "Queue v2",
            StripPosition::Bottom,
            20,
        )) {
            Ok(_) => panic!("duplicate register should fail"),
            Err(err) => err,
        };
        assert!(err.contains("already registered"));
    }

    #[test]
    fn restore_v1_migrates_order_and_disabled_widgets() {
        let registry = StatusWidgetRegistry::with_builtins();
        let raw = r#"
        {
            "top": ["view", "workspace"],
            "bottom": ["clock", "queue_depth"],
            "disabled": ["queue_depth"]
        }
        "#;

        let outcome = restore_status_strip_store(raw, &registry);
        let plan = build_status_strip_plan(&outcome.store, &registry);

        assert!(outcome.migrated);
        assert_eq!(plan_ids_top(&plan), vec!["view", "workspace", "filters"]);
        assert_eq!(plan_ids_bottom(&plan), vec!["clock", "selection", "alerts"]);
    }

    #[test]
    fn restore_invalid_json_falls_back_to_defaults_with_warning() {
        let registry = StatusWidgetRegistry::with_builtins();
        let outcome = restore_status_strip_store("{", &registry);

        assert!(!outcome.warnings.is_empty());
        let plan = build_status_strip_plan(&outcome.store, &registry);
        assert_eq!(plan_ids_top(&plan), vec!["workspace", "view", "filters"]);
    }

    #[test]
    fn restore_v2_ignores_unknown_and_duplicate_widgets() {
        let registry = StatusWidgetRegistry::with_builtins();
        let raw = r#"
        {
            "schema_version": 2,
            "placements": [
                {"widget_id": "workspace", "position": "top", "order": 0, "enabled": true},
                {"widget_id": "workspace", "position": "bottom", "order": 0, "enabled": true},
                {"widget_id": "not_real", "position": "top", "order": 1, "enabled": true}
            ]
        }
        "#;

        let outcome = restore_status_strip_store(raw, &registry);
        assert!(outcome
            .warnings
            .iter()
            .any(|warning| warning.contains("duplicate")));
        assert!(outcome
            .warnings
            .iter()
            .any(|warning| warning.contains("unknown")));

        let plan = build_status_strip_plan(&outcome.store, &registry);
        let top = plan_ids_top(&plan);
        assert_eq!(top[0], "workspace");
        assert!(!top.contains(&"not_real".to_owned()));
    }

    #[test]
    fn move_widget_between_strips_reorders_slots() {
        let registry = StatusWidgetRegistry::with_builtins();
        let mut store = default_status_strip_store(&registry);

        if let Err(err) =
            move_widget_slot(&mut store, "queue_depth", StripPosition::Top, 1, &registry)
        {
            panic!("move queue widget to top should succeed: {err}");
        }

        let plan = build_status_strip_plan(&store, &registry);
        assert_eq!(
            plan_ids_top(&plan),
            vec!["workspace", "queue_depth", "view", "filters"]
        );
        assert_eq!(plan_ids_bottom(&plan), vec!["selection", "alerts", "clock"]);
    }

    #[test]
    fn set_widget_enabled_toggles_visibility() {
        let registry = StatusWidgetRegistry::with_builtins();
        let mut store = default_status_strip_store(&registry);

        if let Err(err) = set_widget_enabled(&mut store, "alerts", false, &registry) {
            panic!("disable alerts should succeed: {err}");
        }
        let plan = build_status_strip_plan(&store, &registry);
        assert!(!plan_ids_bottom(&plan).contains(&"alerts".to_owned()));

        let err = match set_widget_enabled(&mut store, "missing", false, &registry) {
            Ok(()) => panic!("missing widget should fail"),
            Err(err) => err,
        };
        assert!(err.contains("not found"));
    }

    #[test]
    fn render_status_strip_line_prefers_runtime_values_and_truncates() {
        let registry = StatusWidgetRegistry::with_builtins();
        let store = default_status_strip_store(&registry);
        let plan = build_status_strip_plan(&store, &registry);

        let mut values = BTreeMap::new();
        values.insert("workspace".to_owned(), "repo=forge".to_owned());
        values.insert("view".to_owned(), "logs".to_owned());
        values.insert("filters".to_owned(), "state=running".to_owned());

        let full = render_status_strip_line(&plan, StripPosition::Top, &values, 80);
        assert_eq!(full.len(), 80);
        assert!(full.starts_with("[repo=forge] [logs] [state=running]"));

        let truncated = render_status_strip_line(&plan, StripPosition::Top, &values, 20);
        assert_eq!(truncated.len(), 20);
        assert!(truncated.ends_with("..."));
        assert!(truncated.starts_with("[repo=forge] [log"));
    }
}
