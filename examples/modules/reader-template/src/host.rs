//! What the module may ask of the node: the ganglion host functions as a
//! trait, so the logic runs under `cargo test` with a fake and inside the
//! node with the glue of `guest.rs`.

use serde::{Deserialize, Serialize};

/// Why an event did not go out (the codes of the `emit` host function).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Emit {
    /// -1: the manifest declares no such port under `ports.emits`.
    Denied,
    /// -2: the payload does not fit the port's shape; the whole run fails as
    /// `module.format`.
    Format,
    /// -3: not in this run: `init`, `health` and a build's check send nothing.
    NotHere,
    /// -4: the payload is over 64 KiB, the run already sent 100 events, or
    /// the node could not publish it.
    Failed,
}

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
    /// Reads an own cell by name or a listed one by id, with the time it
    /// last changed in ms.
    fn cell_get(&self, id: &str) -> Option<(serde_json::Value, i64)>;
    /// Publishes an event on one of the module's `ports.emits`, by name.
    fn emit(&self, port: &str, payload: &serde_json::Value) -> Result<(), Emit>;
    fn now_unix(&self) -> i64;
    fn log(&self, msg: &str);
}
