// Package router chooses which provider and model handle a task: hard
// filters by tags, rules and weights, then a fallback chain (docs/spec.md,
// section 7.3).
package router

import "context"

// TaskReq is what a task needs from a model. Skills declare it in the
// model block of their SKILL.md frontmatter.
type TaskReq struct {
	Tier    string // "cheap" or "smart"; empty lets the router decide
	Tools   bool
	Context string // "short" or "long"
	Vision  bool
	Privacy string // "local-only" keeps the task off cloud models
	Kind    string // event kind, e.g. "cron" or "message"
}

// Candidate is one "provider + model" option.
type Candidate struct {
	Provider string
	Model    string
}

// Router returns candidates in the order they should be tried; the caller
// moves to the next one on an error, a 429 or a timeout.
type Router interface {
	Route(ctx context.Context, task TaskReq) ([]Candidate, error)
}
