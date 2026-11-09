// Package router chooses which provider and model handle a task: the
// task's purpose names an ordered list of models, hard filters by tags drop
// the ones that do not fit, and the rest is the fallback chain (docs/spec.md,
// section 7.3, R-RTR-6).
package router

import (
	"context"
	"errors"
	"time"
)

// Purpose is what a model call is for (R-RTR-6). Each purpose has its own
// ordered list of models in ganglion.yaml.
type Purpose string

// Purposes.
const (
	PurposeChat      Purpose = "chat"      // a turn: a person's message or a scheduled agent task
	PurposeCheck     Purpose = "check"     // a short yes/no decision: the clean-context check
	PurposeCompact   Purpose = "compact"   // folding old history into a summary
	PurposeTranslate Purpose = "translate" // fixed texts into a language with no file (R-LANG-5)
	PurposeCode      Purpose = "code"      // writing modules and skills (P3)
)

// Purposes lists every purpose, in the order the file shows them.
var Purposes = []Purpose{PurposeChat, PurposeCheck, PurposeCompact, PurposeTranslate, PurposeCode}

// Effort is a model's reasoning effort; "" leaves the provider's default.
type Effort string

// Efforts, from the least to the most.
const (
	EffortLow    Effort = "low"
	EffortMedium Effort = "medium"
	EffortHigh   Effort = "high"
	EffortXHigh  Effort = "xhigh"
	EffortMax    Effort = "max"
)

// TaskReq is what a task needs from a model. Skills declare it in the
// model block of their SKILL.md frontmatter.
type TaskReq struct {
	Purpose Purpose // which list of models the task takes
	Tools   bool
	Context string // "" or "long"
	Vision  bool
	Privacy string // "local-only" keeps the task off cloud models
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
	ID       string // the catalogue id the purposes name
	Provider string
	Name     string // the provider's model name
	Caps     ModelCaps
}

// Choice is one entry of a purpose's list.
type Choice struct {
	Model  string // a Model.ID
	Effort Effort
}

// Candidate is one "provider + model" option. Effort and prices travel with
// it, so the caller can ask for the effort and cost a turn.
type Candidate struct {
	Provider     string
	Model        string
	Effort       Effort
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
