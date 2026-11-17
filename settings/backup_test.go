package settings_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/settings"
)

const backupDoc = `backup:
  endpoint: https://s3.example.com
  region: us-east-1
  bucket: my-backups
  prefix: node
  access_key_id: EXAMPLEKEYID
  secret_access_key: store:BACKUP_SECRET
`

func TestBackupIsOffWithoutTheSection(t *testing.T) {
	for _, src := range []string{"", "backup:\n", "backup:\n  # endpoint: https://s3.example.com\n"} {
		f, err := settings.Parse([]byte(src))
		if err != nil {
			t.Fatalf("%q: %v", src, err)
		}
		if f.Backup != nil {
			t.Errorf("%q: backup section %+v", src, f.Backup)
		}
	}
}

func TestBackupSectionLoadsWithTheDefaultTime(t *testing.T) {
	f, err := settings.Parse([]byte(backupDoc))
	if err != nil {
		t.Fatal(err)
	}
	want := settings.Backup{Endpoint: "https://s3.example.com", Region: "us-east-1", Bucket: "my-backups", Prefix: "node",
		AccessKeyID: "EXAMPLEKEYID", SecretAccessKey: "store:BACKUP_SECRET", At: "03:00"}
	if f.Backup == nil || *f.Backup != want {
		t.Fatalf("backup = %+v", f.Backup)
	}
	if !slices.Contains(f.StoreNames(), "BACKUP_SECRET") {
		t.Errorf("StoreNames %v lacks the backup secret", f.StoreNames())
	}
	g, err := settings.Parse([]byte(backupDoc + "  at: \"04:30\"\n"))
	if err != nil || g.Backup.At != "04:30" {
		t.Fatalf("at: %+v %v", g.Backup, err)
	}
}

func TestBackupSectionIsChecked(t *testing.T) {
	for name, tc := range map[string]struct{ edit, want string }{
		"http endpoint":    {"endpoint: https://s3.example.com", "backup.endpoint"},
		"endpoint path":    {"endpoint: https://s3.example.com/bucket", "backup.endpoint"},
		"endpoint creds":   {"endpoint: https://user:pw@s3.example.com", "backup.endpoint"},
		"no region":        {"region: \"\"", "backup.region"},
		"slash bucket":     {"bucket: a/b", "backup.bucket"},
		"absolute prefix":  {"prefix: /node", "backup.prefix"},
		"dotdot prefix":    {"prefix: a/../b", "backup.prefix"},
		"no key id":        {"access_key_id: \"\"", "backup.access_key_id"},
		"literal secret":   {"secret_access_key: hunter2", "backup.secret_access_key"},
		"bad time":         {"at: \"25:00\"", "backup.at"},
		"time without pad": {"at: \"3:00\"", "backup.at"},
	} {
		doc := backupDoc
		key := tc.edit[:strings.Index(tc.edit, ":")]
		lines := strings.Split(doc, "\n")
		replaced := false
		for i, l := range lines {
			if strings.HasPrefix(strings.TrimSpace(l), key+":") {
				lines[i], replaced = "  "+tc.edit, true
			}
		}
		doc = strings.Join(lines, "\n")
		if !replaced {
			doc += "  " + tc.edit + "\n"
		}
		if name == "http endpoint" {
			doc = strings.Replace(doc, "https://", "http://", 1)
		}
		_, err := settings.Parse([]byte(doc))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %v, want one naming %s", name, err, tc.want)
		}
		if err != nil && (strings.Contains(err.Error(), "hunter2") || strings.Contains(err.Error(), "user:pw")) {
			t.Errorf("%s: the error repeats a value: %v", name, err)
		}
	}
}

func TestBackupSectionNeedsItsKeys(t *testing.T) {
	_, err := settings.Parse([]byte("backup:\n  bucket: b\n"))
	if err == nil || !strings.Contains(err.Error(), "backup.endpoint") {
		t.Fatalf("error %v", err)
	}
	if _, err := settings.Parse([]byte(backupDoc + "  extra: 1\n")); err == nil {
		t.Fatal("an unknown key in backup was accepted")
	}
}

// The backup section is a host edit (R-BAK-4): no path under it is on the
// config.propose allowlist, and none is changed from the chat or by a tool.
func TestBackupIsNotOnTheAllowlist(t *testing.T) {
	for _, p := range settings.AllowedPaths {
		if p == "backup" || strings.HasPrefix(p, "backup.") {
			t.Errorf("%s is on the allowlist", p)
		}
	}
	for _, p := range []string{"backup", "backup.endpoint", "backup.bucket", "backup.prefix", "backup.access_key_id",
		"backup.secret_access_key", "backup.at", "backup.region"} {
		if settings.Allowed(p) || settings.Where(p) != settings.PlaceHost {
			t.Errorf("%s: allowed %v, place %s", p, settings.Allowed(p), settings.Where(p))
		}
	}
}
