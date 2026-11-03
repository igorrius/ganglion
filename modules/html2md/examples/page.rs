//! Converts a saved page: cargo run --release --example page -- page.html [url]
fn main() {
    let mut args = std::env::args().skip(1);
    let path = args.next().expect("page.html");
    let url = args
        .next()
        .unwrap_or_else(|| "https://example.org/".to_string());
    let html = std::fs::read(&path).expect("read page");
    let p = html2md::convert(&String::from_utf8_lossy(&html), &url);
    println!("# {}\n\n{}", p.title, p.markdown);
}
