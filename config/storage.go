package config

import (
	"database/sql"
	"log/slog"

	"github.com/igorrius/ganglion/access"
	"github.com/igorrius/ganglion/agent"
	"github.com/igorrius/ganglion/node"
	"github.com/igorrius/ganglion/queue/sqlqueue"
	"github.com/igorrius/ganglion/sqlitedb"
	"github.com/igorrius/ganglion/turns"
	"github.com/samber/do/v2"
)

// DBLock is the exclusive lock on the database file (ADR-0019).
type DBLock struct{ release func() }

// Shutdown releases the lock.
func (l DBLock) Shutdown() { l.release() }

// Lock provides the database lock: one core process per database file.
func Lock() do.Provider[DBLock] {
	return func(i do.Injector) (DBLock, error) {
		b, err := do.Invoke[Bootstrap](i)
		if err != nil {
			return DBLock{}, err
		}
		release, err := node.LockDB(b.DB)
		return DBLock{release: release}, err
	}
}

// Database is the node's SQLite database.
type Database struct{ *sql.DB }

// Shutdown closes the database.
func (d Database) Shutdown() error { return d.Close() }

// DB provides the database, opened under the lock.
func DB() do.Provider[Database] {
	return func(i do.Injector) (Database, error) {
		var err error
		_, b, start := need[DBLock](i, &err), need[Bootstrap](i, &err), need[Startup](i, &err)
		if err != nil {
			return Database{}, err
		}
		db, err := sqlitedb.Open(start, b.DB)
		return Database{db}, err
	}
}

// Queue provides the inbox and outbox. This process owns the database, so
// leases left in it belong to a process that died (ADR-0019).
func Queue() do.Provider[*sqlqueue.Queue] {
	return func(i do.Injector) (*sqlqueue.Queue, error) {
		var err error
		db, start, logger := need[Database](i, &err), need[Startup](i, &err), need[*slog.Logger](i, &err)
		if err != nil {
			return nil, err
		}
		q, err := sqlqueue.New(start, db.DB)
		if err != nil {
			return nil, err
		}
		if n, err := q.ReleaseLeases(start); err != nil {
			return nil, err
		} else if n > 0 {
			logger.Warn("node: released leases left by a previous process", "events", n)
		}
		return q, nil
	}
}

// Access provides the access list, with the bootstrap admin seeded.
func Access() do.Provider[*access.List] {
	return func(i do.Injector) (*access.List, error) {
		var err error
		db, b, start := need[Database](i, &err), need[Bootstrap](i, &err), need[Startup](i, &err)
		if err != nil {
			return nil, err
		}
		acl, err := access.New(start, db.DB)
		if err != nil {
			return nil, err
		}
		if b.Admin != "" {
			if err := acl.Allow(start, b.Admin, access.RoleAdmin, ""); err != nil {
				return nil, err
			}
		}
		return acl, nil
	}
}

// Turns provides the turn trace.
func Turns() do.Provider[*turns.Log] {
	return func(i do.Injector) (*turns.Log, error) {
		var err error
		db, start := need[Database](i, &err), need[Startup](i, &err)
		if err != nil {
			return nil, err
		}
		return turns.New(start, db.DB)
	}
}

// Archive provides the session archive.
func Archive() do.Provider[agent.SessionStore] {
	return func(i do.Injector) (agent.SessionStore, error) {
		var err error
		db, start := need[Database](i, &err), need[Startup](i, &err)
		if err != nil {
			return nil, err
		}
		return agent.NewArchive(start, db.DB)
	}
}
