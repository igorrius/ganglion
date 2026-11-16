package runner

import (
	"context"
	"strings"
	"unicode/utf8"
)

// NotifyCode is the answer of the notify host function (R-RUN-16).
type NotifyCode int32

// The codes a notify call returns to the module.
const (
	NotifySent    NotifyCode = 0  // the text was handed on to the owner
	NotifyDenied  NotifyCode = -1 // the manifest grants no notify
	NotifyLimit   NotifyCode = -2 // over today's limit, or the node could not take the message now; nothing was sent
	NotifyNotHere NotifyCode = -3 // init and health runs send nothing
)

// MaxNotifyText is the longest text a notify call carries, in characters.
const MaxNotifyText = 1000

// NotifyPort sends a module's text to its owner. The run's identity travels
// in ctx (the host sets it); the port decides limits. Text arrives already
// cut.
type NotifyPort interface {
	Notify(ctx context.Context, text string) NotifyCode
}

// cutText cuts s to MaxNotifyText characters, ending with "…" when cut, and
// makes it valid UTF-8.
func cutText(s string) string {
	s = strings.ToValidUTF8(s, "�")
	if utf8.RuneCountInString(s) <= MaxNotifyText {
		return s
	}
	r := []rune(s)
	return string(r[:MaxNotifyText-1]) + "…"
}

// notify is the notify host function: the manifest's grant and the port are
// checked before the text is cut and handed on.
func (m *Module) notify(ctx context.Context, text string) int32 {
	m.called(ctx, CallNotify)
	if m.manifest.Capabilities.Notify == nil || m.deps.Notify == nil {
		return int32(NotifyDenied)
	}
	return int32(m.deps.Notify.Notify(ctx, cutText(text)))
}
