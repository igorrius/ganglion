package agent

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/secrets"
	"github.com/igorrius/ganglion/secrets/vault"
	"github.com/igorrius/ganglion/texts"
)

// SecretsAdmin is what /secrets does with the secret store (vault.Vault).
// Nothing in it returns a value.
type SecretsAdmin interface {
	List(ctx context.Context) ([]vault.Entry, error)
	Remove(ctx context.Context, scope secrets.Scope, name string) (bool, error)
	Reset(ctx context.Context) (int, error)
	Locked() bool
}

// NoSecrets is a node without a store: nothing listed, nothing removed.
type NoSecrets struct{}

// List lists nothing.
func (NoSecrets) List(context.Context) ([]vault.Entry, error) { return nil, nil }

// Remove has no secret.
func (NoSecrets) Remove(context.Context, secrets.Scope, string) (bool, error) { return false, nil }

// Reset drops nothing.
func (NoSecrets) Reset(context.Context) (int, error) { return 0, nil }

// Locked is never true.
func (NoSecrets) Locked() bool { return false }

// Words of /secrets and the names of the values its texts take.
const (
	wordNode    = "node"
	wordConfirm = "confirm"
	varScope    = "scope"
	varCount    = "count"
)

const secretsNote = commandPrefix + "the owner ran /secrets: "

// secretsReply is what /secrets answers and what the history notes; note
// is "" when nothing changed. A note holds a scope and a secret's name,
// never a value: the command has none.
type secretsReply struct{ text, note string }

// secretsVerbs are the words after /secrets that change the store.
var secretsVerbs = map[string]func(*Agent, context.Context, texts.Speaker, []string) (secretsReply, error){
	"rm":    (*Agent).removeSecret,
	"reset": (*Agent).resetSecrets,
}

// secretsCmd is /secrets: the names of the stored secrets by owner, the
// removal of one, and the reset of a locked store (R-CFG-9, R-CFG-10), for
// an admin in their private chat, without a model. It never shows a value.
func (a *Agent) secretsCmd(ctx context.Context, t *turn, arg string) error {
	if err := a.setAdmin(ctx, t); err != nil {
		return err
	}
	if !t.admin {
		t.reply(a.say(ctx, t, texts.ReplyNotAvailable, nil))
		return nil
	}
	if _, conv, ok := a.d.Private(t.msg.From); !ok || conv != t.event.Conv {
		t.reply(a.say(ctx, t, texts.SecretsPrivateOnly, nil))
		return nil
	}
	r, err := a.secretsReply(ctx, a.speaker(ctx, t), strings.Fields(arg))
	if err != nil {
		return fmt.Errorf("agent: /secrets: %w", err)
	}
	t.reply(r.text)
	if r.note != "" {
		t.note(secretsNote + r.note)
	}
	return nil
}

// secretsReply runs the command in words.
func (a *Agent) secretsReply(ctx context.Context, sp texts.Speaker, words []string) (secretsReply, error) {
	if len(words) == 0 {
		return a.listSecrets(ctx, sp)
	}
	verb, ok := secretsVerbs[words[0]]
	if !ok {
		return secretsReply{text: sp.Text(texts.SecretsUsage, nil)}, nil
	}
	return verb(a, ctx, sp, words[1:])
}

// listSecrets replies with the names by owner and the day each was set.
func (a *Agent) listSecrets(ctx context.Context, sp texts.Speaker) (secretsReply, error) {
	entries, err := a.d.Secrets.List(ctx)
	if err != nil {
		return secretsReply{}, err
	}
	var b strings.Builder
	if a.d.Secrets.Locked() {
		b.WriteString(sp.Text(texts.SecretsLocked, nil) + "\n\n")
	}
	if len(entries) == 0 {
		b.WriteString(sp.Text(texts.SecretsEmpty, nil))
		return secretsReply{text: b.String()}, nil
	}
	b.WriteString(sp.Text(texts.SecretsHead, nil))
	var scope secrets.Scope
	for _, e := range entries { // by scope, then name
		if e.Scope != scope {
			scope = e.Scope
			b.WriteString("\n\n" + scopeTitle(sp, scope))
		}
		b.WriteString("\n- " + sp.Text(texts.SecretsRow, texts.Vars{varName: texts.Escape(e.Name),
			"date": e.SetAt.In(a.cfg.Location).Format(dateLayout)}))
	}
	return secretsReply{text: b.String()}, nil
}

// scopeTitle names a scope to a person: a module by its short name, the
// node's own by a fixed text.
func scopeTitle(sp texts.Speaker, scope secrets.Scope) string {
	if scope == secrets.ScopeNode {
		return sp.Text(texts.SecretsScopeNode, nil)
	}
	return texts.Escape(scopeWord(scope))
}

// scopeWord is the word that names a scope in the command and in a note:
// "node", or a module's short name.
func scopeWord(scope secrets.Scope) string {
	if scope == secrets.ScopeNode {
		return wordNode
	}
	module := strings.TrimPrefix(string(scope), string(secrets.ModuleScope("")))
	return strings.TrimPrefix(module, modstore.LocalPrefix)
}

// scopeOf is the scope a word of the command names: "node", or a module
// with or without "local/". A word that cannot name a module names none.
func scopeOf(word string) (secrets.Scope, bool) {
	if word == wordNode {
		return secrets.ScopeNode, true
	}
	short := strings.TrimPrefix(word, modstore.LocalPrefix)
	if !secrets.ValidName(short) {
		return "", false
	}
	return secrets.ModuleScope(modstore.LocalPrefix + short), true
}

// removeSecret is /secrets rm <module|node> NAME.
func (a *Agent) removeSecret(ctx context.Context, sp texts.Speaker, args []string) (secretsReply, error) {
	if len(args) != 2 || !secrets.ValidName(args[1]) {
		return secretsReply{text: sp.Text(texts.SecretsUsage, nil)}, nil
	}
	scope, ok := scopeOf(args[0])
	if !ok {
		return secretsReply{text: sp.Text(texts.SecretsUsage, nil)}, nil
	}
	removed, err := a.d.Secrets.Remove(ctx, scope, args[1])
	if err != nil {
		return secretsReply{}, err
	}
	vars := texts.Vars{varScope: scopeTitle(sp, scope), varName: texts.Escape(args[1])}
	if !removed {
		return secretsReply{text: sp.Text(texts.SecretsNotFound, vars)}, nil
	}
	return secretsReply{text: sp.Text(texts.SecretsRemoved, vars),
		note: "removed the stored secret " + args[1] + " of " + scopeWord(scope)}, nil
}

// resetSecrets is /secrets reset, which only says what it would drop, and
// /secrets reset confirm, which drops every stored secret and adopts the
// node's key.
func (a *Agent) resetSecrets(ctx context.Context, sp texts.Speaker, args []string) (secretsReply, error) {
	switch {
	case len(args) == 0:
		entries, err := a.d.Secrets.List(ctx)
		if err != nil {
			return secretsReply{}, err
		}
		return secretsReply{text: sp.Text(texts.SecretsResetAsk, texts.Vars{varCount: strconv.Itoa(len(entries))})}, nil
	case len(args) != 1 || args[0] != wordConfirm:
		return secretsReply{text: sp.Text(texts.SecretsUsage, nil)}, nil
	}
	n, err := a.d.Secrets.Reset(ctx)
	if err != nil {
		return secretsReply{}, err
	}
	count := strconv.Itoa(n)
	return secretsReply{text: sp.Text(texts.SecretsResetDone, texts.Vars{varCount: count}),
		note: "reset the secret store, dropping all " + count + " stored secrets"}, nil
}
