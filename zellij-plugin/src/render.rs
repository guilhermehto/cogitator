use crate::app::{App, Status};
use crate::feed::Agent;

// Glyphs, colours and widths mirror the TUI's Agents tab (internal/ui) so
// both surfaces read the same.
const GLYPH_ACTIVE: &str = "\u{f0765}";
const GLYPH_INACTIVE: &str = "\u{f0766}";
const GLYPH_QUESTION: &str = "\u{f0625}";
const GLYPH_PERMISSION: &str = "\u{f033e}";
const GLYPH_ERROR: &str = "\u{f0026}";
const GLYPH_FINISHED: &str = "\u{f012c}";
const GLYPH_MISSING: &str = "\u{f0e7a}";
const GLYPH_HERE: &str = "▸";

const HARNESS_WIDTH: usize = 11;
const DETAIL_MIN_WIDTH: usize = 80;
const SELECTED_BG: u8 = 237;

const HARNESS_PALETTE: [u8; 22] = [
    33, 39, 45, 51, 75, 81, 99, 105, 111, 117, 135, 141, 147, 153, 165, 171, 177, 183, 203, 207,
    213, 219,
];

const HINTS: [(&str, &str); 2] = [("enter", "jump"), ("r", "refresh")];

#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
struct Style {
    fg: Option<u8>,
    bold: bool,
    italic: bool,
}

impl Style {
    const fn fg(colour: u8) -> Self {
        Style {
            fg: Some(colour),
            bold: false,
            italic: false,
        }
    }

    const fn bold(self) -> Self {
        Style { bold: true, ..self }
    }

    const fn italic(self) -> Self {
        Style {
            italic: true,
            ..self
        }
    }
}

const PLAIN: Style = Style {
    fg: None,
    bold: false,
    italic: false,
};
const DIM: Style = Style::fg(240);
const HEADER: Style = Style::fg(244).bold();
const RECENT: Style = Style::fg(244).italic();
const REPO: Style = Style::fg(63);
const PATH: Style = Style::fg(240).italic();
const HINT: Style = Style::fg(214).italic();
const HINT_KEY: Style = Style::fg(111).bold();
const ERROR: Style = Style::fg(196);
const PERMISSION: Style = Style::fg(214).bold();
const QUESTION: Style = Style::fg(226).bold();
const ERRORED: Style = Style::fg(196).bold();
const FINISHED: Style = Style::fg(51).bold();
const INACTIVE: Style = Style::fg(244);
const ACTIVE: Style = Style::fg(82);
const MISSING: Style = Style::fg(196).italic();
const UNKNOWN: Style = Style::fg(226);
const HERE: Style = Style::fg(111).bold();

struct Span {
    text: String,
    style: Style,
}

fn span(text: impl Into<String>, style: Style) -> Span {
    Span {
        text: text.into(),
        style,
    }
}

type Line = Vec<Span>;

pub fn render(app: &App, rows: usize, cols: usize, now_unix_secs: i64) -> Vec<String> {
    if rows == 0 {
        return Vec::new();
    }

    let bottom = match app.notice() {
        Some(notice) => Some(vec![span(notice, HINT)]),
        None if rows > 2 => Some(footer(app.floating())),
        None => None,
    };
    let Some(bottom) = bottom else {
        return render_body(app, rows, cols, now_unix_secs);
    };

    let mut lines = render_body(app, rows - 1, cols, now_unix_secs);
    lines.resize(rows - 1, String::new());
    lines.push(paint(&truncate(bottom, cols), None));
    lines
}

/// A floating plugin hides on q; a docked sidebar hands focus back to the
/// pane on its right instead, since hiding a docked pane loses its place.
fn footer(floating: bool) -> Line {
    let dismiss = if floating {
        ("q", "hide")
    } else {
        ("q", "back")
    };

    let mut line = Vec::new();
    for (i, (key, desc)) in HINTS.iter().chain([&dismiss]).enumerate() {
        if i > 0 {
            line.push(span("   ", PLAIN));
        }
        line.push(span(*key, HINT_KEY));
        line.push(span(format!(" {desc}"), DIM));
    }
    line
}

fn render_body(app: &App, rows: usize, cols: usize, now: i64) -> Vec<String> {
    if rows == 0 {
        return Vec::new();
    }

    let status = match app.status() {
        Status::Ready => return render_list(app, rows, cols, now),
        Status::Loading => span("loading…", DIM),
        Status::NotRunning(_) => span("cogitator is not running — start it inside zellij", HINT),
        Status::PermissionDenied => span("permission denied — grant the plugin permissions", HINT),
        Status::Failed(msg) => span(msg.clone(), ERROR),
    };
    vec![paint(&truncate(vec![status], cols), None)]
}

fn render_list(app: &App, rows: usize, cols: usize, now: i64) -> Vec<String> {
    let agents = app.agents();
    if agents.is_empty() {
        return vec![paint(
            &truncate(vec![span("no agents in flight", DIM)], cols),
            None,
        )];
    }

    let selected = app.selected_index();
    let here = app.here_index();
    // App keeps agents that need the user at the front of the list.
    let split = agents.iter().take_while(|a| a.needs_you()).count();
    let sections = [
        ("Needs you", PERMISSION, 0..split),
        ("Agents", HEADER, split..agents.len()),
    ];

    let mut lines = Vec::new();
    let mut selected_line = 0;
    for (title, style, range) in sections {
        if range.is_empty() {
            continue;
        }

        if !lines.is_empty() {
            lines.push(String::new());
        }
        let header = vec![span(title, style), span(format!(" · {}", range.len()), DIM)];
        lines.push(paint(&truncate(header, cols), None));
        for i in range {
            let is_selected = selected == Some(i);
            if is_selected {
                selected_line = lines.len();
            }
            let mark = RowMark {
                selected: is_selected,
                here: here == Some(i),
            };
            lines.push(agent_row(&agents[i], mark, cols, now, app.home()));
        }
    }

    let offset = scroll_offset(app.scroll.get(), selected_line, rows, lines.len());
    app.scroll.set(offset);

    lines.into_iter().skip(offset).take(rows).collect()
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

struct RowMark {
    selected: bool,
    here: bool,
}

// agent_row lays out status, name and (when wide enough) the muted session
// title on the left, harness and age on the right, as the TUI does. The
// agent the user is sitting at gets a marker that survives the selection band.
fn agent_row(agent: &Agent, mark: RowMark, cols: usize, now: i64, home: &str) -> String {
    let age = agent
        .last_activity_secs()
        .map(|ts| relative_time(now - ts))
        .unwrap_or_default();

    let mut left = vec![status_cell(agent)];
    if mark.here {
        left.push(span(format!("{GLYPH_HERE} "), HERE));
    }
    left.extend(styled_name(agent, home));
    let mut right = vec![span(age.clone(), DIM)];

    if cols >= DETAIL_MIN_WIDTH {
        if !agent.title.is_empty() && agent.title != agent.tab {
            left.push(span("  ", PLAIN));
            left.push(span(agent.title.clone(), PATH));
        }
        let harness = vec![span(agent.harness.clone(), harness_style(&agent.harness))];
        right = pad(truncate(harness, HARNESS_WIDTH), HARNESS_WIDTH);
        right.push(span(format!(" {age:>4}"), DIM));
    }

    let gap = cols.saturating_sub(width(&right) + 2);
    let mut row = pad(truncate(left, gap), gap);
    row.push(span("  ", PLAIN));
    row.extend(right);

    let bg = if mark.selected {
        Some(SELECTED_BG)
    } else {
        None
    };
    paint(&truncate(row, cols), bg)
}

fn status_cell(agent: &Agent) -> Span {
    let (glyph, style) = match agent.state.as_str() {
        "running" => attention_glyph(&agent.attention),
        "stopped" => (GLYPH_INACTIVE, DIM),
        "missing" => (GLYPH_MISSING, MISSING),
        "unknown" => ("?", UNKNOWN),
        _ => ("·", DIM),
    };
    span(format!("{glyph} "), style)
}

fn attention_glyph(attention: &str) -> (&'static str, Style) {
    match attention {
        "permission" => (GLYPH_PERMISSION, PERMISSION),
        "question" => (GLYPH_QUESTION, QUESTION),
        "errored" => (GLYPH_ERROR, ERRORED),
        "finished" => (GLYPH_FINISHED, FINISHED),
        "inactive" => (GLYPH_INACTIVE, INACTIVE),
        _ => (GLYPH_ACTIVE, ACTIVE),
    }
}

// styled_name mutes the repo or workspace so the branch or session reads
// first, led by the agent's tab label when it has one; stopped agents are
// dimmed.
fn styled_name(agent: &Agent, home: &str) -> Line {
    let lead = if agent.state == "stopped" { DIM } else { PLAIN };

    let name = match agent.kind.as_str() {
        "workspace" => vec![
            span(agent.workspace.clone(), REPO),
            span("/", DIM),
            span(agent.session_name.clone(), lead),
        ],
        "stray" => vec![
            span(abbreviate_home(&agent.dir, home), lead),
            span(" ", PLAIN),
            span("stray", RECENT),
        ],
        _ if agent.branch.is_empty() => vec![span(abbreviate_home(&agent.dir, home), lead)],
        _ => vec![
            span(base_name(&agent.repo), REPO),
            span("·", DIM),
            span(agent.branch.clone(), lead),
        ],
    };

    if agent.tab.is_empty() {
        return name;
    }
    let mut labelled = vec![span(agent.tab.clone(), lead), span(" · ", DIM)];
    labelled.extend(name);
    labelled
}

fn base_name(path: &str) -> String {
    path.trim_end_matches('/')
        .rsplit('/')
        .next()
        .unwrap_or_default()
        .to_string()
}

fn abbreviate_home(dir: &str, home: &str) -> String {
    if home.is_empty() {
        return dir.to_string();
    }
    if dir == home {
        return "~".to_string();
    }

    match dir
        .strip_prefix(home)
        .and_then(|rest| rest.strip_prefix('/'))
    {
        Some(rest) => format!("~/{rest}"),
        None => dir.to_string(),
    }
}

fn harness_style(harness: &str) -> Style {
    if harness.is_empty() {
        return Style::fg(141).italic();
    }

    let index = fnv1a32(harness) as usize % HARNESS_PALETTE.len();
    Style::fg(HARNESS_PALETTE[index]).italic()
}

fn fnv1a32(s: &str) -> u32 {
    s.bytes().fold(0x811c_9dc5, |hash, byte| {
        (hash ^ u32::from(byte)).wrapping_mul(0x0100_0193)
    })
}

fn width(line: &[Span]) -> usize {
    line.iter().map(|s| s.text.chars().count()).sum()
}

// truncate cuts line to cols display cells, ending in "…" when anything was
// dropped. Cells are counted as chars, which holds for the glyphs used here.
fn truncate(line: Line, cols: usize) -> Line {
    if width(&line) <= cols {
        return line;
    }
    if cols == 0 {
        return Vec::new();
    }

    let mut budget = cols - 1;
    let mut out = Vec::new();
    let mut tail_style = PLAIN;
    for s in line {
        tail_style = s.style;
        let kept: String = s.text.chars().take(budget).collect();
        budget -= kept.chars().count();
        out.push(span(kept, s.style));
        if budget == 0 {
            break;
        }
    }
    out.push(span("…", tail_style));
    out
}

fn pad(mut line: Line, cols: usize) -> Line {
    let missing = cols.saturating_sub(width(&line));
    if missing > 0 {
        line.push(span(" ".repeat(missing), PLAIN));
    }
    line
}

fn paint(line: &[Span], bg: Option<u8>) -> String {
    let mut out = String::new();
    for s in line {
        if s.text.is_empty() {
            continue;
        }

        let codes = sgr_codes(s.style, bg);
        if codes.is_empty() {
            out.push_str(&s.text);
        } else {
            out.push_str(&format!("\x1b[{}m{}\x1b[0m", codes.join(";"), s.text));
        }
    }
    out
}

fn sgr_codes(style: Style, bg: Option<u8>) -> Vec<String> {
    let mut codes = Vec::new();
    if style.bold {
        codes.push("1".to_string());
    }
    if style.italic {
        codes.push("3".to_string());
    }
    if let Some(fg) = style.fg {
        codes.push(format!("38;5;{fg}"));
    }
    if let Some(bg) = bg {
        codes.push(format!("48;5;{bg}"));
    }
    codes
}

pub fn relative_time(elapsed_secs: i64) -> String {
    if elapsed_secs < 60 {
        return "now".to_string();
    }
    if elapsed_secs < 3600 {
        return format!("{}m", elapsed_secs / 60);
    }
    format!("{}h", elapsed_secs / 3600)
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

    fn simple_app(ids: &[&str]) -> App {
        app_from(&crate::app::tests::feed_json(ids))
    }

    fn one_agent(fields: &str) -> App {
        app_from(&format!(
            r#"{{"version":1,"home":"/Users/me","agents":[{{"id":"/Users/me/p","dir":"/Users/me/p",{fields}}}]}}"#
        ))
    }

    fn plain(line: &str) -> String {
        let mut out = String::new();
        let mut chars = line.chars();
        while let Some(c) = chars.next() {
            if c == '\x1b' {
                for c in chars.by_ref() {
                    if c == 'm' {
                        break;
                    }
                }
                continue;
            }
            out.push(c);
        }
        out
    }

    fn plain_lines(lines: &[String]) -> Vec<String> {
        lines
            .iter()
            .map(|l| plain(l).trim_end().to_string())
            .collect()
    }

    fn row_colours(app: &App, cols: usize) -> String {
        render(app, 5, cols, NOW)[1].replace(";48;5;237", "")
    }

    #[test]
    fn needs_you_section_sits_above_the_rest() {
        let app = app_from(
            r#"{"version":1,"agents":[
                {"id":"/x/newer","dir":"/x/newer","group":"done","ordered_at":"2025-12-31T23:00:00Z"},
                {"id":"/x/ask","dir":"/x/ask","group":"needs_you","ordered_at":"2025-12-31T23:30:00Z"},
                {"id":"/x/older","dir":"/x/older","group":"idle","ordered_at":"2025-12-31T22:00:00Z"}
            ]}"#,
        );
        let lines: Vec<String> = plain_lines(&render(&app, 20, 40, NOW))
            .iter()
            .map(|l| l.split("  ").next().unwrap_or_default().to_string())
            .collect();
        assert_eq!(
            lines[..6],
            [
                "Needs you · 1",
                "· /x/ask",
                "",
                "Agents · 2",
                "· /x/older",
                "· /x/newer"
            ]
        );
    }

    #[test]
    fn needs_you_section_is_omitted_when_nobody_waits() {
        let app = simple_app(&["/a"]);
        assert_eq!(
            plain_lines(&render(&app, 4, 40, NOW))[..2],
            ["Agents · 1", "· /a"]
        );
    }

    #[test]
    fn worktree_row_shows_repo_folder_and_branch() {
        let app = one_agent(r#""kind":"worktree","repo":"/Users/me/.dotfiles","branch":"main""#);
        assert_eq!(
            plain_lines(&render(&app, 5, 40, NOW))[1],
            "· .dotfiles·main"
        );
    }

    #[test]
    fn workspace_row_shows_workspace_and_session() {
        let app = one_agent(r#""kind":"workspace","workspace":"w","session_name":"s""#);
        assert_eq!(plain_lines(&render(&app, 5, 40, NOW))[1], "· w/s");
    }

    #[test]
    fn stray_row_abbreviates_home_and_tags_stray() {
        let app = one_agent(r#""kind":"stray""#);
        assert_eq!(plain_lines(&render(&app, 5, 40, NOW))[1], "· ~/p stray");
    }

    #[test]
    fn running_agents_show_coloured_attention_glyph() {
        let app = one_agent(r#""kind":"stray","state":"running","attention":"permission""#);
        let row = &row_colours(&app, 40);
        assert!(
            row.contains(&format!("38;5;214m{GLYPH_PERMISSION} ")),
            "{row:?}"
        );

        let app = one_agent(r#""kind":"stray","state":"running","attention":"inactive""#);
        assert!(plain(&render(&app, 5, 40, NOW)[1]).starts_with(GLYPH_INACTIVE));
    }

    #[test]
    fn stopped_agents_are_dimmed() {
        let app = one_agent(r#""kind":"worktree","repo":"r","branch":"b","state":"stopped""#);
        let row = &row_colours(&app, 40);
        assert!(
            row.contains(&format!("38;5;240m{GLYPH_INACTIVE} ")),
            "{row:?}"
        );
        assert!(row.contains("38;5;240mb"), "{row:?}");
    }

    #[test]
    fn narrow_row_right_aligns_age_at_the_edge() {
        let app = one_agent(
            r#""kind":"worktree","repo":"r","branch":"b","harness":"omp","last_activity":"2025-12-31T23:55:00Z""#,
        );
        let row = plain(&render(&app, 5, 30, NOW)[1]);
        assert_eq!(row.chars().count(), 30);
        assert!(row.ends_with("  5m"), "{row:?}");
        assert!(!row.contains("omp"));
    }

    #[test]
    fn wide_row_adds_title_and_harness_column() {
        let app = one_agent(
            r#""kind":"worktree","repo":"r","branch":"b","harness":"omp","title":"fix it","last_activity":"2025-12-31T20:00:00Z""#,
        );
        let row = &row_colours(&app, 90);
        assert!(plain(row).ends_with("omp           4h"), "{:?}", plain(row));
        assert!(plain(row).contains("r·b  fix it"));
        assert!(row.contains("3;38;5;81momp"), "{row:?}");
    }

    #[test]
    fn tab_label_leads_the_row_and_is_not_repeated_as_title() {
        let named = one_agent(
            r#""kind":"stray","harness":"omp","tab":"LEX-123","title":"fix it","last_activity":"2025-12-31T20:00:00Z""#,
        );
        assert!(
            plain(&row_colours(&named, 90)).starts_with("· LEX-123 · ~/p stray  fix it"),
            "{:?}",
            plain(&row_colours(&named, 90))
        );

        let titled = one_agent(
            r#""kind":"stray","harness":"omp","tab":"fix it","title":"fix it","last_activity":"2025-12-31T20:00:00Z""#,
        );
        let row = plain(&row_colours(&titled, 90));
        assert_eq!(row.matches("fix it").count(), 1, "{row:?}");
    }

    #[test]
    fn selected_row_paints_background_across_full_width() {
        let app = simple_app(&["/a", "/b"]);
        let lines = render(&app, 5, 30, NOW);
        assert!(lines[1].contains("48;5;237"));
        assert_eq!(plain(&lines[1]).chars().count(), 30);
        assert!(!lines[2].contains("48;5;237"));
    }

    #[test]
    fn row_the_user_sits_at_is_marked_even_when_selected() {
        let mut app = app_from(
            r#"{"version":1,"agents":[{"id":"/a","dir":"/a","window_id":"s:1"},{"id":"/b","dir":"/b","window_id":"s:2"}]}"#,
        );
        app.set_location(crate::app::Location {
            session: "s".into(),
            window: Some("s:2".into()),
        });
        assert_eq!(
            plain_lines(&render(&app, 4, 30, NOW))[1..3],
            ["· /a", "· ▸ /b"]
        );

        app.handle_key(Key::Down);
        let row = &render(&app, 4, 30, NOW)[2];
        assert!(row.contains("48;5;237"), "{row:?}");
        assert!(plain(row).starts_with("· ▸ /b"), "{row:?}");
    }

    #[test]
    fn long_names_truncate_with_ellipsis_counting_chars() {
        let app = one_agent(
            r#""kind":"workspace","workspace":"ñandú","session_name":"日本語日本語日本語""#,
        );
        let row = plain(&render(&app, 5, 16, NOW)[1]);
        assert_eq!(row.chars().count(), 16);
        assert!(row.starts_with("· ñandú/日本語日本…"), "{row:?}");
    }

    #[test]
    fn footer_offers_back_when_docked_and_hide_when_floating() {
        let mut app = simple_app(&["/a"]);
        let docked = plain_lines(&render(&app, 6, 40, NOW));
        assert_eq!(docked.len(), 6);
        assert_eq!(docked[5], "enter jump   r refresh   q back");

        app.set_floating(true);
        let floating = plain_lines(&render(&app, 6, 40, NOW));
        assert_eq!(floating[5], "enter jump   r refresh   q hide");
    }

    #[test]
    fn scrolling_keeps_selection_visible() {
        let mut app = simple_app(&["/a", "/b", "/c", "/d", "/e"]);
        for _ in 0..4 {
            app.handle_key(Key::Down);
        }
        let lines = plain_lines(&render(&app, 4, 40, NOW));
        assert_eq!(lines[..3], ["· /c", "· /d", "· /e"]);

        for _ in 0..4 {
            app.handle_key(Key::Up);
        }
        let lines = plain_lines(&render(&app, 4, 40, NOW));
        assert_eq!(lines[..3], ["Agents · 5", "· /a", "· /b"]);
    }

    #[test]
    fn never_renders_more_than_rows() {
        let app = simple_app(&["/a", "/b", "/c"]);
        assert_eq!(render(&app, 2, 40, NOW).len(), 2);
        assert!(render(&app, 0, 40, NOW).is_empty());
    }

    #[test]
    fn non_ready_statuses_render_single_line() {
        let mut app = App::new();
        app.apply_list_result(Some(1), b"", b"cogitator is not running");
        assert_eq!(
            plain_lines(&render(&app, 2, 80, NOW)),
            ["cogitator is not running — start it inside zellij"]
        );

        app.set_permission_denied();
        assert_eq!(
            plain_lines(&render(&app, 2, 80, NOW)),
            ["permission denied — grant the plugin permissions"]
        );

        app.apply_list_result(Some(3), b"", b"kaput");
        assert_eq!(plain_lines(&render(&app, 2, 4, NOW)), ["kap…"]);
    }

    #[test]
    fn empty_ready_list_says_no_agents_in_flight() {
        let app = simple_app(&[]);
        assert_eq!(
            plain_lines(&render(&app, 2, 80, NOW)),
            ["no agents in flight"]
        );
    }

    #[test]
    fn notice_replaces_the_footer() {
        let mut app = simple_app(&["/a", "/b", "/c"]);
        app.set_notice("focus failed");
        assert_eq!(
            plain_lines(&render(&app, 4, 40, NOW)),
            ["Agents · 3", "· /a", "· /b", "focus failed"]
        );
        assert_eq!(
            plain_lines(&render(&app, 6, 40, NOW)),
            ["Agents · 3", "· /a", "· /b", "· /c", "", "focus failed"]
        );
    }

    #[test]
    fn notice_alone_fits_a_single_row() {
        let mut app = simple_app(&["/a"]);
        app.set_notice("focus failed");
        assert_eq!(plain_lines(&render(&app, 1, 5, NOW)), ["focu…"]);
    }

    #[test]
    fn relative_time_buckets() {
        assert_eq!(relative_time(-5), "now");
        assert_eq!(relative_time(59), "now");
        assert_eq!(relative_time(300), "5m");
        assert_eq!(relative_time(3 * 3600 + 10), "3h");
        assert_eq!(relative_time(361 * 3600), "361h");
    }
}
