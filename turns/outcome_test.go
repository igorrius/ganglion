package turns_test

import (
	"testing"

	"github.com/igorrius/ganglion/turns"
)

// Outcomes are stored in the turns table and used as a metric label:
// their values must never change.
func TestOutcomeValues(t *testing.T) {
	for got, want := range map[turns.Outcome]string{
		turns.OutcomeOK: "ok", turns.OutcomeStepLimit: "step_limit", turns.OutcomeDailyLimit: "daily_limit",
		turns.OutcomeNoModel: "no_model", turns.OutcomeCommand: "command", turns.OutcomeError: "error",
	} {
		if string(got) != want {
			t.Errorf("outcome %q, want %q", got, want)
		}
	}
}
