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
//! `data:` values are dropped before parsing, and every HTML parse is
//! behind the depth gate (`gate`, ruling R25), which runs the parser
//! itself and refuses a page whose tree would be deeper than MAX_DEPTH;
//! such a page is flattened by the pre-pass and gated again, or read as
//! plain text (`plain`). The extractor's walks are iterative; the
//! serializer and `htmd` are tested at MAX_DEPTH.

mod extract;
mod gate;
mod links;
mod nesting;
mod plain;
/// The pre-pass, public for the depth property test (tests/depth.rs).
pub mod prepass;

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

pub use gate::MAX_DEPTH;
/// The gate without the tree, for the depth property test.
#[doc(hidden)]
pub use gate::{Scripting, fits};

/// The way a page went through the module.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Route {
    /// The page fitted the depth gate and was converted as it is.
    Direct,
    /// Too deep; it fitted after the pre-pass flattened its nesting.
    Flattened,
    /// Too deep even flattened: its text, without a tree.
    Text,
    /// Its tree fitted, but the content handed to the Markdown writer (a
    /// second parse) did not: the content's text.
    ContentText,
}

/// What a conversion parsed: each string handed to an HTML parser, the
/// way it went, and the result. For the depth property test.
#[doc(hidden)]
pub struct Trace {
    pub route: Route,
    /// The page as dom_query parsed it, if it did.
    pub parsed: Option<String>,
    /// The content as htmd parsed it, if it did.
    pub markdown_input: Option<String>,
    pub page: Page,
}

/// Returns the page's title and its main content as Markdown. `url` is
/// the page's address, used to make links absolute.
pub fn convert(html: &str, url: &str) -> Page {
    inspect(html, url).page
}

/// convert, with what it parsed. Every HTML parse is behind the depth
/// gate (ruling R25): the page, else the page flattened by the pre-pass,
/// else its plain text; then the content for htmd, else its plain text.
#[doc(hidden)]
pub fn inspect(html: &str, url: &str) -> Trace {
    let cleaned = prepass::strip(html);
    let (route, parsed, doc) = match gate::parse(&cleaned) {
        Some(doc) => (Route::Direct, cleaned, doc),
        None => {
            // The model's output is parsed as it is written, so a page the
            // model cannot save costs only up to where it fails.
            let mut gate = gate::Gate::new(gate::Gated::default(), gate::Scripting::Off);
            let flat = prepass::flatten(&cleaned, &mut |piece| gate.feed(piece));
            match flat.and_then(|flat| Some((flat, gate.finish()?))) {
                Some((flat, doc)) => (Route::Flattened, flat, doc),
                None => {
                    let (title, text) = plain::text(&cleaned);
                    return Trace {
                        route: Route::Text,
                        parsed: None,
                        markdown_input: None,
                        page: Page {
                            title,
                            markdown: tidy(&text),
                        },
                    };
                }
            }
        }
    };
    let title = extract::title(&doc);
    let root = extract::main_content(&doc);
    let base = links::Base::parse(url);
    links::rewrite(&root, base.as_ref());
    let content = root.html().to_string();
    drop(doc);
    if !gate::fits(&content, gate::Scripting::On) {
        let (_, text) = plain::text(&content);
        return Trace {
            route: Route::ContentText,
            parsed: Some(parsed),
            markdown_input: None,
            page: Page {
                title,
                markdown: tidy(&text),
            },
        };
    }
    let writer = htmd::HtmlToMarkdown::builder()
        .skip_tags(SKIPPED.to_vec())
        .build();
    let markdown = writer.convert(&content).unwrap_or_default();
    Trace {
        route,
        parsed: Some(parsed),
        markdown_input: Some(content),
        page: Page {
            title,
            markdown: tidy(&markdown),
        },
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
    fn hostile_nesting_shapes_convert() {
        for body in [
            "<div/>".repeat(300_000),
            "<div></q>".repeat(200_000),
            "<div>".repeat(5_000),
        ] {
            let p = convert(
                &format!("<title>T</title><p>start</p>{body}<p>end</p>"),
                "https://e.example/",
            );
            assert!(
                p.markdown.contains("end"),
                "{}",
                &p.markdown[..p.markdown.len().min(200)]
            );
        }
    }

    /// Pages nested to exactly MAX_DEPTH: every walk (extractor,
    /// serializer, htmd) handles the deepest tree the gate lets through.
    /// The Go test runs the same pages through the wasm build.
    #[test]
    fn the_deepest_tree_converts() {
        for unit in [
            "<div>",
            "<blockquote>",
            "<ul><li>",
            "<ol><li>",
            "<dl><dd>",
            "<b>",
            "<em>",
            "<section>",
            "<table><tr><td>",
            "<pre>",
            "<code>",
            "<span>",
            "<q>",
        ] {
            let page = |n: usize| {
                format!(
                    "<title>t</title>{}<p>Deep text with a few words, <a href=/x>a link</a>, and more.</p>",
                    unit.repeat(n)
                )
            };
            let n = (1..MAX_DEPTH)
                .take_while(|n| gate::fits(&page(*n), gate::Scripting::Off))
                .last()
                .unwrap();
            let t = inspect(&page(n), "https://e.example/");
            assert_eq!(t.route, Route::Direct, "{unit} x{n}");
            let md = &t.page.markdown;
            assert!(
                md.contains("[a link](https://e.example/x)"),
                "{unit} x{n}: {md}"
            );
            assert!(
                !gate::fits(&page(n + 1), gate::Scripting::Off),
                "{unit} x{n}"
            );
        }
    }

    #[test]
    fn too_deep_pages_keep_their_text() {
        let page = format!(
            "<title>T</title><p>start &amp; go</p>{}<p>end</p>",
            "<ruby><p><rt>".repeat(2000)
        );
        let t = inspect(&page, "https://e.example/");
        assert_eq!(t.route, Route::Text);
        assert_eq!(t.page.title, "T");
        assert!(
            t.page.markdown.starts_with("start & go\n"),
            "{}",
            t.page.markdown
        );
        assert!(t.page.markdown.ends_with("end"), "{}", t.page.markdown);
    }

    #[test]
    fn an_unclosed_div_per_comment_keeps_a_tree() {
        // A thread whose template leaves one div open per comment: too
        // deep for the gate, flat enough after the pre-pass's model. Tags
        // past the model's cap are dropped, their text kept; the tree
        // keeps the links of the comments before it (plain text keeps
        // none). The extractor picks the deepest post as the content.
        let comment = |i: usize| {
            format!(
                "<div class=post><a href=/u{i}>user {i}</a><p>Comment {i} with some words, and more.</p>"
            )
        };
        let page = format!(
            "<title>T</title><h1>Thread</h1>{}",
            (0..400).map(comment).collect::<String>()
        );
        let t = inspect(&page, "https://e.example/");
        assert_eq!(t.route, Route::Flattened);
        let md = &t.page.markdown;
        for want in ["[user 198](https://e.example/u198)", "user 399Comment 399"] {
            assert!(md.contains(want), "{want}: {md}");
        }
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
