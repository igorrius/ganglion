package node

import (
	"context"
	"errors"

	"github.com/igorrius/ganglion/transport/telegram"
)

// Alerts sent to every admin when the webhook mode does not hold (R-DLV-19).
const (
	unreachableAlert = "The Telegram webhook is not registered: the node's public address (http.public_url) did not reach this node. " +
		"The node polls until it is restarted. Check the domain and the port it targets, then restart ganglion."
	failingAlert = "Telegram could not deliver updates to the webhook, so the node removed it and polls until it is restarted. " +
		"Check the node's public address (http.public_url), then restart ganglion."
	takenAlert = "Receiving stopped: the bot's webhook is no longer this node's. Another process set its own or removed it. " +
		"Stop that process and restart ganglion."
)

// Webhook is the Telegram transport's receiving side in webhook mode
// (R-DLV-18). When the webhook cannot be registered or stops working, it
// alerts the admins and polls for the rest of the run, so a broken setting
// never cuts the owner off (R-DLV-19); when somebody else takes the bot it
// alerts and stops receiving, as Poller does on 409.
type Webhook struct {
	Poller
}

// Run implements Component.
func (w Webhook) Run(ctx context.Context) error {
	err := w.Transport.Serve(ctx, w.Sink)
	switch {
	case errors.Is(err, telegram.ErrWebhookUnreachable):
		w.alert(ctx, "alert:telegram-webhook-unreachable:", unreachableAlert)
		return w.Poller.Run(ctx)
	case errors.Is(err, telegram.ErrWebhookFailing):
		w.alert(ctx, "alert:telegram-webhook-failing:", failingAlert)
		return w.Poller.Run(ctx)
	case errors.Is(err, telegram.ErrWebhookTaken):
		w.alert(ctx, "alert:telegram-webhook-taken:", takenAlert)
		return nil
	case errors.Is(err, telegram.ErrConflict):
		w.Log.Error("node: Telegram receiving stopped; another process polls with the token", "err", err)
		return alertConflict(ctx, w.Admins, w.Enqueue, w.Started, w.Log)
	}
	return err
}

// alert enqueues text to every admin, once per process start. A failure is
// logged: the node goes on.
func (w Webhook) alert(ctx context.Context, key, text string) {
	alertAdmins(ctx, w.Admins, w.Enqueue, key, text, w.Started, w.Log)
}
