package config

import (
	"log/slog"

	"github.com/igorrius/ganglion/access"
	"github.com/igorrius/ganglion/alert"
	"github.com/igorrius/ganglion/bus/natsbus"
	"github.com/igorrius/ganglion/edgeadmin"
	"github.com/igorrius/ganglion/edgejoin"
	"github.com/igorrius/ganglion/language"
	"github.com/igorrius/ganglion/publichttp"
	"github.com/igorrius/ganglion/queue/sqlqueue"
	"github.com/igorrius/ganglion/secrets/vault"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool/pending"
	"github.com/igorrius/ganglion/transport/telegram"
	"github.com/samber/do/v2"
)

// EdgeJoin provides the join of edges (R-EDGE-2, R-EDGE-5, ADR-0116) over
// the node's trust chain and its secret store. It is nil when the bus has
// no chain (an edge role, an external server without one, a locked store):
// such a node takes no edges, and EdgeAdmin and the listener check for it.
func EdgeJoin() do.Provider[*edgejoin.Service] {
	return func(i do.Injector) (*edgejoin.Service, error) {
		nb := do.MustInvoke[*natsbus.Bus](i)
		if nb.Chain() == nil {
			return nil, nil
		}
		return edgejoin.New(edgejoin.Config{
			Chain: nb.Chain(), Bus: nb, Store: do.MustInvoke[*vault.Vault](i).Internal(),
			// No module is placed on an edge yet (#323 fills this from the node's file).
			Needs:  func(string) (natsbus.EdgeNeeds, error) { return natsbus.EdgeNeeds{}, nil },
			Joined: do.MustInvoke[edgeadmin.Notice](i).Joined,
			// BusURL stays empty: the edge derives its bus address from the one it joined at.
			BusURL: "",
		}), nil
	}
}

// EdgeNotice provides the notice every admin gets when an edge joins.
func EdgeNotice() do.Provider[edgeadmin.Notice] {
	return func(i do.Injector) (edgeadmin.Notice, error) {
		return edgeadmin.Notice{
			Admins:   alert.Admins{List: do.MustInvoke[*access.List](i).Admins, Private: telegram.PrivateConv},
			Enqueue:  do.MustInvoke[*sqlqueue.Queue](i).Enqueue,
			Lang:     do.MustInvoke[*language.Store](i).Resolve,
			Texts:    do.MustInvoke[*texts.Catalog](i),
			Location: do.MustInvoke[Bootstrap](i).Agent.TZ,
			Log:      do.MustInvoke[*slog.Logger](i),
		}, nil
	}
}

// EdgeAdmin provides the owner's side of the edges: the tool edge.join, the
// cards' buttons and /edges. A node that takes no edges gets the service
// that refuses them.
func EdgeAdmin() do.Provider[*edgeadmin.Admin] {
	return func(i do.Injector) (*edgeadmin.Admin, error) {
		b := do.MustInvoke[Bootstrap](i)
		var svc edgeadmin.Service = edgeadmin.Unavailable{}
		if s := do.MustInvoke[*edgejoin.Service](i); s != nil {
			svc = s
		}
		return edgeadmin.New(edgeadmin.Deps{
			Service:  svc,
			Buttons:  do.MustInvoke[*pending.Store](i),
			Enqueue:  do.MustInvoke[*sqlqueue.Queue](i).Enqueue,
			Lang:     do.MustInvoke[*language.Store](i).Resolve,
			Texts:    do.MustInvoke[*texts.Catalog](i),
			IsAdmin:  do.MustInvoke[*access.List](i).IsAdmin,
			Address:  func() string { return b.PublicURL },
			Private:  telegram.PrivateConv,
			Location: b.Agent.TZ,
		}, edgeadmin.WithLogger(do.MustInvoke[*slog.Logger](i))), nil
	}
}

// wsEntry is the bus's WebSocket entry (natsbus.Bus.WebSocketAddr).
type wsEntry interface{ WebSocketAddr() string }

// mountEdgeJoin routes POST /edge/join and /edge/renew on l to the join,
// on the same condition as /bus (ADR-0114, ADR-0116): the bus is the
// embedded server with a trust chain and the WebSocket entry, so only a
// core that an edge can reach has the routes; an edge role, a node without
// a chain or without a listener has none, and they are a 404 like any
// unknown path. The routes are short requests, so they take the listener's
// limits (Handle, not HandleStream).
func mountEdgeJoin(l *publichttp.Listener, svc *edgejoin.Service, entry wsEntry, log *slog.Logger) {
	if svc == nil || entry.WebSocketAddr() == "" {
		return
	}
	l.Handle(edgejoin.JoinPath, svc.JoinHandler(log))
	l.Handle(edgejoin.RenewPath, svc.RenewHandler(log))
}
