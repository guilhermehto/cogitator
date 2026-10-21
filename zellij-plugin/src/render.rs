use crate::app::{App, Status};
use crate::feed::Agent;

// Glyphs, colours and widths mirror the TUI's Agents tab (internal/ui) so
// both surfaces read the same.
const GLYPH_ACTIVE: &str = "\u{f0765}";
const GLYPH_INACTIVE: &str = "\u{f0766}";
const GLYPH_STOPPED: &str = "\u{f0667}";
const GLYPH_QUESTION: &str = "\u{f0625}";
const GLYPH_PERMISSION: &str = "\u{f033e}";
const GLYPH_ERROR: &str = "\u{f0026}";
const GLYPH_FINISHED: &str = "\u{f012c}";
const GLYPH_MISSING: &str = "\u{f0e7a}";
const GLYPH_HERE: &str = "▌";

const HARNESS_WIDTH: usize = 11;
const DETAIL_MIN_WIDTH: usize = 80;
const BAND_BG: u8 = 237;
/// An agent row is its name, then its tab label and session title.
const ROW_LINES: usize = 2;

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
const REPO: Style = Style::fg(103);
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
const HERE: Style = Style::fg(111);

/// Section titles, indexed by Agent::section, as the TUI's agentSections.
const SECTIONS: [(&str, Style); 4] = [
    ("Needs you", PERMISSION),
    ("Working", ACTIVE.bold()),
    ("Done", FINISHED),
    ("Idle", HEADER),
];

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
        Status::Outdated => span(
            "cogitator plugin outdated, run cogitator zellij install",
            HINT,
        ),
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
    // The band shows where keys act while the plugin has focus, and where the
    // user is otherwise; App keeps the cursor on that agent while unfocused.
    let band = if app.focused() { selected } else { here };
    // App keeps the list in section order, so each section is one run.
    let mut lines = Vec::new();
    let mut selected_line = 0;
    for (section, (title, style)) in SECTIONS.into_iter().enumerate() {
        let start = agents.partition_point(|a| a.section() < section);
        let end = agents.partition_point(|a| a.section() <= section);
        let range = start..end;
        if range.is_empty() {
            continue;
        }

        if !lines.is_empty() {
            lines.push(String::new());
        }
        let header = vec![span(title, style), span(format!(" · {}", range.len()), DIM)];
        lines.push(paint(&truncate(header, cols), None));
        for i in range {
            if selected == Some(i) {
                selected_line = lines.len();
            }
            let mark = RowMark {
                band: band == Some(i),
                here: here == Some(i),
            };
            lines.extend(agent_row(&agents[i], mark, cols, now, app.home()));
        }
    }

    let offset = scroll_offset(app.scroll.get(), selected_line, rows, lines.len());
    app.scroll.set(offset);

    lines.into_iter().skip(offset).take(rows).collect()
}

// scroll_offset scrolls no further than it takes to show the selected row,
// which starts at selected_line, whole, or its first line when rows is short.
fn scroll_offset(current: usize, selected_line: usize, rows: usize, total: usize) -> usize {
    let mut offset = current.min(total.saturating_sub(rows));
    if selected_line < offset {
        offset = selected_line.saturating_sub(1);
    }
    let selected_end = selected_line + ROW_LINES;
    if selected_end > offset + rows {
        offset = (selected_end - rows).min(selected_line);
    }
    offset
}

struct RowMark {
    band: bool,
    here: bool,
}

// agent_row lays out an agent on ROW_LINES lines, as the TUI does: status and
// name with (when wide enough) harness and age on the right, then the tab
// label and muted session title under the name. A gutter bar marks the agent
// the user is sitting at on both lines, whichever row carries the band.
fn agent_row(
    agent: &Agent,
    mark: RowMark,
    cols: usize,
    now: i64,
    home: &str,
) -> [String; ROW_LINES] {
    let age = agent
        .last_activity_secs()
        .map(|ts| relative_time(now - ts))
        .unwrap_or_default();

    let status = status_cell(agent);
    let mut detail = vec![
        gutter(mark.here),
        span(" ".repeat(status.text.chars().count()), PLAIN),
    ];
    let mut left = vec![gutter(mark.here), status];
    left.extend(styled_target(agent, home));
    detail.extend(styled_detail(agent));
    let mut right = vec![span(age.clone(), DIM)];

    if cols >= DETAIL_MIN_WIDTH {
        let harness = vec![span(agent.harness.clone(), harness_style(&agent.harness))];
        right = pad(truncate(harness, HARNESS_WIDTH), HARNESS_WIDTH);
        right.push(span(format!(" {age:>4}"), DIM));
    }

    let gap = cols.saturating_sub(width(&right) + 2);
    let mut name = pad(truncate(left, gap), gap);
    name.push(span("  ", PLAIN));
    name.extend(right);

    let bg = mark.band.then_some(BAND_BG);
    [
        paint(&truncate(name, cols), bg),
        paint(&pad(truncate(detail, cols), cols), bg),
    ]
}

fn gutter(here: bool) -> Span {
    if here {
        span(GLYPH_HERE, HERE)
    } else {
        span(" ", PLAIN)
    }
}

fn status_cell(agent: &Agent) -> Span {
    let (glyph, style) = match agent.state.as_str() {
        "running" => attention_glyph(&agent.attention),
        "stopped" => (GLYPH_STOPPED, DIM),
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

// styled_target names what the agent works in, with the repo or workspace
// muted so the branch or session reads first.
fn styled_target(agent: &Agent, home: &str) -> Line {
    let lead = lead_style(agent);
    let repo = repo_style(agent);

    match agent.kind.as_str() {
        "workspace" => vec![
            span(agent.workspace.clone(), repo),
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
            span(base_name(&agent.repo), repo),
            span("·", DIM),
            span(agent.branch.clone(), lead),
        ],
    }
}

// styled_detail is the line under an agent's name: its tab label, then its
// session title muted unless the label already is the title.
fn styled_detail(agent: &Agent) -> Line {
    let mut line = Vec::new();
    if !agent.tab.is_empty() {
        line.push(span(agent.tab.clone(), lead_style(agent)));
    }
    if !agent.title.is_empty() && agent.title != agent.tab {
        if !line.is_empty() {
            line.push(span("  ", PLAIN));
        }
        line.push(span(agent.title.clone(), PATH));
    }
    line
}

// lead_style dims a stopped agent's own text.
fn lead_style(agent: &Agent) -> Style {
    if agent.state == "stopped" {
        DIM
    } else {
        PLAIN
    }
}

// repo_style is lead_style for the repo or workspace beside the branch or
// session, which is muted even while the agent runs.
fn repo_style(agent: &Agent) -> Style {
    if agent.state == "stopped" {
        DIM
    } else {
        REPO
    }
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
    if elapsed_secs < 48 * 3600 {
        return format!("{}h", elapsed_secs / 3600);
    }
    format!("{}d", elapsed_secs / (24 * 3600))
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

    fn tabbed_app(tabs: &[&str]) -> App {
        let items: Vec<String> = tabs
            .iter()
            .map(|t| format!(r#"{{"id":"/{t}","dir":"/{t}","tab":"{t}"}}"#))
            .collect();
        app_from(&format!(
            r#"{{"version":1,"agents":[{}]}}"#,
            items.join(",")
        ))
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
        render(app, 5, cols, NOW)[1].clone()
    }

    fn sits_at(app: &mut App, window: &str) {
        app.set_location(crate::app::Location {
            session: "s".into(),
            window: Some(window.into()),
        });
    }

    #[test]
    fn sections_run_needs_you_working_done_then_idle() {
        let app = app_from(
            r#"{"version":1,"agents":[
                {"id":"/x/done","dir":"/x/done","group":"done","ordered_at":"2025-12-31T23:00:00Z"},
                {"id":"/x/ask","dir":"/x/ask","group":"needs_you","ordered_at":"2025-12-31T23:30:00Z"},
                {"id":"/x/stopped","dir":"/x/stopped","group":"stopped","ordered_at":"2025-12-31T21:00:00Z"},
                {"id":"/x/idle","dir":"/x/idle","group":"idle","ordered_at":"2025-12-31T22:00:00Z"},
                {"id":"/x/busy","dir":"/x/busy","group":"working","ordered_at":"2025-12-31T23:10:00Z"}
            ]}"#,
        );
        let lines: Vec<String> = plain_lines(&render(&app, 20, 40, NOW))
            .iter()
            .map(|l| l.split("  ").next().unwrap_or_default().to_string())
            .collect();
        assert_eq!(
            lines[..17],
            [
                "Needs you · 1",
                " · /x/ask",
                "",
                "",
                "Working · 1",
                " · /x/busy",
                "",
                "",
                "Done · 1",
                " · /x/done",
                "",
                "",
                "Idle · 2",
                " · /x/stopped",
                "",
                " · /x/idle",
                ""
            ]
        );
    }

    #[test]
    fn empty_sections_are_omitted() {
        let app = simple_app(&["/a"]);
        assert_eq!(
            plain_lines(&render(&app, 4, 40, NOW))[..2],
            ["Idle · 1", " · /a"]
        );
    }

    #[test]
    fn worktree_row_shows_repo_folder_and_branch() {
        let app = one_agent(r#""kind":"worktree","repo":"/Users/me/.dotfiles","branch":"main""#);
        assert_eq!(
            plain_lines(&render(&app, 5, 40, NOW))[1],
            " · .dotfiles·main"
        );
    }

    #[test]
    fn workspace_row_shows_workspace_and_session() {
        let app = one_agent(r#""kind":"workspace","workspace":"w","session_name":"s""#);
        assert_eq!(plain_lines(&render(&app, 5, 40, NOW))[1], " · w/s");
    }

    #[test]
    fn stray_row_abbreviates_home_and_tags_stray() {
        let app = one_agent(r#""kind":"stray""#);
        assert_eq!(plain_lines(&render(&app, 5, 40, NOW))[1], " · ~/p stray");
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
        assert!(plain(&render(&app, 5, 40, NOW)[1]).starts_with(&format!(" {GLYPH_INACTIVE}")));
    }

    #[test]
    fn stopped_agents_are_dimmed() {
        let app = one_agent(r#""kind":"worktree","repo":"r","branch":"b","state":"stopped""#);
        let row = &row_colours(&app, 40);
        assert!(
            row.contains(&format!("38;5;240m{GLYPH_STOPPED} ")),
            "{row:?}"
        );
        assert!(row.contains("38;5;240mb"), "{row:?}");
        assert!(row.contains("38;5;240mr"), "{row:?}");
    }

    #[test]
    fn running_agents_mute_the_repo() {
        let app = one_agent(r#""kind":"worktree","repo":"r","branch":"b","state":"running""#);
        let row = &row_colours(&app, 40);
        assert!(row.contains("38;5;103mr"), "{row:?}");
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
    fn wide_row_adds_harness_column() {
        let app = one_agent(
            r#""kind":"worktree","repo":"r","branch":"b","harness":"omp","last_activity":"2025-12-31T20:00:00Z""#,
        );
        let row = &row_colours(&app, 90);
        assert!(plain(row).ends_with("omp           4h"), "{:?}", plain(row));
        assert!(row.contains("3;38;5;81momp"), "{row:?}");
    }

    #[test]
    fn tab_label_and_title_sit_under_the_name() {
        let app = one_agent(
            r#""kind":"worktree","repo":"r","branch":"b","tab":"LEX-123","title":"fix it","last_activity":"2025-12-31T23:55:00Z""#,
        );
        let lines = plain_lines(&render(&app, 5, 40, NOW));
        assert!(lines[1].starts_with(" · r·b "), "{:?}", lines[1]);
        assert!(lines[1].ends_with(" 5m"), "{:?}", lines[1]);
        assert_eq!(lines[2], "   LEX-123  fix it");
    }

    #[test]
    fn title_shows_alone_and_is_not_repeated_as_tab() {
        let untabbed = one_agent(r#""kind":"stray","title":"fix it""#);
        assert_eq!(plain_lines(&render(&untabbed, 5, 40, NOW))[2], "   fix it");

        let titled = one_agent(r#""kind":"stray","tab":"fix it","title":"fix it""#);
        let lines = plain_lines(&render(&titled, 5, 90, NOW));
        assert_eq!(lines.join("\n").matches("fix it").count(), 1, "{lines:?}");
    }

    fn two_tabs_at_second() -> App {
        let mut app = app_from(
            r#"{"version":1,"agents":[{"id":"/a","dir":"/a","window_id":"s:1"},{"id":"/b","dir":"/b","window_id":"s:2"}]}"#,
        );
        sits_at(&mut app, "s:2");
        app
    }

    fn banded(lines: &[String]) -> Vec<usize> {
        (0..lines.len())
            .filter(|&i| lines[i].contains("48;5;237"))
            .collect()
    }

    fn marked_here(lines: &[String]) -> Vec<usize> {
        (0..lines.len())
            .filter(|&i| plain(&lines[i]).starts_with(GLYPH_HERE))
            .collect()
    }

    #[test]
    fn unfocused_band_and_bar_mark_both_lines_of_the_row_the_user_sits_at() {
        let lines = render(&two_tabs_at_second(), 6, 30, NOW);
        assert_eq!(banded(&lines), [3, 4]);
        assert_eq!(marked_here(&lines), [3, 4]);
        for line in &lines[3..5] {
            assert_eq!(plain(line).chars().count(), 30, "{line:?}");
        }
    }

    #[test]
    fn focused_band_follows_the_cursor_and_the_bar_stays_put() {
        let mut app = two_tabs_at_second();
        app.set_focused(true);
        assert_eq!(banded(&render(&app, 6, 30, NOW)), [3, 4]);

        app.handle_key(Key::Up);
        let lines = render(&app, 6, 30, NOW);
        assert_eq!(banded(&lines), [1, 2]);
        assert_eq!(marked_here(&lines), [3, 4]);
    }

    #[test]
    fn unfocused_list_without_an_agent_here_has_no_band() {
        let app = simple_app(&["/a", "/b"]);
        assert!(banded(&render(&app, 6, 30, NOW)).is_empty());
    }

    #[test]
    fn unfocused_list_scrolls_to_the_row_the_user_sits_at() {
        let mut app = app_from(
            r#"{"version":1,"agents":[
                {"id":"/a","dir":"/a","window_id":"s:1"},
                {"id":"/b","dir":"/b","window_id":"s:2"},
                {"id":"/c","dir":"/c","window_id":"s:3"},
                {"id":"/d","dir":"/d","window_id":"s:4"}
            ]}"#,
        );
        sits_at(&mut app, "s:4");
        let lines = plain_lines(&render(&app, 4, 40, NOW));
        assert_eq!(
            lines[1..3],
            [format!("{GLYPH_HERE}· /d"), GLYPH_HERE.to_string()]
        );
    }

    #[test]
    fn long_names_truncate_with_ellipsis_counting_chars() {
        let app = one_agent(
            r#""kind":"workspace","workspace":"ñandú","session_name":"日本語日本語日本語""#,
        );
        let row = plain(&render(&app, 5, 16, NOW)[1]);
        assert_eq!(row.chars().count(), 16);
        assert!(row.starts_with(" · ñandú/日本語日…"), "{row:?}");
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
    fn scrolling_keeps_the_whole_selected_row_visible() {
        let mut app = tabbed_app(&["a", "b", "c", "d", "e"]);
        for _ in 0..4 {
            app.handle_key(Key::Down);
        }
        let lines = plain_lines(&render(&app, 4, 40, NOW));
        assert_eq!(lines[..3], ["   d", " · /e", "   e"]);

        for _ in 0..4 {
            app.handle_key(Key::Up);
        }
        let lines = plain_lines(&render(&app, 4, 40, NOW));
        assert_eq!(lines[..3], ["Idle · 5", " · /a", "   a"]);
    }

    #[test]
    fn single_line_view_keeps_the_selected_name() {
        let mut app = tabbed_app(&["a", "b", "c"]);
        app.handle_key(Key::Down);
        app.handle_key(Key::Down);
        assert_eq!(plain_lines(&render(&app, 1, 40, NOW)), [" · /c"]);
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
    fn newer_feed_renders_outdated_message() {
        let app = app_from(&format!(
            r#"{{"version":{},"agents":[{{"id":"/a","dir":"/a"}}]}}"#,
            crate::feed::VERSION + 1
        ));
        assert_eq!(
            plain_lines(&render(&app, 3, 80, NOW))[0],
            "cogitator plugin outdated, run cogitator zellij install"
        );
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
            ["Idle · 3", " · /a", "", "focus failed"]
        );
        assert_eq!(
            plain_lines(&render(&app, 6, 40, NOW)),
            ["Idle · 3", " · /a", "", " · /b", "", "focus failed"]
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
        assert_eq!(relative_time(47 * 3600), "47h");
        assert_eq!(relative_time(48 * 3600), "2d");
        assert_eq!(relative_time(361 * 3600), "15d");
    }
}
