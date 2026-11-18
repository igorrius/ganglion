package intake

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/secrets"
	"github.com/igorrius/ganglion/texts"
)

// StoreFunc writes a value to the secret store (vault.Vault.Set); by is
// the principal who set it.
type StoreFunc func(ctx context.Context, scope secrets.Scope, name, value, by string) error

// PutFunc puts an event into the inbox (sqlqueue.Queue.Put).
type PutFunc func(ctx context.Context, e event.Event) error

// SecretDoneFunc is told once when a secret request closes: the value is
// in the store (event.SecretSet), or it is not (failed, expired). It never
// gets the value. It runs off the transport's receive path, with no lock
// held.
type SecretDoneFunc func(ctx context.Context, c event.SecretClosed)

// NoSecretDone is the SecretDoneFunc that does nothing.
func NoSecretDone(context.Context, event.SecretClosed) {}

// Asking is who asks for a secret: what the card names.
type Asking string

// Who asks.
const (
	AskingNode   Asking = "node"   // the node itself: a store:NAME of the node's file
	AskingModule Asking = "module" // a module of the owner's, by its short name
	AskingServer Asking = "server" // a remote MCP server, by its name, for a header's key
)

// SecretAsk is one request for a secret, as its opener describes it.
type SecretAsk struct {
	Scope  secrets.Scope // where the value is stored
	Name   string        // the secret's name (secrets.ValidName)
	Asking Asking
	Who    string   // the module's short name or the server's name; "" for the node
	Hosts  []string // the hosts the value may reach; none for the node's own use
	Why    string   // the agent's reason, shown quoted as data; "" for none
	Admin  string   // the principal whose private chat gets the card
	// Transport and Conv are the conversation whose session gets the node
	// note when the request closes (R-INT-6).
	Transport, Conv string
	// Key makes Ask idempotent: a second Ask with the same key (a rerun of
	// the same tool call, tool.Call.Key) while its request lives returns that
	// request and sends no second card. "" for none.
	Key string
	// Tainted says the session that asked holds outside content; the card
	// says so.
	Tainted bool
}

// Asked is an opened secret request.
type Asked struct {
	ID      string
	Expires time.Time
}

// Errors of Ask, beside the registry's (ErrTooMany, ErrPageChanged,
// ErrPageUnchecked).
var (
	// ErrAsk: the request is not complete: no scope, a bad name, no admin,
	// or an admin the node cannot reach in a private chat.
	ErrAsk = errors.New("intake: a secret request that cannot be sent")
)

// varName is the secret's name in a card's texts.
const varName = "name"

// maxWhyRunes is the most of the agent's reason a card quotes.
const maxWhyRunes = 300

// Asker opens secret requests for the store (R-INT-2): it sends the card to
// an admin's private chat, writes the opened value to the store, folds the
// card when the request closes, and puts the node note for the agent
// (R-INT-6). It is the one way a module's secret or a server's header key is
// asked for from the chat; no tool reads a value back.
type Asker struct {
	reg     *Registry
	store   StoreFunc
	enqueue EnqueueFunc
	put     PutFunc
	lang    LangFunc
	private PrivateConvFunc
	texts   *texts.Catalog

	page string
	now  func() time.Time
	loc  *time.Location
	log  *slog.Logger

	// mu serialises the Asks that carry a Key, so a replay finds the
	// request its first run opened; byKey holds those requests until they
	// expire, answered or not, in memory as the registry does.
	mu    sync.Mutex
	byKey map[string]Asked
}

// AskerOption configures an Asker.
type AskerOption func(*Asker)

// WithPage sets the page address the card links to; the default is
// DefaultPage.
func WithPage(page string) AskerOption { return func(a *Asker) { a.page = page } }

// WithAskerClock sets the clock and the zone a card's times are shown in;
// the default is time.Now in UTC.
func WithAskerClock(now func() time.Time, loc *time.Location) AskerOption {
	return func(a *Asker) { a.now, a.loc = now, loc }
}

// WithAskerLogger sets the logger; the default discards. It logs ids,
// scopes, names and outcomes, never a value.
func WithAskerLogger(l *slog.Logger) AskerOption { return func(a *Asker) { a.log = l } }

// NewAsker returns an Asker over reg that writes to store, sends cards
// through enqueue and notes through put, in each admin's language.
func NewAsker(reg *Registry, store StoreFunc, enqueue EnqueueFunc, put PutFunc, lang LangFunc, private PrivateConvFunc,
	cat *texts.Catalog, opts ...AskerOption) *Asker {
	a := &Asker{reg: reg, store: store, enqueue: enqueue, put: put, lang: lang, private: private, texts: cat,
		page: DefaultPage, now: time.Now, loc: time.UTC, log: slog.New(slog.DiscardHandler), byKey: map[string]Asked{}}
	for _, o := range opts {
		o(a)
	}
	return a
}

// asking is one request as the Asker keeps it: what the card said and
// where, for the fold and the note. Its fields are set before the request
// is opened; failed is written by the store write and read by the close
// hook, which run one after the other on the registry's goroutine.
type asking struct {
	k       SecretAsk
	speak   texts.Speaker
	tr      string // the admin's transport
	conv    string // the admin's private conversation
	cardKey string
	card    string
	locked  bool // the store refused the value: it is locked
	done    SecretDoneFunc
}

// Ask opens a secret request for k, sends its card to k.Admin and returns
// the request. done is told once when it closes; pass NoSecretDone for
// nothing. A hosted page that fails its check refuses the request: the
// admin is told that handing secrets over is stopped, and the error wraps
// ErrPageChanged or ErrPageUnchecked (R-INT-3).
//
// A second Ask with the same Key (a rerun of the same tool call) while the
// first request lives returns that request, answered or not, and sends
// nothing: the card already holds its link. One whose request is gone (it
// expired, or the node restarted) opens a new request with a new card.
func (a *Asker) Ask(ctx context.Context, k SecretAsk, done SecretDoneFunc) (Asked, error) {
	if k.Key == "" {
		return a.open(ctx, k, done)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	for key, x := range a.byKey {
		if !now.Before(x.Expires) {
			delete(a.byKey, key)
		}
	}
	if x, ok := a.byKey[k.Key]; ok {
		return x, nil
	}
	asked, err := a.open(ctx, k, done)
	if err == nil {
		a.byKey[k.Key] = asked
	}
	return asked, err
}

// open opens a new request for k and sends its card.
func (a *Asker) open(ctx context.Context, k SecretAsk, done SecretDoneFunc) (Asked, error) {
	st, err := a.prepare(ctx, k, done)
	if err != nil {
		return Asked{}, err
	}
	req, err := a.reg.OpenSecret(ctx, k.Name, st.write(a), OnClosed(st.closed(a)))
	if errors.Is(err, ErrPageChanged) || errors.Is(err, ErrPageUnchecked) {
		a.stopped(ctx, st)
	}
	if err != nil {
		return Asked{}, err
	}
	if err := a.sendCard(ctx, st, req); err != nil {
		return Asked{}, fmt.Errorf("intake: ask %s %s: the card: %w", k.Scope, k.Name, err)
	}
	a.log.Info("intake: a secret was asked for", "request", req.ID, "scope", k.Scope, "name", k.Name)
	return Asked{ID: req.ID, Expires: req.Expires}, nil
}

// prepare checks k and finds the admin's chat and language.
func (a *Asker) prepare(ctx context.Context, k SecretAsk, done SecretDoneFunc) (*asking, error) {
	switch {
	case k.Scope == "":
		return nil, fmt.Errorf("%w: no scope", ErrAsk)
	case !secrets.ValidName(k.Name):
		return nil, fmt.Errorf("%w: %w", ErrAsk, ErrName)
	case k.Admin == "":
		return nil, fmt.Errorf("%w: no admin", ErrAsk)
	}
	tr, conv, ok := a.private(k.Admin)
	if !ok {
		return nil, fmt.Errorf("%w: the admin has no private chat the node can reach", ErrAsk)
	}
	lang, err := a.lang(ctx, k.Admin)
	if err != nil {
		lang = texts.English
	}
	return &asking{k: k, speak: texts.Speaker{Lang: lang, Cat: a.texts}, tr: tr, conv: conv, done: done}, nil
}

// write is the request's SecretFunc: the value goes to the store and
// nowhere else.
func (st *asking) write(a *Asker) SecretFunc {
	return func(ctx context.Context, id string, value []byte) error {
		err := a.store(ctx, st.k.Scope, st.k.Name, string(value), st.k.Admin)
		if err != nil {
			st.locked = errors.Is(err, ErrStoreLocked)
			a.log.Warn("intake: the secret was not stored", "request", id, "scope", st.k.Scope, "name", st.k.Name,
				"locked", st.locked)
			return errors.New("intake: not stored")
		}
		return nil
	}
}

// ErrStoreLocked is what a StoreFunc wraps when the store is locked
// (vault.ErrLocked: intake does not import the store); the card then folds
// saying so.
var ErrStoreLocked = errors.New("intake: the secret store is locked")

// closed is the request's own close hook: fold the card, note it for the
// agent, tell the opener.
func (st *asking) closed(a *Asker) ClosedFunc {
	return func(ctx context.Context, c Closed) {
		out := secretOutcome(c.Outcome)
		sc := event.SecretClosed{Request: c.ID, Name: st.k.Name, Scope: string(st.k.Scope), Outcome: out,
			Principal: st.k.Admin, At: c.At}
		a.fold(ctx, st, c, out)
		a.note(ctx, st, sc)
		st.done(ctx, sc)
	}
}

// secretOutcome maps a request's outcome to the note's.
func secretOutcome(o Outcome) event.SecretOutcome {
	switch o {
	case OutcomeAnswered:
		return event.SecretSet
	case OutcomeExpired:
		return event.SecretExpired
	case OutcomeFailed:
		return event.SecretFailed
	}
	return event.SecretFailed
}

// sendCard sends the card: who asks, the name, where the value may go,
// the agent's reason quoted as data, the link and how long it works.
func (a *Asker) sendCard(ctx context.Context, st *asking, req SecretRequest) error {
	host := ""
	if len(st.k.Hosts) > 0 {
		host = st.k.Hosts[0]
	}
	link := SecretLink(a.page, req, host)
	st.card = a.cardText(st, req, link)
	st.cardKey = "intake:" + req.ID + ":card" // per request: a new request after a restart gets its own card
	return a.enqueue(ctx, event.Outgoing{IdemKey: st.cardKey, Transport: st.tr, Conv: st.conv, Text: st.card, NoPreview: true})
}

// cardText is the card in the admin's language (R-INT-2). Every value from
// the opener is escaped; the reason is one line, cut, and quoted.
func (a *Asker) cardText(st *asking, req SecretRequest, link string) string {
	sp, k := st.speak, st.k
	vars := texts.Vars{varName: texts.Escape(k.Name), "who": texts.Escape(k.Who), "hosts": texts.Escape(strings.Join(k.Hosts, ", ")),
		"time": req.Expires.In(a.loc).Format("15:04"), "link": "[" + sp.Text(texts.SecretEnter, nil) + "](" + link + ")"}
	parts := []string{sp.Text(headKey(k), vars)}
	if why := quoteWhy(k.Why); why != "" {
		parts = append(parts, sp.Text(texts.SecretCardWhy, texts.Vars{"why": why}))
	}
	if k.Tainted {
		parts = append(parts, sp.Text(texts.SecretCardTainted, nil))
	}
	parts = append(parts, sp.Text(texts.SecretCardBody, vars))
	return strings.Join(parts, "\n\n")
}

// headKey is the card's first line for who asks.
func headKey(k SecretAsk) texts.Key {
	switch k.Asking {
	case AskingModule:
		if len(k.Hosts) == 0 {
			return texts.SecretCardModuleNoHosts
		}
		return texts.SecretCardModule
	case AskingServer:
		return texts.SecretCardServer
	case AskingNode:
	}
	return texts.SecretCardNode
}

// quoteWhy is the agent's reason as one escaped line of at most 300
// characters; "" for none.
func quoteWhy(why string) string {
	why = strings.Join(strings.Fields(why), " ")
	if utf8.RuneCountInString(why) > maxWhyRunes {
		why = string([]rune(why)[:maxWhyRunes]) + "…"
	}
	return texts.Escape(why)
}

// stopped tells the admin that handing secrets over is stopped: the page
// failed its check (R-INT-3).
func (a *Asker) stopped(ctx context.Context, st *asking) {
	msg := event.Outgoing{IdemKey: "intake:stopped:" + st.k.Key + ":" + fmt.Sprint(a.now().UnixNano()), Transport: st.tr, Conv: st.conv,
		Text: st.speak.Text(texts.SecretStopped, texts.Vars{varName: texts.Escape(st.k.Name)})}
	if err := a.enqueue(ctx, msg); err != nil {
		a.log.Warn("intake: the stop notice was not stored", "scope", st.k.Scope, "name", st.k.Name)
	}
}

// fold turns the card into one line saying how the request ended, with the
// card kept folded under it (R-INT-6).
func (a *Asker) fold(ctx context.Context, st *asking, c Closed, out event.SecretOutcome) {
	if st.cardKey == "" { // the card was never sent
		return
	}
	vars := texts.Vars{varName: texts.Escape(st.k.Name), "whose": a.whose(st), "time": c.At.In(a.loc).Format("15:04")}
	key := texts.SecretLineSet
	switch {
	case out == event.SecretExpired:
		key = texts.SecretLineExpired
	case out == event.SecretFailed && st.locked:
		key = texts.SecretLineLocked
	case out == event.SecretFailed:
		key = texts.SecretLineFailed
	}
	msg := event.Outgoing{IdemKey: "intake:" + c.ID + ":fold", Transport: st.tr, Conv: st.conv, EditOf: st.cardKey,
		Text: st.speak.Text(key, vars), Folded: st.card, NoPreview: true}
	if err := a.enqueue(ctx, msg); err != nil {
		a.log.Warn("intake: the card was not folded", "request", c.ID)
	}
}

// whose names the secret's owner in the admin's language.
func (a *Asker) whose(st *asking) string {
	who := texts.Vars{"who": texts.Escape(st.k.Who)}
	switch st.k.Asking {
	case AskingModule:
		return st.speak.Text(texts.SecretWhoseModule, who)
	case AskingServer:
		return st.speak.Text(texts.SecretWhoseServer, who)
	case AskingNode:
	}
	return st.speak.Text(texts.SecretWhoseNode, nil)
}

// note puts the node note for the conversation that asked (R-INT-6). A
// request opened with no conversation (none to resume) writes none.
func (a *Asker) note(ctx context.Context, st *asking, sc event.SecretClosed) {
	if st.k.Conv == "" {
		return
	}
	e, err := closedEvent(st.k, sc, a.now())
	if err == nil {
		err = a.put(ctx, e)
	}
	if err != nil {
		a.log.Warn("intake: the node note of a secret was not stored", "request", sc.Request)
	}
}

// closedEvent is the inbox event of a closed request; its key is fixed per
// request, so it is stored once.
func closedEvent(k SecretAsk, sc event.SecretClosed, now time.Time) (event.Event, error) {
	sc.Transport = k.Transport
	payload, err := json.Marshal(sc)
	if err != nil {
		return event.Event{}, err
	}
	return event.Event{ID: event.NewID(now), IdemKey: "intake:" + sc.Request + ":closed", Source: event.SourceIntake,
		Conv: k.Conv, Kind: event.KindSecretClosed, Payload: payload, At: now}, nil
}
