package event_test

import (
	"testing"

	"github.com/igorrius/ganglion/event"
)

// Kinds are stored in the inbox: their values must never change.
func TestKindValues(t *testing.T) {
	if event.KindMessage != "message" {
		t.Errorf("KindMessage = %q", event.KindMessage)
	}
}
