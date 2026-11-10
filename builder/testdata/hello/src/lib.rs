//! The smallest module the builder's test can build: it has no dependencies,
//! so the build needs no network. `handle` answers every trigger with OK.

/// The answer, kept alive for the runner to read.
static OK: &[u8] = br#"{"ok":true,"result":"hello"}"#;

/// Called by the runner with the length of the Input it holds; returns the
/// Output's address (high 32 bits) and length.
#[unsafe(no_mangle)]
pub extern "C" fn handle(_in_len: u32) -> u64 {
    ((OK.as_ptr() as u64) << 32) | OK.len() as u64
}
