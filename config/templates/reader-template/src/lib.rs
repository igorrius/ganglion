//! The reader template (spec R-RDR-1..4): a module that reads an outside
//! service on a schedule and on its `status` tool, and reports what it read
//! as cells. `logic` says what is read and how; `engine` keeps the rules
//! every reader shares and names no service. The ABI glue is compiled only
//! for wasm32.

pub mod engine;
pub mod host;
pub mod logic;

#[cfg(test)]
mod fake;

#[cfg(target_arch = "wasm32")]
mod guest;

use host::Host;

/// Handles one call: the Input's trigger picks what to do (`engine`); the
/// answer is a ganglion Output as JSON.
pub fn run(h: &dyn Host, input: &[u8]) -> Vec<u8> {
    let v = serde_json::from_slice::<serde_json::Value>(input).unwrap_or_default();
    let trigger = v
        .get("trigger")
        .and_then(|t| t.as_str())
        .unwrap_or_default();
    match trigger.strip_prefix("call:") {
        Some(port) => answer_call(h, port, &v),
        None => engine::handle::<logic::Service>(h, trigger),
    }
    .to_string()
    .into_bytes()
}

/// A request that reached a served port: the input's payload is
/// `{"from": "<module>.<port>", "request": <payload>}` and the trigger is
/// `call:<port>`. `logic::on_call` answers; its answer is the call's result,
/// and a failure is told to the caller as a failed call, in the node's words.
fn answer_call(h: &dyn Host, port: &str, input: &serde_json::Value) -> serde_json::Value {
    let from = input
        .pointer("/payload/from")
        .and_then(|f| f.as_str())
        .unwrap_or_default();
    let request = input
        .pointer("/payload/request")
        .cloned()
        .unwrap_or_default();
    match logic::on_call(port, from, &request, h) {
        Ok(result) => serde_json::json!({ "ok": true, "result": result }),
        Err(msg) => engine::failure(engine::KIND_EXCEPTION, &msg),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::fake::fake;

    fn out(f: &crate::fake::Fake, input: &str) -> serde_json::Value {
        serde_json::from_slice(&run(f, input.as_bytes())).unwrap()
    }

    #[test]
    fn a_call_trigger_goes_to_the_call_handler_and_makes_no_request() {
        let f = fake(&[], &[], &[], "{}");
        let o = out(
            &f,
            r#"{"trigger":"call:ask","payload":{"from":"a.c","request":1}}"#,
        );
        assert_eq!(o["ok"], false, "{o}");
        assert_eq!(o["kind"], "module.exception");
        assert_eq!(o["error"], "unknown port ask");
        assert_eq!(f.calls(), 0);
    }

    #[test]
    fn the_other_triggers_still_go_to_the_engine() {
        let f = fake(&[], &[], &[], "{}");
        let o = out(&f, r#"{"trigger":"init"}"#);
        assert_eq!(o["error"], engine::UNKNOWN_TRIGGER);
        let o = out(&f, "not json");
        assert_eq!(o["error"], engine::UNKNOWN_TRIGGER);
    }

    #[test]
    fn the_fake_records_the_calls_and_answers_as_it_is_told() {
        use crate::host::Call;
        let f = fake(&[], &[], &[], "{}");
        *f.call_answer.borrow_mut() = Ok(serde_json::json!("yes"));
        assert_eq!(
            f.call("c", &serde_json::json!(1)),
            Ok(serde_json::json!("yes"))
        );
        *f.call_answer.borrow_mut() = Err(Call::Unavailable);
        assert_eq!(f.call("c", &serde_json::json!(2)), Err(Call::Unavailable));
        assert_eq!(f.called.borrow().len(), 2);
    }
}
