//! The page's own part: the one place, with `manifest.yaml` and `assets/`,
//! that changes when the template is made into the page the owner asked for.
//!
//! The example page shows one number, `producer.value`: its current value
//! (read from the cell), the last day of it as a chart (kept here in `State`
//! from the `reading` events the module accepts) and the latest notices
//! (from the `notices` event, linked from another module's `<module>.notify`).
//!
//! Everything here is pure: the glue in `lib.rs` loads the state, calls these
//! functions and stores the state. Text that came from outside (a notice) is
//! kept as plain text, shortened and cleaned of control characters; every
//! answer escapes it again on the way out (`http.rs`), and the page puts it in
//! with `textContent`.

use serde::{Deserialize, Serialize};
use serde_json::{Value, json};

/// The cell the page shows; the same as `cells.read` in the manifest.
pub const LISTENED: &[&str] = &["producer.value"];

/// The ring buffers' sizes.
const MAX_NOTICES: usize = 20;
const MAX_NOTICE_CHARS: usize = 280;

const TITLE_DEFAULT: &str = "Readings";
const POINTS_DEFAULT: usize = 288;
const KEEP_HOURS_DEFAULT: i64 = 24;

/// A reading kept for the chart: seconds since the epoch and the value.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct Point {
    pub t: i64,
    pub v: f64,
}

/// A notice from another module.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct Notice {
    pub text: String,
    /// RFC 3339 as sent, or the time it arrived when it was not given.
    pub at: String,
}

/// What the page remembers between requests (JSON under the key `state`).
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct State {
    /// Oldest first.
    pub points: Vec<Point>,
    /// Newest first.
    pub notices: Vec<Notice>,
}

/// The module's settings: the manifest's `config`, parsed with defaults.
#[derive(Debug, Clone, PartialEq)]
pub struct Settings {
    /// The page's heading; it is put into the HTML, escaped.
    pub title: String,
    /// The most readings kept (the ring's size).
    pub max_points: usize,
    /// Readings older than this are dropped.
    pub keep_hours: i64,
}

impl Default for Settings {
    fn default() -> Self {
        Settings {
            title: TITLE_DEFAULT.into(),
            max_points: POINTS_DEFAULT,
            keep_hours: KEEP_HOURS_DEFAULT,
        }
    }
}

impl Settings {
    /// Reads the settings with `get`; one that is missing or not valid keeps
    /// its default.
    pub fn from_config(get: &dyn Fn(&str) -> Option<String>) -> Settings {
        let d = Settings::default();
        Settings {
            title: get("title")
                .map(|s| clean(&s, 80))
                .filter(|s| !s.is_empty())
                .unwrap_or(d.title),
            max_points: get("max_points")
                .and_then(|s| s.trim().parse::<usize>().ok())
                .filter(|n| (1..=2000).contains(n))
                .unwrap_or(d.max_points),
            keep_hours: get("keep_hours")
                .and_then(|s| s.trim().parse::<i64>().ok())
                .filter(|n| (1..=168).contains(n))
                .unwrap_or(d.keep_hours),
        }
    }
}

/// A reading event reached the `reading` port: `{"value": <number>}`. It
/// joins the ring; the ring drops what is too old or over its size.
pub fn on_reading(event: &Value, settings: &Settings, state: &mut State, now_s: i64) {
    let Some(v) = event.get("value").and_then(Value::as_f64) else {
        return;
    };
    if !v.is_finite() {
        return;
    }
    state.points.push(Point { t: now_s, v });
    let oldest = now_s - settings.keep_hours * 3600;
    state.points.retain(|p| p.t >= oldest);
    let over = state.points.len().saturating_sub(settings.max_points);
    state.points.drain(..over);
}

/// A notice reached the `notices` port: `{"text": <string>, "at": <string>}`
/// (the shape of `<module>.notify`). The newest is first; the ring keeps the
/// last 20.
pub fn on_notice(event: &Value, state: &mut State, now_s: i64) {
    let Some(text) = event.get("text").and_then(Value::as_str) else {
        return;
    };
    let text = clean(text, MAX_NOTICE_CHARS);
    if text.is_empty() {
        return;
    }
    let at = event
        .get("at")
        .and_then(Value::as_str)
        .map(|s| clean(s, 40))
        .filter(|s| !s.is_empty())
        .unwrap_or_else(|| now_s.to_string());
    state.notices.insert(0, Notice { text, at });
    state.notices.truncate(MAX_NOTICES);
}

/// Plain text of at most `max` characters: control characters (newlines
/// included) become spaces, runs of spaces one, the ends are trimmed.
fn clean(s: &str, max: usize) -> String {
    let spaced: String = s
        .chars()
        .map(|c| if c.is_control() { ' ' } else { c })
        .collect();
    let joined = spaced.split_whitespace().collect::<Vec<_>>().join(" ");
    joined.chars().take(max).collect()
}

/// What `/api/data` answers: the title, the current value of the cell and when
/// it changed (seconds), the points of the ring as `[t, v]` pairs and the
/// notices. The page polls this.
pub fn snapshot(
    settings: &Settings,
    state: &State,
    current: Option<(Value, i64)>,
    now_s: i64,
) -> Value {
    let current = current.map(|(value, at_ms)| json!({ "value": value, "at": at_ms / 1000 }));
    let points: Vec<Value> = state.points.iter().map(|p| json!([p.t, p.v])).collect();
    json!({
        "title": settings.title,
        "now": now_s,
        "current": current,
        "points": points,
        "notices": state.notices,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    const NOW: i64 = 1_793_000_000;

    fn reading(v: f64) -> Value {
        json!({ "value": v })
    }

    #[test]
    fn a_reading_joins_the_ring_and_a_bad_one_is_ignored() {
        let (s, mut st) = (Settings::default(), State::default());
        on_reading(&reading(12.5), &s, &mut st, NOW);
        on_reading(&json!({"value": "x"}), &s, &mut st, NOW + 1);
        on_reading(&json!({}), &s, &mut st, NOW + 2);
        assert_eq!(st.points, vec![Point { t: NOW, v: 12.5 }]);
    }

    #[test]
    fn the_ring_keeps_its_size_and_drops_what_is_too_old() {
        let s = Settings {
            max_points: 3,
            keep_hours: 1,
            ..Settings::default()
        };
        let mut st = State::default();
        on_reading(&reading(1.0), &s, &mut st, NOW);
        for i in 1..=4 {
            on_reading(&reading(i as f64 + 1.0), &s, &mut st, NOW + i * 60);
        }
        let v: Vec<f64> = st.points.iter().map(|p| p.v).collect();
        assert_eq!(v, vec![3.0, 4.0, 5.0], "the oldest went first");
        on_reading(&reading(9.0), &s, &mut st, NOW + 7200);
        assert_eq!(st.points.len(), 1, "an hour is gone");
    }

    #[test]
    fn notices_are_newest_first_cleaned_and_capped_at_twenty() {
        let mut st = State::default();
        on_notice(
            &json!({"text": "first\nline", "at": "2026-11-23T10:00:00Z"}),
            &mut st,
            NOW,
        );
        assert_eq!(st.notices[0].text, "first line");
        for i in 0..25 {
            on_notice(&json!({"text": format!("n{i}"), "at": "t"}), &mut st, NOW);
        }
        assert_eq!(st.notices.len(), 20);
        assert_eq!(st.notices[0].text, "n24");
        on_notice(&json!({"text": "   "}), &mut st, NOW);
        on_notice(&json!({"at": "t"}), &mut st, NOW);
        assert_eq!(st.notices[0].text, "n24", "an empty notice is not kept");
    }

    #[test]
    fn a_long_notice_is_shortened_and_a_missing_time_is_now() {
        let mut st = State::default();
        on_notice(&json!({"text": "x".repeat(1000)}), &mut st, NOW);
        assert_eq!(st.notices[0].text.chars().count(), 280);
        assert_eq!(st.notices[0].at, NOW.to_string());
    }

    #[test]
    fn settings_keep_their_defaults_when_a_value_is_not_valid() {
        let get = |k: &str| match k {
            "title" => Some("  ".to_string()),
            "max_points" => Some("0".to_string()),
            "keep_hours" => Some("many".to_string()),
            _ => None,
        };
        assert_eq!(Settings::from_config(&get), Settings::default());
        let get = |k: &str| match k {
            "title" => Some("Battery".to_string()),
            "max_points" => Some("10".to_string()),
            "keep_hours" => Some("6".to_string()),
            _ => None,
        };
        let s = Settings::from_config(&get);
        assert_eq!(
            (s.title.as_str(), s.max_points, s.keep_hours),
            ("Battery", 10, 6)
        );
    }

    #[test]
    fn the_snapshot_has_the_current_value_the_points_and_the_notices() {
        let (s, mut st) = (Settings::default(), State::default());
        on_reading(&reading(7.0), &s, &mut st, NOW);
        on_notice(&json!({"text": "low", "at": "t"}), &mut st, NOW);
        let v = snapshot(&s, &st, Some((json!(7), NOW * 1000)), NOW + 5);
        assert_eq!(v["title"], "Readings");
        assert_eq!(v["current"]["value"], 7);
        assert_eq!(v["current"]["at"], NOW);
        assert_eq!(v["points"], json!([[NOW, 7.0]]));
        assert_eq!(v["notices"][0]["text"], "low");
        assert!(snapshot(&s, &st, None, NOW)["current"].is_null());
    }
}
