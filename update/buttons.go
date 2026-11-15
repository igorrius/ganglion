package update

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/igorrius/ganglion/tool/pending"
)

// The quick actions of the Install and Rollback buttons (R-UPD-2). The agent
// registers them; the daily check and /update build the buttons.
const (
	// ActionInstall installs the version in its arguments.
	ActionInstall = "update.install"
	// ActionRollback rolls back to the version in its arguments.
	ActionRollback = "update.rollback"
)

// Buttons stores quick-action buttons (pending.Store); the daily notice
// carries none without it.
type Buttons interface {
	AddButtons(ctx context.Context, bs ...pending.Button) error
}

// NoButtons stores nothing: a node without it sends the notice with no
// buttons.
type NoButtons struct{}

// AddButtons stores nothing.
func (NoButtons) AddButtons(context.Context, ...pending.Button) error { return nil }

// installArgs is the argument of an Install button.
type installArgs struct {
	Version string `json:"version"`
}

// rollbackArgs is the argument of a Rollback button: the version the
// rollback goes to, "" for the binary of the image.
type rollbackArgs struct {
	To string `json:"to"`
}

// InstallArgs is the arguments of an Install button for version.
func InstallArgs(version string) json.RawMessage {
	b, _ := json.Marshal(installArgs{Version: version}) // a struct of one string cannot fail
	return b
}

// RollbackArgs is the arguments of a Rollback button that goes to to.
func RollbackArgs(to string) json.RawMessage {
	b, _ := json.Marshal(rollbackArgs{To: to}) // a struct of one string cannot fail
	return b
}

// ParseInstallArgs reads the version of an Install button; it must be a
// release version.
func ParseInstallArgs(raw json.RawMessage) (string, error) {
	var a installArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("update: install button: %w", err)
	}
	if _, err := ParseVersion(a.Version); err != nil {
		return "", err
	}
	return a.Version, nil
}

// ParseRollbackArgs reads the target of a Rollback button; "" or a release
// version.
func ParseRollbackArgs(raw json.RawMessage) (string, error) {
	var a rollbackArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("update: rollback button: %w", err)
	}
	if a.To != "" {
		if _, err := ParseVersion(a.To); err != nil {
			return "", err
		}
	}
	return a.To, nil
}
