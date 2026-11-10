//! A host for the unit tests: canned DeyeCloud answers, state, cells and a
//! clock the test moves. The credentials are made up.

use crate::host::{Host, Request, Response};
use std::cell::{Cell, RefCell};
use std::collections::HashMap;

pub const NOW: i64 = 1_793_000_000;
pub const SN: &str = "2400000001";

pub struct Fake {
    pub secrets: HashMap<&'static str, &'static str>,
    pub config: HashMap<&'static str, &'static str>,
    pub state: RefCell<HashMap<String, String>>,
    /// Cells by short name, with the time they were reported in ms.
    pub cells: RefCell<HashMap<String, (serde_json::Value, i64)>>,
    pub login: RefCell<String>,
    pub latest: RefCell<String>,
    /// The status and headers of the /device/latest answer.
    pub latest_status: Cell<u16>,
    pub latest_headers: RefCell<HashMap<String, String>>,
    pub urls: RefCell<Vec<String>>,
    pub bodies: RefCell<Vec<String>>,
    pub now: Cell<i64>,
}

/// A /device/latest answer collected at `collected`.
pub fn latest(collected: i64, soc: &str, grid_v: &str) -> String {
    format!(
        r#"{{"code":"1000000","success":true,"deviceDataList":[{{"collectionTime":{collected},"dataList":[
        {{"key":"SOC","value":"{soc}"}},{{"key":"BatteryVoltage","value":"52.31"}},
        {{"key":"BatteryPower","value":"-1250"}},{{"key":"BatteryCurrent","value":"-23.9"}},
        {{"key":"GridVoltageL1L2","value":"{grid_v}"}},{{"key":"GridFrequency","value":"50.01"}},
        {{"key":"TotalGridPower","value":"1830"}},{{"key":"InverterOutputPowerL1L2","value":"540"}},
        {{"key":"DC Temperature","value":"31.5"}},{{"key":"AC Temperature","value":"38.2"}}]}}]}}"#
    )
}

/// A host whose cloud answers a login and a reading three minutes old.
pub fn fake() -> Fake {
    Fake {
        secrets: HashMap::from([
            ("DEYE_APP_ID", "app-1"),
            ("DEYE_APP_SECRET", "s3cret"),
            ("DEYE_EMAIL", "me@example.com"),
            ("DEYE_PASSWORD", "pw"),
        ]),
        config: HashMap::from([("inverter_sn", SN)]),
        state: RefCell::new(HashMap::new()),
        cells: RefCell::new(HashMap::new()),
        login: RefCell::new(
            r#"{"code":"1000000","accessToken":"tok-1","expiresIn":"5183999"}"#.into(),
        ),
        latest: RefCell::new(latest(NOW - 180, "57", "230.4")),
        latest_status: Cell::new(200),
        latest_headers: RefCell::new(HashMap::new()),
        urls: RefCell::new(vec![]),
        bodies: RefCell::new(vec![]),
        now: Cell::new(NOW),
    }
}

impl Fake {
    /// How many requests reached the cloud.
    pub fn calls(&self) -> usize {
        self.urls.borrow().len()
    }

    /// How many of them were logins.
    pub fn logins(&self) -> usize {
        self.urls
            .borrow()
            .iter()
            .filter(|u| u.contains("/account/token"))
            .count()
    }

    /// The value of an own cell.
    pub fn cell(&self, name: &str) -> Option<serde_json::Value> {
        self.cells.borrow().get(name).map(|(v, _)| v.clone())
    }
}

impl Host for Fake {
    fn fetch(&self, req: &Request) -> Result<Response, String> {
        self.urls.borrow_mut().push(req.url.clone());
        self.bodies
            .borrow_mut()
            .push(String::from_utf8(req.body.clone()).unwrap());
        if req.url.contains("/account/token") {
            return Ok(Response {
                status: 200,
                headers: Default::default(),
                body: self.login.borrow().clone().into_bytes(),
            });
        }
        assert!(
            req.headers
                .iter()
                .any(|(k, v)| k == "Authorization" && v.starts_with("bearer "))
        );
        Ok(Response {
            status: self.latest_status.get(),
            headers: self.latest_headers.borrow().clone(),
            body: self.latest.borrow().clone().into_bytes(),
        })
    }
    fn cell_report(&self, name: &str, value: &serde_json::Value) -> Result<(), String> {
        if !["soc", "grid", "sample"].contains(&name) {
            return Err("cell is not declared by this module".into());
        }
        self.cells
            .borrow_mut()
            .insert(name.into(), (value.clone(), self.now.get() * 1000));
        Ok(())
    }
    fn cell_get(&self, id: &str) -> Option<(serde_json::Value, i64)> {
        self.cells.borrow().get(id).cloned()
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
    fn config(&self, k: &str) -> Option<String> {
        self.config.get(k).map(|v| v.to_string())
    }
    fn now_unix(&self) -> i64 {
        self.now.get()
    }
    fn log(&self, _: &str) {}
}
