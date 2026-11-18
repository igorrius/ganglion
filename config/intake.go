package config

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/igorrius/ganglion/access"
	"github.com/igorrius/ganglion/intake"
	"github.com/igorrius/ganglion/language"
	"github.com/igorrius/ganglion/queue/sqlqueue"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/transport/telegram"
	"github.com/igorrius/ganglion/web"
	"github.com/samber/do/v2"
)

// intakePageTimeout bounds connecting to the host of the intake page.
const intakePageTimeout = 10 * time.Second

// intakePage is the check of the hosted intake page (R-INT-3): the
// project's site until the `mcp.page` setting comes (#260). It dials
// public addresses only.
func intakePage() *intake.PageChecker {
	hc := &http.Client{Transport: &http.Transport{
		Proxy: nil, DialContext: web.PublicDial(intakePageTimeout), TLSHandshakeTimeout: intakePageTimeout,
		MaxResponseHeaderBytes: 64 << 10,
	}}
	return intake.NewPageChecker(hc, intake.DefaultPageCheckConfig())
}

// Intake provides the registry of requests for a code or a secret handed
// over from the chat (R-INT-1). It answers admins in their Telegram private
// chats, as the other node components do.
func Intake() do.Provider[*intake.Registry] {
	return func(i do.Injector) (*intake.Registry, error) {
		return intake.New(do.MustInvoke[*access.List](i).IsAdmin, do.MustInvoke[*sqlqueue.Queue](i).Enqueue,
			do.MustInvoke[*language.Store](i).Resolve, telegram.PrivateConv, do.MustInvoke[*texts.Catalog](i),
			intakePage().Check, intake.WithLogger(do.MustInvoke[*slog.Logger](i))), nil
	}
}
