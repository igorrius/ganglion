//! The reflex's rules: the one place, with `manifest.yaml`, that changes
//! when the module is made into another one.
//!
//! This template's own rules are a neutral example over one listened cell,
//! `producer.value`:
//!
//! - When the value crosses `threshold` downwards it says so, once, until
//!   the value rises back to the threshold or above.
//! - From the hourly cron, when the cell has not changed for more than
//!   `stale_after` minutes it says "no data for <n> min", once, until the
//!   cell changes again.
//!
//! `react` is pure: it reads what it is given, keeps what it must remember
//! in `State` and returns the messages. It makes no host call, so every rule
//! is a plain test. Message text is made here from a template and numbers;
//! text that came from outside never goes into a message.

use serde::{Deserialize, Serialize};
use serde_json::Value;

/// The cells the module listens to; the same as `cells.read` in the manifest.
pub const LISTENED: &[&str] = &["producer.value"];

const THRESHOLD_DEFAULT: f64 = 30.0;
const STALE_AFTER_DEFAULT: i64 = 60;

/// What woke the module.
pub struct Input {
    /// `cell` or `cron`.
    pub trigger: String,
    /// For a `cell` trigger, the cells that changed.
    pub cells: Vec<String>,
}

/// A listened cell as it is now.
pub struct Reading {
    pub id: String,
    pub value: Value,
    /// When it last changed, in ms.
    pub at_ms: i64,
}

/// What the module remembers between runs (JSON under the key `state`) and
/// the settings of this run (`lib.rs` fills them from the config).
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct State {
    /// The value is below the threshold and the owner was told.
    pub below: bool,
    /// The cell is stale and the owner was told.
    pub stale: bool,
    #[serde(skip)]
    pub threshold: f64,
    /// Minutes.
    #[serde(skip)]
    pub stale_after: i64,
}

impl Default for State {
    fn default() -> Self {
        State {
            below: false,
            stale: false,
            threshold: THRESHOLD_DEFAULT,
            stale_after: STALE_AFTER_DEFAULT,
        }
    }
}

/// Decides what to say about the readings; keeps what it must remember in
/// `state`.
pub fn react(input: &Input, readings: &[Reading], state: &mut State, now_ms: i64) -> Vec<String> {
    let Some(r) = readings.iter().find(|r| r.id == LISTENED[0]) else {
        return Vec::new();
    };
    match input.trigger.as_str() {
        "cell" if input.cells.contains(&r.id) => on_cell(r, state),
        "cron" => on_cron(r, state, now_ms),
        _ => Vec::new(),
    }
}

/// A new value: the cell is alive again; a crossing downwards is said once.
fn on_cell(r: &Reading, state: &mut State) -> Vec<String> {
    let Some(v) = r.value.as_f64() else {
        return Vec::new();
    };
    state.stale = false;
    if v >= state.threshold {
        state.below = false;
        return Vec::new();
    }
    if state.below {
        return Vec::new();
    }
    state.below = true;
    vec![format!(
        "{} fell below {}: it is {}",
        r.id, state.threshold, v
    )]
}

/// The hourly check: a cell silent for more than `stale_after` minutes is
/// said once.
fn on_cron(r: &Reading, state: &mut State, now_ms: i64) -> Vec<String> {
    let age_min = (now_ms - r.at_ms) / 60_000;
    if age_min <= state.stale_after {
        state.stale = false;
        return Vec::new();
    }
    if state.stale {
        return Vec::new();
    }
    state.stale = true;
    vec![format!("no data for {age_min} min")]
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    const NOW_MS: i64 = 1_793_000_000_000;
    const MIN: i64 = 60_000;

    fn cell_input() -> Input {
        Input {
            trigger: "cell".into(),
            cells: vec!["producer.value".into()],
        }
    }

    fn cron_input() -> Input {
        Input {
            trigger: "cron".into(),
            cells: vec![],
        }
    }

    fn reading(value: Value, age_min: i64) -> Vec<Reading> {
        vec![Reading {
            id: "producer.value".into(),
            value,
            at_ms: NOW_MS - age_min * MIN,
        }]
    }

    fn on_cell(value: Value, state: &mut State) -> Vec<String> {
        react(&cell_input(), &reading(value, 0), state, NOW_MS)
    }

    #[test]
    fn a_value_below_the_threshold_is_said_once() {
        let mut s = State::default();
        assert_eq!(
            on_cell(json!(12), &mut s),
            vec!["producer.value fell below 30: it is 12".to_string()]
        );
        assert!(on_cell(json!(11), &mut s).is_empty());
        assert!(s.below);
    }

    #[test]
    fn a_value_at_the_threshold_or_above_says_nothing() {
        let mut s = State::default();
        assert!(on_cell(json!(30), &mut s).is_empty());
        assert!(on_cell(json!(75.5), &mut s).is_empty());
        assert!(!s.below);
    }

    #[test]
    fn it_says_again_after_the_value_rose() {
        let mut s = State::default();
        assert_eq!(on_cell(json!(10), &mut s).len(), 1);
        assert!(on_cell(json!(40), &mut s).is_empty());
        assert!(!s.below);
        assert_eq!(on_cell(json!(9.5), &mut s).len(), 1);
    }

    #[test]
    fn the_threshold_is_the_configured_one() {
        let mut s = State {
            threshold: 50.0,
            ..State::default()
        };
        assert_eq!(
            on_cell(json!(40), &mut s),
            vec!["producer.value fell below 50: it is 40".to_string()]
        );
    }

    #[test]
    fn a_value_that_is_not_a_number_says_nothing_and_changes_nothing() {
        let mut s = State::default();
        for v in [json!(null), json!("low"), json!(true), json!({"a": 1})] {
            assert!(on_cell(v, &mut s).is_empty());
        }
        assert_eq!(s, State::default());
    }

    #[test]
    fn a_cell_trigger_without_the_cell_says_nothing() {
        let mut s = State::default();
        assert!(react(&cell_input(), &[], &mut s, NOW_MS).is_empty());
    }

    #[test]
    fn an_old_cell_is_said_stale_once_by_the_cron() {
        let mut s = State::default();
        let r = reading(json!(50), 61);
        assert_eq!(
            react(&cron_input(), &r, &mut s, NOW_MS),
            vec!["no data for 61 min".to_string()]
        );
        assert!(react(&cron_input(), &r, &mut s, NOW_MS + 60 * MIN).is_empty());
        assert!(s.stale);
    }

    #[test]
    fn a_cell_exactly_at_the_limit_is_not_stale() {
        let mut s = State::default();
        let r = reading(json!(50), 60);
        assert!(react(&cron_input(), &r, &mut s, NOW_MS).is_empty());
    }

    #[test]
    fn staleness_is_said_again_after_the_cell_changed() {
        let mut s = State::default();
        assert_eq!(
            react(&cron_input(), &reading(json!(50), 90), &mut s, NOW_MS).len(),
            1
        );
        assert!(react(&cron_input(), &reading(json!(50), 5), &mut s, NOW_MS).is_empty());
        assert!(!s.stale);
        assert_eq!(
            react(&cron_input(), &reading(json!(50), 70), &mut s, NOW_MS).len(),
            1
        );
    }

    #[test]
    fn stale_after_is_the_configured_one() {
        let mut s = State {
            stale_after: 10,
            ..State::default()
        };
        assert_eq!(
            react(&cron_input(), &reading(json!(50), 11), &mut s, NOW_MS),
            vec!["no data for 11 min".to_string()]
        );
    }

    #[test]
    fn the_cron_without_the_cell_says_nothing() {
        let mut s = State::default();
        assert!(react(&cron_input(), &[], &mut s, NOW_MS).is_empty());
    }

    #[test]
    fn the_cron_does_not_judge_the_threshold() {
        let mut s = State::default();
        assert!(react(&cron_input(), &reading(json!(1), 0), &mut s, NOW_MS).is_empty());
        assert!(!s.below);
    }

    #[test]
    fn a_new_value_clears_staleness_even_when_it_is_not_below() {
        let mut s = State {
            stale: true,
            ..State::default()
        };
        assert!(on_cell(json!(80), &mut s).is_empty());
        assert!(!s.stale);
    }

    #[test]
    fn the_state_keeps_the_episodes_and_not_the_settings() {
        let s = State {
            below: true,
            stale: true,
            threshold: 5.0,
            stale_after: 3,
        };
        let back: State = serde_json::from_str(&serde_json::to_string(&s).unwrap()).unwrap();
        assert_eq!(
            back,
            State {
                below: true,
                stale: true,
                ..State::default()
            }
        );
        let empty: State = serde_json::from_str("{}").unwrap();
        assert_eq!(empty, State::default());
    }
}
