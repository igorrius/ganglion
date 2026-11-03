//! A byte-level pass before parsing. It drops what is never content and
//! is the bulk of hostile or heavy pages: the bodies of scripts, styles
//! and other raw-text elements, and `data:` values of attributes (inline
//! images, fonts). On request it also flattens nesting with a model of
//! the parser's stack of open elements (`nesting`): the second chance
//! for a page the depth gate refused.
//!
//! The pass is approximate (it does not model every rule of HTML
//! parsing); a page it misreads loses some formatting, never safety: the
//! depth gate (`gate`) checks what is parsed, and the whole module runs
//! in the sandbox.

/// Elements whose body is raw text, never shown as content.
const RAW: [&str; 6] = ["script", "style", "noscript", "template", "textarea", "xmp"];

pub use crate::nesting::MAX_NESTING;
use crate::nesting::{Nesting, Verdict};

/// Returns src without raw-text bodies and `data:` attribute values.
pub fn strip(src: &str) -> String {
    scan(src, None, None).unwrap_or_default()
}

/// Returns src with start tags the model counts deeper than MAX_NESTING
/// (and their end tags) left out; their text stays. src has been through
/// strip. Each piece of output is handed to `take` as it is written (at
/// least PIECE bytes, ending where a tag or a text of src ends); when
/// `take` says no, the pass stops and returns None.
pub fn flatten(src: &str, take: &mut dyn FnMut(&str) -> bool) -> Option<String> {
    scan(src, Some(&mut Nesting::default()), Some(take))
}

/// How much output flatten writes between two calls of `take`.
const PIECE: usize = 4096;

fn scan(
    src: &str,
    mut nesting: Option<&mut Nesting>,
    mut take: Option<&mut dyn FnMut(&str) -> bool>,
) -> Option<String> {
    let b = src.as_bytes();
    let mut out = Vec::with_capacity(b.len());
    let mut taken = 0;
    let mut i = 0;
    while i < b.len() {
        if let Some(take) = take.as_mut()
            && out.len() - taken >= PIECE
        {
            if !take(&String::from_utf8_lossy(&out[taken..])) {
                return None;
            }
            taken = out.len();
        }
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
        let keep = nesting.as_mut().is_none_or(|n| n.tag(tag) == Verdict::Keep);
        if keep {
            strip_data(tag, &mut out);
        }
        i = gt + 1;
        let raw = matches!(tag[1].to_ascii_lowercase(), b's' | b'n' | b't' | b'x');
        if let Some(name) = RAW.iter().filter(|_| raw).find(|n| opens(tag, n)) {
            let close = format!("</{name}");
            i = find_ignore_case(b, i, close.as_bytes()).unwrap_or(b.len());
        }
    }
    if let Some(take) = take
        && !take(&String::from_utf8_lossy(&out[taken..]))
    {
        return None;
    }
    // The output is whole pieces of src (valid UTF-8) minus whole
    // attribute values: valid too, so this does not copy.
    Some(
        String::from_utf8(out)
            .unwrap_or_else(|e| String::from_utf8_lossy(e.as_bytes()).into_owned()),
    )
}

/// The index of the '>' that ends the tag starting at lt; a '>' inside a
/// quoted attribute value does not end it. A quote never closed does not
/// hide the rest of the page: the tag then ends at the first '>'.
fn tag_end(b: &[u8], lt: usize) -> Option<usize> {
    let mut i = lt + 1;
    // Jump from one '>' or quote to the next, a vector at a time.
    while let Some(p) = memchr::memchr3(b'>', b'"', b'\'', &b[i..]) {
        i += p;
        match b[i] {
            b'>' => return Some(i),
            q if b[i - 1] == b'=' => match position(&b[i + 1..], q) {
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
    // Most tags hold no ':' at all, so no "data:" value: copy them whole.
    if position(tag, b':').is_none() {
        out.extend_from_slice(tag);
        return;
    }
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

#[cfg(test)]
mod tests {
    use super::*;

    fn fl(src: &str) -> String {
        flatten(src, &mut |_| true).unwrap()
    }

    #[test]
    fn flatten_stops_when_refused() {
        let page = "<p>x</p>".repeat(10_000);
        let mut pieces = 0;
        let got = flatten(&page, &mut |piece| {
            assert!(piece.ends_with('>'), "{piece}");
            pieces += 1;
            pieces < 3
        });
        assert_eq!((got, pieces), (None, 3));
        let mut all = String::new();
        let got = flatten(&page, &mut |piece| {
            all.push_str(piece);
            true
        });
        assert_eq!(got.as_deref(), Some(page.as_str()));
        assert_eq!(all, page);
    }

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
        let got = fl(&deep);
        assert_eq!(got.matches("<div>").count(), MAX_NESTING);
        assert_eq!(got.matches("</div>").count(), MAX_NESTING);
        assert!(got.contains('x'));
    }

    #[test]
    fn self_closed_divs_count_as_open() {
        // html5ever ignores the slash of a non-void element: <div/> opens a div.
        let got = fl(&"<div/>".repeat(1000));
        assert_eq!(got.matches("<div/>").count(), MAX_NESTING);
    }

    #[test]
    fn stray_end_tags_do_not_close_anything() {
        let got = fl(&"<div></q>".repeat(1000));
        assert_eq!(got.matches("<div>").count(), MAX_NESTING);
        let got = fl(&("<div>".repeat(300) + &"</span>".repeat(300) + &"<div>".repeat(300)));
        assert_eq!(got.matches("<div>").count(), MAX_NESTING);
    }

    #[test]
    fn formatting_end_tags_close_only_their_own_top() {
        // </b> runs the adoption agency: the div stays, the tree deepens.
        assert_eq!(
            fl(&"<b><div></b>".repeat(1000)).matches("<div>").count(),
            MAX_NESTING / 2
        );
        assert_eq!(fl(&"<b>x</b>".repeat(1000)).matches("<b>").count(), 1000);
    }

    #[test]
    fn end_tags_do_not_cross_scope_boundaries() {
        // </div> is ignored inside a marquee.
        assert_eq!(
            fl(&"<div><marquee></div>".repeat(1000))
                .matches("<div>")
                .count(),
            MAX_NESTING / 2
        );
        assert_eq!(
            fl(&"<div><table></table></div>".repeat(1000))
                .matches("<div>")
                .count(),
            1000
        );
    }

    #[test]
    fn stray_end_tags_are_left_out() {
        assert_eq!(fl("<div>a</q></div></p></br>"), "<div>a</div></p></br>");
    }

    #[test]
    fn matching_end_tags_close_to_their_element() {
        // An end tag closes the elements opened inside its element.
        let page = "<section><div><span>x</section>".repeat(1000);
        assert_eq!(fl(&page).matches("<section>").count(), 1000);
    }

    #[test]
    fn void_self_closed_tags_stay_flat() {
        let page = "<br/><img/><input/>".repeat(1000) + "<div>y</div>";
        assert!(fl(&page).contains("<div>y</div>"));
    }

    #[test]
    fn unclosed_paragraphs_do_not_count() {
        let page = "<p>x".repeat(1000) + "<div>y</div>";
        assert!(fl(&page).contains("<div>y</div>"));
    }

    #[test]
    fn strip_keeps_nesting() {
        let deep = "<div>".repeat(1000);
        assert_eq!(strip(&deep), deep);
    }

    #[test]
    fn text_with_angle_brackets_stays() {
        assert_eq!(strip("3 < 5 and 6 > 2"), "3 < 5 and 6 > 2");
    }
}
