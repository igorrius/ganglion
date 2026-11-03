//! Finding the main content of a page, in the manner of Readability:
//! remove what is never content, score paragraph-like elements into their
//! ancestors, take the best candidate, and prune link-heavy furniture
//! inside it. Every walk is iterative, so a deep tree cannot exhaust the
//! stack.

use std::collections::HashMap;

use dom_query::{Document, NodeId, NodeRef};

/// Elements that never carry readable content.
const DROPPED: [&str; 22] = [
    "script", "style", "noscript", "template", "iframe", "frame", "frameset", "object", "embed",
    "svg", "math", "canvas", "video", "audio", "head", "nav", "button", "input", "select",
    "textarea", "dialog", "link",
];

/// ARIA roles of page furniture.
const DROPPED_ROLES: [&str; 9] = [
    "navigation",
    "banner",
    "contentinfo",
    "complementary",
    "search",
    "dialog",
    "alertdialog",
    "menu",
    "menubar",
];

/// Elements never removed for their class or id: the page, links, the
/// main content and tables (a "sticky-header" table is still data).
const KEPT: [&str; 11] = [
    "html", "body", "a", "main", "article", "table", "thead", "tbody", "tr", "td", "th",
];

/// Readability's class and id patterns, without "comment": forum threads
/// are pages the owner reads, and their posts are often called comments.
const UNLIKELY: [&str; 31] = [
    "-ad-",
    "ai2html",
    "banner",
    "breadcrumbs",
    "combx",
    "cover-wrap",
    "disqus",
    "extra",
    "footer",
    "gdpr",
    "header",
    "legends",
    "menu",
    "related",
    "rss",
    "shoutbox",
    "sidebar",
    "skyscraper",
    "social",
    "sponsor",
    "supplemental",
    "ad-break",
    "agegate",
    "pagination",
    "pager",
    "popup",
    "yom-remote",
    "cookie",
    "newsletter",
    "subscribe",
    "share",
];
const MAYBE: [&str; 8] = [
    "and", "article", "body", "column", "content", "main", "mathjax", "shadow",
];
const POSITIVE: [&str; 13] = [
    "article", "body", "content", "entry", "hentry", "h-entry", "main", "page", "post", "text",
    "blog", "story", "thread",
];
const NEGATIVE: [&str; 22] = [
    "-ad-",
    "hidden",
    "banner",
    "combx",
    "com-",
    "contact",
    "footer",
    "gdpr",
    "masthead",
    "media",
    "meta",
    "outbrain",
    "promo",
    "related",
    "scroll",
    "share",
    "shoutbox",
    "sidebar",
    "skyscraper",
    "sponsor",
    "shopping",
    "widget",
];

/// Paragraph-like elements whose text scores their ancestors. A table
/// row counts, so a page whose content is a table of short cells is
/// found too.
const SCORED: [&str; 7] = ["p", "td", "tr", "pre", "blockquote", "li", "dd"];

/// Elements that start a block: a div holding none of them is a paragraph.
const BLOCKS: [&str; 22] = [
    "address",
    "article",
    "aside",
    "blockquote",
    "div",
    "dl",
    "fieldset",
    "figure",
    "footer",
    "form",
    "h1",
    "h2",
    "h3",
    "h4",
    "h5",
    "h6",
    "header",
    "ol",
    "p",
    "pre",
    "table",
    "ul",
];

/// Containers removed from the content when they are mostly links:
/// menus, link lists, teasers of other articles.
const PRUNABLE: [&str; 10] = [
    "div", "section", "ul", "ol", "aside", "header", "footer", "figure", "dl", "table",
];

/// The page's og:title, else its `<title>`, else its first h1.
pub fn title(doc: &Document) -> String {
    let og = doc
        .select_single(r#"meta[property="og:title"]"#)
        .attr("content")
        .map(|t| t.to_string());
    let from = |sel: &str| doc.select_single(sel).text().to_string();
    [og.unwrap_or_default(), from("title"), from("h1")]
        .iter()
        .map(|t| collapse(t))
        .find(|t| !t.is_empty())
        .unwrap_or_default()
}

/// The element that holds the page's main content; the body when nothing
/// scores.
pub fn main_content(doc: &Document) -> NodeRef<'_> {
    clean(doc.root());
    let Some(body) = doc.select_single("body").nodes().first().copied() else {
        return doc.root();
    };
    let st = Stats::count(body);
    let mut scores = score(body, &st);
    let root = match best(&mut scores, &st) {
        Some(top) if st.text(top) * 4 >= st.text(body) || st.text(top) >= 500 => {
            whole(climb(top, &scores, &st))
        }
        _ => body,
    };
    prune(root, &st);
    root
}

/// Visits n and its descendants in document order without recursion;
/// visit returns false to skip a node's children.
fn walk<'a>(n: NodeRef<'a>, mut visit: impl FnMut(NodeRef<'a>) -> bool) {
    let mut stack = vec![n];
    while let Some(cur) = stack.pop() {
        if visit(cur) {
            stack.extend(cur.children_it(true));
        }
    }
}

fn name(n: &NodeRef) -> String {
    n.node_name()
        .map(|s| s.to_ascii_lowercase())
        .unwrap_or_default()
}

fn attr(n: &NodeRef, key: &str) -> String {
    n.attr(key).map(|s| s.to_string()).unwrap_or_default()
}

fn any_in(s: &str, words: &[&str]) -> bool {
    words.iter().any(|w| s.contains(w))
}

/// Removes comments and every element that is never content.
fn clean(root: NodeRef) {
    let mut gone = Vec::new();
    walk(root, |n| {
        if n.is_comment() || (n.is_element() && removable(&n)) {
            gone.push(n);
            return false;
        }
        true
    });
    for n in gone {
        n.remove_from_parent();
    }
}

fn removable(n: &NodeRef) -> bool {
    let tag = name(n);
    if DROPPED.contains(&tag.as_str())
        || DROPPED_ROLES.contains(&attr(n, "role").as_str())
        || hidden(n)
    {
        return true;
    }
    if KEPT.contains(&tag.as_str()) {
        return false;
    }
    let ids = format!("{} {}", attr(n, "class"), attr(n, "id")).to_ascii_lowercase();
    any_in(&ids, &UNLIKELY) && !any_in(&ids, &MAYBE) && !inside_table_or_code(n)
}

fn hidden(n: &NodeRef) -> bool {
    if n.has_attr("hidden") || attr(n, "aria-hidden") == "true" {
        return true;
    }
    let style = attr(n, "style").to_ascii_lowercase().replace(' ', "");
    style.contains("display:none") || style.contains("visibility:hidden")
}

fn inside_table_or_code(n: &NodeRef) -> bool {
    n.ancestors_it(None)
        .any(|a| matches!(name(&a).as_str(), "table" | "pre" | "code"))
}

/// Text, link-text and comma counts of every node, counted once,
/// bottom-up.
struct Stats {
    by_node: HashMap<NodeId, Count>,
}

#[derive(Default, Clone, Copy)]
struct Count {
    text: usize,
    links: usize,
    commas: usize,
}

impl Stats {
    fn count(root: NodeRef) -> Stats {
        let mut order = Vec::new();
        walk(root, |n| {
            order.push(n);
            true
        });
        let mut by_node: HashMap<NodeId, Count> = HashMap::with_capacity(order.len());
        for n in order.iter().rev() {
            let mut c = by_node.get(&n.id).copied().unwrap_or_default();
            if n.is_text() {
                let t = n.text();
                c.text = collapsed_len(&t);
                c.commas = t.matches(',').count();
            }
            if n.is_element() && name(n) == "a" {
                c.links = c.text;
            }
            by_node.insert(n.id, c);
            if let Some(p) = n.parent() {
                let pc = by_node.entry(p.id).or_default();
                pc.text += c.text;
                pc.links += c.links;
                pc.commas += c.commas;
            }
        }
        Stats { by_node }
    }

    fn get(&self, n: NodeRef) -> Count {
        self.by_node.get(&n.id).copied().unwrap_or_default()
    }

    fn text(&self, n: NodeRef) -> usize {
        self.get(n).text
    }

    fn density(&self, n: NodeRef) -> f64 {
        let c = self.get(n);
        if c.text == 0 {
            0.0
        } else {
            c.links as f64 / c.text as f64
        }
    }
}

/// The length of s with every run of whitespace counted as one.
fn collapsed_len(s: &str) -> usize {
    let mut len = 0;
    let mut space = true;
    for ch in s.chars() {
        if ch.is_whitespace() {
            if !space {
                len += 1;
                space = true;
            }
        } else {
            len += 1;
            space = false;
        }
    }
    len
}

fn collapse(s: &str) -> String {
    s.split_whitespace().collect::<Vec<_>>().join(" ")
}

/// Gives every candidate its points: a paragraph with at least 25
/// characters of its own text gives 1, one per comma and one per 100
/// characters (up to 3) to its parent, half to its grandparent and a
/// quarter to the one above.
fn score<'a>(body: NodeRef<'a>, st: &Stats) -> HashMap<NodeId, (NodeRef<'a>, f64)> {
    let mut scores: HashMap<NodeId, (NodeRef<'a>, f64)> = HashMap::new();
    walk(body, |n| {
        if !n.is_element() || !paragraph_like(&n) {
            return true;
        }
        let c = st.get(n);
        let own = c.text.saturating_sub(c.links);
        if own < 25 {
            return true;
        }
        let points = 1.0 + c.commas as f64 + (own as f64 / 100.0).min(3.0);
        let mut divisor = 1.0;
        for anc in n.ancestors_it(Some(3)).filter(|a| a.is_element()) {
            let entry = scores.entry(anc.id).or_insert_with(|| (anc, initial(&anc)));
            entry.1 += points / divisor;
            divisor *= 2.0;
        }
        true
    });
    scores
}

fn paragraph_like(n: &NodeRef) -> bool {
    let tag = name(n);
    if SCORED.contains(&tag.as_str()) {
        return true;
    }
    tag == "div"
        && !n
            .element_children()
            .iter()
            .any(|c| BLOCKS.contains(&name(c).as_str()))
}

/// A candidate's score before its paragraphs: by tag and class name.
fn initial(n: &NodeRef) -> f64 {
    let mut s = match name(n).as_str() {
        "div" | "article" | "main" => 5.0,
        "pre" | "td" | "blockquote" => 3.0,
        "address" | "ol" | "ul" | "dl" | "dd" | "dt" | "li" | "form" => -3.0,
        "h1" | "h2" | "h3" | "h4" | "h5" | "h6" | "th" => -5.0,
        _ => 0.0,
    };
    for v in [attr(n, "class"), attr(n, "id")] {
        let v = v.to_ascii_lowercase();
        if v.is_empty() {
            continue;
        }
        if any_in(&v, &NEGATIVE) {
            s -= 25.0;
        }
        if any_in(&v, &POSITIVE) {
            s += 25.0;
        }
    }
    s
}

/// The candidate with the highest score after the link penalty; scores
/// are updated to include the penalty.
fn best<'a>(scores: &mut HashMap<NodeId, (NodeRef<'a>, f64)>, st: &Stats) -> Option<NodeRef<'a>> {
    let mut top: Option<(NodeRef<'a>, f64)> = None;
    for (n, s) in scores.values_mut() {
        *s *= 1.0 - st.density(*n);
        let better = match top {
            None => true,
            Some((t, ts)) => {
                *s > ts
                    || (*s == ts && st.text(*n) > st.text(t))
                    || (*s == ts && st.text(*n) == st.text(t) && n.id < t.id)
            }
        };
        if better {
            top = Some((*n, *s));
        }
    }
    top.map(|(n, _)| n)
}

/// Moves up from top to the nearest ancestor holding at least two
/// candidates nearly as good, as a forum thread's posts or an article
/// split into sections are; a single good block stays as it is.
fn climb<'a>(
    top: NodeRef<'a>,
    scores: &HashMap<NodeId, (NodeRef<'a>, f64)>,
    st: &Stats,
) -> NodeRef<'a> {
    let top_score = scores.get(&top.id).map_or(0.0, |e| e.1);
    let rivals: Vec<NodeRef<'a>> = scores
        .values()
        .filter(|(n, s)| n.id != top.id && *s >= 0.75 * top_score && st.text(*n) > 0)
        .map(|(n, _)| *n)
        .collect();
    if rivals.len() < 2 {
        return top;
    }
    for anc in top.ancestors_it(None).filter(|a| a.is_element()) {
        if name(&anc) == "html" {
            break;
        }
        let inside = rivals
            .iter()
            .filter(|r| r.ancestors_it(None).any(|a| a.id == anc.id))
            .count();
        if inside >= 2 {
            return anc;
        }
    }
    top
}

/// The table a part of a table belongs to: a table is written whole.
fn whole(n: NodeRef) -> NodeRef {
    if matches!(name(&n).as_str(), "tbody" | "thead" | "tfoot" | "tr")
        && let Some(t) = n.ancestors_it(None).find(|a| name(a) == "table")
    {
        return t;
    }
    n
}

/// Removes link-heavy containers under root, as Readability's
/// conditional cleaning does; a table of data stays whatever its links.
fn prune(root: NodeRef, st: &Stats) {
    let mut gone = Vec::new();
    walk(root, |n| {
        if n.id == root.id || !n.is_element() {
            return true;
        }
        let tag = name(&n);
        if tag == "table" && data_table(&n) {
            return false;
        }
        if PRUNABLE.contains(&tag.as_str()) && st.text(n) > 0 && st.density(n) > 0.5 {
            gone.push(n);
            return false;
        }
        true
    });
    for n in gone {
        n.remove_from_parent();
    }
}

/// A table with header cells or a caption holds data, not layout.
fn data_table(n: &NodeRef) -> bool {
    let mut found = false;
    walk(*n, |c| {
        if found || (c.id != n.id && name(&c) == "table") {
            return false;
        }
        found = matches!(name(&c).as_str(), "th" | "thead" | "caption");
        !found
    });
    found
}
