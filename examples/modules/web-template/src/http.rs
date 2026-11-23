//! The HTTP side of a page module: the request the node hands over, the
//! answer it takes back, and the escaping every piece of text that reaches a
//! browser goes through.
//!
//! Shapes (spec R-HTTP-2, ADR-0110; the exact JSON the node implements,
//! `pdk.HTTPRequest` and `pdk.HTTPAnswer`), kept in this one file:
//!
//! - the input is `{"trigger": "http", "payload": {"method": "GET", "path":
//!   "/api/data", "query": "a=1&b=2", "headers": {"accept": "..."}, "body":
//!   "<base64>"}}`; `path` is the path inside the module's prefix and always
//!   one of the declared routes, `query` is the raw query string without `?`,
//!   `headers` holds `accept`, `content-type` and `if-none-match` in lower
//!   case; `query`, `headers` and `body` are left out when empty;
//! - the answer is `{"ok": true, "result": {"status": 200, "headers":
//!   {"content-type": "..."}, "body": "<base64>"}}`; an unknown field is a
//!   contract break (the browser gets 502), and the node keeps only
//!   `content-type`, `cache-control` and `etag`.

use serde_json::{Map, Value, json};

/// What the browser asked for.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Request {
    pub method: String,
    /// The path inside the module's prefix, starting with `/`.
    pub path: String,
    pub query: Vec<(String, String)>,
    /// Lower-case names.
    pub headers: Vec<(String, String)>,
    /// The decoded body bytes.
    pub body: Vec<u8>,
}

impl Request {
    /// Reads the payload of an `http` input; `None` when it has no method or
    /// path (the module then fails as an exception: the node sent a shape this
    /// template does not know).
    pub fn from_payload(p: &Value) -> Option<Request> {
        let method = p.get("method")?.as_str()?.to_ascii_uppercase();
        let path = p.get("path")?.as_str()?.to_string();
        let query = p
            .get("query")
            .and_then(Value::as_str)
            .map(parse_query)
            .unwrap_or_default();
        let headers = match p.get("headers") {
            Some(Value::Object(o)) => o
                .iter()
                .filter_map(|(k, v)| v.as_str().map(|v| (k.to_ascii_lowercase(), v.to_string())))
                .collect(),
            _ => Vec::new(),
        };
        let body = match p.get("body").and_then(Value::as_str) {
            Some(b) => base64_decode(b)?,
            None => Vec::new(),
        };
        Some(Request {
            method,
            path,
            query,
            headers,
            body,
        })
    }

    pub fn header(&self, name: &str) -> Option<&str> {
        self.headers
            .iter()
            .find(|(k, _)| k == name)
            .map(|(_, v)| v.as_str())
    }

    /// The first value of a query parameter.
    pub fn param(&self, name: &str) -> Option<&str> {
        self.query
            .iter()
            .find(|(k, _)| k == name)
            .map(|(_, v)| v.as_str())
    }
}

/// `a=1&b=2` into pairs, with no percent-decoding: the template reads
/// numbers and short words only.
fn parse_query(s: &str) -> Vec<(String, String)> {
    s.trim_start_matches('?')
        .split('&')
        .filter(|p| !p.is_empty())
        .map(|p| match p.split_once('=') {
            Some((k, v)) => (k.to_string(), v.to_string()),
            None => (p.to_string(), String::new()),
        })
        .collect()
}

/// What the module answers.
#[derive(Debug, Clone, PartialEq)]
pub struct Response {
    pub status: u16,
    pub headers: Vec<(&'static str, String)>,
    pub body: String,
}

impl Response {
    pub fn new(status: u16, content_type: &'static str, cache: &str, body: String) -> Response {
        Response {
            status,
            headers: vec![
                ("content-type", content_type.to_string()),
                ("cache-control", cache.to_string()),
            ],
            body,
        }
    }

    pub fn with_etag(mut self, etag: String) -> Response {
        self.headers.push(("etag", etag));
        self
    }

    pub fn text(status: u16, body: &str) -> Response {
        Response::new(
            status,
            "text/plain; charset=utf-8",
            "no-store",
            body.to_string(),
        )
    }

    /// The header's value, by name.
    pub fn header(&self, name: &str) -> Option<&str> {
        self.headers
            .iter()
            .find(|(k, _)| *k == name)
            .map(|(_, v)| v.as_str())
    }

    /// The ganglion Output that carries this answer.
    pub fn to_output(&self) -> Value {
        let mut headers = Map::new();
        for (k, v) in &self.headers {
            headers.insert((*k).to_string(), Value::String(v.clone()));
        }
        json!({
            "ok": true,
            "result": { "status": self.status, "headers": headers, "body": base64_encode(self.body.as_bytes()) },
        })
    }
}

const B64: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";

/// Standard base64 with padding, as Go's `[]byte` is written in JSON.
pub fn base64_encode(data: &[u8]) -> String {
    let mut out = String::with_capacity(data.len().div_ceil(3) * 4);
    for c in data.chunks(3) {
        let n = (u32::from(c[0]) << 16)
            | (u32::from(*c.get(1).unwrap_or(&0)) << 8)
            | u32::from(*c.get(2).unwrap_or(&0));
        for i in 0..4 {
            if i <= c.len() {
                out.push(B64[(n >> (18 - 6 * i) & 63) as usize] as char);
            } else {
                out.push('=');
            }
        }
    }
    out
}

/// The bytes of a padded standard base64 text; `None` when it is not one.
pub fn base64_decode(s: &str) -> Option<Vec<u8>> {
    let b = s.as_bytes();
    if !b.len().is_multiple_of(4) {
        return None;
    }
    let mut out = Vec::with_capacity(b.len() / 4 * 3);
    for (k, q) in b.chunks(4).enumerate() {
        let last = k == b.len() / 4 - 1;
        let pad = q.iter().rev().take_while(|c| **c == b'=').count();
        if pad > 2 || (pad > 0 && !last) {
            return None;
        }
        let mut n = 0u32;
        for (i, c) in q.iter().enumerate() {
            let v = if i >= 4 - pad {
                0
            } else {
                B64.iter().position(|x| x == c)? as u32
            };
            n = (n << 6) | v;
        }
        let bytes = [(n >> 16) as u8, (n >> 8) as u8, n as u8];
        out.extend_from_slice(&bytes[..3 - pad]);
    }
    Some(out)
}

/// Escapes text for HTML, in element content and in quoted attributes.
pub fn escape_html(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    for c in s.chars() {
        match c {
            '&' => out.push_str("&amp;"),
            '<' => out.push_str("&lt;"),
            '>' => out.push_str("&gt;"),
            '"' => out.push_str("&quot;"),
            '\'' => out.push_str("&#39;"),
            c => out.push(c),
        }
    }
    out
}

/// JSON that is safe even when a browser takes it for HTML: the characters
/// that could open a tag or an entity are written as `\u` escapes, which only
/// ever occur inside strings, so the document means the same.
pub fn json_body(v: &Value) -> String {
    let s = v.to_string();
    let mut out = String::with_capacity(s.len());
    for c in s.chars() {
        match c {
            '<' => out.push_str("\\u003c"),
            '>' => out.push_str("\\u003e"),
            '&' => out.push_str("\\u0026"),
            '\u{2028}' => out.push_str("\\u2028"),
            '\u{2029}' => out.push_str("\\u2029"),
            c => out.push(c),
        }
    }
    out
}

/// A strong validator for a fixed body (FNV-1a, 64 bit).
pub fn etag(body: &str) -> String {
    let mut h: u64 = 0xcbf29ce484222325;
    for b in body.bytes() {
        h ^= u64::from(b);
        h = h.wrapping_mul(0x100000001b3);
    }
    format!("\"{h:016x}\"")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn html_escaping_covers_tags_quotes_and_entities() {
        assert_eq!(
            escape_html(r#"<img src=x onerror="a('b')">&"#),
            "&lt;img src=x onerror=&quot;a(&#39;b&#39;)&quot;&gt;&amp;"
        );
    }

    #[test]
    fn json_never_carries_a_raw_angle_bracket_or_ampersand() {
        let v = json!({"text": "</script><b>&amp;\u{2028}"});
        let s = json_body(&v);
        assert!(!s.contains('<') && !s.contains('>') && !s.contains('&'));
        assert!(!s.contains('\u{2028}'));
        let back: Value = serde_json::from_str(&s).unwrap();
        assert_eq!(back, v, "the document means the same");
    }

    #[test]
    fn a_request_reads_the_raw_query_the_headers_and_a_base64_body() {
        let a = Request::from_payload(&json!({
            "method": "get", "path": "/x", "query": "hours=6&b",
            "headers": {"If-None-Match": "\"v\""}, "body": "aGk="
        }))
        .unwrap();
        assert_eq!(a.method, "GET");
        assert_eq!(a.param("hours"), Some("6"));
        assert_eq!(a.param("b"), Some(""));
        assert_eq!(a.header("if-none-match"), Some("\"v\""));
        assert_eq!(a.body, b"hi");
        let b = Request::from_payload(&json!({"method": "GET", "path": "/"})).unwrap();
        assert!(b.query.is_empty() && b.headers.is_empty() && b.body.is_empty());
        assert!(Request::from_payload(&json!({"path": "/"})).is_none());
        assert!(
            Request::from_payload(&json!({"method": "GET", "path": "/", "body": "%%"})).is_none()
        );
    }

    #[test]
    fn base64_matches_the_standard_vectors_both_ways() {
        for (raw, enc) in [
            ("", ""),
            ("f", "Zg=="),
            ("fo", "Zm8="),
            ("foo", "Zm9v"),
            ("foob", "Zm9vYg=="),
        ] {
            assert_eq!(base64_encode(raw.as_bytes()), enc);
            assert_eq!(base64_decode(enc).unwrap(), raw.as_bytes());
        }
        let all: Vec<u8> = (0..=255).collect();
        assert_eq!(base64_decode(&base64_encode(&all)).unwrap(), all);
        for bad in ["Zg=", "Z===", "Zg==Zm8=", "Zm 9", "Zm9v!"] {
            assert!(base64_decode(bad).is_none(), "{bad}");
        }
    }

    #[test]
    fn the_answer_is_an_output_with_status_headers_and_body() {
        let r = Response::text(404, "not found");
        let v = r.to_output();
        assert_eq!(v["ok"], true);
        assert_eq!(v["result"]["status"], 404);
        assert_eq!(v["result"]["body"], base64_encode(b"not found"));
        let keys: Vec<&String> = v["result"].as_object().unwrap().keys().collect();
        assert_eq!(
            keys,
            ["body", "headers", "status"],
            "no field outside the contract"
        );
        assert_eq!(v["result"]["headers"]["cache-control"], "no-store");
    }

    #[test]
    fn the_etag_follows_the_body() {
        assert_eq!(etag("a"), etag("a"));
        assert_ne!(etag("a"), etag("b"));
    }
}
