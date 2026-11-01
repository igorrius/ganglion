//! The deye-reader module in Rust: the same logic as `modules/deye` (Go),
//! kept to compare module toolchains (ADR-0011). The ABI glue is compiled
//! only for wasm32.

pub mod logic;

#[cfg(target_arch = "wasm32")]
mod guest;

use logic::{Error, Host};

/// Handles one call: reads the inverter named by config `inverter_sn` and
/// returns a ganglion Output as JSON (`{"ok":…,"result":…,"kind":…,"error":…}`).
pub fn run(h: &dyn Host, _input: &[u8]) -> Vec<u8> {
    let out = match h.config("inverter_sn").filter(|s| !s.is_empty()) {
        None => failure(
            "module.exception",
            "deye: config.inverter_sn is not set in the manifest",
        ),
        Some(sn) => match logic::read(h, &sn) {
            Ok(s) => serde_json::json!({ "ok": true, "result": s }),
            Err(Error::Auth(m)) => {
                failure("auth_failed", &format!("deye: authentication failed: {m}"))
            }
            Err(Error::Stale(m)) | Err(Error::Other(m)) => {
                failure("module.exception", &format!("deye: {m}"))
            }
        },
    };
    out.to_string().into_bytes()
}

fn failure(kind: &str, msg: &str) -> serde_json::Value {
    serde_json::json!({ "ok": false, "kind": kind, "error": msg })
}
