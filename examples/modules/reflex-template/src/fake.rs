//! A host for the unit tests: state, config, cells and a clock the test
//! moves, and the messages the module sent.

use crate::host::{Host, Notify};
use std::cell::{Cell, RefCell};
use std::collections::HashMap;

pub const NOW: i64 = 1_793_000_000;

pub struct Fake {
    pub config: RefCell<HashMap<String, String>>,
    pub state: RefCell<HashMap<String, String>>,
    /// Cells by id, with the time they changed in ms.
    pub cells: RefCell<HashMap<String, (serde_json::Value, i64)>>,
    /// The messages that went out.
    pub sent: RefCell<Vec<String>>,
    /// What `notify` answers; `Ok` sends.
    pub verdict: Cell<Result<(), Notify>>,
    pub now: Cell<i64>,
}

pub fn fake() -> Fake {
    Fake {
        config: RefCell::new(HashMap::new()),
        state: RefCell::new(HashMap::new()),
        cells: RefCell::new(HashMap::new()),
        sent: RefCell::new(vec![]),
        verdict: Cell::new(Ok(())),
        now: Cell::new(NOW),
    }
}

impl Fake {
    /// Sets the producer's cell, changed `age_min` minutes ago.
    pub fn produce(&self, value: serde_json::Value, age_min: i64) {
        let at = (self.now.get() - age_min * 60) * 1000;
        self.cells
            .borrow_mut()
            .insert("producer.value".into(), (value, at));
    }

    pub fn set_config(&self, key: &str, value: &str) {
        self.config.borrow_mut().insert(key.into(), value.into());
    }
}

impl Host for Fake {
    fn state_get(&self, k: &str) -> Option<String> {
        self.state.borrow().get(k).cloned()
    }
    fn state_put(&self, k: &str, v: &str) {
        self.state.borrow_mut().insert(k.into(), v.into());
    }
    fn config(&self, k: &str) -> Option<String> {
        self.config.borrow().get(k).cloned()
    }
    fn cell_get(&self, id: &str) -> Option<(serde_json::Value, i64)> {
        self.cells.borrow().get(id).cloned()
    }
    fn notify(&self, text: &str) -> Result<(), Notify> {
        let verdict = self.verdict.get();
        if verdict.is_ok() {
            self.sent.borrow_mut().push(text.into());
        }
        verdict
    }
    fn now_unix(&self) -> i64 {
        self.now.get()
    }
    fn log(&self, _: &str) {}
}
