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

// Lock provides the database lock.
func Lock() do.Provider[DBLock] {
	return func(i do.Injector) (DBLock, error) {
		release, err := node.LockDB(do.MustInvoke[Bootstrap](i).DB)
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
		do.MustInvoke[DBLock](i)
		db, err := sqlitedb.Open(do.MustInvoke[Startup](i), do.MustInvoke[Bootstrap](i).DB)
		return Database{db}, err
	}
}

// Queue provides the inbox and outbox, owned by this process.
func Queue() do.Provider[*sqlqueue.Queue] {
	return func(i do.Injector) (*sqlqueue.Queue, error) {
		return sqlqueue.OpenOwned(do.MustInvoke[Startup](i), do.MustInvoke[Database](i).DB, do.MustInvoke[*slog.Logger](i))
	}
}

// Access provides the access list with the bootstrap admin.
func Access() do.Provider[*access.List] {
	return func(i do.Injector) (*access.List, error) {
		return access.New(do.MustInvoke[Startup](i), do.MustInvoke[Database](i).DB,
			access.WithAdmin(do.MustInvoke[Bootstrap](i).Admin))
	}
}

// Turns provides the turn trace.
func Turns() do.Provider[*turns.Log] {
	return func(i do.Injector) (*turns.Log, error) {
		return turns.New(do.MustInvoke[Startup](i), do.MustInvoke[Database](i).DB)
	}
}

// Archive provides the session archive.
func Archive() do.Provider[agent.SessionStore] {
	return func(i do.Injector) (agent.SessionStore, error) {
		return agent.NewArchive(do.MustInvoke[Startup](i), do.MustInvoke[Database](i).DB)
	}
}
