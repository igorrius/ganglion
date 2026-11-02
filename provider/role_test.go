package provider_test

import (
	"encoding/json"
	"testing"

	"github.com/igorrius/ganglion/provider"
)

// The roles are stored in conversation state (ADR-0020): their wire
// values must never change.
func TestRoleWireValues(t *testing.T) {
	for role, want := range map[provider.Role]string{
		provider.RoleSystem: "system", provider.RoleUser: "user",
		provider.RoleAssistant: "assistant", provider.RoleTool: "tool",
	} {
		b, err := json.Marshal(provider.Message{Role: role})
		if err != nil {
			t.Fatal(err)
		}
		if got := string(b); got != `{"role":"`+want+`"}` {
			t.Errorf("%s marshals as %s", want, got)
		}
	}
}
