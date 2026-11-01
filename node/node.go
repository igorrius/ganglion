package node

import (
	"context"
	"errors"
	"log/slog"
	"strings"
)

// Run starts the given roles and blocks until ctx is cancelled.
//
// In P0 the roles have no behaviour yet; Run only reports what it would
// host. Role implementations arrive in P1 (docs/spec.md, section 10).
func Run(ctx context.Context, roles []Role, logger *slog.Logger) error {
	if len(roles) == 0 {
		return errors.New("node: no roles to run")
	}
	names := make([]string, len(roles))
	for i, r := range roles {
		names[i] = string(r)
	}
	logger.Info("node started", "roles", strings.Join(names, ","))
	<-ctx.Done()
	logger.Info("node stopped")
	return nil
}
