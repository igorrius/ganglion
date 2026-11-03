//! A byte-level pass before parsing. It drops what is never content and
//! is the bulk of hostile or heavy pages: the bodies of scripts, styles
//! and other raw-text elements, and `data:` values of attributes (inline
//! images, fonts). It also caps the nesting of elements, because the
//! parser's work grows with the depth of the tree.
//!
//! The pass is approximate (it does not model every rule of HTML
//! parsing); a page it misreads loses some formatting, never safety: the
//! whole module runs in the sandbox.

/// Elements whose body is raw text, never shown as content.
const RAW: [&str; 6] = ["script", "style", "noscript", "template", "textarea", "xmp"];

/// Deepest nesting of elements passed to the parser.
pub const MAX_NESTING: usize = 256;

/// Tags that do not nest: void elements, and elements whose end tag is
/// usually left out (a page of unclosed `<p>` or `<li>` is not deep).
const FLAT: [&str; 22] = [
    "area", "base", "br", "col", "embed", "hr", "img", "input", "link", "meta", "source", "track",
    "wbr", "p", "li", "dt", "dd", "tr", "td", "th", "option", "colgroup",
];

/// Returns src without raw-text bodies and `data:` attribute values, and
/// with start tags nested deeper than MAX_NESTING (and their end tags)
/// left out; their text stays.
pub fn strip(src: &str) -> String {
    let b = src.as_bytes();
    let mut out = Vec::with_capacity(b.len().min(1 << 20));
    let mut nesting = Nesting::default();
    let mut i = 0;
    while i < b.len() {
        let Some(lt) = position(&b[i..], b'<').map(|p| p + i) else {
            out.extend_from_slice(&b[i..]);
            break;
        };
        out.extend_from_slice(&b[i..lt]);
        let Some(gt) = tag_end(b, lt) else {
            out.extend_from_slice(&b[lt..]);
            break;
        };
        let tag = &b[lt..=gt];
        if nesting.keep(tag) {
            strip_data(tag, &mut out);
        }
        i = gt + 1;
        if let Some(name) = RAW.iter().find(|n| opens(tag, n)) {
            let close = format!("</{name}");
            i = find_ignore_case(b, i, close.as_bytes()).unwrap_or(b.len());
        }
    }
    String::from_utf8_lossy(&out).into_owned()
}

/// The index of the '>' that ends the tag starting at lt; a '>' inside a
/// quoted attribute value does not end it. A quote never closed does not
/// hide the rest of the page: the tag then ends at the first '>'.
fn tag_end(b: &[u8], lt: usize) -> Option<usize> {
    let mut i = lt + 1;
    while i < b.len() {
        match b[i] {
            b'>' => return Some(i),
            q @ (b'"' | b'\'') if b[i - 1] == b'=' => match position(&b[i + 1..], q) {
                Some(p) => i += p + 1,
                None => return position(&b[lt..], b'>').map(|p| p + lt),
            },
            _ => {}
        }
        i += 1;
    }
    None
}

/// The first c in s. memchr reads a word (or a SIMD vector) at a time:
/// a plain byte loop costs several times more in WASM, and a hostile
/// page is mostly bytes to skip.
fn position(s: &[u8], c: u8) -> Option<usize> {
    memchr::memchr(c, s)
}

/// The first needle in hay from `from` on, ignoring ASCII case; needle
/// starts with '<'.
fn find_ignore_case(hay: &[u8], from: usize, needle: &[u8]) -> Option<usize> {
    let mut i = from;
    while let Some(p) = memchr::memchr(needle[0], &hay[i..]) {
        let at = i + p;
        if hay.len() - at >= needle.len() && hay[at..at + needle.len()].eq_ignore_ascii_case(needle)
        {
            return Some(at);
        }
        i = at + 1;
    }
    None
}

/// Reports whether tag is a start tag of the element name.
fn opens(tag: &[u8], name: &str) -> bool {
    let n = name.len();
    tag.len() > n + 1
        && tag[1..=n].eq_ignore_ascii_case(name.as_bytes())
        && !tag[n + 1].is_ascii_alphanumeric()
}

/// Writes tag to out with every quoted attribute value that starts with
/// "data:" emptied.
fn strip_data(tag: &[u8], out: &mut Vec<u8>) {
    let mut i = 0;
    while i < tag.len() {
        let c = tag[i];
        if (c == b'"' || c == b'\'') && i > 0 && tag[i - 1] == b'=' {
            let Some(end) = position(&tag[i + 1..], c).map(|p| p + i + 1) else {
                out.extend_from_slice(&tag[i..]);
                return;
            };
            let value = &tag[i + 1..end];
            out.push(c);
            if !(value.len() >= 5 && value[..5].eq_ignore_ascii_case(b"data:")) {
                out.extend_from_slice(value);
            }
            out.push(c);
            i = end + 1;
            continue;
        }
        out.push(c);
        i += 1;
    }
}

/// Nesting counts open elements; start tags past MAX_NESTING are left
/// out, and as many end tags after them.
#[derive(Default)]
struct Nesting {
    depth: usize,
    dropped: usize,
}

impl Nesting {
    fn keep(&mut self, tag: &[u8]) -> bool {
        let end = tag.len() > 1 && tag[1] == b'/';
        let start = if end { 2 } else { 1 };
        let name_len = tag[start..]
            .iter()
            .position(|c| !c.is_ascii_alphanumeric())
            .unwrap_or(tag.len() - start);
        let name = &tag[start..start + name_len];
        if name.is_empty()
            || tag.ends_with(b"/>")
            || FLAT.iter().any(|f| f.as_bytes().eq_ignore_ascii_case(name))
        {
            return true;
        }
        if end {
            if self.dropped > 0 {
                self.dropped -= 1;
                return false;
            }
            self.depth = self.depth.saturating_sub(1);
            return true;
        }
        if self.depth >= MAX_NESTING {
            self.dropped += 1;
            return false;
        }
        self.depth += 1;
        true
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn drops_raw_text_bodies() {
        let got = strip("<p>a</p><SCRIPT type=x>if (a<b) {}</script><style>p{}</STYLE><p>b</p>");
        assert_eq!(
            got,
            "<p>a</p><SCRIPT type=x></script><style></STYLE><p>b</p>"
        );
    }

    #[test]
    fn unclosed_script_drops_the_rest() {
        assert_eq!(strip("<p>a</p><script>x"), "<p>a</p><script>");
    }

    #[test]
    fn empties_data_values() {
        let got = strip(
            r#"<img alt="x" src="data:image/png;base64,AAAA"><a href='DATA:text/html,<b>'>t</a>"#,
        );
        assert_eq!(got, r#"<img alt="x" src=""><a href=''>t</a>"#);
    }

    #[test]
    fn unclosed_quote_does_not_hide_the_page() {
        assert_eq!(
            strip(r#"<a x="y><script>z</script>"#),
            r#"<a x="y><script></script>"#
        );
    }

    #[test]
    fn quoted_angle_bracket_does_not_end_a_tag() {
        assert_eq!(
            strip(r#"<a title="a>b" href="/x">t</a>"#),
            r#"<a title="a>b" href="/x">t</a>"#
        );
    }

    #[test]
    fn caps_nesting() {
        let deep = "<div>".repeat(1000) + "x" + &"</div>".repeat(1000);
        let got = strip(&deep);
        assert_eq!(got.matches("<div>").count(), MAX_NESTING);
        assert_eq!(got.matches("</div>").count(), MAX_NESTING);
        assert!(got.contains('x'));
    }

    #[test]
    fn unclosed_paragraphs_do_not_count() {
        let page = "<p>x".repeat(1000) + "<div>y</div>";
        assert!(strip(&page).contains("<div>y</div>"));
    }

    #[test]
    fn text_with_angle_brackets_stays() {
        assert_eq!(strip("3 < 5 and 6 > 2"), "3 < 5 and 6 > 2");
    }
}
