//! Overview tab parity helpers.
//!
//! Mirrors the Go `internal/looptui` overview pane content: loop metadata
//! lines + a small run-status snapshot.

use forge_ftui_adapter::render::{Rect, RenderFrame, TermColor, TextRole};
use forge_ftui_adapter::widgets::BorderStyle;

use crate::activity_heatmap::{build_loop_activity_trends, LoopTrendBucket, LoopTrendInput};
use crate::app::{LoopView, RunView};
use crate::health_heatmap_timeline::{
    build_cross_loop_health_timeline, render_cross_loop_heatmap_lines, LoopHealthBucket,
    LoopHealthTimelineInput,
};
use crate::hero_widgets::{build_fleet_snapshot, FleetSnapshot};
use crate::smart_loop_clustering::{cluster_loops_by_domain, compact_domain_summary};
use crate::theme::ResolvedPalette;

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct OverviewLine {
    pub text: String,
    pub role: TextRole,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Default)]
pub struct OverviewPaneOptions {
    pub reserve_next_action_slot: bool,
}

#[derive(Debug, Clone)]
struct DetailField {
    text: String,
    color: TermColor,
    bold: bool,
}

fn push_unique_action(actions: &mut Vec<String>, text: &str) {
    if actions.iter().any(|existing| existing == text) {
        return;
    }
    actions.push(text.to_owned());
}

fn build_next_action_lines(
    loop_view: &LoopView,
    run_history: &[RunView],
    selected_run: usize,
) -> Vec<String> {
    let mut actions = Vec::new();
    let state = loop_view.state.trim().to_ascii_lowercase();

    if state == "error" || !loop_view.last_error.trim().is_empty() {
        push_unique_action(&mut actions, "[2] Logs: inspect error lines and root cause");
    }
    if loop_view.queue_depth > 0 {
        push_unique_action(
            &mut actions,
            "[3] Runs: review queue backlog and latest status",
        );
    }
    if !run_history.is_empty() {
        let idx = selected_run.min(run_history.len().saturating_sub(1));
        let status = run_history[idx].status.trim().to_ascii_lowercase();
        if status != "success" {
            push_unique_action(&mut actions, "[3] Runs: inspect latest run output");
        }
    }
    if matches!(state.as_str(), "waiting" | "stopped") {
        push_unique_action(&mut actions, "[r] Resume: wake selected loop");
    }
    if actions.is_empty() {
        push_unique_action(&mut actions, "[2] Logs: verify healthy output stream");
        push_unique_action(&mut actions, "[4] Multi Logs: compare peer loops");
    }

    actions
}

fn truncate_line(text: &str, width: usize) -> String {
    if width == 0 {
        return String::new();
    }
    let count = text.chars().count();
    if count <= width {
        return text.to_owned();
    }
    if width <= 3 {
        return text.chars().take(width).collect();
    }
    let mut out: String = text.chars().take(width - 3).collect();
    out.push_str("...");
    out
}

fn display_name(name: &str, fallback: &str) -> String {
    let name = name.trim();
    if !name.is_empty() {
        return name.to_owned();
    }
    let fallback = fallback.trim();
    if !fallback.is_empty() {
        return fallback.to_owned();
    }
    "-".to_owned()
}

fn loop_display_id(view: &LoopView) -> String {
    let short = view.short_id.trim();
    if !short.is_empty() {
        return short.to_owned();
    }
    if view.id.chars().count() <= 8 {
        return view.id.clone();
    }
    view.id.chars().take(8).collect()
}

fn short_run_id(id: &str) -> String {
    if id.chars().count() <= 8 {
        return id.to_owned();
    }
    id.chars().take(8).collect()
}

fn format_iterations(max: i64) -> String {
    if max <= 0 {
        return "unlimited".to_owned();
    }
    max.to_string()
}

fn format_duration_seconds(seconds: i64) -> String {
    if seconds <= 0 {
        return "-".to_owned();
    }
    let mut remaining = seconds as u64;
    let hours = remaining / 3600;
    remaining %= 3600;
    let minutes = remaining / 60;
    let secs = remaining % 60;
    if hours > 0 {
        format!("{hours}h{minutes}m{secs}s")
    } else if minutes > 0 {
        format!("{minutes}m{secs}s")
    } else {
        format!("{secs}s")
    }
}

fn format_time(value: Option<&str>) -> String {
    value.unwrap_or("-").to_owned()
}

fn run_exit_code(run: &RunView) -> String {
    run.exit_code
        .map_or_else(|| "-".to_owned(), |code| code.to_string())
}

fn format_run_duration(run: &RunView) -> String {
    let trimmed = run.duration.trim();
    if !trimmed.is_empty() {
        return trimmed.to_owned();
    }
    if run.status.trim().eq_ignore_ascii_case("running") {
        return "running".to_owned();
    }
    "-".to_owned()
}

#[derive(Debug, Clone, Copy, Default)]
struct RunCounts {
    success: usize,
    error: usize,
    killed: usize,
    running: usize,
}

fn count_runs(run_history: &[RunView]) -> RunCounts {
    let mut counts = RunCounts::default();
    for run in run_history {
        let status = run.status.trim().to_ascii_lowercase();
        match status.as_str() {
            "success" => counts.success += 1,
            "error" => counts.error += 1,
            "killed" => counts.killed += 1,
            "running" => counts.running += 1,
            _ => {}
        }
    }
    counts
}

fn trim_to_height<T: Clone>(items: &[T], height: usize) -> Vec<T> {
    if height == 0 || items.len() <= height {
        return items.to_vec();
    }
    items[..height].to_vec()
}

#[must_use]
pub fn overview_pane_lines(
    selected_loop: Option<&LoopView>,
    run_history: &[RunView],
    selected_run: usize,
    width: usize,
    height: usize,
) -> Vec<OverviewLine> {
    let content_width = width.saturating_sub(2).max(1);

    let mut out: Vec<OverviewLine> = Vec::with_capacity(32);
    let mut push = |role: TextRole, text: String| {
        out.push(OverviewLine {
            text: truncate_line(&text, content_width),
            role,
        });
    };

    let Some(loop_view) = selected_loop else {
        push(TextRole::Primary, "No loop selected.".to_owned());
        push(
            TextRole::Primary,
            "Use j/k or arrow keys to choose a loop.".to_owned(),
        );
        push(
            TextRole::Primary,
            "Start one: forge up --count 1".to_owned(),
        );
        push(TextRole::Primary, String::new());
        push(
            TextRole::Muted,
            "Workflow: 2=Logs (deep scroll) | 3=Runs | 4=Multi Logs".to_owned(),
        );
        return trim_to_height(&out, height.max(1));
    };

    push(
        TextRole::Primary,
        format!("ID: {}", loop_display_id(loop_view)),
    );
    push(TextRole::Primary, format!("Name: {}", loop_view.name));
    push(
        TextRole::Primary,
        format!("Status: {}", loop_view.state.trim().to_ascii_uppercase()),
    );
    push(TextRole::Primary, format!("Runs: {}", loop_view.runs));
    push(TextRole::Primary, format!("Dir: {}", loop_view.repo_path));
    push(
        TextRole::Primary,
        format!(
            "Pool: {}",
            display_name(&loop_view.pool_name, &loop_view.pool_id)
        ),
    );
    push(
        TextRole::Primary,
        format!(
            "Profile: {}",
            display_name(&loop_view.profile_name, &loop_view.profile_id)
        ),
    );
    push(
        TextRole::Primary,
        format!(
            "Harness/Auth: {} / {}",
            display_name(&loop_view.profile_harness, "-"),
            display_name(&loop_view.profile_auth, "-")
        ),
    );
    push(
        TextRole::Primary,
        format!(
            "Last Run: {}",
            format_time(loop_view.last_run_at.as_deref())
        ),
    );
    push(
        TextRole::Primary,
        format!("Queue Depth: {}", loop_view.queue_depth),
    );
    push(
        TextRole::Primary,
        format!(
            "Interval: {}",
            format_duration_seconds(loop_view.interval_seconds)
        ),
    );
    push(
        TextRole::Primary,
        format!(
            "Max Runtime: {}",
            format_duration_seconds(loop_view.max_runtime_seconds)
        ),
    );
    push(
        TextRole::Primary,
        format!(
            "Max Iterations: {}",
            format_iterations(loop_view.max_iterations)
        ),
    );
    if !loop_view.last_error.trim().is_empty() {
        push(
            TextRole::Primary,
            format!("Last Error: {}", loop_view.last_error),
        );
    }

    let counts = count_runs(run_history);
    push(TextRole::Primary, String::new());
    push(TextRole::Muted, "Run snapshot:".to_owned());
    push(
        TextRole::Primary,
        format!(
            "  total={} success={} error={} killed={} running={}",
            run_history.len(),
            counts.success,
            counts.error,
            counts.killed,
            counts.running
        ),
    );
    if !run_history.is_empty() {
        let idx = selected_run.min(run_history.len().saturating_sub(1));
        let run = &run_history[idx];
        push(
            TextRole::Primary,
            format!(
                "  latest={} status={} exit={} duration={}",
                short_run_id(&run.id),
                run.status.trim().to_ascii_uppercase(),
                run_exit_code(run),
                format_run_duration(run)
            ),
        );
    }

    push(TextRole::Primary, String::new());
    push(
        TextRole::Muted,
        "Workflow: 2=Logs (deep scroll) | 3=Runs | 4=Multi Logs".to_owned(),
    );

    trim_to_height(&out, height.max(1))
}

// ---------------------------------------------------------------------------
// Paneled overview rendering — uses draw_panel for visual hierarchy
// ---------------------------------------------------------------------------

/// Render the overview tab content with bordered panels into a `RenderFrame`.
///
/// Layout:
/// ```text
/// ╭─ Fleet ────────────────────────────────────────────────────╮
/// │ ● 2 run  ○ 0 sleep  ■ 1 stop  ✗ 1 err │ queue:15 │ 50% ok│
/// ╰────────────────────────────────────────────────────────────╯
/// ╭─ Loop: operator-loop-2 ────────────────────────────────────╮
/// │ key-value fields ...                                       │
/// ╰────────────────────────────────────────────────────────────╯
/// ╭─ Run Snapshot ─────────────────────────────────────────────╮
/// │ total=N success=N error=N ...                              │
/// ╰────────────────────────────────────────────────────────────╯
/// ```
pub fn render_overview_paneled(
    frame: &mut RenderFrame,
    loops: &[LoopView],
    selected_loop: Option<&LoopView>,
    run_history: &[RunView],
    selected_run: usize,
    pal: &ResolvedPalette,
    area: Rect,
    _focus_right: bool,
) {
    render_overview_paneled_with_options(
        frame,
        loops,
        selected_loop,
        run_history,
        selected_run,
        pal,
        area,
        _focus_right,
        OverviewPaneOptions::default(),
    );
}

#[allow(clippy::too_many_arguments)]
pub fn render_overview_paneled_with_options(
    frame: &mut RenderFrame,
    loops: &[LoopView],
    selected_loop: Option<&LoopView>,
    run_history: &[RunView],
    selected_run: usize,
    pal: &ResolvedPalette,
    area: Rect,
    _focus_right: bool,
    options: OverviewPaneOptions,
) {
    if area.width < 4 || area.height < 4 {
        return;
    }

    // Fill entire content area background
    frame.fill_bg(area, pal.background);

    let snap = build_fleet_snapshot(loops, run_history);

    // -- Fleet hero panel (4 rows: border + 2 content + border) --
    let hero_h = 4usize.min(area.height);
    let (hero_rect, rest) = area.split_vertical(hero_h);
    render_fleet_hero(frame, hero_rect, &snap, pal);

    // Guard: no space left
    if rest.height < 4 {
        return;
    }

    let Some(loop_view) = selected_loop else {
        // No selection — show guidance in a panel
        let guidance_h = 5usize.min(rest.height);
        let (guide_rect, _) = rest.split_vertical(guidance_h);
        let inner = frame.draw_panel(
            guide_rect,
            "Getting Started",
            BorderStyle::Rounded,
            pal.border,
            pal.panel,
        );
        draw_text_on_bg(
            frame,
            inner.x,
            inner.y,
            "No loop selected.",
            pal.text,
            pal.panel,
        );
        if inner.height > 1 {
            draw_text_on_bg(
                frame,
                inner.x,
                inner.y + 1,
                "Use j/k or arrow keys to choose a loop.",
                pal.text_muted,
                pal.panel,
            );
        }
        if inner.height > 2 {
            draw_text_on_bg(
                frame,
                inner.x,
                inner.y + 2,
                "Start one: forge up --count 1",
                pal.text_muted,
                pal.panel,
            );
        }
        return;
    };

    // -- Loop detail panel --
    let detail_fields = build_detail_fields(loop_view, pal);
    let detail_h = (detail_fields.len() + 2).min(rest.height); // +2 for borders
    let (detail_rect, rest2) = rest.split_vertical(detail_h);
    let loop_status_color = loop_status_color(&loop_view.state, pal);
    let detail_title = format!(
        "Loop: {} [{}]",
        display_name(&loop_view.name, &loop_display_id(loop_view)),
        loop_view.state.trim().to_ascii_uppercase()
    );
    let detail_inner = frame.draw_panel(
        detail_rect,
        &detail_title,
        BorderStyle::Rounded,
        loop_status_color,
        pal.panel,
    );
    for (i, field) in detail_fields.iter().enumerate() {
        if i >= detail_inner.height {
            break;
        }
        let trunc = truncate_line(&field.text, detail_inner.width);
        draw_text_on_bg_with_weight(
            frame,
            detail_inner.x,
            detail_inner.y + i,
            &trunc,
            field.color,
            pal.panel,
            field.bold,
        );
    }

    // -- Run snapshot panel --
    if rest2.height >= 4 {
        let counts = count_runs(run_history);
        let has_domain_panel = rest2.height >= 9;
        let has_next_action_slot = options.reserve_next_action_slot
            && rest2.height >= if has_domain_panel { 13 } else { 9 };
        let reserved_rows =
            if has_domain_panel { 4 } else { 0 } + if has_next_action_slot { 4 } else { 0 };
        let snap_h = 4usize.min(rest2.height.saturating_sub(reserved_rows));
        let (snap_rect, rest3) = rest2.split_vertical(snap_h);
        let run_health_color = if counts.error > 0 {
            pal.error
        } else if counts.running > 0 {
            pal.success
        } else {
            pal.text
        };
        let snap_inner = frame.draw_panel(
            snap_rect,
            "Run Snapshot",
            BorderStyle::Rounded,
            run_health_color,
            pal.panel,
        );
        let summary = format!(
            "total={}  success={}  error={}  killed={}  running={}",
            run_history.len(),
            counts.success,
            counts.error,
            counts.killed,
            counts.running,
        );
        draw_text_on_bg_with_weight(
            frame,
            snap_inner.x,
            snap_inner.y,
            &truncate_line(&summary, snap_inner.width),
            run_health_color,
            pal.panel,
            true,
        );
        if !run_history.is_empty() && snap_inner.height > 1 {
            let idx = selected_run.min(run_history.len().saturating_sub(1));
            let run = &run_history[idx];
            let latest_status_color = run_status_color(&run.status, pal);
            let latest = format!(
                "latest={}  status={}  exit={}  duration={}",
                short_run_id(&run.id),
                run.status.trim().to_ascii_uppercase(),
                run_exit_code(run),
                format_run_duration(run),
            );
            draw_text_on_bg_with_weight(
                frame,
                snap_inner.x,
                snap_inner.y + 1,
                &truncate_line(&latest, snap_inner.width),
                latest_status_color,
                pal.panel,
                true,
            );
        }

        let mut rest = rest3;

        // -- Work domain panel (smart loop clustering) --
        if has_domain_panel && rest.height >= 4 {
            let domain_h = 4usize.min(rest.height);
            let (domain_rect, rest_after_domain) = rest.split_vertical(domain_h);
            rest = rest_after_domain;
            let domain_inner = frame.draw_panel(
                domain_rect,
                "Work Domains (Auto)",
                BorderStyle::Rounded,
                pal.border,
                pal.panel,
            );

            let groups = cluster_loops_by_domain(loops);
            let summary = compact_domain_summary(&groups, 2);
            if domain_inner.height > 0 {
                let line = format!("top: {summary}");
                draw_text_on_bg(
                    frame,
                    domain_inner.x,
                    domain_inner.y,
                    &truncate_line(&line, domain_inner.width),
                    pal.text,
                    pal.panel,
                );
            }
            if domain_inner.height > 1 {
                let groups_line = format!("groups:{}  loops:{}", groups.len(), loops.len());
                draw_text_on_bg(
                    frame,
                    domain_inner.x,
                    domain_inner.y + 1,
                    &truncate_line(&groups_line, domain_inner.width),
                    pal.text_muted,
                    pal.panel,
                );
            }
        }

        if has_next_action_slot && rest.height >= 4 {
            let slot_h = 4usize.min(rest.height);
            let (slot_rect, rest_after_slot) = rest.split_vertical(slot_h);
            rest = rest_after_slot;
            let actions = build_next_action_lines(loop_view, run_history, selected_run);
            let slot_inner = frame.draw_panel(
                slot_rect,
                "Next Action",
                BorderStyle::Rounded,
                pal.border,
                pal.panel,
            );
            if slot_inner.height > 0 {
                draw_text_on_bg(
                    frame,
                    slot_inner.x,
                    slot_inner.y,
                    &truncate_line(
                        actions
                            .first()
                            .map_or("[2] Logs: inspect selected loop", String::as_str),
                        slot_inner.width,
                    ),
                    pal.accent,
                    pal.panel,
                );
            }
            if slot_inner.height > 1 {
                draw_text_on_bg(
                    frame,
                    slot_inner.x,
                    slot_inner.y + 1,
                    &truncate_line(
                        actions
                            .get(1)
                            .map_or("[3] Runs: inspect latest run context", String::as_str),
                        slot_inner.width,
                    ),
                    pal.text_muted,
                    pal.panel,
                );
            }
        }

        // -- Activity heatmap panel (selected loop + cross-loop health strip) --
        if rest.height >= 3 {
            let heatmap_h = 4usize.min(rest.height);
            let (heatmap_rect, rest_after_heatmap) = rest.split_vertical(heatmap_h);
            rest = rest_after_heatmap;
            render_activity_heatmap_panel(frame, heatmap_rect, loops, loop_view, run_history, pal);
        }

        // -- Workflow hint --
        if rest.height >= 1 {
            draw_text_on_bg(
                frame,
                rest.x + 1,
                rest.y,
                "Workflow: 2=Logs (deep scroll) | 3=Runs | 4=Multi Logs",
                pal.text_muted,
                pal.background,
            );
        }
    }
}

fn render_fleet_hero(
    frame: &mut RenderFrame,
    rect: Rect,
    snap: &FleetSnapshot,
    pal: &ResolvedPalette,
) {
    let inner = frame.draw_panel(rect, "Fleet", BorderStyle::Rounded, pal.border, pal.panel);
    if inner.width < 4 || inner.height < 1 {
        return;
    }

    // Row 0: Fleet status line with colored segments
    let mut col = inner.x;
    let y = inner.y;
    let max_col = inner.x + inner.width;

    let segments: Vec<(&str, usize, TermColor)> = vec![
        ("\u{25CF}", snap.running_loops, pal.success), // ● running
        ("\u{25CB}", snap.sleeping_loops, pal.text_muted), // ○ sleeping
        ("\u{25A0}", snap.stopped_loops, pal.warning), // ■ stopped
        ("\u{2716}", snap.error_loops, pal.error),     // ✗ error
    ];

    for (icon, count, color) in &segments {
        let chunk = format!("{icon}{count}  ");
        if col + chunk.len() > max_col {
            break;
        }
        frame.draw_styled_text(col, y, &chunk, *color, pal.panel, false);
        col += chunk.len();
    }

    // Queue depth
    let queue = format!(" q:{}", snap.total_queue_depth);
    if col + queue.len() + 2 <= max_col {
        frame.draw_styled_text(col, y, "\u{2502}", pal.border, pal.panel, false);
        col += 1;
        frame.draw_styled_text(col, y, &queue, pal.info, pal.panel, false);
        col += queue.len();
    }

    // Success rate text
    let success_ratio = if snap.total_runs > 0 {
        snap.success_runs as f64 / snap.total_runs as f64
    } else {
        0.0
    };
    let pct = (success_ratio * 100.0) as u32;
    let ok_text = format!(" {pct}% ok");
    let ok_color = if pct >= 80 {
        pal.success
    } else if pct >= 50 {
        pal.warning
    } else {
        pal.error
    };
    if col + ok_text.len() + 2 <= max_col {
        frame.draw_styled_text(col, y, "\u{2502}", pal.border, pal.panel, false);
        col += 1;
        frame.draw_styled_text(col, y, &ok_text, ok_color, pal.panel, false);
    }

    // Row 1: Gauge bar for success rate (uses adapter draw_gauge)
    if inner.height >= 2 {
        let gauge_y = inner.y + 1;
        let gauge_width = inner.width.min(30);
        frame.draw_gauge(
            inner.x,
            gauge_y,
            gauge_width,
            success_ratio,
            ok_color,
            pal.border,
            pal.panel,
        );
        // Label after gauge
        let label_x = inner.x + gauge_width + 1;
        if label_x < max_col {
            let label = format!("{}/{} runs ok", snap.success_runs, snap.total_runs);
            frame.draw_styled_text(label_x, gauge_y, &label, pal.text_muted, pal.panel, false);
        }
    }
}

fn render_activity_heatmap_panel(
    frame: &mut RenderFrame,
    rect: Rect,
    loops: &[LoopView],
    selected_loop: &LoopView,
    run_history: &[RunView],
    pal: &ResolvedPalette,
) {
    let inner = frame.draw_panel(
        rect,
        "Heatmaps & Trends",
        BorderStyle::Rounded,
        pal.info,
        pal.panel,
    );
    if inner.width == 0 || inner.height == 0 {
        return;
    }

    let mut row = 0usize;
    if let Some(trend) = build_selected_loop_trend(selected_loop, run_history) {
        let summary_color = if trend.summary.error_rate_pct >= 50 {
            pal.error
        } else if trend.summary.error_rate_pct >= 20 {
            pal.warning
        } else {
            pal.success
        };
        let activity_line = format!(
            "{} activity {}",
            loop_display_id(selected_loop),
            trend.activity_heatmap
        );
        draw_text_on_bg_with_weight(
            frame,
            inner.x,
            inner.y + row,
            &truncate_line(&activity_line, inner.width),
            summary_color,
            pal.panel,
            true,
        );
        row += 1;

        if row < inner.height {
            let sparkline_line = format!(
                "run:{} err:{} dur:{} lat:{}",
                trend.run_rate_sparkline,
                trend.error_rate_sparkline,
                trend.duration_sparkline,
                trend.latency_sparkline
            );
            draw_text_on_bg(
                frame,
                inner.x,
                inner.y + row,
                &truncate_line(&sparkline_line, inner.width),
                pal.text,
                pal.panel,
            );
            row += 1;
        }

        if row < inner.height {
            let summary_line = format!(
                "err:{}% runs:{} peak-lat:{}ms",
                trend.summary.error_rate_pct,
                trend.summary.total_runs,
                trend.summary.peak_latency_ms
            );
            draw_text_on_bg(
                frame,
                inner.x,
                inner.y + row,
                &truncate_line(&summary_line, inner.width),
                summary_color,
                pal.panel,
            );
            row += 1;
        }
    } else {
        let empty_line = "No run history yet for selected loop";
        draw_text_on_bg(
            frame,
            inner.x,
            inner.y + row,
            &truncate_line(empty_line, inner.width),
            pal.text_muted,
            pal.panel,
        );
        row += 1;
    }

    if row < inner.height {
        let health = build_cross_loop_health_strip(loops);
        let max_rows = inner.height.saturating_sub(row);
        let lines = render_cross_loop_heatmap_lines(&health, inner.width, max_rows);
        for (offset, line) in lines.iter().enumerate() {
            let y = inner.y + row + offset;
            if y >= inner.y + inner.height {
                break;
            }
            let color = if line.contains('X') {
                pal.error
            } else if line.contains('!') {
                pal.warning
            } else {
                pal.text_muted
            };
            draw_text_on_bg(frame, inner.x, y, line, color, pal.panel);
        }
    }
}

fn build_selected_loop_trend(
    selected_loop: &LoopView,
    run_history: &[RunView],
) -> Option<crate::activity_heatmap::LoopTrendVisual> {
    if run_history.is_empty() {
        return None;
    }

    let mut buckets = Vec::new();
    for (idx, run) in run_history.iter().enumerate().take(24) {
        let status = run.status.trim().to_ascii_lowercase();
        let duration_ms = parse_duration_ms(&run.duration).unwrap_or_else(|| {
            if status == "running" {
                0
            } else {
                1_000
            }
        });
        let error_count = usize::from(status == "error") as u32;
        let latency_penalty = if error_count > 0 { 750 } else { 150 };
        buckets.push(LoopTrendBucket {
            timestamp_epoch_s: idx as i64,
            run_count: 1,
            error_count,
            avg_duration_ms: duration_ms,
            avg_latency_ms: duration_ms.saturating_add(latency_penalty),
        });
    }
    buckets.reverse();

    let input = LoopTrendInput {
        loop_id: selected_loop.id.clone(),
        buckets,
    };
    build_loop_activity_trends(&[input], 24).into_iter().next()
}

fn build_cross_loop_health_strip(
    loops: &[LoopView],
) -> crate::health_heatmap_timeline::CrossLoopHealthTimeline {
    let mut inputs = Vec::with_capacity(loops.len());
    for loop_view in loops {
        let mut buckets = Vec::new();
        for idx in 0..12 {
            let is_error = loop_view.state.eq_ignore_ascii_case("error")
                || !loop_view.last_error.trim().is_empty();
            buckets.push(LoopHealthBucket {
                timestamp_epoch_s: idx as i64,
                state: loop_view.state.clone(),
                run_count: if loop_view.runs == 0 { 0 } else { 1 },
                error_count: if is_error && idx % 3 == 0 { 1 } else { 0 },
                queue_depth: loop_view.queue_depth,
                stalled: loop_view.state.eq_ignore_ascii_case("waiting") && idx >= 8,
            });
        }
        inputs.push(LoopHealthTimelineInput {
            loop_id: loop_display_id(loop_view),
            buckets,
        });
    }
    build_cross_loop_health_timeline(&inputs, 12)
}

fn parse_duration_ms(raw: &str) -> Option<u64> {
    let value = raw.trim().to_ascii_lowercase();
    if value.is_empty() || value == "-" || value == "running" || value == "done" {
        return None;
    }

    let mut total = 0f64;
    let mut current = String::new();
    let chars = value.chars().collect::<Vec<_>>();
    let mut idx = 0usize;
    while idx < chars.len() {
        let ch = chars[idx];
        if ch.is_ascii_digit() || ch == '.' {
            current.push(ch);
            idx += 1;
            continue;
        }

        let parsed = current.parse::<f64>().ok()?;
        current.clear();
        if ch == 'm' && idx + 1 < chars.len() && chars[idx + 1] == 's' {
            total += parsed;
            idx += 2;
            continue;
        }

        total += match ch {
            'h' => parsed * 3_600_000.0,
            'm' => parsed * 60_000.0,
            's' => parsed * 1_000.0,
            _ => return None,
        };
        idx += 1;
    }

    if !current.is_empty() {
        total += current.parse::<f64>().ok()? * 1_000.0;
    }
    if total.is_sign_negative() {
        return None;
    }
    Some(total.round() as u64)
}

fn loop_status_color(state: &str, pal: &ResolvedPalette) -> TermColor {
    match state.trim().to_ascii_lowercase().as_str() {
        "running" => pal.success,
        "error" => pal.error,
        "stopped" => pal.warning,
        "sleeping" | "waiting" => pal.info,
        _ => pal.text_muted,
    }
}

fn run_status_color(status: &str, pal: &ResolvedPalette) -> TermColor {
    match status.trim().to_ascii_lowercase().as_str() {
        "success" => pal.success,
        "error" => pal.error,
        "killed" => pal.warning,
        "running" => pal.info,
        _ => pal.text_muted,
    }
}

/// Build structured detail fields for a loop.
fn build_detail_fields(lv: &LoopView, pal: &ResolvedPalette) -> Vec<DetailField> {
    let status_upper = lv.state.trim().to_ascii_uppercase();
    let status_color = loop_status_color(&lv.state, pal);
    let queue_color = if lv.queue_depth > 0 {
        pal.warning
    } else {
        pal.text
    };

    let mut fields = Vec::with_capacity(14);
    fields.push(DetailField {
        text: format!(
            "Name: {} ({})",
            display_name(&lv.name, &loop_display_id(lv)),
            loop_display_id(lv)
        ),
        color: pal.accent,
        bold: true,
    });
    fields.push(DetailField {
        text: format!("Status: {status_upper}"),
        color: status_color,
        bold: true,
    });
    fields.push(DetailField {
        text: format!("Runs: {}", lv.runs),
        color: pal.text,
        bold: false,
    });
    fields.push(DetailField {
        text: format!("Queue Depth: {}", lv.queue_depth),
        color: queue_color,
        bold: lv.queue_depth > 0,
    });
    fields.push(DetailField {
        text: format!(
            "Profile: {}",
            display_name(&lv.profile_name, &lv.profile_id)
        ),
        color: pal.text,
        bold: false,
    });
    fields.push(DetailField {
        text: format!("Pool: {}", display_name(&lv.pool_name, &lv.pool_id)),
        color: pal.text,
        bold: false,
    });
    fields.push(DetailField {
        text: format!("Dir: {}", lv.repo_path),
        color: pal.text_muted,
        bold: false,
    });
    fields.push(DetailField {
        text: format!(
            "Harness/Auth: {} / {}",
            display_name(&lv.profile_harness, "-"),
            display_name(&lv.profile_auth, "-"),
        ),
        color: pal.text_muted,
        bold: false,
    });
    fields.push(DetailField {
        text: format!("Last Run: {}", format_time(lv.last_run_at.as_deref())),
        color: pal.text_muted,
        bold: false,
    });
    fields.push(DetailField {
        text: format!("Interval: {}", format_duration_seconds(lv.interval_seconds)),
        color: pal.text,
        bold: false,
    });
    fields.push(DetailField {
        text: format!(
            "Max Runtime: {}",
            format_duration_seconds(lv.max_runtime_seconds)
        ),
        color: pal.text,
        bold: false,
    });
    fields.push(DetailField {
        text: format!("Max Iterations: {}", format_iterations(lv.max_iterations)),
        color: pal.text,
        bold: false,
    });
    if !lv.last_error.trim().is_empty() {
        fields.push(DetailField {
            text: format!("Last Error: {}", lv.last_error),
            color: pal.error,
            bold: true,
        });
    }
    fields
}

/// Helper: draw text with explicit fg on a specified bg color.
fn draw_text_on_bg(
    frame: &mut RenderFrame,
    x: usize,
    y: usize,
    text: &str,
    fg: TermColor,
    bg: TermColor,
) {
    draw_text_on_bg_with_weight(frame, x, y, text, fg, bg, false);
}

fn draw_text_on_bg_with_weight(
    frame: &mut RenderFrame,
    x: usize,
    y: usize,
    text: &str,
    fg: TermColor,
    bg: TermColor,
    bold: bool,
) {
    frame.draw_styled_text(x, y, text, fg, bg, bold);
}

#[cfg(test)]
#[allow(clippy::unwrap_used)]
mod tests {
    use super::*;

    use forge_ftui_adapter::render::{FrameSize, RenderFrame};
    use forge_ftui_adapter::snapshot::assert_render_frame_snapshot;

    fn frame_from_lines(lines: &[OverviewLine], width: usize, height: usize) -> RenderFrame {
        let theme = crate::default_theme();
        let mut frame = RenderFrame::new(FrameSize { width, height }, theme);
        for (i, line) in lines.iter().enumerate() {
            if i >= height {
                break;
            }
            frame.draw_text(0, i, &line.text, line.role);
        }
        frame
    }

    #[test]
    fn no_selection_guidance_matches_go_shape() {
        let lines = overview_pane_lines(None, &[], 0, 40, 8);
        assert_eq!(lines[0].text.trim(), "No loop selected.");
        assert!(lines[2].text.contains("forge up --count 1"));
    }

    #[test]
    fn snapshot_overview_pane_content() {
        let view = LoopView {
            id: "abcdef1234567890".to_owned(),
            short_id: "abc12345".to_owned(),
            name: "demo-loop".to_owned(),
            state: "running".to_owned(),
            repo_path: "/repo/demo".to_owned(),
            runs: 7,
            queue_depth: 3,
            last_run_at: Some("2026-02-09T20:00:00Z".to_owned()),
            interval_seconds: 60,
            max_runtime_seconds: 3600,
            max_iterations: 0,
            last_error: "boom".to_owned(),
            pool_name: "".to_owned(),
            pool_id: "pool-1".to_owned(),
            profile_name: "dev".to_owned(),
            profile_id: "profile-1".to_owned(),
            profile_harness: "".to_owned(),
            profile_auth: "ssh".to_owned(),
        };

        let history = vec![
            RunView {
                id: "run-000000001".to_owned(),
                status: "success".to_owned(),
                exit_code: Some(0),
                duration: "12s".to_owned(),
                ..Default::default()
            },
            RunView {
                id: "run-000000002".to_owned(),
                status: "error".to_owned(),
                exit_code: Some(1),
                duration: "3s".to_owned(),
                ..Default::default()
            },
            RunView {
                id: "run-000000003".to_owned(),
                status: "killed".to_owned(),
                exit_code: None,
                duration: "1s".to_owned(),
                ..Default::default()
            },
            RunView {
                id: "run-000000004".to_owned(),
                status: "running".to_owned(),
                exit_code: None,
                duration: "running".to_owned(),
                ..Default::default()
            },
        ];

        let lines = overview_pane_lines(Some(&view), &history, 1, 60, 20);
        let frame = frame_from_lines(&lines, 60, 20);
        assert_render_frame_snapshot(
            "forge_loop_overview_pane",
            &frame,
            "ID: abc12345                                                \nName: demo-loop                                             \nStatus: RUNNING                                             \nRuns: 7                                                     \nDir: /repo/demo                                             \nPool: pool-1                                                \nProfile: dev                                                \nHarness/Auth: - / ssh                                       \nLast Run: 2026-02-09T20:00:00Z                              \nQueue Depth: 3                                              \nInterval: 1m0s                                              \nMax Runtime: 1h0m0s                                         \nMax Iterations: unlimited                                   \nLast Error: boom                                            \n                                                            \nRun snapshot:                                               \n  total=4 success=1 error=1 killed=1 running=1              \n  latest=run-0000 status=ERROR exit=1 duration=3s           \n                                                            \nWorkflow: 2=Logs (deep scroll) | 3=Runs | 4=Multi Logs      ",
        );
    }

    #[test]
    fn paneled_overview_shows_work_domains_when_space_allows() {
        let theme = crate::default_theme();
        let mut frame = RenderFrame::new(
            FrameSize {
                width: 120,
                height: 30,
            },
            theme,
        );
        let loops = vec![
            LoopView {
                id: "loop-a".to_owned(),
                short_id: "a".to_owned(),
                name: "auth-worker-a".to_owned(),
                repo_path: "/repo/crates/auth-core/src".to_owned(),
                profile_name: "dev".to_owned(),
                ..LoopView::default()
            },
            LoopView {
                id: "loop-b".to_owned(),
                short_id: "b".to_owned(),
                name: "auth-worker-b".to_owned(),
                repo_path: "/repo/crates/auth-core/tests".to_owned(),
                profile_name: "dev".to_owned(),
                ..LoopView::default()
            },
            LoopView {
                id: "loop-c".to_owned(),
                short_id: "c".to_owned(),
                name: "db-worker".to_owned(),
                repo_path: "/repo/crates/db-layer/src".to_owned(),
                profile_name: "dev".to_owned(),
                ..LoopView::default()
            },
        ];

        let runs = vec![RunView {
            id: "run-1".to_owned(),
            status: "success".to_owned(),
            exit_code: Some(0),
            duration: "2s".to_owned(),
            ..RunView::default()
        }];
        let pal = crate::theme::resolve_palette_colors(&crate::theme::DEFAULT_PALETTE);

        render_overview_paneled(
            &mut frame,
            &loops,
            Some(&loops[0]),
            &runs,
            0,
            &pal,
            Rect {
                x: 0,
                y: 0,
                width: 120,
                height: 30,
            },
            false,
        );

        let snapshot = frame.snapshot();
        assert!(snapshot.contains("Work Domains (Auto)"));
        assert!(snapshot.contains("groups:"));
        assert!(snapshot.contains("auth-core"));
    }

    #[test]
    fn paneled_overview_next_action_slot_hidden_by_default() {
        let theme = crate::default_theme();
        let mut frame = RenderFrame::new(
            FrameSize {
                width: 120,
                height: 34,
            },
            theme,
        );
        let loops = vec![LoopView {
            id: "loop-a".to_owned(),
            short_id: "a".to_owned(),
            name: "auth-worker-a".to_owned(),
            repo_path: "/repo/crates/auth-core/src".to_owned(),
            profile_name: "dev".to_owned(),
            ..LoopView::default()
        }];
        let runs = vec![RunView {
            id: "run-1".to_owned(),
            status: "success".to_owned(),
            exit_code: Some(0),
            duration: "2s".to_owned(),
            ..RunView::default()
        }];
        let pal = crate::theme::resolve_palette_colors(&crate::theme::DEFAULT_PALETTE);

        render_overview_paneled(
            &mut frame,
            &loops,
            Some(&loops[0]),
            &runs,
            0,
            &pal,
            Rect {
                x: 0,
                y: 0,
                width: 120,
                height: 34,
            },
            false,
        );

        let snapshot = frame.snapshot();
        assert!(!snapshot.contains("Next Action (reserved)"));
    }

    #[test]
    fn paneled_overview_next_action_slot_renders_when_enabled() {
        let theme = crate::default_theme();
        let mut frame = RenderFrame::new(
            FrameSize {
                width: 120,
                height: 34,
            },
            theme,
        );
        let loops = vec![LoopView {
            id: "loop-a".to_owned(),
            short_id: "a".to_owned(),
            name: "auth-worker-a".to_owned(),
            repo_path: "/repo/crates/auth-core/src".to_owned(),
            profile_name: "dev".to_owned(),
            ..LoopView::default()
        }];
        let runs = vec![RunView {
            id: "run-1".to_owned(),
            status: "success".to_owned(),
            exit_code: Some(0),
            duration: "2s".to_owned(),
            ..RunView::default()
        }];
        let pal = crate::theme::resolve_palette_colors(&crate::theme::DEFAULT_PALETTE);

        render_overview_paneled_with_options(
            &mut frame,
            &loops,
            Some(&loops[0]),
            &runs,
            0,
            &pal,
            Rect {
                x: 0,
                y: 0,
                width: 120,
                height: 34,
            },
            false,
            OverviewPaneOptions {
                reserve_next_action_slot: true,
            },
        );

        let snapshot = frame.snapshot();
        assert!(snapshot.contains("Next Action"));
        assert!(snapshot.contains("[2] Logs: verify healthy output stream"));
    }

    #[test]
    fn paneled_overview_next_action_slot_prioritizes_error_remediation() {
        let theme = crate::default_theme();
        let mut frame = RenderFrame::new(
            FrameSize {
                width: 120,
                height: 34,
            },
            theme,
        );
        let loops = vec![LoopView {
            id: "loop-e".to_owned(),
            short_id: "e".to_owned(),
            name: "error-loop".to_owned(),
            state: "error".to_owned(),
            last_error: "panic: broken pipe".to_owned(),
            repo_path: "/repo/crates/auth-core/src".to_owned(),
            profile_name: "dev".to_owned(),
            ..LoopView::default()
        }];
        let runs = vec![RunView {
            id: "run-2".to_owned(),
            status: "error".to_owned(),
            exit_code: Some(1),
            duration: "4s".to_owned(),
            ..RunView::default()
        }];
        let pal = crate::theme::resolve_palette_colors(&crate::theme::DEFAULT_PALETTE);

        render_overview_paneled_with_options(
            &mut frame,
            &loops,
            Some(&loops[0]),
            &runs,
            0,
            &pal,
            Rect {
                x: 0,
                y: 0,
                width: 120,
                height: 34,
            },
            false,
            OverviewPaneOptions {
                reserve_next_action_slot: true,
            },
        );

        let snapshot = frame.snapshot();
        assert!(snapshot.contains("[2] Logs: inspect error lines and root cause"));
        assert!(snapshot.contains("[3] Runs: inspect latest run output"));
    }
}
