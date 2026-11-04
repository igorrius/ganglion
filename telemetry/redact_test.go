package telemetry

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/internal/redact"
)

func TestRedactHandlerHidesTokenInAttrsAndGroups(t *testing.T) {
	const token = "7001:AAAAbbbbCCCCddddEEEEffffGGGGhhhhIII"
	const key = "sk-or-live-0123456789"
	var b bytes.Buffer
	h := RedactHandler(slog.NewJSONHandler(&b, nil), redact.Values(token, key))
	log := slog.New(h).With("bot", token).WithGroup("provider")
	log.Error("call failed with "+key, "err", "401 for key "+key, "url", "https://api.telegram.org/bot"+token+"/getMe",
		slog.Group("req", "auth", "Bearer "+key), "n", 3, "d", time.Second)
	out := b.String()
	for _, secret := range []string{token, key} {
		if strings.Contains(out, secret) {
			t.Fatalf("secret %q in %s", secret, out)
		}
	}
	for _, want := range []string{`"n":3`, `"provider":`, `"req":`, "call failed with [redacted]"} {
		if !strings.Contains(out, want) {
			t.Fatalf("%s lacks %s", out, want)
		}
	}
	if !h.Enabled(context.Background(), slog.LevelInfo) {
		t.Fatal("Enabled not passed through")
	}
}
