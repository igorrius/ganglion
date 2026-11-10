package node

import (
	"context"
	"errors"
)

// ErrRestart is the cause of a stop that asks the supervisor to start the
// node again (R-CFG-6): Run returns it, and main exits with a code that
// makes a supervisor restart it.
var ErrRestart = errors.New("node: restart requested")

// stopped is what Run returns when ctx ended: ErrRestart for a restart,
// nil for any other stop.
func stopped(ctx context.Context) error {
	if errors.Is(context.Cause(ctx), ErrRestart) {
		return ErrRestart
	}
	return nil
}
