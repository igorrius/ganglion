package agent

import (
	"context"
	"errors"

	"github.com/igorrius/ganglion/provider"
)

// Tools is the registry the agent offers to the model.
type Tools interface {
	List() []provider.Tool
	Call(ctx context.Context, call provider.ToolCall) (string, error)
}

// NoTools is an empty registry (M3a).
type NoTools struct{}

// List implements Tools.
func (NoTools) List() []provider.Tool { return nil }

// Call implements Tools.
func (NoTools) Call(context.Context, provider.ToolCall) (string, error) {
	return "", errors.New("no tools")
}
