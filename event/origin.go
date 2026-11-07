package event

import "context"

// OriginKind is what made a message the node sends (R-DLV-14).
type OriginKind string

// Origin kinds.
const (
	OriginTurn     OriginKind = "turn"     // a turn of a conversation with a person
	OriginTask     OriginKind = "task"     // a scheduled task's run
	OriginReminder OriginKind = "reminder" // a reminder, sent as it was set
	OriginSystem   OriginKind = "system"   // a system job, such as the memory audit
)

// Origin is where a message the node sends came from: the session that made it and
// whether outside content had entered that session when it was stored (R-DLV-14,
// ADR-0055). A message stored with none counts as tainted.
type Origin struct {
	Kind    OriginKind `json:"kind"`
	Session string     `json:"session,omitempty"`
	Tainted bool       `json:"tainted,omitempty"`
}

type originKey struct{}

// WithOrigin returns ctx carrying of. A message stored under ctx with no origin of
// its own gets of() at that moment, so what a turn stores through any path, a plan
// or an early toast, carries the turn's origin.
func WithOrigin(ctx context.Context, of func() Origin) context.Context {
	return context.WithValue(ctx, originKey{}, of)
}

// OriginOf is the origin ctx carries now, false when it carries none.
func OriginOf(ctx context.Context) (Origin, bool) {
	of, ok := ctx.Value(originKey{}).(func() Origin)
	if !ok {
		return Origin{}, false
	}
	return of(), true
}
