package memory

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Episode is an archived session that matched a recall: the episodic
// level of memory (spec 7.7).
type Episode struct {
	Conv, Session string
	Ended         time.Time
	Tainted       bool   // the session held outside content
	Text          string // the matching part
}

// Episodes searches the archived sessions (agent.Archive).
type Episodes interface {
	// Search returns at most limit sessions of the conversations convs
	// whose text holds q, case insensitive, newest first.
	Search(ctx context.Context, q string, convs []string, limit int) ([]Episode, error)
}

// NoEpisodes is an archive with nothing in it.
type NoEpisodes struct{}

// Search implements Episodes.
func (NoEpisodes) Search(context.Context, string, []string, int) ([]Episode, error) { return nil, nil }

// HitKind says which level of memory a hit comes from (spec 7.7).
type HitKind string

// Hit kinds.
const (
	// HitFact is a stored fact: the agent wrote it and the checks let it
	// in.
	HitFact HitKind = "fact"
	// HitEpisode is a part of an earlier session: what was said then,
	// which may never have become a fact (ruling R49).
	HitEpisode HitKind = "episode"
)

// Hit is one result of a recall.
type Hit struct {
	Kind    HitKind
	Slug    string // the fact's topic; "" for a session
	Fact    string // the fact's id; "" for a session
	Source  string // "memory/<slug>" or "session <conv> <session>"
	Title   string // the topic's title; "" for a session
	Text    string
	When    time.Time
	Tainted bool // from a tainted session or a fact written in one: outside content
}

// Recall finds q, case insensitive, in the stored facts (a topic's title
// and a fact's text) and in the archived sessions of the conversations convs (the
// caller's own: another person's chat is not searched), newest first, at
// most limit hits.
func (m *Memory) Recall(ctx context.Context, q string, convs []string, limit int) ([]Hit, error) {
	q = strings.TrimSpace(q)
	if q == "" || limit <= 0 {
		return nil, fmt.Errorf("%w: recall needs a query and a limit", ErrInvalid)
	}
	ts, err := m.Topics()
	if err != nil {
		return nil, err
	}
	needle := strings.ToLower(q)
	var hits []Hit
	for _, t := range ts {
		for _, f := range t.Facts {
			if strings.Contains(strings.ToLower(t.Title+"\n"+f.Text), needle) {
				hits = append(hits, Hit{Kind: HitFact, Slug: t.Slug, Fact: f.ID, Source: dirFacts + "/" + t.Slug, Title: t.Title,
					Text: f.Text, When: f.Date, Tainted: f.Tainted})
			}
		}
	}
	eps, err := m.episodes.Search(ctx, q, convs, limit)
	if err != nil {
		return nil, fmt.Errorf("memory: recall: %w", err)
	}
	for _, ep := range eps {
		hits = append(hits, Hit{Kind: HitEpisode, Source: "session " + ep.Conv + " " + ep.Session, Text: ep.Text, When: ep.Ended, Tainted: ep.Tainted})
	}
	slices.SortStableFunc(hits, func(a, b Hit) int { return cmp.Compare(b.When.UnixNano(), a.When.UnixNano()) })
	return hits[:min(len(hits), limit)], nil
}
