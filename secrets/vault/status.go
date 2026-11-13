package vault

import (
	"context"

	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/texts"
)

// Name implements status.Source.
func (v *Vault) Name() string { return component }

// Status implements status.Source: one failing line in the problems
// section while the store is locked, nothing otherwise (R-CFG-10).
func (v *Vault) Status(context.Context, status.Scope) ([]status.Check, error) {
	if !v.Locked() {
		return nil, nil
	}
	return []status.Check{{Section: status.SectionProblems, State: status.Fail,
		Line: texts.StatusSecretsLocked, Reason: texts.StatusReasonSecretsKey, Since: v.opened}}, nil
}
