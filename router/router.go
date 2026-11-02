// Package router chooses which provider and model handle a task: hard
// filters by tags, rules and weights, then a fallback chain (docs/spec.md,
// section 7.3).
package router

import (
	"context"
	"errors"
	"time"
)

// Tier is a model's class in the catalogue.
type Tier string

// Tiers.
const (
	TierCheap Tier = "cheap"
	TierSmart Tier = "smart"
)

// TaskKind is what a model is asked to do; the router picks the tier by it
// when the task names none.
type TaskKind string

// Task kinds.
const (
	TaskMessage    TaskKind = "message"    // answer a person
	TaskCompaction TaskKind = "compaction" // fold old history into a summary
	TaskGuard      TaskKind = "guard"      // the clean-context check of what the agent keeps (R-POL-7)
)

// TaskReq is what a task needs from a model. Skills declare it in the
// model block of their SKILL.md frontmatter.
type TaskReq struct {
	Tier    Tier // empty lets the router decide
	Tools   bool
	Context string // "short" or "long"
	Vision  bool
	Privacy string // "local-only" keeps the task off cloud models
	Kind    TaskKind
}

// ModelCaps are the tags the router filters and ranks a model by
// (R-RTR-1). They are per model: one provider serves models with
// different capabilities and prices (ADR-0020).
type ModelCaps struct {
	Tools         bool
	Vision        bool
	Local         bool // data stays on the user's hardware
	ContextTokens int
	PriceInMTok   float64 // USD per million input tokens; 0 for local and subscription models
	PriceOutMTok  float64 // USD per million output tokens
	Latency       time.Duration
}

// Model is one entry of the model catalogue.
type Model struct {
	Provider string
	Name     string
	Tier     Tier
	Caps     ModelCaps
}

// Candidate is one "provider + model" option. Prices travel with it, so the
// caller can cost a turn.
type Candidate struct {
	Provider     string
	Model        string
	PriceInMTok  float64
	PriceOutMTok float64
}

// Cost is what a call that used inTokens and outTokens costs at the
// candidate's catalogue prices, in USD. Cached input counts as input.
func (c Candidate) Cost(inTokens, outTokens int) float64 {
	return (float64(inTokens)*c.PriceInMTok + float64(outTokens)*c.PriceOutMTok) / 1e6
}

// ErrNoCandidate is returned when no model fits the task.
var ErrNoCandidate = errors.New("router: no model fits the task")

// Router returns candidates in the order they should be tried; the caller
// moves to the next one on an error, a 429 or a timeout, and reports every
// call's outcome so rate-limited models cool down.
type Router interface {
	Route(ctx context.Context, task TaskReq) ([]Candidate, error)
	Report(c Candidate, err error)
}

// Local is the P1 placement: every tool runs on the node that handles
// the turn (R-RTR-3). It satisfies tool.Placement.
type Local struct{}

// Where returns "local" for every tool; it is tool.PlacementLocal.
func (Local) Where(string) string { return "local" }
