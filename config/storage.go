package config

import (
	"database/sql"
	"log/slog"
	"time"

	"github.com/igorrius/ganglion/access"
	"github.com/igorrius/ganglion/agent"
	"github.com/igorrius/ganglion/cells"
	"github.com/igorrius/ganglion/cells/sqlstore"
	"github.com/igorrius/ganglion/language"
	"github.com/igorrius/ganglion/node"
	"github.com/igorrius/ganglion/queue/sqlqueue"
	"github.com/igorrius/ganglion/schedule"
	"github.com/igorrius/ganglion/sqlitedb"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/journal"
	"github.com/igorrius/ganglion/tool/pending"
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

// Languages provides each person's language.
func Languages() do.Provider[*language.Store] {
	return func(i do.Injector) (*language.Store, error) {
		return language.New(do.MustInvoke[Startup](i), do.MustInvoke[Database](i).DB)
	}
}

// Texts provides the fixed texts, machine translations included.
func Texts() do.Provider[*texts.Catalog] {
	return func(i do.Injector) (*texts.Catalog, error) {
		return texts.New(texts.WithPacks(do.MustInvoke[*language.Store](i).Pack))
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

// Cells provides the state cells over the database; confirmations are
// request cells.
func Cells() do.Provider[*cells.Service] {
	return func(i do.Injector) (*cells.Service, error) {
		store, err := sqlstore.New(do.MustInvoke[Startup](i), do.MustInvoke[Database](i).DB)
		if err != nil {
			return nil, err
		}
		return cells.NewService(store), nil
	}
}

// Pending provides the calls awaiting confirmation and their buttons.
func Pending() do.Provider[*pending.Store] {
	return func(i do.Injector) (*pending.Store, error) {
		return pending.New(do.MustInvoke[Startup](i), do.MustInvoke[Database](i).DB, do.MustInvoke[*cells.Service](i),
			time.Now, pending.DefaultTTL)
	}
}

// Jobs provides the scheduled jobs and their runs.
func Jobs() do.Provider[*schedule.Store] {
	return func(i do.Injector) (*schedule.Store, error) {
		return schedule.NewStore(do.MustInvoke[Startup](i), do.MustInvoke[Database](i).DB)
	}
}

// Journal provides the tool-call journal.
func Journal() do.Provider[*journal.Journal] {
	return func(i do.Injector) (*journal.Journal, error) {
		reg := do.MustInvoke[*tool.Registry](i)
		return journal.New(do.MustInvoke[Startup](i), do.MustInvoke[Database](i).DB, time.Now, journal.DefaultRetain,
			journal.WithLogger(do.MustInvoke[*slog.Logger](i)),
			journal.WithTools(func(name string) bool { _, ok := reg.Lookup(name); return ok }))
	}
}
