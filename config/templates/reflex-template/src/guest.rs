//! Module-side glue for the ganglion ABI: host functions in the "ganglion"
//! import module, JSON across the boundary (see `pdk/guest` for Go).

use crate::host::{Host, Notify};
use std::sync::Mutex;

#[link(wasm_import_module = "ganglion")]
unsafe extern "C" {
    #[link_name = "log"]
    fn host_log(ptr: u32, len: u32);
    fn now_ms() -> i64;
    fn take(ptr: u32);
    fn state_get(ptr: u32, len: u32) -> i32;
    fn state_put(kptr: u32, klen: u32, vptr: u32, vlen: u32);
    fn config_get(ptr: u32, len: u32) -> i32;
    fn cell_get(ptr: u32, len: u32) -> i32;
    fn notify(ptr: u32, len: u32) -> i32;
}

fn take_n(n: usize) -> Vec<u8> {
    let mut buf = vec![0u8; n];
    unsafe { take(buf.as_mut_ptr() as u32) };
    buf
}

fn lookup(f: unsafe extern "C" fn(u32, u32) -> i32, key: &str) -> Option<String> {
    let n = unsafe { f(key.as_ptr() as u32, key.len() as u32) };
    if n < 0 {
        return None;
    }
    String::from_utf8(take_n(n as usize)).ok()
}

struct OwnHost;

impl Host for OwnHost {
    fn state_get(&self, key: &str) -> Option<String> {
        lookup(state_get, key)
    }
    fn state_put(&self, key: &str, value: &str) {
        unsafe {
            state_put(
                key.as_ptr() as u32,
                key.len() as u32,
                value.as_ptr() as u32,
                value.len() as u32,
            )
        }
    }
    fn config(&self, key: &str) -> Option<String> {
        lookup(config_get, key)
    }
    fn cell_get(&self, id: &str) -> Option<(serde_json::Value, i64)> {
        let raw = lookup(cell_get, id)?;
        let v: serde_json::Value = serde_json::from_str(&raw).ok()?;
        let at = v.get("at_ms")?.as_i64()?;
        Some((v.get("value")?.clone(), at))
    }
    fn notify(&self, text: &str) -> Result<(), Notify> {
        match unsafe { notify(text.as_ptr() as u32, text.len() as u32) } {
            0 => Ok(()),
            -1 => Err(Notify::Denied),
            -3 => Err(Notify::NotHere),
            _ => Err(Notify::Limit),
        }
    }
    fn now_unix(&self) -> i64 {
        (unsafe { now_ms() }) / 1000
    }
    fn log(&self, msg: &str) {
        unsafe { host_log(msg.as_ptr() as u32, msg.len() as u32) }
    }
}

/// Keeps the last Output alive until the runner has read it.
static OUT: Mutex<Vec<u8>> = Mutex::new(Vec::new());

/// Called by the runner with the length of the Input it holds; returns the
/// Output's address (high 32 bits) and length.
#[unsafe(no_mangle)]
pub extern "C" fn handle(in_len: u32) -> u64 {
    let input = take_n(in_len as usize);
    let out = crate::run(&OwnHost, &input);
    let mut slot = OUT.lock().unwrap();
    *slot = out;
    ((slot.as_ptr() as u64) << 32) | slot.len() as u64
}
