package cmd

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/igorrius/ganglion/cmd/flags"
	"github.com/igorrius/ganglion/diag"
	"github.com/igorrius/ganglion/internal/buildinfo"
	"github.com/igorrius/ganglion/mcp"
	"github.com/igorrius/ganglion/router"
	"github.com/igorrius/ganglion/secrets"
	"github.com/igorrius/ganglion/secrets/vault"
	"github.com/igorrius/ganglion/settings"
	"github.com/igorrius/ganglion/sqlitedb"
	"github.com/urfave/cli/v3"
)

// mcpCommand is `ganglion mcp`: the read-only diagnostics over stdio
// (R-OBS-7, ADR-0044). It needs no running node and no model, opens the
// database read-only, and writes only JSON-RPC to stdout.
func mcpCommand(in io.Reader, out io.Writer) *cli.Command {
	var db, cfg, logDir string
	return &cli.Command{
		Name:  "mcp",
		Usage: "serve read-only diagnostics (logs, the tool-call journal, turns, queues, settings) as an MCP server over stdio",
		Description: "Add it to an MCP client: `ganglion mcp --db <path>`, through ssh, docker exec -i or kubectl exec.\n" +
			"Read access to the data directory is read access to all conversation history.",
		// A usage error must not print help to stdout, the protocol's channel.
		OnUsageError: func(_ context.Context, _ *cli.Command, err error, _ bool) error { return err },
		Flags:        []cli.Flag{flags.DBFlag(&db), flags.ConfigFlag(&cfg), flags.LogDirFlag(&logDir)},
		Action: func(ctx context.Context, _ *cli.Command) error {
			if _, err := os.Stat(db); errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("no database at %s; set GANGLION_DB to the node's database", db)
			}
			ro, err := sqlitedb.OpenReadOnly(ctx, db)
			if err != nil {
				return err
			}
			defer func() { _ = ro.Close() }()
			// A file that does not load is shown in config.show; the
			// diagnostics still serve, on the default log directory.
			f, loadErr := loadSettings(cfg, db, os.ReadFile)
			if loadErr != nil {
				f = settings.Default()
			}
			if logDir != "" {
				f.Log.Dir = logDir
			}
			dir, _ := f.LogDir(db)
			d := diag.New(ro, dir, runSettings(f, loadErr, settings.Sources{Env: secrets.OSEnv(), Store: storedRows{ctx: ctx, db: ro}}, os.Getenv), time.Now)
			srv := mcp.NewServer(diag.ClientSource, buildinfo.Read().Short(), d.Tools(), d.ExecForClient, mcp.WithInstructions(diag.ClientInstructions))
			return srv.Serve(ctx, stdio{in, out})
		},
	}
}

// storedRows answers whether one of the node's own secrets is stored,
// reading the read-only database without the store's key: this command
// never reads a value (ADR-0077). It answers a fixed word for a row that
// exists, which secretSetting shows as "set".
type storedRows struct {
	ctx context.Context //nolint:containedctx // the command's context, for a source that takes none
	db  *sql.DB
}

// Secret implements secrets.Source.
func (s storedRows) Secret(name string) (string, error) {
	has, err := vault.Has(s.ctx, s.db, secrets.ScopeNode, name)
	if err != nil || !has {
		return "", err
	}
	return "stored", nil
}

// stdio joins the command's stdin and stdout into the server's stream.
type stdio struct {
	io.Reader
	io.Writer
}

// runSettings are the settings `ganglion run` would read in this process's
// environment: every key of f with its value, each secret only as its
// variable and whether it is set, the models by id and the purposes in
// order. A file that did not load, or a retired variable that would stop
// the start, is an error row first.
func runSettings(f settings.File, loadErr error, src settings.Sources, getenv func(string) string) diag.Settings {
	var out diag.Settings
	for _, err := range []error{loadErr, settings.Retired(getenv)} {
		if err != nil {
			out = append(out, diag.Setting{Name: "error", Value: err.Error()})
		}
	}
	rest := f
	rest.Models, rest.Modules = nil, nil
	out = append(out, sectionSettings("", reflect.ValueOf(rest), src)...)
	if f.Models != nil {
		out = append(out, modelSettings(*f.Models, src)...)
	}
	for _, m := range f.Modules {
		out = append(out, diag.Setting{Name: "modules", Value: m.Name + map[bool]string{true: " (off)"}[m.Off]})
	}
	return out
}

// sectionSettings lists every leaf of the struct v, by its path in the
// file. A nil pointer is left out.
func sectionSettings(path string, v reflect.Value, src settings.Sources) diag.Settings {
	var out diag.Settings
	t := v.Type()
	for i := range t.NumField() {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("yaml"), ",")
		at := strings.TrimPrefix(path+"."+name, ".")
		switch fv := v.Field(i); {
		case fv.Kind() == reflect.Pointer && fv.IsNil():
		case fv.Kind() == reflect.Struct:
			out = append(out, sectionSettings(at, fv, src)...)
		default:
			out = append(out, leafSetting(at, fv, src))
		}
	}
	return out
}

// leafSetting is one value; a secret is its variable and whether it is set.
func leafSetting(path string, v reflect.Value, src settings.Sources) diag.Setting {
	switch x := v.Interface().(type) {
	case settings.SecretRef:
		return secretSetting(path, x, src)
	case settings.Duration:
		return diag.Setting{Name: path, Value: time.Duration(x).String()}
	case []string:
		return diag.Setting{Name: path, Value: strings.Join(x, ", ")}
	}
	return diag.Setting{Name: path, Value: fmt.Sprint(v.Interface())}
}

// secretSetting is the reference with "(set)" or "(unset)"; the value never leaves
// this function.
func secretSetting(path string, r settings.SecretRef, src settings.Sources) diag.Setting {
	if r == "" {
		return diag.Setting{Name: path, Value: "none"}
	}
	val, err := r.Resolve(src)
	state := map[bool]string{true: "set", false: "unset"}[val != ""]
	if err != nil {
		state = "error: " + err.Error()
	}
	out := diag.Setting{Name: path, Value: fmt.Sprintf("%s (%s)", r, state)}
	if r.Scheme() == settings.SchemeEnv {
		out.Env = r.Name() // the variable; a stored secret is not one
	}
	return out
}

// modelSettings list the providers with their keys as set or unset, the
// models as "id provider/name", and each purpose as "chat: sonnet
// (medium), haiku".
func modelSettings(m settings.Models, src settings.Sources) diag.Settings {
	var out diag.Settings
	for i, p := range m.Providers {
		at := fmt.Sprintf("models.providers[%d]", i)
		out = append(out, diag.Setting{Name: at, Value: fmt.Sprintf("%s type=%s base_url=%s", p.Name, p.Type, p.BaseURL)})
		if p.Key != "" {
			out = append(out, secretSetting(at+".key", p.Key, src))
		}
	}
	for _, d := range m.Models {
		out = append(out, diag.Setting{Name: "models.models", Value: fmt.Sprintf("%s %s/%s tools=%v context=%d", d.ID, d.Provider, d.Name, d.Tools, d.Context)})
	}
	purposes := m.RouterPurposes()
	for _, p := range router.Purposes {
		list, ok := purposes[p]
		if !ok {
			continue
		}
		parts := make([]string, len(list))
		for i, c := range list {
			parts[i] = c.Model
			if c.Effort != "" {
				parts[i] += " (" + string(c.Effort) + ")"
			}
		}
		out = append(out, diag.Setting{Name: "models.purposes", Value: fmt.Sprintf("%s: %s", p, strings.Join(parts, ", "))})
	}
	return out
}
