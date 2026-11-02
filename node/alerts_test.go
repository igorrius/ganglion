package node

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/alert"
	"github.com/igorrius/ganglion/event"
)

// A 409 alert that cannot be stored is logged; the node keeps running and
// sending (deferred M2 minor).
func TestConflictAlertNotStoredDoesNotEndTheNode(t *testing.T) {
	var logs bytes.Buffer
	admins := alert.Admins{
		List:    func(context.Context) ([]string, error) { return []string{"telegram:user:1"}, nil },
		Private: func(string) (string, string, bool) { return "telegram", "1", true },
	}
	enqueue := func(context.Context, ...event.Outgoing) error { return errors.New("disk full") }
	err := alertConflict(context.Background(), admins, enqueue, time.UnixMilli(1), slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatalf("alertConflict = %v, want nil so the node keeps running", err)
	}
	if !strings.Contains(logs.String(), "disk full") {
		t.Fatalf("the failure was not logged: %s", logs.String())
	}
}
