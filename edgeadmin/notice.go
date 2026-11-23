package edgeadmin

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/igorrius/ganglion/alert"
	"github.com/igorrius/ganglion/edgejoin"
	"github.com/igorrius/ganglion/internal/text"
	"github.com/igorrius/ganglion/texts"
)

// Notice tells every admin, in their private chat, that an edge joined. It
// is edgejoin.Config.Joined; it is apart from Admin because the service
// holds it and Admin holds the service.
type Notice struct {
	Admins   alert.Admins
	Enqueue  EnqueueFunc
	Lang     LangFunc
	Texts    *texts.Catalog
	Location *time.Location
	Log      *slog.Logger
}

// Joined sends one message to each admin who has a private chat. It names
// the edge and the time; never a key or a code. A failure is logged: the
// join happened and does not wait for the notice.
func (n Notice) Joined(ctx context.Context, e edgejoin.Edge) {
	key := fmt.Sprintf("edge.joined:%s:%d", e.Name, e.Joined.UnixNano())
	out, err := n.Admins.Each(ctx, key, func(ctx context.Context, principal string) string {
		l, err := n.Lang(ctx, principal)
		if err != nil {
			l = texts.English
		}
		return texts.Speaker{Lang: l, Cat: n.Texts}.Text(texts.EdgeJoined, texts.Vars{varName: text.CodeSpan(e.Name),
			varTime: e.Joined.In(n.Location).Format("2 Jan 15:04")})
	})
	if err == nil && len(out) > 0 {
		err = n.Enqueue(ctx, out...)
	}
	if err != nil {
		n.Log.Warn("edgeadmin: the notice of a join was not sent", "edge", e.Name, "err", err)
	}
}
