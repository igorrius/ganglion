//! The web template: a module that serves a page on an edge's loopback
//! listener (spec R-HTTP-4, R-HTTP-5). The agent copies it (`module.import`),
//! changes `manifest.yaml`, `src/logic.rs` and `assets/`, builds it and
//! installs it. This file is generic glue: routing, the state, the answer's
//! headers. The ABI glue is compiled only for wasm32.

pub mod assets;
pub mod host;
pub mod http;
pub mod logic;

#[cfg(test)]
mod fake;

#[cfg(target_arch = "wasm32")]
mod guest;

use host::Host;
use http::{Request, Response, escape_html, etag, json_body};
use logic::{Settings, State};
use serde_json::{Value, json};

/// The state key; the value is `logic::State` as JSON.
const KEY_STATE: &str = "state";

/// Handles one call. The answer is a ganglion Output as JSON.
pub fn run(h: &dyn Host, input: &[u8]) -> Vec<u8> {
    let out = match serde_json::from_slice::<Value>(input).ok() {
        Some(v) => match v.get("trigger").and_then(Value::as_str) {
            Some("init" | "health") => ok(),
            Some("http") => match Request::from_payload(v.get("payload").unwrap_or(&Value::Null)) {
                Some(req) => serve(h, &req).to_output(),
                None => failure("the http request is not one this module reads"),
            },
            Some(t) if t.starts_with("event:") => run_event(h, t.trim_start_matches("event:"), &v),
            Some(_) => failure("a trigger this module does not use"),
            None => failure("the input is not a ganglion Input"),
        },
        None => failure("the input is not a ganglion Input"),
    };
    out.to_string().into_bytes()
}

fn settings(h: &dyn Host) -> Settings {
    Settings::from_config(&|k| h.config(k))
}

fn load(h: &dyn Host) -> State {
    h.state_get(KEY_STATE)
        .and_then(|s| serde_json::from_str(&s).ok())
        .unwrap_or_default()
}

/// An event that reached an `accepts` port: the payload is `{"from":
/// "<module>.<port>", "event": <payload>}` and the trigger `event:<port>`.
/// The state is stored after the event is applied.
fn run_event(h: &dyn Host, port: &str, input: &Value) -> Value {
    let event = input.pointer("/payload/event").unwrap_or(&Value::Null);
    let now_s = h.now_unix();
    let mut state = load(h);
    match port {
        "reading" => logic::on_reading(event, &settings(h), &mut state, now_s),
        "notices" => logic::on_notice(event, &mut state, now_s),
        _ => return failure("an event on a port this module does not accept"),
    }
    match serde_json::to_string(&state) {
        Ok(s) => h.state_put(KEY_STATE, &s),
        Err(e) => return failure(&format!("the state cannot be stored: {e}")),
    }
    ok()
}

/// Routes one request. Only `GET` is served; every other method is 405.
fn serve(h: &dyn Host, req: &Request) -> Response {
    if req.method != "GET" {
        return Response::text(405, "method not allowed");
    }
    match req.path.as_str() {
        "" | "/" | "/index.html" => {
            let page = assets::INDEX.replace("{{title}}", &escape_html(&settings(h).title));
            fixed(req, "text/html; charset=utf-8", page)
        }
        "/app.js" => fixed(
            req,
            "text/javascript; charset=utf-8",
            assets::SCRIPT.to_string(),
        ),
        "/app.css" => fixed(req, "text/css; charset=utf-8", assets::STYLE.to_string()),
        "/api/data" => {
            let current = logic::LISTENED
                .first()
                .and_then(|id| h.cell_get(id))
                .filter(|(v, _)| !v.is_null());
            let data = logic::snapshot(&settings(h), &load(h), current, h.now_unix());
            Response::new(
                200,
                "application/json; charset=utf-8",
                "no-store",
                json_body(&data),
            )
        }
        _ => Response::text(404, "not found"),
    }
}

/// A fixed file: the browser keeps it and asks again with `If-None-Match`;
/// an unchanged one is answered with 304 and no body.
fn fixed(req: &Request, content_type: &'static str, body: String) -> Response {
    let tag = etag(&body);
    if req.header("if-none-match") == Some(tag.as_str()) {
        return Response::new(304, content_type, "no-cache", String::new()).with_etag(tag);
    }
    Response::new(200, content_type, "no-cache", body).with_etag(tag)
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
    use crate::fake::{Fake, NOW, fake};

    fn get(f: &Fake, path: &str) -> Value {
        call(
            f,
            json!({"method": "GET", "path": path, "query": {}, "headers": {}, "body": ""}),
        )
    }

    fn call(f: &Fake, payload: Value) -> Value {
        let input = json!({"trigger": "http", "payload": payload}).to_string();
        serde_json::from_slice(&run(f, input.as_bytes())).unwrap()
    }

    fn event(f: &Fake, port: &str, event: Value) -> Value {
        let input = json!({
            "trigger": format!("event:{port}"),
            "payload": {"from": "feed.x", "event": event},
        })
        .to_string();
        serde_json::from_slice(&run(f, input.as_bytes())).unwrap()
    }

    fn status(v: &Value) -> u64 {
        v["result"]["status"].as_u64().unwrap()
    }

    fn body(v: &Value) -> &str {
        v["result"]["body"].as_str().unwrap()
    }

    #[test]
    fn the_index_the_script_and_the_style_are_served_with_their_types() {
        let f = fake();
        for (path, ty) in [
            ("/", "text/html"),
            ("/app.js", "text/javascript"),
            ("/app.css", "text/css"),
        ] {
            let v = get(&f, path);
            assert_eq!(status(&v), 200, "{path}");
            assert!(
                v["result"]["headers"]["content-type"]
                    .as_str()
                    .unwrap()
                    .starts_with(ty)
            );
            assert!(v["result"]["headers"]["etag"].is_string());
            assert!(!body(&v).is_empty());
        }
    }

    #[test]
    fn an_unchanged_file_is_answered_304_with_no_body() {
        let f = fake();
        let first = get(&f, "/app.js");
        let tag = first["result"]["headers"]["etag"]
            .as_str()
            .unwrap()
            .to_string();
        let v = call(
            &f,
            json!({"method": "GET", "path": "/app.js", "headers": {"if-none-match": tag}}),
        );
        assert_eq!(status(&v), 304);
        assert_eq!(body(&v), "");
    }

    #[test]
    fn an_unknown_path_is_404_and_another_method_is_405() {
        let f = fake();
        assert_eq!(status(&get(&f, "/nothing")), 404);
        assert_eq!(status(&get(&f, "/../state")), 404);
        let v = call(
            &f,
            json!({"method": "POST", "path": "/api/data", "body": "x"}),
        );
        assert_eq!(status(&v), 405);
    }

    #[test]
    fn the_title_is_escaped_into_the_page() {
        let f = fake();
        f.config.borrow_mut().insert(
            "title".into(),
            r#"</title><script>alert(1)</script>"#.into(),
        );
        let page = get(&f, "/");
        let html = body(&page);
        assert!(!html.contains("<script>alert"), "{html}");
        assert!(html.contains("&lt;/title&gt;&lt;script&gt;alert(1)&lt;/script&gt;"));
        assert!(!html.contains("{{title}}"));
    }

    #[test]
    fn readings_and_notices_reach_the_data_the_page_polls() {
        let f = fake();
        f.cells
            .borrow_mut()
            .insert("producer.value".into(), (json!(42), NOW * 1000));
        assert!(is_ok(&event(&f, "reading", json!({"value": 41.5}))));
        assert!(is_ok(&event(
            &f,
            "notices",
            json!({"text": "battery low", "at": "t"})
        )));
        let v = get(&f, "/api/data");
        assert_eq!(status(&v), 200);
        assert_eq!(v["result"]["headers"]["cache-control"], "no-store");
        let data: Value = serde_json::from_str(body(&v)).unwrap();
        assert_eq!(data["current"]["value"], 42);
        assert_eq!(data["points"][0][1], 41.5);
        assert_eq!(data["notices"][0]["text"], "battery low");
    }

    #[test]
    fn hostile_notice_text_never_reaches_a_browser_as_markup() {
        let f = fake();
        let hostile = r#"</script><img src=x onerror=alert(1)><svg onload=alert(2)>&lt;"#;
        assert!(is_ok(&event(
            &f,
            "notices",
            json!({"text": hostile, "at": hostile})
        )));
        let v = get(&f, "/api/data");
        let b = body(&v);
        assert!(
            !b.contains('<') && !b.contains('>') && !b.contains('&'),
            "{b}"
        );
        let data: Value = serde_json::from_str(b).unwrap();
        assert_eq!(
            data["notices"][0]["text"], hostile,
            "the text itself is kept"
        );
        // The page never takes notices into its markup on the server's side.
        let page = get(&f, "/");
        assert!(!body(&page).contains("onerror"));
    }

    #[test]
    fn a_hostile_title_and_a_hostile_cell_value_stay_data() {
        let f = fake();
        f.config
            .borrow_mut()
            .insert("title".into(), "\"><b>".into());
        f.cells.borrow_mut().insert(
            "producer.value".into(),
            (json!("<script>alert(1)</script>"), NOW * 1000),
        );
        let v = get(&f, "/api/data");
        assert!(!body(&v).contains('<'));
        assert!(!body(&get(&f, "/")).contains("<b>"));
    }

    #[test]
    fn a_missing_cell_is_no_current_value_and_a_broken_state_starts_afresh() {
        let f = fake();
        f.state
            .borrow_mut()
            .insert("state".into(), "not json".into());
        let data: Value = serde_json::from_str(body(&get(&f, "/api/data"))).unwrap();
        assert!(data["current"].is_null());
        assert_eq!(data["points"], json!([]));
    }

    #[test]
    fn a_request_is_read_only_and_never_writes_the_state() {
        let f = fake();
        get(&f, "/api/data");
        get(&f, "/");
        assert!(f.state.borrow().is_empty());
    }

    #[test]
    fn an_event_on_an_unknown_port_is_an_exception_and_stores_nothing() {
        let f = fake();
        let v = event(&f, "other", json!({}));
        assert_eq!(v["ok"], false);
        assert_eq!(v["kind"], "module.exception");
        assert!(f.state.borrow().is_empty());
    }

    #[test]
    fn init_and_health_answer_ok_and_touch_nothing() {
        let f = fake();
        for t in ["init", "health"] {
            let out = run(&f, json!({"trigger": t}).to_string().as_bytes());
            assert!(is_ok(&serde_json::from_slice(&out).unwrap()));
        }
        assert!(f.state.borrow().is_empty());
    }

    #[test]
    fn a_broken_input_or_request_is_an_exception() {
        let f = fake();
        for raw in [
            &b"nope"[..],
            b"{}",
            br#"{"trigger":"http","payload":{}}"#,
            br#"{"trigger":"cron"}"#,
        ] {
            let v: Value = serde_json::from_slice(&run(&f, raw)).unwrap();
            assert_eq!(v["ok"], false);
            assert_eq!(v["kind"], "module.exception");
        }
    }

    fn is_ok(v: &Value) -> bool {
        v["ok"] == true
    }
}
