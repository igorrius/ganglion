//! Differential property test (ruling R24): whatever tag soup comes in,
//! the tree html5ever builds from the pre-pass's output is never much
//! deeper than the pre-pass's cap. Random soups from a fixed seed over an
//! alphabet of every kind of element the parser treats differently.

use dom_query::Document;
use html2md::prepass::{MAX_NESTING, strip};

/// How much deeper than the cap the tree may be: the implied tbody and
/// tr of a cell, the formatting elements the parser reopens over the
/// last counted start tag, and a margin. The deepest tree seen over all
/// the seeds below and in `stress_more_seeds` is 8 over the cap.
const SLACK: usize = 12;

/// Tags of every category: special, formatting (with attributes, which
/// defeat the parser's limit of three identical entries), scope
/// boundaries, optional ends, others, custom, foreign, void,
/// self-closed and stray ends.
const UNITS: &[&str] = &[
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
    "<address>",
    "<blockquote>",
    "<form>",
    "</form>",
    "<button>",
    "</button>",
    "<select>",
    "</select>",
    "<option>",
    "</option>",
    "<optgroup>",
    "<b>",
    "</b>",
    "<i>",
    "</i>",
    "<a href=x>",
    "</a>",
    "<em>",
    "<font>",
    "</font>",
    "<nobr>",
    "<code>",
    "<s>",
    "<table>",
    "</table>",
    "<tr>",
    "</tr>",
    "<td>",
    "</td>",
    "<th>",
    "<tbody>",
    "<caption>",
    "</caption>",
    "<colgroup>",
    "<marquee>",
    "</marquee>",
    "<object>",
    "</object>",
    "<applet>",
    "<span>",
    "</span>",
    "<q>",
    "</q>",
    "<label>",
    "</label>",
    "<abbr>",
    "<cite>",
    "</cite>",
    "<x-el>",
    "</x-el>",
    "<x-other>",
    "</x-other>",
    "<svg>",
    "</svg>",
    "<g>",
    "</g>",
    "<math>",
    "<mi>",
    "</mi>",
    "<desc>",
    "<ruby>",
    "<rt>",
    "<rp>",
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
    "</zzz>",
    "</td>",
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
}

fn soup(rng: &mut Rng, tags: usize) -> String {
    // A soup leans on a few units, so shapes repeat as attacks do.
    let favourites: Vec<&str> = (0..1 + rng.below(4))
        .map(|_| UNITS[rng.below(UNITS.len())])
        .collect();
    let mut s = String::from("<title>t</title>");
    for i in 0..tags {
        let unit = if rng.below(3) == 0 {
            UNITS[rng.below(UNITS.len())]
        } else {
            favourites[rng.below(favourites.len())]
        };
        if unit == "<b>" || unit == "<i>" {
            s.push_str(&format!("{}{} class=c{}>", &unit[..2], "", i));
        } else {
            s.push_str(unit);
        }
    }
    s
}

/// The deepest element under body.
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
    max.saturating_sub(2) // html and body
}

fn check(seed: u64, soups: usize, tags: usize) -> usize {
    let mut rng = Rng(seed);
    let mut deepest = 0;
    for k in 0..soups {
        let html = soup(&mut rng, tags);
        let d = depth(&strip(&html));
        deepest = deepest.max(d);
        assert!(
            d <= MAX_NESTING + SLACK,
            "seed {seed}, soup {k}: depth {d} over {} + {SLACK}:\n{}",
            MAX_NESTING,
            &html[..html.len().min(4000)]
        );
    }
    deepest
}

#[test]
fn short_soups_stay_under_the_cap() {
    let d = check(0x6a09_e667_f3bc_c908, 30000, 600);
    eprintln!("short soups: deepest tree {d}");
}

#[test]
fn long_soups_stay_under_the_cap() {
    let d = check(0xbb67_ae85_84ca_a73b, 300, 20_000);
    eprintln!("long soups: deepest tree {d}");
}

#[test]
#[ignore]
fn stress_more_seeds() {
    for seed in [1u64, 2, 3, 0xdead_beef, 0x1234_5678_9abc] {
        let d1 = check(seed, 30_000, 300);
        let d2 = check(seed ^ 0x5555, 3000, 3000);
        let d3 = check(seed ^ 0xaaaa, 100, 20_000);
        eprintln!("seed {seed}: {d1} {d2} {d3}");
    }
}
