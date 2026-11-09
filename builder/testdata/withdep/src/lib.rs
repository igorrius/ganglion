//! A module with one dependency from crates.io, so the builder's test
//! exercises cargo fetching and unpacking a crate in the sandbox (cargo sets
//! the mode of what it unpacks, ADR-0064). `handle` answers OK.

/// The answer, kept alive for the runner to read.
static OK: &[u8] = br#"{"ok":true,"result":"withdep"}"#;

/// The number of digits of n, through the dependency.
pub fn digits(n: u32) -> usize {
    itoa::Buffer::new().format(n).len()
}

/// Called by the runner with the length of the Input it holds; returns the
/// Output's address (high 32 bits) and length.
#[unsafe(no_mangle)]
pub extern "C" fn handle(_in_len: u32) -> u64 {
    ((OK.as_ptr() as u64) << 32) | OK.len() as u64
}
