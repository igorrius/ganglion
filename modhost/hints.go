package modhost

import (
	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/texts"
)

// hintKeys are the node's fixed hints by how a run failed: what the kind
// means and where to look, so the model does not guess (R-RUN-14). Each is
// the node's text; none holds the module's.
var hintKeys = map[Kind]texts.Key{
	KindAuthFailed: texts.HintModuleAuthFailed,
	KindStale:      texts.HintModuleStale,
	KindFormat:     texts.HintModuleFormat,
	KindException:  texts.HintModuleException,
}

// hint is the node's hint for a failed run of the module short: by the
// result for a timeout or a trap, else by the kind. The model reads it, so
// it is English (texts.English).
func hint(o outcome, short string) string {
	key, ok := hintKeys[o.kind]
	switch o.result {
	case ResultTimeout:
		key, ok = texts.HintModuleTimeout, true
	case ResultException:
		key, ok = texts.HintModuleTrap, true
	case ResultOK, ResultFailed, ResultFormat:
	}
	if !ok {
		return ""
	}
	return texts.Shipped().Label(texts.English, key, texts.Vars{"name": short})
}

// Hint is the node's hint for a module's health or failure kind, as the
// module.status tool shows it; "" for a kind it has none for.
func (h *Host) Hint(name string, kind modstore.Health) string {
	return hint(outcome{result: ResultFailed, kind: Kind(kind)}, modstore.ShortName(fullName(name)))
}

// withHint is the node's words for o followed by its hint.
func withHint(words string, o outcome, short string) string {
	if hn := hint(o, short); hn != "" {
		return words + ". " + hn
	}
	return words
}
