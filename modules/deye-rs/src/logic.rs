//! DeyeCloud reading logic, ported from `modules/deye/deye.go`.

use serde::{Deserialize, Serialize};

/// The DeyeCloud EU endpoint.
pub const API: &str = "https://eu1-developer.deyecloud.com/v1.0";

/// An HTTP request made through the host.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct Request {
    pub method: String,
    pub url: String,
    pub headers: Vec<(String, String)>,
    pub body: Vec<u8>,
}

/// The host's answer to a request.
#[derive(Debug, Clone, Default)]
pub struct Response {
    pub status: u16,
    /// Lower-case names, the first value of each.
    pub headers: std::collections::HashMap<String, String>,
    pub body: Vec<u8>,
}

/// Everything the module may ask of the runner.
pub trait Host {
    fn fetch(&self, req: &Request) -> Result<Response, String>;
    fn state_get(&self, key: &str) -> Option<String>;
    fn state_put(&self, key: &str, value: &str);
    fn secret(&self, name: &str) -> Option<String>;
    fn config(&self, key: &str) -> Option<String>;
    /// Sets a cell the manifest declares; `name` has no module prefix.
    fn cell_report(&self, name: &str, value: &serde_json::Value) -> Result<(), String>;
    /// Reads an own cell by name or a listed one by id, with its time in ms.
    fn cell_get(&self, id: &str) -> Option<(serde_json::Value, i64)>;
    fn now_unix(&self) -> i64;
    fn log(&self, msg: &str);
}

/// Why a read failed; maps to the Output kind.
#[derive(Debug, PartialEq)]
pub enum Error {
    /// Credentials missing or rejected; do not retry.
    Auth(String),
    /// Cloud data older than 20 minutes.
    Stale(String),
    /// Anything else.
    Other(String),
}

/// One reading; field names match the samples table of deye.py.
#[derive(Debug, Clone, PartialEq, Serialize)]
pub struct Sample {
    pub ts: i64,
    pub soc: i64,
    pub batt_v: f64,
    pub batt_w: i64,
    pub batt_a: f64,
    pub grid_v: f64,
    pub freq: f64,
    pub grid_w: i64,
    pub inv_w: i64,
    pub temp_dc: f64,
    pub temp_hs: f64,
    pub grid_on: i64,
}

const GRID_ON_VOLTS: f64 = 170.0;
const STALE_AFTER_SECS: i64 = 20 * 60;
const TOKEN_MIN_LEFT_SECS: i64 = 3600;
const DEFAULT_TOKEN_TTL_SECS: i64 = 86_400;
const TOKEN_INVALID: &str = "2101019";

/// Returns the latest sample of the inverter with serial number `sn`.
pub fn read(h: &dyn Host, sn: &str) -> Result<Sample, Error> {
    let tok = token(h)?;
    let body = serde_json::json!({ "deviceList": [sn] });
    let r = post(h, "/device/latest", &body, Some(&tok))?;
    if r.get("success").and_then(|v| v.as_bool()) != Some(true) {
        let code = code(&r);
        if code == TOKEN_INVALID {
            h.state_put("token", "");
        }
        return Err(Error::Other(format!(
            "cloud read failed: {code} {}",
            text(&r, "msg")
        )));
    }
    let dev = r
        .get("deviceDataList")
        .and_then(|d| d.get(0))
        .ok_or_else(|| Error::Other("cloud returned no device data".into()))?;
    let ts = number(dev.get("collectionTime"))
        .ok_or_else(|| Error::Other("bad collectionTime".into()))? as i64;
    let age = h.now_unix() - ts;
    if age > STALE_AFTER_SECS {
        return Err(Error::Stale(format!(
            "cloud data is stale ({} min old)",
            age / 60
        )));
    }
    let list = dev
        .get("dataList")
        .and_then(|l| l.as_array())
        .cloned()
        .unwrap_or_default();
    let field = |key: &str| -> Result<f64, Error> {
        list.iter()
            .find(|d| d.get("key").and_then(|k| k.as_str()) == Some(key))
            .and_then(|d| number(d.get("value")))
            .ok_or_else(|| Error::Other(format!("cloud data lacks {key}")))
    };
    let mut s = Sample {
        ts,
        soc: field("SOC")? as i64,
        batt_v: field("BatteryVoltage")?,
        batt_w: field("BatteryPower")? as i64,
        batt_a: field("BatteryCurrent")?,
        grid_v: field("GridVoltageL1L2")?,
        freq: field("GridFrequency")?,
        grid_w: field("TotalGridPower")? as i64,
        inv_w: field("InverterOutputPowerL1L2")? as i64,
        temp_dc: field("DC Temperature")?,
        temp_hs: field("AC Temperature")?,
        grid_on: 0,
    };
    if s.grid_v > GRID_ON_VOLTS {
        s.grid_on = 1;
    }
    Ok(s)
}

/// A cached access token, or a fresh login. A rejected login is remembered
/// per credential set, so a wrong password costs one attempt.
fn token(h: &dyn Host) -> Result<String, Error> {
    let get = |k: &str| {
        h.secret(k)
            .ok_or_else(|| Error::Auth(format!("{k} is not set")))
    };
    let (app_id, app_secret, email, password) = (
        get("DEYE_APP_ID")?,
        get("DEYE_APP_SECRET")?,
        get("DEYE_EMAIL")?,
        get("DEYE_PASSWORD")?,
    );
    let fp = hex(&sha256(format!("{email}\0{password}").as_bytes()))[..16].to_string();
    if h.state_get("auth_failed_fp").as_deref() == Some(fp.as_str()) {
        return Err(Error::Auth(
            "DeyeCloud rejected these credentials earlier".into(),
        ));
    }
    if let (Some(tok), Some(tok_fp), Some(exp)) = (
        h.state_get("token"),
        h.state_get("token_fp"),
        h.state_get("token_exp"),
    ) && !tok.is_empty()
        && tok_fp == fp
        && exp.parse::<i64>().unwrap_or(0) - h.now_unix() > TOKEN_MIN_LEFT_SECS
    {
        return Ok(tok);
    }
    let body = serde_json::json!({
        "appSecret": app_secret,
        "email": email,
        "password": hex(&sha256(password.as_bytes())),
    });
    let r = post(h, &format!("/account/token?appId={app_id}"), &body, None)?;
    let tok = text(&r, "accessToken");
    if tok.is_empty() {
        h.state_put("auth_failed_fp", &fp);
        return Err(Error::Auth(format!(
            "DeyeCloud login failed: {} {}",
            code(&r),
            text(&r, "msg")
        )));
    }
    let ttl = number(r.get("expiresIn"))
        .filter(|s| *s > 0.0)
        .map(|s| s as i64)
        .unwrap_or(DEFAULT_TOKEN_TTL_SECS);
    h.state_put("token", &tok);
    h.state_put("token_fp", &fp);
    h.state_put("token_exp", &(h.now_unix() + ttl).to_string());
    Ok(tok)
}

fn post(
    h: &dyn Host,
    path: &str,
    body: &serde_json::Value,
    tok: Option<&str>,
) -> Result<serde_json::Value, Error> {
    let mut headers = vec![("Content-Type".to_string(), "application/json".to_string())];
    if let Some(t) = tok {
        headers.push(("Authorization".into(), format!("bearer {t}")));
    }
    let req = Request {
        method: "POST".into(),
        url: format!("{API}{path}"),
        headers,
        body: body.to_string().into_bytes(),
    };
    let resp = h
        .fetch(&req)
        .map_err(|e| Error::Other(format!("cloud request {path} failed: {e}")))?;
    if !(200..300).contains(&resp.status) {
        return Err(Error::Other(format!(
            "cloud request {path} failed: HTTP {}",
            resp.status
        )));
    }
    serde_json::from_slice(&resp.body)
        .map_err(|e| Error::Other(format!("cloud request {path}: bad JSON: {e}")))
}

/// Reads a number that DeyeCloud may send as a JSON number or a string.
fn number(v: Option<&serde_json::Value>) -> Option<f64> {
    match v? {
        serde_json::Value::Number(n) => n.as_f64(),
        serde_json::Value::String(s) => s.trim().parse().ok(),
        _ => None,
    }
}

fn text(v: &serde_json::Value, key: &str) -> String {
    v.get(key)
        .and_then(|x| x.as_str())
        .unwrap_or_default()
        .to_string()
}

/// DeyeCloud's code field may be a string or a number.
fn code(v: &serde_json::Value) -> String {
    match v.get("code") {
        Some(serde_json::Value::String(s)) => s.clone(),
        Some(other) => other.to_string(),
        None => String::new(),
    }
}

fn sha256(data: &[u8]) -> Vec<u8> {
    use sha2::Digest;
    sha2::Sha256::digest(data).to_vec()
}

fn hex(bytes: &[u8]) -> String {
    bytes.iter().map(|b| format!("{b:02x}")).collect()
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::cell::RefCell;
    use std::collections::HashMap;

    const NOW: i64 = 1_793_000_000;
    const SN: &str = "2400000001";

    struct Fake {
        secrets: HashMap<&'static str, &'static str>,
        state: RefCell<HashMap<String, String>>,
        login: RefCell<String>,
        latest: RefCell<String>,
        urls: RefCell<Vec<String>>,
        bodies: RefCell<Vec<String>>,
    }

    fn latest(collected: i64, soc: &str, grid_v: &str) -> String {
        format!(
            r#"{{"code":"1000000","success":true,"deviceDataList":[{{"collectionTime":{collected},"dataList":[
            {{"key":"SOC","value":"{soc}"}},{{"key":"BatteryVoltage","value":"52.31"}},
            {{"key":"BatteryPower","value":"-1250"}},{{"key":"BatteryCurrent","value":"-23.9"}},
            {{"key":"GridVoltageL1L2","value":"{grid_v}"}},{{"key":"GridFrequency","value":"50.01"}},
            {{"key":"TotalGridPower","value":"1830"}},{{"key":"InverterOutputPowerL1L2","value":"540"}},
            {{"key":"DC Temperature","value":"31.5"}},{{"key":"AC Temperature","value":"38.2"}}]}}]}}"#
        )
    }

    fn fake() -> Fake {
        Fake {
            secrets: HashMap::from([
                ("DEYE_APP_ID", "app-1"),
                ("DEYE_APP_SECRET", "s3cret"),
                ("DEYE_EMAIL", "me@example.com"),
                ("DEYE_PASSWORD", "pw"),
            ]),
            state: RefCell::new(HashMap::new()),
            login: RefCell::new(
                r#"{"code":"1000000","accessToken":"tok-1","expiresIn":"5183999"}"#.into(),
            ),
            latest: RefCell::new(latest(NOW - 180, "57", "230.4")),
            urls: RefCell::new(vec![]),
            bodies: RefCell::new(vec![]),
        }
    }

    impl Host for Fake {
        fn fetch(&self, req: &Request) -> Result<Response, String> {
            self.urls.borrow_mut().push(req.url.clone());
            self.bodies
                .borrow_mut()
                .push(String::from_utf8(req.body.clone()).unwrap());
            let body = if req.url.contains("/account/token") {
                self.login.borrow().clone()
            } else {
                assert!(
                    req.headers
                        .iter()
                        .any(|(k, v)| k == "Authorization" && v.starts_with("bearer "))
                );
                self.latest.borrow().clone()
            };
            Ok(Response {
                status: 200,
                headers: Default::default(),
                body: body.into_bytes(),
            })
        }
        fn cell_report(&self, _: &str, _: &serde_json::Value) -> Result<(), String> {
            Err("no cells in this fake".into())
        }
        fn cell_get(&self, _: &str) -> Option<(serde_json::Value, i64)> {
            None
        }
        fn state_get(&self, k: &str) -> Option<String> {
            self.state.borrow().get(k).cloned()
        }
        fn state_put(&self, k: &str, v: &str) {
            self.state.borrow_mut().insert(k.into(), v.into());
        }
        fn secret(&self, n: &str) -> Option<String> {
            self.secrets
                .get(n)
                .filter(|v| !v.is_empty())
                .map(|v| v.to_string())
        }
        fn config(&self, _: &str) -> Option<String> {
            None
        }
        fn now_unix(&self) -> i64 {
            NOW
        }
        fn log(&self, _: &str) {}
    }

    fn logins(f: &Fake) -> usize {
        f.urls
            .borrow()
            .iter()
            .filter(|u| u.contains("/account/token"))
            .count()
    }

    #[test]
    fn logs_in_and_parses_sample() {
        let f = fake();
        let s = read(&f, SN).unwrap();
        assert_eq!(
            s,
            Sample {
                ts: NOW - 180,
                soc: 57,
                batt_v: 52.31,
                batt_w: -1250,
                batt_a: -23.9,
                grid_v: 230.4,
                freq: 50.01,
                grid_w: 1830,
                inv_w: 540,
                temp_dc: 31.5,
                temp_hs: 38.2,
                grid_on: 1,
            }
        );
        assert!(f.urls.borrow()[0].ends_with("appId=app-1"));
        // sha256("pw")
        assert!(
            f.bodies.borrow()[0]
                .contains("30c952fab122c3f9759f02a6d95c3758b246b4fee239957b2d4fee46e26170c4")
        );
    }

    #[test]
    fn reuses_cached_token() {
        let f = fake();
        read(&f, SN).unwrap();
        read(&f, SN).unwrap();
        assert_eq!(logins(&f), 1);
    }

    #[test]
    fn rejected_login_is_not_retried() {
        let mut f = fake();
        *f.login.borrow_mut() = r#"{"code":"2101006","msg":"password error"}"#.into();
        assert!(matches!(read(&f, SN), Err(Error::Auth(_))));
        assert!(matches!(read(&f, SN), Err(Error::Auth(_))));
        assert_eq!(logins(&f), 1);
        f.secrets.insert("DEYE_PASSWORD", "fixed");
        *f.login.borrow_mut() =
            r#"{"code":"1000000","accessToken":"tok-2","expiresIn":86400}"#.into();
        read(&f, SN).unwrap();
    }

    #[test]
    fn missing_secret_is_auth_error_without_network() {
        let mut f = fake();
        f.secrets.insert("DEYE_APP_SECRET", "");
        assert!(matches!(read(&f, SN), Err(Error::Auth(_))));
        assert!(f.urls.borrow().is_empty());
    }

    #[test]
    fn invalid_token_is_dropped() {
        let f = fake();
        *f.latest.borrow_mut() =
            r#"{"code":"2101019","msg":"token invalid","success":false}"#.into();
        assert!(matches!(read(&f, SN), Err(Error::Other(_))));
        *f.latest.borrow_mut() = latest(NOW, "60", "231");
        read(&f, SN).unwrap();
        assert_eq!(logins(&f), 2);
    }

    #[test]
    fn stale_data_is_an_error() {
        let f = fake();
        *f.latest.borrow_mut() = latest(NOW - 21 * 60, "57", "230");
        assert!(matches!(read(&f, SN), Err(Error::Stale(_))));
    }

    #[test]
    fn grid_off_at_low_voltage() {
        let f = fake();
        *f.latest.borrow_mut() = latest(NOW - 60, "41", "0.0");
        assert_eq!(read(&f, SN).unwrap().grid_on, 0);
    }

    #[test]
    fn missing_field_is_an_error() {
        let f = fake();
        *f.latest.borrow_mut() =
            latest(NOW, "57", "230").replace(r#""key":"SOC""#, r#""key":"SOC_renamed""#);
        match read(&f, SN) {
            Err(Error::Other(m)) => assert!(m.contains("SOC")),
            other => panic!("want an error naming SOC, got {other:?}"),
        }
    }
}
