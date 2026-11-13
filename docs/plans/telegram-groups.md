# Telegram groups: access, the addressing rule and the connect card — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A Telegram group connected by the admin's press gives every member a conversation with the bot, which answers there only when it is addressed.

**Architecture:** The transport decides whether a group message addresses the bot and drops the rest before the sink. The access filter lets a group's message through only when the group is listed under `telegram.groups` in the node's file; an unlisted group produces one request event in the admin's private chat, which the agent turns into a card with Allow and Refuse quick-action buttons. Allow writes the group's line to the file, which is read on every check, so no restart is needed.

**Tech stack:** Go, the project's own Bot API client (`transport/telegram`), SQLite through `sqlitedb`, `go.yaml.in/yaml/v3` node editing in `settings`, OpenTelemetry metrics, the fake Bot API server `telegramtest`, chatlab.

**Design:** [telegram-groups-design.md](telegram-groups-design.md), sections 4–6, 9, 11, 12. Sections 7, 8 and 10 (named destinations, `NotifyTo`, managing groups and destinations in words) are a second plan, written after this one is under way: it builds on this plan's file writer and needs its own ADR.

## Global constraints

- Read [docs/conventions.md](../conventions.md) before writing code. Functions stay under 60 lines and cognitive complexity 20; no `nil` collaborators; closed sets are named string types; dependencies are named function types or small interfaces declared by the consumer; time comes from an injected clock.
- All documentation, comments and commit messages are in English.
- Only `config` builds and connects implementations; domain packages never import `samber/do`.
- A log line carries ids and kinds, never message text. A group's title is outside text: it is never logged at info level or above and never used as a metric label.
- Metric label values come from a closed set of constants.
- Every change that crosses a trust boundary comes with an abuse test (conventions, section 11).
- The spec changes before the code (Task 1). The code map changes in the commit that adds a package.
- Roles: only `family` is accepted as a group's role. Nothing in a group runs with the admin role.
- Commit after each task, on a feature branch, with messages in the repository's style (`area: what is now true`).
- Before a task is called done: `make lint` and `go test ./...` pass.

## File structure

| File | Responsibility |
|---|---|
| `docs/spec.md`, `docs/adr/0076-*.md`, `docs/adr/README.md`, `docs/plans/p1-all-in-one.md`, `docs/README.md` | The decision, the requirements, the phase |
| `settings/settings.go`, `settings/load.go` | `telegram.groups` in the file's schema and its checks |
| `settings/write.go` | `editDoc`: the file editor, no longer tied to `modules:` |
| `settings/write_telegram.go` (new) | `TelegramFile`: read, set and remove a group's line |
| `event/event.go`, `event/message.go` | Two kinds, the group fields, `GroupSeen`, `GroupRequest` |
| `transport/telegram/addressed.go` (new) | The pure addressing rule |
| `transport/telegram/types.go`, `transport.go`, `buttons.go`, `conv.go`, `client.go` | Reading titles, entities and `my_chat_member`; dropping what is not addressed |
| `transport/telegram/joined.go` (new) | The "bot was added" update as an event |
| `transport/telegram/telegramtest/server.go` | The fake: a username, titles, `AddChatMember` |
| `groups/` (new package) | `Store` of requests, `Connected`, `Requests`, the quick actions |
| `access/access.go` | The group rule of the filter |
| `agent/stages.go`, `agent/group_request.go` (new), `agent/agent.go`, `agent/resume.go`, `agent/turn.go` | The admin role only in a private chat; the card and the notice |
| `texts/texts.go`, `texts/lang/{en,ru,uk}.yaml` | Six fixed texts |
| `config/groups.go` (new), `config/delivery.go`, `config/agent.go`, `config/config.go` | Wiring |
| `ganglion.example.yaml`, `docs/architecture/05-code-map.md`, `docs/architecture/` delivery page | The example and the views |

---

### Task 1: The decision in the spec and an ADR

**Files:**
- Create: `docs/adr/0076-telegram-groups-and-the-addressing-rule.md` (copy `docs/adr/template.md`)
- Modify: `docs/adr/README.md`, `docs/spec.md`, `docs/plans/p1-all-in-one.md`, `docs/README.md`

**Interfaces:**
- Produces: requirement ids that later tasks cite in doc comments. `R-DLV-15` is the last delivery requirement today; check with `grep -n "R-DLV-1[5-9]" docs/spec.md` and take the next free numbers. This plan calls them **R-DLV-16** (the addressing rule), **R-DLV-17** (connecting a group) and **R-POL-10** (no admin role in a group; take the next free `R-POL` number and use it wherever this plan says R-POL-10).

- [x] **Step 1: Write ADR-0076.** Status Proposed until the owner accepts it. Context: design sections 1 and 3. Options compared, at least these three:
  - A. A group is listed in the node's file and gives its members a role; the transport drops what does not address the bot; the admin connects a group by a press on a card in the private chat (chosen).
  - B. Every person is listed one by one and a group needs both the group and the person listed. Rejected: nothing adds a person today, so the family chat would answer the owner only.
  - C. Trust Telegram's privacy mode for addressing and keep the per-person list. Rejected: a bot made a group's admin receives every message, and the owner's ruling of 2026-11-06 says "whatever Telegram's privacy mode says".
  Consequences: an allowed person is no longer answered in a group that is not connected; the admin has no admin tools in any group; every member of a connected group can spend the node's model budget, bounded by the existing daily limits; `family` has no tools today, so a group is conversation only until roles become configurable.

- [x] **Step 2: Add the ADR's row** to `docs/adr/README.md`.

- [x] **Step 3: Change the spec.**
  - Add R-DLV-16: in a group the bot is addressed by a mention of its username, a reply to its own message, or a command that carries its name; anything else is dropped before the inbox, whatever the privacy mode.
  - Add R-DLV-17: a group not listed under `telegram.groups` is not answered; the first time the bot is addressed in it or added to it, the node says so once in the group and sends the admin a card with the group's title and id and the buttons Allow and Refuse; Allow lists the group with the role `family`; Refuse is remembered; at most 10 requests wait at once.
  - Add R-POL-10: the admin role holds only in the admin's private chat; in a group everyone acts with the group's role.
  - In the 7.8 security table add the row for `telegram.groups`.
  - In section 14 "Not now": remove "where a bot answers" (the 2026-11-06 ruling, now R-DLV-16) and "limiting Telegram … groups"; keep "per-group behaviour and limits", per-topic settings, reactions in groups and the classifier; add "roles a group's line may name beyond `family`, with their own tools and filters (owner, 2026-11-13); the `role` field of a group's line is where they plug in"; keep "`notify` targets beyond the owner's private chat" with a pointer to the second plan.

- [x] **Step 4: Add the milestone.** In `docs/plans/p1-all-in-one.md` add a row to the milestone table and a task table "Telegram groups" before M6, with this plan's tasks 2–9 as rows and a link to this file. In `docs/README.md` "Where we are", name it as the work in progress and note that the production token moved to ganglion on 2026-11-13, ahead of task 7.5.

- [x] **Step 5: Commit.**

```bash
git add docs/adr/0076-telegram-groups-and-the-addressing-rule.md docs/adr/README.md docs/spec.md docs/plans/p1-all-in-one.md docs/README.md docs/plans/telegram-groups-design.md docs/plans/telegram-groups.md
git commit -m "spec: a Telegram group is connected by the admin and answered only when addressed (ADR-0076)"
```

---

### Task 2: `telegram.groups` in the node's file

**Files:**
- Modify: `settings/settings.go` (the `Telegram` struct, about line 60), `settings/load.go` (the checks), `settings/write.go:160-232` (`edit`, `modulesNode`)
- Create: `settings/write_telegram.go`, `settings/write_telegram_test.go`
- Test: also `settings/load_test.go`, `settings/schema_test.go` if it lists fields

**Interfaces:**
- Produces:

```go
// settings.Group is one line of telegram.groups.
type Group struct {
	Chat int64  `yaml:"chat"` // Telegram's chat id, negative
	Name string `yaml:"name"` // for people: the title when it was connected
	Role string `yaml:"role"` // GroupRoleFamily
}

const GroupRoleFamily = "family"

type TelegramFile struct{ Path string }

func (f TelegramFile) Groups() ([]Group, error)
func (f TelegramFile) SetGroup(g Group) error
func (f TelegramFile) RemoveGroup(chat int64) error
```

- [x] **Step 1: Write the failing tests** in `settings/write_telegram_test.go`.

```go
package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetGroupAddsALineAndKeepsTheRest(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	src := "# the node\ntelegram:\n  reply: auto # quoted in groups\nmodules:\n  - name: local/deye\n"
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	f := TelegramFile{Path: path}
	if err := f.SetGroup(Group{Chat: -1001, Name: "Family", Role: GroupRoleFamily}); err != nil {
		t.Fatal(err)
	}
	got, err := f.Groups()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != (Group{Chat: -1001, Name: "Family", Role: GroupRoleFamily}) {
		t.Fatalf("groups = %+v", got)
	}
	out, _ := os.ReadFile(path)
	for _, want := range []string{"# the node", "# quoted in groups", "name: local/deye"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("the file lost %q:\n%s", want, out)
		}
	}
}

func TestSetGroupReplacesTheLineOfTheSameChat(t *testing.T) {
	f := TelegramFile{Path: filepath.Join(t.TempDir(), FileName)}
	for _, name := range []string{"Old", "New"} {
		if err := f.SetGroup(Group{Chat: -1001, Name: name, Role: GroupRoleFamily}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := f.Groups()
	if err != nil || len(got) != 1 || got[0].Name != "New" {
		t.Fatalf("groups = %+v, err = %v", got, err)
	}
}

func TestRemoveGroup(t *testing.T) {
	f := TelegramFile{Path: filepath.Join(t.TempDir(), FileName)}
	_ = f.SetGroup(Group{Chat: -1001, Name: "A", Role: GroupRoleFamily})
	_ = f.SetGroup(Group{Chat: -1002, Name: "B", Role: GroupRoleFamily})
	if err := f.RemoveGroup(-1001); err != nil {
		t.Fatal(err)
	}
	if err := f.RemoveGroup(-1001); err != nil {
		t.Fatalf("removing a group that is not listed: %v", err)
	}
	got, _ := f.Groups()
	if len(got) != 1 || got[0].Chat != -1002 {
		t.Fatalf("groups = %+v", got)
	}
}

func TestSetGroupRefusesABadLine(t *testing.T) {
	f := TelegramFile{Path: filepath.Join(t.TempDir(), FileName)}
	for _, g := range []Group{
		{Chat: 42, Name: "a private chat", Role: GroupRoleFamily},
		{Chat: -1001, Name: "x", Role: "admin"},
		{Chat: -1001, Name: "x", Role: ""},
	} {
		if err := f.SetGroup(g); err == nil {
			t.Errorf("SetGroup(%+v) = nil, want an error", g)
		}
	}
}
```

In `settings/load_test.go` add a table test, in the style of its neighbours, that loading refuses: `chat: 42`, `role: admin`, a missing `role`, and two lines with the same `chat`.

- [x] **Step 2: Run them and see them fail.**

Run: `go test ./settings/ -run 'Group' -v`
Expected: FAIL to compile, `undefined: TelegramFile`.

- [x] **Step 3: Add the type and the check.** In `settings/settings.go`, in `Telegram`, after `Reactions`:

```go
	// Groups are the group chats whose members may talk to the bot
	// (R-DLV-17). The node writes a line when the admin allows a group.
	Groups []Group `yaml:"groups"`
```

and beside `Module`:

```go
// GroupRoleFamily is the one role a group's line may name: allowed and not
// an admin. More roles are the spec's "Not now".
const GroupRoleFamily = "family"

// Group is one line of telegram.groups.
type Group struct {
	// Chat is Telegram's id of the group, a negative number.
	Chat int64 `yaml:"chat"`
	// Name is for people: the group's title when it was connected.
	Name string `yaml:"name"`
	// Role is what the group's members may do: family.
	Role string `yaml:"role"`
}

// check says what is wrong with the line; nil when nothing is.
func (g Group) check() error {
	if g.Chat >= 0 {
		return errors.New("chat must be a group's id, a negative number")
	}
	if g.Role != GroupRoleFamily {
		return errors.New("role must be family")
	}
	return nil
}
```

In `settings/load.go`, call `checkGroups(f.Telegram.Groups)` from `File.check()` (line 240) and add it in the same shape as the neighbouring checks (same error prefix, same way of naming the field):

```go
// checkGroups refuses a bad line of telegram.groups and a chat listed twice.
func checkGroups(gs []Group) error {
	seen := map[int64]bool{}
	for i, g := range gs {
		if err := g.check(); err != nil {
			return fmt.Errorf("%s: telegram.groups[%d]: %w", FileName, i, err)
		}
		if seen[g.Chat] {
			return fmt.Errorf("%s: telegram.groups[%d]: chat %d is listed twice", FileName, i, g.Chat)
		}
		seen[g.Chat] = true
	}
	return nil
}
```

- [x] **Step 4: Free the editor from `modules:`.** In `settings/write.go` split `edit` so that the part that reads, encodes and replaces the file takes the document:

```go
// editDoc reads the file, hands the document to change and writes the
// result back. A missing file is an empty one.
func editDoc(path string, change func(doc *yaml.Node) error) error {
	writeMu.Lock()
	defer writeMu.Unlock()
	src, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("settings: %w", err)
	}
	var doc yaml.Node
	if len(bytes.TrimSpace(src)) > 0 {
		if err := yaml.Unmarshal(src, &doc); err != nil {
			return fmt.Errorf("%s: %s", FileName, syntaxReason(err))
		}
	}
	if err := change(&doc); err != nil {
		return err
	}
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return fmt.Errorf("settings: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("settings: %w", err)
	}
	return replaceFile(path, out.Bytes())
}

// edit is editDoc on the modules: sequence.
func edit(path string, change func(seq *yaml.Node) error) error {
	return editDoc(path, func(doc *yaml.Node) error {
		seq, err := modulesNode(doc)
		if err != nil {
			return err
		}
		return change(seq)
	})
}
```

Leave `modulesNode` as it is.

- [x] **Step 5: Write `settings/write_telegram.go`.**

```go
package settings

import (
	"fmt"
	"os"
	"strconv"

	"go.yaml.in/yaml/v3"
)

// TelegramFile is the telegram: section of the node's file as it is now:
// it reads the file on every call, so a group the admin allowed a moment
// ago is seen at once (R-DLV-17).
type TelegramFile struct {
	Path string
}

// Groups reads the file and returns telegram.groups; a missing file has
// none. The whole file is checked, as at start.
func (f TelegramFile) Groups() ([]Group, error) {
	file, err := LoadOptional(f.Path, os.ReadFile)
	if err != nil {
		return nil, err
	}
	return file.Telegram.Groups, nil
}

// SetGroup writes g as the line of g.Chat under telegram.groups, adding it
// at the end when it is missing. The rest of the file keeps its comments
// and order.
func (f TelegramFile) SetGroup(g Group) error {
	if err := g.check(); err != nil {
		return fmt.Errorf("settings: group %d: %w", g.Chat, err)
	}
	return editDoc(f.Path, func(doc *yaml.Node) error {
		seq, err := groupsNode(doc)
		if err != nil {
			return err
		}
		item := groupLine(seq, g.Chat)
		if item == nil {
			item = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			seq.Content = append(seq.Content, item)
		}
		setScalar(item, "chat", strconv.FormatInt(g.Chat, 10), "!!int")
		setScalar(item, "name", g.Name, "!!str")
		setScalar(item, "role", g.Role, "!!str")
		return nil
	})
}

// RemoveGroup drops the line of chat from telegram.groups; a chat that is
// not listed is not an error.
func (f TelegramFile) RemoveGroup(chat int64) error {
	return editDoc(f.Path, func(doc *yaml.Node) error {
		seq, err := groupsNode(doc)
		if err != nil {
			return err
		}
		for i, item := range seq.Content {
			if item == groupLine(seq, chat) {
				seq.Content = append(seq.Content[:i], seq.Content[i+1:]...)
				return nil
			}
		}
		return nil
	})
}

// groupsNode finds telegram.groups, creating the document, the mapping
// and the keys as needed.
func groupsNode(doc *yaml.Node) (*yaml.Node, error) {
	if doc.Kind == 0 {
		doc.Kind = yaml.DocumentNode
		doc.Content = []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: the top level is not a mapping", FileName)
	}
	tg, err := child(root, "telegram", yaml.MappingNode, "!!map")
	if err != nil {
		return nil, err
	}
	seq, err := child(tg, "groups", yaml.SequenceNode, "!!seq")
	if err != nil {
		return nil, err
	}
	seq.Style &^= yaml.FlowStyle
	return seq, nil
}

// child is the value of key in mapping m, added empty when it is missing
// or null ("groups:" with every line commented out).
func child(m *yaml.Node, key string, kind yaml.Kind, tag string) (*yaml.Node, error) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value != key {
			continue
		}
		v := m.Content[i+1]
		if v.Kind == yaml.ScalarNode && v.Tag == "!!null" {
			*v = yaml.Node{Kind: kind, Tag: tag, HeadComment: v.HeadComment, LineComment: v.LineComment}
		}
		if v.Kind != kind {
			return nil, fmt.Errorf("%s: %s has the wrong shape", FileName, key)
		}
		return v, nil
	}
	v := &yaml.Node{Kind: kind, Tag: tag}
	m.Content = append(m.Content, plain(key), v)
	return v, nil
}

// groupLine is the line of chat in the sequence; nil when there is none.
func groupLine(seq *yaml.Node, chat int64) *yaml.Node {
	want := strconv.FormatInt(chat, 10)
	for _, item := range seq.Content {
		if v, _ := field(item, "chat"); v != nil && v.Value == want {
			return item
		}
	}
	return nil
}

// setScalar sets key of the mapping item to value, adding the key when it
// is missing.
func setScalar(item *yaml.Node, key, value, tag string) {
	if v, _ := field(item, key); v != nil {
		v.Kind, v.Tag, v.Value, v.Style = yaml.ScalarNode, tag, value, 0
		return
	}
	item.Content = append(item.Content, plain(key), &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value})
}
```

`field` and `plain` already exist in `settings/write.go`. If `field` returns its second value in another meaning than "index", keep the calls and ignore it as above.

- [x] **Step 6: Run the tests.**

Run: `go test ./settings/ -v`
Expected: PASS, the old module-writing tests included.

- [x] **Step 7: Commit.**

```bash
git add settings/
git commit -m "settings: telegram.groups lists the groups whose members may talk to the bot"
```

---

### Task 3: The addressing rule, as a pure function

**Files:**
- Create: `transport/telegram/addressed.go`, `transport/telegram/addressed_test.go`
- Modify: `transport/telegram/types.go` (the `message`, `chat` structs)

**Interfaces:**
- Produces:

```go
// in types.go
type inEntity struct {
	Type   string `json:"type"`
	Offset int    `json:"offset"` // in UTF-16 code units
	Length int    `json:"length"`
	User   *user  `json:"user,omitempty"` // of a text_mention
}
// message gains: Entities, CaptionEntities []inEntity
// chat gains:    Title string

func isGroup(c chat) bool
func addressed(m *message, botID int64, username string) bool
```

- [x] **Step 1: Write the failing test** `transport/telegram/addressed_test.go`.

```go
package telegram

import "testing"

func TestAddressed(t *testing.T) {
	const bot, name = int64(7), "family_bot"
	mention := func(text string, off, n int) *message {
		return &message{Text: text, Entities: []inEntity{{Type: "mention", Offset: off, Length: n}}}
	}
	tests := []struct {
		name string
		m    *message
		want bool
	}{
		{"plain text", &message{Text: "what a day"}, false},
		{"a mention of the bot", mention("@family_bot hi", 0, 11), true},
		{"the mention in another case", mention("@Family_Bot hi", 0, 11), true},
		{"a mention after an emoji counts UTF-16 units", mention("😀 @family_bot", 3, 11), true},
		{"a mention of someone else", mention("@family_bot2 hi", 0, 12), false},
		{"the bot's name typed with no entity", &message{Text: "@family_bot hi"}, false},
		{"a text mention of the bot", &message{Text: "Bot hi", Entities: []inEntity{{Type: "text_mention", Offset: 0, Length: 3, User: &user{ID: bot}}}}, true},
		{"a text mention of a person", &message{Text: "Ann hi", Entities: []inEntity{{Type: "text_mention", Offset: 0, Length: 3, User: &user{ID: 9}}}}, false},
		{"a command with the bot's name", &message{Text: "/new@family_bot", Entities: []inEntity{{Type: "bot_command", Offset: 0, Length: 15}}}, true},
		{"a bare command", &message{Text: "/new", Entities: []inEntity{{Type: "bot_command", Offset: 0, Length: 4}}}, false},
		{"a command for another bot", &message{Text: "/new@other_bot", Entities: []inEntity{{Type: "bot_command", Offset: 0, Length: 14}}}, false},
		{"a mention in a caption", &message{Caption: "@family_bot look", CaptionEntities: []inEntity{{Type: "mention", Offset: 0, Length: 11}}}, true},
		{"a reply to the bot", &message{Text: "and tomorrow?", ReplyToMessage: &message{MessageID: 5, From: &user{ID: bot, IsBot: true}}}, true},
		{"a reply to a person", &message{Text: "yes", ReplyToMessage: &message{MessageID: 5, From: &user{ID: 9}}}, false},
		{"an entity past the end of the text", mention("hi", 5, 11), false},
		{"an entity with a negative offset", mention("@family_bot", -1, 11), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := addressed(tt.m, bot, name); got != tt.want {
				t.Fatalf("addressed = %v, want %v", got, tt.want)
			}
		})
	}
}

// A topic the bot itself created: every message in it names the bot's
// service message as the replied one. That is no reply to the bot.
func TestTopicRootByTheBotIsNotAddressing(t *testing.T) {
	m := &message{Text: "hello all", IsTopicMessage: true, MessageThreadID: 45,
		ReplyToMessage: &message{MessageID: 45, From: &user{ID: 7, IsBot: true}, ForumTopicCreated: []byte(`{"name":"K7"}`)}}
	if addressed(m, 7, "family_bot") {
		t.Fatal("the topic's root counted as a reply to the bot")
	}
}

func TestIsGroup(t *testing.T) {
	for typ, want := range map[string]bool{"private": false, "group": true, "supergroup": true, "channel": false, "": false} {
		if got := isGroup(chat{Type: typ}); got != want {
			t.Errorf("isGroup(%q) = %v, want %v", typ, got, want)
		}
	}
}
```

- [x] **Step 2: Run it and see it fail.**

Run: `go test ./transport/telegram/ -run 'Addressed|IsGroup|TopicRootByTheBot' -v`
Expected: FAIL to compile, `undefined: inEntity`.

- [x] **Step 3: Add the fields.** In `types.go`, in `message` after `Caption`:

```go
	// Entities and CaptionEntities mark the parts of Text and Caption; of
	// them only what addresses the bot is read (R-DLV-16).
	Entities        []inEntity `json:"entities,omitempty"`
	CaptionEntities []inEntity `json:"caption_entities,omitempty"`
```

in `chat` after `Type`:

```go
	// Title is a group's name: outside text.
	Title string `json:"title,omitempty"`
```

and the new type:

```go
// inEntity is a Bot API MessageEntity of an incoming message, reduced to
// what the addressing rule reads. Offset and Length count UTF-16 code units.
type inEntity struct {
	Type   string `json:"type"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
	User   *user  `json:"user,omitempty"` // of a text_mention
}
```

- [x] **Step 4: Write `addressed.go`.**

```go
package telegram

import (
	"strings"
	"unicode/utf16"
)

// Bot API values read by the addressing rule.
const (
	chatGroup      = "group"
	chatSupergroup = "supergroup"

	entityMention     = "mention"      // @username
	entityTextMention = "text_mention" // a name that links to a user with no username
	entityBotCommand  = "bot_command"  // /cmd or /cmd@username
)

// isGroup reports whether c is a group or a supergroup, a forum included.
func isGroup(c chat) bool { return c.Type == chatGroup || c.Type == chatSupergroup }

// addressed reports whether m, a message in a group, addresses the bot
// (R-DLV-16): it mentions the bot, replies to a message the bot sent, or
// is a command that carries the bot's name. A bare command does not count:
// in a group with several bots it goes to all of them. The bot's name
// typed with no entity does not count either: Telegram marks a real
// mention, and a rule that reads the text could be fooled by a quote.
func addressed(m *message, botID int64, username string) bool {
	if r := m.ReplyToMessage; r != nil && r.From != nil && r.From.ID == botID && !topicRoot(m, r) {
		return true
	}
	text, entities := m.Text, m.Entities
	if text == "" {
		text, entities = m.Caption, m.CaptionEntities
	}
	units := utf16.Encode([]rune(text))
	for _, e := range entities {
		switch e.Type {
		case entityTextMention:
			if e.User != nil && e.User.ID == botID {
				return true
			}
		case entityMention:
			if username != "" && strings.EqualFold(part(units, e), "@"+username) {
				return true
			}
		case entityBotCommand:
			if _, to, ok := strings.Cut(part(units, e), "@"); ok && username != "" && strings.EqualFold(to, username) {
				return true
			}
		}
	}
	return false
}

// part is the text an entity marks; "" for an entity outside the text.
func part(units []uint16, e inEntity) string {
	if e.Offset < 0 || e.Length <= 0 || e.Offset > len(units)-e.Length {
		return ""
	}
	return string(utf16.Decode(units[e.Offset : e.Offset+e.Length]))
}
```

`seen()` in `transport.go` compares the chat type to the literals `"group"` and `"supergroup"`: replace that condition with `isGroup(m.Chat)`.

- [x] **Step 5: Run the tests.**

Run: `go test ./transport/telegram/ -v`
Expected: PASS.

- [x] **Step 6: Commit.**

```bash
git add transport/telegram/
git commit -m "telegram: a rule for whether a group message addresses the bot (R-DLV-16)"
```

---

### Task 4: The event carries its group; the transport drops what is not addressed

**Files:**
- Modify: `event/message.go`, `transport/telegram/conv.go`, `transport/telegram/transport.go` (`Run`, `hand`, `toEvent`, `messageEvent`), `transport/telegram/buttons.go` (`decisionEvent`), `transport/telegram/telegramtest/server.go`
- Create: `transport/telegram/groupcount.go`
- Test: `transport/telegram/transport_test.go`, `transport/telegram/buttons_test.go`, `transport/telegram/telegramtest/server_test.go`

**Interfaces:**
- Consumes: `addressed`, `isGroup` (Task 3).
- Produces:

```go
// event.Message gains
Group      string `json:"group,omitempty"`       // the channel's key of the group chat; "" in a private chat
GroupTitle string `json:"group_title,omitempty"` // the group's title: outside text
// event.Decision gains
Group string `json:"group,omitempty"`

// telegram
func GroupKey(chatID int64) string // ConvKey(chatID, 0): "telegram:<chat_id>"

// telegramtest
const BotUsername = "test_bot"
// Incoming gains: Title string
func Mention(text string) map[string]any // the "entities" of text for Incoming.Extra: every "@test_bot" in it
```

- [x] **Step 1: Extend the fake server.** In `telegramtest/server.go`:
  - export `const BotUsername = "test_bot"` and return it as `"username"` from the `getMe` handler (find the handler with `grep -n '"getMe"' transport/telegram/telegramtest/server.go`);
  - add `Title string` to `Incoming` and put `"title": m.Title` into the `"chat"` map of `AddMessage` when the chat is a group and the title is not empty;
  - add the helper:

```go
// Mention is the Bot API entities of text that mark every "@test_bot" in
// it, for Incoming.Extra: {"entities": [...]}. Offsets count UTF-16 units.
func Mention(text string) map[string]any {
	const at = "@" + BotUsername
	var out []map[string]any
	for from := 0; ; {
		i := strings.Index(text[from:], at)
		if i < 0 {
			break
		}
		off := len(utf16.Encode([]rune(text[:from+i])))
		out = append(out, map[string]any{"type": "mention", "offset": off, "length": len(at)})
		from += i + len(at)
	}
	return map[string]any{"entities": out}
}
```

  Add a test for `Mention` in `server_test.go`: `Mention("😀 @test_bot hi")` gives one entity with offset 3 and length 9.

- [x] **Step 2: Write the failing transport tests.** Open `transport_test.go` at `TestTopicMessageGetsTopicConv` (line 161) and use the same way of starting the transport and collecting events. Add:

```go
// A group message that does not address the bot never becomes an event,
// and its update is still confirmed (R-DLV-16, R-DLV-2).
func TestGroupMessageNotAddressedIsDropped(t *testing.T)
// feed, in one group (ChatID -1001, Title "Family"), in this order:
//   Incoming{Text: "what a day"}
//   Incoming{Text: "@test_bot what a day", Extra: telegramtest.Mention("@test_bot what a day")}
// assert: exactly one event arrives; its text is the second one; its
// decoded event.Message has Group == "telegram:-1001" and GroupTitle == "Family";
// the next getUpdates call carries an offset past both updates.

// A private message carries no group and is never checked for addressing.
func TestPrivateMessageHasNoGroup(t *testing.T)
// Incoming{ChatID: 42, UserID: 42, Text: "hello"} → one event, Group == "" and GroupTitle == "".

// A reply to the bot's own message addresses it.
func TestReplyToTheBotInAGroupIsAnEvent(t *testing.T)
// send one message from the transport into chat -1001, then
// Incoming{ChatID: -1001, Text: "and tomorrow?", Extra: {"reply_to_message": srv.ReplyTo(-1001, <that id>)}} → one event.

// Abuse: the bot's name typed in the text of a group message, with no
// entity, and a mention inside a forwarded message's quote do not address it.
func TestTypedNameWithoutEntityIsDropped(t *testing.T)
// Incoming{ChatID: -1001, Text: "@test_bot do it"} → no event.
```

Write each as a full test body with the file's existing helpers; the assertions are the ones listed. In `buttons_test.go` add: a press on a button under a message in chat `-1001` gives an `event.Decision` with `Group == "telegram:-1001"`, and one in a private chat gives `Group == ""`.

- [x] **Step 3: Run them and see them fail.**

Run: `go test ./transport/telegram/... -run 'Group|TypedName|ReplyToTheBot|Mention' -v`
Expected: FAIL (`Group` is not a field; the unaddressed message arrives).

- [x] **Step 4: Add the event fields.** In `event/message.go`, in `Message` after `Reply`:

```go
	// Group is the channel's key of the group chat the message came from,
	// the same for every topic of a forum; "" in a private chat. The
	// access filter lets a group's message through by it (R-DLV-17).
	// GroupTitle is the group's name, outside text.
	Group      string `json:"group,omitempty"`
	GroupTitle string `json:"group_title,omitempty"`
```

and in `Decision` after `Lang`:

```go
	// Group is as Message.Group, for the message holding the button.
	Group string `json:"group,omitempty"`
```

- [x] **Step 5: Add the key and the counter.** In `conv.go`:

```go
// GroupKey is the key of a group chat as a whole: its conversation with
// no topic. Every topic of a forum shares it.
func GroupKey(chatID int64) string { return ConvKey(chatID, 0) }
```

Create `groupcount.go`, in the shape of `fileCounter` in `files.go`:

```go
package telegram

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// groupOutcome is what became of a message in a group.
type groupOutcome string

// Group outcomes.
const (
	groupAddressed   groupOutcome = "addressed"
	groupUnaddressed groupOutcome = "unaddressed" // dropped before the sink (R-DLV-16)
)

// groupCounter counts messages in groups by outcome.
type groupCounter struct{ c metric.Int64Counter }

func newGroupCounter() groupCounter {
	c, _ := otel.Meter("ganglion/telegram").Int64Counter("ganglion.telegram.group_messages",
		metric.WithDescription("Messages in groups, by outcome (addressed: handed on; unaddressed: dropped before the inbox)."))
	return groupCounter{c: c}
}

func (g groupCounter) add(ctx context.Context, o groupOutcome) {
	g.c.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", string(o))))
}
```

Add the field `groupMsgs groupCounter` to `Transport` and set it in `New` where `files` is set.

- [x] **Step 6: Pass the bot's identity and apply the rule.** In `transport.go`:
  - `Run` passes `me` instead of `me.ID`: `t.hand(ctx, sink, me, ups, offset, sinkRetry)`;
  - `hand`, `toEvent`, `messageEvent` and `decisionEvent` take `me user` instead of `botID int64`; inside them use `me.ID` where `botID` was used;
  - in `messageEvent`, right after the "no text and no file" check and before `t.files.add`:

```go
	group := isGroup(m.Chat)
	if group && !addressed(m, me.ID, me.Username) {
		t.groupMsgs.add(context.Background(), groupUnaddressed)
		t.log.Debug("telegram: update skipped", "update_id", u.UpdateID, "reason", "a group message that does not address the bot")
		return event.Event{}, false
	}
```

  - after `msg` is built:

```go
	if group {
		t.groupMsgs.add(context.Background(), groupAddressed)
		msg.Group, msg.GroupTitle = GroupKey(m.Chat.ID), m.Chat.Title
	}
```

  If `messageEvent` passes 60 lines, move the building of `msg` into `func (t *Transport) incoming(me user, m *message, text string, fs []event.File) event.Message`.
  - in `buttons.go`, `decisionEvent`: build the `event.Decision` into a variable, set `d.Group = GroupKey(m.Chat.ID)` when `isGroup(m.Chat)`, then marshal it.

- [x] **Step 7: Run everything.**

Run: `go test ./transport/... ./event/... ./agent/... ./internal/chatlab/... ./cmd/...`
Expected: the new tests PASS. Tests that send a plain message into a negative chat id now see no event: fix each by adding `Extra: telegramtest.Mention(text)` with `@test_bot` in the text when the test is about a group conversation, or by moving it to a private chat when the group was incidental. Do not weaken the rule to make a test pass.

- [x] **Step 8: Add the metric's readback test** beside the existing telemetry tests of the package (`grep -ln "ganglion.files.received" transport/telegram/*_test.go` shows the pattern): after one addressed and one unaddressed group message, the Prometheus text has `ganglion_telegram_group_messages_total{outcome="addressed"} 1` and `{outcome="unaddressed"} 1`.

- [x] **Step 9: Commit.**

```bash
git add event/ transport/
git commit -m "telegram: a group message that does not address the bot is dropped before the inbox"
```

---

### Task 5: "The bot was added to a group" as an event

**Files:**
- Modify: `event/event.go`, `event/message.go`, `event/kind_test.go`, `transport/telegram/types.go`, `transport/telegram/client.go:209-218`, `transport/telegram/transport.go` (`toEvent`), `transport/telegram/telegramtest/server.go`
- Create: `transport/telegram/joined.go`, `transport/telegram/joined_test.go`

**Interfaces:**
- Produces:

```go
// event
const KindGroupJoined Kind = "group.joined"   // payload GroupSeen; the access filter never lets it through
const KindGroupRequest Kind = "group.request" // payload GroupRequest; the node's, in an admin's private chat
const SourceGroups = "groups"                 // beside SourceModule

type GroupSeen struct {
	Group string `json:"group"`           // the group's key
	Conv  string `json:"conv"`            // where it happened: a topic's conversation, or the group's
	Title string `json:"title,omitempty"` // outside text
	From  string `json:"from"`            // principal who addressed or added the bot
	Name  string `json:"name,omitempty"`  // their display name: outside text
}

type GroupRequest struct {
	GroupSeen
	Principal string `json:"principal"` // the admin asked
	Transport string `json:"transport"`
}

// telegramtest
func (s *Server) AddChatMember(chatID int64, title string, byUserID int64, status string) int64
func (s *Server) AllowedUpdates() []string // exists; now also holds "my_chat_member"
```

- [x] **Step 1: Write the failing tests** in `joined_test.go`, with the helpers of `transport_test.go`:

```go
// The bot added to a group becomes a group.joined event in the group's
// conversation, naming who added it (R-DLV-17).
func TestBotAddedToAGroupIsAnEvent(t *testing.T)
// srv.AddChatMember(-1001, "Family", 42, "member") → one event with
// Kind == event.KindGroupJoined, Conv == "telegram:-1001", and a payload
// GroupSeen{Group: "telegram:-1001", Conv: "telegram:-1001", Title: "Family", From: "telegram:user:42"}.

// Being removed, or another member's change, is no event.
func TestOtherMemberChangesAreSkipped(t *testing.T)
// AddChatMember(-1001, "Family", 42, "left") and (…, "kicked") → no event; the updates are confirmed.

// Polling asks for my_chat_member.
func TestPollingAsksForMyChatMember(t *testing.T)
// after one poll, srv.AllowedUpdates() contains "message", "callback_query" and "my_chat_member".
```

- [x] **Step 2: Run them and see them fail.**

Run: `go test ./transport/telegram/ -run 'BotAdded|OtherMember|MyChatMember' -v`
Expected: FAIL to compile, `undefined: AddChatMember`.

- [x] **Step 3: Add the kinds and payloads.** In `event/event.go`, after `KindMemoryReview`:

```go
	// KindGroupJoined says the node's bot was added to a group chat; its
	// payload is a GroupSeen. The access filter takes it and never passes
	// it on: a group that is not connected becomes a request (R-DLV-17).
	KindGroupJoined Kind = "group.joined"
	// KindGroupRequest asks an admin, in their private chat, whether a
	// group that is not connected may talk to the bot; its payload is a
	// GroupRequest and its source is SourceGroups (R-DLV-17).
	KindGroupRequest Kind = "group.request"
```

Add `SourceGroups = "groups"` beside `SourceModule`, with the comment "the source of the node's own events about group chats". Add the two payload types from the Interfaces block to `event/message.go`, with doc comments that say Title and Name are outside text. Update `event/kind_test.go` so its list of kinds holds the two new ones.

- [x] **Step 4: Read the update.** In `types.go`:

```go
// chatMember is a Bot API ChatMemberUpdated about the bot itself, reduced
// to what says the bot was added.
type chatMember struct {
	Chat          chat `json:"chat"`
	From          user `json:"from"`
	NewChatMember struct {
		Status string `json:"status"`
		User   user   `json:"user"`
	} `json:"new_chat_member"`
}
```

Add `MyChatMember *chatMember `json:"my_chat_member,omitempty"`` to `update`, and change its comment to "messages, button presses and the bot's own membership are requested". In `client.go`, `getUpdates`, the allowed updates become `[]string{"message", "callback_query", "my_chat_member"}`.

- [x] **Step 5: Write `joined.go`.**

```go
package telegram

import (
	"encoding/json"
	"strings"

	"github.com/igorrius/ganglion/event"
)

// Statuses of a chat member that mean the bot is in the chat.
const (
	statusMember = "member"
	statusAdmin  = "administrator"
)

// joinedEvent turns "the bot was added to a group" into a group.joined
// event in the group's conversation (R-DLV-17). Leaving, being removed and
// a change in a chat that is no group are skipped.
func (t *Transport) joinedEvent(me user, u update) (event.Event, bool) {
	c := u.MyChatMember
	in := c.NewChatMember.Status == statusMember || c.NewChatMember.Status == statusAdmin
	if !isGroup(c.Chat) || !in || c.NewChatMember.User.ID != me.ID || c.From.IsBot {
		t.log.Debug("telegram: update skipped", "update_id", u.UpdateID, "reason", "not the bot joining a group")
		return event.Event{}, false
	}
	key := GroupKey(c.Chat.ID)
	payload, err := json.Marshal(event.GroupSeen{Group: key, Conv: key, Title: c.Chat.Title,
		From: Principal(c.From.ID), Name: strings.TrimSpace(c.From.FirstName + " " + c.From.LastName)})
	if err != nil {
		return event.Event{}, false // a struct of strings always marshals
	}
	now := t.now()
	return event.Event{ID: event.NewID(now), IdemKey: idemKey(me.ID, u.UpdateID), Source: Name, Conv: key,
		Kind: event.KindGroupJoined, Payload: payload, At: now}, true
}
```

In `toEvent`, before the message case:

```go
	if u.MyChatMember != nil {
		return t.joinedEvent(me, u)
	}
```

- [x] **Step 6: Extend the fake server.** `AddChatMember` queues `{"update_id": n, "my_chat_member": {"chat": {"id", "type": "supergroup", "title"}, "from": sender(byUserID, false, "User", ""), "date": now, "new_chat_member": {"status": status, "user": sender(s.botID, true, "Test", "")}}}` the way `AddMessage` queues an update, and returns the update id.

- [x] **Step 7: Run the tests.**

Run: `go test ./transport/... ./event/...`
Expected: PASS.

- [x] **Step 8: Commit.**

```bash
git add event/ transport/
git commit -m "telegram: the bot being added to a group is an event"
```

---

### Task 6: The `groups` package: connected groups and requests

**Files:**
- Create: `groups/groups.go`, `groups/store.go`, `groups/requests.go`, `groups/telemetry.go`, `groups/migrations/001_group_requests.sql`, `groups/groups_test.go`, `groups/requests_test.go`
- Modify: `docs/architecture/05-code-map.md` (the package list and the dependency diagram: `groups --> event`, `groups --> settings`, `groups --> alert`, `groups --> sqlitedb`)

**Interfaces:**
- Consumes: `settings.Group`, `settings.GroupRoleFamily` (Task 2); `event.GroupSeen`, `event.GroupRequest`, `event.KindGroupRequest`, `event.SourceGroups` (Task 5); `alert.Admins`.
- Produces:

```go
package groups

const (
	MaxPending = 10               // requests that may wait at once (R-DLV-17)
	RequestTTL = 7 * 24 * time.Hour // how long a card's buttons and its request live
)

type Status string
const (
	StatusPending Status = "pending"
	StatusRefused Status = "refused"
)

type Store struct{ /* … */ }
func New(ctx context.Context, db *sql.DB, now func() time.Time) (*Store, error)
func (s *Store) Status(ctx context.Context, group string) (Status, bool, error) // a pending request older than RequestTTL is not known
func (s *Store) Set(ctx context.Context, group, title string, st Status) error
func (s *Store) Forget(ctx context.Context, group string) error
func (s *Store) Pending(ctx context.Context) (int, error)

type ListFunc func() ([]settings.Group, error) // settings.TelegramFile.Groups
type KeyFunc func(chat int64) string           // telegram.GroupKey
type Connected struct{ List ListFunc; Key KeyFunc }
func (c Connected) Has(ctx context.Context, group string) (bool, error)

type PutFunc func(ctx context.Context, e event.Event) error // the inbox
type Requests struct { /* Store, Admins, Put, Now, Log + instruments */ }
func NewRequests(s *Store, admins alert.Admins, put PutFunc, now func() time.Time, log *slog.Logger) Requests
func (r Requests) Seen(ctx context.Context, g event.GroupSeen) error
```

- [x] **Step 1: Write the migration** `groups/migrations/001_group_requests.sql`:

```sql
-- A group chat that is not connected and has addressed the bot (R-DLV-17).
CREATE TABLE group_requests (
    grp      TEXT PRIMARY KEY,           -- the group's key, e.g. telegram:-1001234567890
    title    TEXT NOT NULL DEFAULT '',   -- outside text
    status   TEXT NOT NULL,              -- pending | refused
    asked_ms INTEGER NOT NULL
);
```

- [x] **Step 2: Write the failing tests.** `groups/groups_test.go` (real SQLite, opened the way `access/access_test.go` opens it):

```go
package groups_test

func TestStoreRemembersARequest(t *testing.T)
// Set(g, "Family", StatusPending) → Status = (pending, true); Pending() = 1.
// Set(g, "Family", StatusRefused) → Status = (refused, true); Pending() = 0.
// Forget(g) → Status = ("", false).

func TestAPendingRequestExpires(t *testing.T)
// with an injected clock: Set pending at T; at T + RequestTTL + 1s
// Status = ("", false) and Pending() = 0. A refused one never expires:
// Set refused at T; at T + 10*RequestTTL Status = (refused, true).

func TestConnectedReadsTheListEveryTime(t *testing.T)
// list := []settings.Group{}; c := groups.Connected{List: func() ([]settings.Group, error) { return list, nil },
//   Key: func(chat int64) string { return "telegram:" + strconv.FormatInt(chat, 10) }}
// Has("telegram:-1001") = false; then list = append(list, settings.Group{Chat: -1001, Role: "family"});
// Has("telegram:-1001") = true; Has("telegram:-1002") = false.
// A List error is returned, not read as "not connected".
```

`groups/requests_test.go`, with a fake `put` that records events and `alert.Admins{List: one admin, Private: a fixed conv}`:

```go
func TestSeenAsksTheAdminOnce(t *testing.T)
// Seen(GroupSeen{Group: "telegram:-1001", Conv: "telegram:-1001/45", Title: "Family", From: "telegram:user:9", Name: "Ann"})
// twice → exactly one put event: Kind == event.KindGroupRequest, Source == event.SourceGroups,
// Conv == the admin's private conv, payload GroupRequest with the same GroupSeen, Principal == the admin.
// Store.Status("telegram:-1001") = (pending, true).

func TestSeenStaysSilentAfterARefusal(t *testing.T)
// Store.Set(g, "", StatusRefused); Seen(...) → no event.

func TestSeenCapsWaitingRequests(t *testing.T)
// Abuse: MaxPending+5 different groups → MaxPending events; the store holds MaxPending pending rows.

func TestSeenWithNoAdminAsksNobody(t *testing.T)
// Admins.List returns none → no event, no error, no row.

func TestSeenReturnsAPutError(t *testing.T)
// put fails → Seen returns the error and Status stays ("", false), so the next message asks again.
```

Write each as a full test; the assertions are the ones listed. Add the metric readback: after one asked, one known and one capped call, `ganglion_groups_requests_total` has the three outcomes.

- [x] **Step 3: Run them and see them fail.**

Run: `go test ./groups/ -v`
Expected: FAIL to compile (the package does not exist).

- [x] **Step 4: Write `groups/groups.go`.**

```go
// Package groups is what the node knows about group chats: which are
// connected (the node's file), and which asked to be (its database). A
// group that is not connected gets one request to an admin (R-DLV-17,
// ADR-0076).
package groups

import (
	"context"
	"fmt"
	"time"

	"github.com/igorrius/ganglion/settings"
)

// MaxPending is how many requests may wait at once: a stranger who adds
// the bot to many groups cannot flood the admin.
const MaxPending = 10

// RequestTTL is how long a request waits for the admin; after it the
// group may ask again.
const RequestTTL = 7 * 24 * time.Hour

// ListFunc returns the connected groups (settings.TelegramFile.Groups).
type ListFunc func() ([]settings.Group, error)

// KeyFunc turns a chat id into the group's key (telegram.GroupKey).
type KeyFunc func(chat int64) string

// Connected answers whether a group is connected. It asks List on every
// call, so a group allowed a moment ago is connected at once.
type Connected struct {
	List ListFunc
	Key  KeyFunc
}

// Has reports whether group is connected. An error reading the list is
// returned: it is never read as "not connected".
func (c Connected) Has(_ context.Context, group string) (bool, error) {
	gs, err := c.List()
	if err != nil {
		return false, fmt.Errorf("groups: %w", err)
	}
	for _, g := range gs {
		if c.Key(g.Chat) == group {
			return true, nil
		}
	}
	return false, nil
}
```

- [x] **Step 5: Write `groups/store.go`.**

```go
package groups

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/igorrius/ganglion/sqlitedb"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Status is where a group's request is. Statuses are stored.
type Status string

// Statuses.
const (
	StatusPending Status = "pending" // the admin has the card
	StatusRefused Status = "refused" // the admin said no: the group is not asked about again
)

// Store keeps the requests of groups that are not connected.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// New migrates the "groups" component and returns the store.
func New(ctx context.Context, db *sql.DB, now func() time.Time) (*Store, error) {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return nil, err
	}
	if err := sqlitedb.Migrate(ctx, db, "groups", sub); err != nil {
		return nil, err
	}
	return &Store{db: db, now: now}, nil
}

// alive is the oldest asked_ms a pending request may have.
func (s *Store) alive() int64 { return s.now().Add(-RequestTTL).UnixMilli() }

// Status returns the request of group and whether there is one. A pending
// request older than RequestTTL is not one.
func (s *Store) Status(ctx context.Context, group string) (Status, bool, error) {
	var st string
	var asked int64
	err := s.db.QueryRowContext(ctx, `SELECT status, asked_ms FROM group_requests WHERE grp = ?`, group).Scan(&st, &asked)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("groups: status of %s: %w", group, err)
	}
	if Status(st) == StatusPending && asked < s.alive() {
		return "", false, nil
	}
	return Status(st), true, nil
}

// Set stores the request of group with st, replacing an older one.
func (s *Store) Set(ctx context.Context, group, title string, st Status) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO group_requests (grp, title, status, asked_ms) VALUES (?, ?, ?, ?)
		 ON CONFLICT (grp) DO UPDATE SET title = excluded.title, status = excluded.status, asked_ms = excluded.asked_ms`,
		group, title, string(st), s.now().UnixMilli())
	if err != nil {
		return fmt.Errorf("groups: set %s: %w", group, err)
	}
	return nil
}

// Forget drops the request of group; a group with none is not an error.
func (s *Store) Forget(ctx context.Context, group string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM group_requests WHERE grp = ?`, group); err != nil {
		return fmt.Errorf("groups: forget %s: %w", group, err)
	}
	return nil
}

// Pending counts the requests that wait for an admin now.
func (s *Store) Pending(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM group_requests WHERE status = ? AND asked_ms >= ?`,
		string(StatusPending), s.alive()).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("groups: pending: %w", err)
	}
	return n, nil
}
```

- [x] **Step 6: Write `groups/telemetry.go` and `groups/requests.go`.**

```go
package groups

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// outcome is what became of a group that is not connected.
type outcome string

// Outcomes.
const (
	outcomeAsked   outcome = "asked"    // a request went to an admin
	outcomeKnown   outcome = "known"    // a request waits already, or was refused
	outcomeCapped  outcome = "capped"   // MaxPending requests wait
	outcomeNoAdmin outcome = "no_admin" // no admin with a private chat
)

type instruments struct{ requests metric.Int64Counter }

func newInstruments() instruments {
	c, _ := otel.Meter("ganglion/groups").Int64Counter("ganglion.groups.requests",
		metric.WithDescription("Groups that are not connected and addressed the bot, by outcome."))
	return instruments{requests: c}
}

func (in instruments) count(ctx context.Context, o outcome) {
	in.requests.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", string(o))))
}
```

```go
package groups

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/igorrius/ganglion/alert"
	"github.com/igorrius/ganglion/event"
)

// PutFunc puts an event into the inbox.
type PutFunc func(ctx context.Context, e event.Event) error

// Requests turns a group that is not connected into one request to an
// admin, in the admin's private chat (R-DLV-17).
type Requests struct {
	store  *Store
	admins alert.Admins
	put    PutFunc
	now    func() time.Time
	log    *slog.Logger
	in     instruments
}

// NewRequests returns the requests over s.
func NewRequests(s *Store, admins alert.Admins, put PutFunc, now func() time.Time, log *slog.Logger) Requests {
	return Requests{store: s, admins: admins, put: put, now: now, log: log, in: newInstruments()}
}

// Seen is called for a group that is not connected when someone addresses
// the bot in it or adds the bot to it. The first call asks an admin; while
// that request waits, after a refusal, and past MaxPending waiting
// requests it does nothing. An error means nothing was asked: the caller
// offers the update again.
func (r Requests) Seen(ctx context.Context, g event.GroupSeen) error {
	o, err := r.seen(ctx, g)
	if err != nil {
		return err
	}
	r.in.count(ctx, o)
	r.log.Info("groups: a group that is not connected", "group", g.Group, "outcome", string(o))
	return nil
}

func (r Requests) seen(ctx context.Context, g event.GroupSeen) (outcome, error) {
	if _, known, err := r.store.Status(ctx, g.Group); err != nil || known {
		return outcomeKnown, err
	}
	n, err := r.store.Pending(ctx)
	if err != nil {
		return "", err
	}
	if n >= MaxPending {
		return outcomeCapped, nil
	}
	req, conv, ok, err := r.address(ctx, g)
	if err != nil || !ok {
		return outcomeNoAdmin, err
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("groups: %w", err)
	}
	now := r.now()
	e := event.Event{ID: event.NewID(now), IdemKey: "group:request:" + g.Group + ":" + now.UTC().Format(time.DateOnly),
		Source: event.SourceGroups, Conv: conv, Kind: event.KindGroupRequest, Payload: payload, At: now}
	if err := r.put(ctx, e); err != nil {
		return "", fmt.Errorf("groups: request for %s: %w", g.Group, err)
	}
	return outcomeAsked, r.store.Set(ctx, g.Group, g.Title, StatusPending)
}

// address picks the first admin with a private chat and writes the
// request for them.
func (r Requests) address(ctx context.Context, g event.GroupSeen) (event.GroupRequest, string, bool, error) {
	admins, err := r.admins.List(ctx)
	if err != nil {
		return event.GroupRequest{}, "", false, fmt.Errorf("groups: %w", err)
	}
	for _, p := range admins {
		if transport, conv, ok := r.admins.Private(p); ok {
			return event.GroupRequest{GroupSeen: g, Principal: p, Transport: transport}, conv, true, nil
		}
	}
	return event.GroupRequest{}, "", false, nil
}
```

The IdemKey holds the day: a group whose request expired or was forgotten can ask again on another day, and a put that is repeated the same day is stored once.

- [x] **Step 7: Run the tests, update the code map, commit.**

Run: `go test ./groups/ -v`
Expected: PASS.

```bash
git add groups/ docs/architecture/05-code-map.md
git commit -m "groups: a group that is not connected asks an admin once"
```

---

### Task 7: The access filter knows groups

**Files:**
- Modify: `access/access.go:164-198` (`fromPerson`, `Filter`), `access/access_test.go`, `config/delivery.go:172` (the call, with `access.NoGroups()` for now)

**Interfaces:**
- Consumes: `event.Message.Group`, `event.GroupSeen`, `event.KindGroupJoined`.
- Produces:

```go
// GroupsFunc reports whether a group chat is connected (groups.Connected.Has).
type GroupsFunc func(ctx context.Context, group string) (bool, error)

// UnknownGroupFunc is told about a group that is not connected (groups.Requests.Seen).
type UnknownGroupFunc func(ctx context.Context, g event.GroupSeen) error

// Groups is what the filter knows about group chats.
type Groups struct {
	Has     GroupsFunc
	Unknown UnknownGroupFunc
}

func NoGroups() Groups // no group is connected and nobody is told

func (l *List) Filter(next transport.Sink, g Groups, logger *slog.Logger) transport.Sink
```

- [x] **Step 1: Write the failing tests** in `access/access_test.go`. Add a helper that builds a message event with a group, then:

```go
func groupMsg(from, group, conv string) event.Event // KindMessage, payload event.Message{Text: "hi", From: from, Name: "Ann", Group: group, GroupTitle: "Family"}, Conv: conv

// A stranger in a connected group reaches the sink (R-DLV-17).
func TestFilterPassesAStrangerInAConnectedGroup(t *testing.T)
// Groups{Has: only "telegram:-1001", Unknown: fails the test}; groupMsg("telegram:user:9", "telegram:-1001", "telegram:-1001/45") → next called once, err == nil.

// The admin in a group that is not connected is dropped like anyone else,
// and the group is reported with the conversation it happened in.
func TestFilterDropsAndReportsAGroupThatIsNotConnected(t *testing.T)
// the admin is listed; Has → false; Unknown records its argument.
// → errors.Is(err, transport.ErrDropped); next not called;
// Unknown got GroupSeen{Group: "telegram:-1001", Conv: "telegram:-1001/45", Title: "Family", From: admin, Name: "Ann"}.

// A press in a group that is not connected is dropped and reports nothing.
func TestFilterDropsAPressInAnUnknownGroupSilently(t *testing.T)
// KindDecision with payload event.Decision{Button: "b", From: admin, Group: "telegram:-1001"} → ErrDropped, Unknown not called.

// The bot added to a group never reaches the sink; an unknown group is reported.
func TestFilterTakesGroupJoined(t *testing.T)
// KindGroupJoined with payload GroupSeen{Group: g, Conv: g, Title: "Family", From: stranger}:
//   Has → false: ErrDropped, next not called, Unknown got that GroupSeen;
//   Has → true:  ErrDropped, next not called, Unknown not called.

// Errors are returned, so the transport offers the update again.
func TestFilterReturnsGroupErrors(t *testing.T)
// Has fails → that error, not ErrDropped. Has false and Unknown fails → that error, not ErrDropped.

// A private message is judged as before: the group rule does not apply.
func TestFilterPrivateChatUnchanged(t *testing.T)
// stranger in a private chat with Groups{Has: always true} → ErrDropped (reason stranger).

// Abuse: a payload that claims a connected group but arrives from a
// private conversation cannot be forged by a person: the transport sets
// Group. This test pins that the filter trusts only the payload's group,
// so the transport test of Task 4 (a private message has no group) is the guard.
```

Update the existing calls of `Filter` in the tests to pass `access.NoGroups()`.

- [x] **Step 2: Run them and see them fail.**

Run: `go test ./access/ -v`
Expected: FAIL to compile (`Filter` takes two arguments).

- [x] **Step 3: Rewrite the filter.** Replace `Filter` with:

```go
// GroupsFunc reports whether a group chat is connected.
type GroupsFunc func(ctx context.Context, group string) (bool, error)

// UnknownGroupFunc is told about a group that is not connected, so an
// admin can be asked. An error means it could not ask.
type UnknownGroupFunc func(ctx context.Context, g event.GroupSeen) error

// Groups is what the filter knows about group chats.
type Groups struct {
	Has     GroupsFunc
	Unknown UnknownGroupFunc
}

// NoGroups connects no group and tells nobody.
func NoGroups() Groups {
	return Groups{
		Has:     func(context.Context, string) (bool, error) { return false, nil },
		Unknown: func(context.Context, event.GroupSeen) error { return nil },
	}
}

// Reasons a message is kept out of the inbox, for the metric.
const (
	reasonMalformed = "malformed"
	reasonStranger  = "stranger"
	reasonGroup     = "group"  // from a group that is not connected
	reasonJoined    = "joined" // the bot was added to a group: never for the agent
)

// Filter wraps next so that only these reach it (spec 7.8, R-DLV-17):
// in a private chat, messages and presses of principals on the list; in a
// group chat, those of anyone when the group is connected. A group that
// is not connected is reported to g.Unknown when someone writes in it or
// adds the bot to it. A dropped event returns transport.ErrDropped, so the
// transport confirms it; it is logged without its text. Events that no
// person sent pass through. An error reading the list or the groups is
// returned, so the transport offers the update again later.
func (l *List) Filter(next transport.Sink, g Groups, logger *slog.Logger) transport.Sink {
	f := filter{l: l, next: next, g: g, log: logger}
	return f.sink
}

// filter is one Filter.
type filter struct {
	l    *List
	next transport.Sink
	g    Groups
	log  *slog.Logger
}

func (f filter) sink(ctx context.Context, e event.Event) error {
	switch {
	case e.Kind == event.KindGroupJoined:
		return f.joined(ctx, e)
	case !fromPerson[e.Kind]:
		return f.next(ctx, e)
	}
	var m event.Message // a Decision names its sender and group under the same keys
	if err := json.Unmarshal(e.Payload, &m); err != nil || m.From == "" {
		return f.drop(ctx, e, reasonMalformed, "")
	}
	if m.Group != "" {
		return f.fromGroup(ctx, e, m)
	}
	_, ok, err := f.l.Role(ctx, m.From)
	if err != nil {
		return err
	}
	if !ok {
		return f.drop(ctx, e, reasonStranger, m.From)
	}
	return f.next(ctx, e)
}

// fromGroup passes an event of a connected group and reports a message of
// one that is not.
func (f filter) fromGroup(ctx context.Context, e event.Event, m event.Message) error {
	ok, err := f.g.Has(ctx, m.Group)
	if err != nil {
		return err
	}
	if ok {
		return f.next(ctx, e)
	}
	if e.Kind == event.KindMessage {
		seen := event.GroupSeen{Group: m.Group, Conv: e.Conv, Title: m.GroupTitle, From: m.From, Name: m.Name}
		if err := f.g.Unknown(ctx, seen); err != nil {
			return err
		}
	}
	return f.drop(ctx, e, reasonGroup, m.From)
}

// joined reports a group the bot was added to when it is not connected.
// The event is never for the agent.
func (f filter) joined(ctx context.Context, e event.Event) error {
	var g event.GroupSeen
	if err := json.Unmarshal(e.Payload, &g); err != nil || g.Group == "" || g.From == "" {
		return f.drop(ctx, e, reasonMalformed, "")
	}
	ok, err := f.g.Has(ctx, g.Group)
	if err != nil {
		return err
	}
	if !ok {
		if err := f.g.Unknown(ctx, g); err != nil {
			return err
		}
	}
	return f.drop(ctx, e, reasonJoined, g.From)
}

// drop counts and logs an event kept out of the inbox.
func (f filter) drop(ctx context.Context, e event.Event, reason, principal string) error {
	f.l.dropped.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", reason)))
	f.log.Warn("access: event dropped", "reason", reason, "kind", e.Kind, "id", e.ID,
		"principal", principal, "conv", e.Conv, "source", e.Source)
	return fmt.Errorf("access: %s: %w", reason, transport.ErrDropped)
}
```

`joined` is logged at warn like the others; if that is too loud for a connected group's re-join, log `reasonJoined` at info inside `drop` with a `switch` on the reason. Existing tests that match the old log messages or the `text_len` attribute are updated to the new line.

- [x] **Step 4: Keep `config` compiling.** In `config/delivery.go` pass `access.NoGroups()` as the second argument for now; Task 9 replaces it.

- [x] **Step 5: Run the tests.**

Run: `go test ./access/ ./config/ ./internal/chatlab/...`
Expected: PASS. A chatlab or agent test in which a listed person talks in a group now sees a drop: with `NoGroups` no group is connected. Mark such tests to be fixed in Task 9 by connecting the group in the lab's file (`config/lab.go`), not by loosening the filter; if one cannot wait, connect the group there now.

- [x] **Step 6: Commit.**

```bash
git add access/ config/
git commit -m "access: a group's message passes only when the group is connected"
```

---

### Task 8: The agent: no admin role in a group, the card and its buttons

**Files:**
- Modify: `agent/stages.go:86-94` (`setAdmin`), `agent/turn.go` (the `turn` struct), `agent/agent.go:373-420` (`payloads`), `agent/resume.go:49-70` (`resume`), `texts/texts.go`, `texts/lang/en.yaml`, `texts/lang/ru.yaml`, `texts/lang/uk.yaml`
- Create: `agent/group_request.go`, `agent/group_request_test.go`, `groups/actions.go`, `groups/actions_test.go`
- Test: `agent/stages_test.go` or the file that tests `setAdmin` today

**Interfaces:**
- Consumes: `event.GroupRequest`, `event.KindGroupRequest`, `event.SourceGroups`; `groups.Store`, `groups.RequestTTL`; `settings.Group`, `settings.GroupRoleFamily`; `pending.Button`, `pending.QuickAction`, `pending.QuickPress`, `pending.QuickEdit`, `pending.NewButtonID`; `a.d.Pending.AddButtons`, `a.d.Private`, `a.say`, `a.label`.
- Produces:

```go
// texts
GroupRequestCard  Key = "group.request_card"  // vars: title, id, name
GroupNotConnected Key = "group.not_connected"
GroupAllowed      Key = "group.allowed"       // vars: title
GroupRefused      Key = "group.refused"       // vars: title
ButtonAllow       Key = "button.allow"
ButtonRefuse      Key = "button.refuse"

// groups
const (
	ActionAllow  = "group.allow"
	ActionRefuse = "group.refuse"
)
type ButtonArgs struct {
	Group string `json:"group"`
	Title string `json:"title,omitempty"`
}
type Writer interface{ SetGroup(g settings.Group) error }          // settings.TelegramFile
type ChatFunc func(group string) (chat int64, ok bool)             // the chat id of a group's key
type AdminFunc func(ctx context.Context, principal string) (bool, error)
type Actions struct {
	Store   *Store
	File    Writer
	Chat    ChatFunc
	IsAdmin AdminFunc
}
func (a Actions) QuickActions() map[string]pending.QuickAction
```

- [x] **Step 1: Write the failing test for the role.** Beside the existing tests of the admin check:

```go
// The admin role holds only in the admin's private chat (R-POL-10): in
// a group the admin's turn is offered no admin tool.
func TestAdminRoleDoesNotFollowIntoAGroup(t *testing.T)
// an agent whose IsAdmin says yes for P and whose Private maps P to "telegram:42":
//   a message from P in conv "telegram:42"      → the turn is an admin's (an admin-only tool is on offer);
//   a message from P in conv "telegram:-1001/45" → it is not (no admin-only tool is on offer).
```

Use the agent test kit the neighbouring tests use to read what tools a turn offered.

- [x] **Step 2: Run it and see it fail.**

Run: `go test ./agent/ -run AdminRoleDoesNotFollow -v`
Expected: FAIL: the group turn is offered the admin tools.

- [x] **Step 3: Change `setAdmin`.**

```go
// setAdmin looks up whether the turn's principal may use tools. The admin
// role holds only in the admin's private chat (R-POL-10, ADR-0076): in a
// group everyone acts with the group's role, so text others wrote in a
// shared history cannot steer the agent into an admin's action.
func (a *Agent) setAdmin(ctx context.Context, t *turn) error {
	admin, err := a.d.IsAdmin(ctx, t.msg.From)
	if err != nil {
		return fmt.Errorf("agent: role of %s: %w", t.msg.From, err)
	}
	t.admin = admin && a.inPrivate(t)
	return nil
}

// inPrivate reports whether the turn runs in its principal's private chat.
func (a *Agent) inPrivate(t *turn) bool {
	_, conv, ok := a.d.Private(t.msg.From)
	return ok && conv == t.event.Conv
}
```

Run `go test ./agent/... ./internal/chatlab/... ./cmd/...`. A test in which an admin uses a tool from a conversation that is not their private chat now fails: check that conversation. If it is a group, the test moves to the private chat. If it is a scheduled task or a background job reporting to a conversation the admin made it in, its event's conversation is that private chat and the test already passes. Any other failure means a turn kind whose conversation is not the principal's private chat: stop and report it instead of changing `inPrivate`.

- [x] **Step 4: Add the texts.** In `texts/texts.go` add the six keys of the Interfaces block beside their neighbours (the two buttons beside `ButtonDelete`), add them to `allKeys`, and add `ButtonAllow, ButtonRefuse` to the label case of the function that lists button labels (line 407). In the language files:

`en.yaml`
```yaml
group.request_card: |-
  I was called in the group **{title}** (`{id}`) by {name}. It is not connected, so I do not answer there.

  Allow it? Everyone in that group will be able to talk to me. They get no tools that change the node.
group.not_connected: I have been added here, but I have no permission to answer in this group yet. I have asked my administrator.
group.allowed: The group **{title}** is connected. I answer there when I am addressed.
group.refused: The group **{title}** stays closed. I will not ask about it again.
button.allow: Allow
button.refuse: Refuse
```

`ru.yaml`
```yaml
group.request_card: |-
  Меня позвали в группе **{title}** (`{id}`), написал {name}. Группа не подключена, поэтому я там не отвечаю.

  Разрешить? Со мной сможет говорить каждый участник этой группы. Инструментов, которые меняют ноду, они не получат.
group.not_connected: Меня сюда добавили, но разрешения отвечать в этой группе у меня пока нет. Я спросил администратора.
group.allowed: Группа **{title}** подключена. Я отвечаю там, когда ко мне обращаются.
group.refused: Группа **{title}** остаётся закрытой. Больше про неё не спрошу.
button.allow: Разрешить
button.refuse: Отказать
```

`uk.yaml`
```yaml
group.request_card: |-
  Мене покликали в групі **{title}** (`{id}`), написав(ла) {name}. Групу не підключено, тому я там не відповідаю.

  Дозволити? Зі мною зможе говорити кожен учасник цієї групи. Інструментів, що змінюють ноду, вони не отримають.
group.not_connected: Мене сюди додали, але дозволу відповідати в цій групі я ще не маю. Я запитав адміністратора.
group.allowed: Групу **{title}** підключено. Я відповідаю там, коли до мене звертаються.
group.refused: Група **{title}** лишається закритою. Більше про неї не питатиму.
button.allow: Дозволити
button.refuse: Відмовити
```

Match the placeholder syntax and the YAML style of the neighbouring entries in each file (check one entry with variables before pasting). Run `go test ./texts/`: its tests check that every key exists in every language and fits its length limit; add the keys to whatever list a failing test names.

- [x] **Step 5: Write the failing tests for the actions** in `groups/actions_test.go`:

```go
func press(t *testing.T, action string, a groups.ButtonArgs, principal string) pending.QuickPress
// QuickPress{Speak: texts.Speaker{Lang: texts.English}, Button: pending.Button{Action: action, Principal: principal, Args: json of a}}

// Allow writes the group's line with the role family and forgets the request.
func TestAllowConnectsTheGroup(t *testing.T)
// Store.Set(g, "Family", StatusPending); a fake Writer records SetGroup.
// QuickActions()[ActionAllow](ctx, press(ActionAllow, {Group: "telegram:-1001", Title: "Family"}, admin))
// → SetGroup(settings.Group{Chat: -1001, Name: "Family", Role: "family"}); Status = ("", false);
//   the edit has no buttons, a text that contains "Family", and a Note that names the key and not the title.

// Refuse remembers the refusal and writes nothing.
func TestRefuseIsRemembered(t *testing.T)
// → no SetGroup; Status = (refused, true); the edit has no buttons.

// Abuse: a press by someone who is no longer an admin changes nothing.
func TestAllowByANonAdminIsRefused(t *testing.T)
// IsAdmin → false → an error; no SetGroup; Status stays pending.

// Abuse: arguments that do not name a group change nothing.
func TestAllowWithABadGroupIsRefused(t *testing.T)
// Args {Group: "telegram:42"} with Chat returning ok=false → an error; no SetGroup.

// The title is cut: a group's title is outside text and goes into the file.
func TestAllowCutsALongTitle(t *testing.T)
// Title of 300 runes → the written Name has at most 64 runes.

// A second press of Allow is harmless.
func TestAllowTwice(t *testing.T)
// two calls → the same edit, no error.
```

- [x] **Step 6: Write `groups/actions.go`.**

```go
package groups

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/igorrius/ganglion/internal/text"
	"github.com/igorrius/ganglion/settings"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool/pending"
)

// Actions of the buttons under a group's request.
const (
	ActionAllow  = "group.allow"
	ActionRefuse = "group.refuse"
)

// nameRunes caps the name written to the node's file: a title is the
// group's own text.
const nameRunes = 64

// errNotAdmin refuses a press by someone who is not an admin.
var errNotAdmin = errors.New("groups: only an admin connects a group")

// ButtonArgs are the arguments of both buttons.
type ButtonArgs struct {
	Group string `json:"group"`
	Title string `json:"title,omitempty"`
}

// Writer writes a group's line to the node's file (settings.TelegramFile).
type Writer interface {
	SetGroup(g settings.Group) error
}

// ChatFunc returns the chat id of a group's key; ok is false for a key
// that is no group's.
type ChatFunc func(group string) (chat int64, ok bool)

// AdminFunc reports whether principal is an admin (access.List.IsAdmin).
type AdminFunc func(ctx context.Context, principal string) (bool, error)

// Actions are the buttons under a group's request.
type Actions struct {
	Store   *Store
	File    Writer
	Chat    ChatFunc
	IsAdmin AdminFunc
}

// QuickActions are the buttons by action name, for the agent
// (agent.Deps.QuickActions).
func (a Actions) QuickActions() map[string]pending.QuickAction {
	return map[string]pending.QuickAction{ActionAllow: a.allow, ActionRefuse: a.refuse}
}

// allow connects the group with the role family (R-DLV-17). It is
// idempotent: the line is set, not added.
func (a Actions) allow(ctx context.Context, p pending.QuickPress) (pending.QuickEdit, error) {
	args, chat, err := a.pressed(ctx, p)
	if err != nil {
		return pending.QuickEdit{}, err
	}
	g := settings.Group{Chat: chat, Name: text.Truncate(args.Title, nameRunes), Role: settings.GroupRoleFamily}
	if err := a.File.SetGroup(g); err != nil {
		return pending.QuickEdit{}, fmt.Errorf("groups: connect %s: %w", args.Group, err)
	}
	if err := a.Store.Forget(ctx, args.Group); err != nil {
		return pending.QuickEdit{}, err
	}
	return pending.QuickEdit{
		Text: p.Speak.Text(texts.GroupAllowed, texts.Vars{"title": texts.Escape(g.Name)}),
		Note: "the owner connected the group chat " + args.Group + " by button; its members may now talk to you there",
	}, nil
}

// refuse remembers that the group is not to be asked about again.
func (a Actions) refuse(ctx context.Context, p pending.QuickPress) (pending.QuickEdit, error) {
	args, _, err := a.pressed(ctx, p)
	if err != nil {
		return pending.QuickEdit{}, err
	}
	if err := a.Store.Set(ctx, args.Group, args.Title, StatusRefused); err != nil {
		return pending.QuickEdit{}, err
	}
	return pending.QuickEdit{
		Text: p.Speak.Text(texts.GroupRefused, texts.Vars{"title": texts.Escape(text.Truncate(args.Title, nameRunes))}),
		Note: "the owner refused to connect the group chat " + args.Group + " by button",
	}, nil
}

// pressed checks who pressed and what the button names.
func (a Actions) pressed(ctx context.Context, p pending.QuickPress) (ButtonArgs, int64, error) {
	admin, err := a.IsAdmin(ctx, p.Button.Principal)
	if err != nil {
		return ButtonArgs{}, 0, fmt.Errorf("groups: %w", err)
	}
	if !admin {
		return ButtonArgs{}, 0, errNotAdmin
	}
	var args ButtonArgs
	if err := json.Unmarshal(p.Button.Args, &args); err != nil {
		return ButtonArgs{}, 0, fmt.Errorf("groups: button arguments: %w", err)
	}
	chat, ok := a.Chat(args.Group)
	if !ok {
		return ButtonArgs{}, 0, fmt.Errorf("groups: %q is not a group", args.Group)
	}
	return args, chat, nil
}
```

`text.Truncate` (`internal/text`) is the one truncation by runes (conventions, section 8). The Note never holds the title: a note goes into the model's history, and a title is outside text.

- [x] **Step 7: Write the failing agent test** `agent/group_request_test.go`, with the kit the module-notify tests use (`grep -ln "KindModuleNotify" agent/*_test.go`):

```go
// A group's request becomes a card with two buttons in the admin's
// private chat and one notice in the conversation it came from, with no
// model call (R-DLV-17).
func TestGroupRequestSendsTheCardAndTheNotice(t *testing.T)
// event: Kind KindGroupRequest, Source SourceGroups, Conv "telegram:42" (the admin's private chat), payload
//   GroupRequest{GroupSeen{Group: "telegram:-1001", Conv: "telegram:-1001/45", Title: "Family *chat*", From: "telegram:user:9", Name: "Ann"},
//                Principal: admin, Transport: "telegram"}
// assert: no model call; two outgoing messages:
//   1. Conv "telegram:42", two buttons labelled Allow and Refuse, text containing "telegram:-1001" and the
//      title with its Markdown escaped;
//   2. Conv "telegram:-1001/45", no buttons, the "no permission yet" text.
// The pending store holds two quick-action buttons for the admin in "telegram:42" with actions
// groups.ActionAllow and groups.ActionRefuse, args naming "telegram:-1001", expiring RequestTTL from now.

// Abuse: a request that did not come from the node, or is addressed to
// someone who is not an admin, sends nothing.
func TestGroupRequestFromAnotherSourceIsDropped(t *testing.T)
// the same event with Source "telegram" → no outgoing, no buttons.
// the same event with Principal a family member → no outgoing, no buttons.
```

- [x] **Step 8: Write `agent/group_request.go` and hook it in.** In `agent/turn.go` add to `turn`: `groupReq event.GroupRequest // the request of a KindGroupRequest event`. In `agent/agent.go`, in `payloads`:

```go
	event.KindGroupRequest: func(t *turn) bool {
		g := &t.groupReq
		if json.Unmarshal(t.event.Payload, g) != nil || g.Group == "" || g.Principal == "" {
			return false
		}
		t.msg = event.Message{From: g.Principal}
		return true
	},
```

In `agent/resume.go`, in `resume`, add `case event.KindGroupRequest: return true, a.groupRequest(ctx, t)`, and list `event.KindGroupJoined` in the case that says "never here" with the comment "the access filter takes it". Extend `resume`'s doc comment with "a group's request".

```go
package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/groups"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool/pending"
	"github.com/igorrius/ganglion/turns"
)

// groupRequest asks an admin, in their private chat, whether a group that
// is not connected may talk to the bot, and tells the group once that it
// has asked, with no model (R-DLV-17, ADR-0076). Only the node's event
// counts, and only for an admin. The card is the node's text: the group's
// title and the sender's name are outside text and go in escaped.
func (a *Agent) groupRequest(ctx context.Context, t *turn) error {
	t.record.Outcome = turns.OutcomeCommand
	p := t.groupReq
	if t.event.Source != event.SourceGroups {
		a.log.Warn("agent: a group request that is not the node's; dropped", "source", t.event.Source, "conv", t.event.Conv)
		return nil
	}
	t.transport, t.record.Principal = p.Transport, p.Principal
	if err := a.setAdmin(ctx, t); err != nil {
		return err
	}
	if !t.admin {
		a.log.Warn("agent: a group request for a principal who is not an admin in their private chat; dropped", "group", p.Group)
		return nil
	}
	buttons, err := a.groupButtons(ctx, t, p)
	if err != nil {
		return err
	}
	vars := texts.Vars{"title": texts.Escape(p.Title), "id": p.Group, "name": texts.Escape(p.Name)}
	t.out = append(t.out,
		event.Outgoing{IdemKey: t.event.IdemKey + ":card", Transport: p.Transport, Conv: t.event.Conv,
			Text: a.say(ctx, t, texts.GroupRequestCard, vars), Buttons: buttons, NoPreview: true},
		event.Outgoing{IdemKey: t.event.IdemKey + ":notice", Transport: p.Transport, Conv: p.Conv,
			Text: a.say(ctx, t, texts.GroupNotConnected, nil)})
	return nil
}

// groupButtons stores the Allow and Refuse buttons of p for the admin and
// returns them.
func (a *Agent) groupButtons(ctx context.Context, t *turn, p event.GroupRequest) ([]event.Button, error) {
	args, err := json.Marshal(groups.ButtonArgs{Group: p.Group, Title: p.Title})
	if err != nil {
		return nil, fmt.Errorf("agent: group request: %w", err)
	}
	expires := a.d.Now().Add(groups.RequestTTL)
	stored := []pending.Button{
		{ID: pending.NewButtonID(), Action: groups.ActionAllow, Label: a.label(ctx, t, texts.ButtonAllow),
			Principal: p.Principal, Conv: t.event.Conv, Args: args, Expires: expires},
		{ID: pending.NewButtonID(), Action: groups.ActionRefuse, Label: a.label(ctx, t, texts.ButtonRefuse),
			Principal: p.Principal, Conv: t.event.Conv, Args: args, Expires: expires},
	}
	if err := a.d.Pending.AddButtons(ctx, stored...); err != nil {
		return nil, fmt.Errorf("agent: group request: %w", err)
	}
	return []event.Button{{ID: stored[0].ID, Label: stored[0].Label}, {ID: stored[1].ID, Label: stored[1].Label}}, nil
}
```

Notes for this step:
- `a.d.Pending` is the `Confirmations` interface (`agent/agent.go:84`); if it has no `AddButtons`, add the method to it (`pending.Store` already has it).
- `a.d.Now` is the agent's clock (`agent/agent.go:171`).
- The notice is written in the admin's language: the people of the group are not known to the node yet.
- A turn that runs again stores two more buttons and its outgoing messages are dropped by their IdemKey, so the extra buttons are never shown; they expire.
- If the dependency rules forbid `agent → groups`, move `ActionAllow`, `ActionRefuse` and `ButtonArgs` to `event` (as `event.GroupActionAllow`, …) and use them from both; add the edge to the code map otherwise.

- [x] **Step 9: Run the tests.**

Run: `go test ./agent/... ./groups/... ./texts/...`
Expected: PASS.

- [x] **Step 10: Commit.**

```bash
git add agent/ groups/ texts/ docs/architecture/05-code-map.md
git commit -m "agent: a group's request is a card for the admin, and the admin role stays in the private chat"
```

---

### Task 9: Wiring, the example file, acceptance

**Files:**
- Create: `config/groups.go`
- Modify: `config/config.go:31` (the provider list), `config/delivery.go:172`, `config/agent.go:352-356` (`quickActions`), `config/lab.go` (a way to connect a group in the lab), `ganglion.example.yaml:32-36`, `docs/architecture/05-code-map.md`, the delivery page under `docs/architecture/`, `docs/plans/p1-all-in-one.md`, `docs/README.md`
- Test: `config/config_test.go` (the container test), `internal/chatlab/` (a scenario), `transport/telegram/telegramtest`

**Interfaces:**
- Consumes: everything above.
- Produces: providers `GroupStore() do.Provider[*groups.Store]`, `GroupsFile() do.Provider[settings.TelegramFile]`, `AccessGroups() do.Provider[access.Groups]`, `GroupActions() do.Provider[groups.Actions]`.

- [x] **Step 1: Write the failing end-to-end scenario** in `internal/chatlab/`, in the style of an existing scenario that uses buttons (`grep -ln "AddCallback" internal/chatlab/*_test.go`):

```go
// A group is connected through the card and then answers its members
// only when they address the bot (R-DLV-16, R-DLV-17).
func TestConnectAGroupThroughTheCard(t *testing.T)
// 1. Ann (a stranger, user 9) writes "@test_bot hello" with a mention in group -1001 "Family", topic 45.
//    → the group gets one message in topic 45: the "no permission yet" text;
//      the admin's private chat gets the card with two buttons; no model call was made.
// 2. Ann writes "@test_bot hello?" again → nothing more is sent anywhere.
// 3. The admin presses Allow → the card is edited to "connected", its buttons are gone;
//    the lab node's file has telegram.groups with chat -1001, role family.
// 4. Ann writes "@test_bot hello" again → the scripted model is called and its answer goes to topic 45.
// 5. Ann writes "nice weather" with no mention → nothing is sent and the model is not called.
// 6. The admin writes "@test_bot run status" in the group → the turn is offered no admin-only tool.
```

- [x] **Step 2: Run it and see it fail.**

Run: `go test ./internal/chatlab/ -run ConnectAGroupThroughTheCard -v`
Expected: FAIL at step 1: the filter runs with `NoGroups`, so nothing is sent.

- [x] **Step 3: Write `config/groups.go`.** Providers only, in the shape of `config/modhost.go` (find how it builds `settings.ModulesFile` from the bootstrap's config path and do the same for `settings.TelegramFile`):

```go
package config

// GroupStore provides the requests of groups that are not connected.
func GroupStore() do.Provider[*groups.Store] {
	return func(i do.Injector) (*groups.Store, error) {
		return groups.New(do.MustInvoke[Startup](i), do.MustInvoke[Database](i).DB, time.Now)
	}
}

// GroupsFile provides the telegram: section of the node's file.
func GroupsFile() do.Provider[settings.TelegramFile] {
	return func(i do.Injector) (settings.TelegramFile, error) {
		return settings.TelegramFile{Path: settingsFile(do.MustInvoke[Bootstrap](i))}, nil
	}
}

// AccessGroups provides what the access filter knows about group chats:
// the groups of the node's file, and a request to an admin for the rest.
func AccessGroups() do.Provider[access.Groups] {
	return func(i do.Injector) (access.Groups, error) {
		connected := groups.Connected{List: do.MustInvoke[settings.TelegramFile](i).Groups, Key: telegram.GroupKey}
		requests := groups.NewRequests(do.MustInvoke[*groups.Store](i), do.MustInvoke[alert.Admins](i),
			do.MustInvoke[*sqlqueue.Queue](i).Put, time.Now, do.MustInvoke[*slog.Logger](i))
		return access.Groups{Has: connected.Has, Unknown: requests.Seen}, nil
	}
}

// GroupActions provides the buttons under a group's request.
func GroupActions() do.Provider[groups.Actions] {
	return func(i do.Injector) (groups.Actions, error) {
		return groups.Actions{Store: do.MustInvoke[*groups.Store](i), File: do.MustInvoke[settings.TelegramFile](i),
			Chat: telegram.GroupChat, IsAdmin: do.MustInvoke[*access.List](i).IsAdmin}, nil
	}
}
```

`settingsFile(b)` is the helper `config/modhost.go` uses for `settings.ModulesFile`; `Startup` and `Database` are the types of `config/config.go:23` and `config/storage.go:41`. Add to `transport/telegram/conv.go`, with a test in `conv_test.go` (`GroupChat("telegram:-1001") = (-1001, true)`; false for `"telegram:42"`, `"telegram:-1001/45"`, `"x"`):

```go
// GroupChat returns the chat id of a group's key; ok is false for a key
// that is not a group chat as a whole (a private chat, a topic, another
// transport's key).
func GroupChat(group string) (chat int64, ok bool) {
	chat, thread, err := ParseConv(group)
	return chat, err == nil && thread == 0 && chat < 0
}
```

If the queue's `Put` has another signature than `groups.PutFunc`, adapt it in the provider the way `TelegramReceivers` adapts it for the sink. If the clock is injected elsewhere in `config` (a `Clock` provider), use that instead of `time.Now`.

- [x] **Step 4: Connect the providers.**
  - `config/config.go`: add `do.Lazy(GroupStore()), do.Lazy(GroupsFile()), do.Lazy(AccessGroups()), do.Lazy(GroupActions())` to the list that holds `Admins()`; a role that has no Telegram gets them through the same `choose` its neighbours use if the container test asks for it.
  - `config/delivery.go`, `TelegramReceivers`: `do.MustInvoke[*access.List](i).Filter(do.MustInvoke[*telegram.Reactor](i).Noting(q.Put), do.MustInvoke[access.Groups](i), logger)`.
  - `config/agent.go`, `quickActions`: `maps.Copy(out, do.MustInvoke[groups.Actions](i).QuickActions())`.
  - `config/lab.go`: the lab node writes its file to a temporary directory; make sure `GroupsFile` points at it, so step 3 of the scenario can read the line back.

- [x] **Step 5: Run the scenario and the container test.**

Run: `go test ./config/ ./internal/chatlab/ -v -run 'ConnectAGroup|Container|Roles'`
Expected: PASS. Fix the tests left marked in Task 7 by connecting their group in the lab's file.

- [x] **Step 6: Update the example file.** In `ganglion.example.yaml`, in the `telegram:` section, correct the comment on `reply` to "When an answer quotes the message it answers: auto (in groups, or when newer messages came), always, off", and add:

```yaml
  # Group chats whose members may talk to the bot. In a group it answers
  # only when addressed: a mention, a reply to its message, or /cmd@bot.
  # The node writes a line here when you press Allow on the card it sends
  # you the first time it is addressed in a group.
  # groups:
  #   - chat: -1001234567890
  #     name: Family
  #     role: family
```

- [x] **Step 7: Update the architecture views.** In `docs/architecture/05-code-map.md` describe `groups` and the new edges (`config --> groups`, `agent --> groups` or the `event` variant chosen in Task 8). On the delivery level page add to the incoming flow, as a Mermaid `sequenceDiagram` or in the existing `flowchart`: transport → "addressed?" → access filter → "connected?" → inbox, with the branch to `groups.Requests` → inbox → agent → card. Nothing in it is planned, so nothing is dashed.

- [x] **Step 8: Run everything.**

Run: `make lint && go test ./...`
Expected: PASS. Then the targets the repository uses for the model-free acceptance of the agent (`grep -n "^[a-z-]*:" Makefile | head -40` lists them); run the chatlab target.

- [x] **Step 9: The live check, in the owner's family forum.** Deploy to Railway as the project's deploy note says. With the owner:
  1. A family member mentions the bot in a topic of the forum. The topic gets the "no permission yet" line once; the owner's private chat gets the card with the forum's title and id.
  2. A second mention sends nothing.
  3. The owner presses Allow. The card changes to "connected".
  4. The same member mentions the bot in that topic: the answer comes in that topic.
  5. A message with no mention gets no answer; the journal shows no turn for it.
  6. A reply to the bot's answer gets an answer.
  7. In another topic the bot keeps a separate conversation.
  8. `ganglion.yaml` on the volume holds the group's line; a restart keeps the group connected.
  Record the result in this plan under a "Live check" heading with the date. This is also the topic check the M2 plan could not run: tick it there.

- [ ] **Step 10: Close the task in the docs and commit.** Tick the rows in `docs/plans/p1-all-in-one.md`, update "Where we are" in `docs/README.md`, set ADR-0076 to Accepted if the owner accepted it.

```bash
git add config/ transport/ internal/chatlab/ ganglion.example.yaml docs/
git commit -m "config: groups are wired: the card connects a group and the filter reads the node's file"
```

---

## As built (2026-11-13)

Tasks 1–8 are done, and task 9 up to the live check. What differs from
the steps above:

- **The admin role (task 8).** The plan compared the turn's conversation
  with the principal's private chat. The code asks whether a group's
  message or press started the turn (`event.Message.Group`), which the
  transport sets: `t.admin = admin && t.msg.Group == ""`. It needs no
  private-chat lookup and holds for every transport that names its groups.
  A press carries its group too (`event.Decision.Group`).
- **Wiring (task 9) came with task 7:** the filter reads the node's file
  from the commit that added the group rule, so no commit runs with
  `access.NoGroups()`.
- **The end-to-end scenario is `cmd.TestConnectAGroupThroughTheCard`,**
  not a chatlab scenario: it runs the node's binary against the fake Bot
  API and a fake model, presses the card's button and reads
  `ganglion.yaml` back. Chatlab's scenarios are YAML conversations with a
  model and have no step for a second person in a group or for the node's
  file; the new files are listed in `tools/chatlab/no-scenario.txt`.
- **The fake Bot API** gained `Outgoing.MessageID`, `Incoming.Title`,
  `Mention`, `Server.AddressBot` and `AddChatMember`.
- **`telegram.groups` is in `schemas/node.schema.json`** as well, which the
  plan did not name.
- **The crash harness** (`cmd/crash_test.go`) lists its group in its file
  and addresses the bot in it: it used to rely on an allowed person being
  answered in any group.

The live check is recorded below. Open: the owner's word on ADR-0076,
which is still Proposed.

## Live check (2026-11-13)

Merged as [PR 19](https://github.com/igorrius/ganglion/pull/19) and
deployed to Railway on the production bot. The owner ran the check in the
family's forum; the node's log, its metrics and its file on the volume
agree with what the owner saw.

| Step | Result | Evidence |
|---|---|---|
| A mention in a topic of a group that is not connected | The topic got the notice, the owner's private chat the card | Log, 12:46:16 UTC: `groups: a group that is not connected … outcome=asked`, `access: event dropped reason=group`, two `outbox: sent` (the private chat, the topic); the owner confirmed the card |
| No model for the request | Held | The request's turn: `outcome=command steps=0 model=""` |
| Allow | The card was edited, the line written | Log, 12:46:34: a turn of `outcome=command` and two messages to the private chat; `/data/ganglion.yaml` holds `telegram.groups` with the forum's chat, its title and `role: family` |
| A mention after Allow, with no restart | Answered in the same topic | Log, 12:46:59 and 12:49:57: turns in the topic's conversation, `outcome=ok`, each followed by `outbox: sent` to that topic |
| No tools in a group | Held | Both turns: `tools=0` |
| A message that does not address the bot | No answer, no turn | `ganglion_telegram_group_messages_total{outcome="unaddressed"} 2` and no turn for them; `outcome="addressed"` is 3, the first of which was the request |
| One request per group | Held | `ganglion_groups_requests_total{outcome="asked"} 1` |

Not checked live yet: a reply to the bot's message as the only way of
addressing it, a second topic keeping its own conversation, and the group
staying connected over a restart (the line is in the file on the volume,
which the node reads at every message). The topic check the
[M2 plan](p1-m2-delivery-telegram.md) could not run is covered by the rows
above: a topic's message reached its own conversation and the answer
returned to that topic.

The deploy's first build failed on Railway's side (its build daemon could
not reach buildkit); the second passed. The log's `version=dev commit=""`
is how an image built by `railway up` reports itself.

## What this plan does not do

It is the first of two. The second plan, written next, covers design sections 7, 8 and 10:

- remembering topic titles in connected groups;
- named destinations under `telegram.destinations` and `notify_to` on a module's line;
- `NotifyTo` in the PDK, the runner and the module host, with its own ADR that supersedes ADR-0066 on the target;
- the admin's tools, in the private chat only: connect and disconnect a group in words, name a destination, give one to a module.

Until then a group is disconnected by removing its line from `ganglion.yaml`, and a refused group is asked about again only after its row is removed from `group_requests`.
