//! The page's files, compiled into the module: the node serves nothing from
//! disk for it. Under the node's policy (`default-src 'self'`) the page may
//! load only these files from its own address: no inline script, no inline
//! style (`style=` attributes included), no outside address.

pub const INDEX: &str = include_str!("../assets/index.html");
pub const SCRIPT: &str = include_str!("../assets/app.js");
pub const STYLE: &str = include_str!("../assets/app.css");

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_page_has_no_inline_script_style_handler_or_outside_address() {
        let page = INDEX.to_ascii_lowercase();
        for script in page.split("<script").skip(1) {
            let (tag, rest) = script.split_once('>').unwrap();
            assert!(tag.contains("src="), "an inline script: <script{tag}>");
            assert!(rest.starts_with("</script>"), "script text inside a tag");
        }
        assert!(!page.contains("<style"), "an inline style block");
        assert!(!page.contains("style="), "an inline style attribute");
        for tag in page.split('<').skip(1) {
            let tag = tag.split('>').next().unwrap();
            assert!(
                !tag.split_whitespace().any(|w| w.starts_with("on")),
                "an event-handler attribute in <{tag}>"
            );
        }
        for bad in ["http://", "https://", "//cdn", "javascript:"] {
            assert!(!page.contains(bad), "{bad} in the page");
        }
    }

    #[test]
    fn the_script_builds_the_page_without_parsing_markup() {
        // The SVG namespace is a name, not an address the page loads.
        let script = SCRIPT
            .replace("http://www.w3.org/2000/svg", "")
            .to_ascii_lowercase();
        for bad in [
            "innerhtml",
            "outerhtml",
            "insertadjacenthtml",
            "document.write",
            "eval(",
            "new function",
            "settimeout(\"",
            "http://",
            "https://",
            ".style.csstext",
            "setattribute(\"style\"",
            "setattribute('style'",
        ] {
            assert!(!script.contains(bad), "{bad} in app.js");
        }
    }

    #[test]
    fn the_style_loads_nothing_from_outside() {
        let s = STYLE.to_ascii_lowercase();
        assert!(!s.contains("@import") && !s.contains("url("));
    }
}
