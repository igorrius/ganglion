package intake

import (
	"context"
	"crypto/hpke"
	"errors"
	"strconv"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/texts"
)

// TakeOutcome is what became of a pasted line: a log field and a metric label.
type TakeOutcome string

// Outcomes of Take.
const (
	LineTaken      TakeOutcome = "taken"       // its request got it and did not fail
	LineNotPrivate TakeOutcome = "not_private" // not in the sender's private chat
	LineNotAdmin   TakeOutcome = "not_admin"   // not from an admin, or the check failed
	LineMalformed  TakeOutcome = "malformed"   // does not parse
	LineVersion    TakeOutcome = "version"     // a version this node does not know
	LineUnknown    TakeOutcome = "unknown"     // no open request of that id and kind
	LineExpired    TakeOutcome = "expired"     // the request ran out
	LineFailed     TakeOutcome = "failed"      // taken, and not accepted: the request is closed
	LineNotAlone   TakeOutcome = "not_alone"   // a line with other text, in a caption or forwarded
	LineDropped    TakeOutcome = "dropped"     // no intake is configured
)

// replyKeys are the fixed texts the sender gets; the other outcomes get none.
var replyKeys = map[TakeOutcome]texts.Key{
	LineMalformed: texts.ReplyIntakeMalformed,
	LineVersion:   texts.ReplyIntakeVersion,
	LineUnknown:   texts.ReplyIntakeUnknown,
	LineExpired:   texts.ReplyIntakeExpired,
	LineFailed:    texts.ReplyIntakeFailed,
	LineNotAlone:  texts.ReplyIntakeAlone,
}

// Pasted is a message that Holds accepted, as the transport saw it.
type Pasted struct {
	From      string // the sender's principal
	Private   bool   // in the sender's private conversation
	Text      string // the message's text, or its caption
	Caption   bool   // Text is a file's caption
	Forwarded bool   // the message is forwarded: not the sender's own text
}

// infoPrefix is the format part of the cipher's info (R-INT-4).
const infoPrefix = "ganglion-intake-v1"

// errNotOpened is the one error of a ciphertext that did not open: what
// the hpke package says is not kept.
var errNotOpened = errors.New("intake: the ciphertext did not open")

// sealInfo is the HPKE info: the format, the request id and the name,
// separated by a zero byte.
func sealInfo(id, name string) []byte {
	return []byte(infoPrefix + "\x00" + id + "\x00" + name)
}

// Take handles one pasted line and returns the id of the request it named
// ("" when none could be read), for the transport's log. It never returns
// or logs the line, a code or a value. The order is: private chat, admin,
// the sender's own text message with the line alone, parse, find the
// request, remove it (a line works once, whatever its result), open a
// ciphertext. The opener's function then runs on its own goroutine, bounded
// by Config.HandleTimeout, so a slow code exchange never holds the
// transport's receive path; its outcome reaches ClosedFunc and, on failure,
// the sender. LineTaken means handed over.
func (r *Registry) Take(ctx context.Context, p Pasted) (string, TakeOutcome) {
	id, o := r.take(ctx, p)
	r.tel.line(ctx, o)
	r.sweep(ctx)
	return id, o
}

func (r *Registry) take(ctx context.Context, p Pasted) (string, TakeOutcome) {
	if !p.Private {
		return "", LineNotPrivate
	}
	if !r.admin(ctx, p.From) {
		return "", LineNotAdmin
	}
	line, o := r.read(p)
	if o != "" {
		r.reply(ctx, p.From, "", o)
		return "", o
	}
	q, o := r.claim(line)
	if q == nil {
		r.reply(ctx, p.From, line.ID, o)
		return line.ID, o
	}
	if o == LineExpired {
		r.finish(ctx, q, Closed{ID: line.ID, Kind: q.kind, Outcome: OutcomeExpired, At: r.now()})
		r.reply(ctx, p.From, line.ID, o)
		return line.ID, o
	}
	value, err := r.unseal(line, q)
	if err != nil {
		r.finish(ctx, q, Closed{ID: line.ID, Kind: q.kind, Outcome: OutcomeFailed, At: r.now()})
		r.reply(ctx, p.From, line.ID, LineFailed)
		return line.ID, LineFailed
	}
	r.work.Go(func() { r.deliver(context.WithoutCancel(ctx), p.From, line, q, value) })
	return line.ID, LineTaken
}

// read parses p's text into a line, or returns why it is refused: a line
// that is not the sender's own text message alone, a damaged one, or one
// of a version this node does not know.
func (r *Registry) read(p Pasted) (Line, TakeOutcome) {
	if p.Caption || p.Forwarded {
		return Line{}, LineNotAlone
	}
	line, err := Parse(p.Text)
	switch {
	case err == nil:
		return line, ""
	case errors.Is(err, ErrVersion):
		return Line{}, LineVersion
	case errors.Is(err, ErrNotALine):
		return Line{}, LineNotAlone
	default:
		return Line{}, LineMalformed
	}
}

// admin reports whether from is an admin. A failed check counts as no, and
// is logged by its kind of failure only.
func (r *Registry) admin(ctx context.Context, from string) bool {
	ok, err := r.isAdmin(ctx, from)
	if err != nil {
		r.log.Warn("intake: the admin check failed", "outcome", LineNotAdmin)
		return false
	}
	return ok
}

// claim finds the request l names and removes it from the registry, so the
// line works once. It returns nil with LineUnknown for none, or one of the
// other kind (which stays open), and the request with LineExpired when it
// ran out.
func (r *Registry) claim(l Line) (*request, TakeOutcome) {
	r.mu.Lock()
	defer r.mu.Unlock()
	q, ok := r.reqs[l.ID]
	if !ok || q.kind != l.Kind {
		return nil, LineUnknown
	}
	delete(r.reqs, l.ID)
	if !r.now().Before(q.expires) {
		q.destroy()
		return q, LineExpired
	}
	return q, ""
}

// unseal opens a secret line's ciphertext with the request's key, which is
// destroyed either way; an OAuth line has nothing to open. The error says
// only that it did not open.
func (r *Registry) unseal(l Line, q *request) ([]byte, error) {
	if q.kind == KindOAuth {
		return nil, nil
	}
	value, err := hpke.Open(q.key, hpke.HKDFSHA256(), hpke.AES128GCM(), sealInfo(l.ID, q.name), l.Sealed)
	q.destroy()
	if err != nil {
		return nil, errNotOpened
	}
	return value, nil
}

// deliver hands the code or the opened value to the request's function,
// clears the value, and reports the outcome. The function's owner logs its
// own errors: only the outcome is kept here.
func (r *Registry) deliver(ctx context.Context, from string, l Line, q *request, value []byte) {
	defer clear(value)
	hctx, cancel := context.WithTimeout(ctx, r.cfg.HandleTimeout)
	defer cancel()
	var err error
	if q.kind == KindOAuth {
		err = q.oauth(hctx, OAuthAnswer{ID: l.ID, Code: l.Code, Issuer: l.Issuer})
	} else {
		err = q.secret(hctx, l.ID, value)
	}
	out := OutcomeAnswered
	if err != nil {
		out = OutcomeFailed
	}
	r.finish(ctx, q, Closed{ID: l.ID, Kind: q.kind, Outcome: out, At: r.now()})
	if err != nil {
		r.reply(ctx, from, l.ID, LineFailed)
	}
}

// reply tells the sender, in their private conversation and their
// language, the fixed text of outcome o. A person who cannot be reached,
// or a message that is not stored, is logged by outcome and not returned.
func (r *Registry) reply(ctx context.Context, to, id string, o TakeOutcome) {
	key, ok := replyKeys[o]
	if !ok {
		return
	}
	tr, conv, ok := r.private(to)
	if !ok {
		return
	}
	lang, err := r.lang(ctx, to)
	if err != nil {
		lang = texts.English
	}
	ref := id
	if ref == "" {
		ref = "line"
	}
	msg := event.Outgoing{
		IdemKey:   "intake:" + ref + ":" + string(o) + ":" + strconv.FormatInt(r.now().UnixNano(), 10),
		Transport: tr, Conv: conv, Text: r.texts.Text(lang, key, nil),
	}
	if err := r.enqueue(ctx, msg); err != nil {
		r.log.Warn("intake: the reply was not stored", "outcome", o)
	}
}
