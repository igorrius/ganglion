//! The reflex template: a module that reacts to cells and writes to the
//! owner (spec R-RUN-15..17). The agent copies it (`module.import`), changes
//! `manifest.yaml` and `src/logic.rs` only, builds it and installs it. This
//! file is generic glue: it names no rule and no setting, so changing the
//! rules never touches it. The ABI glue is compiled only for wasm32.

pub mod host;
pub mod logic;

#[cfg(test)]
mod fake;

#[cfg(target_arch = "wasm32")]
mod guest;

use host::Host;
use logic::{Input, Reading, Settings, State};
use serde::{Serialize, de::DeserializeOwned};
use serde_json::{Value, json};

/// The state key; the value is `logic::State` as JSON.
const KEY_STATE: &str = "state";

/// Handles one call: reads the input and the listened cells, lets
/// `logic::react` decide and sends what it says. The answer is a ganglion
/// Output as JSON.
pub fn run(h: &dyn Host, input: &[u8]) -> Vec<u8> {
    let out = match parse(input) {
        Some(i) if i.trigger == "init" || i.trigger == "health" => ok(),
        Some(i) => {
            let settings = Settings::from_config(&|k| h.config(k));
            let react = |i: &Input, r: &[Reading], s: &mut State, now_ms: i64| {
                logic::react(i, r, &settings, s, now_ms)
            };
            execute(h, &i, react)
        }
        None => failure("the input is not a ganglion Input"),
    };
    out.to_string().into_bytes()
}

/// One run: load the state, call `react`, send its messages one by one and
/// store the new state only when every message went out. A refusal (limit,
/// not granted, not in this run) keeps the previous state and attempts no
/// more messages, so the same rule fires again on a later run.
fn execute<S: Default + Serialize + DeserializeOwned>(
    h: &dyn Host,
    input: &Input,
    react: impl Fn(&Input, &[Reading], &mut S, i64) -> Vec<String>,
) -> Value {
    let mut state: S = h
        .state_get(KEY_STATE)
        .and_then(|s| serde_json::from_str(&s).ok())
        .unwrap_or_default();
    let msgs = react(input, &readings(h), &mut state, h.now_unix() * 1000);
    let stored = match serde_json::to_string(&state) {
        Ok(s) => s,
        Err(e) => return failure(&format!("the state cannot be stored: {e}")),
    };
    for msg in &msgs {
        if let Err(e) = h.notify(msg) {
            h.log(&format!("notify refused: {e:?}"));
            return ok();
        }
    }
    h.state_put(KEY_STATE, &stored);
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
    use serde::Deserialize;

    /// A state of the glue's own tests, not the example's.
    #[derive(Default, Serialize, Deserialize, Debug, PartialEq)]
    struct Count {
        n: u32,
    }

    fn input() -> Input {
        Input {
            trigger: "cell".into(),
            cells: vec![],
        }
    }

    /// A react that counts the run and says `msgs`.
    fn says(
        msgs: &'static [&'static str],
    ) -> impl Fn(&Input, &[Reading], &mut Count, i64) -> Vec<String> {
        move |_, _, s, _| {
            s.n += 1;
            msgs.iter().map(|m| m.to_string()).collect()
        }
    }

    fn stored(f: &Fake) -> Option<String> {
        f.state.borrow().get("state").cloned()
    }

    fn is_ok(v: &Value) -> bool {
        v["ok"] == json!(true)
    }

    #[test]
    fn the_state_is_stored_after_every_message_went_out() {
        let f = fake();
        assert!(is_ok(&execute(&f, &input(), says(&["a", "b"]))));
        assert_eq!(*f.sent.borrow(), ["a", "b"]);
        assert_eq!(stored(&f).as_deref(), Some(r#"{"n":1}"#));
        execute(&f, &input(), says(&[]));
        assert_eq!(
            stored(&f).as_deref(),
            Some(r#"{"n":2}"#),
            "the state is loaded"
        );
    }

    #[test]
    fn a_refused_message_keeps_the_previous_state_and_a_later_run_sends_it_again() {
        for refusal in [Notify::Limit, Notify::Denied, Notify::NotHere] {
            let f = fake();
            f.state_put("state", r#"{"n":7}"#);
            f.verdict.set(Err(refusal));
            assert!(
                is_ok(&execute(&f, &input(), says(&["a", "b"]))),
                "{refusal:?}"
            );
            assert!(f.sent.borrow().is_empty());
            assert_eq!(stored(&f).as_deref(), Some(r#"{"n":7}"#), "{refusal:?}");
            f.verdict.set(Ok(()));
            execute(&f, &input(), says(&["a", "b"]));
            assert_eq!(*f.sent.borrow(), ["a", "b"], "{refusal:?}");
            assert_eq!(stored(&f).as_deref(), Some(r#"{"n":8}"#));
        }
    }

    #[test]
    fn messages_after_the_first_refusal_are_not_attempted() {
        let f = fake();
        f.verdict.set(Err(Notify::Limit));
        execute(&f, &input(), says(&["a", "b", "c"]));
        assert_eq!(f.attempts.get(), 1);
    }

    #[test]
    fn a_state_that_cannot_be_read_starts_afresh() {
        let f = fake();
        f.state_put("state", "{broken");
        execute(&f, &input(), says(&[]));
        assert_eq!(stored(&f).as_deref(), Some(r#"{"n":1}"#));
    }

    #[test]
    fn init_and_health_answer_ok_and_touch_nothing() {
        let f = fake();
        for trigger in ["init", "health"] {
            let input = format!(r#"{{"trigger":"{trigger}"}}"#);
            let out: Value = serde_json::from_slice(&run(&f, input.as_bytes())).unwrap();
            assert_eq!(out, json!({"ok": true}), "{trigger}");
        }
        assert!(f.sent.borrow().is_empty());
        assert_eq!(f.attempts.get(), 0);
        assert!(f.state.borrow().is_empty());
    }

    #[test]
    fn a_broken_input_is_an_exception() {
        let f = fake();
        for input in ["{}", "not json", r#"{"trigger":7}"#] {
            let o: Value = serde_json::from_slice(&run(&f, input.as_bytes())).unwrap();
            assert_eq!(
                (o["ok"].as_bool(), o["kind"].as_str()),
                (Some(false), Some("module.exception")),
                "{input}"
            );
        }
    }

    #[test]
    fn the_run_reads_every_listened_cell_that_holds_a_value() {
        let f = fake();
        let id = logic::LISTENED[0];
        let seen = |f: &Fake| {
            let got = std::cell::RefCell::new(vec![]);
            execute::<Count>(f, &input(), |_, r, _, now| {
                got.borrow_mut()
                    .extend(r.iter().map(|r| (r.id.clone(), r.at_ms, now)));
                vec![]
            });
            got.into_inner()
        };
        assert!(seen(&f).is_empty(), "a cell that was never reported");
        f.cells.borrow_mut().insert(id.into(), (json!(null), 5));
        assert!(seen(&f).is_empty(), "a cell holding null");
        f.cells.borrow_mut().insert(id.into(), (json!(1), 5_000));
        assert_eq!(seen(&f), [(id.to_string(), 5_000, crate::fake::NOW * 1000)]);
    }

    #[test]
    fn the_input_carries_the_trigger_and_the_changed_cells() {
        let i = parse(br#"{"trigger":"cell","payload":{"cells":["a.b","c.d"],"producer":"x"}}"#)
            .unwrap();
        assert_eq!(
            (i.trigger.as_str(), i.cells),
            ("cell", vec!["a.b".to_string(), "c.d".to_string()])
        );
        let i = parse(br#"{"trigger":"cron"}"#).unwrap();
        assert_eq!((i.trigger.as_str(), i.cells.len()), ("cron", 0));
    }
}
