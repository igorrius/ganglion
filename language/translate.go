package language

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/router"
	"github.com/igorrius/ganglion/sqlitedb"
	"github.com/igorrius/ganglion/texts"
)

const (
	// attemptsPerDay caps what a failing model can cost per language (R-LANG-5).
	attemptsPerDay = 3
	callTimeout    = 3 * time.Minute
	// retryAfter is how long a language waits after a failed attempt.
	retryAfter = 15 * time.Minute
	// maxAnswerTokens is well above the whole catalogue in a wordy script.
	maxAnswerTokens = 16000
	// echoSweep is the fewest texts that say anything when they all equal English: one word such as
	// "Details" may be correct as it is.
	echoSweep = 5
)

// Outcomes of a translation run: a metric label.
const (
	outcomeDone    = "done"
	outcomePartial = "partial"
	outcomeRefused = "refused"
	outcomeFailed  = "failed"
	outcomeCapped  = "capped"
)

const translatePrompt = `Translate the values of this JSON object into %s (language code %s). They are the fixed texts of a chat assistant: buttons, short notices and questions a person reads.
Keep every {placeholder} exactly as it is, untranslated. Add no Markdown, links, mentions or line breaks.
Use plain, friendly words. Answer with the JSON object only, with the same keys.`

// sourceHash identifies an English text.
func sourceHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

// pack is one stored translation.
type pack struct{ text, hash string }

// Pack finds a machine translation of the current English text; it reads memory and never blocks.
// A text whose English source has changed counts as missing, so a person reads English until it is
// translated again.
func (s *Store) Pack(l texts.Lang, k texts.Key) (string, bool) {
	s.packsMu.RLock()
	defer s.packsMu.RUnlock()
	p, ok := s.packs[l][k]
	if !ok || p.hash != sourceHash(texts.EnglishText(k)) {
		return "", false
	}
	return p.text, true
}

// loadPacks reads the stored translations into memory.
func (s *Store) loadPacks(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT lang, key, text, source_hash FROM packs`)
	if err != nil {
		return fmt.Errorf("language: packs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	all := map[texts.Lang]map[texts.Key]pack{}
	for rows.Next() {
		var l, k, text, hash string
		if err := rows.Scan(&l, &k, &text, &hash); err != nil {
			return fmt.Errorf("language: packs: %w", err)
		}
		if all[texts.Lang(l)] == nil {
			all[texts.Lang(l)] = map[texts.Key]pack{}
		}
		all[texts.Lang(l)][texts.Key(k)] = pack{text, hash}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("language: packs: %w", err)
	}
	s.packsMu.Lock()
	defer s.packsMu.Unlock()
	s.packs = all
	return nil
}

// savePack stores accepted texts in one transaction and refreshes memory.
func (s *Store) savePack(ctx context.Context, l texts.Lang, model string, ok map[texts.Key]string) error {
	err := sqlitedb.Immediate(ctx, s.db, func(c *sql.Conn) error {
		for k, text := range ok {
			if _, err := c.ExecContext(ctx,
				`INSERT OR REPLACE INTO packs (lang, key, text, source_hash, model, made_ms) VALUES (?, ?, ?, ?, ?, ?)`,
				string(l), string(k), text, sourceHash(texts.EnglishText(k)), model, s.now().UnixMilli()); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("language: save pack %s: %w", l, err)
	}
	return s.loadPacks(ctx)
}

// attempt counts one attempt at l today, and reports false if the day's cap is reached.
func (s *Store) attempt(ctx context.Context, l texts.Lang, now time.Time) (bool, error) {
	day := now.UTC().Format(time.DateOnly)
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO pack_attempts (lang, day, n) VALUES (?, ?, 1)
		 ON CONFLICT (lang, day) DO UPDATE SET n = n + 1 WHERE n < ?`,
		string(l), day, attemptsPerDay)
	if err != nil {
		return false, fmt.Errorf("language: attempt %s: %w", l, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("language: attempt %s: %w", l, err)
	}
	return n > 0, nil
}

// TranslatorOption configures a Translator.
type TranslatorOption func(*Translator)

// WithTranslatorClock sets the clock; the default is time.Now.
func WithTranslatorClock(now func() time.Time) TranslatorOption {
	return func(t *Translator) { t.now = now }
}

// WithTranslatorLogger sets the logger; the default drops everything.
func WithTranslatorLogger(l *slog.Logger) TranslatorOption {
	return func(t *Translator) { t.log = l }
}

// Translator translates fixed texts once a person is being written to in a language no file covers,
// in the background (R-LANG-5). It is lazy: nobody written to costs nothing, and a restart needs no
// queue, since the next message asks again.
type Translator struct {
	store     *Store
	cat       *texts.Catalog
	router    router.Router
	providers map[string]provider.Provider
	now       func() time.Time
	log       *slog.Logger
	wake      chan struct{}
	neededMu  sync.Mutex
	needed    map[texts.Lang]bool
	in        instruments
	// failedAt holds when each language last failed, so a short outage waits instead of using up
	// the day on one person's first messages. Only Run's goroutine touches it.
	failedAt map[texts.Lang]time.Time
}

// NewTranslator returns a Translator. cat must find packs through s.
func NewTranslator(s *Store, cat *texts.Catalog, r router.Router, ps map[string]provider.Provider, opts ...TranslatorOption) *Translator {
	t := &Translator{store: s, cat: cat, router: r, providers: ps, now: time.Now, log: slog.New(slog.DiscardHandler),
		wake: make(chan struct{}, 1), needed: map[texts.Lang]bool{}, in: newInstruments(), failedAt: map[texts.Lang]time.Time{}}
	for _, o := range opts {
		o(t)
	}
	return t
}

// Need says a person is being written to in l. It never blocks, since a person's turn is the caller,
// and does nothing when l is covered, so it is cheap on every turn.
func (t *Translator) Need(l texts.Lang) {
	if n, known := texts.Normalize(string(l)); !known || n != l {
		return // only a value exactly from the table reaches the prompt and the metrics
	}
	if len(t.missing(l)) == 0 {
		return
	}
	t.neededMu.Lock()
	t.needed[l] = true
	t.neededMu.Unlock()
	select {
	case t.wake <- struct{}{}:
	default:
	}
}

// Run translates what is needed until ctx ends.
func (t *Translator) Run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.wake:
		}
		t.sweep(ctx)
	}
}

// sweep translates what was asked for since the last one.
func (t *Translator) sweep(ctx context.Context) {
	t.neededMu.Lock()
	needed := slices.Sorted(maps.Keys(t.needed))
	clear(t.needed)
	t.neededMu.Unlock()
	for _, l := range needed {
		if missing := t.missing(l); len(missing) > 0 {
			t.translate(ctx, l, missing)
		}
	}
}

// missing are the English texts of the keys no file or current translation covers in l.
func (t *Translator) missing(l texts.Lang) map[texts.Key]string {
	out := map[texts.Key]string{}
	for _, k := range texts.Keys() {
		if t.cat.Source(l, k) == texts.SourceEnglish {
			out[k] = texts.EnglishText(k)
		}
	}
	return out
}

// translate makes one attempt at l, within the day's cap.
func (t *Translator) translate(ctx context.Context, l texts.Lang, source map[texts.Key]string) {
	if last, failed := t.failedAt[l]; failed && t.now().Sub(last) < retryAfter {
		return
	}
	t.failedAt[l] = t.now() // cleared by a whole result
	ok, err := t.store.attempt(ctx, l, t.now())
	if err != nil {
		t.log.Warn("language: attempt not counted", "lang", string(l), "err", err)
		return
	}
	if !ok {
		t.count(ctx, l, outcomeCapped) // a person stuck in English shows here
		return
	}
	answer, model, err := t.ask(ctx, l, source)
	if err != nil {
		t.count(ctx, l, outcomeFailed)
		t.log.Warn("language: translation failed; texts stay in English", "lang", string(l), "err", err)
		return
	}
	accepted, outcome, reason := accept(source, answer)
	if outcome == outcomeRefused {
		t.count(ctx, l, outcomeRefused)
		t.log.Warn("language: translation refused; texts stay in English", "lang", string(l), "reason", reason)
		return
	}
	if rejected := len(source) - len(accepted); rejected > 0 {
		t.in.refused.Add(ctx, int64(rejected), metric.WithAttributes(attribute.String("lang", string(l))))
		t.log.Warn("language: translated texts refused", "lang", string(l), "count", rejected, "first", reason)
	}
	if err := t.store.savePack(ctx, l, model, accepted); err != nil {
		t.log.Warn("language: translation not stored", "lang", string(l), "err", err)
		return
	}
	if outcome == outcomeDone {
		delete(t.failedAt, l)
	}
	t.count(ctx, l, outcome)
	t.log.Info("language: translated", "lang", string(l), "texts", len(accepted), "outcome", outcome)
}

func (t *Translator) count(ctx context.Context, l texts.Lang, outcome string) {
	t.in.runs.Add(ctx, 1, metric.WithAttributes(attribute.String("lang", string(l)), attribute.String("outcome", outcome)))
}

// ask makes one call to the first candidate, so an attempt is one call and the cap means what it says.
// It has a clean context: no tools, no history, only our strings and the language's name from the
// table (R-LANG-5).
func (t *Translator) ask(ctx context.Context, l texts.Lang, source map[texts.Key]string) (string, string, error) {
	cands, err := t.router.Route(ctx, router.TaskReq{Tier: router.TierCheap, Kind: router.TaskTranslate})
	if err != nil {
		return "", "", err
	}
	if len(cands) == 0 {
		return "", "", errors.New("no model")
	}
	c := cands[0]
	p, ok := t.providers[c.Provider]
	if !ok {
		return "", "", fmt.Errorf("no provider %s", c.Provider)
	}
	raw, err := json.Marshal(source)
	if err != nil {
		return "", "", err
	}
	msgs := []provider.Message{
		{Role: provider.RoleSystem, Content: fmt.Sprintf(translatePrompt, texts.Native(l), string(l))},
		{Role: provider.RoleUser, Content: string(raw)},
	}
	cctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	resp, err := p.Chat(cctx, provider.ChatRequest{Model: c.Model, Messages: msgs, MaxTokens: maxAnswerTokens})
	t.router.Report(c, err)
	if err != nil {
		return "", "", err
	}
	u := resp.Usage
	attrs := metric.WithAttributes(attribute.String("lang", string(l)))
	t.in.tokens.Add(ctx, int64(u.InputTokens+u.OutputTokens), attrs)
	t.in.cost.Add(ctx, c.Cost(u.InputTokens, u.OutputTokens), attrs)
	return resp.Message.Content, c.Model, nil
}

// accept applies the checks to a model's answer (R-LANG-5). A text that fails stays in English; an
// answer that is not JSON, holds nothing, only echoes English, or fails an approval text is refused
// whole. The reason it returns names only keys of ours, since a model's own go to the log.
func accept(source map[texts.Key]string, answer string) (map[texts.Key]string, string, string) {
	var tr map[texts.Key]string
	if err := json.Unmarshal([]byte(unfence(answer)), &tr); err != nil {
		return nil, outcomeRefused, "not JSON"
	}
	ok, rejected := texts.Check(source, tr)
	if len(ok) == 0 {
		return nil, outcomeRefused, "no text accepted"
	}
	var approvals, echoed, same int
	for k := range source {
		if ok[k] == source[k] {
			same++
		}
		if !texts.IsApproval(k) {
			continue
		}
		approvals++
		if _, good := ok[k]; !good {
			return nil, outcomeRefused, "approval text " + string(k) + " failed: " + rejected[k]
		}
		if ok[k] == source[k] {
			echoed++
		}
	}
	if same == len(ok) && len(ok) >= echoSweep || approvals >= echoSweep && echoed == approvals {
		return nil, outcomeRefused, "only echoes English"
	}
	if len(ok) == len(source) {
		return ok, outcomeDone, ""
	}
	for _, k := range slices.Sorted(maps.Keys(source)) {
		if reason, bad := rejected[k]; bad {
			return ok, outcomePartial, string(k) + ": " + reason
		}
	}
	return ok, outcomePartial, "keys missing from the answer"
}

// unfence drops a Markdown code fence around an answer, which models add unasked.
func unfence(s string) string {
	s = strings.TrimSpace(s)
	if rest, ok := strings.CutPrefix(s, "```"); ok {
		if _, body, found := strings.Cut(rest, "\n"); found {
			s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(body), "```"))
		}
	}
	return s
}
