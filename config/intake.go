package config

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/igorrius/ganglion/access"
	"github.com/igorrius/ganglion/intake"
	"github.com/igorrius/ganglion/language"
	"github.com/igorrius/ganglion/queue/sqlqueue"
	"github.com/igorrius/ganglion/secrets"
	"github.com/igorrius/ganglion/secrets/vault"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/transport/telegram"
	"github.com/igorrius/ganglion/web"
	"github.com/samber/do/v2"
)

// intakePageTimeout bounds connecting to the host of the intake page.
const intakePageTimeout = 10 * time.Second

// intakePage is the check of the hosted intake page at page, mcp.page of
// the node's file (R-INT-3). It dials public addresses only.
func intakePage(page string) *intake.PageChecker {
	hc := &http.Client{Transport: &http.Transport{
		Proxy: nil, DialContext: web.PublicDial(intakePageTimeout), TLSHandshakeTimeout: intakePageTimeout,
		MaxResponseHeaderBytes: 64 << 10,
	}}
	cfg := intake.DefaultPageCheckConfig()
	cfg.URL = page
	return intake.NewPageChecker(hc, cfg)
}

// Intake provides the registry of requests for a code or a secret handed
// over from the chat (R-INT-1). It answers admins in their Telegram private
// chats, as the other node components do.
func Intake() do.Provider[*intake.Registry] {
	return func(i do.Injector) (*intake.Registry, error) {
		return intake.New(do.MustInvoke[*access.List](i).IsAdmin, do.MustInvoke[*sqlqueue.Queue](i).Enqueue,
			do.MustInvoke[*language.Store](i).Resolve, telegram.PrivateConv, do.MustInvoke[*texts.Catalog](i),
			intakePage(intakePageOf(do.MustInvoke[Bootstrap](i))).Check, intake.WithLogger(do.MustInvoke[*slog.Logger](i))), nil
	}
}

// SecretAsker provides the secret requests of secret.request and of a
// server's header key (R-INT-2): the card in the admin's private chat, the
// value into the store, the fold and the agent's note (R-INT-6).
func SecretAsker() do.Provider[*intake.Asker] {
	return func(i do.Injector) (*intake.Asker, error) {
		loc := do.MustInvoke[Bootstrap](i).Agent.TZ
		if loc == nil {
			loc = time.UTC
		}
		q := do.MustInvoke[*sqlqueue.Queue](i)
		return intake.NewAsker(do.MustInvoke[*intake.Registry](i), storeSecret(do.MustInvoke[*vault.Vault](i)), q.Enqueue, q.Put,
			do.MustInvoke[*language.Store](i).Resolve, telegram.PrivateConv, do.MustInvoke[*texts.Catalog](i),
			intake.WithAskerClock(time.Now, loc), intake.WithAskerLogger(do.MustInvoke[*slog.Logger](i))), nil
	}
}

// storeSecret writes a value handed over from the chat to the store, and
// marks a locked store as intake knows it.
func storeSecret(v *vault.Vault) intake.StoreFunc {
	return func(ctx context.Context, scope secrets.Scope, name, value, by string) error {
		err := v.Set(ctx, scope, name, value, by)
		if errors.Is(err, vault.ErrLocked) {
			return fmt.Errorf("%w: %w", intake.ErrStoreLocked, err)
		}
		return err
	}
}
