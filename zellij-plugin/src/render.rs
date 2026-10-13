use crate::app::{App, Status, GROUP_ORDER};
use crate::feed::{Agent, Group};

pub fn render(app: &App, rows: usize, cols: usize, now_unix_secs: i64) -> Vec<String> {
    if rows == 0 {
        return Vec::new();
    }

    let Some(notice) = app.notice() else {
        return render_body(app, rows, cols, now_unix_secs);
    };

    let mut lines = render_body(app, rows - 1, cols, now_unix_secs);
    lines.resize(rows - 1, String::new());
    lines.push(truncate(notice, cols));
    lines
}

fn render_body(app: &App, rows: usize, cols: usize, now_unix_secs: i64) -> Vec<String> {
    if rows == 0 {
        return Vec::new();
    }

    let lines = match app.status() {
        Status::Ready => return render_list(app, rows, cols, now_unix_secs),
        Status::Loading => "loading…".to_string(),
        Status::NotRunning(_) => "cogitator is not running — start it inside zellij".to_string(),
        Status::PermissionDenied => "permission denied — grant the plugin permissions".to_string(),
        Status::Failed(msg) => msg.clone(),
    };
    vec![truncate(&lines, cols)]
}

fn render_list(app: &App, rows: usize, cols: usize, now: i64) -> Vec<String> {
    let agents = app.agents();
    if agents.is_empty() {
        return vec![truncate("no agents", cols)];
    }

    let selected = app.selected_index();
    let mut lines = Vec::new();
    let mut selected_line = 0;
    for group in GROUP_ORDER {
        let mut members = agents
            .iter()
            .enumerate()
            .filter(|(_, a)| a.group == group)
            .peekable();
        if members.peek().is_none() {
            continue;
        }

        lines.push(group_title(group).to_string());
        for (i, agent) in members {
            let is_selected = selected == Some(i);
            if is_selected {
                selected_line = lines.len();
            }
            lines.push(agent_row(agent, is_selected, now));
        }
    }

    let offset = scroll_offset(app.scroll.get(), selected_line, rows, lines.len());
    app.scroll.set(offset);

    lines
        .iter()
        .skip(offset)
        .take(rows)
        .map(|l| truncate(l, cols))
        .collect()
}

fn scroll_offset(current: usize, selected_line: usize, rows: usize, total: usize) -> usize {
    let mut offset = current.min(total.saturating_sub(rows));
    if selected_line < offset {
        offset = selected_line.saturating_sub(1);
    }
    if selected_line >= offset + rows {
        offset = selected_line + 1 - rows;
    }
    offset
}

fn group_title(group: Group) -> &'static str {
    match group {
        Group::NeedsYou => "Needs you",
        Group::Done => "Done",
        Group::Working => "Working",
        Group::Idle => "Idle",
        Group::Stopped => "Stopped",
    }
}

fn glyph(attention: &str) -> &'static str {
    match attention {
        "permission" | "question" => "?",
        "errored" => "!",
        "finished" => "✓",
        "active" => "●",
        _ => "·",
    }
}

fn agent_row(agent: &Agent, selected: bool, now: i64) -> String {
    let marker = if selected { "> " } else { "  " };
    let mut row = format!("{marker}{} {}", glyph(&agent.attention), agent.label());
    if !agent.harness.is_empty() {
        row.push(' ');
        row.push_str(&agent.harness);
    }
    if let Some(ts) = parse_rfc3339(&agent.last_activity) {
        row.push(' ');
        row.push_str(&relative_time(now - ts));
    }
    row
}

fn truncate(line: &str, cols: usize) -> String {
    line.chars().take(cols).collect()
}

pub fn relative_time(elapsed_secs: i64) -> String {
    if elapsed_secs < 60 {
        return "now".to_string();
    }
    if elapsed_secs < 3600 {
        return format!("{}m", elapsed_secs / 60);
    }
    if elapsed_secs < 86400 {
        return format!("{}h", elapsed_secs / 3600);
    }
    format!("{}d", elapsed_secs / 86400)
}

pub fn parse_rfc3339(s: &str) -> Option<i64> {
    let b = s.as_bytes();
    if b.len() < 20 || b[4] != b'-' || b[7] != b'-' || !matches!(b[10], b'T' | b't') {
        return None;
    }
    if b[13] != b':' || b[16] != b':' {
        return None;
    }

    let year = num(s, 0, 4)?;
    let month = num(s, 5, 7)?;
    let day = num(s, 8, 10)?;
    let hour = num(s, 11, 13)?;
    let min = num(s, 14, 16)?;
    let sec = num(s, 17, 19)?;
    if !(1..=12).contains(&month) || !(1..=31).contains(&day) || hour > 23 || min > 59 || sec > 60 {
        return None;
    }

    let mut rest = &s[19..];
    if let Some(frac) = rest.strip_prefix('.') {
        let digits = frac.bytes().take_while(u8::is_ascii_digit).count();
        if digits == 0 {
            return None;
        }
        rest = &frac[digits..];
    }

    let offset = parse_offset(rest)?;
    let days = days_from_civil(year, month, day);
    Some(days * 86400 + hour * 3600 + min * 60 + sec - offset)
}

fn parse_offset(s: &str) -> Option<i64> {
    if s == "Z" || s == "z" {
        return Some(0);
    }

    let b = s.as_bytes();
    if b.len() != 6 || b[3] != b':' {
        return None;
    }
    let sign = match b[0] {
        b'+' => 1,
        b'-' => -1,
        _ => return None,
    };
    let h = num(s, 1, 3)?;
    let m = num(s, 4, 6)?;
    Some(sign * (h * 3600 + m * 60))
}

fn num(s: &str, from: usize, to: usize) -> Option<i64> {
    let part = s.get(from..to)?;
    if !part.bytes().all(|c| c.is_ascii_digit()) {
        return None;
    }
    part.parse().ok()
}

fn days_from_civil(y: i64, m: i64, d: i64) -> i64 {
    let y = if m <= 2 { y - 1 } else { y };
    let era = y.div_euclid(400);
    let yoe = y - era * 400;
    let mp = (m + 9) % 12;
    let doy = (153 * mp + 2) / 5 + d - 1;
    let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    era * 146097 + doe - 719468
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::app::Key;

    const NOW: i64 = 1_767_225_600;

    fn app_from(json: &str) -> App {
        let mut app = App::new();
        app.apply_list_result(Some(0), json.as_bytes(), b"");
        app
    }

    fn simple_app(agents: &[(&str, &str)]) -> App {
        app_from(&crate::app::tests::feed_json(agents))
    }

    #[test]
    fn groups_render_in_order_with_headers() {
        let app = simple_app(&[
            ("/x/stopped", "stopped"),
            ("/x/idle", "idle"),
            ("/x/needs", "needs_you"),
            ("/x/done", "done"),
        ]);
        let lines = render(&app, 20, 80, NOW);
        assert_eq!(
            lines,
            vec![
                "Needs you",
                "> · needs",
                "Done",
                "  · done",
                "Idle",
                "  · idle",
                "Stopped",
                "  · stopped",
            ]
        );
    }

    #[test]
    fn row_shows_glyph_label_harness_and_age() {
        let app = app_from(
            r#"{"version":1,"agents":[{"id":"/p","kind":"worktree","repo":"r","branch":"b","harness":"omp","attention":"errored","group":"needs_you","last_activity":"2025-12-31T23:55:00Z"}]}"#,
        );
        assert_eq!(render(&app, 5, 80, NOW)[1], "> ! r/b omp 5m");
    }

    #[test]
    fn attention_glyphs_map_states() {
        assert_eq!(glyph("permission"), "?");
        assert_eq!(glyph("question"), "?");
        assert_eq!(glyph("errored"), "!");
        assert_eq!(glyph("finished"), "✓");
        assert_eq!(glyph("active"), "●");
        assert_eq!(glyph("inactive"), "·");
        assert_eq!(glyph(""), "·");
    }

    #[test]
    fn empty_last_activity_omits_age() {
        let app = app_from(
            r#"{"version":1,"agents":[{"id":"/p","title":"t","harness":"omp","group":"idle"}]}"#,
        );
        assert_eq!(render(&app, 5, 80, NOW)[1], "> · t omp");
    }

    #[test]
    fn lines_truncate_to_cols_counting_chars() {
        let app = app_from(
            r#"{"version":1,"agents":[{"id":"/p","title":"ñandú✓日本語","attention":"finished","group":"done"}]}"#,
        );
        let lines = render(&app, 5, 8, NOW);
        assert_eq!(lines[1], "> ✓ ñand");
        assert!(lines.iter().all(|l| l.chars().count() <= 8));
    }

    #[test]
    fn scrolling_keeps_selection_visible() {
        let mut app = simple_app(&[
            ("/a", "idle"),
            ("/b", "idle"),
            ("/c", "idle"),
            ("/d", "idle"),
            ("/e", "idle"),
        ]);
        for _ in 0..4 {
            app.handle_key(Key::Down);
        }
        let lines = render(&app, 3, 80, NOW);
        assert_eq!(lines, vec!["  · c", "  · d", "> · e"]);

        for _ in 0..4 {
            app.handle_key(Key::Up);
        }
        let lines = render(&app, 3, 80, NOW);
        assert_eq!(lines, vec!["Idle", "> · a", "  · b"]);
    }

    #[test]
    fn never_renders_more_than_rows() {
        let app = simple_app(&[("/a", "idle"), ("/b", "done"), ("/c", "working")]);
        assert_eq!(render(&app, 2, 80, NOW).len(), 2);
        assert!(render(&app, 0, 80, NOW).is_empty());
    }

    #[test]
    fn non_ready_statuses_render_single_line() {
        let mut app = App::new();
        app.apply_list_result(Some(1), b"", b"cogitator is not running");
        assert_eq!(
            render(&app, 5, 80, NOW),
            vec!["cogitator is not running — start it inside zellij"]
        );

        app.set_permission_denied();
        assert_eq!(
            render(&app, 5, 80, NOW),
            vec!["permission denied — grant the plugin permissions"]
        );

        app.apply_list_result(Some(3), b"", b"kaput");
        assert_eq!(render(&app, 5, 4, NOW), vec!["kapu"]);
    }

    #[test]
    fn empty_ready_list_says_no_agents() {
        let app = simple_app(&[]);
        assert_eq!(render(&app, 5, 80, NOW), vec!["no agents"]);
    }

    #[test]
    fn notice_takes_the_bottom_row_below_the_list() {
        let mut app = simple_app(&[("/a", "idle"), ("/b", "idle"), ("/c", "idle")]);
        app.set_notice("focus failed");
        assert_eq!(
            render(&app, 4, 80, NOW),
            vec!["Idle", "> · a", "  · b", "focus failed"]
        );
        assert_eq!(
            render(&app, 5, 80, NOW),
            vec!["Idle", "> · a", "  · b", "  · c", "focus failed"]
        );
        assert_eq!(
            render(&app, 6, 80, NOW),
            vec!["Idle", "> · a", "  · b", "  · c", "", "focus failed"]
        );
    }

    #[test]
    fn notice_alone_fits_a_single_row() {
        let mut app = simple_app(&[("/a", "idle")]);
        app.set_notice("focus failed");
        assert_eq!(render(&app, 1, 5, NOW), vec!["focus"]);
    }

    #[test]
    fn relative_time_buckets() {
        assert_eq!(relative_time(-5), "now");
        assert_eq!(relative_time(59), "now");
        assert_eq!(relative_time(300), "5m");
        assert_eq!(relative_time(3 * 3600 + 10), "3h");
        assert_eq!(relative_time(2 * 86400 + 5), "2d");
    }

    #[test]
    fn parses_rfc3339_utc_and_offsets() {
        assert_eq!(parse_rfc3339("1970-01-01T00:00:00Z"), Some(0));
        assert_eq!(parse_rfc3339("2026-01-01T00:00:00Z"), Some(NOW));
        assert_eq!(parse_rfc3339("2026-01-01T00:00:00.123456789Z"), Some(NOW));
        assert_eq!(parse_rfc3339("2026-01-01T02:00:00+02:00"), Some(NOW));
        assert_eq!(parse_rfc3339("2025-12-31T23:00:00-01:00"), Some(NOW));
        assert_eq!(parse_rfc3339("2024-02-29T00:00:00Z"), Some(1_709_164_800));
        assert_eq!(parse_rfc3339(""), None);
        assert_eq!(parse_rfc3339("2026-01-01 00:00:00"), None);
        assert_eq!(parse_rfc3339("2026-13-01T00:00:00Z"), None);
    }
}
