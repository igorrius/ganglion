//! A host for the unit tests: one canned answer, state, cells and a clock
//! the test moves. It knows no service; the secrets are made up.

use crate::host::{Host, Request, Response};
use std::cell::{Cell, RefCell};
use std::collections::HashMap;

pub const NOW: i64 = 1_793_000_000;

pub struct Fake {
    pub secrets: RefCell<HashMap<String, String>>,
    pub config: RefCell<HashMap<String, String>>,
    pub state: RefCell<HashMap<String, String>>,
    /// The cells the module may report, as its manifest would declare.
    pub declared: Vec<&'static str>,
    /// Cells by short name, with the time they were reported in ms.
    pub cells: RefCell<HashMap<String, (serde_json::Value, i64)>>,
    /// The answer to the next request.
    pub status: Cell<u16>,
    pub headers: RefCell<HashMap<String, String>>,
    pub body: RefCell<String>,
    /// Every request made.
    pub requests: RefCell<Vec<Request>>,
    pub now: Cell<i64>,
}

/// A host with the given secrets, config and declared cells, whose service
/// answers 200 with `body`.
pub fn fake(
    secrets: &[(&str, &str)],
    config: &[(&str, &str)],
    declared: &[&'static str],
    body: &str,
) -> Fake {
    let map = |pairs: &[(&str, &str)]| {
        pairs
            .iter()
            .map(|(k, v)| (k.to_string(), v.to_string()))
            .collect()
    };
    Fake {
        secrets: RefCell::new(map(secrets)),
        config: RefCell::new(map(config)),
        state: RefCell::new(HashMap::new()),
        declared: declared.to_vec(),
        cells: RefCell::new(HashMap::new()),
        status: Cell::new(200),
        headers: RefCell::new(HashMap::new()),
        body: RefCell::new(body.to_string()),
        requests: RefCell::new(Vec::new()),
        now: Cell::new(NOW),
    }
}

impl Fake {
    /// How many requests reached the service.
    pub fn calls(&self) -> usize {
        self.requests.borrow().len()
    }

    /// The value of an own cell.
    pub fn cell(&self, name: &str) -> Option<serde_json::Value> {
        self.cells.borrow().get(name).map(|(v, _)| v.clone())
    }

    /// Answers the next requests with this status, header and body.
    pub fn answer(&self, status: u16, header: Option<(&str, &str)>, body: &str) {
        self.status.set(status);
        let mut h = self.headers.borrow_mut();
        h.clear();
        if let Some((k, v)) = header {
            h.insert(k.into(), v.into());
        }
        self.body.replace(body.to_string());
    }
}

impl Host for Fake {
    fn fetch(&self, req: &Request) -> Result<Response, String> {
        self.requests.borrow_mut().push(req.clone());
        Ok(Response {
            status: self.status.get(),
            headers: self.headers.borrow().clone(),
            body: self.body.borrow().clone().into_bytes(),
        })
    }
    fn cell_report(&self, name: &str, value: &serde_json::Value) -> Result<(), String> {
        if !self.declared.contains(&name) {
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
            .borrow()
            .get(n)
            .filter(|v| !v.is_empty())
            .cloned()
    }
    fn config(&self, k: &str) -> Option<String> {
        self.config.borrow().get(k).cloned()
    }
    fn now_unix(&self) -> i64 {
        self.now.get()
    }
    fn log(&self, _: &str) {}
}
