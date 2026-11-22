//! What the module may ask of the node: the ganglion host functions as a
//! trait, so the logic runs under `cargo test` with a fake and inside the
//! node with the glue of `guest.rs`.

/// Why a message did not go out.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Notify {
    /// The manifest does not grant `notify`.
    Denied,
    /// Today's limit is used up (or the node could not take it now).
    Limit,
    /// Not in this run: `init` and `health` send nothing.
    NotHere,
}

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

/// Everything the module may ask of the runner.
pub trait Host {
    fn state_get(&self, key: &str) -> Option<String>;
    fn state_put(&self, key: &str, value: &str);
    fn config(&self, key: &str) -> Option<String>;
    /// Reads a listed cell by id, with the time it last changed in ms.
    fn cell_get(&self, id: &str) -> Option<(serde_json::Value, i64)>;
    /// Writes to the owner. The text is made by the module's own code.
    fn notify(&self, text: &str) -> Result<(), Notify>;
    /// Publishes an event on one of the module's `ports.emits`, by name.
    fn emit(&self, port: &str, payload: &serde_json::Value) -> Result<(), Emit>;
    fn now_unix(&self) -> i64;
    fn log(&self, msg: &str);
}
