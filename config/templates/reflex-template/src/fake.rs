//! A host for the unit tests: state, config, cells and a clock the test
//! moves, and the messages the module sent.

use crate::host::{Emit, Host, Notify};
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
    /// How many times `notify` was called.
    pub attempts: Cell<usize>,
    /// The events that went out, as (port, payload).
    pub emitted: RefCell<Vec<(String, serde_json::Value)>>,
    /// What `emit` answers; `Ok` sends.
    pub emit_verdict: Cell<Result<(), Emit>>,
    pub now: Cell<i64>,
}

pub fn fake() -> Fake {
    Fake {
        config: RefCell::new(HashMap::new()),
        state: RefCell::new(HashMap::new()),
        cells: RefCell::new(HashMap::new()),
        sent: RefCell::new(vec![]),
        verdict: Cell::new(Ok(())),
        attempts: Cell::new(0),
        emitted: RefCell::new(vec![]),
        emit_verdict: Cell::new(Ok(())),
        now: Cell::new(NOW),
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
        self.attempts.set(self.attempts.get() + 1);
        let verdict = self.verdict.get();
        if verdict.is_ok() {
            self.sent.borrow_mut().push(text.into());
        }
        verdict
    }
    fn emit(&self, port: &str, payload: &serde_json::Value) -> Result<(), Emit> {
        let verdict = self.emit_verdict.get();
        if verdict.is_ok() {
            self.emitted
                .borrow_mut()
                .push((port.into(), payload.clone()));
        }
        verdict
    }
    fn now_unix(&self) -> i64 {
        self.now.get()
    }
    fn log(&self, _: &str) {}
}
