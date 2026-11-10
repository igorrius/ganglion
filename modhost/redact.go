package modhost

import (
	"log/slog"

	"github.com/igorrius/ganglion/internal/redact"
	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/runner"
	"github.com/igorrius/ganglion/telemetry"
)

// moduleLog is the logger for everything a module's run writes to the
// node's log: the runner's lines (the module's own log, a failed init or
// health) and the host's (a failed run, its text). The node's redactor knows
// only the node's own secrets, and a module may echo one it was granted into
// its log or its error, so the values of the secrets its manifest lists, as
// the node holds them, are redacted from every line too (no secret value in
// a log). Like every exact-value redaction, a value shorter than six
// characters is not hidden (redact.Values).
func (h *Host) moduleLog(m runner.Manifest) *slog.Logger {
	var values []string
	for _, name := range m.Capabilities.SecretGet {
		if modstore.NodeSecret(h.d.NodeSecrets, name) {
			continue // never given to a module, and hidden by the node's own redactor
		}
		if v, err := h.d.Secrets.Secret(name); err == nil && v != "" {
			values = append(values, v)
		}
	}
	return slog.New(telemetry.RedactHandler(h.d.Log.Handler(), redact.Values(values...)))
}
