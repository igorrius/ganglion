//! What the module does on each trigger (spec R-DEYE-1..3).
//!
//! - `cron` reads the inverter and reports the cells `soc`, `grid` and
//!   `sample`.
//! - `tool:status` reads fresh under the same rules, or answers the cells'
//!   last values with their age and `fresh: false`.
//! - HTTP 429 stores `not_before` from Retry-After (seconds or an HTTP
//!   date, 60 s when absent); until then no request is made: cron answers
//!   OK, the tool answers the cells.
//! - A reading older than 20 minutes reports nothing; the first run of such
//!   an episode answers `stale`, later ones OK until a fresh reading.
//! - A refused login answers `auth_failed`; the same credentials are never
//!   tried again (`cloud`).
//! - `health` checks what it can without network: `inverter_sn` looks like
//!   a serial number and every `DEYE_*` secret is set; otherwise it fails
//!   as `module.exception`, so the install and the module's card show it.
//! - When a read fails and the cells hold no reading (never reported, so
//!   null), the tool answers the read's own failure, never an answer that
//!   breaks its declared output.
//! - Any other trigger, `init` among them, is unknown: loading the module
//!   makes no request.

use crate::cloud::{self, Error, Sample};
use crate::host::Host;
use serde_json::{Value, json};

/// The error text of a trigger the module does not handle; the node takes
/// it as success for `init` (`pdk.ErrUnknownTrigger`).
pub const UNKNOWN_TRIGGER: &str = "unknown trigger";

/// Output kinds the module reports; the manifest escalates all three.
const KIND_EXCEPTION: &str = "module.exception";
const KIND_AUTH_FAILED: &str = "auth_failed";
const KIND_STALE: &str = "stale";

/// State keys, Unix seconds; "" is unset (state has no delete).
const KEY_NOT_BEFORE: &str = "not_before";
const KEY_STALE_SINCE: &str = "stale_since";

/// The wait after a 429 without a usable Retry-After.
const DEFAULT_RETRY_SECS: i64 = 60;
/// The longest wait a Retry-After can ask for: a day.
const MAX_RETRY_SECS: i64 = 86_400;

/// Handles one call and returns a ganglion Output
/// (`{"ok":…,"result":…,"kind":…,"error":…}`).
pub fn handle(h: &dyn Host, trigger: &str) -> Value {
    match trigger {
        "cron" => cron(h),
        "tool:status" => status(h),
        "health" => health(h),
        _ => failure(KIND_EXCEPTION, UNKNOWN_TRIGGER),
    }
}

/// How a read under the 429 rule went.
enum Read {
    /// A fresh reading, already reported to the cells.
    Fresh(Sample),
    /// No request: DeyeCloud asked to wait until this time.
    Waiting(i64),
    Failed(Error),
}

fn cron(h: &dyn Host) -> Value {
    let sn = match serial(h) {
        Ok(sn) => sn,
        Err(out) => return out,
    };
    match read(h, &sn) {
        Read::Fresh(s) => ok(json!(s)),
        Read::Waiting(until) => ok(json!({ "waiting_until": until })),
        Read::Failed(Error::Stale(m)) => stale(h, &m),
        Read::Failed(e) => fail(e),
    }
}

/// The status tool: a fresh reading, or the cells' last values.
fn status(h: &dyn Host) -> Value {
    let sn = match serial(h) {
        Ok(sn) => sn,
        Err(out) => return out,
    };
    let now = h.now_unix();
    let err = match read(h, &sn) {
        Read::Fresh(s) => return ok(answer(&s, now)),
        Read::Waiting(_) => None,
        Read::Failed(e) => Some(e),
    };
    if let Some(v) = from_cells(h, now) {
        return ok(v);
    }
    match err {
        Some(e) => fail(e),
        None => failure(
            KIND_EXCEPTION,
            "deye: DeyeCloud asked to wait and there is no reading yet",
        ),
    }
}

/// The secrets a read needs; the manifest grants exactly these.
pub const SECRETS: [&str; 4] = [
    "DEYE_APP_ID",
    "DEYE_APP_SECRET",
    "DEYE_EMAIL",
    "DEYE_PASSWORD",
];

/// The length of a serial number the health check accepts; Deye's are ten
/// digits.
const SERIAL_LEN: std::ops::RangeInclusive<usize> = 8..=16;

/// The health trigger: OK when the serial number looks like one and every
/// secret is set. It makes no request, so it cannot tell a wrong password
/// or serial from a right one; the first read does.
fn health(h: &dyn Host) -> Value {
    let sn = h.config("inverter_sn").unwrap_or_default();
    if !SERIAL_LEN.contains(&sn.len()) || !sn.bytes().all(|b| b.is_ascii_digit()) {
        return failure(
            KIND_EXCEPTION,
            "deye: config.inverter_sn does not look like a serial number (8 to 16 digits)",
        );
    }
    if let Some(name) = SECRETS
        .iter()
        .find(|n| h.secret(n).filter(|v| !v.is_empty()).is_none())
    {
        return failure(KIND_EXCEPTION, &format!("deye: secret {name} is not set"));
    }
    ok(json!({}))
}

fn serial(h: &dyn Host) -> Result<String, Value> {
    h.config("inverter_sn")
        .filter(|s| !s.is_empty())
        .ok_or_else(|| failure(KIND_EXCEPTION, "deye: config.inverter_sn is not set"))
}

/// Reads unless DeyeCloud asked to wait; a 429 sets the wait. A fresh
/// reading goes to the cells and ends a stale episode.
fn read(h: &dyn Host, sn: &str) -> Read {
    let now = h.now_unix();
    if let Some(until) = stamp(h, KEY_NOT_BEFORE).filter(|t| *t > now) {
        return Read::Waiting(until);
    }
    match cloud::read(h, sn) {
        Ok(s) => match report(h, &s) {
            Ok(()) => {
                h.state_put(KEY_STALE_SINCE, "");
                Read::Fresh(s)
            }
            Err(m) => Read::Failed(Error::Other(m)),
        },
        Err(Error::RateLimited(header)) => {
            let until = retry_at(header.as_deref(), now);
            h.state_put(KEY_NOT_BEFORE, &until.to_string());
            h.log(&format!(
                "deye: DeyeCloud answered 429; no request before {until}"
            ));
            Read::Waiting(until)
        }
        Err(e) => Read::Failed(e),
    }
}

/// Reports a reading to the three cells.
fn report(h: &dyn Host, s: &Sample) -> Result<(), String> {
    for (name, value) in [
        ("soc", json!(s.soc)),
        ("grid", json!(s.grid_on == 1)),
        ("sample", json!(s)),
    ] {
        h.cell_report(name, &value)
            .map_err(|e| format!("cell {name}: {e}"))?;
    }
    Ok(())
}

/// A stale reading: the first run of an episode fails as `stale`, the
/// rest answer OK.
fn stale(h: &dyn Host, msg: &str) -> Value {
    if let Some(since) = stamp(h, KEY_STALE_SINCE) {
        return ok(json!({ "stale_since": since }));
    }
    h.state_put(KEY_STALE_SINCE, &h.now_unix().to_string());
    failure(KIND_STALE, &format!("deye: {msg}"))
}

/// The status answer for a fresh reading.
fn answer(s: &Sample, now: i64) -> Value {
    json!({
        "soc": s.soc,
        "grid": s.grid_on == 1,
        "age_s": (now - s.ts).max(0),
        "fresh": true,
        "sample": s,
    })
}

/// The status answer from the cells, aged by the reading's own time; None
/// when there was never a reading: a cell that is missing, null (defined
/// but never reported) or not of its type is no reading.
fn from_cells(h: &dyn Host, now: i64) -> Option<Value> {
    let (sample, at_ms) = h.cell_get("sample").filter(|(v, _)| v.is_object())?;
    let ts = sample
        .get("ts")
        .and_then(Value::as_i64)
        .unwrap_or(at_ms / 1000);
    let soc = h
        .cell_get("soc")
        .and_then(|(v, _)| v.as_i64())
        .or_else(|| sample.get("soc").and_then(Value::as_i64))?;
    let grid = h
        .cell_get("grid")
        .and_then(|(v, _)| v.as_bool())
        .or_else(|| {
            sample
                .get("grid_on")
                .and_then(Value::as_i64)
                .map(|g| g == 1)
        })?;
    Some(json!({
        "soc": soc,
        "grid": grid,
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

fn failure(kind: &str, msg: &str) -> Value {
    json!({ "ok": false, "kind": kind, "error": msg })
}

/// A failed read as an Output.
fn fail(e: Error) -> Value {
    match e {
        Error::Auth(m) => failure(
            KIND_AUTH_FAILED,
            &format!("deye: authentication failed: {m}"),
        ),
        Error::Stale(m) => failure(KIND_STALE, &format!("deye: {m}")),
        Error::RateLimited(_) => failure(KIND_EXCEPTION, "deye: DeyeCloud answered 429"),
        Error::Other(m) => failure(KIND_EXCEPTION, &format!("deye: {m}")),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::fake::{Fake, NOW, fake, latest};

    fn kind(out: &Value) -> &str {
        out["kind"].as_str().unwrap_or_default()
    }

    fn rate_limit(f: &Fake, retry_after: Option<&str>) {
        f.latest_status.set(429);
        f.latest.replace(r#"{"msg":"too many requests"}"#.into());
        let mut h = f.latest_headers.borrow_mut();
        h.clear();
        if let Some(v) = retry_after {
            h.insert("retry-after".into(), v.into());
        }
    }

    fn recover(f: &Fake, collected: i64) {
        f.latest_status.set(200);
        f.latest_headers.borrow_mut().clear();
        f.latest.replace(latest(collected, "61", "229.0"));
    }

    #[test]
    fn cron_reports_the_three_cells() {
        let f = fake();
        let out = handle(&f, "cron");
        assert_eq!(out["ok"], true, "{out}");
        assert_eq!(f.cell("soc"), Some(json!(57)));
        assert_eq!(f.cell("grid"), Some(json!(true)));
        let sample = f.cell("sample").unwrap();
        assert_eq!(sample["ts"], NOW - 180);
        assert_eq!(sample["batt_v"], 52.31);
        assert_eq!(sample.as_object().unwrap().len(), 12);
    }

    #[test]
    fn a_429_waits_out_its_retry_after() {
        let f = fake();
        handle(&f, "cron"); // logs in and reads
        rate_limit(&f, Some("120"));
        let out = handle(&f, "cron");
        assert_eq!(out["ok"], true, "{out}");
        assert_eq!(f.state_get("not_before"), Some((NOW + 120).to_string()));
        let calls = f.calls();

        f.now.set(NOW + 119);
        assert_eq!(handle(&f, "cron")["ok"], true);
        let status = handle(&f, "tool:status");
        assert_eq!(f.calls(), calls, "a request was made within Retry-After");
        assert_eq!(status["result"]["fresh"], false, "{status}");

        recover(&f, NOW + 100);
        f.now.set(NOW + 120);
        assert_eq!(handle(&f, "cron")["ok"], true);
        assert_eq!(f.calls(), calls + 1);
        assert_eq!(f.cell("soc"), Some(json!(61)));
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
        let f = fake();
        handle(&f, "cron");
        rate_limit(&f, None);
        let out = handle(&f, "tool:status");
        assert_eq!(out["result"]["fresh"], false, "{out}");
        assert_eq!(f.state_get("not_before"), Some((NOW + 60).to_string()));
    }

    #[test]
    fn a_stale_episode_is_reported_once() {
        let f = fake();
        f.latest.replace(latest(NOW - 21 * 60, "57", "230"));
        let first = handle(&f, "cron");
        assert_eq!(
            (first["ok"].as_bool(), kind(&first)),
            (Some(false), "stale")
        );
        assert!(
            f.cell("sample").is_none(),
            "a stale reading reached the cells"
        );
        f.now.set(NOW + 1800);
        f.latest.replace(latest(NOW - 10 * 60, "57", "230"));
        let second = handle(&f, "cron");
        assert_eq!(second["ok"], true, "{second}");

        recover(&f, NOW + 1700);
        assert_eq!(handle(&f, "cron")["ok"], true);
        assert_eq!(f.state_get("stale_since"), Some(String::new()));

        f.now.set(NOW + 3600);
        let again = handle(&f, "cron");
        assert_eq!(kind(&again), "stale", "a new episode is reported: {again}");
    }

    #[test]
    fn a_refused_login_is_tried_once() {
        let f = fake();
        f.login
            .replace(r#"{"code":"2101006","msg":"password error"}"#.into());
        assert_eq!(kind(&handle(&f, "cron")), "auth_failed");
        assert_eq!(kind(&handle(&f, "cron")), "auth_failed");
        let status = handle(&f, "tool:status");
        assert_eq!(kind(&status), "auth_failed", "{status}");
        assert_eq!(f.logins(), 1);
        assert_eq!(f.calls(), 1);
    }

    #[test]
    fn status_reads_fresh() {
        let f = fake();
        let out = handle(&f, "tool:status");
        let r = &out["result"];
        assert_eq!(
            (&r["soc"], &r["grid"], &r["age_s"], &r["fresh"]),
            (&json!(57), &json!(true), &json!(180), &json!(true)),
            "{out}"
        );
        assert_eq!(r["sample"]["grid_on"], 1);
        assert_eq!(
            f.cell("soc"),
            Some(json!(57)),
            "the tool's reading is reported"
        );
    }

    #[test]
    fn status_falls_back_to_the_cells() {
        let f = fake();
        handle(&f, "cron");
        f.now.set(NOW + 600);
        f.latest
            .replace(r#"{"code":"500","msg":"down","success":false}"#.into());
        let out = handle(&f, "tool:status");
        let r = &out["result"];
        assert_eq!(out["ok"], true, "{out}");
        assert_eq!(
            (&r["soc"], &r["grid"], &r["age_s"], &r["fresh"]),
            (&json!(57), &json!(true), &json!(780), &json!(false))
        );
        assert_eq!(r["sample"]["soc"], 57);
    }

    #[test]
    fn status_with_no_reading_fails() {
        let f = fake();
        f.latest
            .replace(r#"{"code":"500","msg":"down","success":false}"#.into());
        let out = handle(&f, "tool:status");
        assert_eq!(
            (out["ok"].as_bool(), kind(&out)),
            (Some(false), "module.exception")
        );
    }

    /// Cells the node defined at install but the module never reported:
    /// `cell_get` finds them, holding null.
    fn null_cells(f: &Fake) {
        let mut cells = f.cells.borrow_mut();
        for name in ["soc", "grid", "sample"] {
            cells.insert(name.into(), (Value::Null, (NOW - 3600) * 1000));
        }
    }

    #[test]
    fn status_with_a_failed_read_and_null_cells_answers_the_failure() {
        let cases = [
            (
                r#"{"code":"2101006","msg":"password error"}"#,
                None,
                "auth_failed",
            ),
            (
                r#"{"code":"1000000","accessToken":"tok-1"}"#,
                None,
                "module.exception",
            ),
            (
                r#"{"code":"1000000","accessToken":"tok-1"}"#,
                Some(latest(NOW - 21 * 60, "57", "230")),
                "stale",
            ),
        ];
        for (login, reading, want) in cases {
            let f = fake();
            null_cells(&f);
            f.login.replace(login.into());
            f.latest.replace(
                reading.unwrap_or(
                    r#"{"code":"2101010","msg":"no such device","success":false}"#.into(),
                ),
            );
            let out = handle(&f, "tool:status");
            assert_eq!(
                (out["ok"].as_bool(), kind(&out)),
                (Some(false), want),
                "{out}"
            );
            assert!(
                out.get("result").is_none(),
                "an answer with no reading: {out}"
            );
            assert!(
                out["error"].as_str().unwrap().starts_with("deye: "),
                "{out}"
            );
        }
    }

    #[test]
    fn status_never_answers_a_cell_that_is_not_its_type() {
        let f = fake();
        handle(&f, "cron");
        f.cells
            .borrow_mut()
            .insert("grid".into(), (json!("yes"), NOW * 1000));
        f.cells
            .borrow_mut()
            .insert("sample".into(), (json!("garbage"), NOW * 1000));
        f.latest
            .replace(r#"{"code":"500","msg":"down","success":false}"#.into());
        let out = handle(&f, "tool:status");
        assert_eq!(kind(&out), "module.exception", "{out}");
    }

    #[test]
    fn cron_with_a_failed_read_and_null_cells_answers_the_failure() {
        let f = fake();
        null_cells(&f);
        f.latest
            .replace(r#"{"code":"500","msg":"down","success":false}"#.into());
        let out = handle(&f, "cron");
        assert_eq!(kind(&out), "module.exception", "{out}");
    }

    #[test]
    fn health_checks_the_serial_and_the_secrets_without_network() {
        let f = fake();
        let out = handle(&f, "health");
        assert_eq!(out["ok"], true, "{out}");

        for sn in [
            "",
            "<серийник>",
            "24000x0001",
            "1234567",
            "12345678901234567",
            " 2400000001",
        ] {
            let mut f = fake();
            f.config.insert("inverter_sn", sn);
            let out = handle(&f, "health");
            assert_eq!(
                (out["ok"].as_bool(), kind(&out)),
                (Some(false), "module.exception"),
                "{sn:?}: {out}"
            );
            assert!(
                out["error"].as_str().unwrap().contains("inverter_sn"),
                "{out}"
            );
        }
        let mut f = fake();
        f.config.clear();
        assert_eq!(kind(&handle(&f, "health")), "module.exception");

        for name in [
            "DEYE_APP_ID",
            "DEYE_APP_SECRET",
            "DEYE_EMAIL",
            "DEYE_PASSWORD",
        ] {
            for value in [None, Some("")] {
                let mut f = fake();
                match value {
                    None => f.secrets.remove(name),
                    Some(v) => f.secrets.insert(name, v),
                };
                let out = handle(&f, "health");
                assert_eq!(kind(&out), "module.exception", "{name} {value:?}: {out}");
                let msg = out["error"].as_str().unwrap();
                assert!(msg.contains(name), "{msg}");
                assert_eq!(f.calls(), 0);
            }
        }
        assert_eq!(f.calls(), 0);
    }

    #[test]
    fn init_and_other_triggers_are_unknown_and_make_no_request() {
        let f = fake();
        for trigger in ["init", "tool:other", "event:x", ""] {
            let out = handle(&f, trigger);
            assert_eq!(out["ok"], false);
            assert_eq!(out["error"], UNKNOWN_TRIGGER, "{trigger}");
        }
        assert_eq!(f.calls(), 0);
        assert!(f.cells.borrow().is_empty());
    }

    #[test]
    fn no_serial_number_is_an_exception_without_network() {
        let mut f = fake();
        f.config.clear();
        let out = handle(&f, "cron");
        assert_eq!(kind(&out), "module.exception");
        assert!(out["error"].as_str().unwrap().contains("inverter_sn"));
        assert_eq!(f.calls(), 0);
    }
}
