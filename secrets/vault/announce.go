package vault

import "log/slog"

// Announce logs what an operator must know of the store at start: that the
// node made its key file (it must be kept with the database), and that the
// store is locked. path is the key file, "" when the key is not a file the
// node keeps.
func Announce(log *slog.Logger, path string, created, locked bool) {
	if created {
		log.Info("vault: made the secret store's master key; keep the file with the database", "path", path)
	}
	if locked {
		log.Warn("vault: the secret store is locked: its key does not fit the stored secrets; they read as not set until /secrets reset drops them")
	}
}
