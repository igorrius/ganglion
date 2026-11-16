package settings

import (
	"errors"
	"slices"
	"strings"
)

// ErrNotAllowed is a field config.propose does not change (R-CFG-14).
var ErrNotAllowed = errors.New("settings: the field is not changed from the chat")

// AllowedPaths are the fields config.propose changes (R-CFG-14, ADR-0093),
// sorted. A field is added here on purpose, never by a schema change alone:
// one that widens the node, moves its data or names a secret stays a host
// edit.
var AllowedPaths = []string{
	"agent.call_timeout", "agent.languages", "agent.limits.daily_budget_usd",
	"agent.limits.daily_tokens", "agent.limits.daily_turns", pathSearchMode,
	"agent.session_idle", "agent.turn_timeout", pathLogLevel, "memory.prompt_budget",
	"memory.review.enabled", "memory.review.quiet", "models.purposes", "node.memory_limit",
	"node.tz", "telegram.reactions.after", "telegram.reactions.dwell", pathTelegramReply,
	"telegram.rich", "update.check",
}

// Paths that are also named by the start's checks and the retired
// variables.
const (
	pathLogLevel      = "log.level"
	pathTelegramReply = "telegram.reply"
	pathSearchMode    = "agent.search.mode"
	keyModules        = "modules"
)

// Place is where a field of the node's file is changed.
type Place string

// The places of R-CFG-14.
const (
	// PlaceChat is config.propose.
	PlaceChat Place = "chat"
	// PlaceOwnTool is a tool or a card of the field's own: modules,
	// destinations, groups, the update mode and the public listener.
	PlaceOwnTool Place = "own-tool"
	// PlaceHost is an edit of the file on the host.
	PlaceHost Place = "host"
)

// ownTool are the fields, and the sections, that have a tool of their own.
var ownTool = []string{"destinations", "http", keyModules, "telegram.groups", "telegram.updates"}

// Allowed reports whether config.propose changes the field at path.
func Allowed(path string) bool {
	_, ok := slices.BinarySearch(AllowedPaths, path)
	return ok
}

// Where is the place the field at path is changed.
func Where(path string) Place {
	switch {
	case Allowed(path):
		return PlaceChat
	case slices.ContainsFunc(ownTool, func(p string) bool { return within(path, p) }):
		return PlaceOwnTool
	default:
		return PlaceHost
	}
}

// within reports whether path is p or a field under it.
func within(path, p string) bool {
	return path == p || strings.HasPrefix(path, p+".") || strings.HasPrefix(path, p+"[")
}
