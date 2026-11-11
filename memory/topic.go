package memory

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

// Limits of a topic (R-MEM-6, ADR-0071).
const (
	MaxFactRunes    = 300
	MaxSummaryRunes = 150
	MaxTopicFacts   = 50
)

// dateLayout is how a fact's date is written in its comment.
const dateLayout = "2006-01-02"

// Fact is one statement of a topic. Date, Turn and Tainted say where it
// came from (R-MEM-5); a line the owner wrote by hand has none.
type Fact struct {
	ID      string // "f3"; stable inside its topic, never reused; "" until the next write
	Text    string // one line
	Date    time.Time
	Turn    string
	Tainted bool
}

// Topic is one file, memory/<slug>.md (R-MEM-6): a title and a one-line
// summary, then the facts. Next is the number of the next fact id, so an
// id is not reused after its fact is removed.
type Topic struct {
	Slug, Title, Summary string
	Next                 int
	Facts                []Fact
}

// Changed is the newest fact's date; zero when none is dated.
func (t Topic) Changed() time.Time {
	var newest time.Time
	for _, f := range t.Facts {
		if f.Date.After(newest) {
			newest = f.Date
		}
	}
	return newest
}

// topicFront is a topic file's frontmatter.
type topicFront struct {
	Title   string `yaml:"title"`
	Summary string `yaml:"summary,omitempty"`
	Next    int    `yaml:"next,omitempty"`
}

// encodeTopic is the file of t: the frontmatter (title, summary, next),
// then one line per fact: "- [f3] text <!-- 2026-11-11 turn=<id> tainted -->".
// A fact with no id has no "[…]", one with no date no comment.
func encodeTopic(t Topic) ([]byte, error) {
	y, err := yaml.Marshal(topicFront{Title: oneLine(t.Title), Summary: oneLine(t.Summary), Next: t.Next})
	if err != nil {
		return nil, fmt.Errorf("memory: encode %s: %w", t.Slug, err)
	}
	var b bytes.Buffer
	b.WriteString(fence + string(y) + fence)
	if len(t.Facts) > 0 {
		b.WriteString("\n")
	}
	for _, f := range t.Facts {
		b.WriteString(factLine(f) + "\n")
	}
	return b.Bytes(), nil
}

// factLine is one fact as a list item.
func factLine(f Fact) string {
	line := "- "
	if f.ID != "" {
		line += "[" + f.ID + "] "
	}
	line += oneLine(f.Text)
	if f.Date.IsZero() {
		return line
	}
	line += " <!-- " + f.Date.UTC().Format(dateLayout)
	if f.Turn != "" {
		line += " turn=" + f.Turn
	}
	if f.Tainted {
		line += " tainted"
	}
	return line + " -->"
}

// decodeTopic reads a topic file. A file with no frontmatter is a topic
// titled by its slug. Every non-empty body line is a fact (ruling 9 of the
// M5 plan): nothing the owner typed is lost. Next is at least one above
// the highest id.
func decodeTopic(slug string, data []byte) (Topic, error) {
	t := Topic{Slug: slug, Title: slug}
	body := data
	if rest, ok := bytes.CutPrefix(data, []byte(fence)); ok {
		head, after, ok := bytes.Cut(rest, []byte("\n"+fence))
		if !ok {
			if head, ok = bytes.CutSuffix(rest, []byte(strings.TrimSuffix(fence, "\n"))); !ok {
				return Topic{}, fmt.Errorf("memory: %s: %w: it does not end", slug, errFrontmatter)
			}
		}
		var f topicFront
		if err := yaml.Unmarshal(head, &f); err != nil {
			// The parser's message can quote the file's values: it stays out.
			return Topic{}, fmt.Errorf("memory: %s: %w", slug, errFrontmatter)
		}
		if title := oneLine(f.Title); title != "" {
			t.Title = title
		}
		t.Summary, t.Next, body = oneLine(f.Summary), f.Next, after
	}
	for line := range strings.Lines(string(body)) {
		if f, ok := parseFact(line); ok {
			t.Facts = append(t.Facts, f)
		}
	}
	t.Next = max(t.Next, highestID(t.Facts)+1)
	return t, nil
}

var (
	// idPrefix is a fact's id at the start of its line.
	idPrefix = regexp.MustCompile(`^\[(f[1-9][0-9]*)\] +`)
	// sourceComment is the comment that ends a fact's line: its date, then
	// optionally its turn and "tainted".
	sourceComment = regexp.MustCompile(`\s*<!--\s*([0-9]{4}-[0-9]{2}-[0-9]{2})((?:\s+(?:turn=[A-Za-z0-9]+|tainted))*)\s*-->\s*$`)
)

// parseFact reads one line of a topic's body; ok is false for a blank
// line. A comment that does not parse stays part of the text.
func parseFact(line string) (Fact, bool) {
	s := strings.TrimSpace(line)
	s = strings.TrimPrefix(s, "- ")
	var f Fact
	if m := idPrefix.FindStringSubmatch(s); m != nil {
		f.ID, s = m[1], s[len(m[0]):]
	}
	if m := sourceComment.FindStringSubmatchIndex(s); m != nil {
		if d, err := time.Parse(dateLayout, s[m[2]:m[3]]); err == nil {
			f.Date = d
			for tok := range strings.FieldsSeq(s[m[4]:m[5]]) {
				if turn, ok := strings.CutPrefix(tok, "turn="); ok {
					f.Turn = turn
				} else {
					f.Tainted = true
				}
			}
			s = s[:m[0]]
		}
	}
	f.Text = oneLine(s)
	return f, f.Text != ""
}

// highestID is the number of the highest fact id; 0 when none has one.
func highestID(fs []Fact) int {
	n := 0
	for _, f := range fs {
		if v, err := strconv.Atoi(strings.TrimPrefix(f.ID, "f")); err == nil {
			n = max(n, v)
		}
	}
	return n
}

// checkFactText returns s on one line, refusing an empty text, one over
// MaxFactRunes, and one holding a comment marker, which would end or fake
// the fact's source comment.
func checkFactText(s string) (string, error) {
	s = oneLine(s)
	switch {
	case s == "":
		return "", fmt.Errorf("%w: a fact needs a text", ErrInvalid)
	case utf8.RuneCountInString(s) > MaxFactRunes:
		return "", fmt.Errorf("%w: a fact is longer than %d characters", ErrInvalid, MaxFactRunes)
	case strings.Contains(s, "<!--") || strings.Contains(s, "-->"):
		return "", fmt.Errorf("%w: a fact cannot hold <!-- or -->", ErrInvalid)
	}
	return s, nil
}
