//! The reader template (spec R-RDR-1..4): a module that reads an outside
//! service on a schedule and on its `status` tool, and reports what it read
//! as cells. `logic` says what is read and how; `engine` keeps the rules
//! every reader shares and names no service. The ABI glue is compiled only
//! for wasm32.

pub mod engine;
pub mod host;
pub mod logic;

#[cfg(test)]
mod fake;

#[cfg(target_arch = "wasm32")]
mod guest;

use host::Host;

/// Handles one call: the Input's trigger picks what to do (`engine`); the
/// answer is a ganglion Output as JSON.
pub fn run(h: &dyn Host, input: &[u8]) -> Vec<u8> {
    let trigger = serde_json::from_slice::<serde_json::Value>(input)
        .ok()
        .and_then(|v| v.get("trigger")?.as_str().map(str::to_string))
        .unwrap_or_default();
    engine::handle::<logic::Service>(h, &trigger)
        .to_string()
        .into_bytes()
}
