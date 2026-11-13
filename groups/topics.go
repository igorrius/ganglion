package groups

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/igorrius/ganglion/internal/text"
)

// MaxTopics is how many topics of one group the node remembers: a member
// who makes topics cannot fill the table.
const MaxTopics = 200

// titleRunes caps a stored title: it is the group's own text.
const titleRunes = 128

// Topic is a forum topic the node saw. Title is outside text.
type Topic struct {
	Group  string
	Thread int64
	Title  string
}

// HasFunc reports whether a group chat is connected (Connected.Has).
type HasFunc func(ctx context.Context, group string) (bool, error)

// Topics keeps the titles of the topics of connected groups, so the admin
// can name a topic by its title (R-CFG-12).
type Topics struct {
	store *Store
	has   HasFunc
	log   *slog.Logger
}

// NewTopics returns the topics over s for the groups has knows.
func NewTopics(s *Store, has HasFunc, log *slog.Logger) Topics {
	return Topics{store: s, has: has, log: log}
}

// Seen stores the title of a topic of a connected group; a topic of any
// other group is not remembered. It is the transport's hook and never
// fails a message: an error is logged, without the title.
func (t Topics) Seen(ctx context.Context, group string, thread int64, title string, renamed bool) {
	ok, err := t.has(ctx, group)
	if err == nil && ok {
		err = t.store.setTopic(ctx, group, thread, text.Truncate(title, titleRunes), renamed)
	}
	if err != nil {
		t.log.Warn("groups: a topic's title is not stored", "group", group, "thread", thread, "err", err)
	}
}

// setTopic stores a topic's title. A title from the topic's root never
// replaces one from a rename, and a group past MaxTopics takes no new
// topic.
func (s *Store) setTopic(ctx context.Context, group string, thread int64, title string, renamed bool) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO group_topics (grp, thread, title, renamed, seen_ms)
		 SELECT ?1, ?2, ?3, ?4, ?5
		 WHERE (SELECT COUNT(*) FROM group_topics WHERE grp = ?1) < ?6
		    OR EXISTS (SELECT 1 FROM group_topics WHERE grp = ?1 AND thread = ?2)
		 ON CONFLICT (grp, thread) DO UPDATE SET title = excluded.title, renamed = excluded.renamed, seen_ms = excluded.seen_ms
		 WHERE excluded.renamed = 1 OR group_topics.renamed = 0`,
		group, thread, title, renamed, s.now().UnixMilli(), MaxTopics)
	if err != nil {
		return fmt.Errorf("groups: topic %s/%d: %w", group, thread, err)
	}
	return nil
}

// NameTopic stores the title the admin gave a topic of group, when the
// node has not seen the topic's own: the admin sent the topic's link and
// said what it is called. It counts as a rename, so the older title of the
// topic's root does not replace it, and a real rename does.
func (s *Store) NameTopic(ctx context.Context, group string, thread int64, title string) error {
	return s.setTopic(ctx, group, thread, text.Truncate(title, titleRunes), true)
}

// Topics returns the topics the node saw in group, by thread.
func (s *Store) Topics(ctx context.Context, group string) ([]Topic, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT thread, title FROM group_topics WHERE grp = ? ORDER BY thread`, group)
	if err != nil {
		return nil, fmt.Errorf("groups: topics of %s: %w", group, err)
	}
	defer func() { _ = rows.Close() }()
	var out []Topic
	for rows.Next() {
		t := Topic{Group: group}
		if err := rows.Scan(&t.Thread, &t.Title); err != nil {
			return nil, fmt.Errorf("groups: topics of %s: %w", group, err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Topic returns one topic of group and whether the node saw it.
func (s *Store) Topic(ctx context.Context, group string, thread int64) (Topic, bool, error) {
	t := Topic{Group: group, Thread: thread}
	err := s.db.QueryRowContext(ctx, `SELECT title FROM group_topics WHERE grp = ? AND thread = ?`, group, thread).Scan(&t.Title)
	if errors.Is(err, sql.ErrNoRows) {
		return Topic{}, false, nil
	}
	if err != nil {
		return Topic{}, false, fmt.Errorf("groups: topic %s/%d: %w", group, thread, err)
	}
	return t, true, nil
}

// ForgetTopics drops what the node remembers of group's topics, when the
// group is disconnected.
func (s *Store) ForgetTopics(ctx context.Context, group string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM group_topics WHERE grp = ?`, group); err != nil {
		return fmt.Errorf("groups: forget the topics of %s: %w", group, err)
	}
	return nil
}
