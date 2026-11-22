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
        Some(i) if i.trigger.starts_with("event:") => run_event(h, &i, input),
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

/// An event that reached an `accepts` port: the input's payload is
/// `{"from": "<module>.<port>", "event": <payload>}` and the trigger is
/// `event:<port>`. It is handled like a cell run: `logic::on_event` says what
/// to send, and the state is stored when every message went out.
fn run_event(h: &dyn Host, i: &Input, input: &[u8]) -> Value {
    let v: Value = serde_json::from_slice(input).unwrap_or(Value::Null);
    let port = i.trigger.trim_start_matches("event:").to_string();
    let from = v
        .pointer("/payload/from")
        .and_then(Value::as_str)
        .unwrap_or_default()
        .to_string();
    let event = v.pointer("/payload/event").cloned().unwrap_or(Value::Null);
    let settings = Settings::from_config(&|k| h.config(k));
    execute(h, i, |_, _, s: &mut State, now_ms| {
        logic::on_event(&port, &from, &event, &settings, s, now_ms)
    })
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
    fn an_event_trigger_goes_to_the_event_handler_and_a_cell_trigger_still_to_react() {
        let f = fake();
        let input = br#"{"trigger":"event:reading","payload":{"from":"a.x","event":{"v":1}}}"#;
        let out: Value = serde_json::from_slice(&run(&f, input)).unwrap();
        assert_eq!(out, json!({"ok": true}));
        assert_eq!(f.attempts.get(), 0, "the default handler says nothing");
        assert_eq!(
            stored(&f).as_deref(),
            Some(r#"{"below":false,"stale":false}"#)
        );
        let cell = br#"{"trigger":"cell","payload":{"cells":["producer.value"]}}"#;
        assert!(is_ok(&serde_json::from_slice(&run(&f, cell)).unwrap()));
    }

    #[test]
    fn an_event_run_hands_the_port_the_source_and_the_event_to_the_handler() {
        let f = fake();
        let i = parse(br#"{"trigger":"event:reading"}"#).unwrap();
        let input = br#"{"trigger":"event:reading","payload":{"from":"a.x","event":7}}"#;
        let seen = std::cell::RefCell::new(None);
        let v: Value = serde_json::from_slice(input).unwrap();
        let (port, from, event) = (
            i.trigger.trim_start_matches("event:").to_string(),
            v.pointer("/payload/from")
                .and_then(Value::as_str)
                .unwrap()
                .to_string(),
            v.pointer("/payload/event").cloned().unwrap(),
        );
        execute::<Count>(&f, &i, |_, _, _, _| {
            *seen.borrow_mut() = Some((port.clone(), from.clone(), event.clone()));
            vec!["got".into()]
        });
        assert_eq!(
            seen.into_inner(),
            Some(("reading".into(), "a.x".into(), json!(7)))
        );
        assert_eq!(*f.sent.borrow(), ["got"]);
    }

    #[test]
    fn the_fake_records_what_emit_sends_and_what_it_refuses() {
        use crate::host::Emit;
        let f = fake();
        assert_eq!(f.emit("x", &json!(1)), Ok(()));
        f.emit_verdict.set(Err(Emit::Format));
        assert_eq!(f.emit("x", &json!("no")), Err(Emit::Format));
        assert_eq!(*f.emitted.borrow(), [("x".to_string(), json!(1))]);
    }

    #[test]
    fn the_fake_records_the_calls_and_answers_as_it_is_told() {
        use crate::host::Call;
        let f = fake();
        *f.call_answer.borrow_mut() = Ok(json!("yes"));
        assert_eq!(f.call("c", &json!(1)), Ok(json!("yes")));
        *f.call_answer.borrow_mut() = Err(Call::Unavailable);
        assert_eq!(f.call("c", &json!(2)), Err(Call::Unavailable));
        assert_eq!(
            *f.called.borrow(),
            [("c".to_string(), json!(1)), ("c".to_string(), json!(2))]
        );
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
