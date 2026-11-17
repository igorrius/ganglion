package config

import (
	"context"
	"log/slog"
	"testing"

	"github.com/igorrius/ganglion/backup"
	"github.com/igorrius/ganglion/settings"
	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/texts"
	"github.com/samber/do/v2"
)

// The daily backup is a component only when the file has a backup section
// and the database is a file (R-BAK-4); /status always has a line for it.
func TestBackupRunsOnlyWithTheSectionOnAFile(t *testing.T) {
	for _, tc := range []struct {
		name    string
		section bool
		mem     bool
		wants   bool
	}{{"on", true, false, true}, {"no section", false, false, false}, {"in memory", true, true, false}} {
		t.Run(tc.name, func(t *testing.T) {
			b := backupNode(t, t.TempDir(), "https://s3.example.com", nil)
			if !tc.section {
				b.Settings.Backup = nil
			}
			if tc.mem {
				b.DB = ":memory:"
			}
			i := New(context.Background(), b, slog.New(slog.DiscardHandler))
			defer func() { _ = i.Shutdown() }()
			comps, err := do.Invoke[Backups](i)
			if err != nil {
				t.Fatal(err)
			}
			if tc.wants != (len(comps) == 1 && comps[0].Name() == "backup") {
				t.Fatalf("components: %v", comps)
			}
			cs, err := do.MustInvoke[*backup.Status](i).Status(context.Background(), status.Scope{})
			if err != nil || len(cs) != 1 {
				t.Fatalf("status: %+v, %v", cs, err)
			}
			if off := cs[0].State == status.Off && cs[0].Line == texts.StatusBackupOff; off == tc.wants {
				t.Fatalf("status line %+v with the section %v", cs[0], tc.wants)
			}
		})
	}
}

// BackupFor refuses a section the client cannot use, naming no value.
func TestBackupForRefusesAnEndpointThatIsNotHTTPS(t *testing.T) {
	b := backupNode(t, t.TempDir(), "http://s3.example.com", nil)
	b.Settings.Backup = &settings.Backup{Endpoint: "http://s3.example.com", Region: "r", Bucket: "b", AccessKeyID: "k"}
	if _, err := BackupFor(b, nil, [32]byte{}, nil); err == nil {
		t.Fatal("an http endpoint was accepted")
	}
}

// The secret key is in the list the logs and commands hide.
func TestBackupSecretIsHidden(t *testing.T) {
	b := backupNode(t, t.TempDir(), "https://s3.example.com", nil)
	found := false
	for _, v := range b.SecretValues() {
		found = found || v == b.BackupSecret
	}
	if !found {
		t.Fatal("the backup secret is not in SecretValues")
	}
}
