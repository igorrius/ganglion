//! The reflex template: a module that reacts to cells and writes to the
//! owner (spec R-RUN-15..17). The agent copies it (`module.import`), changes
//! `manifest.yaml` and `src/logic.rs` only, builds it and installs it. The
//! ABI glue is compiled only for wasm32.

pub mod host;
pub mod logic;

#[cfg(test)]
mod fake;

#[cfg(target_arch = "wasm32")]
mod guest;

use host::{Host, Notify};
use logic::{Input, Reading, State};
use serde_json::{Value, json};

/// The state key; the value is `logic::State` as JSON.
const KEY_STATE: &str = "state";

/// Handles one call: reads the input, gets every listened cell, lets
/// `logic::react` decide and sends what it says. The answer is a ganglion
/// Output as JSON.
pub fn run(h: &dyn Host, input: &[u8]) -> Vec<u8> {
    let out = match parse(input) {
        Some(i) if i.trigger == "init" || i.trigger == "health" => ok(),
        Some(i) if i.trigger == "cell" || i.trigger == "cron" => react(h, &i),
        Some(_) => failure("unknown trigger"),
        None => failure("the input is not a ganglion Input"),
    };
    out.to_string().into_bytes()
}

fn react(h: &dyn Host, input: &Input) -> Value {
    let now_ms = h.now_unix() * 1000;
    let mut state = load(h);
    let msgs = logic::react(input, &readings(h), &mut state, now_ms);
    h.state_put(
        KEY_STATE,
        &serde_json::to_string(&state).unwrap_or_default(),
    );
    for msg in &msgs {
        match h.notify(msg) {
            Ok(()) => {}
            Err(Notify::Limit) => {
                h.log("notify: today's limit is used up");
                break;
            }
            Err(e) => {
                h.log(&format!("notify: {e:?}"));
                break;
            }
        }
    }
    ok()
}

/// Reads the trigger and, for a cell trigger, the cells that changed.
fn parse(input: &[u8]) -> Option<Input> {
    let v: Value = serde_json::from_slice(input).ok()?;
    let trigger = v.get("trigger")?.as_str()?.to_string();
    let cells = v
        .pointer("/payload/cells")
        .and_then(Value::as_array)
        .map(|a| {
            a.iter()
                .filter_map(|c| c.as_str().map(str::to_string))
                .collect()
        })
        .unwrap_or_default();
    Some(Input { trigger, cells })
}

/// The listened cells that hold a value.
fn readings(h: &dyn Host) -> Vec<Reading> {
    logic::LISTENED
        .iter()
        .filter_map(|id| {
            let (value, at_ms) = h.cell_get(id)?;
            (!value.is_null()).then(|| Reading {
                id: id.to_string(),
                value,
                at_ms,
            })
        })
        .collect()
}

/// The stored state with this run's settings; a missing or unreadable
/// state starts afresh, a setting that is not a number keeps its default.
fn load(h: &dyn Host) -> State {
    let mut state: State = h
        .state_get(KEY_STATE)
        .and_then(|s| serde_json::from_str(&s).ok())
        .unwrap_or_default();
    if let Some(v) = h.config("threshold").and_then(|s| s.trim().parse().ok()) {
        state.threshold = v;
    }
    if let Some(v) = h
        .config("stale_after")
        .and_then(|s| s.trim().parse::<f64>().ok())
    {
        state.stale_after = v as i64;
    }
    state
}

fn ok() -> Value {
    json!({ "ok": true })
}

fn failure(msg: &str) -> Value {
    json!({ "ok": false, "kind": "module.exception", "error": msg })
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::fake::{Fake, fake};
    use crate::host::Notify;
    use serde_json::json;

    const CELL: &str =
        r#"{"trigger":"cell","payload":{"cells":["producer.value"],"producer":"local/producer"}}"#;
    const CRON: &str = r#"{"trigger":"cron"}"#;

    fn out(f: &Fake, input: &str) -> Value {
        serde_json::from_slice(&run(f, input.as_bytes())).unwrap()
    }

    fn kind(out: &Value) -> &str {
        out["kind"].as_str().unwrap_or_default()
    }

    #[test]
    fn a_value_below_the_threshold_is_sent_once() {
        let f = fake();
        f.produce(json!(12), 0);
        assert_eq!(out(&f, CELL), json!({"ok": true}));
        assert_eq!(*f.sent.borrow(), ["producer.value fell below 30: it is 12"]);
        assert_eq!(out(&f, CELL), json!({"ok": true}));
        assert_eq!(f.sent.borrow().len(), 1);
    }

    #[test]
    fn the_config_sets_the_threshold_and_the_staleness() {
        let f = fake();
        f.set_config("threshold", "50");
        f.set_config("stale_after", "10");
        f.produce(json!(40), 0);
        out(&f, CELL);
        assert_eq!(*f.sent.borrow(), ["producer.value fell below 50: it is 40"]);
        f.produce(json!(40), 11);
        out(&f, CRON);
        assert_eq!(f.sent.borrow()[1], "no data for 11 min");
    }

    #[test]
    fn a_config_that_is_not_a_number_falls_back_to_the_default() {
        let f = fake();
        f.set_config("threshold", "low");
        f.produce(json!(29), 0);
        out(&f, CELL);
        assert_eq!(f.sent.borrow().len(), 1);
    }

    #[test]
    fn the_state_lives_across_runs_under_the_key_state() {
        let f = fake();
        f.produce(json!(12), 0);
        out(&f, CELL);
        let stored: Value = serde_json::from_str(&f.state.borrow()["state"]).unwrap();
        assert_eq!(stored, json!({"below": true, "stale": false}));
        f.produce(json!(80), 0);
        out(&f, CELL);
        f.produce(json!(5), 0);
        out(&f, CELL);
        assert_eq!(f.sent.borrow().len(), 2);
    }

    #[test]
    fn a_state_that_cannot_be_read_starts_afresh() {
        let f = fake();
        f.state_put("state", "{broken");
        f.produce(json!(12), 0);
        out(&f, CELL);
        assert_eq!(f.sent.borrow().len(), 1);
    }

    #[test]
    fn the_cron_says_a_stale_cell_once() {
        let f = fake();
        f.produce(json!(50), 90);
        assert_eq!(out(&f, CRON), json!({"ok": true}));
        out(&f, CRON);
        assert_eq!(*f.sent.borrow(), ["no data for 90 min"]);
    }

    #[test]
    fn a_cell_that_was_never_reported_says_nothing() {
        let f = fake();
        out(&f, CELL);
        out(&f, CRON);
        f.produce(json!(null), 0);
        out(&f, CELL);
        out(&f, CRON);
        assert!(f.sent.borrow().is_empty());
    }

    #[test]
    fn a_refused_message_does_not_fail_the_run() {
        for refusal in [Notify::Limit, Notify::Denied, Notify::NotHere] {
            let f = fake();
            f.verdict.set(Err(refusal));
            f.produce(json!(12), 0);
            assert_eq!(out(&f, CELL), json!({"ok": true}), "{refusal:?}");
            assert!(f.sent.borrow().is_empty());
        }
    }

    #[test]
    fn init_and_health_answer_ok_and_send_nothing() {
        let f = fake();
        f.produce(json!(12), 0);
        for trigger in ["init", "health"] {
            let input = format!(r#"{{"trigger":"{trigger}"}}"#);
            assert_eq!(out(&f, &input), json!({"ok": true}), "{trigger}");
        }
        assert!(f.sent.borrow().is_empty());
        assert!(f.state.borrow().is_empty());
    }

    #[test]
    fn another_trigger_or_a_broken_input_is_an_exception() {
        let f = fake();
        for input in [r#"{"trigger":"tool:x"}"#, "{}", "not json"] {
            let o = out(&f, input);
            assert_eq!(
                (o["ok"].as_bool(), kind(&o)),
                (Some(false), "module.exception"),
                "{input}"
            );
        }
    }
}
