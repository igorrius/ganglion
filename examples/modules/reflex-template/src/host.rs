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

/// Everything the module may ask of the runner.
pub trait Host {
    fn state_get(&self, key: &str) -> Option<String>;
    fn state_put(&self, key: &str, value: &str);
    fn config(&self, key: &str) -> Option<String>;
    /// Reads a listed cell by id, with the time it last changed in ms.
    fn cell_get(&self, id: &str) -> Option<(serde_json::Value, i64)>;
    /// Writes to the owner. The text is made by the module's own code.
    fn notify(&self, text: &str) -> Result<(), Notify>;
    fn now_unix(&self) -> i64;
    fn log(&self, msg: &str);
}
