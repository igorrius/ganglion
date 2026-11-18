package config

import (
	"log/slog"

	"github.com/igorrius/ganglion/access"
	"github.com/igorrius/ganglion/intake"
	"github.com/igorrius/ganglion/language"
	"github.com/igorrius/ganglion/queue/sqlqueue"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/transport/telegram"
	"github.com/samber/do/v2"
)

// Intake provides the registry of requests for a code or a secret handed
// over from the chat (R-INT-1). It answers admins in their Telegram private
// chats, as the other node components do.
func Intake() do.Provider[*intake.Registry] {
	return func(i do.Injector) (*intake.Registry, error) {
		return intake.New(do.MustInvoke[*access.List](i).IsAdmin, do.MustInvoke[*sqlqueue.Queue](i).Enqueue,
			do.MustInvoke[*language.Store](i).Resolve, telegram.PrivateConv, do.MustInvoke[*texts.Catalog](i),
			intake.WithLogger(do.MustInvoke[*slog.Logger](i))), nil
	}
}
