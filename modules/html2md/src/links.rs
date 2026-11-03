//! Links in the content: made absolute against the page's address, and
//! kept only for web and mail addresses. A `javascript:` or `data:` link
//! is reduced to its text.

use dom_query::NodeRef;

/// The page's address, split for resolving references.
pub struct Base {
    scheme: String,
    authority: String,
    path: String,
}

impl Base {
    /// Parses an absolute http(s) address; None for anything else.
    pub fn parse(url: &str) -> Option<Base> {
        let (scheme, rest) = url.split_once("://")?;
        let scheme = scheme.to_ascii_lowercase();
        if scheme != "http" && scheme != "https" {
            return None;
        }
        let end = rest.find(['/', '?', '#']).unwrap_or(rest.len());
        let (authority, tail) = rest.split_at(end);
        if authority.is_empty() {
            return None;
        }
        let path_end = tail.find(['?', '#']).unwrap_or(tail.len());
        let path = if tail[..path_end].is_empty() {
            "/"
        } else {
            &tail[..path_end]
        };
        Some(Base {
            scheme,
            authority: authority.to_string(),
            path: path.to_string(),
        })
    }

    /// Resolves reference against the base; None for an in-page anchor
    /// or a scheme other than http, https and mailto.
    pub fn resolve(&self, reference: &str) -> Option<String> {
        let r = reference.trim();
        if r.is_empty() || r.starts_with('#') {
            return None;
        }
        if let Some(scheme) = scheme_of(r) {
            return matches!(scheme.as_str(), "http" | "https" | "mailto").then(|| r.to_string());
        }
        if let Some(rest) = r.strip_prefix("//") {
            return Some(format!("{}://{}", self.scheme, rest));
        }
        let origin = format!("{}://{}", self.scheme, self.authority);
        if r.starts_with('/') {
            return Some(origin + &dot_segments(r));
        }
        if r.starts_with('?') {
            return Some(origin + &self.path + r);
        }
        let dir = &self.path[..=self.path.rfind('/').unwrap_or(0)];
        Some(origin + &dot_segments(&format!("{dir}{r}")))
    }
}

/// The scheme of an absolute reference, lower-cased.
fn scheme_of(r: &str) -> Option<String> {
    let (scheme, _) = r.split_once(':')?;
    let mut chars = scheme.chars();
    let first = chars.next()?;
    (first.is_ascii_alphabetic() && chars.all(|c| c.is_ascii_alphanumeric() || "+-.".contains(c)))
        .then(|| scheme.to_ascii_lowercase())
}

/// Removes "." and ".." segments from the path part of reference.
fn dot_segments(reference: &str) -> String {
    let end = reference.find(['?', '#']).unwrap_or(reference.len());
    let (path, rest) = reference.split_at(end);
    let mut out: Vec<&str> = Vec::new();
    let segments: Vec<&str> = path.split('/').collect();
    for (i, seg) in segments.iter().enumerate() {
        match *seg {
            "." => {}
            ".." => {
                if out.len() > 1 {
                    out.pop();
                }
            }
            s => out.push(s),
        }
        if (*seg == "." || *seg == "..") && i == segments.len() - 1 {
            out.push("");
        }
    }
    out.join("/") + rest
}

/// Rewrites the links under root: an absolute web or mail address, or no
/// href at all; titles and other attributes go, so only the text and the
/// address reach the Markdown. A link with no text is removed.
pub fn rewrite(root: &NodeRef, base: Option<&Base>) {
    let mut stack = vec![*root];
    while let Some(n) = stack.pop() {
        stack.extend(n.children_it(true));
        if !n.is_element() || n.node_name().map(|s| s.to_ascii_lowercase()).as_deref() != Some("a")
        {
            continue;
        }
        if n.text().trim().is_empty() {
            n.remove_from_parent(); // an icon link: nothing to read
            continue;
        }
        let href = n.attr("href").map(|h| h.to_string()).unwrap_or_default();
        let resolved = match base {
            Some(b) => b.resolve(&href),
            None => scheme_of(&href)
                .filter(|s| matches!(s.as_str(), "http" | "https" | "mailto"))
                .map(|_| href.trim().to_string()),
        };
        n.remove_all_attrs();
        if let Some(h) = resolved {
            n.set_attr("href", &h);
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn resolves_references() {
        let b = Base::parse("https://Example.org/a/b/c.html?q=1#f").unwrap();
        for (r, want) in [
            ("/x", Some("https://Example.org/x")),
            ("x", Some("https://Example.org/a/b/x")),
            ("../x", Some("https://Example.org/a/x")),
            ("./x?y=1", Some("https://Example.org/a/b/x?y=1")),
            ("?p=2", Some("https://Example.org/a/b/c.html?p=2")),
            ("//cdn.example/x", Some("https://cdn.example/x")),
            ("HTTP://other.example/", Some("HTTP://other.example/")),
            ("mailto:a@b.example", Some("mailto:a@b.example")),
            ("javascript:alert(1)", None),
            (" JavaScript:alert(1)", None),
            ("data:text/html,x", None),
            ("#top", None),
            ("", None),
        ] {
            assert_eq!(b.resolve(r).as_deref(), want, "{r}");
        }
    }

    #[test]
    fn bad_bases() {
        assert!(Base::parse("ftp://example.org/").is_none());
        assert!(Base::parse("https:///x").is_none());
        assert!(Base::parse("").is_none());
        assert_eq!(
            Base::parse("https://example.org")
                .unwrap()
                .resolve("x")
                .as_deref(),
            Some("https://example.org/x")
        );
    }
}
