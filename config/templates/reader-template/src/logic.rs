//! What this reader reads and how. This file and `manifest.yaml` are the two
//! the agent changes; `engine.rs` calls what is here through `Source` and
//! keeps the rules every reader shares (the schedule, HTTP 429, stale data, a
//! refused login, the status tool).
//!
//! The example service: `GET https://api.example.com/v1/stations/<station>/latest`
//! with a bearer token answers `{"ts": 1793000000, "value": 21.5, "online": true}`.

use crate::engine::Source;
use crate::host::Request;
use serde::Serialize;
use serde_json::{Value, json};

/// The service's address. Its host must be in the manifest's `http.fetch`.
const API: &str = "https://api.example.com/v1";

/// The module's settings, from its `config` in the node's file.
pub struct Settings {
    pub station: String,
}

/// One reading. `ts` is when the service took it, in Unix seconds; the cell
/// `sample` holds the whole of it, as the manifest declares.
#[derive(Debug, Clone, PartialEq, Serialize)]
pub struct Reading {
    pub ts: i64,
    pub value: f64,
    pub online: bool,
}

/// The reader of the example service.
pub struct Service;

impl Source for Service {
    type Settings = Settings;
    type Reading = Reading;

    /// The same names as the manifest's `secret.get`.
    const SECRETS: &'static [&'static str] = &["API_TOKEN"];

    /// A reading older than this is not reported.
    const STALE_AFTER_SECS: i64 = 20 * 60;

    /// Checked without network, by `health` too: say what is wrong in words
    /// the owner can act on.
    fn settings(config: &dyn Fn(&str) -> Option<String>) -> Result<Settings, String> {
        let station = config("station").unwrap_or_default();
        let fits = (1..=64).contains(&station.len())
            && station
                .bytes()
                .all(|b| b.is_ascii_lowercase() || b.is_ascii_digit() || b == b'-');
        if !fits {
            return Err(
                "config.station is not a station id (1 to 64 of a-z, 0-9 and -)".to_string(),
            );
        }
        Ok(Settings { station })
    }

    /// The one request of a read. A secret goes into a header or the body,
    /// never into the address.
    fn request(s: &Settings, secret: &dyn Fn(&str) -> String) -> Request {
        Request {
            method: "GET".into(),
            url: format!("{API}/stations/{}/latest", s.station),
            headers: vec![(
                "Authorization".into(),
                format!("Bearer {}", secret("API_TOKEN")),
            )],
            body: Vec::new(),
        }
    }

    /// Turns the answer into a reading, or says which field is missing. Only
    /// numbers, flags and short fixed values leave the module: never text
    /// the service sent.
    fn parse(body: &[u8]) -> Result<Reading, String> {
        let v: Value = serde_json::from_slice(body).map_err(|e| format!("bad JSON: {e}"))?;
        let ts = v
            .get("ts")
            .and_then(Value::as_i64)
            .ok_or("the answer lacks ts")?;
        let value = v
            .get("value")
            .and_then(Value::as_f64)
            .ok_or("the answer lacks value")?;
        let online = v
            .get("online")
            .and_then(Value::as_bool)
            .ok_or("the answer lacks online")?;
        Ok(Reading { ts, value, online })
    }

    fn taken_at(r: &Reading) -> i64 {
        r.ts
    }

    /// The cells besides `sample`, by the names the manifest reports.
    fn cells(r: &Reading) -> Vec<(&'static str, Value)> {
        vec![("value", json!(r.value)), ("online", json!(r.online))]
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn config(station: &'static str) -> impl Fn(&str) -> Option<String> {
        move |k| (k == "station").then(|| station.to_string())
    }

    #[test]
    fn a_station_id_is_checked() {
        assert_eq!(
            Service::settings(&config("roof-1")).unwrap().station,
            "roof-1"
        );
        for bad in ["", "Roof", "a/b", "a b"] {
            assert!(Service::settings(&config(bad)).is_err(), "{bad:?}");
        }
    }

    #[test]
    fn the_request_carries_the_token_in_a_header() {
        let s = Service::settings(&config("roof-1")).unwrap();
        let req = Service::request(&s, &|_| "tok".to_string());
        assert_eq!(req.method, "GET");
        assert_eq!(req.url, "https://api.example.com/v1/stations/roof-1/latest");
        assert!(!req.url.contains("tok"));
        assert_eq!(
            req.headers,
            vec![("Authorization".to_string(), "Bearer tok".to_string())]
        );
    }

    #[test]
    fn an_answer_becomes_a_reading_and_its_cells() {
        let r = Service::parse(br#"{"ts":1793000000,"value":21.5,"online":true}"#).unwrap();
        assert_eq!(
            r,
            Reading {
                ts: 1_793_000_000,
                value: 21.5,
                online: true
            }
        );
        assert_eq!(Service::taken_at(&r), 1_793_000_000);
        assert_eq!(
            Service::cells(&r),
            vec![("value", json!(21.5)), ("online", json!(true))]
        );
    }

    #[test]
    fn a_missing_field_is_named() {
        let err = Service::parse(br#"{"ts":1793000000,"online":true}"#).unwrap_err();
        assert!(err.contains("value"), "{err}");
        assert!(Service::parse(b"<html>").unwrap_err().contains("bad JSON"));
    }
}
