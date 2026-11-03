//! The html2md module (ADR-0035): the main content of a web page as
//! Markdown, found in the manner of Readability and written by `htmd`.
//!
//! It is a pure function: the manifest grants no host function, so hostile
//! HTML is parsed here, in the sandbox, not in the core (R-WEB-1,
//! R-RUN-7). Input and output follow the ganglion ABI (ADR-0011):
//! `{"trigger":"tool","payload":{"html":…,"url":…}}` in,
//! `{"ok":true,"result":{"title":…,"markdown":…}}` out.
//!
//! Hostile input must not trap or stall the module: raw-text bodies and
//! `data:` values are dropped before parsing, nesting is capped twice
//! (before parsing and before scoring), and every walk is iterative.

mod extract;
mod links;
mod prepass;

#[cfg(target_arch = "wasm32")]
mod guest;

use serde::{Deserialize, Serialize};

/// The tool call's input.
#[derive(Deserialize, Default)]
struct Payload {
    #[serde(default)]
    html: String,
    #[serde(default)]
    url: String,
}

#[derive(Deserialize)]
struct Input {
    #[serde(default)]
    payload: Payload,
}

/// What the module returns for one page.
#[derive(Serialize, Debug, PartialEq)]
pub struct Page {
    pub title: String,
    pub markdown: String,
}

/// Tags the Markdown writer leaves out: their content is not text for a
/// reader (images would only add addresses).
const SKIPPED: [&str; 9] = [
    "script", "style", "noscript", "iframe", "svg", "template", "img", "picture", "video",
];

/// Returns the page's title and its main content as Markdown. `url` is
/// the page's address, used to make links absolute.
pub fn convert(html: &str, url: &str) -> Page {
    let cleaned = prepass::strip(html);
    let doc = dom_query::Document::from(cleaned.as_str());
    let title = extract::title(&doc);
    let root = extract::main_content(&doc);
    let base = links::Base::parse(url);
    links::rewrite(&root, base.as_ref());
    let writer = htmd::HtmlToMarkdown::builder()
        .skip_tags(SKIPPED.to_vec())
        .build();
    let markdown = writer.convert(&root.html()).unwrap_or_default();
    Page {
        title,
        markdown: tidy(&markdown),
    }
}

/// Handles one call: a JSON Input in, a JSON Output out.
pub fn run(input: &[u8]) -> Vec<u8> {
    let out = match serde_json::from_slice::<Input>(input) {
        Ok(i) => {
            serde_json::json!({ "ok": true, "result": convert(&i.payload.html, &i.payload.url) })
        }
        Err(_) => serde_json::json!({
            "ok": false, "kind": "module.exception", "error": "html2md: payload is not {html, url}",
        }),
    };
    out.to_string().into_bytes()
}

/// Trims the ends of lines, keeps at most one empty line between blocks
/// and removes the padding of table rows (it costs the model tokens).
fn tidy(md: &str) -> String {
    let mut out = String::with_capacity(md.len());
    let mut empty = 0;
    for line in md.lines() {
        let compact;
        let mut line = line.trim_end();
        if line.starts_with('|') {
            compact = compact_row(line);
            line = &compact;
        }
        if line.trim().is_empty() {
            empty += 1;
            continue;
        }
        if !out.is_empty() {
            out.push_str(if empty > 0 { "\n\n" } else { "\n" });
        }
        empty = 0;
        out.push_str(line);
    }
    out
}

/// A table row with single spaces around its cells; a separator row
/// with three dashes per column.
fn compact_row(line: &str) -> String {
    let words = line.split_whitespace().collect::<Vec<_>>().join(" ");
    if words.chars().all(|c| matches!(c, '|' | '-' | ' ' | ':')) {
        return words
            .split('|')
            .map(|cell| {
                if cell.trim().is_empty() {
                    cell.trim().to_string()
                } else {
                    " --- ".to_string()
                }
            })
            .collect::<Vec<_>>()
            .join("|");
    }
    words
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn table_rows_lose_their_padding() {
        assert_eq!(
            tidy("| a    | bb |\n| ---- | -- |\n| ccc  | d  |"),
            "| a | bb |\n| --- | --- |\n| ccc | d |"
        );
    }

    #[test]
    fn tidy_keeps_one_empty_line() {
        assert_eq!(tidy("  \n\na  \n\n\n\nb\nc\n\n"), "a\n\nb\nc");
    }

    #[test]
    fn bad_input_is_a_module_exception() {
        let out: serde_json::Value = serde_json::from_slice(&run(b"not json")).unwrap();
        assert_eq!(out["ok"], false);
        assert_eq!(out["kind"], "module.exception");
    }

    #[test]
    fn converts_a_small_page() {
        let p = convert(
            "<html><head><title>T</title></head><body><article><h1>Head</h1>\
             <p>First paragraph with enough text to count, more than twenty-five characters.</p>\
             <p>See <a href=\"/doc\">the docs</a> and <a href=\"javascript:alert(1)\">this</a>.</p>\
             </article></body></html>",
            "https://example.org/a/b",
        );
        assert_eq!(p.title, "T");
        assert!(p.markdown.contains("# Head"), "{}", p.markdown);
        assert!(
            p.markdown.contains("[the docs](https://example.org/doc)"),
            "{}",
            p.markdown
        );
        assert!(!p.markdown.contains("javascript"), "{}", p.markdown);
    }
}
