//! How deep the parser's tree gets, counted from the tags alone, so the
//! pre-pass can cap it (ruling R24). The count follows the HTML
//! tree-construction rules by element category and errs towards depth
//! wherever it simplifies: a tag it is unsure about never makes the
//! count smaller than the tree. `tests/depth.rs` checks the result
//! against html5ever on random tag soups.
//!
//! End tags by category (ruling R24): a special one closes its element
//! only within scope; a formatting one closes its element only when it is
//! on top (the adoption agency keeps the depth otherwise); any other walks
//! down and closes the first element of its name, stopping at a special
//! element. Inside SVG and MathML the foreign rules apply; inside a select
//! almost nothing closes. Where the real rules are more involved (markers,
//! `</form>`, table parts outside a table) the count closes less.

use std::collections::HashMap;

/// Deepest nesting of elements passed to the parser.
pub const MAX_NESTING: usize = 256;

/// Void elements: they never open, with or without "/>".
const VOID: &[&str] = &[
    "area", "base", "basefont", "bgsound", "br", "col", "embed", "hr", "img", "input", "keygen",
    "link", "meta", "param", "source", "track", "wbr",
];

/// The special category (WHATWG): an "any other" end tag stops at them.
const SPECIAL: &[&str] = &[
    "address",
    "applet",
    "area",
    "article",
    "aside",
    "base",
    "basefont",
    "bgsound",
    "blockquote",
    "body",
    "br",
    "button",
    "caption",
    "center",
    "col",
    "colgroup",
    "dd",
    "details",
    "dir",
    "div",
    "dl",
    "dt",
    "embed",
    "fieldset",
    "figcaption",
    "figure",
    "footer",
    "form",
    "frame",
    "frameset",
    "h1",
    "h2",
    "h3",
    "h4",
    "h5",
    "h6",
    "head",
    "header",
    "hgroup",
    "hr",
    "html",
    "iframe",
    "img",
    "input",
    "keygen",
    "li",
    "link",
    "listing",
    "main",
    "marquee",
    "menu",
    "meta",
    "nav",
    "noembed",
    "noframes",
    "noscript",
    "object",
    "ol",
    "p",
    "param",
    "plaintext",
    "pre",
    "script",
    "search",
    "section",
    "select",
    "source",
    "style",
    "summary",
    "table",
    "tbody",
    "td",
    "template",
    "textarea",
    "tfoot",
    "th",
    "thead",
    "title",
    "tr",
    "track",
    "ul",
    "wbr",
    "xmp",
    "mi",
    "mo",
    "mn",
    "ms",
    "mtext",
    "annotation-xml",
    "foreignobject",
    "desc",
];

/// Formatting elements: their end tags run the adoption agency
/// algorithm, and the parser reopens them after another end tag closed
/// them (up to three alike).
const FORMATTING: &[&str] = &[
    "a", "b", "big", "code", "em", "font", "i", "nobr", "s", "small", "strike", "strong", "tt", "u",
];

/// The boundaries of the default scope.
const BOUNDARY: &[&str] = &[
    "applet",
    "caption",
    "html",
    "table",
    "td",
    "th",
    "marquee",
    "object",
    "template",
    "mi",
    "mo",
    "mn",
    "ms",
    "mtext",
    "annotation-xml",
    "foreignobject",
    "desc",
];

/// Start tags that close an open p (in button scope). `table` is left
/// out: in quirks mode, which a page without a doctype is in, it does not.
const CLOSES_P: &[&str] = &[
    "address",
    "article",
    "aside",
    "blockquote",
    "center",
    "details",
    "dialog",
    "dir",
    "div",
    "dl",
    "fieldset",
    "figcaption",
    "figure",
    "footer",
    "header",
    "hgroup",
    "main",
    "menu",
    "nav",
    "ol",
    "p",
    "search",
    "section",
    "summary",
    "ul",
    "h1",
    "h2",
    "h3",
    "h4",
    "h5",
    "h6",
    "pre",
    "listing",
    "form",
    "plaintext",
    "hr",
    "xmp",
    "li",
    "dd",
    "dt",
];

/// End tags the parser closes to their element in (default) scope;
/// `li` in list item scope.
const BLOCK_ENDS: &[&str] = &[
    "address",
    "article",
    "aside",
    "blockquote",
    "button",
    "center",
    "details",
    "dialog",
    "dir",
    "div",
    "dl",
    "fieldset",
    "figcaption",
    "figure",
    "footer",
    "header",
    "hgroup",
    "listing",
    "main",
    "menu",
    "nav",
    "ol",
    "pre",
    "search",
    "section",
    "summary",
    "ul",
    "li",
    "dd",
    "dt",
    "applet",
    "marquee",
    "object",
];

/// End tags of a table and its parts look in table scope: in the table
/// modes an applet or a marquee inside does not stop them.
const TABLE_ENDS: &[&str] = &[
    "table", "caption", "tbody", "thead", "tfoot", "tr", "td", "th",
];

/// HTML start tags that end foreign content.
const BREAKOUT: &[&str] = &[
    "b",
    "big",
    "blockquote",
    "body",
    "br",
    "center",
    "code",
    "dd",
    "div",
    "dl",
    "dt",
    "em",
    "embed",
    "h1",
    "h2",
    "h3",
    "h4",
    "h5",
    "h6",
    "head",
    "hr",
    "i",
    "img",
    "li",
    "listing",
    "menu",
    "meta",
    "nobr",
    "ol",
    "p",
    "pre",
    "ruby",
    "s",
    "small",
    "span",
    "strong",
    "strike",
    "sub",
    "sup",
    "table",
    "tt",
    "u",
    "ul",
    "var",
];

const HEADINGS: &[&str] = &["h1", "h2", "h3", "h4", "h5", "h6"];

fn is(list: &[&str], name: &[u8]) -> bool {
    list.iter().any(|v| v.as_bytes() == name)
}

/// The namespace of an element.
#[derive(Clone, Copy, PartialEq, Eq)]
enum Ns {
    Html,
    Svg,
    MathMl,
}

/// Whether children of an element of name in ns are HTML again.
fn integration(name: &[u8], ns: Ns) -> bool {
    match ns {
        Ns::Svg => matches!(name, b"foreignobject" | b"desc" | b"title"),
        Ns::MathMl => matches!(
            name,
            b"mi" | b"mo" | b"mn" | b"ms" | b"mtext" | b"annotation-xml"
        ),
        Ns::Html => false,
    }
}

/// What Nesting does with a tag.
#[derive(Debug, PartialEq, Eq)]
pub enum Verdict {
    /// Pass the tag to the parser.
    Keep,
    /// Leave it out: past the cap, the end of one left out, or an end
    /// tag the parser would ignore after a long search.
    Drop,
}

/// The count: a model of the parser's stack of open elements with their
/// namespaces. Formatting elements another tag closes are put back on
/// top, because the parser reopens them before the next element. Start
/// tags past MAX_NESTING are left out, and so are their end tags.
#[derive(Default)]
pub struct Nesting {
    open: Vec<Vec<u8>>,
    /// For each open element, its start tag when it is a formatting one
    /// (name and attributes, as the parser compares them), else empty.
    keys: Vec<Vec<u8>>,
    /// For each open element, its namespace: HTML, SVG or MathML.
    ns: Vec<Ns>,
    open_by_name: HashMap<Vec<u8>, usize>,
    dropped: HashMap<Vec<u8>, usize>,
}

/// The parser keeps at most this many alike formatting elements.
const ALIKE: usize = 3;

impl Nesting {
    /// The depth the parser's tree may reach now.
    pub fn depth(&self) -> usize {
        self.open.len()
    }

    /// Judges one tag, `<…>` with its brackets.
    pub fn tag(&mut self, tag: &[u8]) -> Verdict {
        let end = tag.len() > 1 && tag[1] == b'/';
        let start = if end { 2 } else { 1 };
        if !tag.get(start).is_some_and(u8::is_ascii_alphabetic) {
            return Verdict::Keep; // text, a comment or a doctype
        }
        let len = tag[start..]
            .iter()
            .position(|c| c.is_ascii_whitespace() || *c == b'/' || *c == b'>')
            .unwrap_or(tag.len() - start);
        let name = tag[start..start + len].to_ascii_lowercase();
        if end {
            self.end(&name)
        } else {
            self.start(name, tag)
        }
    }

    fn start(&mut self, name: Vec<u8>, tag: &[u8]) -> Verdict {
        if is(VOID, &name) && (is(BREAKOUT, &name) || !self.in_foreign()) {
            self.close_for_start(&name); // <hr> closes an open p, <br> ends SVG
            return Verdict::Keep;
        }
        // In SVG or MathML a <wbr> or an <input> is an ordinary element.
        if matches!(name.as_slice(), b"html" | b"head" | b"body") {
            return Verdict::Keep;
        }
        if self.depth() >= MAX_NESTING {
            *self.dropped.entry(name).or_default() += 1;
            return Verdict::Drop;
        }
        self.close_for_start(&name);
        let key = if is(FORMATTING, &name) {
            tag.to_ascii_lowercase()
        } else {
            Vec::new()
        };
        self.push_keyed(name, key);
        Verdict::Keep
    }

    /// The elements a start tag closes before it opens.
    fn close_for_start(&mut self, name: &[u8]) {
        if self.in_select() {
            // Inside a select the parser's rules differ (and change between
            // versions); only an option closes an option there.
            if matches!(name, b"option" | b"optgroup") && self.top_is(b"option") {
                self.pop();
            }
            return;
        }
        if is(BREAKOUT, name) {
            self.break_out_of_foreign();
        }
        match name {
            b"li" => self.close_item(&[b"li"]),
            b"dd" | b"dt" => self.close_item(&[b"dd", b"dt"]),
            b"option" | b"optgroup" => {
                if self.top_is(b"option") {
                    self.pop();
                }
            }
            b"button" => {
                if let Some(i) = self.find_in_scope(|n| n == b"button", &[]) {
                    self.pop_to(i);
                }
            }
            // Outside a table the parser usually ignores table parts, but
            // not in every context: counting them errs towards depth.
            b"td" | b"th" if self.in_table() => self.open_cell(),
            b"tr" if self.in_table() => self.open_row(),
            b"tbody" | b"thead" | b"tfoot" | b"caption" | b"colgroup" if self.in_table() => {
                self.clear_to_table()
            }
            b"table" => self.close_table_for_table(),
            _ => {}
        }
        if is(CLOSES_P, name)
            && let Some(i) = self.find_in_scope(|n| n == b"p", &[b"button"])
        {
            self.pop_to(i);
        }
        if is(HEADINGS, name) && self.open.last().is_some_and(|t| is(HEADINGS, t)) {
            self.pop();
        }
    }

    /// An HTML start tag such as `<table>` or `<div>` inside SVG or MathML
    /// closes the foreign elements and is handled as HTML. Elements above
    /// an integration point (foreignObject, desc, title, mi …) are HTML
    /// already and stay.
    fn break_out_of_foreign(&mut self) {
        while self.in_foreign() {
            self.pop();
        }
    }

    /// Whether the current node is SVG or MathML that holds no HTML.
    fn in_foreign(&self) -> bool {
        match (self.open.last(), self.ns.last()) {
            (Some(name), Some(&ns)) => ns != Ns::Html && !integration(name, ns),
            _ => false,
        }
    }

    /// li, dd and dt close an open one of their kind, unless a special
    /// element other than address, div and p is in between.
    fn close_item(&mut self, kinds: &[&[u8]]) {
        for i in (0..self.open.len()).rev() {
            if kinds.iter().any(|k| self.html_at(i, k)) {
                self.pop_to(i);
                return;
            }
            let n = &self.open[i];
            if self.special_at(i)
                && !(self.ns[i] == Ns::Html && matches!(n.as_slice(), b"address" | b"div" | b"p"))
            {
                return;
            }
        }
    }

    /// Whether table parts act on a table: an HTML table is open and the
    /// current node is HTML (inside SVG or MathML a `<td>` is just another
    /// element; inside an integration point the table rules apply again).
    fn in_table(&self) -> bool {
        self.find_html(b"table").is_some() && !self.in_foreign()
    }

    /// A table started in a table (not in one of its cells or its
    /// caption) closes that table first.
    fn close_table_for_table(&mut self) {
        if !self.in_table() {
            return;
        }
        let table = self.find_html(b"table").unwrap_or(0);
        if !self.open[table..]
            .iter()
            .any(|n| matches!(n.as_slice(), b"td" | b"th" | b"caption"))
        {
            self.pop_to(table);
        }
    }

    /// Pops everything above the nearest table.
    fn clear_to_table(&mut self) {
        if let Some(i) = self.find_html(b"table") {
            self.pop_to(i + 1);
        }
    }

    /// A row: closes an open row and its cells, and opens the table body
    /// the parser implies when none is open.
    fn open_row(&mut self) {
        let table = self.find_html(b"table").unwrap_or(0);
        match self.open[table..]
            .iter()
            .rposition(|n| matches!(n.as_slice(), b"tbody" | b"thead" | b"tfoot"))
        {
            Some(i) => self.pop_to(table + i + 1),
            None => {
                self.pop_to(table + 1);
                self.push(b"tbody".to_vec());
            }
        }
    }

    /// A cell: closes an open cell, and opens the row (and body) the parser
    /// implies when none is open.
    fn open_cell(&mut self) {
        let table = self.find_html(b"table").unwrap_or(0);
        if let Some(i) = self.open[table..].iter().rposition(|n| n == b"tr") {
            self.pop_to(table + i + 1);
        } else {
            self.open_row();
            self.push(b"tr".to_vec());
        }
    }

    fn end(&mut self, name: &[u8]) -> Verdict {
        if let Some(n) = self.dropped.get_mut(name) {
            *n -= 1;
            if *n == 0 {
                self.dropped.remove(name);
            }
            return Verdict::Drop;
        }
        if self.in_select() {
            return self.end_in_select(name);
        }
        if self.end_in_foreign(name) {
            return Verdict::Keep;
        }
        match name {
            b"p" | b"br" => {
                // </p> without a p, and </br>, make elements: always pass.
                if name == b"p"
                    && let Some(i) = self.find_in_scope(|n| n == b"p", &[b"button"])
                {
                    self.pop_to(i);
                }
                Verdict::Keep
            }
            _ if is(FORMATTING, name) => self.end_formatting(name),
            _ if is(HEADINGS, name) => {
                if let Some(i) = self.find_in_scope(|n| is(HEADINGS, n), &[]) {
                    self.pop_to(i);
                }
                Verdict::Keep
            }
            _ if is(TABLE_ENDS, name) => {
                // Outside a table, table parts are counted but their end
                // tags close nothing (err towards depth). Inside SVG or
                // MathML they still close the table: the parser hands an
                // end tag to the HTML rules once it reaches an HTML element.
                if self.find_html(b"table").is_some()
                    && let Some(i) = self.find_in_table_scope(name)
                {
                    self.pop_to(i);
                }
                Verdict::Keep
            }
            // </form> takes the form off the stack but not what is in it:
            // later elements still nest as deep.
            b"form" => Verdict::Keep,
            _ if is(BLOCK_ENDS, name) => {
                let extra: &[&[u8]] = if name == b"li" { &[b"ol", b"ul"] } else { &[] };
                if let Some(i) = self.find_in_scope(|n| n == name, extra) {
                    self.pop_to(i);
                }
                Verdict::Keep
            }
            // Any other end tag, special ones without a rule of their own
            // (select, iframe, title …) and the foreign names included.
            _ => self.end_other(name),
        }
    }

    /// A formatting end tag closes its element only when that is on top.
    /// Otherwise the adoption agency runs, which keeps the tree as deep
    /// (an optional-end element on top, such as a p, is a furthest block
    /// to it, not something it closes), and the count keeps its depth.
    fn end_formatting(&mut self, name: &[u8]) -> Verdict {
        if self.top_is(name) {
            self.pop();
        }
        Verdict::Keep
    }

    /// Any other end tag: down from the top, the first element of its name
    /// closes; a special element on the way stops it. One the parser
    /// ignores after a long search is left out.
    fn end_other(&mut self, name: &[u8]) -> Verdict {
        if !self.open_by_name.contains_key(name) {
            return Verdict::Drop;
        }
        for i in (0..self.open.len()).rev() {
            if self.html_at(i, name) {
                self.pop_to(i);
                return Verdict::Keep;
            }
            if self.special_at(i) {
                return Verdict::Keep;
            }
        }
        Verdict::Keep
    }

    /// The index of the topmost element that matches, unless a scope
    /// boundary (or one of extra) comes first.
    fn find_in_scope(&self, matches: impl Fn(&[u8]) -> bool, extra: &[&[u8]]) -> Option<usize> {
        for i in (0..self.open.len()).rev() {
            let n = self.open[i].as_slice();
            let html = self.ns[i] == Ns::Html;
            if html && matches(n) {
                return Some(i);
            }
            if self.boundary_at(i) || (html && extra.contains(&n)) {
                return None;
            }
        }
        None
    }

    /// An end tag in SVG or MathML closes the nearest foreign element of
    /// its name; if an HTML element comes first, the HTML rules take it
    /// (false).
    fn end_in_foreign(&mut self, name: &[u8]) -> bool {
        if !self.in_foreign() {
            return false;
        }
        if matches!(name, b"p" | b"br") {
            // </p> and </br> end foreign content, then count as HTML.
            self.break_out_of_foreign();
            return false;
        }
        for i in (0..self.open.len()).rev() {
            if self.ns[i] == Ns::Html {
                return false;
            }
            if self.open[i] == name {
                self.pop_to(i);
                return true;
            }
        }
        false
    }

    fn in_select(&self) -> bool {
        self.open_by_name.contains_key(b"select".as_slice()) && self.find_html(b"select").is_some()
    }

    /// An end tag inside a select: </option> and </optgroup> close their
    /// element on top; </select> closes the select when only options and
    /// option groups are above it (select scope); anything else changes
    /// nothing (err towards depth).
    fn end_in_select(&mut self, name: &[u8]) -> Verdict {
        match name {
            b"option" | b"optgroup" => {
                if self.top_is(b"option") && name == b"optgroup" {
                    self.pop();
                }
                if self.top_is(name) {
                    self.pop();
                }
            }
            b"select" => {
                if let Some(i) = self.find_html(b"select")
                    && self.open[i + 1..]
                        .iter()
                        .all(|n| matches!(n.as_slice(), b"option" | b"optgroup"))
                {
                    self.pop_to(i);
                }
            }
            _ => {}
        }
        Verdict::Keep
    }

    /// The index of the topmost element named name in table scope (only
    /// html, table and template bound it), where table end tags look.
    fn find_in_table_scope(&self, name: &[u8]) -> Option<usize> {
        for i in (0..self.open.len()).rev() {
            if self.html_at(i, name) {
                return Some(i);
            }
            if self.ns[i] == Ns::Html
                && matches!(self.open[i].as_slice(), b"html" | b"table" | b"template")
            {
                return None;
            }
        }
        None
    }

    /// Whether the current node is the HTML element name.
    fn top_is(&self, name: &[u8]) -> bool {
        self.open.last().is_some_and(|t| t == name) && self.ns.last() == Some(&Ns::Html)
    }

    /// Whether the element at i is the HTML element name.
    fn html_at(&self, i: usize, name: &[u8]) -> bool {
        self.open[i] == name && self.ns[i] == Ns::Html
    }

    /// The topmost HTML element name.
    fn find_html(&self, name: &[u8]) -> Option<usize> {
        (0..self.open.len()).rev().find(|&i| self.html_at(i, name))
    }

    /// Whether the element at i is in the special category.
    fn special_at(&self, i: usize) -> bool {
        let n = self.open[i].as_slice();
        match self.ns[i] {
            Ns::Html => is(SPECIAL, n),
            ns => integration(n, ns),
        }
    }

    /// Whether the element at i bounds the default scope.
    fn boundary_at(&self, i: usize) -> bool {
        let n = self.open[i].as_slice();
        match self.ns[i] {
            Ns::Html => is(BOUNDARY, n),
            ns => integration(n, ns),
        }
    }

    fn push(&mut self, name: Vec<u8>) {
        self.push_keyed(name, Vec::new());
    }

    fn push_keyed(&mut self, name: Vec<u8>, key: Vec<u8>) {
        let ns = match name.as_slice() {
            b"svg" => Ns::Svg,
            b"math" => Ns::MathMl,
            _ if self.in_foreign() => self.ns.last().copied().unwrap_or(Ns::Html),
            _ => Ns::Html,
        };
        self.ns.push(ns);
        *self.open_by_name.entry(name.clone()).or_default() += 1;
        self.open.push(name);
        self.keys.push(key);
    }

    /// Pops the elements from index i up. The formatting elements among
    /// them (not the one at i) stay in the parser's list and are reopened
    /// before the next element, so they go back on top, up to three alike
    /// as the parser keeps them.
    fn pop_to(&mut self, i: usize) {
        let mut reopen: Vec<(Vec<u8>, Vec<u8>)> = Vec::new();
        while self.open.len() > i {
            let at_target = self.open.len() == i + 1;
            let key = self.keys.last().cloned().unwrap_or_default();
            let name = self.open.last().cloned().unwrap_or_default();
            self.pop();
            // Markers (cells, captions, applets…) are not modelled: what
            // they would clear is still counted (err towards depth); the
            // limit of three alike keeps that small on real pages.
            if !at_target && !key.is_empty() {
                reopen.push((name, key));
            }
        }
        let mut alike: HashMap<&[u8], usize> = HashMap::new();
        let mut kept: Vec<&(Vec<u8>, Vec<u8>)> = Vec::new();
        for entry in &reopen {
            // reopen is top first: the latest three alike are kept
            let n = alike.entry(entry.1.as_slice()).or_default();
            if *n < ALIKE {
                *n += 1;
                kept.push(entry);
            }
        }
        for (name, key) in kept.into_iter().rev() {
            self.push_keyed(name.clone(), key.clone());
        }
    }

    fn pop(&mut self) {
        self.keys.pop();
        self.ns.pop();
        let Some(top) = self.open.pop() else { return };
        if let Some(n) = self.open_by_name.get_mut(&top) {
            *n -= 1;
            if *n == 0 {
                self.open_by_name.remove(&top);
            }
        }
    }
}
