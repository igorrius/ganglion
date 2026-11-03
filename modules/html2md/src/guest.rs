//! Module-side glue for the ganglion ABI (ADR-0011): the only host
//! function used is `take`, which copies the call's input in. The
//! manifest grants nothing else.

use std::sync::Mutex;

#[link(wasm_import_module = "ganglion")]
unsafe extern "C" {
    fn take(ptr: u32);
}

/// Keeps the last Output alive until the runner has read it.
static OUT: Mutex<Vec<u8>> = Mutex::new(Vec::new());

/// Called by the runner with the length of the Input it holds; returns the
/// Output's address (high 32 bits) and length.
#[unsafe(no_mangle)]
pub extern "C" fn handle(in_len: u32) -> u64 {
    let mut input = vec![0u8; in_len as usize];
    unsafe { take(input.as_mut_ptr() as u32) };
    let out = crate::run(&input);
    let mut slot = OUT.lock().unwrap_or_else(|e| e.into_inner());
    *slot = out;
    ((slot.as_ptr() as u64) << 32) | slot.len() as u64
}
