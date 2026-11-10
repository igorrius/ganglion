//! The deye-reader module (spec section 8, R-DEYE-1..3): reads a Deye
//! inverter through DeyeCloud every 30 minutes and on the `status` tool,
//! and reports the cells `deye-reader.soc`, `.grid` and `.sample`. The agent
//! copies this example into its workspace, builds it and installs it. The
//! ABI glue is compiled only for wasm32.

pub mod cloud;
pub mod host;
pub mod logic;

#[cfg(test)]
mod fake;

#[cfg(target_arch = "wasm32")]
mod guest;

use host::Host;

/// Handles one call: the Input's trigger picks what to do (`logic`); the
/// answer is a ganglion Output as JSON.
pub fn run(h: &dyn Host, input: &[u8]) -> Vec<u8> {
    let trigger = serde_json::from_slice::<serde_json::Value>(input)
        .ok()
        .and_then(|v| v.get("trigger")?.as_str().map(str::to_string))
        .unwrap_or_default();
    logic::handle(h, &trigger).to_string().into_bytes()
}
