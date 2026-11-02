package node

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/igorrius/ganglion/alert"
	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/transport"
	"github.com/igorrius/ganglion/transport/telegram"
)

// conflictAlert is sent to every admin when Telegram polling stops on 409.
const conflictAlert = "Polling stopped: another process or a webhook uses this bot token. Stop it and restart ganglion."

// Poller is the Telegram transport's receiving side as a component. When
// another process takes the bot token (409), it alerts the admins and
// stops polling without ending the node: a supervisor would restart the
// process into the same fight, and the node keeps sending (R-DLV-8).
type Poller struct {
	Transport *telegram.Transport
	Sink      transport.Sink
	Admins    alert.Admins
	Enqueue   func(ctx context.Context, msgs ...event.Outgoing) error
	Started   time.Time // the process start: one alert per start
	Log       *slog.Logger
}

// Name implements Component.
func (p Poller) Name() string { return telegram.Name }

// Run implements Component.
func (p Poller) Run(ctx context.Context) error {
	err := p.Transport.Run(ctx, p.Sink)
	if !errors.Is(err, telegram.ErrConflict) {
		return err
	}
	p.Log.Error("node: Telegram polling stopped; another process or a webhook holds the token", "err", err)
	return alertConflict(ctx, p.Admins, p.Enqueue, p.Started, p.Log)
}

// alertConflict enqueues the conflict alert to every admin, once per
// process start. A failure is logged, never returned: the node keeps
// sending, and a supervisor must not restart it into the same fight.
func alertConflict(ctx context.Context, admins alert.Admins, enqueue func(context.Context, ...event.Outgoing) error,
	started time.Time, logger *slog.Logger) error {
	msgs, err := admins.Messages(ctx, "alert:telegram-conflict:"+strconv.FormatInt(started.UnixMilli(), 10), conflictAlert)
	if err == nil && len(msgs) > 0 {
		err = enqueue(ctx, msgs...)
	}
	if err != nil && ctx.Err() == nil {
		logger.Error("node: the 409 alert was not stored", "err", err)
	}
	return nil
}
