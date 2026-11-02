package transport

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestRetryAfterErrorUnwraps(t *testing.T) {
	err := fmt.Errorf("send: %w", &RetryAfterError{After: 7 * time.Second})
	var ra *RetryAfterError
	if !errors.As(err, &ra) {
		t.Fatalf("errors.As did not find RetryAfterError in %v", err)
	}
	if ra.After != 7*time.Second {
		t.Fatalf("After = %v, want 7s", ra.After)
	}
	if got, want := err.Error(), "send: transport: retry after 7s"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

func TestErrPermanentWraps(t *testing.T) {
	err := fmt.Errorf("telegram: sendMessage: 403 Forbidden: bot was blocked by the user: %w", ErrPermanent)
	if !errors.Is(err, ErrPermanent) {
		t.Fatalf("errors.Is did not find ErrPermanent in %v", err)
	}
}
