//! Module-side glue for the ganglion ABI: host functions in the "ganglion"
//! import module, JSON across the boundary (see `pdk/guest` for Go).

use crate::host::{Host, Request, Response};
use base64::Engine;
use base64::engine::general_purpose::STANDARD as B64;
use std::sync::Mutex;

#[link(wasm_import_module = "ganglion")]
unsafe extern "C" {
    #[link_name = "log"]
    fn host_log(ptr: u32, len: u32);
    fn now_ms() -> i64;
    fn take(ptr: u32);
    fn http_fetch(ptr: u32, len: u32) -> u32;
    fn state_get(ptr: u32, len: u32) -> i32;
    fn state_put(kptr: u32, klen: u32, vptr: u32, vlen: u32);
    fn secret_get(ptr: u32, len: u32) -> i32;
    fn config_get(ptr: u32, len: u32) -> i32;
    fn cell_report(nptr: u32, nlen: u32, vptr: u32, vlen: u32) -> i32;
    fn cell_get(ptr: u32, len: u32) -> i32;
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
    fn fetch(&self, req: &Request) -> Result<Response, String> {
        let headers: serde_json::Map<String, serde_json::Value> = req
            .headers
            .iter()
            .map(|(k, v)| (k.clone(), v.clone().into()))
            .collect();
        let wire = serde_json::json!({
            "method": req.method, "url": req.url, "headers": headers, "body": B64.encode(&req.body),
        })
        .to_string();
        let n = unsafe { http_fetch(wire.as_ptr() as u32, wire.len() as u32) };
        let resp: serde_json::Value =
            serde_json::from_slice(&take_n(n as usize)).map_err(|e| e.to_string())?;
        if let Some(e) = resp
            .get("error")
            .and_then(|e| e.as_str())
            .filter(|e| !e.is_empty())
        {
            return Err(e.to_string());
        }
        let body = match resp.get("body").and_then(|b| b.as_str()) {
            Some(b) => B64.decode(b).map_err(|e| e.to_string())?,
            None => Vec::new(),
        };
        let status = resp.get("status").and_then(|s| s.as_u64()).unwrap_or(0) as u16;
        let headers = resp
            .get("headers")
            .and_then(|h| h.as_object())
            .map(|h| {
                h.iter()
                    .filter_map(|(k, v)| v.as_str().map(|v| (k.clone(), v.to_string())))
                    .collect()
            })
            .unwrap_or_default();
        Ok(Response {
            status,
            headers,
            body,
        })
    }
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
    fn secret(&self, name: &str) -> Option<String> {
        lookup(secret_get, name)
    }
    fn config(&self, key: &str) -> Option<String> {
        lookup(config_get, key)
    }
    fn cell_report(&self, name: &str, value: &serde_json::Value) -> Result<(), String> {
        let v = value.to_string();
        let code = unsafe {
            cell_report(
                name.as_ptr() as u32,
                name.len() as u32,
                v.as_ptr() as u32,
                v.len() as u32,
            )
        };
        match code {
            0 => Ok(()),
            -2 => Err("value does not fit the cell's type".to_string()),
            _ => Err("cell is not declared by this module".to_string()),
        }
    }
    fn cell_get(&self, id: &str) -> Option<(serde_json::Value, i64)> {
        let raw = lookup(cell_get, id)?;
        let v: serde_json::Value = serde_json::from_str(&raw).ok()?;
        let at = v.get("at_ms")?.as_i64()?;
        Some((v.get("value")?.clone(), at))
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
