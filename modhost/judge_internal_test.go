package modhost

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/igorrius/ganglion/pdk"
	"github.com/igorrius/ganglion/runner"
)

// A call its caller stopped is not a module past its time limit, nor a trap.
func TestJudgeTellsWhoStoppedTheCall(t *testing.T) {
	for _, c := range []struct {
		err    error
		result Result
		detail string
	}{
		{fmt.Errorf("runner: probe: %w after 5s", runner.ErrTimeout), ResultTimeout, "the module ran past its time limit"},
		{fmt.Errorf("runner: probe: stopped by its caller: %w", context.DeadlineExceeded), ResultException, "the call was stopped before the module finished"},
		{fmt.Errorf("runner: probe: stopped by its caller: %w", context.Canceled), ResultException, "the call was stopped before the module finished"},
		{errors.New("runner: probe handle: wasm error: unreachable"), ResultException, "the module stopped with an error (a trap, or an answer the node cannot read)"},
	} {
		o := judge(pdk.Output{}, c.err, "probe cron")
		if o.result != c.result || o.detail != c.detail {
			t.Errorf("%v: %s %q, want %s %q", c.err, o.result, o.detail, c.result, c.detail)
		}
	}
}
