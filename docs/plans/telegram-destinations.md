# Named destinations: the node's file routes a module's messages — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The admin says in the private chat where a module's messages go, by a topic's title, and they go there from the next message; the agent can post to a topic by name.

**Architecture:** `destinations:` in the node's file names conversations; a module's line lists `notify_to`. The module host puts one `module.notify` event per destination, resolved when the message is sent. The transport reports the titles of the forum topics it sees; `groups` keeps them for connected groups. Admin tools write the file through `settings`, each after a confirmation.

**Tech stack:** as in [telegram-groups.md](telegram-groups.md): Go, `settings` YAML node editing, SQLite through `sqlitedb`, the fake Bot API, a test that runs the node's binary.

**Design:** [telegram-groups-design.md](telegram-groups-design.md), sections 7, 8 (as changed on 2026-11-13) and 10; [ADR-0079](../adr/0079-a-modules-messages-go-where-the-nodes-file-says.md). Second of two plans; the first is [telegram-groups.md](telegram-groups.md).

## Global constraints

- [docs/conventions.md](../conventions.md) holds: small functions, named function types, no `nil` collaborators, closed sets as types, metrics with closed label sets, an abuse test with every change that crosses a trust boundary.
- English in all documents, comments and commit messages.
- No change to the module ABI, the PDK, the reflex template or a module's manifest.
- A topic's title, a group's title and a module's text are outside text: never in a log line above debug, never a metric label, never in a note the model reads unless wrapped as outside content.
- `owner` is a reserved destination name. At most 8 names on a module's line.
- The spec and the ADR change in task 1, before the code. The code map changes with the commit that adds or changes a package.
- Before a task is done: `make lint` and `go test ./...` pass.

## File structure

| File | Responsibility |
|---|---|
| `docs/adr/0079-*.md`, `docs/spec.md`, `docs/plans/telegram-groups-design.md` | The decision (done with this plan's first commit) |
| `settings/settings.go`, `settings/load.go`, `schemas/node.schema.json` | `destinations:`, `modules[].notify_to`, their checks |
| `settings/write_destinations.go` (new) | `DestinationsFile`: read, set, remove; a module's `notify_to` |
| `transport/telegram/topics.go` (new), `types.go`, `transport.go` | A topic's title from its root and from the service messages, handed to a hook |
| `groups/topics.go` (new), `groups/migrations/002_group_topics.sql` | Titles of the topics of connected groups |
| `groups/destinations.go` (new) | Resolving a name to a transport and a conversation; what a conversation is called |
| `modhost/notify.go`, `modhost/load.go`, `modhost/host.go` | One event per destination |
| `event/message.go`, `agent/module_notify.go` | `ModuleNotify.To`; no note outside the owner's chat |
| `tool/builtin/destinations.go` (new), `tool/builtin/message.go` | The admin's tools; `message.send` by name |
| `texts/…`, `config/groups.go`, `config/agent.go`, `config/modhost.go` | Texts and wiring |
| `skills/builtin/module-writing/SKILL.md`, `ganglion.example.yaml`, `docs/architecture/*` | What the agent and a reader are told |

---

### Task 1: The file: `destinations:` and `notify_to`

**Files:** `settings/settings.go`, `settings/load.go`, `settings/write.go`, `settings/write_destinations.go` (new), `settings/write_destinations_test.go` (new), `settings/load_test.go`, `settings/schema_test.go`, `schemas/node.schema.json`

**Interfaces — produces:**

```go
const DestinationOwner = "owner" // the module's owner's private chat
const MaxNotifyTo = 8

type Destination struct {
	Name string `yaml:"name"` // ^[a-z][a-z0-9-]{0,31}$, not "owner"
	Conv string `yaml:"conv"` // a conversation key, "telegram:-1001/45"
}
// File gains:   Destinations []Destination `yaml:"destinations"`
// Module gains: NotifyTo []string `yaml:"notify_to"` // nil: the owner

type DestinationsFile struct{ Path string }
func (f DestinationsFile) Destinations() ([]Destination, error)
func (f DestinationsFile) SetDestination(d Destination) error
func (f DestinationsFile) RemoveDestination(name string) error // ErrDestinationUsed when a module's line names it
func (f DestinationsFile) SetNotifyTo(module string, names []string) error // nil or [] drops the key
var ErrDestinationUsed, ErrNoDestination error
```

- [x] Tests first: a destination is set, replaced and removed with the file's comments kept; a bad name, the name `owner`, an empty or malformed conv, a duplicate name are refused by the writer and at load with the path of the key; a module's `notify_to` naming an unknown destination, a duplicate or more than 8 is refused at load; removing a destination a module uses returns `ErrDestinationUsed` and names nothing in the file; `SetNotifyTo` writes a flow list, keeps the module's `config`, and with no names removes the key.
- [x] Implement, with the schema (`/$defs/destination`, `module.notify_to`) and the schema test's table.
- [x] `go test ./settings/ ./schemas/`, lint, commit: `settings: destinations and a module's notify_to in the node's file`.

### Task 2: Topic titles

**Files:** `transport/telegram/types.go`, `transport/telegram/topics.go` (new), `transport/telegram/transport.go` (`seen`, options), `transport/telegram/topics_test.go` (new), `transport/telegram/telegramtest/server.go`, `groups/topics.go` (new), `groups/migrations/002_group_topics.sql`, `groups/topics_test.go` (new)

**Interfaces — produces:**

```go
// telegram
type TopicFunc func(ctx context.Context, group string, thread int64, title string)
func WithTopics(f TopicFunc) Option // default: nothing
// telegramtest: Incoming gains TopicTitle string (adds the topic's root as reply_to_message);
// func (s *Server) AddTopicEdited(chatID, threadID int64, title string) int64

// groups
type Topic struct{ Group string; Thread int64; Title string }
type Topics struct{ /* store, connected */ }
func NewTopics(s *Store, has func(ctx context.Context, group string) (bool, error), log *slog.Logger) Topics
func (t Topics) Seen(ctx context.Context, group string, thread int64, title string) // only a connected group's; a failure is logged
func (s *Store) Topics(ctx context.Context, group string) ([]Topic, error)
func (s *Store) Topic(ctx context.Context, group string, thread int64) (Topic, bool, error)
```

- [x] Tests first. Transport: a topic's message whose root carries `forum_topic_created.name`, the service message that creates a topic, and `forum_topic_edited` each call the hook once with the title; the same title again does not call it; a message that does not address the bot still teaches the title; a private chat and a group that is no forum call nothing. Groups: a title is stored only for a connected group, cut at 128 runes, replaced by a newer one; a group holds at most 200 topics (abuse: a member cannot fill the table).
- [x] Implement: `forum_topic_created` and `forum_topic_edited` decoded to `{name}`; `seen` calls the hook, after a per-topic in-memory check that the title changed.
- [x] Tests, lint, code map, commit: `telegram, groups: the node remembers the titles of a connected group's topics`.

### Task 3: A module's messages go to its line's destinations

**Files:** `groups/destinations.go` (new), `groups/destinations_test.go` (new), `event/message.go`, `modhost/host.go`, `modhost/load.go`, `modhost/notify.go`, `modhost/telemetry.go`, `modhost/notify_test.go`, `agent/module_notify.go`, `agent/module_notify_test.go`

**Interfaces — produces:**

```go
// groups
type DestinationsFunc func() ([]settings.Destination, error) // settings.DestinationsFile.Destinations
type Destinations struct{ List DestinationsFunc }
func (d Destinations) Resolve(name string) (transport, conv string, ok bool, err error)

// event.ModuleNotify gains
To string `json:"to,omitempty"` // the destination's name; "" is the owner's private chat

// modhost.Deps gains
Destinations DestinationFunc // func(name string) (transport, conv string, ok bool, err error); NoDestinations when unset
```

- [x] Tests first. Module host: with no `notify_to` one event goes to the owner's chat under the old key; with `[family-k7, owner]` two events go out, the first to the topic's conversation with `To: "family-k7"` and a key ending in `:to:family-k7`, the second as before; the daily count rises by one; a name that does not resolve sends nothing there, sends the rest, and is counted as `no_destination`; the limit line and the chain line go to the owner only; changing the line and reloading changes where the next message goes; a rerun of the same run puts nothing twice. Agent: an event with `To` set sends the text, signed, to the event's conversation, writes no note into that conversation's history and does not roll its session; an event for the owner is as before; abuse: an event with `To` set whose principal is not an admin, or whose source is not the node, sends nothing.
- [x] Implement.
- [x] Tests, lint, commit: `modhost: a module's message goes to every destination its line names`.

### Task 4: The admin's tools, and `message.send` by name

**Files:** `tool/builtin/destinations.go` (new), `tool/builtin/destinations_test.go` (new), `tool/builtin/message.go`, `tool/builtin/message_test.go`, `texts/texts.go`, `texts/lang/{en,ru,uk}.yaml`, `config/groups.go`, `config/agent.go`, `modhost/admin.go` (a `NotifyTo(ctx, module, names)` that writes the line and reloads)

**Tools** (all `AdminOnly`, refused in a task's run; the admin role exists only in the private chat):

| Tool | Policy | Does |
|---|---|---|
| `destination.list` | auto, external | Destinations; connected groups with the topics the node knows (titles wrapped as outside content); each module's `notify_to` |
| `destination.set {name, conv}` | confirm | Names a conversation: the caller's own, a connected group, or a known topic of one. The question shows the name and what the conversation is called |
| `destination.remove {name}` | confirm | Removes it; refused, naming the modules, while one uses it |
| `module.notify_to {module, destinations}` | confirm | Sets a module's list (names or `owner`) and reloads the module |
| `group.disconnect {group}` | confirm | Removes the group's line; refused, naming the destinations, while one points at it or its topics |

`message.send`'s `conv` also takes a destination's name.

- [x] Tests first, per tool: the happy path through the fake file; each refusal; the question's text with a title that holds Markdown; abuse: `destination.set` to a group that is not connected, to a topic the node has not seen, or to a stranger's private chat is refused; a title never appears in a result unwrapped.
- [x] Implement, with the texts in three languages and the wiring.
- [x] Tests, lint, code map, commit: `tools: the admin names destinations and routes a module's messages in words`.

### Task 5: End to end, and what readers are told

**Files:** `cmd/groups_test.go`, `skills/builtin/module-writing/SKILL.md`, `skills/skills_test.go`, `ganglion.example.yaml`, `docs/architecture/04-runtime.md`, `docs/architecture/05-code-map.md`, `docs/README.md`, `docs/plans/p1-all-in-one.md`, `tools/chatlab/no-scenario.txt`

- [x] A test with the node's binary (`cmd.TestAnAdminNamesATopicAndSendsThere`): a connected forum whose topic's title the node learns from a message that did not address the bot; a scripted model calls `destination.set`, the admin confirms, the file has the line; the model calls `message.send` to `family-k7` and the message lands in the topic. A module's fan-out is covered in `modhost` (`TestAMessageGoesToEveryDestinationOfTheLine` and its neighbours), not here: the test node runs no module.
- [x] The module-writing skill says that where messages go is the node's file, set with `module.notify_to`, never the module's code.
- [x] The example file, the runtime page's notify section and diagram, the code map, "Where we are".
- [x] `make lint`, `go test ./...`, commit, pull request.
- [ ] Live: in the private chat, "send the battery alerts to the K7 topic and to me"; one press; the next alert is in both places. Record it here.

## As built (2026-11-13)

All five tasks are done except the live check. What differs from the plan:

- **A topic the node has not seen can still be named.** `destination.set`
  takes any topic of a connected group; the question then names it by its
  number. A topic renamed before the node saw the rename is known by the
  title it was created with, since that is what its root carries; the
  admin can name it by number, or the next rename corrects the title.
- **`TopicFunc` carries `renamed`,** so a title from a rename is not
  replaced by the older one of the topic's root, also over a restart.
- **`destination.list` is not local-safe:** its result holds titles
  written by a group's members.
- **The texts about groups lost their Markdown.** A fixed text is shown as
  written, so the `**` and the backticks of the card of
  [the first plan](telegram-groups.md) were shown literally; they are
  quotation marks now.
- **Where a place is named in a question, it is a text of its own**
  (`ask.place_*`), so the question reads in the person's language.
