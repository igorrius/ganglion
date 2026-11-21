package config

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"time"

	"github.com/igorrius/ganglion/alert"
	"github.com/igorrius/ganglion/backup"
	"github.com/igorrius/ganglion/bus"
	"github.com/igorrius/ganglion/bus/natsbus"
	"github.com/igorrius/ganglion/cells"
	"github.com/igorrius/ganglion/internal/buildinfo"
	"github.com/igorrius/ganglion/language"
	"github.com/igorrius/ganglion/node"
	"github.com/igorrius/ganglion/queue/sqlqueue"
	"github.com/igorrius/ganglion/secrets/vault"
	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/texts"
	"github.com/samber/do/v2"
)

// MasterKey is the node's master key (ADR-0077) as it was loaded once at
// start: the secret store and the backups seal under the same key.
type MasterKey struct {
	Key vault.Key
	// Created is true when this start made the key file.
	Created bool
}

// NodeKey provides the master key: the operator's from the environment,
// else the key file at Bootstrap.MasterKey, made at the first start. A key
// that cannot be used stops the start.
func NodeKey() do.Provider[MasterKey] {
	return func(i do.Injector) (MasterKey, error) {
		b := do.MustInvoke[Bootstrap](i)
		key, created, err := vault.LoadKey(moduleSecrets(b), b.MasterKey)
		return MasterKey{Key: key, Created: created}, err
	}
}

// BackupFor makes the Backup of the node b over the live database db,
// sealed under key and put through the bucket b's backup section names
// (R-BAK-3). hc is the HTTP client to the bucket; nil takes the default, so
// only a test gives one; opts say how the cells bucket is exported
// (cellsExport). It is the one place that maps the section to a Backup, for
// the daily component and for `ganglion backup now`.
func BackupFor(b Bootstrap, db *sql.DB, key vault.Key, hc *http.Client, opts ...backup.Option) (*backup.Backup, error) {
	s := b.Settings.Backup
	var s3opts []backup.S3Option
	if hc != nil {
		s3opts = append(s3opts, backup.WithHTTPClient(hc))
	}
	up, err := backup.NewClient(backup.S3Config{Endpoint: s.Endpoint, Region: s.Region, Bucket: s.Bucket,
		AccessKeyID: s.AccessKeyID, SecretAccessKey: b.BackupSecret}, s3opts...)
	if err != nil {
		return nil, err
	}
	cfg := backup.DefaultConfig(filepath.Dir(b.DB))
	cfg.Workspace, cfg.Modules, cfg.Settings = b.Agent.Workspace, b.ModulesDir, settingsFile(b)
	cfg.Prefix, cfg.Version = s.Prefix, buildinfo.Read().Version
	return backup.New(db, up, key[:], cfg, opts...), nil
}

// cellsExport is the backup's way to export the cells bucket of the bus nb
// (R-BUS-4).
func cellsExport(nb *natsbus.Bus) backup.Option {
	return backup.WithExport(backup.FileExport(func(ctx context.Context, w io.Writer) error {
		_, err := nb.ExportKV(ctx, bus.BucketCells, w)
		return err
	}))
}

// BackupOffline makes one backup of the node b over its database db when no
// node is running on it (`ganglion backup now`): it opens the bus as the
// node would, exports the cells bucket into the archive and closes the bus.
// The caller holds the database lock, so no running node has the bus's
// store open; a running node is asked instead (backup.Ask).
func BackupOffline(ctx context.Context, b Bootstrap, db *sql.DB, key vault.Key, hc *http.Client) (backup.Result, error) {
	nb, err := openBus(ctx, b, slog.New(slog.DiscardHandler))
	if err != nil {
		return backup.Result{}, err
	}
	defer func() { _ = nb.Shutdown(ctx) }()
	bk, err := BackupFor(b, db, key, hc, cellsExport(nb))
	if err != nil {
		return backup.Result{}, err
	}
	return bk.Run(ctx)
}

// backupOn reports whether the node makes backups: the file has a backup
// section and the database is a file to copy.
func backupOn(b Bootstrap) bool {
	return b.Settings.Backup != nil && b.DB != "" && b.DB != memoryDB
}

// memoryDB is the database path of a node that keeps nothing on disk.
const memoryDB = ":memory:"

// Backups are the components that make the daily backup.
type Backups []node.Component

// BackupComponents provides the daily backup when the settings have a
// backup section, and nothing otherwise (R-BAK-4).
func BackupComponents() do.Provider[Backups] {
	return choose[Backups](backupOn, backupDaily, backupNone)
}

// BackupDaily provides the component that makes the backup each day at
// backup.at in the node's zone, and tells the admins of a failure.
func BackupDaily() do.Provider[Backups] {
	return func(i do.Injector) (Backups, error) {
		b := do.MustInvoke[Bootstrap](i)
		// Cells first: it loads the cells a restore left, so no backup
		// exports a bucket that is still waiting for them.
		do.MustInvoke[*cells.Service](i)
		bk, err := BackupFor(b, do.MustInvoke[Database](i).DB, do.MustInvoke[MasterKey](i).Key, b.BackupHTTP,
			cellsExport(do.MustInvoke[*natsbus.Bus](i)))
		if err != nil {
			return nil, err
		}
		at, err := backup.ParseAt(b.Settings.Backup.At)
		if err != nil {
			return nil, err
		}
		opts := []backup.DailyOption{}
		if b.Agent.TZ != nil {
			opts = append(opts, backup.WithDailyLocation(b.Agent.TZ))
		}
		d := backup.NewDaily(bk, filepath.Dir(b.DB), at, do.MustInvoke[alert.Admins](i),
			do.MustInvoke[*sqlqueue.Queue](i).Enqueue, do.MustInvoke[*language.Store](i).Resolve,
			do.MustInvoke[*texts.Catalog](i), do.MustInvoke[*slog.Logger](i), opts...)
		return Backups{d}, nil
	}
}

// BackupStatus provides the backups' status source: the last copy, or off
// (R-BAK-5).
func BackupStatus() do.Provider[*backup.Status] {
	return func(i do.Injector) (*backup.Status, error) {
		b := do.MustInvoke[Bootstrap](i)
		return backup.NewStatus(backupOn(b), filepath.Dir(b.DB), time.Time(do.MustInvoke[ProcessStart](i))), nil
	}
}

var _ status.Source = (*backup.Status)(nil)
