//! The rules every reader shares. This file names no service and no
//! setting: what is read comes through `Source` (`logic.rs`), so it stays as
//! it is when the service changes, and its tests keep passing.
//!
//! - `cron` reads and reports the cells: `sample`, the whole reading, and
//!   the ones `Source::cells` names.
//! - `tool:status` reads fresh under the same rules, or answers the last
//!   reading from the `sample` cell with its age and `fresh: false`.
//! - HTTP 429 stores `not_before` from Retry-After (seconds or an HTTP date,
//!   60 s when absent, at most a day); until then no request is made: cron
//!   answers OK, the tool answers the cells.
//! - A reading older than `STALE_AFTER_SECS` reports nothing; the first run
//!   of such an episode answers `stale`, later ones OK until a fresh reading.
//! - HTTP 401 or 403 answers `auth_failed`, and the same secrets are never
//!   tried again, so a wrong password cannot get an account locked; changing
//!   a secret allows one new attempt.
//! - `health` checks without network that the settings parse and every
//!   secret is set.
//! - When a read fails and there is no reading yet, the tool answers the
//!   read's own failure, never an answer outside its declared output.
//! - Any other trigger, `init` among them, is unknown: loading the module
//!   makes no request. A `call:<port>` trigger never reaches this file: `lib.rs`
//!   hands it to `logic::on_call`.

use crate::host::{Host, Request};
use serde::Serialize;
use serde_json::{Value, json};

/// What one reader reads and how; `logic.rs` implements it.
pub trait Source {
    type Settings;
    type Reading: Serialize;

    /// The secrets a read needs; the manifest's `secret.get` lists the same.
    const SECRETS: &'static [&'static str];
    /// A reading older than this many seconds is stale.
    const STALE_AFTER_SECS: i64;

    /// The settings from the module's config, checked without network.
    fn settings(config: &dyn Fn(&str) -> Option<String>) -> Result<Self::Settings, String>;
    /// The request of one read; `secret` returns a secret of `SECRETS`.
    fn request(settings: &Self::Settings, secret: &dyn Fn(&str) -> String) -> Request;
    /// The reading in a 2xx answer's body.
    fn parse(body: &[u8]) -> Result<Self::Reading, String>;
    /// When the reading was taken, Unix seconds.
    fn taken_at(reading: &Self::Reading) -> i64;
    /// The cells to report besides `sample`.
    fn cells(reading: &Self::Reading) -> Vec<(&'static str, Value)>;
}

/// The error text of a trigger the module does not handle; the node takes
/// it as success for `init` (`pdk.ErrUnknownTrigger`).
pub const UNKNOWN_TRIGGER: &str = "unknown trigger";

/// Output kinds the module reports; the manifest escalates all three.
pub const KIND_EXCEPTION: &str = "module.exception";
const KIND_AUTH_FAILED: &str = "auth_failed";
const KIND_STALE: &str = "stale";

/// The cell that holds the whole reading.
const CELL_SAMPLE: &str = "sample";

/// State keys; "" is unset (state has no delete).
const KEY_NOT_BEFORE: &str = "not_before";
const KEY_STALE_SINCE: &str = "stale_since";
const KEY_REFUSED: &str = "refused";

/// The wait after a 429 without a usable Retry-After.
const DEFAULT_RETRY_SECS: i64 = 60;
/// The longest wait a Retry-After can ask for: a day.
const MAX_RETRY_SECS: i64 = 86_400;

/// Why a read failed; maps to the Output kind.
#[derive(Debug, PartialEq)]
enum Error {
    /// A secret is missing or the service refused them; not tried again.
    Auth(String),
    Stale(String),
    Other(String),
}

/// How a read under the 429 rule went.
enum Read<R> {
    /// A fresh reading, already reported to the cells.
    Fresh(R),
    /// No request: the service asked to wait until this time.
    Waiting(i64),
    Failed(Error),
}

/// Handles one call and returns a ganglion Output
/// (`{"ok":…,"result":…,"kind":…,"error":…}`).
pub fn handle<S: Source>(h: &dyn Host, trigger: &str) -> Value {
    match trigger {
        "cron" => cron::<S>(h),
        "tool:status" => status::<S>(h),
        "health" => health::<S>(h),
        _ => failure(KIND_EXCEPTION, UNKNOWN_TRIGGER),
    }
}

fn cron<S: Source>(h: &dyn Host) -> Value {
    match read::<S>(h) {
        Read::Fresh(r) => ok(json!(r)),
        Read::Waiting(until) => ok(json!({ "waiting_until": until })),
        Read::Failed(Error::Stale(m)) => stale(h, &m),
        Read::Failed(e) => fail(e),
    }
}

/// The status tool: a fresh reading, or the last one from the cell.
fn status<S: Source>(h: &dyn Host) -> Value {
    let now = h.now_unix();
    let err = match read::<S>(h) {
        Read::Fresh(r) => {
            return ok(json!({
                "age_s": (now - S::taken_at(&r)).max(0),
                "fresh": true,
                "sample": r,
            }));
        }
        Read::Waiting(_) => None,
        Read::Failed(e) => Some(e),
    };
    if let Some(v) = from_cell(h, now) {
        return ok(v);
    }
    match err {
        Some(e) => fail(e),
        None => failure(
            KIND_EXCEPTION,
            "the service asked to wait and there is no reading yet",
        ),
    }
}

/// The health trigger: OK when the settings parse and every secret is set.
/// It makes no request, so it cannot tell a wrong secret from a right one;
/// the first read does.
fn health<S: Source>(h: &dyn Host) -> Value {
    if let Err(m) = S::settings(&|k| h.config(k)) {
        return failure(KIND_EXCEPTION, &m);
    }
    match missing_secret::<S>(h) {
        Some(name) => failure(KIND_EXCEPTION, &format!("secret {name} is not set")),
        None => ok(json!({})),
    }
}

fn missing_secret<S: Source>(h: &dyn Host) -> Option<&'static str> {
    S::SECRETS
        .iter()
        .copied()
        .find(|n| h.secret(n).filter(|v| !v.is_empty()).is_none())
}

/// Reads unless the service asked to wait; a 429 sets the wait. A fresh
/// reading goes to the cells and ends a stale episode.
fn read<S: Source>(h: &dyn Host) -> Read<S::Reading> {
    let settings = match S::settings(&|k| h.config(k)) {
        Ok(s) => s,
        Err(m) => return Read::Failed(Error::Other(m)),
    };
    let now = h.now_unix();
    if let Some(until) = stamp(h, KEY_NOT_BEFORE).filter(|t| *t > now) {
        return Read::Waiting(until);
    }
    if let Some(name) = missing_secret::<S>(h) {
        return Read::Failed(Error::Auth(format!("secret {name} is not set")));
    }
    let mark = mark::<S>(h);
    if h.state_get(KEY_REFUSED).as_deref() == Some(mark.as_str()) {
        return Read::Failed(Error::Auth(
            "the service refused these secrets earlier".into(),
        ));
    }
    let req = S::request(&settings, &|n| h.secret(n).unwrap_or_default());
    let resp = match h.fetch(&req) {
        Ok(r) => r,
        Err(e) => return Read::Failed(Error::Other(format!("the request failed: {e}"))),
    };
    match resp.status {
        429 => {
            let until = retry_at(resp.headers.get("retry-after").map(String::as_str), now);
            h.state_put(KEY_NOT_BEFORE, &until.to_string());
            h.log(&format!(
                "the service answered 429; no request before {until}"
            ));
            return Read::Waiting(until);
        }
        401 | 403 => {
            h.state_put(KEY_REFUSED, &mark);
            return Read::Failed(Error::Auth(format!(
                "the service refused the secrets: HTTP {}",
                resp.status
            )));
        }
        s if !(200..300).contains(&s) => {
            return Read::Failed(Error::Other(format!("the request failed: HTTP {s}")));
        }
        _ => {}
    }
    let reading = match S::parse(&resp.body) {
        Ok(r) => r,
        Err(m) => return Read::Failed(Error::Other(m)),
    };
    let age = now - S::taken_at(&reading);
    if age > S::STALE_AFTER_SECS {
        return Read::Failed(Error::Stale(format!(
            "the reading is stale ({} min old)",
            age / 60
        )));
    }
    let mut cells = S::cells(&reading);
    cells.push((CELL_SAMPLE, json!(reading)));
    for (name, value) in cells {
        if let Err(e) = h.cell_report(name, &value) {
            return Read::Failed(Error::Other(format!("cell {name}: {e}")));
        }
    }
    h.state_put(KEY_STALE_SINCE, "");
    Read::Fresh(reading)
}

/// A short mark of the secrets in use, kept in state to notice that one of
/// them changed after a refusal. It is 16 bits on purpose: enough to notice
/// a change, too little to check a guess of a secret against.
fn mark<S: Source>(h: &dyn Host) -> String {
    let mut hash: u32 = 0x811c_9dc5;
    for name in S::SECRETS {
        for b in h.secret(name).unwrap_or_default().bytes().chain([0]) {
            hash = (hash ^ u32::from(b)).wrapping_mul(0x0100_0193);
        }
    }
    format!("{:04x}", (hash >> 16) ^ (hash & 0xffff))
}

/// A stale reading: the first run of an episode fails as `stale`, the
/// rest answer OK.
fn stale(h: &dyn Host, msg: &str) -> Value {
    if let Some(since) = stamp(h, KEY_STALE_SINCE) {
        return ok(json!({ "stale_since": since }));
    }
    h.state_put(KEY_STALE_SINCE, &h.now_unix().to_string());
    failure(KIND_STALE, msg)
}

/// The status answer from the `sample` cell, aged by the reading's own
/// time; None when there was never a reading: a cell that is missing, null
/// (defined but never reported) or not an object is no reading.
fn from_cell(h: &dyn Host, now: i64) -> Option<Value> {
    let (sample, at_ms) = h.cell_get(CELL_SAMPLE).filter(|(v, _)| v.is_object())?;
    let ts = sample
        .get("ts")
        .and_then(Value::as_i64)
        .unwrap_or(at_ms / 1000);
    Some(json!({
        "age_s": (now - ts).max(0),
        "fresh": false,
        "sample": sample,
    }))
}

/// A Unix time kept in state; None when unset.
fn stamp(h: &dyn Host, key: &str) -> Option<i64> {
    h.state_get(key)?.parse().ok()
}

/// When the next request may be made after a 429: Retry-After in seconds
/// or as an HTTP date, 60 s when it is absent or unreadable, at most a day.
pub fn retry_at(header: Option<&str>, now: i64) -> i64 {
    let wait = header
        .map(str::trim)
        .and_then(|v| {
            v.parse::<i64>()
                .ok()
                .or_else(|| http_date(v).map(|t| t - now))
        })
        .unwrap_or(DEFAULT_RETRY_SECS);
    now + wait.clamp(0, MAX_RETRY_SECS)
}

const MONTHS: [&str; 12] = [
    "Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec",
];

/// Parses an HTTP date (IMF-fixdate, "Sun, 06 Nov 1994 08:49:37 GMT") to
/// Unix seconds.
fn http_date(s: &str) -> Option<i64> {
    let fields: Vec<&str> = s.split_whitespace().collect();
    let [_, day, month, year, time, "GMT"] = fields.as_slice() else {
        return None;
    };
    let day: i64 = day.parse().ok().filter(|d| (1..=31).contains(d))?;
    let month = MONTHS.iter().position(|m| m == month)? as i64 + 1;
    let year: i64 = year.parse().ok()?;
    let hms: Vec<i64> = time
        .split(':')
        .map(|p| p.parse().ok())
        .collect::<Option<_>>()?;
    let [hour, min, sec] = hms.as_slice() else {
        return None;
    };
    if *hour > 23 || *min > 59 || *sec > 60 {
        return None;
    }
    Some(days_from_civil(year, month, day) * 86_400 + hour * 3600 + min * 60 + sec)
}

/// Days since 1970-01-01 of a date in the proleptic Gregorian calendar
/// (Howard Hinnant's algorithm).
fn days_from_civil(year: i64, month: i64, day: i64) -> i64 {
    let y = if month <= 2 { year - 1 } else { year };
    let era = y.div_euclid(400);
    let yoe = y - era * 400;
    let doy = (153 * ((month + 9) % 12) + 2) / 5 + day - 1;
    let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    era * 146_097 + doe - 719_468
}
fn ok(result: Value) -> Value {
    json!({ "ok": true, "result": result })
}

pub fn failure(kind: &str, msg: &str) -> Value {
    json!({ "ok": false, "kind": kind, "error": msg })
}

/// A failed read as an Output.
fn fail(e: Error) -> Value {
    match e {
        Error::Auth(m) => failure(KIND_AUTH_FAILED, &format!("authentication failed: {m}")),
        Error::Stale(m) => failure(KIND_STALE, &m),
        Error::Other(m) => failure(KIND_EXCEPTION, &m),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::fake::{Fake, NOW, fake};

    /// A source of the tests' own: one secret, one setting, a reading of a
    /// time and a number. The engine's tests do not depend on `logic.rs`.
    struct Probe;

    #[derive(Serialize)]
    struct Seen {
        ts: i64,
        n: i64,
    }

    impl Source for Probe {
        type Settings = String;
        type Reading = Seen;
        const SECRETS: &'static [&'static str] = &["KEY"];
        const STALE_AFTER_SECS: i64 = 20 * 60;

        fn settings(config: &dyn Fn(&str) -> Option<String>) -> Result<String, String> {
            config("id")
                .filter(|v| !v.is_empty())
                .ok_or_else(|| "config.id is not set".to_string())
        }
        fn request(id: &String, secret: &dyn Fn(&str) -> String) -> Request {
            Request {
                method: "GET".into(),
                url: format!("https://probe.example/{id}"),
                headers: vec![("X-Key".into(), secret("KEY"))],
                body: Vec::new(),
            }
        }
        fn parse(body: &[u8]) -> Result<Seen, String> {
            let v: Value = serde_json::from_slice(body).map_err(|e| e.to_string())?;
            Ok(Seen {
                ts: v.get("ts").and_then(Value::as_i64).ok_or("no ts")?,
                n: v.get("n").and_then(Value::as_i64).ok_or("no n")?,
            })
        }
        fn taken_at(r: &Seen) -> i64 {
            r.ts
        }
        fn cells(r: &Seen) -> Vec<(&'static str, Value)> {
            vec![("n", json!(r.n))]
        }
    }

    fn seen(ts: i64, n: i64) -> String {
        format!(r#"{{"ts":{ts},"n":{n}}}"#)
    }

    /// A host whose service answers a reading three minutes old.
    fn host() -> Fake {
        fake(
            &[("KEY", "k-1")],
            &[("id", "a1")],
            &["n", "sample"],
            &seen(NOW - 180, 57),
        )
    }

    fn run(f: &Fake, trigger: &str) -> Value {
        handle::<Probe>(f, trigger)
    }

    fn kind(out: &Value) -> &str {
        out["kind"].as_str().unwrap_or_default()
    }

    #[test]
    fn cron_reports_the_cells() {
        let f = host();
        let out = run(&f, "cron");
        assert_eq!(out["ok"], true, "{out}");
        assert_eq!(f.cell("n"), Some(json!(57)));
        assert_eq!(f.cell("sample"), Some(json!({"ts": NOW - 180, "n": 57})));
        let req = &f.requests.borrow()[0];
        assert_eq!(req.url, "https://probe.example/a1");
        assert_eq!(req.headers, vec![("X-Key".to_string(), "k-1".to_string())]);
    }

    #[test]
    fn a_429_waits_out_its_retry_after() {
        let f = host();
        run(&f, "cron");
        f.answer(429, Some(("retry-after", "120")), "{}");
        let out = run(&f, "cron");
        assert_eq!(out["ok"], true, "{out}");
        assert_eq!(f.state_get("not_before"), Some((NOW + 120).to_string()));
        let calls = f.calls();

        f.now.set(NOW + 119);
        assert_eq!(run(&f, "cron")["ok"], true);
        let status = run(&f, "tool:status");
        assert_eq!(f.calls(), calls, "a request was made within Retry-After");
        assert_eq!(status["result"]["fresh"], false, "{status}");

        f.answer(200, None, &seen(NOW + 100, 61));
        f.now.set(NOW + 120);
        assert_eq!(run(&f, "cron")["ok"], true);
        assert_eq!(f.calls(), calls + 1);
        assert_eq!(f.cell("n"), Some(json!(61)));
    }

    #[test]
    fn retry_after_is_seconds_or_a_date_and_60_s_when_absent() {
        assert_eq!(retry_at(Some("120"), NOW), NOW + 120);
        assert_eq!(retry_at(None, NOW), NOW + 60);
        assert_eq!(retry_at(Some("soon"), NOW), NOW + 60);
        // 1_793_000_000 is Mon, 26 Oct 2026 07:33:20 GMT.
        assert_eq!(
            retry_at(Some("Mon, 26 Oct 2026 07:35:20 GMT"), NOW),
            NOW + 120
        );
        assert_eq!(retry_at(Some("Mon, 26 Oct 2026 07:00:00 GMT"), NOW), NOW);
        assert_eq!(retry_at(Some("99999999"), NOW), NOW + 86_400);
        assert_eq!(
            http_date("Wed, 21 Oct 2015 07:28:00 GMT"),
            Some(1_445_412_480)
        );
        assert_eq!(http_date("Wed, 21 Oct 2015 07:28:00 PST"), None);
    }

    #[test]
    fn a_429_from_the_tool_also_sets_the_wait() {
        let f = host();
        run(&f, "cron");
        f.answer(429, None, "{}");
        let out = run(&f, "tool:status");
        assert_eq!(out["result"]["fresh"], false, "{out}");
        assert_eq!(f.state_get("not_before"), Some((NOW + 60).to_string()));
    }

    #[test]
    fn a_stale_episode_is_reported_once() {
        let f = host();
        f.answer(200, None, &seen(NOW - 21 * 60, 57));
        let first = run(&f, "cron");
        assert_eq!(
            (first["ok"].as_bool(), kind(&first)),
            (Some(false), "stale")
        );
        assert!(
            f.cell("sample").is_none(),
            "a stale reading reached the cells"
        );
        f.now.set(NOW + 1800);
        let second = run(&f, "cron");
        assert_eq!(second["ok"], true, "{second}");

        f.answer(200, None, &seen(NOW + 1700, 61));
        assert_eq!(run(&f, "cron")["ok"], true);
        assert_eq!(f.state_get("stale_since"), Some(String::new()));

        f.now.set(NOW + 3600);
        let again = run(&f, "cron");
        assert_eq!(kind(&again), "stale", "a new episode is reported: {again}");
    }

    #[test]
    fn refused_secrets_are_tried_once_until_one_changes() {
        let f = host();
        f.answer(401, None, "{}");
        assert_eq!(kind(&run(&f, "cron")), "auth_failed");
        assert_eq!(kind(&run(&f, "cron")), "auth_failed");
        let status = run(&f, "tool:status");
        assert_eq!(kind(&status), "auth_failed", "{status}");
        assert_eq!(f.calls(), 1);

        f.secrets.borrow_mut().insert("KEY".into(), "k-2".into());
        f.answer(200, None, &seen(NOW - 60, 58));
        assert_eq!(run(&f, "cron")["ok"], true);
        assert_eq!(f.calls(), 2);
    }

    #[test]
    fn the_mark_of_the_secrets_does_not_hold_them() {
        let f = host();
        f.answer(403, None, "{}");
        run(&f, "cron");
        let mark = f.state_get("refused").unwrap();
        assert_eq!(mark.len(), 4, "{mark}");
        assert!(!mark.contains("k-1"));
    }

    #[test]
    fn a_missing_secret_is_auth_failed_without_network() {
        let f = host();
        f.secrets.borrow_mut().insert("KEY".into(), String::new());
        assert_eq!(kind(&run(&f, "cron")), "auth_failed");
        assert_eq!(f.calls(), 0);
    }

    #[test]
    fn status_reads_fresh() {
        let f = host();
        let out = run(&f, "tool:status");
        assert_eq!(
            out["result"],
            json!({"age_s": 180, "fresh": true, "sample": {"ts": NOW - 180, "n": 57}}),
            "{out}"
        );
    }

    #[test]
    fn status_falls_back_to_the_cell() {
        let f = host();
        run(&f, "cron");
        f.answer(500, None, "oops");
        f.now.set(NOW + 600);
        let out = run(&f, "tool:status");
        assert_eq!(
            out["result"],
            json!({"age_s": 780, "fresh": false, "sample": {"ts": NOW - 180, "n": 57}}),
            "{out}"
        );
    }

    #[test]
    fn status_with_no_reading_answers_the_failure() {
        let f = host();
        f.answer(500, None, "oops");
        let out = run(&f, "tool:status");
        assert_eq!(
            (out["ok"].as_bool(), kind(&out)),
            (Some(false), "module.exception")
        );
        assert!(out["error"].as_str().unwrap().contains("HTTP 500"), "{out}");

        // A cell that is defined but never reported is null: still no reading.
        f.cells
            .borrow_mut()
            .insert("sample".into(), (Value::Null, 0));
        assert_eq!(run(&f, "tool:status")["ok"], false);
        // And a cell that is not an object is never answered as a sample.
        f.cells.borrow_mut().insert("sample".into(), (json!(7), 0));
        assert_eq!(run(&f, "tool:status")["ok"], false);

        f.answer(429, None, "{}");
        let waiting = run(&f, "tool:status");
        assert_eq!(kind(&waiting), "module.exception", "{waiting}");
    }

    #[test]
    fn a_bad_answer_is_an_exception_that_names_the_field() {
        let f = host();
        f.answer(200, None, r#"{"ts":1}"#);
        let out = run(&f, "cron");
        assert_eq!(kind(&out), "module.exception");
        assert_eq!(out["error"], "no n");
    }

    #[test]
    fn a_cell_the_manifest_lacks_is_an_exception() {
        let mut f = host();
        f.declared = vec!["sample"];
        let out = run(&f, "cron");
        assert_eq!(kind(&out), "module.exception");
        assert!(out["error"].as_str().unwrap().contains("cell n"), "{out}");
    }

    #[test]
    fn health_checks_the_settings_and_the_secrets_without_network() {
        let f = host();
        assert_eq!(run(&f, "health")["ok"], true);
        f.config.borrow_mut().remove("id");
        let out = run(&f, "health");
        assert_eq!(out["error"], "config.id is not set", "{out}");
        f.config.borrow_mut().insert("id".into(), "a1".into());
        f.secrets.borrow_mut().remove("KEY");
        let out = run(&f, "health");
        assert_eq!(out["error"], "secret KEY is not set", "{out}");
        assert_eq!(f.calls(), 0);
    }

    #[test]
    fn bad_settings_are_an_exception_without_network() {
        let f = host();
        f.config.borrow_mut().remove("id");
        for trigger in ["cron", "tool:status"] {
            let out = run(&f, trigger);
            assert_eq!(kind(&out), "module.exception", "{trigger}: {out}");
        }
        assert_eq!(f.calls(), 0);
    }

    #[test]
    fn init_and_other_triggers_are_unknown_and_make_no_request() {
        let f = host();
        for trigger in ["init", "tool:other", ""] {
            let out = run(&f, trigger);
            assert_eq!(out["error"], UNKNOWN_TRIGGER, "{trigger}");
        }
        assert_eq!(f.calls(), 0);
    }
}
