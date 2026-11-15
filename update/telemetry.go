package update

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// outcome is how an install ended, a label of ganglion.update.installs.
// The set is closed: a refusal is the sentinel that caused it, never text.
type outcome string

const (
	outcomeInstalled    outcome = "installed"
	outcomeDevBuild     outcome = "dev_build"
	outcomeNotNewer     outcome = "not_newer"
	outcomeNotFound     outcome = "not_found"
	outcomeNoPlatform   outcome = "no_platform"
	outcomeIncomplete   outcome = "incomplete"
	outcomeBadSignature outcome = "bad_signature"
	outcomeBadRelease   outcome = "bad_release"
	outcomeEnv          outcome = "env"
	outcomeBusy         outcome = "busy"
	outcomeFetch        outcome = "fetch"
	outcomeOther        outcome = "other"
)

// checkOutcome is how a daily check ended, a label of
// ganglion.update.checks. The set is closed.
type checkOutcome string

const (
	checkNewer   checkOutcome = "newer"
	checkCurrent checkOutcome = "current"
	checkFailed  checkOutcome = "failed"
	checkSkipped checkOutcome = "skipped"
)

// refusals maps a sentinel to its outcome; the first that matches wins.
var refusals = []struct {
	err error
	out outcome
}{
	{ErrDevBuild, outcomeDevBuild}, {ErrNotNewer, outcomeNotNewer}, {ErrNotFound, outcomeNotFound},
	{ErrNoPlatform, outcomeNoPlatform}, {ErrIncomplete, outcomeIncomplete}, {ErrBadSignature, outcomeBadSignature},
	{ErrBadSums, outcomeBadRelease}, {ErrSumMismatch, outcomeBadRelease}, {ErrBadManifest, outcomeBadRelease},
	{ErrEnv, outcomeEnv}, {ErrBusy, outcomeBusy}, {ErrFetch, outcomeFetch},
}

// outcomeOf is the outcome of an install that returned err.
func outcomeOf(err error) outcome {
	if err == nil {
		return outcomeInstalled
	}
	for _, r := range refusals {
		if errors.Is(err, r.err) {
			return r.out
		}
	}
	return outcomeOther
}

// telemetry is the update package's metrics (R-OBS-8).
type telemetry struct {
	installs      metric.Int64Counter
	fallbacks     metric.Int64Counter
	confirmations metric.Int64Counter
	checks        metric.Int64Counter
}

func newTelemetry() telemetry {
	m := otel.Meter("ganglion/update")
	var t telemetry
	t.installs, _ = m.Int64Counter("ganglion.update.installs",
		metric.WithDescription("Core release installs, by outcome: installed, or the reason it was refused."))
	t.fallbacks, _ = m.Int64Counter("ganglion.update.fallbacks",
		metric.WithDescription("Releases the launcher gave up on, by reason: failed, unverified, noexec, exec."))
	t.confirmations, _ = m.Int64Counter("ganglion.update.confirmations",
		metric.WithDescription("Releases that ran their confirmation time and were confirmed."))
	t.checks, _ = m.Int64Counter("ganglion.update.checks",
		metric.WithDescription("Daily checks for a newer release, by outcome: newer, current, failed, skipped."))
	return t
}

func (t telemetry) install(ctx context.Context, o outcome) {
	t.installs.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", string(o))))
}

func (t telemetry) fallback(ctx context.Context, r Reason) {
	t.fallbacks.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", string(r))))
}

func (t telemetry) confirmed(ctx context.Context) { t.confirmations.Add(ctx, 1) }

func (t telemetry) check(ctx context.Context, o checkOutcome) {
	t.checks.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", string(o))))
}
