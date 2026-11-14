//! What the module may ask of the node: the ganglion host functions as a
//! trait, so the logic runs under `cargo test` with a fake and inside the
//! node with the glue of `guest.rs`.

use serde::{Deserialize, Serialize};

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
    fn now_unix(&self) -> i64;
    fn log(&self, msg: &str);
}
