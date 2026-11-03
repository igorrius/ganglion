//! The last resort for a page too deep to parse into a tree (ruling R25):
//! its text, read by html5ever's tokenizer alone. The tokenizer builds no
//! tree, so nesting costs nothing; it decodes character references the
//! way the parser does. Tags are dropped; block-level ones become line
//! breaks, so paragraphs, list items and rows stay apart.

use std::cell::RefCell;

use html5ever::tendril::StrTendril;
use html5ever::tokenizer::states::RawKind;
use html5ever::tokenizer::{
    BufferQueue, Tag, TagKind, Token, TokenSink, TokenSinkResult, Tokenizer, TokenizerOpts,
};

/// Elements that end a line before and after them.
fn block(name: &str) -> bool {
    matches!(
        name,
        "address"
            | "article"
            | "aside"
            | "blockquote"
            | "body"
            | "br"
            | "caption"
            | "center"
            | "dd"
            | "details"
            | "dialog"
            | "dir"
            | "div"
            | "dl"
            | "dt"
            | "fieldset"
            | "figcaption"
            | "figure"
            | "footer"
            | "form"
            | "h1"
            | "h2"
            | "h3"
            | "h4"
            | "h5"
            | "h6"
            | "header"
            | "hgroup"
            | "hr"
            | "legend"
            | "li"
            | "listing"
            | "main"
            | "menu"
            | "nav"
            | "ol"
            | "p"
            | "plaintext"
            | "pre"
            | "section"
            | "summary"
            | "table"
            | "tbody"
            | "tfoot"
            | "thead"
            | "tr"
            | "ul"
            | "xmp"
    )
}

/// Returns the title and the text of html.
pub fn text(html: &str) -> (String, String) {
    let tokenizer = Tokenizer::new(Text::default(), TokenizerOpts::default());
    let input = BufferQueue::default();
    input.push_back(StrTendril::from_slice(html));
    let _ = tokenizer.feed(&input);
    tokenizer.end();
    let out = tokenizer.sink.out.into_inner();
    (collapse(&out.title), out.text)
}

#[derive(Default)]
struct Out {
    title: String,
    text: String,
    /// Inside the title element: its text is the title, not the body.
    in_title: bool,
    /// The last character written was white space (or nothing yet).
    space: bool,
}

#[derive(Default)]
struct Text {
    out: RefCell<Out>,
}

impl Out {
    fn chars(&mut self, s: &str) {
        if self.in_title {
            self.title.push_str(s);
            return;
        }
        for c in s.chars() {
            if c.is_whitespace() {
                if !self.space {
                    self.text.push(' ');
                    self.space = true;
                }
            } else {
                self.text.push(c);
                self.space = false;
            }
        }
    }

    fn line(&mut self) {
        while self.text.ends_with(' ') {
            self.text.pop();
        }
        if !self.text.is_empty() && !self.text.ends_with('\n') {
            self.text.push('\n');
        }
        self.space = true;
    }

    /// A tag: block-level ones break the line; cells are set apart.
    fn tag(&mut self, tag: &Tag) -> TokenSinkResult<()> {
        let name: &str = &tag.name;
        let start = tag.kind == TagKind::StartTag;
        if name == "title" {
            self.in_title = start && !tag.self_closing;
            return if self.in_title {
                TokenSinkResult::RawData(RawKind::Rcdata)
            } else {
                TokenSinkResult::Continue
            };
        }
        if block(name) {
            self.line();
        } else if matches!(name, "td" | "th") && !self.space {
            self.text.push(' ');
            self.space = true;
        }
        if !start {
            return TokenSinkResult::Continue;
        }
        // The tokenizer states the tree builder would switch to.
        match name {
            "textarea" => TokenSinkResult::RawData(RawKind::Rcdata),
            "style" | "xmp" | "iframe" | "noembed" | "noframes" => {
                TokenSinkResult::RawData(RawKind::Rawtext)
            }
            "script" => TokenSinkResult::RawData(RawKind::ScriptData),
            "plaintext" => TokenSinkResult::Plaintext,
            _ => TokenSinkResult::Continue,
        }
    }
}

impl TokenSink for Text {
    type Handle = ();

    fn process_token(&self, token: Token, _line: u64) -> TokenSinkResult<()> {
        let mut out = self.out.borrow_mut();
        match token {
            Token::CharacterTokens(s) => out.chars(&s),
            Token::TagToken(tag) => return out.tag(&tag),
            _ => {}
        }
        TokenSinkResult::Continue
    }
}

fn collapse(s: &str) -> String {
    s.split_whitespace().collect::<Vec<_>>().join(" ")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn keeps_text_and_breaks_blocks() {
        let (title, text) = text(
            "<title>A &amp; B</title><h1>Head</h1><p>one <b>two</b>\n  three</p>\
             <ul><li>x &lt; y<li>z</ul><table><tr><td>1<td>2</table>",
        );
        assert_eq!(title, "A & B");
        assert_eq!(text, "Head\none two three\nx < y\nz\n1 2\n");
    }

    #[test]
    fn raw_text_is_not_read_as_tags() {
        let (title, text) = text("<title>a <b> c</title><textarea><p>t</textarea><p>end");
        assert_eq!(title, "a <b> c");
        assert_eq!(text, "<p>t\nend");
    }

    #[test]
    fn nesting_costs_nothing() {
        let page = "<b><div></b>".repeat(100_000) + "end";
        assert!(text(&page).1.ends_with("end"));
    }
}
