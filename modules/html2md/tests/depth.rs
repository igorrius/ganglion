//! Property test of the depth gate (ruling R25): whatever comes in, every
//! tree the module builds is at most MAX_DEPTH deep. The depth is measured
//! here, independently of the gate: each string the module parses (the
//! page after the gate, and the content handed to the Markdown writer) is
//! parsed again with html5ever (dom_query) and its deepest element counted.
//! And the gate's verdicts are checked against an oracle: dom_query's own
//! sink, wrapped to measure the real tree after every change, so the gate
//! may neither pass a page whose tree goes too deep at any moment while
//! being parsed, nor refuse one whose tree never does.
//!
//! Inputs: random units of 2-6 tags over an alphabet of every kind of
//! element the parser treats differently, each repeated 400 times, plus
//! mixed soups; and a regression corpus (tests/shapes.txt). Seeds are
//! fixed and a failure prints its input. The test also checks it bites: a
//! stated share of the generated pages must be too deep for the gate.

use std::borrow::Cow;
use std::cell::{Cell, RefCell};
use std::collections::HashMap;

use dom_query::{Document, NodeId, NodeRef};
use html2md::{MAX_DEPTH, Route, Scripting, Trace, fits, inspect, prepass};
use html5ever::interface::{ElementFlags, NodeOrText, QuirksMode, TreeSink};
use html5ever::tendril::{StrTendril, TendrilSink};
use html5ever::{Attribute, QualName};

/// Tags of every category: special, formatting (`{n}` becomes a number
/// unique in the page, which defeats the parser's limit of three
/// identical formatting entries), scope boundaries, optional ends, the
/// ruby family, others, custom, SVG and MathML with their integration
/// points, raw text, the document's own elements, void, self-closed and
/// stray end tags, comments, CDATA, upper case and text.
const ALPHABET: &[&str] = &[
    "<div>",
    "</div>",
    "<section>",
    "</section>",
    "<p>",
    "</p>",
    "<li>",
    "</li>",
    "<ul>",
    "</ul>",
    "<ol>",
    "<dd>",
    "<dt>",
    "<dl>",
    "<h1>",
    "</h1>",
    "<h2>",
    "<address>",
    "<blockquote>",
    "<center>",
    "<details>",
    "<summary>",
    "<dialog>",
    "<main>",
    "<menu>",
    "<fieldset>",
    "</fieldset>",
    "<legend>",
    "<pre>",
    "</pre>",
    "<listing>",
    "<form>",
    "</form>",
    "<button>",
    "</button>",
    "<select>",
    "</select>",
    "<option>",
    "</option>",
    "<optgroup>",
    "<output>",
    "<b class=c{n}>",
    "<b>",
    "</b>",
    "<i class=c{n}>",
    "</i>",
    "<a href=x{n}>",
    "</a>",
    "<em>",
    "<font face=f{n}>",
    "</font>",
    "<nobr>",
    "</nobr>",
    "<code>",
    "<s>",
    "<u>",
    "<strong>",
    "<table>",
    "</table>",
    "<tr>",
    "</tr>",
    "<td>",
    "</td>",
    "<th>",
    "<tbody>",
    "</tbody>",
    "<thead>",
    "<caption>",
    "</caption>",
    "<colgroup>",
    "<col>",
    "<marquee>",
    "</marquee>",
    "<object>",
    "</object>",
    "<applet>",
    "</applet>",
    "<span>",
    "</span>",
    "<q>",
    "</q>",
    "<label>",
    "</label>",
    "<abbr>",
    "<cite>",
    "</cite>",
    "<slot>",
    "<x-el>",
    "</x-el>",
    "<x-other>",
    "<svg>",
    "</svg>",
    "<g>",
    "</g>",
    "<foreignObject>",
    "</foreignObject>",
    "<desc>",
    "</desc>",
    "<title>",
    "<math>",
    "</math>",
    "<mi>",
    "</mi>",
    "<mtext>",
    "<annotation-xml>",
    "</annotation-xml>",
    "<annotation-xml encoding=text/html>",
    "<ruby>",
    "<rb>",
    "<rt>",
    "<rp>",
    "<rtc>",
    "</rtc>",
    "<video>",
    "</video>",
    "<audio>",
    "<picture>",
    "<iframe>",
    "</iframe>",
    "<noembed>",
    "<frameset>",
    "<frame>",
    "<template>",
    "</template>",
    "<textarea>",
    "</textarea>",
    "<plaintext>",
    "<xmp>",
    "<noscript>",
    "<html>",
    "</html>",
    "<head>",
    "</head>",
    "<body>",
    "</body>",
    "<br>",
    "</br>",
    "<img>",
    "<hr>",
    "<input>",
    "<wbr>",
    "<div/>",
    "<span/>",
    "<b/>",
    "<p/>",
    "<svg/>",
    "</zzz>",
    "<!--c-->",
    "<!---->",
    "<![CDATA[x]]>",
    "<DIV>",
    "<B class=C{n}>",
    "<SVG>",
    "<Math>",
    "<TABLE>",
    "<TD>",
    "<foreignobject>",
    "x",
    " y ",
];

/// xorshift64*: a small deterministic generator, so the test needs no
/// dependency and a failure repeats.
struct Rng(u64);

impl Rng {
    fn next(&mut self) -> u64 {
        self.0 ^= self.0 >> 12;
        self.0 ^= self.0 << 25;
        self.0 ^= self.0 >> 27;
        self.0.wrapping_mul(0x2545_f491_4f6c_dd1d)
    }

    fn below(&mut self, n: usize) -> usize {
        (self.next() % n as u64) as usize
    }

    fn tag(&mut self) -> &'static str {
        ALPHABET[self.below(ALPHABET.len())]
    }
}

/// A page: a start marker, body, an end marker.
fn page(body: &str) -> String {
    format!("<title>t</title><p>start</p>{body}<p>end</p>")
}

/// unit repeated reps times, each `{n}` numbered.
fn repeat(unit: &str, reps: usize) -> String {
    let mut s = String::with_capacity(unit.len() * reps + 16);
    for n in 0..reps {
        s.push_str(&unit.replace("{n}", &n.to_string()));
    }
    s
}

/// A random unit of 2-6 tags.
fn unit(rng: &mut Rng) -> String {
    (0..2 + rng.below(5)).map(|_| rng.tag()).collect()
}

/// A soup of about `tags` tags: runs of 1-4 favourite units, each run
/// repeating one of them 1-60 times, with a random tag between runs now
/// and then. Shapes repeat as attacks do while the mix still varies.
fn soup(rng: &mut Rng, tags: usize) -> String {
    let favourites: Vec<String> = (0..1 + rng.below(4)).map(|_| unit(rng)).collect();
    let mut s = String::new();
    let (mut n, mut count) = (0, 0);
    while count < tags {
        let u = if rng.below(4) == 0 {
            rng.tag().to_string()
        } else {
            favourites[rng.below(favourites.len())].clone()
        };
        let reps = if u.starts_with('<') && u.matches('<').count() > 1 {
            1 + rng.below(60)
        } else {
            1
        };
        for _ in 0..reps {
            count += u.matches('<').count().max(1);
            s.push_str(&u.replace("{n}", &n.to_string()));
            n += 1;
        }
    }
    s
}

/// The deepest element of html as html5ever parses it (the html element
/// is at depth 1).
fn depth(html: &str) -> usize {
    let doc = Document::from(html);
    let mut max = 0;
    let mut stack = vec![(doc.root(), 0usize)];
    while let Some((n, d)) = stack.pop() {
        max = max.max(d);
        stack.extend(
            n.children_it(false)
                .filter(|c| c.is_element())
                .map(|c| (c, d + 1)),
        );
    }
    max
}

/// What a run saw.
#[derive(Default, Debug)]
struct Seen {
    /// Pages with a tree deeper than the cap, and the first of them.
    over: usize,
    first_over: Option<String>,
    pages: usize,
    /// Pages the gate found too deep (any route but Direct).
    gated: usize,
    flattened: usize,
    text: usize,
    content_text: usize,
    /// Refused pages whose final tree fits but which pass through a tree
    /// too deep while being parsed (the parser moves nodes back up later).
    /// The gate stops at the first too-deep moment, by design.
    deep_midway: usize,
    /// Pages whose verdicts were checked against the oracle.
    checked: usize,
    /// The deepest tree the module built.
    deepest: usize,
}

impl Seen {
    fn share(&self) -> f64 {
        self.gated as f64 / self.pages as f64
    }

    /// Fails with the first page whose tree was too deep.
    fn assert_bounded(&self) {
        if let Some(first) = &self.first_over {
            panic!(
                "{} of {} pages built a tree deeper than {MAX_DEPTH}; the first:\n{first}",
                self.over, self.pages
            );
        }
    }
}

/// Converts html and checks every tree the module built.
fn check(seen: &mut Seen, html: &str, what: &str) -> Trace {
    let t = inspect(html, "https://e.example/");
    let d = [&t.parsed, &t.markdown_input]
        .into_iter()
        .flatten()
        .map(|p| depth(p))
        .max()
        .unwrap_or(0);
    seen.deepest = seen.deepest.max(d);
    if d > MAX_DEPTH {
        seen.over += 1;
        seen.first_over.get_or_insert_with(|| {
            format!(
                "{what}: a tree {d} deep, route {:?}; input:\n{}",
                t.route,
                &html[..html.len().min(4000)]
            )
        });
    }
    seen.pages += 1;
    match t.route {
        Route::Direct => {}
        Route::Flattened => seen.flattened += 1,
        Route::Text => seen.text += 1,
        Route::ContentText => seen.content_text += 1,
    }
    if t.route != Route::Direct {
        seen.gated += 1;
    }
    verdicts_match(seen, html, t.route, what);
    t
}

/// Pages up to this size have the gate's verdicts checked against the
/// oracle (larger ones are slow to parse unbounded: that is the attack).
const CHECK_VERDICT: usize = 64 << 10;

/// Checks that each verdict of the gate is the oracle's: the gate refused
/// a page (or the flattened page) exactly when its real tree, at some
/// change while being parsed, holds an element deeper than the cap.
fn verdicts_match(seen: &mut Seen, html: &str, route: Route, what: &str) {
    let page = prepass::strip(html);
    if page.len() > CHECK_VERDICT {
        return;
    }
    let (deepest, last) = oracle(&page);
    seen.checked += 1;
    // The gate without the tree (used for the content handed to htmd)
    // must say the same.
    assert_eq!(
        fits(&page, Scripting::Off),
        deepest <= MAX_DEPTH,
        "{what}: the shape-only gate disagrees with the oracle ({deepest}):\n{html}"
    );
    let refused = route != Route::Direct && route != Route::ContentText
        || route == Route::ContentText && deepest > MAX_DEPTH;
    assert_eq!(
        refused,
        deepest > MAX_DEPTH,
        "{what}: route {route:?} but the tree reaches {deepest}:\n{html}"
    );
    if refused && last <= MAX_DEPTH {
        seen.deep_midway += 1;
    }
    if refused {
        let flat = prepass::flatten(&page, &mut |_| true).unwrap_or_default();
        let (deepest, _) = oracle(&flat);
        assert_eq!(
            route == Route::Text,
            deepest > MAX_DEPTH,
            "{what}: route {route:?} but the flattened tree reaches {deepest}:\n{html}"
        );
    }
}

/// The oracle: dom_query's own sink, wrapped to measure the real tree
/// after every change that places nodes (the placed node's depth plus the
/// height of its subtree; a template's contents count through the
/// template, as in the gate). Returns the deepest seen at any change, and
/// the depth of the finished tree.
fn oracle(html: &str) -> (usize, usize) {
    let opts = html5ever::ParseOpts {
        tree_builder: html5ever::tree_builder::TreeBuilderOpts {
            scripting_enabled: false,
            ..Default::default()
        },
        ..Default::default()
    };
    let sink = Oracle {
        doc: Document::default(),
        deepest: Cell::new(0),
        owners: RefCell::new(HashMap::new()),
    };
    let out = html5ever::parse_document(sink, opts).one(html);
    let last = depth_through(&out, out.doc.tree.root_id());
    (out.deepest.get(), last)
}

struct Oracle {
    doc: Document,
    deepest: Cell<usize>,
    /// template contents → template.
    owners: RefCell<HashMap<NodeId, NodeId>>,
}

impl Oracle {
    fn node(&self, id: NodeId) -> NodeRef<'_> {
        NodeRef {
            id,
            tree: &self.doc.tree,
        }
    }

    /// Element levels from the document down to id, itself included.
    fn depth_of(&self, id: NodeId) -> usize {
        let owners = self.owners.borrow();
        let mut d = 0;
        let mut cur = Some(id);
        while let Some(c) = cur {
            let n = self.node(c);
            d += usize::from(n.is_element());
            cur = n.parent().map(|p| p.id).or_else(|| owners.get(&c).copied());
        }
        d
    }

    fn placed(&self, id: NodeId) {
        let d =
            self.depth_of(id) + depth_through(self, id) - usize::from(self.node(id).is_element());
        self.deepest.set(self.deepest.get().max(d));
    }
}

fn node_of(child: &NodeOrText<NodeId>) -> Option<NodeId> {
    match child {
        NodeOrText::AppendNode(id) => Some(*id),
        NodeOrText::AppendText(_) => None,
    }
}

/// Element levels from id down to its deepest element, id included,
/// entering template contents.
fn depth_through(o: &Oracle, id: NodeId) -> usize {
    let owned: HashMap<NodeId, NodeId> = o.owners.borrow().iter().map(|(c, t)| (*t, *c)).collect();
    let mut max = 0;
    let mut stack = vec![(id, 0usize)];
    while let Some((cur, d)) = stack.pop() {
        let n = o.node(cur);
        let d = d + usize::from(n.is_element());
        max = max.max(d);
        stack.extend(n.children_it(false).map(|c| (c.id, d)));
        if let Some(c) = owned.get(&cur) {
            stack.push((*c, d));
        }
    }
    max
}

impl TreeSink for Oracle {
    type Handle = NodeId;
    type Output = Self;
    type ElemName<'a> = <Document as TreeSink>::ElemName<'a>;

    fn finish(self) -> Self {
        self
    }
    fn parse_error(&self, _msg: Cow<'static, str>) {}
    fn get_document(&self) -> NodeId {
        self.doc.get_document()
    }
    fn elem_name<'a>(&'a self, target: &'a NodeId) -> Self::ElemName<'a> {
        self.doc.elem_name(target)
    }
    fn create_element(&self, name: QualName, attrs: Vec<Attribute>, flags: ElementFlags) -> NodeId {
        let template = flags.template;
        let id = self.doc.create_element(name, attrs, flags);
        if template {
            let contents = self.doc.get_template_contents(&id);
            self.owners.borrow_mut().insert(contents, id);
        }
        id
    }
    fn create_comment(&self, text: StrTendril) -> NodeId {
        self.doc.create_comment(text)
    }
    fn create_pi(&self, target: StrTendril, data: StrTendril) -> NodeId {
        self.doc.create_pi(target, data)
    }
    fn append(&self, parent: &NodeId, child: NodeOrText<NodeId>) {
        let id = node_of(&child);
        self.doc.append(parent, child);
        if let Some(id) = id {
            self.placed(id);
        }
    }
    fn append_based_on_parent_node(
        &self,
        element: &NodeId,
        prev: &NodeId,
        child: NodeOrText<NodeId>,
    ) {
        let id = node_of(&child);
        self.doc.append_based_on_parent_node(element, prev, child);
        if let Some(id) = id {
            self.placed(id);
        }
    }
    fn append_doctype_to_document(&self, name: StrTendril, public: StrTendril, system: StrTendril) {
        self.doc.append_doctype_to_document(name, public, system);
    }
    fn get_template_contents(&self, target: &NodeId) -> NodeId {
        self.doc.get_template_contents(target)
    }
    fn same_node(&self, x: &NodeId, y: &NodeId) -> bool {
        self.doc.same_node(x, y)
    }
    fn set_quirks_mode(&self, mode: QuirksMode) {
        self.doc.set_quirks_mode(mode);
    }
    fn append_before_sibling(&self, sibling: &NodeId, child: NodeOrText<NodeId>) {
        let id = node_of(&child);
        self.doc.append_before_sibling(sibling, child);
        if let Some(id) = id {
            self.placed(id);
        }
    }
    fn add_attrs_if_missing(&self, target: &NodeId, attrs: Vec<Attribute>) {
        self.doc.add_attrs_if_missing(target, attrs);
    }
    fn remove_from_parent(&self, target: &NodeId) {
        self.doc.remove_from_parent(target);
    }
    fn reparent_children(&self, node: &NodeId, new_parent: &NodeId) {
        self.doc.reparent_children(node, new_parent);
        self.placed(*new_parent);
    }
    fn is_mathml_annotation_xml_integration_point(&self, handle: &NodeId) -> bool {
        self.doc.is_mathml_annotation_xml_integration_point(handle)
    }
}

/// Pages from `units` random units repeated `reps` times and `soups` mixed
/// soups of `tags` tags.
fn run(seed: u64, units: usize, reps: usize, soups: usize, tags: usize) -> Seen {
    let mut rng = Rng(seed);
    let mut seen = Seen::default();
    for k in 0..units {
        let u = unit(&mut rng);
        check(
            &mut seen,
            &page(&repeat(&u, reps)),
            &format!("seed {seed:#x}, unit {k} {u:?} x{reps}"),
        );
    }
    for k in 0..soups {
        let s = soup(&mut rng, tags);
        check(
            &mut seen,
            &page(&s),
            &format!("seed {seed:#x}, soup {k} of {tags} tags"),
        );
    }
    eprintln!(
        "seed {seed:#x}: {seen:?}, gated {:.1}%",
        100.0 * seen.share()
    );
    seen
}

// Each test asserts the least share of its pages that must reach the
// gate: below it the generator has stopped producing deep pages and the
// test proves little. The shares are set about 10 points under what the
// seeds give.

#[test]
fn repeated_units_stay_under_the_cap() {
    let seen = run(0x6a09_e667_f3bc_c908, 3000, 400, 0, 0);
    seen.assert_bounded();
    assert!(
        seen.share() >= 0.5,
        "only {:.1}% gated",
        100.0 * seen.share()
    );
}

#[test]
fn mixed_soups_stay_under_the_cap() {
    let seen = run(0xbb67_ae85_84ca_a73b, 0, 0, 1500, 2400);
    seen.assert_bounded();
    assert!(
        seen.share() >= 0.4,
        "only {:.1}% gated",
        100.0 * seen.share()
    );
    let seen = run(0x3c6e_f372_fe94_f82b, 0, 0, 60, 20_000);
    seen.assert_bounded();
    assert!(
        seen.share() >= 0.3,
        "only {:.1}% gated",
        100.0 * seen.share()
    );
}

#[test]
#[ignore]
fn stress_more_seeds() {
    for seed in [1u64, 2, 3, 0xdead_beef, 0x1234_5678_9abc] {
        run(seed, 5000, 400, 3000, 2400).assert_bounded();
        run(seed ^ 0x5555, 0, 0, 100, 20_000).assert_bounded();
    }
}

/// The reviewer's shapes (tests/shapes.txt), each repeated 600 times and
/// to about 120 KiB, keep the cap and both markers.
#[test]
fn regression_corpus() {
    let mut seen = Seen::default();
    let mut lost = Vec::new();
    let shapes = include_str!("shapes.txt")
        .lines()
        .filter(|l| !l.is_empty() && !l.starts_with('#'));
    for shape in shapes {
        for reps in [600, (120 << 10) / shape.len()] {
            let html = page(&repeat(shape, reps));
            let t = check(&mut seen, &html, &format!("{shape:?} x{reps}"));
            let md = &t.page.markdown;
            let kept = md.contains("start") && md.contains("end");
            if !kept && !lost_end(shape) && !end_is_not_content(&t) {
                lost.push(format!(
                    "{shape:?} x{reps} ({:?}): {}",
                    t.route,
                    &md[..md.len().min(200)]
                ));
            }
        }
    }
    eprintln!("corpus: {seen:?}");
    seen.assert_bounded();
    assert!(
        lost.is_empty(),
        "{} pages lost a marker:\n{}",
        lost.len(),
        lost.join("\n")
    );
    // Most of these pages are too deep; the rest were too deep only
    // after the round-3 model dropped end tags it took for stray ones.
    assert!(
        seen.share() >= 0.9,
        "only {:.1}% gated",
        100.0 * seen.share()
    );
}

/// Elements the extractor removes with their content (src/extract.rs).
const DROPPED: &[&str] = &[
    "script", "style", "noscript", "template", "iframe", "frame", "frameset", "object", "embed",
    "svg", "math", "canvas", "video", "audio", "head", "nav", "button", "input", "select",
    "textarea", "dialog", "link",
];

/// Reports whether the parsed page holds the end marker only inside
/// elements the extractor drops (a select's text is not content).
fn end_is_not_content(t: &Trace) -> bool {
    let Some(parsed) = &t.parsed else {
        return false;
    };
    let doc = Document::from(parsed.as_str());
    let mut stack = vec![doc.root()];
    while let Some(n) = stack.pop() {
        if n.is_text() && n.text().contains("end") {
            let dropped = n.ancestors_it(None).any(|a| {
                a.node_name()
                    .is_some_and(|name| DROPPED.contains(&name.to_ascii_lowercase().as_str()))
            });
            if !dropped {
                return false;
            }
        }
        stack.extend(n.children_it(false));
    }
    true
}

/// Shapes after which the end marker is not in the page at all: the
/// pre-pass drops a raw-text body up to its end tag, a frameset replaces
/// the body, an iframe or noembed reads the rest as raw text.
fn lost_end(shape: &str) -> bool {
    let s = shape.to_ascii_lowercase();
    [
        "<frameset>",
        "<template>",
        "<textarea>",
        "<xmp>",
        "<iframe>",
        "<noembed>",
    ]
    .iter()
    .any(|t| s.contains(t))
}
