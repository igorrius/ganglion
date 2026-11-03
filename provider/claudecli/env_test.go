package claudecli

import (
	"os"
	"strings"
	"testing"
)

// An environment without any passed variable must still give claude an
// empty, non-nil environment: a nil exec.Cmd.Env inherits everything.
func TestEnvWithNothingPassedIsEmptyNotInherited(t *testing.T) {
	saved := os.Environ()
	t.Cleanup(func() {
		os.Clearenv()
		for _, kv := range saved {
			k, v, _ := strings.Cut(kv, "=")
			_ = os.Setenv(k, v)
		}
	})
	os.Clearenv()
	if err := os.Setenv("TELEGRAM_BOT_TOKEN", "7001:AAH-secret"); err != nil {
		t.Fatal(err)
	}
	env := (&Client{}).env()
	if env == nil {
		t.Fatal("env() is nil: claude would inherit the whole environment")
	}
	if len(env) != 0 {
		t.Fatalf("env() = %q, want empty", env)
	}
}
