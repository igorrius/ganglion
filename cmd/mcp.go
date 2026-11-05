package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"time"

	"github.com/igorrius/ganglion/agent"
	"github.com/igorrius/ganglion/cmd/flags"
	"github.com/igorrius/ganglion/diag"
	"github.com/igorrius/ganglion/internal/buildinfo"
	"github.com/igorrius/ganglion/mcp"
	"github.com/igorrius/ganglion/router/rules"
	"github.com/igorrius/ganglion/secrets"
	"github.com/igorrius/ganglion/sqlitedb"
	"github.com/urfave/cli/v3"
)

// mcpCommand is `ganglion mcp`: the read-only diagnostics over stdio
// (R-OBS-7, ADR-0044). It needs no running node and no model, opens the
// database read-only, and writes only JSON-RPC to stdout.
func mcpCommand(in io.Reader, out io.Writer) *cli.Command {
	var db, logDir string
	return &cli.Command{
		Name:  "mcp",
		Usage: "serve read-only diagnostics (logs, the tool-call journal, turns, queues, settings) as an MCP server over stdio",
		Description: "Add it to an MCP client: `ganglion mcp --db <path>`, through ssh, docker exec -i or kubectl exec.\n" +
			"Read access to the data directory is read access to all conversation history.",
		// A usage error must not print help to stdout, the protocol's channel.
		OnUsageError: func(_ context.Context, _ *cli.Command, err error, _ bool) error { return err },
		Flags:        []cli.Flag{flags.DBFlag(&db), flags.LogDirFlag(&logDir)},
		Action: func(ctx context.Context, _ *cli.Command) error {
			if _, err := os.Stat(db); errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("no database at %s; set GANGLION_DB to the node's database", db)
			}
			ro, err := sqlitedb.OpenReadOnly(ctx, db)
			if err != nil {
				return err
			}
			defer func() { _ = ro.Close() }()
			dir, on := flags.LogDir(db, logDir)
			if !on {
				dir = ""
			}
			d := diag.New(ro, dir, runSettings(ctx, secrets.OSEnv(), os.ReadFile), time.Now)
			srv := mcp.NewServer(diag.ClientSource, buildinfo.Read().Short(), d.Tools(), d.ExecForClient, mcp.WithInstructions(diag.ClientInstructions))
			return srv.Serve(ctx, stdio{in, out})
		},
	}
}

// stdio joins the command's stdin and stdout into the server's stream.
type stdio struct {
	io.Reader
	io.Writer
}

// runSettings are the settings `ganglion run` would read in this
// process's environment, with each flag's variable; the secrets the node
// reads, only whether they are set; and the model catalogue without keys.
// A setting that cannot be read is shown with its error.
func runSettings(ctx context.Context, src secrets.Source, readFile func(string) ([]byte, error)) diag.Settings {
	var v flags.Run
	def := agent.DefaultConfig()
	fl := v.Flags(def.DailyTurns, def.DailyTokens, def.SessionIdle)
	var out diag.Settings
	c := &cli.Command{Name: "settings", Flags: fl, Writer: io.Discard, ErrWriter: io.Discard,
		Action: func(_ context.Context, c *cli.Command) error {
			for _, f := range fl {
				name := f.Names()[0]
				env := ""
				if keys := envKeys(f); len(keys) > 0 {
					env = keys[0]
				}
				out = append(out, diag.Setting{Name: name, Env: env, Value: fmt.Sprint(c.Value(name))})
			}
			return nil
		}}
	if err := c.Run(ctx, []string{"settings"}); err != nil {
		out = append(out, diag.Setting{Name: "error", Value: err.Error()})
	}
	for _, name := range []string{"TELEGRAM_BOT_TOKEN", "BRAVE_API_KEY"} {
		out = append(out, secretSetting(src, name))
	}
	return append(out, catalogueSettings(v.ModelsFile, src, readFile)...)
}

// envKeys are the environment variables a flag reads.
func envKeys(f cli.Flag) []string {
	switch f := f.(type) {
	case *cli.StringFlag:
		return f.Sources.EnvKeys()
	case *cli.IntFlag:
		return f.Sources.EnvKeys()
	case *cli.FloatFlag:
		return f.Sources.EnvKeys()
	}
	return nil
}

func secretSetting(src secrets.Source, name string) diag.Setting {
	val, err := src.Secret(name)
	if err != nil {
		return diag.Setting{Name: name, Env: name, Value: "error: " + err.Error()}
	}
	return diag.Setting{Name: name, Env: name, Value: val, Secret: true}
}

// catalogueSettings list the catalogue's providers, with whether each
// key is set, and its models.
func catalogueSettings(path string, src secrets.Source, readFile func(string) ([]byte, error)) diag.Settings {
	if path == "" {
		return nil
	}
	data, err := readFile(path)
	if err != nil {
		return diag.Settings{{Name: "models", Value: "error: " + err.Error()}}
	}
	cat, err := rules.LoadCatalog(bytes.NewReader(data))
	if err != nil {
		return diag.Settings{{Name: "models", Value: "error: " + err.Error()}}
	}
	var out diag.Settings
	for _, p := range cat.Providers {
		out = append(out, diag.Setting{Name: "provider " + p.Name,
			Value: fmt.Sprintf("type=%s base_url=%s, its key in %s", p.Type, p.BaseURL, p.KeyEnv)})
		if p.KeyEnv != "" {
			out = append(out, secretSetting(src, p.KeyEnv))
		}
	}
	for _, m := range cat.Models {
		out = append(out, diag.Setting{Name: "model " + m.Provider + "/" + m.Name, Value: fmt.Sprintf("tier=%s tools=%v", m.Tier, m.Tools)})
	}
	return out
}
