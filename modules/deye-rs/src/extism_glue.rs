//! Glue for Extism: Extism's HTTP, config and log, plus the state and
//! secret host functions the runner adds (see `pdk/extismabi` for Go).

use crate::logic::{Host, Request, Response};
use extism_pdk::*;

#[link(wasm_import_module = "extism:host/user")]
unsafe extern "C" {
    fn state_get(key: u64) -> u64;
    fn state_put(key: u64, value: u64);
    fn secret_get(name: u64) -> u64;
}

fn lookup(f: unsafe extern "C" fn(u64) -> u64, key: &str) -> Option<String> {
    let k = Memory::from_bytes(key).ok()?;
    let off = unsafe { f(k.offset()) };
    k.free();
    if off == 0 {
        return None;
    }
    let v = Memory::find(off)?;
    let s = String::from_utf8(v.to_vec()).ok();
    v.free();
    s
}

struct ExtismHost;

impl Host for ExtismHost {
    fn fetch(&self, req: &Request) -> Result<Response, String> {
        let mut r = HttpRequest::new(&req.url).with_method(&req.method);
        for (k, v) in &req.headers {
            r = r.with_header(k, v);
        }
        let resp =
            http::request::<Vec<u8>>(&r, Some(req.body.clone())).map_err(|e| e.to_string())?;
        Ok(Response {
            status: resp.status_code(),
            body: resp.body(),
        })
    }
    fn state_get(&self, key: &str) -> Option<String> {
        lookup(state_get, key)
    }
    fn state_put(&self, key: &str, value: &str) {
        if let (Ok(k), Ok(v)) = (Memory::from_bytes(key), Memory::from_bytes(value)) {
            unsafe { state_put(k.offset(), v.offset()) };
            k.free();
            v.free();
        }
    }
    fn secret(&self, name: &str) -> Option<String> {
        lookup(secret_get, name)
    }
    fn config(&self, key: &str) -> Option<String> {
        config::get(key).ok().flatten()
    }
    fn now_unix(&self) -> i64 {
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .map(|d| d.as_secs() as i64)
            .unwrap_or(0)
    }
    fn log(&self, msg: &str) {
        info!("{}", msg)
    }
}

#[plugin_fn]
pub fn handle(input: Vec<u8>) -> FnResult<Vec<u8>> {
    Ok(crate::run(&ExtismHost, &input))
}
