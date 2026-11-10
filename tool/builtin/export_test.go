package builtin

import "context"

// Step runs the next waiting build, if any, and reports whether one ran:
// the tests drive the queue without its loop.
func (q *BuildQueue) Step(ctx context.Context) bool { return q.next(ctx) }

// Recover is what Run does first: it tells the builds a stopped node left.
func (q *BuildQueue) Recover(ctx context.Context) error { return q.recover(ctx) }
