package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/igorrius/ganglion/internal/buildinfo"
	"github.com/urfave/cli/v3"
)

func versionCommand(out io.Writer) *cli.Command {
	var asJSON bool
	return &cli.Command{
		Name:  "version",
		Usage: "print what this binary is: version, commit, toolchain",
		Flags: []cli.Flag{&cli.BoolFlag{Name: "json", Usage: "print JSON, for scripts and bug reports", Destination: &asJSON}},
		Action: func(context.Context, *cli.Command) error {
			in := buildinfo.Read()
			if asJSON {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(in)
			}
			return printInfo(out, in)
		},
	}
}

func printInfo(out io.Writer, in buildinfo.Info) error {
	commit, at := in.Commit, ""
	if commit == "" {
		commit = "unknown (built without VCS information)"
	}
	if !in.CommitTime.IsZero() {
		at = in.CommitTime.UTC().Format(time.RFC3339)
	}
	_, err := fmt.Fprintf(out, "version: %s\ncommit: %s\ncommit time: %s\nmodified: %t\ngo: %s\nplatform: %s/%s\n",
		in.Version, commit, at, in.Modified, in.Go, in.OS, in.Arch)
	return err
}
