//! The depth gate (ruling R25): html5ever's own tree builder parses the
//! page with a sink that follows the shape of the tree, and the parse
//! stops as soon as an element would be deeper than MAX_DEPTH. `parse`
//! wraps dom_query's sink, so one parse builds the tree and measures it;
//! `fits` keeps only the shape, for a string another parser will read
//! (htmd's input). Nothing here models HTML: every rule (implied end
//! tags, the adoption agency, foster parenting, foreign content) is
//! html5ever's.
//!
//! The sink keeps each node's parent exactly, including through the
//! parser's moves (adoption agency, foster parenting, reparenting), and
//! a height bound per node that may be too high, never too low (it does
//! not shrink when a node is moved away). Each change checks the bound;
//! when it says "too deep", the heights are recounted exactly from the
//! parents and the change is checked again, so a page is refused only
//! when its tree really is too deep. Recounts are rationed so that their
//! total cost stays linear; past the ration the bound's verdict stands,
//! which may refuse a page that fits, never pass one that does not.

use std::borrow::Cow;
use std::cell::{Cell, RefCell};
use std::hash::Hash;
use std::rc::Rc;

use dom_query::{Document, NodeId};
use html5ever::interface::{ElementFlags, NodeOrText, QuirksMode, TreeSink};
use html5ever::tendril::{StrTendril, TendrilSink};
use html5ever::tree_builder::TreeBuilderOpts;
use html5ever::{Attribute, ExpandedName, ParseOpts, QualName, local_name, ns};

/// The deepest element any tree the module builds may have; the html
/// element is at depth 1.
pub const MAX_DEPTH: usize = 256;

/// How much of the page the parser gets between two checks.
const CHUNK: usize = 4096;

/// How the page will be parsed: dom_query parses with scripting off,
/// htmd with scripting on (it changes how `<noscript>` is read).
#[derive(Clone, Copy)]
pub enum Scripting {
    Off,
    On,
}

/// Reports whether html parses to a tree at most MAX_DEPTH deep, without
/// building it (for the content handed to htmd, which parses it itself).
pub fn fits(html: &str, scripting: Scripting) -> bool {
    let mut gate = Gate::new(Shape::default(), scripting);
    gate.feed(html) && gate.finish()
}

/// Parses html with dom_query, unless its tree would be deeper than
/// MAX_DEPTH: then it stops as soon as it would, and returns None.
pub fn parse(html: &str) -> Option<Document> {
    let mut gate = Gate::new(Gated::default(), Scripting::Off);
    if gate.feed(html) { gate.finish() } else { None }
}

/// dom_query's sink with the shape alongside: one parse builds the tree
/// and measures it. A NodeId's index is private to dom_query; its derived
/// Hash writes exactly that index, which `index` reads back.
#[derive(Default)]
pub struct Gated {
    doc: Document,
    tree: RefCell<Tree>,
    over: Cell<bool>,
    /// node → a comment or processing instruction: never a level.
    leaves: RefCell<Vec<bool>>,
}

/// A Hasher that keeps the one usize written to it.
#[derive(Default)]
struct Index(usize);

impl std::hash::Hasher for Index {
    fn finish(&self) -> u64 {
        self.0 as u64
    }
    fn write(&mut self, _bytes: &[u8]) {
        unreachable!("NodeId hashes one usize");
    }
    fn write_usize(&mut self, i: usize) {
        self.0 = i;
    }
}

fn index(id: &NodeId) -> u32 {
    let mut h = Index::default();
    id.hash(&mut h);
    h.0 as u32
}

impl Gated {
    fn change(&self, f: impl FnOnce(&mut Tree) -> Option<u32>) {
        if self.over.get() {
            return;
        }
        let mut tree = self.tree.borrow_mut();
        let fits = f(&mut tree).is_some_and(|moved| tree.check(moved));
        if !fits {
            self.over.set(true);
        }
    }

    /// The node's index in the shape, or None for a leaf (a comment).
    fn shaped(&self, id: &NodeId) -> Option<u32> {
        let i = index(id);
        if self.leaves.borrow().get(i as usize) == Some(&true) {
            return None;
        }
        self.tree.borrow_mut().ensure(i);
        Some(i)
    }

    fn leaf(&self, id: NodeId) -> NodeId {
        let i = index(&id) as usize;
        let mut leaves = self.leaves.borrow_mut();
        if leaves.len() <= i {
            leaves.resize(i + 1, false);
        }
        leaves[i] = true;
        id
    }

    fn placed(&self, parent: &NodeId, child: Option<NodeId>) {
        if let (Some(p), Some(c)) = (self.shaped(parent), child.and_then(|c| self.shaped(&c))) {
            self.change(|t| t.attach(p, c));
        }
    }
}

impl Measured for Gated {
    fn over(&self) -> bool {
        self.over.get()
    }
}

fn node_of(child: &NodeOrText<NodeId>) -> Option<NodeId> {
    match child {
        NodeOrText::AppendNode(id) => Some(*id),
        NodeOrText::AppendText(_) => None,
    }
}

impl TreeSink for Gated {
    type Handle = NodeId;
    /// The tree, if it stayed within MAX_DEPTH.
    type Output = Option<Document>;
    type ElemName<'a> = <Document as TreeSink>::ElemName<'a>;

    fn finish(self) -> Option<Document> {
        (!self.over.get()).then_some(self.doc)
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
        let i = index(&id);
        let mut tree = self.tree.borrow_mut();
        tree.ensure(i);
        tree.n[i as usize].level = 1;
        if template {
            // The contents hang under the template: their depth counts
            // through it, whatever dom_query does with them.
            let c = index(&self.doc.get_template_contents(&id));
            tree.ensure(c);
            let _ = tree.attach(i, c);
        }
        id
    }

    fn create_comment(&self, text: StrTendril) -> NodeId {
        self.leaf(self.doc.create_comment(text))
    }

    fn create_pi(&self, target: StrTendril, data: StrTendril) -> NodeId {
        self.leaf(self.doc.create_pi(target, data))
    }

    fn append(&self, parent: &NodeId, child: NodeOrText<NodeId>) {
        let c = node_of(&child);
        self.doc.append(parent, child);
        self.placed(parent, c);
    }

    fn append_based_on_parent_node(
        &self,
        element: &NodeId,
        prev_element: &NodeId,
        child: NodeOrText<NodeId>,
    ) {
        let has_parent = self
            .shaped(element)
            .is_some_and(|e| self.tree.borrow().parent(e).is_some());
        if has_parent {
            self.append_before_sibling(element, child);
        } else {
            self.append(prev_element, child);
        }
    }

    fn append_doctype_to_document(&self, name: StrTendril, public: StrTendril, system: StrTendril) {
        self.doc.append_doctype_to_document(name, public, system);
    }

    fn get_template_contents(&self, target: &NodeId) -> NodeId {
        self.doc.get_template_contents(target)
    }

    fn same_node(&self, x: &NodeId, y: &NodeId) -> bool {
        x == y
    }

    fn set_quirks_mode(&self, mode: QuirksMode) {
        self.doc.set_quirks_mode(mode);
    }

    fn append_before_sibling(&self, sibling: &NodeId, new_node: NodeOrText<NodeId>) {
        let c = node_of(&new_node);
        self.doc.append_before_sibling(sibling, new_node);
        let Some(c) = c.and_then(|c| self.shaped(&c)) else {
            return;
        };
        let parent = self
            .shaped(sibling)
            .and_then(|s| self.tree.borrow().parent(s));
        match parent {
            Some(p) => self.change(|t| t.attach(p, c)),
            // A sibling without a parent leaves the new node detached.
            None => self.tree.borrow_mut().detach(c),
        }
    }

    fn add_attrs_if_missing(&self, target: &NodeId, attrs: Vec<Attribute>) {
        self.doc.add_attrs_if_missing(target, attrs);
    }

    fn remove_from_parent(&self, target: &NodeId) {
        self.doc.remove_from_parent(target);
        if let Some(t) = self.shaped(target) {
            self.tree.borrow_mut().detach(t);
        }
    }

    fn reparent_children(&self, node: &NodeId, new_parent: &NodeId) {
        self.doc.reparent_children(node, new_parent);
        if let (Some(n), Some(np)) = (self.shaped(node), self.shaped(new_parent)) {
            self.change(|t| t.reparent(n, np));
        }
    }

    fn is_mathml_annotation_xml_integration_point(&self, handle: &NodeId) -> bool {
        self.doc.is_mathml_annotation_xml_integration_point(handle)
    }
}

/// A sink that tells whether its tree went too deep.
pub trait Measured: TreeSink {
    fn over(&self) -> bool;
}

/// The parser over a page that arrives in pieces (the pre-pass's output
/// as it is written), so that the writer can stop once it is refused.
pub struct Gate<S: Measured> {
    parser: html5ever::driver::Parser<S>,
}

impl<S: Measured> Gate<S> {
    pub fn new(sink: S, scripting: Scripting) -> Self {
        let opts = ParseOpts {
            tree_builder: TreeBuilderOpts {
                scripting_enabled: matches!(scripting, Scripting::On),
                ..Default::default()
            },
            ..Default::default()
        };
        Gate {
            parser: html5ever::parse_document(sink, opts),
        }
    }

    /// Parses the next piece, CHUNK bytes at a time; false as soon as the
    /// tree is too deep.
    pub fn feed(&mut self, mut rest: &str) -> bool {
        while !rest.is_empty() {
            if self.over() {
                return false;
            }
            let mut cut = rest.len().min(CHUNK);
            while !rest.is_char_boundary(cut) {
                cut += 1;
            }
            self.parser.process(StrTendril::from_slice(&rest[..cut]));
            rest = &rest[cut..];
        }
        !self.over()
    }

    /// Ends the page (the end of input can still add nodes, an implied
    /// body); the sink's result says whether the tree stayed within.
    pub fn finish(self) -> S::Output {
        self.parser.finish()
    }

    fn over(&self) -> bool {
        self.parser.tokenizer.sink.sink.over()
    }
}

/// No node or container.
const NONE: u32 = u32::MAX;

/// A node as the tree builder holds it: an index into Shape and what the
/// builder asks about it.
#[derive(Clone)]
pub struct Handle(Rc<Node>);

struct Node {
    id: u32,
    name: QualName,
    integration_point: bool,
    /// For a template, the node of its contents; else NONE.
    contents: u32,
}

/// The tree's shape, in two arrays. A node hangs in a container, and a
/// container belongs to a node: the children of a node are the nodes in
/// its containers. So moving all children of a node (reparent_children)
/// is giving their container to another node, without visiting them.
#[derive(Default)]
struct Tree {
    n: Vec<NodeShape>,
    c: Vec<Container>,
    /// Exact recounts done, and the number of nodes at the last one.
    recounts: usize,
    recounted_at: usize,
    /// The current epoch of cached depths.
    epoch: u32,
    /// Scratch for depth walks.
    path: Vec<u32>,
}

#[derive(Clone)]
struct NodeShape {
    /// The container it hangs in, or NONE when detached.
    up: u32,
    /// The container its new children go to.
    kids: u32,
    /// At least the number of element levels below it. It only grows:
    /// when the parser removes or moves nodes the old bound stays.
    height: u32,
    /// Its depth in its tree (levels from its root, itself included),
    /// valid while `stamp` equals the tree's epoch. Any move of a node
    /// that was already placed starts a new epoch; appending a new node
    /// keeps every cached depth right.
    depth: u32,
    stamp: u32,
    /// 1 for an element, 0 for the document, a template's contents and
    /// other nodes that are not a level.
    level: u8,
    /// Whether it was ever placed under a parent.
    placed: bool,
}

#[derive(Clone)]
struct Container {
    /// The node it belongs to.
    owner: u32,
    /// Whether a node was ever put in it.
    used: bool,
}

impl Tree {
    fn node(&mut self, level: u8) -> u32 {
        let id = self.n.len() as u32;
        let kids = self.container(id);
        self.n.push(NodeShape {
            up: NONE,
            kids,
            height: 0,
            depth: 0,
            stamp: u32::MAX,
            level,
            placed: false,
        });
        id
    }

    /// Adds nodes (not levels) until id is one: dom_query numbers every
    /// node it makes, text nodes too.
    fn ensure(&mut self, id: u32) {
        while self.n.len() <= id as usize {
            self.node(0);
        }
    }

    fn container(&mut self, owner: u32) -> u32 {
        self.c.push(Container { owner, used: false });
        (self.c.len() - 1) as u32
    }

    /// The node x hangs under, if any.
    fn parent(&self, x: u32) -> Option<u32> {
        match self.n[x as usize].up {
            NONE => None,
            c => Some(self.c[c as usize].owner),
        }
    }

    /// x's depth in its tree, from the cache where it is valid. A walk
    /// past MAX_DEPTH levels stops there and reports more, and so does a
    /// path longer than any tree within MAX_DEPTH can have (a cycle, which
    /// the parser never makes): between two elements there is at most one
    /// node that is not a level (a template's contents).
    fn depth_of(&mut self, x: u32) -> usize {
        self.path.clear();
        let mut cur = x;
        let mut base = 0;
        let mut levels = 0;
        loop {
            if self.n[cur as usize].stamp == self.epoch {
                base = self.n[cur as usize].depth;
                break;
            }
            self.path.push(cur);
            levels += usize::from(self.n[cur as usize].level);
            if levels > MAX_DEPTH || self.path.len() > 2 * MAX_DEPTH + 4 {
                return MAX_DEPTH + 1;
            }
            match self.parent(cur) {
                Some(p) => cur = p,
                None => break,
            }
        }
        let mut d = base;
        for &y in self.path.iter().rev() {
            d += u32::from(self.n[y as usize].level);
            self.n[y as usize].depth = d;
            self.n[y as usize].stamp = self.epoch;
        }
        self.n[x as usize].depth as usize
    }

    /// Records that x's subtree changed place or height: reports whether
    /// its deepest element is still within MAX_DEPTH, and raises the
    /// height bounds of x's ancestors as far as they fall short. Every
    /// edge keeps height(parent) ≥ height(child) + level(child), so the
    /// raise stops at the first ancestor that already covers it.
    fn settle(&mut self, x: u32) -> bool {
        if self.depth_of(x) + self.n[x as usize].height as usize > MAX_DEPTH {
            return false;
        }
        let mut cur = x;
        while let Some(p) = self.parent(cur) {
            let need = self.n[cur as usize].height + u32::from(self.n[cur as usize].level);
            if self.n[p as usize].height >= need {
                break;
            }
            self.n[p as usize].height = need;
            cur = p;
        }
        true
    }

    /// Takes x off its parent.
    fn detach(&mut self, x: u32) {
        self.n[x as usize].up = NONE;
        self.epoch = self.epoch.wrapping_add(1);
    }

    /// Hangs child under parent, as the last child or before a sibling
    /// (the depth is the same); returns the node whose subtree moved.
    fn attach(&mut self, parent: u32, child: u32) -> Option<u32> {
        let ch = child as usize;
        if self.n[ch].placed || self.c[self.n[ch].kids as usize].used {
            // A move: the cached depths under child are stale.
            self.epoch = self.epoch.wrapping_add(1);
        } else {
            // A new leaf: only its own depth may be cached, from when it
            // was checked as a root of its own (the adoption agency's new
            // element after reparent_children).
            self.n[ch].stamp = u32::MAX;
        }
        let c = self.n[parent as usize].kids;
        self.c[c as usize].used = true;
        self.n[ch].up = c;
        self.n[ch].placed = true;
        Some(child)
    }

    /// Moves every child of node under new_parent. The tree builder only
    /// does this into an element it has just made (the adoption agency's
    /// step 16), so new_parent's container is empty and the two swap. A
    /// new_parent with children would need a merge; it is refused instead
    /// (None).
    fn reparent(&mut self, node: u32, new_parent: u32) -> Option<u32> {
        let empty = self.n[new_parent as usize].kids;
        if self.c[empty as usize].used {
            return None;
        }
        let moved = self.n[node as usize].kids;
        self.c[moved as usize].owner = new_parent;
        self.n[new_parent as usize].kids = moved;
        self.c[empty as usize].owner = node;
        self.c[empty as usize].used = false;
        self.n[node as usize].kids = empty;
        // node has no children now: its bound is exact again. The moved
        // children take node's old bound with them.
        let h = std::mem::take(&mut self.n[node as usize].height);
        let np = &mut self.n[new_parent as usize].height;
        *np = (*np).max(h);
        self.epoch = self.epoch.wrapping_add(1);
        Some(new_parent)
    }

    /// Reports whether x's subtree, just moved, is within MAX_DEPTH:
    /// first by the bounds, then, if they say no and the ration allows,
    /// by exact heights.
    fn check(&mut self, x: u32) -> bool {
        if self.settle(x) {
            return true;
        }
        let nodes = self.n.len();
        if self.recounts >= 4 && nodes < 2 * self.recounted_at {
            return false;
        }
        self.recounts += 1;
        self.recounted_at = nodes;
        self.recount();
        self.settle(x)
    }

    /// Sets every height to the exact number of element levels below the
    /// node, from the parents: each node's depth in its tree (memoised
    /// walks up), then the heights from the deepest nodes up. O(nodes).
    fn recount(&mut self) {
        const UNKNOWN: u32 = u32::MAX;
        let n = self.n.len();
        let mut depth = vec![UNKNOWN; n];
        let mut path = Vec::new();
        for start in 0..n as u32 {
            let mut x = start;
            while depth[x as usize] == UNKNOWN {
                path.push(x);
                match self.parent(x) {
                    Some(p) if path.len() <= n => x = p,
                    _ => break,
                }
            }
            let mut d = 0;
            if depth[x as usize] != UNKNOWN {
                d = depth[x as usize];
            }
            while let Some(y) = path.pop() {
                d += u32::from(self.n[y as usize].level);
                depth[y as usize] = d;
            }
        }
        let deepest = depth.iter().copied().max().unwrap_or(0) as usize;
        let mut by_depth = vec![Vec::new(); deepest + 1];
        for (x, d) in depth.iter().enumerate() {
            by_depth[*d as usize].push(x as u32);
        }
        self.n.iter_mut().for_each(|x| x.height = 0);
        for x in by_depth.into_iter().rev().flatten() {
            if let Some(p) = self.parent(x) {
                let h = self.n[x as usize].height + u32::from(self.n[x as usize].level);
                let ph = &mut self.n[p as usize].height;
                *ph = (*ph).max(h);
            }
        }
    }
}

/// The sink: the tree's shape and whether it went too deep.
pub struct Shape {
    tree: RefCell<Tree>,
    over: Cell<bool>,
    /// The handle shared by nodes that never hold elements (comments,
    /// processing instructions): they are leaves, not levels, so the
    /// sink does not place them at all.
    leaf: Handle,
    document: Handle,
}

impl Default for Shape {
    fn default() -> Self {
        let mut tree = Tree::default();
        let doc = tree.node(0);
        let leaf = tree.node(0);
        let none = || QualName::new(None, ns!(), local_name!(""));
        Shape {
            tree: RefCell::new(tree),
            over: Cell::new(false),
            leaf: Handle(Rc::new(Node {
                id: leaf,
                name: none(),
                integration_point: false,
                contents: NONE,
            })),
            document: Handle(Rc::new(Node {
                id: doc,
                name: none(),
                integration_point: false,
                contents: NONE,
            })),
        }
    }
}

impl Shape {
    /// Runs a change unless the tree is already too deep; a change that
    /// makes it too deep (or that the sink cannot follow) marks it so.
    fn change(&self, f: impl FnOnce(&mut Tree) -> Option<u32>) {
        if self.over.get() {
            return;
        }
        let mut tree = self.tree.borrow_mut();
        let fits = f(&mut tree).is_some_and(|moved| tree.check(moved));
        if !fits {
            self.over.set(true);
        }
    }

    fn append_node(&self, parent: &Handle, child: NodeOrText<Handle>) {
        if let NodeOrText::AppendNode(c) = child
            && c.0.id != self.leaf.0.id
        {
            self.change(|t| t.attach(parent.0.id, c.0.id));
        }
    }
}

impl Measured for Shape {
    fn over(&self) -> bool {
        self.over.get()
    }
}

impl TreeSink for Shape {
    type Handle = Handle;
    /// Whether the tree stayed within MAX_DEPTH.
    type Output = bool;
    type ElemName<'a> = ExpandedName<'a>;

    fn finish(self) -> bool {
        !self.over.get()
    }

    fn parse_error(&self, _msg: Cow<'static, str>) {}

    fn get_document(&self) -> Handle {
        self.document.clone()
    }

    fn elem_name<'a>(&'a self, target: &'a Handle) -> ExpandedName<'a> {
        target.0.name.expanded()
    }

    fn create_element(
        &self,
        name: QualName,
        _attrs: Vec<Attribute>,
        flags: ElementFlags,
    ) -> Handle {
        let mut tree = self.tree.borrow_mut();
        let id = tree.node(1);
        let mut contents = NONE;
        if flags.template {
            // The contents hang under the template: their depth counts
            // through it, whatever the real tree does with them.
            contents = tree.node(0);
            let _ = tree.attach(id, contents);
        }
        Handle(Rc::new(Node {
            id,
            name,
            integration_point: flags.mathml_annotation_xml_integration_point,
            contents,
        }))
    }

    fn create_comment(&self, _text: StrTendril) -> Handle {
        self.leaf.clone()
    }

    fn create_pi(&self, _target: StrTendril, _data: StrTendril) -> Handle {
        self.leaf.clone()
    }

    fn append(&self, parent: &Handle, child: NodeOrText<Handle>) {
        self.append_node(parent, child);
    }

    fn append_based_on_parent_node(
        &self,
        element: &Handle,
        prev_element: &Handle,
        child: NodeOrText<Handle>,
    ) {
        if self.tree.borrow().parent(element.0.id).is_some() {
            self.append_before_sibling(element, child);
        } else {
            self.append_node(prev_element, child);
        }
    }

    fn append_doctype_to_document(&self, _: StrTendril, _: StrTendril, _: StrTendril) {}

    fn get_template_contents(&self, target: &Handle) -> Handle {
        // Only templates are asked for; anything else stands for itself.
        let id = match target.0.contents {
            NONE => target.0.id,
            c => c,
        };
        Handle(Rc::new(Node {
            id,
            name: QualName::new(None, ns!(), local_name!("")),
            integration_point: false,
            contents: NONE,
        }))
    }

    fn same_node(&self, x: &Handle, y: &Handle) -> bool {
        x.0.id == y.0.id
    }

    fn set_quirks_mode(&self, _mode: QuirksMode) {}

    fn append_before_sibling(&self, sibling: &Handle, new_node: NodeOrText<Handle>) {
        let NodeOrText::AppendNode(c) = new_node else {
            return;
        };
        if c.0.id == self.leaf.0.id {
            return; // a comment is never a level and holds nothing
        }
        // A sibling without a parent leaves the new node detached, as in
        // dom_query.
        let Some(parent) = self.tree.borrow().parent(sibling.0.id) else {
            self.tree.borrow_mut().detach(c.0.id);
            return;
        };
        self.change(|t| t.attach(parent, c.0.id));
    }

    fn add_attrs_if_missing(&self, _target: &Handle, _attrs: Vec<Attribute>) {}

    fn remove_from_parent(&self, target: &Handle) {
        if target.0.id != self.leaf.0.id {
            self.tree.borrow_mut().detach(target.0.id);
        }
    }

    fn reparent_children(&self, node: &Handle, new_parent: &Handle) {
        self.change(|t| t.reparent(node.0.id, new_parent.0.id));
    }

    fn is_mathml_annotation_xml_integration_point(&self, handle: &Handle) -> bool {
        handle.0.integration_point
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn nested(open: &str, n: usize) -> String {
        format!("<title>t</title>{}x", open.repeat(n))
    }

    #[test]
    fn depth_counts_from_the_html_element() {
        // html, body and MAX_DEPTH - 2 divs fit; one more does not.
        assert!(fits(&nested("<div>", MAX_DEPTH - 2), Scripting::Off));
        assert!(!fits(&nested("<div>", MAX_DEPTH - 1), Scripting::Off));
    }

    #[test]
    fn the_parsers_own_rules_count() {
        // rt closes an open p only inside ruby, and the parser does so:
        // each unit nests one ruby deeper.
        assert!(!fits(&nested("<ruby><p><rt>", 300), Scripting::Off));
        // Closed paragraphs never nest.
        assert!(fits(&nested("<p>x", 100_000), Scripting::Off));
        assert!(fits(&nested("<div></div>", 100_000), Scripting::Off));
        // An html start tag inside SVG is an SVG element: it nests.
        assert!(!fits(&nested("<svg><html>", 300), Scripting::Off));
    }

    #[test]
    fn adoption_agency_moves_are_counted() {
        // </b> over a div runs the adoption agency: the div's children
        // move under a new b inside it (reparent_children), so each unit
        // nests deeper although the stack of open elements is shallow.
        assert!(!fits(&nested("<b><div></b>", 300), Scripting::Off));
        assert!(fits(&nested("<b><div></b>", 50), Scripting::Off));
        let distinct: String = (0..300)
            .map(|i| format!("<a href=x{i}><div></a>"))
            .collect();
        assert!(!fits(
            &format!("<title>t</title>{distinct}"),
            Scripting::Off
        ));
    }

    #[test]
    fn foster_parenting_is_counted() {
        // The first x-a in a table goes before it, in the table's parent
        // (append_based_on_parent_node); the others nest in it.
        let deep = format!("<title>t</title><table>{}", "<x-a>".repeat(MAX_DEPTH));
        assert!(!fits(&deep, Scripting::Off));
        let deep = format!("<title>t</title><table>{}", "<x-a>".repeat(MAX_DEPTH - 3));
        assert!(fits(&deep, Scripting::Off));
        assert!(fits(
            &nested("<table><tr><td>x</td></tr></table>", 1000),
            Scripting::Off
        ));
    }

    #[test]
    fn template_contents_count_through_the_template() {
        assert!(!fits(&nested("<template><div>", 200), Scripting::Off));
    }

    #[test]
    fn noscript_follows_the_scripting_flag() {
        // With scripting on, a noscript body is raw text: no depth.
        let page = nested("<noscript><div>", 300);
        assert!(!fits(&page, Scripting::Off));
        assert!(fits(&page, Scripting::On));
    }

    #[test]
    fn a_stale_bound_is_recounted_before_refusing() {
        // A chain of MAX_DEPTH - 10 elements under a, then moved away: a
        // keeps its height bound. Hanging a 20 deep would pass the cap by
        // the bound but not in fact.
        let mut t = Tree::default();
        let doc = t.node(0);
        let a = t.node(1);
        let mut chain = vec![t.node(1)];
        for _ in 1..MAX_DEPTH - 10 {
            let next = t.node(1);
            let last = *chain.last().unwrap();
            assert!(t.attach(last, next).is_some_and(|x| t.check(x)));
            chain.push(next);
        }
        assert!(t.attach(a, chain[0]).is_some_and(|x| t.check(x)));
        t.detach(chain[0]); // remove_from_parent
        let mut at = doc;
        for _ in 0..20 {
            let next = t.node(1);
            assert!(t.attach(at, next).is_some_and(|x| t.check(x)));
            at = next;
        }
        let moved = t.attach(at, a).unwrap();
        assert!(!t.settle(moved), "the bound is stale");
        assert!(t.check(a), "the recount sees that a is a leaf");
        assert_eq!(t.recounts, 1);
        // Hanging the chain back under a is too deep in fact.
        assert!(!t.attach(a, chain[0]).is_some_and(|x| t.check(x)));
    }

    #[test]
    fn a_depth_cached_while_detached_is_not_reused() {
        let mut t = Tree::default();
        let doc = t.node(0);
        let mut at = doc;
        for _ in 0..MAX_DEPTH {
            let next = t.node(1);
            assert!(t.attach(at, next).is_some_and(|x| t.check(x)));
            at = next;
        }
        // A new element checked on its own (depth 1), then hung at the
        // bottom: it is one level too deep.
        let fresh = t.node(1);
        assert!(t.check(fresh));
        assert!(!t.attach(at, fresh).is_some_and(|x| t.check(x)));
    }

    #[test]
    fn comments_are_not_levels() {
        // The deepest element at the cap, with comments in it.
        let page = nested("<div><!--c-->", MAX_DEPTH - 2);
        assert!(fits(&page, Scripting::Off));
        let page = nested("<template><!--c-->", MAX_DEPTH - 2);
        assert!(fits(&page, Scripting::Off));
    }

    #[test]
    fn parse_builds_what_dom_query_builds() {
        for page in [
            nested("<div>", MAX_DEPTH - 2),
            nested("<b><div></b>", 50),
            nested("<template><p>x</template><!--c-->", 100),
            format!("<title>t</title><table>{}", "<x-a>".repeat(100)),
            "<p>a<b>b<i>c</b>d</i>e<table><tr><td>f<div>g</table>".to_string(),
        ] {
            let doc = parse(&page).expect("fits");
            assert_eq!(doc.html(), Document::from(page.as_str()).html());
        }
        for page in [
            nested("<div>", MAX_DEPTH - 1),
            nested("<ruby><p><rt>", 300),
            nested("<template><div>", 200),
        ] {
            assert!(parse(&page).is_none());
            assert!(!fits(&page, Scripting::Off));
        }
    }

    #[test]
    fn chunks_split_inside_a_character() {
        let page = format!("{}{}", "é".repeat(CHUNK), nested("<div>", MAX_DEPTH));
        assert!(!fits(&page, Scripting::Off));
        assert!(fits(&"é".repeat(3 * CHUNK + 1), Scripting::Off));
    }
}
